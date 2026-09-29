package agent

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/netguard"
)

// kvGuardHost is the owner's switch, Keep servers away from this machine:
// "on" adds the rules for the machine itself to the metadata rule that is
// always there (netguard). Off, the default, a plugin can still reach a
// database the machine runs. The dashboard turns it on when it makes a
// creator invite, and keeps it on while the machine has creators.
const kvGuardHost = "network_guard_host"

// loadGuard reads the owner's switch, which the agent keeps in memory from
// then on: a database error on one of its looks at the rules mustn't count
// as off and take the rules for the machine out.
func (a *Agent) loadGuard() error {
	v, _, err := a.kvGet(kvGuardHost)
	a.keepAway = v == "on"
	return err
}

// guardHost reports whether the owner keeps servers away from this machine.
func (a *Agent) guardHost() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.keepAway
}

// setGuardHost stores the owner's switch, and only then changes it, and
// reports whether it did.
func (a *Agent) setGuardHost(on bool) (bool, error) {
	a.guardMu.Lock()
	defer a.guardMu.Unlock()
	if on == a.guardHost() {
		return false, nil
	}
	if err := a.kvSet(kvGuardHost, map[bool]string{true: "on", false: "off"}[on]); err != nil {
		return false, err
	}
	a.mu.Lock()
	a.keepAway = on
	a.mu.Unlock()
	return true, nil
}

// defaultFirewall is what runs iptables for an agent with no Firewall option:
// the commands themselves for root, and nothing in dev mode, where the
// machine's firewall is a contributor's own.
func defaultFirewall(cfg config.Config, euid int) netguard.Runner {
	if cfg.Dev || euid != 0 {
		return nil
	}
	return netguard.Exec
}

// hNetworkGuard turns keeping servers away from this machine on or off, and
// changes the rules at once. In dev mode the switch is kept, but the
// firewall left alone.
func (a *Agent) hNetworkGuard(w http.ResponseWriter, r *http.Request) {
	var req api.NetworkGuardRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	changed, err := a.setGuardHost(req.Host)
	if err != nil {
		writeError(w, err)
		return
	}
	if changed {
		a.audit(actor, "machine.network_guard", "", "changed", map[bool]string{true: "servers kept away from this machine", false: "servers may reach this machine"}[req.Host])
	}
	a.guardNetwork(r.Context())
	g := a.guardView()
	if g == nil {
		g = &api.NetworkGuard{Host: req.Host}
	}
	writeJSON(w, http.StatusOK, g)
}

// guardView is how the network guard stands, for Machine: the owner's
// switch, and whether the rules are in place, which they aren't until the
// first server makes Playkeeper's network. It is nil in dev mode.
func (a *Agent) guardView() *api.NetworkGuard {
	if a.opts.Firewall == nil {
		return nil
	}
	a.mu.Lock()
	g := api.NetworkGuard{Host: a.keepAway}
	if a.guard != nil {
		g.On, g.Problem = a.guard.On, a.guard.Problem
	}
	a.mu.Unlock()
	return &g
}

// guardLoop keeps the network guard's rules in place: Docker, ufw and
// firewalld can drop or bury them when they reload their own.
func (a *Agent) guardLoop(ctx context.Context) {
	if a.opts.Firewall == nil {
		return
	}
	a.guardNetwork(ctx)
	if a.opts.GuardInterval < 0 {
		return
	}
	t := time.NewTicker(a.opts.GuardInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.guardNetwork(ctx)
		}
	}
}

// guardNetwork puts the network guard's rules for Playkeeper's network in
// place, or back at the top of their chains, and records how that went.
func (a *Agent) guardNetwork(ctx context.Context) {
	if a.opts.Firewall == nil {
		return
	}
	a.guardMu.Lock()
	defer a.guardMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	n, err := a.docker.NetworkInspect(ctx, networkName)
	if err != nil {
		// There's nothing to guard until the first server makes the
		// network, and while Docker doesn't answer the rules stay as they
		// are.
		return
	}
	host := a.guardHost()
	if n.Driver != "bridge" {
		err = fmt.Errorf("Playkeeper's network uses Docker's %q driver, not a bridge", n.Driver)
	} else {
		err = netguard.Apply(ctx, a.opts.Firewall, netguard.Network{Bridge: bridgeName(n), IPv6: n.EnableIPv6}, host)
	}
	st := &api.NetworkGuard{On: err == nil, Host: host}
	if err != nil {
		st.Problem = err.Error()
	}
	a.mu.Lock()
	was := a.guard
	a.guard = st
	a.mu.Unlock()
	switch {
	case err != nil && (was == nil || was.Problem != st.Problem):
		a.log.Warn("the network guard's rules are not in place", "err", err)
	case err == nil && was != nil && !was.On:
		a.log.Info("the network guard's rules are in place again")
	}
}

// guardRefusal refuses to start a server while servers are to be kept away
// from this machine and the rules can't be put in place: servers otherwise
// start without them, as a machine without iptables always did.
func (a *Agent) guardRefusal() error {
	g := a.guardView()
	if g == nil || !g.Host || g.On {
		return nil
	}
	return errConflict("Playkeeper couldn't keep this server away from the machine: "+nonEmptyOr(g.Problem, "Docker didn't answer")+".",
		"Keep servers away from this machine is on in Machine settings, so servers start once its rules are in place. Try again, or turn it off there.")
}

// bridgeName is the machine's interface for the Docker bridge network n: the
// name its options give it, or else br- and the first 12 characters of its
// ID, as Docker names it.
func bridgeName(n docker.NetworkInfo) string {
	if b := n.Options["com.docker.network.bridge.name"]; b != "" {
		return b
	}
	if len(n.ID) < 12 {
		return ""
	}
	return "br-" + n.ID[:12]
}

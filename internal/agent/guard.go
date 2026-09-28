package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/netguard"
)

// defaultFirewall is what runs iptables for an agent with no Firewall option:
// the commands themselves for root, and nothing in dev mode, where the
// machine's firewall is a contributor's own.
func defaultFirewall(cfg config.Config, euid int) netguard.Runner {
	if cfg.Dev || euid != 0 {
		return nil
	}
	return netguard.Exec
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
// Servers start even when it can't: a machine without iptables never had
// the rules, and the loop tries again.
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
	if n.Driver != "bridge" {
		err = fmt.Errorf("Playkeeper's network uses Docker's %q driver, not a bridge", n.Driver)
	} else {
		err = netguard.Apply(ctx, a.opts.Firewall, netguard.Network{Bridge: bridgeName(n), IPv6: n.EnableIPv6}, !a.cfg.ServersReachHost)
	}
	st := &api.NetworkGuard{On: err == nil, ServersReachHost: a.cfg.ServersReachHost}
	if err != nil {
		st.Problem = err.Error()
	}
	a.mu.Lock()
	was := a.guard
	a.guard = st
	a.mu.Unlock()
	switch {
	case err != nil && (was == nil || was.Problem != st.Problem):
		a.log.Warn("servers are not kept from this machine and the cloud's metadata service", "err", err)
	case err == nil && was != nil && !was.On:
		a.log.Info("servers are kept from this machine and the cloud's metadata service again")
	}
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

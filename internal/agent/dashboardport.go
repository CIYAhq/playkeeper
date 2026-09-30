package agent

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// Serve the dashboard on the standard HTTPS port (443). The panel runs
// without CAP_NET_BIND_SERVICE, so, as for the public page, the agent opens
// port 443 and passes the listening socket to the panel over its own
// socket (pageports.go); the panel then answers the machine's name there
// with the dashboard, and nothing about what the panel may do changes.
//
// The switch is kept here: "on" or "off" once someone changes it, and until
// then the install's choice (config.Dashboard443), which a new install
// makes "on" when nothing uses port 443. Turning it on is refused while
// another program uses port 443 or claims it, as the page's hand-over
// would leave the port alone then.
//
// The dashboard's address loses its port only once a browser from outside
// the machine has reached it on port 443 at the machine's name, which the
// panel sees and tells the agent: a firewall in front of the machine can't
// be seen from it, and an address that doesn't open is worse than one with
// a port. It keeps its port again while another program has port 443.
const (
	kvDashboard443        = "dashboard_443"
	kvDashboard443Reached = "dashboard_443_reached"
)

// dashboard443Path turns the switch on or off, and dashboard443ReachedPath
// notes a visit from outside; joined machines, which run no dashboard, are
// never sent either (see socketOnly).
const (
	dashboard443Path        = "/v1/dashboard-443"
	dashboard443ReachedPath = "/v1/dashboard-443/reached"
)

// dash443 is the switch as last stored, with the name it was last reached
// at, and what the last hand-over found of port 443 (the zero PagePort until
// the panel asks for it). mu guards them; set serializes changing them.
type dash443 struct {
	set sync.Mutex

	mu      sync.Mutex
	on      bool
	chosen  bool
	reached string
	https   api.PagePort
	// discord is the dashboard's address Discord's messages last got.
	discord string
}

// loadDashboard443 reads the switch, which the agent keeps in memory from
// then on.
func (a *Agent) loadDashboard443() error {
	v, chosen, err := a.kvGet(kvDashboard443)
	if err != nil {
		return err
	}
	reached, _, err := a.kvGet(kvDashboard443Reached)
	if err != nil {
		return err
	}
	d := &a.dash
	d.mu.Lock()
	defer d.mu.Unlock()
	d.chosen, d.reached = chosen, reached
	d.on = !a.cfg.NoPanel && (v == "on" || !chosen && a.cfg.Dashboard443 == "on")
	return nil
}

// dashboard443On reports whether the switch is on.
func (a *Agent) dashboard443On() bool {
	a.dash.mu.Lock()
	defer a.dash.mu.Unlock()
	return a.dash.on
}

// dashboard443Wanted reports whether the panel is to answer the machine's
// name on port 443 with the dashboard: the switch is on and the name works
// with a certificate. host is the name, "" while it's wanted nowhere.
func (a *Agent) dashboard443Wanted() (host string) {
	if !a.dashboard443On() {
		return ""
	}
	return a.namedHost()
}

// noteDashboard443 keeps what a hand-over found of port 443.
func (a *Agent) noteDashboard443(p api.PagePort) {
	a.dash.mu.Lock()
	a.dash.https = p
	a.dash.mu.Unlock()
}

// dashboard443View is the switch as the dashboard shows it, with the port
// the dashboard's address has; nil on a joined machine.
func (a *Agent) dashboard443View() *api.Dashboard443 {
	if a.cfg.NoPanel {
		return nil
	}
	return a.dashboard443For(a.namedHost())
}

// dashboard443For is dashboard443View for the machine's working name host.
func (a *Agent) dashboard443For(host string) *api.Dashboard443 {
	d := &a.dash
	d.mu.Lock()
	on, chosen, reached, https := d.on, d.chosen, d.reached, d.https
	d.mu.Unlock()
	v := &api.Dashboard443{On: on, Default: !chosen, State: api.PortOpen, Port: a.cfg.PanelPort}
	blocked := false
	switch {
	case !on:
		v.State = api.PortOff
	case host == "":
		v.State = api.DashboardNoAddress
	case https.State == api.PortBusy || https.State == api.PortClaimed || https.State == api.PortDenied:
		v.State, v.Holder, blocked = https.State, https.Holder, true
	case https.State == api.PortWaiting:
		v.State = api.PortWaiting
	}
	v.Reached = on && host != "" && reached == host
	if v.Reached && !blocked {
		v.Port = 443
	}
	return v
}

// dashboardBase is the dashboard's address under the machine's name, as
// Playkeeper gives it out: without a port while the dashboard answers on
// port 443 there, with the panel's port otherwise, and "" while the name
// doesn't work (see namedHost).
func (a *Agent) dashboardBase() string {
	host := a.namedHost()
	if host == "" || !reDomain.MatchString(host) {
		return ""
	}
	if a.dashboardOn443(host) {
		return "https://" + host
	}
	return "https://" + net.JoinHostPort(host, strconv.Itoa(a.cfg.PanelPort))
}

// dashboardOn443 reports whether the dashboard's address under the
// machine's working name host has no port.
func (a *Agent) dashboardOn443(host string) bool {
	return host != "" && (a.cfg.PanelPort == 443 || !a.cfg.NoPanel && a.dashboard443For(host).Port == 443)
}

// check443 says whether the switch may turn on: nothing claims port 443
// (a Docker container that publishes it or names it, or a web server set to
// start with the machine), and nothing listens on it, unless the panel
// holds it already. The try is one hand-over, so it can't make a hand-over
// under way find the port busy.
func (a *Agent) check443(ctx context.Context, held bool) (api.PagePort, bool) {
	addr := a.opts.PageHTTPSAddr
	port := addrPort(addr)
	if holder, ok := a.portClaims(ctx, port)[port]; ok {
		return api.PagePort{Port: port, State: api.PortClaimed, Holder: holder}, false
	}
	if held {
		return api.PagePort{Port: port, State: api.PortOpen}, true
	}
	a.pagePorts.hand.Lock()
	defer a.pagePorts.hand.Unlock()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		p := api.PagePort{Port: port, State: api.PortBusy}
		if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
			p.State = api.PortDenied
		} else if name, _, ok := a.opts.PortHolder(port); ok {
			p.Holder = name
		}
		return p, false
	}
	ln.Close()
	return api.PagePort{Port: port, State: api.PortOpen}, true
}

// port443Refusal is why the switch can't turn on, naming what has the port.
func port443Refusal(p api.PagePort, panelPort int) (msg, hint string) {
	who := "Another program"
	if p.Holder != "" {
		who = p.Holder
	}
	switch p.State {
	case api.PortClaimed:
		return who + " is set to use port 443, so the dashboard can't have it.",
			"Stop it and keep it from starting with the machine, then turn this on again. Until then the dashboard stays on port " + strconv.Itoa(panelPort) + "."
	case api.PortDenied:
		return "This machine doesn't let Playkeeper use port 443.", "The dashboard stays on port " + strconv.Itoa(panelPort) + "."
	}
	return who + " uses port 443, so the dashboard can't have it.",
		"See what uses it with: sudo ss -ltnp 'sport = :443'. Stop it, then turn this on again. Until then the dashboard stays on port " + strconv.Itoa(panelPort) + "."
}

// hDashboard443 turns Serve the dashboard on the standard HTTPS port on or
// off. Turning it off forgets where it was reached, so turning it on again
// waits for a visit again.
func (a *Agent) hDashboard443(w http.ResponseWriter, r *http.Request) {
	var req api.Dashboard443Request
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	if a.cfg.NoPanel {
		writeErr(w, http.StatusConflict, api.CodeConflict, "This machine runs no dashboard.", "")
		return
	}
	a.dash.set.Lock()
	defer a.dash.set.Unlock()
	if req.On {
		if p, ok := a.check443(r.Context(), req.Held); !ok {
			msg, hint := port443Refusal(p, a.cfg.PanelPort)
			a.audit(actor, "dashboard_443.on", "dashboard_443", "refused", msg)
			writeJSON(w, http.StatusConflict, api.Error{Code: api.CodePortInUse, Error: msg, Hint: hint,
				Params: map[string]any{"port": p.Port, "state": p.State, "holder": p.Holder}})
			return
		}
	}
	value := map[bool]string{true: "on", false: "off"}[req.On]
	if err := a.kvSet(kvDashboard443, value); err != nil {
		writeError(w, err)
		return
	}
	if !req.On {
		if _, err := a.db.Exec(`DELETE FROM kv WHERE key = ?`, kvDashboard443Reached); err != nil {
			writeError(w, err)
			return
		}
	}
	a.dash.mu.Lock()
	was := a.dash.on
	a.dash.on, a.dash.chosen = req.On, true
	if !req.On {
		a.dash.reached = ""
	}
	if req.On && !was {
		a.dash.https = api.PagePort{}
	}
	a.dash.mu.Unlock()
	if req.On {
		// Port 443 was just found free: a busy mark from before would keep
		// the page's hand-over from trying it.
		a.pagePorts.mu.Lock()
		delete(a.pagePorts.busy, a.opts.PageHTTPSAddr)
		a.pagePorts.mu.Unlock()
	}
	a.refreshDiscordDashboard()
	a.audit(actor, "dashboard_443."+value, "dashboard_443", "succeeded", "")
	writeJSON(w, http.StatusOK, a.dashboard443View())
}

// hDashboard443Reached notes that a browser from outside the machine
// reached the dashboard on port 443 at the name the request names, which
// the panel saw. Only the machine's working name counts, only while the
// switch is on, and only from a public address that isn't the machine's
// own, since a request the machine makes to itself never passes a firewall
// in front of it.
func (a *Agent) hDashboard443Reached(w http.ResponseWriter, r *http.Request) {
	var req api.Dashboard443Reached
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(req.Host)), ".")
	from, err := netip.ParseAddr(req.From)
	if err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Say which address reached the dashboard.", "")
		return
	}
	if from = from.Unmap().WithZone(""); !a.outsideAddr(from) {
		writeErr(w, http.StatusConflict, api.CodeConflict, "Only a visit from outside the machine shows port 443 opens.", "")
		return
	}
	a.dash.set.Lock()
	defer a.dash.set.Unlock()
	named := a.dashboard443Wanted()
	if named == "" || host != named {
		writeErr(w, http.StatusConflict, api.CodeConflict, "The dashboard doesn't answer that name on port 443.", "")
		return
	}
	a.dash.mu.Lock()
	known := a.dash.reached == host
	a.dash.mu.Unlock()
	if !known {
		if err := a.kvSet(kvDashboard443Reached, host); err != nil {
			writeError(w, err)
			return
		}
		a.dash.mu.Lock()
		a.dash.reached = host
		a.dash.mu.Unlock()
		a.refreshDiscordDashboard()
		a.audit("system", "dashboard_443.reached", "dashboard_443", "succeeded", "https://"+host)
	}
	writeJSON(w, http.StatusOK, a.dashboard443View())
}

// outsideAddr reports whether ip is a public address that isn't one of the
// machine's own.
func (a *Agent) outsideAddr(ip netip.Addr) bool {
	if ip, ok := publicIP(ip.String()); !ok || ip == a.machineIP(a.address()) {
		return false
	}
	for _, own := range a.opts.PublicAddrs() {
		if own.Unmap().WithZone("") == ip {
			return false
		}
	}
	return true
}

// refreshDiscordDashboard gives Discord's messages the dashboard's address
// again when it changed, such as when it lost or got back its port.
func (a *Agent) refreshDiscordDashboard() {
	if a.disc.n == nil {
		return
	}
	info := a.discordDashboard()
	a.dash.mu.Lock()
	same := a.dash.discord == info.DashboardURL
	a.dash.discord = info.DashboardURL
	a.dash.mu.Unlock()
	if !same {
		a.disc.n.SetServer(info)
	}
}

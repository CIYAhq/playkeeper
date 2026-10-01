package agent

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// dashboardEnv is an agent whose page ports are on free high ports, whose
// machine name works with a certificate, and whose install chose on for the
// dashboard's standard port when install is "on". Public DNS outlives a
// restart, which looks at the name again as it starts.
func dashboardEnv(t *testing.T, install string, setup func(e *agentEnv)) (e *agentEnv, https int) {
	t.Helper()
	dns := &fakeResolver{}
	e, https, _ = pageEnv(t, time.Hour, func(e *agentEnv) {
		e.cfg.Dashboard443 = install
		page := e.tweak
		e.tweak = func(o *Options) {
			page(o)
			o.Resolver = dns
		}
		if setup != nil {
			setup(e)
		}
	})
	e.nameWorks(pageTestHost)
	return e, https
}

// dashboardSwitch turns the switch on or off as the panel does.
func (e *agentEnv) dashboardSwitch(on, held bool) (int, map[string]any) {
	e.t.Helper()
	return e.call("PUT", dashboard443Path, map[string]any{"on": on, "held": held, "actor": "owner"})
}

func (e *agentEnv) dashboardView() api.Dashboard443 {
	e.t.Helper()
	var a api.Address
	if code := e.callInto("GET", "/v1/address", nil, &a); code != 200 || a.Dashboard == nil {
		e.t.Fatalf("the address: %d %+v", code, a.Dashboard)
	}
	return *a.Dashboard
}

func TestTheDashboardsPortFollowsTheInstallUntilSomeoneChangesIt(t *testing.T) {
	e, _ := dashboardEnv(t, "", nil)
	if v := e.dashboardView(); v.On || !v.Default || v.State != api.PortOff || v.Port != e.cfg.PanelPort {
		t.Fatalf("an install from before the switch: %+v", v)
	}

	e, _ = dashboardEnv(t, "on", nil)
	if v := e.dashboardView(); !v.On || !v.Default || v.State != api.PortOpen || v.Reached || v.Port != e.cfg.PanelPort {
		t.Fatalf("a new install that found port 443 free: %+v", v)
	}
	if code, out := e.dashboardSwitch(false, false); code != 200 || out["on"] != false {
		t.Fatalf("turning it off: %d %v", code, out)
	}
	// The owner's choice outlives a restart, and wins over the install's.
	e.stop()
	e.start()
	if v := e.dashboardView(); v.On || v.Default {
		t.Fatalf("after a restart the switch the owner turned off is %+v", v)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'dashboard_443.off' AND actor = 'owner' AND result = 'succeeded'`); n != 1 {
		t.Fatalf("%d audit rows for turning it off", n)
	}
	if code, _ := e.call("PUT", dashboard443Path, map[string]any{"on": true}); code != 400 {
		t.Fatalf("a change without an actor: %d", code)
	}
}

// Turning the switch on is refused while another program uses port 443 or
// will, the way the page's hand-over leaves the port alone, and the refusal
// names what has it.
func TestTheDashboardNeverTakesPort443FromAnotherProgram(t *testing.T) {
	blocker, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	busy := blocker.Addr().(*net.TCPAddr).Port
	e, https := dashboardEnv(t, "", func(e *agentEnv) {
		e.portHolder = func(port int) (string, int, bool) { return "nginx", 4242, port == busy }
	})
	e.a.opts.PageHTTPSAddr = ":" + strconv.Itoa(busy)

	code, out := e.dashboardSwitch(true, false)
	params, _ := out["params"].(map[string]any)
	if code != 409 || out["code"] != api.CodePortInUse || params["holder"] != "nginx" || params["state"] != api.PortBusy || out["error"] != "nginx uses port 443, so the dashboard can't have it." {
		t.Fatalf("with nginx on port 443: %d %v", code, out)
	}
	if v := e.dashboardView(); v.On {
		t.Fatalf("a refused switch is on: %+v", v)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'dashboard_443.on' AND result = 'refused'`); n != 1 {
		t.Fatalf("%d audit rows for the refusal", n)
	}
	// The panel holds the port already, for the public page: nothing is
	// tried, since the panel's own socket would look busy.
	if code, out := e.dashboardSwitch(true, true); code != 200 || out["on"] != true {
		t.Fatalf("with the panel holding the port: %d %v", code, out)
	}
	e.dashboardSwitch(false, false)

	// A stopped Docker container that names the port claims it, held or not.
	e.a.opts.PageHTTPSAddr = ":" + strconv.Itoa(https)
	e.fd.mu.Lock()
	e.fd.others = append(e.fd.others, fakeListed{name: "caddy-proxy", ports: []fakePort{{https, "tcp"}}})
	e.fd.mu.Unlock()
	for _, held := range []bool{false, true} {
		code, out := e.dashboardSwitch(true, held)
		params, _ := out["params"].(map[string]any)
		if code != 409 || params["holder"] != "caddy-proxy" || params["state"] != api.PortClaimed {
			t.Fatalf("with a container naming the port (held %v): %d %v", held, code, out)
		}
	}
	e.fd.mu.Lock()
	e.fd.others = nil
	e.fd.mu.Unlock()

	// So does a web server set to start with the machine.
	wants := filepath.Join(e.a.opts.SystemdDir, "multi-user.target.wants")
	os.MkdirAll(wants, 0o755)
	link := filepath.Join(wants, "caddy.service")
	os.Symlink("/lib/systemd/system/caddy.service", link)
	if code, out := e.dashboardSwitch(true, false); code != 409 || out["params"].(map[string]any)["holder"] != "caddy" {
		t.Fatalf("with caddy enabled: %d %v", code, out)
	}
	os.Remove(link)

	// Free, it turns on, and a busy mark left from an earlier look goes, so
	// the hand-over tries the port.
	e.a.pagePorts.mu.Lock()
	e.a.pagePorts.busy = map[string]api.PagePort{e.a.opts.PageHTTPSAddr: {Port: https, State: api.PortBusy}}
	e.a.pagePorts.mu.Unlock()
	if code, out := e.dashboardSwitch(true, false); code != 200 || out["on"] != true || out["default"] == true {
		t.Fatalf("with the port free: %d %v", code, out)
	}
	ports, files := e.a.takePagePorts(context.Background(), api.PagePortsRequest{HTTPS: true})
	closeAll(files)
	if len(files) != 1 || ports.HTTPS.State != api.PortOpen {
		t.Fatalf("the hand-over after turning it on: %+v (%d files)", ports, len(files))
	}
}

// The panel holds port 443 for the dashboard with every server off the
// public page, but only once the machine's name works with a certificate.
func TestTheDashboardHasPort443WithThePageOff(t *testing.T) {
	e, https, _ := pageEnv(t, time.Hour, func(e *agentEnv) { e.cfg.Dashboard443 = "on" })
	if code, _ := e.call("POST", e.sp("/public-page"), map[string]any{"enabled": false, "actor": "admin"}); code != 200 {
		t.Fatal("turning the page off")
	}
	// The own domain isn't checked yet, so the name doesn't work.
	if st := e.a.publicPageState(nil); st.On || st.Dashboard {
		t.Fatalf("without a working name: %+v", st)
	}
	if v := e.dashboardView(); v.State != api.DashboardNoAddress {
		t.Fatalf("the switch without a working name: %+v", v)
	}
	ports, files := e.a.takePagePorts(context.Background(), api.PagePortsRequest{HTTPS: true, HTTP: true})
	closeAll(files)
	if len(files) != 0 || ports.HTTPS.State != api.PortOff {
		t.Fatalf("without a working name the agent handed over %+v", ports)
	}
	e.nameWorks(pageTestHost)
	if st := e.a.publicPageState(nil); st.On || !st.Dashboard || st.Reached || st.Host != pageTestHost {
		t.Fatalf("with the name working: %+v", st)
	}
	ports, files = e.a.takePagePorts(context.Background(), api.PagePortsRequest{HTTPS: true, HTTP: true})
	closeAll(files)
	if len(files) != 2 || ports.HTTPS.State != api.PortOpen || ports.HTTPS.Port != https {
		t.Fatalf("with the name working the agent handed over %d: %+v", len(files), ports)
	}
	if code, _ := e.dashboardSwitch(false, true); code != 200 {
		t.Fatal("turning it off")
	}
	ports, files = e.a.takePagePorts(context.Background(), api.PagePortsRequest{HTTPS: true, HTTP: true})
	closeAll(files)
	if len(files) != 0 || e.a.publicPageState(nil).Dashboard {
		t.Fatalf("with the page and the switch off the agent handed over %+v", ports)
	}
}

// The dashboard's address loses its port only once a browser from outside
// the machine reached it on port 443, and keeps it again while another
// program has the port.
func TestTheDashboardsAddressLosesItsPortOnceABrowserReachesIt(t *testing.T) {
	e, _ := dashboardEnv(t, "on", nil)
	// Behind NAT the machine's public address, which its name points at, is
	// on no interface: the one the dashboard was opened with.
	const natted = "203.0.113.99"
	if err := e.a.updateAddress(func(st *addressState) { st.IP = natted }); err != nil {
		t.Fatal(err)
	}
	e.a.opts.Resolver.(*fakeResolver).set(pageTestHost, natted)
	const visitor = "198.51.100.77"
	withPort := "https://" + pageTestHost + ":" + strconv.Itoa(e.cfg.PanelPort)
	// Discord hasn't been told where the dashboard was opened, so its
	// messages have no link to it yet.
	if v := e.dashboardView(); v.Port != e.cfg.PanelPort || v.Reached || e.a.panelLink("/packs/x") != withPort+"/packs/x" || e.a.discordBase() != "" {
		t.Fatalf("before a visit: %+v, %q, %q", v, e.a.panelLink("/packs/x"), e.a.discordBase())
	}
	reached := func(host, from string) int {
		code, _ := e.call("POST", dashboard443ReachedPath, map[string]any{"host": host, "from": from})
		return code
	}
	// The machine asking itself, at an interface's address or behind NAT, a
	// private network, shared address space and another name don't count.
	for _, from := range []string{testIP.String(), natted, "10.0.0.8", "192.168.1.20", "100.100.1.2", "127.0.0.1", "fd00::1", "nonsense"} {
		if code := reached(pageTestHost, from); code == 200 {
			t.Errorf("a visit from %s counted", from)
		}
	}
	if code := reached("other.example.com", visitor); code != 409 {
		t.Errorf("a visit to another name: %d", code)
	}
	if v := e.dashboardView(); v.Reached || v.Port != e.cfg.PanelPort {
		t.Fatalf("after visits that don't count: %+v", v)
	}

	if code := reached("MC.Example.com.", visitor); code != 200 {
		t.Fatalf("a visit from outside: %d", code)
	}
	bare := "https://" + pageTestHost
	if v := e.dashboardView(); !v.Reached || v.Port != 443 || v.State != api.PortOpen {
		t.Fatalf("after a visit from outside: %+v", v)
	}
	if got := e.a.panelLink("/packs/x"); got != bare+"/packs/x" {
		t.Fatalf("a friends' pack link: %q", got)
	}
	if got := e.a.discordBase(); got != bare {
		t.Fatalf("Discord's link: %q", got)
	}
	if st := e.a.publicPageState(nil); !st.Dashboard || !st.Reached {
		t.Fatalf("the page's state: %+v", st)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'dashboard_443.reached'`); n != 1 {
		t.Fatalf("%d audit rows for the first visit", n)
	}
	reached(pageTestHost, visitor)
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'dashboard_443.reached'`); n != 1 {
		t.Fatalf("%d audit rows after a second visit", n)
	}

	// The machine restarts: its first minutes don't bring the port back.
	e.a.noteDashboard443(api.PagePort{Port: 443, State: api.PortWaiting})
	if v := e.dashboardView(); v.Port != 443 || v.State != api.PortWaiting {
		t.Fatalf("while the machine starts: %+v", v)
	}
	// Another program took port 443: the address has its port again.
	e.a.noteDashboard443(api.PagePort{Port: 443, State: api.PortBusy, Holder: "nginx"})
	if v := e.dashboardView(); v.Port != e.cfg.PanelPort || v.State != api.PortBusy || v.Holder != "nginx" || !v.Reached {
		t.Fatalf("with nginx on port 443: %+v", v)
	}
	if got := e.a.panelLink("/packs/x"); got != withPort+"/packs/x" {
		t.Fatalf("a link while nginx has port 443: %q", got)
	}
	e.a.noteDashboard443(api.PagePort{Port: 443, State: api.PortOpen})

	// Turning it off forgets the visit: on again, it waits for another.
	e.dashboardSwitch(false, true)
	if v := e.dashboardView(); v.Port != e.cfg.PanelPort || v.Reached {
		t.Fatalf("off: %+v", v)
	}
	if code := reached(pageTestHost, visitor); code != 409 {
		t.Fatalf("a visit while it's off: %d", code)
	}
	e.dashboardSwitch(true, true)
	if v := e.dashboardView(); v.Port != e.cfg.PanelPort || v.Reached {
		t.Fatalf("on again: %+v", v)
	}
	reached(pageTestHost, visitor)
	e.stop()
	e.start()
	if v := e.dashboardView(); v.Port != 443 || !v.Reached {
		t.Fatalf("a visit before a restart: %+v", v)
	}
}

func TestAJoinedMachineHasNoDashboardPort(t *testing.T) {
	e := newAgentEnvWith(t, func(e *agentEnv) {
		e.cfg.NoPanel, e.cfg.Dashboard443 = true, "on"
	})
	var a api.Address
	if code := e.callInto("GET", "/v1/address", nil, &a); code != 200 || a.Dashboard != nil {
		t.Fatalf("a joined machine's address: %d %+v", code, a.Dashboard)
	}
	if code, _ := e.dashboardSwitch(true, false); code != 409 {
		t.Fatalf("turning it on where no dashboard runs: %d", code)
	}
	if e.a.dashboard443On() {
		t.Fatal("the install's choice turned it on where no dashboard runs")
	}
	for _, r := range LinkRoutes() {
		if r.Pattern == dashboard443Path || r.Pattern == dashboard443ReachedPath {
			t.Errorf("a machine link offers %s %s", r.Method, r.Pattern)
		}
	}
}

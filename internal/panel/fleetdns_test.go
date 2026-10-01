package panel

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/dnszone"
	"github.com/CIYAhq/playkeeper/internal/machinelink"
	"github.com/CIYAhq/playkeeper/internal/version"
)

var (
	fsnIP = netip.MustParseAddr("65.108.10.20")
	helIP = netip.MustParseAddr("2a01:4f9:c010:1234::1")
)

func joinedMachine(id string) machine { return machine{ID: id, Name: id, Kind: remoteKind} }

func linkFrom(addr string) *machinelink.Status {
	return &machinelink.Status{State: "connected", Address: addr}
}

func TestMachineAddressIsAJoinedMachinesPublicIP(t *testing.T) {
	fsn := joinedMachine("j2345abcde")
	for _, c := range []struct {
		name string
		m    machine
		link *machinelink.Status
		ip   string
	}{
		{"IPv4 with the link's port", fsn, linkFrom("65.108.10.20:51234"), "65.108.10.20"},
		{"IPv6", fsn, linkFrom("[2a01:4f9:c010:1234::1]:443"), "2a01:4f9:c010:1234::1"},
		{"IPv4 written as IPv6", fsn, linkFrom("[::ffff:65.108.10.20]:51234"), "65.108.10.20"},
		{"IPv4 without a port", fsn, linkFrom("65.108.10.20"), "65.108.10.20"},
		{"IPv6 without a port", fsn, linkFrom("2a01:4f9:c010:1234::1"), "2a01:4f9:c010:1234::1"},
		{"the dashboard's own network", fsn, linkFrom("10.0.0.5:40000"), ""},
		{"this computer", fsn, linkFrom("127.0.0.1:40000"), ""},
		{"a carrier's shared addresses", fsn, linkFrom("100.64.1.2:40000"), ""},
		{"link-local", fsn, linkFrom("[fe80::1]:40000"), ""},
		{"not connected yet", fsn, linkFrom(""), ""},
		{"no link", fsn, nil, ""},
		{"the dashboard's own machine", machine{ID: "l2345abcde", Kind: localKind}, linkFrom("65.108.10.20:1"), ""},
	} {
		name, ip, ok := machineAddress(c.m, c.link)
		if ok != (c.ip != "") || (ok && (ip.String() != c.ip || name != "j2345abcde.m")) {
			t.Errorf("%s: %q, %v, %v; want %q", c.name, name, ip, ok, c.ip)
		}
	}
}

func TestTheZoneNamesServersOnJoinedMachinesWithAPublicAddress(t *testing.T) {
	machines := []machine{{ID: "l2345abcde", Kind: localKind}, joinedMachine("j2345abcde"), joinedMachine("h2345abcde"), joinedMachine("p2345abcde"), joinedMachine("n2345abcde")}
	links := map[string]*machinelink.Status{
		"j2345abcde": linkFrom("65.108.10.20:51234"),
		"h2345abcde": linkFrom("[2a01:4f9:c010:1234::1]:443"),
		"p2345abcde": linkFrom("192.168.1.20:40000"),
	}
	server := func(id, mid, slug string, port float64) map[string]any {
		return map[string]any{"id": id, "machineId": mid, "slug": slug, "gamePort": port}
	}
	disputed := server("dispute001", "j2345abcde", "twice", 25567)
	disputed["disputed"] = true
	got := zoneServers([]map[string]any{
		server("survival01", "l2345abcde", "survival", 25565),
		server("steve00001", "j2345abcde", "steve", 25565),
		server("noslug0001", "j2345abcde", "", 25566),
		server("noport0001", "j2345abcde", "noport", 0),
		disputed,
		server("nova000001", "h2345abcde", "nova", 25565),
		server("lanserver1", "p2345abcde", "lan", 25565),
		server("offline001", "n2345abcde", "offline", 25565),
		server("nomachine1", "z2345abcde", "gone", 25565),
	}, machines, links)
	want := []zoneServer{
		{id: "steve00001", label: "steve", port: 25565, machineID: "j2345abcde", machine: "j2345abcde.m", ip: fsnIP},
		{id: "nova000001", label: "nova", port: 25565, machineID: "h2345abcde", machine: "h2345abcde.m", ip: helIP},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("zoneServers =\n%+v\nwant\n%+v", got, want)
	}
}

// query is a DNS query for name and qtype, as a resolver sends it.
func query(name string, qtype uint16) []byte {
	q := []byte{0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}
	q = append(q, wireName(name)...)
	return binary.BigEndian.AppendUint16(binary.BigEndian.AppendUint16(q, qtype), 1)
}

func wireName(name string) []byte {
	var b []byte
	for _, l := range strings.Split(name, ".") {
		b = append(append(b, byte(len(l))), l...)
	}
	return append(b, 0)
}

// Each joined machine gets a name at its address once, and each of its
// servers an SRV record to the machine's name, which the machine's answers
// give, and a name at the dashboard's machine, whose page shows the server
// there. A server whose name another has, or no DNS name can be, is left
// out.
func TestJoinedServersJoinTheZoneAtTheirMachine(t *testing.T) {
	own := betaAddress()
	own.Records = append(own.Records, api.DNSRecord{Type: "A", Name: "panel.beta.example.com", Value: "203.0.113.5", TTL: 300})
	p := planDNS(own)
	named := p.addJoined([]zoneServer{
		{id: "steve00001", label: "steve", port: 25565, machine: "j2345abcde.m", ip: fsnIP},
		{id: "kara000001", label: "kara", port: 25566, machine: "j2345abcde.m", ip: fsnIP},
		{id: "machinetak", label: "h2345abcde.m", port: 25569, machine: "j2345abcde.m", ip: fsnIP},
		{id: "nova000001", label: "nova", port: 25565, machine: "h2345abcde.m", ip: helIP},
		{id: "alexclash1", label: "alex", port: 25567, machine: "j2345abcde.m", ip: fsnIP},
		{id: "panelclash", label: "panel", port: 25567, machine: "j2345abcde.m", ip: fsnIP},
		{id: "toolong001", label: strings.Repeat("a", 64), port: 25568, machine: "j2345abcde.m", ip: fsnIP},
	})
	var ids []string
	for _, j := range named {
		ids = append(ids, j.id)
	}
	if !slices.Equal(ids, []string{"steve00001", "kara000001", "nova000001"}) {
		t.Fatalf("named %v", ids)
	}
	joined := []dnszone.Record{
		{Name: "steve", Type: dnszone.TypeA, Value: "203.0.113.5"},
		{Name: "_minecraft._tcp.steve", Type: dnszone.TypeSRV, Value: "j2345abcde.m.beta.example.com", Port: 25565},
		{Name: "j2345abcde.m", Type: dnszone.TypeA, Value: "65.108.10.20"},
		{Name: "kara", Type: dnszone.TypeA, Value: "203.0.113.5"},
		{Name: "_minecraft._tcp.kara", Type: dnszone.TypeSRV, Value: "j2345abcde.m.beta.example.com", Port: 25566},
		{Name: "nova", Type: dnszone.TypeA, Value: "203.0.113.5"},
		{Name: "_minecraft._tcp.nova", Type: dnszone.TypeSRV, Value: "h2345abcde.m.beta.example.com", Port: 25565},
		{Name: "h2345abcde.m", Type: dnszone.TypeAAAA, Value: "2a01:4f9:c010:1234::1"},
	}
	want := betaZone([]dnszone.Record{{Name: "panel", Type: dnszone.TypeA, Value: "203.0.113.5"}}, serverRecords("alex", 25565), serverRecords("survival", 25567), joined)
	if !zonesEqual(p.zone, want) {
		t.Fatalf("the zone:\n%+v\nwant\n%+v", p.zone.Records, want.Records)
	}
	if err := p.zone.Check(); err != nil {
		t.Fatal(err)
	}

	var a dnszone.Answerer
	a.Set(p.zone)
	ask := func(name string, qtype uint16) []byte {
		t.Helper()
		ans := a.Answer(query(name, qtype), dnszone.MaxUDP)
		if len(ans) < 12 || ans[3]&0x0f != 0 || binary.BigEndian.Uint16(ans[6:8]) != 1 {
			t.Fatalf("%s: %x", name, ans)
		}
		return ans
	}
	srv := ask("_minecraft._tcp.steve.beta.example.com", 33)
	if !bytes.Contains(srv, append(binary.BigEndian.AppendUint16(nil, 25565), wireName("j2345abcde.m.beta.example.com")...)) {
		t.Errorf("steve's SRV answer: %x", srv)
	}
	if ans := ask("j2345abcde.m.beta.example.com", 1); !bytes.Contains(ans, fsnIP.AsSlice()) {
		t.Errorf("the machine's name: %x", ans)
	}
	for _, name := range []string{"alex.beta.example.com", "panel.beta.example.com"} {
		if ans := ask(name, 1); !bytes.Contains(ans, netip.MustParseAddr("203.0.113.5").AsSlice()) || bytes.Contains(ans, fsnIP.AsSlice()) {
			t.Errorf("%s, on the dashboard's machine, took a joined machine's address: %x", name, ans)
		}
	}
	if ans := ask("steve.beta.example.com", 1); !bytes.Contains(ans, netip.MustParseAddr("203.0.113.5").AsSlice()) || bytes.Contains(ans, fsnIP.AsSlice()) {
		t.Errorf("steve's name, which browsers open, isn't the dashboard's machine, whose page shows him: %x", ans)
	}
	if ans := ask("h2345abcde.m.beta.example.com", 28); !bytes.Contains(ans, helIP.AsSlice()) || bytes.Contains(ans, fsnIP.AsSlice()) {
		t.Errorf("a server took a machine's name: %x", ans)
	}
}

// Once the machine answers DNS for its domain, servers on joined machines
// join its zone. The server list gives them their address without a port
// only once public DNS finds the zone and the machine answers their
// records, and not after the answers go off.
func TestJoinedServersJoinWithoutAPortOnceDNSFindsTheZone(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	z := &zoneAgent{}
	e.zoneLocally(z)
	e.addressIs(t, betaAddress())
	steve := zoneServer{id: "steve00001", label: "steve", port: 25565, machine: "j2345abcde.m", ip: fsnIP}
	kara := zoneServer{id: "kara000001", label: "kara", port: 25566, machine: "j2345abcde.m", ip: fsnIP}
	joined := []zoneServer{steve}
	e.srv.joinedZone = func(context.Context) ([]zoneServer, error) { return joined, nil }

	if r := e.do(t, "PUT", "/api/dns-answers", `{"on":true}`, auth(cookie, csrf)); r.status != http.StatusOK || r.body["servers"] != float64(3) {
		t.Fatalf("turning the answers on: %d %v", r.status, r.body)
	}
	want := betaZone(serverRecords("alex", 25565), serverRecords("survival", 25567), []dnszone.Record{
		{Name: "steve", Type: dnszone.TypeA, Value: "203.0.113.5"},
		{Name: "_minecraft._tcp.steve", Type: dnszone.TypeSRV, Value: "j2345abcde.m.beta.example.com", Port: 25565},
		{Name: "j2345abcde.m", Type: dnszone.TypeA, Value: "65.108.10.20"},
	})
	if p := z.seen(); len(p) != 1 || !zonesEqual(p[0].Zone, want) {
		t.Fatalf("the machine was sent %+v, want %+v", p, want)
	}
	if a := e.srv.zoneAddress(steve.id); a != "" {
		t.Fatalf("before public DNS finds the zone, steve joins at %q", a)
	}

	found := betaAddress()
	found.Check = &api.AddressCheck{Ready: true, PortFree: true}
	e.addressIs(t, found)
	if err := e.srv.syncDNSAnswers(t.Context(), "playkeeper"); err != nil {
		t.Fatal(err)
	}
	if a := e.srv.zoneAddress(steve.id); a != "steve.beta.example.com" {
		t.Fatalf("once public DNS finds the zone, steve joins at %q", a)
	}

	// Kara's server appears while the machine won't take a new zone: the
	// wildcard would answer for her name at the dashboard's machine, so the
	// list gives her none until the machine answers her records.
	joined = []zoneServer{steve, kara}
	e.agent.mu.Lock()
	e.agent.answers["PUT /v1/dns-zone"] = func(w http.ResponseWriter, _ *http.Request) {
		writeErr(w, http.StatusBadGateway, api.CodeInternal, "The zone couldn't be saved.", "")
	}
	e.agent.mu.Unlock()
	if err := e.srv.syncDNSAnswers(t.Context(), "playkeeper"); err == nil {
		t.Fatal("a zone the machine refused")
	}
	if a, b := e.srv.zoneAddress(steve.id), e.srv.zoneAddress(kara.id); a != "steve.beta.example.com" || b != "" {
		t.Fatalf("with the new zone refused, steve joins at %q and kara at %q", a, b)
	}
	e.zoneLocally(z)
	if err := e.srv.syncDNSAnswers(t.Context(), "playkeeper"); err != nil {
		t.Fatal(err)
	}
	if b := e.srv.zoneAddress(kara.id); b != "kara.beta.example.com" {
		t.Fatalf("once the machine answers her records, kara joins at %q", b)
	}

	e.srv.joinedZone = func(context.Context) ([]zoneServer, error) { return nil, errDB }
	if err := e.srv.syncDNSAnswers(t.Context(), "playkeeper"); !errors.Is(err, errDB) || len(z.seen()) != 3 {
		t.Fatalf("with the servers unknown the machine keeps its zone: %v, %d zones sent", err, len(z.seen()))
	}
	if a := e.srv.zoneAddress(steve.id); a != "steve.beta.example.com" {
		t.Fatalf("steve's address went with a failed list: %q", a)
	}

	e.srv.joinedZone = func(context.Context) ([]zoneServer, error) { return joined, nil }
	if r := e.do(t, "PUT", "/api/dns-answers", `{"on":false}`, auth(cookie, csrf)); r.status != http.StatusOK {
		t.Fatalf("turning the answers off: %d %v", r.status, r.body)
	}
	if a := e.srv.zoneAddress(steve.id); a != "" {
		t.Fatalf("with the answers off, steve joins at %q", a)
	}
}

// Joined machines' links that can't be read leave the servers on them
// unknown, which keeps the zone the machine answers, rather than none of
// them having an address.
func TestJoinedServersAreUnknownWhileLinksCantBeRead(t *testing.T) {
	e := newEnvWith(t, func(o *Options) { o.LinkRoutes = nil })
	e.setup(t)
	if joined, err := e.srv.joinedZoneServers(t.Context()); err != nil || joined != nil {
		t.Fatalf("with no machine joined: %+v, %v", joined, err)
	}
	if _, err := e.srv.db.Exec(`INSERT INTO machines(id, project_id, name, kind, created_at) SELECT 'j2345abcde', id, 'fsn1-2', ?, 0 FROM projects LIMIT 1`, remoteKind); err != nil {
		t.Fatal(err)
	}
	if joined, err := e.srv.joinedZoneServers(t.Context()); err == nil {
		t.Fatalf("with links that can't be read: %+v, no error", joined)
	}
}

// A machine that joins from the dashboard's own network gets no name, as
// players can't reach its address; a joined machine's server the zone
// names gets its address without a port in the server list, and a server
// on the dashboard's own machine keeps what its agent says.
func TestTheServerListGivesAJoinedServerItsAddressWithoutAPort(t *testing.T) {
	e := newEnvConfig(t, withDomain, nil)
	cookie, csrf := e.setup(t)
	e.reply("GET", "/v1/machine", `{"hostname":"beta","agentVersion":"0.4.7"}`)
	e.reply("GET", "/v1/servers", `[{"id":"abcdefghjk","name":"Survival","slug":"survival","phase":"online","gamePort":25565}]`)
	addr := e.sharePort(t)
	fp, _ := e.linkInfo(t, cookie)["fingerprint"].(string)
	code, _ := e.joinCode(t, cookie, csrf, `{"name":"fsn1-2","dial":"name"}`)["code"].(string)
	ra := newRemoteAgent()
	ra.reply("GET /v1/servers", `[{"id":"rstuvwxyzq","name":"Cobblemon","slug":"cobblemon","phase":"online","gamePort":25566}]`)
	id := newIdentity(t)
	d, err := machinelink.Join(context.Background(), machinelink.JoinOptions{Address: addr, Code: code, Fingerprint: fp, Identity: id,
		Name: "fsn1-2", Version: version.Version, Now: e.clock.now})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	e.runLink(t, d, id, ra)
	eventually(t, "the machine is connected", func() bool { return linkState(e.machineView(t, cookie, d.MachineID)) == "connected" })

	if joined, err := e.srv.joinedZoneServers(t.Context()); err != nil || len(joined) != 0 {
		t.Fatalf("a machine joined from 127.0.0.1: %+v, %v", joined, err)
	}

	listed := func() map[string]any {
		t.Helper()
		var servers []map[string]any
		e.get(t, "/api/servers", cookie, &servers)
		out := map[string]any{}
		for _, sv := range servers {
			out[sv["id"].(string)] = sv["zoneAddress"]
		}
		return out
	}
	e.srv.setZoneAddresses([]zoneServer{{id: "rstuvwxyzq", label: "cobblemon"}, {id: "abcdefghjk", label: "survival"}}, "beta.playkeeper.me", true)
	if got := listed(); got["rstuvwxyzq"] != "cobblemon.beta.playkeeper.me" || got["abcdefghjk"] != nil {
		t.Fatalf("the server list's addresses without a port: %v", got)
	}
	e.srv.setZoneAddresses([]zoneServer{{id: "rstuvwxyzq", label: "cobblemon"}}, "beta.playkeeper.me", false)
	if got := listed(); got["rstuvwxyzq"] != nil {
		t.Fatalf("before public DNS finds the zone: %v", got)
	}
}

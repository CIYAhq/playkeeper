package certs

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

// machine is a VPS with three servers: the first on the default port and
// two more on their own ports, one with its own name.
func machine() Plan {
	return Plan{
		Name: "MC.Example.com",
		IPv4: here4,
		IPv6: here6,
		Servers: []JoinServer{
			{ID: "survival", Port: 25565},
			{ID: "creative", Host: "Creative.Example.com", Port: 25566},
			{ID: "test", Port: 25567},
		},
	}
}

func TestPlanRecords(t *testing.T) {
	got, err := machine().Records()
	if err != nil {
		t.Fatal(err)
	}
	want := []Record{
		{Type: "A", Name: "mc.example.com", Value: "203.0.113.10", TTL: 300},
		{Type: "AAAA", Name: "mc.example.com", Value: "2001:db8::10", TTL: 300},
		{ServerID: "creative", Type: "SRV", Name: "_minecraft._tcp.creative.example.com", Value: "0 5 25566 mc.example.com.", TTL: 300,
			SRV: &SRVParts{Service: "_minecraft", Protocol: "_tcp", Host: "creative.example.com", Priority: 0, Weight: 5, Port: 25566, Target: "mc.example.com"}},
	}
	if len(got) != len(want) {
		t.Fatalf("records %+v", got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.SRV != nil && w.SRV != nil && *g.SRV == *w.SRV {
			g.SRV, w.SRV = nil, nil
		}
		if g != w {
			t.Errorf("record %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	p := Plan{Name: "mc.example.com", Servers: []JoinServer{{ID: "a", Host: "mc.example.com", Port: 25570}, {ID: "b", Host: "mc.example.com", Port: 25565}}}
	if _, err := p.Records(); err == nil {
		t.Error("two servers with the same host were accepted")
	}
	p.Servers = p.Servers[:1]
	got, err = p.Records()
	if err != nil || len(got) != 1 || got[0].Name != "_minecraft._tcp.mc.example.com" || got[0].Value != "0 5 25570 mc.example.com." {
		t.Errorf("SRV for the machine's own name on another port = %+v, %v", got, err)
	}
	p.Servers = []JoinServer{{ID: "b", Host: "mc.example.com", Port: 25565}}
	if got, err := p.Records(); err != nil || len(got) != 0 {
		t.Errorf("the default port needs no record: %+v, %v", got, err)
	}
}

func TestPlanJoin(t *testing.T) {
	got, err := machine().Join()
	if err != nil {
		t.Fatal(err)
	}
	want := []JoinAddress{
		{ServerID: "survival", Address: "mc.example.com", Direct: "mc.example.com"},
		{ServerID: "creative", Address: "creative.example.com", Direct: "mc.example.com:25566"},
		{ServerID: "test", Address: "mc.example.com:25567", Direct: "mc.example.com:25567"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Join = %+v\nwant %+v", got, want)
	}
}

func TestPlanInvalid(t *testing.T) {
	many := make([]JoinServer, maxPlanServers+1)
	for i := range many {
		many[i] = JoinServer{ID: "s", Port: 30000 + i}
	}
	cases := []struct {
		name string
		edit func(*Plan)
		code string
		kind string
	}{
		{"bad name", func(p *Plan) { p.Name = "https://mc.example.com" }, CodeInvalidName, "not_a_name"},
		{"bad host", func(p *Plan) { p.Servers[1].Host = "creative_example.com" }, CodeInvalidName, "bad_label"},
		{"port zero", func(p *Plan) { p.Servers[2].Port = 0 }, CodeInvalidPlan, "bad_port"},
		{"port too high", func(p *Plan) { p.Servers[2].Port = 65536 }, CodeInvalidPlan, "bad_port"},
		{"same port", func(p *Plan) { p.Servers[2].Port = 25566 }, CodeInvalidPlan, "duplicate_port"},
		{"same host", func(p *Plan) { p.Servers[2].Host = "creative.example.com" }, CodeInvalidPlan, "duplicate_host"},
		{"host of the default server", func(p *Plan) { p.Servers[2].Host = "mc.example.com" }, CodeInvalidPlan, "duplicate_host"},
		{"IPv6 as IPv4", func(p *Plan) { p.IPv4 = here6 }, CodeInvalidPlan, "bad_address"},
		{"IPv4 as IPv6", func(p *Plan) { p.IPv6 = netip.MustParseAddr("::ffff:203.0.113.10") }, CodeInvalidPlan, "bad_address"},
		{"too many servers", func(p *Plan) { p.Servers = many }, CodeInvalidPlan, "too_many"},
	}
	for _, c := range cases {
		p := machine()
		p.Servers = slices.Clone(p.Servers)
		c.edit(&p)
		for _, f := range []func() error{
			func() error { _, err := p.Records(); return err },
			func() error { _, err := p.Join(); return err },
			func() error { _, err := CheckPlan(context.Background(), &fakeResolver{}, p, bothHere); return err },
		} {
			pr := problemOf(t, f())
			if pr.Code != c.code || pr.Params["kind"] != c.kind {
				t.Errorf("%s: %s/%s, want %s/%s", c.name, pr.Code, pr.Params["kind"], c.code, c.kind)
			}
		}
	}
	p := machine()
	p.IPv4 = netip.MustParseAddr("::ffff:203.0.113.10")
	if got, err := p.Records(); err != nil || got[0].Value != "203.0.113.10" {
		t.Errorf("a mapped IPv4 address = %+v, %v", got, err)
	}
	p = machine()
	p.Servers[2].Port = 25565
	pr := problemOf(t, func() error { _, err := p.Records(); return err }())
	if pr.Message != "Two servers use port 25565." {
		t.Errorf("message %q", pr.Message)
	}
}

func srv(target string, port uint16) *net.SRV {
	return &net.SRV{Target: target, Port: port, Priority: 0, Weight: 5}
}

func TestCheckPlan(t *testing.T) {
	good := func() *fakeResolver {
		return &fakeResolver{
			a:    map[string][]string{"mc.example.com": {"203.0.113.10"}},
			aaaa: map[string][]string{"mc.example.com": {"2001:db8::10"}},
			srv:  map[string][]*net.SRV{"_minecraft._tcp.creative.example.com": {srv("mc.example.com.", 25566)}},
		}
	}
	cases := []struct {
		name  string
		edit  func(*fakeResolver)
		ready bool
		rows  []string // code of each record row
		found string
	}{
		{"ready", func(*fakeResolver) {}, true, []string{CodeSRVOK}, ""},
		{"target in capitals", func(r *fakeResolver) {
			r.srv["_minecraft._tcp.creative.example.com"] = []*net.SRV{srv("MC.EXAMPLE.COM.", 25566)}
		}, true, []string{CodeSRVOK}, ""},
		{"SRV missing", func(r *fakeResolver) { delete(r.srv, "_minecraft._tcp.creative.example.com") }, false, []string{CodeSRVMissing}, ""},
		{"wrong port", func(r *fakeResolver) {
			r.srv["_minecraft._tcp.creative.example.com"] = []*net.SRV{srv("mc.example.com.", 25999)}
		}, false, []string{CodeSRVWrong}, "0 5 25999 mc.example.com."},
		{"wrong target", func(r *fakeResolver) {
			r.srv["_minecraft._tcp.creative.example.com"] = []*net.SRV{srv("old-host.example.net.", 25566)}
		}, false, []string{CodeSRVWrong}, "0 5 25566 old-host.example.net."},
		{"one of two wrong", func(r *fakeResolver) {
			r.srv["_minecraft._tcp.creative.example.com"] = append(r.srv["_minecraft._tcp.creative.example.com"], srv("old-host.example.net.", 25565))
		}, false, []string{CodeSRVWrong}, "0 5 25566 mc.example.com., 0 5 25565 old-host.example.net."},
		{"SRV lookup fails", func(r *fakeResolver) {
			r.fail = map[string]error{"SRV _minecraft._tcp.creative.example.com": servfail}
		}, false, []string{CodeSRVLookupFailed}, ""},
		{"default server redirected", func(r *fakeResolver) {
			r.srv["_minecraft._tcp.mc.example.com"] = []*net.SRV{srv("mc.example.com.", 25567)}
		}, false, []string{CodeSRVConflict, CodeSRVOK}, "0 5 25567 mc.example.com."},
		{"default server SRV to itself", func(r *fakeResolver) {
			r.srv["_minecraft._tcp.mc.example.com"] = []*net.SRV{srv("mc.example.com.", 25565)}
		}, true, []string{CodeSRVOK}, ""},
		{"name elsewhere", func(r *fakeResolver) { r.a["mc.example.com"] = []string{"198.51.100.7"} }, false, []string{CodeSRVOK}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := good()
			c.edit(r)
			pc, err := CheckPlan(context.Background(), r, machine(), bothHere)
			if err != nil {
				t.Fatal(err)
			}
			var codes []string
			for _, rc := range pc.Records {
				codes = append(codes, rc.Code)
				if rc.OK != (rc.Code == CodeSRVOK) || strings.ContainsAny(rc.Message+rc.Hint, "{}") {
					t.Errorf("row %+v", rc)
				}
			}
			if pc.Ready != c.ready || !slices.Equal(codes, c.rows) {
				t.Fatalf("Ready %v, rows %v; want %v, %v (name: %s)", pc.Ready, codes, c.ready, c.rows, pc.Name.Code)
			}
			if c.found != "" && pc.Records[0].Params["found"] != c.found {
				t.Errorf("found %q, want %q", pc.Records[0].Params["found"], c.found)
			}
		})
	}

	pc, _ := CheckPlan(context.Background(), &fakeResolver{
		a:   map[string][]string{"mc.example.com": {"203.0.113.10"}},
		srv: map[string][]*net.SRV{"_minecraft._tcp.mc.example.com": {srv("elsewhere.example.net.", 25565)}},
	}, machine(), bothHere)
	conflict := pc.Records[0]
	if conflict.Record.Name != "_minecraft._tcp.mc.example.com" || conflict.Record.ServerID != "survival" ||
		conflict.Hint != "Delete the SRV record _minecraft._tcp.mc.example.com at your DNS provider." {
		t.Errorf("conflict row = %+v", conflict)
	}
	if missing := pc.Records[1]; missing.Record.Value != "0 5 25566 mc.example.com." ||
		missing.Message != "The SRV record for creative.example.com does not exist yet." {
		t.Errorf("missing row = %+v", missing)
	}
}

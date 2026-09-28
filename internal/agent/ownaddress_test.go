package agent

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/certs"
)

// ownDomainEnv is an agent whose machine is play.example.com, pointing
// here, with three servers: Survival on 25565, then Creative and Test.
func ownDomainEnv(t *testing.T) (e *addressEnv, survival, creative, test string) {
	t.Helper()
	e = newAddressEnv(t, nil)
	survival, creative, test = e.addServerNamed("Survival"), e.addServerNamed("Creative"), e.addServerNamed("Test")
	e.dns.set("play.example.com", testIP.String())
	e.dns.setSRV("creative.play.example.com", 25566, "play.example.com")
	e.dns.setSRV("test.play.example.com", 25567, "play.example.com")
	var v api.Address
	check := map[string]any{"domain": "play.example.com", "acceptTerms": true, "panelHost": "203.0.113.10:8443", "actor": "admin"}
	if code := e.callInto("POST", "/v1/address/check", check, &v); code != 200 || !v.Check.Ready || v.Operation == nil {
		t.Fatalf("the machine's domain: %d %+v", code, v)
	}
	if op := e.waitOp(v.Operation.ID); op.Status != api.OpSucceeded {
		t.Fatalf("the machine's certificate: %+v", op)
	}
	return e, survival, creative, test
}

func (e *addressEnv) setOwn(id, address string) (int, map[string]any) {
	e.t.Helper()
	return e.call("POST", "/v1/servers/"+id+"/own-address", map[string]any{"address": address, "actor": "admin"})
}

func joinOf(v api.Address, id string) api.JoinAddress {
	for _, j := range v.Servers {
		if j.ServerID == id {
			return j
		}
	}
	return api.JoinAddress{}
}

func TestAServerGetsAnAddressOfItsOwnUnderTheOwnDomain(t *testing.T) {
	e := newAddressEnv(t, nil)
	creative := e.addServerNamed("Creative")
	if code, out := e.setOwn(creative, "alex.example.com"); code != 409 {
		t.Fatalf("an own address without an own domain: %d %v", code, out)
	}

	e, survival, creative, test := ownDomainEnv(t)
	if code, out := e.setOwn(creative, " Alex.Example.com. "); code != 200 {
		t.Fatalf("setting it: %d %v", code, out)
	}
	if _, err := e.a.checkOwn(context.Background()); err != nil {
		t.Fatal(err)
	}
	v := e.address()
	if j := joinOf(v, creative); j.OwnAddress != "alex.example.com" || j.Address != "alex.example.com" || j.Published {
		t.Fatalf("its join address before its records exist: %+v", j)
	}
	// The domain works whatever a server's own address does.
	if !v.Check.Ready || !joinOf(v, survival).Published || !joinOf(v, test).Published {
		t.Fatalf("the domain before the own address's records exist: ready %v, %+v", v.Check.Ready, v.Servers)
	}
	st := e.a.address()
	e.a.saveNameCheck(st.Host, certs.CheckName(context.Background(), e.dns, st.Host, e.a.expectedAddrs(st)))
	if v := e.address(); !v.Check.Ready {
		t.Fatal("looking at the machine's name alone made the domain wait for the own address")
	}
	var clash api.Address
	if code := e.callInto("POST", "/v1/address/check", map[string]any{"domain": "alex.example.com", "actor": "admin"}, &clash); code != 409 || e.address().Host != "play.example.com" {
		t.Fatalf("moving the machine to a server's own address: %d, now %q", code, e.address().Host)
	}
	var own []api.DNSRecord
	for _, r := range v.Records {
		if r.ServerID == creative {
			own = append(own, r)
		}
	}
	if len(own) != 2 || own[0].Type != "A" || own[0].Name != "alex.example.com" || own[0].Value != testIP.String() ||
		own[1].Type != "SRV" || own[1].Name != "_minecraft._tcp.alex.example.com" || own[1].Value != "0 5 25566 alex.example.com." {
		t.Fatalf("the records it needs: %+v", own)
	}

	if code, out := e.setOwn(test, "play.example.com"); code != 400 || out["error"] != "play.example.com is the machine's own name. Give the server another." {
		t.Fatalf("the machine's name: %d %v", code, out)
	}
	for name, c := range map[string]struct {
		id, address string
		code        int
	}{
		"the machine's name":           {test, "play.example.com", 400},
		"another server's own address": {test, "alex.example.com", 409},
		"another server's address":     {survival, "test.play.example.com", 400},
		"not a name":                   {test, "not a name!", 400},
		"an IP address":                {test, "203.0.113.10", 400},
	} {
		if code, out := e.setOwn(c.id, c.address); code != c.code {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}

	e.dns.set("alex.example.com", testIP.String())
	e.dns.setSRV("alex.example.com", 25566, "alex.example.com")
	if _, err := e.a.checkOwn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if j := joinOf(e.address(), creative); !j.Published {
		t.Fatalf("its join address once its records work: %+v", j)
	}
	if got := e.a.serverByID(creative).Status(context.Background()).JoinAddress; got != "alex.example.com" {
		t.Fatalf("the server's join address: %q", got)
	}

	e.a.serversChanged()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if row := e.a.loadCertificate("alex.example.com"); row != nil && row.status.Certificate != nil {
			if row.challenge != "http-01" {
				t.Fatalf("its certificate row: %+v", row)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the own address got no certificate")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM operations WHERE kind = 'certificate.server'`); n != 1 {
		t.Fatalf("%d certificate operations for the own address", n)
	}
	reqs := e.ca.requests()
	if last := reqs[len(reqs)-1]; !slices.Equal(last.Names, []string{"alex.example.com"}) || last.HTTP01 == nil {
		t.Fatalf("certificate requests: %+v", reqs)
	}

	if code, out := e.setOwn(creative, ""); code != 200 {
		t.Fatalf("clearing it: %d %v", code, out)
	}
	if j := joinOf(e.address(), creative); j.OwnAddress != "" || j.Address != "creative.play.example.com" {
		t.Fatalf("after clearing it: %+v", j)
	}
	if e.a.loadCertificate("alex.example.com") != nil {
		t.Fatal("the cleared address kept its certificate")
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'server.own_address'`); n != 2 {
		t.Fatalf("%d audit entries, want one to set and one to clear", n)
	}
}

// The servers' own addresses share Let's Encrypt's weekly limit with every
// other name under the domain, so at most a few are asked for a day.
func TestOwnAddressesGetAFewCertificatesADay(t *testing.T) {
	e, _, creative, test := ownDomainEnv(t)
	// Three own addresses asked for their certificates an hour ago.
	for _, host := range []string{"one.example.com", "two.example.com", "three.example.com"}[:ownCertsPerDay] {
		row := &certRow{name: host, source: api.AddressOwn, challenge: "http-01", status: certs.Status{Names: []string{host}, LastAttempt: e.a.now().Add(-time.Hour)}}
		if err := e.a.saveCertificate(row); err != nil {
			t.Fatal(err)
		}
		if _, err := e.a.db.Exec(`UPDATE servers SET own_address = ? WHERE id = ?`, host, e.addServerNamed(host)); err != nil {
			t.Fatal(err)
		}
	}
	for id, host := range map[string]string{creative: "alex.example.com", test: "sam.example.com"} {
		e.dns.set(host, testIP.String())
		if code, out := e.setOwn(id, host); code != 200 {
			t.Fatalf("%s: %d %v", host, code, out)
		}
	}
	e.dns.setSRV("alex.example.com", 25566, "alex.example.com")
	e.dns.setSRV("sam.example.com", 25567, "sam.example.com")
	if _, err := e.a.checkOwn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if host, held := e.a.ownCertificateDue(e.a.address()); host != "" || !held {
		t.Fatalf("with the day's certificates used up: %q, held %v", host, held)
	}
	for _, host := range []string{"alex.example.com", "sam.example.com"} {
		if e.a.loadCertificate(host) != nil {
			t.Fatalf("%s was asked for a certificate over the day's limit", host)
		}
	}
	e.skew.Add(int64(25 * time.Hour))
	if host, held := e.a.ownCertificateDue(e.a.address()); host != "alex.example.com" || held {
		t.Fatalf("a day later: %q, held %v", host, held)
	}
}

func TestAnOwnAddressOpensOnlyItsServersPage(t *testing.T) {
	e, _, creative, _ := ownDomainEnv(t)
	if code, out := e.setOwn(creative, "alex.example.com"); code != 200 {
		t.Fatalf("setting it: %d %v", code, out)
	}
	if st := e.a.publicPageState(); st.Host != "play.example.com" || !slices.Equal(st.Hosts, []string{"alex.example.com"}) {
		t.Fatalf("the page's state: %+v", st)
	}
	code, page, _ := e.page("alex.example.com")
	if code != 200 || page.Address != "alex.example.com" || len(page.Servers) != 1 || page.Servers[0].Name != "Creative" {
		t.Fatalf("the own address's page: %d %+v", code, page)
	}
	if _, all, _ := e.page("play.example.com"); len(all.Servers) != 3 {
		t.Fatalf("the machine's page: %+v", all)
	}
	for _, host := range []string{"sam.example.com", "creative.play.example.com", "alex.example.com.evil.test"} {
		if code, _, _ := e.page(host); code != 404 {
			t.Errorf("%s: %d", host, code)
		}
	}
	if e.a.pageServer("alex.example.com", "creative") == nil || e.a.pageServer("alex.example.com", "survival") != nil {
		t.Fatal("the own address's page serves the wrong icons")
	}
	if v := e.a.serverByID(creative).publicPageView(); v.Host != "alex.example.com" {
		t.Fatalf("Settings show the page at %q", v.Host)
	}

	if code, _ := e.call("POST", "/v1/servers/"+creative+"/public-page", map[string]any{"enabled": false, "actor": "admin"}); code != 200 {
		t.Fatalf("turning the page off: %d", code)
	}
	if code, _, _ := e.page("alex.example.com"); code != 404 {
		t.Fatalf("the own address of a server off the page: %d", code)
	}
	if e.a.pageServer("alex.example.com", "creative") != nil {
		t.Fatal("the own address of a server off the page serves its icon")
	}
	if st := e.a.publicPageState(); len(st.Hosts) != 0 {
		t.Fatalf("the page still answers %v", st.Hosts)
	}
}

// An own address has records of its own: it works, gets its certificate
// and shows Bedrock players where to join while the machine's name points
// elsewhere.
func TestAnOwnAddressWorksWithoutTheMachinesName(t *testing.T) {
	e, _, creative, _ := ownDomainEnv(t)
	if _, err := e.a.db.Exec(`UPDATE servers SET config = json_set(config, '$.crossplayPort', 19133) WHERE id = ?`, creative); err != nil {
		t.Fatal(err)
	}
	e.dns.set("alex.example.com", testIP.String())
	e.dns.setSRV("alex.example.com", 25566, "alex.example.com")
	if code, out := e.setOwn(creative, "alex.example.com"); code != 200 {
		t.Fatalf("setting it: %d %v", code, out)
	}
	e.dns.set("play.example.com", "198.51.100.7")
	if _, err := e.a.checkOwn(context.Background()); err != nil {
		t.Fatal(err)
	}
	v := e.address()
	if v.Check.Name.OK || !joinOf(v, creative).Published {
		t.Fatalf("with the machine's name elsewhere: name ok %v, %+v", v.Check.Name.OK, joinOf(v, creative))
	}
	e.a.serversChanged()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if row := e.a.loadCertificate("alex.example.com"); row != nil && row.status.Certificate != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the own address got no certificate while the machine's name points elsewhere")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, page, _ := e.page("alex.example.com")
	if len(page.Servers) != 1 || page.Servers[0].Address != "alex.example.com" || page.Servers[0].Bedrock == nil || page.Servers[0].Bedrock.Host != "alex.example.com" {
		t.Fatalf("the own address's page: %+v", page.Servers)
	}
}

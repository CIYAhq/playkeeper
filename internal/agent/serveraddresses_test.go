package agent

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

func (e *addressEnv) setServerAddresses(on bool) (int, api.Address) {
	e.t.Helper()
	var v api.Address
	code := e.callInto("POST", "/v1/address/server-addresses", map[string]any{"on": on, "actor": "admin"}, &v)
	return code, v
}

// certified waits until each of hosts has a certificate.
func (e *addressEnv) certified(hosts ...string) {
	e.t.Helper()
	e.waitFor("certificates for "+joinNames(hosts), func() bool {
		for _, h := range hosts {
			if row := e.a.loadCertificate(h); row == nil || row.status.Certificate == nil {
				return false
			}
		}
		return true
	})
}

func joinNames(hosts []string) string {
	out := ""
	for i, h := range hosts {
		if i > 0 {
			out += ", "
		}
		out += h
	}
	return out
}

func TestAnAddressForEachServerNeedsTheOwnDomain(t *testing.T) {
	e := newAddressEnv(t, nil)
	e.addServerNamed("Survival")
	if code, _ := e.setServerAddresses(true); code != 409 {
		t.Fatalf("without an own domain: %d", code)
	}
}

// The managed beta's machine: the owner adds one wildcard record, and every
// server gets <name>.<domain> with its own page and certificate. Players
// type the port, since no record says it.
func TestEveryServerGetsAnAddressUnderTheWildcard(t *testing.T) {
	e, survival, creative, test := ownDomainEnvWith(t, func(o *Options) { o.AddressInterval = 20 * time.Millisecond })
	code, v := e.setServerAddresses(true)
	if code != 200 || !v.ServerAddresses {
		t.Fatalf("turning it on: %d %+v", code, v)
	}
	var wild []api.DNSRecord
	for _, r := range v.Records {
		switch {
		case r.Name == "*.play.example.com":
			wild = append(wild, r)
		case r.ServerID != "":
			t.Errorf("a record for one server: %+v", r)
		}
	}
	if len(wild) != 1 || wild[0].Type != "A" || wild[0].Value != testIP.String() {
		t.Fatalf("the records to add: %+v", v.Records)
	}
	want := map[string]string{survival: "survival.play.example.com", creative: "creative.play.example.com:25566", test: "test.play.example.com:25567"}
	for id, address := range want {
		if j := joinOf(v, id); j.Address != address || !j.Automatic || j.OwnAddress+portOf(j) != address || j.Published {
			t.Errorf("before the wildcard record: %+v", j)
		}
	}
	e.waitFor("a look with the wildcard missing", func() bool {
		c := e.address().Check
		return c != nil && slices.ContainsFunc(c.Records, func(rc api.RecordCheck) bool { return rc.Record.Name == "*.play.example.com" && !rc.OK })
	})
	e.a.addr.mu.Lock()
	next := e.a.addr.recheck.Sub(e.a.now())
	e.a.addr.mu.Unlock()
	if next > ownRecheckPending {
		t.Fatalf("with the wildcard record missing, the next look is in %v", next)
	}

	e.dns.set("*.play.example.com", testIP.String())
	if _, err := e.a.checkOwn(context.Background()); err != nil {
		t.Fatal(err)
	}
	v = e.address()
	for id, address := range want {
		if j := joinOf(v, id); j.Address != address || !j.Published {
			t.Errorf("with the wildcard record: %+v", j)
		}
	}
	if got := e.a.serverByID(creative).Status(context.Background()).JoinAddress; got != "creative.play.example.com:25566" {
		t.Fatalf("the server's join address: %q", got)
	}
	e.a.serversChanged()
	e.certified("survival.play.example.com", "creative.play.example.com", "test.play.example.com")
	for _, r := range e.ca.requests()[1:] {
		if len(r.Names) != 1 || r.HTTP01 == nil {
			t.Fatalf("a certificate request: %+v", r)
		}
	}
	if st := e.a.publicPageState(); len(st.Hosts) != 3 {
		t.Fatalf("the page answers %v", st.Hosts)
	}
	if code, page, _ := e.page("creative.play.example.com"); code != 200 || len(page.Servers) != 1 || page.Servers[0].Name != "Creative" {
		t.Fatalf("creative's page: %d %+v", code, page.Servers)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'address.server_addresses'`); n != 1 {
		t.Fatalf("%d audit entries", n)
	}
}

func portOf(j api.JoinAddress) string {
	if j.Port == 25565 {
		return ""
	}
	return ":" + itoa(j.Port)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// A server given an address keeps it, and no server takes another's.
func TestServersGivenAnAddressKeepItUnderTheWildcard(t *testing.T) {
	e, survival, creative, test := ownDomainEnv(t)
	admin := e.addServerNamed("Admin")
	if code, _ := e.setServerAddresses(true); code != 200 {
		t.Fatal("turning it on")
	}
	if code, out := e.setOwn(test, "creative.play.example.com"); code != 409 || out["error"] != "Creative has that address." {
		t.Fatalf("taking creative's address: %d %v", code, out)
	}
	if code, out := e.setOwn(creative, "alex.example.com"); code != 200 {
		t.Fatalf("giving creative its own: %d %v", code, out)
	}
	v := e.address()
	if j := joinOf(v, creative); j.Address != "alex.example.com" || j.Automatic {
		t.Fatalf("creative with an address of its own: %+v", j)
	}
	// Under someone's own domain, any name is theirs to use.
	if j := joinOf(v, admin); j.Address != "admin.play.example.com:25568" || !j.Automatic {
		t.Fatalf("admin: %+v", j)
	}
	if j := joinOf(v, survival); j.Address != "survival.play.example.com" || !j.Automatic {
		t.Fatalf("survival: %+v", j)
	}
	var creativeRecords []string
	for _, r := range v.Records {
		if r.ServerID == creative {
			creativeRecords = append(creativeRecords, r.Type+" "+r.Name)
		}
	}
	if !slices.Equal(creativeRecords, []string{"A alex.example.com", "SRV _minecraft._tcp.alex.example.com"}) {
		t.Fatalf("creative's records: %v", creativeRecords)
	}
}

// A name given to one server before the wildcard was turned on stays that
// server's: the server it would have gone to gets none.
func TestANameGivenBeforeTheWildcardStaysItsServers(t *testing.T) {
	e, survival, _, test := ownDomainEnv(t)
	// Survival, on 25565, joins at the domain itself until the wildcard.
	if code, out := e.setOwn(test, "survival.play.example.com"); code != 200 {
		t.Fatalf("giving test survival's name: %d %v", code, out)
	}
	code, v := e.setServerAddresses(true)
	if code != 200 {
		t.Fatalf("turning it on: %d %+v", code, v)
	}
	if j := joinOf(v, survival); j.Automatic || j.Address != "play.example.com" {
		t.Fatalf("survival: %+v", j)
	}
	if j := joinOf(v, test); j.Automatic || j.Address != "survival.play.example.com" {
		t.Fatalf("test: %+v", j)
	}
}

// Under beta.playkeeper.me, a server whose name the names service keeps
// for Playkeeper, such as Admin, gets no address, so no page there looks
// like Playkeeper's own.
func TestUnderPlaykeeperMeOfficialNamesGetNoAddress(t *testing.T) {
	e := newAddressEnv(t, nil)
	alex, admin := e.addServerNamed("Alex"), e.addServerNamed("Admin")
	e.dns.set("beta.playkeeper.me", testIP.String())
	var v api.Address
	check := map[string]any{"domain": "beta.playkeeper.me", "acceptTerms": true, "panelHost": "203.0.113.10:8443", "actor": "admin"}
	if code := e.callInto("POST", "/v1/address/check", check, &v); code != 200 || v.Operation == nil {
		t.Fatalf("the machine's domain: %d %+v", code, v)
	}
	e.waitOp(v.Operation.ID)
	if code, v := e.setServerAddresses(true); code != 200 || joinOf(v, alex).Address != "alex.beta.playkeeper.me" || joinOf(v, admin).Address != "beta.playkeeper.me:25566" || joinOf(v, admin).Automatic {
		t.Fatalf("turning it on: %d %+v", code, v.Servers)
	}
	for _, js := range e.a.ownAddresses(e.a.address()) {
		if js.id == admin {
			t.Fatalf("admin has an own address, %s", js.own)
		}
	}
}

// Turning it off keeps the certificates, so turning it on again asks Let's
// Encrypt for none; a deleted server's and an old domain's go.
func TestTheWildcardsCertificatesLastUntilTheirServerOrDomainGoes(t *testing.T) {
	e, _, _, test := ownDomainEnvWith(t, func(o *Options) { o.AddressInterval = 20 * time.Millisecond })
	e.dns.set("*.play.example.com", testIP.String())
	if code, _ := e.setServerAddresses(true); code != 200 {
		t.Fatal("turning it on")
	}
	e.certified("survival.play.example.com", "creative.play.example.com", "test.play.example.com")
	asked := len(e.ca.requests())
	if code, v := e.setServerAddresses(false); code != 200 || v.ServerAddresses || joinOf(v, test).Address != "test.play.example.com" || joinOf(v, test).Automatic {
		t.Fatalf("turning it off: %d %+v", code, v)
	}
	if code, _ := e.setServerAddresses(true); code != 200 {
		t.Fatal("turning it on again")
	}
	e.a.serversChanged()
	time.Sleep(200 * time.Millisecond)
	if n := len(e.ca.requests()); n != asked {
		t.Fatalf("turning it off and on asked for %d more certificates", n-asked)
	}

	// With it off again, the certificates it kept still go with their
	// server or domain.
	if code, _ := e.setServerAddresses(false); code != 200 {
		t.Fatal("turning it off again")
	}
	code, out := e.call("POST", "/v1/servers/"+test+"/delete", map[string]any{"confirm": "Test", "actor": "admin"})
	if code != 202 {
		t.Fatalf("delete: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("delete: %+v", op)
	}
	if e.a.loadCertificate("test.play.example.com") != nil {
		t.Fatal("the deleted server's certificate stayed")
	}

	e.dns.set("mc.example.com", testIP.String())
	var v api.Address
	if code := e.callInto("POST", "/v1/address/check", map[string]any{"domain": "mc.example.com", "actor": "admin"}, &v); code != 200 || v.ServerAddresses {
		t.Fatalf("moving to mc.example.com: %d %+v", code, v)
	}
	if e.a.loadCertificate("creative.play.example.com") != nil {
		t.Fatal("the old domain's certificates stayed")
	}
	if v.Operation != nil {
		e.waitOp(v.Operation.ID)
	}
	if code, v := e.setServerAddresses(true); code != 200 || !slices.ContainsFunc(v.Records, func(r api.DNSRecord) bool { return r.Name == "*.mc.example.com" }) {
		t.Fatalf("the new domain's records: %d %+v", code, v.Records)
	}
}

// The day's few certificates count whatever became of their names: with
// the switch turned off, an address given by hand gets none more that day,
// and giving the domain up forgets the certificates but not the count.
func TestTheDaysCertificatesCountWhateverBecameOfTheirNames(t *testing.T) {
	e, _, creative, _ := ownDomainEnvWith(t, func(o *Options) { o.AddressInterval = 20 * time.Millisecond })
	e.dns.set("*.play.example.com", testIP.String())
	if code, _ := e.setServerAddresses(true); code != 200 {
		t.Fatal("turning it on")
	}
	e.certified("survival.play.example.com", "creative.play.example.com", "test.play.example.com")
	if code, _ := e.setServerAddresses(false); code != 200 {
		t.Fatal("turning it off")
	}
	e.dns.set("alex.example.com", testIP.String())
	e.dns.setSRV("alex.example.com", 25566, "alex.example.com")
	if code, out := e.setOwn(creative, "alex.example.com"); code != 200 {
		t.Fatalf("giving creative an address: %d %v", code, out)
	}
	if _, err := e.a.checkOwn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if host, held := e.a.ownCertificateDue(e.a.address()); host != "" || !held {
		t.Fatalf("with the switch off and an address given by hand: %q, held %v", host, held)
	}
	if e.a.loadCertificate("alex.example.com") != nil {
		t.Fatal("alex.example.com was asked for a certificate over the day's limit")
	}
	if code, out := e.call("DELETE", "/v1/address?actor=admin", nil); code != 200 {
		t.Fatalf("giving the domain up: %d %v", code, out)
	}
	if n := e.a.ownCertsToday(e.a.address()); n != ownCertsPerDay {
		t.Fatalf("with the certificates forgotten, %d of the day's count", n)
	}
	// Creative's certificate from the wildcard, from before it was given
	// an address of its own, went with the domain too.
	if e.a.loadCertificate("creative.play.example.com") != nil {
		t.Fatal("creative's certificate from the wildcard stayed")
	}
}

func TestStoppingTheDomainTakesTheWildcardsCertificatesWhileItsOff(t *testing.T) {
	e, _, _, _ := ownDomainEnvWith(t, func(o *Options) { o.AddressInterval = 20 * time.Millisecond })
	e.dns.set("*.play.example.com", testIP.String())
	if code, _ := e.setServerAddresses(true); code != 200 {
		t.Fatal("turning it on")
	}
	e.certified("survival.play.example.com", "creative.play.example.com", "test.play.example.com")
	if code, _ := e.setServerAddresses(false); code != 200 {
		t.Fatal("turning it off")
	}
	if code, out := e.call("DELETE", "/v1/address?actor=admin", nil); code != 200 {
		t.Fatalf("stopping the domain: %d %v", code, out)
	}
	for _, host := range []string{"survival.play.example.com", "creative.play.example.com", "test.play.example.com"} {
		if e.a.loadCertificate(host) != nil {
			t.Errorf("%s's certificate stayed", host)
		}
	}
}

// Every server's address under the wildcard counts against the few
// certificates asked for a day, as a given one does.
func TestTheWildcardsCertificatesStayWithinTheDaysLimit(t *testing.T) {
	e, _, _, _ := ownDomainEnvWith(t, func(o *Options) { o.AddressInterval = 20 * time.Millisecond })
	e.addServerNamed("Lobby")
	e.addServerNamed("Hub")
	e.dns.set("*.play.example.com", testIP.String())
	if code, _ := e.setServerAddresses(true); code != 200 {
		t.Fatal("turning it on")
	}
	e.a.serversChanged()
	e.waitFor("the day's certificates", func() bool { return e.a.ownCertsToday(e.a.address()) == ownCertsPerDay })
	if host, held := e.a.ownCertificateDue(e.a.address()); host != "" || !held {
		t.Fatalf("with the day's certificates used up: %q, held %v", host, held)
	}
	e.skew.Add(int64(25 * time.Hour))
	if host, held := e.a.ownCertificateDue(e.a.address()); host == "" || held {
		t.Fatalf("a day later: %q, held %v", host, held)
	}
}

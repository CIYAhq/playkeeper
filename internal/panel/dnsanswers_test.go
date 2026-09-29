package panel

import (
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/dnszone"
)

// zoneAgent answers the DNS zone routes of the dashboard's machine: it
// keeps the zone it was sent, and refuses one it couldn't hold, as the
// agent does.
type zoneAgent struct {
	mu   sync.Mutex
	zone dnszone.Zone
	puts []api.DNSZoneRequest
}

func (z *zoneAgent) seen() []api.DNSZoneRequest {
	z.mu.Lock()
	defer z.mu.Unlock()
	return slices.Clone(z.puts)
}

func (e *env) zoneLocally(z *zoneAgent) {
	e.agent.mu.Lock()
	defer e.agent.mu.Unlock()
	e.agent.answers["GET /v1/dns-zone"] = func(w http.ResponseWriter, _ *http.Request) {
		z.mu.Lock()
		defer z.mu.Unlock()
		writeJSON(w, http.StatusOK, api.DNSZoneStatus{Zone: z.zone, Listening: []string{"203.0.113.5:53"}})
	}
	e.agent.answers["PUT /v1/dns-zone"] = func(w http.ResponseWriter, _ *http.Request) {
		e.agent.mu.Lock()
		raw := e.agent.lastBody["PUT /v1/dns-zone"]
		e.agent.mu.Unlock()
		var req api.DNSZoneRequest
		json.Unmarshal([]byte(raw), &req)
		if req.Zone.Name != "" {
			if err := req.Zone.Check(); err != nil {
				writeErr(w, http.StatusBadRequest, api.CodeInvalid, err.Error(), "")
				return
			}
		}
		z.mu.Lock()
		defer z.mu.Unlock()
		z.puts, z.zone = append(z.puts, req), req.Zone
		writeJSON(w, http.StatusOK, api.DNSZoneStatus{Zone: z.zone, Listening: []string{"203.0.113.5:53"}})
	}
}

// addressIs has the dashboard's machine answer a with its address.
func (e *env) addressIs(t *testing.T, a api.Address) {
	t.Helper()
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	e.replyStatus("GET", "/v1/address", http.StatusOK, string(b))
}

// betaAddress is the machine's own domain beta.example.com with an address
// for each server: alex and survival have one, a server with its own
// address elsewhere hasn't, and neither has one whose label no DNS name
// has.
func betaAddress(extra ...api.JoinAddress) api.Address {
	return api.Address{Kind: api.AddressOwn, Host: "beta.example.com", IP: "203.0.113.5", PanelPort: 8443, ServerAddresses: true,
		Records: []api.DNSRecord{
			{Type: "A", Name: "beta.example.com", Value: "203.0.113.5", TTL: 300},
			{Type: "A", Name: "*.beta.example.com", Value: "203.0.113.5", TTL: 300},
			{ServerID: "ownzzzzzz3", Type: "A", Name: "play.other.example", Value: "203.0.113.5", TTL: 300},
			{ServerID: "ownzzzzzz3", Type: "SRV", Name: "_minecraft._tcp.play.other.example", Value: "0 5 25568 play.other.example.", TTL: 300,
				SRV: &api.SRVParts{Service: "_minecraft", Protocol: "_tcp", Host: "play.other.example", Weight: 5, Port: 25568, Target: "play.other.example"}},
		},
		Servers: append([]api.JoinAddress{
			{ServerID: "alexzzzzz1", Name: "Alex", Port: 25565, Label: "alex", Automatic: true, OwnAddress: "alex.beta.example.com"},
			{ServerID: "survzzzzz2", Name: "Survival", Port: 25567, Label: "survival", Automatic: true, OwnAddress: "survival.beta.example.com"},
			{ServerID: "ownzzzzzz3", Name: "Play", Port: 25568, OwnAddress: "play.other.example"},
			{ServerID: "badzzzzzz4", Name: "Bad", Port: 25569, Label: "Bad_Label", Automatic: true},
		}, extra...)}
}

// serverRecords are the records a server with an address under the zone
// gets.
func serverRecords(label string, port int) []dnszone.Record {
	return []dnszone.Record{
		{Name: label, Type: dnszone.TypeA, Value: "203.0.113.5"},
		{Name: "_minecraft._tcp." + label, Type: dnszone.TypeSRV, Value: "beta.example.com", Port: port},
	}
}

func betaZone(servers ...[]dnszone.Record) dnszone.Zone {
	z := dnszone.Zone{Name: "beta.example.com", Nameserver: "ns-beta.example.com", Records: []dnszone.Record{
		{Type: dnszone.TypeA, Value: "203.0.113.5"},
		{Name: "*", Type: dnszone.TypeA, Value: "203.0.113.5"},
	}}
	for _, s := range servers {
		z.Records = append(z.Records, s...)
	}
	return z
}

// The owner turns on the machine's answers for its own domain: the dashboard
// shows the two records to add at the domain's parent and the ones there
// the zone takes over, and sends the machine a zone with what the owner was
// asked for under the domain and an SRV record for each server with an
// address there. A label no DNS name has is left out rather than the
// machine refusing them all, and a server's address elsewhere stays out.
// The zone follows the servers, stays while the address can't be read or
// has no IP address, and goes once the address is no longer an own domain
// or the answers are off.
func TestTheDashboardAnswersDNSForItsOwnDomain(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	z := &zoneAgent{}
	e.zoneLocally(z)
	e.addressIs(t, betaAddress())

	answers := func() (int, api.DNSAnswers) {
		var v api.DNSAnswers
		code := e.get(t, "/api/dns-answers", cookie, &v)
		return code, v
	}
	if code, v := answers(); code != http.StatusOK || v.On || v.Zone != "beta.example.com" || v.Nameserver != "ns-beta.example.com" || v.Unavailable != "" || v.Answering != "" {
		t.Fatalf("before the switch: %d %+v", code, v)
	}
	_, v := answers()
	add := []api.DNSRecord{{Type: "A", Name: "ns-beta.example.com", Value: "203.0.113.5", TTL: 300}, {Type: "NS", Name: "beta.example.com", Value: "ns-beta.example.com", TTL: 3600}}
	if !slices.Equal(v.Add, add) || len(v.Remove) != 2 || v.Remove[0].Name != "beta.example.com" || v.Remove[1].Name != "*.beta.example.com" {
		t.Fatalf("the records at the parent: add %+v, remove %+v", v.Add, v.Remove)
	}
	if len(z.seen()) != 0 {
		t.Fatalf("the machine was sent a zone before the switch: %+v", z.seen())
	}

	r := e.do(t, "PUT", "/api/dns-answers", `{"on":true}`, auth(cookie, csrf))
	if r.status != http.StatusOK || r.body["on"] != true || r.body["answering"] != "beta.example.com" || r.body["servers"] != float64(2) {
		t.Fatalf("turning the answers on: %d %v", r.status, r.body)
	}
	want := betaZone(serverRecords("alex", 25565), serverRecords("survival", 25567))
	if p := z.seen(); len(p) != 1 || p[0].Actor != "admin" || !zonesEqual(p[0].Zone, want) {
		t.Fatalf("the machine was sent %+v, want %+v", p, want)
	}

	e.addressIs(t, betaAddress(api.JoinAddress{ServerID: "crezzzzzz5", Name: "Creative", Port: 25570, Label: "creative", Automatic: true}))
	if err := e.srv.syncDNSAnswers(t.Context(), "playkeeper"); err != nil {
		t.Fatal(err)
	}
	want = betaZone(serverRecords("alex", 25565), serverRecords("survival", 25567), serverRecords("creative", 25570))
	if p := z.seen(); len(p) != 2 || p[1].Actor != "playkeeper" || !zonesEqual(p[1].Zone, want) {
		t.Fatalf("after a server was made, the machine was sent %+v", p)
	}

	e.replyStatus("GET", "/v1/address", http.StatusBadGateway, `{"error":"no","code":"internal"}`)
	if err := e.srv.syncDNSAnswers(t.Context(), "playkeeper"); err == nil || len(z.seen()) != 2 {
		t.Fatalf("an address that can't be read: %v, %d zones sent", err, len(z.seen()))
	}
	noIP := betaAddress()
	noIP.IP, noIP.Records = "", nil
	e.addressIs(t, noIP)
	if err := e.srv.syncDNSAnswers(t.Context(), "playkeeper"); err != nil || len(z.seen()) != 2 {
		t.Fatalf("an address with no IP address: %v, %d zones sent", err, len(z.seen()))
	}
	if code, v := answers(); code != http.StatusOK || v.Unavailable != api.DNSUnavailableAddress || v.Answering != "beta.example.com" || len(v.Add) != 0 {
		t.Fatalf("with no IP address: %d %+v", code, v)
	}

	e.addressIs(t, api.Address{Kind: api.AddressPlaykeeper, Host: "alex.playkeeper.me", IP: "203.0.113.5"})
	if err := e.srv.syncDNSAnswers(t.Context(), "playkeeper"); err != nil {
		t.Fatal(err)
	}
	if p := z.seen(); len(p) != 3 || p[2].Zone.Name != "" {
		t.Fatalf("once the address is no longer an own domain, the machine was sent %+v", p)
	}
	if code, v := answers(); code != http.StatusOK || v.Unavailable != api.DNSUnavailableOwnDomain || v.Answering != "" {
		t.Fatalf("with a free address: %d %+v", code, v)
	}

	e.addressIs(t, betaAddress())
	if err := e.srv.syncDNSAnswers(t.Context(), "playkeeper"); err != nil || len(z.seen()) != 4 {
		t.Fatalf("the own domain back: %v, %d zones sent", err, len(z.seen()))
	}
	r = e.do(t, "PUT", "/api/dns-answers", `{"on":false}`, auth(cookie, csrf))
	if p := z.seen(); r.status != http.StatusOK || r.body["on"] != false || len(p) != 5 || p[4].Zone.Name != "" || p[4].Actor != "admin" {
		t.Fatalf("turning the answers off: %d %v, the machine was sent %+v", r.status, r.body, p)
	}
	if err := e.srv.syncDNSAnswers(t.Context(), "playkeeper"); err != nil || len(z.seen()) != 5 {
		t.Fatalf("off, with nothing answered: %v, %d zones sent", err, len(z.seen()))
	}

	apex := betaAddress()
	apex.Host = "example.com"
	e.addressIs(t, apex)
	if code, v := answers(); code != http.StatusOK || v.Unavailable != api.DNSUnavailableSubdomain || v.Zone != "example.com" || v.Nameserver != "" {
		t.Fatalf("a domain that isn't a name under another: %d %+v", code, v)
	}
	if r := e.do(t, "PUT", "/api/dns-answers", `{"on":"yes"}`, auth(cookie, csrf)); r.status != http.StatusBadRequest {
		t.Fatalf("a switch that isn't true or false: %d %v", r.status, r.body)
	}
}

// Only an admin of every server sees and sets the machine's DNS answers: a
// member, however many servers they see, gets neither.
func TestOnlyAnAdminOfEveryServerSetsTheDNSAnswers(t *testing.T) {
	e := newEnv(t)
	e.setup(t)
	z := &zoneAgent{}
	e.zoneLocally(z)
	e.addressIs(t, betaAddress())
	cookie, csrf := e.member(t, "sam")
	if r := e.do(t, "GET", "/api/dns-answers", "", auth(cookie, csrf)); r.status != http.StatusForbidden {
		t.Fatalf("a member reading the answers: %d %v", r.status, r.body)
	}
	if r := e.do(t, "PUT", "/api/dns-answers", `{"on":true}`, auth(cookie, csrf)); r.status != http.StatusForbidden {
		t.Fatalf("a member turning the answers on: %d %v", r.status, r.body)
	}
	if on, err := e.srv.dnsAnswersOn(t.Context()); err != nil || on || len(z.seen()) != 0 {
		t.Fatalf("after the member's try: on %v (%v), %d zones sent", on, err, len(z.seen()))
	}
}

func zonesEqual(a, b dnszone.Zone) bool {
	return a.Name == b.Name && a.Nameserver == b.Nameserver && slices.Equal(a.Records, b.Records)
}

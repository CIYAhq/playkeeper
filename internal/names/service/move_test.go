package service

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/names"
)

// newBase is the domain names move to in these tests, from testBase.
const newBase = "playkeeper.me"

// newZone is a fake Cloudflare for a zone of its own, as the owner sets it
// up for base (services/names/README.md, "Moving names to another domain"):
// a token and zone ID of its own, and only the two records that keep
// anyone from sending email as the domain.
func newZone(t *testing.T, base string) *fakeCloudflare {
	f := newFakeCloudflare(t)
	f.mu.Lock()
	f.token, f.zoneID = "Zq8vN3xB6mK1pW4rT7yC0dF2hJ5lS9gA-w_E3nMe", "7b4e1d8a2c5f9e0b3a6d1c4f7e2b5a8d"
	f.zone["id"], f.zone["name"], f.zone["status"] = f.zoneID, base, "active"
	f.records, f.protected = nil, nil
	f.mu.Unlock()
	f.addByHand("TXT", base, `"v=spf1 -all"`, "")
	f.addByHand("TXT", "_dmarc."+base, `"v=DMARC1; p=reject"`, "")
	return f
}

// useZone sets the service up for zone, the Cloudflare zone of base, as
// the owner does in Coolify, and makes base the one new installs use.
func (e *testEnv) useZone(zone *fakeCloudflare, base string) {
	e.cf, e.base = zone, base
	e.cfg.Base, e.cfg.CloudflareToken, e.cfg.CloudflareZone = base, zone.token, zone.zoneID
	e.cfg.HTTP, e.cfg.cloudflareAPI = zone.srv.Client(), zone.srv.URL+"/client/v4"
}

func (f *fakeCloudflare) all() []fakeRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.records)
}

// moveEnv is a service whose names live under testBase: alice with IPv4
// and IPv6, two server addresses, a challenge record and a certificate
// behind her; bob, whose install never updates; and cleo, released.
// Every dashboard answered the liveness check once.
type moveEnv struct {
	*testEnv
	old              *fakeCloudflare
	alice, bob, cleo *names.Client
	aliceWas, bobWas *nameRow
}

func newMoveEnv(t *testing.T) *moveEnv {
	t.Helper()
	e := &moveEnv{testEnv: newEnv(t)}
	e.old = e.cf
	ctx := context.Background()
	e.alice = e.claimed("alice", "alice", newMachine(aliceV4, aliceV6))
	e.bob = e.claimed("bob", "bob", newMachine("5.75.161.7", ""))
	e.cleo = e.claimed("cleo", "cleo", newMachine("5.75.161.8", ""))
	if _, err := e.cleo.Release(ctx); err != nil {
		t.Fatal(err)
	}
	e.grown()
	for label, port := range map[string]int{"": 25565, "survival": 25566} {
		if _, err := e.alice.SetServer(ctx, label, port); err != nil {
			t.Fatal(err)
		}
	}
	if err := certify(t, e.alice, "before the move"); err != nil {
		t.Fatal(err)
	}
	if err := e.alice.SetTXT(ctx, names.ChallengeFQDN("alice", testBase), acmeValue("left behind")); err != nil {
		t.Fatal(err)
	}
	if sets, _ := e.certSets("alice"); sets != 1 || e.count(`SELECT count(*) FROM challenges`) != 1 {
		t.Fatalf("before the move: %d certificate attempts, %d challenges", sets, e.count(`SELECT count(*) FROM challenges`))
	}
	e.aliceWas, e.bobWas = e.row("alice"), e.row("bob")
	if e.aliceWas.AliveAt == 0 || e.bobWas.AliveAt == 0 {
		t.Fatalf("the dashboards did not answer before the move: %+v %+v", e.aliceWas, e.bobWas)
	}
	return e
}

// move switches the service to a zone of its own for newBase and starts it
// again: steps C and E of the move.
func (e *moveEnv) move() *fakeCloudflare {
	e.t.Helper()
	zone := newZone(e.t, newBase)
	if err := e.restart(func(e *testEnv) { e.useZone(zone, newBase) }); err != nil {
		e.t.Fatalf("starting on %s: %v", newBase, err)
	}
	return zone
}

func (e *testEnv) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.svc.db.QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// settings reads the base domains the database remembers, from the file
// itself, so it works while the service is stopped.
func (e *testEnv) settings() (base, previous string) {
	e.t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(e.cfg.DataDir, "names.db")+"?mode=ro")
	if err != nil {
		e.t.Fatal(err)
	}
	defer db.Close()
	for key, v := range map[string]*string{settingBase: &base, settingPreviousBase: &previous} {
		if err := db.QueryRow(`SELECT coalesce((SELECT value FROM settings WHERE key = ?), '')`, key).Scan(v); err != nil {
			e.t.Fatal(err)
		}
	}
	return base, previous
}

func isUpdateRequired(err error) bool {
	var ne *names.Error
	return asError(err, &ne) && ne.Status == http.StatusGone && ne.Code == names.CodeUpdateRequired &&
		ne.Message == "Free addresses now end in ."+newBase+"." && ne.Hint == "Update Playkeeper to get one or keep yours." && ne.Params["base"] == newBase
}

func TestAMoveRepublishesTheActiveNamesInTheNewZoneAndNeverTouchesTheOldOne(t *testing.T) {
	e := newMoveEnv(t)
	oldRecords, oldRequests := e.old.all(), e.old.requestCount()
	if strings.Contains(e.log.String(), "base domain changed") {
		t.Fatal("a service that never moved logged a move")
	}
	zone := e.move()
	e.tick()

	fqdn := "alice." + newBase
	a, aaaa := zone.get("A", fqdn), zone.get("AAAA", fqdn)
	if len(a) != 1 || a[0].Content != aliceV4 || a[0].TTL != recordTTL || a[0].Proxied || a[0].comment() != "playkeeper-names alice" ||
		len(aaaa) != 1 || aaaa[0].Content != aliceV6 {
		t.Errorf("alice's address records in the new zone: %+v %+v", a, aaaa)
	}
	for rn, port := range map[string]int{"_minecraft._tcp." + fqdn: 25565, "_minecraft._tcp.survival." + fqdn: 25566} {
		if srv := zone.get("SRV", rn); len(srv) != 1 || *srv[0].Data != (cfSRV{Port: port, Target: fqdn}) {
			t.Errorf("%s in the new zone: %+v", rn, srv)
		}
	}
	if txt := zone.get("TXT", names.ChallengeFQDN("alice", newBase)); len(txt) != 0 {
		t.Errorf("a challenge from before the move was published in the new zone: %+v", txt)
	}
	if b := zone.get("A", "bob."+newBase); len(b) != 1 || b[0].Content != "5.75.161.7" {
		t.Errorf("bob's record in the new zone: %+v", b)
	}
	if c := zone.under("cleo." + newBase); len(c) != 0 {
		t.Errorf("a released name got records in the new zone: %+v", c)
	}
	zone.checkUntouched(t)

	if got := e.old.requestCount(); got != oldRequests {
		t.Errorf("the service sent %d requests to the old zone after the move", got-oldRequests)
	}
	if !reflect.DeepEqual(e.old.all(), oldRecords) {
		t.Errorf("the old zone changed after the move:\n was %+v\n now %+v", oldRecords, e.old.all())
	}
	e.old.checkUntouched(t)

	for name, was := range map[string]*nameRow{"alice": e.aliceWas, "bob": e.bobWas} {
		row := e.row(name)
		if row.Key != was.Key || row.State != names.StateActive || row.ClaimedAt != was.ClaimedAt || row.AliveAt != was.AliveAt ||
			row.IPv4 != was.IPv4 || row.IPv6 != was.IPv6 || row.Synced != row.Version || row.Version <= was.Version {
			t.Errorf("%s after the move:\n was %+v\n now %+v", name, was, row)
		}
	}
	if row := e.row("cleo"); row == nil || row.State != names.StateReleased {
		t.Errorf("cleo after the move: %+v", row)
	}
	if n := e.count(`SELECT count(*) FROM challenges`); n != 0 {
		t.Errorf("%d challenges survived the move", n)
	}
	if n := e.count(`SELECT count(*) FROM cert_sets`); n != 0 {
		t.Errorf("%d certificate attempts survived the move", n)
	}
	if base, previous := e.settings(); base != newBase || previous != testBase {
		t.Errorf("the database remembers base %q and previous %q", base, previous)
	}
	log := e.log.String()
	if strings.Count(log, "The base domain changed") != 1 || !strings.Contains(log, "from="+testBase+" to="+newBase+" names_to_publish=2") || strings.Contains(log, "level=ERROR") {
		t.Errorf("the move is not logged once, or with an error:\n%s", log)
	}

	if resp, b := e.get("/", ""); resp.StatusCode != http.StatusOK || !strings.Contains(string(b), "free yourname."+newBase+" addresses") {
		t.Errorf("index after the move: %q", b)
	}
	if av, err := e.install("erin", newMachine("5.75.162.2", "")).Available(context.Background(), "alice"); err != nil || av.Address != fqdn || av.Code != names.CodeNameTaken {
		t.Errorf("alice's availability after the move: %+v, %v", av, err)
	}
}

// An install from before the move signs for the old base. Every signed
// request is refused with update_required and changes nothing, and the
// unsigned ones answer as before.
func TestInstallsFromBeforeTheMoveAreToldToUpdate(t *testing.T) {
	e := newMoveEnv(t)
	ctx := context.Background()
	oldRequests := e.old.requestCount()
	e.move()
	e.tick()
	writes, bobWas := e.cf.writeLog(), e.row("bob")

	bob, fqdn, value := e.bob, names.ChallengeFQDN("bob", testBase), acmeValue("bob")
	for what, call := range map[string]func() error{
		"refreshing":                 func() error { _, err := bob.Refresh(ctx); return err },
		"claiming its name again":    func() error { _, err := bob.Claim(ctx, "bob"); return err },
		"listing its names":          func() error { _, err := bob.Names(ctx); return err },
		"giving a server an address": func() error { _, err := bob.SetServer(ctx, "", 25565); return err },
		"removing a server address":  func() error { return bob.RemoveServer(ctx, "") },
		"publishing a challenge":     func() error { return bob.SetTXT(ctx, fqdn, value) },
		"clearing a challenge":       func() error { return bob.ClearTXT(ctx, fqdn, value) },
		"releasing its name":         func() error { _, err := bob.Release(ctx); return err },
		"claiming its first name":    func() error { _, err := e.clientFor("frank", testBase).Claim(ctx, "frank"); return err },
	} {
		if err := call(); !isUpdateRequired(err) {
			t.Errorf("an install from before the move %s: got %#v", what, err)
		}
	}
	resp, body := e.send(e.signedRequest(http.MethodPost, "/v1/names/bob/address", nil, testKey("bob"), e.clk.Now(), "5.75.161.7"))
	if resp.StatusCode != http.StatusGone || body.Code != names.CodeUpdateRequired {
		t.Errorf("the answer on the wire: HTTP %d %+v", resp.StatusCode, body)
	}

	other, err := http.NewRequest(http.MethodPost, e.srv.URL+"/v1/names/bob/address", nil)
	if err != nil {
		t.Fatal(err)
	}
	names.SignRequest(other, testKey("bob"), "example.com", nil, e.clk.Now())
	other.Header.Set("X-Forwarded-For", "5.75.161.7")
	tampered := e.signedRequest(http.MethodPost, "/v1/names/bob/address", []byte(`{}`), testKey("bob"), e.clk.Now(), "5.75.161.7")
	tampered.Body = http.NoBody
	tampered.ContentLength = 0
	skewed := e.signedRequest(http.MethodPost, "/v1/names/bob/address", nil, testKey("bob"), e.clk.Now().Add(-time.Hour), "5.75.161.7")
	for _, tc := range []struct {
		name   string
		req    *http.Request
		status int
		code   string
	}{
		{"signed for a domain the service never had", other, http.StatusUnauthorized, names.CodeBadSignature},
		{"signed for the old base, then changed", tampered, http.StatusUnauthorized, names.CodeBadSignature},
		{"signed for the old base an hour ago", skewed, http.StatusUnauthorized, names.CodeClockSkew},
	} {
		if resp, body := e.send(tc.req); resp.StatusCode != tc.status || body.Code != tc.code {
			t.Errorf("%s: HTTP %d %+v, want %d %s", tc.name, resp.StatusCode, body, tc.status, tc.code)
		}
	}

	if row := e.row("bob"); !reflect.DeepEqual(row, bobWas) {
		t.Errorf("refused requests changed bob:\n was %+v\n now %+v", bobWas, row)
	}
	if got := e.cf.writeLog(); !slices.Equal(got, writes) {
		t.Errorf("refused requests changed the new zone: %v", got[len(writes):])
	}
	if e.old.requestCount() != oldRequests {
		t.Error("refused requests reached the old zone")
	}
	if av, err := bob.Available(ctx, "bobby"); err != nil || !av.Available || av.Address != "bobby."+newBase {
		t.Errorf("availability for an install from before the move: %+v, %v", av, err)
	}
	if info, err := bob.IP(ctx, names.AnyFamily); err != nil || info.IP != "5.75.161.7" {
		t.Errorf("/v1/ip for an install from before the move: %+v, %v", info, err)
	}
}

// clientFor is an install that has not claimed anything, for base.
func (e *testEnv) clientFor(seed, base string) *names.Client {
	c := e.install(seed, newMachine("5.75.164.9", ""))
	c.Base = base
	return c
}

// An install that updates keeps its name: the same key answers for it
// under the new base, with its servers, and its next certificate is a new
// one for Let's Encrypt.
func TestAnUpdatedInstallKeepsItsNameUnderTheNewBase(t *testing.T) {
	e := newMoveEnv(t)
	ctx := context.Background()
	oldRequests := e.old.requestCount()
	zone := e.move()
	e.update(e.alice)
	fqdn := "alice." + newBase

	n, err := e.alice.Refresh(ctx)
	if err != nil || n.Name != "alice" || n.Address != fqdn || n.State != names.StateActive || n.DNS != names.DNSOK ||
		n.IPv4 != aliceV4 || n.IPv6 != aliceV6 || !n.ClaimedAt.Equal(testStart) || !n.AnsweredAt.Equal(time.Unix(e.aliceWas.AliveAt, 0)) {
		t.Fatalf("the refresh right after the update: %+v, %v", n, err)
	}
	want := []names.Server{
		{Label: "", Address: fqdn, Port: 25565, DNS: names.DNSOK},
		{Label: "survival", Address: "survival." + fqdn, Port: 25566, DNS: names.DNSOK},
	}
	if !slices.Equal(n.Servers, want) {
		t.Errorf("server addresses after the update:\n got %+v\nwant %+v", n.Servers, want)
	}
	if len(zone.get("A", fqdn)) != 1 || len(zone.get("AAAA", fqdn)) != 1 || len(zone.get("SRV", "_minecraft._tcp.survival."+fqdn)) != 1 {
		t.Errorf("records after the update's refresh: %+v", zone.under(fqdn))
	}

	// An hour on, the attempt before the move would make this one a renewal.
	e.clk.Add(certSetWindow)
	logged := len(e.log.String())
	if err := certify(t, e.alice, "after the move"); err != nil {
		t.Fatalf("a certificate under the new base: %v", err)
	}
	if sets, first := e.certSets("alice"); sets != 1 || first != 1 {
		t.Errorf("the first certificate under the new base: %d attempts (%d new), want 1 (1)", sets, first)
	}
	if !strings.Contains(e.log.String()[logged:], "name=alice kind=new new_this_week=1") {
		t.Error("the first certificate under the new base does not count as new")
	}
	if !slices.Contains(zone.writeLog(), "create TXT "+names.ChallengeFQDN("alice", newBase)+` "`+acmeValue("alice after the move")+`"`) {
		t.Errorf("the challenge was not published in the new zone: %v", zone.writeLog())
	}

	e.clk.Add(checkEvery)
	e.tick()
	if row := e.row("alice"); row.AliveAt != e.clk.Now().Unix() {
		t.Errorf("the updated dashboard did not answer the liveness check for %s: %+v", fqdn, row)
	}
	if row := e.row("bob"); row.AliveAt != e.bobWas.AliveAt || row.FailedChecks != 1 {
		t.Errorf("a dashboard that answers for the old base only counted as answering: %+v", row)
	}
	if e.old.requestCount() != oldRequests {
		t.Error("the updated install's requests reached the old zone")
	}
}

// An install that does not update loses the records it never used in the
// new zone a week after its last answer, as any name that stops answering;
// its old record stays where it was. Its name is held for its key for 60
// days, and updating within them brings the name back.
func TestANameWhoseInstallDoesNotUpdateIsHeldUntilItDoes(t *testing.T) {
	e := newMoveEnv(t)
	ctx := context.Background()
	zone := e.move()
	e.update(e.alice)
	fqdn := "bob." + newBase
	answered := time.Unix(e.bobWas.AliveAt, 0)
	for e.clk.Now().Add(checkEvery).Before(answered.Add(unansweredAfter)) {
		e.clk.Add(checkEvery)
		e.tick()
		if row := e.row("bob"); row.State != names.StateActive {
			t.Fatalf("%s after bob's last answer: %s", e.clk.Now().Sub(answered), row.State)
		}
	}
	e.clk.Add(checkEvery)
	e.tick()
	row := e.row("bob")
	if row.State != names.StateLapsed || row.LapseReason != names.LapseNoAnswer {
		t.Fatalf("a week after the last answer for the old base: %s (%s)", row.State, row.LapseReason)
	}
	if a := zone.get("A", fqdn); len(a) != 0 {
		t.Errorf("the new record of an install that did not update stayed: %+v", a)
	}
	if a := e.old.get("A", "bob."+testBase); len(a) != 1 || a[0].Content != "5.75.161.7" {
		t.Errorf("the old record of an install that did not update: %+v", a)
	}
	if e.row("alice").State != names.StateActive {
		t.Error("the updated install's name lapsed too")
	}
	lapsed := e.clk.Now()
	if n, err := e.svc.info(ctx, row); err != nil || !n.FreedAt.Equal(lapsed.Add(freeAfter)) {
		t.Errorf("a name that answered before the move is held until %s, want %s (%v)", n.FreedAt, lapsed.Add(freeAfter), err)
	}
	if _, err := e.install("mallory", newMachine("5.75.165.1", "")).Claim(ctx, "bob"); codeOf(err) != names.CodeNameTaken {
		t.Errorf("another install claiming the held name: got %v, want %s", err, names.CodeNameTaken)
	}

	e.clk.Add(freeAfter - 24*time.Hour)
	e.tick()
	e.update(e.bob)
	n, err := e.bob.Refresh(ctx)
	if err != nil || n.State != names.StateActive || n.Address != fqdn || !n.AnsweredAt.Equal(e.clk.Now()) {
		t.Fatalf("updating a day before the name is freed: %+v, %v", n, err)
	}
	if a := zone.get("A", fqdn); len(a) != 1 || a[0].Content != "5.75.161.7" {
		t.Errorf("the record after updating: %+v", a)
	}
}

func TestAFreshClaimAfterTheMoveLivesInTheNewZone(t *testing.T) {
	e := newMoveEnv(t)
	ctx := context.Background()
	oldRequests := e.old.requestCount()
	zone := e.move()
	dave := e.install("dave", newMachine("5.75.162.20", "2a01:4f8:c012:7a00::20"))
	n, err := dave.Claim(ctx, "dave")
	if err != nil || n.Address != "dave."+newBase || n.State != names.StateActive || n.DNS != names.DNSOK {
		t.Fatalf("a claim after the move: %+v, %v", n, err)
	}
	dave.Name = "dave"
	if n, err = dave.Refresh(ctx); err != nil || n.IPv6 != "2a01:4f8:c012:7a00::20" {
		t.Fatalf("its refresh: %+v, %v", n, err)
	}
	if a, aaaa := zone.get("A", "dave."+newBase), zone.get("AAAA", "dave."+newBase); len(a) != 1 || len(aaaa) != 1 || a[0].comment() != "playkeeper-names dave" {
		t.Errorf("a new name's records: %+v %+v", a, aaaa)
	}
	if err := certify(t, dave, "first"); err != nil {
		t.Errorf("a new name's certificate: %v", err)
	}
	if e.old.requestCount() != oldRequests {
		t.Error("a claim after the move reached the old zone")
	}
	// Names keep their holders across the move.
	for name, code := range map[string]string{"alice": names.CodeNameTaken, "cleo": names.CodeNameHeld} {
		if _, err := e.install("eve-"+name, newMachine("5.75.166.1", "")).Claim(ctx, name); codeOf(err) != code {
			t.Errorf("claiming %s after the move: got %v, want %s", name, err, code)
		}
	}
	zone.checkUntouched(t)
}

// The move happens once, and only once the service runs with the new
// zone: a start that fails because the zone is not the new base's changes
// nothing.
func TestTheMoveHappensOnceAndOnlyWithTheNewZone(t *testing.T) {
	e := newMoveEnv(t)
	err := e.restart(func(e *testEnv) { e.cfg.Base, e.base = newBase, newBase })
	if err == nil || !strings.Contains(err.Error(), EnvZone) || !strings.Contains(err.Error(), EnvBase) {
		t.Fatalf("starting on %s with the old zone: %v", newBase, err)
	}
	if base, previous := e.settings(); base != testBase || previous != "" {
		t.Errorf("a start that failed changed the remembered base: %q, %q", base, previous)
	}
	if strings.Contains(e.log.String(), "base domain changed") {
		t.Error("a start that failed logged a move")
	}
	zone := e.move()
	if sets, _ := e.certSets("alice"); sets != 0 {
		t.Fatal("the move did not happen")
	}
	e.tick()
	aliceWas := e.row("alice")
	if err := e.restart(); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if row := e.row("alice"); row.Version != aliceWas.Version {
		t.Errorf("a second start on the new base marked the names again: version %d, then %d", aliceWas.Version, row.Version)
	}
	if n := strings.Count(e.log.String(), "The base domain changed"); n != 1 {
		t.Errorf("the move was logged %d times", n)
	}
	if _, err := e.bob.Refresh(context.Background()); !isUpdateRequired(err) {
		t.Errorf("an install from before the move after a second start: got %v", err)
	}
	if len(zone.get("A", "alice."+newBase)) != 1 {
		t.Error("alice's record went away")
	}
}

// A database from before the service remembered its base holds names of
// its first base. Its first start with this code can be the move itself,
// or come before it.
func TestNamesFromBeforeTheBaseWasRememberedMoveToo(t *testing.T) {
	forget := func(e *moveEnv) {
		e.t.Helper()
		if err := e.svc.Close(); err != nil {
			e.t.Fatal(err)
		}
		db, err := sql.Open("sqlite", "file:"+filepath.Join(e.cfg.DataDir, "names.db"))
		if err != nil {
			e.t.Fatal(err)
		}
		defer db.Close()
		for _, q := range []string{`DROP TABLE settings`, fmt.Sprintf(`PRAGMA user_version = %d`, len(migrations)-1)} {
			if _, err := db.Exec(q); err != nil {
				e.t.Fatal(err)
			}
		}
	}
	moved := func(e *moveEnv, zone *fakeCloudflare) {
		e.t.Helper()
		e.tick()
		if base, previous := e.settings(); base != newBase || previous != testBase {
			e.t.Errorf("the database remembers base %q and previous %q", base, previous)
		}
		if !strings.Contains(e.log.String(), "from="+testBase+" to="+newBase) || len(zone.get("A", "alice."+newBase)) != 1 {
			e.t.Errorf("the names did not move: %+v", zone.under("alice."+newBase))
		}
		if _, err := e.bob.Refresh(context.Background()); !isUpdateRequired(err) {
			e.t.Errorf("an install from before the move: got %v", err)
		}
	}

	t.Run("the first start is the move", func(t *testing.T) {
		e := newMoveEnv(t)
		forget(e)
		zone := newZone(t, newBase)
		e.useZone(zone, newBase)
		svc, err := New(context.Background(), e.cfg)
		if err != nil {
			t.Fatal(err)
		}
		e.serve(svc)
		moved(e, zone)
	})
	t.Run("the first start comes before the move", func(t *testing.T) {
		e := newMoveEnv(t)
		forget(e)
		svc, err := New(context.Background(), e.cfg)
		if err != nil {
			t.Fatal(err)
		}
		e.serve(svc)
		if base, previous := e.settings(); base != testBase || previous != "" || strings.Contains(e.log.String(), "base domain changed") {
			t.Fatalf("a start on the same base: %q, %q", base, previous)
		}
		if _, err := e.bob.Refresh(context.Background()); err != nil {
			t.Fatalf("an install on the same base: %v", err)
		}
		moved(e, e.move())
	})
}

func TestANewServiceHasNoPreviousBase(t *testing.T) {
	e := newEnv(t, func(e *testEnv) { e.useZone(newZone(t, newBase), newBase) })
	if base, previous := e.settings(); base != newBase || previous != "" {
		t.Errorf("a new database remembers base %q and previous %q", base, previous)
	}
	c := e.claimed("alice", "alice", newMachine(aliceV4, ""))
	if n := nameInfo(t, c, "alice"); n.Address != "alice."+newBase {
		t.Errorf("a claim: %+v", n)
	}
	resp, body := e.send(e.signedRequest(http.MethodPost, "/v1/names/alice/address", nil, testKey("alice"), e.clk.Now(), aliceV4))
	if resp.StatusCode != http.StatusUnauthorized || body.Code != names.CodeBadSignature {
		t.Errorf("a request signed for %s to a service that never had it: HTTP %d %+v", testBase, resp.StatusCode, body)
	}
}

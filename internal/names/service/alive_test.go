package service

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/names"
)

func nameInfo(t *testing.T, c *names.Client, name string) names.Name {
	t.Helper()
	list, err := c.Names(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range list {
		if n.Name == name {
			return n
		}
	}
	t.Fatalf("%s is not in the install's names: %+v", name, list)
	return names.Name{}
}

func TestANameWhoseAddressStopsAnsweringLapsesAfterAWeekAndComesBackOnceItAnswers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.claimed("alice", "alice", newMachine(aliceV4, ""))
	e.claimed("bob", "bob", newMachine("5.75.161.7", ""))
	e.grown()
	answered := e.clk.Now()
	if _, err := c.SetServer(ctx, "", 25565); err != nil {
		t.Fatal(err)
	}
	if n := nameInfo(t, c, "alice"); !n.AnsweredAt.Equal(answered) || !n.AnswerBy.Equal(answered.Add(unansweredAfter)) {
		t.Errorf("an answering name: answered %s, answer by %s", n.AnsweredAt, n.AnswerBy)
	}

	e.panelOf(c).setDown(true)
	for e.clk.Now().Add(checkEvery).Before(answered.Add(unansweredAfter)) {
		e.clk.Add(checkEvery)
		e.tick()
		if row := e.row("alice"); row.State != names.StateActive {
			t.Fatalf("%s after the last answer: %s", e.clk.Now().Sub(answered), row.State)
		}
		if _, err := c.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
	}
	e.clk.Add(checkEvery)
	e.tick()
	if row := e.row("alice"); row.State != names.StateLapsed || row.LapseReason != names.LapseNoAnswer {
		t.Fatalf("a week without an answer: %s (%s)", row.State, row.LapseReason)
	}
	if len(e.cf.get("A", aliceFQD)) != 0 || len(e.cf.get("SRV", "_minecraft._tcp."+aliceFQD)) != 0 {
		t.Error("the records of a name that stopped answering stayed")
	}
	if e.row("bob").State != names.StateActive {
		t.Error("a name that answers lapsed too")
	}
	lapsed := e.clk.Now()
	if n := nameInfo(t, c, "alice"); n.LapseReason != names.LapseNoAnswer || !n.AnsweredAt.Equal(answered) || !n.FreedAt.Equal(lapsed.Add(freeAfter)) {
		t.Errorf("a lapsed name that answered before: reason %q, answered %s, freed %s", n.LapseReason, n.AnsweredAt, n.FreedAt)
	}
	var ne *names.Error
	if _, err := c.SetServer(ctx, "b", 25566); codeOf(err) != names.CodeNameLapsed || !asError(err, &ne) || ne.Params["reason"] != names.LapseNoAnswer || !strings.Contains(ne.Message, "port 8443") {
		t.Errorf("a server address for a name that stopped answering: got %#v", err)
	}

	_, err := c.Refresh(ctx)
	if codeOf(err) != names.CodeNotAnswering || !asError(err, &ne) || ne.Params["ip"] != aliceV4 || !strings.Contains(ne.Hint, "8443") {
		t.Errorf("refreshing while the dashboard still does not answer: got %#v", err)
	}
	if _, err := c.Claim(ctx, "alice"); codeOf(err) != names.CodeNotAnswering {
		t.Errorf("claiming again while the dashboard still does not answer: got %v, want %s", err, names.CodeNotAnswering)
	}
	if row := e.row("alice"); row.State != names.StateLapsed {
		t.Fatalf("a name that does not answer came back: %s", row.State)
	}

	e.panelOf(c).setDown(false)
	n, err := c.Refresh(ctx)
	if err != nil || n.State != names.StateActive || !n.AnsweredAt.Equal(e.clk.Now()) || n.LapseReason != "" {
		t.Fatalf("refreshing once the dashboard answers: %+v, %v", n, err)
	}
	if len(e.cf.get("A", aliceFQD)) != 1 || len(e.cf.get("SRV", "_minecraft._tcp."+aliceFQD)) != 1 {
		t.Error("the records did not come back")
	}
	e.cf.checkUntouched(t)
}

func TestANameThatNeverAnsweredIsFreedAWeekAfterItLapses(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.claimed("bob", "bob", newMachine("5.75.161.7", ""))
	ghost := e.install("ghost", newMachine("5.75.162.8", ""))
	e.panelOf(ghost).setDown(true)
	if _, err := ghost.Claim(ctx, "ghost"); err != nil {
		t.Fatal(err)
	}
	claimed := e.clk.Now()
	for e.clk.Now().Before(claimed.Add(unansweredAfter)) {
		e.clk.Add(checkEvery)
		e.tick()
	}
	row := e.row("ghost")
	if row.State != names.StateLapsed || row.LapseReason != names.LapseNoAnswer || row.AliveAt != 0 {
		t.Fatalf("a name that never answered, a week after its claim: %+v", row)
	}
	lapsed := e.clk.Now()
	if n := nameInfo(t, ghost, "ghost"); !n.FreedAt.Equal(lapsed.Add(freeSilentAfter)) || !n.AnsweredAt.IsZero() {
		t.Errorf("a lapsed name that never answered: freed %s, answered %s", n.FreedAt, n.AnsweredAt)
	}
	e.clk.Add(freeSilentAfter - time.Second)
	e.tick()
	if e.row("ghost") == nil {
		t.Fatal("freed early")
	}
	e.clk.Add(time.Second)
	e.tick()
	if e.row("ghost") != nil {
		t.Fatal("a name that never answered is not freed a week after it lapsed")
	}
	if _, err := e.install("other", newMachine("5.75.163.9", "")).Claim(ctx, "ghost"); err != nil {
		t.Errorf("claiming the freed name: %v", err)
	}
}

func TestNamesDoNotLapseForNotAnsweringWhileNoAddressAnswers(t *testing.T) {
	e := newEnv(t)
	a := e.claimed("alice", "alice", newMachine(aliceV4, ""))
	b := e.claimed("bob", "bob", newMachine("5.75.161.7", ""))
	e.tick()
	e.panelOf(a).setDown(true)
	e.panelOf(b).setDown(true)
	for range 8 * 4 {
		e.clk.Add(checkEvery)
		e.tick()
	}
	if e.row("alice").State != names.StateActive || e.row("bob").State != names.StateActive {
		t.Fatal("names lapsed for not answering while no address answered at all")
	}
	if !strings.Contains(e.log.String(), "alert="+alertChecks) {
		t.Error("the owner is not alerted that no address answers")
	}
	e.panelOf(b).setDown(false)
	e.clk.Add(checkEvery)
	e.tick()
	if row := e.row("alice"); row.State != names.StateLapsed || row.LapseReason != names.LapseNoAnswer {
		t.Errorf("once an address answers again, the name that did not: %s (%s)", row.State, row.LapseReason)
	}
	if e.row("bob").State != names.StateActive {
		t.Error("the name that answers again lapsed")
	}
}

func TestLivenessChecksAreRateLimited(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	c := e.claimed("alice", "alice", newMachine(aliceV4, ""))
	for i, step := range []time.Duration{0, 0, checkEvery - time.Second, time.Second} {
		e.clk.Add(step)
		e.tick()
		if want := []int32{1, 1, 1, 2}[i]; e.dials.Load() != want {
			t.Errorf("tick %d: %d checks of one name, want %d", i+1, e.dials.Load(), want)
		}
	}

	e = newEnv(t)
	shared := newMachine("5.75.170.1", "")
	for i := range 3 {
		if _, err := e.install(fmt.Sprintf("shared-%d", i), shared).Claim(ctx, fmt.Sprintf("shared-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range []int32{1, 2, 3, 3} {
		e.tick()
		if e.dials.Load() != want {
			t.Errorf("tick %d: %d checks of names at one address, want %d", i+1, e.dials.Load(), want)
		}
	}

	e = newEnv(t)
	for i := range checksPerTick + 5 {
		if _, err := e.install(fmt.Sprintf("many-%d", i), newMachine(fmt.Sprintf("5.75.%d.1", 100+i), "")).Claim(ctx, fmt.Sprintf("many-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	e.tick()
	if e.dials.Load() != checksPerTick {
		t.Errorf("one tick checked %d names, want %d", e.dials.Load(), checksPerTick)
	}
	e.tick()
	if e.dials.Load() != checksPerTick+5 {
		t.Errorf("two ticks checked %d names, want %d", e.dials.Load(), checksPerTick+5)
	}

	e = newEnv(t)
	for i := range checksPerTick + 1 {
		name := fmt.Sprintf("crowd-%02d", i)
		if _, err := e.install(name, newMachine(fmt.Sprintf("5.75.%d.1", 100+i), "")).Claim(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.db.Exec(`UPDATE names SET ipv4 = '5.75.99.1' WHERE name LIKE 'crowd-%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.install("loner", newMachine("5.75.98.1", "")).Claim(ctx, "loner"); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if e.dials.Load() != 2 || e.row("loner").CheckedAt == 0 {
		t.Errorf("names crowding one address held up the others: %d checks, loner checked at %d", e.dials.Load(), e.row("loner").CheckedAt)
	}

	e = newEnv(t)
	c = e.claimed("alice", "alice", newMachine(aliceV4, ""))
	e.panelOf(c).setDown(true)
	if _, err := e.svc.db.Exec(`UPDATE names SET state = ?, lapse_reason = ? WHERE name = 'alice'`, names.StateLapsed, names.LapseNoAnswer); err != nil {
		t.Fatal(err)
	}
	for i := range rechecksBurst {
		if _, err := c.Refresh(ctx); codeOf(err) != names.CodeNotAnswering {
			t.Fatalf("refresh %d of a name that does not answer: got %v", i+1, err)
		}
	}
	if _, err := c.Refresh(ctx); codeOf(err) != names.CodeRateLimited || !strings.Contains(err.Error(), "checks of this name's address") {
		t.Errorf("refresh %d: got %v, want %s", rechecksBurst+1, err, names.CodeRateLimited)
	}
	if e.dials.Load() != rechecksBurst {
		t.Errorf("%d checks for %d refreshes, want %d", e.dials.Load(), rechecksBurst+1, rechecksBurst)
	}
	e.clk.Add(time.Hour / rechecksPerHour)
	if _, err := c.Refresh(ctx); codeOf(err) != names.CodeNotAnswering {
		t.Errorf("a refresh once the limit allows another check: got %v", err)
	}
}

var reAliveURL = regexp.MustCompile(`^/\.well-known/playkeeper-names/[A-Za-z0-9_-]{43}$`)

func TestLivenessAnswersMustBeFreshSignedAndSmall(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.claimed("alice", "alice", newMachine(aliceV4, ""))
	p := e.panelOf(c)
	key := e.row("alice").Key
	addr := netip.MustParseAddr(aliceV4)
	aliceKey := testKey("alice")

	genuine := http.NewServeMux()
	genuine.Handle(names.AlivePattern, names.AliveHandler(testBase, func(string) ed25519.PrivateKey { return aliceKey }))
	var mu sync.Mutex
	var nonces []string
	p.serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !reAliveURL.MatchString(r.URL.Path) || r.Host != aliceFQD+":8443" || r.TLS == nil || r.TLS.ServerName != aliceFQD {
			t.Errorf("the check asked %s %s on %s", r.Method, r.URL.Path, r.Host)
		}
		mu.Lock()
		nonces = append(nonces, strings.TrimPrefix(r.URL.Path, names.AlivePath))
		mu.Unlock()
		genuine.ServeHTTP(w, r)
	}))
	for range 2 {
		if err := e.svc.askAlive(ctx, "alice", key, addr); err != nil {
			t.Fatalf("the real handler: %v", err)
		}
	}
	if len(nonces) != 2 || nonces[0] == nonces[1] {
		t.Errorf("nonces of two checks: %q", nonces)
	}

	answer := func(k ed25519.PrivateKey, base, name string, pad int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			nonce := strings.TrimPrefix(r.URL.Path, names.AlivePath)
			b, _ := json.Marshal(names.Alive{Name: name, Signature: names.SignAlive(k, base, name, nonce)})
			w.Header().Set("Content-Type", "application/json")
			w.Write(append(b, strings.Repeat(" ", pad)...))
		}
	}
	stale := names.SignAlive(aliceKey, testBase, "alice", strings.Repeat("A", 43))
	for _, tc := range []struct {
		name string
		h    http.Handler
		ok   bool
	}{
		{"a correct answer", answer(aliceKey, testBase, "alice", 0), true},
		{"a correct answer with room to spare", answer(aliceKey, testBase, "alice", maxAliveAnswer-200), true},
		{"another install's key", answer(testKey("mallory"), testBase, "alice", 0), false},
		{"another name", answer(aliceKey, testBase, "bob", 0), false},
		{"another base domain", answer(aliceKey, "example.com", "alice", 0), false},
		{"an answer to another nonce", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(names.Alive{Name: "alice", Signature: stale})
		}), false},
		{"HTTP 500", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}), false},
		{"not JSON", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }), false},
		{"a redirect to a correct answer", func() http.Handler {
			mux := http.NewServeMux()
			mux.HandleFunc("/.well-known/", func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/elsewhere/"+strings.TrimPrefix(r.URL.Path, names.AlivePath), http.StatusTemporaryRedirect)
			})
			mux.HandleFunc("/elsewhere/", func(w http.ResponseWriter, r *http.Request) {
				nonce := strings.TrimPrefix(r.URL.Path, "/elsewhere/")
				json.NewEncoder(w).Encode(names.Alive{Name: "alice", Signature: names.SignAlive(aliceKey, testBase, "alice", nonce)})
			})
			return mux
		}(), false},
		{"a correct answer over 1 KiB", answer(aliceKey, testBase, "alice", maxAliveAnswer), false},
		{"a correct answer over 1 KiB, streamed", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			answer(aliceKey, testBase, "alice", maxAliveAnswer)(w, r)
		}), false},
	} {
		p.serve(tc.h)
		if err := e.svc.askAlive(ctx, "alice", key, addr); (err == nil) != tc.ok {
			t.Errorf("%s: %v", tc.name, err)
		}
	}

	p.serve(nil)
	before := e.dials.Load()
	for _, a := range []string{"10.0.0.5", "127.0.0.1", "104.16.0.1", "2606:4700::1"} {
		if err := e.svc.askAlive(ctx, "alice", key, netip.MustParseAddr(a)); err == nil {
			t.Errorf("a check of %s passed", a)
		}
	}
	if e.dials.Load() != before {
		t.Error("the service connected to an address that is not public")
	}
}

func TestServerAddressesWaitForTheFirstAnswer(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.install("alice", newMachine(aliceV4, ""))
	e.panelOf(c).setDown(true)
	if _, err := c.Claim(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	c.Name = "alice"
	e.grown()
	var ne *names.Error
	if _, err := c.SetServer(ctx, "", 25566); codeOf(err) != names.CodeNotAnswering || !asError(err, &ne) || ne.Params["port"] != float64(names.AlivePort) || !strings.Contains(ne.Hint, aliceFQD+":25566") {
		t.Errorf("a server address before the first answer: got %#v", err)
	}
	e.panelOf(c).setDown(false)
	e.clk.Add(checkEvery)
	e.tick()
	if _, err := c.SetServer(ctx, "", 25566); err != nil {
		t.Errorf("a server address once the dashboard answered: %v", err)
	}
}

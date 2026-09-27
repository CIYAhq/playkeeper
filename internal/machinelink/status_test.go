package machinelink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// addMachine puts a machine that joined at joined straight into the store,
// the way the panel's database would hold it.
func (th *testHub) addMachine(t *testing.T, name string, joined time.Time) Machine {
	t.Helper()
	ctx := context.Background()
	jc := JoinCode{ID: newID(), Hash: []byte("unused"), CreatedAt: joined, ExpiresAt: joined.Add(CodeTTL), CreatedBy: "alice"}
	if err := th.store.AddJoinCode(ctx, jc); err != nil {
		t.Fatal(err)
	}
	m := Machine{ID: newID(), Name: name, PublicKey: mustIdentity(t).PublicKey(), JoinedAt: joined,
		JoinedFrom: "203.0.113.7", CreatedBy: "alice", Version: "0.4.0"}
	if err := th.store.Pair(ctx, jc.ID, m); err != nil {
		t.Fatal(err)
	}
	return m
}

func (th *testHub) status(t *testing.T, id string) Status {
	t.Helper()
	st, err := th.MachineStatus(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// pinged waits for the hub's first heartbeat to the machine.
func (th *testHub) pinged(t *testing.T, id string) Status {
	t.Helper()
	var st Status
	eventually(t, "the first heartbeat", func() bool {
		st = th.status(t, id)
		return st.RTT > 0
	})
	return st
}

func problemCodes(st Status) []string {
	var out []string
	for _, p := range st.Problems {
		out = append(out, p.Code)
	}
	return out
}

func problemWith(t *testing.T, st Status, code string) Problem {
	t.Helper()
	for _, p := range st.Problems {
		if p.Code == code {
			return p
		}
	}
	t.Fatalf("%s has problems %v, want one with code %s", st.Name, problemCodes(st), code)
	return Problem{}
}

func TestStatusOffline(t *testing.T) {
	th := newTestHub(t, nil)
	ctx := context.Background()
	now := th.clock.Now()
	m := th.addMachine(t, "home-server", now.Add(-24*time.Hour))
	if err := th.store.Seen(ctx, m.ID, now.Add(-10*time.Minute), "0.4.1", "198.51.100.4"); err != nil {
		t.Fatal(err)
	}

	st := th.status(t, m.ID)
	if st.State != StateOffline || st.MachineID != m.ID || st.Name != "home-server" || st.Fingerprint != m.Fingerprint() {
		t.Errorf("status = %+v", st)
	}
	if !st.LastSeen.Equal(now.Add(-10*time.Minute)) || st.Address != "198.51.100.4" || st.Version != "0.4.1" || st.RTT != 0 || !st.ConnectedAt.IsZero() {
		t.Errorf("status = %+v, want what the store remembers", st)
	}
	if got := problemCodes(st); !slices.Equal(got, []string{ProblemOffline}) {
		t.Fatalf("problems = %v", got)
	}
	p := st.Problems[0]
	if p.Message != "home-server hasn't called in for 10 minutes." {
		t.Errorf("message = %q", p.Message)
	}
	if p.Params["name"] != "home-server" || p.Params["minutes"] != "10" || p.Params["duration"] != "10 minutes" || p.Params["since"] != "2026-09-25T11:50:00Z" {
		t.Errorf("params = %v", p.Params)
	}
	if !strings.Contains(p.Hint, "sudo playkeeper status") {
		t.Errorf("hint = %q", p.Hint)
	}

	if err := th.store.Seen(ctx, m.ID, now.Add(-30*time.Second), "", ""); err != nil {
		t.Fatal(err)
	}
	if st := th.status(t, m.ID); st.State != StateOffline || len(st.Problems) != 0 {
		t.Errorf("away for 30 seconds: state %s, problems %v; want offline without a problem yet", st.State, problemCodes(st))
	}
}

func TestStatusWaiting(t *testing.T) {
	th := newTestHub(t, nil)
	now := th.clock.Now()
	m := th.addMachine(t, "home-server", now.Add(-5*time.Minute))

	st := th.status(t, m.ID)
	if st.State != StateWaiting || !st.LastSeen.IsZero() {
		t.Errorf("state %s, last seen %v; want waiting, never seen", st.State, st.LastSeen)
	}
	if got := problemCodes(st); !slices.Equal(got, []string{ProblemNeverConnected}) {
		t.Fatalf("problems = %v", got)
	}
	if p := st.Problems[0]; p.Message != "home-server joined 5 minutes ago but hasn't connected since." || p.Params["duration"] != "5 minutes" || p.Hint == "" {
		t.Errorf("problem = %+v", p)
	}

	fresh := th.addMachine(t, "cabin-pc", now.Add(-time.Minute))
	if st := th.status(t, fresh.ID); st.State != StateWaiting || len(st.Problems) != 0 {
		t.Errorf("joined a minute ago: state %s, problems %v; want waiting without a problem yet", st.State, problemCodes(st))
	}
}

func TestStatusRemoved(t *testing.T) {
	th := newTestHub(t, nil)
	ctx := context.Background()
	now := th.clock.Now()
	gone := th.addMachine(t, "home-server", now.Add(-time.Hour))
	if err := th.store.Seen(ctx, gone.ID, now.Add(-30*time.Minute), "", ""); err != nil {
		t.Fatal(err)
	}
	if err := th.Remove(ctx, gone.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	// A removal the store failed to save still holds while the dashboard runs.
	unsaved := th.addMachine(t, "cabin-pc", now.Add(-time.Hour))
	th.mu.Lock()
	th.revoked[unsaved.ID] = true
	th.mu.Unlock()
	th.addMachine(t, "attic", now.Add(-time.Minute))

	all, err := th.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, st := range all {
		got = append(got, st.Name+" "+string(st.State))
		if st.Problems == nil || (st.State == StateRemoved && len(st.Problems) != 0) {
			t.Errorf("%s: problems %#v, want an empty list", st.Name, st.Problems)
		}
	}
	if want := []string{"home-server removed", "cabin-pc removed", "attic waiting"}; !slices.Equal(got, want) {
		t.Errorf("statuses = %q, want %q", got, want)
	}

	if _, err := th.MachineStatus(ctx, "nosuchmachine"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown machine: %v, want ErrNotFound", err)
	}
}

func TestStatusOfAConnectedMachine(t *testing.T) {
	th := startHub(t, nil)
	m := th.linkMachine(t, "home-server", func(o *LinkOptions) { o.Now = th.clock.Now })
	id := m.d.MachineID

	st := th.pinged(t, id)
	if st.State != StateConnected || st.ConnectedAt.IsZero() || st.LastSeen.IsZero() || st.Version != "0.4.0" || st.Address != "127.0.0.1" {
		t.Errorf("status = %+v", st)
	}
	if len(st.Problems) != 0 {
		t.Errorf("problems = %+v", st.Problems)
	}
	ls := m.link.Status()
	if ls.State != LinkConnected || ls.MachineID != id || ls.Name != "home-server" || ls.ConnectedAt.IsZero() || ls.LastSeen.IsZero() || ls.Problem != nil {
		t.Errorf("link status = %+v", ls)
	}

	th.Close()
	eventually(t, "the machine notices the dashboard is gone", func() bool {
		ls = m.link.Status()
		return ls.State == LinkRetrying && ls.Problem != nil && ls.Problem.Code == CodeDashboardUnreachable
	})
	if ls.NextAttempt.IsZero() || !ls.ConnectedAt.IsZero() || ls.LastSeen.IsZero() || ls.Problem.Hint == "" {
		t.Errorf("link status = %+v", ls)
	}

	eventually(t, "the hub lets the machine go", func() bool { return th.status(t, id).State == StateOffline })
	if st := th.status(t, id); len(st.Problems) != 0 || st.LastSeen.IsZero() {
		t.Errorf("just gone: problems %v, last seen %v; want no problem yet", problemCodes(st), st.LastSeen)
	}
	th.clock.Add(10 * time.Minute)
	st = th.status(t, id)
	if got := problemCodes(st); !slices.Equal(got, []string{ProblemOffline}) || st.Problems[0].Message != "home-server hasn't called in for 10 minutes." {
		t.Errorf("problems = %+v", st.Problems)
	}
}

func TestStatusProblemsWhileConnected(t *testing.T) {
	// One heartbeat per machine, so what it measured stays put.
	th := startHub(t, func(o *HubOptions) { o.Heartbeat = time.Hour })
	ahead := th.linkMachine(t, "home-server", func(o *LinkOptions) {
		o.Version = "0.3.1"
		o.Now = func() time.Time { return th.clock.Now().Add(3 * time.Minute) }
	})
	behind := th.linkMachine(t, "cabin-pc", func(o *LinkOptions) {
		o.Version = "0.5.0"
		o.Now = func() time.Time { return th.clock.Now().Add(-5*time.Minute - 30*time.Second) }
	})

	st := th.pinged(t, ahead.d.MachineID)
	if got := problemCodes(st); !slices.Equal(got, []string{ProblemClockSkew, ProblemVersion}) {
		t.Fatalf("problems = %v", got)
	}
	if p := st.Problems[0]; p.Message != "home-server's clock is 3 minutes ahead of the dashboard's." || p.Params["direction"] != "ahead" || !strings.Contains(p.Hint, "timedatectl set-ntp true") {
		t.Errorf("clock problem = %+v", p)
	}
	if p := st.Problems[1]; p.Message != "home-server runs Playkeeper 0.3.1, and the dashboard runs 0.4.0." ||
		p.Params["older"] != "machine" || p.Params["version"] != "0.3.1" || !strings.Contains(p.Hint, "sudo playkeeper self-update") {
		t.Errorf("version problem = %+v", p)
	}
	if st.Version != "0.3.1" {
		t.Errorf("version = %q, want the one the machine runs now", st.Version)
	}

	st = th.pinged(t, behind.d.MachineID)
	if got := problemCodes(st); !slices.Equal(got, []string{ProblemClockSkew, ProblemVersion}) {
		t.Fatalf("problems = %v", got)
	}
	if p := st.Problems[0]; p.Message != "cabin-pc's clock is 5 minutes behind the dashboard's." || p.Params["direction"] != "behind" {
		t.Errorf("clock problem = %+v", p)
	}
	if p := st.Problems[1]; p.Params["older"] != "dashboard" || p.Hint != "Update Playkeeper on the dashboard's machine." {
		t.Errorf("version problem = %+v", p)
	}

	s := th.session(ahead.d.MachineID)
	s.mu.Lock()
	s.rtt = 1500 * time.Millisecond
	s.mu.Unlock()
	st = th.status(t, ahead.d.MachineID)
	if got := problemCodes(st); !slices.Equal(got, []string{ProblemSlow, ProblemClockSkew, ProblemVersion}) {
		t.Fatalf("problems = %v", got)
	}
	if p := st.Problems[0]; p.Message != "The connection to home-server is slow: a round trip takes 1.5 seconds." || p.Params["rttMs"] != "1500" {
		t.Errorf("slow problem = %+v", p)
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(b), `"rttMs":1500}`) {
		t.Errorf("JSON = %s, want rttMs 1500", b)
	}
}

func TestStatusUnstableLink(t *testing.T) {
	th := startHub(t, nil)
	p := startProxy(t, th.addr)
	d, id := th.join(t, p.addr(), "home-server")
	startLink(t, d, id, newFakeAgent("home-server"), func(o *LinkOptions) { o.Now = th.clock.Now })
	for i := 1; ; i++ {
		eventually(t, fmt.Sprintf("connection %d", i), func() bool { return th.events.count(EventConnected) == i })
		if i == unstableConnects {
			break
		}
		p.cut()
	}

	var st Status
	eventually(t, "the machine is back", func() bool {
		st = th.status(t, d.MachineID)
		return st.State == StateConnected
	})
	if got := problemCodes(st); !slices.Equal(got, []string{ProblemUnstable}) {
		t.Fatalf("problems = %v", got)
	}
	if pr := st.Problems[0]; pr.Message != "The connection to home-server keeps dropping: it connected 5 times in the last 10 minutes." || pr.Params["count"] != "5" {
		t.Errorf("problem = %+v", pr)
	}

	th.clock.Add(recentWindow + time.Minute)
	if st := th.status(t, d.MachineID); st.State != StateConnected || len(st.Problems) != 0 {
		t.Errorf("after 11 quiet minutes: state %s, problems %v", st.State, problemCodes(st))
	}
}

func TestStatusClonedMachine(t *testing.T) {
	th := startHub(t, nil)
	d, id := th.join(t, th.addr, "home-server")
	sameClock := func(o *LinkOptions) { o.Now = th.clock.Now }
	startLink(t, d, id, newFakeAgent("first"), sameClock)
	startLink(t, d, id, newFakeAgent("copy"), sameClock)

	var st Status
	eventually(t, "the hub sees two computers taking turns", func() bool {
		st = th.status(t, d.MachineID)
		return slices.Contains(problemCodes(st), ProblemCloned)
	})
	if got := problemCodes(st); !slices.Equal(got, []string{ProblemCloned}) {
		t.Errorf("problems = %v, want only the clone problem", got)
	}
	p := problemWith(t, st, ProblemCloned)
	if p.Message != "Two computers seem to be taking turns connecting as home-server." || !strings.Contains(p.Hint, "Remove home-server here") {
		t.Errorf("problem = %+v", p)
	}
}

func TestStatusJSON(t *testing.T) {
	st := Status{MachineID: "abcdefghij", Name: "home-server", State: StateWaiting, Problems: []Problem{}}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"machineId":"abcdefghij"`, `"state":"waiting"`, `"problems":[]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON = %s, want %s", b, want)
		}
	}
	for _, unwanted := range []string{"rttMs", "RTT", "connectedAt", "lastSeen", "version", "address"} {
		if strings.Contains(string(b), unwanted) {
			t.Errorf("JSON = %s, want no %s", b, unwanted)
		}
	}

	st.RTT = 12345 * time.Microsecond
	st.LastSeen = time.Date(2026, 9, 25, 11, 50, 0, 0, time.UTC)
	b, err = json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"rttMs":12.3`, `"lastSeen":"2026-09-25T11:50:00Z"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON = %s, want %s", b, want)
		}
	}
}

func TestCompareMinor(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
		ok   bool
	}{
		{"0.3.1", "0.4.0", -1, true},
		{"0.4.0", "0.3.9", 1, true},
		{"v0.4.2", "0.4.0", 0, true},
		{"0.4.0-rc1", "0.4.0", 0, true},
		{"1.0.0", "0.9.9", 1, true},
		{"0.10.0", "0.9.0", 1, true},
		{"dev", "0.4.0", 0, false},
		{"0.x", "0.4.0", 0, false},
		{"0.4.0", "", 0, false},
		{"0.-1.0", "0.4.0", 0, false},
		{"0.+4.0", "0.4.0", 0, false},
	} {
		if got, ok := compareMinor(c.a, c.b); got != c.want || ok != c.ok {
			t.Errorf("compareMinor(%q, %q) = %d, %v; want %d, %v", c.a, c.b, got, ok, c.want, c.ok)
		}
	}
	if _, ok := problemVersion("home-server", "dev", "0.3.1"); ok {
		t.Error("a development dashboard reported a version problem")
	}
}

func TestHumanDuration(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{1500 * time.Millisecond, "1 second"},
		{2 * time.Second, "2 seconds"},
		{59 * time.Second, "59 seconds"},
		{90 * time.Second, "1 minute"},
		{10 * time.Minute, "10 minutes"},
		{10*time.Minute + 59*time.Second, "10 minutes"},
		{90 * time.Minute, "1 hour"},
		{5 * time.Hour, "5 hours"},
		{47 * time.Hour, "47 hours"},
		{72 * time.Hour, "3 days"},
	} {
		if got := humanDuration(c.d); got != c.want {
			t.Errorf("humanDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
	if got := seconds(1500 * time.Millisecond); got != "2" {
		t.Errorf("seconds(1.5s) = %q, want it rounded up to 2", got)
	}
}

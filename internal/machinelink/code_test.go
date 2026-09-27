package machinelink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestNewJoinCode(t *testing.T) {
	re := regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`)
	seen := map[string]bool{}
	for range 1000 {
		c := newJoinCode()
		if !re.MatchString(c) {
			t.Fatalf("code %q has the wrong form", c)
		}
		if n, err := NormalizeJoinCode(c); err != nil || n != c {
			t.Fatalf("NormalizeJoinCode(%q) = %q, %v", c, n, err)
		}
		if seen[c] {
			t.Fatalf("code %q came up twice", c)
		}
		seen[c] = true
	}
}

func TestNormalizeJoinCode(t *testing.T) {
	for in, want := range map[string]string{
		"7KQ2-M9XD":    "7KQ2-M9XD",
		"7kq2-m9xd":    "7KQ2-M9XD",
		"7KQ2M9XD":     "7KQ2-M9XD",
		" 7KQ2 M9XD\t": "7KQ2-M9XD",
		"7-K-Q-2-M9XD": "7KQ2-M9XD",
		"7KQ2-M9XO":    "7KQ2-M9X0",
		"7kq2-m9xl":    "7KQ2-M9X1",
		"7KQ2-M9XI":    "7KQ2-M9X1",
		"0000-0000":    "0000-0000",
		"zzzz-zzzz":    "ZZZZ-ZZZZ",
	} {
		got, err := NormalizeJoinCode(in)
		if err != nil || got != want {
			t.Errorf("NormalizeJoinCode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "7KQ2-M9X", "7KQ2-M9XDD", "7KQ2-M9XU", "7KQ2-M9X!", "7KQ2_M9XD", strings.Repeat("-", 60) + "7KQ2M9XD"} {
		_, err := NormalizeJoinCode(in)
		if e := wantCode(t, err, CodeJoinCodeMalformed); !strings.Contains(e.Msg, "8 letters and digits") {
			t.Errorf("NormalizeJoinCode(%q): %q", in, e.Msg)
		}
	}
}

func TestCodeHashIsKeyedPerDashboard(t *testing.T) {
	k1, _ := codeKey(mustIdentity(t))
	k2, _ := codeKey(mustIdentity(t))
	a, b := hashCode(k1, "7KQ2-M9XD"), hashCode(k1, "7KQ2-M9XD")
	if !bytes.Equal(a, b) {
		t.Fatal("the same code hashed differently")
	}
	if bytes.Equal(a, hashCode(k2, "7KQ2-M9XD")) {
		t.Fatal("two dashboards hash codes the same way")
	}
	if bytes.Equal(a, hashCode(k1, "7KQ2-M9XE")) {
		t.Fatal("two codes hash the same")
	}
}

func TestJoinCodeIsNeverStored(t *testing.T) {
	th := newTestHub(t, nil)
	code, jc, err := th.NewJoinCode(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if jc.CreatedBy != "alice" || !jc.ExpiresAt.Equal(th.clock.Now().Add(CodeTTL)) || jc.State(th.clock.Now()) != JoinCodeWaiting {
		t.Fatalf("join code %+v", jc)
	}
	stored, _ := th.store.JoinCodes(context.Background())
	j, _ := json.Marshal(stored)
	bare := strings.ReplaceAll(code, "-", "")
	for _, s := range []string{fmt.Sprintf("%+v", stored), fmt.Sprintf("%x", stored[0].Hash), string(j)} {
		if strings.Contains(s, code) || strings.Contains(s, bare) {
			t.Fatalf("the store holds the code: %s", s)
		}
	}
	if strings.Contains(string(j), "hash") {
		t.Fatalf("the code's hash is in JSON: %s", j)
	}
}

func TestJoinCodeState(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	c := JoinCode{CreatedAt: now, ExpiresAt: now.Add(CodeTTL)}
	for _, tc := range []struct {
		at   time.Time
		used bool
		want JoinCodeState
	}{
		{now, false, JoinCodeWaiting},
		{now.Add(CodeTTL - time.Second), false, JoinCodeWaiting},
		{now.Add(CodeTTL), false, JoinCodeExpired},
		{now.Add(time.Minute), true, JoinCodeUsed},
		{now.Add(time.Hour), true, JoinCodeUsed},
	} {
		c := c
		if tc.used {
			c.UsedAt = now.Add(time.Minute)
		}
		if got := c.State(tc.at); got != tc.want {
			t.Errorf("State(%v, used %v) = %s, want %s", tc.at.Sub(now), tc.used, got, tc.want)
		}
	}
}

func TestGuardPerAddress(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	g := newGuard(Limits{}, nil, now)
	a := addrPrefix("203.0.113.7:50000")
	b := addrPrefix("203.0.113.8:50000")
	for i := range 5 {
		if w := g.wait(now, a); w != 0 {
			t.Fatalf("refused after %d failures", i)
		}
		g.fail(now.Add(time.Duration(i)*time.Minute), a)
	}
	now = now.Add(5 * time.Minute)
	if w := g.wait(now, a); w != 10*time.Minute {
		t.Fatalf("wait %v after 5 failures, want 10m (until the first one is 15 minutes old)", w)
	}
	if w := g.wait(now, b); w != 0 {
		t.Fatalf("another address waits %v", w)
	}
	if w := g.wait(now.Add(10*time.Minute), a); w != 0 {
		t.Fatalf("still refused once the window passed: %v", w)
	}
}

func TestGuardTotal(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	g := newGuard(Limits{}, nil, now)
	for i := range 20 {
		g.fail(now, addrPrefix(fmt.Sprintf("198.51.100.%d:1", i)))
	}
	if w := g.wait(now, addrPrefix("192.0.2.1:1")); w != 15*time.Minute {
		t.Fatalf("a new address waits %v after 20 failures from others, want 15m", w)
	}
	for i := range 1000 {
		g.fail(now, addrPrefix(fmt.Sprintf("10.0.%d.%d:1", i/256, i%256)))
	}
	if len(g.fails) > 20 {
		t.Fatalf("the guard keeps %d failures", len(g.fails))
	}
}

func TestGuardGroupsIPv6Networks(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	g := newGuard(Limits{AddressFailures: 2, TotalFailures: 100, Window: time.Minute}, nil, now)
	g.fail(now, addrPrefix("[2001:db8:1:2::1]:1"))
	g.fail(now, addrPrefix("[2001:db8:1:2:ffff::9]:1"))
	if g.wait(now, addrPrefix("[2001:db8:1:2::77]:1")) == 0 {
		t.Fatal("addresses in one /64 are counted apart")
	}
	if g.wait(now, addrPrefix("[2001:db8:1:3::1]:1")) != 0 {
		t.Fatal("the next /64 is refused too")
	}
}

// A guard started from the failures another guard kept, as after a
// restart, refuses what that guard refused, for as long.
func TestGuardStartsFromTheFailuresKept(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	g := newGuard(Limits{}, nil, now)
	var kept []JoinFailure
	for i := range 5 {
		kept = g.fail(now.Add(time.Duration(i)*time.Second), addrPrefix("[2001:db8:1:2::1]:1"))
	}
	again := newGuard(Limits{}, kept, now.Add(time.Minute))
	if w := again.wait(now.Add(time.Minute), addrPrefix("[2001:db8:1:2::99]:1")); w != 14*time.Minute {
		t.Fatalf("the /64 waits %v after a restart, want 14m", w)
	}
	if w := again.wait(now.Add(time.Minute), addrPrefix("[2001:db8:1:3::1]:1")); w != 0 {
		t.Fatalf("the next /64 waits %v", w)
	}

	// At most TotalFailures are kept, the newest, in the order they came.
	var many []JoinFailure
	for i := range 25 {
		many = append(many, JoinFailure{At: now.Add(time.Duration(25-i) * time.Second), From: addrPrefix(fmt.Sprintf("198.51.100.%d:1", i))})
	}
	g = newGuard(Limits{}, many, now.Add(time.Minute))
	if len(g.fails) != 20 || g.fails[0].At != now.Add(6*time.Second) || g.fails[19].At != now.Add(25*time.Second) {
		t.Fatalf("kept %d failures, from %v to %v", len(g.fails), g.fails[0].At, g.fails[len(g.fails)-1].At)
	}

	// A failure the clock put in the future counts as now.
	g = newGuard(Limits{AddressFailures: 1}, []JoinFailure{{At: now.AddDate(1, 0, 0), From: addrPrefix("203.0.113.7:1")}}, now)
	if w := g.wait(now, addrPrefix("203.0.113.7:1")); w != 15*time.Minute {
		t.Fatalf("a failure from next year makes the address wait %v, want 15m", w)
	}
}

func TestAddrPrefix(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.7:443":         "203.0.113.7/32",
		"[::ffff:203.0.113.7]:1":  "203.0.113.7/32",
		"[2001:db8:1:2:3::4]:443": "2001:db8:1:2::/64",
		"not an address":          "invalid Prefix",
	} {
		if got := addrPrefix(in).String(); got != want {
			t.Errorf("addrPrefix(%q) = %s, want %s", in, got, want)
		}
	}
	if got := remoteIP("[::ffff:203.0.113.7]:1"); got != "203.0.113.7" {
		t.Errorf("remoteIP = %q", got)
	}
	if addrPrefix("x") != (netip.Prefix{}) {
		t.Error("an unreadable address has its own bucket")
	}
}

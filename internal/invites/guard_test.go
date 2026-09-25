package invites

import (
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestGuardAddress(t *testing.T) {
	clock := &fakeClock{t: t0}
	g := NewGuard(GuardLimits{}, clock.Now)
	friend := netip.MustParseAddr("203.0.113.7")
	for i := range DefaultAddressRequests {
		if err := g.Address(friend); err != nil {
			t.Fatalf("request %d refused: %v", i+1, err)
		}
	}
	e := wantCode(t, g.Address(friend), CodeRateLimited)
	if e.RetryAfter != 20*time.Second || e.Params["seconds"] != "20" || e.Params["scope"] != "address" || e.Status != 429 {
		t.Errorf("refusal %+v", e)
	}
	if err := g.Address(netip.MustParseAddr("203.0.113.8")); err != nil {
		t.Errorf("another address was refused: %v", err)
	}

	clock.Advance(20 * time.Second)
	if err := g.Address(friend); err != nil {
		t.Fatalf("the allowance did not refill: %v", err)
	}
	wantCode(t, g.Address(friend), CodeRateLimited)
}

func TestAddressKey(t *testing.T) {
	for _, tc := range []struct{ ip, want string }{
		{"203.0.113.7", "203.0.113.7"},
		{"::ffff:203.0.113.7", "203.0.113.7"},
		{"2001:db8:1:2:3:4:5:6", "2001:db8:1:2::/64"},
		{"2001:db8:1:2:ffff::1", "2001:db8:1:2::/64"},
		{"2001:db8:1:3::1", "2001:db8:1:3::/64"},
		{"fe80::1%eth0", "fe80::/64"},
	} {
		if got := AddressKey(netip.MustParseAddr(tc.ip)); got != tc.want {
			t.Errorf("AddressKey(%s) = %s, want %s", tc.ip, got, tc.want)
		}
	}
	if AddressKey(netip.Addr{}) == AddressKey(netip.MustParseAddr("203.0.113.7")) {
		t.Error("an unknown address shares a real one's allowance")
	}
}

func TestGuardCountsIPv6ByNetwork(t *testing.T) {
	g := NewGuard(GuardLimits{AddressRequests: 2}, (&fakeClock{t: t0}).Now)
	g.Address(netip.MustParseAddr("2001:db8:1:2::1"))
	g.Address(netip.MustParseAddr("2001:db8:1:2::2"))
	wantCode(t, g.Address(netip.MustParseAddr("2001:db8:1:2::3")), CodeRateLimited)
	if err := g.Address(netip.MustParseAddr("2001:db8:1:3::1")); err != nil {
		t.Errorf("the next /64 was refused: %v", err)
	}
}

func TestGuardInviteFailures(t *testing.T) {
	clock := &fakeClock{t: t0}
	g := NewGuard(GuardLimits{InviteFailures: 3, InviteWindow: 3 * time.Minute}, clock.Now)
	const id, other = "abcdefghij", "kmnpqrstuv"

	cause := errors.New("connection refused")
	for _, err := range []error{
		nil, errors.New("plain"), NotFound(), expired(), playerName(), mojangDown(cause), mojangBusy(time.Minute, cause),
		usernameInvalid("length", "Username must be 3–32 characters."), passwordInvalid("too_short", "Password must be at least 10 characters."),
	} {
		g.Record(id, err)
	}
	if err := g.Invite(id); err != nil {
		t.Fatalf("failures that cost nothing were counted: %v", err)
	}

	g.Record(id, playerNotFound("Nobody_here"))
	g.Record(id, fmt.Errorf("accepting: %w", UsernameTaken()))
	if err := g.Invite(id); err != nil {
		t.Fatalf("refused after 2 of 3 failures: %v", err)
	}
	g.Record(id, playerDemo("PipDemo42"))
	e := wantCode(t, g.Invite(id), CodeRateLimited)
	if e.RetryAfter != time.Minute || e.Params["scope"] != "invite" || e.Msg != "This invite link has had too many tries." {
		t.Errorf("refusal %+v", e)
	}
	if err := g.Invite(other); err != nil {
		t.Errorf("another invite was refused: %v", err)
	}

	clock.Advance(time.Minute)
	for range 5 {
		if err := g.Invite(id); err != nil {
			t.Fatalf("checking an invite should not use up its allowance: %v", err)
		}
	}
	g.Record(id, playerLegacy("OldTimer"))
	wantCode(t, g.Invite(id), CodeRateLimited)
}

// Invites are only tracked after a failure behind a working code, so an
// invite id alone can't lock anybody out.
func TestGuardInviteNeedsNoEntryUntilItFails(t *testing.T) {
	g := NewGuard(GuardLimits{}, (&fakeClock{t: t0}).Now)
	for range 100 {
		g.Invite("abcdefghij")
	}
	if len(g.invites.m) != 0 {
		t.Errorf("%d invites tracked after checks alone", len(g.invites.m))
	}
}

func TestGuardMemoryIsBounded(t *testing.T) {
	clock := &fakeClock{t: t0}
	g := NewGuard(GuardLimits{AddressRequests: 2, AddressWindow: time.Minute, MaxKeys: 3}, clock.Now)
	for i := 1; i <= 3; i++ {
		if err := g.Address(netip.AddrFrom4([4]byte{198, 51, 100, byte(i)})); err != nil {
			t.Fatal(err)
		}
	}
	newcomer := netip.MustParseAddr("198.51.100.99")
	e := wantCode(t, g.Address(newcomer), CodeRateLimited)
	if e.RetryAfter != 30*time.Second || len(g.addresses.m) != 3 {
		t.Errorf("RetryAfter %v with %d addresses tracked", e.RetryAfter, len(g.addresses.m))
	}

	clock.Advance(30 * time.Second)
	if err := g.Address(newcomer); err != nil {
		t.Fatalf("idle addresses were not forgotten: %v", err)
	}
	if len(g.addresses.m) != 1 {
		t.Errorf("%d addresses tracked, want only the newcomer", len(g.addresses.m))
	}
}

func TestGuardConcurrent(t *testing.T) {
	g := NewGuard(GuardLimits{AddressRequests: 50, MaxKeys: 20}, (&fakeClock{t: t0}).Now)
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Go(func() {
			ip := netip.AddrFrom4([4]byte{198, 51, 100, byte(i % 25)})
			for range 10 {
				g.Address(ip)
				g.Invite("abcdefghij")
				g.Record("abcdefghij", playerNotFound("Nobody_here"))
			}
		})
	}
	wg.Wait()
	if len(g.addresses.m) > 20 {
		t.Errorf("%d addresses tracked, more than MaxKeys", len(g.addresses.m))
	}
}

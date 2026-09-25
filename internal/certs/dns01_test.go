package certs

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeChallenger keeps TXT records in memory, the way a DNS provider would.
type fakeChallenger struct {
	t        *testing.T
	mu       sync.Mutex
	records  map[string][]string
	calls    []string
	setErr   error
	clearCtx error
}

func newChallenger(t *testing.T) *fakeChallenger {
	return &fakeChallenger{t: t, records: map[string][]string{}}
}

func (c *fakeChallenger) SetTXT(ctx context.Context, fqdn, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, "set "+fqdn+" "+value)
	if c.setErr != nil {
		return c.setErr
	}
	c.records[fqdn] = append(c.records[fqdn], value)
	return nil
}

func (c *fakeChallenger) ClearTXT(ctx context.Context, fqdn, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, "clear "+fqdn+" "+value)
	c.clearCtx = ctx.Err()
	c.records[fqdn] = slices.DeleteFunc(c.records[fqdn], func(v string) bool { return v == value })
	return nil
}

// lookup answers like a resolver: not found when the name has no TXT.
func (c *fakeChallenger) lookup(_ context.Context, name string) ([]string, error) {
	fqdn, ok := strings.CutSuffix(name, ".")
	if !ok {
		c.t.Errorf("looked up %q without a trailing dot", name)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.records[fqdn]) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: fqdn, IsNotFound: true}
	}
	return slices.Clone(c.records[fqdn]), nil
}

func (c *fakeChallenger) txt(fqdn string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.records[fqdn])
}

func (c *fakeChallenger) log() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

const alexTXT = "_acme-challenge.alex.playkeeper.io"

func TestDNS01Present(t *testing.T) {
	c := newChallenger(t)
	d := &DNS01{Challenger: c, LookupTXT: c.lookup, Interval: time.Millisecond}
	remove, err := d.present(t.Context(), "alex.playkeeper.io", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.txt(alexTXT); !slices.Equal(got, []string{"v1"}) {
		t.Fatalf("records = %q", got)
	}
	remove()
	if got := c.txt(alexTXT); len(got) != 0 {
		t.Errorf("records after remove = %q", got)
	}
	want := []string{"set " + alexTXT + " v1", "clear " + alexTXT + " v1"}
	if got := c.log(); !slices.Equal(got, want) {
		t.Errorf("calls = %q, want %q", got, want)
	}
}

func TestDNS01WaitsForPropagation(t *testing.T) {
	c := newChallenger(t)
	var lookups atomic.Int32
	d := &DNS01{Challenger: c, Interval: time.Millisecond, Timeout: 5 * time.Second,
		LookupTXT: func(ctx context.Context, fqdn string) ([]string, error) {
			if lookups.Add(1) <= 3 {
				return nil, &net.DNSError{Err: "no such host", Name: fqdn, IsNotFound: true}
			}
			return c.lookup(ctx, fqdn)
		}}
	remove, err := d.present(t.Context(), "alex.playkeeper.io", "v1")
	if err != nil {
		t.Fatal(err)
	}
	defer remove()
	if n := lookups.Load(); n != 4 {
		t.Errorf("%d lookups, want 4", n)
	}
}

func TestDNS01NotVisible(t *testing.T) {
	c := newChallenger(t)
	d := &DNS01{Challenger: c, Interval: time.Millisecond, Timeout: 50 * time.Millisecond,
		LookupTXT: func(context.Context, string) ([]string, error) { return []string{"stale"}, nil }}
	start := time.Now()
	_, err := d.present(t.Context(), "alex.playkeeper.io", "v1")
	p := wantProblem(t, err, CodeDNS01NotVisible, "")
	if time.Since(start) > 5*time.Second {
		t.Errorf("gave up after %s", time.Since(start))
	}
	if p.Params["fqdn"] != alexTXT || p.Params["name"] != "alex.playkeeper.io" {
		t.Errorf("params = %v", p.Params)
	}
	if !strings.Contains(p.Message, alexTXT) || !strings.Contains(p.Detail, `"stale"`) {
		t.Errorf("message %q, detail %q", p.Message, p.Detail)
	}
	if got := c.txt(alexTXT); len(got) != 0 {
		t.Errorf("the record was left behind: %q", got)
	}
}

func TestDNS01LookupsFail(t *testing.T) {
	// Outgoing DNS may be blocked on this server while the record is
	// fine; then Let's Encrypt is asked to look anyway.
	c := newChallenger(t)
	var lookups atomic.Int32
	d := &DNS01{Challenger: c, Interval: time.Millisecond, Timeout: 50 * time.Millisecond,
		LookupTXT: func(_ context.Context, fqdn string) ([]string, error) {
			lookups.Add(1)
			return nil, &net.DNSError{Err: "i/o timeout", Name: fqdn, IsTimeout: true}
		}}
	remove, err := d.present(t.Context(), "alex.playkeeper.io", "v1")
	if err != nil {
		t.Fatalf("present = %v, want to go ahead", err)
	}
	if got := c.txt(alexTXT); !slices.Equal(got, []string{"v1"}) {
		t.Errorf("records = %q", got)
	}
	remove()
	if lookups.Load() < 2 {
		t.Errorf("only %d lookups", lookups.Load())
	}
}

func TestDNS01PublishFails(t *testing.T) {
	c := newChallenger(t)
	c.setErr = errors.New("the playkeeper.io service refused the change: too many records")
	d := &DNS01{Challenger: c, LookupTXT: c.lookup}
	_, err := d.present(t.Context(), "alex.playkeeper.io", "v1")
	p := wantProblem(t, err, CodeDNS01PublishFailed, "")
	if p.Detail != c.setErr.Error() || !errors.Is(err, c.setErr) {
		t.Errorf("detail = %q", p.Detail)
	}
	want := []string{"set " + alexTXT + " v1", "clear " + alexTXT + " v1"}
	if got := c.log(); !slices.Equal(got, want) {
		t.Errorf("calls = %q, want %q", got, want)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := d.present(ctx, "alex.playkeeper.io", "v1"); !errors.Is(err, context.Canceled) {
		t.Errorf("present with a canceled context = %v", err)
	}
}

func TestDNS01Canceled(t *testing.T) {
	c := newChallenger(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var lookups atomic.Int32
	d := &DNS01{Challenger: c, Interval: time.Millisecond, Timeout: time.Minute,
		LookupTXT: func(context.Context, string) ([]string, error) {
			if lookups.Add(1) == 2 {
				cancel()
			}
			return nil, nil
		}}
	_, err := d.present(ctx, "alex.playkeeper.io", "v1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("present = %v", err)
	}
	if got := c.txt(alexTXT); len(got) != 0 {
		t.Errorf("the record was left behind: %q", got)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.clearCtx != nil {
		t.Errorf("ClearTXT got a context that was already done: %v", c.clearCtx)
	}
}

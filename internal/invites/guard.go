package invites

import (
	"net/netip"
	"sync"
	"time"
)

// GuardLimits size a Guard. Zero fields take the defaults.
type GuardLimits struct {
	// AddressRequests is how many public invite requests one address may
	// make in a burst; the allowance refills evenly over AddressWindow.
	AddressRequests int
	AddressWindow   time.Duration
	// InviteFailures is how many costly failures one invite may have in a
	// burst (see Guard.Record), refilled over InviteWindow.
	InviteFailures int
	InviteWindow   time.Duration
	// MaxKeys bounds how many addresses, and how many invites, are tracked
	// at once.
	MaxKeys int
}

// Defaults for GuardLimits: enough for a group of friends behind one home
// connection opening a link, checking their names and joining at once, too
// few to wear out Mojang's lookup budget.
const (
	DefaultAddressRequests = 60
	DefaultAddressWindow   = 10 * time.Minute
	DefaultInviteFailures  = 10
	DefaultInviteWindow    = 10 * time.Minute
	DefaultMaxKeys         = 10000
)

// Guard limits attempts on the public invite pages: every request counts
// against the client's address, and failures that cost a Mojang lookup or
// reveal an account count against the invite. Invites are only tracked
// once a request has shown a working code, so knowing an invite's id is
// not enough to lock it. It is safe for concurrent use.
type Guard struct {
	now       func() time.Time
	mu        sync.Mutex
	addresses buckets
	invites   buckets
}

// NewGuard returns a Guard with the given limits and clock.
func NewGuard(l GuardLimits, now func() time.Time) *Guard {
	if l.AddressRequests <= 0 {
		l.AddressRequests = DefaultAddressRequests
	}
	if l.AddressWindow <= 0 {
		l.AddressWindow = DefaultAddressWindow
	}
	if l.InviteFailures <= 0 {
		l.InviteFailures = DefaultInviteFailures
	}
	if l.InviteWindow <= 0 {
		l.InviteWindow = DefaultInviteWindow
	}
	if l.MaxKeys <= 0 {
		l.MaxKeys = DefaultMaxKeys
	}
	if now == nil {
		now = time.Now
	}
	return &Guard{
		now:       now,
		addresses: newBuckets(l.AddressRequests, l.AddressWindow, l.MaxKeys),
		invites:   newBuckets(l.InviteFailures, l.InviteWindow, l.MaxKeys),
	}
}

// Address counts one request from ip, or refuses it. Call it first on
// every public invite route.
func (g *Guard) Address(ip netip.Addr) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if wait, ok := g.addresses.take(AddressKey(ip), g.now()); !ok {
		return rateLimited("address", wait)
	}
	return nil
}

// Invite refuses an invite that has had too many failures lately. Call it
// once the code checked out, before any lookup.
func (g *Guard) Invite(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if wait, ok := g.invites.peek(id, g.now()); !ok {
		return rateLimited("invite", wait)
	}
	return nil
}

// Record counts err against the invite if it is a failure that cost a
// Mojang lookup or told the caller something: an unknown or unusable
// player, or a taken username. Typos caught without a lookup, and outages,
// don't count. Record redemptions and acceptances, not the name preview's
// lookups (LookupPlayer): names half typed would lock the invite.
func (g *Guard) Record(id string, err error) {
	switch CodeOf(err) {
	case CodePlayerUnknown, CodePlayerDemo, CodePlayerLegacy, CodeUsernameTaken:
	default:
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.invites.take(id, g.now())
}

// AddressKey is what an address counts against: the address for IPv4, and
// its /64 for IPv6, since one home connection usually gets a whole /64.
func AddressKey(ip netip.Addr) string {
	ip = ip.Unmap().WithZone("")
	if ip.Is6() {
		p, _ := ip.Prefix(64)
		return p.String()
	}
	return ip.String()
}

// buckets is a keyed token bucket with bounded memory. When every tracked
// key is still busy, new keys are refused rather than old ones forgotten,
// so flooding it with addresses can't wipe an address's record.
type buckets struct {
	capacity float64
	refill   float64
	max      int
	m        map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newBuckets(capacity int, window time.Duration, keys int) buckets {
	return buckets{capacity: float64(capacity), refill: float64(capacity) / window.Seconds(), max: keys, m: map[string]*bucket{}}
}

func (b *buckets) level(k *bucket, now time.Time) {
	k.tokens = min(b.capacity, k.tokens+max(0, now.Sub(k.last).Seconds())*b.refill)
	k.last = now
}

func (b *buckets) wait(tokens float64) time.Duration {
	return time.Duration((1 - tokens) / b.refill * float64(time.Second))
}

// take spends one token of key's bucket, or says how long until one is
// back.
func (b *buckets) take(key string, now time.Time) (time.Duration, bool) {
	k, ok := b.m[key]
	if !ok {
		if len(b.m) >= b.max {
			if wait := b.gc(now); len(b.m) >= b.max {
				return wait, false
			}
		}
		k = &bucket{tokens: b.capacity, last: now}
		b.m[key] = k
	}
	b.level(k, now)
	if k.tokens < 1 {
		return b.wait(k.tokens), false
	}
	k.tokens--
	return 0, true
}

// peek reports whether key's bucket has a token, without spending it.
func (b *buckets) peek(key string, now time.Time) (time.Duration, bool) {
	k, ok := b.m[key]
	if !ok {
		return 0, true
	}
	b.level(k, now)
	if k.tokens < 1 {
		return b.wait(k.tokens), false
	}
	return 0, true
}

// gc forgets buckets that have refilled completely, and returns how long
// until the next one will have.
func (b *buckets) gc(now time.Time) time.Duration {
	soonest := time.Duration(-1)
	for key, k := range b.m {
		missing := b.capacity - k.tokens - max(0, now.Sub(k.last).Seconds())*b.refill
		if missing <= 0 {
			delete(b.m, key)
			continue
		}
		if d := time.Duration(missing / b.refill * float64(time.Second)); soonest < 0 || d < soonest {
			soonest = d
		}
	}
	return max(soonest, time.Second)
}

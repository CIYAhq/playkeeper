package machinelink

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"net/netip"
	"slices"
	"sync"
	"time"
)

const (
	// CodeTTL is how long a join code works.
	CodeTTL = 30 * time.Minute
	// MaxWaitingCodes is how many unused join codes can wait at once;
	// making another drops the oldest.
	MaxWaitingCodes = 5
	// codeKeep is how long a used or expired code stays listed, so that
	// someone trying it late is told it expired or was used rather than
	// that it is wrong.
	codeKeep = time.Hour
)

// JoinCode is a one-time code that lets one machine join. Only a keyed
// hash of the code is kept, never the code: a copy of the database alone
// is not enough to find it, because the key comes from the dashboard's
// private key file.
type JoinCode struct {
	ID        string    `json:"id"`
	Hash      []byte    `json:"-"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	// CreatedBy is the account that made the code; the machine that uses
	// it is recorded as added by them.
	CreatedBy string    `json:"createdBy"`
	UsedAt    time.Time `json:"usedAt,omitzero"`
	MachineID string    `json:"machineId,omitempty"`
}

// JoinCodeState is where a join code is in its life.
type JoinCodeState string

const (
	JoinCodeWaiting JoinCodeState = "waiting"
	JoinCodeUsed    JoinCodeState = "used"
	JoinCodeExpired JoinCodeState = "expired"
)

// State reports whether the code is waiting, used or expired at now.
func (c JoinCode) State(now time.Time) JoinCodeState {
	switch {
	case !c.UsedAt.IsZero():
		return JoinCodeUsed
	case !now.Before(c.ExpiresAt):
		return JoinCodeExpired
	default:
		return JoinCodeWaiting
	}
}

// newJoinCode returns a random code of 8 Crockford base32 characters
// (40 bits), formatted XXXX-XXXX. Forty bits are plenty because a code
// lives 30 minutes and the dashboard accepts only about 60 wrong codes in
// that time (see Limits): even with 5 codes waiting, the chance that a
// guesser hits one is about 1 in 3.7 billion per half hour.
func newJoinCode() string {
	var b [5]byte
	rand.Read(b[:])
	s := b32.EncodeToString(b[:])
	return s[:4] + "-" + s[4:]
}

// NormalizeJoinCode reads a join code as typed or read out: case, spaces
// and dashes don't matter, and O, I and L count as 0, 1 and 1. It returns
// the code as XXXX-XXXX.
func NormalizeJoinCode(s string) (string, error) {
	if len(s) > 64 {
		return "", errJoinCodeMalformed()
	}
	n := normalizeCrockford(s)
	if len(n) != 8 || !isCrockford(n) {
		return "", errJoinCodeMalformed()
	}
	return n[:4] + "-" + n[4:], nil
}

// codeKey derives the key join codes are hashed with from the dashboard's
// private key, for this purpose only.
func codeKey(id *Identity) ([]byte, error) {
	return hkdf.Key(sha256.New, id.key.Seed(), nil, "playkeeper machinelink join codes v1", 32)
}

// hashCode hashes a normalized code.
func hashCode(key []byte, code string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("playkeeper join code v1\x00"))
	m.Write([]byte(code[:4] + code[5:]))
	return m.Sum(nil)
}

func findCode(codes []JoinCode, sum []byte) (JoinCode, bool) {
	for _, c := range codes {
		if hmac.Equal(c.Hash, sum) {
			return c, true
		}
	}
	return JoinCode{}, false
}

// Limits bound how many wrong join codes the dashboard accepts, from one
// network and from everyone, before it refuses joins for a while. Joins
// that fail only because a code expired don't count.
type Limits struct {
	// AddressFailures is how many failures one address (an IPv4 address,
	// or an IPv6 /64 network) may have in Window.
	AddressFailures int
	// TotalFailures is how many failures everyone together may have in
	// Window. It is what bounds guessing from many addresses at once, at
	// the price that such an attack also blocks honest joins for a while.
	TotalFailures int
	Window        time.Duration
}

// DefaultLimits allow 5 failures per address and 20 in total per 15
// minutes: at most about 60 guesses in a code's 30 minutes, against 5
// waiting codes of 2^40 values each.
func DefaultLimits() Limits {
	return Limits{AddressFailures: 5, TotalFailures: 20, Window: 15 * time.Minute}
}

// JoinFailure is a join refused for its code (wrong, malformed or used):
// when, and the network it came from, an IPv4 address or an IPv6 /64. From
// is the zero Prefix when the address couldn't be read.
type JoinFailure struct {
	At   time.Time
	From netip.Prefix
}

// guard counts recent join failures. It keeps at most TotalFailures of
// them, so a flood can't make it grow.
type guard struct {
	limits Limits

	mu    sync.Mutex
	fails []JoinFailure
}

// newGuard starts from the failures a store kept. One the clock puts after
// now counts as now, so a clock that was wrong can't pause joining for
// longer than the window.
func newGuard(l Limits, kept []JoinFailure, now time.Time) *guard {
	d := DefaultLimits()
	if l.AddressFailures <= 0 {
		l.AddressFailures = d.AddressFailures
	}
	if l.TotalFailures <= 0 {
		l.TotalFailures = d.TotalFailures
	}
	if l.Window <= 0 {
		l.Window = d.Window
	}
	g := &guard{limits: l, fails: slices.Clone(kept)}
	for i := range g.fails {
		if g.fails[i].At.After(now) {
			g.fails[i].At = now
		}
	}
	slices.SortStableFunc(g.fails, func(a, b JoinFailure) int { return a.At.Compare(b.At) })
	if over := len(g.fails) - l.TotalFailures; over > 0 {
		g.fails = g.fails[over:]
	}
	return g
}

// wait returns how long joins from addr must wait, or 0.
func (g *guard) wait(now time.Time, addr netip.Prefix) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if d := g.pausedLocked(now); d > 0 {
		return d
	}
	var mine []time.Time
	for _, f := range g.fails {
		if f.From == addr {
			mine = append(mine, f.At)
		}
	}
	if len(mine) >= g.limits.AddressFailures {
		return mine[len(mine)-g.limits.AddressFailures].Add(g.limits.Window).Sub(now)
	}
	return 0
}

// paused returns how long joins from everyone must wait, or 0.
func (g *guard) paused(now time.Time) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pausedLocked(now)
}

func (g *guard) pausedLocked(now time.Time) time.Duration {
	g.prune(now)
	if len(g.fails) >= g.limits.TotalFailures {
		return g.fails[len(g.fails)-g.limits.TotalFailures].At.Add(g.limits.Window).Sub(now)
	}
	return 0
}

// fail counts a failure and returns the failures the guard keeps, for the
// store.
func (g *guard) fail(now time.Time, addr netip.Prefix) []JoinFailure {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.prune(now)
	g.fails = append(g.fails, JoinFailure{At: now, From: addr})
	if over := len(g.fails) - g.limits.TotalFailures; over > 0 {
		g.fails = append(g.fails[:0], g.fails[over:]...)
	}
	return slices.Clone(g.fails)
}

func (g *guard) prune(now time.Time) {
	i := 0
	for i < len(g.fails) && !now.Before(g.fails[i].At.Add(g.limits.Window)) {
		i++
	}
	if i > 0 {
		g.fails = append(g.fails[:0], g.fails[i:]...)
	}
}

// addrPrefix is the network a failure is counted against: the address
// itself for IPv4, its /64 for IPv6 (one customer usually has a whole
// /64). Addresses that can't be read share one bucket.
func addrPrefix(remote string) netip.Prefix {
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		return netip.Prefix{}
	}
	a := ap.Addr().Unmap()
	bits := 32
	if a.Is6() {
		bits = 64
	}
	p, err := a.Prefix(bits)
	if err != nil {
		return netip.Prefix{}
	}
	return p
}

// remoteIP is the address shown to people and recorded: the IP, without
// the port.
func remoteIP(remote string) string {
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		return ""
	}
	return ap.Addr().Unmap().String()
}

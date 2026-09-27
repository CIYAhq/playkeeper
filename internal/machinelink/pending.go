package machinelink

import (
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"
)

// pendingConns are the connections still in their handshake, which have
// proven nothing yet. One source may have perSource of them and everyone
// together max. A newcomer past either limit closes a waiter instead of
// being turned away: its own source's oldest, or else the oldest of the
// source with the most. So connections that never finish their handshake
// can't keep machines from other sources out.
type pendingConns struct {
	max, perSource int
	now            func() time.Time
	log            *slog.Logger

	mu       sync.Mutex
	list     []*pendingConn // oldest first
	closed   int            // since the last warning
	warnedAt time.Time
}

type pendingConn struct {
	from netip.Prefix
	conn net.Conn
}

func newPendingConns(max, perSource int, now func() time.Time, log *slog.Logger) *pendingConns {
	return &pendingConns{max: max, perSource: perSource, now: now, log: log}
}

// add counts c as pending until release is called, which may be more
// than once. A newcomer pushes c out by closing it.
func (p *pendingConns) add(from netip.Prefix, c net.Conn) (release func()) {
	e := &pendingConn{from: from, conn: c}
	p.mu.Lock()
	out := p.victim(from)
	warn := 0
	if out != nil {
		p.remove(out)
		p.closed++
		if now := p.now(); now.Sub(p.warnedAt) >= time.Minute {
			warn, p.closed, p.warnedAt = p.closed, 0, now
		}
	}
	p.list = append(p.list, e)
	p.mu.Unlock()
	if out != nil {
		out.conn.Close()
	}
	if warn > 0 {
		p.log.Warn("too many machine connections waiting for their handshake; closed the oldest",
			"closed", warn, "source", out.from.String())
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			p.remove(e)
			p.mu.Unlock()
		})
	}
}

// victim is the waiter a newcomer from from pushes out, if any.
func (p *pendingConns) victim(from netip.Prefix) *pendingConn {
	counts := make(map[netip.Prefix]int, len(p.list))
	for _, e := range p.list {
		counts[e.from]++
	}
	src, most := from, counts[from]
	if most < p.perSource {
		if len(p.list) < p.max {
			return nil
		}
		// The list is oldest first, so a tie goes to the source with the
		// oldest waiter.
		for _, e := range p.list {
			if counts[e.from] > most {
				src, most = e.from, counts[e.from]
			}
		}
	}
	for _, e := range p.list {
		if e.from == src {
			return e
		}
	}
	return nil
}

func (p *pendingConns) remove(e *pendingConn) {
	if i := slices.Index(p.list, e); i >= 0 {
		p.list = slices.Delete(p.list, i, i+1)
	}
}

// pendingSource is what a pending connection counts against: its address
// for IPv4, and for IPv6 its /48, which tunnel brokers hand out for free.
// Honest machines only wait for a moment, so sharing a source costs them
// little.
func pendingSource(remote string) netip.Prefix {
	p := addrPrefix(remote)
	if p.Addr().Is6() {
		p, _ = p.Addr().Prefix(48)
	}
	return p
}

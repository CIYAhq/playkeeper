// Package service is the stats service behind stats.playkeeper.io
// (cmd/playkeeper-stats): it takes the anonymous reports Playkeeper installs
// send (internal/usage) and answers counts of them. It keeps each install's
// random ID with what its reports say, to the hour, and the days it ran. It
// never keeps a request's address: the address decides the rate limits, in
// memory, and is dropped.
package service

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/store"
)

// Limits, documented for the owner in services/stats/README.md.
const (
	// Requests from one IPv4 address or IPv6 /64: an install sends three
	// reports while it installs and two a day after, so this leaves room
	// for many installs behind one address.
	requestsPerIPBurst, requestsPerIPHour = 60, 120
	// Reports for one install ID.
	reportsPerIDBurst, reportsPerIDHour = 6, 12
	// keepBackups is how many daily database snapshots stay in
	// DataDir/backups.
	keepBackups = 7
)

// Service is the stats service. Handler serves its API; Run does the
// periodic work (pruning and daily snapshots).
type Service struct {
	cfg Config
	db  *sql.DB
	log *slog.Logger
	now func() time.Time

	perIP, perID, newIDs *limiter

	mu         sync.Mutex
	lastBackup string
	warnedAt   time.Time
}

// New opens the database in cfg.DataDir.
func New(cfg Config) (*Service, error) {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewPerDay == 0 {
		cfg.NewPerDay = DefaultNewPerDay
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("could not create the data directory: %w", err)
	}
	db, err := store.Open(filepath.Join(cfg.DataDir, "stats.db"), migrations)
	if err != nil {
		return nil, err
	}
	now := cfg.Now
	s := &Service{
		cfg: cfg, db: db, log: cfg.Log, now: now,
		perIP:  newLimiter(requestsPerIPBurst, requestsPerIPHour, time.Hour, now),
		perID:  newLimiter(reportsPerIDBurst, reportsPerIDHour, time.Hour, now),
		newIDs: newLimiter(cfg.NewPerDay, cfg.NewPerDay, day, now),
	}
	for _, p := range cfg.TrustedProxies {
		if p.Bits() < p.Addr().BitLen() {
			s.log.Warn(EnvTrustedProxies+" lists a whole network, so anything in it can make the service believe any client address. List only the reverse proxy's own address, unless the network holds nothing but the proxy and this service (see services/stats/README.md).",
				"network", p.String())
		}
	}
	return s, nil
}

// Close closes the database.
func (s *Service) Close() error { return s.db.Close() }

// Run does the periodic work every hour until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		now := s.now()
		s.prune(ctx, now)
		s.backup(now)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) logErr(what string, err error) {
	if err != nil {
		s.log.Error(what, "error", err)
	}
}

// backup writes the day's snapshot of the database to DataDir/backups once
// a day and keeps the last keepBackups.
func (s *Service) backup(now time.Time) {
	day := now.UTC().Format("2006-01-02")
	s.mu.Lock()
	done := s.lastBackup == day
	s.lastBackup = day
	s.mu.Unlock()
	if done {
		return
	}
	dir := filepath.Join(s.cfg.DataDir, "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.logErr("Could not create the backups directory", err)
		return
	}
	path := filepath.Join(dir, "stats-"+day+".db")
	if _, err := os.Stat(path); err != nil {
		tmp := path + ".partial"
		os.Remove(tmp)
		_, err := s.db.Exec(`VACUUM INTO ?`, tmp)
		if err == nil {
			err = os.Chmod(tmp, 0o600)
		}
		if err == nil {
			err = os.Rename(tmp, path)
		}
		if err != nil {
			s.logErr("Could not write the daily database snapshot", err)
			os.Remove(tmp)
			return
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var snaps []string
	for _, e := range entries {
		if n := e.Name(); strings.HasPrefix(n, "stats-") && strings.HasSuffix(n, ".db") {
			snaps = append(snaps, n)
		}
	}
	slices.Sort(snaps)
	for len(snaps) > keepBackups {
		s.logErr("Could not remove an old snapshot", os.Remove(filepath.Join(dir, snaps[0])))
		snaps = snaps[1:]
	}
}

// clientAddr is the address a request came from: the connection's peer, or,
// when the peer is a trusted proxy, the rightmost X-Forwarded-For address
// that is not a trusted proxy itself. Each proxy appends the address it saw,
// so everything left of that could have been written by the client. It is
// used for the rate limits and dropped.
func (s *Service) clientAddr(r *http.Request) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	addr := ap.Addr().Unmap().WithZone("")
	if !s.trusted(addr) {
		if r.Header.Get("X-Forwarded-For") != "" && (addr.IsPrivate() || addr.IsLoopback()) {
			s.warnProxy()
		}
		return addr, true
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return netip.Addr{}, false
		}
		addr = a.Unmap().WithZone("")
		if !s.trusted(addr) {
			return addr, true
		}
	}
	return addr, true
}

// warnProxy says, at most once an hour, that requests come through a proxy
// the settings don't list, so every install shares its address's limit.
func (s *Service) warnProxy() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now := s.now(); now.Sub(s.warnedAt) >= time.Hour {
		s.warnedAt = now
		s.log.Warn("Requests come through a proxy that " + EnvTrustedProxies + " does not list, so the service ignores the client addresses it passes on and every install shares one rate limit. Set it to the proxy's address, as services/stats/README.md describes.")
	}
}

func (s *Service) trusted(a netip.Addr) bool {
	for _, p := range s.cfg.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// addrBucket groups addresses for the rate limit: one IPv4 address, or an
// IPv6 /64, since one machine usually has a whole /64.
func addrBucket(a netip.Addr) string {
	if a.Is4() {
		return a.String()
	}
	p, _ := a.Prefix(64)
	return p.String()
}

// limiter is a keyed token bucket. Buckets that have refilled completely are
// forgotten, so memory follows recent traffic.
type limiter struct {
	mu       sync.Mutex
	capacity float64
	refill   float64 // tokens per second
	buckets  map[string]*bucket
	gcAt     int
	now      func() time.Time
}

// minGC is the number of buckets below which a limiter never collects.
const minGC = 10000

type bucket struct {
	tokens float64
	last   time.Time
}

// newLimiter allows n events per period on average, and up to burst at once.
func newLimiter(burst, n int, per time.Duration, now func() time.Time) *limiter {
	return &limiter{capacity: float64(burst), refill: float64(n) / per.Seconds(), buckets: map[string]*bucket{}, gcAt: minGC, now: now}
}

// allow takes one token for key; when there is none it returns the wait.
func (l *limiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.buckets[key]
	if b == nil {
		if len(l.buckets) >= l.gcAt {
			l.gc(now)
			l.gcAt = max(minGC, 2*len(l.buckets))
		}
		b = &bucket{tokens: l.capacity, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.capacity, b.tokens+now.Sub(b.last).Seconds()*l.refill)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / l.refill * float64(time.Second))
}

func (l *limiter) gc(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.last).Seconds()*l.refill >= l.capacity {
			delete(l.buckets, k)
		}
	}
}

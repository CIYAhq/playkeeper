// Package service is the names service behind free yourname.playkeeper.io
// addresses (cmd/playkeeper-names). It keeps claimed names in SQLite and
// manages their DNS records in the base domain's Cloudflare zone, pointing
// each name at the public address its install's signed requests come from.
//
// The Cloudflare token can edit the whole zone, so every change passes one
// guard (Service.owns): the service only touches records whose names match
// its own patterns under a claimed, unreserved name and that carry its
// marker comment. The apex, www, names, mail records and every reserved
// name are never touched.
package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CIYAhq/playkeeper/internal/store"
)

// Limits, documented for the owner in services/names/README.md.
const (
	// lapseAfter: a name that has not been refreshed for this long loses
	// its records; the install can still refresh it back.
	lapseAfter = 30 * 24 * time.Hour
	// freeAfter: a lapsed name goes back to everyone after this.
	freeAfter = 60 * 24 * time.Hour
	// releaseHold: a released name is held from other installs this long.
	releaseHold = 30 * 24 * time.Hour
	// challengeTTL: an ACME challenge record is removed after this even if
	// the install never clears it.
	challengeTTL  = time.Hour
	maxChallenges = 2
	maxBody       = 4 << 10

	requestsPerIPBurst, requestsPerIPHour   = 60, 120
	requestsPerKeyBurst, requestsPerKeyHour = 30, 60
	// claimsPerAddress: new names per day from one IPv4 address or IPv6 /56.
	claimsPerAddress = 3

	// Server addresses (SRV records) one install may have across its names,
	// and all names claimed from one network together.
	serversPerKey     = 5
	serversPerNetwork = 10
	// serverAddressAge: a name gets server addresses only this long after
	// it was claimed, so names claimed in bulk cost two records each.
	serverAddressAge = 3 * 24 * time.Hour

	// Records the zone keeps free on top of the owner's reserve: new names
	// and server addresses leave challengeRoom free for certificate
	// challenges, and server addresses also leave nameRoom free for new
	// names. The owner is alerted once server addresses are refused.
	challengeRoom = 10
	nameRoom      = 40
)

// Service is the names service. Handler serves its API; Run does the
// periodic work (expiry, DNS retries, daily backups).
type Service struct {
	cfg  Config
	base string
	db   *sql.DB
	cf   *cloudflare
	log  *slog.Logger
	now  func() time.Time

	block                 *blocklist
	perIP, perKey         *limiter
	claimsAddr, claimsAll *limiter
	alerts                *alerter

	locks       nameLocks
	zoneOK      atomic.Bool
	warnedProxy atomic.Bool
	lastBackup  string
}

// New opens the database in cfg.DataDir and checks the Cloudflare zone. A
// refused token or a zone of another domain is an error; if Cloudflare
// cannot be reached, the check is repeated before the first change.
func New(ctx context.Context, cfg Config) (*Service, error) {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: noRedirects,
		}
	}
	if cfg.cloudflareAPI == "" {
		cfg.cloudflareAPI = cloudflareAPI
	}
	if cfg.pageSize == 0 {
		cfg.pageSize = 100
	}
	if cfg.MaxNamesPerKey == 0 {
		cfg.MaxNamesPerKey = DefaultMaxNamesPerKey
	}
	if cfg.MaxNamesPerNetwork == 0 {
		cfg.MaxNamesPerNetwork = DefaultMaxNamesPerNetwork
	}
	if cfg.ClaimsPerDay == 0 {
		cfg.ClaimsPerDay = DefaultClaimsPerDay
	}
	if cfg.RecordQuota == 0 {
		cfg.RecordQuota = DefaultRecordQuota
	}
	if err := checkBase(cfg.Base); err != nil {
		return nil, err
	}
	if err := checkWebhook(cfg.AlertWebhook); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("could not create the data directory: %w", err)
	}
	db, err := store.Open(filepath.Join(cfg.DataDir, "names.db"), migrations)
	if err != nil {
		return nil, err
	}
	now := cfg.Now
	s := &Service{
		cfg: cfg, base: cfg.Base, db: db, log: cfg.Log, now: now,
		cf:         &cloudflare{api: cfg.cloudflareAPI, token: cfg.CloudflareToken, zone: cfg.CloudflareZone, hc: cfg.HTTP, now: now, perPage: cfg.pageSize},
		block:      &blocklist{path: cfg.BlocklistFile},
		perIP:      newLimiter(requestsPerIPBurst, requestsPerIPHour, time.Hour, now),
		perKey:     newLimiter(requestsPerKeyBurst, requestsPerKeyHour, time.Hour, now),
		claimsAddr: newLimiter(claimsPerAddress, claimsPerAddress, 24*time.Hour, now),
		claimsAll:  newLimiter(cfg.ClaimsPerDay, cfg.ClaimsPerDay, 24*time.Hour, now),
		alerts: &alerter{
			url: cfg.AlertWebhook, from: "playkeeper-names for " + cfg.Base + ": ", log: cfg.Log, now: now, last: map[string]time.Time{},
			hc: &http.Client{Timeout: alertTimeout, CheckRedirect: noRedirects, Transport: cfg.alertTransport},
		},
	}
	s.block.reload(s.log)
	if err := s.ensureZone(ctx); err != nil {
		var ce *cfError
		if !errors.As(err, &ce) || !ce.temporary() {
			db.Close()
			switch {
			case ce != nil && ce.auth():
				return nil, fmt.Errorf("Cloudflare refused the API token in %s for the zone in %s (%v); check both as services/names/README.md describes", EnvToken, EnvZone, err)
			case ce != nil:
				return nil, fmt.Errorf("could not read the Cloudflare zone in %s (%v); check it as services/names/README.md describes", EnvZone, err)
			}
			return nil, err
		}
		s.log.Warn("Could not reach Cloudflare to check the zone; the check is repeated before the first change", "error", err)
	}
	return s, nil
}

// Close waits for alerts still being sent and closes the database.
func (s *Service) Close() error {
	s.alerts.wait()
	return s.db.Close()
}

// noRedirects makes an http.Client hand back redirects instead of
// following them.
func noRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// Run does the periodic work every minute until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		s.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// nameLocks serialises DNS changes per name.
type nameLocks struct {
	mu sync.Mutex
	m  map[string]*nameLock
}

type nameLock struct {
	mu    sync.Mutex
	users int
}

func (l *nameLocks) lock(name string) (unlock func()) {
	l.mu.Lock()
	if l.m == nil {
		l.m = map[string]*nameLock{}
	}
	e := l.m[name]
	if e == nil {
		e = &nameLock{}
		l.m[name] = e
	}
	e.users++
	l.mu.Unlock()
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		l.mu.Lock()
		if e.users--; e.users == 0 {
			delete(l.m, name)
		}
		l.mu.Unlock()
	}
}

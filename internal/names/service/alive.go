package service

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/names"
)

// Liveness checks (see names.AlivePath), documented for the owner in
// services/names/README.md.
const (
	// checkEvery: each active name's address is checked this often.
	checkEvery = 6 * time.Hour
	// A tick starts at most checksPerTick checks, checkWorkers at once, and
	// one per address.
	checksPerTick = 20
	checkWorkers  = 8
	checkTimeout  = 10 * time.Second
	// unansweredAfter: a name lapses once its address has not answered for
	// this long since its last answer or its claim, and at least
	// minFailedChecks checks in a row failed.
	unansweredAfter = 7 * 24 * time.Hour
	minFailedChecks = 4
	// checkHealthy: names lapse for not answering only while some address
	// answered within this long. When none did, the service itself is
	// probably cut off from the internet.
	checkHealthy = 24 * time.Hour
	// freeSilentAfter: a name that lapsed without ever answering since its
	// claim goes back to everyone this long after lapsing, not freeAfter.
	freeSilentAfter = 7 * 24 * time.Hour
	// A refresh or claim of a name that lapsed for not answering checks
	// its address right away, at most this often per name.
	rechecksBurst, rechecksPerHour = 3, 6
	maxAliveAnswer                 = 1 << 10
)

// checkRow is what a liveness check needs of a name.
type checkRow struct {
	name, key, ipv4, ipv6 string
}

// checkNames checks the addresses of the active names that are due, most
// overdue first. Names that share an address wait for later ticks without
// holding up the others.
func (s *Service) checkNames(ctx context.Context, now time.Time) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, key, ipv4, ipv6 FROM names WHERE state = ? AND checked_at <= ?
		ORDER BY checked_at, name`, names.StateActive, now.Add(-checkEvery).Unix())
	if err != nil {
		s.logErr("Could not list names to check", err)
		return
	}
	var due []checkRow
	seen := map[string]bool{}
	for len(due) < checksPerTick && rows.Next() {
		var c checkRow
		if err := rows.Scan(&c.name, &c.key, &c.ipv4, &c.ipv6); err != nil {
			s.logErr("Could not list names to check", err)
			break
		}
		first := cmp.Or(c.ipv4, c.ipv6)
		if seen[first] {
			continue
		}
		seen[first] = true
		due = append(due, c)
	}
	s.logErr("Could not list names to check", rows.Err())
	rows.Close()
	answered := make([]bool, len(due))
	sem := make(chan struct{}, checkWorkers)
	var wg sync.WaitGroup
	for i, c := range due {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			answered[i] = s.probe(ctx, c)
		}()
	}
	wg.Wait()
	for i, c := range due {
		s.logErr("Could not record a liveness check", s.recordCheck(ctx, c, answered[i], now.Unix()))
	}
}

// probe checks a name's IPv4 address, then its IPv6 address.
func (s *Service) probe(ctx context.Context, c checkRow) bool {
	var errs []error
	for _, a := range []string{c.ipv4, c.ipv6} {
		addr, err := netip.ParseAddr(a)
		if err != nil {
			continue
		}
		if err := s.askAlive(ctx, c.name, c.key, addr); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", addr, err))
			continue
		}
		return true
	}
	s.log.Info("A name's address did not answer the liveness check", "name", c.name, "error", errors.Join(errs...))
	return false
}

// recordCheck keeps the result unless the name changed hands, lapsed or
// moved to other addresses meanwhile; then it is checked again next tick.
func (s *Service) recordCheck(ctx context.Context, c checkRow, answered bool, now int64) error {
	var err error
	if answered {
		_, err = s.db.ExecContext(ctx, `UPDATE names SET checked_at = ?, alive_at = ?, failed_checks = 0
			WHERE name = ? AND key = ? AND state = ? AND ipv4 = ? AND ipv6 = ?`, now, now, c.name, c.key, names.StateActive, c.ipv4, c.ipv6)
	} else {
		_, err = s.db.ExecContext(ctx, `UPDATE names SET checked_at = ?, failed_checks = failed_checks + 1
			WHERE name = ? AND key = ? AND state = ? AND ipv4 = ? AND ipv6 = ?`, now, c.name, c.key, names.StateActive, c.ipv4, c.ipv6)
	}
	return err
}

// askAlive asks the dashboard at addr to sign a fresh nonce for name and
// checks the answer against the name's key.
func (s *Service) askAlive(ctx context.Context, name, key string, addr netip.Addr) error {
	if !publicUnicast(addr) || inAny(cloudflareEdge, addr) {
		return errors.New("not a public address")
	}
	pub, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("the name's key is not valid")
	}
	var b [32]byte
	_, _ = rand.Read(b[:])
	nonce := base64.RawURLEncoding.EncodeToString(b[:])
	fqdn := names.Address(name, s.base)
	target := netip.AddrPortFrom(addr, names.AlivePort).String()
	dial := s.cfg.dialAlive
	hc := &http.Client{
		Timeout:       checkTimeout,
		CheckRedirect: noRedirects,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx, "tcp", target) },
			// The dashboard's certificate may be self-signed or for another
			// name; the signature over the fresh nonce is the proof.
			TLSClientConfig:        &tls.Config{ServerName: fqdn, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:    5 * time.Second,
			ResponseHeaderTimeout:  5 * time.Second,
			MaxResponseHeaderBytes: 8 << 10,
			DisableKeepAlives:      true,
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+net.JoinHostPort(fqdn, strconv.Itoa(names.AlivePort))+names.AlivePath+nonce, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "playkeeper-names (https://github.com/CIYAhq/playkeeper)")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the dashboard answered HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAliveAnswer+1))
	if err != nil {
		return err
	}
	if resp.ContentLength > maxAliveAnswer || len(body) > maxAliveAnswer {
		return errors.New("the answer is larger than expected")
	}
	var a names.Alive
	if err := json.Unmarshal(body, &a); err != nil {
		return fmt.Errorf("the answer is not valid: %w", err)
	}
	if a.Name != name || !names.VerifyAlive(ed25519.PublicKey(pub), s.base, name, nonce, a.Signature) {
		return errors.New("the answer is not signed with the name's key")
	}
	return nil
}

// recheck checks addr right away for a name that lapsed because its
// address did not answer, and reports whether it answers now.
func (s *Service) recheck(ctx context.Context, row *nameRow, addr netip.Addr) (bool, error) {
	if row.State != names.StateLapsed || row.LapseReason != names.LapseNoAnswer {
		return false, nil
	}
	if ok, wait := s.rechecks.allow(row.Name); !ok {
		return false, rateLimited(wait, "checks of this name's address")
	}
	if err := s.askAlive(ctx, row.Name, row.Key, addr); err != nil {
		s.log.Info("A lapsed name's address did not answer the liveness check", "name", row.Name, "ip", addr.String(), "error", err)
		return false, nil
	}
	return true, nil
}

// checksWork reports whether some address answered a liveness check
// within checkHealthy.
func (s *Service) checksWork(ctx context.Context, now time.Time) (bool, error) {
	var last int64
	err := s.db.QueryRowContext(ctx, `SELECT coalesce(max(alive_at), 0) FROM names`).Scan(&last)
	return last > now.Add(-checkHealthy).Unix(), err
}

// lapseSilent takes the records away from names whose address has not
// answered for unansweredAfter, unless no address answered lately.
func (s *Service) lapseSilent(ctx context.Context, now time.Time) {
	silent, err := s.nameStrings(ctx, `SELECT name FROM names WHERE state = ? AND failed_checks >= ? AND max(alive_at, claimed_at) <= ?`,
		names.StateActive, minFailedChecks, now.Add(-unansweredAfter).Unix())
	s.logErr("Could not list names that do not answer", err)
	if len(silent) == 0 {
		return
	}
	ok, err := s.checksWork(ctx, now)
	if err != nil {
		s.logErr("Could not read when a name last answered", err)
		return
	}
	if !ok {
		s.alerts.send(alertChecks, fmt.Sprintf("%d names have not answered the liveness check for %d days, but they do not lapse because no name answered for a day: the service may not reach port %d on the internet. See \"Names must answer\" in services/names/README.md.",
			len(silent), int(unansweredAfter.Hours()/24), names.AlivePort), "names", len(silent))
		return
	}
	for _, name := range silent {
		s.lapse(ctx, now, name, names.LapseNoAnswer, "A name's address did not answer for a week; its records are removed")
	}
}

// lapse takes the records away from an active name.
func (s *Service) lapse(ctx context.Context, now time.Time, name, reason, msg string) {
	err := s.bump(ctx, name, `UPDATE names SET state = ?, lapsed_at = ?, lapse_reason = ? WHERE name = ? AND state = ?`,
		names.StateLapsed, now.Unix(), reason, name, names.StateActive)
	if err == nil {
		_, err = s.db.ExecContext(ctx, `DELETE FROM challenges WHERE name = ?`, name)
	}
	s.logErr("Could not lapse a name", err)
	if err == nil {
		s.log.Info(msg, "name", name)
		_ = s.sync(ctx, name)
	}
}

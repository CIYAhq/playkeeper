package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/names"
)

// recordTTL is the TTL of every record the service creates, in seconds:
// Cloudflare's minimum, so a changed address reaches players quickly.
const recordTTL = 60

// marker is the comment on every record the service creates. A record is
// only ever changed or removed when it carries the marker of the name it
// sits under, so records made by hand are never touched.
func marker(name string) string { return "playkeeper-names " + name }

// owns reports whether r is a record the service manages for name: name is
// a valid, unreserved name, r carries name's marker, and r's type and name
// are one of the service's own patterns under name's address:
//
//	A, AAAA  <name>.<base>
//	TXT      _acme-challenge.<name>.<base>
//	SRV      _minecraft._tcp.<name>.<base> and _minecraft._tcp.<label>.<name>.<base>
//
// The apex, www, names and mail records never match: they are not of these
// forms, or their name is reserved.
func (s *Service) owns(name string, r cfRecord) bool {
	if names.CheckName(name) != nil || reservedName(name) || r.Comment != marker(name) {
		return false
	}
	fqdn := names.Address(name, s.base)
	rn := strings.TrimSuffix(strings.ToLower(r.Name), ".")
	switch r.Type {
	case "A", "AAAA":
		return rn == fqdn
	case "TXT":
		return rn == names.ChallengeFQDN(name, s.base)
	case "SRV":
		rest, ok := strings.CutSuffix(rn, "."+fqdn)
		if !ok {
			return false
		}
		if rest == "_minecraft._tcp" {
			return true
		}
		label, ok := strings.CutPrefix(rest, "_minecraft._tcp.")
		return ok && names.CheckServerLabel(label) == nil
	}
	return false
}

// errRefused means the guard stopped a change to a record the service does
// not manage. It is a bug if it ever happens; the change is not made.
var errRefused = errors.New("the names service refused to touch a DNS record it does not manage")

func (s *Service) refuse(op, name string, r cfRecord) error {
	s.log.Error("Refused to touch a DNS record the service does not manage", "op", op, "name", name, "record", r.Name, "type", r.Type, "id", r.ID)
	return errRefused
}

// The three functions below are the only callers of Cloudflare's mutating
// methods.

func (s *Service) createRecord(ctx context.Context, name string, r cfRecord) error {
	r.ID, r.Comment, r.TTL = "", marker(name), recordTTL
	if !s.owns(name, r) {
		return s.refuse("create", name, r)
	}
	return s.cf.create(ctx, r)
}

func (s *Service) updateRecord(ctx context.Context, name string, old, r cfRecord) error {
	r.ID, r.Comment, r.TTL = "", marker(name), recordTTL
	if old.ID == "" || !s.owns(name, old) || !s.owns(name, r) || old.Type != r.Type || !strings.EqualFold(old.Name, r.Name) {
		return s.refuse("update", name, old)
	}
	return s.cf.update(ctx, old.ID, r)
}

func (s *Service) deleteRecord(ctx context.Context, name string, old cfRecord) error {
	if old.ID == "" || !s.owns(name, old) {
		return s.refuse("delete", name, old)
	}
	return s.cf.delete(ctx, old.ID)
}

// desired is the set of records a name should have.
type desired struct {
	a, aaaa string
	txt     []string
	srv     map[string]int // SRV record name -> port
}

func (s *Service) desiredFor(ctx context.Context, name string) (desired, int64, error) {
	row, err := s.getName(ctx, s.db, name)
	if err != nil || row == nil {
		return desired{}, 0, err
	}
	if row.State != names.StateActive {
		return desired{}, row.Version, nil
	}
	d := desired{a: row.IPv4, aaaa: row.IPv6, srv: map[string]int{}}
	chs, err := s.challengeRows(ctx, name)
	if err != nil {
		return desired{}, 0, err
	}
	now := s.now().Unix()
	for _, ch := range chs {
		if ch.ExpiresAt > now {
			d.txt = append(d.txt, ch.Value)
		}
	}
	servers, err := s.serverRows(ctx, name)
	if err != nil {
		return desired{}, 0, err
	}
	for _, sv := range servers {
		d.srv[srvName(sv.Label, name, s.base)] = sv.Port
	}
	return d, row.Version, nil
}

func srvName(label, name, base string) string {
	return "_minecraft._tcp." + names.ServerAddress(label, name, base)
}

// foreignRecordError: someone made a record by hand where the name's
// address goes, so the service leaves the address alone.
type foreignRecordError struct{ r cfRecord }

func (e *foreignRecordError) Error() string {
	return fmt.Sprintf("%s has a hand-made %s record, so the names service does not add its own", e.r.Name, e.r.Type)
}

func sameName(a, b string) bool {
	return strings.EqualFold(strings.TrimSuffix(a, "."), strings.TrimSuffix(b, "."))
}

// apply makes the records the service manages for name match d. Records
// under the name that the service does not manage are left as they are;
// where one sits at the name's address or at a server address, the service
// leaves that address alone rather than answer next to it.
func (s *Service) apply(ctx context.Context, name string, d desired) error {
	fqdn := names.Address(name, s.base)
	recs, err := s.cf.under(ctx, fqdn)
	if err != nil {
		return err
	}
	var mine []cfRecord
	var errs []error
	addrTaken, srvTaken := false, map[string]bool{}
	for _, r := range recs {
		rn := strings.TrimSuffix(strings.ToLower(r.Name), ".")
		_, wantSRV := d.srv[rn]
		switch {
		case s.owns(name, r):
			mine = append(mine, r)
		case rn == fqdn && (d.a != "" || d.aaaa != "") && (r.Type == "A" || r.Type == "AAAA" || r.Type == "CNAME"):
			addrTaken = true
			errs = append(errs, &foreignRecordError{r})
		case wantSRV && (r.Type == "SRV" || r.Type == "CNAME"):
			srvTaken[rn] = true
			errs = append(errs, &foreignRecordError{r})
		}
	}
	of := func(typ string) []cfRecord {
		var out []cfRecord
		for _, r := range mine {
			if r.Type == typ {
				out = append(out, r)
			}
		}
		return out
	}
	if !addrTaken {
		errs = append(errs,
			s.syncAddress(ctx, name, "A", d.a, of("A")),
			s.syncAddress(ctx, name, "AAAA", d.aaaa, of("AAAA")))
	}
	errs = append(errs,
		s.syncTXT(ctx, name, d.txt, of("TXT")),
		s.syncSRV(ctx, name, d.srv, srvTaken, of("SRV")))
	return errors.Join(errs...)
}

func (s *Service) syncAddress(ctx context.Context, name, typ, want string, have []cfRecord) error {
	var errs []error
	if want != "" {
		off := false
		r := cfRecord{Type: typ, Name: names.Address(name, s.base), Content: want, Proxied: &off}
		if len(have) == 0 {
			return s.createRecord(ctx, name, r)
		}
		first := have[0]
		have = have[1:]
		if !sameAddr(first.Content, want) || first.Proxied == nil || *first.Proxied || first.TTL != recordTTL {
			errs = append(errs, s.updateRecord(ctx, name, first, r))
		}
	}
	for _, r := range have {
		errs = append(errs, s.deleteRecord(ctx, name, r))
	}
	return errors.Join(errs...)
}

func sameAddr(a, b string) bool {
	x, err1 := netip.ParseAddr(a)
	y, err2 := netip.ParseAddr(b)
	return err1 == nil && err2 == nil && x == y
}

// txtValue undoes the quotes Cloudflare may show TXT content in.
func txtValue(content string) string {
	if len(content) >= 2 && content[0] == '"' && content[len(content)-1] == '"' {
		return content[1 : len(content)-1]
	}
	return content
}

func (s *Service) syncTXT(ctx context.Context, name string, want []string, have []cfRecord) error {
	wanted := map[string]bool{}
	for _, v := range want {
		wanted[v] = true
	}
	var errs []error
	present := map[string]bool{}
	for _, r := range have {
		v := txtValue(r.Content)
		if wanted[v] && !present[v] {
			present[v] = true
			continue
		}
		errs = append(errs, s.deleteRecord(ctx, name, r))
	}
	for _, v := range want {
		if !present[v] {
			present[v] = true
			errs = append(errs, s.createRecord(ctx, name, cfRecord{Type: "TXT", Name: names.ChallengeFQDN(name, s.base), Content: v}))
		}
	}
	return errors.Join(errs...)
}

// syncSRV leaves the SRV record names in taken as they are.
func (s *Service) syncSRV(ctx context.Context, name string, want map[string]int, taken map[string]bool, have []cfRecord) error {
	target := names.Address(name, s.base)
	record := func(rn string, port int) cfRecord {
		return cfRecord{Type: "SRV", Name: rn, Data: &cfSRV{Priority: 0, Weight: 0, Port: port, Target: target}}
	}
	var errs []error
	done := map[string]bool{}
	maps.Copy(done, taken)
	for _, r := range have {
		rn := strings.TrimSuffix(strings.ToLower(r.Name), ".")
		port, ok := want[rn]
		switch {
		case taken[rn]:
		case !ok || done[rn]:
			errs = append(errs, s.deleteRecord(ctx, name, r))
		case r.Data == nil || r.Data.Port != port || !sameName(r.Data.Target, target) || r.TTL != recordTTL:
			done[rn] = true
			errs = append(errs, s.updateRecord(ctx, name, r, record(rn, port)))
		default:
			done[rn] = true
		}
	}
	for rn, port := range want {
		if !done[rn] {
			errs = append(errs, s.createRecord(ctx, name, record(rn, port)))
		}
	}
	return errors.Join(errs...)
}

// sync brings name's records in Cloudflare in line with the database. On
// failure the name stays marked for the retry job, with back-off.
func (s *Service) sync(ctx context.Context, name string) error {
	defer s.locks.lock(name)()
	d, version, err := s.desiredFor(ctx, name)
	if err != nil {
		return err
	}
	if err = s.ensureZone(ctx); err == nil {
		err = s.apply(ctx, name, d)
	}
	if err != nil {
		s.log.Warn("Could not update DNS records; will retry", "name", name, "error", err)
		_, dbErr := s.db.ExecContext(ctx, `UPDATE names SET sync_failures = sync_failures + 1,
			sync_after = ? + min(60 * (1 << min(sync_failures, 6)), 3600) WHERE name = ?`, s.now().Unix(), name)
		return errors.Join(err, dbErr)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE names SET synced = ?, sync_failures = 0, sync_after = 0 WHERE name = ? AND synced < ?`, version, name, version)
	return err
}

// syncDetached runs sync with its own deadline, so a client that hangs up
// does not leave a change half made.
func (s *Service) syncDetached(ctx context.Context, name string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_ = s.sync(ctx, name)
}

// errZoneMismatch: the zone ID belongs to another domain. The service then
// changes nothing, since its patterns are only safe in its own zone.
type errZoneMismatch struct{ got, want string }

func (e *errZoneMismatch) Error() string {
	return fmt.Sprintf("the Cloudflare zone %s is %q, not %q: check %s and %s", EnvZone, e.got, e.want, EnvZone, EnvBase)
}

// ensureZone checks once that the zone is the base domain's before the
// first change.
func (s *Service) ensureZone(ctx context.Context) error {
	if s.zoneOK.Load() {
		return nil
	}
	z, err := s.cf.getZone(ctx)
	if err != nil {
		return err
	}
	if !strings.EqualFold(z.Name, s.base) {
		return &errZoneMismatch{got: z.Name, want: s.base}
	}
	if z.Status != "active" {
		s.log.Warn("The Cloudflare zone is not active yet, so its records are not live; finish moving the nameservers (see services/names/README.md)", "status", z.Status)
	}
	s.zoneOK.Store(true)
	return nil
}

// dnsUnavailable is how a Cloudflare failure reaches a client that has to
// wait for it.
func dnsUnavailable(err error) error {
	e := &names.Error{Status: 503, Code: names.CodeDNSUnavailable, RetryAfter: time.Minute,
		Message: "The names service cannot reach its DNS provider right now.",
		Hint:    "Addresses that already work keep working; try again in a few minutes."}
	var ce *cfError
	if errors.As(err, &ce) && ce.RetryAfter > 0 {
		e.RetryAfter = ce.RetryAfter
	}
	return e
}

// checkFree makes sure a new name has no records made by hand at or under
// its address, and that the zone has room for its records.
func (s *Service) checkFree(ctx context.Context, name string) error {
	if err := s.ensureZone(ctx); err != nil {
		s.log.Warn("Could not check the zone before a claim", "name", name, "error", err)
		return dnsUnavailable(err)
	}
	recs, err := s.cf.under(ctx, names.Address(name, s.base))
	if err != nil {
		s.log.Warn("Could not check existing records before a claim", "name", name, "error", err)
		return dnsUnavailable(err)
	}
	for _, r := range recs {
		if !s.owns(name, r) {
			s.log.Info("Refused a claim: the name has records made by hand", "name", name, "record", r.Name, "type", r.Type)
			return &names.Error{Status: 409, Code: names.CodeNameInUse, Params: map[string]any{"name": name},
				Message: names.Address(name, s.base) + " is already in use.", Hint: "Choose another name."}
		}
	}
	return s.checkRoom(ctx, 2, forName)
}

// recordUse is what a change needs records for. A filling zone refuses
// server addresses first, then new names, and certificate challenges last.
type recordUse int

const (
	forServer recordUse = iota
	forName
	forChallenge
)

// checkRoom refuses a change that needs n more records when it would leave
// the zone fewer free records than use must leave: challenges leave the
// owner's reserve, new names also challengeRoom, and server addresses also
// nameRoom. The zone holds the lower of Cloudflare's quota and RecordQuota.
func (s *Service) checkRoom(ctx context.Context, n int, use recordUse) error {
	u, err := s.cf.usage(ctx)
	if err != nil {
		s.log.Warn("Could not read the zone's record usage", "error", err)
		return dnsUnavailable(err)
	}
	quota := s.cfg.RecordQuota
	if u.Quota != nil && *u.Quota < quota {
		quota = *u.Quota
	}
	free := quota - u.Usage
	if free-1 < s.cfg.RecordReserve+challengeRoom+nameRoom {
		s.alerts.send(alertZoneNearlyFull, fmt.Sprintf("The DNS zone is nearly full (%d of %d records used), so new server addresses are refused. See \"Zone full\" in services/names/README.md.", u.Usage, quota),
			"used", u.Usage, "quota", quota)
	}
	keep := s.cfg.RecordReserve
	if use != forChallenge {
		keep += challengeRoom
	}
	if use == forServer {
		keep += nameRoom
	}
	if free-n >= keep {
		return nil
	}
	if use == forChallenge {
		s.alerts.send(alertChallenges, fmt.Sprintf("The DNS zone is full (%d of %d records used), so certificate challenges are refused and dashboards cannot get certificates. See \"Zone full\" in services/names/README.md.", u.Usage, quota),
			"used", u.Usage, "quota", quota)
		return &names.Error{Status: 507, Code: names.CodeZoneFull,
			Message: "The names service cannot publish certificate challenges right now, because its DNS zone is full.",
			Hint:    "Try again later."}
	}
	if use == forName {
		s.alerts.send(alertZoneFull, fmt.Sprintf("The DNS zone is full (%d of %d records used), so new names are refused. See \"Zone full\" in services/names/README.md.", u.Usage, quota),
			"used", u.Usage, "quota", quota)
	}
	return &names.Error{Status: 507, Code: names.CodeZoneFull,
		Message: "No more " + s.base + " addresses can be handed out right now.",
		Hint:    "Use your own domain instead, or try again later."}
}

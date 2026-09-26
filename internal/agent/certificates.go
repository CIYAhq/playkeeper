package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/certs"
	"github.com/CIYAhq/playkeeper/internal/names"
)

// The dashboard's certificate for the machine's name. The agent gets it
// from Let's Encrypt and saves it in the certificates directory, where the
// panel serves it; the certificates table keeps how getting and renewing it
// went.

const kvACMETerms = "acme_terms"

// certRow is a row of the certificates table.
type certRow struct {
	name, source, challenge string
	status                  certs.Status
}

func (a *Agent) loadCertificate(name string) *certRow {
	r := certRow{}
	var namesJSON, file, issuer, serial, sha, problem string
	var notBefore, notAfter, renewAt, last, next sql.NullInt64
	err := a.db.QueryRow(`SELECT name, names, source, challenge, file, not_before, not_after, renew_at, issuer, serial, sha256,
		last_attempt, next_attempt, failures, problem FROM certificates WHERE name = ?`, name).
		Scan(&r.name, &namesJSON, &r.source, &r.challenge, &file, &notBefore, &notAfter, &renewAt, &issuer, &serial, &sha,
			&last, &next, &r.status.Failures, &problem)
	if err != nil {
		return nil
	}
	_ = json.Unmarshal([]byte(namesJSON), &r.status.Names)
	if notAfter.Valid {
		r.status.Certificate = &certs.Certificate{Names: r.status.Names, File: file, NotBefore: fromMillis(notBefore), NotAfter: fromMillis(notAfter),
			RenewAt: fromMillis(renewAt), Issuer: issuer, Serial: serial, SHA256: sha}
	}
	r.status.LastAttempt, r.status.NextAttempt = fromMillis(last), fromMillis(next)
	if problem != "" {
		var p certs.Problem
		if json.Unmarshal([]byte(problem), &p) == nil {
			r.status.Problem = &p
		}
	}
	return &r
}

func (a *Agent) saveCertificate(r *certRow) error {
	namesJSON, err := json.Marshal(r.status.Names)
	if err != nil {
		return err
	}
	var file, issuer, serial, sha, problem string
	var notBefore, notAfter, renewAt any
	if c := r.status.Certificate; c != nil {
		file, issuer, serial, sha = c.File, c.Issuer, c.Serial, c.SHA256
		notBefore, notAfter, renewAt = c.NotBefore.UnixMilli(), c.NotAfter.UnixMilli(), c.RenewAt.UnixMilli()
	}
	if p := r.status.Problem; p != nil {
		b, err := json.Marshal(p)
		if err != nil {
			return err
		}
		problem = string(b)
	}
	_, err = a.db.Exec(`INSERT INTO certificates(name, names, source, challenge, file, not_before, not_after, renew_at, issuer, serial, sha256,
		last_attempt, next_attempt, failures, problem) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET names = excluded.names, source = excluded.source, challenge = excluded.challenge, file = excluded.file,
		not_before = excluded.not_before, not_after = excluded.not_after, renew_at = excluded.renew_at, issuer = excluded.issuer,
		serial = excluded.serial, sha256 = excluded.sha256, last_attempt = excluded.last_attempt, next_attempt = excluded.next_attempt,
		failures = excluded.failures, problem = excluded.problem`,
		r.name, string(namesJSON), r.source, r.challenge, file, notBefore, notAfter, renewAt, issuer, serial, sha,
		toMillis(r.status.LastAttempt), toMillis(r.status.NextAttempt), r.status.Failures, problem)
	return err
}

func fromMillis(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.UnixMilli(v.Int64).UTC()
}

func toMillis(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

// forgetCertificate deletes the certificate of a name the machine no longer
// uses, so the panel stops serving it.
func (a *Agent) forgetCertificate(name string) {
	if name == "" {
		return
	}
	if _, err := a.db.Exec(`DELETE FROM certificates WHERE name = ?`, name); err != nil {
		a.log.Warn("could not forget a certificate", "name", name, "err", err)
	}
	if n, err := certs.NormalizeName(name); err == nil && n == name {
		if err := os.Remove(filepath.Join(a.cfg.CertsDir(), name+".pem")); err != nil && !os.IsNotExist(err) {
			a.log.Warn("could not delete a certificate", "name", name, "err", err)
		}
	}
}

// certificateDue reports whether the machine's name needs a certificate
// now: it has none, it is time to renew, or the wait after a failed attempt
// is over.
func (a *Agent) certificateDue(st addressState) bool {
	if st.Host == "" {
		return false
	}
	row := a.loadCertificate(st.Host)
	return row == nil || row.status.Due(a.now())
}

// namesChallenger publishes DNS-01 records through the names service. Its
// certificate_limit refusal becomes a Problem that waits until the service
// allows the certificate again.
type namesChallenger struct {
	c   *names.Client
	now func() time.Time
}

func (n namesChallenger) SetTXT(ctx context.Context, fqdn, value string) error {
	err := n.c.SetTXT(ctx, fqdn, value)
	var ne *names.Error
	if !errors.As(err, &ne) || ne.Code != names.CodeCertificateLimit {
		return err
	}
	now := n.now().UTC()
	retry := now.Add(time.Hour)
	if s, ok := ne.Params["retryAt"].(string); ne.RetryAfter > 0 {
		retry = now.Add(ne.RetryAfter)
	} else if t, perr := time.Parse(time.RFC3339, s); ok && perr == nil {
		retry = t
	}
	if earliest := now.Add(time.Minute); retry.Before(earliest) {
		retry = earliest
	}
	if latest := now.Add(8 * 24 * time.Hour); retry.After(latest) {
		retry = latest
	}
	retry = retry.Add(time.Second - 1).Truncate(time.Second)
	scope, _ := ne.Params["scope"].(string)
	if scope != "name" && scope != "all" {
		scope = ""
	}
	return certs.CertificateLimit(err, strings.TrimPrefix(fqdn, "_acme-challenge."), scope, retry)
}

func (n namesChallenger) ClearTXT(ctx context.Context, fqdn, value string) error {
	return n.c.ClearTXT(ctx, fqdn, value)
}

// issueCertificate gets or renews the certificate for the machine's name:
// over DNS-01 through the names service for a free address, or over HTTP-01
// on port 80 for an own domain, and only once the public DNS shows that the
// domain points here. Port 80 is open only while Let's Encrypt checks.
func (a *Agent) issueCertificate(ctx context.Context, h *opHandle) error {
	st := a.address()
	req := certs.Request{Names: []string{st.Host}, Dir: a.cfg.CertsDir(), Owner: a.certOwner()}
	challenge := ""
	switch st.Kind {
	case api.AddressPlaykeeper:
		c, err := a.namesClient(true)
		if err != nil {
			return err
		}
		req.DNS01 = &certs.DNS01{Challenger: namesChallenger{c: c, now: a.now}}
		challenge = "dns-01"
	case api.AddressOwn:
		h.phase("checking")
		nc := certs.CheckName(ctx, a.opts.Resolver, st.Host, a.expectedAddrs(st))
		a.saveNameCheck(st.Host, nc)
		if !nc.OK {
			return &apiError{Status: http.StatusConflict, Code: nc.Code, Msg: nc.Message, Hint: nc.Hint, Params: noteParams(nc.Params)}
		}
		req.HTTP01 = &certs.HTTP01Responder{Addr: a.opts.HTTP01Addr}
		challenge = "http-01"
	default:
		return errConflict("This machine has no address to get a certificate for.", "")
	}
	h.phase("certificate")
	row := a.loadCertificate(st.Host)
	if row == nil {
		row = &certRow{name: st.Host, status: certs.Status{Names: []string{st.Host}}}
	}
	row.source, row.challenge = st.Kind, challenge
	cert, err := a.opts.Issue(ctx, a.issuer(), req)
	row.status.Record(a.now().UTC(), cert, err)
	if serr := a.saveCertificate(row); serr != nil {
		return serr
	}
	if err != nil {
		p := row.status.Problem
		return &apiError{Status: http.StatusBadGateway, Code: p.Code, Msg: p.Message, Hint: p.Hint}
	}
	h.set("notAfter", cert.NotAfter)
	return nil
}

// saveNameCheck keeps the result of looking up the own domain's name
// alone, next to the last look at its SRV records. A result that isn't
// ready brings the loop's next full look forward, as checkOwn's would.
func (a *Agent) saveNameCheck(host string, nc certs.NameCheck) {
	now := a.now().UTC()
	saved, ready := false, false
	_ = a.updateAddress(func(st *addressState) {
		if st.Kind != api.AddressOwn || st.Host != host {
			return
		}
		c := api.AddressCheck{}
		if st.Check != nil {
			c = *st.Check
		}
		c.At, c.Name = now, nameCheck(nc)
		c.Ready = nc.OK && !slices.ContainsFunc(c.Records, func(r api.RecordCheck) bool { return !r.OK })
		st.Check = &c
		saved, ready = true, c.Ready
	})
	if !saved || ready {
		return
	}
	a.addr.mu.Lock()
	if soon := now.Add(ownRecheckPending); soon.Before(a.addr.recheck) {
		a.addr.recheck = soon
	}
	a.addr.mu.Unlock()
}

// issue is the default Options.Issue: Let's Encrypt, or the directory the
// config names. Once an admin accepted the terms on the Address page, a new
// account agrees to the CA's current terms.
func (a *Agent) issue(ctx context.Context, is *certs.Issuer, req certs.Request) (*certs.Certificate, error) {
	if is.AgreedTerms == "" && a.termsAccepted() != nil {
		if terms, err := is.Terms(ctx); err == nil {
			is.AgreedTerms = terms
		}
	}
	return is.Issue(ctx, req)
}

// issuer is the ACME client for the machine's certificate.
func (a *Agent) issuer() *certs.Issuer {
	return &certs.Issuer{
		DirectoryURL:   a.cfg.ACMEDirectoryURL,
		AccountKeyFile: filepath.Join(a.cfg.AgentDir(), "acme-account.key"),
		Email:          a.cfg.ACMEEmail,
		AgreedTerms:    a.cfg.ACMEAgreedTerms,
		Now:            a.now,
	}
}

// certOwner lets the panel's group read the saved certificates when the
// agent runs as root; otherwise (dev) the panel runs as the same user.
func (a *Agent) certOwner() *certs.Owner {
	if os.Geteuid() != 0 {
		return nil
	}
	u, err := user.Lookup(a.cfg.PanelUser)
	if err != nil {
		return nil
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return nil
	}
	return &certs.Owner{UID: 0, GID: gid}
}

type termsAcceptance struct {
	At    time.Time `json:"at"`
	Actor string    `json:"actor"`
}

// acceptTerms records that an admin accepted Let's Encrypt's terms.
func (a *Agent) acceptTerms(actor string) {
	if a.termsAccepted() != nil {
		return
	}
	b, err := json.Marshal(termsAcceptance{At: a.now().UTC(), Actor: actor})
	if err != nil {
		return
	}
	if err := a.kvSet(kvACMETerms, string(b)); err != nil {
		a.log.Warn("could not record the accepted terms", "err", err)
		return
	}
	a.audit(actor, "certificate.terms", "letsencrypt", "accepted", "")
}

func (a *Agent) termsAccepted() *time.Time {
	v, ok, err := a.kvGet(kvACMETerms)
	if err != nil || !ok {
		return nil
	}
	var t termsAcceptance
	if json.Unmarshal([]byte(v), &t) != nil || t.At.IsZero() {
		return nil
	}
	return &t.At
}

func certificateView(r *certRow) *api.CertificateStatus {
	s := r.status
	v := &api.CertificateStatus{Names: s.Names, Challenge: r.challenge, Failures: s.Failures, LastAttempt: optTime(s.LastAttempt)}
	if c := s.Certificate; c != nil {
		v.NotBefore, v.NotAfter, v.RenewAt, v.Issuer = optTime(c.NotBefore), optTime(c.NotAfter), optTime(c.RenewAt), c.Issuer
	}
	if s.Failures > 0 {
		v.NextAttempt = optTime(s.NextAttempt)
	}
	if p := s.Problem; p != nil {
		v.Problem = &api.CertificateProblem{Note: api.Note(p.Note), RetryAt: optTime(p.RetryAt), NeedsAction: p.NeedsAction, Detail: p.Detail}
	}
	return v
}

func optTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

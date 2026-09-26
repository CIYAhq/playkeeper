package certs

import "time"

// RenewAt is when to renew a certificate valid from notBefore to notAfter:
// after two thirds of its lifetime, or half of it for certificates shorter
// than ten days. For Let's Encrypt's 90-day certificates that leaves 30 days
// to retry, and it follows the CA as lifetimes shrink. ACME Renewal
// Information (ARI) is not used: golang.org/x/crypto/acme does not support
// it.
func RenewAt(notBefore, notAfter time.Time) time.Time {
	life := notAfter.Sub(notBefore)
	if life <= 0 {
		return notBefore
	}
	if life < 10*24*time.Hour {
		return notBefore.Add(life / 2)
	}
	return notBefore.Add(life / 3 * 2)
}

// Backoff is how long to wait after the given number of failed attempts in
// a row: 15 minutes after the first, doubling up to a day.
func Backoff(failures int) time.Duration {
	if failures < 1 {
		return 0
	}
	d := 15 * time.Minute
	for i := 1; i < failures && d < 24*time.Hour; i++ {
		d *= 2
	}
	return min(d, 24*time.Hour)
}

// NextAttempt is when to try again after an attempt at last that failed with
// p, the failures-th failure in a row. It never comes before p.RetryAt.
func NextAttempt(last time.Time, failures int, p *Problem) time.Time {
	next := last.Add(Backoff(failures))
	if p != nil && p.RetryAt.After(next) {
		next = p.RetryAt
	}
	return next
}

// Status is what the caller keeps about one certificate between attempts,
// one row of its certificates table.
type Status struct {
	Names       []string     `json:"names"`
	Certificate *Certificate `json:"certificate,omitempty"`
	LastAttempt time.Time    `json:"lastAttempt,omitzero"`
	NextAttempt time.Time    `json:"nextAttempt,omitzero"`
	Failures    int          `json:"failures,omitempty"`
	Problem     *Problem     `json:"problem,omitempty"`
}

// Due reports whether it is time to get or renew the certificate.
func (s *Status) Due(now time.Time) bool {
	switch {
	case s.Failures > 0:
		return !now.Before(s.NextAttempt)
	case s.Certificate == nil:
		return true
	}
	return !now.Before(s.Certificate.RenewAt)
}

// Record updates s after an attempt at now that returned cert and err.
func (s *Status) Record(now time.Time, cert *Certificate, err error) {
	s.LastAttempt = now
	if err == nil && cert != nil {
		s.Certificate, s.Failures, s.Problem = cert, 0, nil
		s.NextAttempt = cert.RenewAt
		return
	}
	s.Failures++
	s.Problem = Explain(err, now)
	if s.Problem == nil {
		s.Problem = newProblem(nil, CodeFailed, nil)
	}
	s.NextAttempt = NextAttempt(now, s.Failures, s.Problem)
}

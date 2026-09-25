package certs

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"golang.org/x/crypto/acme"
)

func TestRenewAt(t *testing.T) {
	nb := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		life time.Duration
		want time.Duration
	}{
		{90 * 24 * time.Hour, 60 * 24 * time.Hour},
		{45 * 24 * time.Hour, 30 * 24 * time.Hour},
		{160 * time.Hour, 80 * time.Hour},
		{0, 0},
		{-time.Hour, 0},
	}
	for _, c := range cases {
		if got := RenewAt(nb, nb.Add(c.life)); !got.Equal(nb.Add(c.want)) {
			t.Errorf("RenewAt(life %s) = %s, want %s after notBefore", c.life, got.Sub(nb), c.want)
		}
	}
}

func TestBackoff(t *testing.T) {
	for failures, want := range map[int]time.Duration{
		-1: 0, 0: 0, 1: 15 * time.Minute, 2: 30 * time.Minute, 3: time.Hour, 7: 16 * time.Hour, 8: 24 * time.Hour, 1000: 24 * time.Hour,
	} {
		if got := Backoff(failures); got != want {
			t.Errorf("Backoff(%d) = %s, want %s", failures, got, want)
		}
	}
	last := time.Date(2025, 1, 18, 20, 0, 0, 0, time.UTC)
	if got := NextAttempt(last, 2, nil); !got.Equal(last.Add(30 * time.Minute)) {
		t.Errorf("NextAttempt = %s", got)
	}
	limited := &Problem{RetryAt: last.Add(5 * time.Hour)}
	if got := NextAttempt(last, 1, limited); !got.Equal(limited.RetryAt) {
		t.Errorf("NextAttempt before RetryAt = %s", got)
	}
	soon := &Problem{RetryAt: last.Add(time.Minute)}
	if got := NextAttempt(last, 1, soon); !got.Equal(last.Add(15 * time.Minute)) {
		t.Errorf("NextAttempt ignores the backoff: %s", got)
	}
}

func TestStatus(t *testing.T) {
	now := time.Date(2025, 1, 18, 20, 0, 0, 0, time.UTC)
	s := &Status{Names: []string{"mc.example.com"}}
	if !s.Due(now) {
		t.Fatal("a name without a certificate is not due")
	}

	limited := &acme.Error{StatusCode: http.StatusTooManyRequests, ProblemType: "urn:ietf:params:acme:error:rateLimited",
		Detail: "too many new orders (300) from this account in the last 3h0m0s, retry after 2025-01-18 23:00:00 UTC: see https://letsencrypt.org/docs/rate-limits/#new-orders-per-account"}
	s.Record(now, nil, limited)
	if s.Failures != 1 || s.Problem.Code != CodeRateLimited || !s.NextAttempt.Equal(time.Date(2025, 1, 18, 23, 0, 0, 0, time.UTC)) {
		t.Fatalf("after a rate limit: %+v", s)
	}
	if s.Due(now.Add(2*time.Hour)) || !s.Due(now.Add(3*time.Hour)) {
		t.Error("Due does not wait for the rate limit")
	}

	s.Record(now.Add(3*time.Hour), nil, errors.New("connection reset"))
	if s.Failures != 2 || s.Problem.Code != CodeFailed || !s.NextAttempt.Equal(now.Add(3*time.Hour+30*time.Minute)) {
		t.Fatalf("after a second failure: %+v", s)
	}
	s.Record(now.Add(4*time.Hour), nil, nil)
	if s.Failures != 3 || s.Problem == nil {
		t.Fatalf("an attempt without a certificate or an error: %+v", s)
	}

	nb := now.Add(4 * time.Hour)
	cert := &Certificate{Names: []string{"mc.example.com"}, NotBefore: nb, NotAfter: nb.Add(90 * 24 * time.Hour), RenewAt: RenewAt(nb, nb.Add(90*24*time.Hour))}
	s.Record(nb, cert, nil)
	if s.Failures != 0 || s.Problem != nil || s.Certificate != cert || !s.NextAttempt.Equal(cert.RenewAt) || !s.LastAttempt.Equal(nb) {
		t.Fatalf("after success: %+v", s)
	}
	if s.Due(nb.Add(59*24*time.Hour)) || !s.Due(nb.Add(60*24*time.Hour)) {
		t.Error("renewal is not due at two thirds of the lifetime")
	}

	s.Record(nb.Add(60*24*time.Hour), nil, errors.New("down"))
	if s.Certificate != cert || s.Due(nb.Add(60*24*time.Hour+10*time.Minute)) || !s.Due(nb.Add(60*24*time.Hour+15*time.Minute)) {
		t.Errorf("a failed renewal keeps the certificate and backs off: %+v", s)
	}
}

package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/names"
)

// certSets counts the certificate sets kept for name, and how many of them
// were first certificates.
func (e *testEnv) certSets(name string) (sets, first int) {
	e.t.Helper()
	if err := e.svc.db.QueryRow(`SELECT count(*), coalesce(sum(first), 0) FROM cert_sets WHERE name = ?`, name).Scan(&sets, &first); err != nil {
		e.t.Fatal(err)
	}
	return sets, first
}

// certify publishes and clears a fresh challenge value for c's name.
func certify(t *testing.T, c *names.Client, tag string) error {
	t.Helper()
	fqdn := names.ChallengeFQDN(c.Name, testBase)
	v := acmeValue(c.Name + " " + tag)
	if err := c.SetTXT(context.Background(), fqdn, v); err != nil {
		return err
	}
	if err := c.ClearTXT(context.Background(), fqdn, v); err != nil {
		t.Fatal(err)
	}
	return nil
}

func TestCertificateAttemptsPerNameAreLimited(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.claimed("alice", "alice", newMachine(aliceV4, ""))
	fqdn := names.ChallengeFQDN("alice", testBase)
	publish := func(values ...string) {
		t.Helper()
		for _, v := range values {
			if err := c.SetTXT(ctx, fqdn, acmeValue(v)); err != nil {
				t.Fatal(err)
			}
		}
		for _, v := range values {
			if err := c.ClearTXT(ctx, fqdn, acmeValue(v)); err != nil {
				t.Fatal(err)
			}
		}
	}
	start := e.clk.Now()

	publish("name", "name", "wildcard")
	publish("name again", "wildcard again")
	if sets, first := e.certSets("alice"); sets != 1 || first != 1 {
		t.Errorf("a name and its wildcard, retried within the hour: %d attempts (%d new), want 1 (1)", sets, first)
	}
	publish("fifth")
	e.clk.Add(certSetWindow)
	publish("an hour later")
	if sets, first := e.certSets("alice"); sets != certSetsPerName || first != 1 {
		t.Errorf("%d attempts (%d new), want %d (1)", sets, first, certSetsPerName)
	}

	e.clk.Add(certSetWindow / 2)
	kept := acmeValue("kept")
	if err := c.SetTXT(ctx, fqdn, kept); err != nil {
		t.Fatalf("a value within the last attempt's hour: %v", err)
	}
	e.clk.Add(certSetWindow / 2)
	if err := c.SetTXT(ctx, fqdn, kept); err != nil {
		t.Errorf("publishing a live value again at the limit: %v", err)
	}
	var ne *names.Error
	retry := start.Add(week)
	err := c.SetTXT(ctx, fqdn, acmeValue("one too many"))
	if codeOf(err) != names.CodeCertificateLimit || !asError(err, &ne) || ne.Status != http.StatusTooManyRequests ||
		ne.Params["scope"] != "name" || ne.Params["limit"] != float64(certSetsPerName) || ne.Params["retryAt"] != retry.Format(time.RFC3339) ||
		ne.RetryAfter != retry.Sub(e.clk.Now()) || !strings.Contains(ne.Message, "Try again after 2 October 2026 12:00 UTC.") {
		t.Fatalf("an attempt over the name's limit: got %#v", err)
	}
	if err := c.ClearTXT(ctx, fqdn, kept); err != nil {
		t.Fatal(err)
	}
	if sets, _ := e.certSets("alice"); sets != certSetsPerName {
		t.Errorf("a refused attempt was counted: %d attempts", sets)
	}

	e.clk.Add(retry.Sub(e.clk.Now()) - time.Second)
	if err := certify(t, c, "early"); codeOf(err) != names.CodeCertificateLimit {
		t.Errorf("an attempt a second before a place frees up: got %v", err)
	}
	e.clk.Add(time.Second)
	if err := certify(t, c, "a week later"); err != nil {
		t.Errorf("an attempt once a place freed up: %v", err)
	}
}

func TestNewCertificatesHaveAWeeklyBudgetThatRenewalsDoNotUse(t *testing.T) {
	e := newEnv(t, func(e *testEnv) { e.cfg.NewCertificates = 3 })
	var cs []*names.Client
	for i, name := range []string{"ann", "ben", "cat", "dan"} {
		cs = append(cs, e.claimed(name, name, newMachine(fmt.Sprintf("5.75.%d.9", 160+i), "")))
	}
	start := e.clk.Now()
	for _, c := range cs[:3] {
		if err := certify(t, c, "first"); err != nil {
			t.Fatal(err)
		}
		e.clk.Add(certSetWindow)
	}

	var ne *names.Error
	retry := start.Add(week)
	err := certify(t, cs[3], "first")
	if codeOf(err) != names.CodeCertificateLimit || !asError(err, &ne) || ne.Params["scope"] != "all" || ne.Params["limit"] != float64(3) ||
		ne.Params["retryAt"] != retry.Format(time.RFC3339) || ne.RetryAfter != retry.Sub(e.clk.Now()) ||
		!strings.Contains(ne.Message, "Try again after 2 October 2026 12:00 UTC.") {
		t.Fatalf("a first certificate once the week's are used up: got %#v", err)
	}
	if !strings.Contains(e.log.String(), "alert="+alertCertificates) {
		t.Error("the owner is not alerted that new certificates are refused")
	}
	if err := certify(t, cs[0], "renewal"); err != nil {
		t.Errorf("a renewal once the week's new certificates are used up: %v", err)
	}
	if !strings.Contains(e.log.String(), "name=ann kind=renewal new_this_week=3 new_budget=3 renewals_this_week=1") {
		t.Error("renewals are not counted apart from new certificates")
	}

	e.clk.Add(retry.Sub(e.clk.Now()) - time.Second)
	if err := certify(t, cs[3], "first"); codeOf(err) != names.CodeCertificateLimit {
		t.Errorf("a first certificate a second before a place frees up: got %v", err)
	}
	e.clk.Add(time.Second)
	if err := certify(t, cs[3], "first"); err != nil {
		t.Errorf("a first certificate once a place freed up: %v", err)
	}
}

func TestANamesFirstCertificateSinceItsClaimOrInNinetyDaysIsNew(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.claimed("alice", "alice", newMachine(aliceV4, ""))
	for i, step := range []struct {
		after       time.Duration
		sets, first int
	}{
		{0, 1, 1},
		{60 * 24 * time.Hour, 2, 1},
		{renewalLookback, 1, 1},
	} {
		e.clk.Add(step.after)
		if _, err := c.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		e.tick()
		if err := certify(t, c, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
		if sets, first := e.certSets("alice"); sets != step.sets || first != step.first {
			t.Errorf("attempt %d: %d kept (%d new), want %d (%d)", i+1, sets, first, step.sets, step.first)
		}
	}

	if _, err := c.Release(ctx); err != nil {
		t.Fatal(err)
	}
	e.clk.Add(releaseHold)
	e.tick()
	if e.row("alice") != nil {
		t.Fatal("the released name was not freed")
	}
	bob := e.claimed("bob", "alice", newMachine("5.75.161.7", ""))
	if err := certify(t, bob, "bob"); err != nil {
		t.Fatal(err)
	}
	if sets, first := e.certSets("alice"); sets != 2 || first != 2 {
		t.Errorf("the first certificate of the name's next holder: %d kept (%d new), want 2 (2)", sets, first)
	}
}

package certs

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"
)

// DNSChallenger publishes the TXT records of DNS-01 checks. fqdn is the
// record's name without a trailing dot, such as
// _acme-challenge.alex.playkeeper.io. Errors are shown to the admin, so they
// must not contain secrets.
type DNSChallenger interface {
	SetTXT(ctx context.Context, fqdn, value string) error
	ClearTXT(ctx context.Context, fqdn, value string) error
}

// DNS01 answers DNS-01 checks through a DNSChallenger. It waits until the
// record can be seen before Let's Encrypt is asked to look, and removes it
// afterwards.
type DNS01 struct {
	Challenger DNSChallenger
	// LookupTXT reads the record back, given its name with a trailing dot;
	// nil asks the zone's authoritative nameservers directly, so that no
	// cache hides a new record.
	LookupTXT func(ctx context.Context, fqdn string) ([]string, error)
	// Timeout bounds the wait for the record; 0 means 2 minutes.
	Timeout time.Duration
	// Interval is the time between lookups; 0 means 5 seconds.
	Interval time.Duration
}

// blindWait is how long lookups may fail outright (rather than find no
// record) before the certificate authority is asked to look anyway; outgoing
// DNS may be blocked here while the record is fine.
const blindWait = 30 * time.Second

// present publishes value for name and waits until it can be seen. The
// returned function removes the record.
func (d *DNS01) present(ctx context.Context, name, value string) (func(), error) {
	fqdn := "_acme-challenge." + name
	params := map[string]string{"name": name, "fqdn": fqdn}
	remove := func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		d.Challenger.ClearTXT(cctx, fqdn, value)
	}
	if err := d.Challenger.SetTXT(ctx, fqdn, value); err != nil {
		remove()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, newProblem(err, CodeDNS01PublishFailed, params)
	}
	if err := d.wait(ctx, fqdn, value, params); err != nil {
		remove()
		return nil, err
	}
	return remove, nil
}

func (d *DNS01) wait(ctx context.Context, fqdn, value string, params map[string]string) error {
	timeout := cmp.Or(d.Timeout, 2*time.Minute)
	interval := cmp.Or(d.Interval, 5*time.Second)
	lookup := d.LookupTXT
	if lookup == nil {
		lookup = authoritativeTXT
	}
	start := time.Now()
	answered := false
	var seen []string
	for {
		lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		vals, err := lookup(lctx, fqdn+".")
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil || isNotFound(err) {
			answered = true
			if slices.Contains(vals, value) {
				return nil
			}
			seen = vals
		}
		elapsed := time.Since(start)
		if !answered && (elapsed >= blindWait || elapsed+interval > timeout) {
			return nil
		}
		if elapsed+interval > timeout {
			detail := fmt.Errorf("%s did not have the expected value after %s (found %q)", fqdn, timeout.Round(time.Second), seen)
			return newProblem(detail, CodeDNS01NotVisible, params)
		}
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// authoritativeTXT asks the nameservers of fqdn's zone directly. A value
// counts when every nameserver that answers has it.
func authoritativeTXT(ctx context.Context, fqdn string) ([]string, error) {
	fqdn = strings.TrimSuffix(fqdn, ".")
	servers, err := nameservers(ctx, fqdn)
	if err != nil {
		return nil, err
	}
	var common []string
	answered := 0
	lastErr := errors.New("no nameserver answered")
	for _, s := range servers {
		sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		vals, err := DNSServer(s).LookupTXT(sctx, fqdn+".")
		cancel()
		if err != nil && !isNotFound(err) {
			lastErr = err
			continue
		}
		if answered == 0 {
			common = vals
		} else {
			common = slices.DeleteFunc(common, func(v string) bool { return !slices.Contains(vals, v) })
		}
		answered++
	}
	if answered == 0 {
		return nil, lastErr
	}
	return common, nil
}

// nameservers returns up to four addresses ("ip:53") of the nameservers of
// the zone that holds fqdn, found by walking up its labels.
func nameservers(ctx context.Context, fqdn string) ([]string, error) {
	var r net.Resolver
	for n := fqdn; strings.Contains(n, "."); n = n[strings.IndexByte(n, '.')+1:] {
		nss, err := r.LookupNS(ctx, n+".")
		if err != nil || len(nss) == 0 {
			continue
		}
		var out []string
		for _, ns := range nss {
			ips, err := r.LookupNetIP(ctx, "ip4", ns.Host)
			if err != nil || len(ips) == 0 {
				ips, _ = r.LookupNetIP(ctx, "ip6", ns.Host)
			}
			if len(ips) > 0 {
				out = append(out, net.JoinHostPort(ips[0].Unmap().String(), "53"))
			}
			if len(out) == 4 {
				break
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("found no address for the nameservers of %s", n)
		}
		return out, nil
	}
	return nil, fmt.Errorf("found no nameservers for %s", fqdn)
}

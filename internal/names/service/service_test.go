package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/names"
)

const day = 24 * time.Hour

func TestClaimRefreshServersChallengesAndReleaseEndToEnd(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cf := e.cf
	m := newMachine(aliceV4, aliceV6)
	c := e.install("alice", m)

	n, err := c.Claim(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if n.Name != "alice" || n.Address != aliceFQD || n.State != names.StateActive || n.IPv4 != aliceV4 || n.IPv6 != "" ||
		n.DNS != names.DNSOK || !n.ClaimedAt.Equal(testStart) || !n.RefreshBy.Equal(testStart.Add(30*day)) || !n.FreedAt.Equal(testStart.Add(90*day)) {
		t.Fatalf("claim answered %+v", n)
	}
	a := cf.get("A", aliceFQD)
	if len(a) != 1 || a[0].Content != aliceV4 || a[0].TTL != 60 || a[0].Proxied || a[0].comment() != "playkeeper-names alice" {
		t.Fatalf("A records after the claim: %+v", a)
	}
	c.Name = "alice"
	if n, err = c.Refresh(ctx); err != nil || n.IPv4 != aliceV4 || n.IPv6 != aliceV6 || n.DNS != names.DNSOK {
		t.Fatalf("refresh: %+v, %v", n, err)
	}
	if aaaa := cf.get("AAAA", aliceFQD); len(aaaa) != 1 || aaaa[0].Content != aliceV6 || aaaa[0].TTL != 60 || aaaa[0].Proxied {
		t.Fatalf("AAAA records: %+v", aaaa)
	}

	e.clk.Add(6 * time.Hour)
	before := cf.requestCount()
	if n, err = c.Refresh(ctx); err != nil || !n.RefreshedAt.Equal(e.clk.Now()) || !n.RefreshBy.Equal(e.clk.Now().Add(30*day)) {
		t.Fatalf("an unchanged refresh: %+v, %v", n, err)
	}
	if got := cf.requestCount() - before; got != 0 {
		t.Errorf("an unchanged refresh made %d Cloudflare requests", got)
	}

	m.set("5.75.160.100", aliceV6)
	if n, err = c.Refresh(ctx); err != nil || n.IPv4 != "5.75.160.100" {
		t.Fatalf("refresh from a new address: %+v, %v", n, err)
	}
	if a := cf.get("A", aliceFQD); len(a) != 1 || a[0].Content != "5.75.160.100" {
		t.Errorf("A records after moving: %+v", a)
	}
	m.set("5.75.160.100", "")
	if n, err = c.Refresh(ctx); err != nil || n.IPv6 != "" {
		t.Fatalf("refresh without IPv6: %+v, %v", n, err)
	}
	if aaaa := cf.get("AAAA", aliceFQD); len(aaaa) != 0 {
		t.Errorf("the AAAA record stayed after IPv6 went away: %+v", aaaa)
	}

	if _, err := c.SetServer(ctx, "", 25565); codeOf(err) != names.CodeServerNotYet {
		t.Fatalf("a server address for a new name: got %v, want %s", err, names.CodeServerNotYet)
	}
	e.grown()
	s, err := c.SetServer(ctx, "", 25565)
	if err != nil || s.Address != aliceFQD || s.Port != 25565 || s.DNS != names.DNSOK {
		t.Fatalf("SetServer: %+v, %v", s, err)
	}
	if s, err = c.SetServer(ctx, "survival", 25566); err != nil || s.Address != "survival."+aliceFQD {
		t.Fatalf("SetServer survival: %+v, %v", s, err)
	}
	srv := cf.get("SRV", "_minecraft._tcp.survival."+aliceFQD)
	if len(srv) != 1 || *srv[0].Data != (cfSRV{Priority: 0, Weight: 0, Port: 25566, Target: aliceFQD}) || srv[0].TTL != 60 {
		t.Fatalf("SRV records: %+v", srv)
	}
	if _, err := c.SetServer(ctx, "survival", 25567); err != nil {
		t.Fatal(err)
	}
	if srv := cf.get("SRV", "_minecraft._tcp.survival."+aliceFQD); len(srv) != 1 || srv[0].Data.Port != 25567 {
		t.Errorf("SRV after a port change: %+v", srv)
	}
	if err := c.RemoveServer(ctx, "survival"); err != nil {
		t.Fatal(err)
	}
	if srv := cf.get("SRV", "_minecraft._tcp.survival."+aliceFQD); len(srv) != 0 {
		t.Errorf("SRV after removing the server: %+v", srv)
	}
	if bare := cf.get("SRV", "_minecraft._tcp."+aliceFQD); len(bare) != 1 || bare[0].Data.Port != 25565 {
		t.Errorf("SRV of the bare name: %+v", bare)
	}

	fqdn := names.ChallengeFQDN("alice", testBase)
	v1, v2, v3 := acmeValue("one"), acmeValue("two"), acmeValue("three")
	for _, v := range []string{v1, v2} {
		if err := c.SetTXT(ctx, fqdn, v); err != nil {
			t.Fatal(err)
		}
	}
	if txt := cf.get("TXT", fqdn); len(txt) != 2 || txt[0].TTL != 60 {
		t.Errorf("TXT records for a name and its wildcard: %+v", txt)
	}
	if err := c.SetTXT(ctx, fqdn, v3); codeOf(err) != names.CodeTooManyTXT {
		t.Errorf("a third challenge: got %v, want %s", err, names.CodeTooManyTXT)
	}
	if err := c.SetTXT(ctx, fqdn, v1); err != nil {
		t.Errorf("setting a challenge again: %v", err)
	}
	for _, v := range []string{v1, v2} {
		if err := c.ClearTXT(ctx, fqdn, v); err != nil {
			t.Fatal(err)
		}
	}
	if txt := cf.get("TXT", fqdn); len(txt) != 0 {
		t.Errorf("TXT records after clearing: %+v", txt)
	}

	list, err := c.Names(ctx)
	if err != nil || len(list) != 1 || len(list[0].Servers) != 1 || list[0].Servers[0].Label != "" || list[0].Servers[0].Port != 25565 {
		t.Fatalf("Names: %+v, %v", list, err)
	}

	n, err = c.Release(ctx)
	if err != nil || n.State != names.StateReleased || !n.FreedAt.Equal(e.clk.Now().Add(30*day)) || !n.RefreshBy.IsZero() || len(n.Servers) != 0 {
		t.Fatalf("release: %+v, %v", n, err)
	}
	if left := cf.under(aliceFQD); len(left) != 0 {
		t.Errorf("records left after the release: %+v", left)
	}
	if _, err := c.Refresh(ctx); codeOf(err) != names.CodeNotClaimed {
		t.Errorf("refreshing a released name: got %v, want %s", err, names.CodeNotClaimed)
	}
	bob := e.install("bob", newMachine("5.75.161.7", ""))
	if _, err := bob.Claim(ctx, "alice"); codeOf(err) != names.CodeNameHeld {
		t.Errorf("another install claiming a held name: got %v, want %s", err, names.CodeNameHeld)
	}
	if av, err := bob.Available(ctx, "alice"); err != nil || av.Available || av.Code != names.CodeNameHeld || av.Params["until"] == nil {
		t.Errorf("availability of a held name: %+v, %v", av, err)
	}
	if n, err = c.Claim(ctx, "alice"); err != nil || n.State != names.StateActive || len(cf.get("A", aliceFQD)) != 1 {
		t.Errorf("the same install claiming its released name again: %+v, %v", n, err)
	}
	bob.Name = "alice"
	if _, err := bob.SetServer(ctx, "", 25565); codeOf(err) != names.CodeNotYourName {
		t.Errorf("another install changing the name: got %v, want %s", err, names.CodeNotYourName)
	}
	cf.checkUntouched(t)
}

func TestConcurrentClaimsOfOneNameHaveOneWinner(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := e.install(fmt.Sprintf("racer-%d", i), newMachine(fmt.Sprintf("5.75.170.%d", i+1), ""))
			_, errs[i] = c.Claim(context.Background(), "race")
		}()
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		switch codeOf(err) {
		case "":
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			won++
		case names.CodeNameTaken:
		default:
			t.Errorf("unexpected refusal: %v", err)
		}
	}
	if won != 1 || len(e.cf.get("A", "race.playkeeper.io")) != 1 {
		t.Errorf("%d claims won and %d A records exist, want one each", won, len(e.cf.get("A", "race.playkeeper.io")))
	}
	if t.Failed() {
		t.Log(e.log.String())
	}
}

func TestSignaturesClockSkewAndReplaysAreRefused(t *testing.T) {
	e := newEnv(t)
	key := testKey("alice")
	now := e.clk.Now()

	claim := e.signedRequest(http.MethodPut, "/v1/names/alice", nil, key, now, aliceV4)
	replay := claim.Clone(context.Background())
	if resp, body := e.send(claim); resp.StatusCode != http.StatusOK {
		t.Fatalf("a signed claim: HTTP %d %+v", resp.StatusCode, body)
	}
	if resp, body := e.send(replay); resp.StatusCode != http.StatusUnauthorized || body.Code != names.CodeReplayed {
		t.Errorf("the same request again: HTTP %d %+v, want %s", resp.StatusCode, body, names.CodeReplayed)
	}

	tampered := e.signedRequest(http.MethodPost, "/v1/names/alice/address", []byte(`{}`), key, now, aliceV4)
	tampered.Body, tampered.ContentLength = io.NopCloser(strings.NewReader(`{"clearOther":true}`)), 19
	moved := e.signedRequest(http.MethodPost, "/v1/names/alice/address", nil, key, now, aliceV4)
	moved.URL.Path = "/v1/names/bob/address"
	otherService, err := http.NewRequest(http.MethodPost, e.srv.URL+"/v1/names/alice/address", nil)
	if err != nil {
		t.Fatal(err)
	}
	names.SignRequest(otherService, key, "example.com", nil, now)
	otherService.Header.Set("X-Forwarded-For", aliceV4)
	unsigned, err := http.NewRequest(http.MethodPost, e.srv.URL+"/v1/names/alice/address", nil)
	if err != nil {
		t.Fatal(err)
	}
	unsigned.Header.Set("X-Forwarded-For", aliceV4)
	large := e.signedRequest(http.MethodPut, "/v1/names/alice/servers/survival", []byte(`{"port":25566,"x":"`+strings.Repeat("a", 5000)+`"}`), key, now, aliceV4)
	for _, tc := range []struct {
		name   string
		req    *http.Request
		status int
		code   string
	}{
		{"a changed body", tampered, http.StatusUnauthorized, names.CodeBadSignature},
		{"another path", moved, http.StatusUnauthorized, names.CodeBadSignature},
		{"signed for another service", otherService, http.StatusUnauthorized, names.CodeBadSignature},
		{"no signature", unsigned, http.StatusUnauthorized, names.CodeUnsigned},
		{"a body over 4 KiB", large, http.StatusBadRequest, names.CodeInvalidRequest},
		{"six minutes old", e.signedRequest(http.MethodPost, "/v1/names/alice/address", nil, key, now.Add(-6*time.Minute), aliceV4), http.StatusUnauthorized, names.CodeClockSkew},
		{"six minutes ahead", e.signedRequest(http.MethodPost, "/v1/names/alice/address", nil, key, now.Add(6*time.Minute), aliceV4), http.StatusUnauthorized, names.CodeClockSkew},
	} {
		if resp, body := e.send(tc.req); resp.StatusCode != tc.status || body.Code != tc.code {
			t.Errorf("%s: HTTP %d %+v, want %d %s", tc.name, resp.StatusCode, body, tc.status, tc.code)
		}
	}
	skewed := e.signedRequest(http.MethodPost, "/v1/names/alice/address", nil, key, now.Add(-time.Hour), aliceV4)
	if _, body := e.send(skewed); body.Params["serverTime"] != float64(now.Unix()) || body.Hint == "" {
		t.Errorf("a clock-skew refusal needs the service's time and a hint: %+v", body)
	}
	if row := e.row("alice"); row.IPv4 != aliceV4 || row.IPv6 != "" || row.Version != 1 {
		t.Errorf("a refused request changed the name: %+v", row)
	}

	e.clk.Add(names.MaxSkew + time.Second)
	e.tick()
	var nonces int
	if err := e.svc.db.QueryRow(`SELECT count(*) FROM nonces`).Scan(&nonces); err != nil || nonces != 0 {
		t.Errorf("nonces left after the window: %d, %v", nonces, err)
	}
	replay = claim.Clone(context.Background())
	if resp, body := e.send(replay); resp.StatusCode != http.StatusUnauthorized || body.Code != names.CodeClockSkew {
		t.Errorf("a replay after the window: HTTP %d %+v, want %s", resp.StatusCode, body, names.CodeClockSkew)
	}
}

func TestForwardedForIsOnlyBelievedFromTrustedProxies(t *testing.T) {
	log := &syncBuffer{}
	proxies, err := ParsePrefixes("10.0.1.0/24, 127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{cfg: Config{TrustedProxies: proxies}, log: slog.New(slog.NewTextHandler(log, nil))}
	for _, tc := range []struct {
		peer string
		xff  []string
		want string
	}{
		{"5.75.160.99:40000", nil, "5.75.160.99"},
		{"5.75.160.99:40000", []string{"1.2.3.4"}, "5.75.160.99"},
		{"[2a01:4f8:c012:6f3a::1]:40000", []string{"1.2.3.4"}, "2a01:4f8:c012:6f3a::1"},
		{"[::ffff:5.75.160.99]:40000", nil, "5.75.160.99"},
		{"10.0.1.5:40000", nil, "10.0.1.5"},
		{"10.0.1.5:40000", []string{"5.75.160.99"}, "5.75.160.99"},
		{"10.0.1.5:40000", []string{"6.6.6.6, 5.75.160.99"}, "5.75.160.99"},
		{"10.0.1.5:40000", []string{"6.6.6.6", "5.75.160.99"}, "5.75.160.99"},
		{"10.0.1.5:40000", []string{"5.75.160.99, 10.0.1.7"}, "5.75.160.99"},
		{"10.0.1.5:40000", []string{"2a01:4f8:c012:6f3a::1"}, "2a01:4f8:c012:6f3a::1"},
		{"10.0.1.5:40000", []string{"::ffff:5.75.160.99"}, "5.75.160.99"},
		{"10.0.1.5:40000", []string{"garbage, 5.75.160.99"}, "5.75.160.99"},
		{"127.0.0.1:40000", []string{"10.0.1.7, 127.0.0.1"}, "10.0.1.7"},
		{"10.0.1.5:40000", []string{"5.75.160.99, garbage"}, ""},
		{"10.0.1.5:40000", []string{"5.75.160.99:1234"}, ""},
		{"10.0.1.5:40000", []string{""}, ""},
	} {
		r, err := http.NewRequest(http.MethodGet, "http://names.playkeeper.io/v1/ip", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.RemoteAddr = tc.peer
		for _, v := range tc.xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		got, err := s.clientAddr(r)
		switch {
		case tc.want == "" && err == nil:
			t.Errorf("peer %s, X-Forwarded-For %q: got %s, want an error", tc.peer, tc.xff, got)
		case tc.want != "" && (err != nil || got.String() != tc.want):
			t.Errorf("peer %s, X-Forwarded-For %q: got %s, %v; want %s", tc.peer, tc.xff, got, err, tc.want)
		}
	}
	for range 2 {
		r, _ := http.NewRequest(http.MethodGet, "http://names.playkeeper.io/v1/ip", nil)
		r.RemoteAddr = "192.168.1.20:40000"
		r.Header.Set("X-Forwarded-For", "5.75.160.99")
		if got, err := s.clientAddr(r); err != nil || got.String() != "192.168.1.20" {
			t.Errorf("an untrusted proxy: got %s, %v", got, err)
		}
	}
	if n := strings.Count(log.String(), "does not list"); n != 1 {
		t.Errorf("warned %d times about an unlisted proxy, want once", n)
	}
}

func TestASpoofedForwardedForFromAnUntrustedPeerIsIgnored(t *testing.T) {
	e := newEnv(t, func(e *testEnv) { e.cfg.TrustedProxies = nil })
	resp, b := e.get("/v1/ip", aliceV4)
	var info names.IPInfo
	if err := json.Unmarshal(b, &info); err != nil || resp.StatusCode != http.StatusOK || info.IP != "127.0.0.1" || info.Public {
		t.Errorf("/v1/ip with a spoofed header: HTTP %d %+v, %v", resp.StatusCode, info, err)
	}
	c := e.install("alice", newMachine(aliceV4, ""))
	if _, err := c.Claim(context.Background(), "alice"); codeOf(err) != names.CodeNotPublic {
		t.Errorf("a claim with a spoofed header: got %v, want %s", err, names.CodeNotPublic)
	}
	if e.row("alice") != nil || len(e.cf.get("A", aliceFQD)) != 0 {
		t.Error("the spoofed address was claimed")
	}
	if !strings.Contains(e.log.String(), "does not list") {
		t.Error("the unlisted proxy is not logged")
	}
}

func TestNamesCannotPointAtPrivateReservedOrProxyAddresses(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for i, addr := range []string{
		"10.0.0.5", "172.16.3.4", "192.168.1.20", "100.64.1.1", "127.0.0.2", "169.254.1.1", "0.1.2.3", "240.0.0.1",
		"192.0.2.10", "198.51.100.7", "203.0.113.9", "198.18.0.1",
		"fd00::1", "fe80::1", "::1", "2001:db8::1", "2002:c000:0204::1", "2001::1", "64:ff9b::5.75.160.99",
	} {
		c := e.install(fmt.Sprintf("private-%d", i), newMachine(addr, ""))
		var ne *names.Error
		if _, err := c.Claim(ctx, fmt.Sprintf("private-%d", i)); codeOf(err) != names.CodeNotPublic || !asError(err, &ne) || ne.Params["ip"] == nil {
			t.Errorf("a claim from %s: got %v, want %s", addr, err, names.CodeNotPublic)
		}
	}
	proxied := e.install("proxied", newMachine("104.16.132.229", ""))
	if _, err := proxied.Claim(ctx, "proxied"); codeOf(err) != names.CodeMisconfigured {
		t.Errorf("a claim through Cloudflare's proxy: got %v, want %s", err, names.CodeMisconfigured)
	}
	if !strings.Contains(e.log.String(), "must be DNS only") {
		t.Error("a proxied names record is not logged")
	}
	if info, err := proxied.IP(ctx, names.AnyFamily); err != nil || info.Public {
		t.Errorf("/v1/ip through Cloudflare's proxy: %+v, %v", info, err)
	}
	if info, err := e.install("public", newMachine("", aliceV6)).IP(ctx, names.IPv6); err != nil || !info.Public || info.Family != "ipv6" || info.IP != aliceV6 {
		t.Errorf("/v1/ip over IPv6: %+v, %v", info, err)
	}
}

func asError(err error, target **names.Error) bool {
	e, ok := err.(*names.Error)
	*target = e
	return ok
}

func TestReservedAndBlocklistedNamesCannotBeClaimed(t *testing.T) {
	list := filepath.Join(t.TempDir(), "blocklist.txt")
	if err := os.WriteFile(list, []byte("# names nobody may have\nBadWord\nnot a name\n\nevil\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, func(e *testEnv) { e.cfg.BlocklistFile = list })
	ctx := context.Background()
	c := e.install("alice", newMachine(aliceV4, ""))
	for _, name := range []string{"www", "names", "api", "install", "mail", "autodiscover", "wiki", "status", "playkeeper", "my-playkeeper", "badword", "evil"} {
		if av, err := c.Available(ctx, name); err != nil || av.Available || av.Code != names.CodeNameReserved {
			t.Errorf("availability of %s: %+v, %v", name, av, err)
		}
		if _, err := c.Claim(ctx, name); codeOf(err) != names.CodeNameReserved {
			t.Errorf("claim %s: got %v, want %s", name, err, names.CodeNameReserved)
		}
	}
	if av, err := c.Available(ctx, "grief"); err != nil || !av.Available || av.Address != "grief.playkeeper.io" {
		t.Errorf("availability of a free name: %+v, %v", av, err)
	}
	if av, err := c.Available(ctx, "Grief!"); err != nil || av.Available || av.Code != names.CodeInvalidName {
		t.Errorf("availability of an invalid name: %+v, %v", av, err)
	}

	griefer := e.claimed("griefer", "grief", newMachine("5.75.161.20", ""))
	f, err := os.OpenFile(list, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("grief\n")
	f.Close()
	e.tick()
	if row := e.row("grief"); row == nil || row.State != names.StateReleased {
		t.Fatalf("a blocklisted name was not taken away: %+v", row)
	}
	if left := e.cf.under("grief.playkeeper.io"); len(left) != 0 {
		t.Errorf("records of a blocklisted name: %+v", left)
	}
	if _, err := griefer.Claim(ctx, "grief"); codeOf(err) != names.CodeNameReserved {
		t.Errorf("claiming a blocklisted name back: got %v, want %s", err, names.CodeNameReserved)
	}
	e.cf.checkUntouched(t)
}

func TestAnInstallHoldsOnlyAsManyNamesAsAllowed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.claimed("alice", "alice", newMachine(aliceV4, ""))
	var ne *names.Error
	if _, err := c.Claim(ctx, "alice2"); codeOf(err) != names.CodeLimitReached || !asError(err, &ne) || ne.Params["limit"] != float64(1) {
		t.Errorf("a second name: got %v, want %s", err, names.CodeLimitReached)
	}
	if _, err := c.Claim(ctx, "alice"); err != nil {
		t.Errorf("claiming its own name again: %v", err)
	}
	if _, err := c.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Claim(ctx, "alice2"); err != nil {
		t.Errorf("a new name after releasing the old one: %v", err)
	}
	if _, err := c.Claim(ctx, "alice"); codeOf(err) != names.CodeLimitReached {
		t.Errorf("taking the released name back while holding another: got %v, want %s", err, names.CodeLimitReached)
	}

	e2 := newEnv(t, func(e *testEnv) { e.cfg.MaxNamesPerKey = 2 })
	c2 := e2.install("bob", newMachine("5.75.161.7", ""))
	for _, name := range []string{"bob", "bob2"} {
		if _, err := c2.Claim(ctx, name); err != nil {
			t.Errorf("claim %s with a limit of 2: %v", name, err)
		}
	}
	if _, err := c2.Claim(ctx, "bob3"); codeOf(err) != names.CodeLimitReached {
		t.Errorf("a third name with a limit of 2: got %v, want %s", err, names.CodeLimitReached)
	}
}

func TestRateLimitsPerAddressPerKeyAndForNewNames(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for i := range requestsPerIPBurst {
		if resp, _ := e.get("/v1/ip", "5.75.162.1"); resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d from one address: HTTP %d", i+1, resp.StatusCode)
		}
	}
	resp, b := e.get("/v1/ip", "5.75.162.1")
	var body names.ErrorBody
	_ = json.Unmarshal(b, &body)
	if resp.StatusCode != http.StatusTooManyRequests || body.Code != names.CodeRateLimited || resp.Header.Get("Retry-After") != "30" || body.Params["retryAfterSeconds"] != float64(30) {
		t.Errorf("request over the per-address limit: HTTP %d %s %+v", resp.StatusCode, resp.Header.Get("Retry-After"), body)
	}
	if resp, _ := e.get("/v1/ip", "5.75.162.2"); resp.StatusCode != http.StatusOK {
		t.Errorf("another address is limited too: HTTP %d", resp.StatusCode)
	}
	e.clk.Add(30 * time.Second)
	if resp, _ := e.get("/v1/ip", "5.75.162.1"); resp.StatusCode != http.StatusOK {
		t.Errorf("after 30 seconds: HTTP %d", resp.StatusCode)
	}
	if resp, _ := e.get("/v1/ip", "5.75.162.1"); resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("right after that: HTTP %d", resp.StatusCode)
	}
	for range requestsPerIPBurst {
		e.get("/v1/ip", "2a01:4f8:c012:6f3a::1")
	}
	if resp, _ := e.get("/v1/ip", "2a01:4f8:c012:6f3a::beef"); resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("another address in the same IPv6 /64: HTTP %d", resp.StatusCode)
	}
	if resp, _ := e.get("/v1/ip", "2a01:4f8:c012:6f3b::1"); resp.StatusCode != http.StatusOK {
		t.Errorf("another IPv6 /64: HTTP %d", resp.StatusCode)
	}

	c := e.install("alice", newMachine("5.75.162.3", ""))
	for i := range requestsPerKeyBurst {
		if _, err := c.Names(ctx); err != nil {
			t.Fatalf("signed request %d: %v", i+1, err)
		}
	}
	var ne *names.Error
	if _, err := c.Names(ctx); codeOf(err) != names.CodeRateLimited || !asError(err, &ne) || ne.RetryAfter != time.Minute {
		t.Errorf("request over the per-key limit: got %#v", err)
	}

	// Released names leave the network's limit, not the daily one.
	for i, addr := range []string{"2a01:4f8:c012:6f01::1", "2a01:4f8:c012:6f02::1", "2a01:4f8:c012:6f03::1"} {
		c := e.install(fmt.Sprintf("v6-%d", i), newMachine("", addr))
		if _, err := c.Claim(ctx, fmt.Sprintf("net-%d", i)); err != nil {
			t.Fatalf("claim %d from one /56: %v", i+1, err)
		}
		c.Name = fmt.Sprintf("net-%d", i)
		if _, err := c.Release(ctx); err != nil {
			t.Fatal(err)
		}
	}
	_, err := e.install("v6-3", newMachine("", "2a01:4f8:c012:6fff::1")).Claim(ctx, "net-3")
	if codeOf(err) != names.CodeRateLimited || !strings.Contains(err.Error(), "new names from this address") {
		t.Errorf("a fourth new name from one /56: got %v", err)
	}
	if _, err := e.install("v6-4", newMachine("", "2a01:4f8:c012:7000::1")).Claim(ctx, "net-4"); err != nil {
		t.Errorf("a new name from another /56: %v", err)
	}
}

func TestNewNamesPerDayAcrossEveryone(t *testing.T) {
	e := newEnv(t, func(e *testEnv) { e.cfg.ClaimsPerDay = 2 })
	ctx := context.Background()
	if _, err := e.install("carol", newMachine("5.75.163.1", "")).Claim(ctx, "carol"); codeOf(err) != names.CodeNameInUse {
		t.Fatalf("claiming a name in use: got %v, want %s", err, names.CodeNameInUse)
	}
	for i, name := range []string{"one", "two"} {
		if _, err := e.install(name, newMachine(fmt.Sprintf("5.75.163.%d", i+2), "")).Claim(ctx, name); err != nil {
			t.Fatalf("claim %s: %v", name, err)
		}
	}
	_, err := e.install("three", newMachine("5.75.163.9", "")).Claim(ctx, "three")
	if codeOf(err) != names.CodeRateLimited || !strings.Contains(err.Error(), "new names today") {
		t.Errorf("a third new name in a day: got %v", err)
	}
	if !strings.Contains(e.log.String(), "alert=claims_budget") {
		t.Error("the owner is not alerted when the daily budget runs out")
	}
	e.clk.Add(12 * time.Hour)
	if _, err := e.install("three", newMachine("5.75.163.9", "")).Claim(ctx, "three"); err != nil {
		t.Errorf("after half a day: %v", err)
	}
}

func TestStartupRefusesARefusedTokenOrAnotherZone(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(cf *fakeCloudflare, cfg *Config)
		want  []string
	}{
		{"refused token", func(cf *fakeCloudflare, cfg *Config) { cf.token = "a-token-cloudflare-does-not-know" }, []string{EnvToken, EnvZone, "9106"}},
		{"zone of another domain", func(cf *fakeCloudflare, cfg *Config) { cf.setZone("example.com", "active") }, []string{EnvZone, EnvBase, `"example.com"`}},
		{"unknown zone", func(cf *fakeCloudflare, cfg *Config) { cfg.CloudflareZone = "0123456789abcdef0123456789abcdef" }, []string{EnvZone, "7003"}},
	} {
		cf := newFakeCloudflare(t)
		log := &syncBuffer{}
		cfg := testConfig(t, &testClock{t: testStart}, cf, log)
		tc.setup(cf, &cfg)
		_, err := New(context.Background(), cfg)
		if err == nil {
			t.Errorf("%s: New succeeded", tc.name)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: %q does not mention %s", tc.name, err, w)
			}
		}
		if strings.Contains(err.Error()+log.String(), testToken) {
			t.Errorf("%s: the token leaked: %v", tc.name, err)
		}
	}
}

func TestStartupWithoutCloudflareChecksTheZoneBeforeTheFirstChange(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	e := newEnv(t, func(e *testEnv) { e.cfg.cloudflareAPI = down.URL + "/client/v4" })
	if !strings.Contains(e.log.String(), "Could not reach Cloudflare") {
		t.Error("the unreachable zone check is not logged")
	}
	c := e.install("alice", newMachine(aliceV4, ""))
	var ne *names.Error
	if _, err := c.Claim(context.Background(), "alice"); codeOf(err) != names.CodeDNSUnavailable || !asError(err, &ne) || ne.RetryAfter <= 0 {
		t.Errorf("a claim while Cloudflare is down: got %#v, want %s", err, names.CodeDNSUnavailable)
	}
	if e.row("alice") != nil {
		t.Error("the name was claimed without checking its records")
	}
	e.svc.cf.api = e.cf.srv.URL + "/client/v4"
	e.cf.setZone("example.com", "active")
	if _, err := c.Claim(context.Background(), "alice"); codeOf(err) != names.CodeDNSUnavailable {
		t.Errorf("a claim with the zone of another domain: got %v, want %s", err, names.CodeDNSUnavailable)
	}
	e.cf.setZone(testBase, "pending")
	if n, err := c.Claim(context.Background(), "alice"); err != nil || n.DNS != names.DNSOK {
		t.Errorf("a claim once Cloudflare is back: %+v, %v", n, err)
	}
	if !strings.Contains(e.log.String(), "not active yet") {
		t.Error("a pending zone is not logged")
	}
	e.cf.checkUntouched(t)
}

func TestCloudflareRateLimitPausesChangesUntilRetryAfter(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := newMachine(aliceV4, "")
	c := e.claimed("alice", "alice", m)
	e.grown()
	e.cf.fail(fakeFailure{method: http.MethodPost, status: http.StatusTooManyRequests, fixture: "error_rate_limited.json", retryAfter: "30", left: 1})
	m.set(aliceV4, aliceV6)
	n, err := c.Refresh(ctx)
	if err != nil || n.IPv6 != aliceV6 || n.DNS != names.DNSPending {
		t.Fatalf("a refresh while Cloudflare rate-limits: %+v, %v", n, err)
	}
	before := e.cf.requestCount()
	var ne *names.Error
	if _, err := c.SetServer(ctx, "survival", 25566); codeOf(err) != names.CodeDNSUnavailable || !asError(err, &ne) || ne.RetryAfter != 30*time.Second {
		t.Errorf("a new server address during the pause: got %#v", err)
	}
	if got := e.cf.requestCount() - before; got != 0 {
		t.Errorf("%d Cloudflare requests during the pause", got)
	}
	e.clk.Add(61 * time.Second)
	e.tick()
	if row := e.row("alice"); row.Synced != row.Version {
		t.Errorf("the retry job did not sync: %+v", row)
	}
	if aaaa := e.cf.get("AAAA", aliceFQD); len(aaaa) != 1 {
		t.Errorf("AAAA after the retry: %+v", aaaa)
	}
	if n, err := c.Refresh(ctx); err != nil || n.DNS != names.DNSOK {
		t.Errorf("a refresh after the retry: %+v, %v", n, err)
	}
}

func TestServerAddressesPerNameAreLimited(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.claimed("alice", "alice", newMachine(aliceV4, ""))
	e.grown()
	for i := range serversPerKey {
		if _, err := c.SetServer(ctx, fmt.Sprintf("s%d", i), 25565+i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.SetServer(ctx, "one-more", 25600); codeOf(err) != names.CodeTooManyServers {
		t.Errorf("server address %d: got %v, want %s", serversPerKey+1, err, names.CodeTooManyServers)
	}
	if _, err := c.SetServer(ctx, "s0", 25610); err != nil {
		t.Errorf("changing a port with all server addresses in use: %v", err)
	}
	req := e.signedRequest(http.MethodPut, "/v1/names/alice/servers/s0", nil, testKey("alice"), e.clk.Now(), aliceV4)
	if resp, body := e.send(req); resp.StatusCode != http.StatusBadRequest || body.Code != names.CodeInvalidRequest {
		t.Errorf("a server address without a port: HTTP %d %+v", resp.StatusCode, body)
	}
	req = e.signedRequest(http.MethodPut, "/v1/names/alice/servers/s0", []byte(`{"port":70000}`), testKey("alice"), e.clk.Now(), aliceV4)
	if resp, body := e.send(req); resp.StatusCode != http.StatusBadRequest || body.Code != names.CodeInvalidPort {
		t.Errorf("port 70000: HTTP %d %+v", resp.StatusCode, body)
	}
	req = e.signedRequest(http.MethodPut, "/v1/names/alice/servers/_minecraft", []byte(`{"port":25565}`), testKey("alice"), e.clk.Now(), aliceV4)
	if resp, body := e.send(req); resp.StatusCode != http.StatusBadRequest || body.Code != names.CodeInvalidServer {
		t.Errorf("an invalid label: HTTP %d %+v", resp.StatusCode, body)
	}
	req = e.signedRequest(http.MethodPut, "/v1/names/alice/acme-challenge/not-a-challenge", nil, testKey("alice"), e.clk.Now(), aliceV4)
	if resp, body := e.send(req); resp.StatusCode != http.StatusBadRequest || body.Code != names.CodeInvalidChallenge {
		t.Errorf("an invalid challenge value: HTTP %d %+v", resp.StatusCode, body)
	}
}

func TestNamesLapseAndAreFreedWhenNotRefreshed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.claimed("alice", "alice", newMachine(aliceV4, ""))
	e.grown()
	if _, err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	fqdn := names.ChallengeFQDN("alice", testBase)
	if _, err := c.SetServer(ctx, "", 25565); err != nil {
		t.Fatal(err)
	}
	if err := c.SetTXT(ctx, fqdn, acmeValue("forgotten")); err != nil {
		t.Fatal(err)
	}
	e.clk.Add(59 * time.Minute)
	e.tick()
	if len(e.cf.get("TXT", fqdn)) != 1 {
		t.Error("a challenge record was removed before its hour")
	}
	e.clk.Add(time.Minute)
	e.tick()
	if txt := e.cf.get("TXT", fqdn); len(txt) != 0 {
		t.Errorf("a challenge record outlived its hour: %+v", txt)
	}

	e.clk.Add(30*day - time.Hour - time.Second)
	e.tick()
	if row := e.row("alice"); row.State != names.StateActive || len(e.cf.under(aliceFQD)) != 2 {
		t.Fatalf("a name lapsed early: %+v", row)
	}
	e.clk.Add(time.Second)
	e.tick()
	if row := e.row("alice"); row.State != names.StateLapsed {
		t.Fatalf("a name was not refreshed for 30 days but is %s", row.State)
	}
	if left := e.cf.under(aliceFQD); len(left) != 0 {
		t.Errorf("records of a lapsed name: %+v", left)
	}
	list, err := c.Names(ctx)
	if err != nil || len(list) != 1 || list[0].State != names.StateLapsed || !list[0].RefreshBy.IsZero() || !list[0].FreedAt.Equal(e.clk.Now().Add(60*day)) {
		t.Errorf("a lapsed name: %+v, %v", list, err)
	}
	if _, err := c.SetServer(ctx, "survival", 25566); codeOf(err) != names.CodeNameLapsed {
		t.Errorf("a server address for a lapsed name: got %v, want %s", err, names.CodeNameLapsed)
	}
	if err := c.SetTXT(ctx, fqdn, acmeValue("late")); codeOf(err) != names.CodeNameLapsed {
		t.Errorf("a challenge for a lapsed name: got %v, want %s", err, names.CodeNameLapsed)
	}
	if n, err := c.Refresh(ctx); err != nil || n.State != names.StateActive {
		t.Fatalf("refreshing a lapsed name: %+v, %v", n, err)
	}
	if len(e.cf.get("A", aliceFQD)) != 1 || len(e.cf.get("SRV", "_minecraft._tcp."+aliceFQD)) != 1 {
		t.Errorf("records after refreshing a lapsed name: %+v", e.cf.under(aliceFQD))
	}

	e.clk.Add(30 * day)
	e.tick()
	e.clk.Add(60*day - time.Second)
	e.tick()
	if row := e.row("alice"); row == nil || row.State != names.StateLapsed {
		t.Fatalf("a lapsed name was freed early: %+v", row)
	}
	e.clk.Add(time.Second)
	e.tick()
	if row := e.row("alice"); row != nil {
		t.Fatalf("a name lapsed for 60 days was not freed: %+v", row)
	}
	if list, err := c.Names(ctx); err != nil || len(list) != 0 {
		t.Errorf("names of an install whose name was freed: %+v, %v", list, err)
	}
	if _, err := e.install("bob", newMachine("5.75.161.7", "")).Claim(ctx, "alice"); err != nil {
		t.Errorf("claiming a freed name: %v", err)
	}
	e.cf.checkUntouched(t)
}

func TestReleasedNamesAreHeldThenFreed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	bob := e.claimed("bob", "bobby", newMachine("5.75.161.7", ""))
	if _, err := bob.Release(ctx); err != nil {
		t.Fatal(err)
	}
	carol := e.install("carol", newMachine("5.75.161.8", ""))
	e.clk.Add(30*day - time.Second)
	e.tick()
	if _, err := carol.Claim(ctx, "bobby"); codeOf(err) != names.CodeNameHeld {
		t.Errorf("claiming a held name: got %v, want %s", err, names.CodeNameHeld)
	}
	e.clk.Add(time.Second)
	e.tick()
	if row := e.row("bobby"); row != nil {
		t.Fatalf("a name was not freed after its hold: %+v", row)
	}
	if _, err := carol.Claim(ctx, "bobby"); err != nil {
		t.Errorf("claiming a name after its hold: %v", err)
	}
}

func TestANameIsOnlyFreedOnceItsRecordsAreGone(t *testing.T) {
	e := newEnv(t)
	e.claimed("dan", "dan", newMachine("5.75.161.9", ""))
	e.cf.fail(fakeFailure{status: http.StatusBadGateway, body: "<html>\r\n<head><title>502 Bad Gateway</title></head>\r\n<body>\r\n<center><h1>502 Bad Gateway</h1></center>\r\n<hr><center>cloudflare</center>\r\n</body>\r\n</html>\r\n"})
	e.clk.Add(30 * day)
	e.tick()
	e.clk.Add(60 * day)
	e.tick()
	if row := e.row("dan"); row == nil || row.State != names.StateLapsed || row.Synced == row.Version {
		t.Fatalf("a name whose records could not be removed: %+v", row)
	}
	if len(e.cf.get("A", "dan.playkeeper.io")) != 1 {
		t.Fatal("the record went away while Cloudflare was failing")
	}
	e.cf.stopFailing()
	e.clk.Add(time.Hour)
	e.tick()
	if len(e.cf.get("A", "dan.playkeeper.io")) != 0 {
		t.Error("the retry job did not remove the records")
	}
	e.tick()
	if row := e.row("dan"); row != nil {
		t.Errorf("the name was not freed once its records were gone: %+v", row)
	}
}

func TestDailySnapshotsKeepAWeek(t *testing.T) {
	e := newEnv(t)
	e.claimed("alice", "alice", newMachine(aliceV4, ""))
	for range 9 {
		e.tick()
		e.tick()
		e.clk.Add(day)
	}
	dir := filepath.Join(e.cfg.DataDir, "backups")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range entries {
		got = append(got, entry.Name())
		if fi, err := entry.Info(); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %v, %v", entry.Name(), fi.Mode().Perm(), err)
		}
	}
	want := []string{"names-2026-09-27.db", "names-2026-09-28.db", "names-2026-09-29.db", "names-2026-09-30.db", "names-2026-10-01.db", "names-2026-10-02.db", "names-2026-10-03.db"}
	if !slices.Equal(got, want) {
		t.Fatalf("snapshots %v, want %v", got, want)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, want[len(want)-1])+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM names WHERE name = 'alice'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("the newest snapshot holds %d rows for alice, %v", n, err)
	}
}

func TestIndexHealthAndUnknownRoutes(t *testing.T) {
	e := newEnv(t)
	if resp, b := e.get("/", ""); resp.StatusCode != http.StatusOK || !strings.Contains(string(b), "Source code (AGPL-3.0): https://github.com/CIYAhq/playkeeper") {
		t.Errorf("index: HTTP %d %q", resp.StatusCode, b)
	}
	if resp, b := e.get("/healthz", ""); resp.StatusCode != http.StatusOK || string(b) != "ok\n" {
		t.Errorf("healthz: HTTP %d %q", resp.StatusCode, b)
	}
	if resp, _ := e.get("/v1/unknown", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown route: HTTP %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/v1/ip", nil)
	if resp, err := e.srv.Client().Do(req); err != nil || resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /v1/ip: %v %v", resp.StatusCode, err)
	}
	req = e.signedRequest(http.MethodPut, "/v1/names/alice", []byte(`{"unexpected":true}`), testKey("alice"), e.clk.Now(), aliceV4)
	if resp, body := e.send(req); resp.StatusCode != http.StatusBadRequest || body.Code != names.CodeInvalidRequest {
		t.Errorf("a claim with unknown fields: HTTP %d %+v", resp.StatusCode, body)
	}
}

func TestHandMadeRecordsAtTheNamesAddressesBlockThemUntilRemoved(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := newMachine(aliceV4, "")
	c := e.claimed("alice", "alice", m)
	e.grown()
	e.cf.addByHand("CNAME", "www."+aliceFQD, "example.net", "")
	e.cf.addByHand("TXT", aliceFQD, `"hello"`, "")
	if _, err := c.SetServer(ctx, "", 25565); err != nil {
		t.Fatalf("a server address next to hand-made records: %v", err)
	}
	m.set("5.75.160.102", "")
	if n, err := c.Refresh(ctx); err != nil || n.DNS != names.DNSOK {
		t.Fatalf("a refresh next to hand-made records that are not addresses: %+v, %v", n, err)
	}

	creative := "_minecraft._tcp.creative." + aliceFQD
	handCNAME := e.cf.addByHand("CNAME", creative, "srv.example.net", "")
	if sv, err := c.SetServer(ctx, "creative", 25570); err != nil || sv.DNS != names.DNSPending || len(e.cf.get("SRV", creative)) != 0 {
		t.Fatalf("a server address where a record was made by hand: %+v, %v", sv, err)
	}
	hand := e.cf.addByHand("A", aliceFQD, "5.75.160.200", "")
	m.set("5.75.160.103", "")
	if n, err := c.Refresh(ctx); err != nil || n.IPv4 != "5.75.160.103" || n.DNS != names.DNSPending {
		t.Fatalf("a refresh with a hand-made address record: %+v, %v", n, err)
	}
	a := e.cf.get("A", aliceFQD)
	if len(a) != 2 || !slices.ContainsFunc(a, func(r fakeRecord) bool { return r.Content == "5.75.160.102" && r.comment() == "playkeeper-names alice" }) {
		t.Errorf("the service changed its address next to a hand-made one: %+v", a)
	}
	if !strings.Contains(e.log.String(), aliceFQD+" has a hand-made A record") || !strings.Contains(e.log.String(), creative+" has a hand-made CNAME record") {
		t.Error("the hand-made records are not logged")
	}
	if _, err := c.SetServer(ctx, "survival", 25566); err != nil || len(e.cf.get("SRV", "_minecraft._tcp.survival."+aliceFQD)) != 1 {
		t.Errorf("server addresses stopped syncing because of a hand-made address record: %v", err)
	}
	e.cf.checkUntouched(t)

	e.cf.removeByHand(hand)
	e.cf.removeByHand(handCNAME)
	e.clk.Add(time.Hour)
	e.tick()
	if a := e.cf.get("A", aliceFQD); len(a) != 1 || a[0].Content != "5.75.160.103" {
		t.Errorf("A records once the hand-made one is gone: %+v", a)
	}
	if srv := e.cf.get("SRV", creative); len(srv) != 1 || srv[0].Data.Port != 25570 {
		t.Errorf("SRV records once the hand-made CNAME is gone: %+v", srv)
	}
	if n, err := c.Refresh(ctx); err != nil || n.DNS != names.DNSOK {
		t.Errorf("a refresh once the hand-made record is gone: %+v, %v", n, err)
	}
	e.cf.checkUntouched(t)
}

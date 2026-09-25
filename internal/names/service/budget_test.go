package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/names"
)

func TestNamesPerNetworkAreLimited(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m0 := newMachine("5.75.164.10", "")
	var first *names.Client
	for i := range DefaultMaxNamesPerNetwork {
		m := m0
		if i > 0 {
			m = newMachine(fmt.Sprintf("5.75.164.%d", 10+i), "")
		}
		c := e.install(fmt.Sprintf("v4-%d", i), m)
		if _, err := c.Claim(ctx, fmt.Sprintf("v4-%d", i)); err != nil {
			t.Fatalf("name %d from one IPv4 /24: %v", i+1, err)
		}
		c.Name = fmt.Sprintf("v4-%d", i)
		if i == 0 {
			first = c
		}
	}
	late := e.install("v4-late", newMachine("5.75.164.200", ""))
	var ne *names.Error
	_, err := late.Claim(ctx, "v4-late")
	if codeOf(err) != names.CodeNetworkLimit || !asError(err, &ne) || ne.Params["network"] != "5.75.164.0/24" || ne.Params["limit"] != float64(DefaultMaxNamesPerNetwork) {
		t.Errorf("a fourth name from one IPv4 /24: got %#v", err)
	}
	if _, err := e.install("v4-next", newMachine("5.75.165.1", "")).Claim(ctx, "v4-next"); err != nil {
		t.Errorf("a name from the next /24: %v", err)
	}
	if e.row("v4-0").Network != "5.75.164.0/24" || e.row("v4-next").Network != "5.75.165.0/24" {
		t.Errorf("networks stored: %q, %q", e.row("v4-0").Network, e.row("v4-next").Network)
	}

	// A lapsed name still counts, since it can come back without a claim;
	// a released one does not.
	if _, err := e.svc.db.Exec(`UPDATE names SET state = ? WHERE name = 'v4-1'`, names.StateLapsed); err != nil {
		t.Fatal(err)
	}
	if _, err := late.Claim(ctx, "v4-late"); codeOf(err) != names.CodeNetworkLimit {
		t.Errorf("a name from a /24 whose names include a lapsed one: got %v, want %s", err, names.CodeNetworkLimit)
	}
	if _, err := first.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := late.Claim(ctx, "v4-late"); err != nil {
		t.Errorf("a name from the /24 after one was released: %v", err)
	}
	m0.set("5.75.166.5", "")
	if _, err := first.Claim(ctx, "v4-0"); err != nil || e.row("v4-0").Network != "5.75.166.0/24" {
		t.Errorf("taking a released name back from another network: %v, network %q", err, e.row("v4-0").Network)
	}

	for i, addr := range []string{"2a01:4f8:c013:100::1", "2a01:4f8:c013:200::1", "2a01:4f8:c013:300::1"} {
		if _, err := e.install(fmt.Sprintf("v6-%d", i), newMachine("", addr)).Claim(ctx, fmt.Sprintf("v6-%d", i)); err != nil {
			t.Fatalf("name %d from one IPv6 /48: %v", i+1, err)
		}
	}
	_, err = e.install("v6-late", newMachine("", "2a01:4f8:c013:ff00::1")).Claim(ctx, "v6-late")
	if codeOf(err) != names.CodeNetworkLimit || !asError(err, &ne) || ne.Params["network"] != "2a01:4f8:c013::/48" {
		t.Errorf("a fourth name from one IPv6 /48: got %#v", err)
	}
	if _, err := e.install("v6-next", newMachine("", "2a01:4f8:c014::1")).Claim(ctx, "v6-next"); err != nil {
		t.Errorf("a name from the next /48: %v", err)
	}

	e2 := newEnv(t, func(e *testEnv) { e.cfg.MaxNamesPerNetwork = 1 })
	if _, err := e2.install("one", newMachine("5.75.164.10", "")).Claim(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := e2.install("two", newMachine("5.75.164.11", "")).Claim(ctx, "two"); codeOf(err) != names.CodeNetworkLimit {
		t.Errorf("a second name from one /24 with a limit of 1: got %v, want %s", err, names.CodeNetworkLimit)
	}
}

func TestServerAddressesWaitUntilTheNameIsThreeDaysOld(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.claimed("alice", "alice", newMachine(aliceV4, ""))
	var ne *names.Error
	for _, label := range []string{"", "survival"} {
		_, err := c.SetServer(ctx, label, 25566)
		if codeOf(err) != names.CodeServerNotYet || !asError(err, &ne) || ne.Params["from"] != float64(testStart.Add(serverAddressAge).Unix()) ||
			!strings.Contains(ne.Message, "28 September 2026 12:00 UTC") || !strings.Contains(ne.Hint, aliceFQD+":25566") {
			t.Errorf("server address %q of a new name: got %#v", label, err)
		}
	}
	e.clk.Add(serverAddressAge - time.Second)
	e.tick()
	if _, err := c.SetServer(ctx, "", 25566); codeOf(err) != names.CodeServerNotYet {
		t.Errorf("a second before the name is old enough: got %v, want %s", err, names.CodeServerNotYet)
	}
	e.clk.Add(time.Second)
	if _, err := c.SetServer(ctx, "", 25566); err != nil {
		t.Fatalf("once the name is old enough: %v", err)
	}
	if srv := e.cf.get("SRV", "_minecraft._tcp."+aliceFQD); len(srv) != 1 {
		t.Errorf("SRV records: %+v", srv)
	}
	if _, err := c.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Claim(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetServer(ctx, "", 25566); codeOf(err) != names.CodeServerNotYet {
		t.Errorf("a name claimed again after its release: got %v, want %s", err, names.CodeServerNotYet)
	}
}

func TestServerAddressesPerInstallAndPerNetworkAreLimited(t *testing.T) {
	ctx := context.Background()
	add := func(c *names.Client, n int) {
		t.Helper()
		for i := range n {
			if _, err := c.SetServer(ctx, fmt.Sprintf("s%d", i), 25565+i); err != nil {
				t.Fatalf("server address s%d under %s: %v", i, c.Name, err)
			}
		}
	}

	e := newEnv(t, func(e *testEnv) { e.cfg.MaxNamesPerKey = 2 })
	m := newMachine("5.75.167.10", "")
	a := e.claimed("multi", "multi-a", m)
	b := e.install("multi", m)
	if _, err := b.Claim(ctx, "multi-b"); err != nil {
		t.Fatal(err)
	}
	b.Name = "multi-b"
	e.grown()
	add(a, 3)
	add(b, 2)
	var ne *names.Error
	if _, err := b.SetServer(ctx, "s2", 25567); codeOf(err) != names.CodeTooManyServers || !asError(err, &ne) || ne.Params["scope"] != "install" || ne.Params["max"] != float64(serversPerKey) {
		t.Errorf("a sixth server address across one install's names: got %#v", err)
	}
	if err := a.RemoveServer(ctx, "s2"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SetServer(ctx, "s2", 25567); err != nil {
		t.Errorf("a server address after removing another: %v", err)
	}

	e = newEnv(t)
	var installs []*names.Client
	for i := range 3 {
		installs = append(installs, e.claimed(fmt.Sprintf("net-%d", i), fmt.Sprintf("net-%d", i), newMachine(fmt.Sprintf("5.75.168.%d", 10+i), "")))
	}
	e.grown()
	add(installs[0], serversPerKey)
	add(installs[1], serversPerKey)
	_, err := installs[2].SetServer(ctx, "", 25565)
	if codeOf(err) != names.CodeTooManyServers || !asError(err, &ne) || ne.Params["scope"] != "network" || ne.Params["network"] != "5.75.168.0/24" || ne.Params["max"] != float64(serversPerNetwork) {
		t.Errorf("server address %d from one network: got %#v", serversPerNetwork+1, err)
	}
	if _, err := installs[0].SetServer(ctx, "s0", 25600); err != nil {
		t.Errorf("changing a port with the network's server addresses in use: %v", err)
	}
	e.cf.checkUntouched(t)
}

func TestAFullZoneRefusesServerAddressesThenNamesThenChallenges(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.claimed("alice", "alice", newMachine(aliceV4, ""))
	e.grown()

	quota := e.cf.usage() + 1 + DefaultRecordReserve + challengeRoom + nameRoom
	e.cf.setQuota(&quota)
	if _, err := c.SetServer(ctx, "", 25565); err != nil {
		t.Fatalf("the last server address that fits: %v", err)
	}
	if _, err := c.SetServer(ctx, "b", 25566); codeOf(err) != names.CodeZoneFull {
		t.Errorf("a server address in a zone with only room for names left: got %v, want %s", err, names.CodeZoneFull)
	}
	if _, err := c.SetServer(ctx, "", 25570); err != nil {
		t.Errorf("changing a port in a full zone: %v", err)
	}
	if _, err := e.install("bob", newMachine("5.75.161.7", "")).Claim(ctx, "bob"); err != nil {
		t.Fatalf("a new name once server addresses are refused: %v", err)
	}

	quota = e.cf.usage() + 2 + DefaultRecordReserve + challengeRoom - 1
	e.cf.setQuota(&quota)
	if _, err := e.install("cora", newMachine("5.75.161.8", "")).Claim(ctx, "cora"); codeOf(err) != names.CodeZoneFull {
		t.Errorf("a new name in a zone with only room for challenges left: got %v, want %s", err, names.CodeZoneFull)
	}
	fqdn := names.ChallengeFQDN("alice", testBase)
	if err := c.SetTXT(ctx, fqdn, acmeValue("one")); err != nil {
		t.Fatalf("a certificate challenge once new names are refused: %v", err)
	}

	quota = e.cf.usage() + DefaultRecordReserve
	e.cf.setQuota(&quota)
	var ne *names.Error
	if err := c.SetTXT(ctx, fqdn, acmeValue("two")); codeOf(err) != names.CodeZoneFull || !asError(err, &ne) || !strings.Contains(ne.Message, "certificate challenges") {
		t.Errorf("a certificate challenge that would use the owner's reserve: got %#v", err)
	}
	if err := c.SetTXT(ctx, fqdn, acmeValue("one")); err != nil {
		t.Errorf("setting a live challenge again in a full zone: %v", err)
	}
	for _, kind := range []string{alertZoneNearlyFull, alertZoneFull, alertChallenges} {
		if n := strings.Count(e.log.String(), "alert="+kind); n != 1 {
			t.Errorf("alert %s logged %d times, want once", kind, n)
		}
	}
	e.cf.checkUntouched(t)
}

func TestTheRecordQuotaSettingAppliesWhenCloudflareReportsNoneOrMore(t *testing.T) {
	ctx := context.Background()
	larger := 1000
	for _, tc := range []struct {
		name  string
		quota *int
	}{
		{"no quota from Cloudflare", nil},
		{"a larger quota from Cloudflare", &larger},
	} {
		e := newEnv(t, func(e *testEnv) {
			e.cfg.RecordQuota = e.cf.usage() + 2 + DefaultRecordReserve + challengeRoom
			e.cf.setQuota(tc.quota)
		})
		if _, err := e.install("alice", newMachine(aliceV4, "")).Claim(ctx, "alice"); err != nil {
			t.Errorf("%s: the name that fits: %v", tc.name, err)
		}
		if _, err := e.install("bob", newMachine("5.75.161.7", "")).Claim(ctx, "bob"); codeOf(err) != names.CodeZoneFull {
			t.Errorf("%s: a name over %s: got %v, want %s", tc.name, EnvRecordQuota, err, names.CodeZoneFull)
		}
	}
}

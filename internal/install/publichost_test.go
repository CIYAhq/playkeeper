package install

import (
	"context"
	"errors"
	"testing"
)

// On AWS, Google Cloud, Azure and Oracle Cloud the machine has only a private
// address, and the provider's NAT gives it its public one. The setup link
// printed the private one, which doesn't open from home; it now asks the
// names service which address the machine's requests come from.
func TestTheSetupLinkUsesThePublicAddressBehindNAT(t *testing.T) {
	asked := 0
	none := func() []string { return nil }
	public := func(context.Context) (string, error) { asked++; return "203.0.113.7", nil }
	for _, c := range []struct {
		host, want string
		ask        bool
	}{
		{"10.0.0.5", "203.0.113.7", true},
		{"172.31.4.9", "203.0.113.7", true},
		{"192.168.1.20", "203.0.113.7", true},
		{"100.64.0.3", "203.0.113.7", true},
		{"169.254.10.2", "203.0.113.7", true},
		{"198.51.100.20", "198.51.100.20", false},
		{"5.75.160.99", "5.75.160.99", false},
		{"[2001:db8::1]", "[2001:db8::1]", false},
		{"YOUR-SERVER-IP", "YOUR-SERVER-IP", false},
	} {
		asked = 0
		sys := System{Processes: none, PublicIPv4: public}
		if got := publicHost(context.Background(), sys, opts(""), c.host); got != c.want || (asked > 0) != c.ask {
			t.Errorf("%s: got %s, asked %d times", c.host, got, asked)
		}
	}

	// When the names service can't say, or says something the link can't
	// use, the link keeps the machine's own address, which the summary then
	// calls private.
	for _, answer := range []func(context.Context) (string, error){
		func(context.Context) (string, error) { return "", errors.New("dial tcp: i/o timeout") },
		func(context.Context) (string, error) { return "10.1.2.3", nil },
		func(context.Context) (string, error) { return "2001:db8::7", nil },
		func(context.Context) (string, error) { return "not an address", nil },
	} {
		sys := System{Processes: none, PublicIPv4: answer}
		if got := publicHost(context.Background(), sys, opts(""), "10.0.0.5"); got != "10.0.0.5" || !privateAddr(got) {
			t.Errorf("got %s", got)
		}
	}

	// Test installs ask nothing: CI doesn't depend on the names service.
	o := opts("")
	o.Usage.Test = true
	asked = 0
	if got := publicHost(context.Background(), System{Processes: none, PublicIPv4: public}, o, "10.0.0.5"); got != "10.0.0.5" || asked != 0 {
		t.Errorf("a test install: got %s, asked %d times", got, asked)
	}
	actions := func() []string { return []string{"/home/runner/actions-runner/bin/Runner.Worker spawnclient 117 120"} }
	if got := publicHost(context.Background(), System{Processes: actions, PublicIPv4: public}, opts(""), "10.0.0.5"); got != "10.0.0.5" || asked != 0 {
		t.Errorf("an install in an Actions job: got %s, asked %d times", got, asked)
	}
	if got := publicHost(context.Background(), System{Processes: none}, opts(""), "10.0.0.5"); got != "10.0.0.5" {
		t.Errorf("without a way to ask: got %s", got)
	}
}

// Running the installer again on an install prints the dashboard's link
// with the public address behind NAT too.
func TestAnUpgradesLinkUsesThePublicAddressBehindNAT(t *testing.T) {
	h := newFakeHost(t)
	installedAt(t, h, "0.4.15", true)
	sys := h.system(t)
	bin := newBinary(t, "0.4.16")
	sys.Executable = func() (string, error) { return bin, nil }
	sys.PublicIPv4 = func(context.Context) (string, error) { return "203.0.113.7", nil }
	o := opts("")
	o.Yes = true
	res, err := Run(context.Background(), sys, o, "0.4.16")
	if err != nil {
		t.Fatalf("upgrade: %v\n%s", err, o.Out)
	}
	// Whether there's a private address to swap is up to the machine the
	// tests run on; GitHub's runners have one.
	want := primaryIP()
	if privateAddr(want) {
		want = "203.0.113.7"
	}
	if !res.Upgraded || res.URL != "https://"+want+":8443" || res.PrivateHost {
		t.Errorf("the upgrade's link: %s, private %v", res.URL, res.PrivateHost)
	}
}

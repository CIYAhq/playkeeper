package machinelink

import (
	"slices"
	"strings"
	"testing"
)

func TestParseAddress(t *testing.T) {
	for _, tc := range []struct {
		in, host string
		port     int
		str      string
	}{
		{"panel.example.com", "panel.example.com", 8443, "panel.example.com"},
		{"https://Panel.Example.COM/", "panel.example.com", 8443, "panel.example.com"},
		{"HTTPS://panel.example.com:443", "panel.example.com", 443, "panel.example.com:443"},
		{" panel.example.com. ", "panel.example.com", 8443, "panel.example.com"},
		{"localhost:9000", "localhost", 9000, "localhost:9000"},
		{"203.0.113.7", "203.0.113.7", 8443, "203.0.113.7"},
		{"203.0.113.7:8443", "203.0.113.7", 8443, "203.0.113.7"},
		{"https://203.0.113.7:9443/", "203.0.113.7", 9443, "203.0.113.7:9443"},
		{"2001:db8::1", "2001:db8::1", 8443, "[2001:db8::1]"},
		{"[2001:db8::1]:8443", "2001:db8::1", 8443, "[2001:db8::1]"},
		{"[2001:DB8::1]:9000", "2001:db8::1", 9000, "[2001:db8::1]:9000"},
		{"::ffff:203.0.113.7", "203.0.113.7", 8443, "203.0.113.7"},
	} {
		a, err := ParseAddress(tc.in)
		if err != nil {
			t.Errorf("ParseAddress(%q): %v", tc.in, err)
			continue
		}
		if a.Host != tc.host || a.Port != tc.port || a.String() != tc.str {
			t.Errorf("ParseAddress(%q) = %+v (%s), want %s port %d (%s)", tc.in, a, a, tc.host, tc.port, tc.str)
		}
		if again, err := ParseAddress(a.String()); err != nil || again != a {
			t.Errorf("ParseAddress(%q) = %+v, %v; want %+v", a.String(), again, err, a)
		}
	}
}

func TestParseAddressRefusals(t *testing.T) {
	for _, in := range []string{
		"",
		"https://",
		"http://panel.example.com",
		"ftp://panel.example.com",
		"panel.example.com/path",
		"user@panel.example.com",
		"panel.example.com; reboot",
		"$(reboot)",
		"`id`",
		"panel.example.com&",
		"panel example com",
		"pänel.example.com",
		"panel.example.com:0",
		"panel.example.com:65536",
		"panel.example.com:123456",
		"panel.example.com:+80",
		"panel.example.com:80a",
		"panel.example.com:-1",
		"[panel.example.com]:8443",
		"[203.0.113.7]:8443",
		"[2001:db8::1",
		"[2001:db8::1]x",
		"[2001:db8::1]:",
		"panel.example.com:",
		"1.2.3",
		"203.0.113.999",
		"-panel.example.com",
		"panel..example.com",
		strings.Repeat("a", 64) + ".example.com",
		strings.Repeat("a.", 150) + "com",
	} {
		_, err := ParseAddress(in)
		if e := wantCode(t, err, CodeAddressInvalid); e.Hint == "" || strings.ContainsAny(e.Msg, "\n\r") {
			t.Errorf("ParseAddress(%q): %q / %q", in, e.Msg, e.Hint)
		}
	}
}

func TestCommand(t *testing.T) {
	const fp = "Z287KN4CDZD0Z8A4XXJA514NKG"
	c, err := NewCommand("https://panel.example.com/", "7kq2-m9xd", strings.ToLower(fp))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := c.Install(), "curl -fsSL https://playkeeper.io/install | sudo sh -s -- --join panel.example.com --code 7KQ2-M9XD --fingerprint "+fp; got != want {
		t.Errorf("Install() =\n%s\nwant\n%s", got, want)
	}
	if got, want := c.Join(), "sudo playkeeper join panel.example.com --code 7KQ2-M9XD --fingerprint "+fp; got != want {
		t.Errorf("Join() =\n%s\nwant\n%s", got, want)
	}
	c.Name = "Bob's  server; rm -rf /"
	if got, want := c.JoinArgs(), []string{"panel.example.com", "--code", "7KQ2-M9XD", "--fingerprint", fp, "--name", "Bobs server rm -rf"}; !slices.Equal(got, want) {
		t.Errorf("JoinArgs() = %q, want %q", got, want)
	}
	if got := c.Join(); !strings.HasSuffix(got, " --name 'Bobs server rm -rf'") {
		t.Errorf("Join() with a name = %s", got)
	}

	v6, err := NewCommand("[2001:db8::1]:9000", "7KQ2-M9XD", fp)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := v6.InstallArgs()[:2], []string{"--join", "[2001:db8::1]:9000"}; !slices.Equal(got, want) {
		t.Errorf("InstallArgs() = %q", got)
	}
	if got := v6.Install(); !strings.Contains(got, " --join '[2001:db8::1]:9000' ") {
		t.Errorf("the IPv6 address is not quoted for sh: %s", got)
	}

	for _, tc := range []struct{ addr, code, fp, want string }{
		{"panel.example.com;id", "7KQ2-M9XD", fp, CodeAddressInvalid},
		{"panel.example.com", "7KQ2-M9X", fp, CodeJoinCodeMalformed},
		{"panel.example.com", "7KQ2-M9XD", fp[:20], CodeFingerprintInvalid},
	} {
		_, err := NewCommand(tc.addr, tc.code, tc.fp)
		wantCode(t, err, tc.want)
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"home-server":                 "home-server",
		"  Home   Server  ":           "Home Server",
		"web_01.lan":                  "web_01.lan",
		"a\x00b\x1b[2Jc":              "ab2Jc",
		"$(reboot)":                   "reboot",
		"Bob's server":                "Bobs server",
		"\u202eevil\u202c":            "evil",
		"Café Nord":                   "Café Nord",
		"/// ;; ":                     "",
		strings.Repeat("a", 50):       strings.Repeat("a", 40),
		strings.Repeat("ab ", 20):     strings.Repeat("ab ", 13) + "a",
		"x" + strings.Repeat("é", 45): "x" + strings.Repeat("é", 39),
	} {
		if got := CleanName(in); got != want {
			t.Errorf("CleanName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUniqueName(t *testing.T) {
	long := strings.Repeat("n", MaxNameLen)
	for _, tc := range []struct {
		name  string
		taken []string
		want  string
	}{
		{"home", nil, "home"},
		{"home", []string{"office"}, "home"},
		{"home", []string{"Home"}, "home-2"},
		{"home", []string{"home", "HOME-2", "home-3"}, "home-4"},
		{long, []string{long}, long[:MaxNameLen-2] + "-2"},
	} {
		if got := uniqueName(tc.name, tc.taken); got != tc.want {
			t.Errorf("uniqueName(%q, %q) = %q, want %q", tc.name, tc.taken, got, tc.want)
		}
	}
}

package machinelink

import (
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const (
	// DefaultPort is the dashboard's HTTPS port. Machines connect to the
	// same port as browsers.
	DefaultPort = 8443
	// InstallURL is where the join command downloads Playkeeper's installer.
	InstallURL = "https://playkeeper.io/install"
	// MaxNameLen is the longest machine name, in characters.
	MaxNameLen = 40
)

// Address is where a machine reaches the dashboard.
type Address struct {
	// Host is a DNS name in lower case, or an IP address.
	Host string
	Port int
}

// String is the address as people write it: the port only when it isn't
// DefaultPort, and IPv6 addresses in brackets.
func (a Address) String() string {
	h := a.Host
	if strings.Contains(h, ":") {
		h = "[" + h + "]"
	}
	if a.Port == DefaultPort {
		return h
	}
	return h + ":" + strconv.Itoa(a.Port)
}

// HostPort is the address to dial.
func (a Address) HostPort() string { return net.JoinHostPort(a.Host, strconv.Itoa(a.Port)) }

var reLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ParseAddress reads a dashboard address as people write it: a name or an
// IP address with an optional port, optionally with the https:// and the
// trailing slash of the dashboard's URL. Nothing else is accepted (no
// paths, user names or spaces), so an address is always safe to put in a
// shell command.
func ParseAddress(s string) (Address, error) {
	raw := s
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return Address{}, errAddress(raw, "it is too long")
	}
	if len(s) >= 8 && strings.EqualFold(s[:8], "https://") {
		s = s[8:]
	} else if strings.Contains(s, "://") {
		return Address{}, errAddress(raw, "only https:// addresses work")
	}
	s = strings.TrimSuffix(s, "/")
	if s == "" {
		return Address{}, errAddress(raw, "it is empty")
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == ':' || c == '[' || c == ']') {
			return Address{}, errAddress(raw, "only a name or IP address and a port can be in it")
		}
	}
	host, port, hasPort := s, "", false
	switch {
	case strings.HasPrefix(s, "["):
		end := strings.IndexByte(s, ']')
		if end < 0 {
			return Address{}, errAddress(raw, "a bracket is missing")
		}
		host = s[1:end]
		if rest := s[end+1:]; rest != "" {
			if rest[0] != ':' {
				return Address{}, errAddress(raw, "only a port can follow the brackets")
			}
			port, hasPort = rest[1:], true
		}
		if ip, err := netip.ParseAddr(host); err != nil || !ip.Is6() {
			return Address{}, errAddress(raw, "brackets are only for IPv6 addresses")
		}
	case strings.Count(s, ":") == 1:
		host, port, hasPort = strings.Cut(s, ":")
	}
	p := DefaultPort
	if hasPort {
		n, err := strconv.Atoi(port)
		if err != nil || len(port) > 5 || port[0] < '0' || port[0] > '9' || n < 1 || n > 65535 {
			return Address{}, errAddress(raw, "the port must be a number from 1 to 65535")
		}
		p = n
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return Address{Host: ip.Unmap().String(), Port: p}, nil
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if !validHostname(host) {
		return Address{}, errAddress(raw, "it is not a host name or IP address")
	}
	return Address{Host: host, Port: p}, nil
}

func validHostname(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	labels := strings.Split(h, ".")
	for _, l := range labels {
		if !reLabel.MatchString(l) {
			return false
		}
	}
	// A name whose last part is a number is a mistyped IP address.
	last := labels[len(labels)-1]
	return strings.Trim(last, "0123456789") != ""
}

// Command is the command that connects a machine to the dashboard: the
// dashboard makes it, and the admin pastes it on the new machine. It has
// two forms, one that installs Playkeeper first and one for a machine
// that already has it.
type Command struct {
	Address Address
	// Code is the one-time join code, XXXX-XXXX.
	Code string
	// Fingerprint is the dashboard's key fingerprint. It lets the machine
	// check that it talks to this dashboard before it sends the code.
	Fingerprint string
	// Name, when set, is what the dashboard will call the machine;
	// otherwise the machine's host name is used.
	Name string
}

// NewCommand checks and normalizes the parts of a join command.
func NewCommand(address, code, fingerprint string) (Command, error) {
	a, err := ParseAddress(address)
	if err != nil {
		return Command{}, err
	}
	c, err := NormalizeJoinCode(code)
	if err != nil {
		return Command{}, err
	}
	fp, err := ParseFingerprint(fingerprint)
	if err != nil {
		return Command{}, err
	}
	return Command{Address: a, Code: c, Fingerprint: fp}, nil
}

// InstallArgs are the installer's arguments: --join ADDRESS --code CODE
// --fingerprint FINGERPRINT, then --name NAME when a name is set. The
// address always has its port, like every address the dashboard shows.
func (c Command) InstallArgs() []string {
	return append([]string{"--join", c.Address.HostPort()}, c.flags()...)
}

// JoinArgs are the arguments of playkeeper join: ADDRESS --code CODE
// --fingerprint FINGERPRINT, then --name NAME when a name is set.
func (c Command) JoinArgs() []string {
	return append([]string{c.Address.HostPort()}, c.flags()...)
}

func (c Command) flags() []string {
	f := []string{"--code", c.Code, "--fingerprint", c.Fingerprint}
	if n := CleanName(c.Name); n != "" {
		f = append(f, "--name", n)
	}
	return f
}

// Install is the command for a machine without Playkeeper: it installs
// Playkeeper, then joins the dashboard.
func (c Command) Install() string {
	return "curl -fsSL " + InstallURL + " | sudo sh -s -- " + shellJoin(c.InstallArgs())
}

// Join is the command for a machine that already runs Playkeeper.
func (c Command) Join() string { return "sudo playkeeper join " + shellJoin(c.JoinArgs()) }

// InstallLines and JoinLines are the two forms split for reading: each flag
// and its value on a line of its own, the lines ending in backslashes, so
// pasted together they are the same command.
func (c Command) InstallLines() []string {
	return continued("curl -fsSL "+InstallURL+" | sudo sh -s --", c.InstallArgs())
}

func (c Command) JoinLines() []string {
	args := c.JoinArgs()
	return continued("sudo playkeeper join "+shellJoin(args[:1]), args[1:])
}

func continued(head string, pairs []string) []string {
	lines := []string{head}
	for i := 0; i+1 < len(pairs); i += 2 {
		lines = append(lines, "  "+shellJoin(pairs[i:i+2]))
	}
	for i := range lines[:len(lines)-1] {
		lines[i] += ` \`
	}
	return lines
}

var reShellSafe = regexp.MustCompile(`^[A-Za-z0-9._:/@%+=,-]+$`)

// shellJoin quotes arguments for sh. Every part was validated to contain
// no single quote, so single quotes are enough.
func shellJoin(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		if reShellSafe.MatchString(a) {
			q[i] = a
		} else {
			q[i] = "'" + strings.ReplaceAll(a, "'", "") + "'"
		}
	}
	return strings.Join(q, " ")
}

// CleanName makes a machine name safe to show anywhere and to put in a
// command: letters, digits, single spaces and - _ . only, at most
// MaxNameLen characters. It returns "" when nothing is left.
func CleanName(s string) string {
	var out []rune
	space := false
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.':
			if space && len(out) > 0 {
				out = append(out, ' ')
			}
			space = false
			out = append(out, r)
		case unicode.IsSpace(r):
			space = true
		}
		if len(out) >= MaxNameLen {
			break
		}
	}
	if len(out) > MaxNameLen {
		out = out[:MaxNameLen]
	}
	return strings.TrimSpace(string(out))
}

// uniqueName returns name, or name-2, name-3… when another machine
// already has it (ignoring case).
func uniqueName(name string, taken []string) string {
	used := make(map[string]bool, len(taken))
	for _, t := range taken {
		used[strings.ToLower(t)] = true
	}
	if !used[strings.ToLower(name)] {
		return name
	}
	for i := 2; ; i++ {
		suffix := "-" + strconv.Itoa(i)
		base := []rune(name)
		if len(base)+len(suffix) > MaxNameLen {
			base = base[:MaxNameLen-len(suffix)]
		}
		if n := string(base) + suffix; !used[strings.ToLower(n)] {
			return n
		}
	}
}

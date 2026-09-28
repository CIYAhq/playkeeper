// Package netguardtest is iptables and ip6tables for the tests of code that
// uses netguard: each family's filter table, listed as iptables -S lists it,
// with the matches and defaults iptables writes in, so a rule never reads
// back the way it was written; checked with -C; and changed only by
// iptables-restore --noflush, all or nothing. Like the real one, restore
// empties a chain its script declares.
package netguardtest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Tables are the machine's filter tables.
type Tables struct {
	mu       sync.Mutex
	fams     map[string]*table
	calls    []string
	restores []string
	// Fail, when set and it returns an error for a command, makes it fail.
	Fail func(name string, args []string) error
}

type table struct {
	chains []string // in the order -S lists them
	policy map[string]string
	rules  map[string][]string
	quoted bool
}

// New is a machine running Docker and ufw, with both families: INPUT
// accepts replies and the dashboard's port, FORWARD jumps to Docker's
// chains, and DOCKER-USER is empty.
func New() *Tables {
	ts := &Tables{fams: map[string]*table{}}
	for _, fam := range []string{"iptables", "ip6tables"} {
		t := &table{policy: map[string]string{"INPUT": "DROP", "FORWARD": "DROP", "OUTPUT": "ACCEPT"}, rules: map[string][]string{}}
		t.chains = []string{"INPUT", "FORWARD", "OUTPUT", "DOCKER", "DOCKER-FORWARD", "DOCKER-USER"}
		ts.fams[fam] = t
		for _, r := range [][]string{
			{"INPUT", "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
			{"INPUT", "-p", "tcp", "--dport", "8443", "-j", "ACCEPT"},
			{"FORWARD", "-j", "DOCKER-USER"},
			{"FORWARD", "-j", "DOCKER-FORWARD"},
		} {
			ts.Append(fam, r[0], r[1:]...)
		}
	}
	return ts
}

// Run is a netguard.Runner.
func (ts *Tables) Run(ctx context.Context, input, name string, args ...string) (string, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.calls = append(ts.calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
	if ts.Fail != nil {
		if err := ts.Fail(name, args); err != nil {
			return "", err
		}
	}
	fam, restore := strings.CutSuffix(name, "-restore")
	t := ts.fams[fam]
	if t == nil {
		return "", fmt.Errorf("%s: can't initialize %s table `filter': Table does not exist", name, fam)
	}
	args = withoutWait(args)
	if restore {
		if !slices.Contains(args, "--noflush") {
			return "", errors.New(name + " without --noflush would empty every chain")
		}
		ts.restores = append(ts.restores, input)
		next, err := t.restore(fam, input)
		if err != nil {
			return "", err
		}
		ts.fams[fam] = next
		return "", nil
	}
	switch {
	case len(args) == 1 && args[0] == "-S":
		return t.list(), nil
	case len(args) > 2 && args[0] == "-C":
		if slices.Contains(t.rules[args[1]], canon(fam, args[2:])) {
			return "", nil
		}
		return "", fmt.Errorf("%s: Bad rule (does a matching rule exist in that chain?)", fam)
	}
	return "", fmt.Errorf("%s: the fake doesn't do %q", fam, args)
}

func withoutWait(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-w" {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func (t *table) list() string {
	var b strings.Builder
	for _, c := range t.chains {
		if p, ok := t.policy[c]; ok {
			fmt.Fprintf(&b, "-P %s %s\n", c, p)
		} else {
			fmt.Fprintf(&b, "-N %s\n", c)
		}
	}
	for _, c := range t.chains {
		for _, r := range t.rules[c] {
			if t.quoted {
				w := strings.Fields(r)
				if i := slices.Index(w, "--comment"); i >= 0 && i+1 < len(w) {
					w[i+1] = `"` + w[i+1] + `"`
				}
				r = strings.Join(w, " ")
			}
			fmt.Fprintf(&b, "-A %s %s\n", c, r)
		}
	}
	return b.String()
}

// restore applies a restore script to a copy of the table, which is the
// table once it has all worked.
func (t *table) restore(fam, script string) (*table, error) {
	next := &table{chains: slices.Clone(t.chains), policy: t.policy, rules: map[string][]string{}, quoted: t.quoted}
	for c, rs := range t.rules {
		next.rules[c] = slices.Clone(rs)
	}
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		w := split(line)
		switch {
		case line == "" || line == "*filter" || line == "COMMIT" || strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, ":"):
			c := strings.Fields(line[1:])[0]
			if !slices.Contains(next.chains, c) {
				next.chains = append(next.chains, c)
			}
			next.rules[c] = nil
		case len(w) > 2 && (w[0] == "-D" || w[0] == "-I" || w[0] == "-A"):
			c, rest := w[1], w[2:]
			if !slices.Contains(next.chains, c) {
				return nil, fmt.Errorf("%s-restore: chain %s doesn't exist: %s", fam, c, line)
			}
			switch w[0] {
			case "-D":
				i := slices.Index(next.rules[c], canon(fam, rest))
				if i < 0 {
					return nil, fmt.Errorf("%s-restore: no such rule to delete: %s", fam, line)
				}
				next.rules[c] = slices.Delete(next.rules[c], i, i+1)
			case "-I":
				pos := 1
				if n, err := strconv.Atoi(rest[0]); err == nil {
					pos, rest = n, rest[1:]
				}
				if pos < 1 || pos > len(next.rules[c])+1 {
					return nil, fmt.Errorf("%s-restore: index of insertion too big: %s", fam, line)
				}
				next.rules[c] = slices.Insert(next.rules[c], pos-1, canon(fam, rest))
			case "-A":
				next.rules[c] = append(next.rules[c], canon(fam, rest))
			}
		default:
			return nil, fmt.Errorf("%s-restore: the fake doesn't read %q", fam, line)
		}
	}
	return next, nil
}

// split splits a rule into its words, as iptables-restore does: double
// quotes hold a word with spaces in it.
func split(s string) []string {
	var out []string
	var cur strings.Builder
	quoted, have := false, false
	for _, c := range s {
		switch {
		case c == '"':
			quoted, have = !quoted, true
		case c == ' ' && !quoted:
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteRune(c)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out
}

var (
	states = []string{"INVALID", "NEW", "RELATED", "ESTABLISHED", "UNTRACKED"}
	// head are the options -S lists first, in this order.
	head = []string{"-s", "-d", "-i", "-o", "-p"}
)

// canon is a rule as iptables -S lists it: addresses before interfaces and
// the protocol, the protocol's match written in, conntrack states in
// iptables' order, and REJECT's default reply spelled out.
func canon(fam string, args []string) string {
	var first, rest []string
	proto := ""
	for _, flag := range head {
		for i := 0; i+1 < len(args); i++ {
			if args[i] != flag {
				continue
			}
			if i > 0 && args[i-1] == "!" {
				first = append(first, "!")
			}
			first = append(first, flag, strings.Trim(args[i+1], `"`))
			if flag == "-p" {
				proto = args[i+1]
			}
		}
	}
	for i := 0; i < len(args); i++ {
		switch {
		case slices.Contains(head, args[i]) && i+1 < len(args):
			i++
		case args[i] == "!" && i+1 < len(args) && slices.Contains(head, args[i+1]):
		default:
			rest = append(rest, strings.Trim(args[i], `"`))
		}
	}
	if proto == "tcp" || proto == "udp" {
		if i := slices.IndexFunc(rest, func(w string) bool { return w == "--dport" || w == "--sport" }); i >= 0 && !slices.Contains(rest, proto) {
			if i > 0 && rest[i-1] == "!" {
				i--
			}
			rest = slices.Insert(rest, i, "-m", proto)
		}
	}
	for i, w := range rest {
		if i > 0 && rest[i-1] == "--ctstate" {
			var in []string
			for _, s := range states {
				if slices.Contains(strings.Split(w, ","), s) {
					in = append(in, s)
				}
			}
			rest[i] = strings.Join(in, ",")
		}
	}
	if slices.Contains(rest, "REJECT") && !slices.Contains(rest, "--reject-with") {
		reply := "icmp-port-unreachable"
		if fam == "ip6tables" {
			reply = "icmp6-port-unreachable"
		}
		rest = append(rest, "--reject-with", reply)
	}
	return strings.Join(append(first, rest...), " ")
}

// Rules is chain's rules in fam (iptables or ip6tables), as -S lists them
// but without "-A chain".
func (ts *Tables) Rules(fam, chain string) []string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return slices.Clone(ts.fams[fam].rules[chain])
}

// Append adds a rule at the end of chain, as ufw and Docker do.
func (ts *Tables) Append(fam, chain string, args ...string) {
	ts.Insert(fam, chain, len(ts.Rules(fam, chain))+1, args...)
}

// Insert adds a rule at position pos of chain (1 is the top), as fail2ban
// and an admin's own rules do.
func (ts *Tables) Insert(fam, chain string, pos int, args ...string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t := ts.fams[fam]
	t.rules[chain] = slices.Insert(t.rules[chain], pos-1, canon(fam, args))
}

// Flush empties chain, as a firewall reloading its rules may.
func (ts *Tables) Flush(fam, chain string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.fams[fam].rules[chain] = nil
}

// DeleteChain removes chain and its rules, as when Docker doesn't manage
// the firewall.
func (ts *Tables) DeleteChain(fam, chain string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t := ts.fams[fam]
	t.chains = slices.DeleteFunc(t.chains, func(c string) bool { return c == chain })
	delete(t.rules, chain)
	for c, rs := range t.rules {
		t.rules[c] = slices.DeleteFunc(rs, func(r string) bool { return strings.HasSuffix(r, "-j "+chain) })
	}
}

// NewChain makes an empty chain, as Docker does when it starts.
func (ts *Tables) NewChain(fam, chain string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t := ts.fams[fam]
	if !slices.Contains(t.chains, chain) {
		t.chains = append(t.chains, chain)
	}
}

// RemoveFamily makes fam's command fail, as ip6tables does where IPv6 is
// off.
func (ts *Tables) RemoveFamily(fam string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	delete(ts.fams, fam)
}

// QuoteComments makes -S list comments in double quotes, as older
// iptables did.
func (ts *Tables) QuoteComments(fam string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.fams[fam].quoted = true
}

// Restores are the scripts iptables-restore and ip6tables-restore were given,
// in order.
func (ts *Tables) Restores() []string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return slices.Clone(ts.restores)
}

// Calls are the commands run so far, each with its arguments.
func (ts *Tables) Calls() []string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return slices.Clone(ts.calls)
}

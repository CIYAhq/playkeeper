package names

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckNameAcceptsOnlyPlainDNSLabels(t *testing.T) {
	for _, tc := range []struct {
		name, problem string
	}{
		{"alice", ""},
		{"abc", ""},
		{"a1-b2", ""},
		{"123", ""},
		{strings.Repeat("a", 32), ""},
		{"ab", ProblemTooShort},
		{"", ProblemTooShort},
		{strings.Repeat("a", 33), ProblemTooLong},
		{"Alice", ProblemCharacters},
		{"al_ice", ProblemCharacters},
		{"al.ice", ProblemCharacters},
		{"alïce", ProblemCharacters},
		{"al ice", ProblemCharacters},
		{"ali/ce", ProblemCharacters},
		{"-alice", ProblemHyphenEdge},
		{"alice-", ProblemHyphenEdge},
		{"xn--80ak6aa92e", ProblemDoubleHyphen},
		{"al--ice", ProblemDoubleHyphen},
	} {
		err := CheckName(tc.name)
		if tc.problem == "" {
			if err != nil {
				t.Errorf("CheckName(%q) = %v, want ok", tc.name, err)
			}
			continue
		}
		var e *Error
		if !errors.As(err, &e) || e.Code != CodeInvalidName || e.Params["problem"] != tc.problem || !strings.HasSuffix(e.Message, ".") {
			t.Errorf("CheckName(%q) = %#v, want %s with a sentence", tc.name, err, tc.problem)
		}
	}
}

func TestServerLabelsFollowTheSameRulesFromOneCharacter(t *testing.T) {
	for _, ok := range []string{"a", "survival", "mc-2"} {
		if err := CheckServerLabel(ok); err != nil {
			t.Errorf("CheckServerLabel(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "@", "_minecraft", "a.b", "Survival", "-x", strings.Repeat("s", 33)} {
		var e *Error
		if err := CheckServerLabel(bad); !errors.As(err, &e) || e.Code != CodeInvalidServer {
			t.Errorf("CheckServerLabel(%q) = %v, want %s", bad, err, CodeInvalidServer)
		}
	}
}

func TestNormalizeNameTakesPastedAddresses(t *testing.T) {
	for in, want := range map[string]string{
		"  Alice ":               "alice",
		"alice.playkeeper.me":    "alice",
		"ALICE.PlayKeeper.ME.":   "alice",
		"alice.example.com":      "alice.example.com",
		"survival.alice":         "survival.alice",
		"alice.playkeeper.me.me": "alice.playkeeper.me.me",
	} {
		if got := NormalizeName(in, DefaultBase); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"alice.playkeeper.me":          "alice",
		" Alice.PlayKeeper.IO. ":       "alice",
		"alice.playkeeper.io.me":       "alice.playkeeper.io.me",
		"alice.example.com":            "alice.example.com",
		"survival.alice.playkeeper.io": "survival.alice",
	} {
		if got := NormalizeName(in, DefaultBase, PreviousBase); got != want {
			t.Errorf("NormalizeName(%q) with the previous base = %q, want %q", in, got, want)
		}
	}
	if got := NormalizeName("alice.playkeeper.io", DefaultBase); got != "alice.playkeeper.io" {
		t.Errorf("NormalizeName without the previous base = %q", got)
	}
}

func TestAddressHelpers(t *testing.T) {
	if got := ServerAddress("", "alice", "playkeeper.me"); got != "alice.playkeeper.me" {
		t.Errorf("bare server address = %s", got)
	}
	if got := ServerAddress("survival", "alice", "playkeeper.me"); got != "survival.alice.playkeeper.me" {
		t.Errorf("server address = %s", got)
	}
	if got := ChallengeFQDN("alice", "playkeeper.me"); got != "_acme-challenge.alice.playkeeper.me" {
		t.Errorf("challenge fqdn = %s", got)
	}
}

func TestBaseOfKnowsTheCurrentAndThePreviousBase(t *testing.T) {
	for host, want := range map[string]string{
		"alice.playkeeper.me":           DefaultBase,
		"Survival.Alice.PlayKeeper.ME.": DefaultBase,
		"alice.playkeeper.me:8443":      DefaultBase,
		"playkeeper.me":                 DefaultBase,
		"alice.playkeeper.io":           PreviousBase,
		"alice.playkeeper.io:8443":      PreviousBase,
		"playkeeper.io":                 PreviousBase,
		"alice.example.com":             "",
		"alice.notplaykeeper.me":        "",
		"playkeeper.me.example.com":     "",
		"5.75.160.99:8443":              "",
		"":                              "",
	} {
		if got := BaseOf(host); got != want {
			t.Errorf("BaseOf(%q) = %q, want %q", host, got, want)
		}
	}
}

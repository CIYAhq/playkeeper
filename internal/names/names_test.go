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
		"alice.playkeeper.io":    "alice",
		"ALICE.PlayKeeper.IO.":   "alice",
		"alice.example.com":      "alice.example.com",
		"survival.alice":         "survival.alice",
		"alice.playkeeper.io.io": "alice.playkeeper.io.io",
	} {
		if got := NormalizeName(in, DefaultBase); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAddressHelpers(t *testing.T) {
	if got := ServerAddress("", "alice", "playkeeper.io"); got != "alice.playkeeper.io" {
		t.Errorf("bare server address = %s", got)
	}
	if got := ServerAddress("survival", "alice", "playkeeper.io"); got != "survival.alice.playkeeper.io" {
		t.Errorf("server address = %s", got)
	}
	if got := ChallengeFQDN("alice", "playkeeper.io"); got != "_acme-challenge.alice.playkeeper.io" {
		t.Errorf("challenge fqdn = %s", got)
	}
}

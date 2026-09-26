package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCanonicalID(t *testing.T) {
	longest := `"` + strings.Repeat("x", maxIDBytes-2) + `"`
	for _, tc := range []struct {
		raw, want string // want is empty for an id that must be refused
	}{
		{`7`, `7`},
		{`-7`, `-7`},
		{` 42 `, `42`},
		{`123456789012345678901234567890`, `123456789012345678901234567890`},
		{`"a"`, `"a"`},
		{`"\u0061"`, `"a"`},
		{`"a\/b"`, `"a/b"`},
		{`"<tag>"`, `"\u003ctag\u003e"`},
		{`"\u003ctag\u003e"`, `"\u003ctag\u003e"`},
		{`""`, `""`},
		{longest, longest},
		{`"` + strings.Repeat("x", maxIDBytes-1) + `"`, ""},
		{`1.5`, ""},
		{`1e2`, ""},
		{`1E2`, ""},
		{`01`, ""},
		{`-`, ""},
		{`true`, ""},
		{`null`, ""},
		{`{}`, ""},
		{`[]`, ""},
		{``, ""},
		{`"unterminated`, ""},
	} {
		got, ok := canonicalID(json.RawMessage(tc.raw))
		if ok != (tc.want != "") || string(got) != tc.want {
			t.Errorf("%s: got %s, %v; want %s", tc.raw, got, ok, tc.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	for _, tc := range []struct {
		s    string
		max  int
		want string
	}{
		{"short", 10, "short"},
		{"exactly 10", 10, "exactly 10"},
		{"eleven long", 10, "eleven lon…"},
		{"ééééé", 4, "éé…"},
		{"ééééé", 5, "éé…"},
		{"😀😀", 5, "😀…"},
		{"😀😀", 3, "…"},
	} {
		if got := truncate(tc.s, tc.max); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.s, tc.max, got, tc.want)
		}
	}
}

func TestSelfReportedClientsAreSafeToLog(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want ClientInfo
	}{
		{`{"name":"claude-code","version":"2.1.0"}`, ClientInfo{Name: "claude-code", Version: "2.1.0"}},
		{`{"name":"cursor"}`, ClientInfo{Name: "cursor"}},
		{`{"name":"evil\r\nlevel=ERROR msg=forged","version":"1"}`, ClientInfo{Name: "evillevel=ERROR msg=forged", Version: "1"}},
		{`{"name":"\u001b[31mred\u001b[0m\u0000"}`, ClientInfo{Name: "[31mred[0m"}},
		{`{"name":"café ☕"}`, ClientInfo{Name: "caf "}},
		{`{"name":"` + strings.Repeat("n", 70) + `"}`, ClientInfo{Name: strings.Repeat("n", 64)}},
		// Malformed values are ignored rather than refused.
		{`{"name":5,"version":"1"}`, ClientInfo{}},
		{`"cursor"`, ClientInfo{}},
		{`null`, ClientInfo{}},
		{``, ClientInfo{}},
	} {
		if got := parseClientInfo(json.RawMessage(tc.raw)); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.raw, got, tc.want)
		}
	}
}

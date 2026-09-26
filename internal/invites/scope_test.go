package invites

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestScopeColumn(t *testing.T) {
	for _, tc := range []struct {
		scope Scope
		col   string
	}{
		{AllServers(), "*"},
		{OnlyServers(serverID), serverID},
		{OnlyServers(serverID, otherServer), otherServer + "," + serverID},
	} {
		if got := tc.scope.String(); got != tc.col {
			t.Errorf("%+v is stored as %q, want %q", tc.scope, got, tc.col)
		}
		back, err := ParseScope(tc.col)
		if err != nil || back.All != tc.scope.All || back.String() != tc.col {
			t.Errorf("ParseScope(%q) = %+v, %v", tc.col, back, err)
		}
	}
	if s, err := ParseScope(serverID + "," + otherServer); err != nil || !slices.Equal(s.Servers, []string{otherServer, serverID}) {
		t.Errorf("an unsorted column reads as %+v, %v", s, err)
	}
	var many []string
	for range MaxScopeServers + 1 {
		many = append(many, newID())
	}
	for _, col := range []string{"", ",", "*,*", "**", "all", serverID + ",", "," + serverID, serverID + ",," + otherServer,
		serverID + "," + serverID, "*," + serverID, strings.ToUpper(serverID), " " + serverID, "../etc/pas", serverID + ";" + otherServer,
		strings.Join(many, ",")} {
		if s, err := ParseScope(col); err == nil {
			t.Errorf("ParseScope(%q) = %+v", col, s)
		} else if len(col) > 0 && strings.Contains(err.Error(), col) {
			t.Errorf("the error repeats the column: %v", err)
		}
	}
}

func TestScopeCoversAndWithin(t *testing.T) {
	some := OnlyServers(serverID, otherServer)
	for _, tc := range []struct {
		scope  Scope
		server string
		want   bool
	}{
		{AllServers(), serverID, true},
		{AllServers(), "", false},
		{AllServers(), "not a server", false},
		{some, serverID, true},
		{some, thirdServer, false},
		{Scope{}, serverID, false},
		{OnlyServers(""), "", false},
	} {
		if got := tc.scope.Covers(tc.server); got != tc.want {
			t.Errorf("%+v covers %q: %v", tc.scope, tc.server, got)
		}
	}
	for _, tc := range []struct {
		inner, outer Scope
		want         bool
	}{
		{AllServers(), AllServers(), true},
		{some, AllServers(), true},
		{OnlyServers(serverID), some, true},
		{some, some, true},
		{AllServers(), some, false},
		{some, OnlyServers(serverID), false},
		{OnlyServers(thirdServer), some, false},
		{OnlyServers(serverID), Scope{}, false},
	} {
		if got := tc.inner.Within(tc.outer); got != tc.want {
			t.Errorf("%+v within %+v: %v", tc.inner, tc.outer, got)
		}
	}
}

func TestScopeNarrow(t *testing.T) {
	s := OnlyServers(serverID, otherServer)
	if got, ok := s.narrow([]string{thirdServer, serverID}); !ok || !slices.Equal(got.Servers, []string{serverID}) {
		t.Errorf("narrowed to %+v, %v", got, ok)
	}
	if got, ok := s.narrow([]string{thirdServer}); ok || len(got.Servers) != 0 {
		t.Errorf("nothing left, yet %+v, %v", got, ok)
	}
	if got, ok := AllServers().narrow(nil); !ok || !got.All {
		t.Errorf("all servers narrowed to %+v, %v", got, ok)
	}
	if !slices.Equal(s.Servers, []string{serverID, otherServer}) {
		t.Error("narrow changed its receiver")
	}
}

func TestScopeJSON(t *testing.T) {
	for _, tc := range []struct {
		scope Scope
		want  string
	}{
		{AllServers(), `{"all":true}`},
		{OnlyServers(serverID), `{"servers":["` + serverID + `"]}`},
		{Scope{}, `{}`},
	} {
		if b, _ := json.Marshal(tc.scope); string(b) != tc.want {
			t.Errorf("%+v as JSON: %s, want %s", tc.scope, b, tc.want)
		}
	}
	b, _ := json.Marshal(Invite{Kind: KindPlayer})
	if strings.Contains(string(b), "servers") {
		t.Errorf("a friend invite's JSON names servers: %s", b)
	}
}

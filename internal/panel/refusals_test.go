package panel

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRefusalSinkIsBounded(t *testing.T) {
	var k refusalSink
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	rows := 0
	for i := range 3 * maxRefusalKinds {
		rows += len(k.note(fmt.Sprint("join x 203.0.113.", i), "x", auditRow{at: at, actor: "(unknown machine)", detail: fmt.Sprint(i)}))
	}
	if len(k.runs) != maxRefusalKinds || rows != maxRefusalKinds {
		t.Fatalf("%d kinds counted, %d rows", len(k.runs), rows)
	}
	flushed := k.flush(at.Add(time.Minute), false)
	if len(flushed) != 1 || flushed[0].actor != "(many)" || !strings.HasPrefix(flushed[0].detail, fmt.Sprintf("refusals of many kinds · %d more", 2*maxRefusalKinds)) {
		t.Fatalf("flushed %d rows: %+v", len(flushed), flushed)
	}
	if len(k.flush(at.Add(refusalWindow), false)) != 0 || len(k.runs) != 0 {
		t.Fatalf("after the window: %d kinds", len(k.runs))
	}
}

func TestRefusalCountsSayWhen(t *testing.T) {
	var k refusalSink
	at := time.Date(2026, 9, 24, 12, 0, 30, 0, time.UTC)
	first := auditRow{at: at, actor: "(unknown machine)", action: "machine.join", result: "refused", detail: "join_code_wrong from 203.0.113.7"}
	if got := k.note("k", "join_code_wrong from 203.0.113.7", first); len(got) != 1 || got[0] != first {
		t.Fatalf("the first refusal: %+v", got)
	}
	k.note("k", "", auditRow{at: at.Add(20 * time.Second)})
	k.note("k", "", auditRow{at: at.Add(3 * time.Minute)})
	got := k.flush(at.Add(4*time.Minute), false)
	if len(got) != 1 || got[0].detail != "join_code_wrong from 203.0.113.7 · 2 more between 12:00 and 12:03 UTC" ||
		got[0].actor != first.actor || !got[0].at.Equal(at.Add(3*time.Minute)) {
		t.Fatalf("the count: %+v", got)
	}
	if got := k.flush(at.Add(5*time.Minute), false); len(got) != 0 {
		t.Fatalf("nothing new was counted: %+v", got)
	}
	// Past the window, the next refusal writes the count it ended and is
	// the first of a new run.
	k.note("k", "", auditRow{at: at.Add(6 * time.Minute)})
	if got := k.note("k", "again", auditRow{at: at.Add(11 * time.Minute), detail: "new"}); len(got) != 2 ||
		got[0].detail != "join_code_wrong from 203.0.113.7 · 1 more at 12:06 UTC" || got[1].detail != "new" {
		t.Fatalf("a window later: %+v", got)
	}
}

func TestNetworkOf(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.7":           "203.0.113.7",
		"::ffff:203.0.113.7":    "203.0.113.7",
		"2001:db8:1:2:3:4:5:6":  "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::1":  "2001:db8:1:2::/64",
		"not an address at all": "not an address at all",
	} {
		if got := networkOf(in); got != want {
			t.Errorf("networkOf(%q) = %q, want %q", in, got, want)
		}
	}
}

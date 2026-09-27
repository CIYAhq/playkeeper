package schedule

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

var validNow = utc("2026-06-01T00:00:00Z")

func validSchedule(kind Kind) Schedule {
	s := Schedule{ID: "a1b2c3", ServerID: "srv1", Kind: kind, Enabled: true,
		Timing: Timing{Kind: Daily, TimeZone: "Europe/Berlin", At: "04:00"}}
	switch kind {
	case KindRestart:
		s.Payload = Payload{WarnSeconds: DefaultWarnings(), Message: "Back in a minute!"}
	case KindAnnouncement:
		s.Payload = Payload{Message: "Vote for the server!"}
	case KindCommand:
		s.Payload = Payload{Command: "save-all"}
	case KindBackup:
		s.Payload = Payload{Note: "Nightly"}
	}
	return s
}

func TestValidateAcceptsEachKind(t *testing.T) {
	for _, k := range []Kind{KindRestart, KindBackup, KindAnnouncement, KindCommand} {
		if err := validSchedule(k).Validate(validNow); err != nil {
			t.Errorf("%s: %v", k, err)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		edit   func(*Schedule)
		code   string
		field  string
		params map[string]any
	}{
		{"id", func(s *Schedule) { s.ID = "../x" }, "id_invalid", "id", nil},
		{"server", func(s *Schedule) { s.ServerID = "" }, "server_invalid", "serverId", nil},
		{"long name", func(s *Schedule) { s.Name = strings.Repeat("é", 65) }, "name_too_long", "name", map[string]any{"max": 64}},
		{"name newline", func(s *Schedule) { s.Name = "a\nb" }, "name_invalid", "name", nil},
		{"kind", func(s *Schedule) { s.Kind = "explode" }, "kind_invalid", "kind", nil},
		{"zone", func(s *Schedule) { s.Timing.TimeZone = "Moon/Base" }, "time_zone_invalid", "timing.timeZone", map[string]any{"timeZone": "Moon/Base"}},
		{"warning too early", func(s *Schedule) { s.Payload.WarnSeconds = []int{7200} }, "warning_range", "payload.warnSeconds", map[string]any{"value": 7200, "min": 5, "max": 3600}},
		{"warning too short", func(s *Schedule) { s.Payload.WarnSeconds = []int{3} }, "warning_range", "payload.warnSeconds", nil},
		{"warning not whole minutes", func(s *Schedule) { s.Payload.WarnSeconds = []int{90} }, "warning_whole_minutes", "payload.warnSeconds", map[string]any{"value": 90}},
		{"warning twice", func(s *Schedule) { s.Payload.WarnSeconds = []int{60, 60} }, "warning_duplicate", "payload.warnSeconds", nil},
		{"too many warnings", func(s *Schedule) { s.Payload.WarnSeconds = []int{5, 10, 15, 20, 25, 30, 35, 40, 45} }, "warnings_too_many", "payload.warnSeconds", map[string]any{"max": 8}},
		{"if empty", func(s *Schedule) { s.Payload.IfEmpty = "maybe" }, "if_empty_invalid", "payload.ifEmpty", nil},
		{"restart message newline", func(s *Schedule) { s.Payload.Message = "one\ntwo" }, "message_invalid", "payload.message", nil},
		{"restart too often", func(s *Schedule) { s.Timing = Timing{Kind: Cron, TimeZone: "UTC", Cron: "*/30 * * * *"} }, "too_often", "timing.cron", map[string]any{"gapMinutes": 30, "minMinutes": 60}},
		{"restart warnings longer than the gap", func(s *Schedule) {
			s.Timing = Timing{Kind: Interval, TimeZone: "UTC", At: "00:00", EveryHours: 1}
			s.Payload.WarnSeconds = []int{3600}
		}, "too_often", "timing.everyHours", map[string]any{"gapMinutes": 60, "minMinutes": 75}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := validSchedule(KindRestart)
			c.edit(&s)
			checkValidation(t, s.Validate(validNow), c.code, c.field, c.params)
		})
	}

	other := []struct {
		name  string
		kind  Kind
		edit  func(*Schedule)
		code  string
		field string
	}{
		{"announcement empty", KindAnnouncement, func(s *Schedule) { s.Payload.Message = "" }, "message_required", "payload.message"},
		{"announcement too long", KindAnnouncement, func(s *Schedule) { s.Payload.Message = strings.Repeat("a", 257) }, "message_too_long", "payload.message"},
		{"announcement direction override", KindAnnouncement, func(s *Schedule) { s.Payload.Message = "hi \u202egnp.exe" }, "message_invalid", "payload.message"},
		{"announcement line separator", KindAnnouncement, func(s *Schedule) { s.Payload.Message = "a\u2028b" }, "message_invalid", "payload.message"},
		{"announcement bad utf-8", KindAnnouncement, func(s *Schedule) { s.Payload.Message = "a\xffb" }, "message_invalid", "payload.message"},
		{"announcement every minute", KindAnnouncement, func(s *Schedule) { s.Timing = Timing{Kind: Cron, TimeZone: "UTC", Cron: "* * * * *"} }, "too_often", "timing.cron"},
		{"command empty", KindCommand, func(s *Schedule) { s.Payload.Command = "" }, "command_required", "payload.command"},
		{"command stop", KindCommand, func(s *Schedule) { s.Payload.Command = "stop" }, "command_use_restart", "payload.command"},
		{"command op", KindCommand, func(s *Schedule) { s.Payload.Command = "op Steve" }, "command_not_allowed", "payload.command"},
		{"backup note long", KindBackup, func(s *Schedule) { s.Payload.Note = strings.Repeat("n", 201) }, "note_too_long", "payload.note"},
		{"backup note control", KindBackup, func(s *Schedule) { s.Payload.Note = "a\rb" }, "note_invalid", "payload.note"},
		{"backup every 30 minutes", KindBackup, func(s *Schedule) { s.Timing = Timing{Kind: Cron, TimeZone: "UTC", Cron: "0,30 * * * *"} }, "too_often", "timing.cron"},
	}
	for _, c := range other {
		t.Run(c.name, func(t *testing.T) {
			s := validSchedule(c.kind)
			c.edit(&s)
			checkValidation(t, s.Validate(validNow), c.code, c.field, nil)
		})
	}
}

func checkValidation(t *testing.T, err error, code, field string, params map[string]any) {
	t.Helper()
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("Validate = %v, want a *ValidationError with code %s", err, code)
	}
	if ve.Code != code || ve.Field != field {
		t.Fatalf("Validate = %s on %s (%s), want %s on %s", ve.Code, ve.Field, ve.Msg, code, field)
	}
	if !strings.HasSuffix(ve.Msg, ".") || strings.ToUpper(ve.Msg[:1]) != ve.Msg[:1] {
		t.Fatalf("message %q is not a sentence", ve.Msg)
	}
	for k, v := range params {
		if ve.Params[k] != v {
			t.Fatalf("params = %v, want %s=%v", ve.Params, k, v)
		}
	}
}

func TestSpacingAllowsFrequentCommands(t *testing.T) {
	s := validSchedule(KindCommand)
	s.Timing = Timing{Kind: Cron, TimeZone: "UTC", Cron: "*/5 * * * *"}
	if err := s.Validate(validNow); err != nil {
		t.Fatal(err)
	}
	r := validSchedule(KindRestart)
	r.Timing = Timing{Kind: Interval, TimeZone: "UTC", At: "03:00", EveryHours: 4}
	if err := r.Validate(validNow); err != nil {
		t.Fatal(err)
	}
}

func TestNormalize(t *testing.T) {
	s := Schedule{Kind: KindRestart, Name: "  Nightly  ",
		Payload: Payload{WarnSeconds: []int{10, 600, 60, 600, 30}, Message: " hi ", Command: "save-all", Note: "x"}}
	n := s.Normalize()
	if n.Name != "Nightly" || !slices.Equal(n.Payload.WarnSeconds, []int{600, 60, 30, 10}) || n.Payload.Message != "hi" ||
		n.Payload.Command != "" || n.Payload.Note != "" {
		t.Fatalf("Normalize = %+v", n)
	}
	if s.Payload.WarnSeconds[0] != 10 {
		t.Fatal("Normalize changed the caller's slice")
	}
	c := Schedule{Kind: KindCommand, Payload: Payload{Command: "  /weather   clear  ", Message: "x", WarnSeconds: []int{5}}}.Normalize()
	if c.Payload.Command != "weather clear" || c.Payload.Message != "" || c.Payload.WarnSeconds != nil {
		t.Fatalf("Normalize = %+v", c.Payload)
	}
}

func TestActor(t *testing.T) {
	if a := Actor("0123abcd"); a != "schedule:0123abcd" {
		t.Fatalf("Actor = %q", a)
	}
	if id, ok := ParseActor(Actor("0123abcd")); !ok || id != "0123abcd" {
		t.Fatalf("ParseActor = %q, %v", id, ok)
	}
	for _, bad := range []string{"user:bob", "schedule:", "schedule:../x", "schedule:ABC", "schedule:" + strings.Repeat("a", 33)} {
		if _, ok := ParseActor(bad); ok {
			t.Errorf("ParseActor(%q) accepted", bad)
		}
	}
	if len(Actor(strings.Repeat("a", 32))) > 64 {
		t.Fatal("an actor must fit the agent's 64-character limit")
	}
}

func TestScheduleJSON(t *testing.T) {
	due := utc("2026-06-01T02:00:00Z")
	s := validSchedule(KindRestart)
	s.LastRun = &Run{Due: due, Started: due.Add(-10 * time.Minute), Result: ResultSkipped, Reason: ReasonServerStopped}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"kind":"restart"`, `"timeZone":"Europe/Berlin"`, `"warnSeconds":[600,300,60,30,10]`, `"reason":"server_stopped"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON %s lacks %s", b, want)
		}
	}
	if strings.Contains(string(b), `"finished"`) {
		t.Errorf("JSON %s has an empty finished time", b)
	}
	var back Schedule
	if err := json.Unmarshal(b, &back); err != nil || !back.LastRun.Due.Equal(due) || back.Payload.Message != s.Payload.Message {
		t.Fatalf("round trip = %+v, %v", back, err)
	}
}

func TestRunText(t *testing.T) {
	cases := map[string]Run{
		"Skipped: the server was stopped.":                         {Result: ResultSkipped, Reason: ReasonServerStopped},
		"Missed: Playkeeper was not running at that time.":         {Result: ResultMissed, Reason: ReasonAgentDown},
		"Failed: something went wrong. docker is down":             {Result: ResultFailed, Reason: ReasonError, Detail: "docker is down"},
		"Skipped: the schedule was turned off before the restart.": {Result: ResultSkipped, Reason: ReasonTurnedOff},
		"Done.": {Result: ResultSucceeded},
	}
	for want, r := range cases {
		if got := r.Text(); got != want {
			t.Errorf("Text = %q, want %q", got, want)
		}
	}
}

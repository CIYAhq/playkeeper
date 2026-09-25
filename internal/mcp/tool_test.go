package mcp

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestToolsAreCheckedWhenTheyRegister(t *testing.T) {
	badName := func(name string) string {
		return fmt.Sprintf("mcp: tool name %q must be 1 to 128 characters from A-Z, a-z, 0-9, _, - and .", name)
	}
	const notReadOnly = "mcp: tool t: a tool that is not read-only needs the manage or owner scope"
	const inputObject = "mcp: tool t: the input schema must have type object"
	long := strings.Repeat("a", 128)
	for _, tc := range []struct {
		name string
		edit func(*Tool)
		want string // empty for a valid tool
	}{
		{"a valid tool", func(*Tool) {}, ""},
		{"every character a name may use", func(t *Tool) { t.Name = "Az09_-." }, ""},
		{"a name of 128 characters", func(t *Tool) { t.Name = long }, ""},
		{"a name of 129 characters", func(t *Tool) { t.Name = long + "a" }, badName(long + "a")},
		{"an empty name", func(t *Tool) { t.Name = "" }, badName("")},
		{"a space", func(t *Tool) { t.Name = "server status" }, badName("server status")},
		{"a slash", func(t *Tool) { t.Name = "servers/list" }, badName("servers/list")},
		{"a letter outside ASCII", func(t *Tool) { t.Name = "café" }, badName("café")},
		{"no description", func(t *Tool) { t.Description = "" }, "mcp: tool t: a description is required; models rely on it to pick tools"},
		{"no handler", func(t *Tool) { t.Handler = nil }, "mcp: tool t: the handler is missing"},
		{"no scope", func(t *Tool) { t.Scope = "" }, `mcp: tool t: unknown scope ""`},
		{"an unknown scope", func(t *Tool) { t.Scope = "admin" }, `mcp: tool t: unknown scope "admin"`},
		{"a destructive tool at the read scope", func(t *Tool) { t.Effect = Destructive }, notReadOnly},
		{"an additive tool at the read scope", func(t *Tool) { t.Effect = Additive }, notReadOnly},
		{"an additive tool at the manage scope", func(t *Tool) { t.Effect, t.Scope = Additive, ScopeManage }, ""},
		{"a destructive tool at the owner scope", func(t *Tool) { t.Effect, t.Scope = Destructive, ScopeOwner }, ""},
		{"a read-only tool at the owner scope", func(t *Tool) { t.Scope = ScopeOwner }, ""},
		{"an unknown effect", func(t *Tool) { t.Effect = 7 }, "mcp: tool t: unknown effect 7"},
		{"no input schema", func(t *Tool) { t.InputSchema = Schema{} }, inputObject},
		{"an input schema for a string", func(t *Tool) { t.InputSchema = Schema{Type: "string"} }, inputObject},
		{"an output schema", func(t *Tool) { t.OutputSchema = &Schema{Type: "object"} }, ""},
		{"an output schema for an array", func(t *Tool) { t.OutputSchema = &Schema{Type: "array", Items: &Schema{Type: "string"}} },
			"mcp: tool t: the output schema must have type object"},
		{"a broken output schema", func(t *Tool) {
			t.OutputSchema = &Schema{Type: "object", Properties: map[string]*Schema{"a": {Type: "strin"}}}
		}, `mcp: tool t: outputSchema.a: unknown type "strin"`},
	} {
		tl := toolWithInput(Schema{Type: "object"})
		tc.edit(&tl)
		if _, err := New(Options{Tools: []Tool{tl}}); errText(err) != tc.want {
			t.Errorf("%s:\ngot  %s\nwant %s", tc.name, errText(err), tc.want)
		}
	}

	one := toolWithInput(Schema{Type: "object"})
	if _, err := New(Options{Tools: []Tool{one, one}}); errText(err) != `mcp: tool name "t" is used twice` {
		t.Errorf("a repeated name: %v", err)
	}
}

func TestToolAnnotationsFollowTheEffect(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Tool)
		want string
	}{
		{"read-only", func(*Tool) {},
			`{"destructiveHint":false,"idempotentHint":true,"openWorldHint":false,"readOnlyHint":true}`},
		{"read-only and open-world", func(t *Tool) { t.OpenWorld = true },
			`{"destructiveHint":false,"idempotentHint":true,"openWorldHint":true,"readOnlyHint":true}`},
		{"additive", func(t *Tool) { t.Effect, t.Scope = Additive, ScopeManage },
			`{"destructiveHint":false,"idempotentHint":false,"openWorldHint":false,"readOnlyHint":false}`},
		{"additive and idempotent", func(t *Tool) { t.Effect, t.Scope, t.Idempotent = Additive, ScopeManage, true },
			`{"destructiveHint":false,"idempotentHint":true,"openWorldHint":false,"readOnlyHint":false}`},
		{"destructive, idempotent and open-world", func(t *Tool) {
			t.Effect, t.Scope, t.Idempotent, t.OpenWorld, t.Title = Destructive, ScopeOwner, true, true, "Install add-on"
		}, `{"destructiveHint":true,"idempotentHint":true,"openWorldHint":true,"readOnlyHint":false,"title":"Install add-on"}`},
		{"no effect declared", func(t *Tool) { t.Effect, t.Scope = Tool{}.Effect, ScopeManage },
			`{"destructiveHint":true,"idempotentHint":false,"openWorldHint":false,"readOnlyHint":false}`},
	} {
		tl := toolWithInput(Schema{Type: "object"})
		tc.edit(&tl)
		r, err := register(tl)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		b, err := json.Marshal(r.definition(version20260728)["annotations"])
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != tc.want {
			t.Errorf("%s:\ngot  %s\nwant %s", tc.name, b, tc.want)
		}
	}
}

func TestScopesAreHierarchical(t *testing.T) {
	all := []Scope{ScopeRead, ScopeManage, ScopeOwner, "", "admin"}
	for _, tc := range []struct {
		have, want []Scope
	}{
		{nil, nil},
		{[]Scope{ScopeRead}, []Scope{ScopeRead}},
		{[]Scope{ScopeManage}, []Scope{ScopeRead, ScopeManage}},
		{[]Scope{ScopeOwner}, []Scope{ScopeRead, ScopeManage, ScopeOwner}},
		{[]Scope{ScopeRead, ScopeManage}, []Scope{ScopeRead, ScopeManage}},
		{[]Scope{"admin"}, nil},
		{[]Scope{"Owner"}, nil},
		{[]Scope{"admin", ScopeManage}, []Scope{ScopeRead, ScopeManage}},
	} {
		p := Principal{ID: "token:t", Scopes: tc.have}
		var got []Scope
		for _, s := range all {
			if p.Allows(s) {
				got = append(got, s)
			}
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%q allows %q, want %q", tc.have, got, tc.want)
		}
	}
}

func TestScopeErrorsNameBothScopes(t *testing.T) {
	console := &registered{Tool: Tool{Name: "run_console_command", Scope: ScopeOwner}}
	for _, tc := range []struct {
		have []Scope
		want string
	}{
		{nil, "This token has no scope, but run_console_command needs the owner scope."},
		{[]Scope{"admin"}, "This token has no scope, but run_console_command needs the owner scope."},
		{[]Scope{ScopeRead}, "This token has the read scope, but run_console_command needs the owner scope."},
		{[]Scope{ScopeManage, ScopeRead}, "This token has the manage scope, but run_console_command needs the owner scope."},
	} {
		if got := scopeError(Principal{ID: "token:t", Scopes: tc.have}, console).Msg; got != tc.want {
			t.Errorf("%q:\ngot  %s\nwant %s", tc.have, got, tc.want)
		}
	}
}

func TestEffectString(t *testing.T) {
	for e, want := range map[Effect]string{Destructive: "destructive", Additive: "additive", ReadOnly: "read-only", 7: "Effect(7)"} {
		if got := e.String(); got != want {
			t.Errorf("%d: %q, want %q", int(e), got, want)
		}
	}
}

func TestToolErrorText(t *testing.T) {
	for _, tc := range []struct {
		err  ToolError
		want string
	}{
		{ToolError{Msg: "The server is offline."}, "The server is offline."},
		{ToolError{Msg: "The server is offline.", Hint: "Start it first."}, "The server is offline. Start it first."},
		{ToolError{Msg: "Two things are wrong:\n- one\n- two", Hint: "Fix both."}, "Two things are wrong:\n- one\n- two\nFix both."},
	} {
		if got := tc.err.text(); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
		if got := tc.err.Error(); got != tc.err.Msg {
			t.Errorf("Error() is %q, want the message alone", got)
		}
	}
}

func TestCallBindRefusesFieldsTheHandlerDoesNotKnow(t *testing.T) {
	var args struct {
		Server string `json:"server"`
		Lines  int    `json:"lines"`
	}
	c := &Call{Arguments: json.RawMessage(`{"server":"survival","lines":20}`)}
	if err := c.Bind(&args); err != nil || args.Server != "survival" || args.Lines != 20 {
		t.Errorf("got %+v, %v", args, err)
	}
	// A handler must not silently ignore a parameter its schema declares.
	c.Arguments = json.RawMessage(`{"server":"survival","force":true}`)
	if err := c.Bind(&args); err == nil || !strings.Contains(err.Error(), `unknown field "force"`) {
		t.Errorf("an unknown field gave %v", err)
	}
}

func TestLimiterWaitsAreExact(t *testing.T) {
	for _, tc := range []struct {
		capacity int
		wait     time.Duration // after the burst is spent
	}{
		{1, time.Minute},
		{10, 6 * time.Second},
		{30, 2 * time.Second},
		{120, 500 * time.Millisecond},
		{7, 8571428571 * time.Nanosecond},
	} {
		t.Run(strconv.Itoa(tc.capacity), func(t *testing.T) {
			clk := newClock()
			l := newLimiter(tc.capacity, time.Minute, clk.now)
			burst := func() {
				t.Helper()
				for i := range tc.capacity {
					if ok, wait := l.take("a"); !ok {
						t.Fatalf("request %d had to wait %v", i+1, wait)
					}
				}
			}
			refused := func(want time.Duration) {
				t.Helper()
				if ok, wait := l.peek("a"); ok || wait != want {
					t.Errorf("peek: %v, a wait of %v; want a wait of %v", ok, wait, want)
				}
				if ok, wait := l.take("a"); ok || wait != want {
					t.Errorf("take: %v, a wait of %v; want a wait of %v", ok, wait, want)
				}
			}
			burst()
			refused(tc.wait)
			// Refused requests cost nothing, and other keys are separate.
			refused(tc.wait)
			if ok, _ := l.take("b"); !ok {
				t.Error("another key was refused")
			}
			clk.add(tc.wait / 3)
			refused(tc.wait - tc.wait/3)
			clk.add(tc.wait - tc.wait/3)
			if ok, wait := l.peek("a"); !ok || wait != 0 {
				t.Errorf("after the wait: %v, %v", ok, wait)
			}
			if ok, _ := l.take("a"); !ok {
				t.Error("refused after the wait")
			}
			refused(tc.wait)
			// Idle time refills the burst but never beyond it.
			clk.add(time.Hour)
			burst()
			refused(tc.wait)
		})
	}
}

func TestLimiterMemoryIsBounded(t *testing.T) {
	clk := newClock()
	l := newLimiter(10, time.Minute, clk.now)
	for i := range maxKeys {
		l.take(strconv.Itoa(i))
	}
	if len(l.next) != maxKeys {
		t.Fatalf("%d keys", len(l.next))
	}
	// With every key still active, a new one displaces an arbitrary one.
	if ok, _ := l.take("new"); !ok || len(l.next) != maxKeys {
		t.Errorf("a new key: %v with %d keys", ok, len(l.next))
	}
	// Keys back to their full burst are dropped first.
	clk.add(time.Minute)
	l.take("newer")
	if len(l.next) != 1 {
		t.Errorf("%d keys after they all refilled", len(l.next))
	}
}

func TestRetryAfterRoundsUpToWholeSeconds(t *testing.T) {
	for d, want := range map[time.Duration]int{
		0:                                 1,
		time.Nanosecond:                   1,
		time.Second:                       1,
		time.Second + time.Nanosecond:     2,
		6 * time.Second:                   6,
		8571428571 * time.Nanosecond:      9,
		59*time.Second + time.Millisecond: 60,
	} {
		if got := retryAfter(d); got != want {
			t.Errorf("%v: %d, want %d", d, got, want)
		}
	}
}

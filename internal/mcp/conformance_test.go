package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// These tests speak MCP as a client would, over both transports and in
// every supported revision.

const serverInfoMeta = `{"io.modelcontextprotocol/serverInfo":{"name":"playkeeper","version":"1.2.3"}}`

// wantFields checks that obj has exactly the given fields, as compact JSON.
func wantFields(t *testing.T, obj map[string]any, want map[string]string) {
	t.Helper()
	if got, w := keys(t, obj), sortedKeys(want); !slices.Equal(got, w) {
		t.Errorf("fields %v, want %v", got, w)
	}
	for k, w := range want {
		if got := canon(t, obj[k]); got != w {
			t.Errorf("%s = %s, want %s", k, got, w)
		}
	}
}

func TestInitializeNegotiatesTheRevision(t *testing.T) {
	cases := []struct{ requested, want string }{
		{"2025-11-25", "2025-11-25"},
		{"2025-06-18", "2025-06-18"},
		{"2025-03-26", "2025-03-26"},
		{"2024-11-05", "2024-11-05"},
		// 2026-07-28 has no initialize, so initialize cannot select it.
		{"2026-07-28", "2025-11-25"},
		{"2099-01-01", "2025-11-25"},
		{"1999-01-01", "2025-11-25"},
		{"draft", "2025-11-25"},
	}
	eachTransport(t, func(t *testing.T, open opener) {
		for _, tc := range cases {
			t.Run(tc.requested, func(t *testing.T) {
				c := open(t, newFixture(t, nil), principal(ScopeOwner))
				wantFields(t, c.initialize(t, tc.requested).wantResult(t), map[string]string{
					"protocolVersion": `"` + tc.want + `"`,
					"capabilities":    `{"tools":{}}`,
					"serverInfo":      `{"name":"playkeeper","version":"1.2.3"}`,
					"instructions":    `"Use these tools to look after Minecraft servers."`,
				})
				c.notify(t, "notifications/initialized", nil)
				echo := dig(t, c.call(t, "tools/list", nil).wantResult(t), "tools", 0)
				if has(echo, "title") != atLeast(tc.want, version20250618) {
					t.Errorf("the tool list is not shaped for %s: %v", tc.want, echo)
				}
			})
		}
	})
}

func TestInitializeNeedsAProtocolVersion(t *testing.T) {
	cases := []struct{ name, raw string }{
		{"no params", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`},
		{"no version", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"capabilities":{}}}`},
		{"an empty version", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":""}}`},
		{"a number", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":20250618}}`},
	}
	eachTransport(t, func(t *testing.T, open opener) {
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				c := open(t, newFixture(t, nil), principal(ScopeOwner))
				r := c.send(t, tc.raw, 1)[0]
				if e := r.wantError(t, codeInvalidParams); e.Message != "initialize needs a protocolVersion string." || r.id() != "1" {
					t.Errorf("id %s: %q", r.id(), e.Message)
				}
				// A failed initialize starts nothing.
				c.call(t, "tools/list", nil).wantError(t, codeInvalidParams)
			})
		}
	})
}

func TestRequestsWithoutAProtocolVersionAreRefused(t *testing.T) {
	eachTransport(t, func(t *testing.T, open opener) {
		f := newFixture(t, nil)
		c := open(t, f, principal(ScopeOwner))
		for _, method := range []string{"tools/list", "tools/call", "server/discover", "resources/list"} {
			e := c.call(t, method, map[string]any{"name": "echo"}).wantError(t, codeInvalidParams)
			if !strings.HasPrefix(e.Message, "The request has no protocol version") || e.Data["hint"] == nil {
				t.Errorf("%s: %q %v", method, e.Message, e.Data)
			}
		}
		// Legacy revisions allow ping before initialize.
		if got := canon(t, c.call(t, "ping", nil).wantResult(t)); got != "{}" {
			t.Errorf("ping: %s", got)
		}
		if n := len(f.calls()); n != 0 {
			t.Errorf("%d tool calls ran", n)
		}
	})
}

func TestPingIsLegacyOnly(t *testing.T) {
	eachEra(t, func(t *testing.T, connect opener, version string) {
		r := connect(t, newFixture(t, nil), principal(ScopeRead)).call(t, "ping", nil)
		if isModern(version) {
			r.wantError(t, codeMethodNotFound)
			return
		}
		if got := canon(t, r.wantResult(t)); got != "{}" {
			t.Errorf("ping: %s", got)
		}
	})
}

func TestServerDiscover(t *testing.T) {
	eachEra(t, func(t *testing.T, connect opener, version string) {
		r := connect(t, newFixture(t, nil), principal(ScopeRead)).call(t, "server/discover", nil)
		if !isModern(version) {
			r.wantError(t, codeMethodNotFound)
			return
		}
		wantFields(t, r.wantResult(t), map[string]string{
			"supportedVersions": `["2026-07-28","2025-11-25","2025-06-18","2025-03-26","2024-11-05"]`,
			"capabilities":      `{"tools":{}}`,
			"instructions":      `"Use these tools to look after Minecraft servers."`,
			"ttlMs":             `300000`,
			"cacheScope":        `"private"`,
			"resultType":        `"complete"`,
			"_meta":             serverInfoMeta,
		})
	})
}

func TestToolsListIsShapedForTheRevision(t *testing.T) {
	annotations := map[string]string{
		"echo":           `{"destructiveHint":false,"idempotentHint":true,"openWorldHint":false,"readOnlyHint":true,"title":"Echo"}`,
		"restart_server": `{"destructiveHint":true,"idempotentHint":false,"openWorldHint":false,"readOnlyHint":false,"title":"Restart server"}`,
		"create_backup":  `{"destructiveHint":false,"idempotentHint":false,"openWorldHint":false,"readOnlyHint":false}`,
	}
	eachEra(t, func(t *testing.T, connect opener, version string) {
		res := connect(t, newFixture(t, nil), principal(ScopeOwner)).call(t, "tools/list", nil).wantResult(t)
		fields := []string{"tools"}
		if isModern(version) {
			fields = []string{"_meta", "cacheScope", "resultType", "tools", "ttlMs"}
			if res["cacheScope"] != "private" || canon(t, res["ttlMs"]) != "300000" || res["resultType"] != "complete" ||
				canon(t, res["_meta"]) != serverInfoMeta {
				t.Errorf("2026-07-28 fields: %v", res)
			}
		}
		if got := keys(t, res); !slices.Equal(got, fields) {
			t.Errorf("result fields %v, want %v", got, fields)
		}
		if got := toolNames(t, res); !slices.Equal(got, allTools) {
			t.Fatalf("tools %v, want %v", got, allTools)
		}
		defs := map[string]any{}
		for i, name := range allTools {
			defs[name] = dig(t, res, "tools", i)
		}
		annotated, titled := atLeast(version, version20250326), atLeast(version, version20250618)

		echo := defs["echo"]
		want := []string{"description", "inputSchema", "name"}
		if annotated {
			want = append(want, "annotations")
		}
		if titled {
			want = append(want, "title")
		}
		slices.Sort(want)
		if got := keys(t, echo); !slices.Equal(got, want) {
			t.Errorf("echo has fields %v, want %v", got, want)
		}
		if got := dig(t, echo, "description"); got != "Repeats the text." {
			t.Errorf("description %v", got)
		}
		if got := canon(t, dig(t, echo, "inputSchema")); got != `{"additionalProperties":false,"properties":{"text":{"maxLength":20,"minLength":1,"type":"string"},"times":{"default":1,"maximum":3,"minimum":1,"type":"integer"}},"required":["text"],"type":"object"}` {
			t.Errorf("inputSchema %s", got)
		}
		if titled && dig(t, echo, "title") != "Echo" {
			t.Errorf("title %v", dig(t, echo, "title"))
		}
		if has(defs["create_backup"], "title") {
			t.Error("create_backup has a title it was never given")
		}
		if has(defs["server_status"], "outputSchema") != titled {
			t.Errorf("outputSchema: %v", defs["server_status"])
		}
		if titled {
			if got := canon(t, dig(t, defs["server_status"], "outputSchema")); got != `{"additionalProperties":false,"properties":{"online":{"type":"boolean"},"players":{"minimum":0,"type":"integer"}},"required":["online","players"],"type":"object"}` {
				t.Errorf("outputSchema %s", got)
			}
		}
		for name, want := range annotations {
			if !annotated {
				if has(defs[name], "annotations") {
					t.Errorf("%s has annotations before 2025-03-26", name)
				}
				continue
			}
			if got := canon(t, dig(t, defs[name], "annotations")); got != want {
				t.Errorf("%s annotations %s, want %s", name, got, want)
			}
		}
	})
}

func TestToolsListShowsOnlyToolsTheTokenMayCall(t *testing.T) {
	eachEra(t, func(t *testing.T, connect opener, version string) {
		f := newFixture(t, nil)
		for _, tc := range []struct {
			p    Principal
			want []string
		}{
			{Principal{ID: "token:none", Name: "no scopes"}, []string{}},
			{principal(ScopeRead), allTools[:8]},
			{principal(ScopeManage), allTools[:10]},
			{principal(ScopeOwner), allTools},
			{Principal{ID: "token:mixed", Scopes: []Scope{"admin", ScopeRead, ScopeOwner}}, allTools},
		} {
			res := connect(t, f, tc.p).call(t, "tools/list", nil).wantResult(t)
			if got := toolNames(t, res); !slices.Equal(got, tc.want) {
				t.Errorf("%s sees %v, want %v", tc.p.ID, got, tc.want)
			}
		}
	})
}

func TestToolsListPagination(t *testing.T) {
	eachEra(t, func(t *testing.T, connect opener, version string) {
		f := newFixture(t, func(o *Options) { o.PageSize = 3 })
		c := connect(t, f, principal(ScopeOwner))
		var names []string
		var sizes []int
		var params map[string]any
		for range 10 {
			res := c.call(t, "tools/list", params).wantResult(t)
			page := toolNames(t, res)
			names, sizes = append(names, page...), append(sizes, len(page))
			cursor, ok := res["nextCursor"]
			if !ok {
				break
			}
			params = map[string]any{"cursor": cursor}
		}
		if !slices.Equal(names, allTools) || !slices.Equal(sizes, []int{3, 3, 3, 2}) {
			t.Fatalf("pages of %v: %v", sizes, names)
		}
		res := c.call(t, "tools/list", map[string]any{"cursor": nil}).wantResult(t)
		if got := toolNames(t, res); !slices.Equal(got, allTools[:3]) {
			t.Errorf("a null cursor gave %v", got)
		}

		readerCursor := dig(t, connect(t, f, principal(ScopeRead)).call(t, "tools/list", nil).wantResult(t), "nextCursor")
		fp := fingerprint(f.srv.visible(principal(ScopeOwner)))
		enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
		for _, tc := range []struct {
			name   string
			cursor any
		}{
			{"not a cursor", "%%%"},
			{"a number", 3},
			{"another token's list", readerCursor},
			{"the first page", makeCursor(0, fp)},
			{"past the end", makeCursor(len(allTools), fp)},
			{"no offset", enc("tools:x:" + fp)},
			{"another kind", enc("prompts:3:" + fp)},
		} {
			e := c.call(t, "tools/list", map[string]any{"cursor": tc.cursor}).wantError(t, codeInvalidParams)
			if e.Message != "The cursor is not valid for this tool list." {
				t.Errorf("%s: %q", tc.name, e.Message)
			}
		}
	})
}

func TestToolsCallResultsAreShapedForTheRevision(t *testing.T) {
	eachEra(t, func(t *testing.T, connect opener, version string) {
		f := newFixture(t, nil)
		c := connect(t, f, principal(ScopeOwner))
		structured, modern := atLeast(version, version20250618), isModern(version)
		call := func(name string, args any) map[string]any {
			t.Helper()
			params := map[string]any{"name": name}
			if args != nil {
				params["arguments"] = args
			}
			res := c.call(t, "tools/call", params).wantResult(t)
			if has(res, "isError") {
				t.Errorf("%s: isError is set: %v", name, res)
			}
			if has(res, "resultType") != modern || (modern && canon(t, res["_meta"]) != serverInfoMeta) {
				t.Errorf("%s: 2026-07-28 fields: %v", name, res)
			}
			return res
		}

		res := call("echo", map[string]any{"text": "hi", "times": 2})
		if got := canon(t, res["content"]); got != `[{"text":"hihi","type":"text"}]` {
			t.Errorf("content %s", got)
		}
		if has(res, "structuredContent") {
			t.Error("echo has no output schema but got structuredContent")
		}
		// JSON Schema counts 2.0 as an integer.
		if got := resultText(t, call("echo", map[string]any{"text": "ab", "times": json.RawMessage("2.0")})); got != "abab" {
			t.Errorf("times 2.0 gave %q", got)
		}

		res = call("server_status", map[string]any{"server": "survival"})
		if got := canon(t, res["content"]); got != `[{"text":"The server is online with 3 players.","type":"text"},{"text":"{\"online\":true,\"players\":3}","type":"text"}]` {
			t.Errorf("content %s", got)
		}
		if has(res, "structuredContent") != structured || (structured && canon(t, res["structuredContent"]) != `{"online":true,"players":3}`) {
			t.Errorf("structuredContent: %v", res)
		}

		// Structured content that is not an object needs 2026-07-28.
		res = call("numbers", nil)
		if got := canon(t, res["content"]); got != `[{"text":"[1,2,3]","type":"text"}]` {
			t.Errorf("content %s", got)
		}
		if has(res, "structuredContent") != modern || (modern && canon(t, res["structuredContent"]) != "[1,2,3]") {
			t.Errorf("structuredContent: %v", res)
		}

		for _, args := range []any{nil, json.RawMessage("null"), map[string]any{}} {
			if got := resultText(t, call("create_backup", args)); got != "Backup started." {
				t.Errorf("create_backup with arguments %v: %q", args, got)
			}
		}

		wantTools := []string{"echo", "echo", "server_status", "numbers", "create_backup", "create_backup", "create_backup"}
		recs := f.calls()
		if len(recs) != len(wantTools) {
			t.Fatalf("%d call records, want %d", len(recs), len(wantTools))
		}
		for i, r := range recs {
			if r.Tool != wantTools[i] || r.Outcome != OutcomeOK || r.Principal.ID != "token:owner" || r.Protocol != version ||
				r.Client != (ClientInfo{Name: "test-client", Version: "0.1"}) {
				t.Errorf("record %d: %+v", i, r)
			}
		}
		if recs[0].Effect != ReadOnly || recs[4].Effect != Additive {
			t.Errorf("effects %v and %v", recs[0].Effect, recs[4].Effect)
		}
	})
}

func TestToolFailuresAreResultsWithIsError(t *testing.T) {
	const retry = "Check the tool's input schema and call it again."
	eachEra(t, func(t *testing.T, connect opener, version string) {
		f := newFixture(t, nil)
		owner := connect(t, f, principal(ScopeOwner))
		reader := connect(t, f, principal(ScopeRead))
		for _, tc := range []struct {
			name   string
			c      client
			tool   string
			args   map[string]any
			text   string
			detail string
		}{
			{
				"a missing argument", owner, "echo", map[string]any{},
				`The arguments for echo are not valid: "text" is required. ` + retry,
				`{"hint":"` + retry + `","kind":"invalid_arguments"}`,
			},
			{
				"several problems", owner, "echo", map[string]any{"text": 5, "times": 9, "loud": true},
				"The arguments for echo are not valid:\n" +
					"- \"loud\" is not a known parameter. Known parameters: text, times.\n" +
					"- \"text\" must be a string, not an integer.\n" +
					"- \"times\" must be at most 3.\n" + retry,
				`{"hint":"` + retry + `","kind":"invalid_arguments"}`,
			},
			{
				"a pattern", owner, "server_status", map[string]any{"server": "../etc"},
				`The arguments for server_status are not valid: "server" does not match the pattern ^[a-z0-9-]{1,32}$. ` + retry,
				`{"hint":"` + retry + `","kind":"invalid_arguments"}`,
			},
			{
				"a missing scope", reader, "restart_server", map[string]any{"server": "survival"},
				"This token has the read scope, but restart_server needs the manage scope. Create a token with the manage scope in the Playkeeper panel and update the client's configuration.",
				`{"hint":"Create a token with the manage scope in the Playkeeper panel and update the client's configuration.","kind":"scope_missing","params":{"required_scope":"manage","tool":"restart_server"}}`,
			},
			{
				"a tool error", owner, "offline", nil,
				"The server is offline. Start it first.",
				`{"hint":"Start it first.","kind":"server_offline","params":{"server":"survival"}}`,
			},
			{
				"an internal error", owner, "leaky", nil,
				"leaky failed unexpectedly. The details are in the Playkeeper logs.",
				`{"hint":"The details are in the Playkeeper logs.","kind":"internal"}`,
			},
		} {
			params := map[string]any{"name": tc.tool}
			if tc.args != nil {
				params["arguments"] = tc.args
			}
			res := tc.c.call(t, "tools/call", params).wantResult(t)
			if res["isError"] != true || has(res, "structuredContent") {
				t.Errorf("%s: %v", tc.name, res)
			}
			if got := resultText(t, res); got != tc.text {
				t.Errorf("%s: text\n%s\nwant\n%s", tc.name, got, tc.text)
			}
			if got := canon(t, dig(t, res, "_meta", metaToolError)); got != tc.detail {
				t.Errorf("%s: detail %s, want %s", tc.name, got, tc.detail)
			}
		}
		want := []Outcome{OutcomeInvalid, OutcomeInvalid, OutcomeInvalid, OutcomeDenied, OutcomeToolError, OutcomeFailed}
		if got := f.outcomes(); !slices.Equal(got, want) {
			t.Errorf("outcomes %v, want %v", got, want)
		}
		if kind := f.calls()[4].Kind; kind != "server_offline" {
			t.Errorf("the tool error was recorded with kind %q", kind)
		}
		// The handler's own error is for the operator's log only.
		if !strings.Contains(f.logs.String(), "secret-detail") {
			t.Error("the handler's error was not logged")
		}
	})
}

func TestMalformedCallsAndBrokenToolsAreProtocolErrors(t *testing.T) {
	eachEra(t, func(t *testing.T, connect opener, version string) {
		f := newFixture(t, nil)
		c := connect(t, f, principal(ScopeOwner))
		for _, tc := range []struct {
			name   string
			params map[string]any
			code   int
			msg    string
		}{
			{"an unknown tool", map[string]any{"name": "nope"}, codeInvalidParams, "Unknown tool: nope"},
			{"no name", map[string]any{"arguments": map[string]any{}}, codeInvalidParams, "tools/call needs the name of a tool."},
			{"a name that is not a string", map[string]any{"name": 5}, codeInvalidParams, "tools/call needs the name of a tool."},
			{"arguments in an array", map[string]any{"name": "echo", "arguments": []any{"hi"}}, codeInvalidParams, "The tool arguments must be a JSON object."},
			{"arguments in a string", map[string]any{"name": "echo", "arguments": "text=hi"}, codeInvalidParams, "The tool arguments must be a JSON object."},
			{"a panic", map[string]any{"name": "explode"}, codeInternalError, "The server failed to run the tool."},
			{"a result that breaks the output schema", map[string]any{"name": "wrong_output"}, codeInternalError, "The server failed to run the tool."},
		} {
			if e := c.call(t, "tools/call", tc.params).wantError(t, tc.code); e.Message != tc.msg {
				t.Errorf("%s: %q", tc.name, e.Message)
			}
		}
		logs := f.logs.String()
		for _, want := range []string{"tool panicked", "boom", "does not match its output schema"} {
			if !strings.Contains(logs, want) {
				t.Errorf("the logs lack %q", want)
			}
		}
		if got := f.outcomes(); !slices.Equal(got, []Outcome{OutcomeFailed, OutcomeFailed}) {
			t.Errorf("outcomes %v", got)
		}
	})
}

func TestUnknownMethodsAreNotFound(t *testing.T) {
	eachEra(t, func(t *testing.T, connect opener, version string) {
		c := connect(t, newFixture(t, nil), principal(ScopeOwner))
		methods := []string{"resources/list", "prompts/list", "completion/complete", "logging/setLevel", "subscriptions/listen", "tools/delete"}
		if isModern(version) {
			methods = append(methods, "initialize", "ping")
		} else {
			methods = append(methods, "server/discover")
		}
		for _, m := range methods {
			e := c.call(t, m, nil).wantError(t, codeMethodNotFound)
			if want := `Method "` + m + `" is not supported.`; e.Message != want {
				t.Errorf("%q, want %q", e.Message, want)
			}
		}
	})
}

func TestParamsMustBeAnObject(t *testing.T) {
	eachEra(t, func(t *testing.T, connect opener, version string) {
		c := connect(t, newFixture(t, nil), principal(ScopeOwner))
		r := c.send(t, `{"jsonrpc":"2.0","id":41,"method":"tools/list","params":[1]}`, 1)[0]
		if e := r.wantError(t, codeInvalidParams); e.Message != "The params must be a JSON object." || r.id() != "41" {
			t.Errorf("id %s: %q", r.id(), e.Message)
		}
	})
}

func TestInvalidMessagesGetJSONRPCErrors(t *testing.T) {
	cases := []struct {
		name, raw string
		code      int
		id        string
	}{
		{"not JSON", `{"jsonrpc":"2.0",`, codeParseError, ""},
		{"a string", `"ping"`, codeInvalidRequest, ""},
		{"a number", `42`, codeInvalidRequest, ""},
		{"null", `null`, codeInvalidRequest, ""},
		{"JSON-RPC 1.0", `{"jsonrpc":"1.0","id":1,"method":"ping"}`, codeInvalidRequest, "1"},
		{"no jsonrpc", `{"id":1,"method":"ping"}`, codeInvalidRequest, "1"},
		{"an object id", `{"jsonrpc":"2.0","id":{"n":1},"method":"ping"}`, codeInvalidRequest, ""},
		{"a fractional id", `{"jsonrpc":"2.0","id":1.5,"method":"ping"}`, codeInvalidRequest, ""},
		{"an exponent id", `{"jsonrpc":"2.0","id":1e2,"method":"ping"}`, codeInvalidRequest, ""},
		{"a null id", `{"jsonrpc":"2.0","id":null,"method":"ping"}`, codeInvalidRequest, ""},
		{"a boolean id", `{"jsonrpc":"2.0","id":true,"method":"ping"}`, codeInvalidRequest, ""},
		{"a long id", `{"jsonrpc":"2.0","id":"` + strings.Repeat("x", 300) + `","method":"ping"}`, codeInvalidRequest, ""},
		{"a numeric method", `{"jsonrpc":"2.0","id":1,"method":5}`, codeInvalidRequest, "1"},
		{"an empty method", `{"jsonrpc":"2.0","id":1,"method":""}`, codeInvalidRequest, "1"},
		{"no method", `{"jsonrpc":"2.0","id":1}`, codeInvalidRequest, "1"},
		{"result and error", `{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":1,"message":"x"}}`, codeInvalidRequest, "1"},
		{"string params", `{"jsonrpc":"2.0","id":1,"method":"ping","params":"x"}`, codeInvalidParams, "1"},
	}
	valid := []struct{ raw, id string }{
		{`{"jsonrpc":"2.0","id":"a\u0062","method":"ping"}`, `"ab"`},
		{`{"jsonrpc":"2.0","id":-7,"method":"ping"}`, `-7`},
		{`{"jsonrpc":"2.0","id":12345678901234567890,"method":"ping"}`, `12345678901234567890`},
		{` {"jsonrpc":"2.0","id":"","method":"ping","params":null} `, `""`},
	}
	eachTransport(t, func(t *testing.T, open opener) {
		c := open(t, newFixture(t, nil), principal(ScopeOwner))
		for _, tc := range cases {
			r := c.send(t, tc.raw, 1)[0]
			if r.err == nil || r.err.Code != tc.code || r.id() != tc.id {
				t.Errorf("%s: want error %d with id %q, got %+v id %q", tc.name, tc.code, tc.id, r.err, r.id())
			}
		}
		for _, tc := range valid {
			r := c.send(t, tc.raw, 1)[0]
			if r.wantResult(t); r.id() != tc.id {
				t.Errorf("%s: id %s, want %s", tc.raw, r.id(), tc.id)
			}
		}
	})
}

func TestNotificationsGetNoResponse(t *testing.T) {
	eachEra(t, func(t *testing.T, connect opener, version string) {
		c := connect(t, newFixture(t, nil), principal(ScopeOwner))
		c.notify(t, "notifications/initialized", nil)
		c.notify(t, "notifications/whatever", map[string]any{"x": 1})
		c.notify(t, "notifications/cancelled", map[string]any{"requestId": 99, "reason": "Nothing to cancel."})
		c.quiet(t)
	})
}

func TestBatchesOnlyWhereTheRevisionAllowsThem(t *testing.T) {
	ping := func(id int) string { return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"ping"}`, id) }
	eachEra(t, func(t *testing.T, connect opener, version string) {
		f := newFixture(t, nil)
		c := connect(t, f, principal(ScopeOwner))
		batch := `[` + ping(1) + `,{"jsonrpc":"2.0","method":"notifications/whatever"},` +
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}},` +
			`{"jsonrpc":"2.0","id":3,"method":"nope"}]`
		if !allowsBatch(version) {
			r := c.send(t, batch, 1)[0]
			want := "Protocol " + version + " does not allow JSON-RPC batches."
			if isModern(version) {
				want = "JSON-RPC batches are only accepted after initialize has negotiated protocol 2025-03-26 or 2024-11-05."
			}
			if e := r.wantError(t, codeInvalidRequest); e.Message != want || r.id() != "" {
				t.Errorf("id %q: %q", r.id(), e.Message)
			}
			if len(f.calls()) != 0 {
				t.Error("a refused batch ran a tool")
			}
			return
		}
		rs := c.send(t, batch, 3)
		if canon(t, rs[0].wantResult(t)) != "{}" || rs[0].id() != "1" {
			t.Errorf("first response: %+v", rs[0])
		}
		if resultText(t, rs[1].wantResult(t)) != "hi" || rs[1].id() != "2" {
			t.Errorf("second response: %+v", rs[1])
		}
		if rs[2].wantError(t, codeMethodNotFound); rs[2].id() != "3" {
			t.Errorf("third response has id %s", rs[2].id())
		}

		c.send(t, `[{"jsonrpc":"2.0","method":"notifications/whatever"}]`, 0)
		c.quiet(t)

		rs = c.send(t, `[5,`+
			`{"jsonrpc":"2.0","id":4,"method":"initialize","params":{"protocolVersion":"2025-03-26"}},`+
			`{"jsonrpc":"2.0","id":5,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}},`+
			`{"jsonrpc":"2.0","id":6,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2099-01-01"}}}]`, 4)
		for i, want := range []struct {
			id   string
			code int
			msg  string
		}{
			{"", codeInvalidRequest, "A JSON-RPC message must be a JSON object."},
			{"4", codeInvalidRequest, "initialize cannot be part of a batch."},
			{"5", codeInvalidRequest, "Requests in protocol 2026-07-28 cannot be batched."},
			{"6", codeUnsupportedVersion, `Protocol version "2099-01-01" is not supported.`},
		} {
			if e := rs[i].wantError(t, want.code); e.Message != want.msg || rs[i].id() != want.id {
				t.Errorf("response %d: id %q: %q", i, rs[i].id(), e.Message)
			}
		}

		for _, tc := range []struct{ name, raw, msg string }{
			{"empty", `[]`, "A JSON-RPC batch must hold at least one message."},
			{"too long", "[" + strings.Repeat(ping(9)+",", maxBatch) + ping(9) + "]", "A JSON-RPC batch may hold at most 64 messages."},
		} {
			if e := c.send(t, tc.raw, 1)[0].wantError(t, codeInvalidRequest); e.Message != tc.msg {
				t.Errorf("%s: %q", tc.name, e.Message)
			}
		}
		c.send(t, "["+strings.Repeat(ping(9)+",", maxBatch-1)+ping(9)+"]", maxBatch)
	})
}

func TestModernRequestsNeedTheirMetadata(t *testing.T) {
	const noCapabilities = "The request's params._meta has no io.modelcontextprotocol/clientCapabilities object."
	eachTransport(t, func(t *testing.T, open opener) {
		f := newFixture(t, nil)
		c := start(t, open(t, f, principal(ScopeOwner)), version20260728)
		for _, tc := range []struct {
			name, meta string
			code       int
			msg        string
		}{
			{"an unsupported version", `{"io.modelcontextprotocol/protocolVersion":"2099-01-01","io.modelcontextprotocol/clientCapabilities":{}}`,
				codeUnsupportedVersion, `Protocol version "2099-01-01" is not supported.`},
			{"a version that is not a string", `{"io.modelcontextprotocol/protocolVersion":20260728}`,
				codeInvalidParams, "io.modelcontextprotocol/protocolVersion in params._meta must be a string."},
			{"_meta that is not an object", `"2026-07-28"`, codeInvalidParams, "params._meta must be a JSON object."},
			{"no capabilities", `{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}`, codeInvalidParams, noCapabilities},
			{"capabilities in an array", `{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":[]}`,
				codeInvalidParams, noCapabilities},
			{"null capabilities", `{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":null}`,
				codeInvalidParams, noCapabilities},
		} {
			r := c.send(t, `{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{"_meta":`+tc.meta+`}}`, 1)[0]
			if e := r.wantError(t, tc.code); e.Message != tc.msg || r.id() != "7" {
				t.Errorf("%s: id %s: %q", tc.name, r.id(), e.Message)
			}
			if tc.code == codeUnsupportedVersion {
				if got := canon(t, r.err.Data); got != `{"requested":"2099-01-01","supported":["2026-07-28","2025-11-25","2025-06-18","2025-03-26","2024-11-05"]}` {
					t.Errorf("data %s", got)
				}
			}
		}
		for _, tc := range []struct {
			info string
			want ClientInfo
		}{
			{`"oops"`, ClientInfo{}},
			{`{"name":"bad\u0000\nname","version":"1"}`, ClientInfo{Name: "badname", Version: "1"}},
		} {
			raw := `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"echo","arguments":{"text":"x"},"_meta":{` +
				`"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},` +
				`"io.modelcontextprotocol/clientInfo":` + tc.info + `}}}`
			c.send(t, raw, 1)[0].wantResult(t)
			if recs := f.calls(); recs[len(recs)-1].Client != tc.want {
				t.Errorf("clientInfo %s was recorded as %+v", tc.info, recs[len(recs)-1].Client)
			}
		}
	})
}

func TestALegacyRevisionInMetaDoesNotChangeTheSession(t *testing.T) {
	eachTransport(t, func(t *testing.T, open opener) {
		c := start(t, open(t, newFixture(t, nil), principal(ScopeOwner)), version20250326)
		res := c.call(t, "tools/list", map[string]any{"_meta": map[string]any{metaProtocolVersion: version20251125}}).wantResult(t)
		if echo := dig(t, res, "tools", 0); has(echo, "title") {
			t.Errorf("the tool list is shaped for 2025-11-25: %v", echo)
		}
	})
}

func TestBothErasCanShareAConnection(t *testing.T) {
	eachTransport(t, func(t *testing.T, open opener) {
		c := start(t, open(t, newFixture(t, nil), principal(ScopeOwner)), version20250326)
		legacy := func() {
			t.Helper()
			res := c.call(t, "tools/list", nil).wantResult(t)
			if has(res, "resultType") || has(dig(t, res, "tools", 0), "title") {
				t.Errorf("not shaped for 2025-03-26: %v", res)
			}
		}
		legacy()
		b := c.base()
		b.modern, b.version = true, version20260728
		res := c.call(t, "tools/list", nil).wantResult(t)
		if res["resultType"] != "complete" || !has(dig(t, res, "tools", 0), "title") {
			t.Errorf("not shaped for 2026-07-28: %v", res)
		}
		b.modern, b.version = false, version20250326
		legacy()
	})
}

func TestToolCallsAreRateLimitedPerToken(t *testing.T) {
	eachTransport(t, func(t *testing.T, open opener) {
		f := newFixture(t, func(o *Options) {
			o.ReadOnlyCallsPerMinute = 2
			o.MutatingCallsPerMinute = 1
		})
		call := func(c client, name string) map[string]any {
			t.Helper()
			args := map[string]any{}
			if name == "echo" {
				args["text"] = "hi"
			}
			return c.call(t, "tools/call", map[string]any{"name": name, "arguments": args}).wantResult(t)
		}
		ok := func(res map[string]any) {
			t.Helper()
			if has(res, "isError") {
				t.Errorf("refused: %v", res)
			}
		}
		limited := func(res map[string]any, secs string) {
			t.Helper()
			if want := "Too many tool calls with this token. Try again in " + secs + " seconds."; res["isError"] != true || resultText(t, res) != want {
				t.Errorf("want %q, got %v", want, res)
			}
			if got, want := canon(t, dig(t, res, "_meta", metaToolError)), `{"kind":"rate_limited","params":{"retry_after_seconds":"`+secs+`"}}`; got != want {
				t.Errorf("detail %s, want %s", got, want)
			}
		}
		c := start(t, open(t, f, principal(ScopeOwner)), version20260728)
		ok(call(c, "echo"))
		ok(call(c, "echo"))
		limited(call(c, "echo"), "30")
		// Changes have a budget of their own.
		ok(call(c, "create_backup"))
		limited(call(c, "create_backup"), "60")
		// So does every other token.
		ok(call(start(t, open(t, f, principal(ScopeRead)), version20260728), "echo"))
		f.clock.add(30 * time.Second)
		ok(call(c, "echo"))
		limited(call(c, "echo"), "30")

		want := []Outcome{OutcomeOK, OutcomeOK, OutcomeLimited, OutcomeOK, OutcomeLimited, OutcomeOK, OutcomeOK, OutcomeLimited}
		if got := f.outcomes(); !slices.Equal(got, want) {
			t.Errorf("outcomes %v, want %v", got, want)
		}
	})
}

func TestSlowToolCallsTimeOut(t *testing.T) {
	eachTransport(t, func(t *testing.T, open opener) {
		f := newFixture(t, func(o *Options) { o.CallTimeout = 20 * time.Millisecond })
		c := start(t, open(t, f, principal(ScopeRead)), version20260728)
		res := c.call(t, "tools/call", map[string]any{"name": "slow"}).wantResult(t)
		want := "slow did not finish within 20ms. It may still complete in the background; check the current state before calling it again."
		if res["isError"] != true || resultText(t, res) != want {
			t.Errorf("want %q, got %v", want, res)
		}
		if err := f.gate.waitEnded(t); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("the handler ended with %v", err)
		}
		if got := f.outcomes(); !slices.Equal(got, []Outcome{OutcomeTimeout}) {
			t.Errorf("outcomes %v", got)
		}
	})
}

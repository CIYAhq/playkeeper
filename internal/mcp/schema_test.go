package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
)

// toolWithInput is a valid read-only tool named "t" with the given input
// schema.
func toolWithInput(in Schema) Tool {
	return Tool{
		Name: "t", Description: "A test tool.", InputSchema: in, Effect: ReadOnly, Scope: ScopeRead,
		Handler: func(context.Context, *Call) (*Result, error) { return &Result{}, nil },
	}
}

func object(props map[string]*Schema, required ...string) Schema {
	return Schema{Type: "object", Properties: props, Required: required}
}

func prepared(t *testing.T, s Schema) *Schema {
	t.Helper()
	nodes := 0
	p, err := s.prepare("inputSchema", 0, &nodes)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// validated checks args against s and returns the problems found and the
// normalised value.
func validated(t *testing.T, s *Schema, args string) (*problems, any) {
	t.Helper()
	v, err := decodeJSON([]byte(args))
	if err != nil {
		t.Fatal(err)
	}
	p := &problems{root: "The arguments"}
	return p, s.validate(v, "", p)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestSchemasAreCheckedWhenToolsRegister(t *testing.T) {
	prop := func(s *Schema) Schema { return object(map[string]*Schema{"a": s}) }
	nested := func(levels int) Schema {
		node := &Schema{Type: "string"}
		for range levels {
			node = &Schema{Type: "object", Properties: map[string]*Schema{"n": node}}
		}
		return object(map[string]*Schema{"n": node})
	}
	wide := func(n int) Schema {
		props := map[string]*Schema{}
		for i := range n {
			props[fmt.Sprintf("p%04d", i)] = &Schema{Type: "string"}
		}
		return object(props)
	}
	for _, tc := range []struct {
		name string
		in   Schema
		want string // empty for a valid schema
	}{
		{"a missing property schema", prop(nil), "inputSchema.a: the schema is missing"},
		{"an unknown type", prop(&Schema{Type: "strin"}), `inputSchema.a: unknown type "strin"`},
		{"no type", prop(&Schema{}), `inputSchema.a: unknown type ""`},
		{"properties on a string", prop(&Schema{Type: "string", Properties: map[string]*Schema{}}),
			"inputSchema.a: properties and required apply only to objects"},
		{"required on an array", prop(&Schema{Type: "array", Required: []string{"x"}}),
			"inputSchema.a: properties and required apply only to objects"},
		{"items on a string", prop(&Schema{Type: "string", Items: &Schema{Type: "string"}}),
			"inputSchema.a: items, minItems and maxItems apply only to arrays"},
		{"minItems on an object", prop(&Schema{Type: "object", MinItems: new(1)}),
			"inputSchema.a: items, minItems and maxItems apply only to arrays"},
		{"a pattern on an integer", prop(&Schema{Type: "integer", Pattern: "^1$"}),
			"inputSchema.a: minLength, maxLength and pattern apply only to strings"},
		{"a minimum on a string", prop(&Schema{Type: "string", Minimum: new(1.0)}),
			"inputSchema.a: minimum and maximum apply only to numbers"},
		{"a negative maxLength", prop(&Schema{Type: "string", MaxLength: new(-1)}), "inputSchema.a: maxLength must not be negative"},
		{"a negative minItems", prop(&Schema{Type: "array", MinItems: new(-1)}), "inputSchema.a: minItems must not be negative"},
		{"minItems above maxItems", prop(&Schema{Type: "array", MinItems: new(3), MaxItems: new(2)}),
			"inputSchema.a: minItems is greater than maxItems"},
		{"minLength above maxLength", prop(&Schema{Type: "string", MinLength: new(3), MaxLength: new(2)}),
			"inputSchema.a: minLength is greater than maxLength"},
		{"a minimum that is not a number", prop(&Schema{Type: "number", Minimum: new(math.NaN())}),
			"inputSchema.a: minimum and maximum must be finite"},
		{"an infinite maximum", prop(&Schema{Type: "integer", Maximum: new(math.Inf(1))}),
			"inputSchema.a: minimum and maximum must be finite"},
		{"minimum above maximum", prop(&Schema{Type: "integer", Minimum: new(2.0), Maximum: new(1.0)}),
			"inputSchema.a: minimum is greater than maximum"},
		{"an empty property name", object(map[string]*Schema{"": {Type: "string"}}), "inputSchema: a property has an empty name"},
		{"names that differ only in case", object(map[string]*Schema{"name": {Type: "string"}, "Name": {Type: "string"}}),
			`inputSchema: properties "Name" and "name" differ only in case`},
		{"an undefined required property", object(map[string]*Schema{"a": {Type: "string"}}, "b"),
			`inputSchema: required property "b" is not defined`},
		{"a property required twice", object(map[string]*Schema{"a": {Type: "string"}}, "a", "a"),
			`inputSchema: required property "a" is listed twice`},
		{"a pattern that does not compile", prop(&Schema{Type: "string", Pattern: "("}),
			"inputSchema.a: pattern does not compile: error parsing regexp: missing closing ): `(`"},
		{"an empty enum", prop(&Schema{Type: "string", Enum: []any{}}), "inputSchema.a: enum must list at least one value"},
		{"an enum on an array", prop(&Schema{Type: "array", Enum: []any{1}}),
			"inputSchema.a: enum applies only to strings, numbers and booleans"},
		{"an enum value that is not JSON", prop(&Schema{Type: "number", Enum: []any{math.NaN()}}),
			"inputSchema.a: enum value 0: json: unsupported value: NaN"},
		{"an enum value of another type", prop(&Schema{Type: "string", Enum: []any{"a", 1}}),
			"inputSchema.a: enum value 1 does not fit the schema: The value must be a string, not an integer."},
		{"an enum value out of bounds", prop(&Schema{Type: "integer", Maximum: new(3.0), Enum: []any{1, 5}}),
			"inputSchema.a: enum value 1 does not fit the schema: The value must be at most 3."},
		{"a default of another type", prop(&Schema{Type: "integer", Default: "one"}),
			"inputSchema.a: default does not fit the schema: The value must be an integer, not a string."},
		{"a default that is not JSON", prop(&Schema{Type: "string", Default: func() {}}),
			"inputSchema.a: default: json: unsupported type: func()"},
		{"a bad item schema", object(map[string]*Schema{"list": {Type: "array", Items: &Schema{Type: "strin"}}}),
			`inputSchema.list[]: unknown type "strin"`},
		{"32 levels", nested(31), ""},
		{"33 levels", nested(32), "inputSchema" + strings.Repeat(".n", 33) + ": schemas may nest at most 32 levels"},
		{"2000 nodes", wide(1999), ""},
		{"2001 nodes", wide(2000), "inputSchema.p1999: a schema may have at most 2000 nodes"},
	} {
		_, err := New(Options{Tools: []Tool{toolWithInput(tc.in)}})
		want := ""
		if tc.want != "" {
			want = "mcp: tool t: " + tc.want
		}
		if got := errText(err); got != want {
			t.Errorf("%s:\ngot  %s\nwant %s", tc.name, got, want)
		}
	}
}

func TestSchemaValidationMessages(t *testing.T) {
	s := prepared(t, object(map[string]*Schema{
		"name":   {Type: "string", MinLength: new(2), MaxLength: new(5)},
		"slug":   {Type: "string", Pattern: `^[a-z]+$`},
		"mode":   {Type: "string", Enum: []any{"fast", "safe"}},
		"count":  {Type: "integer", Minimum: new(1.0), Maximum: new(10.0)},
		"big":    {Type: "integer"},
		"ratio":  {Type: "number", Minimum: new(0.0), Maximum: new(1.0)},
		"level":  {Type: "integer", Enum: []any{1, 2, 3}},
		"flag":   {Type: "boolean"},
		"strict": {Type: "boolean", Enum: []any{true}},
		"tags":   {Type: "array", Items: &Schema{Type: "string", MaxLength: new(3)}, MinItems: new(1), MaxItems: new(2)},
		"owner":  {Type: "object", Properties: map[string]*Schema{"id": {Type: "integer"}}, Required: []string{"id"}},
		"empty":  {Type: "object"},
		"none":   {Type: "null"},
	}, "name"))
	const unknown = " is not a known parameter. Known parameters: big, count, empty, flag, level, mode, name, none, owner, ratio, slug, strict, tags."
	for _, tc := range []struct {
		args string
		want []string
	}{
		{`{"name":"ab"}`, nil},
		{`{}`, []string{`"name" is required.`}},
		{`[]`, []string{`The arguments must be an object, not an array.`}},
		{`{"name":"a"}`, []string{`"name" must be at least 2 characters long.`}},
		{`{"name":"abcdef"}`, []string{`"name" must be at most 5 characters long.`}},
		{`{"name":"ééééé"}`, nil},
		{`{"name":"😀😀"}`, nil},
		{`{"name":"éééééé"}`, []string{`"name" must be at most 5 characters long.`}},
		{`{"name":5}`, []string{`"name" must be a string, not an integer.`}},
		{`{"name":2.0}`, []string{`"name" must be a string, not a number.`}},
		{`{"name":null}`, []string{`"name" must be a string, not null.`}},
		{`{"name":"ab","slug":"Ab"}`, []string{`"slug" does not match the pattern ^[a-z]+$.`}},
		{`{"name":"ab","mode":"slow"}`, []string{`"mode" must be one of "fast" or "safe".`}},
		{`{"name":"ab","count":0}`, []string{`"count" must be at least 1.`}},
		{`{"name":"ab","count":11}`, []string{`"count" must be at most 10.`}},
		{`{"name":"ab","count":2.5}`, []string{`"count" must be an integer, not 2.5.`}},
		{`{"name":"ab","count":"3"}`, []string{`"count" must be an integer, not a string.`}},
		{`{"name":"ab","count":1e400}`, []string{`"count" is too large.`}},
		{`{"name":"ab","big":9007199254740991}`, nil},
		{`{"name":"ab","big":-9007199254740991}`, nil},
		{`{"name":"ab","big":9007199254740992}`, []string{`"big" must be between -9007199254740991 and 9007199254740991.`}},
		{`{"name":"ab","big":-1e400}`, []string{`"big" is too large.`}},
		{`{"name":"ab","ratio":0.5}`, nil},
		{`{"name":"ab","ratio":1}`, nil},
		{`{"name":"ab","ratio":1.5}`, []string{`"ratio" must be at most 1.`}},
		{`{"name":"ab","ratio":-0.25}`, []string{`"ratio" must be at least 0.`}},
		{`{"name":"ab","ratio":true}`, []string{`"ratio" must be a number, not a boolean.`}},
		{`{"name":"ab","level":2.0}`, nil},
		{`{"name":"ab","level":4}`, []string{`"level" must be one of 1, 2 or 3.`}},
		{`{"name":"ab","flag":"true"}`, []string{`"flag" must be true or false, not a string.`}},
		{`{"name":"ab","strict":false}`, []string{`"strict" must be true.`}},
		{`{"name":"ab","tags":[]}`, []string{`"tags" must have at least 1 item.`}},
		{`{"name":"ab","tags":["a","b","c"]}`, []string{`"tags" must have at most 2 items.`}},
		{`{"name":"ab","tags":["abcd",5]}`, []string{`"tags[0]" must be at most 3 characters long.`, `"tags[1]" must be a string, not an integer.`}},
		{`{"name":"ab","tags":"a"}`, []string{`"tags" must be an array, not a string.`}},
		{`{"name":"ab","owner":{}}`, []string{`"owner.id" is required.`}},
		{`{"name":"ab","owner":{"id":1,"x":2}}`, []string{`"owner.x" is not a known field. Known fields: id.`}},
		{`{"name":"ab","owner":[]}`, []string{`"owner" must be an object, not an array.`}},
		{`{"name":"ab","empty":{"x":1}}`, []string{`"empty.x" is not a known field; this object takes no fields.`}},
		{`{"name":"ab","none":0}`, []string{`"none" must be null, not an integer.`}},
		{`{"name":"ab","none":null}`, nil},
		// Unknown parameters come first, then the others in name order.
		{`{"zeta":1,"alpha":2,"count":0}`, []string{`"alpha"` + unknown, `"zeta"` + unknown, `"count" must be at least 1.`, `"name" is required.`}},
	} {
		if p, _ := validated(t, s, tc.args); !slices.Equal(p.list, tc.want) {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.args, p.list, tc.want)
		}
	}

	none := prepared(t, object(nil))
	if p, _ := validated(t, none, `{"x":1}`); !slices.Equal(p.list, []string{`"x" is not a known parameter; this tool takes no parameters.`}) {
		t.Errorf("got %q", p.list)
	}
}

func TestSchemaValidationCanonicalisesIntegers(t *testing.T) {
	s := prepared(t, object(map[string]*Schema{
		"n":    {Type: "integer"},
		"list": {Type: "array", Items: &Schema{Type: "integer"}},
		"x":    {Type: "number"},
	}))
	p, out := validated(t, s, `{"n":3.0,"list":[1e2,-0,2.50e1],"x":2.50}`)
	if len(p.list) != 0 {
		t.Fatalf("problems %q", p.list)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"list":[100,0,25],"n":3,"x":2.50}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestSchemaProblemsAreCapped(t *testing.T) {
	s := prepared(t, object(nil))
	fields := make([]string, maxProblems+5)
	for i := range fields {
		fields[i] = fmt.Sprintf(`"k%02d":0`, i)
	}
	p, _ := validated(t, s, "{"+strings.Join(fields, ",")+"}")
	lines := strings.Split(p.summary(), "\n")
	if len(lines) != maxProblems+1 {
		t.Fatalf("%d lines:\n%s", len(lines), p.summary())
	}
	if want := `- "k00" is not a known parameter; this tool takes no parameters.`; lines[0] != want {
		t.Errorf("first line %q, want %q", lines[0], want)
	}
	if want := "- …and 5 more."; lines[maxProblems] != want {
		t.Errorf("last line %q, want %q", lines[maxProblems], want)
	}
}

func TestSchemaJSON(t *testing.T) {
	s := Schema{
		Type: "object", Title: "T", Description: "D",
		Properties: map[string]*Schema{
			"b": {Type: "string", MinLength: new(0), MaxLength: new(3), Pattern: "^x+$", Default: "x"},
			"a": {Type: "array", Items: &Schema{Type: "integer", Minimum: new(0.0), Maximum: new(9.0), Enum: []any{1, 2}}, MinItems: new(1), MaxItems: new(2)},
			"c": {Type: "object"},
		},
		Required: []string{"b"},
	}
	b, err := json.Marshal(prepared(t, s))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"object","title":"T","description":"D","properties":{` +
		`"a":{"type":"array","items":{"type":"integer","enum":[1,2],"minimum":0,"maximum":9},"minItems":1,"maxItems":2},` +
		`"b":{"type":"string","minLength":0,"maxLength":3,"pattern":"^x+$","default":"x"},` +
		`"c":{"type":"object","additionalProperties":false}},` +
		`"required":["b"],"additionalProperties":false}`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
}

func TestRegisteredSchemasAreCopies(t *testing.T) {
	maxLen, minimum := 3, 1.0
	text := &Schema{Type: "string", MaxLength: &maxLen}
	mode := &Schema{Type: "string", Enum: []any{"a", "b"}}
	count := &Schema{Type: "integer", Minimum: &minimum}
	in := object(map[string]*Schema{"text": text, "mode": mode, "count": count}, "text")
	srv, err := New(Options{Tools: []Tool{toolWithInput(in)}})
	if err != nil {
		t.Fatal(err)
	}
	// Change everything the tool's author still holds.
	maxLen, minimum = 1, 5
	mode.Enum[0] = "z"
	text.Type = "integer"
	in.Properties["other"] = &Schema{Type: "string"}
	in.Required[0] = "other"

	p, _ := validated(t, &srv.byName["t"].InputSchema, `{"text":"abc","mode":"a","count":1}`)
	if len(p.list) != 0 {
		t.Errorf("the registered schema changed: %q", p.list)
	}
}

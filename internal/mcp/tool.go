package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Scope is a permission level granted to an API token. Scopes are
// hierarchical: owner includes manage, and manage includes read.
type Scope string

const (
	ScopeRead   Scope = "read"
	ScopeManage Scope = "manage"
	ScopeOwner  Scope = "owner"
)

func (s Scope) rank() int {
	switch s {
	case ScopeRead:
		return 1
	case ScopeManage:
		return 2
	case ScopeOwner:
		return 3
	}
	return 0
}

// Valid reports whether s is one of the defined scopes.
func (s Scope) Valid() bool { return s.rank() > 0 }

// Principal is the authenticated caller of a request.
type Principal struct {
	// ID identifies the caller in audit records and must not be empty, for
	// example "token:8f3a1c". Tool handlers pass it on as the actor.
	ID string
	// Name is for display, such as the token's label.
	Name   string
	Scopes []Scope
}

// Allows reports whether p holds scope s or a scope that includes it.
func (p Principal) Allows(s Scope) bool {
	need := s.rank()
	if need == 0 {
		return false
	}
	for _, have := range p.Scopes {
		if have.rank() >= need {
			return true
		}
	}
	return false
}

func (p Principal) highest() Scope {
	var best Scope
	for _, s := range p.Scopes {
		if s.rank() > best.rank() {
			best = s
		}
	}
	return best
}

// Effect is what a tool does to the system it manages.
type Effect int

const (
	// Destructive tools may change or remove existing state. It is the zero
	// value, so a tool that does not declare its effect gets the most caution.
	Destructive Effect = iota
	// Additive tools only add state, such as a new backup.
	Additive
	// ReadOnly tools change nothing.
	ReadOnly
)

func (e Effect) String() string {
	switch e {
	case Destructive:
		return "destructive"
	case Additive:
		return "additive"
	case ReadOnly:
		return "read-only"
	}
	return fmt.Sprintf("Effect(%d)", int(e))
}

// ClientInfo is a client's self-reported name and version. It is for display
// and logs only and must never drive a security decision.
type ClientInfo struct {
	Name    string
	Version string
}

// Tool is one tool the server offers.
type Tool struct {
	// Name is 1 to 128 characters from A-Z, a-z, 0-9, "_", "-" and ".".
	Name        string
	Title       string
	Description string
	// InputSchema describes the arguments; its Type must be "object".
	InputSchema Schema
	// OutputSchema, when set, must have Type "object". Every successful
	// Result must then carry Structured content that matches it.
	OutputSchema *Schema
	Effect       Effect
	// Idempotent means repeating a call with the same arguments has no
	// further effect.
	Idempotent bool
	// OpenWorld means the tool reaches systems outside Playkeeper, such as
	// an add-on catalogue on the internet.
	OpenWorld bool
	// Scope is the scope a caller needs. Tools that are not ReadOnly need
	// ScopeManage or ScopeOwner.
	Scope   Scope
	Handler Handler
}

// Handler runs a tool call whose arguments already match the input schema.
// Return a *ToolError for failures the model should see and can act on; any
// other error is logged and reported to the model as an internal failure.
// ctx is cancelled when the client cancels the call or the call times out.
type Handler func(ctx context.Context, call *Call) (*Result, error)

// Call is one validated tool call.
type Call struct {
	Tool      string
	Principal Principal
	// Arguments is the argument object, validated against the input schema,
	// with integers in canonical form. It is "{}" when the client sent none.
	Arguments json.RawMessage
	// Protocol is the protocol revision of the request.
	Protocol string
	Client   ClientInfo
}

// Bind decodes the arguments into v, refusing fields v does not declare.
func (c *Call) Bind(v any) error {
	dec := json.NewDecoder(bytes.NewReader(c.Arguments))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// Result is a successful tool result.
type Result struct {
	// Text is the result for the model to read.
	Text string
	// Structured is the machine-readable result. It is required, and must
	// match the schema, when the tool has an OutputSchema.
	Structured any
}

// Kinds of tool errors the server itself reports.
const (
	KindInvalidArguments = "invalid_arguments"
	KindScopeMissing     = "scope_missing"
	KindRateLimited      = "rate_limited"
	KindTimeout          = "timeout"
	KindInternal         = "internal"
)

// ToolError is a failure the model should see and can act on, such as a
// server that is offline. It becomes a result with isError set; Kind, Hint
// and Params are also carried in the result's _meta under
// "io.playkeeper/error" for clients that show them.
type ToolError struct {
	// Kind is a stable machine-readable code, such as "server_offline".
	Kind string
	// Msg is a full plain-English sentence.
	Msg string
	// Hint is an optional sentence suggesting what to do next.
	Hint   string
	Params map[string]string
}

func (e *ToolError) Error() string { return e.Msg }

func (e *ToolError) text() string {
	if e.Hint == "" {
		return e.Msg
	}
	return e.Msg + " " + e.Hint
}

// Outcome classifies a finished tool call for audit records.
type Outcome string

const (
	OutcomeOK        Outcome = "ok"
	OutcomeToolError Outcome = "tool_error"
	OutcomeInvalid   Outcome = "invalid_arguments"
	OutcomeDenied    Outcome = "denied"
	OutcomeLimited   Outcome = "rate_limited"
	OutcomeTimeout   Outcome = "timeout"
	OutcomeCancelled Outcome = "cancelled"
	// OutcomeFailed covers handler errors that are not ToolErrors, panics
	// and results that break the output schema.
	OutcomeFailed Outcome = "failed"
)

// CallRecord describes a finished tool call. It never includes the
// arguments, which may hold player names or other personal data.
type CallRecord struct {
	Principal Principal
	Tool      string
	Effect    Effect
	Outcome   Outcome
	// Kind is the ToolError kind for failed calls.
	Kind     string
	Duration time.Duration
	Protocol string
	Client   ClientInfo
}

// registered is a Tool whose schemas have been checked and copied.
type registered struct {
	Tool
	defs map[string]json.RawMessage // tool definition per protocol revision
}

func register(t Tool) (*registered, error) {
	if !validToolName(t.Name) {
		return nil, fmt.Errorf("mcp: tool name %q must be 1 to 128 characters from A-Z, a-z, 0-9, _, - and .", t.Name)
	}
	fail := func(format string, args ...any) error {
		return fmt.Errorf("mcp: tool %s: %s", t.Name, fmt.Sprintf(format, args...))
	}
	if t.Description == "" {
		return nil, fail("a description is required; models rely on it to pick tools")
	}
	if t.Handler == nil {
		return nil, fail("the handler is missing")
	}
	if !t.Scope.Valid() {
		return nil, fail("unknown scope %q", t.Scope)
	}
	switch t.Effect {
	case Destructive, Additive:
		if t.Scope == ScopeRead {
			return nil, fail("a %s tool must need the manage or owner scope", t.Effect)
		}
	case ReadOnly:
	default:
		return nil, fail("unknown effect %d", int(t.Effect))
	}
	if t.InputSchema.Type != "object" {
		return nil, fail("the input schema must have type object")
	}
	nodes := 0
	in, err := t.InputSchema.prepare("inputSchema", 0, &nodes)
	if err != nil {
		return nil, fail("%v", err)
	}
	t.InputSchema = *in
	if t.OutputSchema != nil {
		if t.OutputSchema.Type != "object" {
			return nil, fail("the output schema must have type object")
		}
		nodes = 0
		out, err := t.OutputSchema.prepare("outputSchema", 0, &nodes)
		if err != nil {
			return nil, fail("%v", err)
		}
		t.OutputSchema = out
	}
	r := &registered{Tool: t, defs: map[string]json.RawMessage{}}
	for _, v := range supportedVersions() {
		b, err := json.Marshal(r.definition(v))
		if err != nil {
			return nil, fail("%v", err)
		}
		r.defs[v] = b
	}
	return r, nil
}

func validToolName(name string) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-', c == '.':
		default:
			return false
		}
	}
	return true
}

// definition is the tool as listed to a client speaking revision v: tool
// annotations arrived in 2025-03-26, and titles and output schemas in
// 2025-06-18.
func (r *registered) definition(v string) map[string]any {
	d := map[string]any{
		"name":        r.Name,
		"description": r.Description,
		"inputSchema": r.InputSchema,
	}
	if atLeast(v, version20250326) {
		readOnly := r.Effect == ReadOnly
		a := map[string]any{
			"readOnlyHint":    readOnly,
			"destructiveHint": r.Effect == Destructive,
			"idempotentHint":  readOnly || r.Idempotent,
			"openWorldHint":   r.OpenWorld,
		}
		if r.Title != "" {
			a["title"] = r.Title
		}
		d["annotations"] = a
	}
	if atLeast(v, version20250618) {
		if r.Title != "" {
			d["title"] = r.Title
		}
		if r.OutputSchema != nil {
			d["outputSchema"] = r.OutputSchema
		}
	}
	return d
}

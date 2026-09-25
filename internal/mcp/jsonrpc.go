package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	codeParseError         = -32700
	codeInvalidRequest     = -32600
	codeMethodNotFound     = -32601
	codeInvalidParams      = -32602
	codeInternalError      = -32603
	codeHeaderMismatch     = -32020
	codeUnsupportedVersion = -32022
)

const (
	metaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaClientInfo         = "io.modelcontextprotocol/clientInfo"
	metaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaServerInfo         = "io.modelcontextprotocol/serverInfo"
	metaToolError          = "io.playkeeper/error"
)

const maxIDBytes = 256

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// newError builds a JSON-RPC error. msg is one sentence; hint, when set, is
// carried in data so that clients can show it separately.
func newError(code int, msg, hint string) *rpcError {
	e := &rpcError{Code: code, Message: msg}
	if hint != "" {
		e.Data = map[string]string{"hint": hint}
	}
	return e
}

func unsupportedVersion(requested string) *rpcError {
	return &rpcError{
		Code:    codeUnsupportedVersion,
		Message: fmt.Sprintf("Protocol version %q is not supported.", truncate(requested, 64)),
		Data:    map[string]any{"supported": supportedVersions(), "requested": requested},
	}
}

func methodNotFound(method string) *rpcError {
	return newError(codeMethodNotFound, fmt.Sprintf("Method %q is not supported.", truncate(method, 128)), "")
}

// errNoContext answers a request that is neither part of a legacy session
// nor carries modern per-request metadata. A modern client that forgot the
// metadata must get -32602, and the code suits a legacy client that skipped
// initialize just as well.
func errNoContext() *rpcError {
	return newError(codeInvalidParams,
		"The request has no protocol version: params._meta lacks io.modelcontextprotocol/protocolVersion and there is no initialized session.",
		"Send initialize first (protocol 2025-11-25 and earlier) or include io.modelcontextprotocol/protocolVersion and io.modelcontextprotocol/clientCapabilities in params._meta (protocol 2026-07-28).")
}

// response is one JSON-RPC response. ID is omitted when the request's id
// could not be read, as MCP requires.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func reply(id json.RawMessage, result any, e *rpcError) *response {
	if e != nil {
		return &response{JSONRPC: "2.0", ID: id, Error: e}
	}
	return &response{JSONRPC: "2.0", ID: id, Result: result}
}

type messageKind int

const (
	kindRequest messageKind = iota
	kindNotification
	kindResponse
)

// message is one decoded JSON-RPC message from the client.
type message struct {
	kind   messageKind
	id     json.RawMessage // canonical form; nil for notifications
	method string
	params map[string]json.RawMessage // nil when absent
}

// decodeMessage reads one JSON-RPC message from a JSON value. It returns
// either the message or the error response to send. It returns neither for a
// malformed notification, which must not be answered.
func decodeMessage(raw json.RawMessage) (*message, *response) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, reply(nil, nil, newError(codeInvalidRequest, "A JSON-RPC message must be a JSON object.", ""))
	}
	var id json.RawMessage
	if rawID, ok := obj["id"]; ok {
		canon, ok := canonicalID(rawID)
		if !ok {
			return nil, reply(nil, nil, newError(codeInvalidRequest, "The message id must be a string or an integer.", ""))
		}
		id = canon
	}
	var version string
	if err := json.Unmarshal(obj["jsonrpc"], &version); err != nil || version != "2.0" {
		return nil, reply(id, nil, newError(codeInvalidRequest, `The message must have "jsonrpc": "2.0".`, ""))
	}
	rawMethod, hasMethod := obj["method"]
	if !hasMethod {
		_, hasResult := obj["result"]
		_, hasError := obj["error"]
		if id != nil && hasResult != hasError {
			return &message{kind: kindResponse, id: id}, nil
		}
		return nil, reply(id, nil, newError(codeInvalidRequest, "The message is neither a request, a notification nor a response.", ""))
	}
	m := &message{kind: kindRequest, id: id}
	if id == nil {
		m.kind = kindNotification
	}
	if err := json.Unmarshal(rawMethod, &m.method); err != nil || m.method == "" {
		if m.kind == kindNotification {
			return nil, nil
		}
		return nil, reply(id, nil, newError(codeInvalidRequest, "The method must be a non-empty string.", ""))
	}
	if rawParams, ok := obj["params"]; ok && !isNull(rawParams) {
		if err := json.Unmarshal(rawParams, &m.params); err != nil || m.params == nil {
			if m.kind == kindNotification {
				return nil, nil
			}
			return nil, reply(id, nil, newError(codeInvalidParams, "The params must be a JSON object.", ""))
		}
	}
	return m, nil
}

// canonicalID validates a request id and re-encodes it, so that ids that
// differ only in string escaping compare equal.
func canonicalID(raw json.RawMessage) (json.RawMessage, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > maxIDBytes {
		return nil, false
	}
	switch c := raw[0]; {
	case c == '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, false
		}
		b, _ := json.Marshal(s)
		return b, true
	case c == '-' || (c >= '0' && c <= '9'):
		if bytes.ContainsAny(raw, ".eE") {
			return nil, false
		}
		var n json.Number
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, false
		}
		return json.RawMessage(n.String()), true
	}
	return nil, false
}

func isNull(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "null" }

// meta returns params._meta. A missing or null _meta yields nil.
func meta(params map[string]json.RawMessage) (map[string]json.RawMessage, *rpcError) {
	raw, ok := params["_meta"]
	if !ok || isNull(raw) {
		return nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, newError(codeInvalidParams, "params._meta must be a JSON object.", "")
	}
	return m, nil
}

// requestVersion returns the protocol version a request declares in
// params._meta, and whether it declares one at all.
func requestVersion(params map[string]json.RawMessage) (string, bool, *rpcError) {
	m, err := meta(params)
	if err != nil {
		return "", false, err
	}
	raw, ok := m[metaProtocolVersion]
	if !ok {
		return "", false, nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", true, newError(codeInvalidParams, metaProtocolVersion+" in params._meta must be a string.", "")
	}
	return v, true, nil
}

// modernMeta checks the per-request fields a 2026-07-28 request must carry
// besides its protocol version, and returns the self-reported client.
func modernMeta(params map[string]json.RawMessage) (ClientInfo, *rpcError) {
	m, err := meta(params)
	if err != nil {
		return ClientInfo{}, err
	}
	var caps map[string]json.RawMessage
	if raw, ok := m[metaClientCapabilities]; !ok || json.Unmarshal(raw, &caps) != nil || caps == nil {
		return ClientInfo{}, newError(codeInvalidParams,
			"The request's params._meta has no "+metaClientCapabilities+" object.",
			"Send the client's capabilities with every request; an empty object declares none.")
	}
	return parseClientInfo(m[metaClientInfo]), nil
}

// parseClientInfo reads a self-reported Implementation leniently: it is for
// display and logs only, so a malformed value is ignored rather than refused.
func parseClientInfo(raw json.RawMessage) ClientInfo {
	var ci struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &ci) != nil {
		return ClientInfo{}
	}
	return ClientInfo{Name: printable(ci.Name, 64), Version: printable(ci.Version, 64)}
}

// printable keeps a self-reported string safe for logs: printable ASCII only,
// truncated to max bytes.
func printable(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= max {
			break
		}
		if r >= 0x20 && r < 0x7f {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

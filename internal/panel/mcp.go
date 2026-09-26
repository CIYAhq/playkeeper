package panel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/mcp"
	"github.com/CIYAhq/playkeeper/internal/mcptools"
	"github.com/CIYAhq/playkeeper/internal/version"
)

// The dashboard serves Playkeeper's MCP tools at /mcp, outside the session
// guard: requests there carry an API token (see tokens.go) and nothing
// else counts, cookies included. Requests from web pages are refused, since
// no browser origin is allowed.

func (s *Server) startMCP() error {
	srv, err := mcp.New(mcp.Options{Name: "playkeeper", Version: version.Version, Instructions: mcptools.Instructions,
		Tools: mcptools.Tools(mcpBackend{s}), Logger: s.log, Now: s.now, OnCall: s.onMCPCall})
	if err != nil {
		return err
	}
	s.mcpHTTP, err = mcp.NewHTTPHandler(srv, mcp.HTTPOptions{Authenticator: mcp.AuthenticatorFunc(s.authenticateToken)})
	return err
}

// onMCPCall audits calls refused before any tool ran: a token whose role
// doesn't allow the tool, or one over its rate limit.
func (s *Server) onMCPCall(rec mcp.CallRecord) {
	switch rec.Outcome {
	case mcp.OutcomeDenied, mcp.OutcomeLimited:
		s.mcpRefused(rec.Principal.ID, rec.Tool, string(rec.Outcome))
	}
}

func (s *Server) mcpRefused(principal, tool, why string) {
	s.refused("mcp "+principal+" "+why+" "+tool, why,
		auditRow{at: s.now(), actor: principal, action: "mcp.call", target: tool, result: "refused", detail: why})
}

// mcpBackend gives the tools the dashboard's view: what a token may do,
// every machine's servers, and the machine that runs each.
type mcpBackend struct{ s *Server }

// Access is what a token may do now. A tool's action goes to permit with
// the token's account as it is now, just as the matching dashboard route's
// does for that account's session.
func (b mcpBackend) Access(_ context.Context, p mcp.Principal) (mcptools.Access, error) {
	id, ok := strings.CutPrefix(p.ID, tokenActorPrefix)
	if !ok {
		return mcptools.Access{}, mcptools.ErrRevoked
	}
	account, r, err := b.s.tokenRights(id)
	switch {
	case errors.Is(err, errTokenGone):
		return mcptools.Access{}, mcptools.ErrRevoked
	case err != nil:
		return mcptools.Access{}, err
	}
	return mcptools.Access{Scope: scopeOf(r.role), AllServers: r.all, Servers: r.servers,
		May: func(act string) bool { return permit(account, action(act), "") == nil }}, nil
}

func (b mcpBackend) Servers(ctx context.Context) ([]mcptools.Server, error) {
	list, machines, err := b.s.allServers(ctx)
	if errors.Is(err, errDB) {
		return nil, &mcp.ToolError{Kind: mcp.KindInternal, Msg: "The dashboard could not read its database.", Hint: "The details are in the Playkeeper logs."}
	}
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, m := range machines {
		name := m.Name
		if name == "" && m.Kind == localKind {
			name, _ = os.Hostname()
		}
		names[m.ID] = name
	}
	out := make([]mcptools.Server, 0, len(list))
	for _, sv := range list {
		raw, err := json.Marshal(sv)
		if err != nil {
			continue
		}
		var v mcptools.Server
		if json.Unmarshal(raw, &v) != nil || v.ID == "" {
			continue
		}
		v.MachineName = names[v.MachineID]
		out = append(out, v)
	}
	return out, nil
}

func (b mcpBackend) Agent(_ context.Context, serverID string) (mcptools.Agent, error) {
	m, err := b.s.machineForServer(serverID)
	switch {
	case errors.Is(err, errDisputed):
		return nil, &mcp.ToolError{Kind: codeServerDisputed, Msg: "Two machines say they run this server, so the dashboard sends its requests to neither.",
			Hint: "Remove the machine that shouldn't list it in Settings › Machines, in the Playkeeper dashboard."}
	case errors.Is(err, errServerMachine):
		return nil, &mcp.ToolError{Kind: api.CodeInternal, Msg: "The dashboard couldn't look up which machine runs this server, so it sent the request to none.", Hint: "Try again in a moment."}
	case err != nil:
		return nil, &mcp.ToolError{Kind: "server_not_found", Msg: "No machine runs this server any more.", Hint: "Call list_servers to see the servers."}
	}
	return m.agent, nil
}

// quietTools are left out of "What agents did lately": agents call them
// all the time, and they say nothing about a server.
var quietTools = map[string]bool{"list_servers": true, "get_operation": true}

func (b mcpBackend) Done(_ context.Context, p mcp.Principal, tool string, server *mcptools.Server) {
	id, ok := strings.CutPrefix(p.ID, tokenActorPrefix)
	if !ok || quietTools[tool] {
		return
	}
	var serverID, serverName string
	if server != nil {
		serverID, serverName = server.ID, server.Name
	}
	b.s.agentDid(id, tool, serverID, serverName)
}

func (b mcpBackend) Refused(_ context.Context, p mcp.Principal, tool, kind string) {
	b.s.mcpRefused(p.ID, tool, kind)
}

const (
	// agentActivityMerge is how soon after the last one a token's call of
	// the same tool on the same server counts on the same line.
	agentActivityMerge = 10 * time.Minute
	maxAgentActivity   = 500
)

// agentDid records a successful tool call for "What agents did lately".
func (s *Server) agentDid(tokenID, tool, serverID, serverName string) {
	now := s.now().UnixMilli()
	res, err := s.db.Exec(`UPDATE agent_activity SET count = count + 1, ts = ?, server_name = ? WHERE id = (
		SELECT id FROM agent_activity WHERE token_id = ? AND tool = ? AND server_id = ? AND ts >= ? ORDER BY id DESC LIMIT 1)`,
		now, serverName, tokenID, tool, serverID, now-agentActivityMerge.Milliseconds())
	if err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			return
		}
	}
	if _, err := s.db.Exec(`INSERT INTO agent_activity(token_id, ts, tool, server_id, server_name) VALUES(?,?,?,?,?)`,
		tokenID, now, tool, serverID, serverName); err != nil {
		s.log.Error("record what an agent did", "err", err)
		return
	}
	_, _ = s.db.Exec(`DELETE FROM agent_activity WHERE id <= (SELECT id FROM agent_activity ORDER BY id DESC LIMIT 1 OFFSET ?)`, maxAgentActivity)
}

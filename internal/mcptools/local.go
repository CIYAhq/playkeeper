package mcptools

import (
	"context"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/mcp"
)

// NewLocal is the backend for "playkeeper mcp": whoever runs it as root may
// do everything to the servers of this machine's agent. Joined machines are
// out of reach; their links end at the dashboard, not at this agent.
func NewLocal(agent Agent) Backend { return local{agent} }

type local struct{ agent Agent }

func (local) Access(context.Context, mcp.Principal) (Access, error) {
	return Access{Scope: mcp.ScopeOwner, AllServers: true}, nil
}

func (l local) Servers(ctx context.Context) ([]Server, error) {
	var list []api.ServerStatus
	if _, err := l.agent.Do(ctx, "GET", "/v1/servers", nil, nil, &list); err != nil {
		return nil, err
	}
	out := make([]Server, 0, len(list))
	for _, st := range list {
		out = append(out, Server{ServerStatus: st})
	}
	return out, nil
}

func (l local) Agent(context.Context, string) (Agent, error) { return l.agent, nil }

func (local) Done(context.Context, mcp.Principal, string, *Server) {}

func (local) Refused(context.Context, mcp.Principal, string, string) {}

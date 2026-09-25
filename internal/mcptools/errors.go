package mcptools

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/machinelink"
	"github.com/CIYAhq/playkeeper/internal/mcp"
)

// maxErrorText bounds what a machine's error message may put in front of
// the model; a joined machine is not trusted to keep it short.
const maxErrorText = 500

// agentError turns a failed agent request into a tool error the model can
// act on, with the agent's or the machine link's code as its kind.
// Cancellations and deadlines pass through, so the server reports them.
func agentError(err error) error {
	var te *mcp.ToolError
	var ae *agentclient.Error
	var le *machinelink.Error
	switch {
	case errors.As(err, &te):
		return te
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.As(err, &ae):
		kind := ae.Body.Code
		if kind == "" {
			kind = api.CodeInternal
		}
		return &mcp.ToolError{Kind: kind, Msg: clip(ae.Body.Error), Hint: clip(ae.Body.Hint), Params: clipParams(ae.Body.Params)}
	case errors.As(err, &le):
		return &mcp.ToolError{Kind: le.Code, Msg: clip(le.Msg), Hint: clip(le.Hint), Params: clipParams(le.Params)}
	case errors.Is(err, agentclient.ErrBadAnswer):
		return &mcp.ToolError{Kind: machinelink.CodeProtocol, Msg: "The machine's answer was not valid.",
			Hint: "Try again. If it keeps happening, update Playkeeper on both machines."}
	}
	return &mcp.ToolError{Kind: api.CodeAgentUnavailable,
		Msg:  "The Playkeeper agent is not running, so the server can't be seen or controlled right now.",
		Hint: "On the machine, check: sudo systemctl status playkeeper-agent"}
}

func clip(s string) string {
	s = strings.ToValidUTF8(s, "")
	if utf8.RuneCountInString(s) <= maxErrorText {
		return s
	}
	return string([]rune(s)[:maxErrorText]) + "…"
}

func clipParams(p map[string]string) map[string]string {
	if len(p) == 0 {
		return nil
	}
	out := make(map[string]string, min(len(p), 10))
	for k, v := range p {
		if len(out) == 10 {
			break
		}
		out[clip(k)] = clip(v)
	}
	return out
}

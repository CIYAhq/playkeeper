package mcptools

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/mcp"
)

type addonStepOut struct {
	Action    string `json:"action"`
	Source    string `json:"source"`
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Channel   string `json:"channel"`
	NeededBy  string `json:"neededBy,omitempty"`
	Was       string `json:"was,omitempty"`
}

// installAddon installs an add-on as the dashboard's detail sheet does: it
// asks the agent what installing it would do, and sends that plan's
// fingerprint with the install, so the agent installs exactly the plan, or
// nothing when the plan has changed since.
func installAddon(ctx context.Context, c *call) (*mcp.Result, error) {
	var args struct {
		Server  string `json:"server"`
		Source  string `json:"source"`
		Project string `json:"project"`
	}
	if err := c.Bind(&args); err != nil {
		return nil, err
	}
	if args.Project == "." || args.Project == ".." {
		return nil, &mcp.ToolError{Kind: mcp.KindInvalidArguments, Msg: "That is not a project id.",
			Hint: "Use the add-on's project id or slug, as in its page's address."}
	}
	var d api.AddonDetails
	if _, err := c.do(ctx, "GET", c.path("/addons/project/"+url.PathEscape(args.Source)+"/"+url.PathEscape(args.Project)), nil, nil, &d); err != nil {
		return nil, err
	}
	name, p := cmp.Or(clip(d.Card.Name), args.Project), d.Plan
	switch {
	case d.Installed != nil:
		text := fmt.Sprintf("%s is already installed on %s, version %s.", name, c.server.Name, clip(d.Installed.VersionNumber))
		if d.UpdateAvailable && d.Latest != nil {
			text += fmt.Sprintf(" Version %s is out; updates are made in the Playkeeper dashboard.", clip(d.Latest.VersionNumber))
		}
		return &mcp.Result{Text: text, Structured: map[string]any{"noop": true, "message": text}}, nil
	case d.Latest != nil && d.Latest.ExternalURL != "":
		page := clip(d.Latest.ExternalURL)
		return nil, &mcp.ToolError{Kind: "external_download", Msg: fmt.Sprintf("%s is only offered on its author's site, so Playkeeper can't install it.", name),
			Hint: "Its page: " + page, Params: map[string]string{"url": page}}
	case d.PlanError != nil:
		return nil, noticeError(*d.PlanError, name)
	case p == nil && d.Notice != nil:
		return nil, noticeError(*d.Notice, name)
	case p == nil:
		return nil, agentError(agentclient.ErrBadAnswer)
	case !p.Ready:
		if why := slices.Concat(p.Blockers, p.Manual); len(why) > 0 {
			return nil, noticeError(why[0], name)
		}
		return nil, agentError(agentclient.ErrBadAnswer)
	case d.Card.Source == "" || d.Card.ProjectID == "" || p.Fingerprint == "":
		return nil, agentError(agentclient.ErrBadAnswer)
	}

	var op api.Operation
	req := api.AddonInstallRequest{Source: d.Card.Source, ProjectID: d.Card.ProjectID, Fingerprint: p.Fingerprint, Actor: c.actor()}
	if _, err := c.do(ctx, "POST", c.path("/addons/install"), nil, req, &op); err != nil {
		return nil, err
	}
	if op.ID == "" {
		return nil, agentError(agentclient.ErrBadAnswer)
	}
	steps := make([]addonStepOut, 0, len(p.Steps))
	var also strings.Builder
	version := ""
	if d.Latest != nil {
		version = clip(d.Latest.VersionNumber)
	}
	for _, s := range p.Steps {
		o := addonStepOut{Action: s.Action, Source: s.Source, ProjectID: clip(s.ProjectID), Name: clip(s.Name), Version: clip(s.VersionNumber),
			Channel: s.Channel, NeededBy: clip(s.NeededBy), Was: clip(s.Was)}
		steps = append(steps, o)
		if s.Source == d.Card.Source && s.ProjectID == d.Card.ProjectID {
			version = o.Version
			continue
		}
		if s.Action == "update" {
			fmt.Fprintf(&also, "\nIt also updates %s from %s to %s", o.Name, o.Was, o.Version)
		} else {
			fmt.Fprintf(&also, "\nIt also installs %s %s", o.Name, o.Version)
		}
		if o.NeededBy != "" {
			fmt.Fprintf(&also, ", which %s needs", o.NeededBy)
		}
		also.WriteString(".")
	}
	warnings := make([]string, 0, len(p.Warnings))
	for _, w := range p.Warnings {
		warnings = append(warnings, sentence(clip(w.Message), clip(w.Hint)))
		also.WriteString("\nNote: " + warnings[len(warnings)-1])
	}
	text := fmt.Sprintf("Installing %s %s on %s.%s\nIt runs in the background as operation %s: call get_operation with server %q and that id "+
		"to follow it. A running server loads it when it restarts (restart_server).", name, version, c.server.Name, also.String(), op.ID, c.server.ID)
	return &mcp.Result{Text: text, Structured: map[string]any{"operation": operationOf(&op), "steps": steps, "warnings": warnings}}, nil
}

// noticeError is the agent's reason an add-on can't be installed, as a
// tool error.
func noticeError(n api.AddonNotice, name string) *mcp.ToolError {
	hint := clip(n.Hint)
	if n.URL != "" {
		hint = strings.TrimSpace(hint + " See " + clip(n.URL))
	}
	return &mcp.ToolError{Kind: cmp.Or(n.Kind, "addon_not_installable"), Msg: cmp.Or(clip(n.Message), "Playkeeper can't install "+name+"."),
		Hint: hint, Params: clipParams(n.Params)}
}

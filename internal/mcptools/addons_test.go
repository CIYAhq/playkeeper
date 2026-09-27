package mcptools

import (
	"net/http"
	"slices"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/machinelink"
	"github.com/CIYAhq/playkeeper/internal/mcp"
)

func paths(reqs []request) []string {
	out := []string{}
	for _, r := range reqs {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

// install_addon asks for the add-on's plan first, as the dashboard's detail
// sheet does, and installs with that plan's fingerprint and the project's
// id rather than the slug it was given, so the agent installs exactly the
// plan, or nothing when the plan has changed since.
func TestInstallAddonSendsThePlanItAskedFor(t *testing.T) {
	b := newWorld(false)
	a := b.agents["m1"]
	c := connect(t, b, token(mcp.ScopeOwner))
	p := "/v1/servers/" + idA

	d := chunky()
	d.Plan.Steps = append(d.Plan.Steps, api.AddonStep{Action: "install", Source: "modrinth", ProjectID: "P7dR8mSH", Name: "Fabric API",
		VersionNumber: "0.92.0", Channel: "release", NeededBy: "Chunky"})
	d.Plan.Warnings = []api.AddonNotice{{Kind: "prerelease", Message: "Chunky 1.4.40 is a beta.", Hint: "It may have bugs."}}
	a.on("GET", p+"/addons/project/modrinth/chunky", http.StatusOK, d)

	r := c.call("install_addon", map[string]any{"server": "survival", "source": "modrinth", "project": "chunky"})
	ok(t, "install_addon", r)
	want := "Installing Chunky 1.4.40 on Survival.\nIt also installs Fabric API 0.92.0, which Chunky needs.\nNote: Chunky 1.4.40 is a beta. It may have bugs.\n" +
		"It runs in the background as operation " + opA + `: call get_operation with server "aaaaaaaaaa" and that id to follow it. ` +
		"A running server loads it when it restarts (restart_server)."
	if r.Text != want {
		t.Errorf("install_addon said:\n%s\nwant:\n%s", r.Text, want)
	}
	reqs := a.requests()
	if got := paths(reqs); !slices.Equal(got, []string{"GET " + p + "/addons/project/modrinth/chunky", "POST " + p + "/addons/install"}) {
		t.Fatalf("requests %v", got)
	}
	if body := reqs[1].Body; body["source"] != "modrinth" || body["projectId"] != "fALzjamp" || body["fingerprint"] != planFP || body["actor"] != actor {
		t.Errorf("the install was sent as %v", body)
	}
	op, _ := r.Structured["operation"].(map[string]any)
	if steps, _ := r.Structured["steps"].([]any); op["id"] != opA || len(steps) != 2 {
		t.Errorf("structured result %v", r.Structured)
	}
}

// install_addon installs nothing a plan doesn't allow, and passes on why.
func TestInstallAddonInstallsNothingThePlanRefuses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(d *api.AddonDetails)
		kind   string
		text   string
	}{
		{"a conflict", func(d *api.AddonDetails) {
			d.Plan.Ready = false
			d.Plan.Blockers = []api.AddonNotice{{Kind: "conflict", Message: "Chunky can't be installed next to ChunkyPregen.", Hint: "Remove ChunkyPregen first.",
				Params: map[string]string{"other": "ChunkyPregen", "file": "ChunkyPregen.jar"}}}
		}, "conflict", "Chunky can't be installed next to ChunkyPregen. Remove ChunkyPregen first."},
		{"a dependency only on another site", func(d *api.AddonDetails) {
			d.Plan.Ready = false
			d.Plan.Manual = []api.AddonNotice{{Kind: "dependency_external", Message: "Chunky needs WorldEdit, which isn't on Modrinth.",
				Hint: "Download it from its site.", URL: "https://example.org/worldedit"}}
		}, "dependency_external", "Chunky needs WorldEdit, which isn't on Modrinth. Download it from its site. See https://example.org/worldedit"},
		{"a download only on the author's site", func(d *api.AddonDetails) {
			d.Plan = nil
			d.Latest.ExternalURL = "https://example.org/chunky"
		}, "external_download", "Chunky is only offered on its author's site, so Playkeeper can't install it. Its page: https://example.org/chunky"},
		{"no version for the server", func(d *api.AddonDetails) {
			d.Plan, d.Latest = nil, nil
			d.Notice = &api.AddonNotice{Kind: "no_compatible_version", Message: "Chunky has no version for Paper 1.21.8."}
		}, "no_compatible_version", "Chunky has no version for Paper 1.21.8."},
		{"a plan that can't be made", func(d *api.AddonDetails) {
			d.Plan = nil
			d.PlanError = &api.AddonNotice{Kind: "dependency_unavailable", Message: "Chunky needs Fabric API, which has no version for 1.21.8."}
		}, "dependency_unavailable", "Chunky needs Fabric API, which has no version for 1.21.8."},
		{"a ready plan without its fingerprint", func(d *api.AddonDetails) {
			d.Plan.Fingerprint = ""
		}, machinelink.CodeProtocol, "The machine's answer was not valid. Try again. If it keeps happening, update Playkeeper on both machines."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newWorld(false)
			a := b.agents["m1"]
			d := chunky()
			tc.change(&d)
			a.on("GET", "/v1/servers/"+idA+"/addons/project/modrinth/chunky", http.StatusOK, d)
			c := connect(t, b, token(mcp.ScopeOwner))
			r := c.call("install_addon", map[string]any{"server": idA, "source": "modrinth", "project": "chunky"})
			if r.Kind != tc.kind || r.Text != tc.text {
				t.Errorf("%s: %s\nwant %s: %s", r.Kind, r.Text, tc.kind, tc.text)
			}
			if got := paths(a.requests()); !slices.Equal(got, []string{"GET /v1/servers/" + idA + "/addons/project/modrinth/chunky"}) {
				t.Errorf("requests %v", got)
			}
		})
	}
}

// An installed add-on is left as it is; arguments the agent would refuse
// and a token without the owner scope get no further than the tool.
func TestInstallAddonLeavesInstalledAddonsAlone(t *testing.T) {
	b := newWorld(false)
	a := b.agents["m1"]
	d := chunky()
	d.Plan, d.UpdateAvailable = nil, true
	d.Installed = &api.Addon{Source: "modrinth", ProjectID: "fALzjamp", Slug: "chunky", Name: "Chunky", VersionID: "aBcDeFgH", VersionNumber: "1.4.39", Channel: "release"}
	a.on("GET", "/v1/servers/"+idA+"/addons/project/modrinth/chunky", http.StatusOK, d)
	c := connect(t, b, token(mcp.ScopeOwner))
	args := map[string]any{"server": idA, "source": "modrinth", "project": "chunky"}

	r := c.call("install_addon", args)
	ok(t, "install_addon", r)
	if r.Text != "Chunky is already installed on Survival, version 1.4.39. Version 1.4.40 is out; updates are made in the Playkeeper dashboard." || r.Structured["noop"] != true {
		t.Errorf("an installed add-on: %s %v", r.Text, r.Structured)
	}
	for _, bad := range []map[string]any{
		{"server": idA, "source": "modrinth", "project": ".."},
		{"server": idA, "source": "modrinth", "project": "chunky/../../backups"},
		{"server": idA, "source": "curseforge", "project": "chunky"},
	} {
		if r := c.call("install_addon", bad); r.Kind != mcp.KindInvalidArguments {
			t.Errorf("%v: %s: %s", bad, r.Kind, r.Text)
		}
	}
	b.setAccess(Access{Scope: mcp.ScopeManage, AllServers: true}, nil)
	if r := c.call("install_addon", args); r.Kind != mcp.KindScopeMissing {
		t.Errorf("with manage rights: %s: %s", r.Kind, r.Text)
	}
	if got := paths(a.requests()); !slices.Equal(got, []string{"GET /v1/servers/" + idA + "/addons/project/modrinth/chunky"}) {
		t.Errorf("requests %v", got)
	}
}

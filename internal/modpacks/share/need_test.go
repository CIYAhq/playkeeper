package share

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/addons/modrinth"
	"github.com/CIYAhq/playkeeper/internal/modpacks/mrpack"
)

func TestLabels(t *testing.T) {
	for _, c := range []struct {
		need      Need
		key, text string
	}{
		{Required, "share.need.required", "Friends need it"},
		{Optional, "share.need.optional", "Optional for friends"},
		{ServerOnly, "share.need.server_only", "Server only"},
		{Unknown, "share.need.unknown", "Unknown"},
		{"", "share.need.unknown", "Unknown"},
		{"sometimes", "share.need.unknown", "Unknown"},
	} {
		l := c.need.Label()
		if l.Key != c.key || l.Text != c.text || l.Params != nil {
			t.Errorf("%q: %+v", c.need, l)
		}
	}
	b, _ := json.Marshal(Required.Label())
	if string(b) != `{"key":"share.need.required","text":"Friends need it"}` {
		t.Errorf("label as JSON: %s", b)
	}
}

// Modrinth's environment values, from its API documentation, and what each
// means for friends when the server already runs the mod.
func TestModrinthEnvironments(t *testing.T) {
	for env, want := range map[string]Need{
		"client_and_server":             Required,
		"client_only":                   Required,
		"client_only_server_optional":   Required,
		"server_only_client_optional":   Optional,
		"client_or_server_prefers_both": Optional,
		"singleplayer_only":             Optional,
		"server_only":                   ServerOnly,
		"dedicated_server_only":         ServerOnly,
		"client_or_server":              ServerOnly,
		"unknown":                       Unknown,
		"":                              Unknown,
	} {
		if got := ModrinthNeed(&modrinth.Version{Environment: env}, nil); got != want {
			t.Errorf("version environment %q: %s, want %s", env, got, want)
		}
		if got := ModrinthNeed(nil, &modrinth.Project{Environment: []string{env}}); got != want {
			t.Errorf("project environment %q: %s, want %s", env, got, want)
		}
	}
}

func TestModrinthNeedFallsBack(t *testing.T) {
	project := func(clientSide string, envs ...string) *modrinth.Project {
		return &modrinth.Project{ClientSide: clientSide, ServerSide: "required", Environment: envs}
	}
	for _, c := range []struct {
		name    string
		version *modrinth.Version
		project *modrinth.Project
		want    Need
	}{
		{"nothing known", nil, nil, Unknown},
		{"the version wins over the project", &modrinth.Version{Environment: "client_or_server"}, project("required", "client_and_server"), ServerOnly},
		{"a version without environment asks the project", &modrinth.Version{}, project("", "client_only"), Required},
		{"an unknown version environment asks the project", &modrinth.Version{Environment: "unknown"}, project("optional"), Optional},
		{"an unknown version environment without project", &modrinth.Version{Environment: "unknown"}, nil, Unknown},
		{"project environments that agree", nil, project("optional", "client_only", "client_and_server"), Required},
		{"unknown project environments are skipped", nil, project("", "unknown", "server_only"), ServerOnly},
		// Async Logger lists client_only and client_or_server_prefers_both.
		{"project environments that disagree ask client_side", nil, project("required", "client_only", "client_or_server_prefers_both"), Required},
		{"disagreeing environments and optional client_side", nil, project("optional", "server_only", "client_or_server_prefers_both"), Optional},
		{"disagreeing environments and no client_side", nil, project("", "server_only", "client_only"), Unknown},
		{"client_side required", nil, project("required"), Required},
		{"client_side optional", nil, project("optional"), Optional},
		{"client_side unsupported", nil, project("unsupported"), ServerOnly},
		{"client_side unknown", nil, project("unknown"), Unknown},
		{"no client_side", nil, project(""), Unknown},
	} {
		if got := ModrinthNeed(c.version, c.project); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestPackNeed(t *testing.T) {
	for _, c := range []struct {
		env  *mrpack.Env
		want Need
	}{
		{nil, Required},
		{&mrpack.Env{Client: mrpack.Required, Server: mrpack.Required}, Required},
		{&mrpack.Env{Client: mrpack.Required, Server: mrpack.Unsupported}, Required},
		{&mrpack.Env{Client: mrpack.Optional, Server: mrpack.Required}, Optional},
		{&mrpack.Env{Client: mrpack.Unsupported, Server: mrpack.Required}, ServerOnly},
		{&mrpack.Env{Client: mrpack.Unknown, Server: mrpack.Required}, Unknown},
		{&mrpack.Env{Client: "", Server: mrpack.Required}, Unknown},
	} {
		if got := PackNeed(c.env); got != c.want {
			t.Errorf("%+v: %s, want %s", c.env, got, c.want)
		}
	}
}

// Every folder, every combination of CurseForge's side tags, required or
// not. Only a mod tagged for one side, or a resource or shader pack, says
// what friends need; the rest is Unknown, never a guess.
func TestCurseForgeNeed(t *testing.T) {
	sides := map[string][]string{"no tags": nil, "client": {"client"}, "server": {"server"}, "both": {"client", "server"}}
	want := map[string]Need{
		"mods no tags true": Unknown, "mods no tags false": Unknown,
		"mods client true": Required, "mods client false": Optional,
		"mods server true": ServerOnly, "mods server false": ServerOnly,
		"mods both true": Unknown, "mods both false": Unknown,
	}
	for _, folder := range []string{"resourcepacks", "shaderpacks"} {
		for tags := range sides {
			want[fmt.Sprintf("%s %s true", folder, tags)] = Required
			want[fmt.Sprintf("%s %s false", folder, tags)] = Optional
		}
	}
	for _, folder := range []string{"mods", "resourcepacks", "shaderpacks"} {
		for tags, s := range sides {
			for _, required := range []bool{true, false} {
				k := fmt.Sprintf("%s %s %t", folder, tags, required)
				if got := CurseForgeNeed(folder, s, required); got != want[k] {
					t.Errorf("%s: %s, want %s", k, got, want[k])
				}
			}
		}
	}
}

func TestText(t *testing.T) {
	got := text("share.notice.two", "Friends need {mod} and {other}", "mod", "Waystones", "other", "Create")
	if got.Text != "Friends need Waystones and Create" || got.Params["mod"] != "Waystones" || got.Params["other"] != "Create" {
		t.Errorf("%+v", got)
	}
	// A name that looks like a placeholder stays as it is.
	got = text("share.notice.two", "Friends need {mod} and {other}", "mod", "{other}", "other", "Create")
	if got.Text != "Friends need {other} and Create" {
		t.Errorf("placeholders were filled twice: %q", got.Text)
	}
	if got := text("share.notice.none", "Friends can join without mods"); got.Params != nil {
		t.Errorf("params without placeholders: %+v", got)
	}
}

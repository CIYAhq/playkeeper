// Package curated is Playkeeper's short, hand-picked list of add-ons worth
// offering on any server. Each entry says in plain words what the add-on is
// for and names the Modrinth or Hangar project to install on each server
// type; the add-ons library installs it like any other add-on.
//
// Some add-ons need more than their files. Simple Voice Chat listens on a UDP
// port of its own, so every server with it needs a free port that its
// container publishes and the firewall allows: SetUpVoiceChat writes the port
// into the add-on's settings and returns what the caller still has to do.
package curated

import (
	"fmt"
	"slices"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/addons"
)

const (
	KindNotForType     addons.Kind = "not_for_server_type"
	KindPortInvalid    addons.Kind = "port_invalid"
	KindNoFreePort     addons.Kind = "no_free_port"
	KindConfigUnusable addons.Kind = "config_unusable"
	KindOpenPort       addons.Kind = "open_port"
	KindClientMod      addons.Kind = "client_mod"
)

// Entry is one curated add-on.
type Entry struct {
	// ID is stable across releases; the UI and the agent refer to entries by
	// it.
	ID string `json:"id"`
	// Name says what the add-on does in plain words, such as "Voice chat".
	Name string `json:"name"`
	// Purpose is one sentence on what it is for.
	Purpose string `json:"purpose"`
	// Projects are what to install, each for some server types. No two
	// projects share a server type.
	Projects []Project `json:"projects"`
	// Setup is what the add-on needs beyond its files, or nil.
	Setup *Setup `json:"setup,omitempty"`
}

// Project is an add-on on Modrinth or Hangar.
type Project struct {
	// Types are the server types (internal/minecraft's ids) it is for.
	Types  []string      `json:"types"`
	Source addons.Source `json:"source"`
	// ID is Modrinth's project id or Hangar's "owner/slug".
	ID   string `json:"id"`
	Slug string `json:"slug"`
	// Title and Author are the project's own name and author as the source
	// lists them. The UI shows both as credit.
	Title  string `json:"title"`
	Author string `json:"author"`
	// License is the SPDX id the source lists.
	License string `json:"license"`
	// Permission links to where the author allows the add-on to be used this
	// way, for a project whose licence does not.
	Permission string `json:"permission,omitempty"`
	PageURL    string `json:"pageUrl"`
}

// Setup is what an add-on needs beyond its files.
type Setup struct {
	// Ports are the ports the add-on listens on. Each server with the add-on
	// needs its own, published from its container and allowed through the
	// firewall.
	Ports []Port `json:"ports,omitempty"`
	// Steps are for the owner or the players; Playkeeper cannot take them.
	Steps []addons.Notice `json:"steps,omitempty"`
}

// Port is a port an add-on listens on.
type Port struct {
	Protocol string `json:"protocol"` // udp or tcp
	// Default is the add-on's own default port. Playkeeper picks the lowest
	// free port from it.
	Default int `json:"default"`
}

// VoiceChatID is the voice chat entry's ID.
const VoiceChatID = "voice-chat"

// List returns every curated add-on, in the order the UI shows them.
func List() []Entry {
	plugins := []string{"paper", "purpur"}
	return []Entry{
		{
			ID: VoiceChatID, Name: "Voice chat",
			Purpose: "Players talk with their microphones to the players near them in the game, or in groups.",
			Projects: []Project{
				modrinth([]string{"paper", "purpur", "fabric", "quilt", "neoforge", "forge"}, "9eGKb6K1", "simple-voice-chat", "Simple Voice Chat", "henkelmax", "LicenseRef-All-Rights-Reserved").
					permitted("https://modrepo.de/minecraft/voicechat/faq"),
			},
			Setup: &Setup{
				Ports: []Port{{Protocol: "udp", Default: VoiceChatPort}},
				Steps: []addons.Notice{voiceChatClientStep()},
			},
		},
		{
			ID: "rollback", Name: "Grief rollback",
			Purpose: "Records who placed, broke or took what, so damage can be looked up and undone.",
			Projects: []Project{
				modrinth(plugins, "Lu3KuzdV", "coreprotect", "CoreProtect", "Intelli", "Artistic-2.0"),
				modrinth([]string{"fabric", "quilt"}, "LVN9ygNV", "ledger", "Ledger", "Potatoboy9999", "LGPL-3.0-only"),
			},
		},
		{
			ID: "pregenerate", Name: "World pre-generation",
			Purpose: "Generates the land around spawn ahead of time, so exploring does not make the server lag.",
			Projects: []Project{
				modrinth([]string{"paper", "purpur", "fabric", "neoforge", "forge"}, "fALzjamp", "chunky", "Chunky", "pop4959", "GPL-3.0-only"),
			},
		},
		{
			ID: "newer-clients", Name: "Newer game versions",
			Purpose: "Lets players whose game has already updated to a newer Minecraft version join before the server updates.",
			Projects: []Project{
				modrinth(plugins, "P1OZGk5p", "viaversion", "ViaVersion", "kennytv", "GPL-3.0-or-later"),
			},
		},
		{
			ID: "essentials", Name: "Everyday commands",
			Purpose: "Adds homes, warps, teleport requests, kits and other commands players expect.",
			Projects: []Project{
				modrinth(plugins, "hXiIvTyT", "essentialsx", "EssentialsX", "mdcfe", "GPL-3.0-only"),
			},
		},
		{
			ID: "permissions", Name: "Permissions",
			Purpose: "Puts players into groups and decides which commands each group may use.",
			Projects: []Project{
				modrinth([]string{"paper", "purpur", "fabric", "neoforge", "forge"}, "Vebnzrzj", "luckperms", "LuckPerms", "lucko", "MIT"),
			},
		},
		{
			// Paper and Purpur come with spark built in.
			ID: "lag-finder", Name: "Lag finder",
			Purpose: "Measures what the server spends its time on, to find what makes it lag.",
			Projects: []Project{
				modrinth([]string{"fabric", "quilt", "neoforge", "forge"}, "l6YH9Als", "spark", "spark", "lucko", "GPL-3.0-only"),
			},
		},
	}
}

func modrinth(types []string, id, slug, title, author, license string) Project {
	return Project{
		Types: types, Source: addons.Modrinth, ID: id, Slug: slug, Title: title, Author: author, License: license,
		PageURL: "https://modrinth.com/project/" + slug,
	}
}

func (p Project) permitted(url string) Project {
	p.Permission = url
	return p
}

// Get returns the entry with the ID.
func Get(id string) (Entry, bool) {
	for _, e := range List() {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// ForType returns the entries with a project for the server type.
func ForType(serverType string) []Entry {
	var out []Entry
	for _, e := range List() {
		if _, ok := e.project(serverType); ok {
			out = append(out, e)
		}
	}
	return out
}

// ByProject returns the entry that installs the project, so its setup can
// also be offered when the add-on came another way, such as with a modpack.
func ByProject(src addons.Source, id string) (Entry, bool) {
	for _, e := range List() {
		for _, p := range e.Projects {
			if p.Source == src && p.ID == id {
				return e, true
			}
		}
	}
	return Entry{}, false
}

// For returns the project to install on a server type.
func (e Entry) For(serverType string) (Project, error) {
	t, err := addons.TargetFor(serverType)
	if err != nil {
		return Project{}, err
	}
	if p, ok := e.project(serverType); ok {
		return p, nil
	}
	var types []string
	for _, p := range e.Projects {
		types = append(types, p.Types...)
	}
	return Project{}, &addons.Error{Notice: notice(KindNotForType,
		kv("id", e.ID, "name", e.Name, "type", serverType, "types", strings.Join(types, ",")),
		fmt.Sprintf("%s is only offered for %s servers; this server runs %s.", e.Name, typeNames(types), t.Name()),
		fmt.Sprintf("Search the add-ons page for one made for %s.", t.Name()))}
}

func (e Entry) project(serverType string) (Project, bool) {
	for _, p := range e.Projects {
		if slices.Contains(p.Types, serverType) {
			return p, true
		}
	}
	return Project{}, false
}

// Request is the add-ons library's request to install the project.
func (p Project) Request() addons.InstallRequest {
	return addons.InstallRequest{Source: p.Source, Project: p.ID}
}

// typeNames lists server types for a sentence: "Paper, Purpur and Fabric".
func typeNames(types []string) string {
	names := make([]string, len(types))
	for i, t := range types {
		names[i] = t
		if target, err := addons.TargetFor(t); err == nil {
			names[i] = target.Name()
		}
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func notice(k addons.Kind, params map[string]string, msg, hint string) addons.Notice {
	return addons.Notice{Kind: k, Params: params, Msg: msg, Hint: hint}
}

func fail(k addons.Kind, params map[string]string, msg, hint string) *addons.Error {
	return &addons.Error{Notice: notice(k, params, msg, hint)}
}

func kv(pairs ...string) map[string]string {
	m := make(map[string]string, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return m
}

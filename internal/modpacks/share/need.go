package share

import (
	"slices"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/addons/modrinth"
	"github.com/CIYAhq/playkeeper/internal/modpacks/mrpack"
)

// Need says whether friends' games need a mod to play on the server.
type Need string

const (
	// Required mods must be in friends' games: they work on both sides, or
	// the pack gives them to every player.
	Required Need = "required"
	// Optional mods are extras friends can play without.
	Optional Need = "optional"
	// ServerOnly mods run on the server alone.
	ServerOnly Need = "server_only"
	// Unknown means the data doesn't say, and Playkeeper doesn't guess.
	Unknown Need = "unknown"
)

// Label is the need as the Mods tab and the public page show it.
func (n Need) Label() Text {
	switch n {
	case Required:
		return text("share.need.required", "Friends need it")
	case Optional:
		return text("share.need.optional", "Optional for friends")
	case ServerOnly:
		return text("share.need.server_only", "Server only")
	}
	return text("share.need.unknown", "Unknown")
}

// rank orders needs for raising a dependency to what its dependent needs.
// An Unknown mod goes into the file as optional, so it ranks with Optional.
func (n Need) rank() int {
	switch n {
	case Required:
		return 2
	case Optional, Unknown:
		return 1
	}
	return 0
}

// ModrinthNeed reads Modrinth's side information for a mod on a server: the
// version's environment, else the project's environments when they agree,
// else the project's older client_side field. Either argument may be nil.
//
// A mod that works on either side (client_or_server) is ServerOnly, since
// the server already runs it. One that works best on both sides, or that
// the server runs and players may add, is Optional, and so is one that only
// works in singleplayer. A client mod is Required: it is in the setup for
// players' games.
func ModrinthNeed(v *modrinth.Version, p *modrinth.Project) Need {
	if v != nil {
		if n := environmentNeed(v.Environment); n != Unknown {
			return n
		}
	}
	if p == nil {
		return Unknown
	}
	n := Unknown
	for _, e := range p.Environment {
		switch m := environmentNeed(e); {
		case m == Unknown:
		case n == Unknown:
			n = m
		case m != n:
			return clientSideNeed(p.ClientSide)
		}
	}
	if n != Unknown {
		return n
	}
	return clientSideNeed(p.ClientSide)
}

func environmentNeed(e string) Need {
	switch e {
	case "client_and_server", "client_only", "client_only_server_optional":
		return Required
	case "server_only_client_optional", "client_or_server_prefers_both", "singleplayer_only":
		return Optional
	case "server_only", "dedicated_server_only", "client_or_server":
		return ServerOnly
	}
	return Unknown
}

func clientSideNeed(s string) Need {
	switch s {
	case "required":
		return Required
	case "optional":
		return Optional
	case "unsupported":
		return ServerOnly
	}
	return Unknown
}

// PackNeed reads a Modrinth pack's env for one of its files: the pack's own
// word on whether players' games get it. A file without env is required on
// both sides. Values outside the format give Unknown, for the caller to ask
// Modrinth about the file instead.
func PackNeed(env *mrpack.Env) Need {
	if env == nil {
		return Required
	}
	switch env.Client {
	case mrpack.Required:
		return Required
	case mrpack.Optional:
		return Optional
	case mrpack.Unsupported:
		return ServerOnly
	}
	return Unknown
}

// CurseForgeNeed reads what a CurseForge pack says about one of its files in
// folder (mods, resourcepacks or shaderpacks): the sides CurseForge tags it
// for and whether the pack requires it. Resource and shader packs exist only
// in games, and a mod tagged for one side says which; a mod tagged for both
// sides or for none gives Unknown, for the caller to ask Modrinth instead.
func CurseForgeNeed(folder string, sides []string, required bool) Need {
	client, server := slices.Contains(sides, "client"), slices.Contains(sides, "server")
	switch {
	case folder == "resourcepacks" || folder == "shaderpacks":
	case server && !client:
		return ServerOnly
	case !client || server:
		return Unknown
	}
	return capped(Required, required)
}

// capped lowers Required to Optional for a file its pack doesn't require.
func capped(n Need, required bool) Need {
	if n == Required && !required {
		return Optional
	}
	return n
}

// Text is something to show: Text in English, and Key with Params for
// translation. Placeholders in the English text are the params' names in
// braces.
type Text struct {
	Key    string            `json:"key"`
	Params map[string]string `json:"params,omitempty"`
	Text   string            `json:"text"`
}

// text fills english's {name} placeholders from pairs of names and values.
func text(key, english string, pairs ...string) Text {
	t := Text{Key: key, Text: english}
	if len(pairs) < 2 {
		return t
	}
	t.Params = make(map[string]string, len(pairs)/2)
	var r []string
	for i := 0; i+1 < len(pairs); i += 2 {
		t.Params[pairs[i]] = pairs[i+1]
		r = append(r, "{"+pairs[i]+"}", pairs[i+1])
	}
	t.Text = strings.NewReplacer(r...).Replace(english)
	return t
}

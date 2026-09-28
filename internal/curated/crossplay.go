package curated

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/gamefiles"
)

// Crossplay lets players of Minecraft: Bedrock Edition (phones, tablets,
// Windows, and consoles through a workaround on their side) join a Java
// server. Geyser translates between the two editions and listens for
// Bedrock players on a UDP port of its own; Floodgate lets them in without a
// Java account, under their Xbox name with FloodgatePrefix in front. Both
// come as Paper plugins, so crossplay is for Paper and Purpur.

// CrossplayPort is Bedrock's own default port: players who type only the
// address reach the first server that has crossplay.
const CrossplayPort = 19132

// FloodgatePrefix goes in front of Bedrock players' names on the server, so
// they never clash with a Java account's: a Java name can't hold a dot.
const FloodgatePrefix = "."

// CrossplayTypes are the server types crossplay runs on.
var CrossplayTypes = []string{"paper", "purpur"}

// Geyser and Floodgate as the add-on library installs them. GeyserMC
// publishes Geyser's Paper builds on Modrinth, each marked as a beta, and
// Floodgate's only on its own server, which its Hangar listing links to.
func Geyser() Project {
	p := modrinth(slices.Clone(CrossplayTypes), "wKkoqHrH", "geyser", "Geyser", "GeyserMC", "MIT")
	p.PageURL = "https://modrinth.com/plugin/geyser"
	return p
}

func Floodgate() Project {
	return Project{Types: slices.Clone(CrossplayTypes), Source: addons.Hangar, ID: "17", Slug: "Floodgate", Title: "Floodgate", Author: "GeyserMC",
		License: "MIT", PageURL: "https://hangar.papermc.io/GeyserMC/Floodgate"}
}

// CrossplayProjects are what crossplay installs, Geyser first.
func CrossplayProjects() []Project { return []Project{Geyser(), Floodgate()} }

// IsCrossplayProject reports whether the add-on with key is one of
// crossplay's, from either of its listings, so that it is installed and
// removed only with crossplay.
func IsCrossplayProject(key addons.Key) bool {
	switch key {
	case addons.Key{Source: addons.Modrinth, ProjectID: "wKkoqHrH"}, addons.Key{Source: addons.Modrinth, ProjectID: "geyser"},
		addons.Key{Source: addons.Hangar, ProjectID: "17"}, addons.Key{Source: addons.Hangar, ProjectID: "Floodgate"},
		addons.Key{Source: addons.Hangar, ProjectID: "14"}, addons.Key{Source: addons.Hangar, ProjectID: "Geyser"}:
		return true
	}
	return false
}

// CrossplayFor checks that crossplay runs on a server type.
func CrossplayFor(serverType string) error {
	t, err := addons.TargetFor(serverType)
	if err != nil {
		return err
	}
	if slices.Contains(CrossplayTypes, serverType) {
		return nil
	}
	return &addons.Error{Notice: notice(KindNotForType, kv("id", "crossplay", "name", "Crossplay", "type", serverType, "types", strings.Join(CrossplayTypes, ",")),
		fmt.Sprintf("Crossplay is only offered for %s servers; this server runs %s.", typeNames(CrossplayTypes), t.Name()),
		"Bedrock players can join a Paper or Purpur server.")}
}

// Where Geyser and Floodgate keep their settings, inside the server's data
// directory.
const (
	GeyserConfigPath    = "plugins/Geyser-Spigot/config.yml"
	FloodgateConfigPath = "plugins/floodgate/config.yml"
)

// maxYAML bounds a plugin config Playkeeper reads; Geyser's is about 16 KB.
const maxYAML = 256 << 10

// GeyserConfig returns Geyser's config with Playkeeper's settings: Bedrock
// players on port, on every address the container has (server.properties'
// server-ip may be one of the machine's the container doesn't have), on the
// same port at every start, checked by Floodgate, and their IP addresses
// kept out of the log, as the server's own are. Every other line stays as
// it is; existing may be empty, and Geyser fills in the rest when it starts.
func GeyserConfig(existing []byte, port int) ([]byte, error) {
	if err := checkPort(port); err != nil {
		return nil, err
	}
	return editYAML(existing, []yamlSet{
		{"bedrock", "address", "0.0.0.0"},
		{"bedrock", "port", strconv.Itoa(port)},
		{"bedrock", "clone-remote-port", "false"},
		{"java", "auth-type", "floodgate"},
		{"", "log-player-ip-addresses", "false"},
	})
}

// FloodgateConfig returns Floodgate's config with Playkeeper's settings:
// FloodgatePrefix in front of Bedrock players' names, spaces in them as
// underscores, and no usage statistics sent to bStats, like the server's
// own. Every other line stays as it is.
func FloodgateConfig(existing []byte) ([]byte, error) {
	return editYAML(existing, []yamlSet{
		{"", "username-prefix", strconv.Quote(FloodgatePrefix)},
		{"", "replace-spaces", "true"},
		{"metrics", "enabled", "false"},
	})
}

// WriteCrossplayConfig sets Playkeeper's settings in Geyser's and
// Floodgate's configs in the server's files, making them when the plugins
// haven't yet. Call it before each start of a server with crossplay: the
// plugins read their configs when they start, and a restored backup or an
// edit by hand may have changed them.
func WriteCrossplayConfig(dataDir string, port int, owner *gamefiles.Owner) error {
	if err := checkPort(port); err != nil {
		return err
	}
	d, err := gamefiles.Open(dataDir, owner)
	if err != nil {
		return crossplayConfigError(GeyserConfigPath, "Geyser", err)
	}
	defer d.Close()
	for _, c := range []struct {
		path, name string
		edit       func([]byte) ([]byte, error)
	}{
		{GeyserConfigPath, "Geyser", func(b []byte) ([]byte, error) { return GeyserConfig(b, port) }},
		{FloodgateConfigPath, "Floodgate", FloodgateConfig},
	} {
		old, err := d.ReadFile(c.path, maxYAML)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return crossplayConfigError(c.path, c.name, err)
		}
		b, err := c.edit(old)
		if err != nil {
			return crossplayConfigError(c.path, c.name, err)
		}
		if string(b) == string(old) {
			continue
		}
		if err := d.WriteFile(c.path, b, 0o640); err != nil {
			return crossplayConfigError(c.path, c.name, err)
		}
	}
	return nil
}

func crossplayConfigError(file, plugin string, err error) *addons.Error {
	var (
		ae *addons.Error
		ge *gamefiles.Error
		r  reason
	)
	msg := fmt.Sprintf("Playkeeper could not write crossplay's settings to %s.", file)
	hint := fmt.Sprintf("Fix or delete the file (%s makes a new one when it starts), then try again.", plugin)
	switch {
	case errors.As(err, &ae):
		return ae
	case errors.As(err, &ge):
		msg, hint = fmt.Sprintf("Playkeeper could not write crossplay's settings to %s: %s", file, ge.Msg), ge.Hint
	case errors.As(err, &r):
		msg = fmt.Sprintf("Playkeeper could not write crossplay's settings to %s: %s.", file, r)
	}
	return &addons.Error{Notice: notice(KindConfigUnusable, kv("file", file), msg, hint), Err: err}
}

// CrossplaySteps are what's left for the owner and the players once
// crossplay has its port.
func CrossplaySteps(port int) []addons.Notice {
	return []addons.Notice{openPortStep("Crossplay", Publish{Port: port, Protocol: "udp"}), consoleStep()}
}

// KindConsoles says how console players reach the server.
const KindConsoles addons.Kind = "consoles"

func consoleStep() addons.Notice {
	const url = "https://geysermc.org/wiki/geyser/using-geyser-with-consoles/"
	return notice(KindConsoles, kv("url", url),
		"Bedrock players on phones, tablets and Windows add the server by its address and port. Xbox, PlayStation and Switch players can only join featured servers, so they need a workaround such as BedrockConnect.",
		"GeyserMC explains the ways in: "+url)
}

// yamlSet is a key Playkeeper sets in a plugin's YAML config: at the top
// level, or one level inside the top-level block parent.
type yamlSet struct{ parent, key, value string }

// editYAML sets keys in a block-style YAML document, the kind Geyser and
// Floodgate write, and leaves every other line as it is. A key that isn't
// there is added, under its block when it has one.
func editYAML(doc []byte, sets []yamlSet) ([]byte, error) {
	if !utf8.Valid(doc) {
		return nil, reason("it is not UTF-8 text")
	}
	text := string(doc)
	nl := "\n"
	if strings.Contains(text, "\r\n") {
		nl = "\r\n"
	}
	var lines []string
	if text = strings.TrimSuffix(strings.ReplaceAll(text, "\r\n", "\n"), "\n"); text != "" {
		lines = strings.Split(text, "\n")
	}
	for _, s := range sets {
		var err error
		if lines, err = setYAML(lines, s); err != nil {
			return nil, err
		}
	}
	return []byte(strings.Join(lines, nl) + nl), nil
}

func setYAML(lines []string, s yamlSet) ([]string, error) {
	from, end, indent := 0, len(lines), 0
	// last is the block's last line with a setting on it, which a missing
	// key goes after.
	last := -1
	if s.parent != "" {
		p := -1
		for i, l := range lines {
			in, key, rest, ok, err := yamlKey(l)
			if err != nil {
				return nil, err
			}
			if ok && in == 0 && key == s.parent {
				if rest != "" {
					return nil, reason(s.parent + " is not a block of settings Playkeeper can change")
				}
				p = i
				break
			}
		}
		if p < 0 {
			return append(lines, s.parent+":", "  "+s.key+": "+s.value), nil
		}
		from, end, indent = p+1, len(lines), 2
		found := false
		for i := from; i < len(lines); i++ {
			in, content := yamlIndent(lines[i])
			if !content {
				continue
			}
			if in == 0 {
				end = i
				break
			}
			if !found {
				indent, found = in, true
			}
			last = i
		}
	}
	for i := from; i < end; i++ {
		in, key, _, ok, err := yamlKey(lines[i])
		if err != nil {
			return nil, err
		}
		if !ok || in != indent || key != s.key {
			continue
		}
		// Drop what the old value held on the lines below it, if anything.
		j := i + 1
		for ; j < end; j++ {
			if in2, content := yamlIndent(lines[j]); content && in2 <= indent {
				break
			}
		}
		kept := slices.DeleteFunc(slices.Clone(lines[i+1:j]), func(l string) bool { _, content := yamlIndent(l); return content })
		out := append(slices.Clone(lines[:i]), strings.Repeat(" ", indent)+s.key+": "+s.value)
		out = append(out, kept...)
		return append(out, lines[j:]...), nil
	}
	line := strings.Repeat(" ", indent) + s.key + ": " + s.value
	switch {
	case s.parent == "":
		return append(lines, line), nil
	case last >= 0:
		return slices.Insert(lines, last+1, line), nil
	}
	return slices.Insert(lines, from, line), nil
}

// yamlIndent is a line's indentation, and whether it holds more than space
// or a comment.
func yamlIndent(line string) (int, bool) {
	t := strings.TrimLeft(line, " ")
	return len(line) - len(t), t != "" && t[0] != '#'
}

// yamlKey reads a "key: value" line: its indentation, the key and what
// follows it without a comment. A tab in the indentation isn't YAML.
func yamlKey(line string) (indent int, key, rest string, ok bool, err error) {
	t := strings.TrimLeft(line, " ")
	indent = len(line) - len(t)
	if strings.HasPrefix(t, "\t") {
		return 0, "", "", false, reason("it is indented with tabs, which YAML doesn't allow")
	}
	if t == "" || t[0] == '#' {
		return indent, "", "", false, nil
	}
	i := 0
	for i < len(t) && (t[i] == '-' || t[i] == '_' || t[i] >= 'a' && t[i] <= 'z' || t[i] >= 'A' && t[i] <= 'Z' || t[i] >= '0' && t[i] <= '9') {
		i++
	}
	if i == 0 || i >= len(t) || t[i] != ':' || i+1 < len(t) && t[i+1] != ' ' {
		return indent, "", "", false, nil
	}
	rest = strings.TrimSpace(t[i+1:])
	if strings.HasPrefix(rest, "#") {
		rest = ""
	}
	return indent, t[:i], rest, true, nil
}

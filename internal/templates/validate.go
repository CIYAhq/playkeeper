package templates

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/addons"
)

var (
	difficulties = []string{"peaceful", "easy", "normal", "hard"}
	gameModes    = []string{"survival", "creative", "adventure", "spectator"}
	levelTypes   = []string{"normal", "flat", "amplified", "large_biomes"}
	playStyles   = []string{"friends", "creative", "hardcore", "solo"}
	channels     = []string{"release", "beta", "alpha"}
	// nonModTypes are the known types that cannot run a modpack; types
	// this Playkeeper does not know yet may.
	nonModTypes = []string{"paper", "purpur", "vanilla"}
)

// settingLabels name the settings in messages, by JSON name.
var settingLabels = map[string]string{
	"difficulty": "difficulty", "gameMode": "game mode", "viewDistance": "view distance", "levelType": "world type",
	"maxPlayers": "player limit", "motd": "server list message", "playStyle": "play style", "memoryMB": "memory",
}

// Validate checks everything Decode checks once a template is read: every
// field, count and length, and the rules between fields. A valid template
// is canonical: Decode reads its link back unchanged.
func (t *Template) Validate() error {
	if t == nil {
		return fail(KindNotTemplate, nil, "This is not a Playkeeper template.", "")
	}
	if e := t.validate(); e != nil {
		return e
	}
	return nil
}

func (t *Template) validate() *Error {
	switch {
	case t.Format > Format:
		return newer()
	case t.Format != Format:
		return invalid("playkeeperTemplate", "value", "The template does not say which format it has.")
	}
	if e := checkText("name", "The template's name", t.Name, maxName, true); e != nil {
		return e
	}
	if e := checkText("description", "The template's description", t.Description, maxDescription, false); e != nil {
		return e
	}
	if e := checkText("author", "The template's author", t.Author, maxLabel, false); e != nil {
		return e
	}
	if t.Created != "" && !validDay(t.Created) {
		return invalid("created", "value", "The day the template was made is not a date.")
	}
	if t.Game != Game {
		return invalid("game", "value", fmt.Sprintf("This template is for a game Playkeeper does not run (\"%s\").", printable(t.Game)))
	}
	if e := t.Server.validate(); e != nil {
		return e
	}
	if bad := badSettings(t.Settings); len(bad) > 0 {
		return invalid("settings."+bad[0], "value", fmt.Sprintf("The template's %s setting is not one Playkeeper can use.", settingLabels[bad[0]]))
	}
	if e := t.validateAddons(); e != nil {
		return e
	}
	if e := t.validateModpack(); e != nil {
		return e
	}
	if e := t.validatePacks(); e != nil {
		return e
	}
	if js, err := canonicalJSON(t); err != nil || len(js) > MaxFileSize {
		return fail(KindTooLarge, kv("limit", maxFileLabel), "The template is larger than the "+maxFileLabel+" a template can be.",
			"Leave out some add-ons.")
	}
	return nil
}

func newer() *Error {
	return fail(KindNewer, nil, "This template was made by a newer Playkeeper.", "Update Playkeeper, then open the template again.")
}

func (s Server) validate() *Error {
	if !validType(s.Type) {
		return invalid("server.type", "value", fmt.Sprintf("The template's server type \"%s\" is not a valid type.", printable(s.Type)))
	}
	if !validMinecraft(s.MinecraftVersion) {
		return invalid("server.minecraftVersion", "value", fmt.Sprintf("The template's Minecraft version \"%s\" is not a valid version.", printable(s.MinecraftVersion)))
	}
	if len(s.Build) > maxBuild {
		return invalid("server.build", "too_many", fmt.Sprintf("The template has more than %d build details.", maxBuild))
	}
	for _, k := range sortedKeys(s.Build) {
		if !validBuildKey(k) || !validBuildValue(s.Build[k]) {
			return invalid("server.build."+printable(k), "value", "The template's build details are not valid.")
		}
	}
	return nil
}

func (t *Template) validateAddons() *Error {
	if len(t.Addons) > MaxAddons {
		return invalid("addons", "too_many", fmt.Sprintf("The template lists more than %d add-ons.", MaxAddons))
	}
	if len(t.Addons) == 0 {
		return nil
	}
	if t.Server.Type == "vanilla" {
		return invalid("addons", "not_allowed", "Vanilla servers cannot load plugins or mods, but the template lists some.")
	}
	target, unknownType := addons.TargetFor(t.Server.Type)
	seen := map[addons.Key]bool{}
	for i, a := range t.Addons {
		field := fmt.Sprintf("addons[%d]", i)
		if e := checkRef(field, fmt.Sprintf("Add-on %d", i+1), a.Source, a.Project, a.Slug); e != nil {
			return e
		}
		if e := checkText(field+".name", fmt.Sprintf("The name of add-on %d", i+1), a.Name, maxLabel, true); e != nil {
			return e
		}
		name := printable(a.Name)
		if unknownType == nil && !slices.Contains(target.Sources(), a.Source) {
			return invalid(field+".source", "not_allowed", fmt.Sprintf("%s comes from %s, which has no add-ons for %s servers.", name, a.Source.Name(), target.Name()))
		}
		switch {
		case a.Pin == nil && !a.Latest:
			return invalid(field+".pin", "missing", fmt.Sprintf("%s has neither a pinned version nor \"newest version\" set.", name))
		case a.Pin != nil && a.Latest:
			return invalid(field+".latest", "not_allowed", fmt.Sprintf("%s has both a pinned version and \"newest version\" set.", name))
		case a.Pin != nil:
			if e := checkPin(field+".pin", name, a.Source, *a.Pin); e != nil {
				return e
			}
		}
		if seen[a.Key()] {
			return invalid(field, "duplicate", fmt.Sprintf("%s is listed twice.", name))
		}
		seen[a.Key()] = true
	}
	parent := map[addons.Key]addons.Key{}
	for i, a := range t.Addons {
		if a.DependencyOf == "" {
			continue
		}
		p := addons.Key{Source: a.Source, ProjectID: a.DependencyOf}
		field, name := fmt.Sprintf("addons[%d].dependencyOf", i), printable(a.Name)
		switch {
		case p == a.Key():
			return invalid(field, "value", fmt.Sprintf("%s is marked as needed by itself.", name))
		case !seen[p]:
			return invalid(field, "value", fmt.Sprintf("%s is marked as needed by an add-on the template does not list.", name))
		}
		parent[a.Key()] = p
	}
	for k := range parent {
		at := k
		for range len(parent) + 1 {
			p, ok := parent[at]
			if !ok {
				break
			}
			if p == k {
				return invalid("addons", "cycle", "The template's add-ons are marked as needed by each other in a circle.")
			}
			at = p
		}
	}
	return nil
}

func (t *Template) validateModpack() *Error {
	m := t.Modpack
	if m == nil {
		return nil
	}
	if m.Source != addons.Modrinth {
		return invalid("modpack.source", "value", "The template's modpack does not come from Modrinth.")
	}
	if e := checkRef("modpack", "The template's modpack", m.Source, m.Project, m.Slug); e != nil {
		return e
	}
	if e := checkText("modpack.name", "The modpack's name", m.Name, maxLabel, true); e != nil {
		return e
	}
	if slices.Contains(nonModTypes, t.Server.Type) {
		return invalid("modpack", "not_allowed", fmt.Sprintf("The template names a modpack, but modpacks need a mod server, not %s.", printable(t.Server.Type)))
	}
	return checkPin("modpack.pin", printable(m.Name), m.Source, m.Pin)
}

func (t *Template) validatePacks() *Error {
	if len(t.Packs) > MaxDataPacks+1 {
		return invalid("packs", "too_many", fmt.Sprintf("The template lists more than %d packs.", MaxDataPacks+1))
	}
	var resource, data int
	urls, names := map[string]bool{}, map[string]bool{}
	for i, p := range t.Packs {
		field := fmt.Sprintf("packs[%d]", i)
		if !validPackName(p.Name) {
			return invalid(field+".name", "value", fmt.Sprintf("Pack %d has no name, or one that cannot be a file name.", i+1))
		}
		name := printable(p.Name)
		if !publicURL(p.URL) {
			return invalid(field+".url", "address", fmt.Sprintf("The address of the pack %s is not a public HTTPS address.", name))
		}
		if urls[p.URL] {
			return invalid(field+".url", "duplicate", fmt.Sprintf("The address of the pack %s is listed twice.", name))
		}
		urls[p.URL] = true
		if p.SHA1 != "" && !validHash("sha1", p.SHA1) || p.SHA256 != "" && !validHash("sha256", p.SHA256) {
			return invalid(field, "hash", fmt.Sprintf("The checksum of the pack %s is not valid.", name))
		}
		switch p.Kind {
		case ResourcePack:
			resource++
			if p.SHA1 == "" {
				return invalid(field+".sha1", "missing", fmt.Sprintf("The resource pack %s has no SHA-1 checksum, which Minecraft needs to check it.", name))
			}
			if e := checkText(field+".prompt", "The resource pack's prompt", p.Prompt, maxPrompt, false); e != nil {
				return e
			}
		case DataPack:
			data++
			if p.SHA1 == "" && p.SHA256 == "" {
				return invalid(field+".sha256", "missing", fmt.Sprintf("The data pack %s has no checksum.", name))
			}
			if p.Required || p.Prompt != "" {
				return invalid(field, "not_allowed", fmt.Sprintf("The data pack %s is marked as required or has a prompt, which only a resource pack can.", name))
			}
			n := strings.ToLower(p.Name)
			if names[n] {
				return invalid(field+".name", "duplicate", fmt.Sprintf("Two data packs are named %s.", name))
			}
			names[n] = true
		default:
			return invalid(field+".kind", "value", fmt.Sprintf("The pack %s is neither a resource pack nor a data pack.", name))
		}
	}
	switch {
	case resource > 1:
		return invalid("packs", "too_many", fmt.Sprintf("A server has one resource pack, but the template lists %d.", resource))
	case data > MaxDataPacks:
		return invalid("packs", "too_many", fmt.Sprintf("The template lists more than %d data packs.", MaxDataPacks))
	}
	return nil
}

// checkRef checks the source and ids of an add-on or modpack.
func checkRef(field, what string, src addons.Source, project, slug string) *Error {
	if src != addons.Modrinth && src != addons.Hangar {
		return invalid(field+".source", "value", fmt.Sprintf("%s comes from \"%s\"; templates list add-ons from Modrinth and Hangar.", what, printable(string(src))))
	}
	if !validID(src, project) {
		return invalid(field+".project", "value", fmt.Sprintf("%s has no valid %s project id.", what, src.Name()))
	}
	if slug != "" && !validRef(slug) {
		return invalid(field+".slug", "value", fmt.Sprintf("%s has a slug that is not valid.", what))
	}
	return nil
}

// checkPin checks a pinned version and the hash its source publishes.
func checkPin(field, name string, src addons.Source, p Pin) *Error {
	if !validID(src, p.VersionID) {
		return invalid(field+".versionId", "value", fmt.Sprintf("%s has no valid version id.", name))
	}
	if e := checkText(field+".versionNumber", "The version number of "+name, p.VersionNumber, maxLabel, true); e != nil {
		return e
	}
	if p.Channel != "" && !slices.Contains(channels, p.Channel) {
		return invalid(field+".channel", "value", fmt.Sprintf("%s has an unknown release channel.", name))
	}
	algo := hashAlgo(src)
	if p.HashAlgo != algo {
		return invalid(field+".hashAlgo", "value", fmt.Sprintf("The hash of %s must be the %s %s publishes.", name, strings.ToUpper(algo[:3])+"-"+algo[3:], src.Name()))
	}
	if !validHash(algo, p.Hash) {
		return invalid(field+".hash", "hash", fmt.Sprintf("The hash of %s is not valid.", name))
	}
	return nil
}

// validDay reports whether s is a day as YYYY-MM-DD, from 2020 on.
func validDay(s string) bool {
	d, err := time.Parse(time.DateOnly, s)
	return err == nil && d.Year() >= 2020 && d.Year() < 10000
}

// checkText checks a line of text a user reads.
func checkText(field, what, s string, max int, required bool) *Error {
	switch {
	case required && strings.TrimSpace(s) == "":
		return invalid(field, "missing", what+" is missing.")
	case utf8.RuneCountInString(s) > max:
		return invalid(field, "too_long", fmt.Sprintf("%s is longer than %d characters.", what, max))
	case !plainText(s):
		return invalid(field, "characters", what+" has characters Playkeeper does not show.")
	case s != strings.TrimSpace(s):
		return invalid(field, "spaces", what+" starts or ends with a space.")
	}
	return nil
}

// badSettings lists the settings that are out of range, by JSON name.
func badSettings(s Settings) []string {
	var bad []string
	add := func(field string, ok bool) {
		if !ok {
			bad = append(bad, field)
		}
	}
	add("difficulty", s.Difficulty == "" || slices.Contains(difficulties, s.Difficulty))
	add("gameMode", s.GameMode == "" || slices.Contains(gameModes, s.GameMode))
	add("viewDistance", s.ViewDistance == 0 || s.ViewDistance >= 3 && s.ViewDistance <= 32)
	add("levelType", s.LevelType == "" || slices.Contains(levelTypes, s.LevelType))
	add("maxPlayers", s.MaxPlayers == 0 || s.MaxPlayers >= 1 && s.MaxPlayers <= 100)
	add("motd", utf8.RuneCountInString(s.MOTD) <= maxMOTD && plainText(s.MOTD))
	add("playStyle", s.PlayStyle == "" || slices.Contains(playStyles, s.PlayStyle))
	add("memoryMB", s.MemoryMB == 0 || s.MemoryMB >= minMemoryMB && s.MemoryMB <= maxMemoryMB)
	return bad
}

func clearSetting(s *Settings, field string) {
	switch field {
	case "difficulty":
		s.Difficulty = ""
	case "gameMode":
		s.GameMode = ""
	case "viewDistance":
		s.ViewDistance = 0
	case "levelType":
		s.LevelType = ""
	case "maxPlayers":
		s.MaxPlayers = 0
	case "motd":
		s.MOTD = ""
	case "playStyle":
		s.PlayStyle = ""
	case "memoryMB":
		s.MemoryMB = 0
	}
}

func hashAlgo(src addons.Source) string {
	if src == addons.Hangar {
		return "sha256"
	}
	return "sha512"
}

// isFormat reports invisible formatting characters, such as bidirectional
// overrides, which make text read differently from what it is.
func isFormat(r rune) bool { return unicode.Is(unicode.Cf, r) }

// plainText accepts one line of text: no control or formatting characters,
// no line breaks, and no § (Minecraft's formatting codes).
func plainText(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || isFormat(r) || r == '§' || r == utf8.RuneError || unicode.IsSpace(r) && r != ' ' {
			return false
		}
	}
	return true
}

func validType(s string) bool {
	if s == "" || len(s) > 32 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	return onlyRunes(s, func(r rune) bool { return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' })
}

func validMinecraft(s string) bool {
	if s == "" || len(s) > 32 || s[0] < '0' || s[0] > '9' {
		return false
	}
	return onlyRunes(s, func(r rune) bool { return isAlnum(r) || r == '.' || r == '-' || r == '_' || r == '+' })
}

func validBuildKey(s string) bool {
	if s == "" || len(s) > 32 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	return onlyRunes(s, isAlnum)
}

func validBuildValue(s string) bool {
	if s == "" || len(s) > 64 || !isAlnum(rune(s[0])) {
		return false
	}
	return onlyRunes(s, func(r rune) bool { return isAlnum(r) || r == '.' || r == '-' || r == '_' || r == '+' })
}

// validID checks a project or version id: base62 on Modrinth, a number on
// Hangar.
func validID(src addons.Source, s string) bool {
	switch src {
	case addons.Modrinth:
		return s != "" && len(s) <= 64 && onlyRunes(s, isAlnum)
	case addons.Hangar:
		if s == "" || len(s) > 18 || s[0] == '0' {
			return false
		}
		_, err := strconv.ParseInt(s, 10, 64)
		return err == nil && onlyRunes(s, func(r rune) bool { return r >= '0' && r <= '9' })
	}
	return false
}

// validRef is the add-on library's rule for slugs.
func validRef(s string) bool {
	if s == "" || len(s) > 64 || s == "." || s == ".." {
		return false
	}
	return onlyRunes(s, func(r rune) bool { return isAlnum(r) || r == '-' || r == '_' || r == '.' })
}

// validHash accepts a lowercase hex digest of the algorithm's length.
func validHash(algo, h string) bool {
	n := map[string]int{"sha512": 128, "sha256": 64, "sha1": 40}[algo]
	return n > 0 && len(h) == n && onlyRunes(h, func(r rune) bool { return r >= '0' && r <= '9' || r >= 'a' && r <= 'f' })
}

func validPackName(s string) bool {
	if strings.TrimSpace(s) != s || s == "" || utf8.RuneCountInString(s) > maxLabel || !plainText(s) {
		return false
	}
	return s[0] != '.' && s[0] != '-' && !strings.HasSuffix(s, ".") && !strings.ContainsAny(s, `/\:*?"<>|`)
}

// publicURL accepts an HTTPS address on a public host name: no IP
// addresses, local names, other ports, user names, queries (which can hold
// keys) or fragments.
func publicURL(raw string) bool {
	if raw == "" || len(raw) > maxURL {
		return false
	}
	for _, r := range raw {
		if !isAlnum(r) && !strings.ContainsRune("-._~:/[]@!$&'()*+,;=%", r) {
			return false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Host == "" || strings.HasPrefix(u.Host, "[") {
		return false
	}
	if p := u.Port(); p != "" && p != "443" {
		return false
	}
	return publicHost(u.Hostname())
}

var localSuffixes = []string{".localhost", ".local", ".internal", ".lan", ".home", ".corp", ".intranet", ".private", ".arpa", ".test", ".invalid", ".example", ".onion"}

func publicHost(h string) bool {
	h = strings.ToLower(h)
	if net.ParseIP(h) != nil || len(h) > 253 || !strings.Contains(h, ".") || h == "localhost" {
		return false
	}
	for _, s := range localSuffixes {
		if strings.HasSuffix(h, s) {
			return false
		}
	}
	labels := strings.Split(h, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		if !onlyRunes(l, func(r rune) bool { return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' }) {
			return false
		}
	}
	tld := labels[len(labels)-1]
	return !onlyRunes(tld, func(r rune) bool { return r >= '0' && r <= '9' })
}

func isAlnum(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

func onlyRunes(s string, ok func(rune) bool) bool {
	for _, r := range s {
		if !ok(r) {
			return false
		}
	}
	return true
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

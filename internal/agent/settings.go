package agent

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/gamefiles"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/minecraft/software"
)

// Server types, from the registry in internal/minecraft/types.go.

func serverTypes() []api.ServerType {
	out := make([]api.ServerType, 0, len(minecraft.Types))
	for _, t := range minecraft.Types {
		out = append(out, api.ServerType{ID: t.ID, Name: t.Name, Available: typeAvailable(t.ID), Check: typeCheck(t.ID)})
	}
	return out
}

// typeAvailable is true for the types Playkeeper can install: Paper through
// its own setup, the others through the software package.
func typeAvailable(id string) bool {
	t, ok := minecraft.TypeByID(id)
	return ok && t.Available && (id == api.TypePaper || software.Supported(id))
}

func typeName(id string) string {
	if t, ok := minecraft.TypeByID(id); ok {
		return t.Name
	}
	return strconv.Quote(id)
}

// Plain-language game settings.

var (
	difficulties = map[string]bool{"peaceful": true, "easy": true, "normal": true, "hard": true}
	gameModes    = map[string]bool{"survival": true, "creative": true, "adventure": true, "spectator": true}
	levelTypes   = map[string]string{"normal": "minecraft:normal", "flat": "minecraft:flat", "amplified": "minecraft:amplified", "large_biomes": "minecraft:large_biomes"}
)

const (
	minViewDistance = 3
	maxViewDistance = 32
)

// validGameplay checks settings from a request. The world type and hardcore
// can only be chosen when the world is created.
func validGameplay(g api.Gameplay, creating bool) (api.Gameplay, error) {
	g.Difficulty = strings.ToLower(strings.TrimSpace(g.Difficulty))
	g.GameMode = strings.ToLower(strings.TrimSpace(g.GameMode))
	g.LevelType = strings.ToLower(strings.TrimSpace(g.LevelType))
	if g.Difficulty != "" && !difficulties[g.Difficulty] {
		return g, errInvalid("Difficulty must be peaceful, easy, normal or hard.")
	}
	if g.GameMode != "" && !gameModes[g.GameMode] {
		return g, errInvalid("The game mode must be survival, creative, adventure or spectator.")
	}
	if g.ViewDistance != 0 && (g.ViewDistance < minViewDistance || g.ViewDistance > maxViewDistance) {
		return g, errInvalid("View distance must be between %d and %d chunks.", minViewDistance, maxViewDistance)
	}
	if !creating && (g.LevelType != "" || g.Hardcore != nil) {
		return g, errInvalid("The world type and hardcore are chosen when a world is created.")
	}
	if _, ok := levelTypes[g.LevelType]; g.LevelType != "" && !ok {
		return g, errInvalid("The world type must be normal, flat, amplified or large_biomes.")
	}
	return g, nil
}

// mergeGameplay applies the set fields of change to cur.
func mergeGameplay(cur, change api.Gameplay) api.Gameplay {
	if change.Difficulty != "" {
		cur.Difficulty = change.Difficulty
	}
	if change.PVP != nil {
		v := *change.PVP
		cur.PVP = &v
	}
	if change.GameMode != "" {
		cur.GameMode = change.GameMode
	}
	if change.ViewDistance != 0 {
		cur.ViewDistance = change.ViewDistance
	}
	return cur
}

// gameplayEnv is the image's environment for the settings chosen in
// Playkeeper. Unset settings add nothing, so a server's definition only
// changes when one of them is set.
func gameplayEnv(g api.Gameplay) []string {
	var env []string
	if g.Difficulty != "" {
		env = append(env, "DIFFICULTY="+g.Difficulty)
	}
	if g.PVP != nil {
		env = append(env, "PVP="+strconv.FormatBool(*g.PVP))
	}
	if g.GameMode != "" {
		env = append(env, "MODE="+g.GameMode)
	}
	if g.Hardcore != nil {
		env = append(env, "HARDCORE="+strconv.FormatBool(*g.Hardcore))
	}
	if g.ViewDistance != 0 {
		env = append(env, "VIEW_DISTANCE="+strconv.Itoa(g.ViewDistance))
	}
	if t, ok := levelTypes[g.LevelType]; ok {
		env = append(env, "LEVEL_TYPE="+t)
	}
	return env
}

// readProperties reads server.properties (key=value lines).
func readProperties(dataDir string) map[string]string {
	d, err := gamefiles.Open(dataDir, nil)
	if err != nil {
		return nil
	}
	defer d.Close()
	b, err := d.ReadProperties()
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

// mergeProperties sets keys in a server.properties file and keeps its other
// lines as they are. Keys it doesn't have yet are added at the end, sorted.
func mergeProperties(cur []byte, set map[string]string) []byte {
	var b bytes.Buffer
	done := map[string]bool{}
	for line := range strings.SplitAfterSeq(string(cur), "\n") {
		k := propertyKey(line)
		if _, ok := set[k]; ok {
			// Java keeps a key's last line; Playkeeper writes the key once.
			if !done[k] {
				fmt.Fprintf(&b, "%s=%s\n", k, escapeProperty(set[k]))
				done[k] = true
			}
			continue
		}
		b.WriteString(line)
		if line != "" && !strings.HasSuffix(line, "\n") {
			b.WriteByte('\n')
		}
	}
	for _, k := range slices.Sorted(maps.Keys(set)) {
		if !done[k] {
			fmt.Fprintf(&b, "%s=%s\n", k, escapeProperty(set[k]))
		}
	}
	return b.Bytes()
}

// propertyKey is the key of a server.properties line; "" for comments and
// blank lines.
func propertyKey(line string) string {
	l := strings.TrimLeft(line, " \t\f")
	if l == "" || l[0] == '#' || l[0] == '!' {
		return ""
	}
	if i := strings.IndexAny(l, "=: \t\f\r\n"); i > 0 {
		return l[:i]
	}
	return strings.TrimRight(l, "\r\n")
}

// escapeProperty writes a value the way Java's Properties.store does, so
// Minecraft reads back exactly the value: separators, comment marks and a
// leading space are escaped, and anything outside printable ASCII becomes
// \uXXXX.
func escapeProperty(v string) string {
	var b strings.Builder
	for i, r := range v {
		switch {
		case r == ' ' && i == 0:
			b.WriteString(`\ `)
		case strings.ContainsRune(`\=:#!`, r):
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r > 0x7e:
			for _, u := range utf16.Encode([]rune{r}) {
				fmt.Fprintf(&b, `\u%04X`, u)
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// effectiveGameplay is what the server runs with: the settings chosen in
// Playkeeper, else server.properties, else Minecraft's defaults.
func effectiveGameplay(chosen api.Gameplay, props map[string]string) api.Gameplay {
	g := api.Gameplay{Difficulty: "easy", GameMode: "survival", ViewDistance: 10, LevelType: "normal"}
	t, f := true, false
	g.PVP, g.Hardcore = &t, &f
	if v := props["difficulty"]; v != "" {
		if d, ok := map[string]string{"0": "peaceful", "1": "easy", "2": "normal", "3": "hard"}[v]; ok {
			v = d
		}
		if difficulties[v] {
			g.Difficulty = v
		}
	}
	if v := props["gamemode"]; v != "" {
		if m, ok := map[string]string{"0": "survival", "1": "creative", "2": "adventure", "3": "spectator"}[v]; ok {
			v = m
		}
		if gameModes[v] {
			g.GameMode = v
		}
	}
	if v, err := strconv.ParseBool(props["pvp"]); err == nil {
		g.PVP = &v
	}
	if v, err := strconv.ParseBool(props["hardcore"]); err == nil {
		g.Hardcore = &v
	}
	if v, err := strconv.Atoi(props["view-distance"]); err == nil && v >= minViewDistance && v <= maxViewDistance {
		g.ViewDistance = v
	}
	if v := strings.TrimPrefix(strings.ToLower(props["level-type"]), "minecraft:"); v != "" {
		if _, ok := levelTypes[v]; ok {
			g.LevelType = v
		}
	}
	if chosen.Difficulty != "" {
		g.Difficulty = chosen.Difficulty
	}
	if chosen.PVP != nil {
		g.PVP = chosen.PVP
	}
	if chosen.GameMode != "" {
		g.GameMode = chosen.GameMode
	}
	if chosen.Hardcore != nil {
		g.Hardcore = chosen.Hardcore
	}
	if chosen.ViewDistance != 0 {
		g.ViewDistance = chosen.ViewDistance
	}
	if chosen.LevelType != "" {
		g.LevelType = chosen.LevelType
	}
	return g
}

// gameplayChanges describes the settings a request changed, for the audit log.
func gameplayChanges(before, after, asked api.Gameplay) []string {
	var out []string
	if asked.Difficulty != "" && before.Difficulty != after.Difficulty {
		out = append(out, fmt.Sprintf("difficulty %s→%s", before.Difficulty, after.Difficulty))
	}
	if asked.PVP != nil && *before.PVP != *after.PVP {
		out = append(out, fmt.Sprintf("pvp %v→%v", *before.PVP, *after.PVP))
	}
	if asked.GameMode != "" && before.GameMode != after.GameMode {
		out = append(out, fmt.Sprintf("gameMode %s→%s", before.GameMode, after.GameMode))
	}
	if asked.ViewDistance != 0 && before.ViewDistance != after.ViewDistance {
		out = append(out, fmt.Sprintf("viewDistance %d→%d", before.ViewDistance, after.ViewDistance))
	}
	return out
}

// Server icon: a 64×64 PNG the Minecraft server list shows.

const (
	maxIconBytes = 64 << 10
	iconFile     = "server-icon.png"
)

// iconNewer reports whether the icon changed after the running server
// started, so a restart is needed to show it.
func iconNewer(sc *api.ServerConfig, startedAt *time.Time) bool {
	return sc.IconUpdatedAt != nil && startedAt != nil && sc.IconUpdatedAt.After(*startedAt)
}

func (s *server) hIcon(w http.ResponseWriter, r *http.Request) {
	b, err := s.readIcon()
	if err != nil {
		writeError(w, errNotFound("Server icon"))
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

// readIcon reads the server's icon from its files, which the game can change.
func (s *server) readIcon() ([]byte, error) {
	d, err := s.gameFiles()
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return d.ReadFile(iconFile, maxIconBytes)
}

func (s *server) hIconSet(w http.ResponseWriter, r *http.Request) {
	actor := actorFromHeader(r)
	if actor == "unknown" {
		writeError(w, errInvalid("X-Playkeeper-Actor header is required"))
		return
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, maxIconBytes+1))
	if err != nil {
		writeError(w, errInvalid("Upload failed: %v", err))
		return
	}
	if len(b) > maxIconBytes {
		writeError(w, errIcon("This one is larger."))
		return
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || format != "png" {
		writeError(w, errIcon("This one is not a PNG."))
		return
	}
	if cfg.Width != 64 || cfg.Height != 64 {
		writeError(w, errIcon("This one is %d × %d.", cfg.Width, cfg.Height))
		return
	}
	if _, err := png.Decode(bytes.NewReader(b)); err != nil {
		writeError(w, errIcon("This one could not be read."))
		return
	}
	if err := s.saveIcon(b); err != nil {
		writeError(w, err)
		return
	}
	s.audit(actor, "settings.changed", "server", "succeeded", "server icon")
	writeJSON(w, http.StatusOK, s.Status(r.Context()))
}

// errIcon refuses an upload before anything is written, so every icon saved
// can be read back.
func errIcon(format string, args ...any) *apiError {
	return &apiError{Status: http.StatusBadRequest, Code: api.CodeIconInvalid, Msg: "Icons need to be 64 × 64 PNG pictures of at most 64 KB. " + fmt.Sprintf(format, args...)}
}

// saveIcon installs the icon and records when. It holds the server's
// operation lock, like a settings change, so no operation rewrites the
// settings or moves the server's files meanwhile.
func (s *server) saveIcon(b []byte) error {
	release, ok := s.holdOpLock()
	if !ok {
		return s.busyError()
	}
	defer release()
	sc, err := s.serverConfig()
	if err != nil {
		return err
	}
	if sc == nil {
		return errNotCreated()
	}
	if err := s.ensureDirs("upload the icon again"); err != nil {
		return err
	}
	d, err := s.gameFiles()
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.WriteFile(iconFile, b, 0o640); err != nil {
		return gameFileError(err, "The server icon could not be saved.")
	}
	now := s.now().UTC()
	sc.IconUpdatedAt = &now
	return s.saveServerConfig(*sc)
}

package pregen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/gamefiles"
)

// Config is the part of Chunky's config Playkeeper manages.
type Config struct {
	// ContinueOnRestart makes Chunky resume unfinished tasks when the
	// server starts.
	ContinueOnRestart bool
	// UpdateInterval is how often Chunky logs progress, in seconds; 0 keeps
	// the current value.
	UpdateInterval int
}

// ConfigPath is Chunky's config file on p, relative to the data directory.
func ConfigPath(p Platform) string {
	if p == Bukkit {
		return "plugins/Chunky/config.yml"
	}
	return "config/chunky/config.json"
}

// TaskDir is where Chunky on p saves tasks, relative to the data directory.
func TaskDir(p Platform) string {
	if p == Bukkit {
		return "plugins/Chunky/tasks"
	}
	return "config/chunky/tasks"
}

const (
	maxConfigBytes = 256 << 10
	maxTaskBytes   = 64 << 10
)

// WriteConfig sets cfg in Chunky's config file under dataDir, keeping the
// rest of the file, and switches Chunky to English so its messages can be
// read. Chunky reads the file when it starts and on "chunky reload"
// (Controller.Configure). The data directory is writable by the server, so
// the file is read and replaced through internal/gamefiles; what it made is
// given to owner.
func WriteConfig(dataDir string, p Platform, cfg Config, owner *gamefiles.Owner) error {
	if !p.valid() {
		return fmt.Errorf("unknown platform %q", p)
	}
	if cfg.UpdateInterval < 0 || cfg.UpdateInterval > 3600 {
		return fmt.Errorf("update interval %d is out of range (0 to 3600 seconds)", cfg.UpdateInterval)
	}
	rel := ConfigPath(p)
	fail := func(err error) error {
		if gamefiles.KindOf(err) != "" {
			return refusal(err, "Playkeeper could not update Chunky's settings.")
		}
		return &Error{
			Code:   CodeConfig,
			Params: map[string]any{"file": rel},
			Msg:    fmt.Sprintf("Playkeeper could not update Chunky's settings in %s: %v.", rel, err),
			Hint:   "Fix or delete the file (Chunky recreates it with defaults), then try again.",
			Err:    err,
		}
	}
	files, err := gamefiles.Open(dataDir, owner)
	if err != nil {
		return fail(err)
	}
	defer files.Close()
	old, err := files.ReadFile(rel, maxConfigBytes)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fail(err)
	}
	var out []byte
	if p == Bukkit {
		out, err = editYAML(old, cfg)
	} else {
		out, err = editJSON(old, cfg)
	}
	if err != nil {
		return fail(err)
	}
	if err := files.WriteFile(rel, out, 0o644); err != nil {
		return fail(err)
	}
	return nil
}

// editYAML sets Playkeeper's keys in Chunky's Bukkit config.yml, a flat
// list of "key: value" lines, leaving other lines as they are.
func editYAML(old []byte, cfg Config) ([]byte, error) {
	if old == nil {
		old = []byte("version: 2\nlanguage: en\ncontinue-on-restart: false\nforce-load-existing-chunks: false\nsilent: false\nupdate-interval: 1\n")
	}
	if !utf8.Valid(old) {
		return nil, errors.New("it is not UTF-8 text")
	}
	set := map[string]string{"language": "en", "continue-on-restart": strconv.FormatBool(cfg.ContinueOnRestart)}
	if cfg.UpdateInterval > 0 {
		set["update-interval"] = strconv.Itoa(cfg.UpdateInterval)
	}
	done := map[string]bool{}
	var b strings.Builder
	for _, line := range strings.SplitAfter(string(old), "\n") {
		key, _, ok := strings.Cut(line, ":")
		if v, managed := set[key]; ok && managed {
			b.WriteString(key + ": " + v + "\n")
			done[key] = true
			continue
		}
		b.WriteString(line)
	}
	if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
		b.WriteString("\n")
	}
	for _, key := range []string{"language", "continue-on-restart", "update-interval"} {
		if v, ok := set[key]; ok && !done[key] {
			b.WriteString(key + ": " + v + "\n")
		}
	}
	return []byte(b.String()), nil
}

// editJSON sets Playkeeper's keys in the config.json Chunky's Fabric and
// NeoForge builds read, keeping unknown keys (such as saved tasks).
func editJSON(old []byte, cfg Config) ([]byte, error) {
	m := map[string]json.RawMessage{}
	if old != nil && len(bytes.TrimSpace(old)) > 0 {
		if !json.Valid(old) {
			return nil, errors.New("it is not valid JSON")
		}
		if err := json.Unmarshal(old, &m); err != nil || m == nil {
			return nil, errors.New("it does not hold a JSON object")
		}
	} else {
		m = map[string]json.RawMessage{
			"version": json.RawMessage("2"), "forceLoadExistingChunks": json.RawMessage("false"),
			"silent": json.RawMessage("false"), "updateInterval": json.RawMessage("1"),
		}
	}
	m["language"] = json.RawMessage(`"en"`)
	m["continueOnRestart"] = json.RawMessage(strconv.FormatBool(cfg.ContinueOnRestart))
	if cfg.UpdateInterval > 0 {
		m["updateInterval"] = json.RawMessage(strconv.Itoa(cfg.UpdateInterval))
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// Task is a task Chunky saved: when it was paused, cancelled or finished,
// or when the server stopped. A running task's file can be older than its
// progress.
type Task struct {
	World string `json:"world"`
	// Cancelled is also true for a finished task; see Finished.
	Cancelled bool    `json:"cancelled"`
	CenterX   float64 `json:"centerX"`
	CenterZ   float64 `json:"centerZ"`
	Radius    float64 `json:"radius"`
	Shape     string  `json:"shape"`
	Pattern   string  `json:"pattern"`
	// Chunks counts the chunks processed before the task was saved.
	Chunks         int64 `json:"chunks"`
	ElapsedSeconds int64 `json:"elapsedSeconds"`
	// Total is the count Chunky's percentage is out of, or 0 for shapes and
	// patterns Playkeeper doesn't start.
	Total int64 `json:"total"`
}

// Finished reports whether the task processed its whole area.
func (t Task) Finished() bool { return t.Cancelled && t.Total > 0 && t.Chunks >= t.Total }

// Percent is how far the task got, or 0 when Total is unknown.
func (t Task) Percent() float64 {
	if t.Total <= 0 {
		return 0
	}
	return min(100, 100*float64(t.Chunks)/float64(t.Total))
}

// ReadTask reads the task Chunky saved for world, reporting false when
// there is none.
func ReadTask(dataDir string, p Platform, world string) (Task, bool, error) {
	if err := p.CheckWorld(world); err != nil {
		return Task{}, false, err
	}
	files, err := gamefiles.Open(dataDir, nil)
	if err != nil {
		return Task{}, false, err
	}
	defer files.Close()
	rel := TaskDir(p) + "/" + strings.ReplaceAll(world, ":", "/") + ".properties"
	b, err := files.ReadFile(rel, maxTaskBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return Task{}, false, nil
	}
	if gamefiles.KindOf(err) != "" {
		return Task{}, false, refusal(err, "Playkeeper could not read Chunky's saved task.")
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("cannot read Chunky's saved task %s: %w", rel, err)
	}
	props := parseProperties(string(b))
	if props["world"] != world {
		return Task{}, false, nil
	}
	t := Task{
		World:     world,
		Cancelled: props["cancelled"] == "true",
		CenterX:   atofStrict(props["center-x"]),
		CenterZ:   atofStrict(props["center-z"]),
		Radius:    atofStrict(props["radius"]),
		Shape:     props["shape"],
		Pattern:   props["pattern"],
		Chunks:    max(atoi64(props["chunks"]), 0),
	}
	if t.Shape == "" {
		t.Shape = string(Square)
	}
	if t.Pattern == "" {
		t.Pattern = "region"
	}
	t.ElapsedSeconds = max(atoi64(props["time"]), 0) / 1000
	if (t.Shape == string(Square) || t.Shape == string(Circle)) && t.Pattern != "world" && t.Pattern != "csv" && t.Radius >= 0 && t.Radius <= 3e7 {
		side := 2*int64(math.Ceil(t.Radius/16)) + 1
		t.Total = side * side
	}
	return t, true, nil
}

// parseProperties reads the key=value lines Chunky writes.
func parseProperties(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == '!' {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

func atofStrict(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

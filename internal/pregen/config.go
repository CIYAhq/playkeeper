package pregen

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"
)

// Owner is the user and group that own the files Playkeeper writes for the
// server, so the server, which runs as that user, can still change them.
type Owner struct {
	UID int
	GID int
}

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
// every path is resolved inside it and files are replaced, never written in
// place.
func WriteConfig(dataDir string, p Platform, cfg Config, owner *Owner) error {
	if !p.valid() {
		return fmt.Errorf("unknown platform %q", p)
	}
	if cfg.UpdateInterval < 0 || cfg.UpdateInterval > 3600 {
		return fmt.Errorf("update interval %d is out of range (0 to 3600 seconds)", cfg.UpdateInterval)
	}
	rel := ConfigPath(p)
	fail := func(err error) error {
		return &Error{
			Code:   CodeConfig,
			Params: map[string]any{"file": rel},
			Msg:    fmt.Sprintf("Playkeeper could not update Chunky's settings in %s: %v.", rel, err),
			Hint:   "Fix or delete the file (Chunky recreates it with defaults), then try again.",
			Err:    err,
		}
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return fail(err)
	}
	defer root.Close()
	if err := mkdirAll(root, path.Dir(rel), owner); err != nil {
		return fail(err)
	}
	old, err := readRegular(root, rel, maxConfigBytes)
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
	if err := writeFileAtomic(root, rel, out, owner); err != nil {
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
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return Task{}, false, err
	}
	defer root.Close()
	rel := TaskDir(p) + "/" + strings.ReplaceAll(world, ":", "/") + ".properties"
	b, err := readRegular(root, rel, maxTaskBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return Task{}, false, nil
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

// readRegular reads a regular file of at most limit bytes inside root.
// Opening without blocking and checking the type afterwards keeps a FIFO or
// device planted by the server from hanging or misleading the reader.
func readRegular(root *os.Root, name string, limit int64) ([]byte, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("it is not a regular file")
	}
	if st.Size() > limit {
		return nil, fmt.Errorf("it is larger than %d bytes", limit)
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("it is larger than %d bytes", limit)
	}
	return b, nil
}

// mkdirAll creates dir and its missing parents inside root, giving new
// directories to owner.
func mkdirAll(root *os.Root, dir string, owner *Owner) error {
	if dir == "." || dir == "" {
		return nil
	}
	cur := ""
	for _, part := range strings.Split(dir, "/") {
		cur = path.Join(cur, part)
		err := root.Mkdir(cur, 0o755)
		switch {
		case err == nil:
			if owner != nil {
				if err := root.Lchown(cur, owner.UID, owner.GID); err != nil {
					return err
				}
			}
		case errors.Is(err, fs.ErrExist):
			st, err := root.Stat(cur)
			if err != nil {
				return err
			}
			if !st.IsDir() {
				return fmt.Errorf("%s is not a directory", cur)
			}
		default:
			return err
		}
	}
	return nil
}

// writeFileAtomic replaces name inside root with data: it writes a new file
// next to it, gives it to owner, syncs it and renames it over the old one.
func writeFileAtomic(root *os.Root, name string, data []byte, owner *Owner) error {
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	tmp := path.Join(path.Dir(name), "."+path.Base(name)+".playkeeper-"+hex.EncodeToString(rnd[:]))
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			root.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if owner != nil {
		if err := f.Chown(owner.UID, owner.GID); err != nil {
			return err
		}
	}
	if err := f.Chmod(0o644); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := root.Rename(tmp, name); err != nil {
		return err
	}
	ok = true
	return nil
}

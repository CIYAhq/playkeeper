package webmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"syscall"
	"time"
)

// State is where a server's map stands.
type State string

const (
	StateUnsupported  State = "unsupported"
	StateNotInstalled State = "not_installed"
	StateStopped      State = "server_stopped"
	StateNeedsRestart State = "needs_restart"
	StateNotAnswering State = "not_answering"
	StateDrawing      State = "drawing"
	StateReady        State = "ready"
)

// Check is what the caller knows about the server when it asks how the map
// is doing.
type Check struct {
	// Installed: the add-on library has squaremap on the server.
	Installed bool
	// Running: the server has finished starting.
	Running bool
	// PendingRestart: squaremap was installed, or its settings changed,
	// after the server started.
	PendingRestart bool
}

// Status is how a server's map is doing: State, Params and English words
// for the map tab, and what is drawn so far.
type Status struct {
	State  State             `json:"state"`
	Params map[string]string `json:"params,omitempty"`
	Msg    string            `json:"message"`
	Hint   string            `json:"hint,omitempty"`
	// Areas counts the 512×512-block areas drawn in all worlds; Bytes is
	// the disk all tiles use.
	Areas int   `json:"areas"`
	Bytes int64 `json:"bytes"`
	// LastDrawn is when squaremap last wrote a detailed tile.
	LastDrawn *time.Time `json:"lastDrawn,omitempty"`
	// Progress is set while a full render runs, in areas.
	Progress  *Progress     `json:"progress,omitempty"`
	Worlds    []WorldStatus `json:"worlds,omitempty"`
	CheckedAt time.Time     `json:"checkedAt"`
}

// WorldStatus is what is drawn of one world.
type WorldStatus struct {
	Name      string     `json:"name"`
	Dimension Dimension  `json:"dimension"`
	Areas     int        `json:"areas"`
	LastDrawn *time.Time `json:"lastDrawn,omitempty"`
	Progress  *Progress  `json:"progress,omitempty"`
}

// Progress counts the areas a full render has drawn and has to draw.
type Progress struct {
	Done    int `json:"done"`
	Total   int `json:"total"`
	Percent int `json:"percent"`
}

// Bounds on reading squaremap's folder, which the game can write.
const (
	maxScanEntries   = 1_000_000
	maxProgressBytes = 8 << 20
)

// Status tells how the server's map is doing: not set up, waiting for the
// server, not answering, drawing (with progress while a full render runs)
// or ready. It reads squaremap's folder in the server's files and asks
// squaremap for its list of worlds. It does not fail; what it cannot read
// counts as not drawn.
func (m Map) Status(ctx context.Context, c Check) Status {
	s := Status{CheckedAt: m.now()}
	l, err := LayoutFor(m.Type)
	if err != nil {
		var e *Error
		errors.As(err, &e)
		return s.set(StateUnsupported, e.Params, e.Msg, e.Hint)
	}
	if !c.Installed {
		return s.set(StateNotInstalled, kv("minutes", strconv.Itoa(EstimatedMinutes), "megabytes", strconv.Itoa(EstimatedMegabytes)),
			"The map is not set up yet.",
			fmt.Sprintf("Turn on the map to install squaremap. Drawing the land explored so far takes about %d minutes and uses about %d MB of disk.", EstimatedMinutes, EstimatedMegabytes))
	}
	m.scan(l, &s)
	areas := strconv.Itoa(s.Areas)
	switch {
	case !c.Running:
		return s.set(StateStopped, kv("areas", areas), "The map shows while the server is running.", "Start the server to see it.")
	case c.PendingRestart:
		return s.set(StateNeedsRestart, nil, "The map starts when the server restarts.", "Restart the server to turn it on. It is down for about 20 seconds.")
	}
	pctx, cancel := context.WithTimeout(ctx, m.timeout())
	defer cancel()
	if _, err := m.worldList(pctx); err != nil && KindOf(err) != KindStarting {
		var e *Error
		if !errors.As(err, &e) {
			e = unreachable(pctx, err)
		}
		return s.set(StateNotAnswering, kv("reason", string(e.Kind)), "The map is not answering.",
			"squaremap runs inside the server. Restart the server; if the map still does not answer, look for squaremap errors in the console.")
	}
	if p := s.Progress; p != nil {
		return s.set(StateDrawing, kv("done", strconv.Itoa(p.Done), "total", strconv.Itoa(p.Total), "percent", strconv.Itoa(p.Percent)),
			fmt.Sprintf("Drawing the map: %s of %s areas (%d%%).", thousands(p.Done), thousands(p.Total), p.Percent),
			"New land appears as it is drawn, and players can keep playing.")
	}
	if s.Areas == 0 {
		return s.set(StateDrawing, nil, "Drawing the map for the first time.", "The first areas appear in a minute or two.")
	}
	return s.set(StateReady, kv("areas", areas), "The map is up to date: "+thousands(s.Areas)+" areas drawn.",
		"New land appears a few minutes after someone explores it.")
}

func (s Status) set(st State, params map[string]string, msg, hint string) Status {
	s.State, s.Params, s.Msg, s.Hint = st, params, msg, hint
	return s
}

// scan counts squaremap's tiles in each world folder (web/tiles/<world>/
// <zoom>/<x>_<z>.png) and reads the progress of a running full render
// (data/<world>/resume_render.json).
func (m Map) scan(l Layout, s *Status) {
	sq, err := openFolder(m.Dir, l.Dir, false, nil)
	if err != nil || sq == nil {
		return
	}
	defer sq.Close()
	tiles, err := openIn(sq, "web/tiles", false, nil)
	if err != nil || tiles == nil {
		return
	}
	defer tiles.Close()
	budget := maxScanEntries
	var total Progress
	for _, e := range readDir(tiles, ".", &budget) {
		if !e.IsDir() || !validWorld(e.Name()) {
			continue
		}
		w := WorldStatus{Name: e.Name(), Dimension: dimensionOfName(e.Name())}
		m.scanWorld(tiles, &w, s, &budget)
		if p := readProgress(sq, w.Name); p != nil {
			w.Progress = p
			total.Done += p.Done
			total.Total += p.Total
		}
		s.Areas += w.Areas
		if w.LastDrawn != nil && (s.LastDrawn == nil || w.LastDrawn.After(*s.LastDrawn)) {
			s.LastDrawn = w.LastDrawn
		}
		s.Worlds = append(s.Worlds, w)
	}
	if total.Total > 0 {
		total.Percent = total.Done * 100 / total.Total
		s.Progress = &total
	}
}

func (m Map) scanWorld(tiles *os.Root, w *WorldStatus, s *Status, budget *int) {
	wr, err := openIn(tiles, w.Name, false, nil)
	if err != nil || wr == nil {
		return
	}
	defer wr.Close()
	var newest time.Time
	for z := 0; z <= ZoomMax; z++ {
		for _, e := range readDir(wr, strconv.Itoa(z), budget) {
			if !e.Type().IsRegular() || !reTileFile.MatchString(e.Name()) {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			s.Bytes += fi.Size()
			if z == ZoomMax {
				w.Areas++
				if t := fi.ModTime(); t.After(newest) {
					newest = t
				}
			}
		}
	}
	if !newest.IsZero() {
		t := newest.UTC()
		w.LastDrawn = &t
	}
}

var errLink = errors.New("it is a link")

// openNoFollow opens name in r for reading and tells what it is. The game
// can write in r, so a link is refused and a named pipe cannot block the
// open.
func openNoFollow(r *os.Root, name string) (*os.File, fs.FileInfo, error) {
	if fi, err := r.Lstat(name); err != nil {
		return nil, nil, err
	} else if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, nil, errLink
	}
	f, err := r.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, fi, nil
}

// readDir lists a folder in r that is not a link, taking entries from
// budget.
func readDir(r *os.Root, name string, budget *int) []fs.DirEntry {
	f, fi, err := openNoFollow(r, name)
	if err != nil {
		return nil
	}
	defer f.Close()
	if !fi.IsDir() {
		return nil
	}
	var out []fs.DirEntry
	for *budget > 0 {
		batch, err := f.ReadDir(min(*budget, 1024))
		*budget -= len(batch)
		out = append(out, batch...)
		if err != nil {
			break
		}
	}
	return out
}

func readProgress(sq *os.Root, world string) *Progress {
	f, fi, err := openNoFollow(sq, "data/"+world+"/resume_render.json")
	if err != nil {
		return nil
	}
	defer f.Close()
	if !fi.Mode().IsRegular() || fi.Size() > maxProgressBytes {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(f, maxProgressBytes+1))
	if err != nil || len(b) > maxProgressBytes {
		return nil
	}
	return parseProgress(b)
}

// parseProgress reads resume_render.json: every region of a running full
// render and whether it is drawn, as Gson writes a map with object keys
// ([[{"x":0,"z":0},true],…]). squaremap deletes the file when the render
// ends; an empty map is {}.
func parseProgress(b []byte) *Progress {
	var pairs [][]json.RawMessage
	if err := json.Unmarshal(b, &pairs); err != nil {
		return nil
	}
	var p Progress
	for _, kv := range pairs {
		var drawn bool
		if len(kv) != 2 || json.Unmarshal(kv[1], &drawn) != nil {
			return nil
		}
		p.Total++
		if drawn {
			p.Done++
		}
	}
	if p.Total == 0 {
		return nil
	}
	p.Percent = p.Done * 100 / p.Total
	return &p
}

// dimensionOfName guesses a world's dimension from squaremap's name for it,
// for folders on disk; squaremap names worlds by dimension id.
func dimensionOfName(name string) Dimension {
	switch name {
	case "minecraft_overworld":
		return Overworld
	case "minecraft_the_nether":
		return Nether
	case "minecraft_the_end":
		return End
	}
	return Custom
}

// thousands writes n with commas: 3,610.
func thousands(n int) string {
	if n < 0 {
		return "-" + thousands(-n)
	}
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

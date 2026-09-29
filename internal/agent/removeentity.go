package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/gamefiles"
	"github.com/CIYAhq/playkeeper/internal/region"
)

const (
	// maxRegionFile caps a region file as read; one holds 1,024 chunks of
	// at most 1 MB each.
	maxRegionFile = 1 << 30
	// maxWorldXZ is where Minecraft's world ends.
	maxWorldXZ = 30_000_000
	maxWorldY  = 4096
)

var (
	reResourceID = regexp.MustCompile(`^[a-z0-9_.-]{1,64}:[a-z0-9_./-]{1,200}$`)
	reLevelName  = regexp.MustCompile(`^[\w.+ -]{1,64}$`)
)

// entityFix is where the entity or block entity to take out is saved.
type entityFix struct {
	req    api.RemoveEntityRequest
	file   string // relative to the data directory
	cx, cz int
}

// label names what is taken out, for the backup's note and the audit.
func (f *entityFix) label() string {
	_, what, _ := strings.Cut(f.req.Type, ":")
	return fmt.Sprintf("%s at %d, %d, %d", what, f.req.X, f.req.Y, f.req.Z)
}

func validEntityRequest(req api.RemoveEntityRequest) error {
	switch {
	case req.What != "entity" && req.What != "block_entity":
		return errInvalid("Say whether an entity or a block's data is to be removed.")
	case !reResourceID.MatchString(req.Type) || !reResourceID.MatchString(req.Dimension):
		return errInvalid("That isn't the id of an entity or a dimension.")
	case req.Level != "" && !reLevelName.MatchString(req.Level):
		return errInvalid("That isn't the name of a world.")
	case abs(req.X) > maxWorldXZ || abs(req.Z) > maxWorldXZ || abs(req.Y) > maxWorldY:
		return errInvalid("That place is outside the world.")
	case len(req.Pos) != 0 && (len(req.Pos) != 3 || req.What != "entity"):
		return errInvalid("An entity's exact position has three numbers.")
	}
	_, path, _ := strings.Cut(req.Dimension, ":")
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return errInvalid("That isn't the id of a dimension.")
		}
	}
	// A crash report rounds the exact position to two decimals, so one at a
	// block's edge can print as the next block's.
	for i, v := range req.Pos {
		b := float64([3]int{req.X, req.Y, req.Z}[i])
		if math.IsNaN(v) || v < b-0.01 || v > b+1.01 {
			return errInvalid("The exact position isn't inside the block given.")
		}
	}
	return nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// dimensionFolders are the folders a dimension's files may be in, in the
// order they're tried: Minecraft 26.1 keeps every dimension under
// dimensions/; before, the Nether and the End were DIM-1 and DIM1 in the
// world folder. Paper and Purpur gave them, and a data pack's dimension,
// folders of their own, which they use over the world folder's (own); other
// types never read those, so what's there isn't their world.
func dimensionFolders(level, dimension string, own bool) []string {
	ns, path, _ := strings.Cut(dimension, ":")
	out := []string{level + "/dimensions/" + ns + "/" + path}
	var paper, world string
	switch dimension {
	case "minecraft:overworld":
		world = level
	case "minecraft:the_nether":
		paper, world = paperWorld(level, dimension)+"/DIM-1", level+"/DIM-1"
	case "minecraft:the_end":
		paper, world = paperWorld(level, dimension)+"/DIM1", level+"/DIM1"
	default:
		paper = paperWorld(level, dimension) + "/dimensions/" + ns + "/" + path
	}
	if own && paper != "" {
		out = append(out, paper)
	}
	if world != "" {
		out = append(out, world)
	}
	return out
}

// paperWorld is the world Paper and Purpur keep a dimension in, which names
// its folder too: the Nether, the End and a data pack's dimension each get
// one of their own, named after the level.
func paperWorld(level, dimension string) string {
	switch dimension {
	case "minecraft:overworld":
		return level
	case "minecraft:the_nether":
		return level + "_nether"
	case "minecraft:the_end":
		return level + "_the_end"
	}
	ns, path, _ := strings.Cut(dimension, ":")
	return level + "_" + ns + "_" + strings.ReplaceAll(path, "/", "_")
}

// planEntityFix finds the region file that saves what req names and checks
// that it's there, so nothing is backed up or changed when it isn't.
func (s *server) planEntityFix(sc api.ServerConfig, req api.RemoveEntityRequest) (*entityFix, error) {
	d, err := s.gameFiles()
	if err != nil {
		return nil, err
	}
	defer d.Close()
	f := &entityFix{req: req, cx: req.X >> 4, cz: req.Z >> 4}
	level, own := s.levelName(sc), takesPlugins(sc)
	// Paper and Purpur name each world as its folder, and the crash report
	// gives that name: one that isn't the world of this dimension is a world
	// a plugin made. Other types have one world, whose level.dat may still
	// carry the name it had before it was imported.
	if own && req.Level != "" && req.Level != level && req.Level != paperWorld(level, req.Dimension) {
		return nil, errConflict(fmt.Sprintf("The %s is in %s, a world a plugin made, and Playkeeper doesn't know where that world is saved, so it changed nothing.", f.label(), req.Level), "Restore a backup instead.")
	}
	kind := "entities"
	if req.What == "block_entity" {
		kind = "region"
	}
	name := fmt.Sprintf("%s/r.%d.%d.mca", kind, f.cx>>5, f.cz>>5)
	var looked []string
	for _, dir := range dimensionFolders(level, req.Dimension, own) {
		file := dir + "/" + name
		if fi, err := d.Lstat(file); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		f.file = file
		// A file that can't be read ends the search: it may be the one that
		// saves it, and the next is at most an old copy.
		if _, found, err := f.apply(d, s.now()); err != nil {
			return nil, err
		} else if found {
			return f, nil
		}
		looked = append(looked, file)
	}
	if len(looked) == 0 {
		return nil, errConflict(fmt.Sprintf("Playkeeper found no %s file for the %s in %s, so there's nothing to remove.", kind, f.label(), s.name()), "Start the server.")
	}
	return nil, f.gone(looked...)
}

// gone refuses a fix whose entity or block entity isn't in the files.
func (f *entityFix) gone(files ...string) error {
	return errConflict(fmt.Sprintf("The %s isn't in %s any more, so there's nothing to remove.", f.label(), strings.Join(files, " or ")), "Start the server.")
}

// apply reads the region file and returns it with the entity or block
// entity taken out; found is false when it isn't in its chunk.
func (f *entityFix) apply(d *gamefiles.Dir, now time.Time) (b []byte, found bool, err error) {
	if b, err = d.ReadFile(f.file, maxRegionFile); err != nil {
		return nil, false, gameFileError(err, "Playkeeper couldn't read "+f.file+".")
	}
	rf, err := region.Parse(b)
	if err != nil {
		return nil, false, errConflict(fmt.Sprintf("%s can't be read (%v), so Playkeeper changed nothing.", f.file, err), "Restore a backup instead.")
	}
	compression, data, err := rf.Chunk(f.cx, f.cz)
	if errors.Is(err, region.ErrNoChunk) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, errConflict(fmt.Sprintf("Chunk %d, %d of %s can't be read (%v), so Playkeeper changed nothing.", f.cx, f.cz, f.file, err), "Restore a backup instead.")
	}
	chunk, err := region.Decode(data)
	if err != nil {
		return nil, false, errConflict(fmt.Sprintf("Chunk %d, %d of %s is damaged (%v), so Playkeeper changed nothing.", f.cx, f.cz, f.file, err), "Restore a backup instead.")
	}
	r := f.req
	if r.What == "entity" && region.RemoveEntity(chunk, r.Type, r.X, r.Y, r.Z, r.Pos) == 0 ||
		r.What == "block_entity" && !region.RemoveBlockEntity(chunk, r.Type, r.X, r.Y, r.Z) {
		return nil, false, nil
	}
	if data, err = region.Encode(chunk); err != nil {
		return nil, false, err
	}
	if err := rf.SetChunk(f.cx, f.cz, compression, data, now); err != nil {
		return nil, false, err
	}
	return rf.Bytes(), true, nil
}

// hRemoveEntity takes one entity, or one block entity's data, out of a
// stopped server's world: the crash helper's fix for a ticking entity, which
// works whatever the loader. The world is backed up first, and only that one
// goes; whatever rode an entity stays.
func (s *server) hRemoveEntity(w http.ResponseWriter, r *http.Request) {
	var req api.RemoveEntityRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := validEntityRequest(req); err != nil {
		writeError(w, err)
		return
	}
	sc, err := s.serverConfig()
	if err != nil {
		writeError(w, err)
		return
	}
	if sc == nil {
		writeError(w, errNotCreated())
		return
	}
	stopFirst := func(ctx context.Context) error {
		if _, running, err := s.containerRunning(ctx); err != nil {
			return err
		} else if running {
			return errConflict(s.name()+" is running.", "Stop it before taking something out of its world.")
		}
		return nil
	}
	if err := stopFirst(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	if _, err := s.planEntityFix(*sc, req); err != nil {
		writeError(w, err)
		return
	}
	op, err := s.beginOp("remove-entity", actor, func(ctx context.Context, h *opHandle) error {
		h.set("type", req.Type)
		if err := stopFirst(ctx); err != nil {
			return err
		}
		fix, err := s.planEntityFix(*sc, req)
		if err != nil {
			return err
		}
		h.set("file", fix.file)
		if err := s.backupOp(ctx, h, actor, "Before removing the "+fix.label(), false); err != nil {
			return err
		}
		h.phase("removing_entity")
		if err := s.writeEntityFix(fix); err != nil {
			s.audit(actor, "world.entity_removed", fix.file, "failed", err.Error())
			return err
		}
		s.audit(actor, "world.entity_removed", fix.file, "succeeded", fix.label())
		s.log.Info("entity removed", "server", s.id, "type", req.Type, "file", fix.file)
		if req.Start {
			return s.startNow(ctx, h)
		}
		return nil
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

func (s *server) writeEntityFix(f *entityFix) error {
	d, err := s.gameFiles()
	if err != nil {
		return err
	}
	defer d.Close()
	b, found, err := f.apply(d, s.now())
	if err != nil {
		return err
	}
	if !found {
		return f.gone(f.file)
	}
	if err := d.WriteFile(f.file, b, 0o640); err != nil {
		return gameFileError(err, "Playkeeper couldn't write "+f.file+".")
	}
	return nil
}

// entityFixRequest is the request a crash's remove_entity fix makes.
func entityFixRequest(p map[string]any) api.RemoveEntityRequest {
	req := api.RemoveEntityRequest{}
	req.What, _ = p["what"].(string)
	req.Type, _ = p["type"].(string)
	req.Dimension, _ = p["dimension"].(string)
	req.Level, _ = p["level"].(string)
	req.X, _ = p["x"].(int)
	req.Y, _ = p["y"].(int)
	req.Z, _ = p["z"].(int)
	req.Pos, _ = p["pos"].([]float64)
	return req
}

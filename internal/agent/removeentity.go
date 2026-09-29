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

var reResourceID = regexp.MustCompile(`^[a-z0-9_.-]{1,64}:[a-z0-9_./-]{1,200}$`)

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
// world folder, and Paper gave them folders of their own.
func dimensionFolders(level, dimension string) []string {
	ns, path, _ := strings.Cut(dimension, ":")
	modern := level + "/dimensions/" + ns + "/" + path
	switch dimension {
	case "minecraft:overworld":
		return []string{modern, level}
	case "minecraft:the_nether":
		return []string{modern, level + "/DIM-1", level + "_nether/DIM-1"}
	case "minecraft:the_end":
		return []string{modern, level + "/DIM1", level + "_the_end/DIM1"}
	}
	return []string{modern, level + "_" + ns + "_" + strings.ReplaceAll(path, "/", "_") + "/dimensions/" + ns + "/" + path}
}

// planEntityFix finds the region file that saves what req names and checks
// that it's there, so nothing is backed up or changed when it isn't.
func (s *server) planEntityFix(sc api.ServerConfig, req api.RemoveEntityRequest) (*entityFix, error) {
	d, err := s.gameFiles()
	if err != nil {
		return nil, err
	}
	defer d.Close()
	cx, cz := req.X>>4, req.Z>>4
	kind := "entities"
	if req.What == "block_entity" {
		kind = "region"
	}
	name := fmt.Sprintf("%s/r.%d.%d.mca", kind, cx>>5, cz>>5)
	f := &entityFix{req: req, cx: cx, cz: cz}
	for _, dir := range dimensionFolders(s.levelName(sc), req.Dimension) {
		if fi, err := d.Lstat(dir + "/" + name); err == nil && fi.Mode().IsRegular() {
			f.file = dir + "/" + name
			break
		}
	}
	if f.file == "" {
		return nil, errConflict(fmt.Sprintf("Playkeeper found no %s file for the %s in %s, so there's nothing to remove.", kind, f.label(), s.name()), "Start the server.")
	}
	if _, err := f.apply(d, s.now()); err != nil {
		return nil, err
	}
	return f, nil
}

// apply reads the region file and returns it with the entity or block
// entity taken out, or refuses when it isn't in its chunk.
func (f *entityFix) apply(d *gamefiles.Dir, now time.Time) ([]byte, error) {
	b, err := d.ReadFile(f.file, maxRegionFile)
	if err != nil {
		return nil, gameFileError(err, "Playkeeper couldn't read "+f.file+".")
	}
	rf, err := region.Parse(b)
	if err != nil {
		return nil, errConflict(fmt.Sprintf("%s can't be read (%v), so Playkeeper changed nothing.", f.file, err), "Restore a backup instead.")
	}
	compression, data, err := rf.Chunk(f.cx, f.cz)
	gone := errConflict(fmt.Sprintf("The %s isn't in %s any more, so there's nothing to remove.", f.label(), f.file), "Start the server.")
	if errors.Is(err, region.ErrNoChunk) {
		return nil, gone
	}
	if err != nil {
		return nil, errConflict(fmt.Sprintf("Chunk %d, %d of %s can't be read (%v), so Playkeeper changed nothing.", f.cx, f.cz, f.file, err), "Restore a backup instead.")
	}
	chunk, err := region.Decode(data)
	if err != nil {
		return nil, errConflict(fmt.Sprintf("Chunk %d, %d of %s is damaged (%v), so Playkeeper changed nothing.", f.cx, f.cz, f.file, err), "Restore a backup instead.")
	}
	r := f.req
	if r.What == "entity" && region.RemoveEntity(chunk, r.Type, r.X, r.Y, r.Z, r.Pos) == 0 ||
		r.What == "block_entity" && !region.RemoveBlockEntity(chunk, r.Type, r.X, r.Y, r.Z) {
		return nil, gone
	}
	if data, err = region.Encode(chunk); err != nil {
		return nil, err
	}
	if err := rf.SetChunk(f.cx, f.cz, compression, data, now); err != nil {
		return nil, err
	}
	return rf.Bytes(), nil
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
	b, err := f.apply(d, s.now())
	if err != nil {
		return err
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
	req.X, _ = p["x"].(int)
	req.Y, _ = p["y"].(int)
	req.Z, _ = p["z"].(int)
	req.Pos, _ = p["pos"].([]float64)
	return req
}

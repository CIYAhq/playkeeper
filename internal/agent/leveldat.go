package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"slices"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/backup"
	"github.com/CIYAhq/playkeeper/internal/gamefiles"
	"github.com/CIYAhq/playkeeper/internal/worldimport"
)

const (
	// maxLevelFile caps level.dat and world_gen_settings.dat as read: a few
	// kilobytes, more with a modded game's registries in them.
	maxLevelFile = 16 << 20
	// levelDecode is the memory one may take once read; see nbt.Limits.
	levelDecode = 64 << 20
	// seedBackups is how many of the newest checked backups are looked in
	// for a world's seed.
	seedBackups = 3
)

// levelFiles are the two files Minecraft keeps a world's settings in.
var levelFiles = []string{"level.dat", "level.dat_old"}

// Where a new level.dat takes the world's seed from.
const (
	seedFromWorld      = "world"      // world_gen_settings.dat, since Minecraft 26.1
	seedFromBackup     = "backup"     // level.dat in a checked backup
	seedFromProperties = "properties" // level-seed in server.properties
)

// levelRepair is what making a new level.dat does for a world whose
// level.dat and level.dat_old can't be read: where the world's seed comes
// from ("" for nowhere, so new land won't match the old), and what starts
// over because this world keeps it in level.dat.
type levelRepair struct {
	world  string
	seed   string
	resets []string
}

// planLevelRepair checks that world, one of the server's world folders, has
// neither a readable level.dat nor a readable level.dat_old, and says what a
// new one would do. A world with either is refused: Minecraft reads it.
func (s *server) planLevelRepair(world string) (*levelRepair, error) {
	d, err := s.gameFiles()
	if err != nil {
		return nil, err
	}
	defer d.Close()
	if !slices.Contains(s.worldFolders(d), world) {
		return nil, errInvalid("%s has no world called %s.", s.name(), world)
	}
	for _, name := range levelFiles {
		p := world + "/" + name
		b, err := d.ReadFile(p, maxLevelFile)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case gamefiles.KindOf(err) != "":
			return nil, gameFileError(err, "Playkeeper couldn't check "+p+".")
		case err != nil:
			return nil, err
		default:
			if _, err := worldimport.ParseLevel(b, levelDecode); err == nil {
				return nil, errConflict(p+" can be read, so "+s.name()+" doesn't need a new level.dat.", "Start the server. If it stops again, the Overview says why.")
			}
		}
	}
	r := &levelRepair{world: world, resets: levelResets(d, world)}
	switch {
	case worldSeed(d, world) != "":
		r.seed = seedFromWorld
	case len(s.seedBackups()) > 0:
		r.seed = seedFromBackup
	case propertiesSeed(d) != "":
		r.seed = seedFromProperties
	}
	return r, nil
}

// levelResets lists what a new level.dat starts over: Minecraft 26.1 moved
// the game rules and the time into files of their own, and 1.21.9 the world
// border. The spawn point is always in level.dat.
func levelResets(d *gamefiles.Dir, world string) []string {
	has := func(files ...string) bool {
		for _, f := range files {
			if fi, err := d.Lstat(world + "/" + f); err == nil && fi.Mode().IsRegular() {
				return true
			}
		}
		return false
	}
	// Paper keeps its copies in the Overworld's own data folder.
	own := "dimensions/minecraft/overworld/data/minecraft/"
	var out []string
	if !has("data/minecraft/game_rules.dat", own+"game_rules.dat") {
		out = append(out, "game_rules")
	}
	if !has("data/minecraft/world_clocks.dat", own+"world_clocks.dat") {
		out = append(out, "time")
	}
	if !has("data/world_border.dat", own+"world_border.dat") {
		out = append(out, "world_border")
	}
	return append(out, "spawn")
}

// worldSeed is the seed in the world's world_gen_settings.dat, where
// Minecraft 26.1 and newer keep it.
func worldSeed(d *gamefiles.Dir, world string) string {
	for _, f := range []string{"data/minecraft/world_gen_settings.dat", "dimensions/minecraft/overworld/data/minecraft/world_gen_settings.dat"} {
		if b, err := d.ReadFile(world+"/"+f, maxLevelFile); err == nil {
			if seed := worldimport.ReadSeed(b, levelDecode); seed != "" {
				return seed
			}
		}
	}
	return ""
}

func propertiesSeed(d *gamefiles.Dir) string {
	b, err := d.ReadProperties()
	if err != nil {
		return ""
	}
	return worldimport.PropertiesSeed(b)
}

// seedBackups are the newest checked backups, which a world's seed is read
// from before Minecraft 26.1.
func (s *server) seedBackups() []api.Backup {
	list, err := s.listBackups(`verified = 1`)
	if err != nil {
		return nil
	}
	return list[:min(len(list), seedBackups)]
}

// backupSeed reads the world's seed from the level.dat, or level.dat_old,
// of the newest checked backup that has a readable one.
func (s *server) backupSeed(world string) (seed string, from *api.Backup) {
	for _, b := range s.seedBackups() {
		for _, name := range levelFiles {
			if seed := s.archiveSeed(b, world+"/"+name); seed != "" {
				return seed, &b
			}
		}
	}
	return "", nil
}

func (s *server) archiveSeed(b api.Backup, rel string) string {
	f, err := os.Open(s.backupPath(b.FileName))
	if err != nil {
		return ""
	}
	defer f.Close()
	gz, err := backup.ReadFile(f, rel, maxLevelFile)
	if err != nil {
		return ""
	}
	lv, err := worldimport.ParseLevel(gz, levelDecode)
	if err != nil {
		return ""
	}
	return lv.Seed
}

// hRebuildLevel makes a new level.dat for a stopped server's world whose
// level.dat and level.dat_old can't be read, the crash helper's alternative
// to restoring a backup. Only the owner's request does it: the world is
// backed up first, the world's seed is written to server.properties, so new
// land matches the old, and both files are deleted. Minecraft then makes a
// new level.dat, and every build, which is in other files, stays.
func (s *server) hRebuildLevel(w http.ResponseWriter, r *http.Request) {
	var req api.RebuildLevelRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
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
	world := req.World
	if world == "" {
		world = s.levelName(*sc)
	}
	stopFirst := func(ctx context.Context) error {
		if _, running, err := s.containerRunning(ctx); err != nil {
			return err
		} else if running {
			return errConflict(s.name()+" is running.", "Stop it before making a new level.dat.")
		}
		return nil
	}
	if err := stopFirst(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	if _, err := s.planLevelRepair(world); err != nil {
		writeError(w, err)
		return
	}
	op, err := s.beginOp("rebuild-level", actor, func(ctx context.Context, h *opHandle) error {
		h.set("world", world)
		if err := stopFirst(ctx); err != nil {
			return err
		}
		plan, err := s.planLevelRepair(world)
		if err != nil {
			return err
		}
		seed, from := "", plan.seed
		switch plan.seed {
		case seedFromWorld:
			if d, err := s.gameFiles(); err == nil {
				seed = worldSeed(d, world)
				d.Close()
			}
		case seedFromBackup:
			var b *api.Backup
			if seed, b = s.backupSeed(world); b != nil {
				h.set("seedBackupId", b.ID)
			} else if d, err := s.gameFiles(); err == nil {
				if propertiesSeed(d) != "" {
					from = seedFromProperties
				}
				d.Close()
			}
			if seed == "" && from == seedFromBackup {
				return errConflict("Playkeeper couldn't read the world's seed from its backups, so it changed nothing.", "Restore the latest backup instead.")
			}
		}
		h.set("seedFrom", from)
		if err := s.backupOp(ctx, h, actor, "Before a new level.dat", false); err != nil {
			return err
		}
		h.phase("rebuilding_level")
		if err := s.rebuildLevel(world, seed); err != nil {
			s.audit(actor, "world.level_rebuilt", world, "failed", err.Error())
			return err
		}
		s.audit(actor, "world.level_rebuilt", world, "succeeded", "seed from "+cmp.Or(from, "nowhere"))
		s.log.Info("new level.dat", "server", s.id, "world", world, "seed_from", from)
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

// rebuildLevel writes seed, when there is one, as the level-seed of
// server.properties, which Minecraft makes a new world's level.dat with,
// and deletes the world's level.dat and level.dat_old.
func (s *server) rebuildLevel(world, seed string) error {
	d, err := s.gameFiles()
	if err != nil {
		return err
	}
	defer d.Close()
	if seed != "" {
		cur, err := d.ReadProperties()
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return gameFileError(err, "Playkeeper couldn't read server.properties, so it changed nothing.")
		}
		if worldimport.PropertiesSeed(cur) != seed {
			if err := d.WriteProperties(worldimport.ApplySettings(cur, []worldimport.Setting{{Key: "level-seed", Value: seed}})); err != nil {
				return gameFileError(err, "Playkeeper couldn't save the world's seed in server.properties, so it changed nothing.")
			}
		}
	}
	for _, name := range levelFiles {
		if err := d.Remove(world + "/" + name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return gameFileError(err, fmt.Sprintf("Playkeeper couldn't delete %s/%s.", world, name))
		}
	}
	return nil
}

// levelRepairParams describes a new level.dat for the crash helper's fix.
func (s *server) levelRepairParams(world string) map[string]any {
	plan, err := s.planLevelRepair(world)
	if err != nil {
		return nil
	}
	return map[string]any{"world": plan.world, "seed_from": plan.seed, "resets": plan.resets}
}

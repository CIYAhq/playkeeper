package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/backup"
	"github.com/CIYAhq/playkeeper/internal/diagnose"
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
	seedFromBackup     = "backup"     // a checked backup's copy of the world
	seedFromProperties = "properties" // level-seed in server.properties, which may not be the world's
)

// levelRepair is what making a new level.dat does for a world whose
// level.dat and level.dat_old can't be read: the world's seed, which
// Playkeeper keeps, and where it read it (from), and what starts over
// because this world keeps it in level.dat. Without the seed, from is
// seedFromProperties when server.properties has a level-seed, which
// Minecraft then uses though nothing says it's the world's, else "".
type levelRepair struct {
	world  string
	seed   string
	from   string
	backup *api.Backup // the backup the seed was read from
	resets []string
}

// keepsSeed reports whether a new level.dat made with its seed from from
// keeps the world's seed, so new terrain matches the old.
func keepsSeed(from string) bool { return from == seedFromWorld || from == seedFromBackup }

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
	if r.seed = worldSeed(d, world); r.seed != "" {
		r.from = seedFromWorld
	} else if r.seed, r.backup = s.backupSeed(world); r.seed != "" {
		r.from = seedFromBackup
	} else if propertiesSeed(d) != "" {
		r.from = seedFromProperties
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

// backupSeed reads the world's seed from the newest of the seedBackups
// newest checked backups that has it, made since the world was last put in
// place: an older one may be of the world a restore or an import replaced,
// and one made once the level.dat files were damaged has no seed to give.
func (s *server) backupSeed(world string) (string, *api.Backup) {
	list, err := s.listBackups(`verified = 1 AND created_at >= ?`, s.worldPlacedAt().UnixMilli())
	if err != nil {
		return "", nil
	}
	for _, b := range list[:min(len(list), seedBackups)] {
		if seed := s.archiveSeed(b, world); seed != "" {
			return seed, &b
		}
	}
	return "", nil
}

// worldPlacedAt is when the server's world was last replaced, by a restore
// or an import; the zero time if it never was.
func (s *server) worldPlacedAt() time.Time {
	op, err := s.scanOperation(s.db.QueryRow(`SELECT `+operationColumns+` FROM operations
		WHERE server_id = ? AND kind IN ('restore', 'offsite-restore', 'world_import') AND status = 'succeeded'
		ORDER BY started_at DESC LIMIT 1`, s.id))
	if err != nil || op.FinishedAt == nil {
		return time.Time{}
	}
	return *op.FinishedAt
}

// archiveSeed reads the world's seed from a backup: from world_gen_settings.dat
// since Minecraft 26.1, else from level.dat or level.dat_old. Each is its own
// pass: world_gen_settings.dat comes early in an archive, and a world from
// before 26.1 has none, so looking for it costs little, while a 26.1 world's
// level.dat comes after all of its dimensions.
func (s *server) archiveSeed(b api.Backup, world string) string {
	for _, files := range [][]string{
		{world + "/data/minecraft/world_gen_settings.dat", world + "/dimensions/minecraft/overworld/data/minecraft/world_gen_settings.dat"},
		{world + "/level.dat", world + "/level.dat_old"},
	} {
		got, err := s.readBackupFiles(b, files)
		if err != nil {
			return ""
		}
		for _, rel := range files {
			data := got[rel]
			if data == nil {
				continue
			}
			if strings.HasSuffix(rel, "world_gen_settings.dat") {
				if seed := worldimport.ReadSeed(data, levelDecode); seed != "" {
					return seed
				}
			} else if lv, err := worldimport.ParseLevel(data, levelDecode); err == nil && lv.Seed != "" {
				return lv.Seed
			}
		}
	}
	return ""
}

func (s *server) readBackupFiles(b api.Backup, files []string) (map[string][]byte, error) {
	f, err := os.Open(s.backupPath(b.FileName))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return backup.ReadFiles(f, files, maxLevelFile)
}

// hRebuildLevel makes a new level.dat for a stopped server's world whose
// level.dat and level.dat_old can't be read, the crash helper's alternative
// to restoring a backup. Only the owner's request does it: the world is
// backed up first, the world's seed, when Playkeeper finds it, is written to
// server.properties, so new terrain matches the old, and both files are
// deleted. Minecraft then makes a new level.dat, and every build, which is
// in other files, stays.
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
	// The owner confirmed what the dialog said about the seed: a new
	// level.dat it said keeps the seed isn't made without it.
	plan := func() (*levelRepair, error) {
		p, err := s.planLevelRepair(world)
		if err != nil {
			return nil, err
		}
		if req.SeedFrom != nil && keepsSeed(*req.SeedFrom) && !keepsSeed(p.from) {
			s.refreshLevelFix(p)
			return nil, errConflict("Playkeeper can't find the world's seed any more, so it changed nothing.",
				"Look at the new level.dat again: without the seed, new terrain won't match the old.")
		}
		return p, nil
	}
	if _, err := plan(); err != nil {
		writeError(w, err)
		return
	}
	op, err := s.beginOp("rebuild-level", actor, func(ctx context.Context, h *opHandle) error {
		h.set("world", world)
		if err := stopFirst(ctx); err != nil {
			return err
		}
		p, err := plan()
		if err != nil {
			return err
		}
		h.set("seedFrom", p.from)
		if p.backup != nil {
			h.set("seedBackupId", p.backup.ID)
		}
		if err := s.backupOp(ctx, h, actor, "Before a new level.dat", false); err != nil {
			return err
		}
		h.phase("rebuilding_level")
		if err := s.rebuildLevel(world, p.seed); err != nil {
			s.audit(actor, "world.level_rebuilt", world, "failed", err.Error())
			return err
		}
		s.audit(actor, "world.level_rebuilt", world, "succeeded", "seed from "+cmp.Or(p.from, "nowhere"))
		s.log.Info("new level.dat", "server", s.id, "world", world, "seed_from", p.from)
		if !req.Start {
			return nil
		}
		if err := s.startNow(ctx, h); err != nil {
			return err
		}
		return s.checkSeedKept(ctx, h, p.seed)
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

var reSeedReply = regexp.MustCompile(`Seed: \[(-?\d{1,20})\]`)

// checkSeedKept asks the server that started with a new level.dat for its
// seed, when it was to keep the world's: another would make new terrain
// that doesn't match the old. The console listens only once the server is
// done starting, so it tries for a while; seed changes nothing, so asking
// again is safe. A server that doesn't answer leaves it unchecked.
func (s *server) checkSeedKept(ctx context.Context, h *opHandle, seed string) error {
	if seed == "" {
		return nil
	}
	var reply string
	var err error
	for try := 0; try < 10; try++ {
		if reply, err = s.rconCommand("seed"); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
	m := reSeedReply.FindStringSubmatch(reply)
	if err != nil || m == nil {
		s.log.Warn("the server didn't say its seed after a new level.dat", "server", s.id, "err", err, "reply", reply)
		return nil
	}
	h.set("seedKept", m[1] == seed)
	if m[1] != seed {
		s.log.Warn("a new level.dat didn't keep the seed", "server", s.id, "want", seed, "got", m[1])
		return errConflict(s.name()+" started with a different seed, so new terrain won't match the old.",
			"Restore the backup Playkeeper made before the new level.dat to undo it.")
	}
	return nil
}

// levelRepairParams describes a new level.dat for the crash helper's fix.
func (s *server) levelRepairParams(world string) map[string]any {
	plan, err := s.planLevelRepair(world)
	if err != nil {
		return nil
	}
	return plan.params()
}

func (p *levelRepair) params() map[string]any {
	return map[string]any{"world": p.world, "seed_from": p.from, "resets": p.resets}
}

// refreshLevelFix puts what a new level.dat does now into the crash's fix,
// once it has changed since the crash was explained.
func (s *server) refreshLevelFix(p *levelRepair) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.crash == nil {
		return
	}
	c := *s.crash
	c.Fixes = slices.Clone(c.Fixes)
	for i := range c.Fixes {
		if diagnose.ActionKind(c.Fixes[i].Kind) == diagnose.ActionRebuildLevel {
			c.Fixes[i].Params = p.params()
		}
	}
	s.crash = &c
}

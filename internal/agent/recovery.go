package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/backup"
)

// pendingRestore is a restore a previous agent process left running, with
// its stage and the stage's swap journal, if it has one.
type pendingRestore struct {
	op       *api.Operation
	stageDir string
	journal  *swapJournal
}

// findInterruptedRestores finds the restores a previous agent process left
// running whose stage is still there, for their servers to finish when the
// agent starts, and returns their operations' IDs.
func (a *Agent) findInterruptedRestores() []string {
	rows, err := a.db.Query(`SELECT id FROM operations WHERE status = 'running' AND kind = 'restore'`)
	if err != nil {
		a.log.Error("find interrupted restores", "err", err)
		return nil
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	var found []string
	for _, id := range ids {
		op, err := a.loadOperation(id)
		if err != nil {
			continue
		}
		s := a.serverByID(op.ServerID)
		stageID, _ := op.Detail["stage"].(string)
		if s == nil || s.recovery != nil || !reStageID.MatchString(stageID) {
			continue
		}
		dir := a.stageDir(stageID)
		if !dirExists(dir) {
			continue
		}
		j, err := readSwapJournal(dir)
		if err != nil || j != nil && (j.ServerID != s.id || j.OpID != op.ID) {
			a.log.Warn("an interrupted restore's journal is unreadable or not its own, so it is not finished", "server", s.id, "operation", op.ID, "err", err)
			continue
		}
		s.recovery = &pendingRestore{op: op, stageDir: dir, journal: j}
		found = append(found, op.ID)
	}
	return found
}

// resuming is true if dir is the stage of a restore the agent is about to
// finish.
func (a *Agent) resuming(dir string) bool {
	for _, s := range a.serverList() {
		if s.recovery != nil && s.recovery.stageDir == dir {
			return true
		}
	}
	return false
}

// pendingVersionChange is a version change a previous agent process left
// running, with its journal.
type pendingVersionChange struct {
	op      *api.Operation
	journal *versionJournal
}

// findInterruptedVersionChanges finds the version changes a previous agent
// process left running with a journal, for their servers to finish when the
// agent starts, and returns their operations' IDs. One without a journal had
// changed nothing yet, and is marked interrupted.
func (a *Agent) findInterruptedVersionChanges() []string {
	rows, err := a.db.Query(`SELECT id FROM operations WHERE status = 'running' AND kind = 'update-version'`)
	if err != nil {
		a.log.Error("find interrupted version changes", "err", err)
		return nil
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	var found []string
	for _, id := range ids {
		op, err := a.loadOperation(id)
		if err != nil {
			continue
		}
		s := a.serverByID(op.ServerID)
		if s == nil || s.recovery != nil || s.versionRecovery != nil {
			continue
		}
		j, err := s.readVersionJournal()
		if err != nil || j != nil && j.OpID != op.ID {
			a.log.Warn("an interrupted version change's journal is unreadable or not its own, so it is not finished", "server", s.id, "operation", op.ID, "err", err)
			continue
		}
		if j == nil {
			continue
		}
		s.versionRecovery = &pendingVersionChange{op: op, journal: j}
		found = append(found, op.ID)
	}
	return found
}

// recoverAtStart finishes, as its own operation, the restore or version
// change a previous agent process left running on the server. Without one,
// it deals with a restore Playkeeper 0.3.0 undid only because the agent was
// stopping.
func (s *server) recoverAtStart() {
	if v := s.versionRecovery; v != nil {
		s.versionRecovery = nil
		s.opLock <- struct{}{}
		s.launchOp(v.op, func(ctx context.Context, h *opHandle) error { return s.recoverVersionChange(ctx, h, v.journal) })
		return
	}
	p := s.recovery
	s.recovery = nil
	if p == nil {
		s.recoverUndoneRestore()
		return
	}
	s.opLock <- struct{}{}
	s.launchOp(p.op, func(ctx context.Context, h *opHandle) error { return s.recoverRestore(ctx, h, p) })
}

// recoverVersionChange finishes a version change a previous agent process
// was in the middle of: one starting its new version saves the new settings,
// which it may not have got to, checks the new version again and keeps it
// once it is online; one being rolled back is rolled back.
func (s *server) recoverVersionChange(ctx context.Context, h *opHandle, j *versionJournal) error {
	h.set("resumedAfterRestart", true)
	if j.State == versionReverting {
		return s.revertVersionChange(h, j)
	}
	if err := s.saveServerConfig(j.Next); err != nil {
		j.Why = "its settings could not be saved after the Playkeeper agent restarted: " + err.Error()
		return s.revertVersionChange(h, j)
	}
	_ = s.setDesired(api.DesiredRunning)
	return s.finishVersionChange(ctx, h, j)
}

// recoverRestore finishes a restore a previous agent process was in the
// middle of: one moving the worlds finishes moving them, one checking its
// restored world checks it again and keeps it if it starts, a kept one
// finishes tidying up, and one being undone is undone. A restore Playkeeper
// 0.3.0 left has no journal, so how far it got is worked out from the worlds
// and settings it left.
func (s *server) recoverRestore(ctx context.Context, h *opHandle, p *pendingRestore) error {
	h.set("resumedAfterRestart", true)
	j := p.journal
	if j == nil {
		var err error
		if j, err = s.adoptRestore(ctx, p); j == nil {
			if s.stopping() {
				h.continues = true
				return nil
			}
			if dirExists(s.dataDir()) {
				os.RemoveAll(p.stageDir)
			}
			return err
		}
	}
	defer func() {
		if j.State == swapDone {
			os.RemoveAll(p.stageDir)
		}
	}()
	switch j.State {
	case swapMoving:
		if err := s.rollForward(p.stageDir, j); err != nil {
			if !j.HadLive || j.Previous == nil {
				return &apiError{Msg: "The restore could not be finished after the Playkeeper agent restarted: " + err.Error() + ".", Hint: "Nothing was deleted. Restore the backup again."}
			}
			j.Why = "The restore could not be finished after the Playkeeper agent restarted (" + err.Error() + ")."
			return s.revertRestore(h, p.stageDir, j)
		}
		restoreStep(ctx, "checking")
		return s.finishRestore(ctx, h, p.stageDir, j)
	case swapChecking:
		return s.finishRestore(ctx, h, p.stageDir, j)
	case swapKept:
		return s.keepRestore(h, j, false)
	case swapReverting:
		if j.Why == "" {
			j.Why = "The Playkeeper agent stopped while the restore was being undone."
		}
		return s.revertRestore(h, p.stageDir, j)
	}
	return fmt.Errorf("swap journal in an unknown state %q", j.State)
}

// adoptRestore works out how far a restore Playkeeper 0.3.0 left running got
// and returns the journal that finishes it or puts the previous world back,
// or nil and the operation's error if there is nothing to finish. 0.3.0 kept
// no journal: it moved the live world aside and the restored world in, saved
// the restored settings, started the server and deleted the previous world's
// copy once it was online. A restored world that did not start went to a
// failed-restore copy, and the previous world and settings were put back.
func (s *server) adoptRestore(ctx context.Context, p *pendingRestore) (*swapJournal, error) {
	unknown := &apiError{Msg: "The Playkeeper agent stopped during this restore, and how far the restore got could not be worked out, so nothing was moved.", Hint: "Check the world, and the copies on the World tab, before you restore the backup again."}
	cur, err := s.serverConfig()
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, errNotCreated()
	}
	f, err := readStageFile(p.stageDir)
	if err != nil {
		return nil, unknown
	}
	aside, failed, err := s.restoreCopiesSince(p.op.StartedAt.Truncate(time.Second))
	if err != nil {
		return nil, unknown
	}
	hasLive, hasStaged, hasAside, hasFailed := dirExists(s.dataDir()), dirExists(filepath.Join(p.stageDir, "data")), aside != "", failed != ""
	stamp := s.now().UTC().Format("20060102-150405")
	for _, name := range []string{aside, failed} {
		if m := reWorldCopy.FindStringSubmatch(name); m != nil {
			stamp = m[2]
			break
		}
	}
	if aside == "" {
		aside = "data.replaced-" + stamp
	}
	if failed == "" {
		failed = "data.failed-restore-" + stamp
	}
	m := f.Manifest
	rollbackID, _ := p.op.Detail["rollbackBackupId"].(string)
	j := &swapJournal{
		ServerID: s.id, OpID: p.op.ID, Actor: p.op.Actor, Aside: aside, Failed: failed, HadLive: hasAside || hasLive && hasStaged,
		StartedAt: p.op.StartedAt, Restored: *cur, SHA256: f.Preview.SHA256, Detail: fmt.Sprintf("restored %s (sha256 %s)", m.LevelName, f.Preview.SHA256),
	}
	if rollbackID != "" {
		j.Detail += "; rollback archive " + rollbackID
	}
	save := func(state swapState, why string) (*swapJournal, error) {
		j.State, j.Why = state, why
		if err := writeSwapJournal(p.stageDir, j); err != nil {
			return nil, fmt.Errorf("could not save the restore's progress file, so nothing was moved: %w", err)
		}
		return j, nil
	}
	previous := func() error {
		prev, err := s.previousConfig(ctx, rollbackID, *cur, m)
		if err != nil {
			return &apiError{Msg: "The Playkeeper agent stopped during this restore, and the settings from before it could not be worked out (" + err.Error() + "), so nothing was moved.", Hint: "The previous world is kept at " + s.copyPath(aside) + "."}
		}
		j.Previous = prev
		return nil
	}
	undo := func(why string) (*swapJournal, error) {
		if err := previous(); err != nil {
			return nil, err
		}
		j.HadLive = true
		return save(swapReverting, why)
	}
	restored, err := s.restoredSettings030(ctx, *cur, m)
	if err != nil && s.stopping() {
		return nil, err
	}
	switch {
	case hasFailed || p.op.Phase == "reverting":
		if hasAside || hasFailed && hasLive {
			return undo("The restored world did not start, and the Playkeeper agent stopped while the previous world was being put back.")
		}
	case hasLive && hasStaged && !hasAside:
		return nil, &apiError{Msg: "The Playkeeper agent stopped before this restore replaced the world, so nothing was replaced.", Hint: "Restore the backup again."}
	case p.op.Phase == string(api.PhaseOnline) && hasLive && !hasStaged:
		return save(swapKept, "")
	case rollbackID == "" && restored && !hasAside && hasLive != hasStaged:
		j.HadLive = false
		return save(swapMoving, "")
	case !hasLive && hasAside:
		return undo("The Playkeeper agent stopped before the restored world was in place.")
	case hasLive && hasAside && !hasStaged && restored:
		if err := previous(); err != nil {
			return nil, err
		}
		return save(swapChecking, "")
	case hasLive && hasAside && !hasStaged:
		return undo("The Playkeeper agent stopped before the restored world's settings were saved.")
	}
	return nil, unknown
}

// restoreCopiesSince names the previous world's copy and the failed restore's
// copy made since t, if there are any.
func (s *server) restoreCopiesSince(t time.Time) (aside, failed string, err error) {
	for _, c := range s.worldCopies() {
		if c.CreatedAt.Before(t) {
			continue
		}
		name := &aside
		if c.Kind == api.WorldCopyFailedRestore {
			name = &failed
		}
		if *name != "" {
			return "", "", fmt.Errorf("more than one world copy of the same kind since %s", t.Format(time.RFC3339))
		}
		*name = c.Name
	}
	return aside, failed, nil
}

// restoredFrom is true if sc has the settings a restore of the backup with
// manifest m saves, as far as they tell backups apart.
func restoredFrom(sc api.ServerConfig, m backup.Manifest) bool {
	level := func(name string) string {
		if name == "" {
			return "world"
		}
		return name
	}
	return level(sc.LevelName) == level(m.LevelName) && sc.MinecraftVersion == m.MinecraftVersion &&
		sc.MOTD == validMOTDOr(m.Settings["motd"]) && sc.MaxPlayers == manifestMaxPlayers(m)
}

// restoredSettings030 is true if sc are, in every field Playkeeper 0.3.0's
// restore saved, what it saved for the backup with manifest m: the backup's
// settings on the build restoreBuild picks, with the memory the restore
// preview suggests (0.3.0's dashboard offered no other), keeping only the
// EULA, creation time and play style. The start after the save may have
// verified the jar since. Settings that match without having been saved are
// the backup's all the same. If they cannot be checked, they do not count.
func (s *server) restoredSettings030(ctx context.Context, sc api.ServerConfig, m backup.Manifest) (bool, error) {
	mem, _ := strconv.Atoi(m.Settings["memoryMB"])
	if s.validMemory(mem, s.id) != nil {
		_, mem, _ = s.memoryFor(s.id)
	}
	if !restoredFrom(sc, m) || sc.MemoryMB != mem {
		return false, nil
	}
	entry, err := s.restoreBuild(ctx, m.MinecraftVersion, m.PaperBuild)
	if err != nil {
		return false, err
	}
	want := s.restoredConfig(m, entry, mem, &sc, sc.EULAAcceptedBy)
	sc.JarVerifiedAt = nil
	return sc == want, nil
}

// previousConfig is the server's settings from before a restore Playkeeper
// 0.3.0 left: the current ones, unless the restored ones already replaced
// them. Those are rebuilt from the record of the rollback archive the
// restore made of the previous world, as a restore of that archive would
// save them; gameplay settings stay in the world's server.properties.
func (s *server) previousConfig(ctx context.Context, rollbackID string, cur api.ServerConfig, restored backup.Manifest) (*api.ServerConfig, error) {
	if rollbackID == "" || !restoredFrom(cur, restored) {
		return &cur, nil
	}
	var mj string
	if err := s.db.QueryRow(`SELECT manifest FROM backups WHERE id = ? AND server_id = ?`, rollbackID, s.id).Scan(&mj); err != nil {
		return nil, fmt.Errorf("the rollback archive %s is not recorded: %w", rollbackID, err)
	}
	var m backup.Manifest
	if err := json.Unmarshal([]byte(mj), &m); err != nil {
		return nil, fmt.Errorf("the rollback archive's record is unreadable: %w", err)
	}
	mem, _ := strconv.Atoi(m.Settings["memoryMB"])
	if mem <= 0 {
		mem = cur.MemoryMB
	}
	if restoredFrom(cur, m) && cur.MemoryMB == mem && cur.PaperBuild == m.PaperBuild {
		return &cur, nil
	}
	entry, err := s.restoreBuild(ctx, m.MinecraftVersion, m.PaperBuild)
	if err != nil {
		if cur.MinecraftVersion != m.MinecraftVersion || cur.PaperBuild != m.PaperBuild {
			return nil, fmt.Errorf("Paper %s build %d could not be looked up: %w", m.MinecraftVersion, m.PaperBuild, err)
		}
		entry = api.CatalogEntry{ID: cur.VersionID, MinecraftVersion: cur.MinecraftVersion, PaperBuild: cur.PaperBuild, JarSHA256: cur.JarSHA256}
	}
	sc := s.restoredConfig(m, entry, mem, &cur, cur.EULAAcceptedBy)
	return &sc, nil
}

// reArchiveLeftover matches the file a backup is written to until it is
// complete.
var reArchiveLeftover = regexp.MustCompile(`^\.playkeeper-.+\.tar\.gz\.partial$`)

// pruneArchiveLeftovers deletes the backups an earlier run of the agent did
// not finish writing, such as the rollback archive of a restore it stopped
// in. Nothing can be writing one yet.
func (a *Agent) pruneArchiveLeftovers() {
	entries, _ := os.ReadDir(a.cfg.BackupsDir())
	removed := 0
	for _, e := range entries {
		if !e.Type().IsRegular() || !reArchiveLeftover.MatchString(e.Name()) {
			continue
		}
		if err := os.Remove(filepath.Join(a.cfg.BackupsDir(), e.Name())); err != nil {
			a.log.Warn("could not remove an unfinished backup", "file", e.Name(), "err", err)
			continue
		}
		removed++
	}
	if removed > 0 {
		a.log.Info("removed unfinished backups", "count", removed)
	}
}

// The error Playkeeper 0.3.0 recorded when the agent stopped while a restored
// world was starting: it undid the restore, but could not stop the restored
// world's container, which kept running from the failed restore's copy.
const (
	undoneRestoreStart = "The restored world did not start ("
	undoneRestoreAgain = "). Your previous world was put back but did not start either: "
)

// recoverUndoneRestore corrects the record of a restore Playkeeper 0.3.0
// undid only because the agent was stopping. If the restored world it could
// not stop is still running, from the failed restore's copy, it is stopped
// and the previous world 0.3.0 put back is started.
func (s *server) recoverUndoneRestore() {
	op, err := s.scanOperation(s.db.QueryRow(`SELECT `+operationColumns+`
		FROM operations WHERE server_id = ? AND kind = 'restore' ORDER BY started_at DESC LIMIT 1`, s.id))
	if err != nil || !undoneByStop(op) {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	c, running, err := s.containerRunning(ctx)
	if err != nil {
		return
	}
	fixed := copyOp(op)
	fixed.Error = "The Playkeeper agent stopped while the restored world was starting, so the restore was undone and your previous world put back."
	fixed.Hint = strings.TrimPrefix(op.Hint, "Press Start on the Overview. ")
	fixed.Detail["recoveredAfterRestart"] = true
	started, ok := c.State.Started()
	if !running || !ok || started.Before(op.StartedAt) || started.After(*op.FinishedAt) {
		s.saveOperation(fixed)
		s.audit("playkeeper", "restore.recovered", op.ID, "succeeded", "corrected the record of a restore undone because the agent stopped")
		return
	}
	s.opLock <- struct{}{}
	s.startOp("recover", "playkeeper", func(ctx context.Context, h *opHandle) error {
		if err := s.stopContainer(ctx, h, c.ID); err != nil {
			return err
		}
		s.saveOperation(fixed)
		s.audit("playkeeper", "restore.recovered", op.ID, "succeeded", "stopped the restored world, which was still running after the restore was undone")
		sc, err := s.serverConfig()
		if err != nil || sc == nil {
			return errNotCreated()
		}
		if s.desired() != api.DesiredRunning {
			return nil
		}
		return s.startServer(ctx, h, *sc)
	})
}

// undoneByStop is true for a restore Playkeeper 0.3.0 undid because the agent
// was stopping while the restored world started, that was not dealt with yet.
func undoneByStop(op *api.Operation) bool {
	if op.Status != api.OpFailed || op.Phase != "reverting" || op.FinishedAt == nil || op.Detail["recoveredAfterRestart"] != nil {
		return false
	}
	why, _, ok := strings.Cut(strings.TrimPrefix(op.Error, undoneRestoreStart), undoneRestoreAgain)
	return ok && strings.HasPrefix(op.Error, undoneRestoreStart) && strings.Contains(why, "context canceled")
}

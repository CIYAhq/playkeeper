package agent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/backup"
	"github.com/CIYAhq/playkeeper/internal/discord"
	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/version"
)

var reBackupID = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9a-f]{6}$`)
var reStageID = regexp.MustCompile(`^[0-9a-f]{16}$`)

const minFreeAfterBackup = 512 << 20

// archiveLimits are the limits backups are written with; tests lower them.
var archiveLimits = backup.DefaultLimits

func validBackupID(id string) error {
	if !reBackupID.MatchString(id) {
		return errInvalid("invalid backup id")
	}
	return nil
}

func (a *Agent) backupPath(fileName string) string {
	return filepath.Join(a.cfg.BackupsDir(), fileName)
}

// allowlistedSize estimates the archive input size for the disk-space check.
func allowlistedSize(dataDir string) int64 {
	var total int64
	_ = filepath.WalkDir(dataDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case "libraries", "versions", "cache", ".cache", "logs":
				return filepath.SkipDir
			}
			return nil
		}
		if info, err := d.Info(); err == nil && !strings.HasSuffix(d.Name(), ".jar") {
			total += info.Size()
		}
		return nil
	})
	return total
}

// archiveName is a new backup's id and archive file name.
func (s *server) archiveName(now time.Time) (id, fileName string) {
	id = now.Format("20060102-150405") + "-" + randomSecret(3)
	label := backup.LevelName(s.dataDir())
	if s.layout != layoutV1 {
		if row, err := s.row(); err == nil {
			label = row.Slug
		}
	}
	return id, fmt.Sprintf("playkeeper-%s-%s.tar.gz", sanitizeName(label), id)
}

// archiveMeta describes the server in a backup's manifest. How the archive
// was made (Consistency) and its files are added when it is written.
func (s *server) archiveMeta(sc api.ServerConfig, now time.Time) backup.Manifest {
	m := backup.Manifest{
		CreatedAt: now, PlaykeeperVersion: version.Version, SourceInstall: shortID(s.cfg.InstallID),
		Type: sc.Type, VersionID: sc.VersionID, MinecraftVersion: sc.MinecraftVersion, PaperBuild: sc.PaperBuild, Build: configBuild(sc), Image: runtimeImage(sc.MinecraftVersion),
		Settings: map[string]string{
			"motd": sc.MOTD, "maxPlayers": strconv.Itoa(sc.MaxPlayers), "memoryMB": strconv.Itoa(sc.MemoryMB), "whitelist": "true",
			"name": s.name(),
		},
	}
	if sc.VoiceChatPort > 0 {
		m.Settings[manifestVoiceChatPort] = strconv.Itoa(sc.VoiceChatPort)
	}
	return m
}

// createArchive writes a verified archive of the (stopped) server's data.
func (s *server) createArchive(sc api.ServerConfig, kind, actor, note string) (*api.Backup, error) {
	now := s.now().UTC()
	id, fileName := s.archiveName(now)
	tmp := s.backupPath("." + fileName + ".partial")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	meta := s.archiveMeta(sc, now)
	meta.Consistency = "server stopped during archive"
	m, err := backup.Create(io.MultiWriter(f, h), s.dataDir(), meta, archiveLimits())
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		var refused *backup.RefusedError
		if errors.As(err, &refused) {
			return nil, err
		}
		return nil, fmt.Errorf("writing the archive failed: %w", err)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	final := s.backupPath(fileName)
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	_ = os.WriteFile(final+".sha256", []byte(sum+"  "+fileName+"\n"), 0o600)
	st, _ := os.Stat(final)
	mj, _ := json.Marshal(m)
	b := &api.Backup{ID: id, ServerID: s.id, Kind: kind, CreatedAt: now, FileName: fileName, SizeBytes: st.Size(), SHA256: sum, Location: "on-host",
		MinecraftVersion: m.MinecraftVersion, LevelName: m.LevelName, FileCount: len(m.Files), CreatedBy: actor, Note: note}
	// Downtime belongs to manual backups (set once the server is back); a
	// rollback archive is part of a restore, which records its own downtime.
	_, err = s.db.Exec(`INSERT INTO backups(id, server_id, kind, created_at, file_name, size_bytes, sha256, manifest, created_by, note, downtime_ms) VALUES(?,?,?,?,?,?,?,?,?,?,0)`,
		id, s.id, kind, now.UnixMilli(), fileName, st.Size(), sum, string(mj), actor, note)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// withRefusalHint adds what to do to an error from a backup the archive rules
// refused: rename or remove the named file, or trim the world.
func (s *server) withRefusalHint(err error) error {
	var refused *backup.RefusedError
	if !errors.As(err, &refused) {
		return err
	}
	hint := fmt.Sprintf("Rename or remove that file in %s, then try again.", s.dataDir())
	if refused.File == "" {
		hint = fmt.Sprintf("Remove files the world does not need from %s, then try again.", s.dataDir())
	}
	msg := err.Error()
	return &apiError{Msg: strings.ToUpper(msg[:1]) + msg[1:], Hint: hint}
}

// archiveRefusal is the refusal an archive of the world would get, with what
// to do, found before the server stops for it, so players are not
// disconnected for nothing. unchanged, like "Nothing was replaced.", starts
// the hint. Anything else Check runs into is left to createArchive.
func (s *server) archiveRefusal(unchanged string) error {
	var refused *backup.RefusedError
	err := backup.Check(s.dataDir(), archiveLimits())
	if !errors.As(err, &refused) {
		return nil
	}
	err = s.withRefusalHint(err)
	if e, ok := err.(*apiError); ok && unchanged != "" {
		e.Hint = unchanged + " " + e.Hint
	}
	return err
}

func sanitizeName(s string) string {
	out := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, s)
	if out == "" {
		return "world"
	}
	return out
}

func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// verifyBackup re-reads an archive: whole-file SHA-256 against the record,
// then every file against the manifest.
func (s *server) verifyBackup(id string) (*api.Backup, error) {
	b, err := s.getBackup(id)
	if err != nil {
		return nil, err
	}
	verr := func() error {
		f, err := os.Open(s.backupPath(b.FileName))
		if err != nil {
			return err
		}
		defer f.Close()
		h := sha256.New()
		if _, err := backup.Verify(io.TeeReader(f, h), backup.DefaultLimits()); err != nil {
			return err
		}
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != b.SHA256 {
			return fmt.Errorf("archive SHA-256 %s does not match the recorded %s", got[:16], b.SHA256[:16])
		}
		return nil
	}()
	now := s.now().UnixMilli()
	msg := ""
	if verr != nil {
		msg = verr.Error()
	}
	_, _ = s.db.Exec(`UPDATE backups SET verified = ?, verified_at = ?, verify_error = ? WHERE id = ?`, boolInt(verr == nil), now, msg, id)
	return s.getBackup(id)
}

func (s *server) getBackup(id string) (*api.Backup, error) {
	if err := validBackupID(id); err != nil {
		return nil, err
	}
	list, err := s.listBackups(`id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, errNotFound("Backup")
	}
	return &list[0], nil
}

// listBackups lists the server's backups, newest first, matching an optional
// SQL condition.
func (s *server) listBackups(cond string, args ...any) ([]api.Backup, error) {
	where := `WHERE server_id = ?`
	if cond != "" {
		where += ` AND (` + cond + `)`
	}
	return s.queryBackups(where, append([]any{s.id}, args...)...)
}

func (a *Agent) queryBackups(where string, args ...any) ([]api.Backup, error) {
	rows, err := a.db.Query(`SELECT id, server_id, kind, created_at, file_name, size_bytes, sha256, manifest, verified, verified_at, verify_error, downtime_ms, created_by, downloaded_at, note, saving_paused_ms, duration_ms
		FROM backups `+where+` ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Backup{}
	for rows.Next() {
		var b api.Backup
		var created int64
		var manifest string
		var verified, verifiedAt, downloaded sql.NullInt64
		if err := rows.Scan(&b.ID, &b.ServerID, &b.Kind, &created, &b.FileName, &b.SizeBytes, &b.SHA256, &manifest, &verified, &verifiedAt, &b.VerifyError, &b.DowntimeMs, &b.CreatedBy, &downloaded, &b.Note, &b.SavingPausedMs, &b.DurationMs); err != nil {
			return nil, err
		}
		b.CreatedAt = time.UnixMilli(created).UTC()
		b.Location = "on-host"
		var m backup.Manifest
		if json.Unmarshal([]byte(manifest), &m) == nil {
			b.MinecraftVersion, b.LevelName, b.FileCount = m.MinecraftVersion, m.LevelName, len(m.Files)
			b.Method = string(backup.MethodOf(m))
		}
		if verified.Valid {
			v := verified.Int64 == 1
			b.Verified = &v
		}
		if verifiedAt.Valid {
			t := time.UnixMilli(verifiedAt.Int64).UTC()
			b.VerifiedAt = &t
		}
		if downloaded.Valid {
			t := time.UnixMilli(downloaded.Int64).UTC()
			b.DownloadedAt = &t
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// errNotOnlineForBackup refuses a backup of a server that is starting or
// stopping: its console can't pause saving yet, and stopping it isn't asked.
func (s *server) errNotOnlineForBackup() error {
	return errConflict(s.name()+" is starting or stopping, so it can't be backed up right now.", "Wait until the server is online, then try again.")
}

// backupOp backs up the world, then verifies the archive. An online server
// keeps its players: backup.Take pauses world saving only while it copies the
// world. With stopped, a running server is stopped for the backup (players
// online are warned in chat first) and started again, and its downtime is
// measured from the stop request until it is online again. A world a restore
// would refuse is refused before the server stops or saving pauses. A server
// that isn't running is backed up as it is.
func (s *server) backupOp(ctx context.Context, h *opHandle, actor, note string, stopped bool) error {
	sc, err := s.serverConfig()
	if err != nil {
		return err
	}
	if sc == nil {
		return errNotCreated()
	}
	_, running, err := s.containerRunning(ctx)
	if err != nil {
		return err
	}
	if running && !stopped && !s.online(ctx) {
		return s.errNotOnlineForBackup()
	}
	now := s.now().UTC()
	id, fileName := s.archiveName(now)
	o := backup.Options{
		DataDir:    s.dataDir(),
		Archive:    s.backupPath(fileName),
		StagingDir: s.cfg.StagingDir(),
		Meta:       s.archiveMeta(*sc, now),
		Limits:     archiveLimits(),
		Reserve:    minFreeAfterBackup,
		DiskFree: func(dir string) (int64, error) {
			free, _, err := s.opts.DiskUsage(dir)
			return free, err
		},
		State: backup.ServerStopped,
		IsRunning: func(ctx context.Context) (bool, error) {
			_, running, err := s.containerRunning(ctx)
			return running, err
		},
		OnPhase: func(p backup.Phase) { h.phase(string(p)) },
		Now:     s.now,
	}
	restart := running && stopped
	if running && !stopped {
		o.State, o.Console = backup.ServerRunning, rconConsole{s}
		o.OnPauseChange = func(paused bool) {
			s.setSavingPaused(paused)
			h.set("savingPaused", paused)
		}
	}
	start := s.now()
	if restart {
		if err := s.archiveRefusal(""); err != nil {
			return err
		}
		if err := backup.CheckSpace(ctx, o); err != nil {
			return s.backupFailed(h, err)
		}
		h.set("stopped", true)
		s.warnBeforeBackup(ctx)
		if err := s.stopServer(ctx, h); err != nil {
			return err
		}
	}
	res, err := backup.Take(ctx, o)
	var b *api.Backup
	if err == nil {
		b, err = s.recordBackup(id, fileName, actor, note, now, res)
	}
	if restart {
		h.phase("restarting")
		if serr := s.startServer(ctx, h, *sc); serr != nil {
			if err != nil {
				return s.backupFailed(h, err)
			}
			return &apiError{Msg: "The backup was saved, but the server did not start again: " + serr.Error(), Hint: "Press Start on the Overview."}
		}
	}
	if err != nil {
		return s.backupFailed(h, err)
	}
	downtime := int64(0)
	if restart {
		downtime = s.now().Sub(start).Milliseconds()
		_, _ = s.db.Exec(`UPDATE backups SET downtime_ms = ? WHERE id = ?`, downtime, b.ID)
	}
	h.setAll(map[string]any{
		"backupId": b.ID, "method": string(res.Method), "savingPausedMs": res.Paused.Milliseconds(), "durationMs": res.Took.Milliseconds(),
		"downtimeMs": downtime, "stagedBytes": res.Staged, "savingWasOff": res.SavingWasOff,
	})
	h.phase("verifying")
	vb, err := s.verifyBackup(b.ID)
	if err != nil {
		return err
	}
	if vb.Verified == nil || !*vb.Verified {
		return &apiError{Msg: "The backup was written but failed verification: " + vb.VerifyError, Hint: "Try again; if it keeps failing, check the disk for errors."}
	}
	s.audit(actor, "backup.created", b.ID, "succeeded", fmt.Sprintf("%s sha256 %s method %s saving paused %s took %s downtime %dms",
		b.FileName, b.SHA256, res.Method, res.Paused.Round(time.Millisecond), res.Took.Round(time.Millisecond), downtime))
	s.alert(discord.BackupSucceeded(vb.SizeBytes))
	return nil
}

// recordBackup stores a manual backup backup.Take finished: the checksum file
// beside the archive, and its row. An archive without a row would never be
// listed or deleted, so it goes if the row can't be written.
func (s *server) recordBackup(id, fileName, actor, note string, created time.Time, res *backup.Result) (*api.Backup, error) {
	final := s.backupPath(fileName)
	_ = os.WriteFile(final+".sha256", []byte(res.SHA256+"  "+fileName+"\n"), 0o600)
	mj, _ := json.Marshal(res.Manifest)
	_, err := s.db.Exec(`INSERT INTO backups(id, server_id, kind, created_at, file_name, size_bytes, sha256, manifest, created_by, note, downtime_ms, saving_paused_ms, duration_ms)
		VALUES(?,?,'manual',?,?,?,?,?,?,?,0,?,?)`,
		id, s.id, created.UnixMilli(), fileName, res.Size, res.SHA256, string(mj), actor, note, res.Paused.Milliseconds(), res.Took.Milliseconds())
	if err != nil {
		os.Remove(final)
		os.Remove(final + ".sha256")
		return nil, err
	}
	return s.getBackup(id)
}

// backupFailed is the operation's error for a failed backup. Its kind and
// facts go in the operation's detail, so the UI can say it in its own words
// and offer the matching action.
func (s *server) backupFailed(h *opHandle, err error) error {
	var e *backup.Error
	if !errors.As(err, &e) {
		return err
	}
	d := map[string]any{"errorKind": string(e.Kind)}
	if e.Kind == backup.KindInsufficientSpace {
		// Lets the UI drop this failure once enough space is free again.
		d["neededBytes"], d["freeBytes"] = e.NeededBytes, e.FreeBytes
	}
	if e.File != "" {
		d["file"] = e.File
	}
	if e.Command != "" {
		d["command"], d["reply"] = e.Command, e.Reply
	}
	if e.Timeout > 0 {
		d["timeoutMs"] = e.Timeout.Milliseconds()
	}
	if e.SavingPaused {
		d["savingPaused"] = true
	}
	h.setAll(d)
	return &apiError{Code: string(e.Kind), Msg: e.Msg, Hint: e.Hint}
}

// setSavingPaused records whether a backup may have left world saving off on
// the server, so the reconciler (and a restarted agent) can turn it back on.
// A pause already recorded keeps its time: progress since then is at risk.
func (s *server) setSavingPaused(paused bool) {
	var err error
	if paused {
		_, err = s.db.Exec(`UPDATE servers SET saving_paused_since = COALESCE(saving_paused_since, ?) WHERE id = ?`, s.now().UnixMilli(), s.id)
	} else {
		_, err = s.db.Exec(`UPDATE servers SET saving_paused_since = NULL WHERE id = ?`, s.id)
	}
	if err != nil {
		s.log.Error("could not record whether world saving is paused", "server", s.id, "err", err)
	}
}

// savingPausedSince is when a backup left world saving off, or nil.
func (s *server) savingPausedSince() *time.Time {
	var ms sql.NullInt64
	if s.db.QueryRow(`SELECT saving_paused_since FROM servers WHERE id = ?`, s.id).Scan(&ms) != nil || !ms.Valid {
		return nil
	}
	t := time.UnixMilli(ms.Int64).UTC()
	return &t
}

// resumeSaving turns world saving back on after a backup left it off. A
// container that stopped or started since then saves again by itself, so
// only an online server that has run all along gets save-on. It holds the
// operation lock, so save-on can't land in the middle of a new backup.
func (s *server) resumeSaving(ctx context.Context, c docker.ContainerJSON, running bool) {
	since := s.savingPausedSince()
	if since == nil {
		return
	}
	release, ok := s.holdOpLock()
	if !ok {
		return
	}
	defer release()
	if started, ok := c.State.Started(); !running || (ok && started.After(*since)) {
		s.setSavingPaused(false)
		return
	}
	s.mu.Lock()
	due := !s.now().Before(s.nextResume)
	s.mu.Unlock()
	if !due || !s.online(ctx) {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := backup.ResumeSaving(cctx, rconConsole{s}); err != nil {
		s.log.Warn("could not turn world saving back on", "server", s.id, "err", err)
		s.mu.Lock()
		s.nextResume = s.now().Add(resumeRetry)
		s.mu.Unlock()
		return
	}
	s.setSavingPaused(false)
	s.recordEvent(s.now(), "saving_resumed", "", "playkeeper", "")
}

// resumeRetry is how long the reconciler waits after save-on failed.
const resumeRetry = 30 * time.Second

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// inWords says a wait the way a chat line does, rounded up to whole seconds:
// "3 seconds", "1 minute".
func inWords(d time.Duration) string {
	n, unit := int((d+time.Second-1)/time.Second), "second"
	if n < 1 {
		n = 1
	}
	if n%60 == 0 {
		n, unit = n/60, "minute"
	}
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// --- restore ---

type stage struct {
	dir      string
	archive  string
	data     string
	manifest backup.Manifest
	preview  api.RestorePreview
}

func (a *Agent) stageDir(id string) string { return filepath.Join(a.cfg.StagingDir(), id) }

// pruneStages deletes restore stages left by an earlier run of the agent (a
// preview nobody applied or discarded, or a restore that is over). Each holds
// an archive copy and its extracted world, and nothing can be using them yet,
// except a restore its operation is about to finish. A swap its restore left
// unsettled is settled first; while that fails its stage is kept, as it may
// hold the restored world.
func (a *Agent) pruneStages() {
	entries, _ := os.ReadDir(a.cfg.StagingDir())
	removed := 0
	for _, e := range entries {
		dir := filepath.Join(a.cfg.StagingDir(), e.Name())
		if a.resuming(dir) {
			continue
		}
		if err := a.settleSwap(dir); err != nil {
			a.log.Warn("could not settle an interrupted restore, so its stage is kept", "stage", e.Name(), "err", err)
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			a.log.Warn("could not remove a leftover restore stage", "stage", e.Name(), "err", err)
			continue
		}
		removed++
	}
	if removed > 0 {
		a.log.Info("removed leftover restore stages", "count", removed)
	}
}

// swapJournal records a restore's world swap in its stage, from before the
// live world moves until the restore is kept or undone, so that an agent that
// stopped in the middle finishes the restore, or its undo, when it starts
// again.
type swapJournal struct {
	ServerID string `json:"serverId"`
	OpID     string `json:"opId"`
	Actor    string `json:"actor"`
	// Aside and Failed name the copies next to the live directory: the
	// previous world, and a restored world that had to make way for it. The
	// previous world's copy is removed only once the restore is kept.
	Aside     string            `json:"aside"`
	Failed    string            `json:"failed"`
	HadLive   bool              `json:"hadLive"`
	StartedAt time.Time         `json:"startedAt"`
	Previous  *api.ServerConfig `json:"previous,omitempty"`
	Restored  api.ServerConfig  `json:"restored"`
	SHA256    string            `json:"sha256"`
	Detail    string            `json:"detail"`
	State     swapState         `json:"state"`
	// Why is what made the restore undo itself, for its operation's error.
	Why string `json:"why,omitempty"`
}

// swapState is how far a restore's world swap got.
type swapState string

const (
	// swapMoving: the worlds are being moved and the restored settings saved.
	swapMoving swapState = "moving"
	// swapChecking: the restored world and its settings are in place, and the
	// restore is kept once the server is online.
	swapChecking swapState = "checking"
	// swapKept: the restored world was online, so the restore is kept.
	swapKept swapState = "kept"
	// swapReverting: the previous world and its settings are being put back.
	swapReverting swapState = "reverting"
	// swapDone is never written: the journal's stage can go.
	swapDone swapState = "done"
)

const swapJournalFile = "swap.json"

// reWorldCopy matches the copies a restore leaves next to a live world
// directory named "data".
var reWorldCopy = regexp.MustCompile(`^data\.(replaced|failed-restore)-([0-9]{8}-[0-9]{6})$`)

func writeSwapJournal(stageDir string, j *swapJournal) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	tmp := filepath.Join(stageDir, swapJournalFile+".new")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, filepath.Join(stageDir, swapJournalFile))
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// readSwapJournal reads a stage's journal, or nil if it has none.
func readSwapJournal(stageDir string) (*swapJournal, error) {
	b, err := os.ReadFile(filepath.Join(stageDir, swapJournalFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var j swapJournal
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, fmt.Errorf("unreadable swap journal: %w", err)
	}
	aside, failed := reWorldCopy.FindStringSubmatch(j.Aside), reWorldCopy.FindStringSubmatch(j.Failed)
	if !reServerID.MatchString(j.ServerID) || aside == nil || aside[1] != "replaced" || failed == nil || failed[1] != "failed-restore" {
		return nil, fmt.Errorf("swap journal for server %q with invalid world copy names %q and %q", j.ServerID, j.Aside, j.Failed)
	}
	switch j.State {
	case swapMoving, swapChecking, swapKept, swapReverting:
		return &j, nil
	}
	return nil, fmt.Errorf("swap journal in an unknown state %q", j.State)
}

// settleSwap settles the world swap in the journal of a stage whose restore
// is over, if it has one: a kept restore loses the previous world's copy, and
// one that stopped while moving the worlds or undoing itself gets the
// previous world and its settings back. A restore that was checking its
// restored world keeps both copies, as its operation reported. It errors
// while the stage may hold the only copy of the restored world.
func (a *Agent) settleSwap(stageDir string) error {
	j, err := readSwapJournal(stageDir)
	if err != nil || j == nil {
		return err
	}
	s := a.serverByID(j.ServerID)
	if s == nil {
		return nil
	}
	switch j.State {
	case swapKept:
		return os.RemoveAll(s.copyPath(j.Aside))
	case swapChecking:
		return nil
	case swapMoving, swapReverting:
		if !j.HadLive || j.Previous == nil {
			if !dirExists(s.dataDir()) && dirExists(filepath.Join(stageDir, "data")) {
				return fmt.Errorf("the restored world is only in the stage, and the world directory %s is missing", s.dataDir())
			}
			return nil
		}
		ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
		defer cancel()
		if _, running, err := s.containerRunning(ctx); err != nil || running {
			return fmt.Errorf("the server must be stopped to put the previous world back (running %v, %v)", running, err)
		}
		if err := s.putPreviousBack(j); err != nil {
			return err
		}
		a.log.Info("put the previous world back after an interrupted restore", "server", s.id)
		return nil
	}
	return fmt.Errorf("swap journal in an unknown state %q", j.State)
}

func (s *server) copyPath(name string) string { return filepath.Join(s.dir(), name) }

// putPreviousBack moves the previous world back into the live directory, a
// restored world in the way to the failed-restore copy, and saves the
// previous settings. Run again after an interruption, it finishes the job:
// the previous world's copy is gone only once it is back in place.
func (s *server) putPreviousBack(j *swapJournal) error {
	live, aside, failed := s.dataDir(), s.copyPath(j.Aside), s.copyPath(j.Failed)
	if dirExists(aside) {
		if dirExists(live) {
			if err := renameDir(live, failed); err != nil {
				return fmt.Errorf("moving the restored world out of the way failed (%v), so nothing was deleted: the restored world is at %s and the previous world at %s", err, live, aside)
			}
		}
		if err := renameDir(aside, live); err != nil {
			where := ""
			if dirExists(failed) {
				where = " and the restored world at " + failed
			}
			return fmt.Errorf("putting the previous world back failed (%v), so nothing was deleted: the previous world is at %s%s", err, aside, where)
		}
	}
	if !dirExists(live) {
		return fmt.Errorf("the world directory %s is missing", live)
	}
	if j.Previous != nil {
		return s.saveServerConfig(*j.Previous)
	}
	return nil
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// newestPreviousWorld is the newest previous world a restore moved aside
// from the live directory, or "".
func (s *server) newestPreviousWorld() string {
	for _, c := range s.worldCopies() {
		if c.Kind == api.WorldCopyPrevious {
			return filepath.Join(s.dir(), c.Name)
		}
	}
	return ""
}

// worldCopies lists the world folders restores left next to the live one,
// newest first, without their sizes.
func (s *server) worldCopies() []api.WorldCopy {
	entries, _ := os.ReadDir(s.dir())
	out := []api.WorldCopy{}
	for _, e := range entries {
		m := reWorldCopy.FindStringSubmatch(e.Name())
		if m == nil || !e.IsDir() {
			continue
		}
		at, err := time.Parse("20060102-150405", m[2])
		if err != nil {
			continue
		}
		kind := api.WorldCopyPrevious
		if m[1] == "failed-restore" {
			kind = api.WorldCopyFailedRestore
		}
		out = append(out, api.WorldCopy{Name: e.Name(), Kind: kind, CreatedAt: at.UTC()})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

func (s *server) hWorldCopies(w http.ResponseWriter, r *http.Request) {
	list := s.worldCopies()
	for i := range list {
		list[i].SizeBytes = dirSize(filepath.Join(s.dir(), list[i].Name))
	}
	writeJSON(w, http.StatusOK, list)
}

// hWorldCopyDelete discards one world copy. Nothing is discarded while the
// live world folder is missing, because a copy may then be the only world.
func (s *server) hWorldCopyDelete(w http.ResponseWriter, r *http.Request) {
	actor, err := validActor(r.URL.Query().Get("actor"))
	if err != nil {
		writeError(w, err)
		return
	}
	name := r.PathValue("name")
	if !reWorldCopy.MatchString(name) {
		writeError(w, errInvalid("invalid world copy name"))
		return
	}
	release, ok := s.holdOpLock()
	if !ok {
		writeError(w, s.busyError())
		return
	}
	defer release()
	path := filepath.Join(s.dir(), name)
	if st, err := os.Lstat(path); err != nil || !st.IsDir() {
		writeError(w, errNotFound("World copy"))
		return
	}
	if !dirExists(s.dataDir()) {
		writeError(w, errConflict("The world folder is missing, so this copy may be the only one of your world.", "Move the copy you want to keep back to "+s.dataDir()+", then try again."))
		return
	}
	if err := os.RemoveAll(path); err != nil {
		writeError(w, fmt.Errorf("could not discard the world copy: %w", err))
		return
	}
	s.audit(actor, "world_copy.deleted", name, "succeeded", "")
	w.WriteHeader(http.StatusNoContent)
}

// stageArchive copies an archive into staging, then verifies and extracts it
// there, to replace target's world or, with no target, to make a new server.
// No world is touched; failures delete the staging dir.
func (a *Agent) stageArchive(src io.Reader, source string, limit int64, target *server) (*api.RestorePreview, error) {
	id := randomSecret(8)
	dir := a.stageDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	fail := func(err error) (*api.RestorePreview, error) {
		os.RemoveAll(dir)
		return nil, err
	}
	arch := filepath.Join(dir, "archive.tar.gz")
	f, err := os.OpenFile(arch, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fail(err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(src, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fail(errInvalid("Upload failed: %v", err))
	}
	if n > limit {
		return fail(&apiError{Status: http.StatusRequestEntityTooLarge, Code: api.CodeInsufficientSpace, Msg: "The archive is larger than the free disk space allows.", Hint: "Free disk space and try again."})
	}
	lim := backup.DefaultLimits()
	if free, _, err := a.opts.DiskUsage(dir); err == nil && free-minFreeAfterBackup < lim.MaxTotalBytes {
		lim.MaxTotalBytes = free - minFreeAfterBackup
	}
	af, err := os.Open(arch)
	if err != nil {
		return fail(err)
	}
	m, err := backup.Extract(af, filepath.Join(dir, "data"), lim)
	af.Close()
	if err != nil {
		return fail(&apiError{Status: http.StatusUnprocessableEntity, Code: api.CodeInvalid, Msg: "This file cannot be restored: " + err.Error(), Hint: "Nothing was changed. Use an archive downloaded from Playkeeper's World page."})
	}
	p := a.buildPreview(id, source, n, hex.EncodeToString(h.Sum(nil)), m, target)
	pj, _ := json.Marshal(stageFile{p, m})
	if err := os.WriteFile(filepath.Join(dir, "stage.json"), pj, 0o600); err != nil {
		return fail(err)
	}
	return &p, nil
}

// stageFile is a stage's stage.json: its preview when it was staged, and the
// archive's manifest.
type stageFile struct {
	Preview  api.RestorePreview `json:"preview"`
	Manifest backup.Manifest    `json:"manifest"`
}

func readStageFile(dir string) (*stageFile, error) {
	b, err := os.ReadFile(filepath.Join(dir, "stage.json"))
	if err != nil {
		return nil, err
	}
	var f stageFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

func (a *Agent) buildPreview(id, source string, size int64, sum string, m backup.Manifest, target *server) api.RestorePreview {
	p := api.RestorePreview{
		ID: id, ServerID: serverIDOf(target), Source: source, ReceivedAt: a.now().UTC(), SizeBytes: size, SHA256: sum, Compatible: true,
		Problems: []string{}, Warnings: []string{},
		Manifest: &api.ManifestSummary{
			CreatedAt: m.CreatedAt, PlaykeeperVersion: m.PlaykeeperVersion, MinecraftVersion: m.MinecraftVersion, PaperBuild: m.PaperBuild,
			VersionID: m.VersionID, LevelName: m.LevelName, FileCount: len(m.Files), TotalBytes: m.TotalBytes, SourceInstall: m.SourceInstall, Settings: m.Settings,
		},
		NotRestored: []string{
			"Playkeeper sign-in accounts and sessions (this host keeps its own)",
			"Player analytics, sessions and the audit log from the source host",
			"Server jar and libraries (downloaded again from PaperMC after checksum verification)",
			"The RCON password (this host generates its own)",
		},
	}
	rt, err := a.restoreTargetFor(a.ctx, m)
	entry := rt.entry
	if m.Type != "" && m.Type != api.TypePaper {
		p.Manifest.Type, p.Manifest.Build = m.Type, m.Build
		p.NotRestored[2] = "Server jar and libraries (downloaded again and checked against their published checksums)"
	}
	switch {
	case err != nil:
		p.Compatible = false
		p.Problems = append(p.Problems, fmt.Sprintf("This backup is from Minecraft %s, which cannot be restored here: %v.", m.MinecraftVersion, err))
	case rt.typ != api.TypePaper:
		if rt.warning != "" {
			p.Warnings = append(p.Warnings, rt.warning)
		}
	case entry.PaperBuild != m.PaperBuild:
		p.Warnings = append(p.Warnings, fmt.Sprintf("The backup used Paper build %d; this host will run build %d of the same Minecraft version, the latest stable one.", m.PaperBuild, entry.PaperBuild))
	}
	switch {
	case !entry.Experimental:
	case rt.typ == api.TypePaper:
		p.Warnings = append(p.Warnings, fmt.Sprintf("Paper %s build %d is experimental (%s), like the build the backup was made with.", entry.MinecraftVersion, entry.PaperBuild, strings.ToLower(entry.Channel)))
	default:
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s %s is experimental (%s), like the build the backup was made with.", typeName(rt.typ), entry.Build, entry.Channel))
	}
	if m.SourceInstall != "" && m.SourceInstall == shortID(a.cfg.InstallID) {
		p.Source += " (made on this host)"
	} else {
		p.Warnings = append(p.Warnings, "This backup was made on a different Playkeeper host.")
	}
	mem, _ := strconv.Atoi(m.Settings["memoryMB"])
	if err := a.validMemory(mem, serverIDOf(target)); err != nil {
		_, rec, _ := a.memoryFor(serverIDOf(target))
		if rec == 0 {
			p.Compatible = false
			p.Problems = append(p.Problems, "There is not enough memory left on this machine for another Minecraft server.")
		} else {
			p.Warnings = append(p.Warnings, fmt.Sprintf("The backup's memory budget (%d MB) does not fit here next to the other servers; %d MB will be used.", mem, rec))
		}
		mem = rec
	}
	p.MemoryMB = mem
	p.NeedsEULA = target == nil
	cw := api.CurrentWorld{}
	if target != nil {
		live := target.dataDir()
		if st, err := os.Stat(filepath.Join(live, backup.LevelName(live))); err == nil && st.IsDir() {
			cw.Exists = true
			cw.LevelName = backup.LevelName(live)
			cw.SizeBytes = allowlistedSize(live)
		}
	}
	p.CurrentWorld = cw
	p.WillCreateRollback = cw.Exists
	if cw.Exists {
		p.ConfirmPhrase = "replace " + cw.LevelName
		p.Steps = []string{
			"Stop the running server (players are disconnected; downtime starts)",
			"Save a rollback archive of the current world \"" + cw.LevelName + "\"",
			"Replace the current world with \"" + m.LevelName + "\" from the backup",
			"Start " + labelFor(m.MinecraftVersion) + " and wait until it is online",
			"If the restored world fails to start, put the previous world back automatically",
		}
	} else {
		p.ConfirmPhrase = "restore"
		p.Steps = []string{
			"Install the world \"" + m.LevelName + "\" from the backup",
			"Download " + labelFor(m.MinecraftVersion) + " from PaperMC and verify its checksum",
			"Start the server and wait until it is online",
		}
	}
	return p
}

func labelFor(mc string) string { return "Paper " + mc }

func (a *Agent) loadStage(id string) (*stage, error) {
	if !reStageID.MatchString(id) {
		return nil, errInvalid("invalid restore id")
	}
	dir := a.stageDir(id)
	s, err := readStageFile(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errNotFound("Restore preview")
	}
	if err != nil {
		return nil, err
	}
	st := &stage{dir: dir, archive: filepath.Join(dir, "archive.tar.gz"), data: filepath.Join(dir, "data"), manifest: s.Manifest}
	var target *server
	if s.Preview.ServerID != "" {
		if target = a.serverByID(s.Preview.ServerID); target == nil {
			return nil, errNotFound("The server this restore was for")
		}
	}
	st.preview = a.buildPreview(id, s.Preview.Source, s.Preview.SizeBytes, s.Preview.SHA256, s.Manifest, target)
	st.preview.ReceivedAt = s.Preview.ReceivedAt
	return st, nil
}

// restoreAsNewServer records the server a restore creates, stopped and with
// the backup's settings, and starts the restore that puts its world in place.
func (a *Agent) restoreAsNewServer(st *stage, req api.RestoreApplyRequest, name, actor string, restore func(s *server) func(ctx context.Context, h *opHandle) error) (*api.Operation, error) {
	m := st.manifest
	rt, err := a.restoreTargetFor(a.ctx, m)
	if err != nil {
		return nil, errInvalid("This backup cannot be restored: %v.", err)
	}
	mem := st.preview.MemoryMB
	if req.MemoryMB != 0 {
		mem = req.MemoryMB
	}
	if name == "" {
		if n, err := validName(m.Settings["name"]); err == nil {
			name = a.uniqueName(n)
		}
	}
	sc := a.restoredConfigFor(m, rt, mem, nil, actor)
	_, op, err := a.addServer(newServerSpec{name: name, typ: rt.typ, config: sc, desired: api.DesiredStopped, actor: actor}, "restore", restore)
	return op, err
}

// restoredConfig is restoredConfigFor on the Paper build entry, the only
// software Playkeeper 0.3.0 ran.
func (a *Agent) restoredConfig(m backup.Manifest, entry api.CatalogEntry, mem int, prev *api.ServerConfig, actor string) api.ServerConfig {
	return a.restoredConfigFor(m, restoreTarget{typ: api.TypePaper, entry: entry}, mem, prev, actor)
}

// restoredConfigFor is a server's settings after restoring the backup with
// manifest m on rt's software: the backup's world, version and game
// settings, and from prev what belongs to the server rather than its world,
// including a modpack it finished installing. The restored server.properties
// carries the backup's game settings, so none chosen since override them.
func (a *Agent) restoredConfigFor(m backup.Manifest, rt restoreTarget, mem int, prev *api.ServerConfig, actor string) api.ServerConfig {
	now := a.now().UTC()
	sc := rt.config(api.ServerConfig{
		MemoryMB: mem, HeapMB: minecraft.HeapMB(mem), LevelName: m.LevelName, MOTD: validMOTDOr(m.Settings["motd"]),
		MaxPlayers: manifestMaxPlayers(m), Whitelist: true, CreatedAt: now, EULAAcceptedAt: now, EULAAcceptedBy: actor,
	})
	if prev != nil {
		sc.EULAAcceptedAt, sc.EULAAcceptedBy, sc.CreatedAt, sc.PlayStyle = prev.EULAAcceptedAt, prev.EULAAcceptedBy, prev.CreatedAt, prev.PlayStyle
		if prev.Modpack != nil && !prev.Modpack.Pending {
			sc.Modpack = prev.Modpack
		}
	}
	return sc
}

func manifestMaxPlayers(m backup.Manifest) int {
	n, _ := strconv.Atoi(m.Settings["maxPlayers"])
	if n < 1 || n > 100 {
		return 10
	}
	return n
}

// uniqueName is name, or name with a number after it if another server has it.
func (a *Agent) uniqueName(name string) string {
	for i := 1; ; i++ {
		n := name
		if i > 1 {
			n = fmt.Sprintf("%s %d", name, i)
		}
		if !a.nameTaken(n, "") {
			return n
		}
	}
}

// renameDir moves directories during a restore; tests replace it to make one
// step of the world swap fail.
var renameDir = os.Rename

// restoreStep marks the steps of a restore after the world swap; tests
// replace it to stop the agent there.
var restoreStep = func(ctx context.Context, step string) {}

// restoreOp replaces the world with a staged, verified archive. A rollback
// archive of the current world is written first, and a restored world that
// fails to start is swapped back out automatically. The stage is deleted when
// the live world is known to be good: the restore finished, nothing was
// replaced, or the previous world is back. If putting it back fails, the
// stage and the aside copy both stay and the error names them. The stage's
// swap journal lets the next start finish a restore the agent stopped in, and
// settle one that could not undo itself.
func (s *server) restoreOp(ctx context.Context, h *opHandle, st *stage, req api.RestoreApplyRequest, actor string) error {
	worldSafe := true
	defer func() {
		if worldSafe {
			os.RemoveAll(st.dir)
		}
	}()
	m := st.manifest
	h.set("stage", st.preview.ID)
	rt, err := s.restoreTargetFor(ctx, m)
	if err != nil {
		return errInvalid("This backup cannot be restored: %v.", err)
	}
	prev, err := s.serverConfig()
	if err != nil {
		return err
	}
	start := s.now()
	var rollback *api.Backup
	wasRunning := false
	if prev != nil {
		if st.preview.CurrentWorld.Exists {
			if err := s.archiveRefusal("Nothing was replaced."); err != nil {
				return err
			}
		}
		_, wasRunning, _ = s.containerRunning(ctx)
		if err := s.stopServer(ctx, h); err != nil {
			return err
		}
		if st.preview.CurrentWorld.Exists {
			h.phase("saving_rollback")
			rb, err := s.saveVerifiedRollback(*prev, actor, "Automatic rollback archive before restoring backup "+st.preview.SHA256[:12])
			if err != nil {
				s.startPrevious(ctx, h, prev, wasRunning)
				return s.withRefusalHint(fmt.Errorf("could not save a verified rollback archive of the current world, so nothing was replaced: %w", err))
			}
			rollback = rb
			h.set("rollbackBackupId", rb.ID)
		}
	}
	h.phase("replacing_world")
	live := s.dataDir()
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		return err
	}
	mem := st.preview.MemoryMB
	if req.MemoryMB != 0 {
		mem = req.MemoryMB
	}
	stamp := s.now().UTC().Format("20060102-150405")
	j := &swapJournal{
		ServerID: s.id, OpID: h.op.ID, Actor: actor, Aside: "data.replaced-" + stamp, Failed: "data.failed-restore-" + stamp,
		StartedAt: start.UTC(), Previous: prev, Restored: s.restoredConfigFor(m, rt, mem, prev, actor), SHA256: st.preview.SHA256,
		Detail: fmt.Sprintf("restored %s (sha256 %s)", m.LevelName, st.preview.SHA256), State: swapMoving,
	}
	var prevPack *api.ResourcePackOffer
	if prev != nil {
		prevPack = prev.ResourcePack
	}
	j.Restored.ResourcePack = restoredPackOffer(prevPack, st.data)
	if err := s.restoredVoiceChat(&j.Restored, prev, m, st.data); err != nil {
		s.startPrevious(ctx, h, prev, wasRunning)
		return fmt.Errorf("could not give the restored voice chat its port, so nothing was replaced: %w", err)
	}
	if rollback != nil {
		j.Detail += "; rollback archive " + rollback.ID
	}
	_, err = os.Stat(live)
	j.HadLive = err == nil
	if err := writeSwapJournal(st.dir, j); err != nil {
		s.startPrevious(ctx, h, prev, wasRunning)
		return fmt.Errorf("could not save the restore's progress file, so nothing was replaced: %w", err)
	}
	// A restored world that has to make way goes to failedAt, next to the live
	// directory and outside the staging folder the agent clears at start.
	aside, failedAt := s.copyPath(j.Aside), s.copyPath(j.Failed)
	if j.HadLive {
		if err := renameDir(live, aside); err != nil {
			return err
		}
	}
	worldSafe = false
	// putBack moves the previous world back into place once the restored one
	// is out of the way, at restoredAt. If that fails the live directory is
	// missing: nothing is deleted, the restored copy leaves the stage, and the
	// error names where both copies are.
	putBack := func(restoredAt string, cause error) error {
		if !j.HadLive {
			worldSafe = true
			return nil
		}
		err := renameDir(aside, live)
		if err == nil {
			worldSafe = true
			return nil
		}
		if restoredAt == st.data && renameDir(st.data, failedAt) == nil {
			restoredAt = failedAt
		}
		return fmt.Errorf("%v; putting the previous world back also failed (%v), so nothing was deleted: the previous world is at %s and the restored world at %s", cause, err, aside, restoredAt)
	}
	// undoing records that the restore is being undone, so an agent that
	// stops meanwhile finishes the undo when it starts again.
	undoing := func(cause error) {
		if !j.HadLive {
			return
		}
		j.State, j.Why = swapReverting, sentence(cause.Error())
		if err := writeSwapJournal(st.dir, j); err != nil {
			s.log.Warn("could not save the restore's progress file", "server", s.id, "err", err)
		}
	}
	if err := renameDir(st.data, live); err != nil {
		cause := fmt.Errorf("could not move the restored world into place: %w", err)
		undoing(cause)
		if perr := putBack(st.data, cause); perr != nil {
			return perr
		}
		return err
	}
	restoreStep(ctx, "moved")
	if err := chownTree(live, s.cfg.GameUID, s.cfg.GameGID); err != nil {
		s.log.Warn("chown restored world", "err", err)
	}
	err = s.saveServerConfig(j.Restored)
	if err == nil {
		j.State = swapChecking
		if err = writeSwapJournal(st.dir, j); err != nil && prev != nil {
			_ = s.saveServerConfig(*prev)
		}
	}
	if err != nil {
		cause := fmt.Errorf("could not record the restored server's settings: %w", err)
		undoing(cause)
		if rerr := renameDir(live, failedAt); rerr != nil {
			where := "there was no previous world"
			if j.HadLive {
				where = "the previous world is at " + aside
			}
			return fmt.Errorf("%v; moving the restored world out of the way also failed (%v), so nothing was deleted: the restored world is at %s and %s", cause, rerr, live, where)
		}
		if perr := putBack(failedAt, cause); perr != nil {
			return perr
		}
		os.RemoveAll(failedAt)
		s.startPrevious(ctx, h, prev, wasRunning)
		if !j.HadLive {
			return fmt.Errorf("%v, so the restore was undone", cause)
		}
		return fmt.Errorf("could not record the restored server's settings, so the previous world was put back: %w", err)
	}
	restoreStep(ctx, "checking")
	err = s.finishRestore(ctx, h, st.dir, j)
	worldSafe = j.State == swapDone
	return err
}

// finishRestore starts the restored world, whose settings are saved, and
// keeps it once it is online; a restored world that does not start is swapped
// back out. The restore is undone only because its world did not start,
// never because the agent stopped: then the operation stays running and the
// journal as it is, and the next start checks the restored world again.
func (s *server) finishRestore(ctx context.Context, h *opHandle, stageDir string, j *swapJournal) error {
	_ = s.setDesired(api.DesiredRunning)
	s.holdRestoredPregen(j.Restored)
	err := s.startServer(ctx, h, j.Restored)
	if err == nil {
		err = s.waitOnline(ctx, h)
	}
	if err != nil && s.stopping() {
		h.continues = true
		return nil
	}
	if err != nil {
		if !j.HadLive || j.Previous == nil {
			j.State = swapDone
			s.forgetPregen()
			return err
		}
		j.Why = "The restored world did not start (" + err.Error() + ")."
		return s.revertRestore(h, stageDir, j)
	}
	restoreStep(ctx, "online")
	// The previous world's copy goes only once the journal says the restore
	// is kept, so an agent that stops while deleting it never puts back a
	// partly deleted world.
	j.State = swapKept
	keepCopy := false
	if err := writeSwapJournal(stageDir, j); err != nil {
		keepCopy = true
		s.log.Warn("could not save the restore's progress file, so the previous world's copy is kept", "server", s.id, "err", err)
	}
	restoreStep(ctx, "kept")
	return s.keepRestore(h, j, keepCopy)
}

// revertRestore puts the previous world and its settings back after the
// restored world did not start, and starts the previous world if the server
// should be running. j.Why says what went wrong, for the operation's error.
// It has its own time limit, as the restore's may be used up by then.
func (s *server) revertRestore(h *opHandle, stageDir string, j *swapJournal) error {
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Minute)
	defer cancel()
	h.phase("reverting")
	live, aside := s.dataDir(), s.copyPath(j.Aside)
	j.State = swapReverting
	if err := writeSwapJournal(stageDir, j); err != nil {
		return fmt.Errorf("%s Saving the restore's progress file failed (%v), so nothing was moved: the restored world is at %s and the previous world at %s.", j.Why, err, live, aside)
	}
	restoreStep(ctx, "reverting")
	if err := s.stopServer(ctx, h); err != nil {
		if s.stopping() {
			h.continues = true
			return nil
		}
		return fmt.Errorf("%s Stopping it failed (%v), so nothing was moved: the restored world is at %s and the previous world at %s.", j.Why, err, live, aside)
	}
	if err := s.putPreviousBack(j); err != nil {
		_ = s.setDesired(api.DesiredStopped)
		return fmt.Errorf("%s %s", j.Why, sentence(err.Error()))
	}
	j.State = swapDone
	hint := ""
	if failed := s.copyPath(j.Failed); dirExists(failed) {
		hint = "The failed restore was kept at " + failed + " for inspection."
	}
	if s.desired() != api.DesiredRunning {
		return &apiError{Msg: j.Why + " Your previous world was put back.", Hint: hint}
	}
	if err := s.startServer(ctx, h, *j.Previous); err != nil {
		if s.stopping() {
			return &apiError{Msg: j.Why + " Your previous world was put back and starts when the Playkeeper agent runs again.", Hint: hint}
		}
		return &apiError{Msg: j.Why + " Your previous world was put back but did not start either: " + err.Error(), Hint: strings.TrimSpace("Press Start on the Overview. " + hint)}
	}
	return &apiError{Msg: j.Why + " Your previous world was put back and is running.", Hint: hint}
}

// keepRestore finishes a restore whose world was online: the previous
// world's copy goes, unless keepCopy, and the restore is recorded.
func (s *server) keepRestore(h *opHandle, j *swapJournal, keepCopy bool) error {
	if j.HadLive && !keepCopy {
		if err := os.RemoveAll(s.copyPath(j.Aside)); err != nil {
			s.log.Warn("could not remove the previous world's copy", "server", s.id, "err", err)
		}
	}
	s.forgetPregen()
	j.State = swapDone
	detail := j.Detail
	if h.get("resumedAfterRestart") == true {
		detail += "; finished after the Playkeeper agent restarted"
	}
	h.set("downtimeMs", s.now().Sub(j.StartedAt).Milliseconds())
	s.recordEvent(s.now(), "world_restored", "", "playkeeper", detail)
	s.audit(j.Actor, "restore.applied", shortSum(j.SHA256), "succeeded", detail)
	return nil
}

func shortSum(sum string) string {
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}

// rollForward finishes moving the restored world into place and saving its
// settings, for a restore the agent stopped in while it was moving the
// worlds.
func (s *server) rollForward(stageDir string, j *swapJournal) error {
	live, aside, staged := s.dataDir(), s.copyPath(j.Aside), filepath.Join(stageDir, "data")
	if dirExists(s.copyPath(j.Failed)) {
		return errors.New("the restored world had already been moved out of the way")
	}
	switch {
	case dirExists(staged):
		if dirExists(live) {
			if !j.HadLive || dirExists(aside) {
				return fmt.Errorf("both %s and %s hold a world", live, staged)
			}
			if err := renameDir(live, aside); err != nil {
				return err
			}
		}
		if err := renameDir(staged, live); err != nil {
			return err
		}
	case !dirExists(live):
		return fmt.Errorf("the restored world is neither in %s nor in %s", staged, live)
	}
	if err := chownTree(live, s.cfg.GameUID, s.cfg.GameGID); err != nil {
		s.log.Warn("chown restored world", "err", err)
	}
	if err := s.saveServerConfig(j.Restored); err != nil {
		return err
	}
	j.State = swapChecking
	return writeSwapJournal(stageDir, j)
}

// sentence makes an error message a sentence: capitalized, with a full stop.
func sentence(msg string) string {
	if msg == "" {
		return msg
	}
	r, n := utf8.DecodeRuneInString(msg)
	msg = string(unicode.ToUpper(r)) + msg[n:]
	if !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	return msg
}

// saveVerifiedRollback archives the current world and reads the archive back.
// A restore replaces the world only once this copy is known to be good.
func (s *server) saveVerifiedRollback(sc api.ServerConfig, actor, note string) (*api.Backup, error) {
	rb, err := s.createArchive(sc, "rollback", actor, note)
	if err != nil {
		return nil, err
	}
	vb, err := s.verifyBackup(rb.ID)
	if err != nil {
		return nil, err
	}
	if vb.Verified == nil || !*vb.Verified {
		return nil, fmt.Errorf("archive %s failed verification: %s", vb.ID, vb.VerifyError)
	}
	s.alert(discord.BackupSucceeded(vb.SizeBytes))
	return vb, nil
}

// startPrevious brings the previous world back up after a restore gave up
// before replacing it, if it was running when the restore began.
func (s *server) startPrevious(ctx context.Context, h *opHandle, prev *api.ServerConfig, wasRunning bool) {
	if prev == nil || !wasRunning {
		return
	}
	if err := s.startServer(ctx, h, *prev); err != nil {
		s.log.Warn("could not start the previous world again", "err", err)
	}
}

func chownTree(root string, uid, gid int) error {
	if os.Geteuid() != 0 {
		return nil
	}
	return giveTree(root, uid, gid)
}

// giveTree gives every file and folder under root to uid:gid with the
// game's modes. A link is given as it is: os.Chmod would change what it
// leads to.
func giveTree(root string, uid, gid int) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return os.Lchown(p, uid, gid)
		}
		mode := os.FileMode(0o640)
		if d.IsDir() {
			mode = 0o750
		}
		if err := os.Chmod(p, mode); err != nil {
			return err
		}
		return os.Lchown(p, uid, gid)
	})
}

func validMOTDOr(s string) string {
	if v, err := validMOTD(s); err == nil && v != "" {
		return v
	}
	return defaultMOTD
}

// warnBeforeBackup tells players online that the server stops for a backup
// and gives them a moment to read it.
func (s *server) warnBeforeBackup(ctx context.Context) {
	s.mu.Lock()
	players := s.players
	s.mu.Unlock()
	if players == nil || players.Online == 0 {
		return
	}
	if _, err := s.rconCommand("say " + backupWarning(s.opts.BackupWarnDelay)); err != nil {
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(s.opts.BackupWarnDelay):
	}
}

// backupWarning is the chat line before a backup: it names the wait before
// the server stops, not a guess at how long the backup takes.
func backupWarning(wait time.Duration) string {
	return "Saving a backup: the server stops in " + inWords(wait) + " and is back soon."
}

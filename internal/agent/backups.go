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
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/backup"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/version"
)

var reBackupID = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9a-f]{6}$`)
var reStageID = regexp.MustCompile(`^[0-9a-f]{16}$`)

const minFreeAfterBackup = 512 << 20

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

// createArchive writes a verified archive of the (stopped) server's data.
func (s *server) createArchive(sc api.ServerConfig, kind, actor, note string) (*api.Backup, error) {
	now := s.now().UTC()
	id := now.Format("20060102-150405") + "-" + randomSecret(3)
	label := backup.LevelName(s.dataDir())
	if s.layout != layoutV1 {
		if row, err := s.row(); err == nil {
			label = row.Slug
		}
	}
	fileName := fmt.Sprintf("playkeeper-%s-%s.tar.gz", sanitizeName(label), id)
	tmp := s.backupPath("." + fileName + ".partial")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	meta := backup.Manifest{
		CreatedAt: now, PlaykeeperVersion: version.Version, SourceInstall: shortID(s.cfg.InstallID),
		Type: sc.Type, VersionID: sc.VersionID, MinecraftVersion: sc.MinecraftVersion, PaperBuild: sc.PaperBuild, Build: configBuild(sc), Image: minecraft.Image,
		Settings: map[string]string{
			"motd": sc.MOTD, "maxPlayers": strconv.Itoa(sc.MaxPlayers), "memoryMB": strconv.Itoa(sc.MemoryMB), "whitelist": "true",
			"name": s.name(),
		},
		Consistency: "server stopped during archive",
	}
	m, err := backup.Create(io.MultiWriter(f, h), s.dataDir(), meta, backup.DefaultLimits())
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
	rows, err := a.db.Query(`SELECT id, server_id, kind, created_at, file_name, size_bytes, sha256, manifest, verified, verified_at, verify_error, downtime_ms, created_by, downloaded_at, note
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
		if err := rows.Scan(&b.ID, &b.ServerID, &b.Kind, &created, &b.FileName, &b.SizeBytes, &b.SHA256, &manifest, &verified, &verifiedAt, &b.VerifyError, &b.DowntimeMs, &b.CreatedBy, &downloaded, &b.Note); err != nil {
			return nil, err
		}
		b.CreatedAt = time.UnixMilli(created).UTC()
		b.Location = "on-host"
		var m backup.Manifest
		if json.Unmarshal([]byte(manifest), &m) == nil {
			b.MinecraftVersion, b.LevelName, b.FileCount = m.MinecraftVersion, m.LevelName, len(m.Files)
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

// backupOp stops the server (saving first), archives, restarts it if it was
// running, then verifies the archive. Downtime is measured from the stop
// request until the server is online again.
func (s *server) backupOp(ctx context.Context, h *opHandle, actor, note string) error {
	sc, err := s.serverConfig()
	if err != nil {
		return err
	}
	if sc == nil {
		return errNotCreated()
	}
	need := allowlistedSize(s.dataDir())
	if free, _, err := s.opts.DiskUsage(s.cfg.BackupsDir()); err == nil && free < need+minFreeAfterBackup {
		// Lets the UI drop this failure once enough space is free again.
		h.set("neededBytes", need+minFreeAfterBackup)
		return &apiError{Code: api.CodeInsufficientSpace, Msg: fmt.Sprintf("Not enough disk space for a backup: %s free, about %s needed.", humanBytes(free), humanBytes(need+minFreeAfterBackup)),
			Hint: "Delete old backups (after downloading any you want to keep) or free disk space, then try again."}
	}
	_, running, err := s.containerRunning(ctx)
	if err != nil {
		return err
	}
	start := s.now()
	if running {
		s.warnBeforeBackup(ctx)
		if err := s.stopServer(ctx, h); err != nil {
			return err
		}
	}
	h.phase("archiving")
	b, archiveErr := s.createArchive(*sc, "manual", actor, note)
	if running {
		h.phase("restarting")
		if err := s.startServer(ctx, h, *sc); err != nil {
			if archiveErr != nil {
				return s.withRefusalHint(archiveErr)
			}
			return &apiError{Msg: "The backup was saved, but the server did not start again: " + err.Error(), Hint: "Press Start on the Overview."}
		}
	}
	if archiveErr != nil {
		return s.withRefusalHint(archiveErr)
	}
	downtime := int64(0)
	if running {
		downtime = s.now().Sub(start).Milliseconds()
		_, _ = s.db.Exec(`UPDATE backups SET downtime_ms = ? WHERE id = ?`, downtime, b.ID)
	}
	h.set("backupId", b.ID)
	h.set("downtimeMs", downtime)
	h.phase("verifying")
	vb, err := s.verifyBackup(b.ID)
	if err != nil {
		return err
	}
	if vb.Verified == nil || !*vb.Verified {
		return &apiError{Msg: "The backup was written but failed verification: " + vb.VerifyError, Hint: "Try again; if it keeps failing, check the disk for errors."}
	}
	s.audit(actor, "backup.created", b.ID, "succeeded", fmt.Sprintf("%s sha256 %s downtime %dms", b.FileName, b.SHA256, downtime))
	return nil
}

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
// preview nobody applied or discarded, or an interrupted restore). Each holds
// an archive copy and its extracted world, and nothing can be using them yet.
func (a *Agent) pruneStages() {
	entries, _ := os.ReadDir(a.cfg.StagingDir())
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(a.cfg.StagingDir(), e.Name())); err != nil {
			a.log.Warn("could not remove a leftover restore stage", "stage", e.Name(), "err", err)
		}
	}
	if len(entries) > 0 {
		a.log.Info("removed leftover restore stages", "count", len(entries))
	}
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
	pj, _ := json.Marshal(struct {
		Preview  api.RestorePreview `json:"preview"`
		Manifest backup.Manifest    `json:"manifest"`
	}{p, m})
	if err := os.WriteFile(filepath.Join(dir, "stage.json"), pj, 0o600); err != nil {
		return fail(err)
	}
	return &p, nil
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
	if p.NeedsEULA {
		p.Warnings = append(p.Warnings, "The restore creates a new server, so you must accept the Minecraft EULA first.")
	}
	return p
}

func labelFor(mc string) string { return "Paper " + mc }

func (a *Agent) loadStage(id string) (*stage, error) {
	if !reStageID.MatchString(id) {
		return nil, errInvalid("invalid restore id")
	}
	dir := a.stageDir(id)
	b, err := os.ReadFile(filepath.Join(dir, "stage.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, errNotFound("Restore preview")
	}
	if err != nil {
		return nil, err
	}
	var s struct {
		Preview  api.RestorePreview `json:"preview"`
		Manifest backup.Manifest    `json:"manifest"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
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
	maxPlayers, _ := strconv.Atoi(m.Settings["maxPlayers"])
	if maxPlayers < 1 || maxPlayers > 100 {
		maxPlayers = 10
	}
	now := a.now().UTC()
	sc := rt.config(api.ServerConfig{
		MemoryMB: mem, HeapMB: minecraft.HeapMB(mem), LevelName: m.LevelName, MOTD: validMOTDOr(m.Settings["motd"]),
		MaxPlayers: maxPlayers, Whitelist: true, CreatedAt: now, EULAAcceptedAt: now, EULAAcceptedBy: actor,
	})
	_, op, err := a.addServer(newServerSpec{name: name, typ: rt.typ, config: sc, desired: api.DesiredStopped, actor: actor}, "restore", restore)
	return op, err
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

// restoreOp replaces the world with a staged, verified archive. A rollback
// archive of the current world is written first, and a restored world that
// fails to start is swapped back out automatically. The stage is deleted when
// the live world is known to be good: the restore finished, nothing was
// replaced, or the previous world is back. If putting it back fails, the
// stage and the aside copy both stay and the error names them.
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
	aside := live + ".replaced-" + s.now().UTC().Format("20060102-150405")
	hadLive := false
	if _, err := os.Stat(live); err == nil {
		if err := renameDir(live, aside); err != nil {
			return err
		}
		hadLive = true
	}
	worldSafe = false
	// A restored world that has to make way goes to failedAt, next to the live
	// directory and outside the staging folder the agent clears at start.
	failedAt := live + ".failed-restore-" + s.now().UTC().Format("20060102-150405")
	// putBack moves the previous world back into place once the restored one
	// is out of the way, at restoredAt. If that fails the live directory is
	// missing: nothing is deleted, the restored copy leaves the stage, and the
	// error names where both copies are.
	putBack := func(restoredAt string, cause error) error {
		if !hadLive {
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
	if err := renameDir(st.data, live); err != nil {
		if perr := putBack(st.data, fmt.Errorf("could not move the restored world into place: %w", err)); perr != nil {
			return perr
		}
		return err
	}
	if err := chownTree(live, s.cfg.GameUID, s.cfg.GameGID); err != nil {
		s.log.Warn("chown restored world", "err", err)
	}
	mem := st.preview.MemoryMB
	if req.MemoryMB != 0 {
		mem = req.MemoryMB
	}
	maxPlayers, _ := strconv.Atoi(m.Settings["maxPlayers"])
	if maxPlayers < 1 || maxPlayers > 100 {
		maxPlayers = 10
	}
	// The restored server.properties carries the backup's game settings, so
	// none chosen since override them.
	sc := rt.config(api.ServerConfig{
		MemoryMB: mem, HeapMB: minecraft.HeapMB(mem),
		LevelName: m.LevelName, MOTD: validMOTDOr(m.Settings["motd"]), MaxPlayers: maxPlayers, Whitelist: true, CreatedAt: s.now().UTC(),
	})
	var prevPack *api.ResourcePackOffer
	if prev != nil {
		sc.EULAAcceptedAt, sc.EULAAcceptedBy, sc.CreatedAt, sc.PlayStyle = prev.EULAAcceptedAt, prev.EULAAcceptedBy, prev.CreatedAt, prev.PlayStyle
		prevPack = prev.ResourcePack
	} else {
		sc.EULAAcceptedAt, sc.EULAAcceptedBy = s.now().UTC(), actor
	}
	sc.ResourcePack = restoredPackOffer(prevPack, live)
	if err := s.saveServerConfig(sc); err != nil {
		cause := fmt.Errorf("could not record the restored server's settings: %w", err)
		if rerr := renameDir(live, failedAt); rerr != nil {
			where := "there was no previous world"
			if hadLive {
				where = "the previous world is at " + aside
			}
			return fmt.Errorf("%v; moving the restored world out of the way also failed (%v), so nothing was deleted: the restored world is at %s and %s", cause, rerr, live, where)
		}
		if perr := putBack(failedAt, cause); perr != nil {
			return perr
		}
		os.RemoveAll(failedAt)
		s.startPrevious(ctx, h, prev, wasRunning)
		if !hadLive {
			return fmt.Errorf("%v, so the restore was undone", cause)
		}
		return fmt.Errorf("could not record the restored server's settings, so the previous world was put back: %w", err)
	}
	_ = s.setDesired(api.DesiredRunning)
	s.holdRestoredPregen(sc)
	startErr := s.startServer(ctx, h, sc)
	if startErr != nil && hadLive && prev != nil {
		h.phase("reverting")
		_ = s.stopServer(ctx, h)
		if err := renameDir(live, failedAt); err != nil {
			return fmt.Errorf("the restored world did not start (%v), and moving it aside failed (%v), so nothing was deleted: the restored world is at %s and the previous world at %s", startErr, err, live, aside)
		}
		if err := putBack(failedAt, fmt.Errorf("the restored world did not start: %w", startErr)); err != nil {
			return err
		}
		_ = s.saveServerConfig(*prev)
		if err := s.startServer(ctx, h, *prev); err != nil {
			return &apiError{Msg: "The restored world did not start (" + startErr.Error() + "). Your previous world was put back but did not start either: " + err.Error(), Hint: "Press Start on the Overview. The failed restore was kept at " + failedAt + " for inspection."}
		}
		return &apiError{Msg: "The restored world did not start (" + startErr.Error() + "). Your previous world was put back and is running.", Hint: "The failed restore was kept at " + failedAt + " for inspection."}
	}
	worldSafe = true
	s.forgetPregen()
	if startErr != nil {
		return startErr
	}
	if hadLive {
		os.RemoveAll(aside)
	}
	detail := fmt.Sprintf("restored %s (sha256 %s)", m.LevelName, st.preview.SHA256)
	if rollback != nil {
		detail += "; rollback archive " + rollback.ID
	}
	h.set("downtimeMs", s.now().Sub(start).Milliseconds())
	s.recordEvent(s.now(), "world_restored", "", "playkeeper", detail)
	s.audit(actor, "restore.applied", st.preview.SHA256[:12], "succeeded", detail)
	return nil
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
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
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

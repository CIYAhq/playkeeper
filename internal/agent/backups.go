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
func (a *Agent) createArchive(sc api.ServerConfig, kind, actor, note string) (*api.Backup, error) {
	now := a.now().UTC()
	id := now.Format("20060102-150405") + "-" + randomSecret(3)
	level := backup.LevelName(a.cfg.ServerDataDir())
	fileName := fmt.Sprintf("playkeeper-%s-%s.tar.gz", sanitizeName(level), id)
	tmp := a.backupPath("." + fileName + ".partial")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	meta := backup.Manifest{
		CreatedAt: now, PlaykeeperVersion: version.Version, SourceInstall: shortID(a.cfg.InstallID),
		VersionID: sc.VersionID, MinecraftVersion: sc.MinecraftVersion, PaperBuild: sc.PaperBuild, Image: minecraft.Image,
		Settings: map[string]string{
			"motd": sc.MOTD, "maxPlayers": strconv.Itoa(sc.MaxPlayers), "memoryMB": strconv.Itoa(sc.MemoryMB), "whitelist": "true",
		},
		Consistency: "server stopped during archive",
	}
	m, err := backup.Create(io.MultiWriter(f, h), a.cfg.ServerDataDir(), meta)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return nil, fmt.Errorf("writing the archive failed: %w", err)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	final := a.backupPath(fileName)
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	_ = os.WriteFile(final+".sha256", []byte(sum+"  "+fileName+"\n"), 0o600)
	st, _ := os.Stat(final)
	mj, _ := json.Marshal(m)
	b := &api.Backup{ID: id, Kind: kind, CreatedAt: now, FileName: fileName, SizeBytes: st.Size(), SHA256: sum, Location: "on-host",
		MinecraftVersion: m.MinecraftVersion, LevelName: m.LevelName, FileCount: len(m.Files), CreatedBy: actor, Note: note}
	// Downtime belongs to manual backups (set once the server is back); a
	// rollback archive is part of a restore, which records its own downtime.
	_, err = a.db.Exec(`INSERT INTO backups(id, kind, created_at, file_name, size_bytes, sha256, manifest, created_by, note, downtime_ms) VALUES(?,?,?,?,?,?,?,?,?,0)`,
		id, kind, now.UnixMilli(), fileName, st.Size(), sum, string(mj), actor, note)
	if err != nil {
		return nil, err
	}
	return b, nil
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
func (a *Agent) verifyBackup(id string) (*api.Backup, error) {
	b, err := a.getBackup(id)
	if err != nil {
		return nil, err
	}
	verr := func() error {
		f, err := os.Open(a.backupPath(b.FileName))
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
	now := a.now().UnixMilli()
	msg := ""
	if verr != nil {
		msg = verr.Error()
	}
	_, _ = a.db.Exec(`UPDATE backups SET verified = ?, verified_at = ?, verify_error = ? WHERE id = ?`, boolInt(verr == nil), now, msg, id)
	return a.getBackup(id)
}

func (a *Agent) getBackup(id string) (*api.Backup, error) {
	if err := validBackupID(id); err != nil {
		return nil, err
	}
	list, err := a.listBackups(`WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, errNotFound("Backup")
	}
	return &list[0], nil
}

func (a *Agent) listBackups(where string, args ...any) ([]api.Backup, error) {
	rows, err := a.db.Query(`SELECT id, kind, created_at, file_name, size_bytes, sha256, manifest, verified, verified_at, verify_error, downtime_ms, created_by, downloaded_at, note
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
		if err := rows.Scan(&b.ID, &b.Kind, &created, &b.FileName, &b.SizeBytes, &b.SHA256, &manifest, &verified, &verifiedAt, &b.VerifyError, &b.DowntimeMs, &b.CreatedBy, &downloaded, &b.Note); err != nil {
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
func (a *Agent) backupOp(ctx context.Context, h *opHandle, actor, note string) error {
	sc, err := a.serverConfig()
	if err != nil {
		return err
	}
	if sc == nil {
		return errNotCreated()
	}
	need := allowlistedSize(a.cfg.ServerDataDir())
	if free, _, err := a.opts.DiskUsage(a.cfg.BackupsDir()); err == nil && free < need+minFreeAfterBackup {
		return &apiError{Code: api.CodeInsufficientSpace, Msg: fmt.Sprintf("Not enough disk space for a backup: %s free, about %s needed.", humanBytes(free), humanBytes(need+minFreeAfterBackup)),
			Hint: "Delete old backups (after downloading any you want to keep) or free disk space, then try again."}
	}
	_, running, err := a.containerRunning(ctx)
	if err != nil {
		return err
	}
	start := a.now()
	if running {
		if err := a.stopServer(ctx, h); err != nil {
			return err
		}
	}
	h.phase("archiving")
	b, archiveErr := a.createArchive(*sc, "manual", actor, note)
	if running {
		h.phase("restarting")
		if err := a.startServer(ctx, h, *sc); err != nil {
			if archiveErr != nil {
				return archiveErr
			}
			return &apiError{Msg: "The backup was saved, but the server did not start again: " + err.Error(), Hint: "Press Start on the Overview."}
		}
	}
	if archiveErr != nil {
		return archiveErr
	}
	downtime := int64(0)
	if running {
		downtime = a.now().Sub(start).Milliseconds()
		_, _ = a.db.Exec(`UPDATE backups SET downtime_ms = ? WHERE id = ?`, downtime, b.ID)
	}
	h.set("backupId", b.ID)
	h.set("downtimeMs", downtime)
	h.phase("verifying")
	vb, err := a.verifyBackup(b.ID)
	if err != nil {
		return err
	}
	if vb.Verified == nil || !*vb.Verified {
		return &apiError{Msg: "The backup was written but failed verification: " + vb.VerifyError, Hint: "Try again; if it keeps failing, check the disk for errors."}
	}
	a.audit(actor, "backup.created", b.ID, "succeeded", fmt.Sprintf("%s sha256 %s downtime %dms", b.FileName, b.SHA256, downtime))
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

// --- restore ---

type stage struct {
	dir      string
	archive  string
	data     string
	manifest backup.Manifest
	preview  api.RestorePreview
}

func (a *Agent) stageDir(id string) string { return filepath.Join(a.cfg.StagingDir(), id) }

// stageArchive copies an archive into staging, then verifies and extracts it
// there. The live world is not touched; failures delete the staging dir.
func (a *Agent) stageArchive(src io.Reader, source string, limit int64) (*api.RestorePreview, error) {
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
	p := a.buildPreview(id, source, n, hex.EncodeToString(h.Sum(nil)), m)
	pj, _ := json.Marshal(struct {
		Preview  api.RestorePreview `json:"preview"`
		Manifest backup.Manifest    `json:"manifest"`
	}{p, m})
	if err := os.WriteFile(filepath.Join(dir, "stage.json"), pj, 0o600); err != nil {
		return fail(err)
	}
	return &p, nil
}

func (a *Agent) buildPreview(id, source string, size int64, sum string, m backup.Manifest) api.RestorePreview {
	p := api.RestorePreview{
		ID: id, Source: source, ReceivedAt: a.now().UTC(), SizeBytes: size, SHA256: sum, Compatible: true,
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
	entry, ok := minecraft.LookupMinecraft(m.MinecraftVersion)
	if !ok {
		p.Compatible = false
		var known []string
		for _, v := range minecraft.Versions() {
			known = append(known, v.MinecraftVersion)
		}
		p.Problems = append(p.Problems, fmt.Sprintf("This backup is from Minecraft %s; this Playkeeper can run %s.", m.MinecraftVersion, strings.Join(known, " and ")))
	} else if entry.PaperBuild != m.PaperBuild {
		p.Warnings = append(p.Warnings, fmt.Sprintf("The backup used Paper build %d; this host will run the pinned build %d of the same Minecraft version.", m.PaperBuild, entry.PaperBuild))
	}
	if m.SourceInstall != "" && m.SourceInstall == shortID(a.cfg.InstallID) {
		p.Source += " (made on this host)"
	} else {
		p.Warnings = append(p.Warnings, "This backup was made on a different Playkeeper host.")
	}
	host := a.opts.HostMemoryMB()
	mem, _ := strconv.Atoi(m.Settings["memoryMB"])
	if !minecraft.ValidBudget(mem, host) {
		_, rec, _ := minecraft.MemoryOptions(host)
		if rec == 0 {
			p.Compatible = false
			p.Problems = append(p.Problems, "This host does not have enough memory to run a Minecraft server.")
		} else {
			p.Warnings = append(p.Warnings, fmt.Sprintf("The backup's memory budget (%d MB) does not fit this host; %d MB will be used.", mem, rec))
		}
		mem = rec
	}
	p.MemoryMB = mem
	sc, _ := a.serverConfig()
	p.NeedsEULA = sc == nil
	cw := api.CurrentWorld{}
	if st, err := os.Stat(filepath.Join(a.cfg.ServerDataDir(), backup.LevelName(a.cfg.ServerDataDir()))); err == nil && st.IsDir() {
		cw.Exists = true
		cw.LevelName = backup.LevelName(a.cfg.ServerDataDir())
		cw.SizeBytes = allowlistedSize(a.cfg.ServerDataDir())
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
		p.Warnings = append(p.Warnings, "You must accept the Minecraft EULA on this host before restoring.")
	}
	return p
}

func labelFor(mc string) string {
	if e, ok := minecraft.LookupMinecraft(mc); ok {
		return e.Label
	}
	return "Minecraft " + mc
}

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
	st.preview = a.buildPreview(id, s.Preview.Source, s.Preview.SizeBytes, s.Preview.SHA256, s.Manifest)
	st.preview.ReceivedAt = s.Preview.ReceivedAt
	return st, nil
}

// restoreOp replaces the world with a staged, verified archive. A rollback
// archive of the current world is written first, and a restored world that
// fails to start is swapped back out automatically.
func (a *Agent) restoreOp(ctx context.Context, h *opHandle, st *stage, req api.RestoreApplyRequest, actor string) error {
	m := st.manifest
	entry, ok := minecraft.LookupMinecraft(m.MinecraftVersion)
	if !ok {
		return errInvalid("backup version %s is not supported", m.MinecraftVersion)
	}
	prev, err := a.serverConfig()
	if err != nil {
		return err
	}
	start := a.now()
	var rollback *api.Backup
	if prev != nil {
		if err := a.stopServer(ctx, h); err != nil {
			return err
		}
		if st.preview.CurrentWorld.Exists {
			h.phase("saving_rollback")
			rb, err := a.createArchive(*prev, "rollback", actor, "Automatic rollback archive before restoring backup "+st.preview.SHA256[:12])
			if err != nil {
				return fmt.Errorf("could not save a rollback archive, so nothing was replaced: %w", err)
			}
			rollback = rb
			h.set("rollbackBackupId", rb.ID)
			if _, err := a.verifyBackup(rb.ID); err != nil {
				return err
			}
		}
	}
	h.phase("replacing_world")
	live := a.cfg.ServerDataDir()
	aside := live + ".replaced-" + a.now().UTC().Format("20060102-150405")
	hadLive := false
	if _, err := os.Stat(live); err == nil {
		if err := os.Rename(live, aside); err != nil {
			return err
		}
		hadLive = true
	}
	if err := os.Rename(st.data, live); err != nil {
		if hadLive {
			_ = os.Rename(aside, live)
		}
		return err
	}
	if err := chownTree(live, a.cfg.GameUID, a.cfg.GameGID); err != nil {
		a.log.Warn("chown restored world", "err", err)
	}
	mem := st.preview.MemoryMB
	if req.MemoryMB != 0 {
		mem = req.MemoryMB
	}
	maxPlayers, _ := strconv.Atoi(m.Settings["maxPlayers"])
	if maxPlayers < 1 || maxPlayers > 100 {
		maxPlayers = 10
	}
	sc := api.ServerConfig{
		VersionID: entry.ID, MinecraftVersion: entry.MinecraftVersion, PaperBuild: entry.PaperBuild, MemoryMB: mem, HeapMB: minecraft.HeapMB(mem),
		LevelName: m.LevelName, MOTD: validMOTDOr(m.Settings["motd"]), MaxPlayers: maxPlayers, Whitelist: true, Image: minecraft.Image, CreatedAt: a.now().UTC(),
	}
	if prev != nil {
		sc.EULAAcceptedAt, sc.EULAAcceptedBy, sc.CreatedAt = prev.EULAAcceptedAt, prev.EULAAcceptedBy, prev.CreatedAt
	} else {
		sc.EULAAcceptedAt, sc.EULAAcceptedBy = a.now().UTC(), actor
	}
	if err := a.saveServerConfig(sc); err != nil {
		return err
	}
	_ = a.setDesired(api.DesiredRunning)
	startErr := a.startServer(ctx, h, sc)
	if startErr != nil && hadLive && prev != nil {
		h.phase("reverting")
		_ = a.stopServer(ctx, h)
		failed := live + ".failed-restore-" + a.now().UTC().Format("20060102-150405")
		if err := os.Rename(live, failed); err == nil {
			if err := os.Rename(aside, live); err == nil {
				_ = a.saveServerConfig(*prev)
				if err := a.startServer(ctx, h, *prev); err == nil {
					return &apiError{Msg: "The restored world did not start (" + startErr.Error() + "). Your previous world was put back and is running.", Hint: "The failed restore was kept at " + failed + " for inspection."}
				}
			}
		}
		return &apiError{Msg: "The restored world did not start: " + startErr.Error(), Hint: "Restore the rollback archive from the World page to return to your previous world."}
	}
	if startErr != nil {
		return startErr
	}
	if hadLive {
		os.RemoveAll(aside)
	}
	os.RemoveAll(st.dir)
	detail := fmt.Sprintf("restored %s (sha256 %s)", m.LevelName, st.preview.SHA256)
	if rollback != nil {
		detail += "; rollback archive " + rollback.ID
	}
	h.set("downtimeMs", a.now().Sub(start).Milliseconds())
	a.recordEvent(a.now(), "world_restored", "", "playkeeper", detail)
	a.audit(actor, "restore.applied", st.preview.SHA256[:12], "succeeded", detail)
	return nil
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

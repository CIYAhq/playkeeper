package agent

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// catalogTTL is how long PaperMC's version list is reused.
const catalogTTL = 30 * time.Minute

type cachedBuild struct {
	entry api.CatalogEntry
	err   error
	at    time.Time
}

type catalogCache struct {
	mu      sync.Mutex
	entries []api.CatalogEntry
	at      time.Time
	builds  map[string]cachedBuild
}

func (a *Agent) fill() minecraft.Fill {
	return minecraft.Fill{BaseURL: a.opts.FillURL, Client: a.opts.HTTPClient}
}

// versionCatalog is the live list of Paper versions from PaperMC, reused for
// catalogTTL. If PaperMC cannot be reached, the last list is used if there
// is one.
func (a *Agent) versionCatalog(ctx context.Context) ([]api.CatalogEntry, error) {
	c := &a.catalog
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries != nil && a.now().Sub(c.at) < catalogTTL {
		return c.entries, nil
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	entries, err := a.fill().Catalog(cctx)
	if err != nil {
		a.log.Warn("could not load the Paper version list", "err", err)
		if c.entries != nil {
			return c.entries, nil
		}
		return nil, err
	}
	c.entries, c.at = entries, a.now()
	return entries, nil
}

// catalogEntry finds a version to create or update a server with.
func (a *Agent) catalogEntry(ctx context.Context, id string) (api.CatalogEntry, error) {
	entries, err := a.versionCatalog(ctx)
	if err != nil {
		return api.CatalogEntry{}, &apiError{Status: http.StatusServiceUnavailable, Code: api.CodeInvalid, Msg: "Could not load the Minecraft versions from PaperMC: " + err.Error(), Hint: "Check that this server can reach fill.papermc.io, then try again."}
	}
	for _, e := range entries {
		if e.ID == id {
			return e, nil
		}
	}
	return api.CatalogEntry{}, errInvalid("Unknown server version. Choose one of the listed versions.")
}

// restoreBuild is the Paper build a backup made with Minecraft mc on build
// `build` is restored with.
func (a *Agent) restoreBuild(ctx context.Context, mc string, build int) (api.CatalogEntry, error) {
	c := &a.catalog
	c.mu.Lock()
	defer c.mu.Unlock()
	key := fmt.Sprintf("%s#%d", mc, build)
	if b, ok := c.builds[key]; ok {
		age := a.now().Sub(b.at)
		if (b.err == nil && age < catalogTTL) || (b.err != nil && age < time.Minute) {
			return b.entry, b.err
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	e, err := a.fill().RestoreBuild(cctx, mc, build)
	if c.builds == nil {
		c.builds = map[string]cachedBuild{}
	}
	c.builds[key] = cachedBuild{entry: e, err: err, at: a.now()}
	return e, err
}

// jarChecksum is the SHA-256 a server's jar must have. Servers created by
// 0.1.0 did not record it; their builds' checksums are known.
func jarChecksum(sc api.ServerConfig) (string, error) {
	if sc.JarSHA256 != "" {
		return sc.JarSHA256, nil
	}
	if sum, ok := minecraft.KnownJarSHA256(sc.MinecraftVersion, sc.PaperBuild); ok {
		return sum, nil
	}
	return "", fmt.Errorf("no checksum is known for Paper %s build %d, so it is not run", sc.MinecraftVersion, sc.PaperBuild)
}

// withBuild returns sc running the catalog entry's build.
func withBuild(sc api.ServerConfig, e api.CatalogEntry) api.ServerConfig {
	sc.VersionID, sc.MinecraftVersion, sc.PaperBuild, sc.JarSHA256 = e.ID, e.MinecraftVersion, e.PaperBuild, e.JarSHA256
	sc.JarVerifiedAt = nil
	sc.Image = minecraft.Image
	return sc
}

// checkNewer refuses a version change that is not an update: an older
// Minecraft version, or the same version on the same or an older build.
func checkNewer(cur api.ServerConfig, e api.CatalogEntry) error {
	switch c := minecraft.CompareMinecraft(e.MinecraftVersion, cur.MinecraftVersion); {
	case c < 0:
		return errConflict(fmt.Sprintf("Minecraft cannot go back from %s to %s: this world has been opened with %s, and an older version could damage it or refuse to open it.", cur.MinecraftVersion, e.MinecraftVersion, cur.MinecraftVersion),
			"To play on an older version, restore a backup that was made with it.")
	case c == 0 && e.PaperBuild <= cur.PaperBuild:
		return errConflict(fmt.Sprintf("The server already runs Paper %s build %d, and this is not newer.", cur.MinecraftVersion, cur.PaperBuild), "")
	}
	return nil
}

func (a *Agent) hVersionChange(w http.ResponseWriter, r *http.Request) {
	var req api.VersionChangeRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	cur, err := a.serverConfig()
	if err != nil {
		writeError(w, err)
		return
	}
	if cur == nil {
		writeError(w, errNotCreated())
		return
	}
	e, err := a.catalogEntry(r.Context(), req.VersionID)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := checkNewer(*cur, e); err != nil {
		a.audit(actor, "server.version", e.ID, "refused", err.Error())
		writeError(w, err)
		return
	}
	if e.Experimental && !req.AcceptExperimental {
		writeError(w, errInvalid("%s is experimental. Confirm that you accept the risk to your world to use it.", e.Label))
		return
	}
	op, err := a.beginOp("update-version", actor, func(ctx context.Context, h *opHandle) error {
		return a.versionChangeOp(ctx, h, e, actor)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

// versionChangeOp moves the server to another Paper build: it takes a
// verified backup first, downloads and verifies the new jar, and starts it.
// If the new version does not start, the backup is put back and the server
// runs the previous version again. Minecraft upgrades a world on first
// start, so the world is restored too, not only the software.
func (a *Agent) versionChangeOp(ctx context.Context, h *opHandle, e api.CatalogEntry, actor string) error {
	prev, err := a.serverConfig()
	if err != nil {
		return err
	}
	if prev == nil {
		return errNotCreated()
	}
	if err := checkNewer(*prev, e); err != nil {
		return err
	}
	h.set("from", prev.MinecraftVersion+" build "+fmt.Sprint(prev.PaperBuild))
	h.set("to", e.MinecraftVersion+" build "+fmt.Sprint(e.PaperBuild))
	need := allowlistedSize(a.cfg.ServerDataDir())
	if free, _, err := a.opts.DiskUsage(a.cfg.BackupsDir()); err == nil && free < 2*need+minFreeAfterBackup {
		return &apiError{Code: api.CodeInsufficientSpace, Msg: fmt.Sprintf("Not enough disk space to update safely: %s free, about %s needed for the backup and a possible rollback.", humanBytes(free), humanBytes(2*need+minFreeAfterBackup)),
			Hint: "Delete old backups (after downloading any you want to keep) or free disk space, then try again."}
	}
	_, wasRunning, err := a.containerRunning(ctx)
	if err != nil {
		return err
	}
	if err := a.stopServer(ctx, h); err != nil {
		return err
	}
	h.phase("backing_up")
	b, err := a.saveVerifiedRollback(*prev, actor, fmt.Sprintf("Automatic backup before updating from Paper %s to %s", prev.MinecraftVersion, e.MinecraftVersion))
	if err != nil {
		a.startPrevious(ctx, h, prev, wasRunning)
		return a.withRefusalHint(fmt.Errorf("could not save a verified backup first, so nothing was changed: %w", err))
	}
	h.set("backupId", b.ID)
	next := withBuild(*prev, e)
	if err := a.ensureServerSoftware(ctx, h, &next); err != nil {
		_ = a.saveServerConfig(*prev)
		a.startPrevious(ctx, h, prev, wasRunning)
		return &apiError{Msg: fmt.Sprintf("Paper %s could not be downloaded and verified (%s), so nothing was changed.", e.MinecraftVersion, err.Error()), Hint: "The server runs " + prev.MinecraftVersion + " as before. Try again later."}
	}
	if err := a.saveServerConfig(next); err != nil {
		a.startPrevious(ctx, h, prev, wasRunning)
		return err
	}
	_ = a.setDesired(api.DesiredRunning)
	h.phase("starting")
	startErr := a.startServer(ctx, h, next)
	if startErr == nil {
		a.audit(actor, "server.version", e.ID, "succeeded", fmt.Sprintf("Paper %s build %d → %s build %d; backup %s", prev.MinecraftVersion, prev.PaperBuild, e.MinecraftVersion, e.PaperBuild, b.ID))
		a.recordEvent(a.now(), "server_version_changed", "", "playkeeper", fmt.Sprintf("%s build %d → %s build %d", prev.MinecraftVersion, prev.PaperBuild, e.MinecraftVersion, e.PaperBuild))
		return nil
	}
	h.phase("reverting")
	a.log.Warn("the new server version did not start; restoring the backup", "version", e.MinecraftVersion, "err", startErr)
	_ = a.stopServer(ctx, h)
	if err := a.putBackupBack(b); err != nil {
		a.audit(actor, "server.version", e.ID, "failed", "rollback failed: "+err.Error())
		return &apiError{Msg: fmt.Sprintf("Paper %s did not start (%s), and putting the backup back failed: %s", e.MinecraftVersion, startErr.Error(), err.Error()),
			Hint: "Your world is safe in backup " + b.ID + ". Restore it from the World page."}
	}
	_ = a.saveServerConfig(*prev)
	if err := a.startServer(ctx, h, *prev); err != nil {
		return &apiError{Msg: fmt.Sprintf("Paper %s did not start (%s). The backup was put back, but %s did not start either: %s", e.MinecraftVersion, startErr.Error(), prev.MinecraftVersion, err.Error()), Hint: "Press Start on the Overview."}
	}
	a.audit(actor, "server.version", e.ID, "rolled back", startErr.Error())
	return &apiError{Msg: fmt.Sprintf("Paper %s did not start (%s), so Playkeeper put the backup from before the update back. The server runs %s again.", e.MinecraftVersion, startErr.Error(), prev.MinecraftVersion),
		Hint: "Nothing was lost. Open the Console to see why the new version stopped."}
}

// putBackupBack replaces the live world with a verified backup's. The world
// the failed start touched is deleted once the backup's copy is in place.
func (a *Agent) putBackupBack(b *api.Backup) error {
	f, err := os.Open(a.backupPath(b.FileName))
	if err != nil {
		return err
	}
	p, err := a.stageArchive(f, "backup:"+b.ID, b.SizeBytes)
	f.Close()
	if err != nil {
		return err
	}
	st, err := a.loadStage(p.ID)
	if err != nil {
		return err
	}
	defer os.RemoveAll(st.dir)
	live := a.cfg.ServerDataDir()
	failed := live + ".failed-update-" + a.now().UTC().Format("20060102-150405")
	if err := renameDir(live, failed); err != nil {
		return err
	}
	if err := renameDir(st.data, live); err != nil {
		if rerr := renameDir(failed, live); rerr != nil {
			return fmt.Errorf("%v; moving the world back also failed (%v): it is at %s", err, rerr, failed)
		}
		return err
	}
	if err := chownTree(live, a.cfg.GameUID, a.cfg.GameGID); err != nil {
		a.log.Warn("chown restored world", "err", err)
	}
	if err := os.RemoveAll(failed); err != nil {
		a.log.Warn("could not delete the world the failed update touched", "path", failed, "err", err)
	}
	return nil
}

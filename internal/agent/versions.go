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
	"github.com/CIYAhq/playkeeper/internal/minecraft/software"
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

// versionCatalog is the live list of Paper versions from PaperMC and when it
// was fetched, reused for catalogTTL. If PaperMC cannot be reached, the last
// list is used if there is one.
func (a *Agent) versionCatalog(ctx context.Context) ([]api.CatalogEntry, time.Time, error) {
	c := &a.catalog
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries != nil && a.now().Sub(c.at) < catalogTTL {
		return c.entries, c.at, nil
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	entries, err := a.fill().Catalog(cctx)
	if err != nil {
		a.log.Warn("could not load the Paper version list", "err", err)
		if c.entries != nil {
			return c.entries, c.at, nil
		}
		return nil, time.Time{}, err
	}
	c.entries, c.at = entries, a.now()
	return entries, c.at, nil
}

// catalogEntry finds a version to create or update a server with.
func (a *Agent) catalogEntry(ctx context.Context, id string) (api.CatalogEntry, error) {
	entries, _, err := a.versionCatalog(ctx)
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

// forgetFailedBuild drops a failed restoreBuild lookup from the cache, so
// the next one asks PaperMC again instead of repeating the failure.
func (a *Agent) forgetFailedBuild(mc string, build int) {
	c := &a.catalog
	c.mu.Lock()
	defer c.mu.Unlock()
	key := fmt.Sprintf("%s#%d", mc, build)
	if b, ok := c.builds[key]; ok && b.err != nil {
		delete(c.builds, key)
	}
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
	sc.JarVerifiedAt, sc.Software = nil, nil
	sc.Image = runtimeImage(sc.MinecraftVersion)
	return sc
}

// checkNewer refuses a version change that is not an update: an older
// Minecraft version, or the same version on the same or an older build.
func checkNewer(cur api.ServerConfig, e api.CatalogEntry) error {
	switch c := minecraft.CompareMinecraft(e.MinecraftVersion, cur.MinecraftVersion); {
	case c < 0:
		return errConflict(fmt.Sprintf("Minecraft cannot go back from %s to %s: this world has been opened with %s, and an older version could damage it or refuse to open it.", cur.MinecraftVersion, e.MinecraftVersion, cur.MinecraftVersion),
			"To play on an older version, restore a backup that was made with it.")
	case c == 0 && cur.Software == nil && e.PaperBuild <= cur.PaperBuild:
		return errConflict(fmt.Sprintf("The server already runs Paper %s build %d, and this is not newer.", cur.MinecraftVersion, cur.PaperBuild), "")
	case c == 0 && cur.Software != nil && e.Build == pinBuild(software.Pin(*cur.Software)):
		return errConflict(fmt.Sprintf("The server already runs %s.", softwareLabel(cur)), "")
	}
	return nil
}

// newerStable is the update a server's Settings tab offers: the newest
// stable, supported Minecraft version of its own type newer than the one it
// runs, on its newest build. It must match the dashboard's newerStable.
func newerStable(cur api.ServerConfig, versions []api.CatalogEntry) (api.CatalogEntry, bool) {
	typ := configType(cur)
	var best api.CatalogEntry
	found := false
	for _, e := range versions {
		if entryType(e) != typ || e.Experimental || !e.Supported || minecraft.CompareMinecraft(e.MinecraftVersion, cur.MinecraftVersion) <= 0 {
			continue
		}
		c := minecraft.CompareMinecraft(e.MinecraftVersion, best.MinecraftVersion)
		if !found || c > 0 || c == 0 && e.PaperBuild > best.PaperBuild {
			best, found = e, true
		}
	}
	return best, found
}

// configType is the server type a config runs.
func configType(sc api.ServerConfig) string {
	if sc.Software != nil {
		return sc.Software.Type
	}
	return api.TypePaper
}

// entryType is the server type a catalog entry is for.
func entryType(e api.CatalogEntry) string {
	if e.Software != nil {
		return e.Software.Type
	}
	return api.TypePaper
}

// nextConfig is cur moved to a catalog entry of its own type, with the
// build the catalog recommends, and whether that build is experimental.
func (a *Agent) nextConfig(ctx context.Context, cur api.ServerConfig, e api.CatalogEntry) (api.ServerConfig, bool, error) {
	if cur.Software == nil {
		return withBuild(cur, e), e.Experimental, nil
	}
	pin, channel, err := a.pinFor(ctx, e, "")
	if err != nil {
		return api.ServerConfig{}, false, err
	}
	return withPin(cur, e, pin), e.Experimental || channel != software.Stable, nil
}

// versionText is a config's version as the update flow shows it: "26.1.2
// build 41" for Paper, the software's name for the others.
func versionText(sc api.ServerConfig) string {
	if sc.Software == nil {
		return fmt.Sprintf("%s build %d", sc.MinecraftVersion, sc.PaperBuild)
	}
	return softwareLabel(sc)
}

func (s *server) hVersionChange(w http.ResponseWriter, r *http.Request) {
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
	cur, err := s.serverConfig()
	if err != nil {
		writeError(w, err)
		return
	}
	if cur == nil {
		writeError(w, errNotCreated())
		return
	}
	e, err := s.typeEntry(r.Context(), configType(*cur), req.VersionID)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := checkNewer(*cur, e); err != nil {
		s.audit(actor, "server.version", e.ID, "refused", err.Error())
		writeError(w, err)
		return
	}
	_, experimental, err := s.nextConfig(r.Context(), *cur, e)
	if err != nil {
		writeError(w, err)
		return
	}
	if experimental && !req.AcceptExperimental {
		writeError(w, errInvalid("%s is experimental. Confirm that you accept the risk to your world to use it.", e.Label))
		return
	}
	op, err := s.beginOp("update-version", actor, func(ctx context.Context, h *opHandle) error {
		return s.versionChangeOp(ctx, h, e, req.WarnPlayers, actor)
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
func (s *server) versionChangeOp(ctx context.Context, h *opHandle, e api.CatalogEntry, warn bool, actor string) error {
	prev, err := s.serverConfig()
	if err != nil {
		return err
	}
	if prev == nil {
		return errNotCreated()
	}
	if err := checkNewer(*prev, e); err != nil {
		return err
	}
	next, _, err := s.nextConfig(ctx, *prev, e)
	if err != nil {
		return err
	}
	kind := typeName(configType(*prev))
	h.set("from", versionText(*prev))
	h.set("to", versionText(next))
	need := allowlistedSize(s.dataDir())
	if free, _, err := s.opts.DiskUsage(s.cfg.BackupsDir()); err == nil && free < 2*need+minFreeAfterBackup {
		return &apiError{Code: api.CodeInsufficientSpace, Msg: fmt.Sprintf("Not enough disk space to update safely: %s free, about %s needed for the backup and a possible rollback.", humanBytes(free), humanBytes(2*need+minFreeAfterBackup)),
			Hint: "Delete old backups (after downloading any you want to keep) or free disk space, then try again."}
	}
	if err := s.archiveRefusal("Nothing was changed."); err != nil {
		return err
	}
	_, wasRunning, err := s.containerRunning(ctx)
	if err != nil {
		return err
	}
	if warn && wasRunning {
		s.warnPlayers(ctx, h, "Updating")
	}
	if err := s.stopServer(ctx, h); err != nil {
		return err
	}
	h.phase("backing_up")
	b, err := s.saveVerifiedRollback(*prev, actor, fmt.Sprintf("Automatic backup before updating from %s %s to %s", kind, prev.MinecraftVersion, e.MinecraftVersion))
	if err != nil {
		s.startPrevious(ctx, h, prev, wasRunning)
		return s.withRefusalHint(fmt.Errorf("could not save a verified backup first, so nothing was changed: %w", err))
	}
	h.set("backupId", b.ID)
	if err := s.ensureSoftware(ctx, h, &next); err != nil {
		_ = s.saveServerConfig(*prev)
		s.startPrevious(ctx, h, prev, wasRunning)
		return &apiError{Msg: fmt.Sprintf("%s %s could not be downloaded and verified (%s), so nothing was changed.", kind, e.MinecraftVersion, err.Error()), Hint: "The server runs " + prev.MinecraftVersion + " as before. Try again later."}
	}
	if err := s.saveServerConfig(next); err != nil {
		s.startPrevious(ctx, h, prev, wasRunning)
		return err
	}
	_ = s.setDesired(api.DesiredRunning)
	h.phase("starting")
	startErr := s.startServer(ctx, h, next)
	if startErr == nil {
		if prev.Software == nil {
			s.audit(actor, "server.version", e.ID, "succeeded", fmt.Sprintf("Paper %s build %d → %s build %d; backup %s", prev.MinecraftVersion, prev.PaperBuild, e.MinecraftVersion, e.PaperBuild, b.ID))
		} else {
			s.audit(actor, "server.version", e.ID, "succeeded", fmt.Sprintf("%s → %s; backup %s", versionText(*prev), versionText(next), b.ID))
		}
		s.recordEvent(s.now(), "server_version_changed", "", "playkeeper", versionText(*prev)+" → "+versionText(next))
		return nil
	}
	h.phase("reverting")
	s.log.Warn("the new server version did not start; restoring the backup", "version", e.MinecraftVersion, "err", startErr)
	_ = s.stopServer(ctx, h)
	if err := s.putBackupBack(b); err != nil {
		s.audit(actor, "server.version", e.ID, "failed", "rollback failed: "+err.Error())
		return &apiError{Msg: fmt.Sprintf("%s %s did not start (%s), and putting the backup back failed: %s", kind, e.MinecraftVersion, startErr.Error(), err.Error()),
			Hint: "Your world is safe in backup " + b.ID + ". Restore it from the World page."}
	}
	_ = s.saveServerConfig(*prev)
	if err := s.startServer(ctx, h, *prev); err != nil {
		return &apiError{Msg: fmt.Sprintf("%s %s did not start (%s). The backup was put back, but %s did not start either: %s", kind, e.MinecraftVersion, startErr.Error(), prev.MinecraftVersion, err.Error()), Hint: "Press Start on the Overview."}
	}
	s.audit(actor, "server.version", e.ID, "rolled back", startErr.Error())
	return &apiError{Msg: fmt.Sprintf("%s %s did not start (%s), so Playkeeper put the backup from before the update back. The server runs %s again.", kind, e.MinecraftVersion, startErr.Error(), prev.MinecraftVersion),
		Hint: "Nothing was lost. Open the Console to see why the new version stopped."}
}

// putBackupBack replaces the live world with a verified backup's. The world
// the failed start touched is deleted once the backup's copy is in place.
func (s *server) putBackupBack(b *api.Backup) error {
	f, err := os.Open(s.backupPath(b.FileName))
	if err != nil {
		return err
	}
	p, err := s.stageArchive(f, "backup:"+b.ID, b.SizeBytes, s)
	f.Close()
	if err != nil {
		return err
	}
	st, err := s.loadStage(p.ID)
	if err != nil {
		return err
	}
	defer os.RemoveAll(st.dir)
	live := s.dataDir()
	failed := live + ".failed-update-" + s.now().UTC().Format("20060102-150405")
	if err := renameDir(live, failed); err != nil {
		return err
	}
	if err := renameDir(st.data, live); err != nil {
		if rerr := renameDir(failed, live); rerr != nil {
			return fmt.Errorf("%v; moving the world back also failed (%v): it is at %s", err, rerr, failed)
		}
		return err
	}
	if err := chownTree(live, s.cfg.GameUID, s.cfg.GameGID); err != nil {
		s.log.Warn("chown restored world", "err", err)
	}
	if err := os.RemoveAll(failed); err != nil {
		s.log.Warn("could not delete the world the failed update touched", "path", failed, "err", err)
	}
	return nil
}

// warnPlayers tells anyone online that the server is about to go down for
// what ("Updating", "Restarting"), then gives them WarnDelay to finish what
// they are doing.
func (s *server) warnPlayers(ctx context.Context, h *opHandle, what string) {
	s.mu.Lock()
	players := s.players
	s.mu.Unlock()
	if players == nil || players.Online == 0 {
		return
	}
	h.phase("warning_players")
	if _, err := s.rconCommand("say " + what + " in " + inWords(s.opts.WarnDelay) + ", back soon!"); err != nil {
		s.log.Warn("could not warn players before going down", "server", s.id, "err", err)
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(s.opts.WarnDelay):
	}
}

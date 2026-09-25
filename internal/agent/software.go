package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/minecraft/software"
)

// Server software of every type besides Paper: the version lists, the
// builds to choose from, the verified install and the check before every
// start. Paper keeps its own path (versions.go and ensureServerSoftware),
// but shares the check for software changed on disk and the reinstall.

// softwareCache keeps each type's version list and build lists, and
// Mojang's release dates, for catalogTTL like Paper's version list.
type softwareCache struct {
	mu       sync.Mutex
	catalogs map[string]cachedCatalog
	builds   map[string]cachedBuilds

	datesMu sync.Mutex
	dates   map[string]time.Time
	datesAt time.Time
}

type cachedCatalog struct {
	entries []api.CatalogEntry
	at      time.Time
}

type cachedBuilds struct {
	builds []software.Build
	at     time.Time
}

// maxCachedBuilds bounds the build lists kept, one per type and version.
const maxCachedBuilds = 64

func (a *Agent) sources() software.Sources {
	return software.Sources{Client: a.opts.UpstreamClient}
}

// typeCheck is how a type's downloads are verified, for the dashboard's
// comparison of types.
func typeCheck(id string) string {
	if id == api.TypePaper {
		return string(software.LevelFull)
	}
	if as, ok := software.AssuranceOf(id); ok {
		return string(as.Level)
	}
	return ""
}

// typeCatalog is the list of versions to offer for a server type and when
// it was fetched, reused for catalogTTL. If the upstream cannot be reached,
// the last list is used if there is one.
func (a *Agent) typeCatalog(ctx context.Context, typ string) ([]api.CatalogEntry, time.Time, error) {
	if typ == api.TypePaper {
		return a.versionCatalog(ctx)
	}
	c := &a.software
	c.mu.Lock()
	defer c.mu.Unlock()
	hit, ok := c.catalogs[typ]
	if ok && a.now().Sub(hit.at) < catalogTTL {
		return hit.entries, hit.at, nil
	}
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	rels, err := a.sources().Catalog(cctx, typ)
	if err != nil {
		a.log.Warn("could not load a server type's version list", "type", typ, "err", err)
		if ok {
			return hit.entries, hit.at, nil
		}
		return nil, time.Time{}, err
	}
	entries := make([]api.CatalogEntry, 0, len(rels))
	for _, r := range rels {
		pin := api.SoftwarePin(r.Pin)
		entries = append(entries, api.CatalogEntry{ID: r.ID, Label: r.Label, MinecraftVersion: r.MinecraftVersion, Java: r.Java,
			Recommended: r.Recommended, Notes: r.Notes, Channel: string(r.Channel), Experimental: r.Experimental, Supported: true,
			Software: &pin, Build: pinBuild(r.Pin)})
	}
	if c.catalogs == nil {
		c.catalogs = map[string]cachedCatalog{}
	}
	now := a.now()
	c.catalogs[typ] = cachedCatalog{entries: entries, at: now}
	return entries, now, nil
}

// typeEntry finds a version to create a server of a type with.
func (a *Agent) typeEntry(ctx context.Context, typ, id string) (api.CatalogEntry, error) {
	if typ == api.TypePaper {
		return a.catalogEntry(ctx, id)
	}
	entries, _, err := a.typeCatalog(ctx, typ)
	if err != nil {
		return api.CatalogEntry{}, softwareError(err)
	}
	for _, e := range entries {
		if e.ID == id {
			return e, nil
		}
	}
	return api.CatalogEntry{}, errInvalid("Unknown %s version. Choose one of the listed versions.", typeName(typ))
}

// releaseDates is when Mojang released each Minecraft release, reused for
// catalogTTL. Without Mojang it is the last dates known, or none: the
// version lists work without them.
func (a *Agent) releaseDates(ctx context.Context) map[string]time.Time {
	c := &a.software
	c.datesMu.Lock()
	defer c.datesMu.Unlock()
	if c.dates != nil && a.now().Sub(c.datesAt) < catalogTTL {
		return c.dates
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	d, err := a.sources().ReleaseDates(cctx)
	if err != nil {
		a.log.Warn("could not load Minecraft release dates", "err", err)
		if c.dates == nil {
			c.dates = map[string]time.Time{}
		}
		// Ask again in a minute, not on every request.
		c.datesAt = a.now().Add(time.Minute - catalogTTL)
		return c.dates
	}
	c.dates, c.datesAt = d, a.now()
	return d
}

// withReleaseDates returns a copy of entries with Mojang's release dates.
func withReleaseDates(entries []api.CatalogEntry, dates map[string]time.Time) []api.CatalogEntry {
	out := slices.Clone(entries)
	for i := range out {
		if d, ok := dates[out[i].MinecraftVersion]; ok {
			out[i].ReleasedAt = &d
		}
	}
	return out
}

// typeBuilds lists a type's builds for one Minecraft version, newest first,
// reused for catalogTTL.
func (a *Agent) typeBuilds(ctx context.Context, typ, mc string) ([]software.Build, time.Time, error) {
	key := typ + "@" + mc
	c := &a.software
	c.mu.Lock()
	defer c.mu.Unlock()
	hit, ok := c.builds[key]
	if ok && a.now().Sub(hit.at) < catalogTTL {
		return hit.builds, hit.at, nil
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	bs, err := a.sources().Builds(cctx, typ, mc)
	if err != nil {
		if ok {
			return hit.builds, hit.at, nil
		}
		return nil, time.Time{}, err
	}
	if c.builds == nil || len(c.builds) >= maxCachedBuilds {
		c.builds = map[string]cachedBuilds{}
	}
	now := a.now()
	c.builds[key] = cachedBuilds{builds: bs, at: now}
	return bs, now, nil
}

func (a *Agent) hCatalogBuilds(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	typ, mc := q.Get("type"), q.Get("version")
	if !software.Supported(typ) {
		writeError(w, errInvalid("Only Vanilla, Purpur, Fabric, Quilt and NeoForge servers have builds to list."))
		return
	}
	bs, at, err := a.typeBuilds(r.Context(), typ, mc)
	if err != nil {
		writeError(w, softwareError(err))
		return
	}
	out := api.SoftwareBuilds{Type: typ, MinecraftVersion: mc, Builds: make([]api.SoftwareBuild, 0, len(bs)), CheckedAt: at}
	for _, b := range bs {
		out.Builds = append(out.Builds, api.SoftwareBuild{Version: b.Version, Channel: string(b.Channel), Recommended: b.Recommended})
	}
	writeJSON(w, http.StatusOK, out)
}

// pinBuild is the build a pin names, as the dashboard shows it.
func pinBuild(p software.Pin) string {
	switch p.Type {
	case software.Purpur:
		return strconv.Itoa(p.PurpurBuild)
	case software.Fabric:
		return p.FabricLoader
	case software.Quilt:
		return p.QuiltLoader
	case software.NeoForge:
		return p.NeoForgeVersion
	}
	return ""
}

// pinFor is the pin to create a server with: the catalog entry's, with the
// build chosen from the type's list, or the recommended one when build is
// empty. It returns the chosen build's channel too.
func (a *Agent) pinFor(ctx context.Context, e api.CatalogEntry, build string) (software.Pin, software.Channel, error) {
	if e.Software == nil {
		return software.Pin{}, "", errInvalid("This version has no build to pin.")
	}
	pin := software.Pin(*e.Software)
	channel := software.Stable
	if e.Experimental {
		channel = software.Channel(e.Channel)
	}
	if build == "" || build == pinBuild(pin) || pin.Type == software.Vanilla {
		if build != "" && pin.Type == software.Vanilla {
			return software.Pin{}, "", errInvalid("Vanilla servers have no build to choose.")
		}
		return pin, channel, pin.Validate()
	}
	bs, _, err := a.typeBuilds(ctx, pin.Type, pin.MinecraftVersion)
	if err != nil {
		return software.Pin{}, "", softwareError(err)
	}
	i := slices.IndexFunc(bs, func(b software.Build) bool { return b.Version == build })
	if i < 0 {
		return software.Pin{}, "", errInvalid("%s has no build %q for Minecraft %s. Choose one of the listed builds.", typeName(pin.Type), build, pin.MinecraftVersion)
	}
	if err := bs[i].Pin.Validate(); err != nil {
		return software.Pin{}, "", softwareError(err)
	}
	return bs[i].Pin, bs[i].Channel, nil
}

// withPin returns sc running the pinned software of a type other than Paper.
func withPin(sc api.ServerConfig, e api.CatalogEntry, pin software.Pin) api.ServerConfig {
	p := api.SoftwarePin(pin)
	sc.VersionID, sc.MinecraftVersion, sc.Software = e.ID, pin.MinecraftVersion, &p
	sc.PaperBuild, sc.JarSHA256, sc.JarVerifiedAt = 0, "", nil
	sc.Image = minecraft.Image
	return sc
}

// softwareLabel names a server's software the way people say it: "Paper
// 26.1.2 build 41", "Fabric 26.2.1 with Fabric Loader 0.17.2".
func softwareLabel(sc api.ServerConfig) string {
	p := sc.Software
	if p == nil {
		return fmt.Sprintf("Paper %s build %d", sc.MinecraftVersion, sc.PaperBuild)
	}
	switch p.Type {
	case software.Purpur:
		return fmt.Sprintf("Purpur %s build %d", p.MinecraftVersion, p.PurpurBuild)
	case software.Fabric:
		return fmt.Sprintf("Fabric %s with Fabric Loader %s", p.MinecraftVersion, p.FabricLoader)
	case software.Quilt:
		return fmt.Sprintf("Quilt %s with Quilt Loader %s", p.MinecraftVersion, p.QuiltLoader)
	case software.NeoForge:
		return fmt.Sprintf("NeoForge %s for Minecraft %s", p.NeoForgeVersion, p.MinecraftVersion)
	}
	return typeName(p.Type) + " " + p.MinecraftVersion
}

// softwareError turns the software package's errors into API errors with
// their plain-language hint.
func softwareError(err error) error {
	var e *software.Error
	if !errors.As(err, &e) {
		return err
	}
	status, code := http.StatusBadGateway, api.CodeUpstream
	switch e.Kind {
	case software.KindUnsupported, software.KindNotFound, software.KindNoVersions:
		status, code = http.StatusBadRequest, api.CodeInvalid
	case software.KindMissingFile, software.KindUnsafePath:
		status, code = http.StatusConflict, api.CodeConflict
	}
	return &apiError{Status: status, Code: code, Msg: e.Msg, Hint: e.Hint}
}

// takesPlugins is true for the types that load Bukkit plugins, whose
// bStats statistics Playkeeper turns off.
func takesPlugins(sc api.ServerConfig) bool {
	return sc.Software == nil || sc.Software.Type == software.Purpur
}

// ensureSoftware makes sure the server's software is installed, verified
// and unchanged before a start.
func (s *server) ensureSoftware(ctx context.Context, h *opHandle, sc *api.ServerConfig) error {
	if sc.Software == nil {
		return s.ensureServerSoftware(ctx, h, sc)
	}
	return s.ensureTypeSoftware(ctx, h, sc)
}

// ensureTypeSoftware checks the install of a type other than Paper against
// the manifest recorded right after it was verified, or installs it the way
// its plan says. A file that changed since is not replaced on its own: it
// can mean someone tampered with the machine, so the server stays off until
// the user reinstalls. Missing files are installed again.
func (s *server) ensureTypeSoftware(ctx context.Context, h *opHandle, sc *api.ServerConfig) error {
	pin := software.Pin(*sc.Software)
	if err := pin.Validate(); err != nil {
		return softwareError(err)
	}
	if m, err := s.loadManifest(); err == nil && m.Pin == pin {
		verr := m.Verify(s.dataDir())
		if verr == nil {
			s.clearSoftwareChanged()
			return nil
		}
		if change := s.changedFile(verr, *sc); change != nil {
			return s.softwareChangedError(change)
		}
		s.log.Info("installing server software again", "server", s.id, "reason", verr)
	}
	return s.installSoftware(ctx, h, sc, pin)
}

// installSoftware carries out a type's install plan: every download checked
// against the hash its upstream publishes, the launch jar, the setup-only
// container, then the checks Finish adds. The manifest it returns is kept
// outside the data directory, which the server container can write.
func (s *server) installSoftware(ctx context.Context, h *opHandle, sc *api.ServerConfig, pin software.Pin) error {
	h.phase(string(api.PhaseDownloading))
	s.setRunPhase(api.PhaseDownloading, "")
	res, err := s.sources().Resolve(ctx, pin)
	if err != nil {
		return softwareError(err)
	}
	plan, err := res.Plan()
	if err != nil {
		return softwareError(err)
	}
	// An install cut short must not be checked against the last record, as
	// if what it half wrote had been changed on disk.
	if err := s.forgetManifest(); err != nil {
		return err
	}
	data := s.dataDir()
	h.set("files", 0)
	h.set("filesTotal", len(plan.Downloads))
	for i, a := range plan.Downloads {
		if err := software.Download(ctx, s.opts.UpstreamClient, data, a); err != nil {
			return softwareError(err)
		}
		h.set("files", i+1)
	}
	if err := software.Prepare(data, plan); err != nil {
		return softwareError(err)
	}
	if err := s.ownSoftware(plan); err != nil {
		return err
	}
	if plan.Setup != nil {
		spec, _ := s.specWith(*sc, plan.Setup.Env, true)
		tail, code, err := s.runSetupContainer(ctx, spec)
		if err != nil {
			return err
		}
		if code != 0 {
			return &apiError{Msg: fmt.Sprintf("Installing %s failed (exit code %d): %s", softwareLabel(*sc), code, lastNonEmpty(tail)),
				Hint: "Check the Console for the installer's output, then press Start to try again."}
		}
	}
	h.phase("verifying_download")
	m, err := software.Finish(data, plan)
	if err != nil {
		return softwareError(err)
	}
	if err := s.saveManifest(m); err != nil {
		return err
	}
	now := s.now().UTC()
	sc.JarVerifiedAt = &now
	if err := s.saveServerConfig(*sc); err != nil {
		return err
	}
	s.clearSoftwareChanged()
	s.recordEvent(now, "server_software_verified", "", "playkeeper", fmt.Sprintf("%s: %d files checked", softwareLabel(*sc), len(m.Checks)))
	s.log.Info("server software verified", "server", s.id, "type", pin.Type, "files", len(m.Checks))
	return nil
}

// ownSoftware gives the game user the files and folders an install wrote,
// as Paper's setup container leaves its jar, so the server and NeoForge's
// installer can use them. Links are never followed.
func (s *server) ownSoftware(p software.Plan) error {
	if os.Geteuid() != 0 {
		return nil
	}
	rels := make([]string, 0, len(p.Downloads)+1)
	for _, a := range p.Downloads {
		rels = append(rels, a.Path)
	}
	if p.Launcher != nil {
		rels = append(rels, p.Launcher.Path)
	}
	done := map[string]bool{}
	for _, rel := range rels {
		cur := s.dataDir()
		for _, seg := range strings.Split(rel, "/") {
			cur = filepath.Join(cur, seg)
			if done[cur] {
				continue
			}
			done[cur] = true
			fi, err := os.Lstat(cur)
			if err != nil {
				return err
			}
			if fi.Mode()&fs.ModeSymlink != 0 {
				return &apiError{Msg: "Playkeeper refused to use " + rel + ": part of it is a symbolic link.", Hint: "Reinstall the server software."}
			}
			if err := os.Lchown(cur, s.cfg.GameUID, s.cfg.GameGID); err != nil {
				return err
			}
		}
	}
	return nil
}

// runSetupContainer runs a setup-only container to its end, copying its
// output to the console, and returns its last lines and exit code.
func (s *server) runSetupContainer(ctx context.Context, spec docker.ContainerConfig) ([]string, int, error) {
	setupName := s.containerName() + "-setup"
	_ = s.docker.ContainerRemove(ctx, setupName, true)
	id, err := s.docker.ContainerCreate(ctx, setupName, spec)
	if err != nil {
		return nil, 0, s.dockerErr(err)
	}
	defer s.docker.ContainerRemove(context.Background(), id, true)
	if err := s.docker.ContainerStart(ctx, id); err != nil {
		return nil, 0, s.dockerErr(err)
	}
	var tail []string
	logs, err := s.docker.ContainerLogs(ctx, id, docker.LogsOptions{Follow: true})
	if err == nil {
		for {
			l, err := logs.Next()
			if err != nil {
				break
			}
			text := minecraft.CleanLine(l.Text)
			s.console.append(l.TS, text)
			tail = append(tail, text)
			if len(tail) > 8 {
				tail = tail[1:]
			}
		}
		logs.Close()
	}
	var c docker.ContainerJSON
	for i := 0; i < 60; i++ {
		c, err = s.docker.ContainerInspect(ctx, id)
		if err != nil || !c.State.Running {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		return tail, 0, s.dockerErr(err)
	}
	return tail, c.State.ExitCode, nil
}

// Manifest: the record of a verified install, kept where only root can
// read it.

func (s *server) manifestPath() string {
	return filepath.Join(s.cfg.AgentDir(), "servers", s.id, "software.json")
}

func (s *server) loadManifest() (software.Manifest, error) {
	s.mu.Lock()
	cached := s.manifest
	s.mu.Unlock()
	if cached != nil {
		return *cached, nil
	}
	b, err := os.ReadFile(s.manifestPath())
	if err != nil {
		return software.Manifest{}, err
	}
	var m software.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return software.Manifest{}, err
	}
	s.mu.Lock()
	s.manifest = &m
	s.mu.Unlock()
	return m, nil
}

func (s *server) saveManifest(m software.Manifest) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	p := s.manifestPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if err := writeFileAtomic(p, b, 0o600); err != nil {
		return err
	}
	s.mu.Lock()
	s.manifest = &m
	s.mu.Unlock()
	return nil
}

// forgetManifest drops the record, so the next start installs again.
func (s *server) forgetManifest() error {
	s.mu.Lock()
	s.manifest = nil
	s.mu.Unlock()
	if err := os.Remove(s.manifestPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// runEnv is the part of the server container's env that depends on the
// type, from the manifest. Before the first install there is none.
func (s *server) runEnv() []string {
	m, err := s.loadManifest()
	if err != nil {
		return nil
	}
	return m.Run.Env
}

// writeFileAtomic writes data through a temporary file in the same folder
// and a rename, so a crash never leaves half a file.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Software changed on disk.

// changedFile describes the file a failed check found changed, or returns
// nil when the check failed another way (a file missing, a record Playkeeper
// cannot use), which a new install fixes without asking.
func (s *server) changedFile(verr error, sc api.ServerConfig) *api.SoftwareChange {
	var e *software.Error
	if !errors.As(verr, &e) {
		return nil
	}
	switch e.Kind {
	case software.KindHashMismatch, software.KindSizeMismatch, software.KindUnsafePath:
	default:
		return nil
	}
	c := &api.SoftwareChange{File: e.Params["file"], Algorithm: e.Params["algorithm"], Recorded: e.Params["want"], Found: e.Params["got"],
		InstalledAt: sc.JarVerifiedAt, DetectedAt: s.now().UTC(), Software: softwareLabel(sc)}
	if fi, err := os.Lstat(filepath.Join(s.dataDir(), filepath.FromSlash(c.File))); err == nil && c.File != "" {
		t := fi.ModTime().UTC()
		c.ChangedAt = &t
	}
	return c
}

// softwareChangedError keeps the server off after a start found its
// software changed, and says so.
func (s *server) softwareChangedError(c *api.SoftwareChange) error {
	s.mu.Lock()
	first := s.softwareChanged == nil
	s.softwareChanged = c
	s.mu.Unlock()
	_ = s.setDesired(api.DesiredStopped)
	if first {
		s.recordEvent(c.DetectedAt, "server_software_changed", "", "playkeeper", c.File+" does not match "+c.Algorithm+" "+short(c.Recorded))
		s.log.Warn("server software changed on disk; not starting", "server", s.id, "file", c.File, "want", c.Recorded, "got", c.Found)
	}
	return &apiError{Status: http.StatusConflict, Code: api.CodeConflict,
		Msg:  fmt.Sprintf("%s's server software doesn't match what Playkeeper installed, so it stayed off.", s.name()),
		Hint: "If you didn't replace it yourself, check who can sign in to this machine and change its passwords. Then reinstall the server software."}
}

func (s *server) clearSoftwareChanged() {
	s.mu.Lock()
	s.softwareChanged = nil
	s.mu.Unlock()
}

func short(h string) string {
	if len(h) > 16 {
		return h[:16]
	}
	return h
}

// hSoftwareReinstall downloads the server's pinned software again, checks
// it, records it and starts the server: the fix after a start found it
// changed on disk. The world, add-ons, settings and backups stay.
func (s *server) hSoftwareReinstall(w http.ResponseWriter, r *http.Request) {
	actor, err := actionActor(r)
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
	op, err := s.beginOp("reinstall", actor, func(ctx context.Context, h *opHandle) error {
		if err := s.stopServer(ctx, h); err != nil {
			return err
		}
		if err := s.discardSoftware(*sc); err != nil {
			return err
		}
		_ = s.setDesired(api.DesiredRunning)
		if err := s.startServer(ctx, h, *sc); err != nil {
			s.startFailed(ctx)
			return err
		}
		s.audit(actor, "server.software_reinstalled", s.id, "succeeded", softwareLabel(*sc))
		return nil
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

// discardSoftware makes the next start install the pinned software from
// scratch: Paper's jar goes, and so does the record of any other type's
// install, whose files are then checked and replaced one by one.
func (s *server) discardSoftware(sc api.ServerConfig) error {
	s.clearSoftwareChanged()
	if sc.Software != nil {
		return s.forgetManifest()
	}
	if err := os.Remove(s.jarPath(sc)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

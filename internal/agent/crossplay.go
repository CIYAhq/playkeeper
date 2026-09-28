package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/backup"
	"github.com/CIYAhq/playkeeper/internal/curated"
	"github.com/CIYAhq/playkeeper/internal/gamefiles"
	"github.com/CIYAhq/playkeeper/internal/names"
)

// Crossplay: Bedrock players join a Paper or Purpur server through Geyser
// and Floodgate, which one switch in the server's Settings installs from
// Modrinth and Hangar. Geyser's UDP port is handled like voice chat's: the
// lowest free one from Bedrock's own 19132, published from the container
// with the same number, written into Geyser's config before every start,
// recorded in backups and closed when crossplay goes off. The server is on
// crossplay while its settings hold that port.

func init() {
	opLabels["crossplay_on"] = "turning on crossplay"
	opLabels["crossplay_off"] = "turning off crossplay"
}

// crossplayOn reports whether the server has crossplay.
func crossplayOn(sc *api.ServerConfig) bool { return sc != nil && sc.CrossplayPort > 0 }

func isCrossplay(key addons.Key) bool { return curated.IsCrossplayProject(key) }

// crossplayFits says whether Geyser and Floodgate have versions for a server
// type and Minecraft version: nil when both do, else why not. GeyserMC
// publishes every Geyser build on Modrinth as a beta, so a beta counts.
func (a *Agent) crossplayFits(ctx context.Context, srv addons.Server) (*addons.Notice, error) {
	if err := curated.CrossplayFor(srv.Type); err != nil {
		var e *addons.Error
		if errors.As(err, &e) {
			return &e.Notice, nil
		}
		return nil, err
	}
	key := srv.Type + "\x00" + srv.MinecraftVersion
	if n, ok := a.crossplayChecks.get(key, a.now()); ok {
		return n, nil
	}
	var found *addons.Notice
	for _, p := range curated.CrossplayProjects() {
		d, err := a.lib().Details(ctx, srv, p.Source, p.ID)
		if err != nil {
			return nil, err
		}
		prerelease := d.Notice != nil && d.Notice.Kind == addons.KindOnlyPrerelease
		if d.Latest == nil || d.Latest.ExternalURL != "" || d.Notice != nil && !(prerelease && p.Source == addons.Modrinth) {
			n := addons.Notice{Kind: addons.KindNoVersion, Params: map[string]string{"name": p.Title, "minecraft": srv.MinecraftVersion},
				Msg:  fmt.Sprintf("%s has no version for Minecraft %s yet, so crossplay can't be turned on.", p.Title, srv.MinecraftVersion),
				Hint: fmt.Sprintf("GeyserMC usually follows a new Minecraft version within days. Try again later, or run Minecraft %s on the version %s supports.", srv.MinecraftVersion, p.Title)}
			if d.Notice != nil && !prerelease {
				n.Msg = d.Notice.Msg
			}
			found = &n
			break
		}
	}
	a.crossplayChecks.put(key, found, a.now())
	return found, nil
}

func (s *server) hCrossplay(w http.ResponseWriter, r *http.Request) {
	sc, err := s.serverConfig()
	if err != nil {
		writeError(w, err)
		return
	}
	if sc == nil {
		writeError(w, errNotCreated())
		return
	}
	out := api.Crossplay{On: crossplayOn(sc), Port: sc.CrossplayPort, Plugins: []api.CrossplayPlugin{}, Prefix: curated.FloodgatePrefix}
	installed, err := s.installedAddons()
	if err != nil {
		writeError(w, err)
		return
	}
	for _, rec := range installed {
		if isCrossplay(rec.Key()) {
			out.Plugins = append(out.Plugins, api.CrossplayPlugin{Name: rec.Name, VersionNumber: rec.VersionNumber, Source: string(rec.Source)})
		}
	}
	if !out.On {
		srv := addons.Server{Dir: s.dataDir(), Type: s.serverType(sc), MinecraftVersion: sc.MinecraftVersion, Owner: s.gameOwner()}
		n, err := s.crossplayFits(r.Context(), srv)
		if err != nil {
			writeError(w, addonError(err))
			return
		}
		if n != nil {
			an := apiNotice(*n)
			out.Notice = &an
		} else {
			out.Available = true
			if port, err := s.freeCrossplayPort(s.id); err == nil {
				out.Port = port
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) hCrossplaySet(w http.ResponseWriter, r *http.Request) {
	var req api.CrossplayRequest
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
	switch {
	case req.On && crossplayOn(sc):
		writeError(w, errConflict("Crossplay is already on.", ""))
		return
	case !req.On && !crossplayOn(sc):
		writeError(w, errConflict("Crossplay is already off.", ""))
		return
	case req.On:
		if err := curated.CrossplayFor(s.serverType(sc)); err != nil {
			writeError(w, addonError(err))
			return
		}
	}
	kind, run := "crossplay_off", s.crossplayOff
	if req.On {
		kind, run = "crossplay_on", s.crossplayOnOp
	}
	op, err := s.beginOp(kind, actor, func(ctx context.Context, h *opHandle) error { return run(ctx, h, actor) })
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

// crossplayOnOp gives crossplay its UDP port and settings, then installs
// Geyser and Floodgate: an install that can't open the port installs
// nothing, and one that fails closes the port again. A running server is
// restarted to publish the port and load the plugins; a stopped one gets
// them when it next starts. The provider's firewall is left to the owner.
func (s *server) crossplayOnOp(ctx context.Context, h *opHandle, actor string) error {
	sc, srv, _, err := s.addonContext()
	if err != nil {
		return err
	}
	if crossplayOn(sc) {
		return errConflict("Crossplay is already on.", "")
	}
	if err := curated.CrossplayFor(srv.Type); err != nil {
		return noticeError(h, err)
	}
	h.phase("opening_port")
	port, release, err := s.holdCrossplayPort(s.id, curated.CrossplayPort)
	defer release()
	if err == nil {
		err = curated.WriteCrossplayConfig(srv.Dir, port, s.gameFilesOwner())
	}
	if err != nil {
		return noticeError(h, err)
	}
	sc.CrossplayPort = port
	if err := s.saveServerConfig(*sc); err != nil {
		return err
	}
	h.set("port", port)
	s.audit(actor, "crossplay.port_opened", "crossplay", "succeeded", fmt.Sprintf("Bedrock players on UDP %d", port))
	h.phase("installing")
	if err := s.installCrossplay(ctx, h, actor, srv); err != nil {
		if cerr := s.closeCrossplay(actor, nil); cerr != nil {
			s.log.Warn("crossplay wasn't installed and its port could not be closed", "server", s.id, "err", cerr)
		}
		return err
	}
	_, running, err := s.containerRunning(ctx)
	if err != nil {
		return restartUnchecked("Crossplay is on", "Restart the server so Bedrock players can join once Docker answers.", err)
	}
	h.set("restartNeeded", false)
	if !running {
		return nil
	}
	h.phase("restarting")
	if err := s.stopServer(ctx, h); err != nil {
		return err
	}
	if err := s.startServer(ctx, h, *sc); err != nil {
		s.startFailed(ctx)
		return err
	}
	return nil
}

// installCrossplay installs whichever of Geyser and Floodgate the server
// hasn't got. Both are planned before either downloads, so one that can't
// be installed installs neither, and if the second still fails, the first
// comes out again. A copy already in the plugins folder, managed or put
// there by hand, is used as it is.
func (s *server) installCrossplay(ctx context.Context, h *opHandle, actor string, srv addons.Server) error {
	installed, err := s.installedAddons()
	if err != nil {
		return err
	}
	lib := s.lib()
	var reqs []addons.InstallRequest
	for _, p := range curated.CrossplayProjects() {
		if slices.ContainsFunc(installed, func(rec addons.Installed) bool { return isCrossplay(rec.Key()) && sameCrossplayPlugin(rec, p) }) {
			continue
		}
		req := addons.InstallRequest{Source: p.Source, Project: p.ID, AllowPrerelease: p.Source == addons.Modrinth}
		plan, err := lib.PlanInstall(ctx, srv, installed, req)
		if err != nil {
			return noticeError(h, err)
		}
		if onlyAlreadyThere(plan, p.Title) {
			continue
		}
		if !plan.Ready {
			n := addons.Notice{Kind: addons.KindNoVersion, Msg: p.Title + " can't be installed.", Hint: "Try again in a few minutes."}
			switch {
			case len(plan.Blockers) > 0:
				n = plan.Blockers[0]
			case len(plan.Manual) > 0:
				n = plan.Manual[0].Notice
			}
			return noticeError(h, &addons.Error{Notice: n})
		}
		req.Fingerprint = plan.Fingerprint
		reqs = append(reqs, req)
	}
	var put []addons.Installed
	undo := func() {
		for _, rec := range put {
			if _, err := lib.Uninstall(context.Background(), srv, append(slices.Clone(installed), put...), rec.Key(), addons.UninstallOptions{}); err != nil {
				s.log.Warn("could not take out a crossplay plugin after the other failed to install", "server", s.id, "plugin", rec.Name, "err", err)
			}
		}
	}
	for _, req := range reqs {
		h.set("plugin", crossplayTitle(req))
		res, err := lib.Install(ctx, srv, append(slices.Clone(installed), put...), req)
		if err != nil {
			undo()
			return noticeError(h, err)
		}
		put = append(put, res.Installed...)
	}
	if len(put) == 0 {
		return nil
	}
	if err := s.saveAddons(put, nil, true); err != nil {
		undo()
		return err
	}
	for _, rec := range put {
		s.audit(actor, "addon.installed", string(rec.Source)+":"+rec.ProjectID, "succeeded", rec.Name+" "+rec.VersionNumber+" for crossplay")
	}
	return nil
}

func sameCrossplayPlugin(rec addons.Installed, p curated.Project) bool {
	return strings.EqualFold(rec.Name, p.Title) || strings.EqualFold(rec.Slug, p.Slug)
}

func crossplayTitle(req addons.InstallRequest) string {
	for _, p := range curated.CrossplayProjects() {
		if p.Source == req.Source && p.ID == req.Project {
			return p.Title
		}
	}
	return req.Project
}

// onlyAlreadyThere reports whether all that stops a plan is that the server
// has the add-on already: from its other listing, by hand, or as a file
// with the name its download has. A copy from GeyserMC's own site calls
// itself Geyser-Spigot, not Geyser, so only its file name gives it away.
func onlyAlreadyThere(p *addons.Plan, name string) bool {
	return len(p.Blockers) > 0 && !slices.ContainsFunc(p.Blockers, func(n addons.Notice) bool {
		return (n.Kind != addons.KindDuplicate && n.Kind != addons.KindFileExists) || !strings.EqualFold(n.Params["name"], name)
	})
}

// noticeError puts an add-on library error's notice in the operation's
// detail and returns it as the operation's error.
func noticeError(h *opHandle, err error) error {
	if n := noticeOf(err); n != nil {
		h.set("notice", *n)
		return &apiError{Msg: n.Message, Hint: n.Hint}
	}
	return err
}

// crossplayOff removes Geyser and Floodgate, keeping their settings (and
// Floodgate's key, so linked accounts stay linked), and closes the port in
// the transaction that drops their records. A running server is restarted
// so the port and the plugins go now, and without Docker's answer on
// whether it runs, nothing changes. A file Playkeeper didn't install, or
// one changed since, stays, and the operation says so after closing the
// port.
func (s *server) crossplayOff(ctx context.Context, h *opHandle, actor string) error {
	sc, srv, _, err := s.addonContext()
	if err != nil {
		return err
	}
	if !crossplayOn(sc) {
		return errConflict("Crossplay is already off.", "")
	}
	if _, _, err := s.containerRunning(ctx); err != nil {
		return &apiError{Status: http.StatusServiceUnavailable, Code: api.CodeDockerUnavailable,
			Msg: "Crossplay is still on: Playkeeper couldn't tell whether the server is running, and a running server keeps letting Bedrock players in until it restarts. " + errText(err), Hint: "Try again once Docker answers."}
	}
	installed, err := s.installedAddons()
	if err != nil {
		return err
	}
	h.phase("removing")
	var drop []addons.Key
	var kept []string
	rest := slices.Clone(installed)
	for _, rec := range installed {
		if !isCrossplay(rec.Key()) {
			continue
		}
		if _, err := s.lib().Uninstall(ctx, srv, rest, rec.Key(), addons.UninstallOptions{}); err != nil {
			if addons.KindOf(err) != addons.KindNotFound {
				kept = append(kept, rec.FileName)
				continue
			}
		}
		drop = append(drop, rec.Key())
		rest = slices.DeleteFunc(rest, func(o addons.Installed) bool { return o.Key() == rec.Key() })
	}
	if err := s.closeCrossplay(actor, drop); err != nil {
		return err
	}
	for _, k := range drop {
		s.audit(actor, "addon.removed", string(k.Source)+":"+k.ProjectID, "succeeded", "with crossplay")
	}
	if sc, err = s.serverConfig(); err != nil || sc == nil {
		return err
	}
	_, running, err := s.containerRunning(ctx)
	if err != nil {
		return restartUnchecked("Crossplay is off", "Restart the server once Docker answers, so Bedrock players can't join any more.", err)
	}
	if running {
		h.phase("restarting")
		if err := s.stopServer(ctx, h); err != nil {
			return err
		}
		if err := s.startServer(ctx, h, *sc); err != nil {
			s.startFailed(ctx)
			return err
		}
	}
	if len(kept) > 0 {
		return &apiError{Msg: "Crossplay is off, but " + strings.Join(kept, " and ") + " stayed in the plugins folder: each changed since Playkeeper installed it.",
			Hint: "Remove it on the Plugins tab if you don't want it."}
	}
	return nil
}

// closeCrossplay takes the port out of the server's settings and drops the
// records of the plugins taken out with it, in one transaction, so the
// records and the port never disagree. The container is made without the
// port at the next start.
func (s *server) closeCrossplay(actor string, drop []addons.Key) error {
	sc, err := s.serverConfig()
	if err != nil || sc == nil || sc.CrossplayPort == 0 {
		return err
	}
	port := sc.CrossplayPort
	sc.CrossplayPort = 0
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := saveConfig(tx, s.id, *sc); err != nil {
		return err
	}
	if err := s.writeAddons(tx, nil, drop, len(drop) > 0); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.audit(actor, "crossplay.port_closed", "crossplay", "succeeded", fmt.Sprintf("Bedrock players' UDP %d", port))
	return nil
}

// freeCrossplayPort is the port crossplay would get on the server with id
// now: the lowest free one from Bedrock's own.
func (a *Agent) freeCrossplayPort(id string) (int, error) {
	a.voicePorts.mu.Lock()
	defer a.voicePorts.mu.Unlock()
	return curated.PickPort(curated.CrossplayPort, a.udpPortTaken(id, true))
}

// holdCrossplayPort is freeCrossplayPort from from, held for the server
// until release, which the caller calls once the server's settings record
// the port or it isn't used after all. avoid are ports the server is being
// given for something else meanwhile.
func (a *Agent) holdCrossplayPort(id string, from int, avoid ...int) (int, func(), error) {
	a.voicePorts.mu.Lock()
	defer a.voicePorts.mu.Unlock()
	port, err := curated.PickPort(from, a.udpPortTaken(id, true, avoid...))
	if err != nil {
		return 0, func() {}, err
	}
	return port, a.voicePorts.hold(port, id), nil
}

func (s *server) gameFilesOwner() *gamefiles.Owner {
	if o := s.gameOwner(); o != nil {
		return &gamefiles.Owner{UID: o.UID, GID: o.GID}
	}
	return nil
}

// ensureCrossplay sets crossplay's port and settings in Geyser's and
// Floodgate's configs before a start, as a restored backup or an edit by
// hand may have changed them.
func (s *server) ensureCrossplay(sc api.ServerConfig) error {
	if !crossplayOn(&sc) {
		return nil
	}
	if err := curated.WriteCrossplayConfig(s.dataDir(), sc.CrossplayPort, s.gameFilesOwner()); err != nil {
		if n := noticeOf(err); n != nil {
			return &apiError{Msg: n.Message + " So the server was not started.", Hint: n.Hint}
		}
		return err
	}
	return nil
}

// bedrockJoin is where Bedrock players join the server while it has
// crossplay.
func (s *server) bedrockJoin(sc *api.ServerConfig) *api.BedrockJoin {
	if !crossplayOn(sc) {
		return nil
	}
	return &api.BedrockJoin{Host: s.bedrockHost(), Port: sc.CrossplayPort}
}

// bedrockHost is the machine's name once its own DNS records work, which
// Bedrock players type with the port, as Bedrock doesn't follow SRV
// records; "" without one.
func (a *Agent) bedrockHost() string {
	st := a.address()
	switch st.Kind {
	case api.AddressPlaykeeper:
		if f := st.Free; f != nil && f.Name.State == names.StateActive && f.Name.DNS == names.DNSOK && !freeLapsed(f.Name, a.now()) {
			return st.Host
		}
	case api.AddressOwn:
		if st.Check != nil && st.Check.Name.OK {
			return st.Host
		}
	}
	return ""
}

// refuseCrossplayAddons refuses to install Geyser or Floodgate from the
// Plugins tab, and to remove or forget them while crossplay is on: they
// come and go with crossplay's port. Updates are left alone.
func (s *server) refuseCrossplayAddons(install bool, keys ...addons.Key) error {
	for _, k := range keys {
		if !isCrossplay(k) {
			continue
		}
		name := "Geyser"
		if strings.EqualFold(k.ProjectID, "17") || strings.EqualFold(k.ProjectID, "floodgate") {
			name = "Floodgate"
		}
		if install {
			return errConflict(name+" comes with crossplay, which also opens the port Bedrock players need.", "Turn on crossplay in the server's Settings instead.")
		}
		if sc, err := s.serverConfig(); err == nil && crossplayOn(sc) {
			return errConflict(name+" is part of crossplay.", "Turn off crossplay in the server's Settings instead.")
		}
	}
	return nil
}

// manifestCrossplayPort is the backup manifest setting that records
// crossplay's UDP port, when the server had crossplay.
const manifestCrossplayPort = "crossplayPort"

// restoredCrossplay gives a restored server crossplay's port when its
// backup was made with crossplay on: the port the server has, or else the
// backup's or the next free one, held for the server until release, which
// the restore calls once the server's settings record how it ended. The
// restored Geyser config in dataDir gets that port. A backup made without
// crossplay leaves the server without it.
func (s *server) restoredCrossplay(sc, prev *api.ServerConfig, m backup.Manifest, dataDir string) (release func(), err error) {
	release = func() {}
	sc.CrossplayPort = 0
	from, _ := strconv.Atoi(m.Settings[manifestCrossplayPort])
	typ := cmp.Or(sc.Type, api.TypePaper)
	if from <= 0 || curated.CrossplayFor(typ) != nil {
		return release, nil
	}
	port := 0
	if prev != nil {
		port = prev.CrossplayPort
	}
	if port == 0 {
		if port, release, err = s.holdCrossplayPort(s.id, from, sc.VoiceChatPort); err != nil {
			port, release, err = s.holdCrossplayPort(s.id, curated.CrossplayPort, sc.VoiceChatPort)
		}
		if err != nil {
			return release, voiceChatError(err)
		}
	}
	if err := curated.WriteCrossplayConfig(dataDir, port, s.gameFilesOwner()); err != nil {
		release()
		return func() {}, voiceChatError(err)
	}
	sc.CrossplayPort = port
	return release, nil
}

// crossplayCheckTTL is how long a check of whether Geyser and Floodgate fit
// a type and Minecraft version holds.
const crossplayCheckTTL = time.Hour

// templateCrossplayLeftOut tells whoever exports a server with crossplay
// that a template doesn't carry it.
var templateCrossplayLeftOut = api.AddonNotice{Kind: "left_out_crossplay",
	Message: "Crossplay was left out, with Geyser and Floodgate: a template can't open the port Bedrock players need.",
	Hint:    "Turn crossplay on in the new server's Settings."}

package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/backup"
	"github.com/CIYAhq/playkeeper/internal/curated"
	"github.com/CIYAhq/playkeeper/internal/templates"
)

// Curated add-ons: the library's "Picked by Playkeeper", and the UDP port
// voice chat needs, opened when it's installed and closed when it's removed.

// curatedTTL is how long a check of which picks fit a type and Minecraft
// version holds.
const curatedTTL = time.Hour

// curatedPick is a curated entry whose project has a version for a server.
type curatedPick struct {
	entry   curated.Entry
	project curated.Project
	card    addons.Card
}

// curatedFor returns the curated entries that have a version for the
// server's type and Minecraft version, in the list's order. A project the
// source could not be asked about is left out, and not remembered.
func (a *Agent) curatedFor(ctx context.Context, srv addons.Server) ([]curatedPick, error) {
	key := srv.Type + "\x00" + srv.MinecraftVersion
	if picks, ok := a.curatedPicks.get(key, a.now()); ok {
		return picks, nil
	}
	entries := curated.ForType(srv.Type)
	found := make([]*curatedPick, len(entries))
	failed := make([]error, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := e.For(srv.Type)
			if err != nil {
				failed[i] = err
				return
			}
			d, err := a.lib().Details(ctx, srv, p.Source, p.ID)
			var ae *addons.Error
			if errors.As(err, &ae) && ae.Kind == addons.KindNotFound {
				return
			}
			if err != nil {
				failed[i] = err
				return
			}
			if d.Latest != nil && d.Notice == nil {
				found[i] = &curatedPick{entry: e, project: p, card: d.Card}
			}
		}()
	}
	wg.Wait()
	picks := []curatedPick{}
	complete := true
	for i := range entries {
		switch {
		case found[i] != nil:
			picks = append(picks, *found[i])
		case failed[i] != nil:
			complete = false
		}
	}
	if !complete && len(picks) == 0 {
		for _, err := range failed {
			if err != nil {
				return nil, err
			}
		}
	}
	if complete {
		a.curatedPicks.put(key, picks, a.now())
	}
	return picks, nil
}

func (s *server) hAddonCurated(w http.ResponseWriter, r *http.Request) {
	_, srv, _, err := s.addonContext()
	if err != nil {
		writeError(w, err)
		return
	}
	picks, err := s.curatedFor(r.Context(), srv)
	if err != nil {
		writeError(w, addonError(err))
		return
	}
	installed, err := s.installedAddons()
	if err != nil {
		writeError(w, err)
		return
	}
	out := api.CuratedAddons{Picks: []api.CuratedAddon{}}
	for _, p := range picks {
		out.Picks = append(out.Picks, api.CuratedAddon{
			ID: p.entry.ID, Card: apiCard(p.card, installed), Permission: webLink(p.project.Permission),
			Ports: s.addonPorts(addons.Key{Source: p.project.Source, ProjectID: p.project.ID}),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// voiceChat reports whether key is the project voice chat installs, whose
// UDP port Playkeeper opens.
func voiceChat(key addons.Key) bool {
	e, ok := curated.ByProject(key.Source, key.ProjectID)
	return ok && e.ID == curated.VoiceChatID
}

// addonPorts are the ports the add-on with key needs of its own on this
// server: the one it has, or the one Playkeeper would open for it.
func (s *server) addonPorts(key addons.Key) []api.AddonPort {
	if !voiceChat(key) {
		return nil
	}
	port, err := s.voiceChatPort()
	if err != nil {
		return nil
	}
	return []api.AddonPort{{Protocol: "udp", Port: port}}
}

// voiceChatPort is the UDP port voice chat has on this server, or the
// lowest free one from the add-on's own default.
func (s *server) voiceChatPort() (int, error) {
	if sc, err := s.serverConfig(); err == nil && sc != nil && sc.VoiceChatPort > 0 {
		return sc.VoiceChatPort, nil
	}
	return s.freeVoicePort(s.id, curated.VoiceChatPort)
}

// holdVoiceChatPort is voiceChatPort, held for this server until release.
func (s *server) holdVoiceChatPort() (int, func(), error) {
	if sc, err := s.serverConfig(); err == nil && sc != nil && sc.VoiceChatPort > 0 {
		return sc.VoiceChatPort, func() {}, nil
	}
	return s.holdVoicePort(s.id, curated.VoiceChatPort)
}

// voicePorts are the voice chat ports held for servers whose settings don't
// record them yet: one being given to a server, or one closed while its
// add-on is removed. Ports are picked and held under mu, so two servers
// can't be given the same one.
type voicePorts struct {
	mu   sync.Mutex
	held map[int]string // by the server's id
}

// hold holds port for the server with id until the returned release is
// called. Call it with mu held.
func (v *voicePorts) hold(port int, id string) func() {
	if v.held == nil {
		v.held = map[int]string{}
	}
	v.held[port] = id
	return sync.OnceFunc(func() {
		v.mu.Lock()
		defer v.mu.Unlock()
		if v.held[port] == id {
			delete(v.held, port)
		}
	})
}

// freeVoicePort is the lowest port from from that voice chat could have on
// the server with id, or on a new server when id is empty.
func (a *Agent) freeVoicePort(id string, from int) (int, error) {
	a.voicePorts.mu.Lock()
	defer a.voicePorts.mu.Unlock()
	return curated.PickPort(from, a.voicePortTaken(id))
}

// holdVoicePort is freeVoicePort, held for the server until release, which
// the caller calls once the server's settings record the port or it isn't
// used after all.
func (a *Agent) holdVoicePort(id string, from int) (int, func(), error) {
	a.voicePorts.mu.Lock()
	defer a.voicePorts.mu.Unlock()
	port, err := curated.PickPort(from, a.voicePortTaken(id))
	if err != nil {
		return 0, func() {}, err
	}
	return port, a.voicePorts.hold(port, id), nil
}

// voicePortTaken reports the ports voice chat can't have on the server with
// id, or on a new server when id is empty: any server's game port, the voice
// chat port of another or one held for another, the dashboard's, and any UDP
// port something on the machine already uses. Call it with voicePorts.mu
// held.
func (a *Agent) voicePortTaken(id string) func(int) bool {
	used := map[int]bool{a.cfg.PanelPort: true}
	for p, holder := range a.voicePorts.held {
		if holder != id {
			used[p] = true
		}
	}
	for _, o := range a.serverList() {
		used[o.gamePort] = true
		if o.id == id {
			continue
		}
		if sc, err := o.serverConfig(); err == nil && sc != nil && sc.VoiceChatPort > 0 {
			used[sc.VoiceChatPort] = true
		}
	}
	return func(p int) bool { return used[p] || a.opts.UDPPortInUse(p) }
}

// udpPortInUse reports whether something on the machine already listens on
// the UDP port.
func udpPortInUse(port int) bool {
	c, err := net.ListenPacket("udp", ":"+strconv.Itoa(port))
	if err != nil {
		return true
	}
	c.Close()
	return false
}

// installVoiceChat gives voice chat its UDP port, written into its settings
// and published from the container, before installing it: an install that
// can't open the port installs nothing, and one that fails closes the port
// it opened, so voice chat never stays without one. A running server is
// restarted to publish the port and load the add-on; with start, a stopped
// one is started. The provider's firewall is left to the owner.
func (s *server) installVoiceChat(ctx context.Context, h *opHandle, actor string, start bool, run func(addons.Server, []addons.Installed, func(addons.Progress)) (*addons.Result, error)) error {
	sc, srv, _, err := s.addonContext()
	if err != nil {
		return err
	}
	opened := sc.VoiceChatPort == 0
	h.phase("opening_port")
	if err := s.setUpVoiceChat(h, sc, srv, actor); err != nil {
		return err
	}
	if err := s.installAddons(ctx, h, actor, run); err != nil {
		if opened {
			_, release, cerr := s.closeVoiceChat(actor)
			release()
			if cerr != nil {
				s.log.Warn("voice chat wasn't installed and its port could not be closed", "server", s.id, "err", cerr)
			}
		}
		return err
	}
	_, running, err := s.containerRunning(ctx)
	if !running {
		if !start {
			h.set("restartNeeded", false)
			return nil
		}
		if err != nil {
			return err
		}
		return s.startNow(ctx, h)
	}
	if err := s.stopServer(ctx, h); err != nil {
		return err
	}
	if err := s.startServer(ctx, h, *sc); err != nil {
		s.startFailed(ctx)
		return err
	}
	h.set("restartNeeded", false)
	return nil
}

// setUpVoiceChat gives voice chat its UDP port: written into the add-on's
// settings in srv, and recorded in sc, which it saves.
func (s *server) setUpVoiceChat(h *opHandle, sc *api.ServerConfig, srv addons.Server, actor string) error {
	port, release, err := s.holdVoiceChatPort()
	defer release()
	if err == nil {
		_, err = curated.SetUpVoiceChat(srv, port)
	}
	if err != nil {
		if n := noticeOf(err); n != nil {
			h.set("notice", *n)
			return &apiError{Msg: n.Message, Hint: n.Hint}
		}
		return err
	}
	sc.VoiceChatPort = port
	if err := s.saveServerConfig(*sc); err != nil {
		return err
	}
	h.set("voiceChatPort", port)
	s.audit(actor, "addon.port_opened", string(addons.Modrinth)+":"+curatedVoiceChatProject(), "succeeded", fmt.Sprintf("voice chat on UDP %d", port))
	return nil
}

// templateVoiceChat opens voice chat's UDP port when a template brought the
// add-on, as the plan the user confirmed said (voiceChatNotice), before the
// server's container is made.
func (s *server) templateVoiceChat(h *opHandle, sc *api.ServerConfig, planned []templates.PlannedAddon) error {
	if sc.VoiceChatPort > 0 || !slices.ContainsFunc(planned, func(pa templates.PlannedAddon) bool { return voiceChat(pa.Key()) }) {
		return nil
	}
	installed, err := s.installedAddons()
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(installed, func(i addons.Installed) bool { return voiceChat(i.Key()) }) {
		return nil
	}
	_, srv, _, err := s.addonContext()
	if err != nil {
		return err
	}
	h.phase("opening_port")
	return s.setUpVoiceChat(h, sc, srv, h.op.Actor)
}

// voiceChatNotice tells whoever imports a template with voice chat that the
// new server opens a UDP port for it, and which one it would be now.
func (a *Agent) voiceChatNotice(planned []templates.PlannedAddon) *addons.Notice {
	if !slices.ContainsFunc(planned, func(pa templates.PlannedAddon) bool { return voiceChat(pa.Key()) }) {
		return nil
	}
	port, err := a.freeVoicePort("", curated.VoiceChatPort)
	if err != nil {
		return nil
	}
	p := strconv.Itoa(port)
	return &addons.Notice{Kind: kindTemplateVoiceChat, Params: map[string]string{"port": p},
		Msg:  "Voice chat travels on its own port: Playkeeper opens UDP " + p + " on this machine for it.",
		Hint: "Open UDP " + p + " in your provider's firewall too. Friends add the Simple Voice Chat mod to talk."}
}

// manifestVoiceChatPort is the backup manifest setting that records voice
// chat's UDP port, when the server had it open.
const manifestVoiceChatPort = "voiceChatPort"

// restoredVoiceChat gives a restored server voice chat's UDP port when its
// backup was made with the port open: the port the server has, or else the
// backup's or the next free one, held for the server until release, which
// the restore calls once the server's settings record how it ended. The
// restored add-on settings in dataDir get that port, so voice chat listens
// where the container publishes it. A backup made without the port leaves
// the server without one.
func (s *server) restoredVoiceChat(sc, prev *api.ServerConfig, m backup.Manifest, dataDir string) (release func(), err error) {
	release = func() {}
	sc.VoiceChatPort = 0
	from, _ := strconv.Atoi(m.Settings[manifestVoiceChatPort])
	typ := cmp.Or(sc.Type, api.TypePaper)
	if _, err := addons.TargetFor(typ); from <= 0 || err != nil {
		return release, nil
	}
	port := 0
	if prev != nil {
		port = prev.VoiceChatPort
	}
	if port == 0 {
		if port, release, err = s.holdVoicePort(s.id, from); err != nil {
			port, release, err = s.holdVoicePort(s.id, curated.VoiceChatPort)
		}
		if err != nil {
			return release, voiceChatError(err)
		}
	}
	if _, err := curated.SetUpVoiceChat(addons.Server{Dir: dataDir, Type: typ, Owner: s.gameOwner()}, port); err != nil {
		release()
		return func() {}, voiceChatError(err)
	}
	sc.VoiceChatPort = port
	return release, nil
}

func voiceChatError(err error) error {
	if n := noticeOf(err); n != nil {
		return &apiError{Msg: n.Message, Hint: n.Hint}
	}
	return err
}

// closeVoiceChat stops publishing voice chat's port as the add-on is
// removed, and returns the port it closed: the container is made without it
// at the next start. The port stays held for the server until release, which
// the removal calls once it has given the port back or removed voice chat.
func (s *server) closeVoiceChat(actor string) (port int, release func(), err error) {
	sc, err := s.serverConfig()
	if err != nil || sc == nil || sc.VoiceChatPort == 0 {
		return 0, func() {}, err
	}
	port = sc.VoiceChatPort
	s.voicePorts.mu.Lock()
	release = s.voicePorts.hold(port, s.id)
	s.voicePorts.mu.Unlock()
	sc.VoiceChatPort = 0
	if err := s.saveServerConfig(*sc); err != nil {
		release()
		return 0, func() {}, err
	}
	s.audit(actor, "addon.port_closed", string(addons.Modrinth)+":"+curatedVoiceChatProject(), "succeeded", fmt.Sprintf("voice chat's UDP %d", port))
	return port, release, nil
}

// reopenVoiceChat gives voice chat back the port closeVoiceChat closed, when
// its removal didn't go ahead after all.
func (s *server) reopenVoiceChat(port int, actor string) {
	if port == 0 {
		return
	}
	sc, err := s.serverConfig()
	if err == nil && sc != nil {
		sc.VoiceChatPort = port
		err = s.saveServerConfig(*sc)
	}
	if err != nil {
		s.log.Warn("voice chat stayed but its port could not be opened again", "server", s.id, "port", port, "err", err)
		return
	}
	s.audit(actor, "addon.port_opened", string(addons.Modrinth)+":"+curatedVoiceChatProject(), "succeeded", fmt.Sprintf("voice chat on UDP %d again", port))
}

func curatedVoiceChatProject() string {
	e, _ := curated.Get(curated.VoiceChatID)
	for _, p := range e.Projects {
		if slices.Contains(p.Types, "paper") {
			return p.ID
		}
	}
	return ""
}

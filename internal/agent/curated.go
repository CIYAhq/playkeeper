package agent

import (
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
	"github.com/CIYAhq/playkeeper/internal/curated"
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
	return curated.PickPort(curated.VoiceChatPort, s.portTaken())
}

// portTaken reports the ports voice chat on this server can't have: any
// server's game port, the voice chat port of another, the dashboard's, and
// any UDP port something on the machine already uses.
func (s *server) portTaken() func(int) bool {
	used := map[int]bool{s.cfg.PanelPort: true}
	for _, o := range s.serverList() {
		used[o.gamePort] = true
		if o.id == s.id {
			continue
		}
		if sc, err := o.serverConfig(); err == nil && sc != nil && sc.VoiceChatPort > 0 {
			used[sc.VoiceChatPort] = true
		}
	}
	return func(p int) bool { return used[p] || s.opts.UDPPortInUse(p) }
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

// openVoiceChat gives voice chat its UDP port: written into its settings
// and published from the container, which a running server is restarted
// for. The provider's firewall is left to the owner.
func (s *server) openVoiceChat(ctx context.Context, h *opHandle, actor string) error {
	sc, srv, _, err := s.addonContext()
	if err != nil {
		return err
	}
	h.phase("opening_port")
	port, err := s.voiceChatPort()
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
	if _, running, _ := s.containerRunning(ctx); !running {
		return nil
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

// closeVoiceChat stops publishing voice chat's port once the add-on is
// removed: the container is made without it at the next start.
func (s *server) closeVoiceChat(actor string) error {
	sc, err := s.serverConfig()
	if err != nil || sc == nil || sc.VoiceChatPort == 0 {
		return err
	}
	port := sc.VoiceChatPort
	sc.VoiceChatPort = 0
	if err := s.saveServerConfig(*sc); err != nil {
		return err
	}
	s.audit(actor, "addon.port_closed", string(addons.Modrinth)+":"+curatedVoiceChatProject(), "succeeded", fmt.Sprintf("voice chat's UDP %d", port))
	return nil
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

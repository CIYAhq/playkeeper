package panel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/modpacks/share"
)

// friendsPackLimits let a few friends behind one address open a pack page
// together: a visit is the page, its data, the emblem and the file, and
// Prism Launcher can fetch the file again by its link.
var friendsPackLimits = publicLimits{perMinute: 60, open: 4, read: 10 * time.Second, write: time.Minute, stall: 30 * time.Second}

// maxFriendsPackFile bounds the friends' .mrpack passed on from the agent:
// the share's index limit, plus the zip around it.
const maxFriendsPackFile = 9 << 20

// errPackGone is every link that doesn't open a shared pack, whatever the
// reason.
var errPackGone = errors.New("pack not available")

// friendsPack is a shared pack as the panel serves it, and the machine that
// runs its server.
type friendsPack struct {
	m     machine
	link  api.PackLink
	share share.Share
}

// friendsPacks serves the public friends' pack pages under share.PathPrefix
// (see share.PathPrefix for the paths). The page itself is the same for
// every link, and asks for the rest. A token no server has, a page that is
// off, a stopped server, a machine that can't answer and a wrong file name
// all get the same 404 for each kind of request, which never names the
// server.
func (s *Server) friendsPacks() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		token, part, sub := strings.Cut(strings.TrimPrefix(r.URL.Path, share.PathPrefix), "/")
		get := r.Method == http.MethodGet || r.Method == http.MethodHead
		if !get || strings.Contains(part, "/") || (sub && part == "") {
			packGone(w)
			return
		}
		if !sub {
			s.packPage(w, r)
			return
		}
		fp, err := s.friendsPack(r.Context(), token)
		switch {
		case err != nil:
			packGone(w)
		case part == "page" && r.Method == http.MethodGet:
			s.packPageData(w, r, fp, token)
		case part == "icon" && r.Method == http.MethodGet:
			s.packIcon(w, r, fp)
		case part == share.FileName(fp.link.Slug) && share.ValidSlug(fp.link.Slug):
			fp.share.ServeFile(w, r, fp.link.Slug)
		default:
			packGone(w)
		}
	})
}

// friendsPack finds the shared pack token opens. A link the dashboard
// recorded (see recordLink) is asked only of the machine it was made on,
// while that machine runs the link's server, and opens only that server's
// pack. A link it has no record of is asked of every machine, and opens
// only when every machine answers and exactly one has it. It returns
// errPackGone when no machine may open the link, and another error when
// one can't answer.
func (s *Server) friendsPack(ctx context.Context, token string) (*friendsPack, error) {
	if !share.ValidToken(token) {
		return nil, errPackGone
	}
	m, serverID, err := s.linkMachine(packLink, token)
	switch {
	case errors.Is(err, errNoLinkRecord):
		return s.unrecordedPack(ctx, token)
	case errors.Is(err, errNotFound), errors.Is(err, errDisputed):
		return nil, errPackGone
	case err != nil:
		s.log.Warn("a friends' pack page could not be answered", "err", err)
		return nil, err
	}
	fp, err := askPack(ctx, m, token)
	switch {
	case errors.Is(err, errPackGone):
		return nil, err
	case err != nil:
		s.log.Warn("a friends' pack page could not be answered", "machine", m.ID, "err", err)
		return nil, err
	case fp.link.Server != serverID:
		s.log.Warn("a machine answered a friends' pack link for another server", "machine", m.ID, "server", serverID)
		return nil, errPackGone
	}
	return fp, nil
}

// unrecordedPack asks every machine at once about a link the dashboard has
// no record of. Two machines that both open it get the same 404 as a link
// nobody has, and so does one while another can't answer: it might have
// the link too.
func (s *Server) unrecordedPack(ctx context.Context, token string) (*friendsPack, error) {
	list, err := s.machines()
	if err != nil {
		return nil, err
	}
	type answer struct {
		fp  *friendsPack
		err error
	}
	got := make([]answer, len(list))
	var wg sync.WaitGroup
	for i, m := range list {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, machineTimeout)
			defer cancel()
			got[i].fp, got[i].err = askPack(ctx, m, token)
		})
	}
	wg.Wait()
	var found []*friendsPack
	var failed error
	for _, a := range got {
		switch {
		case a.err == nil:
			found = append(found, a.fp)
		case !errors.Is(a.err, errPackGone):
			failed = a.err
		}
	}
	switch {
	case len(found) > 1:
		ids := make([]string, len(found))
		for i, fp := range found {
			ids[i] = fp.m.ID
		}
		s.log.Warn("more than one machine opens a friends' pack link, so it opens on none", "machines", strings.Join(ids, " "))
		return nil, errPackGone
	case failed != nil:
		s.log.Warn("a friends' pack page could not be answered", "err", failed)
		return nil, failed
	case len(found) == 1:
		return found[0], nil
	}
	return nil, errPackGone
}

// askPack asks m which shared pack token opens.
func askPack(ctx context.Context, m machine, token string) (*friendsPack, error) {
	var link api.PackLink
	if _, err := m.agent.Do(ctx, "GET", "/v1/packs/"+token, nil, nil, &link); err != nil {
		var ae *agentclient.Error
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			return nil, errPackGone
		}
		return nil, err
	}
	fp := &friendsPack{m: m, link: link}
	if err := json.Unmarshal(link.Share, &fp.share); err != nil {
		return nil, err
	}
	return fp, nil
}

// recordPackLink keeps the friends' page link a machine made for a server
// (see recordLink).
func (s *Server) recordPackLink(m machine, serverID string, raw json.RawMessage) {
	var ps api.PackShare
	if json.Unmarshal(raw, &ps) != nil || !ps.Public || !share.ValidToken(ps.Token) {
		return
	}
	s.recordLink(packLink, ps.Token, serverID, m)
}

// packPage is the dashboard's page, which shows the pack or "This pack
// isn't available" from the data it asks for next. It answers 200 for every
// link, as the public group answers a route's 404 with its own.
func (s *Server) packPage(w http.ResponseWriter, r *http.Request) {
	b := []byte(uiMissing)
	if s.static != nil {
		if index, err := fs.ReadFile(s.static, "index.html"); err == nil {
			b = index
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(b)
	}
}

// friendsPackPage is the public page's data: only what friends get.
type friendsPackPage struct {
	*share.Page
	HasIcon bool `json:"hasIcon"`
}

func (s *Server) packPageData(w http.ResponseWriter, r *http.Request, fp *friendsPack, token string) {
	address := fp.link.JoinAddress
	switch {
	case fp.m.Kind == remoteKind:
		// Names stay with the dashboard's machine, whatever a joined one
		// reports: its servers join at its IP and port.
		address = s.joinedAddress(r.Context(), fp.m, fp.link.GamePort)
	case address == "":
		address = joinAddressAt(r.Host, fp.link.GamePort)
	}
	p, err := fp.share.Page(token, fp.link.Slug, address)
	if err != nil {
		packGone(w)
		return
	}
	writeJSON(w, http.StatusOK, friendsPackPage{Page: p, HasIcon: fp.link.HasIcon})
}

func (s *Server) packIcon(w http.ResponseWriter, r *http.Request, fp *friendsPack) {
	if !fp.link.HasIcon {
		packGone(w)
		return
	}
	resp, err := fp.m.agent.Raw(r.Context(), "GET", "/v1/servers/"+fp.link.Server+"/icon", nil, nil, nil, false)
	if err != nil {
		packGone(w)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		packGone(w)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}

// joinedAddress is where friends join a joined machine's server: the IP the
// machine last called in from, with the server's port, or "" before it has.
func (s *Server) joinedAddress(ctx context.Context, m machine, gamePort int) string {
	if s.hub == nil {
		return ""
	}
	st, err := s.hub.MachineStatus(ctx, m.ID)
	if err != nil || st.Address == "" {
		return ""
	}
	return joinAddressAt(st.Address, gamePort)
}

// joinAddressAt is the address friends join at, as "Copy join address" gives
// it: the host the page was opened at, with the game's port unless it is
// Minecraft's usual one. share.Share.Page drops anything that isn't a host
// name or IP address.
func joinAddressAt(hostport string, gamePort int) string {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	if gamePort == 25565 || gamePort <= 0 {
		return host
	}
	return host + ":" + strconv.Itoa(gamePort)
}

func packGone(w http.ResponseWriter) {
	http.Error(w, "This pack isn't available.", http.StatusNotFound)
}

// hPackShareFile passes the friends' .mrpack on to a signed-in user. It
// doesn't depend on the public page's switch.
func (s *Server) hPackShareFile(w http.ResponseWriter, r *http.Request, _ *session) {
	m, ok := s.target(w, r)
	if !ok {
		return
	}
	resp, err := m.agent.Raw(r.Context(), "GET", agentPath("/v1/servers/{id}/mods/share.mrpack", r), nil, nil, nil, false)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, io.LimitReader(resp.Body, 1<<20))
		return
	}
	h := w.Header()
	h.Set("Content-Type", share.ContentType)
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		h.Set("Content-Disposition", cd)
	}
	h.Set("Cache-Control", "no-store")
	io.Copy(w, io.LimitReader(resp.Body, maxFriendsPackFile))
}

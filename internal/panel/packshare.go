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
// (see share.PathPrefix for the paths). A token no server has, a page that
// is off, a stopped server and a wrong file name all get the same answer
// for each kind of request, which never names the server.
func (s *Server) friendsPacks() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		token, part, sub := strings.Cut(strings.TrimPrefix(r.URL.Path, share.PathPrefix), "/")
		get := r.Method == http.MethodGet || r.Method == http.MethodHead
		if !get || strings.Contains(part, "/") || (sub && part == "") {
			packGone(w)
			return
		}
		fp, err := s.friendsPack(r.Context(), token)
		switch {
		case !sub:
			status := http.StatusOK
			if errors.Is(err, errPackGone) {
				status = http.StatusNotFound
			} else if err != nil {
				status = http.StatusServiceUnavailable
			}
			s.packPage(w, r, status)
		case err != nil && !errors.Is(err, errPackGone):
			packBusy(w)
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

// friendsPack asks the machines which shared pack token opens. It returns
// errPackGone when none does, and another error when a machine can't answer.
func (s *Server) friendsPack(ctx context.Context, token string) (*friendsPack, error) {
	if !share.ValidToken(token) {
		return nil, errPackGone
	}
	list, err := s.machines()
	if err != nil {
		return nil, err
	}
	failed := error(nil)
	for _, m := range list {
		var link api.PackLink
		if _, err := m.agent.Do(ctx, "GET", "/v1/packs/"+token, nil, nil, &link); err != nil {
			var ae *agentclient.Error
			if !errors.As(err, &ae) || ae.Status != http.StatusNotFound {
				failed = err
			}
			continue
		}
		fp := &friendsPack{m: m, link: link}
		if err := json.Unmarshal(link.Share, &fp.share); err != nil {
			return nil, err
		}
		return fp, nil
	}
	if failed != nil {
		s.log.Warn("a friends' pack page could not be answered", "err", failed)
		return nil, failed
	}
	return nil, errPackGone
}

// packPage is the dashboard's page, which shows the pack or "This pack
// isn't available" from the data it asks for next.
func (s *Server) packPage(w http.ResponseWriter, r *http.Request, status int) {
	b := []byte(uiMissing)
	if s.static != nil {
		if index, err := fs.ReadFile(s.static, "index.html"); err == nil {
			b = index
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
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
	p, err := fp.share.Page(token, fp.link.Slug, joinAddressAt(r.Host, fp.link.GamePort))
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
		packBusy(w)
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

func packBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "60")
	http.Error(w, "Playkeeper can't make this pack right now. Try again in a few minutes.", http.StatusServiceUnavailable)
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

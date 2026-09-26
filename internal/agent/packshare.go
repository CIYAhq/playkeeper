package agent

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/minecraft/software"
	"github.com/CIYAhq/playkeeper/internal/modpacks/share"
)

// The friends' share of a modded server: the .mrpack friends import, and
// the public page at /packs/<token> with its switch.

const (
	// shareRetry is how long a failed build answers with its error, so that
	// visits to a public page never turn into Modrinth traffic.
	shareRetry = time.Minute
	// shareBuildTimeout bounds one build, which asks Modrinth about the
	// server's files.
	shareBuildTimeout = 2 * time.Minute
)

// friendsShares keeps each server's built share until its setup changes.
type friendsShares struct {
	mu      sync.Mutex
	entries map[string]*friendsShare
}

type friendsShare struct {
	key   string
	done  chan struct{} // closed when the build has ended
	share *share.Share
	err   error
	at    time.Time
}

// friendsShare is the server's share, built again only when its setup
// changed since the last build. One build runs per server at a time, and a
// failed one answers with its error for shareRetry.
func (s *server) friendsShare(ctx context.Context) (*share.Share, error) {
	setup, err := s.shareSetup()
	if err != nil {
		return nil, err
	}
	key, fs := setup.Key(), &s.shares
	for {
		fs.mu.Lock()
		if fs.entries == nil {
			fs.entries = map[string]*friendsShare{}
		}
		if e := fs.entries[s.id]; e != nil && e.key == key {
			select {
			case <-e.done:
				if e.err == nil || s.now().Sub(e.at) < shareRetry {
					fs.mu.Unlock()
					return e.share, e.err
				}
			default:
				fs.mu.Unlock()
				select {
				case <-e.done:
					continue
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
		}
		e := &friendsShare{key: key, done: make(chan struct{})}
		fs.entries[s.id] = e
		fs.mu.Unlock()

		sh, err := s.buildShare(setup)
		fs.mu.Lock()
		e.share, e.err, e.at = sh, err, s.now()
		close(e.done)
		fs.mu.Unlock()
		return sh, err
	}
}

// buildShare builds a share for as long as the server exists, whoever
// asked for it, as every later visitor uses the result.
func (s *server) buildShare(setup share.Setup) (*share.Share, error) {
	lib := s.lib()
	if lib == nil || lib.Modrinth == nil {
		return nil, errConflict("Playkeeper can't reach Modrinth on this machine, so it can't make the pack.", "")
	}
	ctx, cancel := context.WithTimeout(s.ctx, shareBuildTimeout)
	defer cancel()
	return (&share.Builder{Modrinth: lib.Modrinth}).Build(ctx, setup)
}

// shareSetup is what the server's share is made from: its versions, the
// modpack's record and the add-on library's rows. Nothing is read from the
// server's folder.
func (s *server) shareSetup() (share.Setup, error) {
	sc, err := s.serverConfig()
	if err != nil {
		return share.Setup{}, err
	}
	if sc == nil {
		return share.Setup{}, errNotCreated()
	}
	typ := s.serverType(sc)
	st := share.Setup{Name: s.name(), Type: typ, MinecraftVersion: sc.MinecraftVersion, LoaderVersion: loaderVersion(sc)}
	if !share.Supported(typ) {
		return st, nil
	}
	if st.Addons, err = s.installedAddons(); err != nil {
		return share.Setup{}, err
	}
	if m := sc.Modpack; m != nil && !m.Pending {
		if st.Pack, err = s.packRecord(); err != nil {
			return share.Setup{}, err
		}
	}
	return st, nil
}

// loaderVersion is the version of the loader friends need: Fabric Loader,
// Quilt Loader or NeoForge.
func loaderVersion(sc *api.ServerConfig) string {
	if p := sc.Software; p != nil {
		switch p.Type {
		case software.Fabric:
			return p.FabricLoader
		case software.Quilt:
			return p.QuiltLoader
		case software.NeoForge:
			return p.NeoForgeVersion
		}
	}
	return ""
}

// packsPublic is the friends' page switch and its link token.
func (s *server) packsPublic() (bool, string) {
	var on bool
	var token string
	if err := s.db.QueryRow(`SELECT packs_public, packs_token FROM servers WHERE id = ?`, s.id).Scan(&on, &token); err != nil {
		return false, ""
	}
	return on && token != "", token
}

// shareView is a share without its file's index, which only the file needs.
type shareView struct {
	*share.Share
	Index *struct{} `json:"index,omitempty"`
}

func (s *server) packShare(ctx context.Context) (*api.PackShare, error) {
	row, err := s.row()
	if err != nil {
		return nil, err
	}
	sh, err := s.friendsShare(ctx)
	if err != nil {
		return nil, addonError(err)
	}
	file, err := sh.File()
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(shareView{Share: sh})
	if err != nil {
		return nil, err
	}
	out := &api.PackShare{File: share.FileName(row.Slug), Size: int64(len(file)), LoaderName: sh.Type, Share: raw}
	if t, err := addons.TargetFor(sh.Type); err == nil {
		out.LoaderName = t.Name()
	}
	out.Public, out.Token = s.packsPublic()
	if !out.Public {
		out.Token = ""
	}
	return out, nil
}

// hPackShare is the server's friends' share for the Mods tab and the share
// sheet: what friends need, and the public page's link while it is shared.
func (s *server) hPackShare(w http.ResponseWriter, r *http.Request) {
	out, err := s.packShare(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// hPackShareSet turns the friends' page on or off. Turning it on makes a new
// link token unless it is on already; turning it off forgets the token, so an
// old link never opens the page again.
func (s *server) hPackShareSet(w http.ResponseWriter, r *http.Request) {
	var req api.PackShareRequest
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
	if req.Public {
		if !share.Supported(s.serverType(sc)) {
			writeError(w, &apiError{Status: http.StatusConflict, Code: string(share.KindUnsupported), Msg: s.name() + " doesn't run mods, so there is no pack to share."})
			return
		}
		token, err := share.NewToken(rand.Reader)
		if err != nil {
			writeError(w, err)
			return
		}
		res, err := s.db.Exec(`UPDATE servers SET packs_public = 1, packs_token = ? WHERE id = ? AND (packs_public = 0 OR packs_token = '')`, token, s.id)
		if err != nil {
			writeError(w, err)
			return
		}
		if n, _ := res.RowsAffected(); n > 0 {
			s.audit(actor, "packs.shared", "friends-pack", "succeeded", "")
		}
	} else {
		res, err := s.db.Exec(`UPDATE servers SET packs_public = 0, packs_token = '' WHERE id = ? AND (packs_public = 1 OR packs_token != '')`, s.id)
		if err != nil {
			writeError(w, err)
			return
		}
		if n, _ := res.RowsAffected(); n > 0 {
			s.audit(actor, "packs.unshared", "friends-pack", "succeeded", "")
		}
	}
	out, err := s.packShare(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// hPackShareFile is the friends' file for signed-in users. It doesn't depend
// on the switch, so "Download the file" works without sharing.
func (s *server) hPackShareFile(w http.ResponseWriter, r *http.Request) {
	row, err := s.row()
	if err != nil {
		writeError(w, err)
		return
	}
	sh, err := s.friendsShare(r.Context())
	if err != nil {
		writeError(w, addonError(err))
		return
	}
	sh.ServeFile(w, r, row.Slug)
}

// errPackUnavailable is the one answer for every link that doesn't open a
// shared pack, whatever the reason, so it never tells a server exists.
func errPackUnavailable() *apiError { return errNotFound("Pack") }

// hPackLink tells the panel which server and share a public page's token
// opens. A token no server has, a page that is off, and a server that is
// stopped or doesn't run mods all get errPackUnavailable.
func (a *Agent) hPackLink(w http.ResponseWriter, r *http.Request) {
	s := a.serverForPackToken(r.PathValue("token"))
	if s == nil {
		writeError(w, errPackUnavailable())
		return
	}
	row, err := s.row()
	if err != nil {
		writeError(w, errPackUnavailable())
		return
	}
	sh, err := s.friendsShare(r.Context())
	if err != nil {
		var ae *addons.Error
		if errors.As(err, &ae) && ae.Kind == share.KindUnsupported {
			writeError(w, errPackUnavailable())
			return
		}
		a.log.Warn("friends' pack could not be made", "server", s.id, "err", err)
		writeError(w, &apiError{Status: http.StatusServiceUnavailable, Code: api.CodeUpstream, Msg: "Playkeeper can't make this pack right now.", Hint: "Try again in a few minutes."})
		return
	}
	raw, err := json.Marshal(sh)
	if err != nil {
		writeError(w, err)
		return
	}
	sc, _ := s.serverConfig()
	writeJSON(w, http.StatusOK, api.PackLink{Server: s.id, Slug: row.Slug, GamePort: s.gamePort, JoinAddress: s.joinAddress(), HasIcon: sc != nil && sc.IconUpdatedAt != nil, Share: raw})
}

// serverForPackToken is the server whose friends' page token is token while
// the page is on, the server runs mods and it isn't stopped, or nil. Tokens
// are compared in constant time.
func (a *Agent) serverForPackToken(token string) *server {
	if !share.ValidToken(token) {
		return nil
	}
	rows, err := a.db.Query(`SELECT id, packs_token FROM servers WHERE packs_public = 1 AND packs_token != ''`)
	if err != nil {
		return nil
	}
	var found string
	for rows.Next() {
		var id, t string
		if rows.Scan(&id, &t) == nil && subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1 {
			found = id
		}
	}
	rows.Close()
	s := a.serverByID(found)
	if s == nil {
		return nil
	}
	sc, err := s.serverConfig()
	if err != nil || sc == nil || !share.Supported(s.serverType(sc)) || s.desired() != api.DesiredRunning {
		return nil
	}
	return s
}

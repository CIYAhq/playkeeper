package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/modpacks"
	"github.com/CIYAhq/playkeeper/internal/modpacks/curseforge"
)

// Settings › Add-on sources: the owner's own CurseForge API key, kept in
// curseForgeKeyFile and never sent back, only its last four characters.

// keyCheckTimeout bounds asking CurseForge whether it accepts a key.
const keyCheckTimeout = 20 * time.Second

func (a *Agent) addonSources() api.AddonSources {
	a.packKeyMu.Lock()
	key, problem := a.packKey, a.packKeyProblem
	a.packKeyMu.Unlock()
	cf := api.CurseForgeSource{Key: string(key.Source()), Problem: problem}
	if key.Source() == curseforge.KeyFile {
		cf.Ending = key.Ending()
	}
	return api.AddonSources{CurseForge: cf}
}

func (a *Agent) hAddonSources(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.addonSources())
}

// hCurseForgeKeySet saves the owner's key once CurseForge accepts it, and
// from then on uses it for modpacks.
func (a *Agent) hCurseForgeKeySet(w http.ResponseWriter, r *http.Request) {
	var req api.CurseForgeKeyRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := curseforge.NewKey(req.Key, curseforge.KeyFile)
	if err != nil {
		writeError(w, keyRefused())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), keyCheckTimeout)
	defer cancel()
	if err := modpacks.CheckKey(ctx, a.opts.UpstreamClient, key); err != nil {
		var ae *addons.Error
		switch {
		case errors.As(err, &ae) && ae.Kind == modpacks.KindKeyRefused:
			a.audit(actor, "curseforge.key_saved", "curseforge", "refused", "CurseForge did not accept the key")
			writeError(w, keyRefused())
		case errors.As(err, &ae):
			writeError(w, &apiError{Status: http.StatusBadGateway, Code: api.CodeUpstream, Msg: ae.Msg, Hint: ae.Hint})
		default:
			writeError(w, &apiError{Status: http.StatusBadGateway, Code: api.CodeUpstream, Msg: "Playkeeper could not ask CurseForge about the key.", Hint: "Try again in a minute."})
		}
		return
	}
	a.keyFileMu.Lock()
	err = writePrivateFile(a.curseForgeKeyFile(), []byte(strings.TrimSpace(req.Key)+"\n"))
	if err == nil {
		a.loadPacks()
	}
	a.keyFileMu.Unlock()
	if err != nil {
		writeError(w, &apiError{Status: http.StatusInternalServerError, Code: api.CodeInternal, Msg: "Playkeeper could not save the key: " + err.Error(), Err: err})
		return
	}
	a.audit(actor, "curseforge.key_saved", "curseforge", "succeeded", "key ending "+key.Ending())
	writeJSON(w, http.StatusOK, a.addonSources())
}

// hCurseForgeKeyRemove forgets the owner's key; the build's own key, if it
// has one, is used again.
func (a *Agent) hCurseForgeKeyRemove(w http.ResponseWriter, r *http.Request) {
	actor, err := validActor(r.URL.Query().Get("actor"))
	if err != nil {
		writeError(w, err)
		return
	}
	a.keyFileMu.Lock()
	err = os.Remove(a.curseForgeKeyFile())
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	if err == nil {
		a.loadPacks()
	}
	a.keyFileMu.Unlock()
	if err != nil {
		writeError(w, &apiError{Status: http.StatusInternalServerError, Code: api.CodeInternal, Msg: "Playkeeper could not remove the key: " + err.Error(), Err: err})
		return
	}
	a.audit(actor, "curseforge.key_removed", "curseforge", "succeeded", "")
	writeJSON(w, http.StatusOK, a.addonSources())
}

func keyRefused() *apiError {
	return &apiError{Status: http.StatusBadRequest, Code: api.CodeKeyRefused, Msg: "That key didn't work. Copy it again from console.curseforge.com."}
}

// writePrivateFile replaces path with b, readable by its owner only,
// through a new file beside it so a reader never sees half of it.
func writePrivateFile(path string, b []byte) error {
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+"."+hex.EncodeToString(suffix))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

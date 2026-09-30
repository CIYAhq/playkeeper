package agent

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// The owner's own AI keys, which the AI Build Battle plugin builds with
// (0.4.9). Each is a file in the server's secrets folder, beside data/ and
// never in it, which the container mounts read-only at secretsMount: no
// backup, copy, export or the Files tab reaches it. No answer, log, audit
// entry or error carries a key or any part of it.

const (
	secretsFolder = "secrets"
	secretsMount  = "/run/playkeeper/secrets"
	// The AI Build Battle plugin ships inside Playkeeper; a server has it
	// while it has this add-on record.
	aiBuildBattleSource  = "playkeeper"
	aiBuildBattleProject = "ai-build-battle"
	minAIKey, maxAIKey   = 20, 256
)

// aiProvider is an AI service whose key an owner can give their server.
type aiProvider struct {
	name   string // as its customers know it
	file   string // in the secrets folder, where the plugin reads it
	prefix string // every key of theirs starts with it
	site   string // where they make one
}

// aiProviders are the services the plugin can build with, by the name the
// routes use.
var aiProviders = map[string]aiProvider{
	"openrouter": {name: "OpenRouter", file: "openrouter_api_key", prefix: "sk-or-", site: "openrouter.ai/keys"},
}

func (s *server) secretsDir() string { return filepath.Join(s.dir(), secretsFolder) }

// hasSecretsDir reports whether the server has its secrets folder, and so
// whether its container mounts it: a link there doesn't count.
func (s *server) hasSecretsDir() bool {
	fi, err := os.Lstat(s.secretsDir())
	return err == nil && fi.IsDir()
}

// hasAIBuildBattle reports whether the server has the AI Build Battle plugin.
func (s *server) hasAIBuildBattle() (bool, error) {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM addons WHERE server_id = ? AND source = ? AND project_id = ?`, s.id, aiBuildBattleSource, aiBuildBattleProject).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// prepareSecrets makes the secrets folder before a start of a server with the
// AI Build Battle plugin, so its container mounts it and a key saved later
// needs no restart, and gives a folder the server has back to the game user.
func (s *server) prepareSecrets() error {
	has, err := s.hasAIBuildBattle()
	if err != nil {
		return err
	}
	if !has && !s.hasSecretsDir() {
		return nil
	}
	return s.ensureSecretsDir()
}

// ensureSecretsDir makes the server's secrets folder, or checks the one it
// has: a folder, not a link, the game user's and only theirs to read.
func (s *server) ensureSecretsDir() error {
	if err := os.MkdirAll(s.dir(), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(s.secretsDir(), 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return s.setSecretsMode(0o500)
}

// setSecretsMode gives the secrets folder to the game user with mode, through
// a handle opened without following a link or waiting on a pipe.
func (s *server) setSecretsMode(mode fs.FileMode) error {
	f, err := os.OpenFile(s.secretsDir(), os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR) {
		return errConflict("Something that isn't a folder is where this server keeps its AI keys ("+s.secretsDir()+").",
			"Move it away (sudo mv "+s.secretsDir()+" "+s.secretsDir()+".old), then try again.")
	}
	if err != nil {
		return err
	}
	defer f.Close()
	if os.Geteuid() == 0 {
		if err := f.Chown(s.cfg.GameUID, s.cfg.GameGID); err != nil {
			return err
		}
	}
	return f.Chmod(mode)
}

// changeSecrets runs fn in the secrets folder, which it makes if need be.
// Root writes in the folder as it is; anyone else, as in development, only
// while it's open to its owner.
func (s *server) changeSecrets(fn func(root *os.Root) error) error {
	if err := s.ensureSecretsDir(); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		if err := s.setSecretsMode(0o700); err != nil {
			return err
		}
		defer s.setSecretsMode(0o500)
	}
	root, err := os.OpenRoot(s.secretsDir())
	if err != nil {
		return err
	}
	defer root.Close()
	return fn(root)
}

// saveAIKey puts key in p's file: the game user's and only theirs to read,
// with no newline after it, written beside it and renamed over it so the
// plugin never reads half of one.
func (s *server) saveAIKey(p aiProvider, key string) error {
	return s.changeSecrets(func(root *os.Root) error {
		suffix := make([]byte, 6)
		if _, err := rand.Read(suffix); err != nil {
			return err
		}
		tmp := "." + p.file + "." + hex.EncodeToString(suffix)
		f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
		if err != nil {
			return err
		}
		defer root.Remove(tmp)
		_, err = io.WriteString(f, key)
		if err == nil && os.Geteuid() == 0 {
			err = f.Chown(s.cfg.GameUID, s.cfg.GameGID)
		}
		if err == nil {
			err = f.Chmod(0o400)
		}
		if err == nil {
			err = f.Sync()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		return root.Rename(tmp, p.file)
	})
}

// removeAIKey deletes p's key. The folder stays, so the container keeps its
// mount and a new key needs no restart.
func (s *server) removeAIKey(p aiProvider) error {
	if _, err := os.Lstat(s.secretsDir()); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return s.changeSecrets(func(root *os.Root) error {
		if err := root.Remove(p.file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	})
}

// removeSecretsDir deletes a secrets folder with its keys, first opening it
// to its owner, which only root could delete them from otherwise.
func removeSecretsDir(dir string) error {
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err == nil {
		err = f.Chmod(0o700)
		f.Close()
	}
	if err != nil && !errors.Is(err, syscall.ELOOP) && !errors.Is(err, syscall.ENOTDIR) {
		return err
	}
	return os.RemoveAll(dir)
}

func (s *server) aiKeySet(p aiProvider) bool {
	fi, err := os.Lstat(filepath.Join(s.secretsDir(), p.file))
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}

// secretsPending reports whether the server's running container was made
// before the server had its secrets folder: it sees no key until the
// server's next start makes it again, with the folder mounted.
func (s *server) secretsPending(ctx context.Context) bool {
	c, err := s.docker.ContainerInspect(ctx, s.containerName())
	if err != nil || !c.State.Running {
		return false
	}
	for _, b := range c.HostConfig.Binds {
		if parts := strings.Split(b, ":"); len(parts) > 1 && parts[1] == secretsMount {
			return false
		}
	}
	return true
}

func (s *server) aiKeys(ctx context.Context) (api.AIKeys, error) {
	has, err := s.hasAIBuildBattle()
	if err != nil {
		return api.AIKeys{}, err
	}
	out := api.AIKeys{Keys: map[string]api.AIKey{}, Available: has}
	set := false
	for id, p := range aiProviders {
		k := api.AIKey{Set: s.aiKeySet(p)}
		out.Keys[id] = k
		set = set || k.Set
	}
	out.Pending = set && s.secretsPending(ctx)
	return out, nil
}

// validAIKey is key, without the spaces around it, if it can be one of p's,
// or why it can't. None of the reasons quotes it.
func validAIKey(p aiProvider, key string) (string, error) {
	key = strings.TrimSpace(key)
	msg := ""
	switch {
	case key == "":
		msg = "Paste your " + p.name + " key."
	case !strings.HasPrefix(key, p.prefix):
		msg = "That isn't an " + p.name + " key: those start with " + p.prefix + "."
	case strings.ContainsFunc(key, func(r rune) bool { return r < '!' || r > '~' }):
		msg = "Keys have no spaces or characters like that. Copy it again from " + p.site + "."
	case len(key) < minAIKey:
		msg = "That key is too short. Copy all of it from " + p.site + "."
	case len(key) > maxAIKey:
		msg = "That key is too long. Copy only the key from " + p.site + "."
	default:
		return key, nil
	}
	return "", &apiError{Status: http.StatusBadRequest, Code: api.CodeInvalid, Msg: msg, Field: "key"}
}

func (s *server) hAIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.aiKeys(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

// hAIKeySet saves the owner's key for a provider, in place of the one it
// had. The body isn't read with decode, whose errors quote it.
func (s *server) hAIKeySet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("provider")
	p, ok := aiProviders[id]
	if !ok {
		writeError(w, errNotFound("AI provider"))
		return
	}
	var req api.AIKeyRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.More() {
		writeError(w, errInvalid("Invalid request body."))
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := validAIKey(p, req.Key)
	if err != nil {
		writeError(w, err)
		return
	}
	release, ok := s.holdOpLock()
	if !ok {
		writeError(w, s.busyError())
		return
	}
	defer release()
	if s.busy() {
		writeError(w, s.busyError())
		return
	}
	if err := s.saveAIKey(p, key); err != nil {
		writeError(w, s.aiKeyError(id, "save", err))
		return
	}
	s.audit(actor, "ai_key.saved", id, "succeeded", "")
	s.hAIKeys(w, r)
}

// hAIKeyRemove deletes the owner's key for a provider.
func (s *server) hAIKeyRemove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("provider")
	p, ok := aiProviders[id]
	if !ok {
		writeError(w, errNotFound("AI provider"))
		return
	}
	actor, err := validActor(r.URL.Query().Get("actor"))
	if err != nil {
		writeError(w, err)
		return
	}
	release, ok := s.holdOpLock()
	if !ok {
		writeError(w, s.busyError())
		return
	}
	defer release()
	if s.busy() {
		writeError(w, s.busyError())
		return
	}
	had := s.aiKeySet(p)
	if err := s.removeAIKey(p); err != nil {
		writeError(w, s.aiKeyError(id, "remove", err))
		return
	}
	if had {
		s.audit(actor, "ai_key.removed", id, "succeeded", "")
	}
	s.hAIKeys(w, r)
}

// aiKeyError answers for a key that couldn't be saved or removed: with what
// Playkeeper says itself, and otherwise only that it couldn't, in its log.
func (s *server) aiKeyError(provider, verb string, err error) error {
	var ae *apiError
	if errors.As(err, &ae) {
		return err
	}
	s.log.Warn("an AI key could not be changed", "server", s.id, "provider", provider, "change", verb, "err", err)
	return &apiError{Status: http.StatusInternalServerError, Code: api.CodeInternal, Msg: "Playkeeper couldn't " + verb + " the key.",
		Hint: "sudo journalctl -u playkeeper-agent says why.", Err: err}
}

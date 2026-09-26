package agent

import (
	"errors"
	"io/fs"
	"maps"
	"net/http"
	"os"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/gamefiles"
)

// maxPlayerList caps whitelist.json, ops.json and banned-players.json, which
// list at most a few thousand players.
const maxPlayerList = 1 << 20

// gameFiles opens the server's data directory, which the game and its
// plugins and mods can write, for the agent to use through
// internal/gamefiles. What the agent makes there is given to the game's
// user.
func (s *server) gameFiles() (*gamefiles.Dir, error) {
	var owner *gamefiles.Owner
	if os.Geteuid() == 0 {
		owner = &gamefiles.Owner{UID: s.cfg.GameUID, GID: s.cfg.GameGID}
	}
	return gamefiles.Open(s.dataDir(), owner)
}

// gameFileError explains a file in the server's data directory that
// Playkeeper refused, after the sentence saying what it could not do. Other
// errors are returned as they are.
func gameFileError(err error, couldNot string) error {
	var ge *gamefiles.Error
	if !errors.As(err, &ge) {
		return err
	}
	return &apiError{Status: http.StatusConflict, Code: api.CodeConflict, Msg: couldNot + " " + ge.Msg, Hint: ge.Hint, Err: ge}
}

// noteRefusal records the file that stopped a start. A start that failed
// before it checked the server's files (pulling the image, a download) keeps
// the last one, since that file may still be there; a start that got past
// them clears it.
func (s *server) noteRefusal(err error, pastFiles bool) {
	var ge *gamefiles.Error
	refused := errors.As(err, &ge)
	if !refused && !pastFiles {
		return
	}
	var r *api.FileRefusal
	if refused {
		r = &api.FileRefusal{Code: string(ge.Kind), Params: maps.Clone(ge.Params), Message: ge.Msg, Hint: ge.Hint}
	}
	s.mu.Lock()
	s.refusal = r
	s.mu.Unlock()
}

// readPlayerList reads whitelist.json, ops.json or banned-players.json into
// v. A missing file leaves v as it is.
func (s *server) readPlayerList(name string, v any) error {
	d, err := s.gameFiles()
	if err == nil {
		defer d.Close()
		err = d.ReadJSON(name, maxPlayerList, v)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

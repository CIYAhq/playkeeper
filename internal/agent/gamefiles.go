package agent

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/gamefiles"
)

// maxPlayerList caps whitelist.json and ops.json, which list at most a few
// thousand players.
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
	return &apiError{Status: http.StatusConflict, Code: api.CodeConflict, Msg: couldNot + " " + ge.Msg, Hint: ge.Hint}
}

// readPlayerList reads whitelist.json or ops.json into v. A missing file
// leaves v as it is.
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

// The game process owns its data directory, so a plugin can swap any file in
// it for a symlink to a host file or for a FIFO. The agent runs as root: it
// reads those files only through os.Root (no symlink leads out of the
// directory), opened non-blocking (a FIFO cannot hang it), only when they are
// regular files, and never more than a limit.

func openGameFile(root *os.Root, rel string) (*os.File, fs.FileInfo, error) {
	f, err := root.OpenFile(filepath.FromSlash(rel), gameReadFlags, 0)
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, nil, fmt.Errorf("%s is not a regular file", filepath.Base(rel))
	}
	return f, st, nil
}

// readGameFile reads at most limit bytes of rel below dir.
func readGameFile(dir, rel string, limit int64) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, _, err := openGameFile(root, rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}

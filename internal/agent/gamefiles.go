package agent

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

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

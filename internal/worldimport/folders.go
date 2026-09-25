package worldimport

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// WorldFolders lists the folders in a server's data directory that make up
// its current world, which an import replaces: the level-name folder and the
// separate Nether, End and custom dimension folders Paper and its forks keep
// next to it, such as world_nether and world_the_end. Names are relative to
// dataDir and sorted. A world folder that is a link to somewhere else is
// refused with an *Error, because replacing it would move the world off the
// disk it was put on.
func WorldFolders(dataDir, levelName string) ([]string, error) {
	if levelName == "" {
		levelName = "world"
	}
	if !validLevelName(levelName) {
		return nil, levelNameError(levelName)
	}
	entries, err := os.ReadDir(dataDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("worldimport: list world folders: %w", err)
	}
	var out []string
	for _, de := range entries {
		name := de.Name()
		switch {
		case name == levelName, name == levelName+"_nether", name == levelName+"_the_end":
			if de.Type()&fs.ModeSymlink != 0 {
				return nil, refuse(KindFolderLink, "Replace the link with the folder itself, or import the world by hand.",
					fmt.Sprintf("%s in the server's folder is a link to another place. Playkeeper doesn't replace world folders that are links.", quoted(name)),
					"folder", clip(name))
			}
		case len(name) > len(levelName)+1 && name[:len(levelName)+1] == levelName+"_" && de.IsDir() &&
			customCompanionDir(filepath.Join(dataDir, name), name[len(levelName):]):
		default:
			continue
		}
		out = append(out, name)
	}
	return out, nil
}

// customCompanionDir reports whether dir is the folder Paper keeps a custom
// dimension in: <level-name>_<namespace>_<path> holding
// dimensions/<namespace>/<path>.
func customCompanionDir(dir, suffix string) bool {
	namespaces, err := os.ReadDir(filepath.Join(dir, "dimensions"))
	if err != nil {
		return false
	}
	for _, ns := range namespaces {
		if !ns.IsDir() {
			continue
		}
		paths, err := os.ReadDir(filepath.Join(dir, "dimensions", ns.Name()))
		if err != nil {
			continue
		}
		for _, p := range paths {
			if p.IsDir() && "_"+ns.Name()+"_"+p.Name() == suffix {
				return true
			}
		}
	}
	return false
}

package worldimport

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// WorldFolders lists the folders in a server's data directory that make up
// its current world, which an import replaces: the level-name folder and the
// separate Nether, End and custom dimension folders Paper and its forks keep
// next to it, such as world_nether and world_the_end. Names are relative to
// dataDir and sorted. A link named like any of them, a custom dimension's
// <level-name>_<namespace>_<path> included, is refused with an *Error,
// because replacing it would move the world off the disk it was put on. The
// name alone decides: a link in the game's folder is never followed.
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
		link := de.Type()&fs.ModeSymlink != 0
		dim, custom := strings.CutPrefix(name, levelName+"_")
		switch {
		case name == levelName, name == levelName+"_nether", name == levelName+"_the_end":
		case custom && link && dimensionName(dim):
		case custom && de.IsDir() && customCompanionDir(filepath.Join(dataDir, name), dim):
		default:
			continue
		}
		if link {
			return nil, refuse(KindFolderLink, "Replace the link with the folder itself, or import the world by hand.",
				fmt.Sprintf("%s in the server's folder is a link to another place. Playkeeper doesn't replace world folders that are links.", quoted(name)),
				"folder", clip(name))
		}
		out = append(out, name)
	}
	return out, nil
}

// dimensionName reports whether dim can be the <namespace>_<path> part of a
// custom dimension folder's name: two parts joined by "_", made only of the
// characters a dimension's id may have.
func dimensionName(dim string) bool {
	if len(dim) < 3 || !strings.Contains(dim[1:len(dim)-1], "_") {
		return false
	}
	for _, c := range dim {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '.' && c != '-' {
			return false
		}
	}
	return true
}

// customCompanionDir reports whether dir is the folder Paper keeps a custom
// dimension in: <level-name>_<namespace>_<path> holding
// dimensions/<namespace>/<path>, where dim is <namespace>_<path>.
func customCompanionDir(dir, dim string) bool {
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
			if p.IsDir() && ns.Name()+"_"+p.Name() == dim {
				return true
			}
		}
	}
	return false
}

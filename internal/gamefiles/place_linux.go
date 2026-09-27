package gamefiles

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// renameInto renames staged into the folder handle dir as name, relative to
// both folders' handles. Without replace, something at name is refused
// rather than replaced. It is a variable so that a test can make it answer
// as it does across file systems.
var renameInto = func(staged string, dir *os.File, name string, replace bool) error {
	src, err := os.Open(filepath.Dir(staged))
	if err != nil {
		return err
	}
	defer src.Close()
	if !replace {
		if err := renameNoReplace(src, filepath.Base(staged), dir, name); !errors.Is(err, errNoReplace) {
			return err
		}
	}
	err = unix.Renameat(int(src.Fd()), filepath.Base(staged), int(dir.Fd()), name)
	if errors.Is(err, unix.EXDEV) {
		return errOtherFileSystem
	}
	if err != nil {
		return &os.LinkError{Op: "renameat", Old: staged, New: name, Err: err}
	}
	return nil
}

// renameNoReplace renames from, in the folder fromDir, to to, in the folder
// toDir, and refuses with fs.ErrExist in the same step when something is at
// to already. A file system that can't do that, or an old kernel, gets
// errNoReplace, and nothing is renamed. It is a variable so that a test can
// make it answer as such a file system does.
var renameNoReplace = func(fromDir *os.File, from string, toDir *os.File, to string) error {
	err := unix.Renameat2(int(fromDir.Fd()), from, int(toDir.Fd()), to, unix.RENAME_NOREPLACE)
	switch {
	case errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOSYS), errors.Is(err, unix.EOPNOTSUPP):
		return errNoReplace
	case errors.Is(err, unix.EXDEV):
		return errOtherFileSystem
	case err != nil:
		return &os.LinkError{Op: "renameat2", Old: from, New: to, Err: err}
	}
	return nil
}

package gamefiles

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// renameInto renames staged into the folder handle dir as name, relative to
// both folders' handles. It is a variable so that a test can make it answer
// as it does across file systems.
var renameInto = func(staged string, dir *os.File, name string) error {
	src, err := os.Open(filepath.Dir(staged))
	if err != nil {
		return err
	}
	defer src.Close()
	err = syscall.Renameat(int(src.Fd()), filepath.Base(staged), int(dir.Fd()), name)
	if errors.Is(err, syscall.EXDEV) {
		return errOtherFileSystem
	}
	if err != nil {
		return &os.LinkError{Op: "renameat", Old: staged, New: name, Err: err}
	}
	return nil
}

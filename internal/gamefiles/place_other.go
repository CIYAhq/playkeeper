//go:build !linux

package gamefiles

import "os"

// renameInto always copies where renaming relative to a folder's handle
// isn't available.
var renameInto = func(string, *os.File, string) error { return errOtherFileSystem }

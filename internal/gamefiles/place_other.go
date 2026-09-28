//go:build !linux

package gamefiles

import "os"

// renameInto always copies where renaming relative to a folder's handle
// isn't available.
var renameInto = func(string, *os.File, string, bool) error { return errOtherFileSystem }

// renameNoReplace leaves the check to the caller where renaming without
// replacing isn't available.
var renameNoReplace = func(*os.File, string, *os.File, string) error { return errNoReplace }

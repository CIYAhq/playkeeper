package sleep

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	iconFile   = "server-icon.png"
	iconPrefix = "data:image/png;base64,"
	// maxIconBytes keeps the status response within what clients accept.
	maxIconBytes = 20 << 10
	maxIconURI   = len(iconPrefix) + (maxIconBytes+2)/3*4
)

// ReadIcon reads the server list icon, server-icon.png in the server's data
// directory, as the data URI a status response carries. No icon is not an
// error: it returns "". The icon must be a 64×64 PNG of at most 20 KiB. A
// link is refused, since the server can write that directory and the agent
// must not read files outside it.
func ReadIcon(dataDir string) (string, error) {
	path := filepath.Join(dataDir, iconFile)
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", iconUnreadable(err)
	}
	if !fi.Mode().IsRegular() {
		return "", &Error{Code: "icon_invalid", Msg: "The server icon, server-icon.png, must be a file, not a link or a folder.",
			Hint: "Upload the icon again as a 64×64 PNG image."}
	}
	if fi.Size() > maxIconBytes {
		return "", iconTooLarge()
	}
	f, err := os.Open(path)
	if err != nil {
		return "", iconUnreadable(err)
	}
	defer f.Close()
	if opened, err := f.Stat(); err != nil || !os.SameFile(fi, opened) {
		return "", iconUnreadable(err)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxIconBytes+1))
	if err != nil {
		return "", iconUnreadable(err)
	}
	if len(b) > maxIconBytes {
		return "", iconTooLarge()
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width != 64 || cfg.Height != 64 {
		return "", &Error{Code: "icon_invalid", Msg: "The server icon, server-icon.png, must be a 64×64 PNG image.",
			Hint: "Resize the image to 64×64 pixels and save it as PNG.", Err: err}
	}
	return iconPrefix + base64.StdEncoding.EncodeToString(b), nil
}

func iconTooLarge() error {
	return &Error{Code: "icon_too_large", Msg: "The server icon, server-icon.png, is larger than 20 KiB.",
		Hint: "Save it as a 64×64 PNG without extra metadata; that is usually a few KiB.", Params: map[string]any{"maxBytes": maxIconBytes}}
}

func iconUnreadable(err error) error {
	return &Error{Code: "icon_unreadable", Msg: "The server icon, server-icon.png, could not be read.", Err: err}
}

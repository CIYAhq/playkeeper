package sleep

import (
	"bytes"
	"encoding/base64"
	"image/png"
)

const (
	// IconFile is the server list icon in the server's data directory.
	IconFile   = "server-icon.png"
	iconPrefix = "data:image/png;base64,"
	// MaxIconBytes keeps the status response within what clients accept.
	MaxIconBytes = 20 << 10
	maxIconURI   = len(iconPrefix) + (MaxIconBytes+2)/3*4
)

// IconURI turns the server list icon, IconFile as read from the server's
// data directory, into the data URI a status response carries. The icon must
// be a 64×64 PNG of at most MaxIconBytes. The caller reads the file without
// following links (internal/gamefiles), since the game can write there.
func IconURI(b []byte) (string, error) {
	if len(b) > MaxIconBytes {
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
		Hint: "Save it as a 64×64 PNG without extra metadata; that is usually a few KiB.", Params: map[string]any{"maxBytes": MaxIconBytes}}
}

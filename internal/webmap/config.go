package webmap

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Settings are Playkeeper's choices in squaremap's config for one server.
type Settings struct {
	// Link is the map's public address while friends may open it
	// (https://<panel address>/map/<server slug>), otherwise empty.
	// squaremap shows it to operators who type /squaremap link and to
	// players with its client mod; it reads it once at startup.
	Link string
	// RenderThreads bounds the threads a full render uses, from 1 to
	// MaxRenderThreads; 0 means 1, which suits a small VPS.
	RenderThreads int
}

const (
	MaxRenderThreads = 4
	maxLink          = 200
)

// configTemplate is squaremap's config.yml (config-version 2, keys from
// squaremap's Config and WorldConfig). Keys not listed keep squaremap's
// defaults. Playkeeper writes every key its proxy and status depend on:
// the web server's port and folder, the zoom levels, and the command name.
const configTemplate = `# Written by Playkeeper before every start; changes made here are replaced.
# squaremap's web server only answers inside the server's container, and
# Playkeeper shows the map in its own pages.
config-version: 2
settings:
  update-checker: false
  web-address: '%s'
  web-directory:
    path: web
    auto-update: true
  internal-webserver:
    enabled: true
    bind: 0.0.0.0
    port: %d
    flush-json-immediately: false
  commands:
    main-command-label: squaremap
  render-progress-logging:
    enabled: true
    interval-seconds: 60
world-settings:
  default:
    map:
      enabled: true
      max-render-threads: %d
      zoom:
        maximum: %d
        default: %d
        extra: 2
      background-render:
        enabled: true
        max-chunks-per-interval: 256
        interval-seconds: 30
        max-render-threads: 1
    player-tracker:
      enabled: true
      update-interval-seconds: 1
      nameplate:
        enabled: true
        show-head: false
        heads-url: ''
        show-armor: false
        show-health: false
      hide:
        invisible: true
        spectators: true
        map-invisibility-equipment: true
      use-display-names: false
`

// Config returns squaremap's config.yml for the settings.
//
// The web server binds to every address inside the container, which is its
// loopback and its one interface on Playkeeper's private network; Docker
// assigns that address at start, so it cannot be named here. The port is
// not published, and the network keeps containers from reaching each
// other, so only this machine reaches it. Update checks are off and no
// player heads are fetched from anyone. Only the Paper build sends bStats
// statistics, and the agent turns bStats off for the whole server before
// every start. Player health and armour are left out of what squaremap
// publishes. Drawing uses one thread for new land and at most
// RenderThreads for a full render, so the game keeps its CPU.
func Config(s Settings) ([]byte, error) {
	if !validLink(s.Link) {
		return nil, fail(KindInvalid, kv("field", "link"),
			"The map link must be a plain https:// address of at most 200 characters.",
			"Playkeeper builds it from the panel's address and the server's short name.")
	}
	threads := s.RenderThreads
	if threads == 0 {
		threads = 1
	}
	if threads < 1 || threads > MaxRenderThreads {
		return nil, fail(KindInvalid, kv("field", "renderThreads", "max", strconv.Itoa(MaxRenderThreads)),
			fmt.Sprintf("The map can draw with 1 to %d threads, not %d.", MaxRenderThreads, s.RenderThreads), "")
	}
	return fmt.Appendf(nil, configTemplate, s.Link, Port, threads, ZoomMax, ZoomMax), nil
}

// validLink accepts an https URL that is safe inside single quotes in YAML:
// printable ASCII without quotes or backslashes, no user, query or
// fragment.
func validLink(s string) bool {
	if s == "" {
		return true
	}
	if len(s) > maxLink {
		return false
	}
	for _, r := range s {
		if r <= 0x20 || r >= 0x7f || r == '\'' || r == '"' || r == '\\' {
			return false
		}
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.Opaque == "" && u.User == nil &&
		u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

// WriteConfig writes squaremap's config.yml into the server's files,
// replacing the one there. Call it before every start of a server that has
// squaremap: squaremap reads it once at startup, and a restored backup or
// an edit by hand may have changed it. The game can write in its data
// directory, so every step stays inside it and refuses links.
func (m Map) WriteConfig(s Settings) error {
	l, err := LayoutFor(m.Type)
	if err != nil {
		return err
	}
	b, err := Config(s)
	if err != nil {
		return err
	}
	dir, err := openFolder(m.Dir, l.Dir, true, m.Owner)
	if err != nil {
		return folderError(l.Dir, err)
	}
	defer dir.Close()
	if err := writeFile(dir, "config.yml", b, m.Owner); err != nil {
		return folderError(l.Dir, err)
	}
	return nil
}

func folderError(dir string, err error) *Error {
	why := reason(err)
	return &Error{Kind: KindFolderUnusable, Params: kv("folder", dir, "reason", why),
		Msg:  "Playkeeper cannot write squaremap's settings in " + dir + " in the server's files (" + why + ").",
		Hint: "Make sure " + dir + " is a normal folder inside the server's files, not a link, then start the server again.",
		Err:  err}
}

// reason says what went wrong with a file, without Go's names for system
// calls.
func reason(err error) string {
	var nf notFolder
	var pe *fs.PathError
	var le *os.LinkError
	switch {
	case errors.As(err, &nf):
		return nf.Error()
	case errors.As(err, &pe):
		return pe.Err.Error()
	case errors.As(err, &le):
		return le.Err.Error()
	}
	return err.Error()
}

// notFolder is a path in the server's files that is a file or a link where
// a folder should be.
type notFolder string

func (p notFolder) Error() string { return string(p) + " is not a folder" }

// openFolder opens rel inside dataDir, one folder at a time, refusing links
// and anything that is not a folder. Missing folders are created (and
// given to o) when create is set; otherwise openFolder returns nil, nil.
func openFolder(dataDir, rel string, create bool, o *Owner) (*os.Root, error) {
	data, err := os.OpenRoot(dataDir)
	if err != nil {
		return nil, err
	}
	defer data.Close()
	return openIn(data, rel, create, o)
}

func openIn(r *os.Root, rel string, create bool, o *Owner) (*os.Root, error) {
	parts := strings.Split(rel, "/")
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		fi, err := r.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist) && create:
			if err := r.Mkdir(p, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
				return nil, err
			}
			if err := chown(r, p, o); err != nil {
				return nil, err
			}
		case errors.Is(err, fs.ErrNotExist):
			return nil, nil
		case err != nil:
			return nil, err
		case !fi.IsDir():
			return nil, notFolder(p)
		}
	}
	return r.OpenRoot(rel)
}

func chown(r *os.Root, name string, o *Owner) error {
	if o == nil {
		return nil
	}
	return r.Lchown(name, o.UID, o.GID)
}

// writeFile replaces name in r with b: a hidden temporary file first, then
// a rename, so squaremap never reads half a file.
func writeFile(r *os.Root, name string, b []byte, o *Owner) error {
	tmp := "." + name + ".playkeeper-" + randomHex()
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = chown(r, tmp, o)
	}
	if err == nil {
		err = r.Rename(tmp, name)
	}
	if err != nil {
		r.Remove(tmp)
	}
	return err
}

func randomHex() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

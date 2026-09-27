// Package webmap shows a server's live map in Playkeeper's own pages.
//
// squaremap, a map plugin (a mod on Fabric, Quilt and NeoForge), draws the
// world into image tiles inside the game server. Playkeeper installs it
// through the add-on library, writes its settings so that its web server
// only answers inside the server's container and it never contacts anyone,
// and passes on the few read-only answers the map page needs: tiles, the
// list of worlds, and where players are. squaremap's own web page is never
// shown, so its scripts and third-party images stay out of the panel and
// viewers' addresses stay with Playkeeper.
//
// The package keeps no state. Callers pass the server's type and data
// directory, squaremap's address, an HTTP client and a clock.
package webmap

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// squaremap on Modrinth and Hangar. The add-on library installs it by
// (Source, ProjectID) from LayoutFor; Hangar lists the same Paper jar under
// its numeric id, kept as text like the library keys it.
const (
	PluginName        = "squaremap"
	ModrinthProjectID = "PFb7ZqK6"
	ModrinthSlug      = "squaremap"
	HangarProjectID   = "20"
	HangarNamespace   = "jmp/squaremap"
)

// Port is squaremap's web server port inside the server's container. It is
// never published on the host.
const Port = 25580

// Tile geometry: squaremap draws 512×512-pixel tiles. At zoom ZoomMax one
// pixel is one block, so a tile covers one region file; each lower zoom
// halves the detail.
const (
	TileSize = 512
	ZoomMax  = 3
)

// What drawing the land explored so far costs on a small VPS, for the setup
// card.
const (
	EstimatedMinutes   = 10
	EstimatedMegabytes = 200
)

const (
	// DefaultTimeout bounds each answer from squaremap.
	DefaultTimeout = 5 * time.Second
	// MaxConnsPerServer bounds NewClient's connections to one squaremap.
	MaxConnsPerServer = 16
)

// Layout is how squaremap is installed on one server type.
type Layout struct {
	Type string `json:"type"`
	// Source and ProjectID are the add-on library's key for squaremap.
	Source    string `json:"source"`
	ProjectID string `json:"projectId"`
	// Folder is where the add-on library puts the jar (plugins or mods) and
	// Dir is squaremap's own folder, both inside the server's data
	// directory.
	Folder string `json:"folder"`
	Dir    string `json:"dir"`
}

// LayoutFor tells where squaremap goes on a server type (the ids in the
// server type registry). Modrinth has squaremap builds for Paper, Fabric
// and NeoForge; Purpur runs the Paper build, and Quilt the Fabric one with
// Fabric API, which the add-on library adds as a dependency.
func LayoutFor(serverType string) (Layout, error) {
	l := Layout{Type: serverType, Source: "modrinth", ProjectID: ModrinthProjectID}
	switch serverType {
	case "paper", "purpur":
		l.Folder, l.Dir = "plugins", "plugins/squaremap"
	case "fabric", "quilt", "neoforge":
		l.Folder, l.Dir = "mods", "squaremap"
	case "vanilla":
		return Layout{}, fail(KindUnsupported, kv("type", serverType),
			"Vanilla servers cannot show a live map.",
			"The map needs a plugin or mod. Switch the server to Paper, Fabric, Quilt or NeoForge to use it.")
	default:
		return Layout{}, fail(KindUnknownType, kv("type", printable(serverType)),
			fmt.Sprintf("Playkeeper does not know the server type \"%s\", so it cannot set up a map for it.", printable(serverType)),
			"Choose a supported server type in the server's settings.")
	}
	return l, nil
}

// Owner owns the files and folders Playkeeper writes for squaremap: the
// game's user, so that squaremap can update its own config.
type Owner struct{ UID, GID int }

// Map is one server's map. Build one per request from what the caller
// knows; it keeps nothing between calls.
type Map struct {
	// Dir is the server's data directory on this machine.
	Dir string
	// Type is the server type: paper, purpur, fabric, quilt, neoforge or
	// vanilla.
	Type string
	// Addr is squaremap's web server: the container's address on the
	// private network, and Port. Empty while the server is stopped.
	Addr string
	// Client reaches Addr: one from NewClient, shared across requests.
	// nil makes a one-off client without keep-alive.
	Client *http.Client
	// Timeout bounds each answer from squaremap; 0 means DefaultTimeout.
	Timeout time.Duration
	Now     func() time.Time
	// Owner, when set, owns what WriteConfig creates.
	Owner *Owner
}

func (m Map) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func (m Map) timeout() time.Duration {
	if m.Timeout > 0 {
		return m.Timeout
	}
	return DefaultTimeout
}

// NewClient returns a client for reaching squaremap in servers' containers,
// to share across requests and servers. Unlike http.DefaultClient it never
// goes through an HTTP proxy from the environment: squaremap answers
// directly or not at all. It opens at most MaxConnsPerServer connections
// to each squaremap, which shares the game's CPU, however many people have
// the map open; further requests wait their turn within their timeout.
func NewClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: DefaultTimeout}).DialContext,
		ResponseHeaderTimeout: DefaultTimeout,
		MaxConnsPerHost:       MaxConnsPerServer,
		MaxIdleConnsPerHost:   MaxConnsPerServer / 2,
		IdleConnTimeout:       time.Minute,
	}}
}

// client never follows redirects and keeps no cookies.
func (m Map) client() *http.Client {
	c := http.Client{Transport: &http.Transport{
		DialContext:       (&net.Dialer{Timeout: m.timeout()}).DialContext,
		DisableKeepAlives: true,
	}}
	if m.Client != nil {
		c = *m.Client
	}
	c.Jar = nil
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

var errNoAddr = errors.New("no address")

// addr accepts only an IP address and port: the container's address comes
// from Docker, never from a name that would need looking up.
func (m Map) addr() (string, error) {
	if m.Addr == "" {
		return "", errNoAddr
	}
	ap, err := netip.ParseAddrPort(m.Addr)
	if err != nil || ap.Addr().IsUnspecified() || ap.Port() == 0 {
		return "", fmt.Errorf("%q is not an IP address and port", m.Addr)
	}
	return ap.String(), nil
}

func printable(s string) string {
	r := []rune(s)
	if len(r) > 40 {
		r = append(r[:40], '…')
	}
	for i, c := range r {
		if c < 0x20 || c == 0x7f {
			r[i] = '?'
		}
	}
	return string(r)
}

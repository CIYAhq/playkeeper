package share

import (
	"net"
	"strconv"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/addons"
)

// Enabled reports whether a server's public page and its file answer: the
// owner's switch is on and the server runs mods. It is checked on every
// public request, with the switch as stored, and a false answers exactly as
// an unknown token does, so turning the switch off works at once. Signed-in
// users' downloads don't depend on it.
func Enabled(on bool, serverType string) bool { return on && Supported(serverType) }

// PathPrefix is where public pack pages live: the page at /packs/<token>,
// its data at /packs/<token>/page, the server's emblem at
// /packs/<token>/icon and the file at /packs/<token>/<slug>.mrpack. The
// token is random (see NewToken), never the server's name.
const PathPrefix = "/packs/"

// FilePath is the public route of a server's file, or "" for a token or slug
// that isn't valid.
func FilePath(token, slug string) string {
	if !ValidToken(token) || !ValidSlug(slug) {
		return ""
	}
	return PathPrefix + token + "/" + FileName(slug)
}

// Page is the data of the public /packs/<token> page. It lists only what
// friends get; server-only mods stay off it.
type Page struct {
	Server           string `json:"server"`
	MinecraftVersion string `json:"minecraftVersion"`
	Loader           string `json:"loader"`     // fabric, quilt or neoforge
	LoaderName       string `json:"loaderName"` // Fabric, Quilt or NeoForge
	LoaderVersion    string `json:"loaderVersion"`
	Pack             *Pack  `json:"pack,omitempty"`
	Notice           Text   `json:"notice"`
	// Steps are what friends do, in short; Launchers say it step by step
	// for each launcher.
	Steps     []Text     `json:"steps"`
	Launchers []Launcher `json:"launchers"`
	Mods      []PageMod  `json:"mods"`
	Yourself  []Yourself `json:"yourself,omitempty"`
	Download  Download   `json:"download"`
	// Address is the server's join address, or "" when there is none to
	// show.
	Address string `json:"address,omitempty"`
}

// PageMod is a mod friends get, in the file or by hand.
type PageMod struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	From    From   `json:"from"`
	Page    string `json:"page,omitempty"`
	Need    Need   `json:"need"`
	Label   Text   `json:"label"`
	InFile  bool   `json:"inFile"`
	// NeededBy names the user's mod this one was added for, as in "Needed by
	// Waystones".
	NeededBy string `json:"neededBy,omitempty"`
}

// Launcher is how to add the file in one launcher.
type Launcher struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Site  string `json:"site"` // where to get the launcher
	Steps []Text `json:"steps"`
}

// Download is the file the page offers.
type Download struct {
	URL  string `json:"url"` // on the panel's own address
	Name string `json:"name"`
	Size int64  `json:"size"`
	Type string `json:"type"`
}

// Page returns the data of the public page at token, for the server with
// slug. address is the server's join address as players type it, or "" when
// there is none to show. It asks no one.
func (s *Share) Page(token, slug, address string) (*Page, error) {
	if !ValidToken(token) {
		return nil, fail(addons.KindInvalid, kv("field", "token"), "That is not a pack's link.", "")
	}
	if !ValidSlug(slug) {
		return nil, fail(addons.KindInvalid, kv("field", "slug"), "That is not a server's link name.", "")
	}
	b, err := s.File()
	if err != nil {
		return nil, err
	}
	file, address := FileName(slug), joinAddress(address)
	p := &Page{
		Server: s.Server, MinecraftVersion: s.MinecraftVersion, Loader: s.Type, LoaderName: loaderName(s.Type), LoaderVersion: s.LoaderVersion,
		Pack: s.Pack, Notice: s.Notice, Steps: Steps(file, address, false), Launchers: Launchers(file),
		Mods: []PageMod{}, Yourself: s.Yourself, Address: address,
		Download: Download{URL: FilePath(token, slug), Name: file, Size: int64(len(b)), Type: ContentType},
	}
	for _, m := range s.Mods {
		if m.InFile || m.ByHand {
			p.Mods = append(p.Mods, PageMod{Name: m.Name, Version: m.Version, From: m.From, Page: m.Page, Need: m.Need, Label: m.Label, InFile: m.InFile,
				NeededBy: s.neededBy(m)})
		}
	}
	return p, nil
}

// neededBy is the name of the user's mod that m was added for, or "".
func (s *Share) neededBy(m Mod) string {
	if m.From != FromUser || m.DependencyOf == "" {
		return ""
	}
	for _, o := range s.Mods {
		if o.From == FromUser && o.Source == m.Source && o.Project == m.DependencyOf {
			return o.Name
		}
	}
	return ""
}

// Steps are what friends do, in short. link is set for the owner's share
// dialog, where friends start from the link rather than the page.
func Steps(file, address string, link bool) []Text {
	first := text("share.step.download", "Download {file}.", "file", file)
	if link {
		first = text("share.step.open_link", "Open the link and download the file.")
	}
	last := text("share.step.play", "Press Play, then join the server.")
	if address != "" {
		last = text("share.step.play_join", "Press Play, then join {address}.", "address", address)
	}
	return []Text{first, text("share.step.import", "Import it in the Modrinth App or Prism Launcher."), last}
}

// Launchers say how to add the file in the launchers that read .mrpack
// files.
func Launchers(file string) []Launcher {
	return []Launcher{
		{ID: "modrinth-app", Name: "Modrinth App", Site: "https://modrinth.com/app", Steps: []Text{
			text("share.launcher.modrinth_app.add", "In the Modrinth App, click + in the sidebar and choose From file."),
			text("share.launcher.modrinth_app.pick", "Pick {file}. The app downloads the mods from Modrinth.", "file", file),
			text("share.launcher.modrinth_app.play", "Open the new instance and press Play."),
		}},
		{ID: "prism", Name: "Prism Launcher", Site: "https://prismlauncher.org", Steps: []Text{
			text("share.launcher.prism.add", "In Prism Launcher, click Add Instance and choose Import."),
			text("share.launcher.prism.pick", "Click Browse, pick {file} and click OK. Pasting the download link works too.", "file", file),
			text("share.launcher.prism.launch", "Select the new instance and click Launch."),
		}},
	}
}

func loaderName(serverType string) string {
	if t, err := addons.TargetFor(serverType); err == nil {
		return t.Name()
	}
	return serverType
}

// joinAddress accepts a server address as players type it: a host name or IP
// address, with a port or without.
func joinAddress(a string) string {
	a = strings.TrimSpace(a)
	if a == "" || len(a) > 262 {
		return ""
	}
	host := a
	if h, port, err := net.SplitHostPort(a); err == nil {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return ""
		}
		host = h
	}
	if net.ParseIP(host) == nil && !hostName(host) {
		return ""
	}
	return a
}

func hostName(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

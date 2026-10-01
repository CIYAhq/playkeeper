package minecraft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// DefaultFillURL is PaperMC's Fill v3 API, which lists Paper versions and
// builds with the SHA-256 of every jar.
const DefaultFillURL = "https://fill.papermc.io"

// ImageJava is the Java version in the pinned runtime image.
const ImageJava = 25

// OldestRelease is the oldest Minecraft release Playkeeper runs, for every
// server type and for modpacks: the version most modpacks of Minecraft 1.20
// were made for.
const OldestRelease = "1.20.1"

// Fill reads Paper versions and builds from PaperMC's Fill v3 API.
type Fill struct {
	BaseURL string
	Client  *http.Client
}

type fillVersion struct {
	Version struct {
		ID      string `json:"id"`
		Support struct {
			Status string `json:"status"`
		} `json:"support"`
		Java struct {
			Version struct {
				Minimum int `json:"minimum"`
			} `json:"version"`
		} `json:"java"`
	} `json:"version"`
}

type fillBuild struct {
	ID        int                     `json:"id"`
	Channel   string                  `json:"channel"`
	Downloads map[string]fillDownload `json:"downloads"`
}

type fillDownload struct {
	Name      string `json:"name"`
	Checksums struct {
		SHA256 string `json:"sha256"`
	} `json:"checksums"`
	URL string `json:"url"`
}

var (
	reRelease = regexp.MustCompile(`^[0-9]+(\.[0-9]+)+$`)
	reSHA256  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// errNoVersion means PaperMC has no such Minecraft version.
var errNoVersion = errors.New("PaperMC has no such version")

// paperSource is where Paper's versions and builds are read: PaperMC's Fill
// API, or the list built into Playkeeper.
type paperSource interface {
	versions(ctx context.Context) ([]fillVersion, error)
	version(ctx context.Context, id string) (fillVersion, error)
	builds(ctx context.Context, id string) ([]fillBuild, error)
}

func (f Fill) versions(ctx context.Context) ([]fillVersion, error) {
	var list struct {
		Versions []fillVersion `json:"versions"`
	}
	err := f.get(ctx, "/v3/projects/paper/versions", &list)
	return list.Versions, err
}

func (f Fill) version(ctx context.Context, id string) (fillVersion, error) {
	var v fillVersion
	err := f.get(ctx, "/v3/projects/paper/versions/"+id, &v)
	return v, err
}

func (f Fill) builds(ctx context.Context, id string) ([]fillBuild, error) {
	var builds []fillBuild
	err := f.get(ctx, "/v3/projects/paper/versions/"+id+"/builds", &builds)
	return builds, err
}

func (f Fill) get(ctx context.Context, path string, v any) error {
	u, err := url.JoinPath(f.BaseURL, path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "playkeeper (https://github.com/CIYAhq/playkeeper)")
	hc := f.Client
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach PaperMC (%s): %w", f.BaseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errNoVersion
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("PaperMC answered HTTP %d for %s", resp.StatusCode, path)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v); err != nil {
		return fmt.Errorf("unexpected answer from PaperMC for %s: %w", path, err)
	}
	return nil
}

// Catalog lists the Paper versions to offer, newest first: the newest
// release of every version family from OldestRelease on, each with its
// latest stable build. A version PaperMC supports but has no stable build of
// yet is offered as experimental, with its latest build. The newest version
// with a stable build is recommended.
func (f Fill) Catalog(ctx context.Context) ([]api.CatalogEntry, error) { return catalogFrom(ctx, f) }

func catalogFrom(ctx context.Context, src paperSource) ([]api.CatalogEntry, error) {
	list, err := src.versions(ctx)
	if err != nil {
		return nil, err
	}
	var releases []fillVersion
	for _, v := range list {
		if reRelease.MatchString(v.Version.ID) {
			releases = append(releases, v)
		}
	}
	// Newest first, whatever order PaperMC lists them in.
	slices.SortStableFunc(releases, func(a, b fillVersion) int { return CompareMinecraft(b.Version.ID, a.Version.ID) })
	stableFamily := map[string]bool{}
	var out []api.CatalogEntry
	for _, v := range releases {
		id := v.Version.ID
		fam := family(id)
		if stableFamily[fam] || CompareMinecraft(id, OldestRelease) < 0 || v.Version.Java.Version.Minimum > ImageJava {
			continue
		}
		supported := v.Version.Support.Status == "SUPPORTED"
		builds, err := src.builds(ctx, id)
		if err != nil {
			return nil, err
		}
		e, ok := entryFor(id, supported, v.Version.Java.Version.Minimum, builds)
		if !ok || (e.Experimental && !supported) {
			continue
		}
		if !e.Experimental {
			stableFamily[fam] = true
		}
		out = append(out, e)
	}
	for i := range out {
		if !out[i].Experimental {
			out[i].Recommended = true
			break
		}
	}
	for i := range out {
		out[i].Notes = notes(out[i])
	}
	if len(out) == 0 {
		return nil, errors.New("PaperMC lists no Paper version Playkeeper can run")
	}
	return out, nil
}

// RestoreBuild picks the build to restore a backup made with Minecraft
// mcVersion on Paper build `build`: the version's latest stable build if it
// is not older than the backup's, otherwise the backup's own build.
func (f Fill) RestoreBuild(ctx context.Context, mcVersion string, build int) (api.CatalogEntry, error) {
	return restoreBuildFrom(ctx, f, mcVersion, build)
}

// ErrNoPaperBuild matches a restore that has no build to run whoever is
// asked, as opposed to one PaperMC could not be asked about.
var ErrNoPaperBuild = errors.New("no Paper build to restore with")

type noPaperBuild string

func (e noPaperBuild) Error() string      { return string(e) }
func (noPaperBuild) Is(target error) bool { return target == ErrNoPaperBuild }

func restoreBuildFrom(ctx context.Context, src paperSource, mcVersion string, build int) (api.CatalogEntry, error) {
	if !reRelease.MatchString(mcVersion) {
		return api.CatalogEntry{}, noPaperBuild(fmt.Sprintf("%q is not a Minecraft release version", mcVersion))
	}
	if CompareMinecraft(mcVersion, OldestRelease) < 0 {
		return api.CatalogEntry{}, noPaperBuild(fmt.Sprintf("Playkeeper runs Minecraft %s and newer, not %s", OldestRelease, mcVersion))
	}
	v, err := src.version(ctx, mcVersion)
	if err != nil {
		if errors.Is(err, errNoVersion) {
			return api.CatalogEntry{}, noPaperBuild(fmt.Sprintf("PaperMC has no Paper build of Minecraft %s", mcVersion))
		}
		return api.CatalogEntry{}, err
	}
	java := v.Version.Java.Version.Minimum
	if java > ImageJava {
		return api.CatalogEntry{}, noPaperBuild(fmt.Sprintf("Paper for Minecraft %s needs Java %d; this Playkeeper runs Java %d", mcVersion, java, ImageJava))
	}
	builds, err := src.builds(ctx, mcVersion)
	if err != nil {
		return api.CatalogEntry{}, err
	}
	supported := v.Version.Support.Status == "SUPPORTED"
	if e, ok := entryFor(mcVersion, supported, java, builds); ok && !e.Experimental && e.PaperBuild >= build {
		e.Notes = notes(e)
		return e, nil
	}
	for _, b := range builds {
		if b.ID == build {
			if e, ok := entryFor(mcVersion, supported, java, []fillBuild{b}); ok {
				e.Notes = notes(e)
				return e, nil
			}
		}
	}
	return api.CatalogEntry{}, noPaperBuild(fmt.Sprintf("PaperMC has no usable build of Minecraft %s for this backup (it was made with build %d)", mcVersion, build))
}

func stableChannel(c string) bool { return c == "STABLE" || c == "RECOMMENDED" }

// entryFor picks a version's latest stable build, or its latest build when
// it has no stable one (experimental). Builds without a usable jar checksum
// are ignored.
func entryFor(id string, supported bool, java int, builds []fillBuild) (api.CatalogEntry, bool) {
	var stable, latest *fillBuild
	for i := range builds {
		b := &builds[i]
		d, ok := b.Downloads["server:default"]
		if !ok || !reSHA256.MatchString(d.Checksums.SHA256) {
			continue
		}
		if latest == nil || b.ID > latest.ID {
			latest = b
		}
		if stableChannel(b.Channel) && (stable == nil || b.ID > stable.ID) {
			stable = b
		}
	}
	pick, experimental := stable, false
	if pick == nil {
		pick, experimental = latest, true
	}
	if pick == nil {
		return api.CatalogEntry{}, false
	}
	return api.CatalogEntry{
		ID: VersionID(id), Label: "Paper " + id, MinecraftVersion: id, PaperBuild: pick.ID,
		JarSHA256: pick.Downloads["server:default"].Checksums.SHA256, Java: java,
		Channel: pick.Channel, Experimental: experimental, Supported: supported,
	}, true
}

func notes(e api.CatalogEntry) string {
	switch {
	case e.Experimental:
		return fmt.Sprintf("Experimental: PaperMC only has %s builds so far. It may crash or damage your world, and a world opened with it cannot go back to an older version.", strings.ToLower(e.Channel))
	case e.Recommended:
		return "Recommended: the newest stable Paper release. Java Edition " + e.MinecraftVersion + " clients can join."
	case !e.Supported:
		return "Older version that PaperMC no longer updates. Choose it for friends or plugins that still need " + e.MinecraftVersion + "."
	}
	return "Stable. Java Edition " + e.MinecraftVersion + " clients can join."
}

// VersionID is the catalog ID for a Minecraft version.
func VersionID(mc string) string { return "paper-" + mc }

// CompareMinecraft compares two Minecraft release versions ("26.2",
// "1.21.11") part by part; a missing part counts as 0.
func CompareMinecraft(a, b string) int { return compareParts(parts(a), parts(b)) }

func parts(v string) []int {
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	return out
}

func compareParts(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// family is a version's first two parts: 26.2 for 26.2.1, 1.21 for 1.21.11.
func family(v string) string {
	p := strings.SplitN(v, ".", 3)
	if len(p) < 2 {
		return v
	}
	return p[0] + "." + p[1]
}

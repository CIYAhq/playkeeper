package minecraft

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// PaperDownloads is PaperMC's download host. It serves each build's jar at
// an address made of the jar's SHA-256 and file name, apart from the Fill
// API, so a build whose checksum Playkeeper knows downloads while the API
// is down.
const PaperDownloads = "https://fill-data.papermc.io"

// PaperJarURL is where PaperDownloads serves a build's jar.
func PaperJarURL(mcVersion string, build int, sha256 string) (string, bool) {
	if !reRelease.MatchString(mcVersion) || build <= 0 || !reSHA256.MatchString(sha256) {
		return "", false
	}
	return fmt.Sprintf("%s/v1/objects/%s/paper-%s-%d.jar", PaperDownloads, sha256, mcVersion, build), true
}

// BuiltInPaper is Paper's version list as Playkeeper was built, for a
// machine with no list of its own while the Fill API can't be reached:
// every release from OldestRelease on that the image's Java runs, with the
// builds a catalog or a restore picks from it, as PaperMC listed them at
// MadeAt. cmd/version-lists writes it (make version-lists).
type BuiltInPaper struct {
	MadeAt   time.Time             `json:"madeAt"`
	Versions []BuiltInPaperVersion `json:"versions"`
}

type BuiltInPaperVersion struct {
	ID      string              `json:"id"`
	Support string              `json:"support"`
	Java    int                 `json:"java"`
	Builds  []BuiltInPaperBuild `json:"builds"`
}

type BuiltInPaperBuild struct {
	ID      int    `json:"id"`
	Channel string `json:"channel"`
	SHA256  string `json:"sha256"`
}

//go:embed builtin/paper.json
var builtinPaperJSON []byte

var builtinPaper = sync.OnceValues(func() (BuiltInPaper, error) { return ParseBuiltInPaper(builtinPaperJSON) })

// ParseBuiltInPaper reads a list cmd/version-lists wrote, refusing one a
// catalog couldn't offer anything from.
func ParseBuiltInPaper(b []byte) (BuiltInPaper, error) {
	var l BuiltInPaper
	if err := json.Unmarshal(b, &l); err != nil {
		return BuiltInPaper{}, fmt.Errorf("the Paper version list built into Playkeeper can't be read: %w", err)
	}
	if l.MadeAt.IsZero() || len(l.Versions) == 0 {
		return BuiltInPaper{}, errors.New("the Paper version list built into Playkeeper is empty")
	}
	return l, nil
}

// BuiltInCatalog is Catalog from the list built into Playkeeper, and when
// that list was made.
func BuiltInCatalog() ([]api.CatalogEntry, time.Time, error) {
	l, err := builtinPaper()
	if err != nil {
		return nil, time.Time{}, err
	}
	return l.Catalog()
}

// Catalog is what Fill.Catalog offers from this list.
func (l BuiltInPaper) Catalog() ([]api.CatalogEntry, time.Time, error) {
	entries, err := catalogFrom(context.Background(), builtinSource(l))
	return entries, l.MadeAt, err
}

// BuiltInRestoreBuild is RestoreBuild from the list built into Playkeeper,
// and when that list was made.
func BuiltInRestoreBuild(mcVersion string, build int) (api.CatalogEntry, time.Time, error) {
	l, err := builtinPaper()
	if err != nil {
		return api.CatalogEntry{}, time.Time{}, err
	}
	return l.RestoreBuild(mcVersion, build)
}

// RestoreBuild is what Fill.RestoreBuild picks from this list.
func (l BuiltInPaper) RestoreBuild(mcVersion string, build int) (api.CatalogEntry, time.Time, error) {
	e, err := restoreBuildFrom(context.Background(), builtinSource(l), mcVersion, build)
	return e, l.MadeAt, err
}

type builtinSource BuiltInPaper

func (b builtinSource) versions(context.Context) ([]fillVersion, error) {
	out := make([]fillVersion, 0, len(b.Versions))
	for _, v := range b.Versions {
		out = append(out, v.fill())
	}
	return out, nil
}

func (b builtinSource) version(_ context.Context, id string) (fillVersion, error) {
	for _, v := range b.Versions {
		if v.ID == id {
			return v.fill(), nil
		}
	}
	return fillVersion{}, errNoVersion
}

func (b builtinSource) builds(_ context.Context, id string) ([]fillBuild, error) {
	for _, v := range b.Versions {
		if v.ID != id {
			continue
		}
		out := make([]fillBuild, 0, len(v.Builds))
		for _, bb := range v.Builds {
			d := fillDownload{Name: fmt.Sprintf("paper-%s-%d.jar", v.ID, bb.ID)}
			d.Checksums.SHA256 = bb.SHA256
			out = append(out, fillBuild{ID: bb.ID, Channel: bb.Channel, Downloads: map[string]fillDownload{"server:default": d}})
		}
		return out, nil
	}
	return nil, errNoVersion
}

func (v BuiltInPaperVersion) fill() fillVersion {
	var f fillVersion
	f.Version.ID = v.ID
	f.Version.Support.Status = v.Support
	f.Version.Java.Version.Minimum = v.Java
	return f
}

// BuiltInList reads the list to build into Playkeeper from the Fill API:
// each release from OldestRelease on that the image's Java runs, with its
// newest stable build and its newest build when that is newer. Every jar
// must be where PaperJarURL says, or a machine couldn't download it while
// the API is down.
func (f Fill) BuiltInList(ctx context.Context, now time.Time) (BuiltInPaper, error) {
	list, err := f.versions(ctx)
	if err != nil {
		return BuiltInPaper{}, err
	}
	out := BuiltInPaper{MadeAt: now.UTC().Truncate(time.Second)}
	for _, v := range list {
		id := v.Version.ID
		if !reRelease.MatchString(id) || CompareMinecraft(id, OldestRelease) < 0 || v.Version.Java.Version.Minimum > ImageJava {
			continue
		}
		builds, err := f.builds(ctx, id)
		if err != nil {
			return BuiltInPaper{}, err
		}
		var stable, latest *fillBuild
		for i := range builds {
			b := &builds[i]
			d, ok := b.Downloads["server:default"]
			if !ok || !reSHA256.MatchString(d.Checksums.SHA256) {
				continue
			}
			if want, _ := PaperJarURL(id, b.ID, d.Checksums.SHA256); d.URL != want {
				return BuiltInPaper{}, fmt.Errorf("PaperMC serves Paper %s build %d at %q, not %q: PaperJarURL needs updating", id, b.ID, d.URL, want)
			}
			if latest == nil || b.ID > latest.ID {
				latest = b
			}
			if stableChannel(b.Channel) && (stable == nil || b.ID > stable.ID) {
				stable = b
			}
		}
		bv := BuiltInPaperVersion{ID: id, Support: v.Version.Support.Status, Java: v.Version.Java.Version.Minimum}
		for _, b := range []*fillBuild{latest, stable} {
			if b != nil && !slices.ContainsFunc(bv.Builds, func(x BuiltInPaperBuild) bool { return x.ID == b.ID }) {
				bv.Builds = append(bv.Builds, BuiltInPaperBuild{ID: b.ID, Channel: b.Channel, SHA256: b.Downloads["server:default"].Checksums.SHA256})
			}
		}
		if len(bv.Builds) > 0 {
			out.Versions = append(out.Versions, bv)
		}
	}
	slices.SortStableFunc(out.Versions, func(a, b BuiltInPaperVersion) int { return CompareMinecraft(b.ID, a.ID) })
	if _, _, err := out.Catalog(); err != nil {
		return BuiltInPaper{}, err
	}
	return out, nil
}

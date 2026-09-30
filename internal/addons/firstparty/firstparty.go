// Package firstparty is the add-ons Playkeeper makes itself and ships inside
// its binary, for its templates: plugins no public registry lists, like AI
// Build Battle's. Each is one entry in the registry below and one jar beside
// this file, embedded when Playkeeper is built. A jar's version comes from its
// plugin.yml, and its SHA-256 and size from its bytes, so a new build of a
// plugin replaces the jar under the same name with no change to the code.
//
// The add-on library installs them as the source "playkeeper"
// (addons.Playkeeper) with no network request, and templates list them pinned
// to the version the binary carries.
package firstparty

import (
	"archive/zip"
	"bytes"
	"cmp"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

//go:embed *.jar
var jars embed.FS

// registry is every first-party add-on.
var registry = []*Plugin{
	{
		ID: "ai-build-battle", Slug: "ai-build-battle", Name: "AI Build Battle",
		Summary: "Type /aibuild and an AI model builds it in front of you, block by block.",
		License: "AGPL-3.0-only", Author: "Playkeeper", Types: []string{"paper", "purpur"},
		Published: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC),
		jar:       "ai-build-battle.jar",
	},
}

// Plugin is one first-party add-on, as the registry records it.
type Plugin struct {
	// ID is its project id and Slug its slug, as templates list them.
	ID, Slug string
	Name     string
	Summary  string
	// License is its licence's SPDX id.
	License string
	Author  string
	// Types are the server types it runs on.
	Types []string
	// Published is the day the registry records for it, which every build
	// carries, so builds are told apart by their version (Newer).
	Published time.Time
	// jar is its file among the embedded jars.
	jar string

	once sync.Once
	file Jar
	err  error
}

// Jar is the build of a plugin the binary carries.
type Jar struct {
	// Version is the version its plugin.yml gives.
	Version string
	// FileName is the name it's installed under: <slug>-<version>.jar.
	FileName string
	// SHA256 is its hash, lowercase hex.
	SHA256 string
	Size   int64
	data   []byte
}

// Open reads the jar.
func (j Jar) Open() io.Reader { return bytes.NewReader(j.data) }

// Plugins lists the first-party add-ons.
func Plugins() []*Plugin { return slices.Clone(registry) }

// Lookup is the first-party add-on with this project id, or nil.
func Lookup(id string) *Plugin {
	for _, p := range registry {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// ByHash is the first-party add-on whose embedded jar has this SHA-256, and
// that jar; nil when none has.
func ByHash(sum string) (*Plugin, Jar) {
	for _, p := range registry {
		if j, err := p.Jar(); err == nil && j.SHA256 == strings.ToLower(sum) {
			return p, j
		}
	}
	return nil, Jar{}
}

// RunsOn reports whether the plugin runs on servers of this type.
func (p *Plugin) RunsOn(serverType string) bool { return slices.Contains(p.Types, serverType) }

// Jar reads the plugin's embedded jar the first time it's asked for. An
// error means the binary carries no usable build of it (the package's tests
// make sure it does), and the plugin can't be installed.
func (p *Plugin) Jar() (Jar, error) {
	p.once.Do(func() {
		b, err := jars.ReadFile(p.jar)
		if err != nil {
			p.err = err
			return
		}
		p.file, p.err = parseJar(p.Slug, b)
		if p.err != nil {
			p.err = fmt.Errorf("%s: %w", p.jar, p.err)
		}
	})
	return p.file, p.err
}

// maxMeta is the most of a plugin.yml read.
const maxMeta = 256 << 10

// reVersion is a version that can be a version id, as the add-on library
// takes them, and part of a file name.
var reVersion = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$`)

// parseJar reads the version of a plugin jar from its plugin.yml, or
// paper-plugin.yml, and hashes it.
func parseJar(slug string, b []byte) (Jar, error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return Jar{}, err
	}
	var name, version string
	for _, file := range []string{"paper-plugin.yml", "plugin.yml"} {
		f := slices.IndexFunc(zr.File, func(f *zip.File) bool { return f.Name == file })
		if f < 0 {
			continue
		}
		rc, err := zr.File[f].Open()
		if err != nil {
			return Jar{}, err
		}
		meta, err := io.ReadAll(io.LimitReader(rc, maxMeta))
		rc.Close()
		if err != nil {
			return Jar{}, err
		}
		meta = bytes.TrimPrefix(meta, []byte("\ufeff"))
		if name = yamlTop(meta, "name"); name != "" {
			version = yamlTop(meta, "version")
			break
		}
	}
	switch {
	case name == "":
		return Jar{}, errors.New("it has no plugin.yml with a name")
	case !reVersion.MatchString(version):
		return Jar{}, fmt.Errorf("its plugin.yml has no usable version (%q)", version)
	}
	sum := sha256.Sum256(b)
	return Jar{Version: version, FileName: slug + "-" + version + ".jar", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(b)), data: b}, nil
}

// yamlTop reads a top-level scalar from a plugin.yml without a YAML parser.
func yamlTop(b []byte, key string) string {
	for line := range strings.Lines(string(b)) {
		rest, ok := strings.CutPrefix(line, key+":")
		if !ok {
			continue
		}
		v := strings.TrimSpace(rest)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
			if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
				return v[1 : end+1]
			}
		}
		if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		return v
	}
	return ""
}

// Newer reports whether version a comes after b, part by part with numbers
// as numbers: 0.10.0 comes after 0.9.2, and 1.0.0 after 1.0.0-beta.
func Newer(a, b string) bool { return compareVersions(a, b) > 0 }

func compareVersions(a, b string) int {
	for a != "" || b != "" {
		var x, y string
		x, a, _ = strings.Cut(a, ".")
		y, b, _ = strings.Cut(b, ".")
		if c := comparePart(x, y); c != 0 {
			return c
		}
	}
	return 0
}

func comparePart(x, y string) int {
	nx, rx := splitNumber(x)
	ny, ry := splitNumber(y)
	if c := cmp.Or(cmp.Compare(len(nx), len(ny)), strings.Compare(nx, ny)); c != 0 {
		return c
	}
	switch {
	case rx == ry:
		return 0
	case rx == "":
		return 1
	case ry == "":
		return -1
	}
	return strings.Compare(rx, ry)
}

func splitNumber(s string) (number, rest string) {
	i := len(s) - len(strings.TrimLeft(s, "0123456789"))
	return strings.TrimLeft(s[:i], "0"), s[i:]
}

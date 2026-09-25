package addons

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
)

// JarMeta is what a plugin or mod jar says about itself.
type JarMeta struct {
	// ID is the plugin's name (Bukkit and Paper, also its settings folder
	// under plugins/) or the mod's id (Fabric, Quilt, NeoForge).
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	Kind    string `json:"kind,omitempty"` // plugin or mod
}

const (
	maxJarSize  = 1 << 30
	maxMetaSize = 256 << 10
)

var (
	tomlModID   = regexp.MustCompile(`(?m)^\s*modId\s*=\s*"([^"]{1,128})"`)
	tomlVersion = regexp.MustCompile(`(?m)^\s*version\s*=\s*"([^"]{1,128})"`)
	tomlName    = regexp.MustCompile(`(?m)^\s*displayName\s*=\s*"([^"]{1,128})"`)
)

// readJarMeta reads the descriptor of a plugin or mod jar. Anything it
// cannot read gives an empty JarMeta; it never extracts files.
func readJarMeta(r io.ReaderAt, size int64) JarMeta {
	if size <= 0 || size > maxJarSize {
		return JarMeta{}
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return JarMeta{}
	}
	entries := map[string]*zip.File{}
	for _, f := range zr.File {
		switch f.Name {
		case "paper-plugin.yml", "plugin.yml", "fabric.mod.json", "quilt.mod.json", "META-INF/neoforge.mods.toml", "META-INF/mods.toml":
			if entries[f.Name] == nil {
				entries[f.Name] = f
			}
		}
	}
	read := func(name string) []byte {
		f := entries[name]
		if f == nil {
			return nil
		}
		rc, err := f.Open()
		if err != nil {
			return nil
		}
		defer rc.Close()
		b, _ := io.ReadAll(io.LimitReader(rc, maxMetaSize))
		return []byte(strings.TrimPrefix(string(b), "\ufeff"))
	}

	for _, name := range []string{"paper-plugin.yml", "plugin.yml"} {
		if b := read(name); b != nil {
			if id := yamlTop(b, "name"); id != "" {
				return JarMeta{ID: id, Version: yamlTop(b, "version"), Kind: "plugin"}
			}
		}
	}
	if b := read("fabric.mod.json"); b != nil {
		var m struct{ ID, Name, Version string }
		if json.Unmarshal(b, &m) == nil && m.ID != "" {
			return JarMeta{ID: m.ID, Name: m.Name, Version: m.Version, Kind: "mod"}
		}
	}
	if b := read("quilt.mod.json"); b != nil {
		var m struct {
			Loader struct {
				ID       string `json:"id"`
				Version  string `json:"version"`
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			} `json:"quilt_loader"`
		}
		if json.Unmarshal(b, &m) == nil && m.Loader.ID != "" {
			return JarMeta{ID: m.Loader.ID, Name: m.Loader.Metadata.Name, Version: m.Loader.Version, Kind: "mod"}
		}
	}
	for _, name := range []string{"META-INF/neoforge.mods.toml", "META-INF/mods.toml"} {
		b := read(name)
		m := tomlModID.FindSubmatch(b)
		if m == nil {
			continue
		}
		meta := JarMeta{ID: string(m[1]), Kind: "mod"}
		if v := tomlVersion.FindSubmatch(b); v != nil && !strings.HasPrefix(string(v[1]), "${") {
			meta.Version = string(v[1])
		}
		if n := tomlName.FindSubmatch(b); n != nil {
			meta.Name = string(n[1])
		}
		return meta
	}
	return JarMeta{}
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
		if len(v) > 128 {
			return ""
		}
		return v
	}
	return ""
}

func metaOf(path string) JarMeta {
	f, err := os.Open(path)
	if err != nil {
		return JarMeta{}
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return JarMeta{}
	}
	return readJarMeta(f, fi.Size())
}

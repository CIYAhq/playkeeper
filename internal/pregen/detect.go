package pregen

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
	"syscall"
)

// Installed is the Chunky jar found on a server.
type Installed struct {
	// File is the jar's path relative to the data directory.
	File string `json:"file"`
	// Version is the version the jar declares, such as "1.5.3".
	Version string `json:"version"`
}

const (
	maxJars     = 1000
	maxJarBytes = 512 << 20
	maxMetaSize = 1 << 20
)

// Detect looks for Chunky among the server's plugins (Bukkit) or mods
// (Fabric, NeoForge) by reading the metadata inside each jar, so a renamed
// jar is still found. A server without it gets ErrNotInstalled.
func Detect(dataDir string, p Platform) (Installed, error) {
	dir := "mods"
	if p == Bukkit {
		dir = "plugins"
	}
	if !p.valid() {
		return Installed{}, fmt.Errorf("unknown platform %q", p)
	}
	notInstalled := &Error{Code: CodeNotInstalled, Msg: "Chunky is not installed on this server.", Hint: "Install Chunky from the add-ons page and restart the server."}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return Installed{}, err
	}
	defer root.Close()
	d, err := root.Open(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return Installed{}, notInstalled
	}
	if err != nil {
		return Installed{}, err
	}
	entries, err := d.ReadDir(maxJars + 1)
	d.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return Installed{}, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(strings.ToLower(e.Name()), ".jar") {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	for _, name := range names[:min(len(names), maxJars)] {
		if v, ok := chunkyVersion(root, dir+"/"+name, p); ok {
			return Installed{File: dir + "/" + name, Version: v}, nil
		}
	}
	return Installed{}, notInstalled
}

// chunkyVersion reads the jar's plugin or mod metadata and returns its
// version if the jar is Chunky. Unreadable jars are not Chunky.
func chunkyVersion(root *os.Root, name string, p Platform) (string, bool) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxJarBytes {
		return "", false
	}
	zr, err := zip.NewReader(f, st.Size())
	if err != nil {
		return "", false
	}
	read := func(entry string) ([]byte, bool) {
		for _, zf := range zr.File {
			if zf.Name != entry || zf.UncompressedSize64 > maxMetaSize {
				continue
			}
			rc, err := zf.Open()
			if err != nil {
				return nil, false
			}
			defer rc.Close()
			b, err := io.ReadAll(io.LimitReader(rc, maxMetaSize+1))
			if err != nil || len(b) > maxMetaSize {
				return nil, false
			}
			return b, true
		}
		return nil, false
	}
	switch p {
	case Bukkit:
		for _, entry := range []string{"paper-plugin.yml", "plugin.yml"} {
			if b, ok := read(entry); ok {
				m := yamlTopLevel(string(b))
				return m["version"], strings.EqualFold(m["name"], "Chunky")
			}
		}
	case Fabric:
		if b, ok := read("fabric.mod.json"); ok {
			var meta struct {
				ID      string `json:"id"`
				Version string `json:"version"`
			}
			if json.Unmarshal(b, &meta) == nil && meta.ID == "chunky" {
				return meta.Version, true
			}
		}
	case NeoForge:
		if b, ok := read("META-INF/neoforge.mods.toml"); ok {
			return tomlMod(string(b), "chunky")
		}
	}
	return "", false
}

// yamlTopLevel reads the unindented "key: value" lines of a plugin.yml.
func yamlTopLevel(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimRight(line, "\r"), ":")
		if !ok {
			continue
		}
		if i := strings.Index(v, " #"); i >= 0 {
			v = v[:i]
		}
		out[strings.TrimSpace(k)] = unquote(strings.TrimSpace(v))
	}
	return out
}

var reTOMLKey = regexp.MustCompile(`^\s*([A-Za-z]+)\s*=\s*("[^"]*"|'[^']*')`)

// tomlMod finds the [[mods]] table with modId id in a neoforge.mods.toml
// and returns its version.
func tomlMod(s, id string) (string, bool) {
	inMods, found, version := false, false, ""
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			if found {
				break
			}
			inMods, version = t == "[[mods]]", ""
			continue
		}
		m := reTOMLKey.FindStringSubmatch(line)
		if !inMods || m == nil {
			continue
		}
		switch m[1] {
		case "modId":
			found = unquote(m[2]) == id
		case "version":
			version = unquote(m[2])
		}
	}
	return version, found
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

package curseforge

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/modpacks/mrpack"
)

// ManifestName is the manifest at the root of a CurseForge modpack's zip.
const ManifestName = "manifest.json"

// Manifest is a modpack's manifest.json.
type Manifest struct {
	Minecraft       ManifestMinecraft `json:"minecraft"`
	ManifestType    string            `json:"manifestType"`
	ManifestVersion int               `json:"manifestVersion"`
	Name            string            `json:"name"`
	Version         string            `json:"version"`
	Author          string            `json:"author"`
	Files           []ManifestFile    `json:"files"`
	// Overrides is the folder in the zip copied into the server's folder.
	Overrides string `json:"overrides"`
}

type ManifestMinecraft struct {
	Version    string      `json:"version"`
	ModLoaders []ModLoader `json:"modLoaders"`
}

// ModLoader is a loader id such as "fabric-0.19.5" or "neoforge-21.1.77".
type ModLoader struct {
	ID      string `json:"id"`
	Primary bool   `json:"primary"`
}

// ManifestFile is one project file the pack uses. Required false means the
// pack ships it switched off.
type ManifestFile struct {
	ProjectID int64 `json:"projectID"`
	FileID    int64 `json:"fileID"`
	Required  bool  `json:"required"`
}

// ManifestError is a manifest Playkeeper cannot use.
type ManifestError struct{ Reason string }

func (e *ManifestError) Error() string { return "the pack's manifest.json " + e.Reason }

// ParseManifest decodes and checks a manifest.json.
func ParseManifest(b []byte) (*Manifest, error) {
	if !utf8.Valid(b) {
		return nil, &ManifestError{Reason: "is not UTF-8 text"}
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, &ManifestError{Reason: "is not valid JSON (" + err.Error() + ")"}
	}
	if m.Overrides == "" {
		m.Overrides = "overrides"
	}
	switch {
	case m.ManifestType != "minecraftModpack":
		return nil, &ManifestError{Reason: fmt.Sprintf("has manifestType %q, not a Minecraft modpack", m.ManifestType)}
	case m.ManifestVersion != 1:
		return nil, &ManifestError{Reason: fmt.Sprintf("uses manifest version %d; Playkeeper reads version 1", m.ManifestVersion)}
	case strings.TrimSpace(m.Name) == "":
		return nil, &ManifestError{Reason: "has no name"}
	case !versionToken(m.Minecraft.Version):
		return nil, &ManifestError{Reason: "does not say which Minecraft version it is for"}
	}
	if err := mrpack.CheckPath(m.Overrides); err != nil {
		return nil, &ManifestError{Reason: "names an unsafe overrides folder: " + err.Error()}
	}
	projects := make(map[int64]bool, len(m.Files))
	for _, f := range m.Files {
		switch {
		case f.ProjectID <= 0 || f.FileID <= 0:
			return nil, &ManifestError{Reason: "lists a file without a project or file id"}
		case projects[f.ProjectID]:
			return nil, &ManifestError{Reason: fmt.Sprintf("lists project %d more than once", f.ProjectID)}
		}
		projects[f.ProjectID] = true
	}
	for _, l := range m.Minecraft.ModLoaders {
		if _, _, err := SplitLoader(l.ID); err != nil {
			return nil, err
		}
	}
	return &m, nil
}

// Loader returns the pack's loader (the primary one when it lists several)
// as a name such as "fabric" or "neoforge" and a version, or "" for a
// vanilla pack.
func (m *Manifest) Loader() (name, version string, err error) {
	ls := m.Minecraft.ModLoaders
	if len(ls) == 0 {
		return "", "", nil
	}
	pick := ls[0]
	for _, l := range ls {
		if l.Primary {
			pick = l
			break
		}
	}
	return SplitLoader(pick.ID)
}

// SplitLoader splits a loader id: "forge-47.2.0" is forge 47.2.0,
// "neoforge-1.20.1-47.1.99" is neoforge 1.20.1-47.1.99. The name is lower
// case; the caller decides whether it knows it.
func SplitLoader(id string) (name, version string, err error) {
	name, version, ok := strings.Cut(id, "-")
	if !ok || name == "" || !versionToken(version) {
		return "", "", &ManifestError{Reason: fmt.Sprintf("names a loader Playkeeper cannot read (%q)", printable(id))}
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return "", "", &ManifestError{Reason: fmt.Sprintf("names a loader Playkeeper cannot read (%q)", printable(id))}
		}
	}
	return strings.ToLower(name), version, nil
}

// versionToken accepts version strings such as "1.21.1" or "21.1.77-beta".
// They end up in the server's settings, so nothing else is let through.
func versionToken(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '-' || r == '_' || r == '+') {
			return false
		}
	}
	return true
}

func printable(s string) string {
	s = strings.ToValidUTF8(s, "?")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return '?'
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80]) + "…"
	}
	return s
}

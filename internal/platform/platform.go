// Package platform lists the operating systems Playkeeper supports and
// decides how the installer treats the one it runs on. Each supported
// release is installed on, played on and uninstalled from in a fresh copy of
// its official cloud image before every release
// (.github/workflows/os-matrix.yml). A release of a supported distribution
// that came out later is newer than every tested one: the installer warns
// and continues, so a new Ubuntu or Debian works before the list names it.
package platform

import (
	"os"
	"strconv"
	"strings"
)

// Release is a supported release of a distribution.
type Release struct {
	// Distro and Version are its os-release ID and VERSION_ID, like
	// "ubuntu" and "24.04".
	Distro, Version string
	// Name is what people call it, like "Ubuntu 24.04 LTS".
	Name string
	// SecurityEnded, when set, is when its free security updates ended,
	// like "May 2025". Playkeeper still runs on it, and the installer says
	// that the system itself no longer gets fixes.
	SecurityEnded string
}

// Supported lists the supported releases, each distribution's oldest first.
var Supported = []Release{
	{Distro: "ubuntu", Version: "20.04", Name: "Ubuntu 20.04 LTS", SecurityEnded: "May 2025"},
	{Distro: "ubuntu", Version: "22.04", Name: "Ubuntu 22.04 LTS"},
	{Distro: "ubuntu", Version: "24.04", Name: "Ubuntu 24.04 LTS"},
	{Distro: "ubuntu", Version: "26.04", Name: "Ubuntu 26.04 LTS"},
	{Distro: "debian", Version: "11", Name: "Debian 11", SecurityEnded: "August 2026"},
	{Distro: "debian", Version: "12", Name: "Debian 12"},
	{Distro: "debian", Version: "13", Name: "Debian 13"},
}

// Distro is a supported distribution with its supported releases, oldest
// first.
type Distro struct {
	ID, Name string
	Releases []Release
}

// Oldest and Newest are the distribution's oldest and newest supported
// releases.
func (d Distro) Oldest() Release { return d.Releases[0] }
func (d Distro) Newest() Release { return d.Releases[len(d.Releases)-1] }

var distroNames = map[string]string{"ubuntu": "Ubuntu", "debian": "Debian"}

// Distros lists the supported distributions in the order Supported first
// names them.
func Distros() []Distro {
	var out []Distro
	for _, r := range Supported {
		i := indexOf(out, r.Distro)
		if i < 0 {
			out = append(out, Distro{ID: r.Distro, Name: distroNames[r.Distro]})
			i = len(out) - 1
		}
		out[i].Releases = append(out[i].Releases, r)
	}
	return out
}

func indexOf(ds []Distro, id string) int {
	for i, d := range ds {
		if d.ID == id {
			return i
		}
	}
	return -1
}

// Summary names what Playkeeper runs on in one phrase: "Ubuntu 20.04 or
// later, or Debian 11 or later".
func Summary() string {
	var parts []string
	for _, d := range Distros() {
		parts = append(parts, d.Name+" "+d.Oldest().Version+" or later")
	}
	return joinOr(parts)
}

// joinOr lists items as "a, b, or c", or "a, or b" for two, so that each
// item's own "or later" stays readable.
func joinOr(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + ", or " + items[len(items)-1]
}

// Short is Summary for a line with little room: "Ubuntu 20.04+ or Debian
// 11+".
func Short() string {
	var parts []string
	for _, d := range Distros() {
		parts = append(parts, d.Name+" "+d.Oldest().Version+"+")
	}
	if len(parts) < 2 {
		return strings.Join(parts, "")
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " or " + parts[len(parts)-1]
}

// OS is what /etc/os-release says about a system.
type OS struct {
	ID, VersionID, Name, PrettyName, Codename string
}

// ReadOS reads an os-release file; a missing one gives an empty OS.
func ReadOS(path string) OS {
	b, err := os.ReadFile(path)
	if err != nil {
		return OS{}
	}
	return ParseOS(string(b))
}

// ParseOS parses the contents of an os-release file.
func ParseOS(s string) OS {
	var o OS
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			o.ID = v
		case "VERSION_ID":
			o.VersionID = v
		case "NAME":
			o.Name = v
		case "PRETTY_NAME":
			o.PrettyName = v
		case "VERSION_CODENAME":
			o.Codename = v
		}
	}
	return o
}

// DistroName is the distribution's short name, like "Debian" for Debian
// GNU/Linux, or "" when os-release names none.
func (o OS) DistroName() string {
	if n, ok := distroNames[o.ID]; ok {
		return n
	}
	return strings.TrimSuffix(o.Name, " GNU/Linux")
}

// Display names the system and its version for people, like "Debian 12",
// or "Debian forky/sid" for a release without a version number.
func (o OS) Display() string {
	switch {
	case o.DistroName() != "" && o.VersionID != "":
		return o.DistroName() + " " + o.VersionID
	case o.PrettyName != "":
		return strings.Replace(o.PrettyName, " GNU/Linux", "", 1)
	case o.DistroName() != "":
		return o.DistroName()
	case o.ID != "":
		return o.ID
	}
	return "an unknown system"
}

// Support is how far Playkeeper supports a system.
type Support int

const (
	// Unsupported is another distribution, or a release older than its
	// distribution's oldest supported one.
	Unsupported Support = iota
	// Tested is a supported release.
	Tested
	// Newer is a release of a supported distribution that Supported doesn't
	// name but that is newer than its oldest supported release: one that
	// came out after this version of Playkeeper, an interim Ubuntu, or
	// Debian testing. It should work.
	Newer
)

// Verdict is what Check found.
type Verdict struct {
	Support Support
	// Distro is the system's distribution when it is supported.
	Distro *Distro
	// Release is the supported release the system is (Tested), or the
	// newest supported release older than it (Newer).
	Release Release
}

// Check says how Playkeeper supports the system o describes.
func Check(o OS) Verdict {
	var d *Distro
	for _, x := range Distros() {
		if x.ID == o.ID {
			d = &x
			break
		}
	}
	if d == nil {
		return Verdict{Support: Unsupported}
	}
	v := Verdict{Distro: d}
	if o.VersionID == "" {
		// Debian testing and unstable have no version number yet.
		v.Support, v.Release = Newer, d.Newest()
		return v
	}
	for _, r := range d.Releases {
		switch c := compareVersions(o.VersionID, r.Version); {
		case c == 0:
			v.Support, v.Release = Tested, r
			return v
		case c > 0:
			v.Support, v.Release = Newer, r
		}
	}
	return v
}

// compareVersions compares dotted release numbers such as "24.04" and
// "26.04" part by part. A part that isn't a number sorts above every
// number: it names a release still in development.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		x, y := part(pa, i), part(pb, i)
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func part(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	n, err := strconv.Atoi(parts[i])
	if err != nil {
		return int(^uint(0) >> 1)
	}
	return n
}

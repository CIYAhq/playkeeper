// Package platform lists the operating systems Playkeeper supports and
// decides how the installer treats the one it runs on. Each supported
// release is installed on, played on and uninstalled from in a fresh copy of
// its official cloud image before every release
// (.github/workflows/os-matrix.yml), except the few the installer accepts as
// their family (Release.Untested). A release of a supported distribution
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
	// SecurityEnded, when set, is when its standard security updates ended,
	// like "May 2025". Playkeeper still runs on it, and the installer says
	// that the system itself no longer gets them.
	SecurityEnded string
	// Untested marks a release the OS matrix doesn't boot that the installer
	// accepts as its family: RHEL, which AlmaLinux, Rocky Linux and Oracle
	// Linux rebuild (its images need a subscription), and CentOS Stream 10,
	// where RHEL 10 is developed.
	Untested bool
}

// Supported lists the supported releases, each distribution's oldest first.
// Debian 11 isn't one: since its long-term support ended in August 2026,
// Debian's archive no longer has the docker.io and containerd builds its
// package lists point to, so Docker can't be installed from it. A version
// without a dot stands for every minor release of it: 9 is 9.6 too.
var Supported = []Release{
	{Distro: "ubuntu", Version: "20.04", Name: "Ubuntu 20.04 LTS", SecurityEnded: "May 2025"},
	{Distro: "ubuntu", Version: "22.04", Name: "Ubuntu 22.04 LTS"},
	{Distro: "ubuntu", Version: "24.04", Name: "Ubuntu 24.04 LTS"},
	{Distro: "ubuntu", Version: "26.04", Name: "Ubuntu 26.04 LTS"},
	{Distro: "debian", Version: "12", Name: "Debian 12"},
	{Distro: "debian", Version: "13", Name: "Debian 13"},
	{Distro: "almalinux", Version: "9", Name: "AlmaLinux 9"},
	{Distro: "almalinux", Version: "10", Name: "AlmaLinux 10"},
	{Distro: "rocky", Version: "9", Name: "Rocky Linux 9"},
	{Distro: "rocky", Version: "10", Name: "Rocky Linux 10"},
	{Distro: "ol", Version: "9", Name: "Oracle Linux 9"},
	{Distro: "rhel", Version: "9", Name: "RHEL 9", Untested: true},
	{Distro: "rhel", Version: "10", Name: "RHEL 10", Untested: true},
	{Distro: "centos", Version: "9", Name: "CentOS Stream 9"},
	{Distro: "centos", Version: "10", Name: "CentOS Stream 10", Untested: true},
	{Distro: "amzn", Version: "2023", Name: "Amazon Linux 2023"},
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

var distroNames = map[string]string{
	"ubuntu": "Ubuntu", "debian": "Debian",
	"almalinux": "AlmaLinux", "rocky": "Rocky Linux", "ol": "Oracle Linux", "rhel": "RHEL", "centos": "CentOS Stream", "amzn": "Amazon Linux",
}

// familyNames group distributions for a short list: AlmaLinux, Rocky Linux,
// Oracle Linux and CentOS Stream are RHEL, rebuilt or ahead of it.
var familyNames = map[string]string{"almalinux": "the RHEL family", "rocky": "the RHEL family", "ol": "the RHEL family", "rhel": "the RHEL family", "centos": "the RHEL family"}

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

// A Group is one item of a short list of the supported systems: a
// distribution, or a family of them with its members' names.
type Group struct {
	Name, Version string
	Members       []string
}

// Groups lists the supported distributions with each family as one item,
// in the order Supported first names them. A family's version is its
// members' oldest.
func Groups() []Group {
	var out []Group
	for _, d := range Distros() {
		fam := familyNames[d.ID]
		if n := len(out); fam != "" && n > 0 && out[n-1].Name == fam {
			out[n-1].Members = append(out[n-1].Members, d.Name)
			if compareVersions(d.Oldest().Version, out[n-1].Version) < 0 {
				out[n-1].Version = d.Oldest().Version
			}
			continue
		}
		g := Group{Name: d.Name, Version: d.Oldest().Version}
		if fam != "" {
			g = Group{Name: fam, Version: d.Oldest().Version, Members: []string{d.Name}}
		}
		out = append(out, g)
	}
	return out
}

// Summary names what Playkeeper runs on in one phrase: "Ubuntu 20.04 or
// later, Debian 12 or later, the RHEL family 9 or later (AlmaLinux, …), or
// Amazon Linux 2023 or later".
func Summary() string {
	var parts []string
	for _, g := range Groups() {
		part := g.Name + " " + g.Version + " or later"
		if len(g.Members) > 0 {
			part += " (" + joinAnd(g.Members) + ")"
		}
		parts = append(parts, part)
	}
	return joinOr(parts)
}

func joinAnd(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// joinOr lists items as "a, b, or c", or "a, or b" for two, so that each
// item's own "or later" stays readable.
func joinOr(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + ", or " + items[len(items)-1]
}

// Short is Summary for a line with little room: "Ubuntu 20.04+, Debian
// 12+, the RHEL family 9+ or Amazon Linux 2023+".
func Short() string {
	var parts []string
	for _, g := range Groups() {
		parts = append(parts, g.Name+" "+g.Version+"+")
	}
	if len(parts) < 2 {
		return strings.Join(parts, "")
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " or " + parts[len(parts)-1]
}

// OS is what /etc/os-release says about a system. IDLike is its ID_LIKE,
// the distributions it is like: "rhel centos fedora".
type OS struct {
	ID, VersionID, Name, PrettyName, Codename, IDLike string
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
		case "ID_LIKE":
			o.IDLike = v
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
	// Family is a supported release the OS matrix doesn't boot, accepted as
	// its family (Release.Untested).
	Family
)

// Verdict is what Check found.
type Verdict struct {
	Support Support
	// Distro is the system's distribution when it is supported.
	Distro *Distro
	// Release is the supported release the system is (Tested or Family), or
	// the newest supported release older than it (Newer).
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
		version := o.VersionID
		if !strings.Contains(r.Version, ".") {
			version, _, _ = strings.Cut(version, ".")
		}
		switch c := compareVersions(version, r.Version); {
		case c == 0 && r.Untested:
			v.Support, v.Release = Family, r
			return v
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

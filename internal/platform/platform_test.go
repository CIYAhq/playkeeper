package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	ubuntu2604 = `PRETTY_NAME="Ubuntu 26.04 LTS"
NAME="Ubuntu"
VERSION_ID="26.04"
VERSION="26.04 LTS (Resolute Raccoon)"
VERSION_CODENAME=resolute
ID=ubuntu
ID_LIKE=debian
`
	debian12 = `PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"
NAME="Debian GNU/Linux"
VERSION_ID="12"
VERSION="12 (bookworm)"
VERSION_CODENAME=bookworm
ID=debian
`
	debianSid = `PRETTY_NAME="Debian GNU/Linux forky/sid"
NAME="Debian GNU/Linux"
VERSION_CODENAME=forky
ID=debian
`
)

func TestParseOSReadsTheFieldsTheInstallerUses(t *testing.T) {
	o := ParseOS(ubuntu2604)
	if o.ID != "ubuntu" || o.VersionID != "26.04" || o.Codename != "resolute" || o.Display() != "Ubuntu 26.04" {
		t.Errorf("Ubuntu 26.04: %+v, shown as %q", o, o.Display())
	}
	if d := ParseOS(debian12).Display(); d != "Debian 12" {
		t.Errorf("Debian 12 is shown as %q", d)
	}
	if d := ParseOS(debianSid).Display(); d != "Debian forky/sid" {
		t.Errorf("Debian unstable is shown as %q", d)
	}
	if d := ParseOS("NAME='Fedora Linux'\nID=fedora\nVERSION_ID=42\n").Display(); d != "Fedora Linux 42" {
		t.Errorf("Fedora is shown as %q", d)
	}
	if d := (OS{}).Display(); d != "an unknown system" {
		t.Errorf("a system without os-release is shown as %q", d)
	}
	path := filepath.Join(t.TempDir(), "os-release")
	os.WriteFile(path, []byte(debian12), 0o644)
	if o := ReadOS(path); o.ID != "debian" || o.VersionID != "12" {
		t.Errorf("ReadOS: %+v", o)
	}
	if o := ReadOS(filepath.Join(t.TempDir(), "missing")); o != (OS{}) {
		t.Errorf("a missing os-release: %+v", o)
	}
}

func TestEverySupportedReleaseIsTested(t *testing.T) {
	for _, r := range Supported {
		v := Check(OS{ID: r.Distro, VersionID: r.Version})
		if v.Support != Tested || v.Release != r {
			t.Errorf("%s: %+v", r.Name, v)
		}
	}
}

func TestLaterReleasesOfASupportedDistributionAreNewerNotRefused(t *testing.T) {
	cases := []struct {
		os      OS
		release string // the supported release the verdict names
	}{
		{OS{ID: "ubuntu", VersionID: "26.10"}, "26.04"},
		{OS{ID: "ubuntu", VersionID: "28.04"}, "26.04"},
		{OS{ID: "ubuntu", VersionID: "25.10"}, "24.04"},
		{OS{ID: "ubuntu", VersionID: "21.04"}, "20.04"},
		{OS{ID: "debian", VersionID: "14"}, "13"},
		{ParseOS(debianSid), "13"},
	}
	for _, c := range cases {
		v := Check(c.os)
		if v.Support != Newer || v.Release.Version != c.release || v.Distro == nil {
			t.Errorf("%s: %+v, want newer than %s", c.os.Display(), v, c.release)
		}
	}
}

func TestOlderReleasesAndOtherSystemsAreUnsupported(t *testing.T) {
	for _, o := range []OS{{ID: "ubuntu", VersionID: "18.04"}, {ID: "debian", VersionID: "11"}} {
		if v := Check(o); v.Support != Unsupported || v.Distro == nil || v.Distro.ID != o.ID {
			t.Errorf("%s: %+v, want unsupported, naming its distribution", o.Display(), v)
		}
	}
	for _, o := range []OS{{ID: "alpine", VersionID: "3.20"}, {}} {
		if v := Check(o); v.Support != Unsupported || v.Distro != nil {
			t.Errorf("%s: %+v, want unsupported", o.Display(), v)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"24.04", "24.04", 0}, {"24.04", "26.04", -1}, {"26.10", "26.04", 1}, {"13", "12", 1},
		{"12", "12.0", 0}, {"9", "10", -1}, {"rolling", "26.04", 1},
	} {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestSummaryNamesEachDistributionsOldestRelease(t *testing.T) {
	if got, want := Summary(), "Ubuntu 20.04 or later, or Debian 12 or later"; got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
	if got, want := Short(), "Ubuntu 20.04+ or Debian 12+"; got != want {
		t.Errorf("Short() = %q, want %q", got, want)
	}
	for _, d := range Distros() {
		for i := 1; i < len(d.Releases); i++ {
			if compareVersions(d.Releases[i-1].Version, d.Releases[i].Version) >= 0 {
				t.Errorf("%s: %s is listed before %s", d.Name, d.Releases[i-1].Version, d.Releases[i].Version)
			}
		}
		if d.Name == "" {
			t.Errorf("%s has no name", d.ID)
		}
	}
}

// TestEveryClaimNamesTheSupportedSystems keeps what the README, the notes in
// the release tarball and the release notes say Playkeeper runs on the same
// as Supported.
func TestEveryClaimNamesTheSupportedSystems(t *testing.T) {
	for _, f := range []string{"README.md", "packaging/README-INSTALL.txt", "scripts/release-notes.sh"} {
		b, err := os.ReadFile(filepath.Join("..", "..", f))
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(strings.Fields(string(b)), " ")
		if !strings.Contains(text, Summary()) {
			t.Errorf("%s doesn't say %q", f, Summary())
		}
		for _, stale := range []string{"only tested on Ubuntu", "Ubuntu 24.04 LTS, x86_64", "On an Ubuntu 24.04"} {
			if strings.Contains(text, stale) {
				t.Errorf("%s still says %q", f, stale)
			}
		}
	}
}

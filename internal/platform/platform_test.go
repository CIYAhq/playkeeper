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
	alma := ParseOS("NAME=\"AlmaLinux\"\nVERSION=\"9.6 (Sage Margay)\"\nID=\"almalinux\"\nID_LIKE=\"rhel centos fedora\"\nVERSION_ID=\"9.6\"\n")
	if alma.IDLike != "rhel centos fedora" || alma.Display() != "AlmaLinux 9.6" {
		t.Errorf("AlmaLinux 9.6: %+v, shown as %q", alma, alma.Display())
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

func TestEverySupportedReleaseIsTestedOrAcceptedAsItsFamily(t *testing.T) {
	for _, r := range Supported {
		want := Tested
		if r.Untested {
			want = Family
		}
		v := Check(OS{ID: r.Distro, VersionID: r.Version})
		if v.Support != want || v.Release != r {
			t.Errorf("%s: %+v", r.Name, v)
		}
	}
}

// A release given by its major version stands for its minor releases, the
// VERSION_ID the RHEL family's os-release has.
func TestAMinorReleaseIsItsMajorRelease(t *testing.T) {
	for _, c := range []struct {
		os      OS
		support Support
		release string
	}{
		{OS{ID: "almalinux", VersionID: "9.6"}, Tested, "9"},
		{OS{ID: "rocky", VersionID: "10.0"}, Tested, "10"},
		{OS{ID: "ol", VersionID: "9.8"}, Tested, "9"},
		{OS{ID: "centos", VersionID: "9"}, Tested, "9"},
		{OS{ID: "amzn", VersionID: "2023"}, Tested, "2023"},
		{OS{ID: "rhel", VersionID: "9.4"}, Family, "9"},
		{OS{ID: "centos", VersionID: "10"}, Family, "10"},
		{OS{ID: "almalinux", VersionID: "11.0"}, Newer, "10"},
		{OS{ID: "ol", VersionID: "10.1"}, Newer, "9"},
	} {
		if v := Check(c.os); v.Support != c.support || v.Release.Version != c.release {
			t.Errorf("%s: %+v, want support %d as %s", c.os.Display(), v, c.support, c.release)
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
	for _, o := range []OS{{ID: "ubuntu", VersionID: "18.04"}, {ID: "debian", VersionID: "11"}, {ID: "almalinux", VersionID: "8.10"}, {ID: "amzn", VersionID: "2"}} {
		if v := Check(o); v.Support != Unsupported || v.Distro == nil || v.Distro.ID != o.ID {
			t.Errorf("%s: %+v, want unsupported, naming its distribution", o.Display(), v)
		}
	}
	for _, o := range []OS{{ID: "alpine", VersionID: "3.20"}, {ID: "fedora", VersionID: "42", IDLike: ""}, {}} {
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
	if got, want := Summary(), "Ubuntu 20.04 or later, Debian 12 or later, the RHEL family 9 or later (AlmaLinux, Rocky Linux, Oracle Linux, RHEL and CentOS Stream), or Amazon Linux 2023 or later"; got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
	if got, want := Short(), "Ubuntu 20.04+, Debian 12+, the RHEL family 9+ or Amazon Linux 2023+"; got != want {
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

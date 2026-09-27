package install

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/platform"
)

// major is the version's first number: 9 for 9.6.
func major(o platform.OS) string {
	m, _, _ := strings.Cut(o.VersionID, ".")
	return m
}

// likes reports whether the system is id or says it is like id.
func likes(o platform.OS, id string) bool {
	return o.ID == id || slices.Contains(strings.Fields(o.IDLike), id)
}

func nonEmpty(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// SudoLink is where the installer links the binary when sudo wouldn't find
// it: the RHEL family's sudo searches only /sbin, /bin, /usr/sbin and
// /usr/bin (its secure_path), so `sudo playkeeper …` needs it there.
const SudoLink = "/usr/bin/playkeeper"

// sudoMissesBin reports whether sudo's secure_path leaves out the binary's
// folder, from the last one in the sudoers files.
func sudoMissesBin(sys System) bool {
	files := []string{sys.P("/etc/sudoers")}
	more, _ := filepath.Glob(sys.P("/etc/sudoers.d/*"))
	path := ""
	for _, f := range append(files, more...) {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if m := securepath.FindStringSubmatch(line); m != nil {
				path = m[1]
			}
		}
	}
	return path != "" && !slices.Contains(strings.Split(path, ":"), filepath.Dir(BinPath))
}

var securepath = regexp.MustCompile(`^\s*Defaults\s+secure_path\s*=\s*"?([^"\s]+)"?`)

// selinuxMode is "enforcing" or "permissive", or "" where SELinux is off.
func selinuxMode(sys System) string {
	b, err := os.ReadFile(sys.P("/sys/fs/selinux/enforce"))
	switch {
	case err != nil:
		return ""
	case strings.TrimSpace(string(b)) == "1":
		return "enforcing"
	}
	return "permissive"
}

// A family is the distributions that install software the same way.
type family struct {
	pm packageManager
	// docker is where Docker Engine comes from on o; nil when Playkeeper
	// doesn't know.
	docker func(o platform.OS) *dockerSource
}

var (
	debianFamily = &family{pm: apt{}, docker: func(o platform.OS) *dockerSource {
		archive := archiveName(o)
		// The docker command, where the distribution packages it on its own,
		// as Debian 13 does with docker-cli, which docker.io only recommends.
		return &dockerSource{pm: apt{}, pkgs: []string{"docker.io"}, optional: []string{"docker-cli"},
			what: archive + " docker.io package",
			plan: "docker.io and the docker command from " + archive + " archive (with the packages they depend on)"}
	}}
	rhelFamily = &family{pm: dnf{}, docker: rhelDocker}
)

// families are the distributions internal/platform supports, by os-release
// ID, with the way each installs software.
var families = map[string]*family{
	"ubuntu": debianFamily, "debian": debianFamily,
	"almalinux": rhelFamily, "rocky": rhelFamily, "ol": rhelFamily, "rhel": rhelFamily, "centos": rhelFamily, "amzn": rhelFamily,
}

// familyOf is how the installer treats o: as its distribution's family, or,
// on a system Playkeeper doesn't support, as the family o says it is like.
func familyOf(o platform.OS) *family {
	if f, ok := families[o.ID]; ok {
		return f
	}
	switch {
	case likes(o, "debian") || likes(o, "ubuntu"):
		return debianFamily
	case likes(o, "rhel") || likes(o, "centos"):
		return rhelFamily
	}
	return nil
}

// dockerSource is where Docker Engine comes from on a distribution.
type dockerSource struct {
	pm   packageManager
	pkgs []string
	// what says what the install installs, for preflight, and plan says it
	// for the plan.
	what, plan string
	// optional are installed too where the package manager has them.
	optional []string
	// repo, when set, is added before the install and removed with Docker.
	repo *rpmRepo
	// blockers are installed packages that stop the install, with why.
	blockers map[string]blocker
	// more, when set, adds what this machine needs on top of pkgs to extra,
	// and says so in what and plan; forHost runs it.
	more  func(sys System, s *dockerSource)
	extra []string
}

// forHost is src for this machine, with what it needs on top.
func (src *dockerSource) forHost(sys System) *dockerSource {
	host := *src
	if src.more != nil {
		src.more(sys, &host)
	}
	return &host
}

// packages are what the install asks the package manager for.
func (src *dockerSource) packages() []string { return append(slices.Clone(src.pkgs), src.extra...) }

type blocker struct{ why, fix string }

// rpmRepo is a package repository the installer adds, with the key its
// packages are signed with.
type rpmRepo struct {
	file, keyFile string
	id, name      string
	baseURL       string
	key           []byte
}

func (r *rpmRepo) content() string {
	return fmt.Sprintf("# Added by Playkeeper to install Docker Engine; `playkeeper uninstall` removes it with Docker.\n[%s]\nname=%s\nbaseurl=%s\nenabled=1\ngpgcheck=1\ngpgkey=file://%s\n", r.id, r.name, r.baseURL, r.keyFile)
}

// dockerRPMKey is Docker's signing key for its RPM repositories, as served at
// https://download.docker.com/linux/centos/gpg (and /linux/rhel/gpg): "Docker
// Release (CE rpm)", fingerprint 060A 61C5 1B55 8A7F 742B 77AA C52F EB6B 621E
// 9F35. Shipping it means the installer never trusts a key it downloads.
//
//go:embed docker-ce-rpm.gpg
var dockerRPMKey []byte

const (
	dockerRepoFile = "/etc/yum.repos.d/playkeeper-docker-ce.repo"
	dockerKeyFile  = "/etc/pki/rpm-gpg/playkeeper-docker-ce.gpg"
)

// rhelDocker is Docker Engine from Docker's own repository on the RHEL
// family (the distributions ship Podman instead), or Amazon Linux's docker
// package.
func rhelDocker(o platform.OS) *dockerSource {
	if o.ID == "amzn" {
		if n, err := strconv.Atoi(o.VersionID); err != nil || n < 2023 {
			return nil
		}
		what := "docker from Amazon Linux's repository"
		return &dockerSource{pm: dnf{}, pkgs: []string{"docker"}, what: what, plan: what + " (with the packages it depends on)"}
	}
	major := major(o)
	if !slices.Contains([]string{"8", "9", "10"}, major) {
		return nil
	}
	flavour, label := "centos", "CentOS "+major
	// Docker's CentOS 8 builds stopped at 26.1; its RHEL 8 builds go on.
	if o.ID == "rhel" || major == "8" {
		flavour, label = "rhel", "RHEL "+major
	}
	pkgs := []string{"docker-ce", "docker-ce-cli", "containerd.io"}
	what := "Docker Engine (" + strings.Join(pkgs, ", ") + ") from Docker's repository for " + label
	src := &dockerSource{
		pm:   dnf{},
		pkgs: pkgs,
		what: what,
		plan: what + " (with the packages it depends on)",
		repo: &rpmRepo{file: dockerRepoFile, keyFile: dockerKeyFile, id: "playkeeper-docker-ce", name: "Docker CE Stable (added by Playkeeper)",
			baseURL: "https://download.docker.com/linux/" + flavour + "/" + major + "/$basearch/stable", key: dockerRPMKey},
		blockers: map[string]blocker{
			"podman-docker": {
				why: "podman-docker is installed: it makes the docker command run Podman, and Docker Engine can't be installed next to it.",
				fix: "Remove it with: sudo dnf remove podman-docker (Podman and its containers keep working), then run the installer again.",
			},
			"runc": {
				why: "runc is installed, and Docker's containerd.io package would replace it: your container tools may use it.",
				fix: "Install Docker Engine yourself (https://docs.docker.com/engine/install/), or remove runc if nothing uses it (sudo dnf remove runc; Podman uses crun), then run the installer again.",
			},
		},
	}
	// From 10 on, the kernel's modules for iptables rules like Docker's
	// (nft_compat, xt_addrtype and the others) are in kernel-modules-extra,
	// which cloud images leave out.
	if major == "10" {
		src.more = func(sys System, s *dockerSource) {
			if _, err := sys.Run("modinfo", "-F", "filename", "xt_addrtype"); err == nil {
				return
			}
			release, err := sys.Run("uname", "-r")
			if err != nil || strings.TrimSpace(release) == "" {
				return
			}
			pkg := "kernel-modules-extra-" + strings.TrimSpace(release)
			s.extra = append(s.extra, pkg)
			s.what += ", and " + pkg + ", the running kernel's netfilter modules Docker's rules need"
			s.plan = s.what + " (with the packages it depends on)"
		}
	}
	return src
}

// dockerBlockers are the installed packages that stop installing Docker from
// src, sorted by name.
func dockerBlockers(src *dockerSource, installed map[string]bool) []blocker {
	var names []string
	for name := range src.blockers {
		if installed[name] {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	var out []blocker
	for _, n := range names {
		out = append(out, src.blockers[n])
	}
	return out
}

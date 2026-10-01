package install

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// packageManager installs and removes a distribution's packages: apt on
// Debian and Ubuntu, dnf on the RHEL family.
type packageManager interface {
	// id is what the install manifest records: "" for apt, which manifests
	// from before other families don't name.
	id() string
	// installed lists the installed packages by name.
	installed(sys System) (map[string]bool, error)
	// install installs pkgs with the packages they need, and nothing they
	// only recommend, and each of optional that the package manager has once
	// its lists are fresh; dnf's sources name none.
	install(ctx context.Context, sys System, out io.Writer, pkgs, optional []string) error
	// removable splits pkgs into those that can go and those that other
	// installed software needs, saying which needs what.
	removable(sys System, pkgs []string) (remove, keep []string, why string)
	// remove removes pkgs, and fails rather than remove anything else.
	remove(sys System, out io.Writer, pkgs []string) error
	// removeHint is the command that removes pkgs by hand.
	removeHint(pkgs []string) string
	// uninstallHint is the everyday command that removes pkg, one of the
	// admin's own packages; it asks before removing anything else.
	uninstallHint(pkg string) string
	// cleanHint frees space in the package cache.
	cleanHint() string
	// lock names the package lock, what usually holds it on a new server,
	// and the command that shows whether anything still does.
	lock() (name, who, busy string)
}

// packageManagerOf is the package manager a manifest's packages were
// installed with.
func packageManagerOf(m Manifest) packageManager {
	if m.PackageManager == (dnf{}).id() {
		return dnf{}
	}
	return apt{}
}

type apt struct{}

func (apt) id() string { return "" }

func (apt) installed(sys System) (map[string]bool, error) { return installedPackages(sys) }

// install asks about optional only after apt-get update: a new server's
// package lists are empty until then, so apt has no candidate for anything.
// Each try updates the lists, so one after a mirror failure reads them afresh.
func (apt) install(ctx context.Context, sys System, out io.Writer, pkgs, optional []string) error {
	return mirrorRetry(ctx, sys, out, aptMirrorFailed, func(bool) (string, error) {
		if o, err := aptGet(sys, out, "update"); err != nil {
			return o, err
		}
		want := slices.Clone(pkgs)
		for _, p := range optional {
			if aptCandidate(sys, p) {
				want = append(want, p)
			}
		}
		return aptGet(sys, out, append([]string{"install", "-y", "--no-install-recommends"}, want...)...)
	})
}

// aptMirrorFailed reports whether apt-get failed because the mirror didn't
// have a package list its Release file named ("File has unexpected size (…).
// Mirror sync in progress?", or a hash sum mismatch), or a package the lists
// name. A mirror it can't reach at all is only a warning to apt-get update.
func aptMirrorFailed(out string) bool {
	return strings.Contains(out, "E: Some index files failed to download") || strings.Contains(out, "E: Unable to fetch some archives")
}

func (apt) removable(_ System, pkgs []string) ([]string, []string, string) { return pkgs, nil, "" }

func (apt) remove(sys System, out io.Writer, pkgs []string) error {
	_, err := aptGet(sys, out, append([]string{"purge", "-y"}, pkgs...)...)
	return err
}

func (apt) removeHint(pkgs []string) string {
	return "sudo apt-get purge -y " + strings.Join(pkgs, " ")
}

func (apt) uninstallHint(pkg string) string { return "sudo apt-get remove " + pkg }

func (apt) cleanHint() string { return "sudo apt-get clean" }

func (apt) lock() (string, string, string) {
	return "apt's lock", "unattended-upgrades", "ps -C apt,apt-get,dpkg,unattended-upgr"
}

type dnf struct{}

func (dnf) id() string { return "dnf" }

// installed names RPM's signing keys by their version too: each is a
// "gpg-pubkey" package, and the one importing Docker's repository adds must
// be told apart from the distribution's.
func (dnf) installed(sys System) (map[string]bool, error) {
	out, err := sys.Run("rpm", "-qa", "--qf", `%{NAME} %{VERSION}-%{RELEASE}\n`)
	if err != nil {
		return nil, err
	}
	m := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		name, ver, _ := strings.Cut(strings.TrimSpace(l), " ")
		if name == "gpg-pubkey" {
			name += "-" + ver
		}
		if name != "" {
			m[name] = true
		}
	}
	return m, nil
}

// install waits for another dnf or rpm first; dnf also waits for them
// itself, but silently. A try after a mirror failure reads the repositories'
// metadata afresh.
func (dnf) install(ctx context.Context, sys System, out io.Writer, pkgs, _ []string) error {
	if err := waitForPackageLock(sys, out, dnf{}, sys.Now().Add(lockWait)); err != nil {
		return err
	}
	return mirrorRetry(ctx, sys, out, dnfMirrorFailed, func(again bool) (string, error) {
		args := []string{"-y"}
		if again {
			args = append(args, "--refresh")
		}
		return sys.Run("dnf", append(append(args, "--setopt=install_weak_deps=False", "install"), pkgs...)...)
	})
}

// dnfMirrorFailed reports whether dnf gave up because no mirror had a
// repository's metadata or a package. A "[MIRROR]" line only says dnf moved
// on to the next mirror, and comes before other failures too.
func dnfMirrorFailed(out string) bool {
	return strings.Contains(out, "Failed to download metadata") || strings.Contains(out, "Error downloading packages")
}

// removable asks rpm what removing pkgs would break, and keeps each package
// that provides something another installed package needs, like
// container-selinux once Podman shares it with Docker, until nothing would
// break.
func (dnf) removable(sys System, pkgs []string) ([]string, []string, string) {
	remove := slices.Clone(pkgs)
	var keep, why []string
	for len(remove) > 0 {
		o, err := sys.Run("rpm", append([]string{"-e", "--test"}, remove...)...)
		if err == nil {
			break
		}
		kept := false
		for _, n := range needs(o + "\n" + err.Error()) {
			what, _, _ := strings.Cut(n.what, " ")
			providers, _ := sys.Run("rpm", "-q", "--whatprovides", what, "--qf", `%{NAME}\n`)
			for _, p := range strings.Fields(providers) {
				if i := slices.Index(remove, p); i >= 0 {
					remove = slices.Delete(remove, i, i+1)
					keep, why, kept = append(keep, p), append(why, n.by+" needs "+p), true
				}
			}
		}
		if !kept {
			break
		}
	}
	return remove, keep, strings.Join(why, "; ")
}

// remove checks with rpm that nothing else needs pkgs before dnf removes
// them: removing a package dnf also removes everything that needs it. Signing
// keys are removed with rpm, as dnf doesn't.
func (dnf) remove(sys System, out io.Writer, pkgs []string) error {
	if len(pkgs) == 0 {
		return nil
	}
	if err := waitForPackageLock(sys, out, dnf{}, sys.Now().Add(lockWait)); err != nil {
		return err
	}
	if o, err := sys.Run("rpm", append([]string{"-e", "--test"}, pkgs...)...); err != nil {
		if n := needs(o + "\n" + err.Error()); len(n) > 0 {
			return fmt.Errorf("other software needs them: %s", describeNeeds(n))
		}
		return err
	}
	var packages, keys []string
	for _, p := range pkgs {
		if strings.HasPrefix(p, "gpg-pubkey-") {
			keys = append(keys, p)
		} else {
			packages = append(packages, p)
		}
	}
	if len(packages) > 0 {
		if _, err := sys.Run("dnf", append([]string{"-y", "remove", "--noautoremove"}, packages...)...); err != nil {
			return err
		}
	}
	if len(keys) > 0 {
		if _, err := sys.Run("rpm", append([]string{"-e"}, keys...)...); err != nil {
			return err
		}
	}
	return nil
}

// need is one of rpm's "X is needed by (installed) Y" lines: Y needs X, a
// package name, a versioned one or a file.
type need struct{ what, by string }

func needs(rpmOut string) []need {
	var out []need
	for _, l := range strings.Split(rpmOut, "\n") {
		what, by, ok := strings.Cut(strings.TrimSpace(l), " is needed by (installed) ")
		if ok {
			out = append(out, need{strings.TrimSpace(what), strings.TrimSpace(by)})
		}
	}
	return out
}

func describeNeeds(ns []need) string {
	var out []string
	for _, n := range ns {
		out = append(out, n.by+" needs "+n.what)
	}
	return strings.Join(out, "; ")
}

func (dnf) removeHint(pkgs []string) string { return "sudo rpm -e " + strings.Join(pkgs, " ") }

func (dnf) uninstallHint(pkg string) string { return "sudo dnf remove " + pkg }

func (dnf) cleanHint() string { return "sudo dnf clean all" }

func (dnf) lock() (string, string, string) {
	return "the RPM database", "dnf-makecache or cloud-init", "ps -C dnf,yum,rpm"
}

// rpmLocks are the files rpm holds fcntl locks on while it changes the
// package database (/usr/lib/sysimage/rpm from RHEL 10 on).
var rpmLocks = []string{"/var/lib/rpm/.rpm.lock", "/usr/lib/sysimage/rpm/.rpm.lock"}

// dnfLocks are the files a running dnf writes its process ID in.
var dnfLocks = []string{"/var/cache/dnf/metadata_lock.pid", "/var/cache/dnf/download_lock.pid", "/var/lib/dnf/rpmdb_lock.pid", "/var/log/log_lock.pid"}

// pidLockHeld reports whether a process that is still running wrote one of
// the files in paths.
func pidLockHeld(root string, paths []string) bool {
	for _, p := range paths {
		b, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || pid <= 0 || pid == os.Getpid() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, "/proc", strconv.Itoa(pid))); err == nil {
			return true
		}
	}
	return false
}

// lockWait bounds how long install and uninstall wait for another package
// manager to finish; on a new server unattended-upgrades often runs first.
const lockWait = 15 * time.Minute

// aptGet runs apt-get once no other package manager holds apt's lock, and
// waits again if another one takes it first.
func aptGet(sys System, out io.Writer, args ...string) (string, error) {
	deadline := sys.Now().Add(lockWait)
	args = append([]string{"-o", "DPkg::Lock::Timeout=60"}, args...)
	for {
		if err := waitForPackageLock(sys, out, apt{}, deadline); err != nil {
			return "", err
		}
		o, err := sys.Run("apt-get", args...)
		if err == nil || !aptLockError(o, err) || !sys.Now().Before(deadline) {
			return o, err
		}
		sys.Sleep(5 * time.Second)
	}
}

// mirrorTries bounds how often install runs the package manager when no
// mirror had what it needed; mirrorWait is the first wait between tries,
// doubled after each.
const (
	mirrorTries = 4
	mirrorWait  = 15 * time.Second
)

// mirrorRetry runs try until it works or fails for a reason other than a
// mirror's, as mirrorFailed tells from its output, at most mirrorTries times;
// again tells try that a mirror failed the try before. A mirror part-way
// through a sync can list files it doesn't have yet, as AlmaLinux 9's extras
// repository did on 1 Oct 2026 ("Cannot download, all mirrors were already
// tried without success"), and has them a little later. It says once that it
// tries again, and gives up as soon as ctx is done while it waits, as when
// the installer is interrupted.
func mirrorRetry(ctx context.Context, sys System, out io.Writer, mirrorFailed func(output string) bool, try func(again bool) (string, error)) error {
	wait := mirrorWait
	for n := 1; ; n++ {
		o, err := try(n > 1)
		if err == nil || n == mirrorTries || !mirrorFailed(o+"\n"+err.Error()) {
			return err
		}
		if n == 1 {
			fmt.Fprintln(out, "    a package mirror wasn't ready; trying again...")
		}
		for end := sys.Now().Add(wait); ctx.Err() == nil && sys.Now().Before(end); {
			sys.Sleep(time.Second)
		}
		if ctx.Err() != nil {
			return err
		}
		wait *= 2
	}
}

func waitForPackageLock(sys System, out io.Writer, pm packageManager, deadline time.Time) error {
	lock, who, busy := pm.lock()
	told := false
	for sys.PackageLockHeld() {
		if !sys.Now().Before(deadline) {
			return fmt.Errorf("another package manager has held %s for over %s (on a new server this is usually %s); run this again when `%s` shows nothing", lock, lockWait, who, busy)
		}
		if !told {
			fmt.Fprintf(out, "    waiting for another package manager to finish (on a new server this is usually %s)...\n", who)
			told = true
		}
		sys.Sleep(5 * time.Second)
	}
	return nil
}

func aptLockError(out string, err error) bool {
	s := out + " " + err.Error()
	return strings.Contains(s, "Could not get lock") || strings.Contains(s, "Unable to acquire the dpkg frontend lock") || strings.Contains(s, "Unable to lock directory")
}

func installedPackages(sys System) (map[string]bool, error) {
	out, err := sys.Run("dpkg-query", "-W", "-f", "${Package}\\n")
	if err != nil {
		return nil, err
	}
	m := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			m[l] = true
		}
	}
	return m, nil
}

// Package install implements `playkeeper install` and `playkeeper uninstall`:
// preflight before any change, an explicit change plan, journaled steps that
// roll back on failure, an install manifest, and an uninstall that keeps
// worlds and backups.
package install

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/CIYAhq/playkeeper/internal/docker"
)

// System is every host interaction the installer performs. Production uses
// Real(); tests substitute fields and point Root at a temporary directory.
type System struct {
	Root       string
	Run        func(name string, args ...string) (string, error)
	IsRoot     func() bool
	Arch       func() string
	MemTotalMB func() int
	DiskFree   func(path string) int64
	Listening  func(port int) bool
	Processes  func() []string
	LookupUser func(name string) (uid, gid int, ok bool)
	Docker     func(ctx context.Context) (DockerInfo, error)
	Executable func() (string, error)
	Chown      func(path string, uid, gid int) error
	Now        func() time.Time
	Sleep      func(time.Duration)
	// PackageLockHeld reports whether another program (apt, dpkg,
	// unattended-upgrades) holds apt's or dpkg's lock.
	PackageLockHeld func() bool
	// WaitHealthy blocks until the agent socket and panel HTTPS answer.
	WaitHealthy func(ctx context.Context, socket, certPath string, panelPort int) error
	// WaitVersion blocks until the agent and the panel both answer and report
	// version want.
	WaitVersion func(ctx context.Context, socket, certPath string, panelPort int, want string) error
	// Version runs a playkeeper binary's `version` command and returns the
	// version it reports.
	Version func(binary string) (string, error)
}

type DockerInfo struct {
	Version    string
	Containers []docker.ContainerSummary
}

// P maps an absolute host path into the system root.
func (s System) P(path string) string { return filepath.Join(s.Root, path) }

func Real() System {
	return System{
		Root: "/",
		Run: func(name string, args ...string) (string, error) {
			cmd := exec.Command(name, args...)
			cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive", "LC_ALL=C")
			out, err := cmd.CombinedOutput()
			if err != nil {
				return string(out), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(lastLines(string(out), 5)))
			}
			return string(out), nil
		},
		IsRoot:     func() bool { return os.Geteuid() == 0 },
		Arch:       func() string { return runtime.GOARCH },
		MemTotalMB: memTotalMB,
		DiskFree: func(path string) int64 {
			for p := path; p != "" && p != "."; p = filepath.Dir(p) {
				var st syscall.Statfs_t
				if err := syscall.Statfs(p, &st); err == nil {
					return int64(st.Bavail) * st.Bsize
				}
				if p == "/" {
					break
				}
			}
			return 0
		},
		Listening: listening,
		Processes: processes,
		LookupUser: func(name string) (int, int, bool) {
			u, err := user.Lookup(name)
			if err != nil {
				return 0, 0, false
			}
			uid, _ := strconv.Atoi(u.Uid)
			gid, _ := strconv.Atoi(u.Gid)
			return uid, gid, true
		},
		Docker: func(ctx context.Context) (DockerInfo, error) {
			if _, err := os.Stat("/var/run/docker.sock"); err != nil {
				return DockerInfo{}, err
			}
			c := docker.New("/var/run/docker.sock")
			v, err := c.Negotiate(ctx)
			if err != nil {
				return DockerInfo{}, err
			}
			list, err := c.ContainerList(ctx, true)
			if err != nil {
				return DockerInfo{}, err
			}
			return DockerInfo{Version: v.Version, Containers: list}, nil
		},
		Executable:      os.Executable,
		Chown:           os.Lchown,
		Now:             time.Now,
		Sleep:           time.Sleep,
		PackageLockHeld: func() bool { return lockHeld(aptLocks) },
		WaitHealthy:     waitHealthy,
		WaitVersion:     waitVersion,
		Version:         binaryVersion,
	}
}

// binaryVersion runs `<binary> version`, which prints
// "playkeeper VERSION (COMMIT, DATE)".
func binaryVersion(binary string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "version").Output()
	if err != nil {
		return "", fmt.Errorf("%s version: %w", binary, err)
	}
	return ParseVersionLine(string(out))
}

// ParseVersionLine reads the version from `playkeeper version` output.
func ParseVersionLine(out string) (string, error) {
	f := strings.Fields(out)
	if len(f) < 2 || f[0] != "playkeeper" {
		return "", fmt.Errorf("unexpected version output %q", strings.TrimSpace(out))
	}
	return f[1], nil
}

// aptLocks are the files apt and dpkg hold fcntl locks on while they work.
var aptLocks = []string{"/var/lib/dpkg/lock-frontend", "/var/lib/dpkg/lock", "/var/lib/apt/lists/lock", "/var/cache/apt/archives/lock"}

// lockHeld reports whether another process holds an fcntl lock on any of
// paths. It only asks; it never takes a lock.
func lockHeld(paths []string) bool {
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		lk := syscall.Flock_t{Type: syscall.F_WRLCK}
		err = syscall.FcntlFlock(f.Fd(), syscall.F_GETLK, &lk)
		f.Close()
		if err == nil && lk.Type != syscall.F_UNLCK {
			return true
		}
	}
	return false
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

func memTotalMB() int {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, _ := strconv.Atoi(fields[1])
			return kb / 1024
		}
	}
	return 0
}

// listening parses /proc/net/tcp{,6} for sockets in LISTEN state.
func listening(port int) bool {
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) < 4 || fields[3] != "0A" {
				continue
			}
			local := fields[1]
			i := strings.LastIndex(local, ":")
			if i < 0 {
				continue
			}
			p, err := strconv.ParseInt(local[i+1:], 16, 32)
			if err == nil && int(p) == port {
				return true
			}
		}
	}
	return false
}

func processes() []string {
	var out []string
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || len(b) == 0 {
			continue
		}
		out = append(out, strings.ReplaceAll(strings.TrimRight(string(b), "\x00"), "\x00", " "))
	}
	return out
}

var errDeclined = errors.New("installation cancelled; nothing was changed")

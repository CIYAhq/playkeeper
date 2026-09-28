package agent

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/diagnose"
	"github.com/CIYAhq/playkeeper/internal/platform"
)

// hostMemoryMB reads MemTotal from /proc/meminfo.
func hostMemoryMB() int {
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

func diskUsage(path string) (free, total int64, err error) {
	for p := path; ; {
		if _, err := os.Stat(p); err == nil {
			return statfs(p)
		}
		parent := p[:strings.LastIndex(p, "/")]
		if parent == "" || parent == p {
			return statfs("/")
		}
		p = parent
	}
}

// checkEgress confirms the host can reach PaperMC's download API, which the
// server image needs on first start.
func checkEgress(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://fill.papermc.io/v3/projects/paper", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("PaperMC answered HTTP %d", resp.StatusCode)
	}
	return nil
}

// portInUse reports whether anything accepts TCP connections on the port.
func portInUse(port int) bool {
	for _, host := range []string{"127.0.0.1", "::1"} {
		c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 500*time.Millisecond)
		if err == nil {
			c.Close()
			return true
		}
	}
	return false
}

// hostLoop samples the machine's CPU use and whether Docker answers, with
// its version. Docker's health is the machine's, so it is known before the
// first server exists.
func (a *Agent) hostLoop(ctx context.Context) {
	t := time.NewTicker(a.opts.SampleInterval)
	defer t.Stop()
	for {
		if b, err := a.opts.ProcStat(); err == nil {
			if cur, err := diagnose.ParseProcStat(b); err == nil {
				a.recordCPU(a.now(), cur)
			}
		}
		v, err := a.docker.Negotiate(ctx)
		if ctx.Err() != nil {
			return
		}
		a.mu.Lock()
		a.dockerOK = err == nil
		if err == nil {
			a.dockerVersion = v.Version
		}
		a.mu.Unlock()
		if err == nil {
			a.refreshSELinux(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// selinuxLabels reports whether Docker runs containers under SELinux labels
// ("selinux-enabled" in its configuration, which RHEL-family admins may
// turn on): what a server's container bind-mounts must then be relabelled
// for containers (":z"), or the server can't read its own world. hostLoop
// keeps it current; until Docker first answers it is asked here.
func (a *Agent) selinuxLabels() bool {
	a.mu.Lock()
	known, on := a.selinuxKnown, a.selinux
	a.mu.Unlock()
	if known || a.docker == nil || a.ctx == nil {
		return on
	}
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	return a.refreshSELinux(ctx)
}

func (a *Agent) refreshSELinux(ctx context.Context) bool {
	info, err := a.docker.Info(ctx)
	if err != nil {
		return false
	}
	a.mu.Lock()
	a.selinuxKnown, a.selinux = true, info.SELinux()
	a.mu.Unlock()
	return info.SELinux()
}

// osName is the distribution's name and version, like "Ubuntu 24.04" or
// "Debian 12".
func osName() string {
	o := platform.ReadOS("/etc/os-release")
	if o == (platform.OS{}) {
		return runtime.GOOS
	}
	return o.Display()
}

func archName() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86-64"
	case "arm64":
		return "ARM64"
	}
	return runtime.GOARCH
}

func numCPU() int { return runtime.NumCPU() }

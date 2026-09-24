package agent

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
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

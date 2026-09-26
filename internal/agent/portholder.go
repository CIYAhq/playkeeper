package agent

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

const (
	// Bounds on the /proc scan, which runs only when a start failed over a
	// taken port.
	maxSocketLines = 200_000
	maxProcesses   = 32_768
	maxOpenFiles   = 65_536
	tcpListen      = "0A"
)

// portHolder finds the process listening on TCP port on the host: the
// listening socket's inode in proc's net/tcp and net/tcp6, then the process
// with that socket open. The agent can only look at the open files of
// processes run by root, as Docker's port proxy is, so a program another
// user runs is not found.
func portHolder(proc string, port int) (name string, pid int, ok bool) {
	inodes := map[string]bool{}
	for _, f := range []string{"tcp", "tcp6"} {
		listeningInodes(filepath.Join(proc, "net", f), port, inodes)
	}
	if len(inodes) == 0 {
		return "", 0, false
	}
	entries, err := os.ReadDir(proc)
	if err != nil {
		return "", 0, false
	}
	seen, files := 0, 0
	for _, e := range entries {
		id, err := strconv.Atoi(e.Name())
		if err != nil || id <= 0 {
			continue
		}
		if seen++; seen > maxProcesses {
			break
		}
		dir := filepath.Join(proc, e.Name())
		fds, err := os.ReadDir(filepath.Join(dir, "fd"))
		if err != nil {
			continue
		}
		for _, fd := range fds {
			if files++; files > maxOpenFiles {
				return "", 0, false
			}
			link, err := os.Readlink(filepath.Join(dir, "fd", fd.Name()))
			if err != nil {
				continue
			}
			inode, found := strings.CutPrefix(link, "socket:[")
			if !found || !inodes[strings.TrimSuffix(inode, "]")] {
				continue
			}
			if name := processName(filepath.Join(dir, "comm")); name != "" {
				return name, id, true
			}
			return "", 0, false
		}
	}
	return "", 0, false
}

// listeningInodes adds the inodes of the sockets listening on port, from
// one of the kernel's socket tables.
func listeningInodes(table string, port int, into map[string]bool) {
	f, err := os.Open(table)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 0; sc.Scan() && n < maxSocketLines; n++ {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 || fields[3] != tcpListen {
			continue
		}
		_, hexPort, found := strings.Cut(fields[1], ":")
		p, err := strconv.ParseUint(hexPort, 16, 16)
		if !found || err != nil || int(p) != port || fields[9] == "0" {
			continue
		}
		into[fields[9]] = true
	}
}

// processName reads a process's name as the kernel keeps it: at most 15
// bytes the process chose itself, so anything but plain printable text is
// dropped.
func processName(comm string) string {
	f, err := os.Open(comm)
	if err != nil {
		return ""
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 64))
	name := strings.Map(func(r rune) rune {
		if r == unicode.ReplacementChar || !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(string(b)))
	if r := []rune(name); len(r) > 15 {
		name = string(r[:15])
	}
	return strings.TrimSpace(name)
}

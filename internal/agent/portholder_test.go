package agent

import (
	"os"
	"path/filepath"
	"testing"
)

const tcpHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

// fakeProc lays out the parts of /proc portHolder reads: the socket tables,
// and for each process its name and open files.
func fakeProc(t *testing.T, tcp, tcp6 string, procs map[string]fakeProcess) string {
	t.Helper()
	proc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proc, "net"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"tcp": tcp, "tcp6": tcp6} {
		if err := os.WriteFile(filepath.Join(proc, "net", name), []byte(tcpHeader+body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for pid, p := range procs {
		fd := filepath.Join(proc, pid, "fd")
		if err := os.MkdirAll(fd, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(proc, pid, "comm"), []byte(p.comm), 0o644); err != nil {
			t.Fatal(err)
		}
		for i, target := range p.fds {
			if err := os.Symlink(target, filepath.Join(fd, string(rune('3'+i)))); err != nil {
				t.Fatal(err)
			}
		}
	}
	return proc
}

type fakeProcess struct {
	comm string
	fds  []string
}

// Lines as the kernel writes them: port 25565 is 63DD, 25566 is 63DE and
// 25567 is 63DF; state 0A is listening, 01 an open connection.
const (
	tcpLines = "   0: 00000000:63DD 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 4242 1 0000000000000000 100 0 0 10 0\n" +
		"   1: 0100007F:63DD 0100007F:D431 01 00000000:00000000 00:00000000 00000000  1000        0 999 1 0000000000000000 20 4 30 10 -1\n" +
		"   2: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 5000 1 0000000000000000 100 0 0 10 0\n" +
		"   3: 00000000:63DE 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1001        0 0 1 0000000000000000 100 0 0 10 0\n"
	tcp6Lines = "   0: 00000000000000000000000000000000:63DF 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 6161 1 0000000000000000 100 0 0 10 0\n"
)

func TestPortHolderFindsTheListeningProcess(t *testing.T) {
	proc := fakeProc(t, tcpLines, tcp6Lines, map[string]fakeProcess{
		"48211": {comm: "java\n", fds: []string{"/dev/null", "socket:[4242]"}},
		"700":   {comm: "bash\n", fds: []string{"socket:[999]"}},
		"812":   {comm: "docker-proxy\n", fds: []string{"pipe:[77]", "socket:[6161]"}},
		"self":  {comm: "playkeeper\n", fds: []string{"socket:[4242]"}},
	})
	for _, c := range []struct {
		port int
		name string
		pid  int
		ok   bool
	}{
		{25565, "java", 48211, true},
		{25567, "docker-proxy", 812, true},
		// Listening, but with no inode: a socket another user's program holds
		// in a way the table doesn't show.
		{25566, "", 0, false},
		{25568, "", 0, false},
	} {
		name, pid, ok := portHolder(proc, c.port)
		if name != c.name || pid != c.pid || ok != c.ok {
			t.Errorf("port %d: got %q %d %v, want %q %d %v", c.port, name, pid, ok, c.name, c.pid, c.ok)
		}
	}
}

// A connection on the port is not what holds it, and a holder whose open
// files can't be read is not guessed at.
func TestPortHolderNeedsTheListeningSocketItself(t *testing.T) {
	proc := fakeProc(t, tcpLines, "", map[string]fakeProcess{
		"700": {comm: "bash\n", fds: []string{"socket:[999]"}},
	})
	if name, pid, ok := portHolder(proc, 25565); ok {
		t.Errorf("a connection to the port was taken for its holder: %q %d", name, pid)
	}
	if _, _, ok := portHolder(filepath.Join(proc, "missing"), 25565); ok {
		t.Error("found a holder without a /proc")
	}
}

// A process names itself, so its name is cut to what the kernel keeps and
// anything but printable text is dropped.
func TestPortHolderKeepsOnlyAPlainName(t *testing.T) {
	proc := fakeProc(t, tcpLines, "", map[string]fakeProcess{
		"48211": {comm: "ja\x1b[31mva\u200b-server-with-a-long-name\n", fds: []string{"socket:[4242]"}},
	})
	name, pid, ok := portHolder(proc, 25565)
	if !ok || pid != 48211 || name != "ja[31mva-server" {
		t.Errorf("got %q %d %v", name, pid, ok)
	}
	proc = fakeProc(t, tcpLines, "", map[string]fakeProcess{
		"48211": {comm: "\x00\x01\n", fds: []string{"socket:[4242]"}},
	})
	if name, _, ok := portHolder(proc, 25565); ok {
		t.Errorf("a holder without a readable name: %q", name)
	}
}

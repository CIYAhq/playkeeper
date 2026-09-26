package curated

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/addons"
)

func TestVoiceChatConfigPath(t *testing.T) {
	for typ, want := range map[string]string{
		"paper":    "plugins/voicechat/voicechat-server.properties",
		"purpur":   "plugins/voicechat/voicechat-server.properties",
		"fabric":   "config/voicechat/voicechat-server.properties",
		"quilt":    "config/voicechat/voicechat-server.properties",
		"neoforge": "config/voicechat/voicechat-server.properties",
		"forge":    "config/voicechat/voicechat-server.properties",
	} {
		if got, err := VoiceChatConfigPath(typ); err != nil || got != want {
			t.Errorf("VoiceChatConfigPath(%q) = %q, %v", typ, got, err)
		}
	}
	_, err := VoiceChatConfigPath("vanilla")
	wantKind(t, err, addons.KindNoAddons)
	_, err = VoiceChatConfigPath("folia")
	wantKind(t, err, addons.KindUnknownServerType)
}

func TestVoiceChatConfig(t *testing.T) {
	defaults := readFile(t, "testdata/voicechat-server.properties")
	for _, c := range []struct {
		name, in, want string
	}{
		{"no file yet", "", "port=24455\nbind_address=*\n"},
		{"the add-on's defaults", defaults,
			strings.Replace(strings.Replace(defaults, "\nport=24454\n", "\nport=24455\n", 1), "\nbind_address=\n", "\nbind_address=*\n", 1)},
		{"other separators", "port = 1\nbind_address : 10.0.0.5\n", "port=24455\nbind_address=*\n"},
		{"space as separator", "  port 1\n", "port=24455\nbind_address=*\n"},
		{"a key twice", "port=1\ncodec=VOIP\nport=2\n", "port=24455\ncodec=VOIP\nport=24455\nbind_address=*\n"},
		{"comments and look-alikes", "#port=1\n! port=2\nports=3\nvoice_host=example.com:24454\n",
			"#port=1\n! port=2\nports=3\nvoice_host=example.com:24454\nport=24455\nbind_address=*\n"},
		{"Windows line ends", "port=1\r\nbind_address=\r\ncodec=VOIP\r\n", "port=24455\r\nbind_address=*\r\ncodec=VOIP\r\n"},
		{"no line end at the end", "codec=VOIP", "codec=VOIP\nport=24455\nbind_address=*\n"},
		{"a value carried on", "voice_host=a\\\nport=1\n", "voice_host=a\\\nport=1\nport=24455\nbind_address=*\n"},
		{"the port carried on", "port=1\\\n  2\ncodec=VOIP\n", "port=24455\ncodec=VOIP\nbind_address=*\n"},
		{"the port carried on to the end", "port=1\\\n2\\", "port=24455\nbind_address=*\n"},
		{"a file that ends inside a value", "voice_host=a\\", "voice_host=a\\\n\nport=24455\nbind_address=*\n"},
		{"an escaped backslash", "voice_host=a\\\\\nport=1\n", "voice_host=a\\\\\nport=24455\nbind_address=*\n"},
	} {
		got, err := VoiceChatConfig([]byte(c.in), 24455)
		if err != nil || string(got) != c.want {
			t.Errorf("%s:\ngot  %q, %v\nwant %q", c.name, got, err, c.want)
		}
	}
	for _, port := range []int{-1, 0, 80, 1023, 65536} {
		_, err := VoiceChatConfig(nil, port)
		wantKind(t, err, KindPortInvalid)
	}
	for _, port := range []int{1024, 65535} {
		if _, err := VoiceChatConfig(nil, port); err != nil {
			t.Errorf("port %d: %v", port, err)
		}
	}
}

func TestSetUpVoiceChat(t *testing.T) {
	const file = "config/voicechat/voicechat-server.properties"
	srv := addons.Server{Dir: t.TempDir(), Type: "fabric", MinecraftVersion: "26.2", Owner: &addons.Owner{UID: os.Getuid(), GID: os.Getgid()}}
	vc, err := SetUpVoiceChat(srv, 24455)
	if err != nil {
		t.Fatal(err)
	}
	if vc.Port != 24455 || vc.Config != file || vc.Publish != (Publish{Port: 24455, Protocol: "udp"}) || vc.Publish.String() != "24455/udp" {
		t.Errorf("setup %+v", vc)
	}
	var steps []string
	for _, s := range vc.Steps {
		steps = append(steps, string(s.Kind)+": "+s.Msg)
	}
	wantList(t, "steps", steps,
		"open_port: Voice chat uses UDP port 24455. If your hosting provider has a firewall in its control panel, allow UDP port 24455 there.",
		"client_mod: Each player who wants to talk also installs Simple Voice Chat in their own game. Players without it can still join and play.")
	if got := readFile(t, filepath.Join(srv.Dir, file)); got != "port=24455\nbind_address=*\n" {
		t.Errorf("settings %q", got)
	}
	for _, p := range []string{"config", "config/voicechat", file} {
		fi, err := os.Lstat(filepath.Join(srv.Dir, p))
		if err != nil || fi.Mode().Perm()&0o007 != 0 {
			t.Errorf("%s: %v, %v; other users must not read it", p, fi.Mode(), err)
		}
	}

	defaults := readFile(t, "testdata/voicechat-server.properties")
	writeFile(t, filepath.Join(srv.Dir, file), defaults)
	if _, err := SetUpVoiceChat(srv, 24456); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(strings.Replace(defaults, "\nport=24454\n", "\nport=24456\n", 1), "\nbind_address=\n", "\nbind_address=*\n", 1)
	if got := readFile(t, filepath.Join(srv.Dir, file)); got != want {
		t.Errorf("settings:\n%s\nwant\n%s", got, want)
	}
	entries, _ := os.ReadDir(filepath.Join(srv.Dir, "config/voicechat"))
	if len(entries) != 1 {
		t.Errorf("the settings folder holds %v; the temporary file stayed", entries)
	}

	paper := addons.Server{Dir: t.TempDir(), Type: "paper", MinecraftVersion: "26.2"}
	if vc, err := SetUpVoiceChat(paper, 24454); err != nil || vc.Config != "plugins/voicechat/voicechat-server.properties" {
		t.Errorf("Paper setup %+v, %v", vc, err)
	} else if got := readFile(t, filepath.Join(paper.Dir, vc.Config)); got != "port=24454\nbind_address=*\n" {
		t.Errorf("Paper settings %q", got)
	}
}

func TestSetUpVoiceChatRefusals(t *testing.T) {
	const file = "config/voicechat/voicechat-server.properties"
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.txt"), "not the server's")
	for _, c := range []struct {
		name    string
		prepare func(dir string)
		msg     string
	}{
		{"settings file is a link", func(dir string) {
			mkdir(t, filepath.Join(dir, "config/voicechat"))
			symlink(t, filepath.Join(outside, "secret.txt"), filepath.Join(dir, file))
		}, "Playkeeper could not write voice chat's settings to " + file + ": it is a link, a folder or a special file."},
		{"settings file is a folder", func(dir string) {
			mkdir(t, filepath.Join(dir, file))
		}, "Playkeeper could not write voice chat's settings to " + file + ": it is a link, a folder or a special file."},
		{"settings folder is a link", func(dir string) {
			mkdir(t, filepath.Join(dir, "config"))
			symlink(t, outside, filepath.Join(dir, "config/voicechat"))
		}, "Playkeeper could not write voice chat's settings to " + file + ": config/voicechat is a file or a link, not a folder."},
		{"config is a file", func(dir string) {
			writeFile(t, filepath.Join(dir, "config"), "")
		}, "Playkeeper could not write voice chat's settings to " + file + ": config is a file or a link, not a folder."},
		{"settings file is too large", func(dir string) {
			mkdir(t, filepath.Join(dir, "config/voicechat"))
			writeFile(t, filepath.Join(dir, file), strings.Repeat("#", maxConfig+1))
		}, "Playkeeper could not write voice chat's settings to " + file + ": it is larger than the 64 KB Playkeeper reads."},
	} {
		dir := t.TempDir()
		c.prepare(dir)
		_, err := SetUpVoiceChat(addons.Server{Dir: dir, Type: "fabric"}, 24455)
		if e := wantKind(t, err, KindConfigUnusable); e.Msg != c.msg || e.Params["file"] != file {
			t.Errorf("%s: %+v", c.name, e.Notice)
		}
	}
	if got := readFile(t, filepath.Join(outside, "secret.txt")); got != "not the server's" {
		t.Errorf("a file outside the server was changed: %q", got)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 1 {
		t.Errorf("files were written outside the server: %v", entries)
	}

	_, err := SetUpVoiceChat(addons.Server{Dir: filepath.Join(t.TempDir(), "missing"), Type: "fabric"}, 24455)
	wantKind(t, err, KindConfigUnusable)
	_, err = SetUpVoiceChat(addons.Server{Dir: t.TempDir(), Type: "vanilla"}, 24455)
	wantKind(t, err, addons.KindNoAddons)
	dir := t.TempDir()
	_, err = SetUpVoiceChat(addons.Server{Dir: dir, Type: "fabric"}, 80)
	wantKind(t, err, KindPortInvalid)
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a refused setup left %v", entries)
	}
}

func TestPickPort(t *testing.T) {
	taken := func(ports ...int) func(int) bool {
		return func(p int) bool { return slices.Contains(ports, p) }
	}
	for _, c := range []struct {
		from  int
		taken []int
		want  int
	}{
		{VoiceChatPort, nil, 24454},
		{VoiceChatPort, []int{24454, 24455, 24457}, 24456},
		{VoiceChatPort, []int{25565, 24455}, 24454},
		{65535, nil, 65535},
	} {
		if got, err := PickPort(c.from, taken(c.taken...)); err != nil || got != c.want {
			t.Errorf("PickPort(%d, %v) = %d, %v; want %d", c.from, c.taken, got, err, c.want)
		}
	}
	_, err := PickPort(65534, taken(65534, 65535))
	if e := wantKind(t, err, KindNoFreePort); e.Msg != "Every port from 65534 up is already in use on this machine." {
		t.Errorf("message %q", e.Msg)
	}
	_, err = PickPort(80, taken())
	if e := wantKind(t, err, KindPortInvalid); e.Msg != "Port 80 cannot be used for an add-on: it must be from 1024 to 65535." {
		t.Errorf("message %q", e.Msg)
	}
}

func wantList(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s:\ngot  %s\nwant %s", what, strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeFile(t *testing.T, name, data string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(data), 0o640); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, name string) {
	t.Helper()
	if err := os.MkdirAll(name, 0o750); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}

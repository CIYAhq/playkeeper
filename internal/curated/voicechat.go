package curated

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/addons"
)

// VoiceChatPort is Simple Voice Chat's default UDP port.
const VoiceChatPort = 24454

// The game runs as an unprivileged user in its container, so it cannot
// listen below 1024.
const minPort, maxPort = 1024, 65535

// maxConfig bounds the settings file Playkeeper reads; the add-on's own is
// about 2 KB.
const maxConfig = 64 << 10

// Publish is a port a server's container publishes on the machine, with the
// same number inside the container and outside.
type Publish struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
}

// String is "24454/udp": Docker's name for the container port, the key in
// ExposedPorts and PortBindings.
func (p Publish) String() string { return strconv.Itoa(p.Port) + "/" + p.Protocol }

// VoiceChat is Simple Voice Chat's setup on one server.
type VoiceChat struct {
	// Port is the UDP port players' games send and receive voice on.
	Port int `json:"port"`
	// Config is the settings file written, inside the server's data
	// directory.
	Config string `json:"config"`
	// Publish is the port the server's container must publish. That is all
	// the machine needs: Docker's published ports bypass ufw. A firewall at
	// the hosting provider is left to the owner (Steps).
	Publish Publish `json:"publish"`
	// Steps are left for the owner and the players.
	Steps []addons.Notice `json:"steps"`
}

// PickPort returns the lowest port from from up that taken reports free.
// taken should cover every port the machine's servers publish, TCP or UDP,
// and may also probe the machine.
func PickPort(from int, taken func(port int) bool) (int, error) {
	if err := checkPort(from); err != nil {
		return 0, err
	}
	for p := from; p <= maxPort; p++ {
		if !taken(p) {
			return p, nil
		}
	}
	return 0, fail(KindNoFreePort, kv("from", strconv.Itoa(from)),
		fmt.Sprintf("Every port from %d up is already in use on this machine.", from),
		"Remove a server or an add-on that uses a port, then try again.")
}

func checkPort(port int) error {
	if port < minPort || port > maxPort {
		return fail(KindPortInvalid, kv("port", strconv.Itoa(port)),
			fmt.Sprintf("Port %d cannot be used for an add-on: it must be from %d to %d.", port, minPort, maxPort),
			"Choose a port in that range.")
	}
	return nil
}

// VoiceChatConfigPath is where Simple Voice Chat keeps its server settings on
// a server type, inside the server's data directory.
func VoiceChatConfigPath(serverType string) (string, error) {
	t, err := addons.TargetFor(serverType)
	if err != nil {
		return "", err
	}
	if t.Kind == "plugin" {
		return "plugins/voicechat/voicechat-server.properties", nil
	}
	return "config/voicechat/voicechat-server.properties", nil
}

// VoiceChatConfig returns Simple Voice Chat's server settings with the voice
// port set, keeping every other line as it is. existing may be empty.
func VoiceChatConfig(existing []byte, port int) ([]byte, error) {
	if err := checkPort(port); err != nil {
		return nil, err
	}
	// An empty bind_address makes the add-on listen on server.properties'
	// server-ip, which may be an address of the machine that the container
	// does not have.
	keys := []string{"port", "bind_address"}
	want := map[string]string{"port": strconv.Itoa(port), "bind_address": "*"}
	done := map[string]bool{}
	var out []string
	cont, drop := false, false
	if text := strings.TrimSuffix(string(existing), "\n"); text != "" {
		for _, raw := range strings.Split(text, "\n") {
			line := strings.TrimSuffix(raw, "\r")
			if cont {
				// The line carries on the value of the line before it.
				cont = oddBackslashes(line)
				if !drop {
					out = append(out, raw)
				}
				continue
			}
			k, ok := propertyKey(line)
			cont, drop = ok && oddBackslashes(line), false
			if v, managed := want[k]; ok && managed {
				out = append(out, k+"="+v+raw[len(line):])
				done[k], drop = true, cont
				continue
			}
			out = append(out, raw)
		}
	}
	for _, k := range keys {
		if done[k] {
			continue
		}
		if cont && !drop {
			// A blank line ends the value the last line carries on.
			out, cont = append(out, ""), false
		}
		out = append(out, k+"="+want[k])
	}
	return []byte(strings.Join(out, "\n") + "\n"), nil
}

// propertyKey returns the key of a Java properties line, or false for a
// blank or comment line.
func propertyKey(line string) (string, bool) {
	s := strings.TrimLeft(line, " \t\f")
	if s == "" || s[0] == '#' || s[0] == '!' {
		return "", false
	}
	if i := strings.IndexAny(s, "=: \t\f"); i >= 0 {
		s = s[:i]
	}
	return s, true
}

func oddBackslashes(line string) bool {
	return (len(line)-len(strings.TrimRight(line, `\`)))%2 == 1
}

// SetUpVoiceChat writes port into Simple Voice Chat's settings on the server,
// making the file if the add-on has not made it yet. The server should be
// stopped: the add-on reads its settings when it starts.
func SetUpVoiceChat(srv addons.Server, port int) (*VoiceChat, error) {
	name, err := VoiceChatConfigPath(srv.Type)
	if err != nil {
		return nil, err
	}
	if err := checkPort(port); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(srv.Dir)
	if err != nil {
		return nil, configError(name, err)
	}
	defer root.Close()
	if err := mkdirs(root, path.Dir(name), srv.Owner); err != nil {
		return nil, configError(name, err)
	}
	old, err := readConfig(root, name)
	if err != nil {
		return nil, configError(name, err)
	}
	b, err := VoiceChatConfig(old, port)
	if err != nil {
		return nil, err
	}
	if err := writeConfig(root, name, b, srv.Owner); err != nil {
		return nil, configError(name, err)
	}
	pub := Publish{Port: port, Protocol: "udp"}
	return &VoiceChat{Port: port, Config: name, Publish: pub, Steps: []addons.Notice{openPortStep("Voice chat", pub), voiceChatClientStep()}}, nil
}

// reason is why a file or folder cannot be used, in words for the user.
type reason string

func (r reason) Error() string { return string(r) }

func mkdirs(root *os.Root, dir string, o *addons.Owner) error {
	parts := strings.Split(dir, "/")
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		fi, err := root.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := root.Mkdir(p, 0o750); err != nil {
				return err
			}
			if err := chown(root, p, o); err != nil {
				return err
			}
		case err != nil:
			return err
		case !fi.IsDir():
			return reason(p + " is a file or a link, not a folder")
		}
	}
	return nil
}

func readConfig(root *os.Root, name string) ([]byte, error) {
	fi, err := root.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, err
	case !fi.Mode().IsRegular():
		return nil, reason("it is a link, a folder or a special file")
	case fi.Size() > maxConfig:
		return nil, reason(fmt.Sprintf("it is larger than the %d KB Playkeeper reads", maxConfig>>10))
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if opened, err := f.Stat(); err != nil || !os.SameFile(fi, opened) {
		return nil, reason("it changed while Playkeeper read it")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxConfig+1))
	if err == nil && len(b) > maxConfig {
		err = reason(fmt.Sprintf("it is larger than the %d KB Playkeeper reads", maxConfig>>10))
	}
	return b, err
}

// writeConfig replaces name through a hidden temporary file beside it, so
// the add-on never reads half a file.
func writeConfig(root *os.Root, name string, b []byte, o *addons.Owner) error {
	tmp := path.Join(path.Dir(name), "."+path.Base(name)+".playkeeper-"+randomHex())
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = chown(root, tmp, o)
	}
	if err != nil {
		return err
	}
	return root.Rename(tmp, name)
}

func chown(root *os.Root, name string, o *addons.Owner) error {
	if o == nil {
		return nil
	}
	return root.Lchown(name, o.UID, o.GID)
}

func randomHex() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func configError(name string, err error) *addons.Error {
	msg := fmt.Sprintf("Playkeeper could not write voice chat's settings to %s.", name)
	var r reason
	if errors.As(err, &r) {
		msg = fmt.Sprintf("Playkeeper could not write voice chat's settings to %s: %s.", name, r)
	}
	return &addons.Error{Notice: notice(KindConfigUnusable, kv("file", name), msg,
		"Make sure the file and the folders above it are normal files and folders in the server's files, not links, then try again."), Err: err}
}

func openPortStep(name string, p Publish) addons.Notice {
	proto := strings.ToUpper(p.Protocol)
	return notice(KindOpenPort, kv("name", name, "port", strconv.Itoa(p.Port), "protocol", p.Protocol),
		fmt.Sprintf("%s uses %s port %d. If your hosting provider has a firewall in its control panel, allow %s port %d there.", name, proto, p.Port, proto, p.Port),
		name+" does not work for players until the port is open.")
}

func voiceChatClientStep() addons.Notice {
	const url = "https://modrinth.com/mod/simple-voice-chat"
	return notice(KindClientMod, kv("name", "Simple Voice Chat", "url", url),
		"Each player who wants to talk also installs Simple Voice Chat in their own game. Players without it can still join and play.",
		"Simple Voice Chat is on Modrinth for Fabric, Quilt, NeoForge and Forge: "+url)
}

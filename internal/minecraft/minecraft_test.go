package minecraft

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestParseRecognisesPlayerEvents(t *testing.T) {
	cases := []struct {
		line   string
		kind   EventKind
		player string
	}{
		{"[13:35:18 INFO]: PkSpikeBot joined the game", EventJoin, "PkSpikeBot"},
		{"[13:35:21 INFO]: PkSpikeBot left the game", EventLeave, "PkSpikeBot"},
		{"[13:35:18] [Server thread/INFO]: PkBotBuilder joined the game", EventJoin, "PkBotBuilder"},
		{"[13:35:15 INFO]: UUID of player PkSpikeBot is 5507140b-cf95-3383-b75a-47dd34196981", EventUUID, "PkSpikeBot"},
		{`[13:34:35 INFO]: Done (11.903s)! For help, type "help"`, EventReady, ""},
		{"[13:40:00 INFO]: Stopping server", EventStopping, ""},
		{"[13:34:20 INFO]: Starting minecraft server version 26.1.2", EventStarting, ""},
		{`[13:34:28 INFO]: Preparing level "world"`, EventPreparing, ""},
		{"[20:38:30] [Server thread/INFO] [minecraft/MinecraftServer]: PkBotBuilder joined the game", EventJoin, "PkBotBuilder"},
		{"[20:38:41] [Server thread/INFO] [minecraft/MinecraftServer]: PkBotBuilder left the game", EventLeave, "PkBotBuilder"},
		{`[20:38:10] [Server thread/INFO] [minecraft/DedicatedServer]: Done (3.007s)! For help, type "help"`, EventReady, ""},
		{"[20:38:07] [Server thread/INFO] [minecraft/DedicatedServer]: Starting minecraft server version 26.2", EventStarting, ""},
		{`[20:38:07] [Server thread/INFO] [minecraft/DedicatedServer]: Preparing level "world"`, EventPreparing, ""},
		{"[init] [ERROR] Failed to download paper", EventInitError, ""},
		{"java.lang.OutOfMemoryError: Java heap space", EventOOM, ""},
	}
	for _, c := range cases {
		p := Parse(c.line)
		if p.Kind != c.kind || p.Player != c.player {
			t.Errorf("Parse(%q) = %+v, want kind %q player %q", c.line, p, c.kind, c.player)
		}
	}
}

// Player-controlled text must never produce join/leave events.
func TestParseIgnoresSpoofedChat(t *testing.T) {
	spoofs := []string{
		"[13:35:18 INFO]: <PkBotFriend> Foo joined the game",
		"[13:35:18 INFO]: [Not Secure] <PkBotFriend> Foo joined the game",
		"[13:35:18 INFO]: [PkBotFriend] Foo joined the game",
		"[13:35:18 INFO]: * PkBotFriend Foo left the game",
		"[13:35:18 INFO]: [Server] Foo joined the game",
		"[13:35:18 INFO]: Foo joined the game!",
		"[13:35:18 INFO]: Foo Bar joined the game",
		"Foo joined the game",
		"[13:35:18 INFO]: PkBotFriend issued server command: /say Foo joined the game",
		"[13:35:18 INFO]: ThisNameIsWayTooLongForMinecraft joined the game",
		"[20:38:30] [Server thread/INFO] [minecraft/MinecraftServer]: <PkBotFriend> Foo joined the game",
		"[20:38:30] [Server thread/INFO] [minecraft/MinecraftServer]: [PkBotFriend] [minecraft/MinecraftServer]: Foo joined the game",
		"[20:38:30] [Server thread/INFO] [minecraft/MinecraftServer]: <PkBotFriend> [a/b]: Foo left the game",
		"[20:38:30] [minecraft/MinecraftServer]: Foo joined the game",
	}
	for _, s := range spoofs {
		if p := Parse(s); p.Kind == EventJoin || p.Kind == EventLeave {
			t.Errorf("spoofed line produced %s event for %q: %q", p.Kind, p.Player, s)
		}
	}
}

func TestRedactIPs(t *testing.T) {
	in := "[13:35:18 INFO]: PkSpikeBot[/172.18.0.1:50284] logged in with entity id 10; Thread RCON Client /10.0.0.5 started; v6 /[2001:db8::1]:25565; Paper 26.1.2 build 74"
	out := RedactIPs(in)
	for _, leak := range []string{"172.18.0.1", "50284", "10.0.0.5", "2001:db8::1"} {
		if strings.Contains(out, leak) {
			t.Fatalf("redacted line still contains %q: %s", leak, out)
		}
	}
	if !strings.Contains(out, "26.1.2 build 74") {
		t.Fatalf("version numbers must survive redaction: %s", out)
	}
}

func TestCleanLineStripsANSI(t *testing.T) {
	if got := CleanLine("\x1b[1;31m[mc-image-helper] ERROR\x1b[0;39m"); got != "[mc-image-helper] ERROR" {
		t.Fatalf("got %q", got)
	}
}

func TestValidPlayerName(t *testing.T) {
	for _, ok := range []string{"PkBotBuilder", "PkBotFriend", "abc", "A_b_1234567890123"[:16]} {
		if !ValidPlayerName(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "ab", "has space", "semi;id", "$(id)", "`id`", "../../x", "ThisNameIsTooLong17", "nämé"} {
		if ValidPlayerName(bad) {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestParseList(t *testing.T) {
	on, max, names, ok := ParseList("There are 2 of a max of 10 players online: PkBotBuilder, PkBotFriend")
	if !ok || on != 2 || max != 10 || len(names) != 2 || names[1] != "PkBotFriend" {
		t.Fatalf("got %d %d %v %v", on, max, names, ok)
	}
	on, _, names, ok = ParseList("There are 0 of a max of 20 players online: ")
	if !ok || on != 0 || len(names) != 0 {
		t.Fatalf("empty list parsed as %d %v %v", on, names, ok)
	}
	if _, _, _, ok := ParseList("Unknown command"); ok {
		t.Fatal("garbage must not parse")
	}
}

func TestMemoryBudgets(t *testing.T) {
	opts, rec, _ := MemoryOptions(2971)
	if len(opts) != 2 || opts[0] != 1536 || rec != 1536 {
		t.Fatalf("3 GB host: %v rec %d", opts, rec)
	}
	if opts, _, _ := MemoryOptions(2000); len(opts) != 0 {
		t.Fatalf("2 GB host should get no budgets, got %v", opts)
	}
	if !ValidBudget(2048, 4096) || ValidBudget(-1, 4096) || ValidBudget(99999, 4096) || ValidBudget(1000, 4096) {
		t.Fatal("budget validation wrong")
	}
	if HeapMB(1536) != 1024 || HeapMB(8192) != 6144 {
		t.Fatalf("heap: %d %d", HeapMB(1536), HeapMB(8192))
	}
}

func TestImageAndKnownBuildsArePinned(t *testing.T) {
	if !strings.Contains(Image, "@sha256:") {
		t.Fatalf("image must be pinned by digest: %s", Image)
	}
	if sum, ok := KnownJarSHA256("26.1.2", 74); !ok || len(sum) != 64 {
		t.Fatalf("0.1.0's build of 26.1.2 must keep its checksum: %q %v", sum, ok)
	}
	if _, ok := KnownJarSHA256("26.1.2", 75); ok {
		t.Fatal("another build must not borrow a known checksum")
	}
}

// fakeRCON serves the RCON protocol and, like Paper, drops the connection if
// a second packet arrives in the same read (no pipelining).
func fakeRCON(t *testing.T, password string, reply func(cmd string) string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				authed := false
				buf := make([]byte, 8192)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					data := buf[:n]
					size := int(binary.LittleEndian.Uint32(data))
					if len(data) != size+4 {
						return // pipelined or partial packet: behave like Paper and hang up
					}
					id := int32(binary.LittleEndian.Uint32(data[4:]))
					typ := int32(binary.LittleEndian.Uint32(data[8:]))
					body := string(bytes.TrimRight(data[12:], "\x00"))
					send := func(id int32, typ int32, s string) {
						p := make([]byte, 14+len(s))
						binary.LittleEndian.PutUint32(p, uint32(10+len(s)))
						binary.LittleEndian.PutUint32(p[4:], uint32(id))
						binary.LittleEndian.PutUint32(p[8:], uint32(typ))
						copy(p[12:], s)
						c.Write(p)
					}
					switch {
					case typ == 3 && body == password:
						authed = true
						send(id, 2, "")
					case typ == 3:
						send(-1, 2, "")
					case typ == 2 && authed:
						out := reply(body)
						for len(out) > 4096 {
							send(id, 0, out[:4096])
							out = out[4096:]
						}
						send(id, 0, out)
					default:
						send(id, 0, "Unknown request c8")
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String()
}

func TestRCONCommands(t *testing.T) {
	addr := fakeRCON(t, "secret", func(cmd string) string {
		if cmd == "big" {
			return strings.Repeat("x", 9000)
		}
		return "ran: " + cmd
	})
	if _, err := DialRCON(addr, "wrong", time.Second); err != ErrRCONAuth {
		t.Fatalf("wrong password: got %v, want ErrRCONAuth", err)
	}
	r, err := DialRCON(addr, "secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for i := 0; i < 3; i++ {
		out, err := r.Command(";id", time.Second)
		if err != nil || out != "ran: ;id" {
			t.Fatalf("command %d: %q %v", i, out, err)
		}
	}
	out, err := r.Command("big", time.Second)
	if err != nil || len(out) != 9000 {
		t.Fatalf("multi-packet reply: len %d err %v", len(out), err)
	}
	if _, err := r.Command(strings.Repeat("a", 2000), time.Second); err == nil {
		t.Fatal("oversized command must be refused")
	}
}

func TestPing(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 512)
		c.Read(buf)
		status, _ := json.Marshal(map[string]any{
			"version": map[string]any{"name": "Paper 26.1.2", "protocol": 775},
			"players": map[string]any{"max": 10, "online": 1, "sample": []map[string]string{{"name": "PkBotFriend", "id": "x"}}},
		})
		var body bytes.Buffer
		writeVarInt(&body, 0)
		writeVarInt(&body, int32(len(status)))
		body.Write(status)
		var pkt bytes.Buffer
		writeVarInt(&pkt, int32(body.Len()))
		pkt.Write(body.Bytes())
		c.Write(pkt.Bytes())
		io.Copy(io.Discard, c)
	}()
	st, err := Ping(ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if st.Online != 1 || st.Max != 10 || st.Protocol != 775 || len(st.Sample) != 1 {
		t.Fatalf("got %+v", st)
	}
}

func TestParseTPS(t *testing.T) {
	for in, want := range map[string]float64{
		"§6TPS from last 1m, 5m, 15m: §a*20.0, §a20.0, §a20.0": 20,
		"TPS from last 1m, 5m, 15m: 17.42, 18.1, 19.9":         17.42,
	} {
		if got, ok := ParseTPS(in); !ok || got != want {
			t.Errorf("ParseTPS(%q) = %v %v, want %v", in, got, ok, want)
		}
	}
	if _, ok := ParseTPS("Unknown command. Type \"/help\" for help."); ok {
		t.Error("a server without the tps command has no tick rate")
	}
}

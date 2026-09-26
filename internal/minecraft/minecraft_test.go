package minecraft

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
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
		{"[03:11:30 ERROR]: Encountered an unexpected exception", EventCrashed, ""},
		{"[03:11:31 ERROR]: This crash report has been saved to: /data/./crash-reports/crash-2026-09-25_03.11.31-server.txt", EventCrashed, ""},
		{"[21:40:12 ERROR]: The server has stopped responding! This is (probably) not a Paper bug.", EventCrashed, ""},
		{"[12:00:00] [Server Watchdog/FATAL]: A single server tick took 60.00 seconds (should be max 0.05)", EventCrashed, ""},
		{"[12:00:00] [main/ERROR] [minecraft/Main]: Failed to start the minecraft server", EventCrashed, ""},
		{"[03:11:30 INFO]: Encountered an unexpected exception", EventNone, ""},
		{"[03:11:30 ERROR]: <PkBotFriend> Encountered an unexpected exception", EventNone, ""},
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

// Addresses go wherever they appear; four-part version numbers, like
// NeoForge's (real lines from a NeoForge 26.2.0.88 server), stay readable.
func TestRedactIPsKeepsVersionNumbers(t *testing.T) {
	for in, want := range map[string]string{
		"[mc-image-helper] 00:32:24.817 INFO  : Running NeoForge 26.2.0.88 installer for Minecraft 26.2. This might take a while...": "",
		"\t\tNeoForge 26.2.0.88 (neoforge)":  "",
		"\t\tWaystones 21.1.0.4 (waystones)": "",
		"[00:32:33] [main/INFO] [ne.ne.fm.lo.FMLLoader/]:  - ~libraries/net/neoforged/neoforge/26.2.0.88/neoforge-26.2.0.88-universal.jar > net.neoforged.neoforge-coremods-26.2.0.88.jar": "",
		" - minecraft (jar(~libraries/net/neoforged/minecraft-server-patched/26.2-20260902.101010/minecraft-server-patched-26.2-20260902.101010.jar))":                                     "",
		"[00:32:37] [modloading-worker-0/INFO] [ne.ne.ne.co.NeoForgeMod/NEOFORGE-MOD]: NeoForge mod loading, version 26.2.0.88, for MC 26.2":                                               "",
		"Loading Minecraft 1.21.4 with Fabric Loader 0.16.10":                                                     "",
		"[12:00:00 INFO]: PkSpikeBot[/203.0.113.7:50284] logged in with entity id 10 at ([world]1.5, 64.0, -3.5)": "[12:00:00 INFO]: PkSpikeBot[/[ip redacted]] logged in with entity id 10 at ([world]1.5, 64.0, -3.5)",
		"[12:00:00 INFO]: Thread RCON Client /10.0.0.5 started":                                                   "[12:00:00 INFO]: Thread RCON Client /[ip redacted] started",
		"[12:00:00 INFO]: Disconnecting PkSpikeBot (/192.0.2.44:61000): Timed out":                                "[12:00:00 INFO]: Disconnecting PkSpikeBot (/[ip redacted]): Timed out",
		"[12:00:00 INFO]: [AuthMe] PkSpikeBot logged in from 198.51.100.23":                                       "[12:00:00 INFO]: [AuthMe] PkSpikeBot logged in from [ip redacted]",
		"[12:00:00 INFO]: Connection from 203.0.113.9:1234, 198.51.100.8 and 192.0.2.1.":                          "[12:00:00 INFO]: Connection from [ip redacted], [ip redacted] and [ip redacted].",
		"[12:00:00 INFO]: PkSpikeBot (/[2001:db8::1]:25565) lost connection":                                      "[12:00:00 INFO]: PkSpikeBot (/[ip redacted]) lost connection",
	} {
		if want == "" {
			want = in
		}
		if got := RedactIPs(in); got != want {
			t.Errorf("RedactIPs(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

// Plugins colour their command replies and log lines with § codes, hex
// colours included (a real Paper 26.2 `plugins` reply).
func TestCleanLineStripsColourCodes(t *testing.T) {
	in := "§x§3§4§9§f§d§aℹ §fServer Plugins (4): | §x§e§d§8§1§0§6Bukkit Plugins: | §8- §aChunky§r, §aViaBackwards§r, §AViaRewind§R, §aViaVersion"
	want := "ℹ Server Plugins (4): | Bukkit Plugins: | - Chunky, ViaBackwards, ViaRewind, ViaVersion"
	if got := CleanLine(in); got != want {
		t.Fatalf("got %q", got)
	}
	if got := CleanLine("§f§oChecking version, please wait..."); got != "Checking version, please wait..." {
		t.Fatalf("got %q", got)
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

// A mod loader keeps more memory outside the heap than Paper, more with
// each mod, and never more than half the budget; Paper, Purpur and Vanilla
// keep the heap they always had, so their containers don't change.
func TestHeapForModLoaders(t *testing.T) {
	budgets, _, _ := MemoryOptions(64 << 10)
	for _, b := range budgets {
		for _, typ := range []string{"paper", "purpur", "vanilla", ""} {
			if got := HeapFor(b, typ, 40); got != HeapMB(b) {
				t.Errorf("%s at %d MB: heap %d, want the %d it always had", typ, b, got, HeapMB(b))
			}
		}
		for _, typ := range []string{"fabric", "quilt", "neoforge", "forge"} {
			none, some, many := HeapFor(b, typ, 0), HeapFor(b, typ, 17), HeapFor(b, typ, 400)
			if none > HeapMB(b) || some > none || many > some || many < b/2 {
				t.Errorf("%s at %d MB: heap %d with no mods, %d with 17, %d with 400; Paper gets %d", typ, b, none, some, many, HeapMB(b))
			}
		}
	}
	for _, c := range []struct {
		typ          string
		budget, mods int
		want         int
	}{
		// The Quilt server a player's join got killed: 2 GB, Chunky and Fabric API.
		{"quilt", 2048, 2, 2048 - 780},
		{"fabric", 2048, 17, 2048 - 870},
		{"neoforge", 2048, 0, 1024},
		{"neoforge", 3072, 1, 3072 - 1030},
		{"fabric", 4096, 12, 3072},
		{"neoforge", 6144, 150, 6144 - 1924},
		// A Forge server keeps as much outside the heap as NeoForge.
		{"forge", 2048, 0, 1024},
		{"forge", 3072, 1, 3072 - 1030},
		{"forge", 6144, 150, 6144 - 1924},
	} {
		if got := HeapFor(c.budget, c.typ, c.mods); got != c.want {
			t.Errorf("%s at %d MB with %d mods: heap %d, want %d", c.typ, c.budget, c.mods, got, c.want)
		}
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

// Replies with these prefixes make fakeRCON hang up: without replying (the
// reply is lost), or right after replying (the connection goes stale).
const (
	hangUp          = "\x00hang up"
	replyThenHangUp = "\x00reply then hang up:"
)

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
						if out == hangUp {
							return
						}
						after, closeAfter := strings.CutPrefix(out, replyThenHangUp)
						if closeAfter {
							out = after
						}
						for len(out) > 4096 {
							send(id, 0, out[:4096])
							out = out[4096:]
						}
						send(id, 0, out)
						if closeAfter {
							return
						}
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
	if _, err := r.Command(strings.Repeat("a", 2000), time.Second); err == nil || !errors.Is(err, ErrNotSent) {
		t.Fatalf("oversized command must be refused before sending: %v", err)
	}
}

// A command is written at most once, and the caller learns whether it may
// have run: resending save-on after a lost reply would read as saving turned
// back on by someone else.
// The connection's timeout can arrive a moment before its context reports
// the deadline. It still counts as the deadline, while a timeout before the
// deadline, or another error at it, stays what it is.
func TestRCONTimeoutAtTheDeadlineCountsAsTheDeadline(t *testing.T) {
	r := &RCON{}
	passed := lateCtx{Context: context.Background(), d: time.Now().Add(-time.Millisecond)}
	if err := r.ctxErr(passed, timeoutErr{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a timeout at the deadline: got %v", err)
	}
	ahead := lateCtx{Context: context.Background(), d: time.Now().Add(time.Hour)}
	if err := r.ctxErr(ahead, timeoutErr{}); errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a timeout before the deadline: got %v", err)
	}
	if err := r.ctxErr(passed, io.EOF); !errors.Is(err, io.EOF) {
		t.Fatalf("a closed connection at the deadline: got %v", err)
	}
}

// lateCtx has a deadline but doesn't report itself done, like a context
// whose timer hasn't fired yet.
type lateCtx struct {
	context.Context
	d time.Time
}

func (c lateCtx) Deadline() (time.Time, bool) { return c.d, true }
func (c lateCtx) Err() error                  { return nil }

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestRCONCommandContextNeverResendsAndHonoursTheContext(t *testing.T) {
	var mu sync.Mutex
	var got []string
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	addr := fakeRCON(t, "secret", func(cmd string) string {
		mu.Lock()
		got = append(got, cmd)
		mu.Unlock()
		switch cmd {
		case "lost":
			return hangUp
		case "last":
			return replyThenHangUp + "bye"
		case "slow":
			<-block
		}
		return "ran: " + cmd
	})
	dial := func() *RCON {
		t.Helper()
		r, err := DialRCONContext(context.Background(), addr, "secret")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.Close() })
		return r
	}
	count := func(cmd string) int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, c := range got {
			if c == cmd {
				n++
			}
		}
		return n
	}

	r := dial()
	if _, err := r.CommandContext(context.Background(), "lost"); err == nil || errors.Is(err, ErrNotSent) {
		t.Fatalf("a lost reply must say the command may have run, got %v", err)
	}
	if count("lost") != 1 {
		t.Fatalf("the command reached the server %d times, want once", count("lost"))
	}

	r = dial()
	if out, err := r.CommandContext(context.Background(), "last"); err != nil || out != "bye" {
		t.Fatalf("got %q %v", out, err)
	}
	// The fake hangs up just after its reply. Wait until this side can see
	// that rather than for a fixed time, which a busy machine can outlast.
	for deadline := time.Now().Add(5 * time.Second); r.stale() == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the server's hang-up never reached this side")
		}
	}
	if _, err := r.CommandContext(context.Background(), "after"); !errors.Is(err, ErrNotSent) {
		t.Fatalf("a connection the server closed must be noticed before writing, got %v", err)
	}
	if count("after") != 0 {
		t.Fatal("nothing may be written to a connection the server closed")
	}

	r = dial()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := r.CommandContext(ctx, "slow")
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrNotSent) {
		t.Fatalf("an expired deadline: got %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("the deadline was not honoured: %s", d)
	}

	r = dial()
	ctx, cancel = context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start = time.Now()
	if _, err := r.CommandContext(ctx, "slow"); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled context: got %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("cancellation was not honoured: %s", d)
	}
	if _, err := r.CommandContext(ctx, "never"); !errors.Is(err, ErrNotSent) {
		t.Fatalf("a command with a context already done is not sent, got %v", err)
	}
	if count("never") != 0 {
		t.Fatal("a command with a context already done reached the server")
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

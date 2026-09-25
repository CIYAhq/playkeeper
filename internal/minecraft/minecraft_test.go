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
	time.Sleep(50 * time.Millisecond)
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

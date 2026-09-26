package sleep

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

const wakingMsg = "Survival is waking up. Join again in about 30 seconds."

func TestStatusShowsTheSleepingServer(t *testing.T) {
	m, wakes := newManager(t, nil)
	c := dial(t, m.Addr())
	if _, err := c.Write(append(handshakePkt(767, "play.example.com", 25565, 1), pkt(0x00)...)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	id, data, err := readPacket(br)
	if err != nil || id != 0x00 {
		t.Fatalf("status response: id %d, %v", id, err)
	}
	s, _ := readTestString(data)
	var doc statusDoc
	if err := json.Unmarshal([]byte(s), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version.Protocol != 767 || doc.Version.Name != "Paper 26.2" || doc.Players.Max != 20 || doc.Players.Online != 0 {
		t.Fatalf("status %s", s)
	}
	if got := doc.Description.plain(); got != "Survival\nAsleep · join to wake it" {
		t.Fatalf("description %q", got)
	}
	if doc.Favicon != iconURI(t) {
		t.Fatalf("favicon %.40q…", doc.Favicon)
	}

	payload := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	if _, err := c.Write(pkt(0x01, payload)); err != nil {
		t.Fatal(err)
	}
	id, data, err = readPacket(br)
	if err != nil || id != 0x01 || !bytes.Equal(data, payload) {
		t.Fatalf("pong: id %d, %x, %v", id, data, err)
	}
	expectClosed(t, c, time.Second)
	expectNoWake(t, wakes)
}

func TestTheAgentsPingSeesTheServersProtocol(t *testing.T) {
	m, _ := newManager(t, nil)
	st, err := minecraft.Ping(m.Addr(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if st.Protocol != 775 || st.VersionName != "Paper 26.2" || st.Max != 20 || st.Online != 0 {
		t.Fatalf("got %+v", st)
	}
}

func TestPingWithoutAStatusRequest(t *testing.T) {
	m, _ := newManager(t, nil)
	c := dial(t, m.Addr())
	payload := []byte{9, 9, 9, 9, 0, 0, 0, 1}
	if _, err := c.Write(append(handshakePkt(767, "localhost", 25565, 1), pkt(0x01, payload)...)); err != nil {
		t.Fatal(err)
	}
	id, data, err := readPacket(bufio.NewReader(c))
	if err != nil || id != 0x01 || !bytes.Equal(data, payload) {
		t.Fatalf("pong: id %d, %x, %v", id, data, err)
	}
}

func TestJoiningWakesTheServerOnce(t *testing.T) {
	m, wakes := newManager(t, func(c *Config) { c.MaxConnsPerIP = 64 })
	if got := mustJoin(t, m.Addr(), "Steve"); got != wakingMsg {
		t.Fatalf("got %q", got)
	}
	expectWake(t, wakes, "Steve")

	var wg sync.WaitGroup
	for i := range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := join(m.Addr(), fmt.Sprintf("Player%d", i), 2)
			if err != nil || got != wakingMsg {
				t.Errorf("player %d: %q, %v", i, got, err)
			}
		}()
	}
	wg.Wait()
	expectNoWake(t, wakes)
}

func TestTransferCountsAsJoining(t *testing.T) {
	m, wakes := newManager(t, nil)
	got, err := join(m.Addr(), "Alex", 3)
	if err != nil || got != wakingMsg {
		t.Fatalf("got %q, %v", got, err)
	}
	expectWake(t, wakes, "Alex")
}

// guardedMsg is what every valid name reads when Admit guards wakes.
const guardedMsg = "Survival is asleep. If you're on the list, it's waking up: join again in about 30 seconds."

// TestRepliesNeverRevealTheWhitelist is the L9 regression test: listed and
// unlisted names read exactly the same reply, before, during and after a
// wake, and only listed names wake the server.
func TestRepliesNeverRevealTheWhitelist(t *testing.T) {
	clock := &testClock{t: t0}
	var mu sync.Mutex
	var asked []string
	m, wakes := newManager(t, func(c *Config) {
		c.Now = clock.Now
		c.Admit = func(p string) bool {
			mu.Lock()
			asked = append(asked, p)
			mu.Unlock()
			return p == "Alex" || p == "JunoFox"
		}
	})
	replies := map[string]string{}
	for _, name := range []string{"Mallory", "Eve_2", "Steve"} {
		replies[name] = mustJoin(t, m.Addr(), name)
	}
	expectNoWake(t, wakes)
	replies["Alex"] = mustJoin(t, m.Addr(), "Alex")
	expectWake(t, wakes, "Alex")
	clock.add(time.Minute)
	for _, name := range []string{"Mallory", "JunoFox"} {
		replies[name+" while waking"] = mustJoin(t, m.Addr(), name)
	}
	expectNoWake(t, wakes)
	for who, got := range replies {
		if got != guardedMsg {
			t.Errorf("%s read %q, want the same reply as everyone: %q", who, got, guardedMsg)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"Mallory", "Eve_2", "Steve", "Alex"}; strings.Join(asked, ",") != strings.Join(want, ",") {
		t.Errorf("Admit was asked about %v, want %v: nobody is asked while a wake is under way", asked, want)
	}
}

func TestHeldBackWakesReadTheSameForEveryName(t *testing.T) {
	clock := &testClock{t: t0}
	m, wakes := newManager(t, func(c *Config) {
		c.Now = clock.Now
		c.Admit = func(p string) bool { return p != "Mallory" }
	})
	for i := range maxWakesPerHour {
		name := fmt.Sprintf("Player%d", i)
		mustJoin(t, m.Addr(), name)
		expectWake(t, wakes, name)
		clock.add(3 * time.Minute)
	}
	listed, unlisted := mustJoin(t, m.Addr(), "Steve"), mustJoin(t, m.Addr(), "Mallory")
	if listed != unlisted || !strings.Contains(listed, "too often") {
		t.Fatalf("listed %q, unlisted %q: want the same held-back reply", listed, unlisted)
	}
	expectNoWake(t, wakes)
}

func TestInvalidNamesDoNotWakeTheServer(t *testing.T) {
	m, wakes := newManager(t, nil)
	for _, name := range []string{"ab", "has space", "semi;colon", "Ünïcode"} {
		if got := mustJoin(t, m.Addr(), name); got != "That player name isn't valid." {
			t.Errorf("%q: got %q", name, got)
		}
	}
	if _, err := join(m.Addr(), strings.Repeat("a", 17), 2); err == nil {
		t.Error("a 17-character name got a reply")
	}
	expectNoWake(t, wakes)
}

func TestWakesAreRateLimited(t *testing.T) {
	clock := &testClock{t: t0}
	m, wakes := newManager(t, func(c *Config) { c.Now = clock.Now })
	for i := range maxWakesPerHour {
		name := fmt.Sprintf("Player%d", i)
		if got := mustJoin(t, m.Addr(), name); got != wakingMsg {
			t.Fatalf("wake %d: %q", i+1, got)
		}
		expectWake(t, wakes, name)
		clock.add(time.Minute)
		if got := mustJoin(t, m.Addr(), "Late"+name); got != wakingMsg {
			t.Fatalf("during wake %d: %q", i+1, got)
		}
		expectNoWake(t, wakes)
		clock.add(2 * time.Minute)
	}
	want := "Survival has woken up too often in the last hour. Try again in about 42 minutes."
	if got := mustJoin(t, m.Addr(), "Steve"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	expectNoWake(t, wakes)
	clock.add(42 * time.Minute)
	if got := mustJoin(t, m.Addr(), "Steve"); got != wakingMsg {
		t.Fatalf("an hour after the first wake: %q", got)
	}
	expectWake(t, wakes, "Steve")
}

func TestLegacyPings(t *testing.T) {
	m, _ := newManager(t, nil)
	modern := "§1\x00127\x00Paper 26.2\x00Survival - Asleep · join to wake it\x000\x0020"
	cases := []struct {
		name string
		send []byte
		want string
	}{
		{"beta 1.8 to 1.3", []byte{0xFE}, "Survival - Asleep · join to wake it§0§20"},
		{"1.4 and 1.5", []byte{0xFE, 0x01}, modern},
		{"1.6", legacy16Ping("play.example.com", 25565), modern},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conn := dial(t, m.Addr())
			if _, err := conn.Write(c.send); err != nil {
				t.Fatal(err)
			}
			got, err := readLegacy(conn)
			if err != nil || got != c.want {
				t.Fatalf("got %q, %v, want %q", got, err, c.want)
			}
			expectClosed(t, conn, time.Second)
		})
	}
}

// legacy16Ping is what a 1.6 client sends: 0xFE 0x01, then an MC|PingHost
// plugin message with its protocol, the host and the port.
func legacy16Ping(host string, port int32) []byte {
	u16 := func(s string) []byte {
		var b []byte
		for _, c := range utf16.Encode([]rune(s)) {
			b = binary.BigEndian.AppendUint16(b, c)
		}
		return b
	}
	channel := "MC|PingHost"
	b := []byte{0xFE, 0x01, 0xFA}
	b = binary.BigEndian.AppendUint16(b, uint16(len(channel)))
	b = append(b, u16(channel)...)
	b = binary.BigEndian.AppendUint16(b, uint16(7+2*len(host)))
	b = append(b, 78)
	b = binary.BigEndian.AppendUint16(b, uint16(len(host)))
	b = append(b, u16(host)...)
	return binary.BigEndian.AppendUint32(b, uint32(port))
}

func TestAHandshakeThatLooksLikeALegacyPing(t *testing.T) {
	m, _ := newManager(t, nil)
	hs := handshakePkt(767, strings.Repeat("h", 246), 25565, 1)
	if hs[0] != 0xFE || hs[1] != 0x01 || hs[2] != 0x00 {
		t.Fatalf("handshake starts %x, want fe0100", hs[:3])
	}
	c := dial(t, m.Addr())
	if _, err := c.Write(append(hs, pkt(0x00)...)); err != nil {
		t.Fatal(err)
	}
	id, _, err := readPacket(bufio.NewReader(c))
	if err != nil || id != 0x00 {
		t.Fatalf("status response: id %d, %v", id, err)
	}
}

func TestMalformedPacketsAreDropped(t *testing.T) {
	m, wakes := newManager(t, nil)
	status := handshakePkt(767, "localhost", 25565, 1)
	login := handshakePkt(767, "localhost", 25565, 2)
	cases := []struct {
		name string
		send []byte
	}{
		{"length VarInt over three bytes", []byte{0x80, 0x80, 0x80, 0x01}},
		{"zero length", []byte{0x00}},
		{"longer than any handshake", varint(2000)},
		{"largest possible length", []byte{0xff, 0xff, 0x7f}},
		{"wrong packet id", pkt(0x05, varint(767), mcString("localhost"), []byte{0x63, 0xdd}, varint(1))},
		{"negative host length", pkt(0x00, varint(767), varint(-1))},
		{"host length past the packet", pkt(0x00, varint(767), varint(200), []byte("abc"))},
		{"host over 255 characters", handshakePkt(767, strings.Repeat("a", 256), 25565, 1)},
		{"intent 0", handshakePkt(767, "localhost", 25565, 0)},
		{"intent 4", handshakePkt(767, "localhost", 25565, 4)},
		{"negative intent", handshakePkt(767, "localhost", 25565, -1)},
		{"trailing byte in handshake", pkt(0x00, varint(767), mcString("localhost"), []byte{0x63, 0xdd}, varint(1), []byte{0})},
		{"status request with data", append(status, pkt(0x00, []byte{1})...)},
		{"short ping", append(status, pkt(0x01, []byte{1, 2, 3})...)},
		{"unknown status packet", append(status, pkt(0x02)...)},
		{"name over 16 characters", append(login, loginStartPkt(strings.Repeat("a", 17))...)},
		{"name not UTF-8", append(login, pkt(0x00, varint(3), []byte{0xff, 0xfe, 0xfd})...)},
		{"login start over 8 KiB", append(login, varint(9000)...)},
		{"wrong login packet id", append(login, pkt(0x01, mcString("Steve"))...)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conn := dial(t, m.Addr())
			_, _ = conn.Write(c.send)
			expectClosed(t, conn, time.Second)
		})
	}
	t.Run("packet cut short", func(t *testing.T) {
		conn := dial(t, m.Addr())
		_, _ = conn.Write(status[:5])
		_ = conn.(*net.TCPConn).CloseWrite()
		expectClosed(t, conn, time.Second)
	})
	expectNoWake(t, wakes)
	if doc := queryStatus(t, m.Addr(), 767); doc.Version.Protocol != 767 {
		t.Fatalf("after malformed packets: %+v", doc)
	}
}

func TestSlowClientsAreCutOff(t *testing.T) {
	m, wakes := newManager(t, func(c *Config) { c.ConnTimeout = 300 * time.Millisecond })

	t.Run("silent", func(t *testing.T) {
		c := dial(t, m.Addr())
		start := time.Now()
		expectClosed(t, c, 2*time.Second)
		if d := time.Since(start); d < 250*time.Millisecond {
			t.Fatalf("closed after %v, before the timeout", d)
		}
	})

	t.Run("one byte at a time", func(t *testing.T) {
		c := dial(t, m.Addr())
		msg := append(handshakePkt(767, "localhost", 25565, 2), loginStartPkt("Steve")...)
		go func() {
			for _, b := range msg {
				if _, err := c.Write([]byte{b}); err != nil {
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
		}()
		start := time.Now()
		expectClosed(t, c, 2*time.Second)
		if d := time.Since(start); d < 250*time.Millisecond {
			t.Fatalf("closed after %v, before the timeout", d)
		}
	})
	expectNoWake(t, wakes)
}

func TestConnectionCaps(t *testing.T) {
	t.Run("per address", func(t *testing.T) {
		m, _ := newManager(t, func(c *Config) { c.MaxConnsPerIP = 2 })
		a, b := dial(t, m.Addr()), dial(t, m.Addr())
		_ = b
		expectClosed(t, dial(t, m.Addr()), time.Second)
		a.Close()
		waitForStatus(t, m.Addr())
	})
	t.Run("in total", func(t *testing.T) {
		m, _ := newManager(t, func(c *Config) { c.MaxConns = 2; c.MaxConnsPerIP = 10 })
		a, b := dial(t, m.Addr()), dial(t, m.Addr())
		_ = b
		expectClosed(t, dial(t, m.Addr()), time.Second)
		a.Close()
		waitForStatus(t, m.Addr())
	})
}

// waitForStatus retries a status ping until a connection slot is free.
func waitForStatus(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		st, err := minecraft.Ping(addr, time.Second)
		if err == nil && st.Max == 20 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no status after a connection closed: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAddrKey(t *testing.T) {
	cases := map[string]string{
		"203.0.113.9:5000":                  "203.0.113.9",
		"[::ffff:203.0.113.9]:5000":         "203.0.113.9",
		"[2001:db8:1:2:aaaa:bbbb:cc:d]:443": "2001:db8:1:2::",
		"[2001:db8:1:2::1]:443":             "2001:db8:1:2::",
		"[2001:db8:1:3::1]:443":             "2001:db8:1:3::",
	}
	for in, want := range cases {
		a, err := net.ResolveTCPAddr("tcp", in)
		if err != nil {
			t.Fatal(err)
		}
		if got := addrKey(a); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

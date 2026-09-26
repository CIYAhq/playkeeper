package sleep

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"sync"
	"testing"
	"time"
	"unicode/utf16"
)

// A minimal Minecraft client for the tests, written from the protocol
// description rather than from the package's own encoder.

func varint(v int32) []byte {
	var out []byte
	u := uint32(v)
	for {
		b := byte(u & 0x7F)
		if u >>= 7; u == 0 {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

func mcString(s string) []byte { return append(varint(int32(len(s))), s...) }

func pkt(id int32, fields ...[]byte) []byte {
	body := varint(id)
	for _, f := range fields {
		body = append(body, f...)
	}
	return append(varint(int32(len(body))), body...)
}

func handshakePkt(protocol int32, host string, port uint16, intent int32) []byte {
	return pkt(0x00, varint(protocol), mcString(host), binary.BigEndian.AppendUint16(nil, port), varint(intent))
}

func loginStartPkt(name string) []byte {
	return pkt(0x00, mcString(name), make([]byte, 16))
}

func readTestVarint(r io.ByteReader) (int32, error) {
	var v uint32
	for i := 0; i < 5; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		v |= uint32(b&0x7F) << (7 * i)
		if b&0x80 == 0 {
			return int32(v), nil
		}
	}
	return 0, errors.New("varint too long")
}

func readPacket(br *bufio.Reader) (int32, []byte, error) {
	n, err := readTestVarint(br)
	if err != nil {
		return 0, nil, err
	}
	if n <= 0 || n > 1<<20 {
		return 0, nil, fmt.Errorf("packet length %d", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(br, body); err != nil {
		return 0, nil, err
	}
	r := bytes.NewReader(body)
	id, err := readTestVarint(r)
	if err != nil {
		return 0, nil, err
	}
	rest, _ := io.ReadAll(r)
	return id, rest, nil
}

func readTestString(b []byte) (string, error) {
	r := bytes.NewReader(b)
	n, err := readTestVarint(r)
	if err != nil || int(n) != r.Len() {
		return "", fmt.Errorf("string of %d bytes in %d", n, r.Len())
	}
	return string(b[len(b)-int(n):]), nil
}

func dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	t.Cleanup(func() { c.Close() })
	return c
}

// join tries to join the way a game client does and returns the text of
// the disconnect message.
func join(addr, name string, intent int32) (string, error) {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write(append(handshakePkt(767, "localhost", 25565, intent), loginStartPkt(name)...)); err != nil {
		return "", err
	}
	id, data, err := readPacket(bufio.NewReader(c))
	if err != nil {
		return "", err
	}
	if id != 0x00 {
		return "", fmt.Errorf("packet id %d, want a login disconnect", id)
	}
	s, err := readTestString(data)
	if err != nil {
		return "", err
	}
	var text struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(s), &text); err != nil {
		return "", fmt.Errorf("disconnect reason %q: %w", s, err)
	}
	return text.Text, nil
}

func mustJoin(t *testing.T, addr, name string) string {
	t.Helper()
	msg, err := join(addr, name, 2)
	if err != nil {
		t.Fatalf("join as %s: %v", name, err)
	}
	return msg
}

// queryStatus does a server list ping and returns the status document.
func queryStatus(t *testing.T, addr string, protocol int32) statusDoc {
	t.Helper()
	c := dial(t, addr)
	if _, err := c.Write(append(handshakePkt(protocol, "localhost", 25565, 1), pkt(0x00)...)); err != nil {
		t.Fatal(err)
	}
	id, data, err := readPacket(bufio.NewReader(c))
	if err != nil || id != 0x00 {
		t.Fatalf("status response: id %d, %v", id, err)
	}
	s, err := readTestString(data)
	if err != nil {
		t.Fatal(err)
	}
	var doc statusDoc
	if err := json.Unmarshal([]byte(s), &doc); err != nil {
		t.Fatalf("status %q: %v", s, err)
	}
	return doc
}

// readLegacy reads a legacy kick packet and returns its text.
func readLegacy(r io.Reader) (string, error) {
	var head [3]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return "", err
	}
	if head[0] != 0xFF {
		return "", fmt.Errorf("packet %#x, want a kick", head[0])
	}
	u := make([]uint16, binary.BigEndian.Uint16(head[1:]))
	if err := binary.Read(r, binary.BigEndian, u); err != nil {
		return "", err
	}
	return string(utf16.Decode(u)), nil
}

// expectClosed checks that the stand-in closed the connection without
// sending anything, well before the connection's deadline.
func expectClosed(t *testing.T, c net.Conn, within time.Duration) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(within))
	buf := make([]byte, 64)
	n, err := c.Read(buf)
	if n > 0 {
		t.Fatalf("got %d bytes (%x), want the connection closed", n, buf[:n])
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("connection still open after %v", within)
	}
}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func iconPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(i * 7)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func iconURI(t *testing.T) string {
	return iconPrefix + base64.StdEncoding.EncodeToString(iconPNG(t, 64, 64))
}

// newManager starts a stand-in on a free loopback port.
func newManager(t *testing.T, mod func(*Config)) (*Manager, chan string) {
	t.Helper()
	wakes := make(chan string, 32)
	cfg := Config{
		Addr:        "127.0.0.1:0",
		Status:      Status{Name: "Survival", Version: "Paper 26.2", Protocol: 775, MaxPlayers: 20, Icon: iconURI(t)},
		OnWake:      func(p string) { wakes <- p },
		ConnTimeout: 3 * time.Second,
		BindTimeout: 2 * time.Second,
	}
	if mod != nil {
		mod(&cfg)
	}
	m, err := NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Listen(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m, wakes
}

func expectWake(t *testing.T, wakes <-chan string, player string) {
	t.Helper()
	select {
	case p := <-wakes:
		if p != player {
			t.Fatalf("woken by %q, want %q", p, player)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("OnWake was not called for %s", player)
	}
}

func expectNoWake(t *testing.T, wakes <-chan string) {
	t.Helper()
	select {
	case p := <-wakes:
		t.Fatalf("woken by %q, want no wake", p)
	case <-time.After(100 * time.Millisecond):
	}
}

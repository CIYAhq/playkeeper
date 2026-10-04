package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/usage"
)

// statusJSON is a server's status, with a player and a description the
// service never reads.
const statusJSON = `{"version":{"name":"Paper 26.1.2","protocol":775},"players":{"max":10,"online":1,"sample":[{"name":"Notch","id":"069a79f4-44e9-4726-a5be-fca90e38aaf5"}]},"description":{"text":"alice's survival world"}}`

// statusPacket is a status response holding js.
func statusPacket(js string) []byte {
	payload := appendVarInt(nil, 0)
	payload = appendVarInt(payload, int32(len(js)))
	payload = append(payload, js...)
	return append(appendVarInt(nil, int32(len(payload))), payload...)
}

// fakeServer answers each connection with answer, once it has read the
// request's first bytes, and keeps everything each connection sent until it
// closed.
type fakeServer struct {
	ln   net.Listener
	sent chan []byte
}

func startFake(t *testing.T, answer []byte) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeServer{ln: ln, sent: make(chan []byte, 8)}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(5 * time.Second))
				first := make([]byte, 1)
				if _, err := io.ReadFull(c, first); err != nil {
					f.sent <- nil
					return
				}
				if answer != nil {
					c.Write(answer)
				}
				rest, _ := io.ReadAll(c)
				f.sent <- append(first, rest...)
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return f
}

// dialTo has the service's checks dial to instead, keeping the addresses
// they meant to reach.
func (e *env) dialTo(to func(ctx context.Context) (net.Conn, error)) *[]string {
	var mu sync.Mutex
	var meant []string
	e.svc.reach.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		mu.Lock()
		meant = append(meant, network+" "+address)
		mu.Unlock()
		return to(ctx)
	}
	return &meant
}

func dialLocal(addr string) func(ctx context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) { return (&net.Dialer{}).DialContext(ctx, "tcp", addr) }
}

// check asks for a check of port, from the address from through the trusted
// proxy.
func (e *env) check(from string, port int) (int, usage.ReachAnswer) {
	e.t.Helper()
	w := e.do("POST", usage.PathReach, from, usage.ReachRequest{Port: port})
	var a usage.ReachAnswer
	json.Unmarshal(w.Body.Bytes(), &a)
	return w.Code, a
}

// A check connects back to the address the request came from, on the port
// it names, and to nothing else: the address a request names in
// X-Forwarded-For counts only when Coolify's proxy, which the service
// trusts, passed it on.
func TestACheckConnectsBackToTheRequestsOwnAddress(t *testing.T) {
	e := newEnv(t)
	fake := startFake(t, statusPacket(statusJSON))
	meant := e.dialTo(dialLocal(fake.ln.Addr().String()))
	if code, a := e.check("198.51.100.40", 25566); code != http.StatusOK || a.Result != usage.ReachReachable {
		t.Fatalf("a check through the proxy: %d %+v", code, a)
	}
	if code, a := e.check("2001:db8:40::7", 25565); code != http.StatusOK || a.Result != usage.ReachReachable {
		t.Fatalf("a check from an IPv6 address: %d %+v", code, a)
	}
	direct := func(remote, claimed string) int {
		req := httptest.NewRequest("POST", usage.PathReach, strings.NewReader(`{"port":25565}`))
		req.RemoteAddr = remote
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", claimed)
		w := httptest.NewRecorder()
		e.h.ServeHTTP(w, req)
		return w.Code
	}
	if code := direct("198.51.100.41:5000", "203.0.113.9"); code != http.StatusOK {
		t.Fatalf("a check straight from a machine that names another address: %d", code)
	}
	want := []string{"tcp 198.51.100.40:25566", "tcp [2001:db8:40::7]:25565", "tcp 198.51.100.41:25565"}
	if strings.Join(*meant, ", ") != strings.Join(want, ", ") {
		t.Errorf("the checks connected to %v, want %v", *meant, want)
	}
	// What it sent: the status handshake naming the address it reached,
	// then the status request, and nothing more.
	for _, to := range []netip.AddrPort{netip.MustParseAddrPort("198.51.100.40:25566"), netip.MustParseAddrPort("[2001:db8:40::7]:25565")} {
		if sent := <-fake.sent; !bytes.Equal(sent, statusRequest(to)) {
			t.Errorf("a check of %s sent %x, want only the status request %x", to, sent, statusRequest(to))
		}
	}
}

// A check says what it found: a Minecraft server, a closed port, a port
// nothing answers on, an address the network can't reach, or something else
// on the port.
func TestACheckSaysWhatItFound(t *testing.T) {
	e := newEnv(t)
	e.svc.reach.connect, e.svc.reach.answer = 100*time.Millisecond, 200*time.Millisecond
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := closed.Addr().String()
	closed.Close()
	silent := startFake(t, nil)
	other := startFake(t, []byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
	notJSON := startFake(t, statusPacket("Hello there"))
	for i, c := range []struct {
		name string
		to   func(ctx context.Context) (net.Conn, error)
		want string
	}{
		{"a Minecraft server", dialLocal(startFake(t, statusPacket(statusJSON)).ln.Addr().String()), usage.ReachReachable},
		{"a closed port", dialLocal(closedAddr), usage.ReachRefused},
		{"a dropped connection", func(ctx context.Context) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }, usage.ReachTimeout},
		{"an unreachable address", func(context.Context) (net.Conn, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH)}
		}, usage.ReachUnreachable},
		{"a server that never answers", dialLocal(silent.ln.Addr().String()), usage.ReachNotMinecraft},
		{"a web server", dialLocal(other.ln.Addr().String()), usage.ReachNotMinecraft},
		{"a status that isn't JSON", dialLocal(notJSON.ln.Addr().String()), usage.ReachNotMinecraft},
	} {
		e.dialTo(c.to)
		if code, a := e.check("198.51.100."+string(rune('0'+i)), 25565); code != http.StatusOK || a.Result != c.want {
			t.Errorf("%s: %d %+v, want %s", c.name, code, a, c.want)
		}
	}
}

// Only a Minecraft server's port can be checked, only with a request that
// names the port alone, and only at a public address: never one on a
// private network, the service's own machine, a cloud's metadata service or
// carrier-grade NAT. None of those are connected to.
func TestOnlyAMinecraftPortAtAPublicAddressIsChecked(t *testing.T) {
	e := newEnv(t)
	meant := e.dialTo(func(context.Context) (net.Conn, error) {
		t.Error("a refused check was connected")
		return nil, syscall.ECONNREFUSED
	})
	for _, body := range []string{`{"port":22}`, `{"port":8443}`, `{"port":25564}`, `{"port":26565}`, `{"port":"25565"}`, `{}`,
		`{"port":25565,"host":"example.com"}`, `{"port":25565,"address":"192.0.2.1"}`, `{"port":25565,"id":"0123456789abcdef0123456789abcdef"}`,
		`{"port":25565}{"port":25566}`, `{"port":25565}` + strings.Repeat(" ", maxReachBody)} {
		if w := e.do("POST", usage.PathReach, "198.51.100.50", body); w.Code != http.StatusBadRequest && w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	if w := e.do("POST", usage.PathReach, "198.51.100.50", nil, "Content-Type", "text/plain"); w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("a check that isn't JSON: %d", w.Code)
	}
	for _, from := range []string{"10.0.0.7", "172.16.4.4", "192.168.1.5", "127.0.0.1", "169.254.169.254", "100.64.1.1", "0.1.2.3", "192.0.0.9", "255.255.255.255",
		"::1", "fd00::1", "fe80::1", "::ffff:10.0.0.7", "64:ff9b::a00:7", "2002:a00:7::1", "2001:0:a00:7::1"} {
		w := e.do("POST", usage.PathReach, from, usage.ReachRequest{Port: 25565})
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"not_public"`) {
			t.Errorf("a check from %s: %d %s", from, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), from) {
			t.Errorf("the refusal of %s repeats the address: %s", from, w.Body)
		}
	}
	if len(*meant) != 0 {
		t.Errorf("refused checks connected to %v", *meant)
	}
	if w := e.do("GET", usage.PathReach, "198.51.100.50", nil); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET %s: %d", usage.PathReach, w.Code)
	}
}

// An address runs one check at a time, and a few an hour; and the service
// runs a bounded number at once.
func TestChecksAreOneAtATimeAndLimited(t *testing.T) {
	e := newEnv(t)
	hold, holding := make(chan struct{}), make(chan struct{})
	e.dialTo(func(ctx context.Context) (net.Conn, error) { return nil, syscall.ECONNREFUSED })
	refused := e.svc.reach.dial
	var held atomic.Bool
	e.svc.reach.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		if strings.HasPrefix(address, "198.51.100.60:") && held.CompareAndSwap(false, true) {
			holding <- struct{}{}
			<-hold
		}
		return refused(ctx, network, address)
	}
	done := make(chan int)
	go func() { code, _ := e.check("198.51.100.60", 25565); done <- code }()
	<-holding
	w := e.do("POST", usage.PathReach, "198.51.100.60", usage.ReachRequest{Port: 25566})
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), `"busy"`) || w.Header().Get("Retry-After") == "" {
		t.Errorf("a second check from the same address while one runs: %d %s", w.Code, w.Body)
	}
	if code, a := e.check("198.51.100.61", 25565); code != http.StatusOK || a.Result != usage.ReachRefused {
		t.Errorf("a check from another address while one runs: %d %+v", code, a)
	}
	taken := 0
	for len(e.svc.reach.slots) < cap(e.svc.reach.slots) {
		e.svc.reach.slots <- struct{}{}
		taken++
	}
	if w := e.do("POST", usage.PathReach, "198.51.100.62", usage.ReachRequest{Port: 25565}); w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "60" {
		t.Errorf("a check with every slot taken: %d %s", w.Code, w.Body)
	}
	for range taken {
		<-e.svc.reach.slots
	}
	close(hold)
	if code := <-done; code != http.StatusOK {
		t.Errorf("the check that ran: %d", code)
	}
	for i := 1; i < reachPerIPBurst; i++ {
		if code, _ := e.check("198.51.100.60", 25565); code != http.StatusOK {
			t.Fatalf("check %d from one address: %d", i+1, code)
		}
	}
	w = e.do("POST", usage.PathReach, "198.51.100.60", usage.ReachRequest{Port: 25565})
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), `"rate_limited"`) || w.Header().Get("Retry-After") == "" {
		t.Errorf("check %d from one address: %d %s", reachPerIPBurst+1, w.Code, w.Body)
	}
	e.advance(time.Hour / reachPerIPHour)
	if code, _ := e.check("198.51.100.60", 25565); code != http.StatusOK {
		t.Errorf("a check once the address's limit refilled: %d", code)
	}
}

// Nothing of a check is kept: not its address, not what the server said,
// and no install.
func TestACheckKeepsNothing(t *testing.T) {
	e := newEnv(t)
	e.svc.reach.connect = 100 * time.Millisecond
	e.dialTo(dialLocal(startFake(t, statusPacket(statusJSON)).ln.Addr().String()))
	addrs := []string{"203.0.113.77", "2001:db8:77:1::7"}
	for _, a := range addrs {
		if code, r := e.check(a, 25565); code != http.StatusOK || r.Result != usage.ReachReachable {
			t.Fatalf("a check from %s: %d %+v", a, code, r)
		}
	}
	e.dialTo(func(ctx context.Context) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() })
	if code, r := e.check(addrs[0], 25566); code != http.StatusOK || r.Result != usage.ReachTimeout {
		t.Fatalf("a check that timed out: %d %+v", code, r)
	}
	stored := e.storedBytes()
	for _, secret := range append(addrs, "2001:db8:77:1::", "203.0.113", "Notch", "alice", "25566") {
		if bytes.Contains(stored, []byte(secret)) {
			t.Errorf("the service's files contain %q", secret)
		}
		if strings.Contains(e.log.String(), secret) {
			t.Errorf("the service's log contains %q", secret)
		}
	}
	var n int
	if err := e.svc.db.QueryRow(`SELECT COUNT(*) FROM installs`).Scan(&n); err != nil || n != 0 {
		t.Errorf("checks left %d installs (%v)", n, err)
	}
}

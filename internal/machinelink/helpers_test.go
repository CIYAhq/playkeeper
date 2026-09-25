package machinelink

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func mustIdentity(t *testing.T) *Identity {
	t.Helper()
	id, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// wantCode fails unless err is an *Error with code, and returns it.
func wantCode(t *testing.T, err error, code string) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("got error %v (code %q), want code %q", err, CodeOf(err), code)
	}
	return e
}

// testRoutes is a slice of the agent's route table.
func testRoutes() []Route {
	return []Route{
		{Method: "GET", Pattern: "/v1/machine"},
		{Method: "GET", Pattern: "/v1/servers"},
		{Method: "POST", Pattern: "/v1/servers/{id}/start"},
		{Method: "POST", Pattern: "/v1/servers/{id}/settings"},
		{Method: "DELETE", Pattern: "/v1/servers/{id}/whitelist/{name}"},
		{Method: "GET", Pattern: "/v1/servers/{id}/logs", Stream: true},
		{Method: "GET", Pattern: "/v1/servers/{id}/backups/{bid}/download", Stream: true},
		{Method: "POST", Pattern: "/v1/restore/upload", Stream: true},
		{Method: "GET", Pattern: "/v1/test/big"},
		{Method: "GET", Pattern: "/v1/test/slow"},
		{Method: "GET", Pattern: "/v1/test/headers"},
		{Method: "GET", Pattern: "/v1/test/redirect"},
		{Method: "GET", Pattern: "/v1/test/abort"},
	}
}

type eventLog struct {
	mu   sync.Mutex
	list []Event
}

func (l *eventLog) add(e Event) {
	l.mu.Lock()
	l.list = append(l.list, e)
	l.mu.Unlock()
}

func (l *eventLog) all() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.list)
}

func (l *eventLog) count(kind EventKind) int {
	n := 0
	for _, e := range l.all() {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func (l *eventLog) last(kind EventKind) (Event, bool) {
	all := l.all()
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Kind == kind {
			return all[i], true
		}
	}
	return Event{}, false
}

type testHub struct {
	*Hub
	id     *Identity
	store  *MemoryStore
	clock  *fakeClock
	events *eventLog
	logs   *syncBuffer
	addr   string
}

// syncBuffer is a log destination tests can read while the hub writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newTestHub makes a hub with fast heartbeats that listens nowhere.
func newTestHub(t *testing.T, mod func(*HubOptions)) *testHub {
	t.Helper()
	th := &testHub{id: mustIdentity(t), store: NewMemoryStore(), clock: newClock(), events: &eventLog{}, logs: &syncBuffer{}}
	o := HubOptions{
		Identity: th.id, Store: th.store, Routes: testRoutes(), Version: "0.4.0",
		Now: th.clock.Now, OnEvent: th.events.add,
		Logger:    slog.New(slog.NewTextHandler(th.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Heartbeat: 100 * time.Millisecond, HeartbeatTimeout: 300 * time.Millisecond, HandshakeTimeout: 2 * time.Second,
	}
	if mod != nil {
		mod(&o)
	}
	h, err := NewHub(o)
	if err != nil {
		t.Fatal(err)
	}
	th.Hub = h
	return th
}

// startHub is newTestHub, serving on a loopback listener of its own.
func startHub(t *testing.T, mod func(*HubOptions)) *testHub {
	t.Helper()
	th := newTestHub(t, mod)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go th.Serve(ln)
	t.Cleanup(func() { th.Close() })
	th.addr = ln.Addr().String()
	return th
}

func (th *testHub) code(t *testing.T) string {
	t.Helper()
	code, _, err := th.NewJoinCode(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// try joins through the hub's own address, reporting any error.
func (th *testHub) try(id *Identity, code string) (Dashboard, error) {
	return Join(context.Background(), JoinOptions{Address: th.addr, Code: code, Fingerprint: th.Fingerprint(),
		Identity: id, Name: "home-server", Version: "0.4.0", Timeout: 5 * time.Second})
}

// join joins a new machine through addr (the hub's, or a proxy's).
func (th *testHub) join(t *testing.T, addr, name string) (Dashboard, *Identity) {
	t.Helper()
	id := mustIdentity(t)
	d, err := Join(context.Background(), JoinOptions{Address: addr, Code: th.code(t), Fingerprint: th.Fingerprint(), Identity: id, Name: name, Version: "0.4.0"})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	return d, id
}

// machine is a joined machine whose link runs, with a fake agent behind
// it.
type machine struct {
	link  *testLink
	agent *fakeAgent
	d     Dashboard
	id    *Identity
	rt    http.RoundTripper
}

func (th *testHub) linkMachine(t *testing.T, name string, mod func(*LinkOptions)) *machine {
	t.Helper()
	d, id := th.join(t, th.addr, name)
	m := &machine{agent: newFakeAgent(name), d: d, id: id, rt: th.Transport(d.MachineID)}
	m.link = startLink(t, d, id, m.agent, mod)
	eventually(t, name+" connects", func() bool { return th.Connected(d.MachineID) })
	return m
}

type recordLog struct {
	mu   sync.Mutex
	list []RequestRecord
}

func (l *recordLog) add(r RequestRecord) {
	l.mu.Lock()
	l.list = append(l.list, r)
	l.mu.Unlock()
}

func (l *recordLog) all() []RequestRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.list)
}

type testLink struct {
	*Link
	records *recordLog
	stopped chan struct{}
	err     error // Run's result, once stopped is closed
}

func startLink(t *testing.T, d Dashboard, id *Identity, h http.Handler, mod func(*LinkOptions)) *testLink {
	t.Helper()
	tl := &testLink{records: &recordLog{}, stopped: make(chan struct{})}
	o := LinkOptions{Dashboard: d, Identity: id, Handler: h, Routes: testRoutes(), Version: "0.4.0", OnRequest: tl.records.add,
		MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond, RetryLater: 300 * time.Millisecond, HandshakeTimeout: time.Second}
	if mod != nil {
		mod(&o)
	}
	l, err := NewLink(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	tl.Link = l
	go func() {
		tl.err = l.Run(ctx)
		close(tl.stopped)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-tl.stopped:
		case <-time.After(5 * time.Second):
			t.Error("the link did not stop")
		}
	})
	return tl
}

// wait waits for Run to return by itself.
func (tl *testLink) wait(t *testing.T) error {
	t.Helper()
	select {
	case <-tl.stopped:
		return tl.err
	case <-time.After(10 * time.Second):
		t.Fatal("the link kept running")
		return nil
	}
}

// fakeAgent answers the test routes like an agent would.
type fakeAgent struct {
	name        string
	calls       atomic.Int64
	bigBytes    atomic.Int64
	bigLength   atomic.Bool
	headerBytes atomic.Int64
	slowStarted chan struct{}
	logGate     chan struct{}

	mu      sync.Mutex
	headers []http.Header
}

func newFakeAgent(name string) *fakeAgent {
	return &fakeAgent{name: name, slowStarted: make(chan struct{}, 8), logGate: make(chan struct{})}
}

func (a *fakeAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.calls.Add(1)
	a.mu.Lock()
	a.headers = append(a.headers, r.Header.Clone())
	a.mu.Unlock()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/machine", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"hostname": a.name})
	})
	mux.HandleFunc("GET /v1/servers", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"id":"srv-%s"}]`, a.name)
	})
	mux.HandleFunc("POST /v1/servers/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"started": r.PathValue("id"), "on": a.name})
	})
	mux.HandleFunc("POST /v1/servers/{id}/settings", func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, "too big", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/restore/upload", func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/servers/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "first line")
		http.NewResponseController(w).Flush()
		select {
		case <-a.logGate:
		case <-r.Context().Done():
			return
		}
		fmt.Fprintln(w, "second line")
	})
	mux.HandleFunc("GET /v1/test/big", func(w http.ResponseWriter, r *http.Request) {
		size := int(a.bigBytes.Load())
		if a.bigLength.Load() {
			w.Header().Set("Content-Length", fmt.Sprint(size))
		}
		chunk := bytes.Repeat([]byte("x"), 32<<10)
		for n := size; n > 0; n -= len(chunk) {
			if _, err := w.Write(chunk[:min(n, len(chunk))]); err != nil {
				return
			}
		}
	})
	mux.HandleFunc("GET /v1/test/slow", func(w http.ResponseWriter, r *http.Request) {
		a.slowStarted <- struct{}{}
		<-r.Context().Done()
	})
	mux.HandleFunc("GET /v1/test/headers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "session=stolen")
		if n := a.headerBytes.Load(); n > 0 {
			w.Header().Set("X-Big", strings.Repeat("y", int(n)))
		}
		w.Write([]byte("{}"))
	})
	mux.HandleFunc("GET /v1/test/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/v1/machine", http.StatusFound)
	})
	mux.HandleFunc("GET /v1/test/abort", func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	})
	mux.ServeHTTP(w, r)
}

func (a *fakeAgent) seen() []http.Header {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.headers)
}

func (a *fakeAgent) seenActors() []string {
	var out []string
	for _, h := range a.seen() {
		out = append(out, h.Get(ActorHeader))
	}
	return out
}

func waitFor(t *testing.T, what string, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting until %s", what)
	}
}

// send makes a request through a machine's transport, the way the panel's
// agent client would.
func send(rt http.RoundTripper, method, path, actor string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, "http://machine"+path, body)
	if err != nil {
		return nil, err
	}
	if actor != "" {
		req.Header.Set(ActorHeader, actor)
	}
	return (&http.Client{Transport: rt, Timeout: 10 * time.Second}).Do(req)
}

// sendRead is send, then reads the whole reply.
func sendRead(rt http.RoundTripper, method, path, actor string) (int, string, error) {
	resp, err := send(rt, method, path, actor, nil)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), err
}

// rawDial makes a machine's TLS connection to the hub by hand, pinning
// key, so tests can send what a real machine never would.
func rawDial(t *testing.T, addr string, id *Identity, key ed25519.PublicKey) *tls.Conn {
	t.Helper()
	a, err := ParseAddress(addr)
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", a.HostPort())
	if err != nil {
		t.Fatal(err)
	}
	tc := tls.Client(c, clientTLS(id, a, pin{key: key}))
	tc.SetDeadline(time.Now().Add(5 * time.Second))
	if err := tc.Handshake(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tc.Close() })
	return tc
}

// frame is a hello frame with any contents.
func frame(payload []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(len(payload)))
	return append(b, payload...)
}

func jsonFrame(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return frame(b)
}

// exchange sends raw bytes as a hello and reads the welcome.
func (th *testHub) exchange(t *testing.T, id *Identity, raw []byte) welcome {
	t.Helper()
	tc := rawDial(t, th.addr, id, th.id.PublicKey())
	if _, err := tc.Write(raw); err != nil {
		t.Fatal(err)
	}
	var w welcome
	if err := readFrame(tc, &w); err != nil {
		t.Fatalf("reading the welcome: %v", err)
	}
	return w
}

// tcpProxy relays TCP between a machine and the hub. It can cut every
// connection, or stall them the way a dead network does: nothing gets
// through, not even a connection closing.
type tcpProxy struct {
	ln     net.Listener
	target string
	stall  atomic.Bool

	mu    sync.Mutex
	conns []net.Conn
	seen  []byte
}

func startProxy(t *testing.T, target string) *tcpProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &tcpProxy{ln: ln, target: target}
	go p.accept()
	t.Cleanup(func() {
		p.stall.Store(false)
		ln.Close()
		p.cut()
	})
	return p
}

func (p *tcpProxy) addr() string { return p.ln.Addr().String() }

func (p *tcpProxy) accept() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		u, err := net.Dial("tcp", p.target)
		if err != nil {
			c.Close()
			continue
		}
		p.mu.Lock()
		p.conns = append(p.conns, c, u)
		p.mu.Unlock()
		go p.pipe(u, c)
		go p.pipe(c, u)
	}
}

func (p *tcpProxy) pipe(dst, src net.Conn) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		p.hold()
		if n > 0 {
			p.mu.Lock()
			p.seen = append(p.seen, buf[:n]...)
			p.mu.Unlock()
			if _, werr := dst.Write(buf[:n]); werr != nil {
				src.Close()
				return
			}
		}
		if err != nil {
			dst.Close()
			return
		}
	}
}

func (p *tcpProxy) hold() {
	for p.stall.Load() {
		time.Sleep(5 * time.Millisecond)
	}
}

func (p *tcpProxy) cut() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		c.Close()
	}
	p.conns = nil
}

func (p *tcpProxy) bytesSeen() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.seen)
}

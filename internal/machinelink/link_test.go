package machinelink

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLinkRoutesRequests(t *testing.T) {
	th := startHub(t, nil)
	m := th.linkMachine(t, "home-server", nil)

	status, body, err := sendRead(m.rt, "GET", "/v1/machine", "")
	if err != nil || status != 200 || strings.TrimSpace(body) != `{"hostname":"home-server"}` {
		t.Fatalf("GET /v1/machine = %d %q, %v", status, body, err)
	}
	status, body, err = sendRead(m.rt, "POST", "/v1/servers/abc/start", "alice")
	if err != nil || status != 200 || !strings.Contains(body, `"started":"abc"`) {
		t.Fatalf("POST start = %d %q, %v", status, body, err)
	}
	req, _ := http.NewRequestWithContext(WithActor(context.Background(), "schedule:nightly"), "POST", "http://ignored/v1/servers/abc/start", nil)
	resp, err := m.rt.RoundTrip(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("POST with WithActor: %v, %v", resp, err)
	}
	resp.Body.Close()
	resp, err = send(m.rt, "HEAD", "/v1/machine", "", nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("HEAD: %v, %v", resp, err)
	}
	resp.Body.Close()

	if got, want := m.agent.seenActors(), []string{"", "alice", "schedule:nightly", ""}; !slices.Equal(got, want) {
		t.Fatalf("the agent saw actors %q, want %q", got, want)
	}
	eventually(t, "the machine records 4 requests", func() bool { return len(m.link.records.all()) == 4 })
	recs := m.link.records.all()
	if r := recs[1]; r.Actor != "alice" || r.Method != "POST" || r.Route != "POST /v1/servers/{id}/start" || r.Path != "/v1/servers/abc/start" || r.Status != 200 || r.Bytes == 0 {
		t.Fatalf("record %+v", r)
	}
}

func TestLinkKeepsMachinesApart(t *testing.T) {
	th := startHub(t, nil)
	alpha := th.linkMachine(t, "alpha", nil)
	beta := th.linkMachine(t, "beta", nil)
	for _, m := range []*machine{alpha, beta} {
		_, body, err := sendRead(m.rt, "GET", "/v1/servers", "")
		if err != nil || body != fmt.Sprintf(`[{"id":"srv-%s"}]`, m.agent.name) {
			t.Fatalf("%s answered %q, %v", m.agent.name, body, err)
		}
	}
	if alpha.agent.calls.Load() != 1 || beta.agent.calls.Load() != 1 {
		t.Fatalf("calls: alpha %d, beta %d", alpha.agent.calls.Load(), beta.agent.calls.Load())
	}
	_, err := send(th.Transport("nosuchmachine"), "GET", "/v1/machine", "", nil)
	if e := wantCode(t, err, CodeNotConnected); e.Params["name"] != "That machine" {
		t.Errorf("unknown machine: %+v", e)
	}
}

func TestLinkRefusesRequestsOutsideTheAllowlist(t *testing.T) {
	th := startHub(t, nil)
	m := th.linkMachine(t, "home", nil)
	for _, tc := range []struct{ method, path, actor, want string }{
		{"GET", "/v1/secret", "", CodeRouteNotAllowed},
		{"PUT", "/v1/machine", "alice", CodeRouteNotAllowed},
		{"GET", "/v1/servers/../machine", "", CodeRouteNotAllowed},
		{"GET", "//v1/machine", "", CodeRouteNotAllowed},
		{"POST", "/v1/servers/a%2Fb/start", "alice", CodeRouteNotAllowed},
		{"GET", "/_link/ping", "", CodeRouteNotAllowed},
		{"POST", "/v1/servers/abc/start", "", CodeActorRequired},
		{"DELETE", "/v1/servers/abc/whitelist/Steve", "", CodeActorRequired},
		{"POST", "/v1/servers/abc/start", strings.Repeat("a", 65), CodeActorRequired},
		{"GET", "/v1/machine", "bad\x01actor", CodeActorRequired},
	} {
		_, err := send(m.rt, tc.method, tc.path, tc.actor, nil)
		if CodeOf(err) != tc.want {
			t.Errorf("%s %s (actor %q): %v, want %s", tc.method, tc.path, tc.actor, err, tc.want)
		}
	}
	if n := m.agent.calls.Load(); n != 0 {
		t.Fatalf("%d refused requests reached the machine", n)
	}
}

func TestLinkKeepsCredentialsOnTheDashboard(t *testing.T) {
	th := startHub(t, nil)
	m := th.linkMachine(t, "home", nil)
	req, _ := http.NewRequest("GET", "http://machine/v1/test/headers", nil)
	req.Header.Set("Cookie", "session=secret")
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Connection", "X-Private")
	req.Header.Set("X-Private", "secret")
	req.Header.Set("X-Request-Id", "r1")
	resp, err := (&http.Client{Transport: m.rt}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if c := resp.Header.Get("Set-Cookie"); c != "" {
		t.Fatalf("the machine set a cookie on the dashboard: %q", c)
	}
	h := m.agent.seen()[0]
	for _, k := range []string{"Cookie", "Authorization", "X-Private"} {
		if h.Get(k) != "" {
			t.Errorf("the machine received %s", k)
		}
	}
	if h.Get("X-Request-Id") != "r1" {
		t.Error("an ordinary header was dropped")
	}

	_, err = send(m.rt, "GET", "/v1/test/redirect", "", nil)
	if e := wantCode(t, err, CodeProtocol); !strings.Contains(e.Msg, "redirect") {
		t.Errorf("redirect: %q", e.Msg)
	}
	if n := m.agent.calls.Load(); n != 2 {
		t.Fatalf("the redirect was followed: %d calls", n)
	}
}

func TestLinkReconnectsAfterADrop(t *testing.T) {
	th := startHub(t, nil)
	p := startProxy(t, th.addr)
	d, id := th.join(t, th.addr, "home")
	d.Address = p.addr()
	agent := newFakeAgent("home")
	startLink(t, d, id, agent, nil)
	eventually(t, "connected", func() bool { return th.Connected(d.MachineID) })

	p.cut()
	eventually(t, "the dashboard notices", func() bool { return th.events.count(EventDisconnected) == 1 })
	if e, _ := th.events.last(EventDisconnected); e.Code != CodeDropped || e.MachineID != d.MachineID {
		t.Fatalf("disconnected event %+v", e)
	}
	eventually(t, "connected again", func() bool {
		return th.events.count(EventConnected) == 2 && th.Connected(d.MachineID)
	})
	if _, _, err := sendRead(th.Transport(d.MachineID), "GET", "/v1/machine", ""); err != nil {
		t.Fatalf("after reconnecting: %v", err)
	}
}

func TestLinkHeartbeatTimeout(t *testing.T) {
	th := startHub(t, nil)
	p := startProxy(t, th.addr)
	d, id := th.join(t, th.addr, "home")
	d.Address = p.addr()
	tl := startLink(t, d, id, newFakeAgent("home"), nil)
	eventually(t, "connected", func() bool { return th.Connected(d.MachineID) })

	p.stall.Store(true)
	eventually(t, "the dashboard gives up on the machine", func() bool {
		e, ok := th.events.last(EventDisconnected)
		return ok && e.Code == CodeHeartbeatTimeout
	})
	if th.Connected(d.MachineID) {
		t.Fatal("still connected")
	}
	_, err := send(th.Transport(d.MachineID), "GET", "/v1/machine", "", nil)
	wantCode(t, err, CodeNotConnected)
	eventually(t, "the machine gives up on the dashboard", func() bool { return tl.Status().State != LinkConnected })

	p.stall.Store(false)
	eventually(t, "connected again", func() bool { return th.Connected(d.MachineID) && tl.Status().State == LinkConnected })
}

func TestRemoveCutsTheMachineOff(t *testing.T) {
	th := startHub(t, nil)
	m := th.linkMachine(t, "home", nil)
	errc := make(chan error, 1)
	go func() {
		_, _, err := sendRead(m.rt, "GET", "/v1/test/slow", "")
		errc <- err
	}()
	waitFor(t, "the slow request reaches the machine", m.agent.slowStarted)

	if err := th.Remove(context.Background(), m.d.MachineID, "alice"); err != nil {
		t.Fatal(err)
	}
	if th.Connected(m.d.MachineID) {
		t.Fatal("still connected after Remove returned")
	}
	select {
	case err := <-errc:
		if e := wantCode(t, err, CodeMachineRemoved); e.Params["name"] != "home" {
			t.Errorf("in-flight request: %+v", e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the request in flight was not cut")
	}
	_, err := send(m.rt, "GET", "/v1/machine", "", nil)
	wantCode(t, err, CodeMachineRemoved)

	// The machine is refused when it calls again, and stops.
	wantCode(t, m.link.wait(t), CodeMachineRemoved)
	if s := m.link.Status(); s.State != LinkRemoved || s.Problem == nil || s.Problem.Code != CodeMachineRemoved {
		t.Fatalf("link status %+v", s)
	}
	if e, ok := th.events.last(EventRemoved); !ok || e.Actor != "alice" || e.MachineID != m.d.MachineID {
		t.Fatalf("removed event %+v", e)
	}
	if n := th.events.count(EventDisconnected); n != 0 {
		t.Fatalf("%d disconnected events besides the removal", n)
	}
	stored, _, _ := th.store.Machine(context.Background(), m.d.MachineID)
	if !stored.Removed() || stored.RevokedBy != "alice" {
		t.Fatalf("stored %+v", stored)
	}
}

func TestLeave(t *testing.T) {
	th := startHub(t, nil)
	m := th.linkMachine(t, "home", nil)
	if err := Leave(context.Background(), LeaveOptions{Dashboard: m.d, Identity: m.id}); err != nil {
		t.Fatal(err)
	}
	wantCode(t, m.link.wait(t), CodeMachineRemoved)
	want := "machine:" + m.d.MachineID
	if e, ok := th.events.last(EventLeft); !ok || e.Actor != want || e.Address != "127.0.0.1" {
		t.Fatalf("left event %+v", e)
	}
	if stored, _, _ := th.store.Machine(context.Background(), m.d.MachineID); stored.RevokedBy != want {
		t.Fatalf("revoked by %q", stored.RevokedBy)
	}
	if err := Leave(context.Background(), LeaveOptions{Dashboard: m.d, Identity: m.id}); err != nil {
		t.Fatalf("leaving twice: %v", err)
	}
	if err := Leave(context.Background(), LeaveOptions{Dashboard: m.d, Identity: mustIdentity(t)}); err != nil {
		t.Fatalf("leaving a dashboard that never knew this key: %v", err)
	}
	other := m.d
	other.Key = mustIdentity(t).PublicKey()
	err := Leave(context.Background(), LeaveOptions{Dashboard: other, Identity: m.id})
	wantCode(t, err, CodeDashboardKeyMismatch)
}

func TestLinkRefusesOversizedReplies(t *testing.T) {
	th := startHub(t, func(o *HubOptions) { o.MaxResponseBytes = 64 << 10 })
	m := th.linkMachine(t, "home", nil)

	m.agent.bigBytes.Store(200 << 10)
	_, _, err := sendRead(m.rt, "GET", "/v1/test/big", "")
	if e := wantCode(t, err, CodeTooLarge); e.Params["limit"] != "64 KiB" || !strings.Contains(e.Msg, "home") {
		t.Errorf("too large: %+v", e)
	}
	m.agent.bigLength.Store(true)
	_, err = send(m.rt, "GET", "/v1/test/big", "", nil)
	wantCode(t, err, CodeTooLarge)

	m.agent.bigLength.Store(false)
	m.agent.bigBytes.Store(64 << 10)
	if _, body, err := sendRead(m.rt, "GET", "/v1/test/big", ""); err != nil || len(body) != 64<<10 {
		t.Fatalf("a reply at the limit: %d bytes, %v", len(body), err)
	}

	if !th.Connected(m.d.MachineID) || th.events.count(EventDisconnected) != 0 {
		t.Fatal("refusing replies dropped the link")
	}

	// HTTP/2 refuses a header block this far over its limit by closing the
	// whole connection, before reading it into memory.
	m.agent.headerBytes.Store(100 << 10)
	_, err = send(m.rt, "GET", "/v1/test/headers", "", nil)
	wantCode(t, err, CodeNotConnected)
	m.agent.headerBytes.Store(0)
	eventually(t, "the machine connects again", func() bool { return th.events.count(EventConnected) == 2 })
	if _, _, err := sendRead(m.rt, "GET", "/v1/test/headers", ""); err != nil {
		t.Fatalf("after the refusals: %v", err)
	}
}

func TestLinkReportsBrokenReplies(t *testing.T) {
	th := startHub(t, nil)
	m := th.linkMachine(t, "home", nil)
	_, err := send(m.rt, "GET", "/v1/test/abort", "", nil)
	if e := wantCode(t, err, CodeProtocol); e.Msg != "The reply from home broke off or was not valid." || e.Hint == "" {
		t.Errorf("broken reply: %+v", e)
	}
	if _, _, err := sendRead(m.rt, "GET", "/v1/machine", ""); err != nil || th.events.count(EventDisconnected) != 0 {
		t.Fatalf("one broken reply dropped the link: %v", err)
	}
}

func TestLinkLimitsRequestBodies(t *testing.T) {
	th := startHub(t, nil)
	m := th.linkMachine(t, "home", func(o *LinkOptions) { o.MaxRequestBytes = 1 << 10 })
	for size, want := range map[int]int{4 << 10: http.StatusRequestEntityTooLarge, 1 << 10: http.StatusNoContent} {
		resp, err := send(m.rt, "POST", "/v1/servers/abc/settings", "alice", strings.NewReader(strings.Repeat("x", size)))
		if err != nil {
			t.Fatalf("%d bytes: %v", size, err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%d bytes: status %d, want %d", size, resp.StatusCode, want)
		}
	}

	// A body that fails on the dashboard's side is the caller's error, not
	// the link's.
	boom := errors.New("upload interrupted")
	_, err := send(m.rt, "POST", "/v1/restore/upload", "alice", io.MultiReader(strings.NewReader("part"), iotestErrReader{boom}))
	if !errors.Is(err, boom) {
		t.Fatalf("failing body: %v", err)
	}
}

type iotestErrReader struct{ err error }

func (r iotestErrReader) Read([]byte) (int, error) { return 0, r.err }

func TestLinkTimeLimits(t *testing.T) {
	th := startHub(t, func(o *HubOptions) { o.RequestTimeout = 200 * time.Millisecond })
	m := th.linkMachine(t, "home", nil)

	_, _, err := sendRead(m.rt, "GET", "/v1/test/slow", "")
	if e := wantCode(t, err, CodeTimeout); !strings.Contains(e.Msg, "didn't answer") {
		t.Errorf("timeout: %q", e.Msg)
	}

	// Streams have no time limit, and arrive as they are written.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://machine/v1/servers/abc/logs", nil)
	resp, err := m.rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	if line, err := br.ReadString('\n'); err != nil || line != "first line\n" {
		t.Fatalf("first line %q, %v", line, err)
	}
	time.Sleep(300 * time.Millisecond)
	close(m.agent.logGate)
	if rest, err := io.ReadAll(br); err != nil || string(rest) != "second line\n" {
		t.Fatalf("rest of the stream %q, %v", rest, err)
	}
	if !th.Connected(m.d.MachineID) {
		t.Fatal("the link dropped")
	}
}

// slowSource gives its chunks with a pause before each, as a browser that
// uploads slowly does.
type slowSource struct {
	chunks []string
	pause  time.Duration
}

func (s *slowSource) Read(p []byte) (int, error) {
	if len(s.chunks) == 0 {
		return 0, io.EOF
	}
	time.Sleep(s.pause)
	n := copy(p, s.chunks[0])
	s.chunks = s.chunks[1:]
	return n, nil
}

// The time limit of a request that is not a stream counts only waiting on
// the machine: an answer that keeps coming, one read slowly, or a request
// body from a slow source may all take longer. An answer that stops coming
// still ends at the limit.
func TestLinkTimeLimitsCountOnlyWaitingOnTheMachine(t *testing.T) {
	const limit = 300 * time.Millisecond
	th := startHub(t, func(o *HubOptions) { o.RequestTimeout = limit })
	m := th.linkMachine(t, "home", nil)

	start := time.Now()
	if _, body, err := sendRead(m.rt, "GET", "/v1/test/trickle", ""); err != nil || body != strings.Repeat("tick\n", 12) || time.Since(start) < 2*limit {
		t.Fatalf("an answer that keeps coming: %q, %v after %s", body, err, time.Since(start))
	}

	m.agent.bigBytes.Store(64 << 10)
	resp, err := send(m.rt, "GET", "/v1/test/big", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, buf := 0, make([]byte, 32<<10)
	for err == nil {
		time.Sleep(limit + limit/2)
		var n int
		n, err = resp.Body.Read(buf)
		got += n
	}
	resp.Body.Close()
	if err != io.EOF || got != 64<<10 {
		t.Fatalf("an answer read slowly: %d bytes, %v", got, err)
	}

	resp, err = send(m.rt, "POST", "/v1/servers/abc/settings", "alice", &slowSource{chunks: []string{"{", `"motd":"hi"`, "}"}, pause: limit + limit/2})
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("a request body from a slow source: %v, %v", resp, err)
	}
	resp.Body.Close()

	_, body, err := sendRead(m.rt, "GET", "/v1/test/stall", "")
	if e := wantCode(t, err, CodeTimeout); body != "first\n" || !strings.Contains(e.Msg, "didn't answer") {
		t.Fatalf("an answer that stops coming: %q, %v", body, e)
	}
	if !th.Connected(m.d.MachineID) {
		t.Fatal("the link dropped")
	}
}

// On a link the dashboard is the HTTP/2 client: whatever a machine sends
// as requests is never served.
func TestMachineCannotSendRequests(t *testing.T) {
	th := startHub(t, nil)
	d, id := th.join(t, th.addr, "home")
	tc := rawDial(t, th.addr, id, th.id.PublicKey())
	if _, err := tc.Write(jsonFrame(t, hello{V: 1, Mode: modeLink, Version: "0.4.0"})); err != nil {
		t.Fatal(err)
	}
	var w welcome
	if err := readFrame(tc, &w); err != nil || !w.OK || w.MachineID != d.MachineID {
		t.Fatalf("welcome %+v, %v", w, err)
	}
	var req bytes.Buffer
	req.WriteString("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")
	req.Write([]byte{0, 0, 0, 4, 0, 0, 0, 0, 0})
	block := append([]byte{0x82, 0x84, 0x86, 0x41, 9}, "dashboard"...)
	req.Write([]byte{0, 0, byte(len(block)), 1, 5, 0, 0, 0, 1})
	req.Write(block)
	if _, err := tc.Write(req.Bytes()); err != nil {
		t.Fatal(err)
	}
	tc.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(tc)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal("the dashboard kept the connection open")
	}
	if !bytes.HasPrefix(got, []byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")) {
		t.Fatalf("the dashboard did not speak as the client: %q", got[:min(len(got), 40)])
	}
	eventually(t, "the dashboard drops the connection", func() bool { return th.events.count(EventDisconnected) == 1 })
}

func TestSharedPortWithThePanel(t *testing.T) {
	th := startHub(t, nil)
	panel := &tls.Config{Certificates: []tls.Certificate{mustIdentity(t).cert}}
	srv := &http.Server{
		Handler:      http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "panel ", r.Proto) }),
		TLSConfig:    th.ShareTLS(panel),
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){ALPN: th.HandleTLSNextProto},
		ErrorLog:     log.New(io.Discard, "", 0),
		Protocols:    new(http.Protocols),
	}
	srv.Protocols.SetHTTP1(true)
	srv.Protocols.SetHTTP2(true)
	srv.RegisterOnShutdown(func() { th.Close() })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.ServeTLS(ln, "", "")
	addr := ln.Addr().String()

	browser := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, ForceAttemptHTTP2: true}}
	resp, err := browser.Get("https://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.ProtoMajor != 2 || string(body) != "panel HTTP/2.0" {
		t.Fatalf("the browser got %s %q", resp.Proto, body)
	}

	d, id := th.join(t, addr, "home")
	startLink(t, d, id, newFakeAgent("home"), nil)
	eventually(t, "connected through the panel's port", func() bool { return th.Connected(d.MachineID) })
	if _, body, err := sendRead(th.Transport(d.MachineID), "GET", "/v1/machine", ""); err != nil || !strings.Contains(body, "home") {
		t.Fatalf("through the panel's port: %q, %v", body, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("the panel did not shut down with a machine connected: %v", err)
	}
	if th.Connected(d.MachineID) {
		t.Fatal("the machine is still connected after shutdown")
	}
}

func TestLinkRefusals(t *testing.T) {
	th := startHub(t, nil)
	ghost := Dashboard{Address: th.addr, Key: th.id.PublicKey(), MachineID: "abcdefghij", Name: "ghost"}
	unknown := startLink(t, ghost, mustIdentity(t), newFakeAgent("ghost"), func(o *LinkOptions) { o.RetryLater = time.Hour })

	d, id := th.join(t, th.addr, "home")
	d.Key = mustIdentity(t).PublicKey()
	changed := startLink(t, d, id, newFakeAgent("home"), func(o *LinkOptions) { o.RetryLater = time.Hour })

	for tl, code := range map[*testLink]string{unknown: CodeMachineUnknown, changed: CodeDashboardKeyMismatch} {
		eventually(t, "the link reports "+code, func() bool {
			s := tl.Status()
			return s.State == LinkRetrying && s.Problem != nil && s.Problem.Code == code
		})
		// Trying again soon can't help, so the machine waits RetryLater.
		if s := tl.Status(); time.Until(s.NextAttempt) < 30*time.Minute || s.Problem.Message == "" || s.Problem.Hint == "" {
			t.Errorf("%s: status %+v", code, s)
		}
	}
	if n := th.events.count(EventConnected); n != 0 {
		t.Fatalf("%d refused machines connected", n)
	}
}

func TestAgentProxy(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	agent := newFakeAgent("home")
	srv := &http.Server{Handler: agent}
	go srv.Serve(ln)
	defer srv.Close()

	th := startHub(t, nil)
	d, id := th.join(t, th.addr, "home")
	startLink(t, d, id, AgentProxy(sock), nil)
	eventually(t, "connected", func() bool { return th.Connected(d.MachineID) })
	rt := th.Transport(d.MachineID)

	status, body, err := sendRead(rt, "POST", "/v1/servers/abc/start", "alice")
	if err != nil || status != 200 || !strings.Contains(body, `"on":"home"`) {
		t.Fatalf("through the agent's socket: %d %q, %v", status, body, err)
	}
	if got := agent.seenActors(); !slices.Equal(got, []string{"alice"}) {
		t.Fatalf("the agent saw actors %q", got)
	}
	if h := agent.seen()[0]; h.Get("X-Forwarded-For") != "" {
		t.Fatalf("the proxy added X-Forwarded-For: %v", h)
	}

	srv.Close()
	resp, err := send(rt, "GET", "/v1/machine", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var e apiError
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil || resp.StatusCode != http.StatusBadGateway || e.Code != CodeAgentDown || e.Hint == "" {
		t.Fatalf("with the agent down: %d %+v, %v", resp.StatusCode, e, err)
	}
}

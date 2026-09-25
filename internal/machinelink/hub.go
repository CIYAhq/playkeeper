package machinelink

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// HubOptions configure the dashboard's side of machine links.
type HubOptions struct {
	// Identity is the dashboard's key. Machines pin it when they join, so
	// it must stay the same for as long as they are connected.
	Identity *Identity
	Store    Store
	// Routes are the agent requests machines may be sent: the agent's
	// route table.
	Routes []Route
	// Version is this dashboard's Playkeeper version.
	Version string
	Now     func() time.Time
	Logger  *slog.Logger
	// OnEvent, when set, is told about joins, refused joins, connections
	// and removals, for the audit log. It is called without locks held
	// and should return quickly.
	OnEvent func(Event)
	Limits  Limits
	// Heartbeat is how often the dashboard checks each machine (default
	// 15s), and HeartbeatTimeout how long it waits for the answer (10s).
	Heartbeat        time.Duration
	HeartbeatTimeout time.Duration
	// HandshakeTimeout bounds a connection's TLS handshake and hello (10s).
	HandshakeTimeout time.Duration
	// RequestTimeout bounds a whole request that is not a stream (60s),
	// and MaxResponseBytes the size of its answer (16 MiB).
	RequestTimeout   time.Duration
	MaxResponseBytes int64
	// OfflineAfter is how long a machine may be away before its status
	// shows a problem (2m).
	OfflineAfter time.Duration
	// MaxPending bounds the connections still in their handshake (64).
	MaxPending int
}

// Hub is the dashboard's side: it hands out join codes, accepts machines'
// connections, and sends them requests.
type Hub struct {
	id      *Identity
	store   Store
	allow   *allowlist
	opts    HubOptions
	now     func() time.Time
	log     *slog.Logger
	codeKey []byte
	guard   *guard
	tlsConf *tls.Config
	pending chan struct{}
	joinMu  sync.Mutex

	mu        sync.Mutex
	closed    bool
	sessions  map[string]*session
	revoked   map[string]bool
	stats     map[string]*machineStats
	conns     map[net.Conn]struct{}
	listeners map[net.Listener]struct{}
	wg        sync.WaitGroup
}

// ErrHubClosed is returned by Serve after Close.
var ErrHubClosed = errors.New("machinelink: hub closed")

// NewHub checks the options and fills in defaults.
func NewHub(o HubOptions) (*Hub, error) {
	if o.Identity == nil || o.Store == nil {
		return nil, errors.New("machinelink: a hub needs an identity and a store")
	}
	allow, err := newAllowlist(o.Routes)
	if err != nil {
		return nil, err
	}
	key, err := codeKey(o.Identity)
	if err != nil {
		return nil, err
	}
	setDefault(&o.Heartbeat, 15*time.Second)
	setDefault(&o.HeartbeatTimeout, 10*time.Second)
	setDefault(&o.HandshakeTimeout, 10*time.Second)
	setDefault(&o.RequestTimeout, time.Minute)
	setDefault(&o.OfflineAfter, 2*time.Minute)
	if o.MaxResponseBytes <= 0 {
		o.MaxResponseBytes = 16 << 20
	}
	if o.MaxPending <= 0 {
		o.MaxPending = 64
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	o.Version = cleanVersion(o.Version)
	return &Hub{
		id: o.Identity, store: o.Store, allow: allow, opts: o, now: o.Now, log: o.Logger,
		codeKey: key, guard: newGuard(o.Limits), tlsConf: serverTLS(o.Identity),
		pending:  make(chan struct{}, o.MaxPending),
		sessions: map[string]*session{}, revoked: map[string]bool{}, stats: map[string]*machineStats{},
		conns: map[net.Conn]struct{}{}, listeners: map[net.Listener]struct{}{},
	}, nil
}

func setDefault(d *time.Duration, v time.Duration) {
	if *d <= 0 {
		*d = v
	}
}

// Fingerprint is the dashboard's key fingerprint, for join commands.
func (h *Hub) Fingerprint() string { return h.id.Fingerprint() }

// TLSConfig is the TLS configuration for machine connections.
func (h *Hub) TLSConfig() *tls.Config { return h.tlsConf.Clone() }

// ShareTLS returns a copy of the panel's TLS configuration that hands
// connections offering ALPN to the hub, so machines and browsers share
// one port. The panel's http.Server must also set
// TLSNextProto[ALPN] = hub.HandleTLSNextProto, and set Protocols
// explicitly, or adding that entry turns HTTP/2 off for browsers.
func (h *Hub) ShareTLS(panel *tls.Config) *tls.Config {
	c := panel.Clone()
	link := h.TLSConfig()
	next := panel.GetConfigForClient
	c.GetConfigForClient = func(hi *tls.ClientHelloInfo) (*tls.Config, error) {
		if slices.Contains(hi.SupportedProtos, ALPN) {
			return link, nil
		}
		if next != nil {
			return next(hi)
		}
		return nil, nil
	}
	return c
}

// HandleTLSNextProto serves one machine connection that arrived on the
// panel's port. It returns when the connection ends.
func (h *Hub) HandleTLSNextProto(_ *http.Server, c *tls.Conn, _ http.Handler) {
	if !h.enter() {
		c.Close()
		return
	}
	defer h.wg.Done()
	h.serveConn(c)
}

// Serve accepts machine connections on a listener of its own, doing TLS
// itself. It returns ErrHubClosed after Close.
func (h *Hub) Serve(ln net.Listener) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		ln.Close()
		return ErrHubClosed
	}
	h.listeners[ln] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.listeners, ln)
		h.mu.Unlock()
	}()
	var delay time.Duration
	for {
		c, err := ln.Accept()
		if err != nil {
			if h.isClosed() {
				return ErrHubClosed
			}
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			delay = min(max(2*delay, 5*time.Millisecond), time.Second)
			time.Sleep(delay)
			continue
		}
		delay = 0
		if !h.enter() {
			c.Close()
			return ErrHubClosed
		}
		go func() {
			defer h.wg.Done()
			h.serveConn(tls.Server(c, h.tlsConf))
		}()
	}
}

// enter counts a connection handler, unless the hub is closed.
func (h *Hub) enter() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	h.wg.Add(1)
	return true
}

func (h *Hub) isClosed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closed
}

// Close drops every machine connection and stops Serve. Machines connect
// again when the dashboard is back.
func (h *Hub) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	var sessions []*session
	for _, s := range h.sessions {
		sessions = append(sessions, s)
	}
	var closers []io.Closer
	for c := range h.conns {
		closers = append(closers, c)
	}
	for ln := range h.listeners {
		closers = append(closers, ln)
	}
	h.mu.Unlock()
	for _, s := range sessions {
		s.close(errHubClosed)
	}
	for _, c := range closers {
		c.Close()
	}
	h.wg.Wait()
	return nil
}

var (
	errHubClosed   = errors.New("machinelink: the dashboard is shutting down")
	errReplaced    = errors.New("machinelink: the machine connected again")
	errRevoked     = errors.New("machinelink: the machine was removed")
	errLinkClosed  = errors.New("machinelink: the connection closed")
	errPingTimeout = errors.New("machinelink: the machine didn't answer the heartbeat")
)

func (h *Hub) emit(e Event) {
	if h.opts.OnEvent != nil {
		h.opts.OnEvent(e)
	}
}

func (h *Hub) serveConn(tc *tls.Conn) {
	defer tc.Close()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.conns[tc] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.conns, tc)
		h.mu.Unlock()
	}()
	select {
	case h.pending <- struct{}{}:
	default:
		h.log.Warn("too many machine connections waiting for their handshake; dropping one", "addr", remoteIP(tc.RemoteAddr().String()))
		return
	}
	var once sync.Once
	release := func() { once.Do(func() { <-h.pending }) }
	defer release()

	deadline := time.Now().Add(h.opts.HandshakeTimeout)
	tc.SetDeadline(deadline)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if err := tc.HandshakeContext(ctx); err != nil {
		return
	}
	cs := tc.ConnectionState()
	key, ok := peerKey(cs)
	if !ok || cs.NegotiatedProtocol != ALPN {
		return
	}
	remote := tc.RemoteAddr().String()
	var hel hello
	if err := readFrame(tc, &hel); err != nil {
		switch {
		case errors.Is(err, errFrameTooLarge):
			h.refuse(tc, errHelloTooLarge())
		case !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF):
			h.refuse(tc, errProtocol(err))
		}
		return
	}
	if hel.V != protocolVersion {
		h.refuse(tc, errVersionUnsupported(protocolVersion))
		return
	}
	switch hel.Mode {
	case modeJoin:
		h.join(ctx, tc, key, hel, remote)
	case modeLeave:
		h.leave(ctx, tc, key, remote)
	case modeLink:
		h.link(ctx, tc, key, hel, remote, release)
	default:
		h.refuse(tc, errProtocol(fmt.Errorf("unknown mode %q", cleanText(hel.Mode, 20))))
	}
}

func (h *Hub) refuse(c net.Conn, e *Error) {
	writeFrame(c, welcome{V: protocolVersion, Error: e.wire()})
}

func (h *Hub) welcomeFor(m Machine) welcome {
	return welcome{OK: true, V: protocolVersion, MachineID: m.ID, Name: m.Name, Version: h.opts.Version, HeartbeatMs: h.opts.Heartbeat.Milliseconds()}
}

func (h *Hub) join(ctx context.Context, tc *tls.Conn, key ed25519.PublicKey, hel hello, remote string) {
	m, ev, e := h.pair(ctx, key, hel, remote)
	if e != nil {
		h.refuse(tc, e)
		h.log.Warn("machine join refused", "addr", remoteIP(remote), "code", e.Code, "err", e.Err)
		h.emit(Event{Kind: EventJoinRefused, At: h.now(), Address: remoteIP(remote), Code: e.Code})
		return
	}
	if ev != nil {
		h.log.Info("machine joined", "machine", m.ID, "name", m.Name, "addr", ev.Address)
		h.emit(*ev)
	}
	writeFrame(tc, h.welcomeFor(m))
}

// pair checks a join code and adds the machine. The event is nil when the
// machine had already joined with this code and lost the answer.
func (h *Hub) pair(ctx context.Context, key ed25519.PublicKey, hel hello, remote string) (Machine, *Event, *Error) {
	h.joinMu.Lock()
	defer h.joinMu.Unlock()
	now := h.now()
	from := addrPrefix(remote)
	if wait := h.guard.wait(now, from); wait > 0 {
		return Machine{}, nil, errJoinRateLimited(wait)
	}
	code, err := NormalizeJoinCode(hel.Code)
	if err != nil {
		h.guard.fail(now, from)
		return Machine{}, nil, errJoinCodeMalformed()
	}
	codes, err := h.store.JoinCodes(ctx)
	if err != nil {
		return Machine{}, nil, errDashboard(err)
	}
	jc, ok := findCode(codes, hashCode(h.codeKey, code))
	if !ok {
		h.guard.fail(now, from)
		return Machine{}, nil, errJoinCodeWrong()
	}
	m, found, err := h.store.MachineByKey(ctx, key)
	if err != nil {
		return Machine{}, nil, errDashboard(err)
	}
	switch {
	case jc.MachineID != "":
		if found && m.ID == jc.MachineID && !m.Removed() {
			return m, nil, nil
		}
		h.guard.fail(now, from)
		return Machine{}, nil, errJoinCodeUsed()
	case jc.State(now) == JoinCodeExpired:
		return Machine{}, nil, errJoinCodeExpired()
	case found && m.Removed():
		return Machine{}, nil, errMachineRemoved()
	case found:
		return Machine{}, nil, errAlreadyJoined(m.Name)
	}
	all, err := h.store.Machines(ctx)
	if err != nil {
		return Machine{}, nil, errDashboard(err)
	}
	var names []string
	ids := map[string]bool{}
	for _, x := range all {
		ids[x.ID] = true
		if !x.Removed() {
			names = append(names, x.Name)
		}
	}
	id := newID()
	for ids[id] {
		id = newID()
	}
	name := CleanName(hel.Name)
	if name == "" {
		name = "machine"
	}
	m = Machine{ID: id, Name: uniqueName(name, names), PublicKey: slices.Clone(key), JoinedAt: now,
		JoinedFrom: remoteIP(remote), CreatedBy: jc.CreatedBy, Version: cleanVersion(hel.Version)}
	switch err := h.store.Pair(ctx, jc.ID, m); {
	case errors.Is(err, ErrCodeUsed):
		h.guard.fail(now, from)
		return Machine{}, nil, errJoinCodeUsed()
	case errors.Is(err, ErrNotFound):
		return Machine{}, nil, errJoinCodeWrong()
	case err != nil:
		return Machine{}, nil, errDashboard(err)
	}
	return m, &Event{Kind: EventJoined, At: now, MachineID: m.ID, Name: m.Name, Actor: jc.CreatedBy, Address: m.JoinedFrom}, nil
}

func (h *Hub) leave(ctx context.Context, tc *tls.Conn, key ed25519.PublicKey, remote string) {
	m, found, err := h.store.MachineByKey(ctx, key)
	switch {
	case err != nil:
		h.refuse(tc, errDashboard(err))
		return
	case !found:
		h.refuse(tc, errMachineUnknown())
		return
	}
	if !m.Removed() {
		if err := h.remove(ctx, m, "machine:"+m.ID, EventLeft, remoteIP(remote)); err != nil {
			h.refuse(tc, errDashboard(err))
			return
		}
	}
	writeFrame(tc, welcome{OK: true, V: protocolVersion, MachineID: m.ID, Name: m.Name})
}

// Remove removes a machine: its key stops working at once, and its
// connection is closed, with any requests in flight. by is the account
// that removed it.
func (h *Hub) Remove(ctx context.Context, id, by string) error {
	m, found, err := h.store.Machine(ctx, id)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	return h.remove(ctx, m, by, EventRemoved, "")
}

func (h *Hub) remove(ctx context.Context, m Machine, by string, kind EventKind, addr string) error {
	h.mu.Lock()
	h.revoked[m.ID] = true
	s := h.sessions[m.ID]
	delete(h.sessions, m.ID)
	h.mu.Unlock()
	if s != nil {
		s.close(errRevoked)
	}
	if m.Removed() {
		return nil
	}
	if err := h.store.Revoke(ctx, m.ID, h.now(), by); err != nil {
		return err
	}
	h.log.Info("machine removed", "machine", m.ID, "name", m.Name, "by", by)
	h.emit(Event{Kind: kind, At: h.now(), MachineID: m.ID, Name: m.Name, Actor: by, Address: addr})
	return nil
}

func (h *Hub) link(ctx context.Context, tc *tls.Conn, key ed25519.PublicKey, hel hello, remote string, release func()) {
	m, found, err := h.store.MachineByKey(ctx, key)
	switch {
	case err != nil:
		h.refuse(tc, errDashboard(err))
		return
	case !found:
		h.refuse(tc, errMachineUnknown())
		return
	case m.Removed() || h.isRevoked(m.ID):
		h.refuse(tc, errMachineRemoved())
		return
	}
	if err := writeFrame(tc, h.welcomeFor(m)); err != nil {
		return
	}
	tc.SetDeadline(time.Time{})
	s, err := h.newSession(ctx, tc, m, hel, remote)
	if err != nil {
		h.log.Warn("machine link failed to start", "machine", m.ID, "err", err)
		return
	}
	release()
	old, ok := h.register(s)
	if !ok {
		s.close(errHubClosed)
		return
	}
	if old != nil {
		old.close(errReplaced)
	}
	h.seen(s)
	h.log.Info("machine connected", "machine", m.ID, "name", m.Name, "addr", s.addr, "version", s.version)
	h.emit(Event{Kind: EventConnected, At: s.connectedAt, MachineID: m.ID, Name: m.Name, Address: s.addr})

	s.heartbeat()

	if !h.unregister(s) {
		return
	}
	h.seen(s)
	code := CodeDropped
	if errors.Is(s.reason, errPingTimeout) {
		code = CodeHeartbeatTimeout
	}
	h.log.Info("machine disconnected", "machine", m.ID, "name", m.Name, "reason", s.reason)
	h.emit(Event{Kind: EventDisconnected, At: h.now(), MachineID: m.ID, Name: m.Name, Address: s.addr, Code: code})
}

func (h *Hub) isRevoked(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.revoked[id]
}

// register makes s the machine's connection, returning the one it
// replaces.
func (h *Hub) register(s *session) (*session, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.revoked[s.id] {
		return nil, false
	}
	old := h.sessions[s.id]
	h.sessions[s.id] = s
	st := h.statsFor(s.id)
	st.name = s.name
	st.connects = pushRecent(st.connects, s.connectedAt)
	if old != nil && old.instance != s.instance && s.instance != "" && old.alive(s.connectedAt) {
		st.clones = pushRecent(st.clones, s.connectedAt)
	}
	return old, true
}

// unregister reports whether s was still the machine's connection.
func (h *Hub) unregister(s *session) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.statsFor(s.id)
	if ls := s.seenAt(); ls.After(st.lastSeen) {
		st.lastSeen = ls
	}
	if h.sessions[s.id] != s {
		return false
	}
	delete(h.sessions, s.id)
	return true
}

func (h *Hub) statsFor(id string) *machineStats {
	st := h.stats[id]
	if st == nil {
		st = &machineStats{}
		h.stats[id] = st
	}
	return st
}

// seen records the session's last contact in the store.
func (h *Hub) seen(s *session) {
	ctx, cancel := context.WithTimeout(context.Background(), h.opts.HandshakeTimeout)
	defer cancel()
	s.mu.Lock()
	at, version := s.lastSeen, s.version
	s.mu.Unlock()
	if err := h.store.Seen(ctx, s.id, at, version, s.addr); err != nil {
		h.log.Warn("could not record when a machine was last seen", "machine", s.id, "err", err)
	}
}

// Connected reports whether the machine is connected now.
func (h *Hub) Connected(id string) bool { return h.session(id) != nil }

func (h *Hub) session(id string) *session {
	h.mu.Lock()
	s := h.sessions[id]
	h.mu.Unlock()
	if s == nil || s.isDone() {
		return nil
	}
	return s
}

func (h *Hub) nameOf(id string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if st := h.stats[id]; st != nil && st.name != "" {
		return st.name
	}
	return "That machine"
}

// NewJoinCode makes a join code for one machine, made by the account by.
// The code itself is only returned here, to show once; the store keeps a
// keyed hash of it. Making a code drops codes expired over an hour ago
// and, beyond MaxWaitingCodes waiting ones, the oldest.
func (h *Hub) NewJoinCode(ctx context.Context, by string) (string, JoinCode, error) {
	now := h.now()
	codes, err := h.store.JoinCodes(ctx)
	if err != nil {
		return "", JoinCode{}, err
	}
	var waiting []JoinCode
	for _, c := range codes {
		switch c.State(now) {
		case JoinCodeWaiting:
			waiting = append(waiting, c)
		case JoinCodeExpired:
			if now.Sub(c.ExpiresAt) > codeKeep {
				h.store.DeleteJoinCode(ctx, c.ID)
			}
		case JoinCodeUsed:
			if now.Sub(c.UsedAt) > codeKeep {
				h.store.DeleteJoinCode(ctx, c.ID)
			}
		}
	}
	slices.SortFunc(waiting, func(a, b JoinCode) int { return a.CreatedAt.Compare(b.CreatedAt) })
	for len(waiting) >= MaxWaitingCodes {
		if err := h.store.DeleteJoinCode(ctx, waiting[0].ID); err != nil && !errors.Is(err, ErrNotFound) {
			return "", JoinCode{}, err
		}
		waiting = waiting[1:]
	}
	code := newJoinCode()
	jc := JoinCode{ID: newID(), Hash: hashCode(h.codeKey, code), CreatedAt: now, ExpiresAt: now.Add(CodeTTL), CreatedBy: by}
	if err := h.store.AddJoinCode(ctx, jc); err != nil {
		return "", JoinCode{}, err
	}
	return code, jc, nil
}

// JoinCodes lists the waiting codes, and those used or expired in the
// last hour.
func (h *Hub) JoinCodes(ctx context.Context) ([]JoinCode, error) { return h.store.JoinCodes(ctx) }

// CancelJoinCode deletes a join code before it is used.
func (h *Hub) CancelJoinCode(ctx context.Context, id string) error {
	return h.store.DeleteJoinCode(ctx, id)
}

// newID returns an id in the panel's format: 10 characters of a-z and
// 2-9 without l, o, 0 and 1.
func newID() string {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	var b [10]byte
	rand.Read(b[:])
	for i := range b {
		b[i] = alphabet[b[i]&31]
	}
	return string(b[:])
}

// session is one machine's live connection.
type session struct {
	hub         *Hub
	id, name    string
	host        string
	conn        *plainConn
	cc          *http.ClientConn
	instance    string
	addr        string
	connectedAt time.Time

	done      chan struct{}
	closeOnce sync.Once
	reason    error

	mu       sync.Mutex
	lastSeen time.Time
	rtt      time.Duration
	skew     time.Duration
	pinged   bool
	version  string
}

func (h *Hub) newSession(ctx context.Context, tc *tls.Conn, m Machine, hel hello, remote string) (*session, error) {
	pc := newPlainConn(tc)
	var dialed atomic.Bool
	tr := &http.Transport{
		Protocols: h2c(),
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			if !dialed.CompareAndSwap(false, true) {
				return nil, errors.New("machinelink: the link connection is already in use")
			}
			return pc, nil
		},
		DisableCompression:     true,
		MaxResponseHeaderBytes: maxHeaderBytes,
		HTTP2: &http.HTTP2Config{
			SendPingTimeout:  2 * h.opts.Heartbeat,
			PingTimeout:      h.opts.HeartbeatTimeout,
			WriteByteTimeout: 2 * h.opts.HeartbeatTimeout,
		},
	}
	host := m.ID + ".machine.invalid"
	cc, err := tr.NewClientConn(ctx, "http", host+":80")
	if err != nil {
		pc.Close()
		return nil, err
	}
	now := h.now()
	s := &session{hub: h, id: m.ID, name: m.Name, host: host, conn: pc, cc: cc,
		instance: cleanInstance(hel.Instance), addr: remoteIP(remote), connectedAt: now,
		done: make(chan struct{}), lastSeen: now, version: cleanVersion(hel.Version)}
	// The hook may run inside RoundTrip, which close would wait for.
	cc.SetStateHook(func(cc *http.ClientConn) {
		if cc.Err() != nil {
			go s.close(errLinkClosed)
		}
	})
	go func() {
		select {
		case <-pc.done:
			s.close(errLinkClosed)
		case <-s.done:
		}
	}()
	return s, nil
}

func (s *session) close(reason error) {
	s.closeOnce.Do(func() {
		s.reason = reason
		close(s.done)
		s.conn.Close()
		s.cc.Close()
	})
}

func (s *session) isDone() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *session) seenAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSeen
}

// alive reports whether the session answered a heartbeat recently.
func (s *session) alive(now time.Time) bool {
	return now.Sub(s.seenAt()) <= s.hub.opts.Heartbeat+s.hub.opts.HeartbeatTimeout
}

// heartbeat checks the machine every Heartbeat until the connection ends.
func (s *session) heartbeat() {
	h := s.hub
	t := time.NewTimer(0)
	defer t.Stop()
	saved := time.Now()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
		}
		if err := s.ping(); err != nil {
			s.close(err)
			return
		}
		if time.Since(saved) >= 5*time.Minute {
			h.seen(s)
			saved = time.Now()
		}
		t.Reset(h.opts.Heartbeat)
	}
}

func (s *session) ping() error {
	h := s.hub
	if err := s.cc.Reserve(); err != nil {
		if cerr := s.cc.Err(); cerr != nil {
			return cerr
		}
		// Every stream is busy. HTTP/2's own pings watch the connection
		// until one is free.
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), h.opts.HeartbeatTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+s.host+pingPath, nil)
	if err != nil {
		s.cc.Release()
		return err
	}
	start := time.Now()
	resp, err := s.cc.RoundTrip(req)
	if err != nil {
		if ctx.Err() != nil {
			return errPingTimeout
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("machinelink: heartbeat answered %d", resp.StatusCode)
	}
	var p pong
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<10)).Decode(&p); err != nil {
		if ctx.Err() != nil {
			return errPingTimeout
		}
		return fmt.Errorf("machinelink: bad heartbeat answer: %w", err)
	}
	rtt := time.Since(start)
	now := h.now()
	s.mu.Lock()
	s.lastSeen, s.rtt, s.pinged = now, rtt, true
	if v := cleanVersion(p.Version); v != "" {
		s.version = v
	}
	if !p.Time.IsZero() {
		s.skew = p.Time.Sub(now.Add(-rtt / 2))
	}
	s.mu.Unlock()
	return nil
}

// Transport returns the RoundTripper for one machine: requests through it
// go to that machine's agent, over its link, as they would go to the
// local agent over its socket. Only the allowed routes pass, requests
// that change something need an actor (ActorHeader or WithActor), and a
// machine that is not connected fails at once with
// machine_not_connected. The URL's host is ignored.
func (h *Hub) Transport(machineID string) http.RoundTripper {
	return &machineTransport{hub: h, id: machineID}
}

type machineTransport struct {
	hub *Hub
	id  string
}

func (t *machineTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	h := t.hub
	fail := func(err error) (*http.Response, error) {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, err
	}
	name := h.nameOf(t.id)
	if req.URL == nil {
		return fail(errors.New("machinelink: request without a URL"))
	}
	if req.Response != nil {
		return fail(errRedirect(name))
	}
	route, ok := h.allow.match(req.Method, req.URL)
	if !ok {
		return fail(errRouteNotAllowed(req.Method, req.URL.Path))
	}
	raw := actorOf(req)
	actor, ok := cleanActor(raw)
	if !ok && (raw != "" || mutating(req.Method)) {
		return fail(errActorRequired(name))
	}
	s := h.session(t.id)
	if s == nil {
		if h.isRevoked(t.id) {
			return fail(errRemoved(t.id, name))
		}
		return fail(errNotConnected(t.id, name))
	}
	parent := req.Context()
	var ctx context.Context
	var cancel context.CancelFunc
	if route.Stream {
		ctx, cancel = context.WithCancel(parent)
	} else {
		ctx, cancel = context.WithTimeout(parent, h.opts.RequestTimeout)
	}
	out := req.Clone(ctx)
	out.URL = &url.URL{Scheme: "http", Host: s.host, Path: req.URL.Path, RawQuery: req.URL.RawQuery}
	out.Host, out.RequestURI, out.Close = "", "", false
	if out.Header == nil {
		out.Header = make(http.Header)
	}
	stripHeaders(out.Header)
	out.Header.Del(ActorHeader)
	if actor != "" {
		out.Header.Set(ActorHeader, actor)
	}
	var body *requestBody
	if out.Body != nil && out.Body != http.NoBody {
		body = &requestBody{rc: out.Body}
		out.Body = body
	}
	resp, err := s.cc.RoundTrip(out)
	if err != nil {
		cancel()
		if body != nil {
			body.Close()
			if rerr := body.readErr(); rerr != nil && errors.Is(err, rerr) {
				return nil, err
			}
		}
		return nil, t.mapErr(s, name, parent, ctx, err)
	}
	stripHeaders(resp.Header)
	resp.Header.Del("Set-Cookie")
	if route.Stream {
		resp.Body = &streamBody{rc: resp.Body, cancel: cancel}
		return resp, nil
	}
	if resp.ContentLength > h.opts.MaxResponseBytes {
		resp.Body.Close()
		cancel()
		return nil, errTooLarge(name, h.opts.MaxResponseBytes)
	}
	resp.Body = &limitedBody{rc: resp.Body, left: h.opts.MaxResponseBytes, ctx: ctx, parent: parent, cancel: cancel,
		tooLarge: errTooLarge(name, h.opts.MaxResponseBytes), timeout: errTimeout(name, h.opts.RequestTimeout)}
	return resp, nil
}

func (t *machineTransport) mapErr(s *session, name string, parent, ctx context.Context, err error) error {
	switch {
	case parent.Err() != nil:
		return parent.Err()
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return errTimeout(name, t.hub.opts.RequestTimeout)
	case s.isDone() && errors.Is(s.reason, errRevoked):
		return errRemoved(t.id, name)
	case !s.isDone() && s.cc.Err() == nil:
		// The link is fine, so only this request failed: HTTP/2 marks a
		// connection closed before it fails the requests on it.
		return errBadReply(name, err)
	default:
		return errDropped(t.id, name, err)
	}
}

// requestBody lets RoundTrip close a request body that HTTP/2 may have
// closed already (some of its errors leave the body open), and tell the
// body's own errors from the link's.
type requestBody struct {
	rc   io.ReadCloser
	once sync.Once
	cerr error

	mu   sync.Mutex
	rerr error
}

func (b *requestBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if err != nil && err != io.EOF {
		b.mu.Lock()
		if b.rerr == nil {
			b.rerr = err
		}
		b.mu.Unlock()
	}
	return n, err
}

func (b *requestBody) Close() error {
	b.once.Do(func() { b.cerr = b.rc.Close() })
	return b.cerr
}

func (b *requestBody) readErr() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rerr
}

// limitedBody ends a reply that grows past its limit or outlives its
// request's time limit.
type limitedBody struct {
	rc          io.ReadCloser
	left        int64
	ctx, parent context.Context
	cancel      context.CancelFunc
	tooLarge    error
	timeout     error
	err         error
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	if b.left <= 0 {
		var one [1]byte
		n, err := b.rc.Read(one[:])
		if n > 0 {
			b.err = b.tooLarge
			return 0, b.err
		}
		return 0, b.fix(err)
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	return n, b.fix(err)
}

func (b *limitedBody) fix(err error) error {
	if err != nil && err != io.EOF && b.parent.Err() == nil && b.ctx.Err() == context.DeadlineExceeded {
		b.err = b.timeout
		return b.err
	}
	return err
}

func (b *limitedBody) Close() error {
	err := b.rc.Close()
	b.cancel()
	return err
}

// streamBody is a streamed reply, which has no time limit; closing it
// releases its request's context.
type streamBody struct {
	rc     io.ReadCloser
	cancel context.CancelFunc
}

func (b *streamBody) Read(p []byte) (int, error) { return b.rc.Read(p) }

func (b *streamBody) Close() error {
	err := b.rc.Close()
	b.cancel()
	return err
}

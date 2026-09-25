package machinelink

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

type dialFunc func(ctx context.Context, network, address string) (net.Conn, error)

func defaultDial() dialFunc {
	return (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
}

// Dashboard is what a machine keeps about the dashboard it joined. Its
// file needs no secrecy, but nobody else may change it: it says which
// dashboard the machine obeys.
type Dashboard struct {
	Address string            `json:"address"`
	Key     ed25519.PublicKey `json:"key"`
	// MachineID and Name are what the dashboard calls this machine.
	MachineID string    `json:"machineId"`
	Name      string    `json:"name"`
	JoinedAt  time.Time `json:"joinedAt"`
}

// Fingerprint is the dashboard's key fingerprint.
func (d Dashboard) Fingerprint() string { return Fingerprint(d.Key) }

func (d Dashboard) check() (Address, error) {
	a, err := ParseAddress(d.Address)
	if err != nil {
		return Address{}, err
	}
	if len(d.Key) != ed25519.PublicKeySize {
		return Address{}, errors.New("machinelink: the dashboard's key is missing")
	}
	if !reMachineID.MatchString(d.MachineID) {
		return Address{}, errors.New("machinelink: the machine id is missing")
	}
	return a, nil
}

// Save writes the dashboard's details to path, readable only by its
// owner, replacing the file atomically.
func (d Dashboard) Save(path string) error {
	if _, err := d.check(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'), 0o600)
}

// LoadDashboard reads a file written by Dashboard.Save. It refuses a file
// that other users of the computer could have changed.
func LoadDashboard(path string) (Dashboard, error) {
	f, err := os.Open(path)
	if err != nil {
		return Dashboard{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Dashboard{}, err
	}
	if !st.Mode().IsRegular() {
		return Dashboard{}, keyFileError(path, "is not a regular file", "")
	}
	if st.Mode().Perm()&0o022 != 0 {
		return Dashboard{}, keyFileError(path, "can be changed by other users of this computer", "sudo chmod 600 "+path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxKeyFileBytes+1))
	if err != nil {
		return Dashboard{}, err
	}
	var d Dashboard
	if len(b) > maxKeyFileBytes || json.Unmarshal(b, &d) != nil {
		return Dashboard{}, keyFileError(path, "is damaged", "")
	}
	if _, err := d.check(); err != nil {
		return Dashboard{}, keyFileError(path, "is damaged", "")
	}
	return d, nil
}

// dialDashboard connects, checks the dashboard's key, and exchanges the
// hello and welcome. Nothing is sent before the key check passes.
func dialDashboard(ctx context.Context, dial dialFunc, a Address, id *Identity, p pin, joined bool, h hello) (*tls.Conn, welcome, error) {
	conn, err := dial(ctx, "tcp", a.HostPort())
	if err != nil {
		return nil, welcome{}, errUnreachable(a, err)
	}
	tc := tls.Client(conn, clientTLS(id, a, p))
	if dl, ok := ctx.Deadline(); ok {
		tc.SetDeadline(dl)
	}
	fail := func(err error) (*tls.Conn, welcome, error) {
		tc.Close()
		return nil, welcome{}, err
	}
	if err := tc.HandshakeContext(ctx); err != nil {
		return fail(handshakeError(a, joined, err))
	}
	if err := writeFrame(tc, h); err != nil {
		return fail(handshakeError(a, joined, err))
	}
	var w welcome
	if err := readFrame(tc, &w); err != nil {
		if errors.Is(err, errFrameTooLarge) || errors.Is(err, errBadFrame) {
			return fail(errProtocol(err))
		}
		return fail(handshakeError(a, joined, err))
	}
	if !w.OK {
		if w.Error == nil {
			return fail(errProtocol(errors.New("refused without a reason")))
		}
		return fail(w.Error.err())
	}
	if w.V != protocolVersion {
		return fail(errVersionUnsupported(w.V))
	}
	if !reMachineID.MatchString(w.MachineID) {
		return fail(errProtocol(errors.New("bad machine id in welcome")))
	}
	return tc, w, nil
}

// JoinOptions are what a machine needs to join a dashboard: the parts of
// the join command, and its own new key.
type JoinOptions struct {
	Address     string
	Code        string
	Fingerprint string
	// Identity is the machine's key. Make a new one for every join: a key
	// that was removed from a dashboard never works there again.
	Identity *Identity
	// Name is what the dashboard should call the machine, usually its
	// host name; the dashboard makes it unique.
	Name    string
	Version string
	Dial    func(ctx context.Context, network, address string) (net.Conn, error)
	// Timeout bounds the whole join (30s).
	Timeout time.Duration
	Now     func() time.Time
}

// Join proves to the dashboard that this machine has a join code, and
// swaps keys with it. The machine checks the dashboard's key against the
// fingerprint before it sends the code, so a machine in the middle never
// sees the code. Save the result and the identity, then Run a Link.
func Join(ctx context.Context, o JoinOptions) (Dashboard, error) {
	a, err := ParseAddress(o.Address)
	if err != nil {
		return Dashboard{}, err
	}
	code, err := NormalizeJoinCode(o.Code)
	if err != nil {
		return Dashboard{}, err
	}
	fp, err := ParseFingerprint(o.Fingerprint)
	if err != nil {
		return Dashboard{}, err
	}
	if o.Identity == nil {
		return Dashboard{}, errors.New("machinelink: joining needs the machine's identity")
	}
	dial := dialFunc(o.Dial)
	if dial == nil {
		dial = defaultDial()
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	tc, w, err := dialDashboard(ctx, dial, a, o.Identity, pin{fingerprint: fp}, false,
		hello{V: protocolVersion, Mode: modeJoin, Code: code, Name: CleanName(o.Name), Version: cleanVersion(o.Version)})
	if err != nil {
		return Dashboard{}, err
	}
	defer tc.Close()
	key, _ := peerKey(tc.ConnectionState())
	name := CleanName(w.Name)
	if name == "" {
		name = w.MachineID
	}
	return Dashboard{Address: a.String(), Key: slices.Clone(key), MachineID: w.MachineID, Name: name, JoinedAt: o.Now().UTC()}, nil
}

// LeaveOptions are what a machine needs to leave its dashboard.
type LeaveOptions struct {
	Dashboard Dashboard
	Identity  *Identity
	Dial      func(ctx context.Context, network, address string) (net.Conn, error)
	// Timeout bounds the whole exchange (30s).
	Timeout time.Duration
}

// Leave tells the dashboard to remove this machine and forget its key.
// It succeeds when the dashboard no longer knows the machine. If the
// dashboard can't be reached, the caller may delete its files anyway;
// the dashboard then shows the machine as offline until it is removed
// there.
func Leave(ctx context.Context, o LeaveOptions) error {
	a, err := o.Dashboard.check()
	if err != nil {
		return err
	}
	if o.Identity == nil {
		return errors.New("machinelink: leaving needs the machine's identity")
	}
	dial := dialFunc(o.Dial)
	if dial == nil {
		dial = defaultDial()
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	tc, _, err := dialDashboard(ctx, dial, a, o.Identity, pin{key: o.Dashboard.Key}, true, hello{V: protocolVersion, Mode: modeLeave})
	switch CodeOf(err) {
	case "":
		if err != nil {
			return err
		}
		tc.Close()
		return nil
	case CodeMachineRemoved, CodeMachineUnknown:
		return nil
	default:
		return err
	}
}

// LinkOptions configure a machine's side of the link.
type LinkOptions struct {
	Dashboard Dashboard
	Identity  *Identity
	// Handler answers the dashboard's requests: the agent's API, usually
	// through AgentProxy. Only the allowed routes reach it.
	Handler http.Handler
	// Routes are the agent requests the dashboard may send: the agent's
	// route table, as on the dashboard.
	Routes  []Route
	Version string
	// OnRequest, when set, is told about every request the dashboard
	// sent, for this machine's own log. It should return quickly.
	OnRequest func(RequestRecord)
	Now       func() time.Time
	Logger    *slog.Logger
	Dial      func(ctx context.Context, network, address string) (net.Conn, error)
	// MinBackoff and MaxBackoff bound the wait between attempts (1s and
	// 1m); after a connection that lasted StableAfter (1m) the wait starts
	// again from MinBackoff. RetryLater (1h) is the wait after a refusal
	// that trying again soon can't fix.
	MinBackoff  time.Duration
	MaxBackoff  time.Duration
	StableAfter time.Duration
	RetryLater  time.Duration
	// HandshakeTimeout bounds connecting and the hello (10s).
	HandshakeTimeout time.Duration
	// MaxRequestBytes bounds the body of a request that is not a stream
	// (1 MiB).
	MaxRequestBytes int64
}

// RequestRecord is one request the dashboard sent, for the machine's log.
type RequestRecord struct {
	At     time.Time `json:"at"`
	Actor  string    `json:"actor,omitempty"`
	Method string    `json:"method"`
	// Route is the matched route, such as "POST /v1/servers/{id}/start";
	// empty when the request was refused.
	Route    string        `json:"route,omitempty"`
	Path     string        `json:"path"`
	Status   int           `json:"status"`
	Bytes    int64         `json:"bytes"`
	Duration time.Duration `json:"duration"`
}

// LinkState is where a machine's link is.
type LinkState string

const (
	LinkConnecting LinkState = "connecting"
	LinkConnected  LinkState = "connected"
	LinkRetrying   LinkState = "retrying"
	// LinkRemoved means the dashboard removed this machine; Run returned.
	LinkRemoved LinkState = "removed"
	LinkStopped LinkState = "stopped"
)

// LinkStatus is what the machine shows about its link, for example in
// playkeeper status.
type LinkStatus struct {
	State       LinkState `json:"state"`
	Dashboard   string    `json:"dashboard"`
	MachineID   string    `json:"machineId"`
	Name        string    `json:"name"`
	ConnectedAt time.Time `json:"connectedAt,omitzero"`
	// LastSeen is the dashboard's last heartbeat.
	LastSeen    time.Time `json:"lastSeen,omitzero"`
	NextAttempt time.Time `json:"nextAttempt,omitzero"`
	Problem     *Problem  `json:"problem,omitempty"`
}

// Link is a machine's connection to its dashboard: it dials out, proves
// who it is, and answers the dashboard's requests until stopped.
type Link struct {
	o        LinkOptions
	addr     Address
	allow    *allowlist
	now      func() time.Time
	log      *slog.Logger
	dial     dialFunc
	instance string

	mu      sync.Mutex
	status  LinkStatus
	running bool
}

// NewLink checks the options and fills in defaults.
func NewLink(o LinkOptions) (*Link, error) {
	a, err := o.Dashboard.check()
	if err != nil {
		return nil, err
	}
	if o.Identity == nil || o.Handler == nil {
		return nil, errors.New("machinelink: a link needs the machine's identity and a handler")
	}
	allow, err := newAllowlist(o.Routes)
	if err != nil {
		return nil, err
	}
	setDefault(&o.MinBackoff, time.Second)
	setDefault(&o.MaxBackoff, time.Minute)
	setDefault(&o.StableAfter, time.Minute)
	setDefault(&o.RetryLater, time.Hour)
	setDefault(&o.HandshakeTimeout, 10*time.Second)
	if o.MaxBackoff < o.MinBackoff {
		o.MaxBackoff = o.MinBackoff
	}
	if o.MaxRequestBytes <= 0 {
		o.MaxRequestBytes = 1 << 20
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	dial := dialFunc(o.Dial)
	if dial == nil {
		dial = defaultDial()
	}
	var inst [8]byte
	rand.Read(inst[:])
	o.Version = cleanVersion(o.Version)
	l := &Link{o: o, addr: a, allow: allow, now: o.Now, log: o.Logger, dial: dial, instance: hex.EncodeToString(inst[:])}
	l.status = LinkStatus{State: LinkStopped, Dashboard: a.String(), MachineID: o.Dashboard.MachineID, Name: o.Dashboard.Name}
	return l, nil
}

// Status is the link's state now.
func (l *Link) Status() LinkStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.status
	if s.Problem != nil {
		p := *s.Problem
		s.Problem = &p
	}
	return s
}

func (l *Link) update(f func(*LinkStatus)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f(&l.status)
}

// Run keeps the link up until ctx ends, reconnecting with backoff. It
// returns ctx's error, or a machine_removed *Error when the dashboard
// removed this machine: then the machine should forget the dashboard
// (delete its files) rather than run again.
func (l *Link) Run(ctx context.Context) error {
	l.mu.Lock()
	if l.running {
		l.mu.Unlock()
		return errors.New("machinelink: the link is already running")
	}
	l.running = true
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.running = false
		l.mu.Unlock()
	}()
	stopped := func() error {
		l.update(func(s *LinkStatus) {
			s.State, s.ConnectedAt, s.NextAttempt = LinkStopped, time.Time{}, time.Time{}
		})
		return ctx.Err()
	}
	attempt := 0
	for {
		l.update(func(s *LinkStatus) { s.State, s.NextAttempt = LinkConnecting, time.Time{} })
		lasted, err := l.connect(ctx)
		if ctx.Err() != nil {
			return stopped()
		}
		if CodeOf(err) == CodeMachineRemoved {
			l.update(func(s *LinkStatus) {
				s.State, s.ConnectedAt, s.Problem = LinkRemoved, time.Time{}, problemOf(err)
			})
			l.log.Warn("the dashboard removed this machine", "dashboard", l.addr.String())
			return err
		}
		if lasted >= l.o.StableAfter {
			attempt = 0
		}
		var wait time.Duration
		switch CodeOf(err) {
		case CodeMachineUnknown, CodeDashboardKeyMismatch, CodeVersionUnsupported, CodeNotADashboard:
			wait = l.o.RetryLater
		default:
			wait = backoff(l.o.MinBackoff, l.o.MaxBackoff, attempt)
			attempt++
		}
		wait = max(wait, retryAfterOf(err))
		l.update(func(s *LinkStatus) {
			s.State, s.ConnectedAt, s.NextAttempt, s.Problem = LinkRetrying, time.Time{}, l.now().Add(wait), problemOf(err)
		})
		l.log.Info("no link to the dashboard; trying again", "in", wait.Round(time.Millisecond), "code", CodeOf(err), "err", err)
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return stopped()
		case <-t.C:
		}
	}
}

// backoff doubles from lo up to hi, then picks a random wait in the upper
// half, so machines that lost the dashboard together don't all come back
// at the same moment.
func backoff(lo, hi time.Duration, attempt int) time.Duration {
	d := lo
	for i := 0; i < attempt && d < hi; i++ {
		d *= 2
	}
	d = min(d, hi)
	return d/2 + mrand.N(d/2+1)
}

func problemOf(err error) *Problem {
	var e *Error
	if errors.As(err, &e) {
		return &Problem{Code: e.Code, Params: e.Params, Message: e.Msg, Hint: e.Hint}
	}
	p := errLinkDropped(err)
	return &Problem{Code: p.Code, Message: p.Msg, Hint: p.Hint}
}

// connect makes one connection and serves it until it ends. It reports
// how long the connection was up.
func (l *Link) connect(ctx context.Context) (time.Duration, error) {
	hctx, cancel := context.WithTimeout(ctx, l.o.HandshakeTimeout)
	tc, w, err := dialDashboard(hctx, l.dial, l.addr, l.o.Identity, pin{key: l.o.Dashboard.Key}, true,
		hello{V: protocolVersion, Mode: modeLink, Version: l.o.Version, Instance: l.instance})
	cancel()
	if err != nil {
		return 0, err
	}
	if w.MachineID != l.o.Dashboard.MachineID {
		tc.Close()
		return 0, errProtocol(fmt.Errorf("the dashboard calls this machine %s, not %s", w.MachineID, l.o.Dashboard.MachineID))
	}
	tc.SetDeadline(time.Time{})
	hb := time.Duration(w.HeartbeatMs) * time.Millisecond
	if hb <= 0 {
		hb = 15 * time.Second
	}
	hb = min(max(hb, 50*time.Millisecond), 10*time.Minute)
	up := time.Now()
	now := l.now()
	l.update(func(s *LinkStatus) {
		s.State, s.ConnectedAt, s.LastSeen, s.NextAttempt, s.Problem = LinkConnected, now, now, time.Time{}, nil
		if n := CleanName(w.Name); n != "" {
			s.Name = n
		}
	})
	l.log.Info("connected to the dashboard", "dashboard", l.addr.String(), "machine", w.MachineID)
	err = l.serve(ctx, tc, hb)
	return time.Since(up), err
}

// activity tracks requests on a connection, for the watchdog.
type activity struct {
	start    time.Time
	last     atomic.Int64
	inFlight atomic.Int64
}

func (a *activity) begin() {
	a.inFlight.Add(1)
	a.last.Store(int64(time.Since(a.start)))
}

func (a *activity) end() {
	a.last.Store(int64(time.Since(a.start)))
	a.inFlight.Add(-1)
}

// idle is how long the connection has had no request at all.
func (a *activity) idle() time.Duration {
	if a.inFlight.Load() > 0 {
		return 0
	}
	return time.Since(a.start) - time.Duration(a.last.Load())
}

// serve answers the dashboard's HTTP/2 requests on the connection. The
// dashboard sends a heartbeat every hb; after three missed ones the
// connection is dropped and made again. HTTP/2's own pings cover
// connections whose requests are all long streams.
func (l *Link) serve(ctx context.Context, tc *tls.Conn, hb time.Duration) error {
	pc := newPlainConn(tc)
	act := &activity{start: time.Now()}
	srv := &http.Server{
		Handler:        l.handler(act),
		Protocols:      h2c(),
		MaxHeaderBytes: maxHeaderBytes,
		HTTP2: &http.HTTP2Config{
			MaxConcurrentStreams: 100,
			SendPingTimeout:      2 * hb,
			PingTimeout:          min(max(hb, time.Second), 15*time.Second),
			WriteByteTimeout:     max(2*hb, 5*time.Second),
		},
		ErrorLog: slog.NewLogLogger(l.log.Handler(), slog.LevelDebug),
	}
	go srv.Serve(newOneConnListener(pc))
	defer srv.Close()
	tick := time.NewTicker(max(hb/4, 10*time.Millisecond))
	defer tick.Stop()
	for {
		select {
		case <-pc.done:
			return errLinkDropped(nil)
		case <-ctx.Done():
			pc.Close()
			return ctx.Err()
		case <-tick.C:
			if idle := act.idle(); idle > 3*hb {
				pc.Close()
				return errHeartbeat(idle)
			}
		}
	}
}

func (l *Link) handler(act *activity) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		act.begin()
		defer act.end()
		if r.Method == http.MethodGet && r.URL.Path == pingPath {
			now := l.now()
			l.update(func(s *LinkStatus) { s.LastSeen = now })
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(pong{Version: l.o.Version, Time: now})
			return
		}
		start := time.Now()
		rec := RequestRecord{At: l.now(), Method: r.Method, Path: cleanText(r.URL.Path, 200)}
		rw := &recordingWriter{ResponseWriter: w}
		route, ok := l.allow.match(r.Method, r.URL)
		actor, actorOK := cleanActor(r.Header.Get(ActorHeader))
		switch {
		case !ok:
			writeAPIError(rw, http.StatusNotFound, errRouteNotAllowed(r.Method, r.URL.Path))
		case !actorOK && mutating(r.Method):
			writeAPIError(rw, http.StatusBadRequest, errActorRequired("this machine"))
		default:
			rec.Route = route.Method + " " + route.Pattern
			if !route.Stream {
				r.Body = http.MaxBytesReader(rw, r.Body, l.o.MaxRequestBytes)
			}
			l.o.Handler.ServeHTTP(rw, r)
		}
		rec.Actor = actor
		rec.Status = rw.status
		if rec.Status == 0 {
			rec.Status = http.StatusOK
		}
		rec.Bytes = rw.bytes
		rec.Duration = time.Since(start)
		if l.o.OnRequest != nil {
			l.o.OnRequest(rec)
		}
	})
}

// recordingWriter notes the status and size of a reply. It keeps
// flushing working, for log streams.
type recordingWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *recordingWriter) WriteHeader(code int) {
	if w.status == 0 && code >= 200 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *recordingWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *recordingWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *recordingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

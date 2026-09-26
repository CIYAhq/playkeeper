package sleep

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// Status is what the stand-in tells Minecraft clients about the server.
type Status struct {
	// Name is the server's name, for example "Survival". It heads the
	// server list entry and the message a joining player sees.
	Name string
	// Version is the server's version as tools show it, "Paper 26.2".
	Version string
	// Protocol is the server's protocol number, for tools that don't say
	// which one they speak. Game clients get their own number back.
	Protocol   int
	MaxPlayers int
	// Icon is the server list icon as a data URI, from ReadIcon; "" for none.
	Icon string
}

const (
	maxNameRunes    = 48
	maxVersionRunes = 64
)

func (s Status) clean() Status {
	s.Name = oneLine(s.Name, maxNameRunes)
	s.Version = oneLine(s.Version, maxVersionRunes)
	if s.Version == "" {
		s.Version = "Minecraft"
	}
	s.Protocol = min(max(s.Protocol, 0), math.MaxInt32)
	s.MaxPlayers = min(max(s.MaxPlayers, 0), 1_000_000)
	if !strings.HasPrefix(s.Icon, iconPrefix) || len(s.Icon) > maxIconURI {
		s.Icon = ""
	}
	return s
}

// oneLine makes text safe to show in the server list and in a disconnect
// message: one line, no control or formatting characters, at most max runes.
func oneLine(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '§':
			return -1
		case unicode.IsSpace(r) || !unicode.IsPrint(r):
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max]))
	}
	return s
}

// Config sets up the Manager of one server.
type Config struct {
	// Addr is where the container publishes the game port, ":25565".
	Addr string
	// Status is what the stand-in shows at first; SetStatus changes it.
	Status Status
	// OnWake is called on its own goroutine when a player tries to join and
	// may wake the server, at most once per wake. It should start the
	// agent's wake operation, which calls Wake.
	OnWake func(player string)
	// Admit says whether a player may wake the server, for example whether
	// they are on the whitelist or an operator. Nil admits every valid name.
	// Names are unverified. It is called for join attempts that would wake
	// the server, so it should be cheap, and safe for concurrent use. A name
	// it refuses gets the same reply as one it admits.
	Admit func(player string) bool
	// Now is the clock for wake limits. Nil means time.Now.
	Now func() time.Time
	// Logf reports notable events. It never gets addresses. Nil discards.
	Logf func(format string, args ...any)

	// Limits; zero means the default.
	MaxConns      int           // connections at once: 64
	MaxConnsPerIP int           // from one address, or one IPv6 /64: 8
	ConnTimeout   time.Duration // for a connection's whole exchange: 10 s
	BindTimeout   time.Duration // how long to retry a busy port: 30 s
}

// closeGrace is how long Wake and Close let replies being written finish.
const closeGrace = 2 * time.Second

// Manager answers on a sleeping server's game port and hands the port to
// the server when it wakes. Its methods are safe for concurrent use;
// Listen, Sleep, Wake and Close run one at a time.
type Manager struct {
	cfg        Config
	closeGrace time.Duration

	op sync.Mutex

	mu    sync.Mutex
	addr  string
	st    Status
	si    *standIn
	limit wakeLimit
}

func NewManager(cfg Config) (*Manager, error) {
	if _, port, err := net.SplitHostPort(cfg.Addr); err != nil {
		return nil, fmt.Errorf("sleep: game address %q: %w", cfg.Addr, err)
	} else if p, err := strconv.Atoi(port); err != nil || p < 0 || p > 65535 {
		return nil, fmt.Errorf("sleep: game address %q has no valid port", cfg.Addr)
	}
	if cfg.OnWake == nil {
		return nil, errors.New("sleep: Config.OnWake is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	cfg.MaxConns = orDefault(cfg.MaxConns, 64)
	cfg.MaxConnsPerIP = orDefault(cfg.MaxConnsPerIP, 8)
	cfg.ConnTimeout = orDefault(cfg.ConnTimeout, 10*time.Second)
	cfg.BindTimeout = orDefault(cfg.BindTimeout, 30*time.Second)
	return &Manager{cfg: cfg, closeGrace: closeGrace, addr: cfg.Addr, st: cfg.Status.clean()}, nil
}

func orDefault[T int | time.Duration](v, def T) T {
	if v <= 0 {
		return def
	}
	return v
}

// SetStatus changes what the stand-in shows, for example after the owner
// renames the server or changes its icon.
func (m *Manager) SetStatus(st Status) {
	st = st.clean()
	m.mu.Lock()
	m.st = st
	m.mu.Unlock()
}

func (m *Manager) status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st
}

// Listening reports whether the stand-in holds the game port.
func (m *Manager) Listening() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.si != nil
}

// Addr is the stand-in's address, with the port it was given if
// Config.Addr asked for port 0.
func (m *Manager) Addr() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.addr
}

// Listen starts answering on the game port, for a server that is already
// stopped, for example when the agent starts and finds it asleep. It
// retries while the port is busy, for up to BindTimeout, and does nothing
// if the stand-in is already listening.
func (m *Manager) Listen(ctx context.Context) error {
	m.op.Lock()
	defer m.op.Unlock()
	return m.listen(ctx)
}

// Sleep stops the server with stop, then answers on its port in its place
// as soon as the stopped container lets go of it. If stop fails, nothing
// listens and its error is returned. Sleep does nothing if the stand-in is
// already listening.
func (m *Manager) Sleep(ctx context.Context, stop func(context.Context) error) error {
	m.op.Lock()
	defer m.op.Unlock()
	if m.Listening() {
		return nil
	}
	if err := stop(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	m.limit.slept()
	m.mu.Unlock()
	return m.listen(context.WithoutCancel(ctx))
}

// Wake hands the port to the server: it stops the stand-in, waits until
// the port is free and every connection is closed, then calls start. If
// start fails and the server was asleep, the stand-in answers again, wakes
// pause for a while, and start's error is returned. Starting a server that
// was not asleep through Wake is fine: it only calls start.
func (m *Manager) Wake(ctx context.Context, start func(context.Context) error) error {
	m.op.Lock()
	defer m.op.Unlock()
	asleep := m.release()
	err := start(ctx)
	if !asleep {
		return err
	}
	m.mu.Lock()
	if err == nil {
		m.limit.succeeded()
	} else {
		m.limit.failed(m.cfg.Now())
	}
	m.mu.Unlock()
	if err == nil {
		return nil
	}
	if lerr := m.listen(context.WithoutCancel(ctx)); lerr != nil {
		return errors.Join(err, lerr)
	}
	return err
}

// Close stops the stand-in and frees the port, for example when sleeping
// is turned off, the server is deleted or the agent shuts down. The
// Manager can Listen again afterwards.
func (m *Manager) Close() {
	m.op.Lock()
	defer m.op.Unlock()
	m.release()
}

// release stops the stand-in, if it runs, and reports whether it did.
func (m *Manager) release() bool {
	m.mu.Lock()
	si := m.si
	m.si = nil
	m.mu.Unlock()
	if si == nil {
		return false
	}
	si.close(m.closeGrace)
	m.cfg.Logf("sleep: handed port %d back to the server", m.port())
	return true
}

func (m *Manager) port() int {
	_, p, _ := net.SplitHostPort(m.Addr())
	n, _ := strconv.Atoi(p)
	return n
}

func (m *Manager) listen(ctx context.Context) error {
	if m.Listening() {
		return nil
	}
	ln, err := m.bind(ctx)
	if err != nil {
		return err
	}
	si := newStandIn(m, ln)
	m.mu.Lock()
	m.si = si
	m.mu.Unlock()
	si.start()
	m.cfg.Logf("sleep: answering on port %d while the server sleeps", m.port())
	return nil
}

// bind listens on the game port, retrying while it is in use: a container
// that has just stopped can hold it for a moment.
func (m *Manager) bind(ctx context.Context) (net.Listener, error) {
	ctx, cancel := context.WithTimeout(ctx, m.cfg.BindTimeout)
	defer cancel()
	addr := m.Addr()
	var lc net.ListenConfig
	for delay := 20 * time.Millisecond; ; delay = min(2*delay, 500*time.Millisecond) {
		ln, err := lc.Listen(ctx, "tcp", addr)
		if err == nil {
			host, _, _ := net.SplitHostPort(addr)
			m.mu.Lock()
			m.addr = net.JoinHostPort(host, strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))
			m.mu.Unlock()
			return ln, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, m.portError("listen_failed", err)
		}
		select {
		case <-ctx.Done():
			return nil, m.portError("port_in_use", err)
		case <-time.After(delay):
		}
	}
}

func (m *Manager) portError(code string, err error) error {
	port := m.port()
	e := &Error{Code: code, Params: map[string]any{"port": port}, Err: err}
	switch code {
	case "port_in_use":
		e.Msg = fmt.Sprintf("Port %d is still in use, so Playkeeper can't answer players while the server sleeps.", port)
		e.Hint = fmt.Sprintf("Find the program using it with: sudo ss -ltnp 'sport = :%d'", port)
	default:
		e.Msg = fmt.Sprintf("Playkeeper couldn't answer players on port %d while the server sleeps.", port)
		e.Hint = "Check `sudo journalctl -u playkeeper-agent` for details."
	}
	return e
}

// joinAttempt decides what a player who tries to join is told, and wakes
// the server when they may wake it. The reply depends only on the wake
// limits, never on Admit, so every valid name reads the same thing.
func (m *Manager) joinAttempt(player string) string {
	st := m.status()
	guarded := m.cfg.Admit != nil
	if !minecraft.ValidPlayerName(player) {
		return answerText(answerNameInvalid, st.Name, 0, guarded)
	}
	m.mu.Lock()
	a, wait := m.limit.check(m.cfg.Now())
	m.mu.Unlock()
	if a == answerWake {
		if guarded && !m.cfg.Admit(player) {
			m.cfg.Logf("sleep: %s tried to join but may not wake the server", player)
			return answerText(answerWake, st.Name, 0, guarded)
		}
		m.mu.Lock()
		a, wait = m.limit.try(m.cfg.Now())
		m.mu.Unlock()
	}
	switch a {
	case answerWake:
		m.cfg.Logf("sleep: %s tried to join, waking the server", player)
		go m.cfg.OnWake(player)
	case answerTooMany, answerFailed:
		m.cfg.Logf("sleep: %s tried to join, but wakes are held back for %s", player, aboutDuration(wait))
	case answerWaking, answerNameInvalid:
	}
	return answerText(a, st.Name, wait, guarded)
}

// answerText is what a joining player reads. Minecraft shows it as sent,
// so it is in English. When Admit guards wakes, the waking reply is hedged,
// since it goes to names that don't wake the server too.
func answerText(a answer, server string, wait time.Duration, guarded bool) string {
	if server == "" {
		server = "This server"
	}
	switch a {
	case answerWake, answerWaking:
		if guarded {
			return server + " is asleep. If you're on the list, it's waking up: join again in about 30 seconds."
		}
		return server + " is waking up. Join again in about 30 seconds."
	case answerTooMany:
		return fmt.Sprintf("%s has woken up too often in the last hour. Try again in about %s.", server, aboutDuration(wait))
	case answerFailed:
		return fmt.Sprintf("%s couldn't wake up just now. Try again in about %s.", server, aboutDuration(wait))
	case answerNameInvalid:
		return "That player name isn't valid."
	}
	return server + " is asleep."
}

func aboutDuration(d time.Duration) string {
	n := int((d + time.Minute - 1) / time.Minute)
	switch {
	case n <= 1:
		return "1 minute"
	case n < 60:
		return fmt.Sprintf("%d minutes", n)
	case n == 60:
		return "1 hour"
	}
	return fmt.Sprintf("%d hours", (n+59)/60)
}

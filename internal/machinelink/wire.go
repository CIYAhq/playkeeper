package machinelink

import (
	"bytes"
	"crypto/ed25519"
	"crypto/subtle"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"sync"
	"time"
)

const (
	// ALPN is the TLS application protocol machines ask for. It is how the
	// dashboard tells them from browsers on its HTTPS port.
	ALPN            = "playkeeper-link/1"
	protocolVersion = 1

	modeJoin  = "join"
	modeLink  = "link"
	modeLeave = "leave"

	maxFrame       = 4 << 10
	maxHeaderBytes = 64 << 10
	pingPath       = "/_link/ping"
)

// After the TLS handshake the machine sends one hello frame and the
// dashboard answers with one welcome frame: a 4-byte big-endian length,
// then that many bytes of JSON. For a link, HTTP/2 follows on the same
// connection, with the dashboard as the client.
type hello struct {
	V        int    `json:"v"`
	Mode     string `json:"mode"`
	Code     string `json:"code,omitempty"`
	Name     string `json:"name,omitempty"`
	Version  string `json:"version,omitempty"`
	Instance string `json:"instance,omitempty"`
}

type welcome struct {
	OK          bool       `json:"ok"`
	V           int        `json:"v"`
	MachineID   string     `json:"machineId,omitempty"`
	Name        string     `json:"name,omitempty"`
	Version     string     `json:"version,omitempty"`
	HeartbeatMs int64      `json:"heartbeatMs,omitempty"`
	Error       *wireError `json:"error,omitempty"`
}

type wireError struct {
	Code         string            `json:"code"`
	Params       map[string]string `json:"params,omitempty"`
	Message      string            `json:"message"`
	Hint         string            `json:"hint,omitempty"`
	RetryAfterMs int64             `json:"retryAfterMs,omitempty"`
}

type pong struct {
	Version string    `json:"version"`
	Time    time.Time `json:"time"`
}

var (
	reMachineID = regexp.MustCompile(`^[a-z0-9]{6,32}$`)
	reVersion   = regexp.MustCompile(`^[A-Za-z0-9.+_-]{1,40}$`)
	reInstance  = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

func cleanVersion(v string) string {
	if reVersion.MatchString(v) {
		return v
	}
	return ""
}

func cleanInstance(s string) string {
	if reInstance.MatchString(s) {
		return s
	}
	return ""
}

var (
	errFrameTooLarge = errors.New("machinelink: frame too large")
	errBadFrame      = errors.New("machinelink: bad frame")
)

func writeFrame(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > maxFrame {
		return errFrameTooLarge
	}
	buf := make([]byte, 4, 4+len(b))
	binary.BigEndian.PutUint32(buf, uint32(len(b)))
	_, err = w.Write(append(buf, b...))
	return err
}

// readFrame reads exactly one frame and nothing after it: whatever
// follows belongs to HTTP/2.
func readFrame(r io.Reader, v any) error {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(n[:])
	if size > maxFrame {
		return errFrameTooLarge
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %v", errBadFrame, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: trailing data", errBadFrame)
	}
	return nil
}

// h2c is HTTP/2 without its own TLS: the link's TLS is underneath.
func h2c() *http.Protocols {
	p := new(http.Protocols)
	p.SetUnencryptedHTTP2(true)
	return p
}

// plainConn hides the *tls.Conn from net/http, which would otherwise
// handle TLS and ALPN itself instead of speaking HTTP/2 inside it. Done
// is closed when the connection is closed, by either side's code.
type plainConn struct {
	net.Conn
	once sync.Once
	done chan struct{}
}

func newPlainConn(c net.Conn) *plainConn { return &plainConn{Conn: c, done: make(chan struct{})} }

func (c *plainConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.done) })
	return err
}

// oneConnListener hands one connection to an http.Server.
type oneConnListener struct {
	conn   net.Conn
	once   sync.Once
	closed chan struct{}
	mu     sync.Mutex
	taken  bool
}

func newOneConnListener(c net.Conn) *oneConnListener {
	return &oneConnListener{conn: c, closed: make(chan struct{})}
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.taken {
		l.taken = true
		l.mu.Unlock()
		return l.conn, nil
	}
	l.mu.Unlock()
	<-l.closed
	return nil, net.ErrClosed
}

func (l *oneConnListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *oneConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

// peerKey returns the Ed25519 key of the other side's only certificate.
func peerKey(cs tls.ConnectionState) (ed25519.PublicKey, bool) {
	if len(cs.PeerCertificates) != 1 {
		return nil, false
	}
	k, ok := cs.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	return k, ok && len(k) == ed25519.PublicKeySize
}

var (
	errWrongALPN = errors.New("machinelink: the other side does not speak " + ALPN)
	errBadPeer   = errors.New("machinelink: the other side has no Ed25519 certificate")
	errWrongKey  = errors.New("machinelink: the dashboard's key does not match")
)

// serverTLS is the dashboard's side. Any machine may complete the
// handshake, which proves it holds its private key; whether that key
// belongs to a machine is decided after it, so the refusal can say why.
func serverTLS(id *Identity) *tls.Config {
	return &tls.Config{
		MinVersion:             tls.VersionTLS13,
		Certificates:           []tls.Certificate{id.cert},
		NextProtos:             []string{ALPN},
		ClientAuth:             tls.RequireAnyClientCert,
		SessionTicketsDisabled: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if cs.NegotiatedProtocol != ALPN {
				return errWrongALPN
			}
			if _, ok := peerKey(cs); !ok {
				return errBadPeer
			}
			return nil
		},
	}
}

// pin is what a machine knows about the dashboard's key: its fingerprint
// from the join command, or, once joined, the whole key.
type pin struct {
	key         ed25519.PublicKey
	fingerprint string
}

func (p pin) matches(k ed25519.PublicKey) bool {
	if p.key != nil {
		return subtle.ConstantTimeCompare(p.key, k) == 1
	}
	return subtle.ConstantTimeCompare([]byte(Fingerprint(k)), []byte(p.fingerprint)) == 1
}

// clientTLS is a machine's side. The dashboard's certificate is usually
// self-signed, so it is not checked against certificate authorities; the
// pinned key replaces that check, and nothing is sent before it passes.
func clientTLS(id *Identity, a Address, p pin) *tls.Config {
	c := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		Certificates:       []tls.Certificate{id.cert},
		NextProtos:         []string{ALPN},
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if cs.NegotiatedProtocol != ALPN {
				return errWrongALPN
			}
			k, ok := peerKey(cs)
			if !ok {
				return errBadPeer
			}
			if !p.matches(k) {
				return errWrongKey
			}
			return nil
		},
	}
	if validHostname(a.Host) {
		c.ServerName = a.Host
	}
	return c
}

// handshakeError explains why a machine's TLS handshake failed.
func handshakeError(a Address, joined bool, err error) error {
	var rh tls.RecordHeaderError
	var op *net.OpError
	switch {
	case errors.Is(err, errWrongKey):
		return errKeyMismatch(a, joined)
	case errors.Is(err, errWrongALPN), errors.Is(err, errBadPeer), errors.As(err, &rh):
		return errNotADashboard(a, err)
	case errors.As(err, &op) && op.Op == "remote error":
		// A TLS alert from the server: typically "no application
		// protocol" from an HTTPS server that is not a dashboard.
		return errNotADashboard(a, err)
	default:
		return errUnreachable(a, err)
	}
}

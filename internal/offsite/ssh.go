package offsite

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const handshakeTimeout = 30 * time.Second

// HostKey is the key an SSH server proves who it is with.
type HostKey struct {
	// Key is the key as known_hosts has it, without the host name, for
	// example "ssh-ed25519 AAAAC3Nz…". Once the user has confirmed
	// Fingerprint, save Key as SFTPConfig.HostKey.
	Key         string `json:"key"`
	Type        string `json:"type"`        // ssh-ed25519, ecdsa-sha2-nistp256, ssh-rsa…
	Fingerprint string `json:"fingerprint"` // "SHA256:…", as ssh-keygen -l shows it
}

func hostKeyOf(k ssh.PublicKey) *HostKey {
	if k == nil {
		return nil
	}
	return &HostKey{Key: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))), Type: k.Type(), Fingerprint: ssh.FingerprintSHA256(k)}
}

var errBadHostKey = errors.New("not a host key Playkeeper accepts")

func parseHostKey(s string) (ssh.PublicKey, error) {
	if len(s) > 16<<10 {
		return nil, errBadHostKey
	}
	k, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(s))
	if err != nil || len(options) > 0 || len(bytes.TrimSpace(rest)) > 0 || !acceptedHostKey(k) {
		return nil, errBadHostKey
	}
	return k, nil
}

// acceptedHostKey reports whether k is a kind of host key still safe:
// Ed25519, ECDSA, or RSA of at least 2048 bits.
func acceptedHostKey(k ssh.PublicKey) bool {
	switch k.Type() {
	case ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521:
		return true
	case ssh.KeyAlgoRSA:
		ck, ok := k.(ssh.CryptoPublicKey)
		if !ok {
			return false
		}
		rk, ok := ck.CryptoPublicKey().(*rsa.PublicKey)
		return ok && rk.N.BitLen() >= 2048
	}
	return false
}

// hostKeyAlgorithms asks for the confirmed key's kind, so a server with
// several host keys presents the confirmed one.
func hostKeyAlgorithms(pinned ssh.PublicKey) []string {
	switch {
	case pinned == nil:
		return []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	case pinned.Type() == ssh.KeyAlgoRSA:
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	}
	return []string{pinned.Type()}
}

// SSHKey is a key Playkeeper makes to sign in to the other machine.
type SSHKey struct {
	PrivateKey Secret `json:"-"`         // in OpenSSH's format; save it as SFTPConfig.PrivateKey
	PublicKey  string `json:"publicKey"` // "ssh-ed25519 AAAA… playkeeper-survival"
	// AuthorizedKey is the line for ~/.ssh/authorized_keys on the other
	// machine: the public key with "restrict", which turns off
	// forwarding and terminals, since copies need only SFTP.
	AuthorizedKey string `json:"authorizedKey"`
	Fingerprint   string `json:"fingerprint"`
}

// NewSSHKey makes an ed25519 key for the server named server, which names
// the key in its comment.
func NewSSHKey(server string) (SSHKey, error) {
	fail := func(err error) (SSHKey, error) {
		return SSHKey{}, &Error{Kind: KindUnexpected, Op: opSetup, Err: err, Msg: "Playkeeper couldn't make a key to sign in with.", Hint: "Try again."}
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fail(err)
	}
	comment := "playkeeper-" + slug(server)
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return fail(err)
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		return fail(err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))) + " " + comment
	return SSHKey{PrivateKey: NewSecret(string(pem.EncodeToMemory(block))), PublicKey: line,
		AuthorizedKey: "restrict " + line, Fingerprint: ssh.FingerprintSHA256(sp)}, nil
}

// stallConn fails a read or write that makes no progress for d, so a
// connection that stops answering doesn't hold an upload forever. The
// session's keepalives keep an idle connection busy.
type stallConn struct {
	net.Conn
	d       time.Duration
	stalled atomic.Bool
}

func (c *stallConn) Read(p []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(c.d))
	n, err := c.Conn.Read(p)
	c.note(err)
	return n, err
}

func (c *stallConn) Write(p []byte) (int, error) {
	_ = c.Conn.SetWriteDeadline(time.Now().Add(c.d))
	n, err := c.Conn.Write(p)
	c.note(err)
	return n, err
}

func (c *stallConn) note(err error) {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		c.stalled.Store(true)
	}
}

// session is one signed-in SFTP connection.
type session struct {
	ssh  *ssh.Client
	sftp *sftp.Client
	conn *stallConn
	stop func() bool
	done chan struct{}
}

func (s *session) close() {
	close(s.done)
	s.sftp.Close()
	s.ssh.Close()
	s.stop()
}

func (s *session) keepAlive(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			if _, _, err := s.ssh.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				return
			}
		}
	}
}

// handshake is what connecting found out, for its error. The host key
// callback runs on another goroutine.
type handshake struct {
	mu      sync.Mutex
	key     ssh.PublicKey
	verdict string // ok, unknown, changed or weak
	tried   []string
}

func (h *handshake) note(method string) {
	h.mu.Lock()
	h.tried = append(h.tried, method)
	h.mu.Unlock()
}

var (
	errHostKey   = errors.New("host key not accepted")
	errChallenge = errors.New("the server asks more than the password")
)

// connect dials the other machine, checks its host key before anything
// else is sent, signs in and starts SFTP. The connection closes when ctx
// ends. It returns the host key the machine presented, even on failure.
func (c *sftpClient) connect(ctx context.Context, op string, stall time.Duration) (*session, *HostKey, error) {
	addr := net.JoinHostPort(c.cfg.Host, strconv.Itoa(c.port))
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	raw, err := c.dial(dctx, "tcp", addr)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctxError(op, "", ctx.Err())
		}
		return nil, nil, c.netError(op, "", err)
	}
	conn := &stallConn{Conn: raw, d: stall}
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	var slow atomic.Bool
	timer := time.AfterFunc(handshakeTimeout, func() { slow.Store(true); raw.Close() })
	h := &handshake{}
	sc, chans, reqs, err := ssh.NewClientConn(conn, addr, &ssh.ClientConfig{
		User:              c.cfg.User,
		Auth:              c.auth(h),
		HostKeyCallback:   c.checkHostKey(h),
		HostKeyAlgorithms: hostKeyAlgorithms(c.pinned),
		ClientVersion:     "SSH-2.0-Playkeeper",
	})
	timer.Stop()
	h.mu.Lock()
	hk := hostKeyOf(h.key)
	h.mu.Unlock()
	if err != nil {
		stop()
		raw.Close()
		return nil, hk, c.handshakeError(ctx, op, err, h, slow.Load())
	}
	client := ssh.NewClient(sc, chans, reqs)
	sf, err := sftp.NewClient(client, sftp.UseConcurrentWrites(true), sftp.MaxConcurrentRequestsPerFile(64))
	if err != nil {
		client.Close()
		stop()
		return nil, hk, c.sftpStartError(ctx, op, err, conn)
	}
	s := &session{ssh: client, sftp: sf, conn: conn, stop: stop, done: make(chan struct{})}
	go s.keepAlive(stall / 4)
	return s, hk, nil
}

func (c *sftpClient) checkHostKey(h *handshake) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.key = key
		switch {
		case !acceptedHostKey(key):
			h.verdict = "weak"
		case c.pinned == nil:
			h.verdict = "unknown"
		case !bytes.Equal(key.Marshal(), c.pinned.Marshal()):
			h.verdict = "changed"
		default:
			h.verdict = "ok"
			return nil
		}
		return errHostKey
	}
}

// auth answers with the key, or with the password to a password request
// or to a single hidden keyboard-interactive question. Anything more,
// such as a one-time code, is refused rather than answered wrongly.
func (c *sftpClient) auth(h *handshake) []ssh.AuthMethod {
	if c.signer != nil {
		return []ssh.AuthMethod{ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
			h.note("publickey")
			return []ssh.Signer{c.signer}, nil
		})}
	}
	pw := c.cfg.Password.Reveal()
	answered := false
	return []ssh.AuthMethod{
		ssh.PasswordCallback(func() (string, error) {
			h.note("password")
			return pw, nil
		}),
		ssh.KeyboardInteractive(func(_, _ string, questions []string, echos []bool) ([]string, error) {
			if len(questions) == 0 {
				return nil, nil
			}
			if answered || len(questions) != 1 || echos[0] {
				h.note("challenge")
				return nil, errChallenge
			}
			answered = true
			h.note("keyboard-interactive")
			return []string{pw}, nil
		}),
	}
}

func (c *sftpClient) handshakeError(ctx context.Context, op string, err error, h *handshake, slow bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	hk := hostKeyOf(h.key)
	var neg *ssh.AlgorithmNegotiationError
	switch {
	case ctx.Err() != nil:
		return ctxError(op, "", ctx.Err())
	case h.verdict == "unknown":
		return &Error{Kind: KindHostKeyUnknown, Op: op, Field: "hostKey", HostKey: hk,
			Msg:  fmt.Sprintf("Check that the other machine's host key fingerprint is %s, then confirm it.", hk.Fingerprint),
			Hint: "On the other machine, ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub shows it. Nothing is sent before you confirm it."}
	case h.verdict == "changed":
		return c.changedHostKey(op, hk)
	case h.verdict == "weak":
		return &Error{Kind: KindUnexpected, Op: op, Field: "host", HostKey: hk,
			Msg:  fmt.Sprintf("The other machine's host key is an old or short kind of key (%s) that Playkeeper doesn't trust.", hk.Type),
			Hint: "Give the other machine an ed25519 host key (sudo ssh-keygen -A makes one), then try again."}
	case errors.As(err, &neg) && neg.What == "host key" && c.pinned != nil:
		return c.changedHostKey(op, nil)
	case errors.As(err, &neg):
		return &Error{Kind: KindUnexpected, Op: op, Field: "host", Err: err,
			Msg:  "Playkeeper and the other machine's SSH server have no secure settings in common.",
			Hint: "Update the SSH server on the other machine."}
	case errors.Is(err, errChallenge):
		return c.loginError(op, append(h.tried, "challenge"))
	case strings.Contains(err.Error(), "unable to authenticate"):
		return c.loginError(op, h.tried)
	case slow:
		return &Error{Kind: KindNetwork, Op: op, Retry: true, Err: err,
			Msg:  "The other machine didn't finish connecting in time.",
			Hint: "Check that the address and port are the other machine's SSH server, then try again."}
	case strings.Contains(err.Error(), "version string"):
		return &Error{Kind: KindUnexpected, Op: op, Field: "port", Err: err,
			Msg:  fmt.Sprintf("Something other than an SSH server answered on port %d of %s.", c.port, c.cfg.Host),
			Hint: "Check the port. SSH usually listens on port 22."}
	}
	return c.netError(op, "", err)
}

func (c *sftpClient) changedHostKey(op string, hk *HostKey) *Error {
	pinned := ssh.FingerprintSHA256(c.pinned)
	msg := fmt.Sprintf("The other machine no longer offers the host key you confirmed (%s), so Playkeeper didn't connect.", pinned)
	if hk != nil {
		msg = fmt.Sprintf("The other machine presented a different host key (%s) than the one you confirmed (%s), so Playkeeper didn't connect.", hk.Fingerprint, pinned)
	}
	return &Error{Kind: KindHostKeyChanged, Op: op, Field: "hostKey", HostKey: hk, Pinned: pinned, Msg: msg,
		Hint: "That happens when the machine is reinstalled, or when another machine pretends to be it. " +
			"If it was reinstalled, check the new fingerprint on it and confirm it again in the SFTP settings. If not, don't."}
}

func (c *sftpClient) loginError(op string, tried []string) *Error {
	e := &Error{Kind: KindLoginRefused, Op: op, User: c.cfg.User, Field: "password"}
	switch {
	case c.signer != nil && slices.Contains(tried, "publickey"):
		e.Field = "privateKey"
		e.Msg = fmt.Sprintf("The other machine didn't accept Playkeeper's key for the user %s.", c.cfg.User)
		e.Hint = fmt.Sprintf("Add the public key Playkeeper shows, on one line, to ~/.ssh/authorized_keys of %s on the other machine.", c.cfg.User)
	case c.signer != nil:
		e.Field = "privateKey"
		e.Msg = "The other machine doesn't accept keys for signing in."
		e.Hint = "Sign in with a password, or allow keys on the other machine (PubkeyAuthentication yes in /etc/ssh/sshd_config)."
	case slices.Contains(tried, "challenge"):
		e.Msg = "The other machine asks for more than a password, such as a one-time code, which Playkeeper can't answer."
		e.Hint = "Sign in with a key Playkeeper makes instead."
	case slices.Contains(tried, "password") || slices.Contains(tried, "keyboard-interactive"):
		e.Msg = fmt.Sprintf("The other machine refused the password for the user %s.", c.cfg.User)
		e.Hint = "Check the user name and the password."
	default:
		e.Msg = "The other machine doesn't accept passwords for signing in."
		e.Hint = "Sign in with a key Playkeeper makes instead."
	}
	return e
}

func (c *sftpClient) sftpStartError(ctx context.Context, op string, err error, conn *stallConn) error {
	var ne net.Error
	switch {
	case ctx.Err() != nil:
		return ctxError(op, "", ctx.Err())
	case conn.stalled.Load():
		return stalledError(op, "", conn.d)
	case strings.Contains(err.Error(), "subsystem request failed"):
		return &Error{Kind: KindUnexpected, Op: op, Field: "host", Err: err,
			Msg:  "The other machine accepted the sign-in but doesn't offer SFTP.",
			Hint: "Turn on SFTP in its SSH server (the Subsystem sftp line in /etc/ssh/sshd_config)."}
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &ne):
		return c.netError(op, "", err)
	}
	return &Error{Kind: KindUnexpected, Op: op, Field: "host", Err: err,
		Msg:  "The other machine's SFTP server didn't answer as expected.",
		Hint: "Check that SFTP works there, for example with the sftp command. A login script that prints text breaks SFTP."}
}

func stalledError(op, name string, d time.Duration) *Error {
	return &Error{Kind: KindNetwork, Op: op, Name: name, Retry: true, Err: errStalled,
		Msg:  fmt.Sprintf("The connection to the other machine stalled: no data moved for %s.", humanDuration(d)),
		Hint: "Check the internet connection of both machines, then try again."}
}

// netError turns a failed connection into an Error.
func (c *sftpClient) netError(op, name string, err error) *Error {
	e := &Error{Kind: KindNetwork, Op: op, Name: name, Retry: true, Err: err}
	var (
		dnsErr  *net.DNSError
		refused *refusedAddrError
		netErr  net.Error
	)
	switch {
	case errors.As(err, &refused):
		e.Kind, e.Retry, e.Field = KindInvalidConfig, false, "host"
		e.Msg = fmt.Sprintf("The host name points to %s, a link-local, multicast or cloud metadata address Playkeeper never connects to.", refused.ip)
		e.Hint = "Check the other machine's address."
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		e.Retry, e.Field = false, "host"
		e.Msg = fmt.Sprintf("The host name %s could not be found.", c.cfg.Host)
		e.Hint = "Check the other machine's address."
	case errors.As(err, &dnsErr):
		e.Field = "host"
		e.Msg = "This machine couldn't look up the other machine's address (DNS)."
		e.Hint = "Check this machine's DNS settings and internet connection."
	case errors.Is(err, syscall.ECONNREFUSED):
		e.Retry, e.Field = false, "port"
		e.Msg = fmt.Sprintf("Nothing accepted the connection on port %d of %s (connection refused).", c.port, c.cfg.Host)
		e.Hint = "Check the port, and that the SSH server is running on the other machine."
	case errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH):
		e.Msg = "This machine can't reach the other machine's network."
		e.Hint = "Check the address, and that the other machine is on and online."
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout():
		e.Msg = "The other machine didn't answer in time."
		e.Hint = "Check the address and port, and that a firewall lets SSH through."
	default:
		e.Msg = "The connection to the other machine broke."
		e.Hint = "Check the internet connection of both machines, then try again."
	}
	return e
}

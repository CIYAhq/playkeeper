package offsite

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const (
	testHost     = "backup.example"
	testPassword = "correct horse battery staple"
	testFolder   = "backups/survival"
)

var (
	errRefused  = errors.New("refused")
	errBrokeOff = errors.New("the client broke off")
)

type dialer = func(ctx context.Context, network, addr string) (net.Conn, error)

// testHostKeys are the host keys test servers present, made once.
var testHostKeys = sync.OnceValue(func() map[string]ssh.Signer {
	ed := func() (any, error) {
		_, k, err := ed25519.GenerateKey(rand.Reader)
		return k, err
	}
	keys := map[string]ssh.Signer{}
	for name, gen := range map[string]func() (any, error){
		"ed25519":             ed,
		"ed25519 reinstalled": ed,
		"ecdsa":               func() (any, error) { return ecdsa.GenerateKey(elliptic.P256(), rand.Reader) },
		"rsa":                 func() (any, error) { return rsa.GenerateKey(rand.Reader, 2048) },
		"rsa 1024":            func() (any, error) { return rsa.GenerateKey(rand.Reader, 1024) },
	} {
		k, err := gen()
		if err != nil {
			panic(err)
		}
		s, err := ssh.NewSignerFromKey(k)
		if err != nil {
			panic(err)
		}
		keys[name] = s
	}
	return keys
})

func hostKey(name string) ssh.Signer { return testHostKeys()[name] }

func signers(names ...string) []ssh.Signer {
	var out []ssh.Signer
	for _, n := range names {
		out = append(out, hostKey(n))
	}
	return out
}

func fingerprint(name string) string { return ssh.FingerprintSHA256(hostKey(name).PublicKey()) }

// authorizedKey is k as SFTPConfig.HostKey holds it.
func authorizedKey(k ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

// sftpSettings signs in to testHost as playkeeper with the password,
// trusting the host key k unless it is nil.
func sftpSettings(k ssh.Signer) SFTPConfig {
	cfg := SFTPConfig{Host: testHost, Port: 2222, User: "playkeeper", Folder: testFolder, Password: NewSecret(testPassword)}
	if k != nil {
		cfg.HostKey = authorizedKey(k.PublicKey())
	}
	return cfg
}

// quickSFTP gives c 64 KiB segments and no waiting between attempts, and
// returns the waits it would have made.
func quickSFTP(c *sftpClient) *[]time.Duration {
	c.segment = 64 << 10
	var mu sync.Mutex
	waits := &[]time.Duration{}
	c.sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		*waits = append(*waits, d)
		mu.Unlock()
		return ctx.Err()
	}
	return waits
}

// faults break a connection to the test server once it has moved so many
// bytes; zero never does.
type faults struct {
	dropAt    int64 // closes it after reading this many
	dropOutAt int64 // closes it after writing this many
	hangAt    int64 // stops answering after reading this many
}

// dropFirst closes the first connection once it has read n bytes.
func dropFirst(n int64) func(conn int) faults {
	return func(conn int) faults {
		if conn == 1 {
			return faults{dropAt: n}
		}
		return faults{}
	}
}

// faultConn is the server's side of a connection. It counts what it
// reads and writes.
type faultConn struct {
	net.Conn
	faults
	read, written atomic.Int64
}

func (c *faultConn) Read(p []byte) (int, error) {
	n := c.read.Load()
	switch {
	case c.dropAt > 0 && n >= c.dropAt:
		c.Conn.Close()
		return 0, net.ErrClosed
	case c.hangAt > 0 && n >= c.hangAt:
		// Reading on keeps the client's writes from blocking, so only the
		// missing answers can tell it that the server hangs.
		buf := make([]byte, 32<<10)
		for {
			if _, err := c.Conn.Read(buf); err != nil {
				return 0, err
			}
		}
	}
	for _, at := range []int64{c.dropAt, c.hangAt} {
		if at > n {
			p = p[:min(int64(len(p)), at-n)]
		}
	}
	m, err := c.Conn.Read(p)
	c.read.Add(int64(m))
	return m, err
}

func (c *faultConn) Write(p []byte) (int, error) {
	n := c.written.Load()
	if c.dropOutAt > 0 && n+int64(len(p)) >= c.dropOutAt {
		m, _ := c.Conn.Write(p[:max(0, c.dropOutAt-n)])
		c.written.Add(int64(m))
		c.Conn.Close()
		return m, net.ErrClosed
	}
	m, err := c.Conn.Write(p)
	c.written.Add(int64(m))
	return m, err
}

// sshServer is the other machine: an SSH server on the loopback address
// that offers SFTP on a folder of its own, or on files in memory with
// handlers. It records sign-ins and passwords before it answers them.
type sshServer struct {
	t        *testing.T
	ln       net.Listener
	root     string
	hostKeys []ssh.Signer
	// methods are the ways it lets users sign in: password (if nil),
	// publickey and keyboard-interactive.
	methods  []string
	password string
	keys     []ssh.PublicKey
	// kbd are the rounds of questions keyboard-interactive asks; one
	// asking for the password if nil.
	kbd      [][]string
	echo     bool // keyboard-interactive shows the answers as they are typed
	more     bool // asks for a one-time code once the password or key is right
	noSFTP   bool
	readOnly bool
	handlers *sftp.Handlers
	fault    func(conn int) faults

	wg        sync.WaitGroup
	mu        sync.Mutex
	closed    bool
	conns     []*faultConn
	logins    []string // "<method> ok", "<method> partial" or "<method> refused"
	passwords []string // every password and answer to a question it was sent
	dialed    []string
}

func newSSHServer(t *testing.T, mod func(*sshServer)) *sshServer {
	t.Helper()
	s := &sshServer{t: t, root: t.TempDir(), hostKeys: signers("ed25519"), password: testPassword}
	if err := os.MkdirAll(s.folder(), 0o755); err != nil {
		t.Fatal(err)
	}
	if mod != nil {
		mod(s)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.ln = ln
	t.Cleanup(s.close)
	s.wg.Add(1)
	go s.accept()
	return s
}

func (s *sshServer) folder() string { return filepath.Join(s.root, filepath.FromSlash(testFolder)) }

func (s *sshServer) settings() SFTPConfig { return sftpSettings(s.hostKeys[0]) }

// open returns a destination on the server with quick retries, a spool
// folder of its own and plenty of room on this machine.
func (s *sshServer) open(keys Keys, cfg SFTPConfig) (*Destination, *[]time.Duration) {
	s.t.Helper()
	d, err := Open(Config{Type: TypeSFTP, SFTP: cfg}, keys, Options{SpoolDir: s.t.TempDir(), Dial: s.dial,
		DiskFree: func(string) (int64, error) { return 1 << 40, nil }})
	if err != nil {
		s.t.Fatalf("Open: %v", err)
	}
	return d, quickSFTP(d.b.(*sftpClient))
}

// dial connects to the server whatever address it is given, and records
// the address.
func (s *sshServer) dial(ctx context.Context, _, addr string) (net.Conn, error) {
	s.record(&s.dialed, addr)
	var d net.Dialer
	return d.DialContext(ctx, "tcp", s.ln.Addr().String())
}

func (s *sshServer) accept() {
	defer s.wg.Done()
	for n := 1; ; n++ {
		raw, err := s.ln.Accept()
		if err != nil {
			return
		}
		c := &faultConn{Conn: raw}
		if s.fault != nil {
			c.faults = s.fault(n)
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			raw.Close()
			return
		}
		s.conns = append(s.conns, c)
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serve(c)
	}
}

func (s *sshServer) serve(c net.Conn) {
	defer s.wg.Done()
	defer c.Close()
	sc, chans, reqs, err := ssh.NewServerConn(c, s.config())
	if err != nil {
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go s.session(ch, creqs)
	}
}

func (s *sshServer) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer s.wg.Done()
	defer ch.Close()
	for req := range reqs {
		var sub struct{ Name string }
		if req.Type == "subsystem" && ssh.Unmarshal(req.Payload, &sub) == nil && sub.Name == "sftp" && !s.noSFTP {
			req.Reply(true, nil)
			go ssh.DiscardRequests(reqs)
			s.serveSFTP(ch)
			return
		}
		req.Reply(false, nil)
	}
}

func (s *sshServer) serveSFTP(ch ssh.Channel) {
	if s.handlers != nil {
		rs := sftp.NewRequestServer(ch, *s.handlers)
		rs.Serve()
		rs.Close()
		return
	}
	opts := []sftp.ServerOption{sftp.WithServerWorkingDirectory(s.root)}
	if s.readOnly {
		opts = append(opts, sftp.ReadOnly())
	}
	srv, err := sftp.NewServer(ch, opts...)
	if err != nil {
		return
	}
	srv.Serve()
}

func (s *sshServer) config() *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		ServerVersion: "SSH-2.0-OpenSSH_9.6p1 Ubuntu-3ubuntu13.5",
		AuthLogCallback: func(_ ssh.ConnMetadata, method string, err error) {
			var partial *ssh.PartialSuccessError
			switch {
			case method == "none" || errors.Is(err, errBrokeOff):
			case err == nil:
				s.record(&s.logins, method+" ok")
			case errors.As(err, &partial):
				s.record(&s.logins, method+" partial")
			default:
				s.record(&s.logins, method+" refused")
			}
		},
	}
	methods := s.methods
	if methods == nil {
		methods = []string{"password"}
	}
	for _, m := range methods {
		switch m {
		case "password":
			cfg.PasswordCallback = s.checkPassword
		case "publickey":
			cfg.PublicKeyCallback = s.checkKey
		case "keyboard-interactive":
			cfg.KeyboardInteractiveCallback = s.askPassword
		}
	}
	for _, k := range s.hostKeys {
		cfg.AddHostKey(k)
	}
	return cfg
}

func (s *sshServer) checkPassword(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
	s.record(&s.passwords, string(pw))
	if string(pw) != s.password {
		return nil, errRefused
	}
	return s.andMore()
}

func (s *sshServer) checkKey(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
	for _, known := range s.keys {
		if bytes.Equal(known.Marshal(), k.Marshal()) {
			return s.andMore()
		}
	}
	return nil, errRefused
}

// askPassword asks the rounds of questions in kbd. The first round with
// questions must have only one, the password; any round after that is
// refused, since no answer to it is right.
func (s *sshServer) askPassword(_ ssh.ConnMetadata, ask ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
	rounds := s.kbd
	if rounds == nil {
		rounds = [][]string{{"Password: "}}
	}
	asked := false
	for _, questions := range rounds {
		answers, err := ask("", "", questions, slices.Repeat([]bool{s.echo}, len(questions)))
		if err != nil {
			return nil, errBrokeOff
		}
		for _, a := range answers {
			s.record(&s.passwords, a)
		}
		switch {
		case len(questions) == 0:
			continue
		case asked || len(answers) != 1 || answers[0] != s.password:
			return nil, errRefused
		}
		asked = true
	}
	return s.andMore()
}

// andMore lets the user in, or with more asks for a one-time code, hidden
// as Google Authenticator's PAM module asks it; no answer is right.
func (s *sshServer) andMore() (*ssh.Permissions, error) {
	if !s.more {
		return nil, nil
	}
	return nil, &ssh.PartialSuccessError{Next: ssh.ServerAuthCallbacks{
		KeyboardInteractiveCallback: func(_ ssh.ConnMetadata, ask ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			answers, err := ask("", "", []string{"Verification code: "}, []bool{false})
			if err != nil {
				return nil, errBrokeOff
			}
			for _, a := range answers {
				s.record(&s.passwords, a)
			}
			return nil, errRefused
		},
	}}
}

func (s *sshServer) record(list *[]string, v string) {
	s.mu.Lock()
	*list = append(*list, v)
	s.mu.Unlock()
}

func (s *sshServer) copyOf(list *[]string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(*list)
}

func (s *sshServer) connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// received is how many bytes the server read on connection n, counting
// from 1.
func (s *sshServer) received(n int) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns[n-1].read.Load()
}

func (s *sshServer) close() {
	s.mu.Lock()
	s.closed = true
	conns := slices.Clone(s.conns)
	s.mu.Unlock()
	s.ln.Close()
	for _, c := range conns {
		c.Close()
	}
	s.wg.Wait()
}

// memDisk is the other machine's disk, in memory, for what a folder on
// this machine can't do: fill up, refuse writes to a user who owns it, or
// keep other bytes than it was sent.
type memDisk struct {
	mu       sync.Mutex
	free     int64 // what the server says is free
	room     int64 // what writes may add before the disk is full; no limit if negative
	corrupt  bool  // keeps the first byte of every file flipped
	readOnly bool
	cut      int64 // ends the next file opened for reading after this many bytes
	list     sftp.FileLister
}

// inMemory makes the server keep its files on disk instead of in a folder.
func inMemory(disk *memDisk) func(*sshServer) {
	return func(s *sshServer) {
		mem := sftp.InMemHandler()
		for _, dir := range []string{"/backups", "/" + testFolder} {
			if err := mem.FileCmd.Filecmd(sftp.NewRequest("Mkdir", dir)); err != nil {
				s.t.Fatal(err)
			}
		}
		disk.list = mem.FileList
		s.handlers = &sftp.Handlers{
			FileGet:  diskReader{disk, mem.FileGet},
			FilePut:  diskWriter{disk, mem.FilePut},
			FileCmd:  diskCmd{disk, mem.FileCmd},
			FileList: mem.FileList,
		}
	}
}

// names lists the folder the copies go in.
func (d *memDisk) names(t *testing.T) []string {
	t.Helper()
	l, err := d.list.Filelist(sftp.NewRequest("List", "/"+testFolder))
	if err != nil {
		t.Fatal(err)
	}
	fis := make([]os.FileInfo, 100)
	n, err := l.ListAt(fis, 0)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	var names []string
	for _, fi := range fis[:n] {
		names = append(names, fi.Name())
	}
	return names
}

type diskReader struct {
	disk *memDisk
	next sftp.FileReader
}

func (r diskReader) Fileread(req *sftp.Request) (io.ReaderAt, error) {
	f, err := r.next.Fileread(req)
	if err != nil {
		return nil, err
	}
	r.disk.mu.Lock()
	defer r.disk.mu.Unlock()
	if cut := r.disk.cut; cut > 0 {
		r.disk.cut = 0
		return io.NewSectionReader(f, 0, cut), nil
	}
	return f, nil
}

type diskWriter struct {
	disk *memDisk
	next sftp.FileWriter
}

func (w diskWriter) Filewrite(req *sftp.Request) (io.WriterAt, error) {
	if w.disk.readOnly {
		return nil, syscall.EACCES
	}
	f, err := w.next.Filewrite(req)
	if err != nil {
		return nil, err
	}
	return diskFile{w.disk, f}, nil
}

type diskFile struct {
	disk *memDisk
	w    io.WriterAt
}

func (f diskFile) WriteAt(p []byte, off int64) (int, error) {
	d := f.disk
	d.mu.Lock()
	if d.room >= 0 && int64(len(p)) > d.room {
		d.free = d.room
		d.mu.Unlock()
		return 0, syscall.ENOSPC
	}
	if d.room >= 0 {
		d.room -= int64(len(p))
	}
	d.free -= int64(len(p))
	if d.corrupt && off == 0 && len(p) > 0 {
		p = bytes.Clone(p)
		p[0] ^= 0xff
	}
	d.mu.Unlock()
	return f.w.WriteAt(p, off)
}

type diskCmd struct {
	disk *memDisk
	next sftp.FileCmder
}

func (c diskCmd) Filecmd(req *sftp.Request) error { return c.next.Filecmd(req) }

// StatVFS answers statvfs@openssh.com, which tells the client how much
// room is left.
func (c diskCmd) StatVFS(*sftp.Request) (*sftp.StatVFS, error) {
	c.disk.mu.Lock()
	defer c.disk.mu.Unlock()
	blocks := uint64(max(c.disk.free, 0)) / 4096
	return &sftp.StatVFS{Bsize: 4096, Frsize: 4096, Blocks: 1 << 30, Bfree: blocks, Bavail: blocks, Namemax: 255}, nil
}

// listening accepts connections on the loopback address and has serve
// answer each, as something other than an SSH server would.
func listening(t *testing.T, serve func(net.Conn)) dialer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(10 * time.Second))
				serve(c)
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		wg.Wait()
	})
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", ln.Addr().String())
	}
}

// replying reads the line the client starts with and sends reply.
func replying(reply string) func(net.Conn) {
	return func(c net.Conn) {
		if _, err := bufio.NewReader(c).ReadString('\n'); err == nil {
			io.WriteString(c, reply)
		}
	}
}

// closedPort dials a port on the loopback address that nothing listens on.
func closedPort(t *testing.T) dialer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}
}

func failing(err error) dialer {
	return func(context.Context, string, string) (net.Conn, error) { return nil, err }
}

package certs

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// maxStoreFiles bounds the certificates a Store loads.
const maxStoreFiles = 64

// StoreOptions configures a Store.
type StoreOptions struct {
	// Dir holds the certificates Issue saved, as "<name>.pem". A missing
	// Dir holds none.
	Dir string
	// FallbackCert and FallbackKey are the PEM files of the certificate
	// served when none in Dir fits, such as the panel's self-signed one for
	// access by IP address. Empty means no fallback.
	FallbackCert string
	FallbackKey  string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// RecheckEvery is how often a handshake looks for changed files; 0 means
	// every 5 seconds. A negative value means every time, waiting for a look
	// in progress, so each use sees every change made before it.
	RecheckEvery time.Duration
}

// Store serves certificates to a TLS server by SNI: set tls.Config's
// GetCertificate to Store.GetCertificate and leave Certificates empty. For
// a name it serves a certificate from Dir that covers the name and is valid
// now, the one valid longest if several do; otherwise, and for clients that
// send no name (access by IP address), the fallback. It notices new,
// renewed and removed files by itself.
type Store struct {
	opts      StoreOptions
	state     atomic.Pointer[storeState]
	lastCheck atomic.Int64
	reloadMu  sync.Mutex
}

type storeState struct {
	stamp       string
	entries     []storeEntry
	fallback    *tls.Certificate
	fallbackErr error
	err         error
}

type storeEntry struct {
	cert *tls.Certificate
	info Certificate
}

// NewStore loads the certificates. It fails when the fallback cannot be
// loaded; certificates in Dir that cannot be used are reported by Loaded.
func NewStore(opts StoreOptions) (*Store, error) {
	if opts.Dir == "" {
		return nil, errors.New("certs: the store has no directory")
	}
	if (opts.FallbackCert == "") != (opts.FallbackKey == "") {
		return nil, errors.New("certs: the fallback needs both a certificate and a key file")
	}
	s := &Store{opts: opts}
	s.state.Store(&storeState{})
	s.lastCheck.Store(s.now().UnixNano())
	if st := s.reload(true); st.fallbackErr != nil {
		return nil, fmt.Errorf("certs: loading the fallback certificate: %w", st.fallbackErr)
	}
	return s, nil
}

// GetCertificate picks the certificate for a TLS handshake.
func (s *Store) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.maybeReload()
	st := s.state.Load()
	switch best := st.serving(storeName(hello.ServerName), s.now()); {
	case best != nil:
		return best.cert, nil
	case st.fallback != nil:
		return st.fallback, nil
	}
	return nil, fmt.Errorf("certs: no certificate for %q", displayName(hello.ServerName))
}

// Trusted reports whether a client that trusts roots (nil: the system's)
// accepts the certificate the store serves for name, now and still after
// margin. Like the store, it matches the name exactly, never by a wildcard.
func (s *Store) Trusted(name string, roots *x509.CertPool, margin time.Duration) bool {
	s.maybeReload()
	name, now := storeName(name), s.now()
	e := s.state.Load().serving(name, now)
	if e == nil {
		return false
	}
	inter := x509.NewCertPool()
	for _, der := range e.cert.Certificate[1:] {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return false
		}
		inter.AddCert(c)
	}
	for _, at := range []time.Time{now, now.Add(margin)} {
		opts := x509.VerifyOptions{DNSName: name, Roots: roots, Intermediates: inter, CurrentTime: at}
		if _, err := e.cert.Leaf.Verify(opts); err != nil {
			return false
		}
	}
	return true
}

func storeName(serverName string) string {
	return strings.TrimSuffix(strings.ToLower(serverName), ".")
}

// serving is the certificate served for name at now: one that covers the
// name and is valid, the one valid longest if several do.
func (st *storeState) serving(name string, now time.Time) *storeEntry {
	if name == "" {
		return nil
	}
	var best *storeEntry
	for i := range st.entries {
		e := &st.entries[i]
		if !slices.Contains(e.info.Names, name) || now.Before(e.info.NotBefore) || !now.Before(e.info.NotAfter) {
			continue
		}
		if best == nil || e.info.NotAfter.After(best.info.NotAfter) {
			best = e
		}
	}
	return best
}

// Loaded describes the certificates loaded from Dir, including any that
// are not valid now and so are not served. Its error says why other files
// in Dir, or a changed fallback, cannot be used.
func (s *Store) Loaded() ([]Certificate, error) {
	s.maybeReload()
	st := s.state.Load()
	out := make([]Certificate, len(st.entries))
	for i, e := range st.entries {
		out[i] = e.info
	}
	return out, st.err
}

// Reload reads Dir and the fallback again at once, for callers that know a
// file changed. Its error is Loaded's.
func (s *Store) Reload() error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	s.lastCheck.Store(s.now().UnixNano())
	return s.reload(true).err
}

func (s *Store) now() time.Time {
	if s.opts.Now != nil {
		return s.opts.Now()
	}
	return time.Now()
}

// maybeReload reloads changed files at most every RecheckEvery. A handshake
// never waits for another one's reload, unless RecheckEvery is negative.
func (s *Store) maybeReload() {
	every := s.opts.RecheckEvery
	if every < 0 {
		s.reloadMu.Lock()
		defer s.reloadMu.Unlock()
		s.reload(false)
		return
	}
	if every == 0 {
		every = 5 * time.Second
	}
	now := s.now().UnixNano()
	if d := now - s.lastCheck.Load(); d >= 0 && d < int64(every) {
		return
	}
	if !s.reloadMu.TryLock() {
		return
	}
	defer s.reloadMu.Unlock()
	s.lastCheck.Store(now)
	s.reload(false)
}

// reload loads the files again when they changed, or always with force.
// The caller holds reloadMu, except in NewStore.
func (s *Store) reload(force bool) *storeState {
	prev := s.state.Load()
	files, stamp, dirErr := s.scan()
	if !force && stamp == prev.stamp {
		return prev
	}
	next := &storeState{stamp: stamp, fallback: prev.fallback}
	var errs []error
	if dirErr != nil {
		errs = append(errs, dirErr)
	}
	if s.opts.FallbackCert != "" {
		fb, err := loadFallback(s.opts.FallbackCert, s.opts.FallbackKey)
		if err != nil {
			next.fallbackErr = err
			errs = append(errs, fmt.Errorf("fallback certificate: %w", err))
		} else {
			next.fallback = fb
		}
	}
	for _, f := range files {
		e, err := loadEntry(s.opts.Dir, f)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		next.entries = append(next.entries, e)
	}
	next.err = errors.Join(errs...)
	s.state.Store(next)
	return next
}

// scan lists the certificate files in Dir: regular files named after the
// name they are for, "<name>.pem". The stamp changes whenever one of them
// or the fallback is replaced.
func (s *Store) scan() (files []string, stamp string, err error) {
	var b strings.Builder
	for _, p := range []string{s.opts.FallbackCert, s.opts.FallbackKey} {
		if p == "" {
			continue
		}
		if info, err := os.Stat(p); err == nil {
			b.WriteString(fileStamp(p, info))
		} else {
			fmt.Fprintf(&b, "%s:missing;", p)
		}
	}
	entries, err := os.ReadDir(s.opts.Dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, b.String() + "dir:" + err.Error(), err
	}
	for _, e := range entries {
		stem, ok := strings.CutSuffix(e.Name(), ".pem")
		if !ok || !e.Type().IsRegular() {
			continue
		}
		if n, err := NormalizeName(stem); err != nil || n != stem {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, e.Name())
		b.WriteString(fileStamp(e.Name(), info))
		if len(files) == maxStoreFiles {
			break
		}
	}
	return files, b.String(), nil
}

// fileStamp identifies one version of a file. The inode number changes on
// every save, because saves rename a new file into place.
func fileStamp(name string, info fs.FileInfo) string {
	var ino uint64
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		ino = st.Ino
	}
	return fmt.Sprintf("%s:%d:%d:%d;", name, info.Size(), info.ModTime().UnixNano(), ino)
}

// loadEntry loads one certificate file from dir, which must cover the name
// the file is named after.
func loadEntry(dir, file string) (storeEntry, error) {
	path := filepath.Join(dir, file)
	f, err := os.OpenFile(path, os.O_RDONLY|noFollow, 0)
	if err != nil {
		return storeEntry{}, err
	}
	defer f.Close()
	b, err := readRegular(f)
	if err != nil {
		return storeEntry{}, fmt.Errorf("%s: %w", path, err)
	}
	cert, err := parseBundle(b)
	if err != nil {
		return storeEntry{}, fmt.Errorf("%s: %w", path, err)
	}
	info := certificateInfo(path, cert.Leaf)
	if name := strings.TrimSuffix(file, ".pem"); !slices.Contains(info.Names, name) {
		return storeEntry{}, fmt.Errorf("%s: the certificate does not cover %s", path, name)
	}
	return storeEntry{cert: cert, info: info}, nil
}

// loadFallback loads a certificate and its key from separate PEM files.
func loadFallback(certFile, keyFile string) (*tls.Certificate, error) {
	read := func(p string) ([]byte, error) {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		b, err := readRegular(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		return b, nil
	}
	certPEM, err := read(certFile)
	if err != nil {
		return nil, err
	}
	keyPEM, err := read(keyFile)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &cert, nil
}

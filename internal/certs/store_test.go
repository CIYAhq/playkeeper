package certs

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct{ ns atomic.Int64 }

func newClock(t time.Time) *fakeClock {
	c := &fakeClock{}
	c.ns.Store(t.UnixNano())
	return c
}

func (c *fakeClock) now() time.Time          { return time.Unix(0, c.ns.Load()) }
func (c *fakeClock) add(d time.Duration)     { c.ns.Add(int64(d)) }
func (c *fakeClock) set(t time.Time)         { c.ns.Store(t.UnixNano()) }
func hello(name string) *tls.ClientHelloInfo { return &tls.ClientHelloInfo{ServerName: name} }

// saveBundle saves a certificate file the way Issue does.
func saveBundle(t *testing.T, dir, file string, data []byte) {
	t.Helper()
	r, err := openDir(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := writeFile(r, file, data, 0o600, nil); err != nil {
		t.Fatal(err)
	}
}

func leafOf(t *testing.T, c *tls.Certificate) *x509.Certificate {
	t.Helper()
	if c == nil {
		t.Fatal("no certificate")
	}
	if c.Leaf != nil {
		return c.Leaf
	}
	l, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return l
}

type storeFixture struct {
	t        *testing.T
	ca       *testCA
	dir      string
	certFile string
	keyFile  string
	clock    *fakeClock
	store    *Store
}

func newStoreFixture(t *testing.T) *storeFixture {
	base := t.TempDir()
	f := &storeFixture{t: t, ca: newTestCA(t), dir: filepath.Join(base, "certs"), clock: newClock(time.Now())}
	f.certFile, f.keyFile = writeSelfSigned(t, base)
	return f
}

func (f *storeFixture) open() *Store {
	s, err := NewStore(StoreOptions{Dir: f.dir, FallbackCert: f.certFile, FallbackKey: f.keyFile, Now: f.clock.now, RecheckEvery: time.Second})
	if err != nil {
		f.t.Fatal(err)
	}
	f.store = s
	return s
}

func (f *storeFixture) save(file string, names []string, notBefore, notAfter time.Time) {
	saveBundle(f.t, f.dir, file, f.ca.bundle(f.t, names, notBefore, notAfter))
}

// served returns the names of the certificate served for name, or
// "fallback" for the self-signed one.
func (f *storeFixture) served(name string) string {
	f.t.Helper()
	c, err := f.store.GetCertificate(hello(name))
	if err != nil {
		f.t.Fatalf("GetCertificate(%q): %v", name, err)
	}
	l := leafOf(f.t, c)
	if len(l.IPAddresses) > 0 {
		return "fallback"
	}
	return strings.Join(l.DNSNames, ",") + "@" + l.NotAfter.UTC().Format("2006-01-02")
}

func TestStoreServesBySNI(t *testing.T) {
	f := newStoreFixture(t)
	now := f.clock.now()
	f.save("mc.example.com.pem", []string{"mc.example.com"}, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	f.save("www.example.com.pem", []string{"www.example.com", "shop.example.com"}, now.Add(-time.Hour), now.Add(30*24*time.Hour))
	f.open()

	day := func(d time.Duration) string { return now.Add(d).UTC().Format("2006-01-02") }
	for name, want := range map[string]string{
		"mc.example.com":    "mc.example.com@" + day(90*24*time.Hour),
		"MC.Example.COM.":   "mc.example.com@" + day(90*24*time.Hour),
		"shop.example.com":  "www.example.com,shop.example.com@" + day(30*24*time.Hour),
		"":                  "fallback",
		"other.example.com": "fallback",
		"example.com":       "fallback",
	} {
		if got := f.served(name); got != want {
			t.Errorf("served(%q) = %s, want %s", name, got, want)
		}
	}
	loaded, err := f.store.Loaded()
	if err != nil || len(loaded) != 2 || loaded[0].File != filepath.Join(f.dir, "mc.example.com.pem") || loaded[0].Issuer != "Playkeeper Test T1" {
		t.Errorf("Loaded = %+v, %v", loaded, err)
	}
}

func TestStoreNeverServesInvalid(t *testing.T) {
	f := newStoreFixture(t)
	now := f.clock.now()
	f.save("expired.example.com.pem", []string{"expired.example.com"}, now.Add(-100*24*time.Hour), now.Add(-10*24*time.Hour))
	f.save("future.example.com.pem", []string{"future.example.com"}, now.Add(time.Hour), now.Add(90*24*time.Hour))
	f.save("wrong.example.com.pem", []string{"right.example.com"}, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	f.save("mc.example.com.pem", []string{"mc.example.com"}, now.Add(-time.Hour), now.Add(24*time.Hour))
	f.open()

	for _, name := range []string{"expired.example.com", "future.example.com", "wrong.example.com", "right.example.com"} {
		if got := f.served(name); got != "fallback" {
			t.Errorf("served(%q) = %s, want the fallback", name, got)
		}
	}
	loaded, err := f.store.Loaded()
	if len(loaded) != 3 || err == nil || !strings.Contains(err.Error(), "wrong.example.com.pem: the certificate does not cover wrong.example.com") {
		t.Errorf("Loaded = %d certificates, %v", len(loaded), err)
	}

	if got := f.served("mc.example.com"); !strings.HasPrefix(got, "mc.example.com@") {
		t.Fatalf("served = %s", got)
	}
	f.clock.add(24 * time.Hour)
	if got := f.served("mc.example.com"); got != "fallback" {
		t.Errorf("after it expired, served = %s", got)
	}
	f.clock.set(now.Add(2 * time.Hour))
	if got := f.served("future.example.com"); !strings.HasPrefix(got, "future.example.com@") {
		t.Errorf("once valid, served = %s", got)
	}
}

func TestStoreReloads(t *testing.T) {
	f := newStoreFixture(t)
	now := f.clock.now()
	f.save("mc.example.com.pem", []string{"mc.example.com"}, now.Add(-time.Hour), now.Add(10*24*time.Hour))
	f.open()
	first := f.served("mc.example.com")

	f.save("mc.example.com.pem", []string{"mc.example.com"}, now.Add(-time.Minute), now.Add(90*24*time.Hour))
	f.save("new.example.com.pem", []string{"new.example.com"}, now.Add(-time.Minute), now.Add(90*24*time.Hour))
	if got := f.served("mc.example.com"); got != first {
		t.Errorf("reloaded before RecheckEvery: %s", got)
	}
	f.clock.add(time.Second)
	if got := f.served("mc.example.com"); got == first || !strings.HasPrefix(got, "mc.example.com@") {
		t.Errorf("the renewed certificate is not served: %s (was %s)", got, first)
	}
	if got := f.served("new.example.com"); !strings.HasPrefix(got, "new.example.com@") {
		t.Errorf("a new certificate is not served: %s", got)
	}

	if err := os.Remove(filepath.Join(f.dir, "new.example.com.pem")); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := f.served("new.example.com"); got != "fallback" {
		t.Errorf("a removed certificate is still served: %s", got)
	}

	f.clock.add(-time.Hour)
	f.save("new.example.com.pem", []string{"new.example.com"}, now.Add(-2*time.Hour), now.Add(90*24*time.Hour))
	if got := f.served("new.example.com"); !strings.HasPrefix(got, "new.example.com@") {
		t.Errorf("after the clock went back, files are not checked: %s", got)
	}
}

func TestStoreSkipsOtherFiles(t *testing.T) {
	f := newStoreFixture(t)
	now := f.clock.now()
	outside := t.TempDir()
	f.save("mc.example.com.pem", []string{"mc.example.com"}, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	saveBundle(t, outside, "evil.example.com.pem", f.ca.bundle(t, []string{"evil.example.com"}, now.Add(-time.Hour), now.Add(90*24*time.Hour)))
	if err := os.Symlink(filepath.Join(outside, "evil.example.com.pem"), filepath.Join(f.dir, "evil.example.com.pem")); err != nil {
		t.Fatal(err)
	}
	valid := f.ca.bundle(t, []string{"x.example.com"}, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	for _, name := range []string{"README", "x.example.com.pem.bak", ".x.example.com.pem.tmp-0a1b2c", "X.example.com.pem", "x_y.example.com.pem", "localhost.pem"} {
		if err := os.WriteFile(filepath.Join(f.dir, name), valid, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(f.dir, "dir.example.com.pem"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "garbage.example.com.pem"), []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "huge.example.com.pem"), make([]byte, maxPEMSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	f.open()

	loaded, err := f.store.Loaded()
	var names []string
	for _, c := range loaded {
		names = append(names, c.Names...)
	}
	if !slices.Equal(names, []string{"mc.example.com"}) {
		t.Errorf("loaded %v", names)
	}
	if err == nil || !strings.Contains(err.Error(), "garbage.example.com.pem") || !strings.Contains(err.Error(), "huge.example.com.pem") || strings.Contains(err.Error(), "evil") {
		t.Errorf("Loaded error = %v", err)
	}
	if got := f.served("evil.example.com"); got != "fallback" {
		t.Errorf("a symbolic link was followed: %s", got)
	}
}

func TestStoreFallback(t *testing.T) {
	f := newStoreFixture(t)
	if _, err := NewStore(StoreOptions{}); err == nil {
		t.Error("a store without a directory was created")
	}
	if _, err := NewStore(StoreOptions{Dir: f.dir, FallbackCert: f.certFile}); err == nil {
		t.Error("a fallback without a key was accepted")
	}
	if _, err := NewStore(StoreOptions{Dir: f.dir, FallbackCert: f.certFile, FallbackKey: f.certFile + ".missing"}); err == nil {
		t.Error("a missing fallback key was accepted")
	}
	s, err := NewStore(StoreOptions{Dir: f.dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCertificate(hello("")); err == nil || !strings.Contains(err.Error(), "no certificate") {
		t.Errorf("without a fallback: %v", err)
	}

	f.open()
	before := f.served("")
	if err := os.WriteFile(f.certFile, []byte("damaged"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Reload(); err == nil || !strings.Contains(err.Error(), "fallback certificate") {
		t.Errorf("Reload with a damaged fallback = %v", err)
	}
	if got := f.served(""); got != before {
		t.Errorf("the previous fallback is not kept: %s", got)
	}
}

func TestStoreHandshake(t *testing.T) {
	f := newStoreFixture(t)
	now := f.clock.now()
	f.save("mc.example.com.pem", []string{"mc.example.com"}, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	f.open()

	handshake := func(cfg *tls.Config) (*x509.Certificate, error) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		done := make(chan error, 1)
		go func() {
			conn, err := ln.Accept()
			if err != nil {
				done <- err
				return
			}
			defer conn.Close()
			done <- tls.Server(conn, &tls.Config{GetCertificate: f.store.GetCertificate}).Handshake()
		}()
		conn, err := tls.Dial("tcp", ln.Addr().String(), cfg)
		if err != nil {
			<-done
			return nil, err
		}
		defer conn.Close()
		if err := <-done; err != nil {
			return nil, err
		}
		return conn.ConnectionState().PeerCertificates[0], nil
	}
	leaf, err := handshake(&tls.Config{ServerName: "mc.example.com", RootCAs: f.ca.pool()})
	if err != nil || leaf.DNSNames[0] != "mc.example.com" {
		t.Fatalf("verified handshake for mc.example.com: %v", err)
	}
	fallback, err := handshake(&tls.Config{InsecureSkipVerify: true})
	if err != nil || len(fallback.IPAddresses) != 1 || !fallback.IPAddresses[0].Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("handshake by IP address: %v", err)
	}
	if _, err := handshake(&tls.Config{ServerName: "other.example.com", RootCAs: f.ca.pool()}); err == nil {
		t.Error("a browser would trust the fallback for another name")
	}
}

func TestStoreConcurrent(t *testing.T) {
	f := newStoreFixture(t)
	now := f.clock.now()
	f.save("mc.example.com.pem", []string{"mc.example.com"}, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	f.open()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				f.clock.add(time.Second)
				if c, err := f.store.GetCertificate(hello("mc.example.com")); err != nil || c == nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	for j := 0; j < 5; j++ {
		f.save("mc.example.com.pem", []string{"mc.example.com"}, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	}
	wg.Wait()
}

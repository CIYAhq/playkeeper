package certs

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	mrand "math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// testCA issues certificates the way a public CA does: a root, an
// intermediate that signs leaves, and chains of leaf plus intermediate.
type testCA struct {
	root, inter       *x509.Certificate
	rootKey, interKey *ecdsa.PrivateKey
}

func newKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func serial(t testing.TB) *big.Int {
	t.Helper()
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func newTestCA(t testing.TB) *testCA {
	t.Helper()
	now := time.Now()
	ca := &testCA{rootKey: newKey(t), interKey: newKey(t)}
	rootTmpl := &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               pkix.Name{Organization: []string{"Playkeeper Test"}, CommonName: "Playkeeper Test Root"},
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &ca.rootKey.PublicKey, ca.rootKey)
	if err != nil {
		t.Fatal(err)
	}
	if ca.root, err = x509.ParseCertificate(der); err != nil {
		t.Fatal(err)
	}
	interTmpl := &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               pkix.Name{Organization: []string{"Playkeeper Test"}, CommonName: "T1"},
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.Add(5 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err = x509.CreateCertificate(rand.Reader, interTmpl, ca.root, &ca.interKey.PublicKey, ca.rootKey)
	if err != nil {
		t.Fatal(err)
	}
	if ca.inter, err = x509.ParseCertificate(der); err != nil {
		t.Fatal(err)
	}
	return ca
}

// issue returns the chain (leaf, intermediate) of a certificate for pub.
func (ca *testCA) issue(t testing.TB, pub crypto.PublicKey, names []string, notBefore, notAfter time.Time) [][]byte {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: serial(t),
		DNSNames:     names,
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if len(names) > 0 {
		tmpl.Subject = pkix.Name{CommonName: names[0]}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.inter, pub, ca.interKey)
	if err != nil {
		t.Fatal(err)
	}
	return [][]byte{der, ca.inter.Raw}
}

// bundle is a saved certificate file as Issue writes it.
func (ca *testCA) bundle(t testing.TB, names []string, notBefore, notAfter time.Time) []byte {
	t.Helper()
	key := newKey(t)
	b, err := encodeBundle(ca.issue(t, &key.PublicKey, names, notBefore, notAfter), key)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (ca *testCA) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.root)
	return p
}

// writeSelfSigned writes a certificate for 127.0.0.1 and its key the way
// the panel's EnsureSelfSignedCert does, returning the two paths.
func writeSelfSigned(t testing.TB, dir string) (certFile, keyFile string) {
	t.Helper()
	key := newKey(t)
	tmpl := &x509.Certificate{
		SerialNumber: serial(t),
		Subject:      pkix.Name{Organization: []string{"Playkeeper"}, CommonName: "Playkeeper panel"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(825 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

// handshake connects to a TLS server on loopback that picks its
// certificate with getCert, returning the certificate the client accepted.
func handshake(t testing.TB, getCert func(*tls.ClientHelloInfo) (*tls.Certificate, error), cfg *tls.Config) (*x509.Certificate, error) {
	t.Helper()
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
		done <- tls.Server(conn, &tls.Config{GetCertificate: getCert}).Handshake()
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

// freePorts are the ports freePort gave out, so it never gives one twice.
var freePorts = struct {
	sync.Mutex
	given map[int]bool
}{given: map[int]bool{}}

// freePort returns a TCP port on 127.0.0.1 that was free a moment ago, for
// code under test to bind itself, such as the HTTP-01 responder. A port the
// kernel handed out for port 0 won't do: once it is closed, another
// package's test, run in parallel, can be handed it too and answer on it.
// So the port lies below the kernel's range for port 0.
func freePort(t testing.TB) int {
	t.Helper()
	low := 32768
	if b, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range"); err == nil {
		if f := strings.Fields(string(b)); len(f) == 2 {
			if n, err := strconv.Atoi(f[0]); err == nil {
				low = n
			}
		}
	}
	const from = 10000
	if low-from < 1000 {
		t.Fatalf("the kernel hands out ports from %d, leaving too few below it for tests", low)
	}
	freePorts.Lock()
	defer freePorts.Unlock()
	for range 1000 {
		p := from + mrand.IntN(low-from)
		if freePorts.given[p] {
			continue
		}
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err != nil {
			continue
		}
		ln.Close()
		freePorts.given[p] = true
		return p
	}
	t.Fatalf("no free port between %d and %d", from, low)
	return 0
}

// problemOf returns err as a *Problem, failing the test otherwise.
func problemOf(t testing.TB, err error) *Problem {
	t.Helper()
	var p *Problem
	if !errors.As(err, &p) {
		t.Fatalf("error %v (%T) is not a *Problem", err, err)
	}
	return p
}

// wantProblem checks a Problem's code and, when kind is not empty, its
// kind parameter.
func wantProblem(t testing.TB, err error, code, kind string) *Problem {
	t.Helper()
	p := problemOf(t, err)
	if p.Code != code || p.Params["kind"] != kind {
		t.Fatalf("problem %s (kind %q), want %s (kind %q): %s [%s]", p.Code, p.Params["kind"], code, kind, p.Message, p.Detail)
	}
	return p
}

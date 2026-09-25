package packs

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/portshare"
)

// selfSigned is a certificate for 127.0.0.1, like the panel's own, with a
// pool that trusts it.
func selfSigned(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Playkeeper test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}, pool
}

// TestOnePort serves the panel over HTTPS and packs over plain HTTP on one
// port, as Playkeeper wires them, while connections that stay silent or
// stop after their first byte are held open.
func TestOnePort(t *testing.T) {
	cert, pool := selfSigned(t)
	s := Store{Dir: t.TempDir()}
	pack := resourcePack(t)
	info, err := s.Put(context.Background(), bytes.NewReader(pack), int64(len(pack)))
	if err != nil {
		t.Fatal(err)
	}
	packs := NewHandler(s, func(sum string) bool { return sum == info.SHA1 })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	split := portshare.Split(ln, portshare.Options{PeekTimeout: time.Minute})
	mux := http.NewServeMux()
	mux.Handle("/packs/", packs)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "panel") })
	quiet := log.New(io.Discard, "", 0)
	secure := &http.Server{
		Handler:           mux,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}},
		ReadHeaderTimeout: time.Minute,
		ErrorLog:          quiet,
	}
	plain := &http.Server{Handler: NewPlainHandler(packs), ReadHeaderTimeout: time.Minute, ErrorLog: quiet}
	served := make(chan error, 2)
	go func() { served <- secure.ServeTLS(split.TLS(), "", "") }()
	go func() { served <- plain.Serve(split.Plain()) }()

	addr := ln.Addr().String()
	var stalled []net.Conn
	for _, first := range []string{"", "G", "\x16"} {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if _, err := io.WriteString(c, first); err != nil {
			t.Fatal(err)
		}
		stalled = append(stalled, c)
	}

	tlsTransport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}, ForceAttemptHTTP2: true}
	defer tlsTransport.CloseIdleConnections()
	tlsClient := &http.Client{Transport: tlsTransport, Timeout: 10 * time.Second}
	plainTransport := &http.Transport{}
	defer plainTransport.CloseIdleConnections()
	plainClient := &http.Client{
		Transport:     plainTransport,
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	get := func(c *http.Client, url string, header http.Header) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		if header != nil {
			req.Header = header
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		return resp, body
	}

	resp, body := get(tlsClient, "https://"+addr+"/", nil)
	if resp.StatusCode != http.StatusOK || string(body) != "panel" || resp.TLS == nil || resp.ProtoMajor != 2 {
		t.Errorf("panel over HTTPS = %d %q, %s", resp.StatusCode, body, resp.Proto)
	}
	resp, body = get(tlsClient, "https://"+addr+PackPath(info.SHA1), nil)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, pack) {
		t.Errorf("pack over HTTPS = %d, %d bytes", resp.StatusCode, len(body))
	}
	resp, body = get(plainClient, "http://"+addr+PackPath(info.SHA1), nil)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, pack) || resp.TLS != nil || resp.Header.Get("ETag") != `"`+info.SHA1+`"` {
		t.Errorf("pack over HTTP = %d, %d bytes, ETag %q", resp.StatusCode, len(body), resp.Header.Get("ETag"))
	}
	resp, body = get(plainClient, "http://"+addr+PackPath(info.SHA1), http.Header{"Range": {"bytes=0-9"}})
	if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(body, pack[:10]) {
		t.Errorf("resumed download over HTTP = %d %q", resp.StatusCode, body)
	}
	if resp, _ = get(plainClient, "http://"+addr+PackPath(testSum), nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown pack over HTTP = %d", resp.StatusCode)
	}
	resp, _ = get(plainClient, "http://"+addr+"/servers?tab=world", nil)
	location := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusPermanentRedirect || location != "https://"+addr+"/servers?tab=world" {
		t.Fatalf("panel over HTTP = %d %q", resp.StatusCode, location)
	}
	if resp, body = get(tlsClient, location, nil); resp.StatusCode != http.StatusOK || string(body) != "panel" {
		t.Errorf("following the redirect = %d %q", resp.StatusCode, body)
	}

	slow, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	time.Sleep(50 * time.Millisecond)
	if _, err := fmt.Fprintf(slow, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", PackPath(info.SHA1), addr); err != nil {
		t.Fatal(err)
	}
	slow.SetReadDeadline(time.Now().Add(10 * time.Second))
	if resp, err := http.ReadResponse(bufio.NewReader(slow), nil); err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("a request slow to start: %v", err)
	}

	// Shutdown would wait on the stalled connections, which never send a
	// request.
	secure.Close()
	plain.Close()
	for range 2 {
		select {
		case err := <-served:
			if !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("serving ended with %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the servers didn't stop")
		}
	}
	if _, err := ln.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Errorf("after closing both servers, the port accepts: %v", err)
	}
	for i, c := range stalled {
		c.SetReadDeadline(time.Now().Add(10 * time.Second))
		var ne net.Error
		if _, err := c.Read(make([]byte, 1)); err == nil || errors.As(err, &ne) && ne.Timeout() {
			t.Errorf("stalled connection %d is still open: %v", i, err)
		}
	}
}

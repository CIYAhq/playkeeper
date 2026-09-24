package panel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnsureSelfSignedCert creates the panel's self-signed certificate if it does
// not exist and returns its SHA-256 fingerprint. The private key never leaves
// the host; users verify the fingerprint the installer prints.
func EnsureSelfSignedCert(dir string, now time.Time) (fingerprint string, err error) {
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if b, err := os.ReadFile(certPath); err == nil {
		if _, err := os.Stat(keyPath); err == nil {
			return FingerprintPEM(b)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Playkeeper panel (" + host + ")", Organization: []string{"Playkeeper self-signed"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(825 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	if host != "" && !strings.ContainsAny(host, " /") {
		tmpl.DNSNames = append(tmpl.DNSNames, host)
	}
	tmpl.IPAddresses = append(tmpl.IPAddresses, HostIPs()...)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return "", err
	}
	cb := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certPath, cb, 0o644); err != nil {
		return "", err
	}
	return Fingerprint(der), nil
}

// HostIPs lists global unicast addresses of this host's interfaces.
func HostIPs() []net.IP {
	var out []net.IP
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || !ipn.IP.IsGlobalUnicast() {
			continue
		}
		out = append(out, ipn.IP)
	}
	return out
}

func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

func FingerprintPEM(b []byte) (string, error) {
	blk, _ := pem.Decode(b)
	if blk == nil {
		return "", fmt.Errorf("no PEM certificate found")
	}
	return Fingerprint(blk.Bytes), nil
}

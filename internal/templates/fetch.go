package templates

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/CIYAhq/playkeeper/internal/packs"
)

// Downloading a data pack of a confirmed import.
const (
	KindPackAddress     Kind = "pack_address_refused"
	KindPackUnreachable Kind = "pack_unreachable"
	KindPackHash        Kind = "pack_hash_mismatch"
)

const maxPackRedirects = 5

// nonPublic are the ranges netip's own tests don't cover that a pack
// download must never reach: shared address space (CGNAT, where some clouds
// keep their metadata service), IETF, documentation, benchmarking and
// reserved ranges, and IPv6 transition ranges that carry an IPv4 address.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

// nat64 addresses reach the IPv4 address in their last four bytes.
var nat64 = netip.MustParsePrefix("64:ff9b::/96")

// publicAddr reports whether a pack download may connect to a: not
// loopback, private, link-local (which holds most clouds' metadata
// service), multicast or any range in nonPublic.
func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	if nat64.Contains(a) {
		b := a.As16()
		return publicAddr(netip.AddrFrom4([4]byte(b[12:])))
	}
	if !a.IsValid() || a.IsUnspecified() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsMulticast() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

func publicAddrPort(ap netip.AddrPort) bool {
	return ap.Port() == 443 && publicAddr(ap.Addr())
}

// addrRefused is a connection a pack download refused to make.
type addrRefused struct{ addr string }

func (e *addrRefused) Error() string {
	return "refused to connect to " + e.addr + ", which is not a public address on port 443"
}

// refuseAddr is the dialer's Control: it sees the address DNS resolved to,
// just before the connection is made.
func refuseAddr(address string, allowed func(netip.AddrPort) bool) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || !allowed(ap) {
		return &addrRefused{addr: address}
	}
	return nil
}

var errPlainHTTP = errors.New("refused a download that is not HTTPS")

// httpsOnly refuses every request that isn't HTTPS, each redirect's too.
type httpsOnly struct{ rt http.RoundTripper }

func (h httpsOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" {
		if r.Body != nil {
			r.Body.Close()
		}
		return nil, errPlainHTTP
	}
	return h.rt.RoundTrip(r)
}

// redirectRefused is a redirect that breaks the rules of a pack's address.
type redirectRefused struct{ host string }

func (e *redirectRefused) Error() string {
	return "refused a redirect to " + e.host + ", which is not HTTPS on port 443 to a public host name"
}

// checkRedirect holds each redirect to the rules of a pack's own address:
// HTTPS on port 443 to a public host name. Its query may stay, as download
// hosts sign their links with one; it is kept out of errors for that reason.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxPackRedirects {
		return fmt.Errorf("stopped after %d redirects", maxPackRedirects)
	}
	u := req.URL
	if u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") || !publicHost(u.Hostname()) {
		return &redirectRefused{host: u.Hostname()}
	}
	return nil
}

// PackClient is the client a template's data packs download through. A
// template may name any public host, so hosts can't be on a fixed list:
// instead every connection, each redirect's too, must be HTTPS to a public
// address on port 443, checked after DNS resolution and never through a
// proxy.
func PackClient() *http.Client {
	return packClient(publicAddrPort)
}

func packClient(allowed func(netip.AddrPort) bool) *http.Client {
	d := &net.Dialer{Timeout: 15 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		return refuseAddr(address, allowed)
	}}
	tr := &http.Transport{
		DialContext:           d.DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{Transport: httpsOnly{tr}, CheckRedirect: checkRedirect, Timeout: 5 * time.Minute}
}

// FetchPack downloads a data pack of a confirmed import through hc, which
// is PackClient outside tests, into an unnamed file in dir (see
// packs.Stage), and keeps it only if it matches the template's checksum.
// The caller closes the file.
func FetchPack(ctx context.Context, hc *http.Client, dir string, pk Pack, lim packs.Limits) (*os.File, int64, error) {
	name, host := printable(pk.Name), ""
	if u, err := url.Parse(pk.URL); err == nil {
		host = strings.ToLower(u.Hostname())
	}
	params := kv("name", name, "host", host)
	refused := func(err error) error {
		return &Error{Notice: notice(KindPackAddress, params,
			fmt.Sprintf("Playkeeper didn't download the data pack %s: its address isn't HTTPS on a public website.", name),
			"Ask whoever shared the template for a public HTTPS link, then add the pack on the World tab."), Err: err}
	}
	var want string
	var h hash.Hash
	switch {
	case pk.SHA256 != "":
		want, h = pk.SHA256, sha256.New()
	case pk.SHA1 != "":
		want, h = pk.SHA1, sha1.New()
	}
	if pk.Kind != DataPack || !publicURL(pk.URL) || h == nil {
		return nil, 0, refused(nil)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pk.URL, nil)
	if err != nil {
		return nil, 0, refused(err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		var addr *addrRefused
		var redirect *redirectRefused
		if errors.As(err, &addr) || errors.As(err, &redirect) || errors.Is(err, errPlainHTTP) {
			return nil, 0, refused(err)
		}
		return nil, 0, &Error{Notice: notice(KindPackUnreachable, params,
			fmt.Sprintf("Playkeeper couldn't download the data pack %s from %s.", name, host),
			"Check this machine's connection, then add the pack on the World tab."), Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fail(KindPackUnreachable, kv("name", name, "host", host, "status", strconv.Itoa(resp.StatusCode)),
			fmt.Sprintf("%s answered %d instead of sending the data pack %s.", host, resp.StatusCode, name),
			"Ask whoever shared the template for a new link, then add the pack on the World tab.")
	}
	f, n, err := packs.Stage(dir, io.TeeReader(resp.Body, h), packs.Data, lim)
	if err != nil {
		return nil, 0, err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		f.Close()
		return nil, 0, fail(KindPackHash, params,
			fmt.Sprintf("The data pack %s from %s doesn't match the template's checksum, so it wasn't used.", name, host),
			"The file may have changed since the template was made. Ask whoever shared it for a new one.")
	}
	return f, n, nil
}

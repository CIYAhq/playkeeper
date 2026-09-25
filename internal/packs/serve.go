package packs

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
)

// PathPrefix is where the panel serves resource packs. `/packs/` is the
// public modpack page for friends, so resource packs stay out of it.
const PathPrefix = "/resource-packs/"

// PackPath is the URL path at which NewHandler serves the pack whose SHA-1
// hash is sum.
func PackPath(sum string) string { return PathPrefix + sum + ".zip" }

func parsePackPath(p string) (string, bool) {
	sum, ok := strings.CutPrefix(p, PathPrefix)
	if !ok {
		return "", false
	}
	sum, ok = strings.CutSuffix(sum, ".zip")
	return sum, ok && validSHA1(sum)
}

// NewHandler serves the packs in store for which active returns true, at
// PackPath, to GET and HEAD requests, including the range requests of
// resumed downloads. Everything else is 404 Not Found, and the store is
// never listed. Players' games download packs without signing in, so the
// handler needs no authentication: it serves only packs a server offers
// every player anyway. active is called on every request, so it should be
// cheap.
func NewHandler(store Store, active func(sum string) bool) http.Handler {
	return packHandler{store, active}
}

type packHandler struct {
	store  Store
	active func(string) bool
}

func (h packHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sum, ok := parsePackPath(r.URL.Path)
	if !ok || r.Method != http.MethodGet && r.Method != http.MethodHead || h.active == nil || !h.active(sum) {
		http.NotFound(w, r)
		return
	}
	f, st, err := h.store.open(sum)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	hdr := w.Header()
	hdr.Set("Content-Type", "application/zip")
	hdr.Set("ETag", `"`+sum+`"`)
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Content-Disposition", `attachment; filename="`+sum+`.zip"`)
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// NewPlainHandler handles the plain-HTTP requests that reach the panel's
// port: pack downloads go to packs, and every other request is redirected
// to HTTPS on the same host and port, as the panel is served only over
// HTTPS.
func NewPlainHandler(packs http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, PathPrefix) {
			packs.ServeHTTP(w, r)
			return
		}
		host, port, ok := redirectHost(r)
		if !ok {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		hostport := net.JoinHostPort(host, port)
		if port == "443" {
			hostport = strings.TrimSuffix(hostport, ":443")
		}
		http.Redirect(w, r, "https://"+hostport+r.URL.RequestURI(), http.StatusPermanentRedirect)
	})
}

// redirectHost is where to send a plain-HTTP request over HTTPS: the host
// it was sent to, and the port in its Host header or else the port it
// arrived on.
func redirectHost(r *http.Request) (host, port string, ok bool) {
	host = r.Host
	if h, p, err := net.SplitHostPort(r.Host); err == nil {
		host, port = h, p
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if port == "" {
		if a, isAddr := r.Context().Value(http.LocalAddrContextKey).(net.Addr); isAddr {
			if _, p, err := net.SplitHostPort(a.String()); err == nil {
				port = p
			}
		}
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return "", "", false
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return host, port, a.Zone() == ""
	}
	if host == "" || len(host) > 253 || strings.HasPrefix(host, ".") || strings.Contains(host, "..") {
		return "", "", false
	}
	for i := 0; i < len(host); i++ {
		if c := host[i] | 0x20; !(c >= 'a' && c <= 'z' || host[i] >= '0' && host[i] <= '9' || host[i] == '-' || host[i] == '.' || host[i] == '_') {
			return "", "", false
		}
	}
	return host, port, true
}

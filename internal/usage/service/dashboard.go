package service

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"net/http"
	"path"
	"time"
)

// The dashboard is one page and its files, built into the binary. It holds
// no numbers: its script asks GET /v1/summary for them with the read token,
// which the browser keeps and sends nowhere else.
//
//go:embed dashboard
var dashboardFS embed.FS

// dashboardFiles are the dashboard's paths and the files they serve.
var dashboardFiles = map[string]string{
	"/dashboard":          "dashboard/index.html",
	"/dashboard/app.css":  "dashboard/app.css",
	"/dashboard/app.js":   "dashboard/app.js",
	"/dashboard/icon.svg": "dashboard/icon.svg",
}

// dashboardCSP lets the page load only its own files and ask only this
// service for the counts. form-action 'none' keeps a sign-in without its
// script from putting the token in an address.
const dashboardCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

var dashboardTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".svg":  "image/svg+xml",
}

type dashboardFile struct {
	body        []byte
	contentType string
	etag        string
}

// dashboard serves the dashboard's files, each revalidated by its ETag.
func dashboard() http.HandlerFunc {
	files := map[string]dashboardFile{}
	for p, name := range dashboardFiles {
		b, err := dashboardFS.ReadFile(name)
		if err != nil {
			panic(err)
		}
		sum := sha256.Sum256(b)
		files[p] = dashboardFile{body: b, contentType: dashboardTypes[path.Ext(name)], etag: `"` + hex.EncodeToString(sum[:12]) + `"`}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		f, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", f.contentType)
		h.Set("Content-Security-Policy", dashboardCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("X-Robots-Tag", "noindex")
		h.Set("Cache-Control", "no-cache")
		h.Set("ETag", f.etag)
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.body))
	}
}

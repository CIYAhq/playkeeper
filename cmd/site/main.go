// Command site builds playkeeper.io from site/ and the repository's docs into
// a folder nginx serves (see internal/site). site/Dockerfile runs it; locally,
// make site builds it into site/dist, and -serve serves it to a browser.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/site"
)

func main() {
	root := flag.String("root", ".", "the repository")
	out := flag.String("out", "site/dist", "the folder to build into: html/ for the web root and nginx/ for nginx.conf's include")
	serve := flag.String("serve", "", "instead of writing the site, serve it at this address, as nginx would (for example 127.0.0.1:8080)")
	flag.Parse()
	o, err := site.Build(site.Options{Root: os.DirFS(*root), Settings: site.Default, Now: time.Now()})
	if err == nil && *serve != "" {
		fmt.Printf("serving playkeeper.io at http://%s/\n", *serve)
		err = http.ListenAndServe(*serve, handler(o))
	} else if err == nil {
		err = write(*out, o)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "site:", err)
		os.Exit(1)
	}
	fmt.Printf("built %d files into %s\n", len(o.Files), *out)
}

func write(out string, o *site.Output) error {
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	for name, b := range o.Files {
		p := filepath.Join(out, "html", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(out, "nginx"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "nginx", "site.conf"), o.Nginx, 0o644)
}

// handler answers like site/nginx.conf: /pricing is pricing.html, and a
// missing page is 404.html with a 404.
func handler(o *site.Output) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		b, ok := o.Files[name]
		if !ok {
			b, ok = o.Files[name+".html"]
			name += ".html"
		}
		status := http.StatusOK
		if !ok {
			b, name, status = o.Files["404.html"], "404.html", http.StatusNotFound
		}
		if ct := mimeType(name); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(status)
		_, _ = w.Write(b)
	})
}

func mimeType(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".json":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".xml":
		return "application/xml"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".woff2":
		return "font/woff2"
	}
	return ""
}

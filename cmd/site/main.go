// Command site builds playkeeper.io from site/ and the repository's docs into
// a folder nginx serves (see internal/site). site/Dockerfile runs it; locally,
// make site builds it into site/dist.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/CIYAhq/playkeeper/internal/site"
)

func main() {
	root := flag.String("root", ".", "the repository")
	out := flag.String("out", "site/dist", "the folder to build into: html/ for the web root and nginx/ for nginx.conf's include")
	flag.Parse()
	o, err := site.Build(site.Options{Root: os.DirFS(*root), Settings: site.Default, Now: time.Now()})
	if err == nil {
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

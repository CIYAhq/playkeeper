// Command version-lists writes the version lists built into Playkeeper,
// which a machine offers while an upstream's own list can't be reached and
// it keeps none of its own: Paper's, from PaperMC's Fill API, into
// internal/minecraft/builtin/paper.json, and each other type's releases
// with what installing them needs, into
// internal/minecraft/software/builtin/lists.json. Run it with
// make version-lists before a release, so each release ships recent lists.
// A list whose upstream doesn't answer is kept as it was. CI never runs it:
// no build or check depends on an upstream answering.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/minecraft/software"
)

func main() {
	paper := flag.String("paper", "internal/minecraft/builtin/paper.json", "where to write Paper's list")
	others := flag.String("software", "internal/minecraft/software/builtin/lists.json", "where to write the other types' lists")
	fill := flag.String("fill", minecraft.DefaultFillURL, "PaperMC's Fill API")
	checkOnly := flag.Bool("check", false, "write nothing: check that every upstream answers and serves what its list says, and how the built-in lists compare")
	flag.Parse()
	now := time.Now()
	if *checkOnly {
		dir, err := os.MkdirTemp("", "version-lists-check-")
		if err != nil {
			fmt.Fprintln(os.Stderr, "version-lists:", err)
			os.Exit(1)
		}
		defer os.RemoveAll(dir)
		hc := &http.Client{Timeout: 10 * time.Minute}
		failed := newChecker(os.Stdout, minecraft.Fill{BaseURL: *fill, Client: hc}, software.Sources{Client: hc}, hc, dir, now).run()
		if len(failed) > 0 {
			fmt.Fprintln(os.Stderr, "version-lists: these upstreams failed:", strings.Join(failed, ", "))
			os.RemoveAll(dir)
			os.Exit(1)
		}
		return
	}
	hc := &http.Client{Timeout: time.Minute}
	var failed []string
	if err := writePaper(*paper, minecraft.Fill{BaseURL: *fill, Client: hc}, now); err != nil {
		failed = append(failed, err.Error())
	}
	if err := writeSoftware(*others, software.Sources{Client: hc}, now); err != nil {
		failed = append(failed, err.Error())
	}
	if len(failed) > 0 {
		fmt.Fprintln(os.Stderr, "version-lists:", strings.Join(failed, "\nversion-lists: "))
		os.Exit(1)
	}
}

// writePaper writes Paper's list to path, keeping the one there when the
// Fill API can't be read, so a release made during an outage still ships
// the last list that was made.
func writePaper(path string, f minecraft.Fill, now time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	l, err := f.BuiltInList(ctx, now)
	if err != nil {
		return fmt.Errorf("Paper's list was kept as it was: %w", err)
	}
	if err := writeJSON(path, l); err != nil {
		return err
	}
	fmt.Printf("Paper: %d versions, as PaperMC listed them at %s\n", len(l.Versions), l.MadeAt.Format(time.RFC3339))
	return nil
}

// writeSoftware writes each other type's list to path, keeping a type's
// list there when its upstream can't be read.
func writeSoftware(path string, s software.Sources, now time.Time) error {
	var kept software.BuiltInLists
	if b, err := os.ReadFile(path); err == nil {
		kept, _ = software.ParseBuiltInLists(b)
	}
	out := software.BuiltInLists{Types: map[string]software.BuiltInType{}}
	var failed []string
	for _, typ := range []string{software.Vanilla, software.Purpur, software.Fabric, software.Quilt, software.NeoForge, software.Forge} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		t, err := s.RecordBuiltIn(ctx, typ, now)
		cancel()
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s's list was kept as it was: %v", typ, err))
			if k, ok := kept.Types[typ]; ok {
				out.Types[typ] = k
			}
			continue
		}
		out.Types[typ] = t
		fmt.Printf("%s: %d versions, as its upstream listed them at %s\n", typ, len(t.Releases), t.MadeAt.Format(time.RFC3339))
	}
	if len(out.Types) > 0 {
		if err := writeJSON(path, out); err != nil {
			return err
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%s", strings.Join(failed, "; "))
	}
	return nil
}

// writeJSON writes v to path through a temporary file and a rename.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

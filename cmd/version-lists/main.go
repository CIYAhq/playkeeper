// Command version-lists writes the version lists built into Playkeeper,
// which a machine offers while an upstream's own list can't be reached and
// it keeps none of its own: Paper's, from PaperMC's Fill API, into
// internal/minecraft/builtin/paper.json. Run it with make version-lists
// before a release, so each release ships a recent list. CI never runs it:
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
	"time"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

func main() {
	out := flag.String("paper", "internal/minecraft/builtin/paper.json", "where to write Paper's list")
	fill := flag.String("fill", minecraft.DefaultFillURL, "PaperMC's Fill API")
	flag.Parse()
	if err := writePaper(*out, minecraft.Fill{BaseURL: *fill, Client: &http.Client{Timeout: time.Minute}}, time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "version-lists:", err)
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
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".paper-*.json")
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
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	fmt.Printf("Paper: %d versions, as PaperMC listed them at %s\n", len(l.Versions), l.MadeAt.Format(time.RFC3339))
	return nil
}

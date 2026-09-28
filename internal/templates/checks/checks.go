// Package checks is what happened the last time a server was created and
// started from one of playkeeper.io's templates: the files in
// site/data/checks, one per template, which cmd/template-check writes and the
// site reads. Nobody edits them by hand.
package checks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"time"
)

// Status is how the template's last check ended.
type Status string

const (
	// Passing: the server reached Done with every add-on installed and
	// nothing failing to load.
	Passing Status = "passing"
	// Failing: it didn't, so the site holds the template until it's fixed.
	Failing Status = "failing"
)

// Check is one template's last check.
type Check struct {
	Status Status `json:"status"`
	// Failure says what went wrong, for a failing check.
	Failure string `json:"failure,omitempty"`
	// Checked is the day (YYYY-MM-DD), and Release the Playkeeper version it
	// ran on.
	Checked string `json:"checked"`
	Release string `json:"release"`
	// Build is the server software's build the plan picked.
	Build string `json:"build,omitempty"`
	// DoneSeconds is what the server's log said when it was ready: "Done
	// (12.005s)!".
	DoneSeconds float64 `json:"doneSeconds,omitempty"`
	// Addons are the template's add-ons as they installed, in its order.
	Addons  []Addon  `json:"addons,omitempty"`
	Modpack *Modpack `json:"modpack,omitempty"`
	// Crossplay says the release turned on crossplay for the server, and
	// Geyser and Floodgate started beside the template's add-ons. It's
	// false on a release without crossplay and for a server it isn't
	// offered for.
	Crossplay bool `json:"crossplay,omitempty"`
}

// Addon is one add-on as it installed.
type Addon struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Slug    string `json:"slug"`
	Version string `json:"version"`
	// Licence is the SPDX id its source lists; LicenseRef-All-Rights-Reserved
	// for none.
	Licence string `json:"licence"`
	// Downloads is its total on its source on the day of the check.
	Downloads int `json:"downloads"`
}

// Modpack is the modpack a template installs, as it installed.
type Modpack struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Mods counts the mods it put on the server.
	Mods      int `json:"mods"`
	Downloads int `json:"downloads"`
}

var reRelease = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// Valid reports what's wrong with a check read from a file, if anything.
func (c *Check) Valid() error {
	switch {
	case c.Status != Passing && c.Status != Failing:
		return fmt.Errorf("status %q is neither passing nor failing", c.Status)
	case c.Status == Failing && c.Failure == "":
		return errors.New("a failing check says what failed")
	case !reRelease.MatchString(c.Release):
		return fmt.Errorf("release %q isn't a Playkeeper release like 0.4.2", c.Release)
	case c.Status == Passing && c.DoneSeconds <= 0:
		return errors.New("a passing check says how long the server took to be ready")
	}
	if _, err := time.Parse(time.DateOnly, c.Checked); err != nil {
		return fmt.Errorf("checked is a day, YYYY-MM-DD: %w", err)
	}
	for _, a := range c.Addons {
		if a.Name == "" || a.Source == "" || a.Slug == "" || a.Version == "" {
			return fmt.Errorf("add-on %q needs its name, source, slug and version", a.Name)
		}
	}
	return nil
}

// Read reads the checks in dir, one .json file per template id. A missing
// dir is no checks.
func Read(fsys fs.FS, dir string) (map[string]*Check, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]*Check{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]*Check{}
	for _, e := range entries {
		id, ok := cutJSON(e.Name())
		if e.IsDir() || !ok {
			continue
		}
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		c := &Check{}
		d := json.NewDecoder(bytes.NewReader(b))
		d.DisallowUnknownFields()
		if err := d.Decode(c); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if err := c.Valid(); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		out[id] = c
	}
	return out, nil
}

// Write writes a template's check to dir/<id>.json.
func Write(dir, id string, c *Check) error {
	if err := c.Valid(); err != nil {
		return fmt.Errorf("%s: %w", id, err)
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, id+".json"), append(b, '\n'), 0o644)
}

func cutJSON(name string) (string, bool) {
	if len(name) > 5 && name[len(name)-5:] == ".json" {
		return name[:len(name)-5], true
	}
	return "", false
}

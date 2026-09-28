// Package library is the list of server templates each release carries for
// New server › A template: the templates playkeeper.io offers, which agents
// create and start on a real Playkeeper before the site lists them.
//
// The templates live in site/data/templates. library.json is generated from
// them with go generate, and internal/site's tests fail when the two differ.
package library

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/CIYAhq/playkeeper/internal/update"
)

//go:generate go run ../../../cmd/site -root ../../.. -library library.json

//go:embed library.json
var raw []byte

// Template is one template of the list.
type Template struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Art is the pixel scene on its card: a file in the dashboard's
	// assets/pixel-art.
	Art string `json:"art"`
	// Page is its page on playkeeper.io, when it has one.
	Page string `json:"page,omitempty"`
	// OpensFrom is the first release that opens it, when older ones can't.
	OpensFrom string `json:"opensFrom,omitempty"`
	// Checked is the day a server was last created and started from it, and
	// Release the Playkeeper it ran on.
	Checked string `json:"checked,omitempty"`
	Release string `json:"release,omitempty"`
	// File is the template itself, in the format of internal/templates.
	File json.RawMessage `json:"file"`
}

// All is every template in the list, by name.
func All() ([]Template, error) {
	var out []Template
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("the template library: %w", err)
	}
	return out, nil
}

// For is the templates the release version opens; its pre-releases open
// what it does. A development build, whose version isn't a release, gets
// them all.
func For(version string) ([]Template, error) {
	all, err := All()
	if err != nil {
		return nil, err
	}
	v, err := update.ParseVersion(version)
	if err != nil {
		return all, nil
	}
	v.Pre = nil
	out := make([]Template, 0, len(all))
	for _, t := range all {
		if t.OpensFrom != "" {
			from, err := update.ParseVersion(t.OpensFrom)
			if err != nil || v.Compare(from) < 0 {
				continue
			}
		}
		out = append(out, t)
	}
	return out, nil
}

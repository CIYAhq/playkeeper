package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons/firstparty"
)

// project is what a check records from an add-on's source: its licence and
// downloads that day.
type project struct {
	licence   string
	downloads int
}

// sources asks Modrinth and Hangar about projects, once each per run. A
// source that doesn't answer leaves the fields empty rather than failing a
// template that installed. Playkeeper's own plugins are answered from the
// registry the binary carries, with no request: their licence, and no
// downloads, since nothing counts them.
type sources struct {
	hc       *http.Client
	modrinth string
	hangar   string
	seen     map[string]project
}

func newSources() *sources {
	return &sources{hc: &http.Client{Timeout: 30 * time.Second}, modrinth: "https://api.modrinth.com/v2", hangar: "https://hangar.papermc.io/api/v1", seen: map[string]project{}}
}

func (s *sources) project(ctx context.Context, source, id, slug string) project {
	key := source + "/" + id
	if p, ok := s.seen[key]; ok {
		return p
	}
	var p project
	switch source {
	case "modrinth":
		var m struct {
			Downloads int `json:"downloads"`
			License   struct {
				ID string `json:"id"`
			} `json:"license"`
		}
		if s.get(ctx, s.modrinth+"/project/"+url.PathEscape(id), &m) {
			p = project{licence: m.License.ID, downloads: m.Downloads}
		}
	case "hangar":
		var h struct {
			Stats struct {
				Downloads int `json:"downloads"`
			} `json:"stats"`
			Settings struct {
				License struct {
					Type string `json:"type"`
				} `json:"license"`
			} `json:"settings"`
		}
		if s.get(ctx, s.hangar+"/projects/"+url.PathEscape(firstOf(slug, id)), &h) {
			p = project{licence: h.Settings.License.Type, downloads: h.Stats.Downloads}
		}
	case "playkeeper":
		if fp := firstparty.Lookup(id); fp != nil {
			p = project{licence: fp.License}
		}
	}
	s.seen[key] = p
	return p
}

func (s *sources) get(ctx context.Context, u string, out any) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "CIYAhq/playkeeper template-check (https://github.com/CIYAhq/playkeeper)")
	resp, err := s.hc.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(out) == nil
}

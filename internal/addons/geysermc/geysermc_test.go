package geysermc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
)

// startFake serves testdata's real answers, behind the redirect the API
// answers "latest" with.
func startFake(t *testing.T) (*Client, *[]string) {
	t.Helper()
	var paths []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/v2/projects/floodgate/versions/latest/builds/latest":
			http.Redirect(w, r, "/v2/projects/floodgate/versions/2.2.5/builds/141", http.StatusFound)
		case "/v2/projects/floodgate/versions/2.2.5/builds/141":
			b, _ := os.ReadFile("testdata/latest-floodgate.json")
			w.Header().Set("Content-Type", "application/json")
			w.Write(b)
		case "/v2/projects/away/versions/latest/builds/latest":
			http.Redirect(w, r, "https://example.org/somewhere-else", http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"Project not found."}`))
		}
	}))
	t.Cleanup(srv.Close)
	return New(fetch.Options{BaseURL: srv.URL + "/v2", HTTP: srv.Client()}), &paths
}

func TestLatestFollowsTheRedirectToTheBuild(t *testing.T) {
	c, paths := startFake(t)
	b, err := c.Latest(context.Background(), "floodgate")
	if err != nil {
		t.Fatal(err)
	}
	if b.Project != "floodgate" || b.Version != "2.2.5" || b.Build != 141 || b.Name() != "2.2.5-b141" || b.Channel != "default" ||
		!b.Time.Equal(time.Date(2026, 9, 17, 14, 53, 3, 841000000, time.UTC)) || len(b.Changes) != 1 || b.Changes[0].Summary != "Update to 26.3 (#686)" {
		t.Errorf("build %+v", b)
	}
	if d := b.Downloads["spigot"]; d.Name != "floodgate-spigot.jar" || d.SHA256 != "21570aff9ce17d6983928e8552777760e1ede5050026b04c686b0ae112e6fd7e" {
		t.Errorf("spigot download %+v", d)
	}
	if got := strings.Join(*paths, " "); got != "/v2/projects/floodgate/versions/latest/builds/latest /v2/projects/floodgate/versions/2.2.5/builds/141" {
		t.Errorf("asked for %s", got)
	}
	if u := c.FileURL(b, "spigot"); !strings.HasSuffix(u, "/v2/projects/floodgate/versions/2.2.5/builds/141/downloads/spigot") || !strings.HasPrefix(u, "https://") {
		t.Errorf("file URL %s", u)
	}
}

func TestLatestRefusesAnotherHostAndMissingProjects(t *testing.T) {
	c, _ := startFake(t)
	var re *fetch.RedirectError
	if _, err := c.Latest(context.Background(), "away"); !errors.As(err, &re) || re.To != "example.org" {
		t.Errorf("redirect elsewhere: %v", err)
	}
	if _, err := c.Latest(context.Background(), "nope"); !errors.Is(err, fetch.ErrNotFound) {
		t.Errorf("missing project: %v", err)
	}
}

func TestParseLatestLink(t *testing.T) {
	for raw, want := range map[string]string{
		"https://download.geysermc.org/v2/projects/floodgate/versions/latest/builds/latest/downloads/spigot":      "floodgate spigot",
		"https://download.geysermc.org/v2/projects/geyser/versions/latest/builds/latest/downloads/velocity":       "geyser velocity",
		"http://download.geysermc.org/v2/projects/floodgate/versions/latest/builds/latest/downloads/spigot":       "",
		"https://download.geysermc.org.evil/v2/projects/floodgate/versions/latest/builds/latest/downloads/spigot": "",
		"https://user@download.geysermc.org/v2/projects/floodgate/versions/latest/builds/latest/downloads/spigot": "",
		"https://download.geysermc.org/v2/projects/floodgate/versions/2.2.5/builds/141/downloads/spigot":          "",
		"https://download.geysermc.org/v2/projects/floodgate/versions/latest/builds/latest/downloads/spigot?x=1":  "",
		"https://download.geysermc.org/v2/projects/../versions/latest/builds/latest/downloads/spigot":             "",
		"https://download.geysermc.org/v2/projects/Floodgate/versions/latest/builds/latest/downloads/spigot":      "",
		"https://github.com/GeyserMC/Floodgate/releases":                                                          "",
	} {
		p, pl, ok := ParseLatestLink(raw)
		got := ""
		if ok {
			got = p + " " + pl
		}
		if got != want {
			t.Errorf("ParseLatestLink(%s) = %q, want %q", raw, got, want)
		}
	}
}

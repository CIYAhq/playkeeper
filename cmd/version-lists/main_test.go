package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/minecraft/software"
)

// A release made while the other types' upstreams are down still ships the
// lists that were last made, as they were.
func TestTheOtherTypesListsAreKeptWhileTheirUpstreamsAreDown(t *testing.T) {
	built, err := os.ReadFile("../../internal/minecraft/software/builtin/lists.json")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "lists.json")
	if err := os.WriteFile(path, built, 0o644); err != nil {
		t.Fatal(err)
	}
	down := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("no network here") })}
	err = writeSoftware(path, software.Sources{Client: down}, time.Now())
	if err == nil || strings.Count(err.Error(), "kept as it was") != 6 {
		t.Fatalf("every type's list must be kept, and say so: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(built) {
		t.Fatal("the lists changed while their upstreams were down")
	}
}

// When the lists already there can't be read, a type whose upstream fails
// can't be kept, so nothing is written: a file without that type, while the
// error says it was kept, would ship a release without it. With the lists
// readable, the type is kept and the others are refreshed.
func TestAFailedTypeIsNeverDroppedFromTheLists(t *testing.T) {
	built, err := os.ReadFile("../../internal/minecraft/software/builtin/lists.json")
	if err != nil {
		t.Fatal(err)
	}
	lists, err := software.ParseBuiltInLists(built)
	if err != nil {
		t.Fatal(err)
	}
	onlyVanilla := func(_ context.Context, typ string) (software.BuiltInType, error) {
		if typ == software.Vanilla {
			return lists.Types[typ], nil
		}
		return software.BuiltInType{}, errors.New("no network here")
	}
	path := filepath.Join(t.TempDir(), "lists.json")
	if err := os.WriteFile(path, []byte("{not the lists"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = writeLists(path, onlyVanilla)
	if err == nil || !strings.Contains(err.Error(), "can't be read") || strings.Contains(err.Error(), "kept as it was") {
		t.Fatalf("an unreadable file and a failed type write nothing, and say so: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "{not the lists" {
		t.Fatalf("the file was rewritten without the failed types: %.200s", b)
	}

	if err := os.WriteFile(path, built, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeLists(path, onlyVanilla); err == nil || strings.Count(err.Error(), "kept as it was") != 5 {
		t.Fatalf("with readable lists the five failed types are kept: %v", err)
	}
	b, _ := os.ReadFile(path)
	if got, err := software.ParseBuiltInLists(b); err != nil || len(got.Types) != 6 {
		t.Fatalf("every type is still in the lists: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A release made while PaperMC's API is down still ships the last list
// that was made; once it answers, the list is replaced with what it lists.
func TestPapersListIsReplacedOnlyWithWhatPaperMCLists(t *testing.T) {
	down := true
	sum := strings.Repeat("ab", 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case down:
			w.WriteHeader(http.StatusServiceUnavailable)
		case r.URL.Path == "/v3/projects/paper/versions":
			fmt.Fprint(w, `{"versions":[{"version":{"id":"26.2","support":{"status":"SUPPORTED"},"java":{"version":{"minimum":25}}}}]}`)
		case r.URL.Path == "/v3/projects/paper/versions/26.2/builds":
			fmt.Fprintf(w, `[{"id":129,"channel":"STABLE","downloads":{"server:default":{"name":"paper-26.2-129.jar","checksums":{"sha256":%q},"url":"https://fill-data.papermc.io/v1/objects/%s/paper-26.2-129.jar"}}}]`, sum, sum)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	path := filepath.Join(t.TempDir(), "paper.json")
	old := []byte(`{"madeAt":"2026-09-01T00:00:00Z","versions":[{"id":"26.1.2","support":"UNSUPPORTED","java":25,"builds":[{"id":74,"channel":"STABLE","sha256":"` + strings.Repeat("cd", 32) + `"}]}]}` + "\n")
	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatal(err)
	}
	fill := minecraft.Fill{BaseURL: srv.URL, Client: srv.Client()}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := writePaper(path, fill, now); err == nil || !strings.Contains(err.Error(), "kept as it was") {
		t.Fatalf("with PaperMC down the list must be kept, and say so: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(old) {
		t.Fatalf("the list changed while PaperMC was down: %s", b)
	}
	down = false
	if err := writePaper(path, fill, now); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	l, err := minecraft.ParseBuiltInPaper(b)
	if err != nil || !l.MadeAt.Equal(now) || len(l.Versions) != 1 || l.Versions[0].ID != "26.2" || l.Versions[0].Builds[0].SHA256 != sum {
		t.Fatalf("the list must be what PaperMC lists, dated now: %+v %v", l, err)
	}
}

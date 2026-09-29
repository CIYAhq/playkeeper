package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
)

// A saved world's server loads the land around spawn, asking again while
// it's too busy to answer, until the square's corners are loaded; then it's
// stopped and its world comes down through the Files API, from the folder
// server.properties names.
func TestASavedWorldIsLoadedStoppedAndDownloaded(t *testing.T) {
	var commands []string
	busy, unloaded := 1, 2
	stopped := false
	agent := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/servers/s1/command":
			var req api.CommandRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			commands = append(commands, req.Command)
			switch {
			case busy > 0:
				busy--
				http.Error(w, `{"error":"The server did not accept the command: rcon: no reply in time"}`, http.StatusInternalServerError)
			case req.Command == "gamerule spawnChunkRadius 10":
				reply(api.CommandResponse{Output: "Gamerule spawnChunkRadius is now set to: 10\n"})
			case unloaded > 0:
				unloaded--
				reply(api.CommandResponse{Output: "Test failed\n"})
			default:
				reply(api.CommandResponse{Output: "Test passed\n"})
			}
		case "POST /v1/servers/s1/stop":
			stopped = true
			reply(api.Operation{ID: "stop"})
		case "GET /v1/operations/stop":
			reply(api.Operation{Status: "succeeded"})
		case "GET /v1/servers/s1/files/content":
			reply(api.FileContent{Text: "level-name=skyworld\n"})
		case "GET /v1/servers/s1/files/download":
			if !stopped || r.Header.Get("X-Playkeeper-Actor") == "" {
				http.Error(w, `{"error":"stop it first, and say who you are"}`, http.StatusBadRequest)
				return
			}
			switch r.URL.Query().Get("path") {
			case "skyworld/level.dat":
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write([]byte("level"))
			case "skyworld/region":
				w.Header().Set("Content-Type", "application/zip")
				zw := zip.NewWriter(w)
				f, _ := zw.Create("region/r.0.0.mca")
				_, _ = f.Write([]byte("chunks"))
				_ = zw.Close()
			default:
				http.Error(w, `{"error":"no such file"}`, http.StatusNotFound)
			}
		default:
			http.Error(w, `{"error":"no such call"}`, http.StatusNotFound)
		}
	})
	c := &checker{agent: agentclient.Via(through{agent}), actor: "template-check", now: time.Now, poll: time.Millisecond}
	if err := c.loadSpawn(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"gamerule spawnChunkRadius 10", "gamerule spawnChunkRadius 10", "execute if loaded ~129 ~ ~129", "execute if loaded ~129 ~ ~129", "execute if loaded ~129 ~ ~129",
		"execute if loaded ~-129 ~ ~129", "execute if loaded ~129 ~ ~-129", "execute if loaded ~-129 ~ ~-129"}
	if !slices.Equal(commands, want) {
		t.Errorf("commands:\n%q\nwant:\n%q", commands, want)
	}
	dir := filepath.Join(t.TempDir(), "worlds", "sky")
	if err := c.fetchWorld(context.Background(), "s1", dir); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]string{"level.dat": "level", "region/r.0.0.mca": "chunks"} {
		if b, err := os.ReadFile(filepath.Join(dir, file)); err != nil || string(b) != want {
			t.Errorf("%s = %q, %v; want %q", file, b, err, want)
		}
	}
}

// The world is where server.properties says, and in world when it doesn't
// say, or names a folder outside the server's.
func TestLevelNameIn(t *testing.T) {
	for _, c := range []struct{ properties, want string }{
		{"motd=A Minecraft Server\nlevel-name=world\n", "world"},
		{"level-name = skyblock\r\nlevel-seed=\n", "skyblock"},
		{"level-name=\n", "world"},
		{"level-name=../elsewhere\n", "world"},
		{"#level-name=old\n", "world"},
		{"", "world"},
	} {
		if got := levelNameIn(c.properties); got != c.want {
			t.Errorf("levelNameIn(%q) = %q; want %q", c.properties, got, c.want)
		}
	}
}

// A folder's zip unpacks into the folder, and a zip naming a file outside
// it is refused.
func TestUnzipStaysInItsFolder(t *testing.T) {
	zipOf := func(names ...string) *zip.Reader {
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		for _, n := range names {
			w, err := zw.Create(n)
			if err != nil {
				t.Fatal(err)
			}
			w.Write([]byte(n))
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		zr, err := zip.NewReader(bytes.NewReader(b.Bytes()), int64(b.Len()))
		if err != nil {
			t.Fatal(err)
		}
		return zr
	}
	dir := t.TempDir()
	if err := unzip(zipOf("region/", "region/r.0.0.mca", "region/r.-1.0.mca"), dir); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "region", "r.-1.0.mca")); err != nil || string(b) != "region/r.-1.0.mca" {
		t.Errorf("region/r.-1.0.mca = %q, %v", b, err)
	}
	if err := unzip(zipOf("../outside"), dir); err == nil {
		t.Error("a zip with ../outside unpacked")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "outside")); err == nil {
		t.Error("../outside was written")
	}
}

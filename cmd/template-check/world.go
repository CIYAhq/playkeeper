package main

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/templates"
)

// A server the capture bot can't join, like a NeoForge or Forge pack's, is
// captured from the world it saved: site/tools/thumbnails/world.mjs reads
// the chunks around spawn from its region files, leaving out the mods'
// blocks, which Minecraft's textures don't have. A spawn under the ground
// has no view, so a modpack's picture is then made of its logo.

// worldRadius is how many chunks around spawn's a saved world's snapshot
// takes in (world.mjs --radius).
const worldRadius = 10

// firstSpawnChunkRadius is the first Minecraft version with the
// spawnChunkRadius game rule, whose default keeps only 2 chunks around spawn
// loaded. Older servers keep the snapshot's chunks loaded from the start.
const firstSpawnChunkRadius = "1.20.5"

// savedWorld captures a passing template's server from its saved world into
// c.shots/<id>.json.gz, and keeps the world's files in c.shots/worlds/<id>,
// or writes a modpack's logo picture into c.shots/captures.
func (c *checker) savedWorld(ctx context.Context, id, sid string, t *templates.Template) error {
	if minecraft.CompareMinecraft(t.Server.MinecraftVersion, firstSpawnChunkRadius) >= 0 {
		if err := c.loadSpawn(ctx, sid); err != nil {
			return fmt.Errorf("loading the land around spawn: %w", err)
		}
	}
	dir, err := filepath.Abs(filepath.Join(c.shots, "worlds", id))
	if err != nil {
		return err
	}
	if err := c.fetchWorld(ctx, sid, dir); err != nil {
		return err
	}
	out, err := filepath.Abs(filepath.Join(c.shots, id+".json.gz"))
	if err != nil {
		return err
	}
	err = c.tool(ctx, 3*time.Minute, "world.mjs", dir, "--template", id, "--out", out, "--radius", fmt.Sprint(worldRadius))
	var failed *toolFailed
	if !errors.As(err, &failed) || failed.code != noView || t.Modpack == nil {
		return err
	}
	// A modpack whose spawn has no view gets a picture of its logo, which
	// the agent looks up for a CurseForge pack with its release's key.
	captures, err := filepath.Abs(filepath.Join(c.shots, "captures"))
	if err != nil {
		return err
	}
	socket, err := filepath.Abs(c.socket)
	if err != nil {
		return err
	}
	return c.tool(ctx, 2*time.Minute, "pack-art.mjs", captures, id, "--socket", socket)
}

// noView is world.mjs's exit code for a spawn under the ground.
const noView = 3

// fetchWorld stops the server, which saves every chunk it has loaded, and
// downloads its world's level.dat and region files into dir.
func (c *checker) fetchWorld(ctx context.Context, sid, dir string) error {
	var op api.Operation
	if _, err := c.agent.Do(ctx, "POST", "/v1/servers/"+sid+"/stop", nil, map[string]string{"actor": c.actor}, &op); err != nil {
		return fmt.Errorf("stopping it to read its world: %w", err)
	}
	if op.ID != "" {
		if _, err := c.wait(ctx, op.ID, 5*time.Minute); err != nil {
			return fmt.Errorf("stopping it to read its world: %w", err)
		}
	}
	level, err := c.levelName(ctx, sid)
	if err != nil {
		return fmt.Errorf("reading its server.properties: %w", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	for _, p := range []string{level + "/level.dat", level + "/region"} {
		if err := c.download(ctx, sid, p, dir); err != nil {
			return fmt.Errorf("downloading its %s: %w", p, err)
		}
	}
	return nil
}

// loadSpawn has the server keep the chunks around spawn's loaded, which
// generates them, and waits until they are. The console's commands run at
// spawn, and a chunk within the radius less one is only loaded (entity
// ticking) once the chunks around it are generated: the four positions it
// asks about are in such chunks, wherever spawn is in its own, near the
// square's corners, the last the server gets to.
func (c *checker) loadSpawn(ctx context.Context, sid string) error {
	deadline := c.now().Add(10 * time.Minute)
	// A server busy generating answers late, which the agent reports as an
	// error, so a command is sent again until it answers as it should.
	until := func(cmd string, ok func(string) bool) error {
		for {
			out, err := c.command(ctx, sid, cmd)
			if err == nil && ok(out) {
				return nil
			}
			if c.now().After(deadline) {
				if err == nil {
					err = errors.New(strings.TrimSpace(out))
				}
				return fmt.Errorf("%s, after 10 minutes: %w", cmd, err)
			}
			if err := sleep(ctx, c.poll); err != nil {
				return err
			}
		}
	}
	if err := until(fmt.Sprintf("gamerule spawnChunkRadius %d", worldRadius), func(string) bool { return true }); err != nil {
		return err
	}
	d := (worldRadius-1)*16 - 15
	for _, corner := range [][2]int{{d, d}, {-d, d}, {d, -d}, {-d, -d}} {
		passed := func(out string) bool { return strings.HasPrefix(strings.TrimSpace(out), "Test passed") }
		if err := until(fmt.Sprintf("execute if loaded ~%d ~ ~%d", corner[0], corner[1]), passed); err != nil {
			return err
		}
	}
	return nil
}

// command runs a console command on the server and returns what it said.
func (c *checker) command(ctx context.Context, sid, cmd string) (string, error) {
	var out api.CommandResponse
	_, err := c.agent.Do(ctx, "POST", "/v1/servers/"+sid+"/command", nil, api.CommandRequest{Command: cmd, Actor: c.actor}, &out)
	return out.Output, err
}

// levelName is the folder the server keeps its world in: server.properties'
// level-name, or world.
func (c *checker) levelName(ctx context.Context, sid string) (string, error) {
	var content api.FileContent
	if _, err := c.agent.Do(ctx, "GET", "/v1/servers/"+sid+"/files/content", url.Values{"path": {"server.properties"}}, nil, &content); err != nil {
		return "", err
	}
	return levelNameIn(content.Text), nil
}

func levelNameIn(properties string) string {
	for _, line := range strings.Split(properties, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && strings.TrimSpace(k) == "level-name" {
			if v = strings.TrimSpace(v); v != "" && filepath.IsLocal(v) {
				return filepath.ToSlash(v)
			}
		}
	}
	return "world"
}

// download saves one of the server's files or folders into dir, as its Files
// tab downloads them: a file as itself, a folder as a zip of the folder.
func (c *checker) download(ctx context.Context, sid, p, dir string) error {
	resp, err := c.agent.Raw(ctx, "GET", "/v1/servers/"+sid+"/files/download", url.Values{"path": {p}}, nil, map[string]string{"X-Playkeeper-Actor": c.actor}, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return agentclient.DecodeError(resp)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	if resp.Header.Get("Content-Type") != "application/zip" {
		if err := f.Close(); err != nil {
			return err
		}
		return os.Rename(f.Name(), filepath.Join(dir, path.Base(p)))
	}
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(f, size)
	if err != nil {
		return err
	}
	return unzip(zr, dir)
}

// unzip writes a zip's files into dir, refusing a name outside it.
func unzip(zr *zip.Reader, dir string) error {
	for _, zf := range zr.File {
		if !filepath.IsLocal(zf.Name) {
			return fmt.Errorf("the zip has a file outside its folder: %s", zf.Name)
		}
		dst := filepath.Join(dir, filepath.FromSlash(zf.Name))
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := unzipFile(zf, dst); err != nil {
			return err
		}
	}
	return nil
}

func unzipFile(zf *zip.File, dst string) error {
	r, err := zf.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/templates"
)

// -shots: after a template passes, a bot joins its server and saves the
// world around spawn, or around the mode's showpiece, for
// site/tools/thumbnails/render.mjs to draw as the template's thumbnails.
// The bot joins as a player would, so the agent must run its servers in
// offline mode (playkeeper dev with PLAYKEEPER_E2E_OFFLINE_MODE_UNSAFE=1). A
// server the bot can't join is captured from the world it saved (world.go).

// shotVersion is the Minecraft version the capture bot speaks
// (site/tools/thumbnails/version.js). A server on another version gets
// ViaVersion for the capture.
const shotVersion = "26.1"

// shotBot is the capture bot's name on the servers it joins.
const shotBot = "PkShot"

// shotTool is where the capture bot and the renderer live, in the
// repository.
const shotTool = "site/tools/thumbnails"

// cantShoot says why the capture bot can't join servers like this one: the
// template's thumbnails come from its saved world instead (savedWorld).
type cantShoot string

func (e cantShoot) Error() string { return string(e) }

// viaFor is what a server of this type and version needs so the capture bot
// can join: ViaVersion lets a newer client onto an older server (ViaFabric
// on Fabric), and ViaBackwards an older client onto a newer one. Nothing,
// on the bot's own version. A vanilla server joins only on that version,
// and a NeoForge or Forge server never: its mods must be on the client too.
func viaFor(typ, version string) ([]string, error) {
	var via []string
	switch {
	case typ == "paper" || typ == "purpur" || typ == "folia":
		via = []string{"viaversion"}
	case typ == "fabric":
		via = []string{"viafabric"}
	case typ == "vanilla" && version == shotVersion:
	default:
		return nil, cantShoot(fmt.Sprintf("the capture bot speaks Minecraft %s, and can't join %s servers on %s", shotVersion, typ, version))
	}
	if version == shotVersion {
		return nil, nil
	}
	if minecraft.CompareMinecraft(version, shotVersion) > 0 {
		via = append(via, "viabackwards")
	}
	return via, nil
}

// shoot captures a passing template's world into c.shots/<id>.json.gz: it
// adds what the bot needs to join, lets the bot on as an operator, and runs
// the bot. A server the bot can't join is captured from its saved world.
func (c *checker) shoot(ctx context.Context, id, sid string, t *templates.Template) error {
	via, err := viaFor(t.Server.Type, t.Server.MinecraftVersion)
	if errors.As(err, new(cantShoot)) {
		return c.savedWorld(ctx, id, sid, t)
	}
	if err != nil {
		return err
	}
	for _, slug := range via {
		add := c.addAddon
		if t.Server.Type == "fabric" {
			add = func(ctx context.Context, sid, slug string) error {
				return c.uploadMod(ctx, sid, slug, t.Server.MinecraftVersion)
			}
		}
		if err := add(ctx, sid, slug); err != nil {
			return fmt.Errorf("adding %s for the capture bot: %w", slug, err)
		}
	}
	// The bot goes on the whitelist and the operators by its offline-mode
	// UUID while the server is stopped: asked by name, a vanilla or Fabric
	// server looks the name up at Mojang, which knows another player.
	var op api.Operation
	if _, err := c.agent.Do(ctx, "POST", "/v1/servers/"+sid+"/stop", nil, map[string]string{"actor": c.actor}, &op); err != nil {
		return fmt.Errorf("stopping it to let the capture bot on: %w", err)
	}
	if op.ID != "" {
		if _, err := c.wait(ctx, op.ID, 5*time.Minute); err != nil {
			return fmt.Errorf("stopping it to let the capture bot on: %w", err)
		}
	}
	uuid := offlineUUID(shotBot)
	for file, entry := range map[string]map[string]any{
		"whitelist.json": {"uuid": uuid, "name": shotBot},
		"ops.json":       {"uuid": uuid, "name": shotBot, "level": 4, "bypassesPlayerLimit": false},
	} {
		if err := c.addEntry(ctx, sid, file, entry); err != nil {
			return fmt.Errorf("adding the capture bot to %s: %w", file, err)
		}
	}
	if _, err := c.agent.Do(ctx, "POST", "/v1/servers/"+sid+"/start", nil, map[string]string{"actor": c.actor}, nil); err != nil {
		return fmt.Errorf("starting it again: %w", err)
	}
	limit := 5 * time.Minute
	if minecraft.ModLoader(t.Server.Type) {
		limit = 20 * time.Minute
	}
	if err := c.waitOnline(ctx, sid, limit); err != nil {
		return fmt.Errorf("starting it again%s: %w", parenthesized(strings.Join(via, " and ")), err)
	}
	var s api.ServerStatus
	if _, err := c.agent.Do(ctx, "GET", "/v1/servers/"+sid, nil, nil, &s); err != nil {
		return err
	}
	if err := os.MkdirAll(c.shots, 0o755); err != nil {
		return err
	}
	out, err := filepath.Abs(filepath.Join(c.shots, id+".json.gz"))
	if err != nil {
		return err
	}
	return c.tool(ctx, 6*time.Minute, "capture.js", "--port", strconv.Itoa(s.GamePort), "--name", shotBot, "--template", id, "--out", out)
}

// toolFailed is a site/tools/thumbnails script's failure: the last line it
// wrote to stderr, and its exit code.
type toolFailed struct {
	why  string
	code int
}

func (e *toolFailed) Error() string { return e.why }

// tool runs one of site/tools/thumbnails' scripts, with its output on
// stderr.
func (c *checker) tool(ctx context.Context, limit time.Duration, script string, args ...string) error {
	run, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(run, "node", append([]string{script}, args...)...)
	cmd.Dir = filepath.Join(c.root, shotTool)
	var log bytes.Buffer
	cmd.Stdout, cmd.Stderr = os.Stderr, &log
	err := cmd.Run()
	var exit *exec.ExitError
	if err == nil || !errors.As(err, &exit) {
		return err
	}
	os.Stderr.Write(log.Bytes())
	why := lastLine(log.String())
	if why == "" {
		why = fmt.Sprintf("%s: %v", script, err)
	}
	return &toolFailed{why: why, code: exit.ExitCode()}
}

// addAddon installs the newest version of a Modrinth project that runs on
// the server, as its Mods or Plugins tab would.
func (c *checker) addAddon(ctx context.Context, sid, slug string) error {
	var d api.AddonDetails
	if _, err := c.agent.Do(ctx, "GET", "/v1/servers/"+sid+"/addons/project/modrinth/"+url.PathEscape(slug), nil, nil, &d); err != nil {
		return err
	}
	if d.Installed != nil {
		return nil
	}
	if d.Plan == nil || !d.Plan.Ready {
		why := "no version runs on it"
		if d.PlanError != nil {
			why = d.PlanError.Message
		}
		return errors.New(why)
	}
	var op api.Operation
	req := api.AddonInstallRequest{Source: "modrinth", ProjectID: slug, Fingerprint: d.Plan.Fingerprint, Actor: c.actor}
	if _, err := c.agent.Do(ctx, "POST", "/v1/servers/"+sid+"/addons/install", nil, req, &op); err != nil {
		return err
	}
	_, err := c.wait(ctx, op.ID, 5*time.Minute)
	return err
}

// uploadMod puts the newest Fabric build of a Modrinth project for this
// Minecraft version into the server's mods folder, through its Files tab's
// upload, after checking the file against Modrinth's SHA-512. ViaFabric
// publishes only alpha builds, which the Mods tab doesn't offer.
func (c *checker) uploadMod(ctx context.Context, sid, slug, version string) error {
	var versions []struct {
		Files []struct {
			URL      string `json:"url"`
			Filename string `json:"filename"`
			Primary  bool   `json:"primary"`
			Hashes   struct {
				SHA512 string `json:"sha512"`
			} `json:"hashes"`
		} `json:"files"`
	}
	q := url.Values{"loaders": {`["fabric"]`}, "game_versions": {`["` + version + `"]`}}
	if !c.sources.get(ctx, c.sources.modrinth+"/project/"+url.PathEscape(slug)+"/version?"+q.Encode(), &versions) || len(versions) == 0 || len(versions[0].Files) == 0 {
		return fmt.Errorf("Modrinth has no Fabric build of it for Minecraft %s", version)
	}
	f := versions[0].Files[0]
	for _, x := range versions[0].Files {
		if x.Primary {
			f = x
		}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", f.URL, nil)
	if err != nil {
		return err
	}
	resp, err := c.sources.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	jar, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if sum := sha512.Sum512(jar); resp.StatusCode != http.StatusOK || hex.EncodeToString(sum[:]) != f.Hashes.SHA512 {
		return fmt.Errorf("%s didn't download as Modrinth lists it", f.Filename)
	}
	var up api.FileUpload
	if _, err := c.agent.Do(ctx, "POST", "/v1/servers/"+sid+"/files/uploads", nil, api.FileUploadRequest{Folder: "mods", Actor: c.actor}, &up); err != nil {
		return err
	}
	var file api.FileUploadFile
	if _, err := c.agent.Do(ctx, "POST", "/v1/servers/"+sid+"/files/uploads/"+up.ID+"/files", nil, api.FileUploadFileRequest{Name: f.Filename, Size: int64(len(jar)), Actor: c.actor}, &file); err != nil {
		return err
	}
	put, err := c.agent.Raw(ctx, "PUT", "/v1/servers/"+sid+"/files/uploads/"+up.ID+"/files/"+strconv.Itoa(file.Index), url.Values{"offset": {"0"}}, bytes.NewReader(jar), map[string]string{"Content-Type": "application/octet-stream", "X-Playkeeper-Actor": c.actor}, false)
	if err != nil {
		return err
	}
	defer put.Body.Close()
	if put.StatusCode >= 400 {
		return agentclient.DecodeError(put)
	}
	deadline := c.now().Add(time.Minute)
	for {
		if _, err := c.agent.Do(ctx, "GET", "/v1/servers/"+sid+"/files/uploads/"+up.ID, nil, nil, &up); err != nil {
			return err
		}
		for _, x := range up.Files {
			if x.Index == file.Index && x.Error != "" {
				return errors.New(x.Error)
			}
			if x.Index == file.Index && x.Placed {
				return nil
			}
		}
		if c.now().After(deadline) {
			return fmt.Errorf("%s wasn't put in the mods folder after a minute", f.Filename)
		}
		if err := sleep(ctx, c.poll); err != nil {
			return err
		}
	}
}

// offlineUUID is the UUID an offline-mode server gives a player of this
// name: Java's UUID.nameUUIDFromBytes of "OfflinePlayer:" and the name.
func offlineUUID(name string) string {
	b := md5.Sum([]byte("OfflinePlayer:" + name))
	b[6] = b[6]&0x0f | 0x30
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// addEntry adds an entry to a JSON list in the server's folder, such as
// whitelist.json, through its Files tab.
func (c *checker) addEntry(ctx context.Context, sid, file string, entry map[string]any) error {
	var list []map[string]any
	var content api.FileContent
	status, err := c.agent.Do(ctx, "GET", "/v1/servers/"+sid+"/files/content", url.Values{"path": {file}}, nil, &content)
	switch {
	case status == http.StatusNotFound:
	case err != nil:
		return err
	case strings.TrimSpace(content.Text) != "":
		if err := json.Unmarshal([]byte(content.Text), &list); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(append(list, entry), "", "  ")
	if err != nil {
		return err
	}
	resp, err := c.agent.Raw(ctx, "PUT", "/v1/servers/"+sid+"/files/content", url.Values{"path": {file}}, bytes.NewReader(b), map[string]string{"Content-Type": "text/plain", "X-Playkeeper-Actor": c.actor}, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return agentclient.DecodeError(resp)
	}
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

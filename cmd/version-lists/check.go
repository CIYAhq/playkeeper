package main

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/minecraft/software"
)

// checker asks every upstream Playkeeper reads, as the Live upstreams
// workflow does: each list loads as a machine loads it, the files of each
// type's recommended version download and match their hashes, the add-on and
// modpack libraries answer, and the lists built into this build are compared
// with what the upstreams list now. It writes nothing but its report.
type checker struct {
	out      io.Writer
	fill     minecraft.Fill
	sources  software.Sources
	hc       *http.Client
	dir      string
	now      time.Time
	actions  bool   // write GitHub Actions annotations
	modrinth string // Modrinth's API
	hangar   string // Hangar's API
	cf       string // CurseForge's API, asked only with cfKey
	cfKey    string
	failed   []string
}

func newChecker(out io.Writer, fill minecraft.Fill, s software.Sources, hc *http.Client, dir string, now time.Time) *checker {
	return &checker{out: out, fill: fill, sources: s, hc: hc, dir: dir, now: now, actions: os.Getenv("GITHUB_ACTIONS") == "true",
		modrinth: "https://api.modrinth.com/v2", hangar: "https://hangar.papermc.io/api/v1", cf: "https://api.curseforge.com", cfKey: os.Getenv("CURSEFORGE_API_KEY")}
}

func (c *checker) ok(name, msg string) { fmt.Fprintf(c.out, "ok    %s: %s\n", name, msg) }

func (c *checker) fail(name string, err error) {
	fmt.Fprintf(c.out, "FAIL  %s: %v\n", name, err)
	if c.actions {
		fmt.Fprintf(c.out, "::error title=%s::%v\n", name, err)
	}
	c.failed = append(c.failed, name)
}

// behind notes a built-in list that offers another version than the
// upstream does now, which the next make version-lists catches up.
func (c *checker) behind(name, msg string) {
	fmt.Fprintf(c.out, "note  %s: %s\n", name, msg)
	if c.actions {
		fmt.Fprintf(c.out, "::warning title=%s::%s\n", name, msg)
	}
}

func (c *checker) run() []string {
	c.paper()
	for _, typ := range []string{software.Vanilla, software.Purpur, software.Fabric, software.Quilt, software.NeoForge, software.Forge} {
		c.software(typ)
	}
	c.libraries()
	return c.failed
}

func (c *checker) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Minute)
}

func (c *checker) paper() {
	ctx, cancel := c.ctx()
	defer cancel()
	l, err := c.fill.BuiltInList(ctx, c.now)
	if err != nil {
		c.fail("Paper", err)
		return
	}
	live, _, err := l.Catalog()
	if err != nil {
		c.fail("Paper", err)
		return
	}
	rec := recommendedEntry(live)
	u, _ := minecraft.PaperJarURL(rec.MinecraftVersion, rec.PaperBuild, rec.JarSHA256)
	if err := c.download(ctx, u, sha256.New(), rec.JarSHA256); err != nil {
		c.fail("Paper", err)
		return
	}
	c.ok("Paper", fmt.Sprintf("%d versions; %s build %d downloads from %s and matches its SHA-256", len(l.Versions), rec.MinecraftVersion, rec.PaperBuild, minecraft.PaperDownloads))
	if built, at, err := minecraft.BuiltInCatalog(); err != nil {
		c.fail("Paper's built-in list", err)
	} else if b := recommendedEntry(built); b.MinecraftVersion != rec.MinecraftVersion || b.PaperBuild != rec.PaperBuild {
		c.behind("Paper's built-in list", fmt.Sprintf("made %s, it recommends %s build %d; PaperMC's now recommends %s build %d", at.Format(time.RFC3339), b.MinecraftVersion, b.PaperBuild, rec.MinecraftVersion, rec.PaperBuild))
	}
}

func recommendedEntry(entries []api.CatalogEntry) api.CatalogEntry {
	if i := slices.IndexFunc(entries, func(e api.CatalogEntry) bool { return e.Recommended }); i >= 0 {
		return entries[i]
	}
	return api.CatalogEntry{}
}

func (c *checker) software(typ string) {
	ctx, cancel := c.ctx()
	defer cancel()
	name := typ
	t, err := c.sources.RecordBuiltIn(ctx, typ, c.now)
	if err != nil {
		c.fail(name, err)
		return
	}
	i := slices.IndexFunc(t.Releases, func(r software.Release) bool { return r.Recommended })
	res := t.Resolved[slices.IndexFunc(t.Resolved, func(r software.Resolved) bool { return r.Pin == t.Releases[i].Pin })]
	p, err := res.Plan()
	if err != nil {
		c.fail(name, err)
		return
	}
	dir := filepath.Join(c.dir, typ)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.fail(name, err)
		return
	}
	for _, a := range p.Downloads {
		if err := software.Download(ctx, c.hc, dir, a); err != nil {
			c.fail(name, err)
			return
		}
	}
	c.ok(name, fmt.Sprintf("%d versions; %s's %d files download and match their hashes", len(t.Releases), t.Releases[i].Label, len(p.Downloads)))
	built, at, err := software.Sources{}.BuiltInCatalog(typ)
	if err != nil {
		c.fail(name+"'s built-in list", err)
		return
	}
	if j := slices.IndexFunc(built, func(r software.Release) bool { return r.Recommended }); j < 0 || built[j].Pin != t.Releases[i].Pin {
		c.behind(name+"'s built-in list", fmt.Sprintf("made %s, it doesn't recommend what the upstream now does, %s", at.Format(time.RFC3339), t.Releases[i].Label))
	}
}

// libraries asks the add-on and modpack libraries, which no server's
// creation depends on: Modrinth's API and a file from its CDN, Hangar's
// API, and CurseForge's with a key.
func (c *checker) libraries() {
	ctx, cancel := c.ctx()
	defer cancel()
	var versions []struct {
		Files []struct {
			URL    string `json:"url"`
			Hashes struct {
				SHA512 string `json:"sha512"`
			} `json:"hashes"`
		} `json:"files"`
	}
	err := c.getJSON(ctx, c.modrinth+"/project/luckperms/version", nil, &versions)
	if err == nil && (len(versions) == 0 || len(versions[0].Files) == 0) {
		err = fmt.Errorf("it lists no LuckPerms file")
	}
	if err == nil {
		f := versions[0].Files[0]
		err = c.download(ctx, f.URL, sha512.New(), f.Hashes.SHA512)
	}
	if err != nil {
		c.fail("Modrinth", err)
	} else {
		c.ok("Modrinth", "LuckPerms' versions load, and its newest file downloads and matches its SHA-512")
	}
	var project struct {
		Name string `json:"name"`
	}
	if err := c.getJSON(ctx, c.hangar+"/projects/GeyserMC/Floodgate", nil, &project); err != nil || project.Name == "" {
		c.fail("Hangar", fmt.Errorf("Floodgate's project didn't load: %v", err))
	} else {
		c.ok("Hangar", "Floodgate's project loads")
	}
	if c.cfKey == "" {
		fmt.Fprintln(c.out, "skip  CurseForge: no CURSEFORGE_API_KEY")
		return
	}
	var game struct {
		Data struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, c.cf+"/v1/games/432", map[string]string{"x-api-key": c.cfKey}, &game); err != nil || game.Data.Name == "" {
		c.fail("CurseForge", fmt.Errorf("Minecraft's game didn't load: %v", err))
	} else {
		c.ok("CurseForge", "Minecraft's game loads with the key")
	}
}

func (c *checker) get(ctx context.Context, raw string, header map[string]string) (*http.Response, error) {
	if u, err := url.Parse(raw); err != nil || u.Scheme != "https" {
		return nil, fmt.Errorf("%q is not an HTTPS address", raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "CIYAhq/playkeeper live upstreams check (https://github.com/CIYAhq/playkeeper)")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s answered HTTP %d", req.URL.Host, resp.StatusCode)
	}
	return resp, nil
}

func (c *checker) getJSON(ctx context.Context, raw string, header map[string]string, v any) error {
	resp, err := c.get(ctx, raw, header)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}

// download reads raw to its end and checks it against want.
func (c *checker) download(ctx context.Context, raw string, h hash.Hash, want string) error {
	resp, err := c.get(ctx, raw, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if _, err := io.Copy(h, io.LimitReader(resp.Body, 512<<20)); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%s doesn't match its checksum (got %.16s, want %.16s)", raw, got, want)
	}
	return nil
}

package panel

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/mojang"
)

// Player faces. The panel looks a player up at Mojang, downloads their skin,
// crops the face and caches it, so the browser only loads images from the
// panel and viewers' addresses never reach a third party. A player without a
// skin of their own gets no face (the dashboard shows initials), because
// Mojang's default skins are Mojang art.

const (
	headOK      = "ok"
	headDefault = "default"
	headUnknown = "unknown"
	headFailed  = "failed"

	maxProfileBytes = 64 << 10
	maxSkinBytes    = 256 << 10
)

// headTTL is how long each kind of answer is reused.
var headTTL = map[string]time.Duration{
	headOK: 12 * time.Hour, headDefault: 6 * time.Hour, headUnknown: 6 * time.Hour, headFailed: 10 * time.Minute,
}

// defaultSkins are the texture hashes of Mojang's default skins: Steve and
// Alex before 2022, and the nine default skins since, in both models.
var defaultSkins = map[string]bool{
	"1a4af718455d4aab528e7a61f86fa25e6a369d1768dcb13f7df319a713eb810b": true,
	"83cee5ca6afcdb171285aa00e8049c297b2dbeba0efb8ff970a5677a1b644032": true,
	"fac8a4da65fd31e8f8ced871f99e9a912ef311aad6670ad493ab0cd17d079655": true,
	"3b01ae593edad844e4c05ca6656694d0876b7d1d88db54fcc51291c9931035a2": true,
	"5c500205248f3af53ea628f862ebf756fe8e7c9ec8afa4bd963fed1497f46ee1": true,
	"60ffae942203730323a3edd8256383872dd9db35db5c22df922785b2c90c6720": true,
	"ccd65620b04f968664ee9de85616cd2bce5eeb751de0f52e640bc31fe45b2ef7": true,
	"ab9ccefab11b79a2a7af49087fb45a4943a2d13dc357326bfe7b9f919e7f14a2": true,
	"9e2d846bdcb21bfc5632e0af525f12579f54f5204d81b9ea4ce6b83e0f05cc23": true,
	"6faa242d95972b82e8160696e52d26875499161d0a62d597e4b752747ab4e41e": true,
	"d6476070a9bf9ca6bf80e72e44637a26a9dde5682d90c484d72100b2113e9e40": true,
	"dacc31f64251853e22f109563b89086233e7c059b515b39bc6af6ac5a57a52dc": true,
	"26c79ece805634f3557bf468dda5a6c0a9213edf39ac31a7057d9510cd98b639": true,
	"fd8937b15c55e715af05fe53a39516c76b36c04d59068f7793069a6a64b959e7": true,
	"c7dfec078861fd074de1b855b70c22b14afaa4891480038d91f7193a5d3f763e": true,
	"f765c92b4b5d24c1c15018b812b1635c236298e7bd419e7015eec1f089ca42c4": true,
	"6ed02cee51900ffe710580584ac9377d6f62eae181cd1ce1aa3bf9c7a291d3ef": true,
	"8a672522c9fe792b3155b57081d61e89f28253d61dcb59121ab6925d7a8a2c81": true,
	"a132b170fe633ca499edcab1053e59e39271eb4e043f951cdcfbdfd82d408d09": true,
	"eb706768fc86a7fc2b2bb17f3d8a424e94e4b434b5a4654a6138cfd6ffb11fb":  true,
}

var reUUID = regexp.MustCompile(`^[0-9a-f]{8}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{12}$`)

// HeadSources are where skins come from; tests point them at local servers.
// Names are looked up with the panel's Mojang client (Options.Mojang).
type HeadSources struct {
	SessionURL  string // UUID → textures
	TexturesURL string // skin files; only this host is fetched
	Client      *http.Client
}

func defaultHeadSources() HeadSources {
	return HeadSources{
		SessionURL: "https://sessionserver.mojang.com", TexturesURL: "https://textures.minecraft.net",
		Client: &http.Client{Timeout: 8 * time.Second},
	}
}

type headFetcher struct {
	src      HeadSources
	mojang   *mojang.Client
	sem      chan struct{}
	mu       sync.Mutex
	inflight map[string]*headCall
}

type headCall struct {
	done   chan struct{}
	status string
	png    []byte
}

func newHeadFetcher(src HeadSources, mc *mojang.Client) *headFetcher {
	return &headFetcher{src: src, mojang: mc, sem: make(chan struct{}, 2), inflight: map[string]*headCall{}}
}

func (s *Server) hHead(w http.ResponseWriter, r *http.Request, _ *session) {
	name := r.PathValue("name")
	if !minecraft.ValidPlayerName(name) {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Minecraft usernames are 3–16 letters, numbers or underscores.", "")
		return
	}
	uuid := strings.ToLower(r.URL.Query().Get("uuid"))
	if uuid != "" && !reUUID.MatchString(uuid) {
		uuid = ""
	}
	status, img := s.head(r.Context(), name, strings.ReplaceAll(uuid, "-", ""))
	if status != headOK {
		w.Header().Set("Cache-Control", "private, max-age=600")
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "No face for this player.", "")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write(img)
}

// head returns a player's cached face, fetching it when the cache is stale.
// Minecraft names are case-insensitive, so the cache is keyed in lower case.
func (s *Server) head(ctx context.Context, name, uuid string) (string, []byte) {
	key := strings.ToLower(name)
	var status string
	var img []byte
	var fetched int64
	err := s.db.QueryRow(`SELECT status, png, fetched_at FROM player_heads WHERE name = ?`, key).Scan(&status, &img, &fetched)
	if err == nil && s.now().Sub(time.UnixMilli(fetched)) < headTTL[status] {
		return status, img
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return headFailed, nil
	}
	f := s.heads
	f.mu.Lock()
	call, ok := f.inflight[key]
	if !ok {
		call = &headCall{done: make(chan struct{})}
		f.inflight[key] = call
		go func() {
			call.status, call.png = f.fetch(name, uuid)
			if _, err := s.db.Exec(`INSERT INTO player_heads(name, status, png, fetched_at) VALUES(?,?,?,?)
				ON CONFLICT(name) DO UPDATE SET status = excluded.status, png = excluded.png, fetched_at = excluded.fetched_at`,
				key, call.status, call.png, s.now().UnixMilli()); err != nil {
				s.log.Warn("could not cache a player's face", "err", err)
			}
			f.mu.Lock()
			delete(f.inflight, key)
			f.mu.Unlock()
			close(call.done)
		}()
	}
	f.mu.Unlock()
	select {
	case <-call.done:
		return call.status, call.png
	case <-ctx.Done():
		return headFailed, nil
	}
}

// fetch looks a player up and crops their face.
func (f *headFetcher) fetch(name, uuid string) (string, []byte) {
	f.sem <- struct{}{}
	defer func() { <-f.sem }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	given := uuid != ""
	if !given {
		p, err := f.mojang.Lookup(ctx, name)
		switch {
		case errors.Is(err, mojang.ErrNotFound) || errors.Is(err, mojang.ErrInvalidName) || p.Demo:
			return headUnknown, nil
		case err != nil:
			return headFailed, nil
		}
		uuid = strings.ReplaceAll(strings.ToLower(p.ID), "-", "")
	}
	if !reUUID.MatchString(uuid) {
		return headUnknown, nil
	}
	var profile struct {
		Name       string `json:"name"`
		Properties []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"properties"`
	}
	switch status, err := f.getJSON(ctx, f.src.SessionURL+"/session/minecraft/profile/"+uuid, &profile); {
	case err != nil:
		return headFailed, nil
	case status == http.StatusNotFound || status == http.StatusNoContent:
		return headUnknown, nil
	}
	// The face is cached under the name, so a UUID from the request only
	// counts when it is that player's.
	if given && !strings.EqualFold(profile.Name, name) {
		return headUnknown, nil
	}
	skinURL := ""
	for _, p := range profile.Properties {
		if p.Name != "textures" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(p.Value)
		if err != nil {
			return headFailed, nil
		}
		var tex struct {
			Textures struct {
				Skin *struct {
					URL string `json:"url"`
				} `json:"SKIN"`
			} `json:"textures"`
		}
		if json.Unmarshal(raw, &tex) != nil {
			return headFailed, nil
		}
		if tex.Textures.Skin != nil {
			skinURL = tex.Textures.Skin.URL
		}
	}
	u, err := url.Parse(skinURL)
	if skinURL == "" || err != nil {
		return headDefault, nil
	}
	allowed, _ := url.Parse(f.src.TexturesURL)
	hash := path.Base(u.Path)
	if u.Host != allowed.Host || !strings.HasPrefix(u.Path, "/texture/") {
		return headFailed, nil
	}
	if defaultSkins[hash] {
		return headDefault, nil
	}
	img, err := f.get(ctx, allowed.Scheme+"://"+allowed.Host+"/texture/"+url.PathEscape(hash), maxSkinBytes)
	if err != nil {
		return headFailed, nil
	}
	face, err := cropFace(img)
	if err != nil {
		return headFailed, nil
	}
	return headOK, face
}

func (f *headFetcher) get(ctx context.Context, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.src.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("too large")
	}
	return b, nil
}

func (f *headFetcher) getJSON(ctx context.Context, u string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := f.src.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNoContent {
		return resp.StatusCode, nil
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxProfileBytes+1))
	if err != nil {
		return 0, err
	}
	if len(b) > maxProfileBytes {
		return 0, errors.New("too large")
	}
	return resp.StatusCode, json.Unmarshal(b, out)
}

// cropFace cuts the 8×8 face out of a 64×64 or 64×32 skin and lays the hat
// layer over it.
func cropFace(skin []byte) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(skin))
	if err != nil || format != "png" || cfg.Width != 64 || (cfg.Height != 64 && cfg.Height != 32) {
		return nil, errors.New("not a Minecraft skin")
	}
	src, err := png.Decode(bytes.NewReader(skin))
	if err != nil {
		return nil, err
	}
	face := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	draw.Draw(face, face.Bounds(), src, image.Pt(8, 8), draw.Src)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			c := color.NRGBAModel.Convert(face.At(x, y)).(color.NRGBA)
			c.A = 255
			face.SetNRGBA(x, y, c)
		}
	}
	draw.Draw(face, face.Bounds(), src, image.Pt(40, 8), draw.Over)
	var out bytes.Buffer
	if err := png.Encode(&out, face); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

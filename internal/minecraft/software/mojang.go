package software

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

const mojangManifestURL = "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json"

// mojangEntry is one version in Mojang's version manifest. SHA1 pins the
// version's own file, which holds the server jar's SHA-1.
type mojangEntry struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	URL         string `json:"url"`
	SHA1        string `json:"sha1"`
	ReleaseTime string `json:"releaseTime"`
}

type mojangVersion struct {
	ID     string
	Java   int
	Server Artifact
}

// mojangManifest lists Mojang's versions by ID.
func mojangManifest(ctx context.Context, hc *http.Client) (map[string]mojangEntry, error) {
	u := mojangUpstream(hc)
	const what = "its version manifest"
	var m struct {
		Versions []mojangEntry `json:"versions"`
	}
	if err := u.getJSON(ctx, mojangManifestURL, what, &m); err != nil {
		return nil, err
	}
	out := make(map[string]mojangEntry, len(m.Versions))
	for _, e := range m.Versions {
		if _, dup := out[e.ID]; e.ID != "" && !dup {
			out[e.ID] = e
		}
	}
	if len(out) == 0 {
		return nil, u.malformed(what, errors.New("it lists no versions"))
	}
	return out, nil
}

// ReleaseDates reads when Mojang released each Minecraft release, from its
// version manifest. The dates are the same whatever software a server runs,
// Paper's included.
func (s Sources) ReleaseDates(ctx context.Context) (map[string]time.Time, error) {
	man, err := mojangManifest(ctx, s.Client)
	if err != nil {
		return nil, err
	}
	out := make(map[string]time.Time, len(man))
	for id, e := range man {
		if t, err := time.Parse(time.RFC3339, e.ReleaseTime); err == nil && e.Type == "release" {
			out[id] = t.UTC()
		}
	}
	return out, nil
}

// mojangVersionFor reads one version's file, refusing it unless it matches
// the SHA-1 the manifest lists, and returns the server jar it names.
func mojangVersionFor(ctx context.Context, hc *http.Client, e mojangEntry) (mojangVersion, error) {
	u := mojangUpstream(hc)
	what := "the version file of Minecraft " + e.ID
	want, ok := parseHash(SHA1, e.SHA1)
	if !ok {
		return mojangVersion{}, u.malformed(what, errors.New("the manifest lists no valid SHA-1 for it"))
	}
	b, err := u.read(ctx, e.URL, what, maxMetadata)
	if err != nil {
		return mojangVersion{}, err
	}
	sum := sha1.Sum(b)
	if got := hex.EncodeToString(sum[:]); got != want.Value {
		return mojangVersion{}, &Error{Kind: KindHashMismatch,
			Msg: fmt.Sprintf("Mojang's version file for Minecraft %s does not match the SHA-1 in Mojang's version manifest (got %s, want %s), so Playkeeper did not use it.",
				e.ID, short(got), short(want.Value)),
			Hint:   "This can mean a corrupted download or a tampered mirror. Try again.",
			Params: u.params("", "minecraftVersion", e.ID, "algorithm", string(SHA1), "got", got, "want", want.Value)}
	}
	var v struct {
		ID          string `json:"id"`
		JavaVersion struct {
			MajorVersion int `json:"majorVersion"`
		} `json:"javaVersion"`
		Downloads struct {
			Server struct {
				SHA1 string `json:"sha1"`
				Size int64  `json:"size"`
				URL  string `json:"url"`
			} `json:"server"`
		} `json:"downloads"`
	}
	if err := u.decode(b, what, &v); err != nil {
		return mojangVersion{}, err
	}
	switch {
	case v.ID != e.ID:
		return mojangVersion{}, u.malformed(what, fmt.Errorf("it describes version %q", v.ID))
	case v.JavaVersion.MajorVersion <= 0:
		return mojangVersion{}, u.malformed(what, errors.New("it names no Java version"))
	}
	s := v.Downloads.Server
	h, ok := parseHash(SHA1, s.SHA1)
	if !ok || s.Size <= 0 || s.Size > maxArtifact {
		return mojangVersion{}, u.malformed(what, errors.New("it lists no server jar with a SHA-1 and size"))
	}
	if _, err := u.checkURL(s.URL, "the server jar of Minecraft "+e.ID); err != nil {
		return mojangVersion{}, err
	}
	return mojangVersion{ID: v.ID, Java: v.JavaVersion.MajorVersion, Server: Artifact{
		URL: s.URL, Size: s.Size, Hash: h, Source: "Mojang's version manifest", Upstream: u.name, Hosts: u.hosts,
	}}, nil
}

// mojangRelease looks up one Minecraft release and checks that the image's
// Java can run it.
func mojangRelease(ctx context.Context, hc *http.Client, mc string) (mojangVersion, error) {
	man, err := mojangManifest(ctx, hc)
	if err != nil {
		return mojangVersion{}, err
	}
	e, ok := man[mc]
	if !ok || e.Type != "release" {
		return mojangVersion{}, &Error{Kind: KindNotFound, Msg: fmt.Sprintf("Mojang does not list Minecraft %s as a release.", mc),
			Hint: "Choose another version.", Params: map[string]string{"upstream": "Mojang", "minecraftVersion": mc}}
	}
	v, err := mojangVersionFor(ctx, hc, e)
	if err != nil {
		return mojangVersion{}, err
	}
	if v.Java > minecraft.ImageJava {
		return mojangVersion{}, &Error{Kind: KindUnsupported,
			Msg:    fmt.Sprintf("Minecraft %s needs Java %d; this Playkeeper runs Java %d.", mc, v.Java, minecraft.ImageJava),
			Hint:   "Choose an older version, or update Playkeeper.",
			Params: map[string]string{"minecraftVersion": mc, "java": strconv.Itoa(v.Java), "imageJava": strconv.Itoa(minecraft.ImageJava)}}
	}
	return v, nil
}

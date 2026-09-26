package software

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
)

const purpurAPI = "https://api.purpurmc.org/v2/purpur"

func purpurUpstream(hc *http.Client) upstream {
	return upstream{name: "Purpur", hosts: []string{"api.purpurmc.org"}, hc: hc}
}

type purpurBuild struct {
	Version  string `json:"version"`
	Build    string `json:"build"`
	Result   string `json:"result"`
	Metadata struct {
		Type string `json:"type"`
	} `json:"metadata"`
	MD5 string `json:"md5"`
}

func (b purpurBuild) number() int {
	n, err := strconv.Atoi(b.Build)
	if err != nil || n <= 0 || strconv.Itoa(n) != b.Build {
		return 0
	}
	return n
}

func (b purpurBuild) channel() Channel {
	if b.Metadata.Type == "experimental" {
		return Experimental
	}
	return Stable
}

// purpurBuilds lists a version's finished builds that have a checksum,
// newest first.
func purpurBuilds(ctx context.Context, hc *http.Client, mc string) ([]purpurBuild, error) {
	u := purpurUpstream(hc)
	what := "its build list for Minecraft " + mc
	var v struct {
		Version string `json:"version"`
		Builds  struct {
			All []purpurBuild `json:"all"`
		} `json:"builds"`
	}
	if err := u.getJSON(ctx, purpurAPI+"/"+mc+"?detailed=true", what, &v); err != nil {
		return nil, err
	}
	if v.Version != mc {
		return nil, u.malformed(what, fmt.Errorf("it is for version %q", v.Version))
	}
	var out []purpurBuild
	for _, b := range v.Builds.All {
		if _, ok := parseHash(MD5, b.MD5); ok && b.number() > 0 && b.Result == "SUCCESS" &&
			!slices.ContainsFunc(out, func(o purpurBuild) bool { return o.Build == b.Build }) {
			out = append(out, b)
		}
	}
	slices.SortFunc(out, func(a, b purpurBuild) int { return cmp.Compare(b.number(), a.number()) })
	return out, nil
}

func purpurCatalog(ctx context.Context, hc *http.Client) ([]Release, error) {
	man, err := mojangManifest(ctx, hc)
	if err != nil {
		return nil, err
	}
	var project struct {
		Versions []string `json:"versions"`
	}
	if err := purpurUpstream(hc).getJSON(ctx, purpurAPI, "its version list", &project); err != nil {
		return nil, err
	}
	return offer(ctx, hc, man, Purpur, project.Versions, func(mc string) (choice, bool, error) {
		bs, err := purpurBuilds(ctx, hc, mc)
		if isNotFound(err) {
			return choice{}, false, nil
		}
		if err != nil {
			return choice{}, false, err
		}
		if len(bs) == 0 {
			return choice{}, false, nil
		}
		pick := bs[0]
		if i := slices.IndexFunc(bs, func(b purpurBuild) bool { return b.channel() == Stable }); i >= 0 {
			pick = bs[i]
		}
		return choice{pin: Pin{Type: Purpur, MinecraftVersion: mc, PurpurBuild: pick.number()}, channel: pick.channel()}, true, nil
	})
}

func purpurBuildList(ctx context.Context, hc *http.Client, mc string) ([]Build, error) {
	bs, err := purpurBuilds(ctx, hc, mc)
	if err != nil {
		return nil, err
	}
	if len(bs) == 0 {
		return nil, noBuilds(Purpur, mc)
	}
	versions := make([]string, len(bs))
	channels := map[string]Channel{}
	for i, b := range bs {
		versions[i], channels[b.Build] = b.Build, b.channel()
	}
	return builds(Pin{Type: Purpur, MinecraftVersion: mc}, versions,
		func(v string) Channel { return channels[v] },
		func(p *Pin, v string) { p.PurpurBuild, _ = strconv.Atoi(v) }), nil
}

func purpurResolve(ctx context.Context, hc *http.Client, r *Resolved) error {
	u := purpurUpstream(hc)
	mc, n := r.Pin.MinecraftVersion, strconv.Itoa(r.Pin.PurpurBuild)
	what := "build " + n + " for Minecraft " + mc
	var b purpurBuild
	if err := u.getJSON(ctx, purpurAPI+"/"+mc+"/"+n, what, &b); err != nil {
		return err
	}
	if b.Version != mc || b.Build != n {
		return u.malformed(what, fmt.Errorf("it describes build %q for Minecraft %q", b.Build, b.Version))
	}
	if b.Result != "SUCCESS" {
		return &Error{Kind: KindNotFound, Msg: fmt.Sprintf("Purpur build %s for Minecraft %s did not finish, so it has no jar to download.", n, mc),
			Hint: "Choose another build.", Params: u.params("", "minecraftVersion", mc, "build", n, "result", b.Result)}
	}
	h, ok := parseHash(MD5, b.MD5)
	if !ok {
		return u.malformed(what, errors.New("it has no valid MD5 checksum"))
	}
	r.Software = &Artifact{URL: purpurAPI + "/" + mc + "/" + n + "/download", Path: "purpur-" + mc + "-" + n + ".jar",
		Hash: h, Source: "Purpur's build API", Upstream: u.name, Hosts: u.hosts}
	return nil
}

// purpurPlan puts Mojang's server jar where Purpur's Paperclip launcher
// looks for it, so Paperclip never downloads anything. At every start
// Paperclip patches it and unpacks the libraries, checking each result
// against the SHA-256 list inside the Purpur jar.
func purpurPlan(r Resolved) (Plan, error) {
	if r.Software == nil {
		return Plan{}, incomplete(r.Pin)
	}
	jar, server := *r.Software, r.Server
	server.Path = "cache/mojang_" + r.Pin.MinecraftVersion + ".jar"
	return Plan{
		Downloads: []Artifact{jar, server},
		Derive:    []Derivation{{Kind: DerivePaperclip, From: jar.Path, Into: "cache"}},
		Run:       Container{Env: []string{"TYPE=CUSTOM", "CUSTOM_SERVER=/data/" + jar.Path}},
	}, nil
}

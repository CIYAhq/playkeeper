package software

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
)

const quiltMeta = "https://meta.quiltmc.org/v3"

var reQuiltLoader = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-(beta|pre|rc)\.[0-9]+)?$`)

func quiltUpstream(hc *http.Client) upstream {
	return upstream{name: "Quilt", hosts: []string{"meta.quiltmc.org"}, hc: hc}
}

// Quilt's profiles use libraries from Fabric's Maven repository too.
func quiltMaven(hc *http.Client) upstream {
	return upstream{name: "Quilt", hosts: []string{"maven.quiltmc.org", "maven.fabricmc.net"}, hc: hc}
}

func quiltChannel(v string) Channel {
	if strings.Contains(v, "-") {
		return Beta
	}
	return Stable
}

func quiltLoaders(ctx context.Context, hc *http.Client) ([]string, error) {
	u := quiltUpstream(hc)
	loaders, err := metaLoaders(ctx, u, quiltMeta+"/versions/loader", reQuiltLoader.MatchString)
	if err != nil {
		return nil, err
	}
	if len(loaders) == 0 {
		return nil, &Error{Kind: KindNoVersions, Msg: "Quilt lists no loader version Playkeeper can run.",
			Hint: "Try again later, or choose another server type.", Params: u.params("", "type", Quilt)}
	}
	return loaders, nil
}

func quiltCatalog(ctx context.Context, hc *http.Client) ([]Release, error) {
	man, err := mojangManifest(ctx, hc)
	if err != nil {
		return nil, err
	}
	games, err := metaGames(ctx, quiltUpstream(hc), quiltMeta+"/versions/game")
	if err != nil {
		return nil, err
	}
	loaders, err := quiltLoaders(ctx, hc)
	if err != nil {
		return nil, err
	}
	loader := loaders[0]
	for _, v := range loaders {
		if quiltChannel(v) == Stable {
			loader = v
			break
		}
	}
	return offer(ctx, hc, man, Quilt, games, func(mc string) (choice, bool, error) {
		return choice{pin: Pin{Type: Quilt, MinecraftVersion: mc, QuiltLoader: loader}, channel: quiltChannel(loader)}, true, nil
	})
}

func quiltLoaderList(ctx context.Context, hc *http.Client, mc string) ([]Build, error) {
	if err := supportsGame(ctx, quiltUpstream(hc), quiltMeta+"/versions/game", Quilt, mc); err != nil {
		return nil, err
	}
	loaders, err := quiltLoaders(ctx, hc)
	if err != nil {
		return nil, err
	}
	return builds(Pin{Type: Quilt, MinecraftVersion: mc}, loaders, quiltChannel,
		func(p *Pin, v string) { p.QuiltLoader = v }), nil
}

// quiltResolve reads the server profile Quilt's installer uses. Quilt's
// metadata lists hashes for the loader, but they do not match the files
// Quilt serves, so every library gets the SHA-512 file next to it in its
// Maven repository instead.
func quiltResolve(ctx context.Context, hc *http.Client, r *Resolved) error {
	meta := quiltUpstream(hc)
	mc, loader := r.Pin.MinecraftVersion, r.Pin.QuiltLoader
	p, what, err := loaderProfile(ctx, meta, quiltMeta+"/versions/loader/"+mc+"/"+loader+"/server/json", mc, loader)
	if err != nil {
		return err
	}
	if !reJavaClass.MatchString(p.LauncherMainClass) {
		return meta.malformed(what, errors.New("it names no valid launcher class"))
	}
	libs, err := mavenLibraries(ctx, meta, quiltMaven(hc), p.Libraries, what, "")
	if err != nil {
		return err
	}
	jar, err := loaderJar(meta, libs, "org.quiltmc:quilt-loader:"+loader, what)
	if err != nil {
		return err
	}
	r.Libraries, r.Loader, r.LauncherMainClass = libs, jar, p.LauncherMainClass
	return nil
}

// quiltPlan writes the launch jar Quilt's installer writes: a manifest
// naming the launcher class from the profile.
func quiltPlan(r Resolved) (Plan, error) {
	if !reJavaClass.MatchString(r.LauncherMainClass) {
		return Plan{}, incomplete(r.Pin)
	}
	return loaderPlan(r, Launcher{Path: "quilt-server-launch.jar", MainClass: r.LauncherMainClass})
}

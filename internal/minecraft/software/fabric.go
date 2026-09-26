package software

import (
	"context"
	"errors"
	"net/http"
	"regexp"
)

const fabricMeta = "https://meta.fabricmc.net/v2"

var reFabricLoader = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// fabricNewestShaded is the newest Fabric loader whose server launcher needs
// the libraries copied into the launch jar. Playkeeper only offers newer
// loaders, whose launch jar holds no code.
const fabricNewestShaded = "0.12.5"

func fabricUpstream(hc *http.Client) upstream {
	return upstream{name: "Fabric", hosts: []string{"meta.fabricmc.net"}, hc: hc}
}

func fabricMaven(hc *http.Client) upstream {
	return upstream{name: "Fabric", hosts: []string{"maven.fabricmc.net"}, hc: hc}
}

func fabricLoaderOK(v string) bool {
	return reFabricLoader.MatchString(v) && compareVersions(v, fabricNewestShaded) > 0
}

// fabricLoaders lists the loaders Playkeeper can launch, newest first.
// Fabric's metadata marks only its newest loader stable; every loader it
// lists is a release, so all of them count as stable here.
func fabricLoaders(ctx context.Context, hc *http.Client) ([]string, error) {
	u := fabricUpstream(hc)
	loaders, err := metaLoaders(ctx, u, fabricMeta+"/versions/loader", fabricLoaderOK)
	if err != nil {
		return nil, err
	}
	if len(loaders) == 0 {
		return nil, &Error{Kind: KindNoVersions, Msg: "Fabric lists no loader version Playkeeper can run.",
			Hint: "Try again later, or choose another server type.", Params: u.params("", "type", Fabric)}
	}
	return loaders, nil
}

func fabricCatalog(ctx context.Context, hc *http.Client) ([]Release, error) {
	man, err := mojangManifest(ctx, hc)
	if err != nil {
		return nil, err
	}
	games, err := metaGames(ctx, fabricUpstream(hc), fabricMeta+"/versions/game")
	if err != nil {
		return nil, err
	}
	loaders, err := fabricLoaders(ctx, hc)
	if err != nil {
		return nil, err
	}
	return offer(ctx, hc, man, Fabric, games, func(mc string) (choice, bool, error) {
		return choice{pin: Pin{Type: Fabric, MinecraftVersion: mc, FabricLoader: loaders[0]}, channel: Stable}, true, nil
	})
}

func fabricLoaderList(ctx context.Context, hc *http.Client, mc string) ([]Build, error) {
	if err := supportsGame(ctx, fabricUpstream(hc), fabricMeta+"/versions/game", Fabric, mc); err != nil {
		return nil, err
	}
	loaders, err := fabricLoaders(ctx, hc)
	if err != nil {
		return nil, err
	}
	return builds(Pin{Type: Fabric, MinecraftVersion: mc}, loaders,
		func(string) Channel { return Stable },
		func(p *Pin, v string) { p.FabricLoader = v }), nil
}

// fabricResolve reads the server profile Fabric's installer uses. Fabric's
// metadata lists a SHA-512 for most libraries; the loader and the mappings
// get theirs from Fabric's Maven repository.
func fabricResolve(ctx context.Context, hc *http.Client, r *Resolved) error {
	meta := fabricUpstream(hc)
	mc, loader := r.Pin.MinecraftVersion, r.Pin.FabricLoader
	p, what, err := loaderProfile(ctx, meta, fabricMeta+"/versions/loader/"+mc+"/"+loader+"/server/json", mc, loader)
	if err != nil {
		return err
	}
	if !reJavaClass.MatchString(p.MainClass) {
		return meta.malformed(what, errors.New("it names no valid main class"))
	}
	libs, err := mavenLibraries(ctx, meta, fabricMaven(hc), p.Libraries, what, "Fabric's metadata API")
	if err != nil {
		return err
	}
	jar, err := loaderJar(meta, libs, "net.fabricmc:fabric-loader:"+loader, what)
	if err != nil {
		return err
	}
	r.Libraries, r.Loader, r.LaunchMainClass = libs, jar, p.MainClass
	return nil
}

// fabricPlan writes the launch jar Fabric's installer writes: the manifest
// of the verified loader jar names the launcher class, and a properties
// file names the class the launcher starts.
func fabricPlan(r Resolved) (Plan, error) {
	if !reJavaClass.MatchString(r.LaunchMainClass) {
		return Plan{}, incomplete(r.Pin)
	}
	return loaderPlan(r, Launcher{Path: "fabric-server-launch.jar", MainClassFrom: r.Loader,
		Properties: "fabric-server-launch.properties", PropertiesBody: "launch.mainClass=" + r.LaunchMainClass + "\n"})
}

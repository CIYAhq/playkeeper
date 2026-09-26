package software

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
)

// Fabric and Quilt describe a server as a launcher profile: the class to
// start and the Maven libraries it needs. Playkeeper downloads the libraries
// itself, each checked against a SHA-512, and writes the code-free launch jar
// the official installers write, so no installer runs.

type metaLibrary struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA512 string `json:"sha512"`
	Size   int64  `json:"size"`
}

type metaProfile struct {
	InheritsFrom      string        `json:"inheritsFrom"`
	MainClass         string        `json:"mainClass"`
	LauncherMainClass string        `json:"launcherMainClass"`
	Libraries         []metaLibrary `json:"libraries"`
}

type metaVersion struct {
	Version string `json:"version"`
	Stable  bool   `json:"stable"`
}

// metaGames lists the Minecraft versions a loader's metadata API marks
// stable.
func metaGames(ctx context.Context, u upstream, url string) ([]string, error) {
	var vs []metaVersion
	if err := u.getJSON(ctx, url, "its list of Minecraft versions", &vs); err != nil {
		return nil, err
	}
	var out []string
	for _, v := range vs {
		if v.Stable && !slices.Contains(out, v.Version) {
			out = append(out, v.Version)
		}
	}
	return out, nil
}

// metaLoaders lists the loader versions valid accepts, newest first.
func metaLoaders(ctx context.Context, u upstream, url string, valid func(string) bool) ([]string, error) {
	var vs []metaVersion
	if err := u.getJSON(ctx, url, "its list of loader versions", &vs); err != nil {
		return nil, err
	}
	var out []string
	for _, v := range vs {
		if valid(v.Version) && !slices.Contains(out, v.Version) {
			out = append(out, v.Version)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return compareVersions(b, a) })
	return out, nil
}

// supportsGame checks that a loader's metadata lists mc as a stable
// Minecraft version.
func supportsGame(ctx context.Context, u upstream, url, typeID, mc string) error {
	games, err := metaGames(ctx, u, url)
	if err != nil {
		return err
	}
	if !slices.Contains(games, mc) {
		return &Error{Kind: KindNotFound, Msg: fmt.Sprintf("%s does not list Minecraft %s as a stable version.", typeName(typeID), mc),
			Hint: "Choose another version.", Params: u.params("", "type", typeID, "minecraftVersion", mc)}
	}
	return nil
}

// loaderProfile reads a loader's server profile for one Minecraft version.
func loaderProfile(ctx context.Context, u upstream, url, mc, loader string) (metaProfile, string, error) {
	what := "the server profile for loader " + loader + " and Minecraft " + mc
	var p metaProfile
	if err := u.getJSON(ctx, url, what, &p); err != nil {
		return metaProfile{}, what, err
	}
	if p.InheritsFrom != mc {
		return metaProfile{}, what, u.malformed(what, fmt.Errorf("it is for Minecraft %q", p.InheritsFrom))
	}
	if len(p.Libraries) == 0 || len(p.Libraries) > 1000 {
		return metaProfile{}, what, u.malformed(what, fmt.Errorf("it lists %d libraries", len(p.Libraries)))
	}
	return p, what, nil
}

// mavenLibraries turns a profile's libraries into downloads under
// libraries/, in class path order. Each gets the SHA-512 the profile lists
// when listedSource names who publishes it, and otherwise the SHA-512 file
// next to the jar in its Maven repository. Library URLs must be on maven's
// hosts.
func mavenLibraries(ctx context.Context, meta, maven upstream, libs []metaLibrary, what, listedSource string) ([]Artifact, error) {
	var out []Artifact
	for _, l := range libs {
		rel, ok := mavenPath(l.Name)
		if !ok {
			return nil, meta.malformed(what, fmt.Errorf("library %q is not a valid Maven coordinate", l.Name))
		}
		rel = "libraries/" + rel
		if slices.ContainsFunc(out, func(a Artifact) bool { return a.Path == rel }) {
			continue
		}
		base := l.URL
		if !strings.HasSuffix(base, "/") {
			base += "/"
		}
		fileURL := base + strings.TrimPrefix(rel, "libraries/")
		name := path.Base(rel)
		pu, err := maven.checkURL(fileURL, name)
		if err != nil {
			return nil, err
		}
		a := Artifact{URL: fileURL, Path: rel, Upstream: maven.name, Hosts: maven.hosts}
		if h, ok := parseHash(SHA512, l.SHA512); ok && listedSource != "" {
			a.Hash, a.Source = h, listedSource
			if l.Size > 0 && l.Size <= maxArtifact {
				a.Size = l.Size
			}
		} else {
			if a.Hash, err = maven.checksum(ctx, fileURL, SHA512, name); err != nil {
				return nil, err
			}
			a.Source = mavenSource(pu.Hostname())
		}
		out = append(out, a)
	}
	return out, nil
}

func mavenSource(host string) string {
	switch host {
	case "maven.fabricmc.net":
		return "Fabric's Maven repository"
	case "maven.quiltmc.org":
		return "Quilt's Maven repository"
	}
	return "the Maven repository at " + host
}

// loaderJar returns the path of the loader jar among the libraries.
func loaderJar(u upstream, libs []Artifact, coord, what string) (string, error) {
	rel, ok := mavenPath(coord)
	if ok && slices.ContainsFunc(libs, func(a Artifact) bool { return a.Path == "libraries/"+rel }) {
		return "libraries/" + rel, nil
	}
	return "", u.malformed(what, fmt.Errorf("it does not include %s", coord))
}

// loaderPlan downloads the libraries and Mojang's server jar, which both
// loaders look for as server.jar, and writes the launch jar l.
func loaderPlan(r Resolved, l Launcher) (Plan, error) {
	if r.Loader == "" || len(r.Libraries) == 0 {
		return Plan{}, incomplete(r.Pin)
	}
	server := r.Server
	server.Path = "server.jar"
	for _, a := range r.Libraries {
		l.ClassPath = append(l.ClassPath, a.Path)
	}
	return Plan{
		Downloads: append(slices.Clone(r.Libraries), server),
		Launcher:  &l,
		Record:    []string{l.Path},
		Run:       Container{Env: []string{"TYPE=CUSTOM", "CUSTOM_SERVER=/data/" + l.Path}},
	}, nil
}

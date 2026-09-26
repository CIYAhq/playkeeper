package software

import (
	"context"
	"net/http"
)

func vanillaCatalog(ctx context.Context, hc *http.Client) ([]Release, error) {
	man, err := mojangManifest(ctx, hc)
	if err != nil {
		return nil, err
	}
	var versions []string
	for id, e := range man {
		if e.Type == "release" {
			versions = append(versions, id)
		}
	}
	return offer(ctx, hc, man, Vanilla, versions, func(mc string) (choice, bool, error) {
		return choice{pin: Pin{Type: Vanilla, MinecraftVersion: mc}, channel: Stable}, true, nil
	})
}

// vanillaPlan runs Mojang's server jar as it is. The jar unpacks its own
// libraries at every start and checks them against the SHA-256 list inside
// it, so nothing else needs checking.
func vanillaPlan(r Resolved) (Plan, error) {
	server := r.Server
	server.Path = "minecraft_server." + r.Pin.MinecraftVersion + ".jar"
	return Plan{
		Downloads: []Artifact{server},
		Run:       Container{Env: []string{"TYPE=CUSTOM", "CUSTOM_SERVER=/data/" + server.Path}},
	}, nil
}

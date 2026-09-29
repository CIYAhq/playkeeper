package main

import (
	"errors"
	"slices"
	"testing"
)

// The bot's offline-mode UUID is the one Paper put on the whitelist for it.
func TestOfflineUUIDIsTheServers(t *testing.T) {
	if got := offlineUUID("PkShot"); got != "6366deb3-89eb-386c-b857-9d44a79297e3" {
		t.Errorf("offlineUUID(PkShot) = %s", got)
	}
}

// A server on the bot's version needs nothing; an older one ViaVersion, a
// newer one ViaBackwards too, on Fabric through ViaFabric; and a server the
// bot can't join, whatever its version, is captured from its saved world.
func TestViaLetsTheBotOn(t *testing.T) {
	for _, c := range []struct {
		typ, version string
		want         []string
	}{
		{"paper", shotVersion, nil},
		{"fabric", shotVersion, nil},
		{"vanilla", shotVersion, nil},
		{"paper", "1.21.11", []string{"viaversion"}},
		{"purpur", "26.2", []string{"viaversion", "viabackwards"}},
		{"fabric", "26.2", []string{"viafabric", "viabackwards"}},
	} {
		if got, err := viaFor(c.typ, c.version); err != nil || !slices.Equal(got, c.want) {
			t.Errorf("viaFor(%s, %s) = %v, %v; want %v", c.typ, c.version, got, err, c.want)
		}
	}
	for _, c := range [][2]string{{"neoforge", "1.21.1"}, {"neoforge", shotVersion}, {"forge", shotVersion}, {"vanilla", "26.2"}} {
		var cant cantShoot
		if _, err := viaFor(c[0], c[1]); !errors.As(err, &cant) {
			t.Errorf("a %s server on %s can be captured: %v", c[0], c[1], err)
		}
	}
}

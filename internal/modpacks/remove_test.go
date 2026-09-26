package modpacks

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/addons"
)

func TestRemoveVanillaPerfected(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "fabric", "26.1.2")
	credits, err := os.ReadFile("testdata/packs/vanilla-perfected-1.0.2/overrides/credits.txt")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, srv, "credits.txt", string(credits))
	writeFile(t, srv, "server.properties", "motd=Mine\n")
	writeFile(t, srv, "world/level.dat", "a world")
	rec := roundTrip(t, mustInstall(t, l, srv, InstallRequest{Ref: vpRef("jmYEuzNA")}).Record)
	if f := recordFiles(rec)["credits.txt"]; !f.Preexisting {
		t.Errorf("credits.txt was on the server before the pack: %+v", f)
	}

	writeFile(t, srv, "config/iris.properties", "shaderPack=mine\n")
	writeFile(t, srv, "mods/mine.jar", "my own mod")
	if err := os.Remove(filepath.Join(srv.Dir, "mods", "cloth-config-26.1.154.jar")); err != nil {
		t.Fatal(err)
	}

	// A pack file swapped for a link and a pack folder swapped for a link
	// are not followed, even to copies of the pack's own files.
	outside := t.TempDir()
	for _, p := range []string{"mods/placeholder-api-3.0.0+26.1.jar", "datapacks/AllMobHeads_V11.zip"} {
		if err := os.Rename(filepath.Join(srv.Dir, p), filepath.Join(outside, filepath.Base(p))); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(outside, "placeholder-api-3.0.0+26.1.jar"), filepath.Join(srv.Dir, "mods", "placeholder-api-3.0.0+26.1.jar")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(srv.Dir, "datapacks")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(srv.Dir, "datapacks")); err != nil {
		t.Fatal(err)
	}

	// The record is only trusted as far as the rules for pack files go.
	above := filepath.Join(filepath.Dir(srv.Dir), "outside.txt")
	if err := os.WriteFile(above, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, srv, "mods/md5.jar", "md5")
	m5 := md5.Sum([]byte("md5"))
	rec.Files = append(rec.Files,
		File{Path: "../outside.txt", HashAlgo: "sha512", Hash: sha512hex([]byte("outside")), Origin: Override},
		File{Path: "world/level.dat", HashAlgo: "sha512", Hash: sha512hex([]byte("a world")), Origin: Override},
		File{Path: "server.properties", HashAlgo: "sha512", Hash: sha512hex([]byte("motd=Mine\n")), Origin: Override},
		File{Path: "mods/md5.jar", HashAlgo: "md5", Hash: hex.EncodeToString(m5[:]), Origin: Download},
	)

	pl, err := l.PlanRemove(srv, rec)
	if err != nil {
		t.Fatal(err)
	}
	wantList(t, "changes", changeList(pl.Changes),
		"remove config/MouseTweaks.cfg",
		"remove config/animatica.properties",
		"remove config/continuity.json",
		"keep config/iris.properties (yours)",
		"remove config/litematica/litematica_Mom's House.json",
		"remove config/packed_packs/preferences.properties",
		"remove config/simple_datapacks.json",
		"keep credits.txt (yours)",
		"remove mods/MouseTweaks-fabric-mc26.1-2.31.jar",
		"remove mods/fabric-api-0.150.0+26.1.2.jar",
		"remove mods/fabric-language-kotlin-1.13.11+kotlin.2.3.21.jar",
		"remove mods/lithium-fabric-0.24.3+mc26.1.2.jar",
		"remove mods/pickupnotifications-2.0.2+26.1.jar",
		"keep mods/placeholder-api-3.0.0+26.1.jar (yours)")
	wantList(t, "warnings", noticeList(pl.Warnings),
		"modded_world: The server stays a Fabric server on Minecraft 26.1.2, and its world stays as it is.")

	_, err = l.Remove(srv, rec, "0123456789abcdef0123456789abcdef")
	wantKind(t, err, addons.KindPlanChanged)
	if readFile(t, srv, "config/MouseTweaks.cfg") == "" {
		t.Error("a refused removal removed files")
	}

	res, err := l.Remove(srv, rec, pl.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	wantList(t, "kept", res.Kept, "config/iris.properties", "credits.txt", "mods/placeholder-api-3.0.0+26.1.jar")
	if len(res.Removed) != 11 || !res.RestartNeeded {
		t.Errorf("removed %q", res.Removed)
	}
	// Folders the removal emptied go, except top-level ones.
	wantTree(t, srv.Dir, "config/", "config/iris.properties", "credits.txt", "datapacks", "mods/", "mods/md5.jar", "mods/mine.jar",
		"mods/placeholder-api-3.0.0+26.1.jar", "server.properties", "world/", "world/level.dat")
	wantTree(t, outside, "AllMobHeads_V11.zip", "placeholder-api-3.0.0+26.1.jar")
	if b, err := os.ReadFile(above); err != nil || string(b) != "outside" {
		t.Errorf("a file outside the server's folder changed: %q, %v", b, err)
	}
	if readFile(t, srv, "config/iris.properties") != "shaderPack=mine\n" || readFile(t, srv, "credits.txt") != string(credits) {
		t.Error("the user's files changed")
	}
}

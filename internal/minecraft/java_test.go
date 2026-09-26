package minecraft

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// testdata/mojang-java-versions.json holds the javaVersion of real version
// files from piston-meta.mojang.com, releases and pre-releases at every
// boundary.
func TestJavaForMatchesMojang(t *testing.T) {
	b, err := os.ReadFile("testdata/mojang-java-versions.json")
	if err != nil {
		t.Fatal(err)
	}
	var versions []struct {
		ID          string `json:"id"`
		JavaVersion struct {
			MajorVersion int `json:"majorVersion"`
		} `json:"javaVersion"`
	}
	if err := json.Unmarshal(b, &versions); err != nil {
		t.Fatal(err)
	}
	if len(versions) < 20 {
		t.Fatalf("only %d versions in the fixture", len(versions))
	}
	for _, v := range versions {
		if got := JavaFor(v.ID); got != v.JavaVersion.MajorVersion {
			t.Errorf("JavaFor(%q) = %d, Mojang says %d", v.ID, got, v.JavaVersion.MajorVersion)
		}
	}
	for _, unreadable := range []string{"", "24w14a", "latest", "1", "1.x", "1.21.4.1.2", "26"} {
		if got := JavaFor(unreadable); got != NewestJava {
			t.Errorf("JavaFor(%q) = %d, want the newest, %d", unreadable, got, NewestJava)
		}
	}
}

var reRuntime = regexp.MustCompile(`^docker\.io/itzg/minecraft-server@sha256:[0-9a-f]{64}$`)

// Every Java a Minecraft release runs on has an image, pinned by digest,
// from the same release of the image as the default one.
func TestEveryJavaHasAPinnedRuntime(t *testing.T) {
	if img, tag, ok := ImageFor(NewestJava); !ok || img != Image || tag != ImageTag {
		t.Fatalf("the newest Java's image is the default one: %q %q %v", img, tag, ok)
	}
	release := strings.TrimSuffix(ImageTag, "-java"+strconv.Itoa(NewestJava))
	seen := map[string]bool{}
	for _, java := range []int{8, 16, 17, 21, 25} {
		img, tag, ok := ImageFor(java)
		if !ok || !reRuntime.MatchString(img) || tag != release+"-java"+strconv.Itoa(java) {
			t.Errorf("Java %d: %q %q %v", java, img, tag, ok)
		}
		if seen[img] {
			t.Errorf("Java %d shares its image with another Java", java)
		}
		seen[img] = true
	}
	if _, _, ok := ImageFor(11); ok {
		t.Error("no Minecraft release runs on Java 11, so there is no image for it")
	}
	if got := Runtimes(); len(got) != len(seen) || got[0] != Image {
		t.Errorf("Runtimes() = %v", got)
	}
}

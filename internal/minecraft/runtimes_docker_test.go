package minecraft

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestPinnedRuntimesRunJavaOnThisCPU pulls every pinned runtime image and runs
// its Java on the machine the tests run on: Docker must pick the image for
// this CPU from each digest's index, and run it without emulation. It needs
// Docker and Docker Hub, so it runs only with PLAYKEEPER_RUNTIME_IMAGES_TEST=1,
// as the ARM64 workflow sets it on an ARM runner.
func TestPinnedRuntimesRunJavaOnThisCPU(t *testing.T) {
	if os.Getenv("PLAYKEEPER_RUNTIME_IMAGES_TEST") != "1" {
		t.Skip("PLAYKEEPER_RUNTIME_IMAGES_TEST=1 pulls the runtime images and runs them (needs Docker)")
	}
	machine := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	if machine == "" {
		t.Fatalf("Playkeeper has no release for %s", runtime.GOARCH)
	}
	docker := func(t *testing.T, args ...string) string {
		t.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	for _, java := range []int{8, 16, 17, 21, 25} {
		img, tag, ok := ImageFor(java)
		if !ok {
			t.Fatalf("no image for Java %d", java)
		}
		t.Run(tag, func(t *testing.T) {
			docker(t, "pull", "--quiet", img)
			if platform := docker(t, "image", "inspect", "--format", "{{.Os}}/{{.Architecture}}", img); platform != "linux/"+runtime.GOARCH {
				t.Fatalf("Docker pulled the %s image of %s", platform, tag)
			}
			if got := docker(t, "run", "--rm", "--entrypoint", "uname", img, "-m"); got != machine {
				t.Fatalf("%s runs as %s on this %s machine", tag, got, machine)
			}
			// Java 8 calls itself 1.8.0; later ones start with their number.
			want := `version "` + strconv.Itoa(java)
			if java == 8 {
				want = `version "1.8.0`
			}
			v := docker(t, "run", "--rm", "--entrypoint", "java", img, "-version")
			if !strings.Contains(v, want) {
				t.Fatalf("%s's java -version says:\n%s", tag, v)
			}
			first, _, _ := strings.Cut(v, "\n")
			t.Logf("%s on %s: %s", tag, machine, first)
		})
	}
}

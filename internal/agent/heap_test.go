package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/minecraft/software"
)

// installedFabric makes the test server a Fabric server whose install
// Playkeeper recorded, so a start runs it without downloading Fabric.
func (e *agentEnv) installedFabric() {
	e.t.Helper()
	e.fabricForShare()
	s := e.srv()
	sc, err := s.serverConfig()
	if err != nil {
		e.t.Fatal(err)
	}
	launcher := []byte("fake fabric launcher")
	if err := os.WriteFile(filepath.Join(e.dataDir(), "fabric-server-launch.jar"), launcher, 0o644); err != nil {
		e.t.Fatal(err)
	}
	sum := sha256.Sum256(launcher)
	m := software.Manifest{
		Pin: software.Pin(*sc.Software),
		Checks: []software.Check{{Path: "fabric-server-launch.jar", Hash: software.Hash{Algorithm: software.SHA256, Value: hex.EncodeToString(sum[:])},
			Origin: software.Recorded, Source: "test"}},
		Run: software.Container{Env: []string{"TYPE=CUSTOM", "CUSTOM_SERVER=/data/fabric-server-launch.jar"}},
	}
	if err := s.saveManifest(m); err != nil {
		e.t.Fatal(err)
	}
}

// A mod loader keeps more of its memory outside the Java heap, sized by the
// jars in its mods folder when it starts and when its memory changes, and
// its container gets that heap. A Paper server's heap is the one it always
// had, so its container doesn't change.
func TestModLoaderHeapLeavesRoomForItsMods(t *testing.T) {
	e := newAgentEnv(t)
	e.createWith(map[string]any{"memoryMB": 2048})
	if got := e.containerEnvVar("MEMORY"); got != fmt.Sprintf("%dM", minecraft.HeapMB(2048)) {
		t.Fatalf("Paper's heap: %s, want %dM", got, minecraft.HeapMB(2048))
	}

	e.installedFabric()
	mods := filepath.Join(e.dataDir(), "mods")
	if err := os.MkdirAll(mods, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 17 {
		if err := os.WriteFile(filepath.Join(mods, fmt.Sprintf("mod-%02d.jar", i)), []byte("jar"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(mods, "README.txt"), []byte("not a mod"), 0o644); err != nil {
		t.Fatal(err)
	}
	if op := e.runOp("POST", "/restart"); op.Status != api.OpSucceeded {
		t.Fatalf("restart: %+v", op)
	}
	e.waitFor("online", func() bool { return e.status().Phase == api.PhaseOnline })
	want := minecraft.HeapFor(2048, "fabric", 17)
	if got := e.containerEnvVar("MEMORY"); got != fmt.Sprintf("%dM", want) || want >= minecraft.HeapMB(2048) {
		t.Fatalf("Fabric with 17 mods at 2 GB: the container's heap is %s, want %dM, less than Paper's %d", got, want, minecraft.HeapMB(2048))
	}
	if sc, _ := e.srv().serverConfig(); sc.HeapMB != want {
		t.Fatalf("the sized heap is recorded: %d", sc.HeapMB)
	}

	if err := os.WriteFile(filepath.Join(mods, "late.jar"), []byte("jar"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := e.status(); st.PendingRestart {
		t.Fatal("a mod added while the server runs changed its container's definition")
	}
	// Starting a server that runs as defined leaves it alone; the HTTP Start
	// answers "already running" before this, but a start from anywhere else
	// mustn't stop it to size the heap for the mod added since.
	running := e.containerID()
	sc, err := e.srv().serverConfig()
	if err != nil {
		t.Fatal(err)
	}
	h := &opHandle{save: func(*api.Operation) {}, op: &api.Operation{Actor: "admin", Detail: map[string]any{}}, mu: func() func() { return func() {} }}
	if err := e.srv().startServer(context.Background(), h, *sc); err != nil {
		t.Fatalf("start while running: %v", err)
	}
	if id := e.containerID(); id != running {
		t.Fatal("a start of the running server stopped it to size the heap for the mod added since")
	}
	if code, out := e.call("POST", e.sp("/settings"), map[string]any{"memoryMB": 2048, "actor": "admin"}); code != 200 {
		t.Fatalf("settings: %d %v", code, out)
	}
	if sc, _ := e.srv().serverConfig(); sc.HeapMB != want || e.status().PendingRestart {
		t.Fatalf("a save with the same memory resized the heap to %d (restart pending: %v)", sc.HeapMB, e.status().PendingRestart)
	}
	if code, out := e.call("POST", e.sp("/settings"), map[string]any{"memoryMB": 3072, "actor": "admin"}); code != 200 {
		t.Fatalf("settings: %d %v", code, out)
	}
	if sc, _ := e.srv().serverConfig(); sc.HeapMB != minecraft.HeapFor(3072, "fabric", 18) {
		t.Fatalf("after giving it 3 GB: heap %d, want %d", sc.HeapMB, minecraft.HeapFor(3072, "fabric", 18))
	}
}

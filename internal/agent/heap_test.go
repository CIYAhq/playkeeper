package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// addMods writes n jars to the server's mods folder, numbered from from.
func (e *agentEnv) addMods(from, n int) {
	e.t.Helper()
	mods := filepath.Join(e.dataDir(), "mods")
	if err := os.MkdirAll(mods, 0o755); err != nil {
		e.t.Fatal(err)
	}
	for i := from; i < from+n; i++ {
		if err := os.WriteFile(filepath.Join(mods, fmt.Sprintf("mod-%02d.jar", i)), []byte("jar"), 0o644); err != nil {
			e.t.Fatal(err)
		}
	}
}

// sizedFabric is a running Fabric server with 2 GB whose last start sized its
// heap for 17 mods.
func sizedFabric(t *testing.T) *agentEnv {
	t.Helper()
	e := newAgentEnv(t)
	e.createWith(map[string]any{"memoryMB": 2048})
	e.installedFabric()
	e.addMods(0, 17)
	if op := e.runOp("POST", "/restart"); op.Status != api.OpSucceeded {
		t.Fatalf("restart: %+v", op)
	}
	e.waitFor("online", e.onlineIdle)
	if got, want := e.containerEnvVar("MEMORY"), fmt.Sprintf("%dM", minecraft.HeapFor(2048, "fabric", 17)); got != want {
		t.Fatalf("the heap for 17 mods: %s, want %s", got, want)
	}
	return e
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
	e.addMods(0, 17)
	mods := filepath.Join(e.dataDir(), "mods")
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
	if code, out := e.call("POST", e.sp("/settings"), map[string]any{"memoryMB": 3072, "actor": "admin"}); code != 200 {
		t.Fatalf("settings: %d %v", code, out)
	}
	if sc, _ := e.srv().serverConfig(); sc.HeapMB != minecraft.HeapFor(3072, "fabric", 18) {
		t.Fatalf("after giving it 3 GB: heap %d, want %d", sc.HeapMB, minecraft.HeapFor(3072, "fabric", 18))
	}
}

// A start that finds a mod loader running leaves it alone, even when the mods
// added since call for another heap: the container keeps the heap it was made
// with, nothing asks for a restart over it, and the next restart sizes it.
func TestAStartLeavesARunningModLoaderAndItsHeapAlone(t *testing.T) {
	e := sizedFabric(t)
	s := e.srv()
	container := func() (string, time.Time) {
		e.fd.mu.Lock()
		defer e.fd.mu.Unlock()
		c := e.fd.byName[e.cname()]
		return c.id, c.started
	}
	id, started := container()
	e.addMods(17, 40)
	sc, err := s.serverConfig()
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.beginOp("start", "admin", func(ctx context.Context, h *opHandle) error { return s.startServer(ctx, h, *sc) })
	if err != nil {
		t.Fatal(err)
	}
	if op := e.waitOp(op.ID); op.Status != api.OpSucceeded {
		t.Fatalf("start: %+v", op)
	}
	if nowID, nowStarted := container(); nowID != id || !nowStarted.Equal(started) {
		t.Fatal("a start restarted the running server for the heap its new mods call for")
	}
	was := minecraft.HeapFor(2048, "fabric", 17)
	if got := e.containerEnvVar("MEMORY"); got != fmt.Sprintf("%dM", was) {
		t.Fatalf("the running container's heap: %s, want %dM", got, was)
	}
	if sc, _ := s.serverConfig(); sc.HeapMB != was {
		t.Fatalf("recorded heap %d, want the running container's %d", sc.HeapMB, was)
	}
	if st := e.status(); st.PendingRestart || st.Phase != api.PhaseOnline {
		t.Fatalf("after the start: phase %s, pending restart %v", st.Phase, st.PendingRestart)
	}

	if op := e.runOp("POST", "/restart"); op.Status != api.OpSucceeded {
		t.Fatalf("restart: %+v", op)
	}
	e.waitFor("online", e.onlineIdle)
	if got, want := e.containerEnvVar("MEMORY"), fmt.Sprintf("%dM", minecraft.HeapFor(2048, "fabric", 57)); got != want {
		t.Fatalf("after a restart: heap %s, want %s", got, want)
	}
}

// A settings save that leaves the memory budget as it was leaves the heap as
// it was too, however many mods were added since the last start, so it
// doesn't ask for a restart. A new budget is sized for the mods there are now.
func TestASaveWithTheSameMemoryLeavesTheHeapAlone(t *testing.T) {
	e := sizedFabric(t)
	e.addMods(17, 40)
	if code, out := e.call("POST", e.sp("/settings"), map[string]any{"memoryMB": 2048, "actor": "admin"}); code != 200 {
		t.Fatalf("settings: %d %v", code, out)
	}
	if sc, _ := e.srv().serverConfig(); sc.HeapMB != minecraft.HeapFor(2048, "fabric", 17) {
		t.Fatalf("a save with the same 2 GB: heap %d, want %d", sc.HeapMB, minecraft.HeapFor(2048, "fabric", 17))
	}
	if e.status().PendingRestart {
		t.Fatal("a save with the same memory asks for a restart")
	}

	if code, out := e.call("POST", e.sp("/settings"), map[string]any{"memoryMB": 3072, "actor": "admin"}); code != 200 {
		t.Fatalf("settings: %d %v", code, out)
	}
	if sc, _ := e.srv().serverConfig(); sc.HeapMB != minecraft.HeapFor(3072, "fabric", 57) {
		t.Fatalf("after giving it 3 GB: heap %d, want %d", sc.HeapMB, minecraft.HeapFor(3072, "fabric", 57))
	}
	if !e.status().PendingRestart {
		t.Fatal("a new budget doesn't ask for a restart")
	}
}

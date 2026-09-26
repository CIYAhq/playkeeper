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

// A start sizes a mod loader's heap for the mods it has whenever it makes a
// container: for a stopped server, and for a running one it recreates for
// another change. Only a running container it keeps as it is keeps the heap
// it was made with.
func TestAStartSizesTheHeapOfEveryContainerItMakes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		before    func(e *agentEnv, sc *api.ServerConfig)
		recreated bool
		mods      int
	}{
		{"running and kept", func(*agentEnv, *api.ServerConfig) {}, false, 17},
		{"running but recreated for another change", func(_ *agentEnv, sc *api.ServerConfig) { sc.MOTD = "A new message of the day" }, true, 57},
		{"stopped", func(e *agentEnv, _ *api.ServerConfig) {
			if op := e.runOp("POST", "/stop"); op.Status != api.OpSucceeded {
				t.Fatalf("stop: %+v", op)
			}
		}, true, 57},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := sizedFabric(t)
			s := e.srv()
			container := func() string {
				e.fd.mu.Lock()
				defer e.fd.mu.Unlock()
				return e.fd.byName[e.cname()].id
			}
			id := container()
			e.addMods(17, 40)
			sc, err := s.serverConfig()
			if err != nil {
				t.Fatal(err)
			}
			tc.before(e, sc)
			op, err := s.beginOp("start", "admin", func(ctx context.Context, h *opHandle) error { return s.startServer(ctx, h, *sc) })
			if err != nil {
				t.Fatal(err)
			}
			if op := e.waitOp(op.ID); op.Status != api.OpSucceeded {
				t.Fatalf("start: %+v", op)
			}
			if recreated := container() != id; recreated != tc.recreated {
				t.Fatalf("container recreated: %v, want %v", recreated, tc.recreated)
			}
			want := minecraft.HeapFor(2048, "fabric", tc.mods)
			if got := e.containerEnvVar("MEMORY"); got != fmt.Sprintf("%dM", want) {
				t.Fatalf("the container's heap: %s, want %dM, for %d mods", got, want, tc.mods)
			}
			if sc, _ := s.serverConfig(); sc.HeapMB != want {
				t.Fatalf("recorded heap %d, want %d", sc.HeapMB, want)
			}
		})
	}
}

// runningFabric is a running Fabric server with memoryMB whose last start
// sized its heap for mods jars, and that waits an hour before starting again
// after a crash.
func runningFabric(t *testing.T, memoryMB, mods int) *agentEnv {
	t.Helper()
	e := newAgentEnvWith(t, func(e *agentEnv) { e.crashBackoff = []time.Duration{time.Hour} })
	e.createWith(map[string]any{"memoryMB": memoryMB})
	e.installedFabric()
	e.addMods(0, mods)
	if op := e.runOp("POST", "/restart"); op.Status != api.OpSucceeded {
		t.Fatalf("restart: %+v", op)
	}
	e.waitFor("online", e.onlineIdle)
	if got, want := e.containerEnvVar("MEMORY"), fmt.Sprintf("%dM", minecraft.HeapFor(memoryMB, "fabric", mods)); got != want {
		t.Fatalf("the heap for %d mods: %s, want %s", mods, got, want)
	}
	return e
}

// recordHeap has the server's settings name another heap than its container's.
func recordHeap(mb int) func(t *testing.T, e *agentEnv) {
	return func(t *testing.T, e *agentEnv) {
		sc, err := e.srv().serverConfig()
		if err != nil {
			t.Fatal(err)
		}
		sc.HeapMB = mb
		if err := e.srv().saveServerConfig(*sc); err != nil {
			t.Fatal(err)
		}
	}
}

// saveMemory saves a new memory budget, which waits for a restart.
func saveMemory(mb int) func(t *testing.T, e *agentEnv) {
	return func(t *testing.T, e *agentEnv) {
		if code, out := e.call("POST", e.sp("/settings"), map[string]any{"memoryMB": mb, "actor": "admin"}); code != 200 {
			t.Fatalf("settings: %d %v", code, out)
		}
	}
}

// Settings › Memory reads two weeks of garbage collection against the heap
// the server's container has, whatever mods came or went since it started or
// its settings say: a real verdict, naming that heap. A budget saved since
// is read against the heap its restart gives it.
func TestMemoryAdviceReadsTheHeapTheServerRunsWith(t *testing.T) {
	running := minecraft.HeapFor(3072, "fabric", 60)
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, e *agentEnv)
		heap   int
	}{
		{"with the same mods", func(*testing.T, *agentEnv) {}, running},
		{"with 50 mods removed since it started", func(t *testing.T, e *agentEnv) {
			for i := range 50 {
				if err := os.Remove(filepath.Join(e.dataDir(), "mods", fmt.Sprintf("mod-%02d.jar", i))); err != nil {
					t.Fatal(err)
				}
			}
		}, running},
		{"with 50 mods added since it started", func(_ *testing.T, e *agentEnv) { e.addMods(60, 50) }, running},
		{"with another heap in its settings since", recordHeap(running + 500), running},
		{"with a smaller budget waiting for a restart", saveMemory(2048), minecraft.HeapFor(2048, "fabric", 60)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := runningFabric(t, 3072, 60)
			now := time.Now().UTC()
			for h := 6; h < 14*24; h += 6 {
				e.addGCWindow(now.Add(-time.Duration(h)*time.Hour), running/4, running/2, running, 0)
			}
			tc.change(t, e)
			a := e.memory("UTC")
			if a.Verdict == "not_enough_data" || a.HeapMB != tc.heap || a.Params["heap_mb"] != float64(tc.heap) {
				t.Fatalf("want a verdict for the %d MB heap: %s, heap %d, params %v", tc.heap, a.Verdict, a.HeapMB, a.Params)
			}
		})
	}
}

// Crash help explains a crash with the memory the server's container was
// made with: not another heap its settings name since, nor a budget saved
// since that waits for a restart.
func TestCrashHelpExplainsTheMemoryTheServerRanWith(t *testing.T) {
	ran := minecraft.HeapFor(2048, "fabric", 17)
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, e *agentEnv)
	}{
		{"with its settings as the container was made", func(*testing.T, *agentEnv) {}},
		{"with another heap in its settings since", recordHeap(ran + 300)},
		{"with a bigger budget waiting for a restart", saveMemory(3072)},
	} {
		for _, crash := range []struct {
			name string
			run  func(e *agentEnv)
			kind string
		}{
			{"Java runs out of heap", func(e *agentEnv) {
				e.fd.addLog("java.lang.OutOfMemoryError: Java heap space")
				e.fd.crash(1)
			}, "heap_out_of_memory"},
			{"Docker kills it at its memory limit", func(e *agentEnv) { e.fd.oomKill() }, "container_memory_limit"},
		} {
			t.Run(tc.name+", "+crash.name, func(t *testing.T) {
				e := runningFabric(t, 2048, 17)
				tc.change(t, e)
				crash.run(e)
				c := e.waitCrash()
				if c.Kind != crash.kind || c.Params["heap_mb"] != ran || c.Params["budget_mb"] != 2048 {
					t.Fatalf("want %s explained with the %d MB heap of 2048 MB it ran with: %s %v", crash.kind, ran, c.Kind, c.Params)
				}
			})
		}
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

package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

func (e *agentEnv) autoRestarts() int {
	return e.countRows(`SELECT COUNT(*) FROM operations WHERE kind = 'auto-restart'`)
}

// A server that crashes right after logging "Done" stays crashed when the log
// follower reads the stopped container's log again and Docker sends that line
// once more, so each automatic restart comes when the backoff says, and the
// third crash in the window is given up.
func TestACrashRightAfterDoneStaysCrashedUntilItsRestart(t *testing.T) {
	e := newAgentEnv(t)
	e.stop()
	e.crashBackoff = []time.Duration{time.Minute, 30 * time.Second}
	e.start()
	e.create()
	for n := 1; n <= maxCrashes; n++ {
		e.waitFor("online", e.onlineIdle)
		e.fd.crash(1)
		e.waitFor(fmt.Sprintf("crash %d recorded", n), func() bool { return e.crashEvents() == n })
		e.waitReread()
		if st := e.status(); st.Phase != api.PhaseCrashed || st.CrashCount != n {
			t.Fatalf("crash %d, after the follower read the log again: phase %q with %d crashes, want crashed with %d", n, st.Phase, st.CrashCount, n)
		}
		if got := e.autoRestarts(); got != n-1 {
			t.Fatalf("crash %d: %d automatic restarts before its backoff, want %d", n, got, n-1)
		}
		if n == maxCrashes {
			break
		}
		backoff := e.crashBackoff[n-1]
		e.skew.Add(int64(backoff / 2))
		time.Sleep(300 * time.Millisecond)
		if got := e.autoRestarts(); got != n-1 {
			t.Fatalf("crash %d: restarted before its %s backoff", n, backoff)
		}
		e.skew.Add(int64(backoff / 2))
		e.waitFor(fmt.Sprintf("restart %d", n), func() bool { return e.autoRestarts() == n })
	}
	e.skew.Add(int64(time.Hour))
	time.Sleep(300 * time.Millisecond)
	if st := e.status(); st.Phase != api.PhaseCrashed || !strings.Contains(st.LastError, "stopped restarting") || e.autoRestarts() != maxCrashes-1 {
		t.Fatalf("after the third crash: phase %q, %d automatic restarts, error %q; want crashed and given up", st.Phase, e.autoRestarts(), st.LastError)
	}
}

// A line the follower has read changes nothing when Docker sends it again:
// an out-of-memory error logged just before the third crash does not replace
// the notice that Playkeeper stopped restarting the server.
func TestALineReadAgainKeepsTheGiveUpNotice(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	for n := 1; n <= maxCrashes; n++ {
		e.waitFor("online", e.onlineIdle)
		if n == maxCrashes {
			e.fd.addLog("java.lang.OutOfMemoryError: Java heap space")
			e.waitFor("the error read", func() bool { return e.status().LastError == "Java ran out of memory." })
		}
		e.fd.crash(3)
		e.waitFor(fmt.Sprintf("crash %d recorded", n), func() bool { return e.crashEvents() == n })
	}
	e.waitReread()
	if st := e.status(); st.Phase != api.PhaseCrashed || !strings.Contains(st.LastError, "stopped restarting") {
		t.Fatalf("after the follower read the log again: phase %q, error %q; want crashed and given up", st.Phase, st.LastError)
	}
}

// When the log cannot be read as the server stops, the exit is judged
// followerGrace after it was seen. A "Done" line the follower reads only
// afterwards is from a run that has ended: it neither undoes the crash nor
// stops the automatic restart.
func TestADoneLineReadAfterTheExitWasJudgedChangesNothing(t *testing.T) {
	e := newAgentEnv(t)
	e.stop()
	e.crashBackoff = []time.Duration{time.Minute}
	e.start()
	e.create()
	s := e.srv()
	c, err := e.a.docker.ContainerInspect(context.Background(), s.containerName())
	if err != nil {
		t.Fatal(err)
	}
	logs := "/containers/" + c.ID + "/logs"
	e.fd.mu.Lock()
	e.fd.down = logs
	e.fd.mu.Unlock()
	attempts := e.fd.called("GET " + logs)
	e.fd.rotate(100, false) // ends the open log stream
	e.waitFor("the follower to fail to attach again", func() bool { return e.fd.called("GET "+logs) > attempts })
	e.fd.addLog(`[12:00:30 INFO]: Done (1.000s)! For help, type "help"`)
	e.fd.crash(1)
	e.waitFor("the exit seen", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.exitSeen[c.ID].at.IsZero()
	})
	e.skew.Add(int64(followerGrace))
	e.waitFor("the crash recorded", func() bool { return e.crashEvents() == 1 })
	e.fd.mu.Lock()
	e.fd.down = ""
	e.fd.mu.Unlock()
	e.waitExitRead()
	if st := e.status(); st.Phase != api.PhaseCrashed || e.autoRestarts() != 0 {
		t.Fatalf("after the follower read the ended run's Done line: phase %q, %d automatic restarts; want crashed, none yet", st.Phase, e.autoRestarts())
	}
	e.skew.Add(int64(time.Minute))
	e.waitFor("the automatic restart", func() bool { return e.autoRestarts() == 1 && e.onlineIdle() })
}

package agent

import (
	"context"
	"testing"
	"time"
)

func TestConsoleNeverSendsACommandTwice(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.rcon.mu.Lock()
	e.rcon.hangUp["chunky confirm"] = true
	e.rcon.mu.Unlock()
	if _, err := (rconConsole{e.srv()}).Command(context.Background(), "chunky confirm"); err == nil {
		t.Fatal("a command the server hung up on must fail")
	}
	if n := e.rcon.count("chunky confirm"); n != 1 {
		t.Fatalf("the server got the command %d times, want once", n)
	}

	// A connection the server closed while idle is replaced before the
	// command goes out, so the command still runs once.
	if _, err := e.srv().rconExec(context.Background(), "say before"); err != nil {
		t.Fatal(err)
	}
	e.rcon.dropAll()
	time.Sleep(50 * time.Millisecond)
	if _, err := e.srv().rconExec(context.Background(), "say after"); err != nil {
		t.Fatalf("a stale connection must be replaced: %v", err)
	}
	if n := e.rcon.count("say after"); n != 1 {
		t.Fatalf("the server got the command %d times, want once", n)
	}
}

func TestConsoleHonoursTheCallersDeadline(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.srv().rconExec(ctx, "say late"); err == nil {
		t.Fatal("a cancelled caller must not send")
	}
	if n := e.rcon.count("say late"); n != 0 {
		t.Fatalf("the server got a command from a cancelled caller %d times", n)
	}
}

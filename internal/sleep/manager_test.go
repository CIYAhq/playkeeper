package sleep

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// takePort holds a free loopback port, as a running container does.
func takePort(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

func noWake(string) {}

func TestWakeFreesThePortBeforeStarting(t *testing.T) {
	m, _ := newManager(t, nil)
	m.closeGrace = 100 * time.Millisecond
	addr := m.Addr()
	idle := dial(t, addr)
	started := false
	err := m.Wake(context.Background(), func(context.Context) error {
		started = true
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("the container could not take the game port: %w", err)
		}
		return ln.Close()
	})
	if err != nil || !started {
		t.Fatalf("wake: started %v, %v", started, err)
	}
	if m.Listening() {
		t.Fatal("still listening after waking")
	}
	expectClosed(t, idle, time.Second)
}

func TestWakeListensAgainWhenStartFails(t *testing.T) {
	clock := &testClock{t: t0}
	m, wakes := newManager(t, func(c *Config) { c.Now = clock.Now })
	addr := m.Addr()
	boom := errors.New("docker could not start the container")
	for _, pause := range []time.Duration{5 * time.Minute, 10 * time.Minute} {
		err := m.Wake(context.Background(), func(context.Context) error { return boom })
		if !errors.Is(err, boom) {
			t.Fatalf("wake: %v", err)
		}
		if !m.Listening() || m.Addr() != addr {
			t.Fatalf("after a failed start: listening %v on %s, want %s", m.Listening(), m.Addr(), addr)
		}
		want := fmt.Sprintf("Survival couldn't wake up just now. Try again in about %d minutes.", int(pause.Minutes()))
		if got := mustJoin(t, addr, "Steve"); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
		expectNoWake(t, wakes)
		clock.add(pause)
		if got := mustJoin(t, addr, "Steve"); got != wakingMsg {
			t.Fatalf("after the pause: %q", got)
		}
		expectWake(t, wakes, "Steve")
	}
}

func TestWakeOfAServerThatWasNotAsleepOnlyStartsIt(t *testing.T) {
	ln := takePort(t)
	addr := ln.Addr().String()
	ln.Close()
	m, err := NewManager(Config{Addr: addr, OnWake: noWake})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	boom := errors.New("the jar failed its checksum")
	if err := m.Wake(context.Background(), func(context.Context) error { return boom }); err != boom {
		t.Fatalf("got %v", err)
	}
	if m.Listening() {
		t.Fatal("a stopped server looks asleep after a failed start")
	}
}

func TestSleepTakesThePortBackWhenTheContainerLetsGo(t *testing.T) {
	container := takePort(t)
	addr := container.Addr().String()
	m, err := NewManager(Config{Addr: addr, Status: Status{Name: "Survival", MaxPlayers: 20}, OnWake: noWake, BindTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	err = m.Sleep(context.Background(), func(context.Context) error {
		time.AfterFunc(300*time.Millisecond, func() { container.Close() })
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Listening() || m.Addr() != addr {
		t.Fatalf("listening %v on %s", m.Listening(), m.Addr())
	}
	waitForStatus(t, addr)
}

func TestListenGivesUpWhenThePortStaysBusy(t *testing.T) {
	addr := takePort(t).Addr().String()
	_, p, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(p)
	m, err := NewManager(Config{Addr: addr, OnWake: noWake, BindTimeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = m.Listen(context.Background())
	var e *Error
	if !errors.As(err, &e) || e.Code != "port_in_use" || e.Params["port"] != port || !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("got %#v", err)
	}
	if want := fmt.Sprintf("Port %d is still in use, so Playkeeper can't answer players while the server sleeps.", port); e.Error() != want {
		t.Fatalf("message %q", e.Error())
	}
	if d := time.Since(start); d < 300*time.Millisecond || d > 3*time.Second {
		t.Fatalf("gave up after %v", d)
	}
	if m.Listening() {
		t.Fatal("listening")
	}
}

func TestSleepStaysQuietWhenStopFails(t *testing.T) {
	ln := takePort(t)
	addr := ln.Addr().String()
	ln.Close()
	m, err := NewManager(Config{Addr: addr, OnWake: noWake})
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("the server did not stop")
	if err := m.Sleep(context.Background(), func(context.Context) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if m.Listening() {
		t.Fatal("listening although the server may still run")
	}
}

func TestSleepDoesNothingWhileAsleep(t *testing.T) {
	m, _ := newManager(t, nil)
	called := false
	if err := m.Sleep(context.Background(), func(context.Context) error { called = true; return nil }); err != nil || called {
		t.Fatalf("stop called %v, %v", called, err)
	}
}

func TestCloseFreesThePortAndListenStartsAgain(t *testing.T) {
	m, _ := newManager(t, nil)
	addr := m.Addr()
	m.Close()
	m.Close()
	if m.Listening() {
		t.Fatal("listening after Close")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("port not freed: %v", err)
	}
	ln.Close()
	if err := m.Listen(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.Addr() != addr {
		t.Fatalf("port changed from %s to %s", addr, m.Addr())
	}
	waitForStatus(t, addr)
}

func TestSleepWakeSleep(t *testing.T) {
	m, wakes := newManager(t, nil)
	addr := m.Addr()
	if got := mustJoin(t, addr, "Steve"); got != wakingMsg {
		t.Fatalf("got %q", got)
	}
	expectWake(t, wakes, "Steve")
	var container net.Listener
	err := m.Wake(context.Background(), func(context.Context) error {
		var err error
		container, err = net.Listen("tcp", addr)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Sleep(context.Background(), func(context.Context) error { return container.Close() }); err != nil {
		t.Fatal(err)
	}
	if !m.Listening() || m.Addr() != addr {
		t.Fatalf("listening %v on %s", m.Listening(), m.Addr())
	}
	if got := mustJoin(t, addr, "Alex"); got != wakingMsg {
		t.Fatalf("after sleeping again: %q", got)
	}
	expectWake(t, wakes, "Alex")
}

func TestNewManagerChecksItsConfig(t *testing.T) {
	for _, cfg := range []Config{
		{Addr: "25565", OnWake: noWake},
		{Addr: ":port", OnWake: noWake},
		{Addr: ":70000", OnWake: noWake},
		{Addr: ":25565"},
	} {
		if _, err := NewManager(cfg); err == nil {
			t.Errorf("accepted %+v", cfg)
		}
	}
}

func TestSetStatusCleansWhatClientsSee(t *testing.T) {
	m, _ := newManager(t, nil)
	m.SetStatus(Status{Name: "My\nServer §cRed\x00", Protocol: -5, MaxPlayers: -1, Icon: "https://example.com/icon.png"})
	doc := queryStatus(t, m.Addr(), 767)
	if got := doc.Description.plain(); got != "My Server cRed\nAsleep · join to wake it" {
		t.Errorf("description %q", got)
	}
	if doc.Version.Name != "Minecraft" || doc.Players.Max != 0 || doc.Favicon != "" {
		t.Errorf("got %+v", doc)
	}
	m.SetStatus(Status{Name: `Bob's "Realm"`})
	if got := mustJoin(t, m.Addr(), "Steve"); got != `Bob's "Realm" is waking up. Join again in about 30 seconds.` {
		t.Errorf("got %q", got)
	}
	m.SetStatus(Status{})
	if got := mustJoin(t, m.Addr(), "Alex"); got != "This server is waking up. Join again in about 30 seconds." {
		t.Errorf("without a name: %q", got)
	}
}

func TestLogsNeverIncludeAddresses(t *testing.T) {
	var mu sync.Mutex
	var logs []string
	m, wakes := newManager(t, func(c *Config) {
		c.Logf = func(format string, args ...any) {
			mu.Lock()
			logs = append(logs, fmt.Sprintf(format, args...))
			mu.Unlock()
		}
		c.Admit = func(p string) bool { return p != "Mallory" }
	})
	mustJoin(t, m.Addr(), "Mallory")
	mustJoin(t, m.Addr(), "Steve")
	expectWake(t, wakes, "Steve")
	m.Close()
	mu.Lock()
	defer mu.Unlock()
	all := strings.Join(logs, "\n")
	if strings.Contains(all, "127.0.0.1") {
		t.Fatalf("logs name an address:\n%s", all)
	}
	for _, want := range []string{"Mallory tried to join but may not wake the server", "Steve tried to join, waking the server", "handed port"} {
		if !strings.Contains(all, want) {
			t.Errorf("logs lack %q:\n%s", want, all)
		}
	}
}

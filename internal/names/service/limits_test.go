package service

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLimiterRefillsAndForgetsFullBuckets(t *testing.T) {
	clk := &testClock{t: testStart}
	l := newLimiter(3, 60, time.Hour, clk.Now)
	for i := range 3 {
		if ok, _ := l.allow("a"); !ok {
			t.Fatalf("event %d of a burst of 3 was refused", i+1)
		}
	}
	if ok, wait := l.allow("a"); ok || wait != time.Minute {
		t.Errorf("event 4: allowed %v, wait %v; want a minute", ok, wait)
	}
	if ok, _ := l.allow("b"); !ok {
		t.Error("another key was refused")
	}
	clk.Add(time.Minute)
	if ok, _ := l.allow("a"); !ok {
		t.Error("refused after the wait")
	}

	for i := range minGC - len(l.buckets) {
		l.allow(fmt.Sprint(i))
	}
	clk.Add(3 * time.Minute)
	l.allow("new")
	if len(l.buckets) != 1 || l.gcAt != minGC {
		t.Errorf("after collecting full buckets: %d buckets, next collection at %d", len(l.buckets), l.gcAt)
	}
}

func TestLimiterCollectsOnlyOnceTheMapHasDoubled(t *testing.T) {
	clk := &testClock{t: testStart}
	l := newLimiter(1, 1, time.Hour, clk.Now)
	for i := range 3 * minGC {
		l.allow(fmt.Sprint(i))
	}
	if len(l.buckets) != 3*minGC || l.gcAt != 4*minGC {
		t.Errorf("%d buckets, next collection at %d; want %d and %d", len(l.buckets), l.gcAt, 3*minGC, 4*minGC)
	}
}

func TestReservedNames(t *testing.T) {
	for _, name := range []string{"www", "names", "mail", "install", "api", "status", "playkeeper", "playkeeper-io", "myplaykeeper", "minecraft"} {
		if !reservedName(name) {
			t.Errorf("%s is not reserved", name)
		}
	}
	for _, name := range []string{"alice", "survival-world", "play", "keeper", "mc"} {
		if reservedName(name) {
			t.Errorf("%s is reserved", name)
		}
	}
}

func TestBlocklistReloadsWhenTheFileChanges(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := filepath.Join(t.TempDir(), "blocklist.txt")
	b := &blocklist{path: path}
	b.reload(log)
	if b.has("evil") {
		t.Error("a missing file blocks names")
	}
	if err := os.WriteFile(path, []byte("# comment\n Evil \nnot valid!\n-dash\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b.reload(log)
	if !b.has("evil") || b.has("not valid!") || b.has("-dash") || b.has("# comment") {
		t.Errorf("blocklist %v", b.names)
	}
	if err := os.WriteFile(path, []byte("grief\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	b.reload(log)
	if b.has("evil") || !b.has("grief") {
		t.Errorf("after the file changed: %v", b.names)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	b.reload(log)
	if b.has("grief") {
		t.Error("a removed file still blocks names")
	}
}

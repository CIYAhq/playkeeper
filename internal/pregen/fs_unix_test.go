//go:build unix

package pregen

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/gamefiles"
)

func TestWriteConfigStaysInDataDir(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "config.yml")
	if err := os.WriteFile(secret, []byte("secret: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "plugins")); err != nil {
		t.Fatal(err)
	}
	wantCode(t, WriteConfig(dir, Bukkit, Config{}, nil), CodeFileRefused)

	dir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "plugins", "Chunky"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "plugins", "Chunky", "config.yml")); err != nil {
		t.Fatal(err)
	}
	wantCode(t, WriteConfig(dir, Bukkit, Config{}, nil), CodeFileRefused)

	if got := readFile(t, secret); got != "secret: 1\n" {
		t.Errorf("file outside the data directory changed to %q", got)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 1 {
		t.Errorf("files created outside the data directory: %v", entries)
	}

	dir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "plugins", "Chunky", "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "plugins", "Chunky", "tasks", "world.properties")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadTask(dir, Bukkit, "world"); err == nil {
		t.Error("task file outside the data directory was read")
	}
}

// A link that stays inside the data directory is refused as well, as
// internal/gamefiles refuses it, so the refusal names the link to fix and
// what it leads to is neither read nor changed.
func TestLinksInsideTheDataDirAreNotFollowed(t *testing.T) {
	dir := t.TempDir()
	elsewhere := filepath.Join(dir, "elsewhere")
	makeJar(t, filepath.Join(elsewhere, "Chunky.jar"), map[string]string{"plugin.yml": fixture(t, "plugin.yml")})
	if err := os.WriteFile(filepath.Join(elsewhere, "config.yml"), []byte("language: de\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(elsewhere, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(elsewhere, "tasks", "world.properties"), []byte("world=world\ncancelled=false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(dir, "plugins")

	if err := os.Symlink("elsewhere", plugins); err != nil {
		t.Fatal(err)
	}
	e := wantCode(t, WriteConfig(dir, Bukkit, Config{}, nil), CodeFileRefused)
	if e.Msg != "Playkeeper could not update Chunky's settings. plugins in the server's files is a link, which Playkeeper does not follow." ||
		e.Hint == "" || e.Params["path"] != "plugins" || e.Params["kind"] != "link" {
		t.Errorf("refusal = %+v", e)
	}
	_, err := Detect(dir, Bukkit)
	if e := wantCode(t, err, CodeFileRefused); e.Params["kind"] != "link" {
		t.Errorf("Detect through a linked plugins folder = %+v", e)
	}

	if err := os.Remove(plugins); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(plugins, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../elsewhere", filepath.Join(plugins, "Chunky")); err != nil {
		t.Fatal(err)
	}
	wantCode(t, WriteConfig(dir, Bukkit, Config{}, nil), CodeFileRefused)
	_, _, err = ReadTask(dir, Bukkit, "world")
	wantCode(t, err, CodeFileRefused)

	if err := os.Symlink("../elsewhere/Chunky.jar", filepath.Join(plugins, "Chunky.jar")); err != nil {
		t.Fatal(err)
	}
	if _, err := Detect(dir, Bukkit); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("Detect followed a linked jar: %v", err)
	}

	if got := readFile(t, filepath.Join(elsewhere, "config.yml")); got != "language: de\n" {
		t.Errorf("the file a link leads to changed to %q", got)
	}
}

func TestSpecialFilesDoNotBlock(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "plugins", "Chunky", "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "plugins", "Chunky", "config.yml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "plugins", "Chunky", "tasks", "world.properties"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "plugins", "pipe.jar"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := WriteConfig(dir, Bukkit, Config{}, nil); err == nil {
			t.Error("config written over a FIFO")
		}
		if _, _, err := ReadTask(dir, Bukkit, "world"); err == nil {
			t.Error("task read from a FIFO")
		}
		if _, err := Detect(dir, Bukkit); err == nil {
			t.Error("Chunky detected in a FIFO")
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}
}

func TestWriteConfigOwner(t *testing.T) {
	owner := gamefiles.Owner{UID: os.Getuid(), GID: os.Getgid()}
	if os.Geteuid() == 0 {
		owner = gamefiles.Owner{UID: 4242, GID: 4243}
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfig(dir, Fabric, Config{}, &owner); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"config/chunky", "config/chunky/config.json"} {
		st, err := os.Lstat(filepath.Join(dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		sys := st.Sys().(*syscall.Stat_t)
		if int(sys.Uid) != owner.UID || int(sys.Gid) != owner.GID {
			t.Errorf("%s is owned by %d:%d, want %d:%d", rel, sys.Uid, sys.Gid, owner.UID, owner.GID)
		}
	}
}

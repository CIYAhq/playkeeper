package backup

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestMeasureMatchesCreate(t *testing.T) {
	d := fixtureDataDir(t)
	size, err := Measure(d, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	arch, m := createArchive(t, d)
	if size.Files != len(m.Files) || size.Bytes != m.TotalBytes {
		t.Errorf("Measure says %d files, %d bytes; the archive has %d, %d", size.Files, size.Bytes, len(m.Files), m.TotalBytes)
	}
	if size.DiskBytes < size.Bytes+int64(size.Files)*blockSize/2 {
		t.Errorf("a copy's footprint %d is not rounded up to blocks (%d bytes in %d files)", size.DiskBytes, size.Bytes, size.Files)
	}
	if int64(len(arch)) > size.ArchiveBytes() {
		t.Errorf("the archive is %d bytes, over the %d bound", len(arch), size.ArchiveBytes())
	}
}

func TestMeasureRefusesWhatCreateRefuses(t *testing.T) {
	d := fixtureDataDir(t)
	write(t, d, "world/region/r.0.0\n.mca", "x")
	_, err := Measure(d, DefaultLimits())
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.File != "world/region/r.0.0\n.mca" {
		t.Fatalf("got %v, want a refusal of the file", err)
	}
}

func stageFixture(t *testing.T, inject func(d, step string) error) (dataDir, staged string, err error) {
	t.Helper()
	dataDir, staged = fixtureDataDir(t), t.TempDir()
	s := &stager{retryDelay: time.Millisecond}
	if inject != nil {
		s.inject = func(step string) error { return inject(dataDir, step) }
	}
	return dataDir, staged, s.copyAll(context.Background(), dataDir, staged)
}

func TestStagingCopyHoldsTheAllowlistWithoutSecrets(t *testing.T) {
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	d, staged, err := stageFixture(t, func(d, step string) error {
		if step == "open world/level.dat" {
			return os.Chtimes(filepath.Join(d, "world", "level.dat"), old, old)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := archiveFiles(d, LevelName(d))
	var got []string
	filepath.WalkDir(staged, func(p string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			rel, _ := filepath.Rel(staged, p)
			got = append(got, filepath.ToSlash(rel))
		}
		return err
	})
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("staged %q, want the allowlist %q", got, want)
	}
	props, _ := os.ReadFile(filepath.Join(staged, "server.properties"))
	if strings.Contains(string(props), "hunter2") || strings.Contains(string(props), "abc123secret") || !strings.Contains(string(props), "level-name=world") {
		t.Errorf("staged server.properties: %q", props)
	}
	st, err := os.Stat(filepath.Join(staged, "world", "level.dat"))
	if err != nil || !st.ModTime().Equal(old) || st.Mode().Perm() != 0o600 {
		t.Errorf("staged level.dat: %v, mtime %v, mode %v", err, st.ModTime(), st.Mode())
	}
}

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestStagingCopiesAFileAgainWhenItChanges(t *testing.T) {
	changed := false
	_, staged, err := stageFixture(t, func(d, step string) error {
		if step == "copy world/level.dat" && !changed {
			changed = true
			appendTo(t, filepath.Join(d, "world", "level.dat"), " and more")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(staged, "world", "level.dat")); string(b) != "level-data and more" {
		t.Errorf("staged level.dat is %q", b)
	}
}

func TestStagingGivesUpOnAFileThatKeepsChanging(t *testing.T) {
	_, _, err := stageFixture(t, func(d, step string) error {
		if step == "copy plugins/spark/config.json" {
			appendTo(t, filepath.Join(d, "plugins", "spark", "config.json"), " ")
		}
		return nil
	})
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindFileChanging || e.File != "plugins/spark/config.json" {
		t.Fatalf("got %v, want the file reported as changing", err)
	}
}

func TestStagingLeavesOutAFileThatWentAway(t *testing.T) {
	_, staged, err := stageFixture(t, func(d, step string) error {
		if step == "open plugins/spark/config.json" {
			os.Remove(filepath.Join(d, "plugins", "spark", "config.json"))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(staged, "plugins", "spark", "config.json")); err == nil {
		t.Error("a deleted file was staged")
	}
}

// Minecraft replaces level.dat by renaming the old file away and then the new
// one in, so the file is briefly missing.
func TestStagingWaitsForAFileBeingReplaced(t *testing.T) {
	opens := 0
	_, staged, err := stageFixture(t, func(d, step string) error {
		if step != "open world/level.dat" {
			return nil
		}
		level := filepath.Join(d, "world", "level.dat")
		switch opens++; opens {
		case 1:
			write(t, d, "world/level.dat.tmp", "new level-data")
			return os.Rename(level, level+"_old")
		case 2:
			return os.Rename(level+".tmp", level)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(staged, "world", "level.dat")); string(b) != "new level-data" {
		t.Errorf("staged level.dat is %q", b)
	}
}

func TestStagingStopsWhenCancelled(t *testing.T) {
	d := fixtureDataDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	s := &stager{retryDelay: time.Millisecond, inject: func(step string) error {
		if step == "copy world/level.dat" {
			cancel()
		}
		return nil
	}}
	if err := s.copyAll(ctx, d, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

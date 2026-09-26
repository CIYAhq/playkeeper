package zipdir

import (
	"archive/zip"
	"bytes"
	"errors"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/zipdir/zipdirtest"
)

func buildZip(t *testing.T, names ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range names {
		if _, err := zw.Create(name); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The packs tests damage every field Check reads; these cover the limits
// and the zips crafted against archive/zip.
func TestCheck(t *testing.T) {
	jar := buildZip(t, "plugin.yml", "META-INF/MANIFEST.MF", "org/example/Plugin.class")
	if n, err := Check(bytes.NewReader(jar), int64(len(jar)), Metadata); err != nil || n != 3 {
		t.Fatalf("Check of a jar = %d, %v", n, err)
	}
	for _, tc := range []struct {
		name string
		zip  []byte
		lim  Limits
		want Problem
	}{
		{"more entries than the limit", jar, Limits{Bytes: 1 << 20, Entries: 2}, TooManyEntries},
		{"a table of contents larger than the limit", jar, Limits{Bytes: 100, Entries: 10}, TooLarge},
		{"a count that wraps", zipdirtest.Bomb(65_537, false), Metadata, MoreEntries},
		{"entries by the hundred thousand", zipdirtest.Bomb(200_000, true), Metadata, TooManyEntries},
		{"a table of contents by the dozen megabytes", zipdirtest.Bomb(400_000, false), Metadata, TooLarge},
		{"too short", []byte("PK\x05\x06"), Metadata, NotZip},
		{"not a zip", bytes.Repeat([]byte("jar"), 100), Metadata, NotZip},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, err := Check(bytes.NewReader(tc.zip), int64(len(tc.zip)), tc.lim)
			var e *Error
			if !errors.As(err, &e) || e.Problem != tc.want {
				t.Fatalf("Check = %d, %v; want problem %d", n, err, tc.want)
			}
		})
	}
}

type brokenDisk struct{}

var errBroken = errors.New("input/output error")

func (brokenDisk) ReadAt([]byte, int64) (int, error) { return 0, errBroken }

func TestCheckReturnsReadErrors(t *testing.T) {
	if _, err := Check(brokenDisk{}, 1000, Metadata); err != errBroken {
		t.Errorf("Check = %v, want the read's error", err)
	}
}

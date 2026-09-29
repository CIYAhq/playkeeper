package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The world is where server.properties says, and in world when it doesn't
// say, or names a folder outside the server's.
func TestLevelNameIn(t *testing.T) {
	for _, c := range []struct{ properties, want string }{
		{"motd=A Minecraft Server\nlevel-name=world\n", "world"},
		{"level-name = skyblock\r\nlevel-seed=\n", "skyblock"},
		{"level-name=\n", "world"},
		{"level-name=../elsewhere\n", "world"},
		{"#level-name=old\n", "world"},
		{"", "world"},
	} {
		if got := levelNameIn(c.properties); got != c.want {
			t.Errorf("levelNameIn(%q) = %q; want %q", c.properties, got, c.want)
		}
	}
}

// A folder's zip unpacks into the folder, and a zip naming a file outside
// it is refused.
func TestUnzipStaysInItsFolder(t *testing.T) {
	zipOf := func(names ...string) *zip.Reader {
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		for _, n := range names {
			w, err := zw.Create(n)
			if err != nil {
				t.Fatal(err)
			}
			w.Write([]byte(n))
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		zr, err := zip.NewReader(bytes.NewReader(b.Bytes()), int64(b.Len()))
		if err != nil {
			t.Fatal(err)
		}
		return zr
	}
	dir := t.TempDir()
	if err := unzip(zipOf("region/", "region/r.0.0.mca", "region/r.-1.0.mca"), dir); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "region", "r.-1.0.mca")); err != nil || string(b) != "region/r.-1.0.mca" {
		t.Errorf("region/r.-1.0.mca = %q, %v", b, err)
	}
	if err := unzip(zipOf("../outside"), dir); err == nil {
		t.Error("a zip with ../outside unpacked")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "outside")); err == nil {
		t.Error("../outside was written")
	}
}

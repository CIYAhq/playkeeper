package sleep

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeIcon(t *testing.T, dir string, b []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, iconFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadIcon(t *testing.T) {
	dir := t.TempDir()
	if got, err := ReadIcon(dir); got != "" || err != nil {
		t.Fatalf("without an icon: %q, %v", got, err)
	}
	b := iconPNG(t, 64, 64)
	writeIcon(t, dir, b)
	got, err := ReadIcon(dir)
	if err != nil || got != iconPrefix+base64.StdEncoding.EncodeToString(b) {
		t.Fatalf("got %.40q…, %v", got, err)
	}
	if st := (Status{Icon: got}).clean(); st.Icon != got {
		t.Fatal("Status dropped an icon ReadIcon returned")
	}
}

func TestReadIconRefuses(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
		code  string
	}{
		{"wrong size", func(t *testing.T, dir string) { writeIcon(t, dir, iconPNG(t, 32, 32)) }, "icon_invalid"},
		{"not a PNG", func(t *testing.T, dir string) { writeIcon(t, dir, []byte("GIF89a not a png at all")) }, "icon_invalid"},
		{"empty", func(t *testing.T, dir string) { writeIcon(t, dir, nil) }, "icon_invalid"},
		{"over 20 KiB", func(t *testing.T, dir string) {
			writeIcon(t, dir, append(iconPNG(t, 64, 64), make([]byte, maxIconBytes)...))
		}, "icon_too_large"},
		{"link", func(t *testing.T, dir string) {
			other := t.TempDir()
			writeIcon(t, other, iconPNG(t, 64, 64))
			if err := os.Symlink(filepath.Join(other, iconFile), filepath.Join(dir, iconFile)); err != nil {
				t.Fatal(err)
			}
		}, "icon_invalid"},
		{"folder", func(t *testing.T, dir string) {
			if err := os.Mkdir(filepath.Join(dir, iconFile), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "icon_invalid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			c.setup(t, dir)
			got, err := ReadIcon(dir)
			var e *Error
			if !errors.As(err, &e) || e.Code != c.code || got != "" {
				t.Fatalf("got %.40q, %#v, want code %s", got, err, c.code)
			}
			if e.Msg == "" {
				t.Fatal("no message")
			}
		})
	}
}

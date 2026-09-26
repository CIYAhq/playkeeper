package web

import (
	"io/fs"
	"testing"
	"testing/fstest"
)

func TestDistIsTheBuiltUIOnlyOnceItHasAnIndex(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	if dist(fstest.MapFS{"dist_placeholder.txt": file("")}) != nil {
		t.Fatal("before make web there is no UI to serve")
	}
	if dist(fstest.MapFS{"dist/assets/index-a1b2.js": file("js")}) != nil {
		t.Fatal("a build without index.html is not a UI")
	}
	d := dist(fstest.MapFS{"dist/index.html": file("<!doctype html>"), "dist/assets/index-a1b2.js": file("js")})
	if d == nil {
		t.Fatal("a build with index.html is the UI")
	}
	for name, want := range map[string]string{"index.html": "<!doctype html>", "assets/index-a1b2.js": "js"} {
		if b, err := fs.ReadFile(d, name); err != nil || string(b) != want {
			t.Fatalf("%s: %q %v", name, b, err)
		}
	}
}

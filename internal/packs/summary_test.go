package packs

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// pngOf is a w×h PNG picture.
func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.NRGBA{R: 0x3c, G: 0x8d, B: 0x3f, A: 0xff})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func summarize(b []byte, use Kind) Summary {
	return Summarize(context.Background(), bytes.NewReader(b), int64(len(b)), use)
}

func packIcon(b []byte) ([]byte, error) {
	return PackIcon(context.Background(), bytes.NewReader(b), int64(len(b)))
}

func TestSummarize(t *testing.T) {
	icon := pngOf(t, 64, 64)
	for _, tc := range []struct {
		name string
		zip  []byte
		use  Kind
		want Summary
	}{
		{"data pack", dataPack(t, file("pack.png", string(icon))), Data, Summary{"A test data pack", true}},
		{"resource pack", resourcePack(t, file("pack.png", string(icon))), Resource, Summary{"A test resource pack", true}},
		{"no icon", dataPack(t), Data, Summary{"A test data pack", false}},
		{"formatted description", buildZip(t, file("pack.mcmeta", `{"pack": {"description": [{"text": "§6Graves", "bold": true}, "\nKeeps your items"], "pack_format": 48}}`)), Data, Summary{"Graves\nKeeps your items", false}},
		{"icon too large", dataPack(t, file("pack.png", strings.Repeat("x", maxIconBytes+1))), Data, Summary{"A test data pack", false}},
		{"icon in a folder", dataPack(t, file("assets/pack.png", string(icon))), Data, Summary{"A test data pack", false}},
		{"broken pack.mcmeta", buildZip(t, file("pack.mcmeta", `{"pack": `), file("pack.png", string(icon))), Data, Summary{"", true}},
		{"no pack.mcmeta", buildZip(t, file("data/test/function/a.mcfunction", "")), Data, Summary{}},
		{"encrypted pack.mcmeta", buildZip(t, entry{name: "pack.mcmeta", body: dataMcmeta, edit: func(fh *zip.FileHeader) { fh.Flags |= flagEncrypted }}), Data, Summary{}},
		{"not a zip", []byte("PK\x03\x04 but not really"), Data, Summary{}},
		{"empty", nil, Data, Summary{}},
	} {
		if got := summarize(tc.zip, tc.use); got != tc.want {
			t.Errorf("%s: Summarize = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestPackIcon(t *testing.T) {
	icon := pngOf(t, 128, 128)
	got, err := packIcon(dataPack(t, file("pack.png", string(icon))))
	if err != nil || !bytes.Equal(got, icon) {
		t.Fatalf("PackIcon = %d bytes, %v", len(got), err)
	}
	if got, err := packIcon(dataPack(t, file("pack.png", string(pngOf(t, maxIconSide, 16))))); err != nil || len(got) == 0 {
		t.Errorf("PackIcon of a %d-pixel-wide icon: %v", maxIconSide, err)
	}
	for name, zip := range map[string][]byte{
		"missing":   dataPack(t),
		"text":      dataPack(t, file("pack.png", "not a picture")),
		"jpeg":      dataPack(t, file("pack.png", "\xff\xd8\xff\xe0\x00\x10JFIF")),
		"too wide":  dataPack(t, file("pack.png", string(pngOf(t, maxIconSide+1, 1)))),
		"truncated": dataPack(t, file("pack.png", string(icon[:len(icon)-20]))),
		"too large": dataPack(t, file("pack.png", string(icon)+strings.Repeat("\x00", maxIconBytes))),
		"not a zip": []byte("not a zip at all, but long enough to look for one"),
	} {
		if _, err := packIcon(zip); !errors.Is(err, ErrNoIcon) {
			t.Errorf("PackIcon of a %s icon = %v, want %s", name, err, CodeNoIcon)
		}
	}
}

func TestDataPackSummary(t *testing.T) {
	ctx := context.Background()
	d := DataPacks{DataDir: t.TempDir(), Level: "world"}
	folder := filepath.Join(d.DataDir, "world", "datapacks")
	icon := pngOf(t, 32, 32)
	zipped := dataPack(t, file("pack.png", string(icon)))
	writeFile(t, filepath.Join(folder, "graves.zip"), string(zipped))
	writeFile(t, filepath.Join(folder, "Loose Pack", "pack.mcmeta"), `{"pack": {"description": "Unpacked by hand", "pack_format": 48}}`)
	writeFile(t, filepath.Join(folder, "Loose Pack", "pack.png"), string(icon))
	writeFile(t, filepath.Join(folder, "Bare", "pack.mcmeta"), `{"pack": {"description": "No icon", "pack_format": 48}}`)
	writeFile(t, filepath.Join(folder, "notes.txt"), "not a pack")

	for name, want := range map[string]Summary{
		"graves.zip": {"A test data pack", true},
		"Loose Pack": {"Unpacked by hand", true},
		"Bare":       {"No icon", false},
	} {
		got, err := d.Summary(ctx, name)
		if err != nil || got != want {
			t.Errorf("Summary(%q) = %+v, %v, want %+v", name, got, err, want)
		}
	}
	for _, name := range []string{"graves.zip", "Loose Pack"} {
		if got, err := d.Icon(ctx, name); err != nil || !bytes.Equal(got, icon) {
			t.Errorf("Icon(%q) = %d bytes, %v", name, len(got), err)
		}
	}
	_, err := d.Icon(ctx, "Bare")
	wantCode(t, err, CodeNoIcon)

	for _, name := range []string{"missing.zip", "notes.txt"} {
		_, err := d.Summary(ctx, name)
		wantCode(t, err, CodeNotFound)
		_, err = d.Icon(ctx, name)
		wantCode(t, err, CodeNotFound)
	}
	for _, name := range []string{"../world/datapacks/graves.zip", "", ".."} {
		_, err := d.Summary(ctx, name)
		wantCode(t, err, CodeInvalidName)
		_, err = d.Icon(ctx, name)
		wantCode(t, err, CodeInvalidName)
	}
	_, err = DataPacks{DataDir: d.DataDir, Level: "../world"}.Summary(ctx, "graves.zip")
	wantCode(t, err, CodeInvalidLevel)
}

func TestStoreIcon(t *testing.T) {
	ctx := context.Background()
	s := Store{Dir: filepath.Join(t.TempDir(), "resourcepacks")}
	icon := pngOf(t, 64, 64)
	pack := resourcePack(t, file("pack.png", string(icon)))
	info, err := s.Put(ctx, bytes.NewReader(pack), int64(len(pack)))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Icon(ctx, info.SHA1); err != nil || !bytes.Equal(got, icon) {
		t.Errorf("Icon = %d bytes, %v", len(got), err)
	}
	plain := resourcePack(t)
	bare, err := s.Put(ctx, bytes.NewReader(plain), int64(len(plain)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Icon(ctx, bare.SHA1)
	wantCode(t, err, CodeNoIcon)
	_, err = s.Icon(ctx, strings.Repeat("0", 40))
	wantCode(t, err, CodeNotFound)
	_, err = s.Icon(ctx, "../"+info.SHA1)
	wantCode(t, err, CodeInvalidOffer)
}

func TestStorePrune(t *testing.T) {
	ctx := context.Background()
	s := Store{Dir: filepath.Join(t.TempDir(), "resourcepacks")}
	if err := s.Prune(func(string) bool { return false }); err != nil {
		t.Fatalf("Prune without a folder: %v", err)
	}
	var sums []string
	for i := range 3 {
		pack := resourcePack(t, file("assets/test/lang/en_us.json", strings.Repeat("{}", i+1)))
		info, err := s.Put(ctx, bytes.NewReader(pack), int64(len(pack)))
		if err != nil {
			t.Fatal(err)
		}
		sums = append(sums, info.SHA1)
	}
	old := time.Now().Add(-2 * leftoverAge)
	for name, mtime := range map[string]time.Time{
		"." + sums[0] + ".zip.playkeeper-0a1b2c3d4e5f": old,
		"." + sums[1] + ".zip.playkeeper-0a1b2c3d4e5f": time.Now(),
		"notes.txt":                       old,
		"not-a-hash.zip":                  old,
		strings.ToUpper(sums[2]) + ".zip": old,
	} {
		p := filepath.Join(s.Dir, name)
		writeFile(t, p, "left over")
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	var asked []string
	err := s.Prune(func(sum string) bool {
		asked = append(asked, sum)
		return sum == sums[1]
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(asked)
	want := slices.Sorted(slices.Values(sums))
	if !slices.Equal(asked, want) {
		t.Errorf("Prune asked about %q, want %q", asked, want)
	}
	got := dirNames(t, s.Dir)
	wantLeft := []string{"." + sums[1] + ".zip.playkeeper-0a1b2c3d4e5f", sums[1] + ".zip", strings.ToUpper(sums[2]) + ".zip", "not-a-hash.zip", "notes.txt"}
	slices.Sort(wantLeft)
	if !slices.Equal(got, wantLeft) {
		t.Errorf("after Prune the folder holds %q, want %q", got, wantLeft)
	}
}

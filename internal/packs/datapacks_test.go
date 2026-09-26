package packs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// changingReaderAt reads b, whose middle byte changes at read number
// change, like a file overwritten while it is read. A negative change
// never comes.
type changingReaderAt struct {
	b      []byte
	change int
	reads  int
}

func (c *changingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if c.reads == c.change {
		c.b[len(c.b)/2] ^= 0xff
	}
	c.reads++
	return bytes.NewReader(c.b).ReadAt(p, off)
}

// readsToInspect is how many reads Inspect makes of b.
func readsToInspect(t *testing.T, b []byte, use Kind) int {
	t.Helper()
	r := &changingReaderAt{b: b, change: -1}
	if _, err := Inspect(context.Background(), r, int64(len(b)), use, Limits{}); err != nil {
		t.Fatal(err)
	}
	return r.reads
}

func writeFile(t *testing.T, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckName(t *testing.T) {
	for _, name := range []string{"terralith.zip", "Terralith_2.6.4+mc26.3.zip", "a.zip", "_x.zip", "+x.zip", "9.zip", "a..b.zip", strings.Repeat("n", 96) + ".zip"} {
		if err := CheckName(name); err != nil {
			t.Errorf("CheckName(%q) = %v", name, err)
		}
	}
	for _, name := range []string{"", ".zip", "x", "x.ZIP", "x.zip.txt", "../x.zip", "a/b.zip", `a\b.zip`, "a b.zip", ".hidden.zip", "-x.zip", "ü.zip", "x.zip\n", strings.Repeat("n", 97) + ".zip"} {
		if e := wantCode(t, CheckName(name), CodeInvalidName); e.Params["name"] != shortName(name) {
			t.Errorf("CheckName(%q) params %v", name, e.Params)
		}
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"Terralith_2.6.4.zip":                   "Terralith_2.6.4.zip",
		`C:\Users\me\Downloads\My Pack (1).zip`: "My_Pack_1.zip",
		"/tmp/x/../pack.ZIP":                    "pack.zip",
		"pack.tar.gz":                           "pack.tar.gz.zip",
		"ünïcödé.zip":                           "n_c_d.zip",
		"---a.zip":                              "a.zip",
		".hidden.zip":                           "hidden.zip",
		"._.zip":                                "_.zip",
		"trailing....zip":                       "trailing.zip",
		"a\x00b.zip":                            "a_b.zip",
		"..zip":                                 "datapack.zip",
		"-.zip":                                 "datapack.zip",
		"":                                      "datapack.zip",
		strings.Repeat("a", 200) + ".zip":       strings.Repeat("a", 96) + ".zip",
		strings.Repeat("a", 95) + "..b.zip":     strings.Repeat("a", 95) + ".zip",
	} {
		got := SafeName(in)
		if got != want {
			t.Errorf("SafeName(%q) = %q, want %q", in, got, want)
		}
		if err := CheckName(got); err != nil {
			t.Errorf("CheckName(SafeName(%q)) = %v", in, err)
		}
	}
}

func TestInstall(t *testing.T) {
	ctx := context.Background()
	d := DataPacks{DataDir: t.TempDir(), Level: "world"}
	folder := filepath.Join(d.DataDir, "world", "datapacks")
	pack := dataPack(t)
	info, dp, err := d.Install(ctx, "test.zip", bytes.NewReader(pack), int64(len(pack)), false)
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != Data || dp.Name != "test.zip" || dp.ID != "file/test.zip" || dp.Size != int64(len(pack)) || dp.ModTime.IsZero() || dp.Folder {
		t.Errorf("Install = %+v, %+v", info, dp)
	}
	target := filepath.Join(folder, "test.zip")
	if got, err := os.ReadFile(target); err != nil || !bytes.Equal(got, pack) {
		t.Errorf("installed file: %v", err)
	}
	if st, err := os.Stat(target); err != nil || st.Mode() != 0o644 {
		t.Errorf("installed file mode: %v, %v", st.Mode(), err)
	}
	if names := dirNames(t, folder); !slices.Equal(names, []string{"test.zip"}) {
		t.Errorf("datapacks folder holds %q", names)
	}

	other := dataPack(t, file("data/test/function/bye.mcfunction", "say bye\n"))
	_, _, err = d.Install(ctx, "test.zip", bytes.NewReader(other), int64(len(other)), false)
	if e := wantCode(t, err, CodeAlreadyInstalled); e.Params["name"] != "test.zip" {
		t.Errorf("params %v", e.Params)
	}
	if got, _ := os.ReadFile(target); !bytes.Equal(got, pack) {
		t.Error("Install without replace changed the installed pack")
	}
	if _, _, err := d.Install(ctx, "test.zip", bytes.NewReader(other), int64(len(other)), true); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); !bytes.Equal(got, other) {
		t.Error("Install with replace kept the old pack")
	}
	if names := dirNames(t, folder); !slices.Equal(names, []string{"test.zip"}) {
		t.Errorf("datapacks folder holds %q", names)
	}
}

func TestInstallRefusals(t *testing.T) {
	ctx := context.Background()
	pack := dataPack(t)
	install := func(d DataPacks, name string, b []byte) error {
		_, _, err := d.Install(ctx, name, bytes.NewReader(b), int64(len(b)), true)
		return err
	}

	d := DataPacks{DataDir: t.TempDir(), Level: "world"}
	wantCode(t, install(d, "../x.zip", pack), CodeInvalidName)
	wantCode(t, install(d, "x.zip", resourcePack(t)), CodeWrongKind)
	for _, level := range []string{"", ".", "..", "a/b", `a\b`, "a\nb"} {
		wantCode(t, install(DataPacks{DataDir: d.DataDir, Level: level}, "x.zip", pack), CodeInvalidLevel)
	}
	if names := dirNames(t, d.DataDir); len(names) != 0 {
		t.Errorf("refused installs left %q", names)
	}

	if err := os.MkdirAll(filepath.Join(d.DataDir, "world", "datapacks", "folder.zip"), 0o755); err != nil {
		t.Fatal(err)
	}
	wantCode(t, install(d, "folder.zip", pack), CodeFolderPack)

	wantCode(t, install(DataPacks{DataDir: filepath.Join(d.DataDir, "missing"), Level: "world"}, "x.zip", pack), CodeFileFailed)
	writeFile(t, filepath.Join(d.DataDir, "file-world"), "")
	e := wantCode(t, install(DataPacks{DataDir: d.DataDir, Level: "file-world"}, "x.zip", pack), CodeFileRefused)
	if e.Msg != "Playkeeper couldn't install the data pack. file-world in the server's files is not a folder." {
		t.Errorf("message %q", e.Msg)
	}
}

func TestInstallPackChanging(t *testing.T) {
	pack := dataPack(t)
	r := &changingReaderAt{b: slices.Clone(pack), change: readsToInspect(t, pack, Data)}
	d := DataPacks{DataDir: t.TempDir(), Level: "world"}
	_, _, err := d.Install(context.Background(), "test.zip", r, int64(len(pack)), false)
	if e := wantCode(t, err, CodeFileFailed); e.Params["detail"] != "the pack changed while it was being copied" {
		t.Errorf("params %v", e.Params)
	}
	if names := dirNames(t, filepath.Join(d.DataDir, "world", "datapacks")); len(names) != 0 {
		t.Errorf("datapacks folder holds %q", names)
	}
}

func TestList(t *testing.T) {
	d := DataPacks{DataDir: t.TempDir(), Level: "world"}
	packs, err := d.List()
	if err != nil || packs == nil || len(packs) != 0 {
		t.Errorf("List of a world without a datapacks folder = %#v, %v", packs, err)
	}

	folder := filepath.Join(d.DataDir, "world", "datapacks")
	writeFile(t, filepath.Join(folder, "b.zip"), "bbbbb")
	writeFile(t, filepath.Join(folder, "a.zip"), "aaa")
	writeFile(t, filepath.Join(folder, "notes.txt"), "")
	writeFile(t, filepath.Join(folder, "upper.ZIP"), "")
	writeFile(t, filepath.Join(folder, "unpacked", "pack.mcmeta"), dataMcmeta)
	writeFile(t, filepath.Join(folder, "c.zip", "pack.mcmeta"), dataMcmeta)
	if err := os.MkdirAll(filepath.Join(folder, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(folder, "odd", "pack.mcmeta"), 0o755); err != nil {
		t.Fatal(err)
	}
	packs, err = d.List()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range packs {
		got = append(got, p.Name)
		if p.ID != DataPackID(p.Name) || p.ModTime.IsZero() {
			t.Errorf("pack %+v", p)
		}
	}
	if want := []string{"a.zip", "b.zip", "c.zip", "unpacked"}; !slices.Equal(got, want) {
		t.Fatalf("List = %q, want %q", got, want)
	}
	if packs[0].Size != 3 || packs[0].Folder || packs[1].Size != 5 || !packs[2].Folder || packs[2].Size != 0 || !packs[3].Folder {
		t.Errorf("List = %+v", packs)
	}

	wantCode(t, func() error { _, err := (DataPacks{DataDir: d.DataDir, Level: "../x"}).List(); return err }(), CodeInvalidLevel)
	_, err = DataPacks{DataDir: filepath.Join(d.DataDir, "missing"), Level: "world"}.List()
	wantCode(t, err, CodeFileFailed)
}

// The game can replace the datapacks folder with a named pipe, which would
// hold List in open(2) until something writes to it.
func TestListDoesNotWaitOnAPipe(t *testing.T) {
	d := DataPacks{DataDir: t.TempDir(), Level: "world"}
	pipe := filepath.Join(d.DataDir, "world", "datapacks")
	if err := errors.Join(os.Mkdir(filepath.Dir(pipe), 0o755), syscall.Mkfifo(pipe, 0o644)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := d.List()
		done <- err
	}()
	select {
	case err := <-done:
		wantCode(t, err, CodeFileRefused)
	case <-time.After(5 * time.Second):
		if fd, err := syscall.Open(pipe, syscall.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			syscall.Close(fd)
		}
		<-done
		t.Fatal("List waited on the pipe")
	}
}

func TestRemove(t *testing.T) {
	d := DataPacks{DataDir: t.TempDir(), Level: "world"}
	folder := filepath.Join(d.DataDir, "world", "datapacks")
	writeFile(t, filepath.Join(folder, "a.zip"), "a")
	writeFile(t, filepath.Join(folder, "notes.txt"), "")
	writeFile(t, filepath.Join(folder, "unpacked", "pack.mcmeta"), dataMcmeta)

	if err := d.Remove("a.zip"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(folder, "a.zip")); !os.IsNotExist(err) {
		t.Errorf("a.zip is still there: %v", err)
	}
	if e := wantCode(t, d.Remove("a.zip"), CodeNotFound); e.Params["name"] != "a.zip" {
		t.Errorf("params %v", e.Params)
	}
	wantCode(t, d.Remove("notes.txt"), CodeNotFound)
	wantCode(t, d.Remove("unpacked"), CodeFolderPack)
	if names := dirNames(t, folder); !slices.Equal(names, []string{"notes.txt", "unpacked"}) {
		t.Errorf("datapacks folder holds %q", names)
	}
	for _, name := range []string{"", ".", "..", "a/b.zip", `a\b.zip`, "x\n.zip", "\xff.zip", strings.Repeat("a", 256)} {
		wantCode(t, d.Remove(name), CodeInvalidName)
	}
	wantCode(t, DataPacks{DataDir: d.DataDir, Level: "../x"}.Remove("a.zip"), CodeInvalidLevel)
	wantCode(t, DataPacks{DataDir: d.DataDir, Level: "other"}.Remove("a.zip"), CodeNotFound)
}

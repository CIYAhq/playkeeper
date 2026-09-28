package checks

import (
	"os"
	"strings"
	"testing"
)

func TestChecksRoundTripAndRefuseWhatNoCheckWrites(t *testing.T) {
	dir := t.TempDir()
	good := &Check{Status: Passing, Checked: "2026-09-28", Release: "0.4.2", Build: "129", DoneSeconds: 12.5,
		Addons: []Addon{{Name: "Chunky", Source: "modrinth", Slug: "chunky", Version: "1.5.3", Licence: "GPL-3.0-only", Downloads: 1}}}
	if err := Write(dir, "smp", good); err != nil {
		t.Fatal(err)
	}
	read, err := Read(os.DirFS(dir), ".")
	if err != nil || read["smp"] == nil || read["smp"].Addons[0].Version != "1.5.3" {
		t.Fatalf("read back %+v, %v", read, err)
	}
	for name, c := range map[string]Check{
		"no status":                {Checked: "2026-09-28", Release: "0.4.2", DoneSeconds: 1},
		"failing without a reason": {Status: Failing, Checked: "2026-09-28", Release: "0.4.2"},
		"a development build":      {Status: Passing, Checked: "2026-09-28", Release: "dev", DoneSeconds: 1},
		"passing without seconds":  {Status: Passing, Checked: "2026-09-28", Release: "0.4.2"},
		"no day":                   {Status: Passing, Checked: "28 Sep", Release: "0.4.2", DoneSeconds: 1},
		"an add-on without version": {Status: Passing, Checked: "2026-09-28", Release: "0.4.2", DoneSeconds: 1,
			Addons: []Addon{{Name: "Chunky", Source: "modrinth", Slug: "chunky"}}},
	} {
		if err := Write(dir, "bad", &c); err == nil {
			t.Errorf("%s was written", name)
		}
	}
	if err := os.WriteFile(dir+"/typo.json", []byte(`{"status": "passing", "checkd": "2026-09-28"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(os.DirFS(dir), "."); err == nil || !strings.Contains(err.Error(), "typo.json") {
		t.Errorf("a file with an unknown field read: %v", err)
	}
	if got, err := Read(os.DirFS(dir), "missing"); err != nil || len(got) != 0 {
		t.Errorf("a missing folder is no checks: %v %v", got, err)
	}
}

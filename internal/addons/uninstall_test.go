package addons

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestUninstallKeepsSettingsUnlessAsked(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	plugins := filepath.Join(srv.Dir, "plugins")
	settings := filepath.Join(plugins, "Chunky", "config.yml")

	installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "chunky"})
	writeFile(t, settings, []byte("radius: 5000\n"))
	rm, err := l.Uninstall(srv, installed, installed[0].Key(), UninstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "removal", rm, &Removal{Removed: installed[0], ConfigFolder: "Chunky", Orphans: []Installed{}, Warnings: []Notice{}, RestartNeeded: true})
	if got := ls(t, plugins); !slices.Equal(got, []string{"Chunky"}) || string(readFile(t, settings)) != "radius: 5000\n" {
		t.Errorf("plugins holds %v", got)
	}

	installed = mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "chunky"})
	rm, err = l.Uninstall(srv, installed, installed[0].Key(), UninstallOptions{RemoveConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	if rm.ConfigFolder != "Chunky" || !rm.ConfigRemoved || len(rm.Warnings) != 0 {
		t.Errorf("removal %+v", rm)
	}
	if got := ls(t, plugins); got != nil {
		t.Errorf("plugins holds %v", got)
	}
}

// A link where the settings folder would be may point anywhere, even out
// of the server.
func TestUninstallLeavesALinkedSettingsFolderAlone(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "config.yml"), []byte("keep me\n"))
	installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "chunky"})
	if err := os.Symlink(outside, filepath.Join(srv.Dir, "plugins", "Chunky")); err != nil {
		t.Fatal(err)
	}

	rm, err := l.Uninstall(srv, installed, installed[0].Key(), UninstallOptions{RemoveConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	if rm.ConfigFolder != "" || rm.ConfigRemoved {
		t.Errorf("removal %+v", rm)
	}
	if string(readFile(t, filepath.Join(outside, "config.yml"))) != "keep me\n" {
		t.Error("the folder the link points to was changed")
	}
	if got := ls(t, filepath.Join(srv.Dir, "plugins")); !slices.Equal(got, []string{"Chunky"}) {
		t.Errorf("plugins holds %v", got)
	}
}

func TestUninstallHangar(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Hangar, Project: "ViaBackwards"})
	writeFile(t, filepath.Join(srv.Dir, "plugins", "ViaBackwards", "config.yml"), []byte("enabled: true\n"))

	rm, err := l.Uninstall(srv, installed, installed[0].Key(), UninstallOptions{RemoveConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "removal", rm, &Removal{Removed: installed[0], ConfigFolder: "ViaBackwards", ConfigRemoved: true,
		Orphans: []Installed{installed[1]}, Warnings: []Notice{}, RestartNeeded: true})
	if got := ls(t, filepath.Join(srv.Dir, "plugins")); !slices.Equal(got, []string{"ViaVersion-5.12.0.jar"}) {
		t.Errorf("plugins holds %v", got)
	}
}

func TestUninstallOffersLeftoverDependencies(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	left := mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "viarewind"})
	rewind, backwards, via := left[0], left[1], left[2]

	for _, step := range []struct {
		rec     Installed
		orphans []Installed
	}{
		{rewind, []Installed{backwards}},
		{backwards, []Installed{via}},
		{via, []Installed{}},
	} {
		rm, err := l.Uninstall(srv, left, step.rec.Key(), UninstallOptions{})
		if err != nil {
			t.Fatalf("removing %s: %v", step.rec.Name, err)
		}
		sameJSON(t, "orphans after removing "+step.rec.Name, rm.Orphans, step.orphans)
		left = slices.DeleteFunc(slices.Clone(left), func(rec Installed) bool { return rec.Key() == step.rec.Key() })
	}
	if got := ls(t, filepath.Join(srv.Dir, "plugins")); got != nil {
		t.Errorf("plugins holds %v", got)
	}
}

func TestUninstallRefusesWhatOthersNeed(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	plugins := filepath.Join(srv.Dir, "plugins")
	all := mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "viarewind"})
	backwards, via := all[1], all[2]

	_, err := l.Uninstall(srv, all, via.Key(), UninstallOptions{})
	e := wantKind(t, err, KindNeededBy)
	if e.Msg != "ViaVersion is needed by ViaRewind and ViaBackwards." || e.Hint != "Remove ViaRewind and ViaBackwards first, or remove ViaVersion anyway." {
		t.Errorf("message %q, hint %q", e.Msg, e.Hint)
	}
	if got := ls(t, plugins); len(got) != 3 {
		t.Errorf("plugins holds %v", got)
	}

	// What counts is what the installed versions require, not what an
	// add-on was first installed for.
	all[0].Requires = []string{via.ProjectID}
	rm, err := l.Uninstall(srv, all, backwards.Key(), UninstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rm.Orphans) != 0 {
		t.Errorf("orphans %+v", rm.Orphans)
	}

	rm, err = l.Uninstall(srv, []Installed{all[0], via}, via.Key(), UninstallOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if rm.Removed.Key() != via.Key() || len(rm.Orphans) != 0 {
		t.Errorf("removal %+v", rm)
	}
	if got := ls(t, plugins); !slices.Equal(got, []string{"ViaRewind-4.2.0.jar"}) {
		t.Errorf("plugins holds %v", got)
	}
}

func TestUninstallRefusesChangedFiles(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, jar string, data []byte)
	}{
		{"edited", func(t *testing.T, jar string, data []byte) {
			writeFile(t, jar, append(slices.Clone(data), "patched"...))
		}},
		{"same size", func(t *testing.T, jar string, data []byte) {
			b := slices.Clone(data)
			b[len(b)-1] ^= 1
			writeFile(t, jar, b)
		}},
		{"link to a copy", func(t *testing.T, jar string, data []byte) {
			dup := filepath.Join(t.TempDir(), "Chunky.jar")
			writeFile(t, dup, data)
			if err := errors.Join(os.Remove(jar), os.Symlink(dup, jar)); err != nil {
				t.Fatal(err)
			}
		}},
		{"folder", func(t *testing.T, jar string, data []byte) {
			if err := errors.Join(os.Remove(jar), os.Mkdir(jar, 0o755)); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, "paper", "26.2")
			installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "chunky"})
			jar := filepath.Join(srv.Dir, "plugins", "Chunky-Bukkit-1.5.3.jar")
			tc.change(t, jar, readFile(t, jar))
			before, err := os.Lstat(jar)
			if err != nil {
				t.Fatal(err)
			}

			for _, force := range []bool{false, true} {
				_, err := l.Uninstall(srv, installed, installed[0].Key(), UninstallOptions{Force: force})
				e := wantKind(t, err, KindModified)
				if e.Msg != "Chunky-Bukkit-1.5.3.jar has changed since Playkeeper installed it, so Playkeeper will not delete it." {
					t.Errorf("message %q", e.Msg)
				}
			}
			if after, err := os.Lstat(jar); err != nil || after.Mode() != before.Mode() || after.Size() != before.Size() {
				t.Errorf("the file was touched: %v", err)
			}
		})
	}
}

func TestUninstallRefusals(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "chunky"})
	props := []byte("motd=A Minecraft Server\n")
	writeFile(t, filepath.Join(srv.Dir, "server.properties"), props)
	writeFile(t, filepath.Join(srv.Dir, "plugins", "ViaVersion.jar"), f.mfile("FaishMnD").data)

	_, err := l.Uninstall(srv, installed, Key{Modrinth, "P1OZGk5p"}, UninstallOptions{Force: true})
	if e := wantKind(t, err, KindNotManaged); e.Msg != "Playkeeper did not install this add-on, so it will not delete it." {
		t.Errorf("message %q", e.Msg)
	}

	for _, name := range []string{"../server.properties", "../server.properties.jar", "server.properties", ".Chunky.jar", "a/b.jar", ""} {
		rec := installed[0]
		rec.FileName, rec.Hash, rec.Size = name, sha512hex(props), int64(len(props))
		_, err := l.Uninstall(srv, []Installed{rec}, rec.Key(), UninstallOptions{Force: true})
		e := wantKind(t, err, KindBadFileName)
		if name == "../server.properties" && e.Msg != `The record of Chunky names the file "../server.properties", which Playkeeper will not touch.` {
			t.Errorf("message %q", e.Msg)
		}
	}

	_, err = l.Uninstall(Server{Dir: srv.Dir, Type: "velocity"}, installed, installed[0].Key(), UninstallOptions{})
	wantKind(t, err, KindUnknownServerType)

	if string(readFile(t, filepath.Join(srv.Dir, "server.properties"))) != string(props) {
		t.Error("server.properties was changed")
	}
	if got := ls(t, filepath.Join(srv.Dir, "plugins")); !slices.Equal(got, []string{"Chunky-Bukkit-1.5.3.jar", "ViaVersion.jar"}) {
		t.Errorf("plugins holds %v", got)
	}
}

func TestUninstallWhenTheFileIsGone(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "chunky"})
	plugins := filepath.Join(srv.Dir, "plugins")
	if err := os.Remove(filepath.Join(plugins, "Chunky-Bukkit-1.5.3.jar")); err != nil {
		t.Fatal(err)
	}

	for _, what := range []string{"file", "folder"} {
		if what == "folder" {
			if err := os.Remove(plugins); err != nil {
				t.Fatal(err)
			}
		}
		rm, err := l.Uninstall(srv, installed, installed[0].Key(), UninstallOptions{RemoveConfig: true})
		if err != nil {
			t.Fatalf("without the %s: %v", what, err)
		}
		if rm.Removed.Key() != installed[0].Key() || !rm.RestartNeeded {
			t.Errorf("without the %s: removal %+v", what, rm)
		}
		wantNotices(t, "warnings without the "+what, rm.Warnings, "not_found: Chunky-Bukkit-1.5.3.jar was already gone from the plugins folder.")
	}
	if _, err := os.Lstat(plugins); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the plugins folder was created again: %v", err)
	}
}

func TestUninstallRefusesALinkedFolder(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "chunky"})
	plugins := filepath.Join(srv.Dir, "plugins")
	elsewhere := filepath.Join(t.TempDir(), "plugins")
	if err := errors.Join(os.Rename(plugins, elsewhere), os.Symlink(elsewhere, plugins)); err != nil {
		t.Fatal(err)
	}

	_, err := l.Uninstall(srv, installed, installed[0].Key(), UninstallOptions{})
	if e := wantKind(t, err, KindFolderUnusable); e.Msg != "Playkeeper cannot use the server's plugins folder: it is not a folder." {
		t.Errorf("message %q", e.Msg)
	}
	if got := ls(t, elsewhere); !slices.Equal(got, []string{"Chunky-Bukkit-1.5.3.jar"}) {
		t.Errorf("the linked folder holds %v", got)
	}
	_, err = l.PlanInstall(context.Background(), srv, nil, InstallRequest{Source: Modrinth, Project: "viaversion"})
	wantKind(t, err, KindFolderUnusable)
}

package addons

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGeyserMCProjectsComeFromGeyserMC(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	file := f.gfile("floodgate", "spigot")

	p := mustPlan(t, l, srv, nil, InstallRequest{Source: Hangar, Project: "Floodgate"})
	if got := stepList(p); got != "Floodgate 2.2.5-b141 2.2.5-b141" || !p.Ready || len(p.Manual) != 0 {
		t.Fatalf("steps %q, ready %v, manual %+v, blockers %+v", got, p.Ready, p.Manual, p.Blockers)
	}
	s := p.Steps[0]
	if s.Source != Hangar || s.ProjectID != "17" || s.FileName != "floodgate-spigot.jar" || s.Size != int64(len(file.data)) ||
		s.HashAlgo != "sha256" || s.Hash != sha256hex(file.data) || s.Channel != release ||
		!s.Published.Equal(time.Date(2026, 9, 17, 14, 53, 3, 841000000, time.UTC)) {
		t.Errorf("step %+v", s)
	}
	if !strings.HasSuffix(s.url, "/v2/projects/floodgate/versions/2.2.5/builds/141/downloads/spigot") {
		t.Errorf("the plan downloads %s, not the pinned build", s.url)
	}

	recs := mustInstall(t, l, srv, nil, InstallRequest{Source: Hangar, Project: "Floodgate", Fingerprint: p.Fingerprint})
	if got := string(readFile(t, filepath.Join(srv.Dir, "plugins", "floodgate-spigot.jar"))); got != string(file.data) {
		t.Error("the installed file is not GeyserMC's")
	}
	if len(recs) != 1 || recs[0].VersionNumber != "2.2.5-b141" || recs[0].HashAlgo != "sha256" || recs[0].Hash != sha256hex(file.data) ||
		recs[0].Size != int64(len(file.data)) || recs[0].Source != Hangar || recs[0].ProjectID != "17" {
		t.Errorf("records %+v", recs)
	}
	if len(f.sent("cdn")) != 0 || len(f.strays()) != 0 {
		t.Errorf("downloaded %d files from Hangar's CDN, strays %v", len(f.sent("cdn")), f.strays())
	}
	var heads, gets int
	for _, r := range f.sentTo("geysermc", file.path) {
		switch r.method {
		case http.MethodHead:
			heads++
		case http.MethodGet:
			gets++
		}
	}
	if heads == 0 || gets != 1 {
		t.Errorf("asked GeyserMC for the file's size %d times and downloaded it %d times", heads, gets)
	}

	vs, err := l.Versions(context.Background(), srv, Hangar, "Floodgate")
	if err != nil || len(vs) != 1 || vs[0].VersionNumber != "2.2.5-b141" || vs[0].ExternalURL != "" || vs[0].FileName != "floodgate-spigot.jar" {
		t.Errorf("versions %+v, %v", vs, err)
	}
	d, err := l.Details(context.Background(), srv, Hangar, "Geyser")
	if err != nil || d.Latest == nil || d.Latest.VersionNumber != "2.11.3-b1247" || d.Latest.FileName != "Geyser-Spigot.jar" || d.Notice != nil {
		t.Errorf("Geyser's details %+v, %v", d, err)
	}

	f.newGeyserBuild("floodgate", 142, time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC))
	st, err := l.CheckUpdates(context.Background(), srv, recs)
	if err != nil || len(st) != 1 || !st[0].Available || st[0].Latest == nil || st[0].Latest.VersionNumber != "2.2.5-b142" {
		t.Fatalf("updates %+v, %v", st, err)
	}
	res, err := l.Update(context.Background(), srv, recs, UpdateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	newer := f.gfile("floodgate", "spigot")
	if len(res.Installed) != 1 || res.Installed[0].VersionNumber != "2.2.5-b142" ||
		string(readFile(t, filepath.Join(srv.Dir, "plugins", "floodgate-spigot.jar"))) != string(newer.data) {
		t.Errorf("update %+v", res)
	}
}

func TestGeyserMCDownloadsAreChecked(t *testing.T) {
	t.Run("a file that doesn't match GeyserMC's hash", func(t *testing.T) {
		f := newFakes(t)
		file := f.gfile("floodgate", "spigot")
		f.hook("geysermc:"+file.path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "13")
			if r.Method != http.MethodHead {
				w.Write([]byte("not floodgate"))
			}
		})
		srv := newServer(t, "paper", "26.2")
		_, err := f.library().Install(context.Background(), srv, nil, InstallRequest{Source: Hangar, Project: "Floodgate"})
		e := wantKind(t, err, KindHashMismatch)
		if e.Msg != "The download of floodgate-spigot.jar does not match the sha256 hash GeyserMC publishes, so Playkeeper did not install it." || e.Params["source"] != "GeyserMC" {
			t.Errorf("notice %+v", e.Notice)
		}
		if ls(t, filepath.Join(srv.Dir, "plugins")) != nil {
			t.Error("something was installed")
		}
	})
	t.Run("a file sent from another host", func(t *testing.T) {
		f := newFakes(t)
		file := f.gfile("floodgate", "spigot")
		f.hook("geysermc:"+file.path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead {
				w.Header().Set("Content-Length", "13")
				return
			}
			http.Redirect(w, r, f.evil.URL+"/floodgate.jar", http.StatusFound)
		})
		srv := newServer(t, "paper", "26.2")
		_, err := f.library().Install(context.Background(), srv, nil, InstallRequest{Source: Hangar, Project: "Floodgate"})
		e := wantKind(t, err, KindRedirectRefused)
		if !strings.Contains(e.Msg, "which is not one of GeyserMC's own file hosts") || e.Hint != "Playkeeper only downloads Floodgate from GeyserMC's own file host. Nothing was installed." {
			t.Errorf("notice %+v", e.Notice)
		}
		if f.evilHits != 0 || ls(t, filepath.Join(srv.Dir, "plugins")) != nil {
			t.Errorf("reached the other host %d times, installed %v", f.evilHits, ls(t, filepath.Join(srv.Dir, "plugins")))
		}
	})
	t.Run("GeyserMC's server is only for GeyserMC's files", func(t *testing.T) {
		f := newFakes(t)
		l := f.library()
		l.GeyserFiles = nil
		srv := newServer(t, "paper", "26.2")
		p := mustPlan(t, l, srv, nil, InstallRequest{Source: Hangar, Project: "Floodgate"})
		wantNotices(t, "blockers", p.Blockers, "host_not_allowed: floodgate-spigot.jar would be downloaded from 127.0.0.1, which is not one of GeyserMC's own file hosts.")
	})
	t.Run("GeyserMC can't be reached", func(t *testing.T) {
		f := newFakes(t)
		l := f.library()
		f.geyser.Close()
		_, err := l.PlanInstall(context.Background(), newServer(t, "paper", "26.2"), nil, InstallRequest{Source: Hangar, Project: "Floodgate"})
		if e := wantKind(t, err, KindUnreachable); e.Msg != "Playkeeper could not reach GeyserMC." {
			t.Errorf("notice %+v", e.Notice)
		}
	})
}

func TestOnlyGeyserMCsOwnProjectsFollowItsLinks(t *testing.T) {
	f := newFakes(t)
	// Another project's link to Floodgate's newest build stays a link.
	link := "https://download.geysermc.org/v2/projects/floodgate/versions/latest/builds/latest/downloads/spigot"
	via := num(f.hProjects["ViaVersion"]["id"])
	for _, id := range f.hOrder {
		if num(f.hVersions[id]["projectId"]) == via {
			f.patchHangarVersion(id, func(v obj) {
				v["downloads"].(obj)["PAPER"] = obj{"fileInfo": nil, "externalUrl": link, "downloadUrl": nil}
			})
		}
	}
	l := f.library()
	p := mustPlan(t, l, newServer(t, "paper", "26.2"), nil, InstallRequest{Source: Hangar, Project: "ViaVersion"})
	if len(p.Steps) != 0 || len(p.Manual) != 1 || p.Manual[0].Kind != KindExternal || p.Manual[0].URL != link {
		t.Errorf("steps %q, manual %+v", stepList(p), p.Manual)
	}
	if len(f.sent("geysermc")) != 0 {
		t.Errorf("asked GeyserMC %d times", len(f.sent("geysermc")))
	}
}

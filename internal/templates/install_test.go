package templates

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
)

// fakeLibrary answers like the add-on library, with the versions Modrinth
// and Hangar publish for the Paper fixture's add-ons. Each project's
// answer is the steps PlanInstall plans for it; steps for add-ons the
// server already has drop out, as the library counts them as satisfied.
type fakeLibrary struct {
	answers    map[addons.Key][]addons.Step
	blockers   map[addons.Key][]Notice
	manual     map[addons.Key][]addons.ManualStep
	planErr    map[addons.Key]error
	installErr map[addons.Key]error
	// afterPlan runs after each PlanInstall, as a source changing its
	// answer between the plan and the download would.
	afterPlan func(*fakeLibrary)

	calls    []string
	requests []addons.InstallRequest
	servers  []addons.Server
}

var (
	chunkyKey       = addons.Key{Source: addons.Modrinth, ProjectID: "fALzjamp"}
	viaVersionKey   = addons.Key{Source: addons.Hangar, ProjectID: "31"}
	viaBackwardsKey = addons.Key{Source: addons.Hangar, ProjectID: "12"}
	installedAt     = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
)

func newFakeLibrary(t *testing.T) *fakeLibrary {
	t.Helper()
	rows := map[addons.Key]addons.Installed{}
	for _, rec := range installedRows(t) {
		rows[rec.Key()] = rec
	}
	return &fakeLibrary{
		answers: map[addons.Key][]addons.Step{
			chunkyKey:     {stepOf(rows[chunkyKey], "")},
			viaVersionKey: {stepOf(rows[viaVersionKey], "")},
			// ViaBackwards needs ViaVersion.
			viaBackwardsKey: {stepOf(rows[viaBackwardsKey], ""), stepOf(rows[viaVersionKey], "12")},
		},
		blockers: map[addons.Key][]Notice{}, manual: map[addons.Key][]addons.ManualStep{},
		planErr: map[addons.Key]error{}, installErr: map[addons.Key]error{},
	}
}

func stepOf(rec addons.Installed, dependencyOf string) addons.Step {
	return addons.Step{
		Action: addons.ActionInstall, Source: rec.Source, ProjectID: rec.ProjectID, Slug: rec.Slug, Name: rec.Name, IconURL: rec.IconURL,
		VersionID: rec.VersionID, VersionNumber: rec.VersionNumber, Channel: rec.Channel, Published: rec.Published, FileName: rec.FileName,
		Size: rec.Size, HashAlgo: rec.HashAlgo, Hash: rec.Hash, DependencyOf: dependencyOf, Requires: rec.Requires,
	}
}

func (f *fakeLibrary) plan(installed []addons.Installed, req addons.InstallRequest) (*addons.Plan, error) {
	key := addons.Key{Source: req.Source, ProjectID: req.Project}
	if err := f.planErr[key]; err != nil {
		return nil, err
	}
	p := &addons.Plan{
		Steps: []addons.Step{}, Satisfied: []addons.Satisfied{}, Manual: append([]addons.ManualStep{}, f.manual[key]...), Suggestions: []addons.Suggestion{},
		Blockers: append([]Notice{}, f.blockers[key]...), Warnings: []Notice{},
	}
	for _, s := range f.answers[key] {
		if slices.ContainsFunc(installed, func(rec addons.Installed) bool {
			return rec.Key() == (addons.Key{Source: s.Source, ProjectID: s.ProjectID})
		}) {
			continue
		}
		p.Steps = append(p.Steps, s)
		if s.Channel != "release" {
			p.Warnings = append(p.Warnings, Notice{Kind: addons.KindPrerelease, Msg: fmt.Sprintf("%s %s is a %s version and may be unstable.", s.Name, s.VersionNumber, s.Channel)})
		}
	}
	p.Ready = len(p.Steps) > 0 && len(p.Blockers) == 0
	b, _ := json.Marshal([]any{p.Steps, p.Manual, p.Blockers})
	sum := sha256.Sum256(b)
	p.Fingerprint = hex.EncodeToString(sum[:16])
	return p, nil
}

func (f *fakeLibrary) PlanInstall(ctx context.Context, srv addons.Server, installed []addons.Installed, req addons.InstallRequest) (*addons.Plan, error) {
	f.calls = append(f.calls, "plan "+req.Project)
	f.requests, f.servers = append(f.requests, req), append(f.servers, srv)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := f.plan(installed, req)
	if f.afterPlan != nil {
		f.afterPlan(f)
	}
	return p, err
}

func (f *fakeLibrary) Install(ctx context.Context, srv addons.Server, installed []addons.Installed, req addons.InstallRequest) (*addons.Result, error) {
	f.calls = append(f.calls, "install "+req.Project)
	f.requests, f.servers = append(f.requests, req), append(f.servers, srv)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := f.plan(installed, req)
	switch {
	case err != nil:
		return nil, err
	case req.Fingerprint != "" && req.Fingerprint != p.Fingerprint:
		return nil, &Error{Notice: Notice{Kind: addons.KindPlanChanged, Msg: "What this would do has changed since you confirmed it."}}
	case len(p.Blockers) > 0:
		return nil, &Error{Notice: p.Blockers[0]}
	}
	if err := f.installErr[addons.Key{Source: req.Source, ProjectID: req.Project}]; err != nil {
		return nil, err
	}
	res := &addons.Result{Installed: []addons.Installed{}, Replaced: []addons.Installed{}, Manual: p.Manual, Warnings: p.Warnings, RestartNeeded: true}
	for _, s := range p.Steps {
		res.Installed = append(res.Installed, addons.Installed{
			Source: s.Source, ProjectID: s.ProjectID, Slug: s.Slug, Name: s.Name, IconURL: s.IconURL, VersionID: s.VersionID,
			VersionNumber: s.VersionNumber, Channel: s.Channel, Published: s.Published, FileName: s.FileName, HashAlgo: s.HashAlgo,
			Hash: strings.ToLower(s.Hash), Size: s.Size, DependencyOf: s.DependencyOf, Requires: s.Requires, InstalledAt: installedAt,
		})
	}
	return res, nil
}

// recs lists records by project id, with the add-on each was installed
// for after a <.
func recs(rs []addons.Installed) []string {
	out := []string{}
	for _, r := range rs {
		s := r.ProjectID
		if r.DependencyOf != "" {
			s += "<" + r.DependencyOf
		}
		out = append(out, s)
	}
	return out
}

func sortRecords(rs []addons.Installed) {
	slices.SortFunc(rs, func(a, b addons.Installed) int {
		return cmp.Or(strings.Compare(string(a.Source), string(b.Source)), strings.Compare(a.ProjectID, b.ProjectID))
	})
}

func newServer(t *testing.T, p *Plan) addons.Server {
	return addons.Server{Dir: t.TempDir(), Type: p.Type.ID, MinecraftVersion: p.Version.MinecraftVersion}
}

func TestImportPaperTemplate(t *testing.T) {
	// The create-server flow gets the template from its page's fragment,
	// as the share page hands it over.
	link := mustLink(t, fixture(t, "paper-server.json"))
	payload := strings.TrimPrefix(link.URL, ShareURL+"#")
	tp, err := DecodeLink("https://survival.example.net:8443/servers/new#template=" + payload)
	if err != nil {
		t.Fatal(err)
	}
	p := planOf(t, tp, paperCatalog())
	if err := p.Confirm(p.Fingerprint); err != nil {
		t.Fatal(err)
	}

	lib := newFakeLibrary(t)
	srv := newServer(t, p)
	var installed []addons.Installed
	for _, a := range p.Addons {
		res, err := InstallAddon(context.Background(), lib, srv, installed, a)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != AddonInstalled || res.Reason != nil {
			t.Fatalf("%s: got %s (%+v)", a.Name, res.Status, res.Reason)
		}
		installed = append(installed, res.Records...)
	}
	for _, rec := range LinkDependencies(installed, p) {
		installed[slices.IndexFunc(installed, func(r addons.Installed) bool { return r.Key() == rec.Key() })] = rec
	}
	if want := []string{"plan fALzjamp", "install fALzjamp", "plan 31", "install 31", "plan 12", "install 12"}; !slices.Equal(lib.calls, want) {
		t.Errorf("got calls %v, want %v", lib.calls, want)
	}
	for _, s := range lib.servers {
		if s != srv {
			t.Errorf("an add-on went to %+v", s)
		}
	}

	// The new server's records are the ones of the server the template
	// was exported from, and exporting it gives the template back.
	want := installedRows(t)
	sortRecords(installed)
	sortRecords(want)
	if !reflect.DeepEqual(installed, want) {
		t.Errorf("records differ:\n got %+v\nwant %+v", installed, want)
	}
	st := p.Settings
	st.MemoryMB = p.MemoryMB
	s := Setup{Name: p.Name, Description: p.Description, Type: p.Type.ID, MinecraftVersion: p.Version.MinecraftVersion, Build: p.Version.Build,
		Settings: st, Addons: installed}
	for _, pk := range p.Packs {
		s.Packs = append(s.Packs, PackSetup{Pack: pk.Pack})
	}
	back, _, err := Export(s, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sameTemplate(t, back, tp)
}

func TestInstallAddon(t *testing.T) {
	p := planOf(t, paperTemplate(t), paperCatalog())
	chunky, viaVersion, viaBackwards := p.Addons[0], p.Addons[1], p.Addons[2]
	rows := map[addons.Key]addons.Installed{}
	for _, rec := range installedRows(t) {
		rows[rec.Key()] = rec
	}
	onItsOwn := rows[viaVersionKey]
	onItsOwn.DependencyOf = ""
	refuse := func(k Kind, msg string) error { return &Error{Notice: Notice{Kind: k, Msg: msg}} }
	answer := func(f *fakeLibrary, edit func(*addons.Step)) { edit(&f.answers[chunkyKey][0]) }

	cases := []struct {
		name      string
		addon     PlannedAddon
		installed []addons.Installed
		setup     func(*fakeLibrary)
		status    AddonStatus
		reason    Kind
		records   []string
		calls     []string
		check     func(*testing.T, *AddonResult, *fakeLibrary)
	}{
		{name: "pinned version", addon: chunky, status: AddonInstalled, records: []string{"fALzjamp"},
			calls: []string{"plan fALzjamp", "install fALzjamp"}, check: func(t *testing.T, r *AddonResult, f *fakeLibrary) {
				want := addons.InstallRequest{Source: addons.Modrinth, Project: "fALzjamp", VersionID: "MdY6JATr"}
				if len(f.requests) != 2 {
					t.Fatalf("got requests %+v", f.requests)
				}
				if f.requests[0] != want || f.requests[1].Fingerprint == "" {
					t.Errorf("got requests %+v", f.requests)
				}
				want.Fingerprint = f.requests[1].Fingerprint
				if f.requests[1] != want {
					t.Errorf("got %+v, want the confirmed plan's fingerprint with the same request", f.requests[1])
				}
			}},
		{name: "pre-release with the add-on it needs", addon: viaBackwards, status: AddonInstalled, records: []string{"12", "31<12"},
			calls: []string{"plan 12", "install 12"}, check: func(t *testing.T, r *AddonResult, f *fakeLibrary) {
				if !f.requests[0].AllowPrerelease || len(r.Warnings) != 2 || r.Warnings[0].Kind != addons.KindPrerelease {
					t.Errorf("got %+v and warnings %+v", f.requests[0], r.Warnings)
				}
			}},
		{name: "the add-on it needs is there already", addon: viaBackwards, installed: []addons.Installed{onItsOwn}, status: AddonInstalled,
			records: []string{"12"}, calls: []string{"plan 12", "install 12"}},
		{name: "already on the server", addon: viaVersion, installed: []addons.Installed{rows[viaVersionKey]}, status: AddonPresent},
		{name: "version the source no longer offers", addon: chunky, setup: func(f *fakeLibrary) {
			f.planErr[chunkyKey] = refuse(addons.KindNoVersion, "That version of Chunky does not fit this server (Paper, Minecraft 26.2).")
		}, status: AddonSkipped, reason: addons.KindNoVersion, calls: []string{"plan fALzjamp"}},
		{name: "source out of reach", addon: chunky, setup: func(f *fakeLibrary) {
			f.planErr[chunkyKey] = refuse(addons.KindUnreachable, "Playkeeper could not reach Modrinth.")
		}, status: AddonSkipped, reason: addons.KindUnreachable, calls: []string{"plan fALzjamp"}},
		{name: "blocked", addon: chunky, setup: func(f *fakeLibrary) {
			f.blockers[chunkyKey] = []Notice{{Kind: addons.KindConflict, Msg: "Chunky cannot run together with ChunkyBorder."}}
		}, status: AddonSkipped, reason: addons.KindConflict, calls: []string{"plan fALzjamp"}},
		{name: "blocked with nothing to download", addon: chunky, setup: func(f *fakeLibrary) {
			f.answers[chunkyKey] = nil
			f.blockers[chunkyKey] = []Notice{{Kind: addons.KindFileExists, Msg: "A file named Chunky-Bukkit-1.5.3.jar is already in the plugins folder."}}
		}, status: AddonSkipped, reason: addons.KindFileExists, calls: []string{"plan fALzjamp"}},
		{name: "download only from its own site", addon: chunky, setup: func(f *fakeLibrary) {
			f.answers[chunkyKey] = nil
			f.manual[chunkyKey] = []addons.ManualStep{{Notice: Notice{Kind: addons.KindExternal, Msg: "Chunky is only available from its own site."},
				URL: "https://modrinth.com/plugin/chunky"}}
		}, status: AddonSkipped, reason: addons.KindExternal, calls: []string{"plan fALzjamp"}, check: func(t *testing.T, r *AddonResult, f *fakeLibrary) {
			if len(r.Manual) != 1 || r.Manual[0].URL != "https://modrinth.com/plugin/chunky" {
				t.Errorf("got manual steps %+v", r.Manual)
			}
		}},
		{name: "nothing to install", addon: chunky, setup: func(f *fakeLibrary) { f.answers[chunkyKey] = nil },
			status: AddonSkipped, reason: addons.KindUpToDate, calls: []string{"plan fALzjamp"}},
		{name: "another project", addon: chunky, setup: func(f *fakeLibrary) {
			answer(f, func(s *addons.Step) { s.ProjectID, s.Slug, s.Name = "Xy7Zw6Vu", "chunkyborder", "ChunkyBorder" })
		}, status: AddonSkipped, reason: KindProjectMismatch, calls: []string{"plan fALzjamp"}},
		{name: "project renamed", addon: chunky, setup: func(f *fakeLibrary) {
			answer(f, func(s *addons.Step) { s.Slug, s.Name = "chunkyborder", "ChunkyBorder" })
		}, status: AddonSkipped, reason: KindProjectMismatch, calls: []string{"plan fALzjamp"}, check: func(t *testing.T, r *AddonResult, f *fakeLibrary) {
			if r.Reason == nil || r.Reason.Msg != "The template calls this add-on Chunky, but Modrinth's project fALzjamp is ChunkyBorder, so Playkeeper did not install it." {
				t.Errorf("got %+v", r.Reason)
			}
		}},
		{name: "new title, same slug", addon: chunky, setup: func(f *fakeLibrary) {
			answer(f, func(s *addons.Step) { s.Name = "Chunky Pregenerator" })
		}, status: AddonInstalled, records: []string{"fALzjamp"}, calls: []string{"plan fALzjamp", "install fALzjamp"}},
		{name: "another file for the pinned version", addon: chunky, setup: func(f *fakeLibrary) {
			answer(f, func(s *addons.Step) { s.Hash = digest("sha512", "re-uploaded") })
		}, status: AddonSkipped, reason: KindPinMismatch, calls: []string{"plan fALzjamp"}, check: func(t *testing.T, r *AddonResult, f *fakeLibrary) {
			if r.Reason == nil || r.Reason.Msg != "The file Modrinth offers for Chunky 1.5.3 is not the one the template names, so Playkeeper did not install it." {
				t.Errorf("got %+v", r.Reason)
			}
		}},
		{name: "another version", addon: chunky, setup: func(f *fakeLibrary) {
			answer(f, func(s *addons.Step) { s.VersionID, s.VersionNumber = "vbGiEu4k", "1.5.4" })
		}, status: AddonSkipped, reason: KindPinMismatch, calls: []string{"plan fALzjamp"}},
		{name: "hash in capitals", addon: chunky, setup: func(f *fakeLibrary) {
			answer(f, func(s *addons.Step) { s.Hash = strings.ToUpper(s.Hash) })
		}, status: AddonInstalled, records: []string{"fALzjamp"}, calls: []string{"plan fALzjamp", "install fALzjamp"}},
		{name: "answer changed before the download", addon: chunky, setup: func(f *fakeLibrary) {
			f.afterPlan = func(f *fakeLibrary) { answer(f, func(s *addons.Step) { s.Size++ }) }
		}, status: AddonSkipped, reason: addons.KindPlanChanged, calls: []string{"plan fALzjamp", "install fALzjamp"}},
		{name: "download that does not match its hash", addon: chunky, setup: func(f *fakeLibrary) {
			f.installErr[chunkyKey] = refuse(addons.KindHashMismatch, "The downloaded file of Chunky does not match the hash Modrinth publishes.")
		}, status: AddonSkipped, reason: addons.KindHashMismatch, calls: []string{"plan fALzjamp", "install fALzjamp"}},
		{name: "request altered in the plan", addon: func() PlannedAddon {
			a := chunky
			a.Request = addons.InstallRequest{Source: addons.Modrinth, Project: "P7dR8mSH", AllowPrerelease: true}
			return a
		}(), status: AddonInstalled, records: []string{"fALzjamp"}, calls: []string{"plan fALzjamp", "install fALzjamp"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lib := newFakeLibrary(t)
			if c.setup != nil {
				c.setup(lib)
			}
			res, err := InstallAddon(context.Background(), lib, newServer(t, p), c.installed, c.addon)
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != c.status || res.Addon.Key() != c.addon.Key() {
				t.Errorf("got %s for %s, want %s", res.Status, res.Addon.Name, c.status)
			}
			switch {
			case c.reason == "" && res.Reason != nil:
				t.Errorf("got reason %+v", res.Reason)
			case c.reason != "" && (res.Reason == nil || res.Reason.Kind != c.reason):
				t.Errorf("got reason %+v, want %s", res.Reason, c.reason)
			case res.Reason != nil:
				checkNotice(t, *res.Reason)
			}
			if got := recs(res.Records); !slices.Equal(got, c.records) {
				t.Errorf("got records %v, want %v", got, c.records)
			}
			if !slices.Equal(lib.calls, c.calls) {
				t.Errorf("got calls %v, want %v", lib.calls, c.calls)
			}
			if js, _ := json.Marshal(res); strings.Contains(string(js), "null") {
				t.Errorf("the result's JSON has nulls: %s", js)
			}
			if c.check != nil {
				c.check(t, res, lib)
			}
		})
	}
}

func TestInstallAddonStops(t *testing.T) {
	p := planOf(t, paperTemplate(t), paperCatalog())
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name  string
		ctx   context.Context
		setup func(*fakeLibrary)
		want  error
	}{
		{name: "cancelled", ctx: cancelled, want: context.Canceled},
		{name: "folder unreadable", setup: func(f *fakeLibrary) { f.planErr[chunkyKey] = syscall.EIO }, want: syscall.EIO},
		{name: "disk full", setup: func(f *fakeLibrary) {
			f.installErr[chunkyKey] = fmt.Errorf("write plugins/Chunky-Bukkit-1.5.3.jar: %w", syscall.ENOSPC)
		}, want: syscall.ENOSPC},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lib := newFakeLibrary(t)
			if c.setup != nil {
				c.setup(lib)
			}
			ctx := context.Background()
			if c.ctx != nil {
				ctx = c.ctx
			}
			res, err := InstallAddon(ctx, lib, newServer(t, p), nil, p.Addons[0])
			if !errors.Is(err, c.want) || res != nil {
				t.Errorf("got %+v and %v, want %v", res, err, c.want)
			}
		})
	}
}

func TestInstallAddonUnpinned(t *testing.T) {
	// Planned on a Playkeeper without Minecraft 1.21.4, so the add-ons get
	// the newest versions that fit 1.21.11 instead of the pinned ones.
	tp := paperTemplate(t)
	tp.Server.MinecraftVersion, tp.Server.Build = "1.21.4", nil
	p := planOf(t, tp, paperCatalog())
	lib := newFakeLibrary(t)
	answer := func(k addons.Key, version, number string) {
		s := &lib.answers[k][0]
		s.VersionID, s.VersionNumber, s.Hash = version, number, digest(s.HashAlgo, number)
	}
	answer(chunkyKey, "vbGiEu4k", "1.4.40")
	answer(viaVersionKey, "29001", "5.4.2")

	srv := newServer(t, p)
	var installed []addons.Installed
	for _, a := range p.Addons[:2] {
		res, err := InstallAddon(context.Background(), lib, srv, installed, a)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != AddonInstalled {
			t.Fatalf("%s: got %s (%+v)", a.Name, res.Status, res.Reason)
		}
		installed = append(installed, res.Records...)
	}
	want := []addons.InstallRequest{{Source: addons.Modrinth, Project: "fALzjamp"}, {Source: addons.Hangar, Project: "31", AllowPrerelease: true}}
	if lib.requests[0] != want[0] || lib.requests[2] != want[1] {
		t.Errorf("got requests %+v, want %+v", lib.requests, want)
	}
	if installed[0].VersionNumber != "1.4.40" || installed[1].VersionNumber != "5.4.2" || srv.MinecraftVersion != "1.21.11" {
		t.Errorf("got %+v on %s", installed, srv.MinecraftVersion)
	}

	// Add-ons named as "the newest version that fits" are not checked
	// against a version either.
	fp := planOf(t, fixture(t, "fabric-modpack.json"), withType(paperCatalog(), "fabric", "1.21.1"))
	lib = newFakeLibrary(t)
	answer(chunkyKey, "RDDLpNhp", "1.4.16")
	res, err := InstallAddon(context.Background(), lib, newServer(t, fp), nil, fp.Addons[0])
	if err != nil || res.Status != AddonInstalled || lib.requests[0] != (addons.InstallRequest{Source: addons.Modrinth, Project: "fALzjamp"}) {
		t.Errorf("got %+v, %v and %+v", res, err, lib.requests)
	}
}

func TestLinkDependencies(t *testing.T) {
	p := planOf(t, paperTemplate(t), paperCatalog())
	rows := func(edit func(map[addons.Key]*addons.Installed)) []addons.Installed {
		rs := installedRows(t)
		byKey := map[addons.Key]*addons.Installed{}
		for i := range rs {
			byKey[rs[i].Key()] = &rs[i]
		}
		byKey[viaVersionKey].DependencyOf = ""
		if edit != nil {
			edit(byKey)
		}
		return slices.DeleteFunc(rs, func(r addons.Installed) bool { return r.ProjectID == "" })
	}
	cases := []struct {
		name      string
		installed []addons.Installed
		want      []string
	}{
		{name: "installed on its own first", installed: rows(nil), want: []string{"31<12"}},
		{name: "already marked", installed: rows(func(m map[addons.Key]*addons.Installed) { m[viaVersionKey].DependencyOf = "12" })},
		{name: "the add-on that needs it was skipped", installed: rows(func(m map[addons.Key]*addons.Installed) { *m[viaBackwardsKey] = addons.Installed{} })},
		{name: "the version installed no longer needs it", installed: rows(func(m map[addons.Key]*addons.Installed) { m[viaBackwardsKey].Requires = nil })},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := recs(LinkDependencies(c.installed, p)); !slices.Equal(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

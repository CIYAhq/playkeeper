package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// PaperMC's API answered 503 from about 09:25 UTC on 1 Oct 2026, and every
// new install stopped at onboarding. These tests run that outage against
// the fake PaperMC and the fake image, and each one's negative control
// takes the way around it away and fails.

// useBuiltInPaper makes the Paper list built into Playkeeper one whose
// jars are the fake image's, as PaperMC listed them at madeAt; with no
// versions there is no built-in list.
func (e *agentEnv) useBuiltInPaper(madeAt time.Time, versions ...minecraft.BuiltInPaperVersion) {
	e.t.Helper()
	l := minecraft.BuiltInPaper{MadeAt: madeAt, Versions: versions}
	cat, res := builtInPaperCatalog, builtInPaperRestore
	e.t.Cleanup(func() { builtInPaperCatalog, builtInPaperRestore = cat, res })
	if len(versions) == 0 {
		none := errors.New("no list is built in")
		builtInPaperCatalog = func() ([]api.CatalogEntry, time.Time, error) { return nil, time.Time{}, none }
		builtInPaperRestore = func(string, int) (api.CatalogEntry, time.Time, error) { return api.CatalogEntry{}, time.Time{}, none }
		return
	}
	builtInPaperCatalog = l.Catalog
	builtInPaperRestore = l.RestoreBuild
}

// forgetPaperLists leaves the machine with no Paper list of its own, in
// memory or on disk, and none built in.
func (e *agentEnv) forgetPaperLists() {
	e.t.Helper()
	e.useBuiltInPaper(time.Time{})
	e.a.catalog.mu.Lock()
	e.a.catalog.list, e.a.catalog.retryAt = versionList{}, time.Time{}
	e.a.catalog.mu.Unlock()
	if err := os.Remove(filepath.Join(e.cfg.AgentDir(), "software-lists", "catalog-paper.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		e.t.Fatal(err)
	}
}

// builtInVersion is a stable Paper build in a built-in list, with the fake
// image's jar.
func (e *agentEnv) builtInVersion(id, support string, build int) minecraft.BuiltInPaperVersion {
	return minecraft.BuiltInPaperVersion{ID: id, Support: support, Java: 25, Builds: []minecraft.BuiltInPaperBuild{{ID: build, Channel: "STABLE", SHA256: e.fill.sum}}}
}

// paperMCDown is an agent env while PaperMC's API answers every request
// with 503 and the fake image can download Paper only from its download
// host, as on 1 Oct 2026.
func paperMCDown(t *testing.T) *agentEnv {
	return newAgentEnvWith(t, func(e *agentEnv) {
		e.fill.setDown(true)
		e.fd.mu.Lock()
		e.fd.paperAPIDown = true
		e.fd.mu.Unlock()
		e.tweak = func(o *Options) {
			o.CheckEgress = func(context.Context) error {
				return &egressTrouble{down: []string{"PaperMC answered HTTP 503"}}
			}
		}
	})
}

func (e *agentEnv) setupEnvs() [][]string {
	e.fd.mu.Lock()
	defer e.fd.mu.Unlock()
	return slices.Clone(e.fd.setupEnvs)
}

func TestWhilePaperMCAnswers503TheFirstServerIsStillCreated(t *testing.T) {
	made := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	e := paperMCDown(t)
	e.useBuiltInPaper(made, e.builtInVersion("26.2", "SUPPORTED", 129), e.builtInVersion("26.1.2", "UNSUPPORTED", 74))

	pre := e.a.Preflight(t.Context())
	i := slices.IndexFunc(pre.Checks, func(c api.PreflightCheck) bool { return c.ID == "egress" })
	if !pre.OK || i < 0 || pre.Checks[i].Status != "warn" || !strings.Contains(pre.Checks[i].Detail, "PaperMC answered HTTP 503") || !strings.Contains(pre.Checks[i].Detail, "uses the versions it knows") {
		t.Fatalf("onboarding's check must let the owner continue and say PaperMC is down: %+v", pre)
	}

	cat := e.a.catalogInfo(t.Context(), "")
	if cat.VersionsError != "" || len(cat.Versions) != 2 || !cat.Versions[0].Recommended || cat.Versions[0].ID != "paper-26.2" || cat.VersionsCheckedAt == nil || !cat.VersionsCheckedAt.Equal(made) ||
		cat.VersionsFrom != "builtin" || cat.VersionsUpstream != "PaperMC" {
		t.Fatalf("the first server's versions come from the list built into Playkeeper, dated when it was made, and say so: %+v", cat)
	}

	e.createWith(map[string]any{"versionId": "paper-26.2"})
	sc, err := e.srv().serverConfig()
	if err != nil || sc.MinecraftVersion != "26.2" || sc.PaperBuild != 129 || sc.JarSHA256 != e.fill.sum || sc.JarVerifiedAt == nil {
		t.Fatalf("the server runs the verified build from the list: %+v %v", sc, err)
	}
	want, _ := minecraft.PaperJarURL("26.2", 129, e.fill.sum)
	if envs := e.setupEnvs(); len(envs) != 1 || !slices.Contains(envs[0], "PAPER_DOWNLOAD_URL="+want) {
		t.Fatalf("Paper must download from PaperMC's download host by its checksum, once: %v", envs)
	}
	if e.fill.asked() == 0 {
		t.Fatal("PaperMC must still be asked first")
	}
}

// The negative control: the same outage with no list kept or built in
// leaves onboarding where it was on 1 Oct, with no version to create.
func TestWithoutAListOfItsOwnOrBuiltInTheOutageLeavesNoVersion(t *testing.T) {
	e := paperMCDown(t)
	e.useBuiltInPaper(time.Time{})
	cat := e.a.catalogInfo(t.Context(), "")
	if len(cat.Versions) != 0 || !strings.Contains(cat.VersionsError, "HTTP 503") {
		t.Fatalf("with nothing to fall back on there are no versions: %+v", cat)
	}
	code, out := e.startCreate(map[string]any{"versionId": "paper-26.2"})
	if code != http.StatusServiceUnavailable || !strings.Contains(out["error"].(string), "Could not load the Minecraft versions from PaperMC") {
		t.Fatalf("no server can be created: %d %v", code, out)
	}
}

// The negative control: with the list but without the jar's address on
// PaperMC's download host, the image asks the API that's down, as the
// pinned image did on 1 Oct, and the server isn't created.
func TestWithoutTheDownloadAddressTheOutageStopsTheDownload(t *testing.T) {
	e := paperMCDown(t)
	e.useBuiltInPaper(time.Now(), e.builtInVersion("26.2", "SUPPORTED", 129))
	old := paperJarURL
	paperJarURL = func(string, int, string) (string, bool) { return "", false }
	t.Cleanup(func() { paperJarURL = old })
	code, out := e.startCreate(map[string]any{"versionId": "paper-26.2"})
	if code != http.StatusAccepted {
		t.Fatalf("create: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "Downloading the Minecraft server software failed") || !strings.Contains(op.Error, "Failed to download paper") {
		t.Fatalf("the download must fail on PaperMC's API: %+v", op)
	}
	if envs := e.setupEnvs(); len(envs) != 1 || slices.ContainsFunc(envs[0], isPaperDownloadURL) {
		t.Fatalf("the image was asked to download through the API alone: %v", envs)
	}
}

// A machine keeps the last list PaperMC gave on disk, so after a restart
// during an outage it offers that list, which is newer than the one built
// into Playkeeper; only a machine without one falls back to the built-in
// list. Once PaperMC answers again, a minute on, its live list is back.
func TestPaperMCsLastListIsKeptOnDiskForTheNextOutage(t *testing.T) {
	e := newAgentEnv(t)
	live := e.a.catalogInfo(t.Context(), "")
	if live.VersionsError != "" || len(live.Versions) != 3 || live.VersionsCheckedAt == nil {
		t.Fatalf("live: %+v", live)
	}
	kept := filepath.Join(e.cfg.AgentDir(), "software-lists", "catalog-paper.json")
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("the list PaperMC gave must be kept: %v", err)
	}

	made := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	e.useBuiltInPaper(made, e.builtInVersion("26.1.2", "UNSUPPORTED", 74))
	e.stop()
	e.fill.setDown(true)
	e.start()
	cat := e.a.catalogInfo(t.Context(), "")
	if cat.VersionsError != "" || len(cat.Versions) != 3 || !cat.VersionsCheckedAt.Equal(*live.VersionsCheckedAt) || cat.VersionsFrom != "kept" || live.VersionsFrom != "" {
		t.Fatalf("after a restart during the outage the kept list is offered, dated when PaperMC gave it, and says so: %+v", cat)
	}
	asked := e.fill.asked()
	if again := e.a.catalogInfo(t.Context(), ""); len(again.Versions) != 3 || e.fill.asked() != asked {
		t.Fatalf("PaperMC is asked again only after a minute, not on every request: %d more requests", e.fill.asked()-asked)
	}

	// The negative control: without the kept list, the built-in one.
	e.stop()
	if err := os.Remove(kept); err != nil {
		t.Fatal(err)
	}
	e.start()
	if cat := e.a.catalogInfo(t.Context(), ""); len(cat.Versions) != 1 || cat.Versions[0].ID != "paper-26.1.2" || !cat.VersionsCheckedAt.Equal(made) {
		t.Fatalf("a machine without a kept list offers the built-in one: %+v", cat)
	}

	e.fill.setDown(false)
	e.skew.Add(int64(upstreamRetry + time.Second))
	if cat := e.a.catalogInfo(t.Context(), ""); len(cat.Versions) != 3 || cat.VersionsCheckedAt.Equal(made) {
		t.Fatalf("once PaperMC answers, its live list is back: %+v", cat)
	}
}

// A backup of a Paper server restores while PaperMC is down, on a stable
// build of its version from the list the machine has, or else the built-in
// one. Settling a restore Playkeeper 0.3.0 left still waits for PaperMC's
// own pick, and a backup newer than every list says PaperMC is down.
func TestAPaperBackupIsRestoredWhilePaperMCIsDown(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.fill.setDown(true)
	e.a.catalog.mu.Lock()
	e.a.catalog.builds = nil
	e.a.catalog.mu.Unlock()
	got, err := e.a.restoreBuildOrKnown(t.Context(), "26.1.2", 74)
	if err != nil || got.PaperBuild != 74 || got.JarSHA256 != e.fill.sum {
		t.Fatalf("a backup of 26.1.2 build 74 restores from the kept list: %+v %v", got, err)
	}
	if _, err := e.a.restoreBuild(t.Context(), "26.1.2", 74); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("the 0.3.0 settle's lookup still needs PaperMC: %v", err)
	}
	if _, err := e.a.restoreBuildOrKnown(t.Context(), "26.1.2", 90); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("a build newer than every list can't be restored, and says why: %v", err)
	}

	e.stop()
	if err := os.Remove(filepath.Join(e.cfg.AgentDir(), "software-lists", "catalog-paper.json")); err != nil {
		t.Fatal(err)
	}
	e.useBuiltInPaper(time.Now(), e.builtInVersion("26.2", "SUPPORTED", 129), e.builtInVersion("26.1.2", "UNSUPPORTED", 80))
	e.start()
	if got, err := e.a.restoreBuildOrKnown(t.Context(), "26.1.2", 74); err != nil || got.PaperBuild != 80 {
		t.Fatalf("without a kept list, the built-in list's newer stable build: %+v %v", got, err)
	}
}

// When PaperMC's download host doesn't have the jar where Playkeeper looks,
// the image asks PaperMC's API, which knows where its downloads went. When
// both fail, the error is the download host's.
func TestPaperIsAskedForThroughItsAPIWhenItsDownloadHostFails(t *testing.T) {
	e := newAgentEnvWith(t, func(e *agentEnv) { e.fd.paperDownloadsDown = true })
	e.create()
	envs := e.setupEnvs()
	if len(envs) != 2 || !slices.ContainsFunc(envs[0], isPaperDownloadURL) || slices.ContainsFunc(envs[1], isPaperDownloadURL) {
		t.Fatalf("the download host first, then the API: %v", envs)
	}

	both := newAgentEnvWith(t, func(e *agentEnv) {
		e.fd.paperDownloadsDown = true
		e.fd.paperAPIDown = true
	})
	code, out := both.startCreate(nil)
	if code != http.StatusAccepted {
		t.Fatalf("create: %d %v", code, out)
	}
	op := both.waitOp(out["id"].(string))
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "custom PaperMC URL") || !strings.Contains(op.Hint, "fill-data.papermc.io") {
		t.Fatalf("with both down the download host's error is shown: %+v", op)
	}
}

// The machine check fails only when none of the services a first server
// downloads from answers. One answering 503, or not at all, is a warning
// that names it, and the owner can go on.
func TestTheMachineCheckWarnsWhileAServiceIsDownAndFailsOnlyWhenNoneAnswers(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	t.Cleanup(up.Close)
	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	t.Cleanup(busy.Close)
	gone := closedURL(t)
	hosts := egressHosts
	t.Cleanup(func() { egressHosts = hosts })
	set := func(urls ...string) {
		egressHosts = nil
		for i, u := range urls {
			egressHosts = append(egressHosts, struct{ name, host, url string }{hosts[i].name, hosts[i].host, u})
		}
	}
	e := newAgentEnvWith(t, func(e *agentEnv) { e.tweak = func(o *Options) { o.CheckEgress = checkEgress } })
	row := func() api.PreflightCheck {
		pre := e.a.Preflight(t.Context())
		i := slices.IndexFunc(pre.Checks, func(c api.PreflightCheck) bool { return c.ID == "egress" })
		if i < 0 || pre.OK != (pre.Checks[i].Status != "fail") {
			t.Fatalf("preflight: %+v", pre)
		}
		return pre.Checks[i]
	}
	for _, tc := range []struct {
		name, status string
		urls         []string
		detail, fix  string
	}{
		{"every service answers", "pass", []string{up.URL, up.URL, up.URL}, "reachable", ""},
		{"PaperMC answers 503, as on 1 Oct", "warn", []string{busy.URL, up.URL, up.URL}, "PaperMC answered HTTP 503", ""},
		{"PaperMC's downloads don't answer", "warn", []string{up.URL, gone, up.URL}, "PaperMC's downloads (fill-data.papermc.io) didn't answer", "fill-data.papermc.io"},
		{"nothing answers", "fail", []string{gone, gone, gone}, "can't reach", "Allow outbound HTTPS"},
	} {
		set(tc.urls...)
		c := row()
		if c.Status != tc.status || !strings.Contains(c.Detail, tc.detail) || !strings.Contains(c.Fix, tc.fix) {
			t.Errorf("%s: got %s %q (fix %q), want %s with %q (fix %q)", tc.name, c.Status, c.Detail, c.Fix, tc.status, tc.detail, tc.fix)
		}
	}
}

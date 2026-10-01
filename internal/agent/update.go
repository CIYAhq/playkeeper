package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/update"
	"github.com/CIYAhq/playkeeper/internal/version"
)

const (
	kvUpdateCheck  = "update_check"
	kvUpdateResult = "update_result"
	// kvUpdateAuto is "off" once the owner turned the automatic check off.
	kvUpdateAuto = "update_auto"
	// kvUpdateAbandoned is the operation of the last update the agent gave
	// up waiting for, so a restart does not wait for it again.
	kvUpdateAbandoned = "update_abandoned"
	// updaterStartTimeout is how long a staged update may wait for systemd to
	// start the updater before the agent gives up on it.
	updaterStartTimeout = 2 * time.Minute
	// updaterRunTimeout bounds how long the agent refuses other operations
	// while the updater works.
	updaterRunTimeout = 30 * time.Minute
)

// The automatic check (updateLoop): a random moment in the first
// UpdateCheckFirst after the agent starts, then every UpdateCheckInterval,
// give or take checkJitter of it at random, so machines that started
// together drift apart and a release doesn't reach them all at once. A check
// whose first source failed waits twice as long for each failure in a row,
// up to checkBackoffMax, and at least what a source asked for with
// Retry-After, up to retryAfterMax.
const (
	checkJitter     = 0.2
	checkBackoffMax = 6 * time.Hour
	retryAfterMax   = 24 * time.Hour
	// checkTimeout bounds each source's answer.
	checkTimeout = 20 * time.Second
)

type updateState struct {
	mu         sync.Mutex
	latest     *update.Manifest
	checkedAt  time.Time
	checkErr   string
	installing string
	opID       string
	since      time.Time
	lastResult *api.UpdateResult
	// cached is the latest release as each source served it last, by its
	// location, so a check asks it only whether it changed.
	cached map[string]*update.Cached
	// checking is closed when the check in progress ends; a check asked
	// for meanwhile waits for it rather than asking again, and waiting
	// counts those.
	checking chan struct{}
	waiting  int
	// next is when the automatic check runs, failures how many checks in a
	// row found their first source failing, and off whether the owner
	// turned the automatic check off. kick wakes the loop.
	next     time.Time
	failures int
	off      bool
	kick     chan struct{}
}

type savedCheck struct {
	Latest    *update.Manifest `json:"latest,omitempty"`
	CheckedAt time.Time        `json:"checkedAt"`
	Error     string           `json:"error,omitempty"`
	// Cached is updateState.cached, so a check after a restart asks only
	// whether the release changed too (0.4.9).
	Cached map[string]*update.Cached `json:"cached,omitempty"`
}

func (a *Agent) updateDir() string { return filepath.Join(a.cfg.AgentDir(), "update") }

func (a *Agent) updateSource() update.Source {
	base := a.cfg.ReleaseURL
	if base == "" {
		base = update.DefaultReleaseURL
	}
	return update.Source{BaseURL: base, Client: a.opts.HTTPClient}
}

// checkSources are where a check looks for the latest release, in order: the
// release location config.json names, or else playkeeper.io's copy of the
// latest release, then GitHub's own when playkeeper.io doesn't answer with
// one. Downloads always come from updateSource.
func (a *Agent) checkSources() []update.Source {
	if a.cfg.ReleaseURL != "" {
		return []update.Source{a.updateSource()}
	}
	return []update.Source{{BaseURL: update.CheckURL, Client: a.opts.HTTPClient}, a.updateSource()}
}

// updatesUnsupported says why this build cannot install updates, if it cannot.
func (a *Agent) updatesUnsupported() string {
	switch {
	case len(a.opts.UpdateKeys) == 0:
		return "This build has no release signing key, so it cannot check updates for authenticity. Update with the one-line installer."
	case a.cfg.Dev:
		return "Updates are installed by Playkeeper's updater service on an installed server, not in `playkeeper dev`."
	}
	if _, err := update.ParseVersion(version.Version); err != nil {
		return fmt.Sprintf("This is a development build (%s); it does not install releases.", version.Version)
	}
	return ""
}

// loadUpdateState restores the last check and result, and notices an update
// the updater is still working on.
func (a *Agent) loadUpdateState() {
	u := &a.upd
	if v, ok, _ := a.kvGet(kvUpdateCheck); ok {
		var s savedCheck
		if json.Unmarshal([]byte(v), &s) == nil {
			u.latest, u.checkedAt, u.checkErr, u.cached = s.Latest, s.CheckedAt, s.Error, s.Cached
		}
	}
	if v, _, _ := a.kvGet(kvUpdateAuto); v == "off" {
		u.off = true
	}
	u.next = a.now().Add(time.Duration(a.opts.UpdateJitter() * float64(a.opts.UpdateCheckFirst)))
	if v, ok, _ := a.kvGet(kvUpdateResult); ok {
		var r api.UpdateResult
		if json.Unmarshal([]byte(v), &r) == nil {
			u.lastResult = &r
		}
	}
	abandoned, _, _ := a.kvGet(kvUpdateAbandoned)
	for _, f := range []string{update.RequestFile, update.ApplyingFile} {
		p := filepath.Join(a.updateDir(), f)
		var req update.Request
		b, err := os.ReadFile(p)
		if err != nil || json.Unmarshal(b, &req) != nil {
			continue
		}
		if req.OpID != "" && req.OpID == abandoned {
			continue
		}
		// The handoff timeouts count from when the update was handed over,
		// not from this start.
		since := a.now()
		if st, err := os.Stat(p); err == nil && st.ModTime().Before(since) {
			since = st.ModTime()
		}
		u.installing, u.opID, u.since = req.Version, req.OpID, since
	}
}

func (a *Agent) installingUpdate() string {
	a.upd.mu.Lock()
	defer a.upd.mu.Unlock()
	return a.upd.installing
}

func (a *Agent) updateInfo() api.UpdateInfo {
	info := api.UpdateInfo{Current: version.Version}
	if reason := a.updatesUnsupported(); reason != "" {
		info.Reason = reason
	} else {
		info.Supported = true
	}
	u := &a.upd
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.checkedAt.IsZero() {
		t := u.checkedAt
		info.CheckedAt = &t
	}
	info.CheckError, info.Installing, info.LastResult, info.AutoCheck = u.checkErr, u.installing, u.lastResult, !u.off
	if u.latest != nil {
		info.Latest, info.Notes, info.ReleaseDate = u.latest.Version, u.latest.Notes, u.latest.Date
		if c, err := update.CompareVersions(u.latest.Version, version.Version); err == nil && c > 0 && info.Supported {
			info.Available = true
		}
	}
	return info
}

// checkUpdate fetches the latest release's manifest from the first source
// that has it (checkSources) and keeps it only if its signature verifies.
// The button in Settings and the automatic check are this one check; the
// automatic one passes conditional, asking each source only whether the
// release changed since the last check. A check asked for while another runs
// waits for that one and answers what it found, however many ask.
func (a *Agent) checkUpdate(conditional bool) api.UpdateInfo {
	if a.updatesUnsupported() != "" {
		return a.updateInfo()
	}
	u := &a.upd
	u.mu.Lock()
	if running := u.checking; running != nil {
		u.waiting++
		u.mu.Unlock()
		select {
		case <-running:
		case <-a.ctx.Done():
		}
		u.mu.Lock()
		u.waiting--
		u.mu.Unlock()
		return a.updateInfo()
	}
	done := make(chan struct{})
	u.checking = done
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.checking = nil
		u.mu.Unlock()
		close(done)
	}()

	var rel *update.Release
	var errs []string
	var firstFailed bool
	var wait time.Duration
	for i, src := range a.checkSources() {
		var prev *update.Cached
		if conditional {
			u.mu.Lock()
			prev = u.cached[src.BaseURL]
			u.mu.Unlock()
		}
		ctx, cancel := context.WithTimeout(a.ctx, checkTimeout)
		got, cached, err := src.LatestSince(ctx, a.opts.UpdateKeys, prev)
		cancel()
		if err != nil {
			a.log.Warn("update check failed", "source", src.BaseURL, "err", err)
			errs = append(errs, err.Error())
			firstFailed = firstFailed || i == 0
			if se := (*update.StatusError)(nil); errors.As(err, &se) {
				wait = max(wait, min(se.RetryAfter, retryAfterMax))
			}
			continue
		}
		rel = got
		u.mu.Lock()
		if u.cached == nil {
			u.cached = map[string]*update.Cached{}
		}
		u.cached[src.BaseURL] = cached
		u.mu.Unlock()
		break
	}
	u.mu.Lock()
	u.checkedAt = a.now().UTC()
	if rel == nil {
		u.checkErr = "Could not check for updates: " + strings.Join(errs, "; ")
	} else {
		u.latest, u.checkErr = rel.Manifest, ""
	}
	if firstFailed {
		u.failures++
	} else {
		u.failures = 0
	}
	u.next = a.now().Add(checkWait(a.opts.UpdateCheckInterval, u.failures, wait, a.opts.UpdateJitter()))
	saved, _ := json.Marshal(savedCheck{Latest: u.latest, CheckedAt: u.checkedAt, Error: u.checkErr, Cached: u.cached})
	u.mu.Unlock()
	_ = a.kvSet(kvUpdateCheck, string(saved))
	info := a.updateInfo()
	if rel != nil && info.Available {
		a.alertUpdate(info.Latest)
	}
	return info
}

// checkWait is how long the automatic check waits after a check: interval,
// doubled for each failure in a row up to checkBackoffMax, at least
// retryAfter, give or take checkJitter of it (r is at random in [0, 1)).
func checkWait(interval time.Duration, failures int, retryAfter time.Duration, r float64) time.Duration {
	if interval <= 0 {
		return max(retryAfter, 0)
	}
	wait := interval
	for range failures {
		if wait >= checkBackoffMax {
			break
		}
		wait *= 2
	}
	wait = min(wait, max(checkBackoffMax, interval))
	wait = time.Duration(float64(wait) * (1 - checkJitter + 2*checkJitter*r))
	return max(wait, retryAfter)
}

// autoCheckDue reports whether the automatic check should run now: it's
// time, the owner hasn't turned it off, this build can install releases, and
// the machine is no other dashboard's, which finds releases for it.
func (a *Agent) autoCheckDue() bool {
	u := &a.upd
	u.mu.Lock()
	due := a.opts.UpdateCheckInterval > 0 && !u.off && !a.now().Before(u.next)
	u.mu.Unlock()
	return due && a.updatesUnsupported() == "" && !a.joined()
}

func (a *Agent) hUpdate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.updateInfo())
}

// hUpdateCheck is Settings' Check for updates: the whole release again, not
// only whether it changed.
func (a *Agent) hUpdateCheck(w http.ResponseWriter, r *http.Request) {
	var req api.UpdateCheckRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if _, err := validActor(req.Actor); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.checkUpdate(false))
}

// hUpdateAuto is Settings' Check for updates automatically. Turned on, the
// agent looks at once.
func (a *Agent) hUpdateAuto(w http.ResponseWriter, r *http.Request) {
	var req api.UpdateAutoRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	v := "off"
	if req.On {
		v = "on"
	}
	if err := a.kvSet(kvUpdateAuto, v); err != nil {
		writeError(w, err)
		return
	}
	u := &a.upd
	u.mu.Lock()
	u.off = !req.On
	if req.On {
		u.next = a.now()
	}
	u.mu.Unlock()
	a.audit(actor, "update_checks."+v, "", "succeeded", "")
	if req.On {
		select {
		case u.kick <- struct{}{}:
		default:
		}
	}
	writeJSON(w, http.StatusOK, a.updateInfo())
}

func (a *Agent) hUpdateApply(w http.ResponseWriter, r *http.Request) {
	var req api.UpdateApplyRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	if reason := a.updatesUnsupported(); reason != "" {
		writeError(w, errConflict(reason, ""))
		return
	}
	if _, err := update.ParseVersion(req.Version); err != nil {
		writeError(w, errInvalid("Choose the version to install."))
		return
	}
	op, err := a.beginMachineOp("update", actor, func(ctx context.Context, h *opHandle) error {
		return a.stageUpdate(ctx, h, req.Version, actor)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

// stageUpdate downloads the chosen release, checks its signature, size and
// checksums, and hands it to the updater. The agent cannot replace its own
// binary (its sandbox is read-only outside /var/lib/playkeeper); systemd
// starts the updater when the request file appears.
func (a *Agent) stageUpdate(ctx context.Context, h *opHandle, want, actor string) error {
	current := version.Version
	h.phase("checking")
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	rel, err := a.updateSource().Latest(cctx, a.opts.UpdateKeys)
	cancel()
	if err != nil {
		return &apiError{Msg: "Could not get the update: " + err.Error(), Hint: "Nothing was changed. Check this server's internet access, then try again."}
	}
	m := rel.Manifest
	if m.Version != want {
		return errConflict(fmt.Sprintf("The latest release is now %s, not %s.", m.Version, want), "Check for updates again and review what changed.")
	}
	if c, err := update.CompareVersions(m.Version, current); err != nil || c <= 0 {
		return errConflict(fmt.Sprintf("Playkeeper %s is not newer than the installed %s; Playkeeper never goes back to an older version.", m.Version, current), "")
	}
	asset, err := m.Asset()
	if err != nil {
		return err
	}
	if free, _, err := a.opts.DiskUsage(a.cfg.DataDir); err == nil && free < 3*asset.Size+(256<<20) {
		return &apiError{Code: api.CodeInsufficientSpace, Msg: fmt.Sprintf("Not enough disk space to download the update: %s free.", humanBytes(free)), Hint: "Free some disk space, then try again."}
	}
	staged := filepath.Join(a.updateDir(), update.StagedDir)
	if err := os.RemoveAll(staged); err != nil {
		return err
	}
	if err := os.MkdirAll(staged, 0o700); err != nil {
		return err
	}
	fail := func(err error) error {
		os.RemoveAll(staged)
		return err
	}
	h.phase("downloading")
	h.set("version", m.Version)
	tarball := filepath.Join(staged, asset.File)
	if err := a.updateSource().Download(ctx, asset, tarball); err != nil {
		return fail(&apiError{Msg: "The update could not be downloaded safely: " + err.Error(), Hint: "Nothing was changed. Try again later."})
	}
	h.phase("verifying")
	bin := filepath.Join(staged, update.BinaryFile)
	if err := update.ExtractBinary(tarball, asset, bin); err != nil {
		return fail(&apiError{Msg: "The update did not pass its checks: " + err.Error(), Hint: "Nothing was changed."})
	}
	os.Remove(tarball)
	if err := os.WriteFile(filepath.Join(staged, update.ManifestFile), rel.Raw, 0o600); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(filepath.Join(staged, update.SignatureFile), rel.Signature, 0o600); err != nil {
		return fail(err)
	}
	if v, err := a.opts.BinaryVersion(bin); err != nil || v != m.Version {
		return fail(&apiError{Msg: fmt.Sprintf("The downloaded Playkeeper reports version %q instead of %s (%v).", v, m.Version, err), Hint: "Nothing was changed."})
	}
	req := update.Request{OpID: h.op.ID, Version: m.Version, From: current, Actor: actor, RequestedAt: a.now().UTC()}
	b, _ := json.MarshalIndent(req, "", "  ")
	tmp := filepath.Join(a.updateDir(), "."+update.RequestFile)
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fail(err)
	}
	u := &a.upd
	u.mu.Lock()
	u.installing, u.opID, u.since = m.Version, h.op.ID, a.now()
	u.mu.Unlock()
	if err := os.Rename(tmp, filepath.Join(a.updateDir(), update.RequestFile)); err != nil {
		a.clearInstalling()
		return fail(err)
	}
	h.continues = true
	h.set("from", current)
	h.phase("restarting")
	a.audit(actor, "update.requested", m.Version, "handed to the updater", "from "+current)
	a.log.Info("update staged; waiting for the updater", "version", m.Version)
	return nil
}

func (a *Agent) clearInstalling() {
	a.upd.mu.Lock()
	a.upd.installing, a.upd.opID = "", ""
	a.upd.mu.Unlock()
}

// collectUpdateResult reports what the updater did: the update operation is
// finished with the outcome, and the result is kept for the dashboard.
func (a *Agent) collectUpdateResult() {
	path := filepath.Join(a.updateDir(), update.ResultFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var res update.Result
	if err := json.Unmarshal(b, &res); err != nil {
		a.log.Error("unreadable update result", "err", err)
		os.Rename(path, path+".unreadable")
		return
	}
	status, msg, hint := api.OpFailed, "", ""
	switch res.Outcome {
	case update.OutcomeUpdated:
		status = api.OpSucceeded
	case update.OutcomeRolledBack:
		msg = fmt.Sprintf("Playkeeper %s did not work, so %s was put back and is running. What went wrong: %s.", res.To, res.From, res.Error)
		hint = "Nothing else changed. Your server and worlds were not touched."
	case update.OutcomeRefused:
		msg = fmt.Sprintf("Playkeeper %s was not installed: %s.", res.To, res.Error)
		hint = "Nothing was changed."
	default:
		msg = fmt.Sprintf("The update to Playkeeper %s failed, and putting the previous version back failed too: %s", res.To, res.Error)
		hint = "See docs/RECOVERY.md (\"A Playkeeper update went wrong\")."
	}
	r := api.UpdateResult{From: res.From, To: res.To, Outcome: res.Outcome, Error: msg, FinishedAt: res.FinishedAt}
	rb, _ := json.Marshal(r)
	actor, action, detail := nonEmptyOr(res.Actor, "playkeeper"), "update."+res.Outcome, strings.TrimSpace("from "+res.From+" "+res.Error)
	a.upd.mu.Lock()
	defer a.upd.mu.Unlock()
	var op *api.Operation
	if res.OpID != "" {
		op, _ = a.loadOperation(res.OpID)
	}
	if op != nil {
		fin := res.FinishedAt
		op.Status, op.Error, op.Hint, op.Phase, op.FinishedAt = status, msg, hint, res.Outcome, &fin
		if op.Detail == nil {
			op.Detail = map[string]any{}
		}
		op.Detail["from"], op.Detail["to"] = res.From, res.To
		a.endUpdateOp(op, actor, action, res.To, res.Outcome, detail)
	} else {
		a.audit(actor, action, res.To, res.Outcome, detail)
	}
	_ = a.kvSet(kvUpdateResult, string(rb))
	a.upd.lastResult = &r
	a.upd.installing, a.upd.opID = "", ""
	os.Remove(path)
	a.log.Info("update finished", "outcome", res.Outcome, "from", res.From, "to", res.To)
}

// watchHandoff gives up on an update the updater never picked up (for
// example when playkeeper-update.path is not enabled) or never reported on,
// so the dashboard is not blocked forever and the operation says what
// happened. A result that arrives later still replaces it.
func (a *Agent) watchHandoff() {
	u := &a.upd
	u.mu.Lock()
	installing, opID, since := u.installing, u.opID, u.since
	u.mu.Unlock()
	if installing == "" {
		return
	}
	dir := a.updateDir()
	waited := a.now().Sub(since)
	if _, err := os.Stat(filepath.Join(dir, update.RequestFile)); err == nil && waited > updaterStartTimeout {
		os.Remove(filepath.Join(dir, update.RequestFile))
		os.RemoveAll(filepath.Join(dir, update.StagedDir))
		a.abandonUpdate(opID, installing, update.OutcomeRefused,
			fmt.Sprintf("Playkeeper %s was downloaded and verified, but the updater did not start, so nothing was changed.", installing),
			"On the server, check: sudo systemctl status playkeeper-update.path")
		return
	}
	if waited > updaterRunTimeout {
		a.log.Warn("the updater has not reported after a long time", "version", installing)
		a.abandonUpdate(opID, installing, update.OutcomeFailed,
			fmt.Sprintf("The updater has not said how the update to Playkeeper %s ended after %d minutes. Playkeeper %s is running.", installing, int(updaterRunTimeout.Minutes()), version.Version),
			"On the server, check: sudo journalctl -u playkeeper-update. See docs/RECOVERY.md (\"A Playkeeper update went wrong\").")
	}
}

// abandonUpdate finishes an update operation the updater did not report on.
func (a *Agent) abandonUpdate(opID, v, outcome, msg, hint string) {
	r := api.UpdateResult{From: version.Version, To: v, Outcome: outcome, Error: msg, FinishedAt: a.now().UTC()}
	rb, _ := json.Marshal(r)
	a.upd.mu.Lock()
	defer a.upd.mu.Unlock()
	if op, err := a.loadOperation(opID); err == nil {
		fin := a.now().UTC()
		op.Status, op.Error, op.Hint, op.Phase, op.FinishedAt = api.OpFailed, msg, hint, outcome, &fin
		a.endUpdateOp(op, "playkeeper", "update."+outcome, v, outcome, msg)
	} else {
		a.audit("playkeeper", "update."+outcome, v, outcome, msg)
	}
	_ = a.kvSet(kvUpdateResult, string(rb))
	_ = a.kvSet(kvUpdateAbandoned, opID)
	a.upd.lastResult = &r
	a.upd.installing, a.upd.opID = "", ""
}

// endUpdateOp stores the update operation op as ended together with its
// audit entry, the entry first, as finishOperation does for the others. The
// caller holds the update's lock until what the agent says about the update
// has changed too, so whoever sees the operation ended, or the agent free,
// finds the update's result.
func (a *Agent) endUpdateOp(op *api.Operation, actor, action, target, result, detail string) {
	tx, err := a.db.Begin()
	if err == nil {
		if err = a.insertAudit(tx, "", actor, action, target, result, detail); err == nil {
			err = writeOperation(tx, op)
		}
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
	}
	if err != nil {
		// The operation must not stay running for want of its audit entry.
		a.log.Error("update operation write failed", "err", err)
		a.saveOperation(op)
		a.audit(actor, action, target, result, detail)
	}
}

// updateLoop reports updater results, times out a handoff nobody picked up,
// checks for a new Playkeeper release when the agent starts and about every
// half hour (see checkWait), and twice a day looks for newer Minecraft
// versions for the servers.
func (a *Agent) updateLoop(ctx context.Context) {
	a.collectUpdateResult()
	nextMinecraft := a.now().Add(time.Minute)
	t := time.NewTicker(a.opts.ReconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-a.upd.kick:
		}
		a.collectUpdateResult()
		a.watchHandoff()
		if a.autoCheckDue() {
			a.checkUpdate(true)
		}
		if a.opts.MinecraftCheckInterval > 0 && a.now().After(nextMinecraft) {
			nextMinecraft = a.now().Add(a.opts.MinecraftCheckInterval)
			a.alertMinecraftUpdates(ctx)
		}
	}
}

// joined reports whether this machine belongs to another machine's
// dashboard: installed to join one, or linked to one since.
func (a *Agent) joined() bool {
	_, err := os.Stat(a.cfg.LinkDashboardPath())
	return a.cfg.NoPanel || err == nil
}

// binaryVersion runs `<binary> version` (a staged update, before handing it
// to the updater).
func binaryVersion(bin string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(out))
	if len(f) < 2 || f[0] != "playkeeper" {
		return "", errors.New("unexpected output")
	}
	return f[1], nil
}

func nonEmptyOr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

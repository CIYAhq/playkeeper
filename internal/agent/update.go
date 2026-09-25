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

type updateState struct {
	mu         sync.Mutex
	latest     *update.Manifest
	checkedAt  time.Time
	checkErr   string
	installing string
	opID       string
	since      time.Time
	lastResult *api.UpdateResult
}

type savedCheck struct {
	Latest    *update.Manifest `json:"latest,omitempty"`
	CheckedAt time.Time        `json:"checkedAt"`
	Error     string           `json:"error,omitempty"`
}

func (a *Agent) updateDir() string { return filepath.Join(a.cfg.AgentDir(), "update") }

func (a *Agent) updateSource() update.Source {
	base := a.cfg.ReleaseURL
	if base == "" {
		base = update.DefaultReleaseURL
	}
	return update.Source{BaseURL: base, Client: a.opts.HTTPClient}
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
			u.latest, u.checkedAt, u.checkErr = s.Latest, s.CheckedAt, s.Error
		}
	}
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
	info.CheckError, info.Installing, info.LastResult = u.checkErr, u.installing, u.lastResult
	if u.latest != nil {
		info.Latest, info.Notes, info.ReleaseDate = u.latest.Version, u.latest.Notes, u.latest.Date
		if c, err := update.CompareVersions(u.latest.Version, version.Version); err == nil && c > 0 && info.Supported {
			info.Available = true
		}
	}
	return info
}

// checkUpdate fetches the latest release's manifest and keeps it only if its
// signature verifies.
func (a *Agent) checkUpdate(ctx context.Context) api.UpdateInfo {
	if a.updatesUnsupported() != "" {
		return a.updateInfo()
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rel, err := a.updateSource().Latest(cctx, a.opts.UpdateKeys)
	u := &a.upd
	u.mu.Lock()
	u.checkedAt = a.now().UTC()
	if err != nil {
		u.checkErr = "Could not check for updates: " + err.Error()
		a.log.Warn("update check failed", "err", err)
	} else {
		u.latest, u.checkErr = rel.Manifest, ""
	}
	saved, _ := json.Marshal(savedCheck{Latest: u.latest, CheckedAt: u.checkedAt, Error: u.checkErr})
	u.mu.Unlock()
	_ = a.kvSet(kvUpdateCheck, string(saved))
	return a.updateInfo()
}

func (a *Agent) hUpdate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.updateInfo())
}

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
	writeJSON(w, http.StatusOK, a.checkUpdate(r.Context()))
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
	op, err := a.beginOp("update", actor, func(ctx context.Context, h *opHandle) error {
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
	if res.OpID != "" {
		if op, err := a.loadOperation(res.OpID); err == nil {
			fin := res.FinishedAt
			op.Status, op.Error, op.Hint, op.Phase, op.FinishedAt = status, msg, hint, res.Outcome, &fin
			if op.Detail == nil {
				op.Detail = map[string]any{}
			}
			op.Detail["from"], op.Detail["to"] = res.From, res.To
			a.saveOperation(op)
		}
	}
	a.audit(nonEmptyOr(res.Actor, "playkeeper"), "update."+res.Outcome, res.To, res.Outcome, strings.TrimSpace("from "+res.From+" "+res.Error))
	r := api.UpdateResult{From: res.From, To: res.To, Outcome: res.Outcome, Error: msg, FinishedAt: res.FinishedAt}
	rb, _ := json.Marshal(r)
	_ = a.kvSet(kvUpdateResult, string(rb))
	a.upd.mu.Lock()
	a.upd.lastResult = &r
	a.upd.installing, a.upd.opID = "", ""
	a.upd.mu.Unlock()
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
	if op, err := a.loadOperation(opID); err == nil {
		fin := a.now().UTC()
		op.Status, op.Error, op.Hint, op.Phase, op.FinishedAt = api.OpFailed, msg, hint, outcome, &fin
		a.saveOperation(op)
	}
	r := api.UpdateResult{From: version.Version, To: v, Outcome: outcome, Error: msg, FinishedAt: a.now().UTC()}
	rb, _ := json.Marshal(r)
	_ = a.kvSet(kvUpdateResult, string(rb))
	_ = a.kvSet(kvUpdateAbandoned, opID)
	a.upd.mu.Lock()
	a.upd.lastResult = &r
	a.upd.installing, a.upd.opID = "", ""
	a.upd.mu.Unlock()
	a.audit("playkeeper", "update."+outcome, v, outcome, msg)
}

// updateLoop reports updater results, times out a handoff nobody picked up,
// and checks for new releases now and then.
func (a *Agent) updateLoop(ctx context.Context) {
	a.collectUpdateResult()
	next := a.now().Add(time.Minute)
	t := time.NewTicker(a.opts.ReconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		a.collectUpdateResult()
		a.watchHandoff()
		if a.opts.UpdateCheckInterval > 0 && a.now().After(next) {
			next = a.now().Add(a.opts.UpdateCheckInterval)
			a.checkUpdate(ctx)
		}
	}
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

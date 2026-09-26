#!/usr/bin/env bash
# Negative controls: removes one safety guard at a time in a throwaway git
# worktree and runs the tests that cover it. Every run must FAIL; a control
# that still passes means the guard is untested. Nothing is committed.
# The worktree is made from HEAD, so commit changes before running it.
# Usage: scripts/negative-controls.sh
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
export PATH="$root/.tools/go/bin:$PATH" CGO_ENABLED=0
wt="$(mktemp -d)/playkeeper"
git -C "$root" worktree add --detach -q "$wt" HEAD
trap 'git -C "$root" worktree remove --force "$wt"' EXIT
cd "$wt"
echo "negative controls at $(git rev-parse --short=12 HEAD)"

bad=0
control() { # NAME FILE FROM TO PACKAGE TESTS [RUNS]
  local name=$1 file=$2 pkg=$5 tests=$6 runs=${7:-1}
  FROM=$3 TO=$4 perl -0pi -e 's/\Q$ENV{FROM}\E/$ENV{TO}/ or die "guard not found\n"' "$file"
  if ! go vet "$pkg" >/dev/null 2>&1; then
    echo "INVALID  $name: the mutated code does not build"
    bad=1
  elif go test -count="$runs" "$pkg" -run "$tests" >/tmp/negative-control.out 2>&1; then
    echo "MISSED   $name: $tests still pass without the guard"
    bad=1
  else
    echo "caught   $name: $(grep -m1 -E '^\s+[a-z0-9_]+_test\.go:[0-9]+:' /tmp/negative-control.out | sed 's/^\s*//')"
  fi
  git checkout -q -- "$file"
}

control "CSRF token check" internal/panel/server.go \
  'if !s.sameOrigin(r) || tok == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(sess.CSRF)) != 1 {' \
  'if false && (!s.sameOrigin(r) || tok == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(sess.CSRF)) != 1) {' \
  ./internal/panel '^TestEveryRouteRequiresSessionAndCSRF$'
control "session required" internal/panel/server.go \
  'sess, err := s.sessionFrom(r)
			if err != nil {' \
  'sess, err := s.sessionFrom(r)
			if false && err != nil {' \
  ./internal/panel '^TestEveryRouteRequiresSessionAndCSRF$'
control "idle and absolute session expiry" internal/panel/auth.go \
  'if !now.Before(sess.ExpiresAt) || now.Sub(sess.LastSeen) >= s.opts.IdleTimeout {' \
  'if false && (!now.Before(sess.ExpiresAt) || now.Sub(sess.LastSeen) >= s.opts.IdleTimeout) {' \
  ./internal/panel '^TestSessionIdleAndAbsoluteExpiry$'
control "per-address sign-in and setup rate limit" internal/panel/server.go \
  'if ok, wait := s.loginIP.allow(limitKey(clientIP(r))); !ok {' \
  'if ok, wait := s.loginIP.allow(limitKey(clientIP(r))); false && !ok {' \
  ./internal/panel '^TestSignInAndSetupAreRateLimitedPerAddress$'
control "agent socket peer allowlist" internal/agent/agent.go \
  'if err != nil || !a.allowed[uid] {' \
  'if false && (err != nil || !a.allowed[uid]) {' \
  ./internal/agent '^TestSocketRejectsUnlistedPeers$'
control "agent closed operation list" internal/agent/agent.go \
  'writeErr(w, http.StatusNotFound, api.CodeNotFound, "Unknown agent operation.", "")' \
  'writeJSON(w, http.StatusOK, map[string]any{"ok": true})' \
  ./internal/agent '^TestInvalidInputsAndUnknownVerbsAreRejected$'
control "EULA gate" internal/agent/handlers.go \
  'if !req.AcceptEULA {' \
  'if false && !req.AcceptEULA {' \
  ./internal/agent '^TestEULAGateRefusesAndDownloadsNothing$'
control "restore typed confirmation" internal/agent/handlers.go \
  'if strings.TrimSpace(req.Confirm) != p.ConfirmPhrase {' \
  'if false && strings.TrimSpace(req.Confirm) != p.ConfirmPhrase {' \
  ./internal/agent '^TestBackupRestoreRollbackAndRefusals$'
control "restore needs a verified rollback archive" internal/agent/backups.go \
  'if vb.Verified == nil || !*vb.Verified {
		return nil, fmt.Errorf(' \
  'if false {
		return nil, fmt.Errorf(' \
  ./internal/agent '^TestRestoreNeedsAVerifiedRollbackArchive$'
control "restore undoes the swap when settings cannot be saved" internal/agent/backups.go \
  'if rerr := renameDir(live, failedAt); rerr != nil {' \
  'if rerr := error(nil); rerr != nil {' \
  ./internal/agent '^TestRestoreUndoesTheSwapWhenSettingsCannotBeSaved$'
control "one admin from concurrent setups" internal/panel/auth.go \
  'SELECT ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM users)' \
  'SELECT ?, ?, ?, ?' \
  ./internal/panel '^TestConcurrentSetupsCreateOneAdmin$' 3
control "archive per-file checksums" internal/backup/archive.go \
  'if got.Size != f.Size || got.SHA256 != f.SHA256 {' \
  'if false && (got.Size != f.Size || got.SHA256 != f.SHA256) {' \
  ./internal/backup '^(TestMaliciousArchivesAreRefused|TestFlippedByteIsRefused|TestTruncatedArchiveIsRefused)$'
control "archive gzip trailer check" internal/backup/archive.go \
  'if _, err := io.Copy(io.Discard, gz); err != nil {' \
  'if _, err := io.Copy(io.Discard, gz); false && err != nil {' \
  ./internal/backup '^TestTruncatedArchiveIsRefused$'
control "archive path check refuses .. parts" internal/backup/archive.go \
  'if part == "" || part == "." || part == ".." {' \
  'if part == "" || part == "." {' \
  ./internal/backup '^(TestMaliciousArchivesAreRefused|TestValidRelRefusesUnsafePaths)$'
control "extraction stays inside its destination" internal/backup/archive.go \
  'if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {' \
  'if false && !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {' \
  ./internal/backup '^TestExtractFileStaysInsideDestination$'
control "backup creation applies the restore rules" internal/backup/archive.go \
  'if err := tally.add(rel, size); err != nil {' \
  'if err := tally.add(rel, size); false && err != nil {' \
  ./internal/backup '^(TestCreateRefusesNamesARestoreRefuses|TestCreateAndVerifyAgreeOnLimits)$'
control "backup check compares the whole-archive SHA-256" internal/agent/backups.go \
  'if got := hex.EncodeToString(h.Sum(nil)); got != b.SHA256 {' \
  'if got := hex.EncodeToString(h.Sum(nil)); false && got != b.SHA256 {' \
  ./internal/agent '^TestRecompressedBackupFailsItsRecordedChecksum$'
control "restore from a backup compares the whole-archive SHA-256" internal/agent/handlers.go \
  'if p.SHA256 != b.SHA256 {' \
  'if false && p.SHA256 != b.SHA256 {' \
  ./internal/agent '^TestRecompressedBackupFailsItsRecordedChecksum$'
control "a console command that was written is never sent again" internal/agent/collector.go \
  'if attempt > 0 || !errors.Is(err, minecraft.ErrNotSent) || ctx.Err() != nil {' \
  'if attempt > 0 || ctx.Err() != nil {' \
  ./internal/agent '^(TestLostConsoleRepliesAreNeverResent|TestLostSaveOnReplyKeepsTheBackup)$'
control "a lost console reply says the command may have run" internal/minecraft/rcon.go \
  'gotID, _, body, err := r.read()
		if err != nil {
			return "", r.ctxErr(ctx, err)' \
  'gotID, _, body, err := r.read()
		if err != nil {
			return "", notSent(r.ctxErr(ctx, err))' \
  ./internal/minecraft '^TestRCONCommandContextNeverResendsAndHonoursTheContext$'
control "a failed online backup turns saving back on" internal/backup/online.go \
  'if !r.paused || r.resumeTried {' \
  'if !r.paused || r.resumeTried || true {' \
  ./internal/backup '^(TestSavingIsTurnedBackOnAfterEveryFailure|TestSavingIsTurnedBackOnAfterAPanic)$'
control "a failed online backup turns saving back on (agent)" internal/backup/online.go \
  'if !r.paused || r.resumeTried {' \
  'if !r.paused || r.resumeTried || true {' \
  ./internal/agent '^TestFailedOnlineBackupTurnsSavingBackOn$'
control "saving left paused by a backup is remembered" internal/agent/backups.go \
  's.setSavingPaused(paused)' \
  's.setSavingPaused(false)' \
  ./internal/agent '^TestSavingLeftPausedIsShownAndTurnedBackOn$'
control "a backup that can't fit never stops the server" internal/agent/backups.go \
  'if err := backup.CheckSpace(ctx, o); err != nil {' \
  'if err := backup.CheckSpace(ctx, o); false && err != nil {' \
  ./internal/agent '^TestBackupWithoutRoomNeverTouchesTheServer$'
control "the reconciler turns saving back on" internal/agent/backups.go \
  'due := !s.now().Before(s.nextResume)' \
  'due := false && !s.now().Before(s.nextResume)' \
  ./internal/agent '^TestReconcilerTurnsSavingBackOn$'
control "the GC log flag stays out of the container definition's hash" internal/agent/lifecycle.go \
  'b, _ := json.Marshal(cfg)' \
  'cfg.Env = append(cfg.Env, "JVM_OPTS="+gcLogFlag)
	b, _ := json.Marshal(cfg)' \
  ./internal/agent '^TestMigratedServerKeepsItsExactContainerDefinition$'
control "a running server never needs a restart for the GC log" internal/agent/handlers.go \
  'st.PendingRestart = c.Config.Labels[labelSpec] != hash' \
  'st.PendingRestart = c.Config.Labels[labelSpec] != hash || c.Config.Labels[labelGCLog] != gcLogVersion' \
  ./internal/agent '^TestGCLogFlagAppliesFromTheNextStart$'
control "a stopped server gets the GC log at its next start" internal/agent/lifecycle.go \
  'case err == nil && (c.Config.Labels[labelSpec] != hash || c.Config.Labels[labelGCLog] != gcLogVersion):' \
  'case err == nil && c.Config.Labels[labelSpec] != hash:' \
  ./internal/agent '^TestGCLogFlagAppliesFromTheNextStart$'
control "a GC log line still being written is not read" internal/agent/running.go \
  "end := bytes.LastIndexByte(buf, '\n')" \
  "end := max(bytes.LastIndexByte(buf, '\n'), len(buf))" \
  ./internal/agent '^TestGCLogIsReadOnce$'
control "the GC log cursor is stored with the pauses it read" internal/agent/running.go \
  'UPDATE servers SET gc_cursor = ? WHERE id = ?`, string(b), s.id)' \
  'UPDATE servers SET gc_cursor = ? WHERE id = ?`, string(b), "")' \
  ./internal/agent '^TestGCLogIsReadOnce$'
control "the GC log is read through the game-file helper" internal/agent/running.go \
  'buf, st, err := d.ReadRange(gcLogRel, cur.Offset, gcReadLimit)' \
  'f, err := http.Dir(s.dataDir()).Open(gcLogRel)
	if err != nil {
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	f.Seek(cur.Offset, 0)
	buf := make([]byte, gcReadLimit)
	n, _ := f.Read(buf)
	buf = buf[:n]' \
  ./internal/agent '^TestGCLogIsReadOnce$'
control "chunk counts read region folders only" internal/agent/running.go \
  'e.Type().IsRegular() && path.Base(dir) == "region" && strings.HasSuffix(p, ".mca")' \
  'e.Type().IsRegular() && path.Base(dir) != "" && strings.HasSuffix(p, ".mca")' \
  ./internal/agent '^TestNewChunksComeFromRegionFiles$'
control "a crash that logs Stopping server is still a crash" internal/agent/lifecycle.go \
  'graceful := s.sawStopping && !s.sawCrash' \
  'graceful := s.sawStopping' \
  ./internal/agent '^TestCrashIsExplainedFromTheRunsLog$'
control "a log line Docker sends again changes nothing" internal/agent/collector.go \
  'if mark.next(c.ID, runStart, l) {' \
  'if mark.next(c.ID, runStart, l) || true {' \
  ./internal/agent '^TestALineReadAgainKeepsTheGiveUpNotice$'
control "a Done line from a run that has stopped is not a start" internal/agent/collector.go \
  'if current && ended.IsZero() {' \
  'if current {' \
  ./internal/agent '^TestADoneLineReadAfterTheExitWasJudgedChangesNothing$'
control "a Done line stamped after a stopped run's end is not a start" internal/agent/collector.go \
  'if current && ended.IsZero() {' \
  'if current && (ended.IsZero() || ts.After(ended)) {' \
  ./internal/agent '^TestADoneLineStampedAfterTheRunEndedChangesNothing$'
control "a stopped run's log is read without following" internal/agent/collector.go \
  'docker.LogsOptions{Follow: c.State.Running, Since: since}' \
  'docker.LogsOptions{Follow: true, Since: since}' \
  ./internal/agent '^TestARunStartedAsTheFollowerAttachesIsReadAsItsOwn$'
control "the crash helper reads the run's log from Docker" internal/agent/crash.go \
  'in.Console = s.runLog(ctx, id, runStart)' \
  'in.Console = s.runLog(ctx, id, runStart)[:0]' \
  ./internal/agent '^TestCrashIsExplainedFromTheRunsLog$'
control "a crash report from an earlier run explains nothing" internal/agent/crash.go \
  'info.ModTime().Before(from) ||' \
  'false ||' \
  ./internal/agent '^TestCrashReportIsTheOneThisRunWrote$'
control "a start that failed before the server ran reads no report" internal/agent/crash.go \
  'if since.IsZero() {' \
  'if false && since.IsZero() {' \
  ./internal/agent '^TestCrashHelperReadsReportsWithoutFollowingLinksOrWaiting$'
control "crash reports are read only up to their cap" internal/agent/crash.go \
  'b, _, err := d.ReadRange(path.Join(r.dir, name), 0, crashReportLimit)' \
  'b, err := os.ReadFile(s.dataDir() + "/" + path.Join(r.dir, name))' \
  ./internal/agent '^TestCrashHelperReadsReportsWithoutFollowingLinksOrWaiting$'
control "the crash helper lists add-ons without waiting on a pipe" internal/agent/crash.go \
  'entries, err := d.ReadDir(addonDir(sc), maxDirEntries)' \
  'f, err := os.Open(s.dataDir() + "/" + addonDir(sc))
	if err != nil {
		return nil
	}
	defer f.Close()
	entries, err := f.ReadDir(maxDirEntries)' \
  ./internal/agent '^TestCrashHelperReadsReportsWithoutFollowingLinksOrWaiting$'
control "add-on file names are validated" internal/agent/crash.go \
  'return errInvalid("That is not the name of a plugin or mod file.")' \
  'return nil' \
  ./internal/agent '^TestRemoveAddonMovesOnlyThatJarAside$'
control "add-on removal never follows a symlinked folder" internal/agent/crash.go \
  'if st, err := root.Lstat(rel); err != nil || !st.Mode().IsRegular() {' \
  'if st, err := os.Lstat(s.dataDir() + "/" + rel); err != nil || !st.Mode().IsRegular() {' \
  ./internal/agent '^TestRemoveAddonMovesOnlyThatJarAside$'
control "an add-on is not removed while the server runs" internal/agent/crash.go \
  '} else if running {
		writeError(w, errConflict(' \
  '} else if false && running {
		writeError(w, errConflict(' \
  ./internal/agent '^TestRemoveAddonMovesOnlyThatJarAside$'
control "members cannot turn saving back on" internal/panel/server.go \
  'sm("POST", "/api/servers/{id}/saving/resume", "/v1/servers/{id}/saving/resume"),' \
  '{"POST", "/api/servers/{id}/saving/resume", needSessionCSRF, actView, s.serverProxy("POST", "/v1/servers/{id}/saving/resume")},' \
  ./internal/panel '^TestMembersCanLookButNotManage$'
control "members cannot remove a plugin or mod" internal/panel/server.go \
  'sm("POST", "/api/servers/{id}/addons/remove-file", "/v1/servers/{id}/addons/remove-file"),' \
  '{"POST", "/api/servers/{id}/addons/remove-file", needSessionCSRF, actView, s.serverProxy("POST", "/v1/servers/{id}/addons/remove-file")},' \
  ./internal/panel '^TestMembersCanLookButNotManage$'
control "a port holder's name keeps only printable text" internal/agent/portholder.go \
  'if r == unicode.ReplacementChar || !unicode.IsPrint(r) {' \
  'if r == unicode.ReplacementChar && !unicode.IsPrint(r) {' \
  ./internal/agent '^TestPortHolderKeepsOnlyAPlainName$'
control "only the listening socket names a port's holder" internal/agent/portholder.go \
  'if len(fields) < 10 || fields[3] != tcpListen {' \
  'if len(fields) < 10 {' \
  ./internal/agent '^TestPortHolderNeedsTheListeningSocketItself$'
control "a container Playkeeper made isn't named as another program's" internal/agent/portholder.go \
  'if c.Labels[labelManaged] == "true" {' \
  'if false && c.Labels[labelManaged] == "true" {' \
  ./internal/agent '^TestPortCrashNamesTheDockerContainerHoldingThePort$'
control "only a container publishing the game port over TCP is named" internal/agent/portholder.go \
  'if p.PublicPort == port && p.Type == "tcp" {' \
  'if p.PublicPort == port {' \
  ./internal/agent '^TestPortCrashNamesTheDockerContainerHoldingThePort$'
control "a container is named only by a name Docker allows" internal/agent/portholder.go \
  'if n = strings.TrimPrefix(n, "/"); reContainerName.MatchString(n) {' \
  'if n = strings.TrimPrefix(n, "/"); n != "" {' \
  ./internal/agent '^TestPortCrashNamesTheDockerContainerHoldingThePort$'
control "a crash fix whose add-on can't be put in place doesn't start the server" internal/agent/addons.go \
  'if err := s.installAddons(ctx, h, actor, run); err != nil {' \
  'if err := s.installAddons(ctx, h, actor, run); err != nil && !start {' \
  ./internal/agent '^TestAddonFixesStartTheStoppedServer$'
control "a start that isn't accepted keeps the crash" internal/agent/handlers.go \
  'op, err := s.beginOp("start", actor, s.startNow)' \
  's.forgetCrashes(); op, err := s.beginOp("start", actor, s.startNow)' \
  ./internal/agent '^TestAStartThatDoesNotGoAheadKeepsTheCrash$'
control "a remove-and-start keeps the crash until its start goes ahead" internal/agent/crash.go \
  'op, err := s.beginOp("remove-addon", actor, func(ctx context.Context, h *opHandle) error {' \
  'if req.Start { s.forgetCrashes() }; op, err := s.beginOp("remove-addon", actor, func(ctx context.Context, h *opHandle) error {' \
  ./internal/agent '^TestAStartThatDoesNotGoAheadKeepsTheCrash$'
control "preflight port collision" internal/install/install.go \
  'if sys.Listening(p.port) {' \
  'if false && sys.Listening(p.port) {' \
  ./internal/install '^TestPreflightRefusesEachCollisionWithAFix$'
control "preflight existing Minecraft setups" internal/install/install.go \
  'case len(existing) == 0:' \
  'case true:' \
  ./internal/install '^TestPreflightRefusesEachCollisionWithAFix$'
control "start/stop no-op under the operation lock" internal/agent/handlers.go \
  'release, ok := s.holdOpLock()
	if !ok {
		writeError(w, s.busyError())
		return
	}
	_, running, err := s.containerRunning(r.Context())
	if err == nil && !running {' \
  'release, ok := func() { }, !s.busy()
	if !ok {
		writeError(w, s.busyError())
		return
	}
	_, running, err := s.containerRunning(r.Context())
	if err == nil && !running {' \
  ./internal/agent '^TestConcurrentStartAndStopLeaveDesiredMatchingContainer$' 5

control "release manifest signature" internal/update/manifest.go \
  'if !verified {' \
  'if false && !verified {' \
  ./internal/update '^TestOnlyManifestsSignedByATrustedKeyAreAccepted$'
control "update download size" internal/update/fetch.go \
  'if n != a.Size {' \
  'if false && n != a.Size {' \
  ./internal/update '^TestDownloadsAreCheckedAgainstTheSignedManifest$'
control "update download stops at the signed size" internal/update/fetch.go \
  'io.LimitReader(body, a.Size+1)' \
  'body' \
  ./internal/update '^TestDownloadsAreCheckedAgainstTheSignedManifest$'
control "update download SHA-256" internal/update/fetch.go \
  'if got := hex.EncodeToString(h.Sum(nil)); got != a.SHA256 {' \
  'if got := hex.EncodeToString(h.Sum(nil)); false && got != a.SHA256 {' \
  ./internal/update '^TestDownloadsAreCheckedAgainstTheSignedManifest$'
control "update binary SHA-256" internal/update/fetch.go \
  'if got := hex.EncodeToString(h.Sum(nil)); got != want {' \
  'if got := hex.EncodeToString(h.Sum(nil)); false && got != want {' \
  ./internal/update '^TestExtractBinaryReadsOnlyTheSignedBinary$'
control "release location must be HTTPS unless it is this machine" internal/update/fetch.go \
  'return nil, fmt.Errorf("release location %s is not HTTPS (plain HTTP is only allowed for this machine)", raw)' \
  'return u, nil' \
  ./internal/update '^TestReleaseLocationsMustBeHTTPSUnlessLocal$'
control "agent installs only newer releases" internal/agent/update.go \
  'if c, err := update.CompareVersions(m.Version, current); err != nil || c <= 0 {' \
  'if c, err := update.CompareVersions(m.Version, current); false && (err != nil || c <= 0) {' \
  ./internal/agent '^TestUpdateRefusesDownloadsThatDoNotMatchAndStaleChoices$'
control "updater checks the staged release again" internal/install/selfupdate.go \
  'if err := verifyStaged(staged, bin, req.Version, keys); err != nil {' \
  'if err := verifyStaged(staged, bin, req.Version, keys); false && err != nil {' \
  ./internal/install '^TestUpdaterRefusesAnythingItCannotVerify$'
control "updater installs only newer releases" internal/install/selfupdate.go \
  'if c, err := update.CompareVersions(req.Version, current); err != nil || c <= 0 {' \
  'if c, err := update.CompareVersions(req.Version, current); false && (err != nil || c <= 0) {' \
  ./internal/install '^TestUpdaterRefusesAnythingItCannotVerify$'
control "updater refuses stale requests" internal/install/selfupdate.go \
  'age > update.StaleAfter || age < -update.StaleAfter {' \
  'false && (age > update.StaleAfter || age < -update.StaleAfter) {' \
  ./internal/install '^TestUpdaterRefusesAnythingItCannotVerify$'
control "updater refuses requests for another installed version" internal/install/selfupdate.go \
  'if req.From != current {' \
  'if false && req.From != current {' \
  ./internal/install '^TestUpdaterRefusesAnythingItCannotVerify$'
control "upgrades write only Playkeeper's systemd units" internal/install/upgrade.go \
  'if !allowed[name] {' \
  'if false && !allowed[name] {' \
  ./internal/install '^TestUpdaterRefusesAnythingItCannotVerify$'
control "upgrade waits for the new version to be healthy" internal/install/upgrade.go \
  'func() error { return u.waitHealthy(ctx, u.o.NewVersion) }' \
  'func() error { return nil }' \
  ./internal/install '^(TestUnhealthyUpgradePutsTheOldVersionBack|TestUpdaterRollsBackAnUnhealthyReleaseAndFinishesAnInterruptedOne)$'
control "upgrade checks the new version again after it answers" internal/install/upgrade.go \
  'if err := u.sys.WaitVersion(hctx2, u.cfg.SocketPath, cert, u.cfg.PanelPort, version); err != nil {' \
  'if err := u.sys.WaitVersion(hctx2, u.cfg.SocketPath, cert, u.cfg.PanelPort, version); false && err != nil {' \
  ./internal/install '^TestUpgradeRollsBackAVersionThatStopsRightAfterAnswering$'
control "rollback puts the databases back" internal/install/upgrade.go \
  'errs = append(errs, u.restoreDatabases())' \
  'errs = append(errs, nil)' \
  ./internal/install '^TestUnhealthyUpgradePutsTheOldVersionBack$'
control "the installer never goes back to an older version" internal/install/inplace.go \
  'case cmp < 0:' \
  'case false:' \
  ./internal/install '^TestInstallerNeverGoesBackAndLeavesTheCurrentVersionAlone$'
control "an update handed to the updater keeps running until it reports" internal/agent/lifecycle.go \
  'case h.continues:' \
  'case false:' \
  ./internal/agent '^TestUpdateIsVerifiedStagedAndHandedToTheUpdater$'
control "an update the updater is installing is not marked interrupted" internal/agent/state.go \
  'keep = a.upd.opID' \
  'keep = ""' \
  ./internal/agent '^TestAnUpdateKeepsRunningAcrossAgentRestartsUntilTheUpdaterReports$'
control "an updater that never reports fails its update" internal/agent/update.go \
  'if waited > updaterRunTimeout {' \
  'if false && waited > updaterRunTimeout {' \
  ./internal/agent '^TestFailedUpdatesAreReportedAndDoNotBlockTheDashboard$'
control "a restart does not wait again for an update the agent gave up on" internal/agent/update.go \
  'if req.OpID != "" && req.OpID == abandoned {' \
  'if false && req.OpID == abandoned {' \
  ./internal/agent '^TestFailedUpdatesAreReportedAndDoNotBlockTheDashboard$'
control "a restart does not start the handoff timeouts over" internal/agent/update.go \
  'since = st.ModTime()' \
  'since = a.now()' \
  ./internal/agent '^TestFailedUpdatesAreReportedAndDoNotBlockTheDashboard$'
control "an updater that waited does not install over what the installer installed" internal/install/selfupdate.go \
  'if installed != current {' \
  'if false && installed != current {' \
  ./internal/install '^TestAnUpdaterThatWaitedForTheInstallerDoesNotInstallOverIt$'
control "the installer refuses while a dashboard update is pending" internal/install/inplace.go \
  'if msg := pendingUpdate(sys, cfg); msg != "" {' \
  'if msg := pendingUpdate(sys, cfg); false && msg != "" {' \
  ./internal/install '^TestTheInstallerAndTheUpdaterNeverUpgradeAtTheSameTime$'
control "the installer takes the upgrade lock" internal/install/inplace.go \
  'unlock, err := lockUpgrades(sys, cfg, false)' \
  'unlock, err := func() {}, error(nil)' \
  ./internal/install '^TestTheInstallerAndTheUpdaterNeverUpgradeAtTheSameTime$'
control "the updater waits for the upgrade lock" internal/install/selfupdate.go \
  'unlock, err := lockUpgrades(sys, cfg, true)' \
  'unlock, err := func() {}, error(nil)' \
  ./internal/install '^TestTheInstallerAndTheUpdaterNeverUpgradeAtTheSameTime$'
control "a kept interrupted update is recorded in the install manifest" internal/install/upgrade.go \
  'u.recordVersion(want)' \
  '_ = want' \
  ./internal/install '^TestUpdaterRollsBackAnUnhealthyReleaseAndFinishesAnInterruptedOne$'
control "an install manifest problem does not fail a finished update" internal/install/upgrade.go \
  'u.recordVersion(o.NewVersion)
		return nil' \
  'return u.updateManifest(o.NewVersion)' \
  ./internal/install '^TestAnUpdateThatCannotRecordItsVersionIsStillAnUpdate$'
control "uninstall disables the updater even if the manifest misses it" internal/install/uninstall.go \
  'if contains(m.Units, u) || updater[u] {' \
  'if contains(m.Units, u) {' \
  ./internal/install '^TestUninstallRemovesTheUpdaterEvenIfTheManifestMissesIt$'
control "RCON finds a closed connection before writing" internal/minecraft/rcon.go \
  'if err := r.stale(); err != nil {' \
  'if err := r.stale(); false && err != nil {' \
  ./internal/minecraft '^TestRCONCommandOnClosedConnectionIsNotSent$'
control "RCON stops when the caller's context ends" internal/minecraft/rcon.go \
  '		_ = r.conn.SetDeadline(time.Now())
' \
  '' \
  ./internal/minecraft '^(TestRCONCommandContextHonoursTheDeadline|TestRCONCommandContextStopsWhenCancelled)$'
control "a console command that went out is never sent again" internal/agent/collector.go \
  'if attempt > 0 || !errors.Is(err, minecraft.ErrNotSent) || ctx.Err() != nil {' \
  'if attempt > 0 || ctx.Err() != nil {' \
  ./internal/agent '^TestConsoleNeverSendsACommandTwice$'
control "Paper versions are sorted newest first" internal/minecraft/fill.go \
  'return CompareMinecraft(b.Version.ID, a.Version.ID)' \
  'return 0' \
  ./internal/minecraft '^TestCatalogDoesNotDependOnTheOrderPaperMCListsVersionsIn$'
control "experimental versions need consent" internal/agent/handlers.go \
  'if entry.Experimental && !req.AcceptExperimental {' \
  'if false && entry.Experimental && !req.AcceptExperimental {' \
  ./internal/agent '^TestCatalogIsLiveFromPaperMCAndExperimentalNeedsConsent$'
control "version changes to experimental versions need consent" internal/agent/versions.go \
  'if e.Experimental && !req.AcceptExperimental {' \
  'if false && e.Experimental && !req.AcceptExperimental {' \
  ./internal/agent '^TestVersionChangesNeverGoBack$'
control "Minecraft never goes back to an older version" internal/agent/versions.go \
  '	case c < 0:' \
  '	case false:' \
  ./internal/agent '^TestVersionChangesNeverGoBack$'
control "a version that does not start gets the world back" internal/agent/versions.go \
  'if err := s.putBackupBack(b); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/agent '^TestVersionThatDoesNotStartPutsTheWorldBack$'
control "Paper builds without a checksum are not offered" internal/minecraft/fill.go \
  'if !ok || !reSHA256.MatchString(d.Checksums.SHA256) {' \
  'if !ok || false && !reSHA256.MatchString(d.Checksums.SHA256) {' \
  ./internal/minecraft '^TestCatalogSkipsBuildsWithoutAChecksumAndVersionsTheImageCannotRun$'
control "Paper jar checksum" internal/agent/lifecycle.go \
  'if sum != want {' \
  'if false && sum != want {' \
  ./internal/agent '^(TestJarChecksumMismatchIsNeverRun|TestServersFrom010KeepTheirPinnedChecksum)$'
control "game files: a link on the way to a file is refused" internal/gamefiles/gamefiles.go \
  'err = folderError(p, fi)' \
  'err = nil' \
  ./internal/gamefiles '^TestLinksAreRefusedAtEveryStep$'
control "game files: a link or special file is refused before it is opened" internal/gamefiles/gamefiles.go \
  'err = fileError(name, fi)' \
  'err = nil' \
  ./internal/gamefiles '^(TestLinksAreRefusedAtEveryStep|TestSpecialFilesAreRefusedWithoutWaiting)$'
control "game files: a link or special file is not written over" internal/gamefiles/gamefiles.go \
  'if err := fileError(name, fi); err != nil {' \
  'if err := fileError(name, fi); false && err != nil {' \
  ./internal/gamefiles '^TestLinksAreRefusedAtEveryStep$'
control "game files: opening a named pipe does not wait" internal/gamefiles/gamefiles.go \
  'os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)' \
  'os.O_RDONLY|syscall.O_NOFOLLOW, 0)' \
  ./internal/gamefiles '^TestFilesSwappedAfterTheCheckAreRefused$'
control "game files: the opened file is the one checked" internal/gamefiles/gamefiles.go \
  'if err == nil && (!st.Mode().IsRegular() || !os.SameFile(fi, st)) {' \
  'if false && (!st.Mode().IsRegular() || !os.SameFile(fi, st)) {' \
  ./internal/gamefiles '^TestFilesSwappedAfterTheCheckAreRefused$'
control "game files: reads are capped" internal/gamefiles/gamefiles.go \
  'if int64(len(b)) > limit {' \
  'if false {' \
  ./internal/gamefiles '^TestOversizedFilesAreRefused$'
control "game files: a file too large to hash is not read" internal/gamefiles/gamefiles.go \
  'if size > limit {' \
  'if false {' \
  ./internal/gamefiles '^TestHugeSparseFilesAreRefusedQuickly$'
control "game files: hashing reads only the size it checked" internal/gamefiles/gamefiles.go \
  'io.LimitReader(f, size)' \
  'f' \
  ./internal/gamefiles '^TestHashingReadsOnlyTheSizeItSawAndStopsWithItsContext$'
control "game files: hashing stops with its context" internal/gamefiles/gamefiles.go \
  'if err := c.ctx.Err(); err != nil {' \
  'if err := c.ctx.Err(); false && err != nil {' \
  ./internal/gamefiles '^TestHashingReadsOnlyTheSizeItSawAndStopsWithItsContext$'
control "game files: the temporary file is always a new one" internal/gamefiles/gamefiles.go \
  'os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW' \
  'os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW' \
  ./internal/gamefiles '^TestTheTemporaryFileIsAlwaysANewOne$'
control "game files: a new folder is given to the game through its own handle" internal/gamefiles/gamefiles.go \
  'if err == nil && (!st.IsDir() || !os.SameFile(fi, st)) {' \
  'if false && (!st.IsDir() || !os.SameFile(fi, st)) {' \
  ./internal/gamefiles '^TestNewFoldersAreGivenToTheGameThroughTheirOwnHandle$'
control "game files: folder listings are capped" internal/gamefiles/gamefiles.go \
  'if len(es) > limit {' \
  'if false {' \
  ./internal/gamefiles '^TestReadDirListsRealFoldersOnly$'
control "game files: paths stay inside the server's files" internal/gamefiles/gamefiles.go \
  'if !fs.ValidPath(name) || name == "." {' \
  'if false {' \
  ./internal/gamefiles '^TestPathsMustStayInsideTheDataDirectory$'
control "a planted link stops the start before the bStats write" internal/gamefiles/gamefiles.go \
  'err = folderError(p, fi)' \
  'err = nil' \
  ./internal/agent '^TestPlantedLinksCannotRedirectTheBStatsWrite$'
control "the agent reads no game file through a link" internal/gamefiles/gamefiles.go \
  'err = fileError(name, fi)' \
  'err = nil' \
  ./internal/agent '^TestGameFilesAreReadWithoutFollowingLinks$'
control "a start that fails before the server's files keeps the refusal" internal/agent/gamefiles.go \
  'if !refused && !pastFiles {' \
  'if false {' \
  ./internal/agent '^TestARefusalLastsUntilAStartGetsPastTheFiles$'
control "the status keeps the refusal while Docker isn't answering" internal/agent/handlers.go \
  'st.LastErrorHint = "Check the Docker service: sudo systemctl status docker"
		st.Refusal = refusal' \
  'st.LastErrorHint = "Check the Docker service: sudo systemctl status docker"' \
  ./internal/agent '^TestARefusalLastsUntilAStartGetsPastTheFiles$'
control "a refused restart replaces the crash's explanation, so the status explains it once" internal/agent/gamefiles.go \
  'if refused {
		s.crash = nil
	}' \
  '' \
  ./internal/agent '^TestARefusedRestartReplacesTheCrash$'
control "the jar is hashed only up to a size no Paper jar reaches" internal/agent/lifecycle.go \
  'const maxJarBytes = 256 << 20' \
  'const maxJarBytes = 1 << 62' \
  ./internal/agent '^TestAHugeSparseJarDoesNotHoldUpTheStart$'
control "a backup reads the level name without following a link or waiting on a pipe" internal/backup/archive.go \
  'b, err := readProperties(dataDir)' \
  'b, err := os.ReadFile(filepath.Join(dataDir, "server.properties"))' \
  ./internal/backup '^TestLevelNameDoesNotFollowALinkOrWaitOnAPipe$'
control "a restored world is given to the game without following links" internal/agent/backups.go \
  'if d.Type()&fs.ModeSymlink != 0 {' \
  'if false {' \
  ./internal/agent '^TestRestoredWorldsAreGivenToTheGameWithoutFollowingLinks$'
control "add-on files: opening a named pipe does not wait" internal/addons/files.go \
  'os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)' \
  'os.O_RDONLY|syscall.O_NOFOLLOW, 0)' \
  ./internal/addons '^TestPipesSwappedInAreRefusedWithoutWaiting$'
control "add-on files: the opened file is the regular file checked" internal/addons/files.go \
  'if err == nil && (!st.Mode().IsRegular() || !os.SameFile(fi, st)) {' \
  'if false && (!st.Mode().IsRegular() || !os.SameFile(fi, st)) {' \
  ./internal/addons '^TestPipesSwappedInAreRefusedWithoutWaiting$'
control "add-on folder: a named pipe swapped in fails at once" internal/addons/files.go \
  'data.OpenRoot(t.Folder + "/.")' \
  'data.OpenRoot(t.Folder)' \
  ./internal/addons '^TestPipesSwappedInAreRefusedWithoutWaiting$'
control "add-on files: only a file of the recorded size is hashed" internal/addons/files.go \
  '|| rec.Size > 0 && size != rec.Size ||' \
  '||' \
  ./internal/addons '^TestGrownJarsAreNotHashed$'
control "add-on files: a record without a size is hashed only up to the size limit" internal/addons/files.go \
  '|| rec.Size <= 0 && size > max {' \
  '{' \
  ./internal/addons '^TestGrownJarsAreNotHashed$'
control "add-on files: hashing reads only the size it saw" internal/addons/files.go \
  'ctxReader{ctx, io.NewSectionReader(f, 0, size)}' \
  'ctxReader{ctx, f}' \
  ./internal/addons '^TestHashingReadsOnlyTheSizeItSawAndStopsWithItsContext$'
control "add-on files: hashing stops with its context" internal/addons/files.go \
  'if err := c.ctx.Err(); err != nil {' \
  'if err := c.ctx.Err(); false && err != nil {' \
  ./internal/addons '^TestHashingReadsOnlyTheSizeItSawAndStopsWithItsContext$'
control "add-on scan: stops with its context" internal/addons/scan.go \
  'l.readLocal(ctx, root, lf, identify, verify)
			if err := ctx.Err(); err != nil {' \
  'l.readLocal(ctx, root, lf, identify, verify)
			if err := ctx.Err(); false && err != nil {' \
  ./internal/addons '^TestAScanStopsWithItsContext$'
control "pre-generation: a named pipe for the plugins folder is refused before it is opened" internal/gamefiles/gamefiles.go \
  'err = folderError(p, fi)' \
  'err = nil' \
  ./internal/pregen '^TestDetectDoesNotWaitOnAPipe$'
control "data packs: a named pipe for the datapacks folder is refused before it is opened" internal/gamefiles/gamefiles.go \
  'err = folderError(p, fi)' \
  'err = nil' \
  ./internal/packs '^TestListDoesNotWaitOnAPipe$'
control "resource packs: an offer that can't be built doesn't clear the pack or read as applied" internal/agent/packs.go \
  'set, err := offerOf(o).Settings()' \
  'set, err := offerOf(o).Settings(); if err != nil { set, err = settings, nil }' \
  ./internal/agent '^TestResourcePackOfferThatCantBeBuilt$'
control "resource packs: an offer that can't be built says what's wrong on the Packs page" internal/agent/packs.go \
  'out.Problem = offerProblem(err)' \
  'out.Problem = ""' \
  ./internal/agent '^TestResourcePackOfferThatCantBeBuilt$'
control "resource packs: a recreated container keeps the pack settings it had" internal/agent/lifecycle.go \
  'pack = keptPackEnv(current)' \
  'pack = keptPackEnv(nil)' \
  ./internal/agent '^TestResourcePackOfferThatCantBeBuilt$'
control "resource packs: without a container the pack settings are left alone, not cleared" internal/agent/packs.go \
  'if v, ok := kept[st.Env]; ok {' \
  'if v := kept[st.Env]; true {' \
  ./internal/agent '^TestResourcePackOfferThatCantBeBuilt$'
control "resource packs: a server without pack settings keeps the pack its server.properties names served" internal/agent/packs.go \
  'if len(settings) == 0 {' \
  'if false && len(settings) == 0 {' \
  ./internal/agent '^TestResourcePackOfferThatCantBeBuilt$'
control "add-on jars: the table of contents is checked before archive/zip reads it" internal/addons/jar.go \
  'n, err := zipdir.Check(r, size, zipdir.Metadata)
	if err != nil {' \
  'n, err := zipdir.Check(r, size, zipdir.Metadata)
	if false && err != nil {' \
  ./internal/addons '^TestJarsWithHugeTablesOfContentsAreNotRead$'
control "pre-generation: a jar's table of contents is checked before archive/zip reads it" internal/pregen/detect.go \
  'n, err := zipdir.Check(f, st.Size(), zipdir.Metadata)
	if err != nil {' \
  'n, err := zipdir.Check(f, st.Size(), zipdir.Metadata)
	if false && err != nil {' \
  ./internal/pregen '^TestDetectDoesNotReadHugeTablesOfContents$'
control "jar metadata: a table of contents is bounded to what metadata needs" internal/zipdir/zipdir.go \
  'var Metadata = Limits{Bytes: 16 << 20, Entries: 100_000}' \
  'var Metadata = Limits{Bytes: 1 << 40, Entries: 1 << 40}' \
  ./internal/addons '^TestJarsWithHugeTablesOfContentsAreNotRead$'
control "zip tables of contents: more entries than the limit are refused" internal/zipdir/zipdir.go \
  'case records > uint64(max(lim.Entries, 0)):' \
  'case false:' \
  ./internal/zipdir '^TestCheck$'
control "zip tables of contents: one larger than the limit is refused" internal/zipdir/zipdir.go \
  'case dirSize > uint64(max(lim.Bytes, 0)):' \
  'case false:' \
  ./internal/zipdir '^TestCheck$'
control "agent unit: its memory is capped" internal/install/units.go \
  'MemoryMax=384M
' \
  '' \
  ./internal/install '^TestAgentUnitCapsItsMemory$'
control "public routes: limited per address" internal/panel/public.go \
  'if ok, wait := perMinute.allow(key); !ok {' \
  'if ok, wait := perMinute.allow(key); false && !ok {' \
  ./internal/panel '^TestPublicRoutesAreLimitedPerAddress$'
control "public routes: an address is the connection's, not a forwarded header" internal/panel/public.go \
  'key := rt.prefix + " " + addressKey(r.RemoteAddr)' \
  'key := rt.prefix + " " + addressKey(r.RemoteAddr) + r.Header.Get("X-Forwarded-For")' \
  ./internal/panel '^TestPublicRoutesAreLimitedPerAddress$'
control "public routes: an IPv6 address counts as its /64" internal/panel/public.go \
  'p, err := a.Prefix(64)' \
  'p, err := a.Prefix(128)' \
  ./internal/panel '^TestAddressKeyIgnoresHeadersAndGroupsIPv6$'
control "public routes: the requests an address has open are capped" internal/panel/public.go \
  'if !g.enter(key, rt.limits.open) {' \
  'if false && !g.enter(key, rt.limits.open) {' \
  ./internal/panel '^TestPublicDownloadsAreCapped$'
control "public downloads: capped for every address together" internal/panel/public.go \
  'downloads: make(chan struct{}, publicDownloads)' \
  'downloads: make(chan struct{}, 10*publicDownloads)' \
  ./internal/panel '^TestPublicDownloadsAreCapped$'
control "public downloads: a client that stops reading is dropped" internal/panel/public.go \
  '_ = w.rc.SetWriteDeadline(d)' \
  '_ = d' \
  ./internal/panel '^TestPublicDownloadsDropClientsThatStopReading$'
control "public routes: unknown, switched off and stopped answer one 404" internal/panel/public.go \
  'return status == http.StatusForbidden || status == http.StatusNotFound || status == http.StatusGone || status >= 500' \
  'return status == http.StatusNotFound' \
  ./internal/panel '^TestPublicRoutesAnswerOneNotFound$'
control "public routes: a 404 comes no sooner than one the agent was asked for" internal/panel/public.go \
  'if wait := publicNotFoundAfter - time.Since(start); wait > 0 {' \
  'if wait := publicNotFoundAfter - time.Since(start); false && wait > 0 {' \
  ./internal/panel '^TestPublicRoutesAnswerOneNotFound$'
control "public routes: a 404 frees its download slot while it waits" internal/panel/public.go \
  'rt.handler.ServeHTTP(pw, r)
		release()' \
  'rt.handler.ServeHTTP(pw, r)' \
  ./internal/panel '^TestPublicRoutesAnswerOneNotFound$'
control "public routes: a handler can't make an answer cacheable" internal/panel/public.go \
  'w.Header().Set("Cache-Control", cache)' \
  '_ = cache' \
  ./internal/panel '^TestPublicRoutesCacheOnlyWhatTheyMay$'
control "public routes: only successful answers may be cached" internal/panel/public.go \
  'if w.cache != "" && (status < 300 || status == http.StatusNotModified) {' \
  'if w.cache != "" {' \
  ./internal/panel '^TestPublicRoutesCacheOnlyWhatTheyMay$'
control "add-on installs: a confirmed plan is required" internal/agent/addons.go \
  'if err := confirmedPlan(req.Fingerprint); err != nil {' \
  'if err := confirmedPlan(req.Fingerprint); false && err != nil {' \
  ./internal/agent '^TestAddonRoutesRejectBadInput$'
control "add-on updates: a confirmed plan is required" internal/agent/addons.go \
  'keys, err := updateKeys(req.Addons)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := confirmedPlan(req.Fingerprint); err != nil {' \
  'keys, err := updateKeys(req.Addons)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := confirmedPlan(req.Fingerprint); false && err != nil {' \
  ./internal/agent '^TestAddonRoutesRejectBadInput$'
control "add-on installs: only the confirmed plan is carried out" internal/agent/addons.go \
  'Project: key.ProjectID, Fingerprint: req.Fingerprint, OnProgress: progress' \
  'Project: key.ProjectID, OnProgress: progress' \
  ./internal/agent '^TestAddonsInstallUpdateRemove$'
control "add-on updates: only the confirmed plan is carried out" internal/agent/addons.go \
  'Changed: req.Changed, Fingerprint: req.Fingerprint, OnProgress: progress' \
  'Changed: req.Changed, OnProgress: progress' \
  ./internal/agent '^TestAddonsInstallUpdateRemove$'
control "add-on plans: the order Hangar lists dependencies in does not change the plan" internal/addons/resolve.go \
  'c.deps = append(c.deps, dd)
	}
	sortDeps(c.deps)' \
  'c.deps = append(c.deps, dd)
	}' \
  ./internal/addons '^TestInstallHangarWhateverOrderItListsDependenciesIn$'
control "add-on plans: the fingerprint ignores the order of the steps" internal/addons/plan.go \
  'slices.SortStableFunc(steps, ' \
  'slices.SortStableFunc(steps[:0], ' \
  ./internal/addons '^TestFingerprintIgnoresOrderButNotVersions$'
control "add-on plans: the fingerprint ignores the order of what the server already has" internal/addons/plan.go \
  'slices.SortStableFunc(satisfied, ' \
  'slices.SortStableFunc(satisfied[:0], ' \
  ./internal/addons '^TestFingerprintIgnoresOrderButNotVersions$'
control "add-on versions: a Hangar release behind pages of snapshots is asked for by channel" internal/addons/resolve.go \
  'releases.Offset, releases.Channel = 0, "Release"' \
  'releases.Offset, releases.Channel = 0, ""' \
  ./internal/addons '^TestHangarReleaseBehindPagesOfSnapshots$/^in_the_Release_channel$'
control "add-on versions: a Hangar release channel named otherwise is found on a later page" internal/addons/resolve.go \
  'for page := 1; page < hangarPages' \
  'for page := hangarPages; page < hangarPages' \
  ./internal/addons '^TestHangarReleaseBehindPagesOfSnapshots$/^in_a_channel_named_Stable$'
control "add-on versions: reading Hangar's pages stops at the first release that fits" internal/addons/resolve.go \
  'page < hangarPages && more && !found' \
  'page < hangarPages && more' \
  ./internal/addons '^TestHangarReleaseBehindPagesOfSnapshots$/^in_a_channel_named_Stable$'
control "add-on versions: reading Hangar's pages stops after hangarPages" internal/addons/resolve.go \
  'page < hangarPages && more && !found' \
  'more && !found' \
  ./internal/addons '^TestHangarOnlySnapshotsStayPreRelease$/^more_than_the_pages_read$'
control "port sharing: a connection nobody accepts is closed" internal/portshare/portshare.go \
  't := time.NewTimer(l.s.handoff)' \
  't := time.NewTimer(time.Hour)' \
  ./internal/portshare '^TestHandoffTimeout$'

# Follow-ups after 0.3.0.
control "an interrupted restore gets the previous world back at start" internal/agent/backups.go \
  'if dirExists(aside) {' \
  'if false && dirExists(aside) {' \
  ./internal/agent '^TestInterruptedRestoreIsSettledAtStart$'
control "an interrupted restore gets the previous settings back at start" internal/agent/backups.go \
  'return s.saveServerConfig(*j.Previous)' \
  'return nil' \
  ./internal/agent '^TestInterruptedRestoreIsSettledAtStart$'
control "a restore stage is kept while its swap is not settled" internal/agent/backups.go \
  'if err := a.settleSwap(dir); err != nil {' \
  'if err := a.settleSwap(dir); false && err != nil {' \
  ./internal/agent '^TestTripleFailedRestoreKeepsItsStageUntilThePreviousWorldIsBack$'
control "no start recreates a world directory a restore moved aside" internal/agent/lifecycle.go \
  'if prev := s.newestPreviousWorld(); prev != "" {' \
  'if prev := s.newestPreviousWorld(); false && prev != "" {' \
  ./internal/agent '^TestTripleFailedRestoreKeepsItsStageUntilThePreviousWorldIsBack$'
control "a world copy is discarded only by its exact name" internal/agent/backups.go \
  'if !reWorldCopy.MatchString(name) {' \
  'if false && !reWorldCopy.MatchString(name) {' \
  ./internal/agent '^TestWorldCopiesAreListedAndDiscarded$'
control "no world copy is discarded while the live world folder is missing" internal/agent/backups.go \
  'if !dirExists(s.dataDir()) {' \
  'if false && !dirExists(s.dataDir()) {' \
  ./internal/agent '^TestWorldCopiesAreListedAndDiscarded$'
control "a world a restore would refuse is refused before the server stops" internal/agent/backups.go \
  'err := backup.Check(s.dataDir(), archiveLimits())
	if !errors.As(err, &refused) {' \
  'err := backup.Check(s.dataDir(), archiveLimits())
	if !errors.As(err, &refused) || true {' \
  ./internal/agent '^(TestBackupRefusesAWorldARestoreWouldRefuse|TestBackupRefusesAWholeWorldOverALimitBeforeStopping|TestRestoreAndUpdateRefuseAWorldTheirBackupWouldRefuseBeforeStopping)$'
control "an update refuses such a world before the server stops" internal/agent/versions.go \
  'if err := s.archiveRefusal("Nothing was changed."); err != nil {' \
  'if err := s.archiveRefusal("Nothing was changed."); false && err != nil {' \
  ./internal/agent '^TestRestoreAndUpdateRefuseAWorldTheirBackupWouldRefuseBeforeStopping$'
control "the pre-stop check applies the archive limits" internal/backup/archive.go \
  'if err := tally.add(rel, size); err != nil {' \
  'if err := tally.add(rel, size); false && err != nil {' \
  ./internal/backup '^TestCheckRefusesWhatCreateRefuses$'
control "a failed undo deletes neither copy of the world" internal/agent/backups.go \
  'if perr := putBack(failedAt, cause); perr != nil {' \
  'if perr := putBack(failedAt, cause); false && perr != nil {' \
  ./internal/agent '^TestRestoreKeepsBothCopiesWhenPuttingThePreviousWorldBackFails$'
control "a failed undo moves the restored world out of the stage" internal/agent/backups.go \
  'if restoredAt == st.data && renameDir(st.data, failedAt) == nil {' \
  'if false && restoredAt == st.data && renameDir(st.data, failedAt) == nil {' \
  ./internal/agent '^TestRestoreKeepsBothCopiesWhenPuttingThePreviousWorldBackFails$'
control "a restore the agent stops in is not undone" internal/agent/backups.go \
  'if err != nil && s.stopping() {' \
  'if false && err != nil && s.stopping() {' \
  ./internal/agent '^TestRestoreSurvivesTheAgentStopping$/^stops_while'
control "an undo the agent stops in is finished by the next start" internal/agent/backups.go \
  'if s.stopping() {' \
  'if false && s.stopping() {' \
  ./internal/agent '^TestInterruptedRestoreIsSettledAtStart$/^stops_while_the_previous_world_is_put_back$'
control "the stage of a restore being finished is not pruned at start" internal/agent/backups.go \
  'if a.resuming(dir) {' \
  'if false && a.resuming(dir) {' \
  ./internal/agent '^TestRestoreSurvivesTheAgentStopping$/^dies_after_the_swap$'
control "the previous world's copy goes only once the restore is recorded as kept" internal/agent/backups.go \
  'j.State = swapKept' \
  'j.State = swapChecking' \
  ./internal/agent '^TestRestoreSurvivesTheAgentStopping$/^dies_once_the_restore_is_kept$'
control "a restored world still starting after a restart is kept only once online" internal/agent/backups.go \
  'err = s.waitOnline(ctx, h)' \
  'err = nil' \
  ./internal/agent '^TestRestoredWorldThatDoesNotStartIsSwappedBackOut$/^after_the_agent_stops_while_the_restored_world_boots$'
control "a restore finished after a restart saves the restored settings" internal/agent/backups.go \
  'if err := s.saveServerConfig(j.Restored); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/agent '^TestRestoreSurvivesTheAgentStopping$/^dies_after_the_swap$'
control "a restored world that does not start is swapped back out" internal/agent/backups.go \
  'return s.revertRestore(h, stageDir, j)' \
  'return err' \
  ./internal/agent '^TestRestoredWorldThatDoesNotStartIsSwappedBackOut$/^during_the_restore$'
control "a 0.3.0 restore that saved its settings is finished" internal/agent/recovery.go \
  'case hasLive && hasAside && !hasStaged && restored:' \
  'case hasLive && hasAside && !hasStaged && false:' \
  ./internal/agent '^TestRestoreInterruptedUnder030IsRecovered$/^dies_with_the_restored_settings_saved$'
control "a 0.3.0 restore whose world was online is kept" internal/agent/recovery.go \
  'case p.op.Phase == string(api.PhaseOnline) && hasLive && !hasStaged:' \
  'case false:' \
  ./internal/agent '^TestRestoreInterruptedUnder030IsRecovered$/^dies_while_deleting_the_previous_world.s_copy$'
control "a 0.3.0 restore that moved the live world aside is undone" internal/agent/recovery.go \
  'case !hasLive && hasAside:' \
  'case false:' \
  ./internal/agent '^TestRestoreInterruptedUnder030IsRecovered$/^dies_with_the_live_world_moved_aside$'
control "a 0.3.0 restore counts as having saved its settings only if they are the backup's" internal/agent/recovery.go \
  'return sc == want, nil' \
  'return sc == want || true, nil' \
  ./internal/agent '^TestRestoreInterruptedUnder030IsKeptOnlyWithTheBackupsSettings$/^dies_before_the_save_with_only_the_build_changed$'
control "a 0.3.0 restore whose settings only look like the backup's is undone" internal/agent/recovery.go \
  'restored, err := s.restoredSettings030(ctx, *cur, m)' \
  'restored, err := restoredFrom(*cur, m), error(nil)' \
  ./internal/agent '^TestRestoreInterruptedUnder030IsKeptOnlyWithTheBackupsSettings$/^dies_before_the_save_with_memory_and_build_changed$'
control "a 0.3.0 restore the agent stops in while checking its settings is left for the next start" internal/agent/recovery.go \
  'if err != nil && s.stopping() {' \
  'if false && err != nil && s.stopping() {' \
  ./internal/agent '^TestStoppingWhileTakingOverA030RestoreLeavesItForTheNextStart$/^the_server_has_the_backup.s_settings$'
control "an undone 0.3.0 restore tells the previous MOTD from the backup's" internal/agent/recovery.go \
  'sc.MOTD == validMOTDOr(m.Settings["motd"])' \
  'true' \
  ./internal/agent '^TestRestoreInterruptedUnder030IsRecovered$/restored_world_does_not_start$'
control "an undone 0.3.0 restore gets the settings from its rollback archive back" internal/agent/recovery.go \
  'if rollbackID == "" || !restoredFrom(cur, restored) {' \
  'if true {' \
  ./internal/agent '^TestRestoreInterruptedUnder030IsRecovered$/restored_world_does_not_start$'
control "only unfinished backups are deleted at start" internal/agent/recovery.go \
  'if !e.Type().IsRegular() || !reArchiveLeftover.MatchString(e.Name()) {' \
  'if !e.Type().IsRegular() {' \
  ./internal/agent '^TestRestoreInterruptedUnder030IsRecovered$/^dies_while_saving_the_rollback_archive$'
control "an unfinished rollback archive is deleted at start" internal/agent/agent.go \
  'a.pruneArchiveLeftovers()' \
  '' \
  ./internal/agent '^TestRestoreInterruptedUnder030IsRecovered$/^dies_while_saving_the_rollback_archive$'
control "a 0.3.0 restore the agent stops in while taking it over is left for the next start" internal/agent/recovery.go \
  'if s.stopping() {' \
  'if false && s.stopping() {' \
  ./internal/agent '^TestStoppingWhileTakingOverA030RestoreLeavesItForTheNextStart$'
control "only a restored world started during a restore 0.3.0 undid is stopped" internal/agent/recovery.go \
  'if !running || !ok || started.Before(op.StartedAt) || started.After(*op.FinishedAt) {' \
  'if !running || !ok || started.IsZero() {' \
  ./internal/agent '^TestRestoreUndoneBy030StoppingIsTidiedUp$/^the_server_was_restarted_since$'
control "only a restore 0.3.0 undid because the agent stopped is corrected" internal/agent/recovery.go \
  'strings.Contains(why, "context canceled")' \
  'strings.Contains(why, "")' \
  ./internal/agent '^TestUndoneByStop$'
control "a restore 0.3.0 undid is dealt with once" internal/agent/recovery.go \
  ' || op.Detail["recoveredAfterRestart"] != nil' \
  '' \
  ./internal/agent '^TestUndoneByStop$'
control "the pre-stop check sizes server.properties without following a link or waiting on a pipe" internal/backup/archive.go \
  'if rel == "server.properties" {
		b, err := readProperties(dataDir)' \
  'if rel == "server.properties" {
		b, err := os.ReadFile(filepath.Join(dataDir, rel))' \
  ./internal/backup '^TestArchivedSizeDoesNotFollowALinkOrWaitOnAPipe$'
shcontrol() { # NAME FILE FROM TO TEST-SCRIPT
  local name=$1 file=$2 test=$5
  FROM=$3 TO=$4 perl -0pi -e 's/\Q$ENV{FROM}\E/$ENV{TO}/ or die "guard not found\n"' "$file"
  if ! sh -n "$file" 2>/dev/null; then
    echo "INVALID  $name: the mutated script does not parse"
    bad=1
  elif bash "$test" >/tmp/negative-control.out 2>&1; then
    echo "MISSED   $name: $test still passes without the guard"
    bad=1
  else
    echo "caught   $name: $(grep -m1 '^FAIL: ' /tmp/negative-control.out | cut -c1-200)"
  fi
  git checkout -q -- "$file"
}
# shellcheck disable=SC2016
shcontrol "every get.sh download is size-limited" packaging/get.sh \
  '--max-filesize "$3" ' \
  '' \
  packaging/get_test.sh
# shellcheck disable=SC2016
shcontrol "get.sh refuses an oversized download curl let through" packaging/get.sh \
  ' || { [ "$rc" = 0 ] && [ "$(wc -c <"$2")" -gt "$3" ]; }' \
  '' \
  packaging/get_test.sh
# shellcheck disable=SC2016
shcontrol "get.sh says a download curl stopped is too large" packaging/get.sh \
  '[ "$rc" = 63 ] || ' \
  '' \
  packaging/get_test.sh

control "names service owns only records with the name's marker" internal/names/service/dns.go \
  'if names.CheckName(name) != nil || reservedName(name) || r.Comment != marker(name) {' \
  'if names.CheckName(name) != nil || reservedName(name) {' \
  ./internal/names/service '^(TestOwnsOnlyMarkedRecordsInTheServicesOwnPatterns|TestTheGuardRefusesEveryChangeOutsideItsPatterns|TestRecordsTheServiceDoesNotManageAreNeverTouched)$'
control "names service never owns records of reserved names" internal/names/service/dns.go \
  'if names.CheckName(name) != nil || reservedName(name) || r.Comment != marker(name) {' \
  'if names.CheckName(name) != nil || r.Comment != marker(name) {' \
  ./internal/names/service '^(TestOwnsOnlyMarkedRecordsInTheServicesOwnPatterns|TestTheGuardRefusesEveryChangeOutsideItsPatterns)$'
control "names service owns address records only at the name itself" internal/names/service/dns.go \
  'return rn == fqdn' \
  'return strings.HasSuffix(rn, fqdn)' \
  ./internal/names/service '^TestOwnsOnlyMarkedRecordsInTheServicesOwnPatterns$'
control "names service owns challenge records only at _acme-challenge" internal/names/service/dns.go \
  'return rn == names.ChallengeFQDN(name, s.base)' \
  'return strings.HasSuffix(rn, fqdn)' \
  ./internal/names/service '^(TestOwnsOnlyMarkedRecordsInTheServicesOwnPatterns|TestTheGuardRefusesEveryChangeOutsideItsPatterns)$'
control "names service owns server records only with a valid label" internal/names/service/dns.go \
  'return ok && names.CheckServerLabel(label) == nil' \
  'return ok && label != ""' \
  ./internal/names/service '^TestOwnsOnlyMarkedRecordsInTheServicesOwnPatterns$'
control "names service creates only records it owns" internal/names/service/dns.go \
  'return s.refuse("create", name, r)' \
  'return s.cf.create(ctx, r)' \
  ./internal/names/service '^TestTheGuardRefusesEveryChangeOutsideItsPatterns$'
control "names service updates only records it made" internal/names/service/dns.go \
  'if old.ID == "" || !s.owns(name, old) || !s.owns(name, r) ||' \
  'if old.ID == "" || !s.owns(name, r) ||' \
  ./internal/names/service '^TestTheGuardRefusesEveryChangeOutsideItsPatterns$'
control "names service never moves a record to another type or name" internal/names/service/dns.go \
  'old.Type != r.Type || !strings.EqualFold(old.Name, r.Name) {' \
  'false {' \
  ./internal/names/service '^TestTheGuardRefusesEveryChangeOutsideItsPatterns$'
control "names service deletes only records it made" internal/names/service/dns.go \
  'if old.ID == "" || !s.owns(name, old) {' \
  'if old.ID == "" {' \
  ./internal/names/service '^TestTheGuardRefusesEveryChangeOutsideItsPatterns$'
control "a name with records made by hand cannot be claimed" internal/names/service/dns.go \
  'if !s.owns(name, r) {
			s.log.Info("Refused a claim' \
  'if false && !s.owns(name, r) {
			s.log.Info("Refused a claim' \
  ./internal/names/service '^(TestRecordsTheServiceDoesNotManageAreNeverTouched|TestNewNamesPerDayAcrossEveryone)$'
control "an address with a record made by hand is left alone" internal/names/service/dns.go \
  'if !addrTaken {' \
  'if !addrTaken || true {' \
  ./internal/names/service '^TestHandMadeRecordsAtTheNamesAddressesBlockThemUntilRemoved$'
control "a server address with a record made by hand is left alone" internal/names/service/dns.go \
  'maps.Copy(done, taken)' \
  'maps.Copy(done, map[string]bool{})' \
  ./internal/names/service '^TestRecordsTheServiceDoesNotManageAreNeverTouched$'
control "names service changes only its own domain's zone" internal/names/service/dns.go \
  'if !strings.EqualFold(z.Name, s.base) {' \
  'if false && !strings.EqualFold(z.Name, s.base) {' \
  ./internal/names/service '^TestStartupRefusesARefusedTokenOrAnotherZone$'
control "X-Forwarded-For is only believed from trusted proxies" internal/names/service/addr.go \
  'if !s.trusted(addr) {
		if r.Header.Get("X-Forwarded-For")' \
  'if false && !s.trusted(addr) {
		if r.Header.Get("X-Forwarded-For")' \
  ./internal/names/service '^(TestForwardedForIsOnlyBelievedFromTrustedProxies|TestASpoofedForwardedForFromAnUntrustedPeerIsIgnored)$'
control "the client is the rightmost X-Forwarded-For address that is not a proxy" internal/names/service/addr.go \
  'for i := len(hops) - 1; i >= 0; i-- {' \
  'for i := 0; i < len(hops); i++ {' \
  ./internal/names/service '^TestForwardedForIsOnlyBelievedFromTrustedProxies$'
control "a signed names request works only once" internal/names/service/handlers.go \
  'if n, err := res.RowsAffected(); err != nil || n == 0 {' \
  'if n, err := res.RowsAffected(); false && (err != nil || n == 0) {' \
  ./internal/names/service '^TestSignaturesClockSkewAndReplaysAreRefused$'
control "names point only at public addresses" internal/names/service/handlers.go \
  'if !publicUnicast(a) {' \
  'if false && !publicUnicast(a) {' \
  ./internal/names/service '^TestNamesCannotPointAtPrivateReservedOrProxyAddresses$'
control "requests through Cloudflare's proxy cannot claim names" internal/names/service/handlers.go \
  'if inAny(cloudflareEdge, a) {' \
  'if false && inAny(cloudflareEdge, a) {' \
  ./internal/names/service '^TestNamesCannotPointAtPrivateReservedOrProxyAddresses$'
control "reserved names cannot be claimed" internal/names/service/handlers.go \
  'if reservedName(name) || s.block.has(name) {' \
  'if s.block.has(name) {' \
  ./internal/names/service '^TestReservedAndBlocklistedNamesCannotBeClaimed$'
control "blocklisted names cannot be claimed" internal/names/service/handlers.go \
  'if reservedName(name) || s.block.has(name) {' \
  'if reservedName(name) {' \
  ./internal/names/service '^TestReservedAndBlocklistedNamesCannotBeClaimed$'
control "only the install that holds a name can change it" internal/names/service/handlers.go \
  'case row.Key != key:' \
  'case false:' \
  ./internal/names/service '^TestClaimRefreshServersChallengesAndReleaseEndToEnd$'
control "an install holds only as many names as allowed" internal/names/service/handlers.go \
  'if n < s.cfg.MaxNamesPerKey {' \
  'if n <= s.cfg.MaxNamesPerKey {' \
  ./internal/names/service '^TestAnInstallHoldsOnlyAsManyNamesAsAllowed$'
control "names service per-address rate limit" internal/names/service/handlers.go \
  'if ok, wait := s.perIP.allow(addrBucket(addr, 64)); !ok {' \
  'if ok, wait := s.perIP.allow(addrBucket(addr, 64)); false && !ok {' \
  ./internal/names/service '^TestRateLimitsPerAddressPerKeyAndForNewNames$'
control "names service per-install rate limit" internal/names/service/handlers.go \
  'if ok, wait := s.perKey.allow(c.key); !ok {' \
  'if ok, wait := s.perKey.allow(c.key); false && !ok {' \
  ./internal/names/service '^TestRateLimitsPerAddressPerKeyAndForNewNames$'
control "new names per address a day" internal/names/service/handlers.go \
  'if ok, wait := s.claimsAddr.allow(addrBucket(c.addr, 56)); !ok {' \
  'if ok, wait := s.claimsAddr.allow(addrBucket(c.addr, 56)); false && !ok {' \
  ./internal/names/service '^TestRateLimitsPerAddressPerKeyAndForNewNames$'
control "new names a day across everyone" internal/names/service/handlers.go \
  'if ok, wait := s.claimsAll.allow("all"); !ok {' \
  'if ok, wait := s.claimsAll.allow("all"); false && !ok {' \
  ./internal/names/service '^TestNewNamesPerDayAcrossEveryone$'
control "challenge records per name" internal/names/service/handlers.go \
  'if others >= maxChallenges {' \
  'if false && others >= maxChallenges {' \
  ./internal/names/service '^TestClaimRefreshServersChallengesAndReleaseEndToEnd$'
control "the zone keeps a reserve of free records" internal/names/service/dns.go \
  'keep := s.cfg.RecordReserve' \
  'keep := 0' \
  ./internal/names/service '^TestAFullZoneRefusesServerAddressesThenNamesThenChallenges$'
control "challenge records expire after an hour" internal/names/service/jobs.go \
  'SELECT DISTINCT name FROM challenges WHERE expires_at <= ?' \
  'SELECT DISTINCT name FROM challenges WHERE expires_at <= ? AND 0' \
  ./internal/names/service '^TestNamesLapseAndAreFreedWhenNotRefreshed$'
control "names lapse when they are not refreshed" internal/names/service/jobs.go \
  'SELECT name FROM names WHERE state = ? AND refreshed_at <= ?' \
  'SELECT name FROM names WHERE state = ? AND refreshed_at <= ? AND 0' \
  ./internal/names/service '^TestNamesLapseAndAreFreedWhenNotRefreshed$'
control "released names are held from others" internal/names/service/jobs.go \
  'names.StateReleased, now.Add(-releaseHold).Unix())' \
  'names.StateReleased, now.Unix())' \
  ./internal/names/service '^TestReleasedNamesAreHeldThenFreed$'
control "Cloudflare errors never show the token" internal/names/service/cloudflare.go \
  'scrub(env.Errors, c.token)' \
  'scrub(nil, c.token)' \
  ./internal/names/service '^TestCloudflareErrorsNeverShowTheToken$'
control "names service follows no redirects from Cloudflare" internal/names/service/service.go \
  'CheckRedirect: noRedirects,' \
  'CheckRedirect: nil,' \
  ./internal/names/service '^TestTheServiceFollowsNoRedirects$'
control "names claimed from one network are limited" internal/names/service/handlers.go \
  'if n < s.cfg.MaxNamesPerNetwork {' \
  'if n <= s.cfg.MaxNamesPerNetwork {' \
  ./internal/names/service '^TestNamesPerNetworkAreLimited$'
control "server addresses per install are limited" internal/names/service/handlers.go \
  'case byKey >= serversPerKey:' \
  'case false:' \
  ./internal/names/service '^TestServerAddressesPerInstallAndPerNetworkAreLimited$'
control "server addresses per network are limited" internal/names/service/handlers.go \
  'case byNetwork >= serversPerNetwork:' \
  'case false:' \
  ./internal/names/service '^TestServerAddressesPerInstallAndPerNetworkAreLimited$'
control "server addresses wait until the name is 3 days old" internal/names/service/handlers.go \
  '.Add(serverAddressAge); s.now().Before(from) {' \
  '.Add(serverAddressAge); false && s.now().Before(from) {' \
  ./internal/names/service '^TestServerAddressesWaitUntilTheNameIsThreeDaysOld$'
control "server addresses wait for the dashboard's first answer" internal/names/service/handlers.go \
  'if row.AliveAt == 0 {' \
  'if false && row.AliveAt == 0 {' \
  ./internal/names/service '^TestServerAddressesWaitForTheFirstAnswer$'
control "the zone holds no more records than the quota setting" internal/names/service/dns.go \
  'quota := s.cfg.RecordQuota' \
  'quota := 1 << 30' \
  ./internal/names/service '^TestTheRecordQuotaSettingAppliesWhenCloudflareReportsNoneOrMore$'
control "Cloudflare's record quota wins when it is lower" internal/names/service/dns.go \
  'if u.Quota != nil && *u.Quota < quota {' \
  'if false && u.Quota != nil && *u.Quota < quota {' \
  ./internal/names/service '^TestAFullZoneRefusesServerAddressesThenNamesThenChallenges$'
control "new names leave room for certificate challenges" internal/names/service/dns.go \
  'keep += challengeRoom' \
  'keep += 0' \
  ./internal/names/service '^TestAFullZoneRefusesServerAddressesThenNamesThenChallenges$'
control "server addresses leave room for new names" internal/names/service/dns.go \
  'keep += nameRoom' \
  'keep += 0' \
  ./internal/names/service '^TestAFullZoneRefusesServerAddressesThenNamesThenChallenges$'
control "the alert webhook must be HTTPS" internal/names/service/config.go \
  'if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {' \
  'if err != nil || u.Host == "" || u.User != nil {' \
  ./internal/names/service '^TestNewRefusesAWebhookThatIsNotHTTPS$'
control "alerts follow no redirects" internal/names/service/service.go \
  'CheckRedirect: noRedirects, Transport: cfg.alertTransport' \
  'Transport: cfg.alertTransport' \
  ./internal/names/service '^TestAlertWebhookFailuresAreLoggedWithoutItsURL$'
control "alert failures never show the webhook URL" internal/names/service/alert.go \
  'if errors.As(err, &ue) {' \
  'if false && errors.As(err, &ue) {' \
  ./internal/names/service '^TestAlertWebhookFailuresAreLoggedWithoutItsURL$'
control "each kind of alert goes out at most every 6 hours" internal/names/service/alert.go \
  'if t, ok := a.last[kind]; ok && now.Sub(t) < alertEvery {' \
  'if t, ok := a.last[kind]; false && ok && now.Sub(t) < alertEvery {' \
  ./internal/names/service '^TestAlertsReachTheWebhookAtMostOncePerKindEverySixHours$'
control "names lapse when their dashboard stops answering" internal/names/service/alive.go \
  'SELECT name FROM names WHERE state = ? AND failed_checks >= ? AND max(alive_at, claimed_at) <= ?' \
  'SELECT name FROM names WHERE state = ? AND failed_checks >= ? AND max(alive_at, claimed_at) <= ? AND 0' \
  ./internal/names/service '^TestANameWhoseAddressStopsAnsweringLapsesAfterAWeekAndComesBackOnceItAnswers$'
control "a name lapses only after a week without an answer" internal/names/service/alive.go \
  'names.StateActive, minFailedChecks, now.Add(-unansweredAfter).Unix())' \
  'names.StateActive, minFailedChecks, now.Unix())' \
  ./internal/names/service '^TestANameWhoseAddressStopsAnsweringLapsesAfterAWeekAndComesBackOnceItAnswers$'
control "a name lapses only after 4 failed checks" internal/names/service/alive.go \
  'AND failed_checks >= ? AND' \
  'AND (failed_checks >= ? OR 1) AND' \
  ./internal/names/service '^TestANameLapsesOnlyAfterFourFailedChecksInARow$'
control "an answer starts the failed checks again" internal/names/service/alive.go \
  'UPDATE names SET checked_at = ?, alive_at = ?, failed_checks = 0' \
  'UPDATE names SET checked_at = ?, alive_at = ?, failed_checks = failed_checks' \
  ./internal/names/service '^TestANameLapsesOnlyAfterFourFailedChecksInARow$'
control "names do not lapse while no address answers" internal/names/service/alive.go \
  'return last > now.Add(-checkHealthy).Unix(), err' \
  'return true, err' \
  ./internal/names/service '^TestNamesDoNotLapseForNotAnsweringWhileNoAddressAnswers$'
control "a lapsed name stays off until its dashboard answers" internal/names/service/handlers.go \
  'WHERE name = ? AND key = ? AND (state != ? OR lapse_reason != ? OR ? > 0)' \
  'WHERE name = ? AND key = ? AND (1 OR state != ? OR lapse_reason != ? OR ? > 0)' \
  ./internal/names/service '^TestANameWhoseAddressStopsAnsweringLapsesAfterAWeekAndComesBackOnceItAnswers$'
control "a name that answered before is held for 60 days" internal/names/service/jobs.go \
  'lapse_reason = ? AND alive_at = 0 AND lapsed_at <= ?' \
  'lapse_reason = ? AND lapsed_at <= ?' \
  ./internal/names/service '^TestANameWhoseAddressStopsAnsweringLapsesAfterAWeekAndComesBackOnceItAnswers$'
control "liveness answers must be signed with the name's key" internal/names/service/alive.go \
  'if a.Name != name || !names.VerifyAlive(ed25519.PublicKey(pub), s.base, name, nonce, a.Signature) {' \
  'if false {' \
  ./internal/names/service '^TestLivenessAnswersMustBeFreshSignedAndSmall$'
control "liveness checks follow no redirects" internal/names/service/alive.go \
  'CheckRedirect: noRedirects,' \
  'CheckRedirect: nil,' \
  ./internal/names/service '^TestLivenessAnswersMustBeFreshSignedAndSmall$'
control "liveness answers are limited to 1 KiB" internal/names/service/alive.go \
  'if resp.ContentLength > maxAliveAnswer || len(body) > maxAliveAnswer {' \
  'if false {' \
  ./internal/names/service '^TestLivenessAnswersMustBeFreshSignedAndSmall$'
control "liveness checks connect only to public addresses" internal/names/service/alive.go \
  'if !publicUnicast(addr) || inAny(cloudflareEdge, addr) {' \
  'if inAny(cloudflareEdge, addr) {' \
  ./internal/names/service '^TestLivenessAnswersMustBeFreshSignedAndSmall$'
control "liveness checks never connect to Cloudflare's proxy" internal/names/service/alive.go \
  'if !publicUnicast(addr) || inAny(cloudflareEdge, addr) {' \
  'if !publicUnicast(addr) {' \
  ./internal/names/service '^TestLivenessAnswersMustBeFreshSignedAndSmall$'
control "a lapsed name's address is rechecked at most 6 times an hour" internal/names/service/alive.go \
  'if ok, wait := s.rechecks.allow(row.Name); !ok {' \
  'if ok, wait := s.rechecks.allow(row.Name); false && !ok {' \
  ./internal/names/service '^TestLivenessChecksAreRateLimited$'
control "each name is checked every 6 hours, not more often" internal/names/service/alive.go \
  'names.StateActive, now.Add(-checkEvery).Unix())' \
  'names.StateActive, now.Unix())' \
  ./internal/names/service '^TestLivenessChecksAreRateLimited$'
control "at most 20 liveness checks a minute" internal/names/service/alive.go \
  'for len(due) < checksPerTick && rows.Next() {' \
  'for rows.Next() {' \
  ./internal/names/service '^TestLivenessChecksAreRateLimited$'
control "one liveness check per address at a time" internal/names/service/alive.go \
  'if seen[first] {' \
  'if false && seen[first] {' \
  ./internal/names/service '^TestLivenessChecksAreRateLimited$'
control "names crowding one address do not hold up the others' checks" internal/names/service/alive.go \
  'ORDER BY checked_at, name`' \
  'ORDER BY checked_at, name LIMIT 20`' \
  ./internal/names/service '^TestLivenessChecksAreRateLimited$'
control "certificate attempts per name are limited" internal/names/service/certs.go \
  'if sets >= certSetsPerName {' \
  'if false && sets >= certSetsPerName {' \
  ./internal/names/service '^TestCertificateAttemptsPerNameAreLimited$'
control "new certificates have a weekly budget" internal/names/service/certs.go \
  'if budget := s.cfg.NewCertificates; used >= budget {' \
  'if budget := s.cfg.NewCertificates; false && used >= budget {' \
  ./internal/names/service '^TestNewCertificatesHaveAWeeklyBudgetThatRenewalsDoNotUse$'
control "a challenge value joins an attempt only within its hour" internal/names/service/certs.go \
  'row.Name, now.Add(-certSetWindow).Unix(), challengesPerSet)' \
  'row.Name, now.Add(-100*certSetWindow).Unix(), challengesPerSet)' \
  ./internal/names/service '^TestCertificateAttemptsPerNameAreLimited$'
control "an attempt holds at most 4 challenge values" internal/names/service/certs.go \
  'row.Name, now.Add(-certSetWindow).Unix(), challengesPerSet)' \
  'row.Name, now.Add(-certSetWindow).Unix(), 1000)' \
  ./internal/names/service '^TestCertificateAttemptsPerNameAreLimited$'
control "a name's next holder renews none of the last holder's certificates" internal/names/service/certs.go \
  'row.Name, row.ClaimedAt, now.Add(-renewalLookback).Unix()).Scan(&earlier)' \
  'row.Name, 0, now.Add(-renewalLookback).Unix()).Scan(&earlier)' \
  ./internal/names/service '^TestANamesFirstCertificateSinceItsClaimOrInNinetyDaysIsNew$'
control "a certificate after 90 days without one is new" internal/names/service/certs.go \
  'row.Name, row.ClaimedAt, now.Add(-renewalLookback).Unix()).Scan(&earlier)' \
  'row.Name, row.ClaimedAt, 0).Scan(&earlier)' \
  ./internal/names/service '^TestANamesFirstCertificateSinceItsClaimOrInNinetyDaysIsNew$'
control "certificate attempts are forgotten after 90 days" internal/names/service/jobs.go \
  'DELETE FROM cert_sets WHERE started_at <= ?' \
  'DELETE FROM cert_sets WHERE started_at <= ? AND 0' \
  ./internal/names/service '^TestANamesFirstCertificateSinceItsClaimOrInNinetyDaysIsNew$'
control "trusting a whole network is warned about" internal/names/service/service.go \
  'if p.Bits() < p.Addr().BitLen() {' \
  'if false && p.Bits() < p.Addr().BitLen() {' \
  ./internal/names/service '^TestTrustingAWholeNetworkIsWarnedAbout$'
control "requests through an unlisted proxy alert the owner" internal/names/service/addr.go \
  'if r.Header.Get("X-Forwarded-For") != "" && (addr.IsPrivate() || addr.IsLoopback()) {' \
  'if false && r.Header.Get("X-Forwarded-For") != "" && (addr.IsPrivate() || addr.IsLoopback()) {' \
  ./internal/names/service '^TestForwardedForIsOnlyBelievedFromTrustedProxies$'
control "trusted proxies that would trust every address are refused" internal/names/service/config.go \
  'if p.Bits() == 0 {' \
  'if false && p.Bits() == 0 {' \
  ./internal/names/service '^TestParsePrefixes$'
control "names request signature" internal/names/sign.go \
  'if !ed25519.Verify(' \
  'if false && !ed25519.Verify(' \
  ./internal/names '^TestTamperedRequestsAreRefused$'
control "names requests with a clock more than 5 minutes off are refused" internal/names/sign.go \
  'if skew := at.Sub(now); skew > MaxSkew || skew < -MaxSkew {' \
  'if skew := at.Sub(now); false && (skew > MaxSkew || skew < -MaxSkew) {' \
  ./internal/names '^TestClockSkewBeyondFiveMinutesIsRefusedWithTheServiceTime$'
control "names client sends challenge records only for its own name" internal/names/client.go \
  'if got := strings.TrimSuffix(strings.ToLower(fqdn), "."); got != want {' \
  'if got := strings.TrimSuffix(strings.ToLower(fqdn), "."); false && got != want {' \
  ./internal/names '^TestChallengesForOtherRecordsAreRefusedBeforeSending$'
control "names key files others can read are refused" internal/names/key.go \
  'if fi.Mode().Perm()&0o077 != 0 {' \
  'if false && fi.Mode().Perm()&0o077 != 0 {' \
  ./internal/names '^TestKeyFilesOthersCanReadOrThatAreNotRegularAreRefused$'
control "names service location must be HTTPS unless it is this machine" internal/names/client.go \
  'return nil, fmt.Errorf("the names service location %s must be an https:// address", u.Redacted())' \
  'return u, nil' \
  ./internal/names '^TestCheckServiceURLAllowsHTTPSAndLocalHTTPOnly$'
control "names client follows no redirects" internal/names/client.go \
  'CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },' \
  'CheckRedirect: nil,' \
  ./internal/names '^TestRedirectsAreNotFollowed$'
control "names client believes a Retry-After of at most 8 days" internal/names/client.go \
  ' && time.Duration(s) <= maxRetryAfter/time.Second {' \
  ' {' \
  ./internal/names '^TestServiceRefusalsKeepTheirCodeHintParamsAndRetryAfter$'
control "liveness answers can never pass as signed requests" internal/names/alive.go \
  'const aliveContext = "playkeeper-names-alive-v1"' \
  'const aliveContext = signingContext' \
  ./internal/names '^TestAliveAnswersCanNeverPassAsSignedRequests$'
control "the dashboard signs only well-formed liveness nonces" internal/names/alive.go \
  'if !reNonce.MatchString(nonce) {' \
  'if false && !reNonce.MatchString(nonce) {' \
  ./internal/names '^TestAliveHandlerAnswersOnlyForTheNamesItHolds$'

# Wave 2: two-factor sign-in.
control "a pending sign-in is not a session" internal/panel/auth.go \
  'FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id_hash = ?`, tokenHash(token)).' \
  'FROM (SELECT id_hash, user_id, csrf, created_at, last_seen, expires_at FROM sessions UNION ALL SELECT id_hash, user_id, id_hash, created_at, created_at, expires_at FROM pending_logins) s JOIN users u ON u.id = s.user_id WHERE s.id_hash = ?`, tokenHash(token)).' \
  ./internal/panel '^TestPendingSignInIsNeverASession$'
control "a correct password alone starts no session" internal/panel/twofactor.go \
  'INSERT INTO pending_logins(id_hash, user_id, created_at, expires_at) VALUES(?,?,?,?)`,' \
  'INSERT INTO sessions(id_hash, user_id, created_at, expires_at, csrf, last_seen) SELECT column1, column2, column3, column4, column1, column3 FROM (VALUES(?,?,?,?))`,' \
  ./internal/panel '^TestPendingSignInIsNeverASession$'
control "the second step waits for the code when two-factor sign-in is on" internal/panel/server.go \
  '} else if started {' \
  '} else if false && started {' \
  ./internal/panel '^TestPendingSignInIsNeverASession$'
control "one password may try only so many codes" internal/panel/twofactor.go \
  'WHERE id_hash = ? AND attempts < ?`, idHash, pendingAttempts)' \
  'WHERE id_hash = ? AND ? > 0`, idHash, pendingAttempts)' \
  ./internal/panel '^TestOnePasswordBuysTenCodes$'
control "the second step spends the per-address budget" internal/panel/twofactor.go \
  'if !s.rateLimitIP(w, r) {' \
  'if false && !s.rateLimitIP(w, r) {' \
  ./internal/panel '^TestSecondStepIsRateLimitedPerAddress$'
control "every second-factor code is counted under concurrency" internal/panel/twofactor.go \
  'conn.ExecContext(ctx, `BEGIN IMMEDIATE' \
  'conn.ExecContext(ctx, `BEGIN' \
  ./internal/panel '^TestConcurrentWrongCodesAreAllCounted$'
control "the sign-in limit counts IPv6 by /64" internal/panel/server.go \
  'if p, err := a.Prefix(64); err == nil {' \
  'if p, err := a.Prefix(128); err == nil {' \
  ./internal/panel '^TestSignInLimiterCountsIPv6By64$'
control "wrong recovery codes never lock app codes" internal/twofactor/twofactor.go \
  'f.RecoveryFailures++' \
  'f.Failures++' \
  ./internal/twofactor '^TestWrongRecoveryCodesNeverLockAppCodes$'
control "wrong recovery codes are stored" internal/panel/twofactor.go \
  'next.Failures, next.RecoveryFailures, nullMS(next.LockedUntil), next.Revision}' \
  'next.Failures, 0, nullMS(next.LockedUntil), next.Revision}' \
  ./internal/panel '^TestWrongRecoveryCodesAreCountedWithoutLockingAppCodes$'
control "an unfinished setup shows only to the session that started it" internal/panel/twofactor.go \
  'if f.On() || setupSession == idHash {' \
  'if true || f.On() || setupSession == idHash {' \
  ./internal/panel '^TestAnUnfinishedSetupBelongsToTheSessionThatStartedIt$'
control "only the session that started a setup cancels it" internal/panel/twofactor.go \
  'confirmed_at IS NULL AND setup_session = ?`, sess.User.ID, sess.IDHash)' \
  'confirmed_at IS NULL AND length(?) > 0`, sess.User.ID, sess.IDHash)' \
  ./internal/panel '^TestAnUnfinishedSetupBelongsToTheSessionThatStartedIt$'
control "an app code works once" internal/twofactor/twofactor.go \
  'f.LastStep = step' \
  '_ = step' \
  ./internal/twofactor '^TestSignInWithAnAppCodeOnce$'
control "wrong app codes lock app codes" internal/twofactor/twofactor.go \
  'case f.Failures >= LockAfter:' \
  'case false && f.Failures >= LockAfter:' \
  ./internal/twofactor '^TestWrongCodesLockAppCodesForLongerEachTime$'
control "turning two-factor sign-in on needs the password" internal/twofactor/twofactor.go \
  'return f, Setup{}, &Error{Kind: KindPasswordWrong}' \
  '_ = f' \
  ./internal/twofactor '^TestTurningOnShowsTheSecretOnlyUntilConfirmed$'
control "turning two-factor sign-in off needs a code" internal/twofactor/twofactor.go \
  'if next, _, err := f.check(code, now); err != nil {' \
  'if next, _, err := f.check(code, now); false && err != nil {' \
  ./internal/twofactor '^TestTurningOffNeedsThePasswordAndACode$'
control "turning two-factor sign-in on signs out other sessions" internal/panel/twofactor.go \
  'DELETE FROM sessions WHERE user_id = ? AND id_hash != ?`, sess.User.ID, sess.IDHash)' \
  'DELETE FROM sessions WHERE 0 AND user_id = ? AND id_hash != ?`, sess.User.ID, sess.IDHash)' \
  ./internal/panel '^TestTwoFactorSignInNeedsACodeAfterThePassword$'
control "resetting two-factor sign-in signs out every session" internal/panel/twofactor.go \
  's.deleteUserSessions(u.ID)' \
  '_ = u.ID' \
  ./internal/panel '^TestResetTwoFactorFromTheCommandLine$'
control "signing out everywhere ends pending sign-ins" internal/panel/auth.go \
  'DELETE FROM pending_logins WHERE user_id = ?`, userID)' \
  'DELETE FROM pending_logins WHERE 0 AND user_id = ?`, userID)' \
  ./internal/panel '^TestSecondStepExpiresAndCanBeCancelled$'
control "a password change ends pending sign-ins" internal/panel/server.go \
  'DELETE FROM pending_logins WHERE user_id = ?`, sess.User.ID)' \
  'DELETE FROM pending_logins WHERE 0 AND user_id = ?`, sess.User.ID)' \
  ./internal/panel '^TestSecondStepExpiresAndCanBeCancelled$'

# Wave 2: the names service's liveness check.
control "the liveness check answers only for the machine's name" internal/agent/address.go \
  'st.Free != nil && st.Free.Name.Name == name && st.Free.Name.State' \
  'st.Free != nil && st.Free.Name.State' \
  ./internal/agent '^TestLivenessCheckIsAnsweredOnlyForTheMachinesName$'
control "the liveness check does not answer for a released name" internal/agent/address.go \
  ' && st.Free.Name.State != names.StateReleased' \
  '' \
  ./internal/agent '^TestLivenessCheckIsAnsweredOnlyForTheMachinesName$'
control "the liveness check answers for the name being claimed" internal/agent/address.go \
  'a.setClaiming(name)' \
  'a.setClaiming("")' \
  ./internal/agent '^TestLivenessCheckIsAnsweredOnlyForTheMachinesName$'
control "the liveness check is a public route, answered without sign-in" internal/panel/public.go \
  '{prefix: names.AlivePath, limits: aliveLimits, handler: s.aliveRoute()},' \
  '{prefix: names.AlivePath + "off/", limits: aliveLimits, handler: s.aliveRoute()},' \
  ./internal/panel '^TestLivenessCheckIsPassedToTheAgentWithoutSignIn$|^TestTheNamesServiceGetsItsSignedAnswerOnThePanelsPort$'
control "the liveness check tells the agent the Host it asked" internal/panel/alive.go \
  'url.Values{"host": {r.Host}}' \
  'nil' \
  ./internal/panel '^TestLivenessCheckIsPassedToTheAgentWithoutSignIn$'
control "the port-8443 listener serves only the liveness check" internal/panel/alive.go \
  'return s.securityHeaders(s.logRequests(mux))' \
  'mux.Handle("/", s.Handler()); return s.securityHeaders(s.logRequests(mux))' \
  ./internal/panel '^TestLivenessCheckIsPassedToTheAgentWithoutSignIn$'
control "per-address liveness check limit" internal/panel/alive.go \
  'var aliveLimits = publicLimits{perMinute: 20,' \
  'var aliveLimits = publicLimits{perMinute: 1 << 20,' \
  ./internal/panel '^TestLivenessChecksAreLimitedPerAddress$'
control "the port-8443 listener goes through the public group" internal/panel/alive.go \
  'mux.Handle(names.AlivePath, s.public.handler(names.AlivePath))' \
  'mux.Handle(names.AlivePath, s.aliveRoute())' \
  ./internal/panel '^TestLivenessChecksAreLimitedPerAddress$'
control "a certificate limit waits for the names service's Retry-After" internal/agent/certificates.go \
  'retry = now.Add(ne.RetryAfter)' \
  'retry = now.Add(time.Hour)' \
  ./internal/agent '^TestCertificateLimitWaitsForTheNamesService$'
control "refused server addresses are asked for again only when due" internal/agent/address.go \
  'if st.Free.ServersWait != "" && !now.Before(st.Free.ServersRetry) {' \
  'if st.Free.ServersWait != "" {' \
  ./internal/agent '^TestServerAddressesWaitForTheNamesService$'
control "resource pack links: HTTPS only with a certificate players' games trust" internal/certs/store.go \
  'if _, err := e.cert.Leaf.Verify(opts); err != nil {' \
  'if _, err := e.cert.Leaf.Verify(opts); err != nil && at.IsZero() {' \
  ./internal/certs '^TestStoreTrusted$'
control "resource pack links: a look at the certificates in progress doesn't hide a new one" internal/certs/store.go \
  'if every < 0 {' \
  'if every < 0 && false {' \
  ./internal/certs '^TestStoreCanLookEveryTime$'
control "resource pack links: back to plain HTTP a week before the certificate runs out" internal/agent/packs.go \
  'const packCertMargin = 7 * 24 * time.Hour' \
  'const packCertMargin = 0' \
  ./internal/agent '^TestResourcePackLinksUseHTTPSWithATrustedCertificate$'
control "resource pack links: a start offers the link the certificate allows now" internal/agent/lifecycle.go \
  'pack, err := resourcePackEnv(s.currentOffer(sc.ResourcePack))' \
  'pack, err := resourcePackEnv(sc.ResourcePack)' \
  ./internal/agent '^TestResourcePackLinksUseHTTPSWithATrustedCertificate$'
control "free address refresh: a refused connection keeps the other IP version's record" internal/names/client.go \
  'errors.Is(err, syscall.EADDRNOTAVAIL)' \
  'errors.Is(err, syscall.EADDRNOTAVAIL) || errors.Is(err, syscall.ECONNREFUSED)' \
  ./internal/names '^TestRefreshSetsBothVersionsAndClearsOnlyOneThatHasNoRoute$'
control "free address change: undone at the names service when it can't be saved" internal/agent/address.go \
  'if _, rerr := c.Release(ctx); rerr != nil && !namesCode(rerr, names.CodeNotClaimed) {' \
  'if rerr := error(nil); rerr != nil {' \
  ./internal/agent '^TestFreeAddressChangeAndRelease$'
control "free address change: the old name is claimed back only once the new one is released" internal/agent/address.go \
  'if _, rerr := c.Release(ctx); rerr != nil && !namesCode(rerr, names.CodeNotClaimed) {' \
  'if _, rerr := c.Release(ctx); false && rerr != nil {' \
  ./internal/agent '^TestFreeAddressChangeAndRelease$'
control "own domain: setting one keeps the released free name claimable" internal/agent/address.go \
  'Since: a.now().UTC(), IP: st.IP, Released: st.Released}' \
  'Since: a.now().UTC(), IP: st.IP}' \
  ./internal/agent '^TestFreeAddressChangeAndRelease$'
control "own domain: removing it keeps the released free name claimable" internal/agent/address.go \
  'if err := a.setAddress(addressState{IP: st.IP, Released: st.Released}); err != nil {' \
  'if err := a.setAddress(addressState{IP: st.IP}); err != nil {' \
  ./internal/agent '^TestFreeAddressChangeAndRelease$'
control "own domain: a certificate attempt that finds the name wrong brings the next look forward" internal/agent/certificates.go \
  'if !saved || ready {' \
  'if true || !saved || ready {' \
  ./internal/agent '^TestOwnDomainChecksTheNameBeforeHTTP01$'

if [ "$bad" != 0 ]; then
  echo "some guards are not covered by a failing test"
  exit 1
fi
echo "every guard's test failed without it"

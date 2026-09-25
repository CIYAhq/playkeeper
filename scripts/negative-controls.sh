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
    echo "caught   $name: $(grep -m1 -E '^\s+[a-z_]+_test\.go:[0-9]+:' /tmp/negative-control.out | sed 's/^\s*//')"
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
  'if ok, wait := s.loginIP.allow("ip:" + clientIP(r)); !ok {' \
  'if ok, wait := s.loginIP.allow("ip:" + clientIP(r)); false && !ok {' \
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
  'e.Type().IsRegular() && strings.HasSuffix(p, ".mca")' \
  ./internal/agent '^TestNewChunksComeFromRegionFiles$'
control "a crash that logs Stopping server is still a crash" internal/agent/lifecycle.go \
  'graceful := s.sawStopping && !s.sawCrash' \
  'graceful := s.sawStopping' \
  ./internal/agent '^TestCrashIsExplainedFromTheRunsLog$'
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
  'entries, err := os.ReadDir(s.dataDir() + "/" + addonDir(sc))' \
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
  'sm("POST", "/api/servers/{id}/addons/remove", "/v1/servers/{id}/addons/remove"),' \
  '{"POST", "/api/servers/{id}/addons/remove", needSessionCSRF, actView, s.serverProxy("POST", "/v1/servers/{id}/addons/remove")},' \
  ./internal/panel '^TestMembersCanLookButNotManage$'
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

if [ "$bad" != 0 ]; then
  echo "some guards are not covered by a failing test"
  exit 1
fi
echo "every guard's test failed without it"

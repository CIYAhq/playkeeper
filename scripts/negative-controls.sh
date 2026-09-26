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
  'if err := u.sys.WaitVersion(hctx2, u.cfg.SocketPath, cert, u.panelPort, version); err != nil {' \
  'if err := u.sys.WaitVersion(hctx2, u.cfg.SocketPath, cert, u.panelPort, version); false && err != nil {' \
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
  'if contains(m.Units, u) || extra[u] {' \
  'if contains(m.Units, u) {' \
  ./internal/install '^TestUninstallRemovesTheUpdaterEvenIfTheManifestMissesIt$'
control "a machine joins one dashboard at a time" internal/install/link.go \
  'if d, err := machinelink.LoadDashboard(sys.P(cfg.LinkDashboardPath())); err == nil {' \
  'if d, err := machinelink.LoadDashboard(sys.P(cfg.LinkDashboardPath())); false && err == nil {' \
  ./internal/install '^TestJoiningStartsTheLinkAndLeavingTellsTheDashboardFirst$'
control "leaving changes nothing unless the dashboard was told or --force is set" internal/install/link.go \
  'if err != nil && !force {' \
  'if false && err != nil && !force {' \
  ./internal/install '^TestLeavingADashboardThatIsGoneNeedsForce$'
control "a machine installed to join opens only the game port" internal/install/install.go \
  'return []int{o.GamePort}' \
  'return []int{o.PanelPort, o.GamePort}' \
  ./internal/install '^TestInstallingToJoinRunsNoDashboardAndOpensOnlyTheGamePort$'
control "the hub keeps the wrong join codes it counts" internal/machinelink/hub.go \
  'if err := h.store.SetJoinFailures(ctx, h.guard.fail(now, from)); err != nil {' \
  'if err := h.store.SetJoinFailures(ctx, nil); h.guard.fail(now, from) == nil && err != nil {' \
  ./internal/machinelink '^TestJoinPauseOutlastsARestart$'
control "a restarted hub starts from the wrong join codes kept" internal/machinelink/hub.go \
  'guard: newGuard(o.Limits, kept, o.Now())' \
  'guard: newGuard(o.Limits, kept[:0], o.Now())' \
  ./internal/machinelink '^TestJoinPauseOutlastsARestart$'
control "a wrong code kept from a wrong clock pauses joining no longer than the window" internal/machinelink/code.go \
  '			g.fails[i].At = now' \
  '			_ = now' \
  ./internal/machinelink '^TestGuardStartsFromTheFailuresKept$'
control "the dashboard keeps wrong join codes in panel.db" internal/panel/linkstore.go \
  '	for _, f := range fails {
		network := ""' \
  '	for _, f := range fails[:0] {
		network := ""' \
  ./internal/panel '^(TestTooManyWrongCodesPauseJoining|TestLinkStoreKeepsJoinFailures)$'
control "RCON finds a closed connection before writing" internal/minecraft/rcon.go \
  'if err := r.probe(); err != nil {' \
  'if err := r.probe(); false && err != nil {' \
  ./internal/minecraft '^TestRCONExecOnClosedConnectionIsUnsent$'
control "RCON stops when the caller's context ends" internal/minecraft/rcon.go \
  '		_ = r.conn.SetDeadline(time.Unix(1, 0))
' \
  '' \
  ./internal/minecraft '^(TestRCONExecHonoursContextDeadline|TestRCONExecStopsWhenCancelled)$'
control "a console command that went out is never sent again" internal/agent/collector.go \
  'if attempt == 1 || !errors.Is(err, minecraft.ErrUnsent) || ctx.Err() != nil {' \
  'if attempt == 1 || ctx.Err() != nil {' \
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
control "a restore preview read again keeps its staged source" internal/agent/backups.go \
  'st.preview.Source, st.preview.ReceivedAt = s.Preview.Source, s.Preview.ReceivedAt' \
  'st.preview.ReceivedAt = s.Preview.ReceivedAt' \
  ./internal/agent '^TestRestorePreviewSaysOnceTheBackupWasMadeHere$'
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

if [ "$bad" != 0 ]; then
  echo "some guards are not covered by a failing test"
  exit 1
fi
echo "every guard's test failed without it"

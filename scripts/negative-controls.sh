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
control "preflight port collision" internal/install/install.go \
  'if sys.Listening(p.port) {' \
  'if false && sys.Listening(p.port) {' \
  ./internal/install '^TestPreflightRefusesEachCollisionWithAFix$'
control "preflight existing Minecraft setups" internal/install/install.go \
  'case len(existing) == 0:' \
  'case true:' \
  ./internal/install '^TestPreflightRefusesEachCollisionWithAFix$'
control "start/stop no-op under the operation lock" internal/agent/handlers.go \
  'release, ok := a.holdOpLock()
	if !ok {
		writeError(w, a.busyError())
		return
	}
	_, running, err := a.containerRunning(r.Context())
	if err == nil && !running {' \
  'release, ok := func() { }, !a.busy()
	if !ok {
		writeError(w, a.busyError())
		return
	}
	_, running, err := a.containerRunning(r.Context())
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
  'if err := a.putBackupBack(b); err != nil {' \
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
  'u.Usage+n+s.cfg.RecordReserve <= *u.Quota' \
  'u.Usage+n <= *u.Quota' \
  ./internal/names/service '^TestAFullZoneRefusesNewNamesAndServerAddresses$'
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
  'CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },' \
  'CheckRedirect: nil,' \
  ./internal/names/service '^TestTheServiceFollowsNoRedirects$'
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

if [ "$bad" != 0 ]; then
  echo "some guards are not covered by a failing test"
  exit 1
fi
echo "every guard's test failed without it"

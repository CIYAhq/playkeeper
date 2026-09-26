#!/usr/bin/env bash
# Negative controls: removes one safety guard at a time in a throwaway git
# worktree and runs the tests that cover it. Every run must FAIL; a control
# that still passes means the guard is untested. Nothing is committed.
# The worktree is made from HEAD, so commit changes before running it.
# Usage: scripts/negative-controls.sh
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
export PATH="$root/.tools/go/bin:$root/.tools/node/bin:$PATH" CGO_ENABLED=0
wt="$(mktemp -d)/playkeeper"
git -C "$root" worktree add --detach -q "$wt" HEAD
trap 'git -C "$root" worktree remove --force "$wt"' EXIT
cd "$wt"
# The web controls run vitest with the checkout's own dependencies, which
# scripts/setup.sh installs.
if [ -d "$root/web/node_modules" ]; then
  ln -s "$root/web/node_modules" web/node_modules
fi
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

webcontrol() { # NAME FILE FROM TO TEST-FILE TEST-NAME
  local name=$1 file=$2 test=${5#web/} pattern=$6
  if [ ! -d web/node_modules ]; then
    echo "INVALID  $name: web/node_modules is missing; run scripts/setup.sh"
    bad=1
    return
  fi
  FROM=$3 TO=$4 perl -0pi -e 's/\Q$ENV{FROM}\E/$ENV{TO}/ or die "guard not found\n"' "$file"
  if (cd web && npx vitest run "$test" -t "$pattern") >/tmp/negative-control.out 2>&1; then
    echo "MISSED   $name: $test \"$pattern\" still passes without the guard"
    bad=1
  elif grep -qE 'Transform failed|SyntaxError|Failed to load url|No test files found' /tmp/negative-control.out; then
    echo "INVALID  $name: the mutated code does not run"
    bad=1
  else
    echo "caught   $name: $(grep -m1 -E '^ +(FAIL|×) ' /tmp/negative-control.out | sed 's/^ *//' | cut -c1-200)"
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
control "previews don't spend the actions' rate limit" internal/panel/server.go \
  'bucket = s.previews' \
  'bucket = s.control' \
  ./internal/panel '^TestPreviewsHaveTheirOwnRateLimit$'
control "previews have a rate limit of their own" internal/panel/server.go \
  'newLimiter(120, time.Minute, opts.Now)' \
  'newLimiter(1000, time.Minute, opts.Now)' \
  ./internal/panel '^TestPreviewsHaveTheirOwnRateLimit$'
control "every preview bucket entry is a route" internal/panel/server.go \
  '"POST /api/servers/{id}/backup-rules/estimate": true,' \
  '"POST /api/servers/{id}/backup-rules/estimates": true,' \
  ./internal/panel '^TestEveryPreviewRouteIsACheckedRoute$'
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
# Restore path, found checking it on a real server.
control "why a restore was undone reads as one sentence" internal/agent/backups.go \
  'j.Why = "The restored world did not start (" + clause(err) + ")."' \
  'j.Why = "The restored world did not start (" + err.Error() + ")."' \
  ./internal/agent '^TestAnUndoneRestoreSaysWhyInOneSentence$'
control "a world that isn't there yet is looked for at the next sample" internal/agent/collector.go \
  'if !dirExists(filepath.Join(s.dataDir(), level)) {' \
  'if false && !dirExists(filepath.Join(s.dataDir(), level)) {' \
  ./internal/agent '^TestWorldSizeIsMeasuredOnceTheWorldExists$'
control "a restore has the world measured again at the next sample" internal/agent/backups.go \
  '	s.worldChanged()
	restoreStep(ctx, "moved")' \
  '	restoreStep(ctx, "moved")' \
  ./internal/agent '^TestWorldSizeIsMeasuredAgainAfterARestore$'
control "a backup of a server folder without its world says so" internal/backup/online.go \
  'case errors.As(err, &noWorld):' \
  'case false && errors.As(err, &noWorld):' \
  ./internal/backup '^TestABackupWithoutAWorldSaysSo$'
control "the status says where the previous world is while its folder is missing" internal/agent/handlers.go \
  'st.WorldMissing = s.worldMissing()' \
  'st.WorldMissing = nil' \
  ./internal/agent '^TestAWorldFolderARestoreLeftMissingIsShownUntilItIsBack$'
control "a backup refused for a missing world folder says where the previous world is" internal/agent/backups.go \
  'if m := s.worldMissing(); m != nil {
		err := errWorldMissing(m, "back it up")' \
  'if m := s.worldMissing(); false && m != nil {
		err := errWorldMissing(m, "back it up")' \
  ./internal/agent '^TestAWorldFolderARestoreLeftMissingIsShownUntilItIsBack$'
control "a start refused for a missing world folder is marked so" internal/agent/lifecycle.go \
  '	if err := s.ensureDirs("press Start"); err != nil {
		markRestoreRefusal(h, err)
		return err' \
  '	if err := s.ensureDirs("press Start"); err != nil {
		return err' \
  ./internal/agent '^TestAWorldFolderARestoreLeftMissingIsShownUntilItIsBack$'
control "a restore the next start put back says so" internal/agent/backups.go \
  's.restoreSettled(j, movedBack, atStart)' \
  '_, _ = movedBack, atStart' \
  ./internal/agent '^TestARestoreSettledAtStartSaysSo$'
control "a restore finished after a restart has its own activity line" internal/agent/backups.go \
  'kind = "world_restored_after_restart"' \
  'kind = "world_restored"' \
  ./internal/agent '^TestARestoreFinishedAfterARestartSaysSo$'
webcontrol "the Overview says where the previous world is while its folder is missing" web/src/pages/server/overview.tsx \
  'if (s.worldMissing) return <WorldMissingNotice server={s} />' \
  'if (s.worldMissing && false) return <WorldMissingNotice server={s} />' \
  web/src/pages/pages.test.tsx 'long after the restore failed'
webcontrol "the World tab says where the previous world is while its folder is missing" web/src/pages/server/world.tsx \
  'if (s.worldMissing) return <WorldMissingNotice server={s} className={className} />' \
  'if (s.worldMissing && false) return <WorldMissingNotice server={s} className={className} />' \
  web/src/pages/pages.test.tsx 'long after the restore failed'
webcontrol "a start or backup refused for a missing world folder goes once the world is back" web/src/lib/phase.ts \
  "if (op.detail?.errorKind === 'world_missing') return !s.worldMissing" \
  "if (op.detail?.errorKind === 'never') return !s.worldMissing" \
  web/src/pages/pages.test.tsx 'once the world is back'
webcontrol "a backup that just failed never hides a world copy's Discard" web/src/pages/server/world.tsx \
  "  const failed = failedJob(s)
  return (
" \
  "  const failed = failedJob(s)
  if (failed?.kind === 'backup' && dismissed !== failed.id) return <FailedJobNotice server={s} op={failed} onDismiss={() => setDismissed(failed.id)} className={className} />
  return (
" \
  web/src/pages/pages.test.tsx 'Discard under a backup'
webcontrol "Start says it waits for the missing world folder" web/src/lib/phase.ts \
  "if (st.worldMissing) return t('reason.worldMissing')" \
  "if (st.worldMissing && false) return t('reason.worldMissing')" \
  web/src/lib/lib.test.ts 'start a server whose world folder'
webcontrol "a backup says it waits for the missing world folder" web/src/lib/phase.ts \
  "    case 'change':
      return undefined
    case 'backup':
" \
  "    case 'change':
    case 'backup':
      return undefined
" \
  web/src/lib/lib.test.ts 'back up a server whose world folder'
webcontrol "Back up now waits for the missing world folder" web/src/pages/server/world.tsx \
  "const blocked = whyNot(s, 'backup', offline)" \
  "const blocked = whyNot(s, 'change', offline)" \
  web/src/pages/pages.test.tsx 'offer a backup while the world folder is missing'
webcontrol "Make my first backup waits for the missing world folder" web/src/pages/server/world.tsx \
  "disabledReason={whyNot(s, 'backup', offline)}" \
  "disabledReason={whyNot(s, 'change', offline)}" \
  web/src/pages/pages.test.tsx 'offer a backup while the world folder is missing'
webcontrol "the server menu's Back up now waits for the missing world folder" web/src/pages/server/index.tsx \
  "const backUpBlocked = whyNot(server, 'backup', offline)" \
  "const backUpBlocked = whyNot(server, 'change', offline)" \
  web/src/pages/pages.test.tsx 'offer a backup while the world folder is missing'
webcontrol "Home says a server's world folder is missing instead of napping" web/src/pages/home.tsx \
  'if (s.worldMissing)
        return (' \
  'if (s.worldMissing && false)
        return (' \
  web/src/pages/pages.test.tsx 'lets only a stopped server nap'
webcontrol "the activity says a restore was finished after Playkeeper restarted" web/src/components/app/activity.tsx \
  "return t('activity.restoredAfterRestart', { server })" \
  "return t('activity.restored', { server })" \
  web/src/lib/lib.test.ts 'what Playkeeper did after it restarted'
webcontrol "the activity says a previous world was put back after Playkeeper restarted" web/src/components/app/activity.tsx \
  "return t('activity.putBack', { server })" \
  "return t('activity.restored', { server })" \
  web/src/lib/lib.test.ts 'what Playkeeper did after it restarted'
webcontrol "a long activity line shortens instead of widening the page" web/src/components/app/activity.tsx \
  '<span className="w-0 flex-1 truncate">' \
  '<span className="min-w-0 flex-1 truncate">' \
  web/src/pages/pages.test.tsx 'long activity line'
control "the data pack list waits for the missing world folder" internal/agent/packs.go \
  'if m := s.worldMissing(); m != nil {
		return nil, errWorldMissing(m, "try again")' \
  'if m := s.worldMissing(); false && m != nil {
		return nil, errWorldMissing(m, "try again")' \
  ./internal/agent '^TestTheDataPackListWaitsForTheMissingWorldFolder$'
control "applying a restore waits for the missing world folder or an unsettled restore" internal/agent/handlers.go \
  'if err := target.restoreRefusal("restore again"); err != nil {
			a.auditFor(p.ServerID' \
  'if err := target.restoreRefusal("restore again"); false && err != nil {
			a.auditFor(p.ServerID' \
  ./internal/agent '^TestNoRestoreStartsWhileAnotherIsUnsettled$'
control "a new icon refused for the missing world folder says to upload it again" internal/agent/settings.go \
  's.ensureDirs("upload the icon again")' \
  's.ensureDirs("press Start")' \
  ./internal/agent '^TestAnIconOrPackRefusedForTheMissingWorldFolderSaysWhatToRedo$'
control "a new data pack refused for the missing world folder says to add it again" internal/agent/packs.go \
  's.ensureDirs("add the data pack again")' \
  's.ensureDirs("press Start")' \
  ./internal/agent '^TestAnIconOrPackRefusedForTheMissingWorldFolderSaysWhatToRedo$'
webcontrol "pre-generating says it waits for the missing world folder" web/src/lib/phase.ts \
  "      return undefined
    case 'backup':
    case 'pregen':
" \
  "    case 'pregen':
      return undefined
    case 'backup':
" \
  web/src/lib/lib.test.ts 'pre-generate or restore while'
webcontrol "a restore says it waits for the missing world folder" web/src/lib/phase.ts \
  "return worldMissingReason(st) ?? restoreUnsettledReason(st)" \
  "return restoreUnsettledReason(st)" \
  web/src/lib/lib.test.ts 'pre-generate or restore while'
webcontrol "the pre-generation page's Start waits for the missing world folder" web/src/pages/server/world-pregen.tsx \
  "whyNot({ ...s, operation: otherJob }, 'pregen', ws.stale)" \
  "whyNot({ ...s, operation: otherJob }, 'change', ws.stale)" \
  web/src/pages/server/world.test.tsx 'waits for a world folder a restore left missing'
webcontrol "the World card's pre-generation row waits for the missing world folder" web/src/pages/server/world-links.tsx \
  'lineKey={pg?.state} busy={working(pg)} disabledReason={worldMissingReason(s)}' \
  'lineKey={pg?.state} busy={working(pg)}' \
  web/src/pages/server/world.test.tsx 'pre-generating while'
webcontrol "the phone's pre-generation row waits for the missing world folder" web/src/pages/server/world-links.tsx \
  'line={active ? pregenLine(pg, s.name) : undefined} busy={working(pg)} disabledReason={worldMissingReason(s)}' \
  'line={active ? pregenLine(pg, s.name) : undefined} busy={working(pg)}' \
  web/src/pages/server/world.test.tsx 'pre-generating while'
webcontrol "the World tab's restore drop zone waits for the missing world folder" web/src/pages/server/world.tsx \
  '<RestoreDropZone server={s} onPreview={setPreview} disabledReason={restoreBlocked} />' \
  '<RestoreDropZone server={s} onPreview={setPreview} />' \
  web/src/pages/pages.test.tsx 'offer a restore while'
webcontrol "a disabled drop zone won't choose a file" web/src/components/app/restore.tsx \
  '<button type="button" disabled={!!disabledReason} title={disabledReason}' \
  '<button type="button"' \
  web/src/pages/pages.test.tsx 'offer a restore while'
webcontrol "a backup's restore waits for the missing world folder" web/src/pages/server/world.tsx \
  '<MenuItem disabled={!!restoreBlocked} title={restoreBlocked} onClick={onRestore}' \
  '<MenuItem onClick={onRestore}' \
  web/src/pages/pages.test.tsx 'offer a restore while'
webcontrol "the phone's Restore a world waits for the missing world folder" web/src/pages/server/world.tsx \
  '<button type="button" disabled={!!restoreBlocked} title={restoreBlocked} onClick={() => setRestoreSheet(true)}' \
  '<button type="button" onClick={() => setRestoreSheet(true)}' \
  web/src/pages/pages.test.tsx 'offer a restore while'
webcontrol "a copy's restore waits for the missing world folder" web/src/pages/server/copy-restore.tsx \
  "const cantRestore = whyNot(s, 'restore', offline)" \
  "const cantRestore = whyNot(s, 'change', offline)" \
  web/src/pages/pages.test.tsx 'restore a copy while'
webcontrol "a refused server icon says what to do next" web/src/pages/server/settings.tsx \
  "else toastManager.add({ title: errorText(e), description: e instanceof ApiError ? e.hint : undefined, type: 'error' })" \
  "else toastManager.add({ title: errorText(e), type: 'error' })" \
  web/src/pages/pages.test.tsx 'new icon is refused'
webcontrol "a toast wraps a long path" web/src/components/ui/toast.tsx \
  '<div className="flex min-w-0 flex-col gap-0.5 wrap-anywhere">' \
  '<div className="flex flex-col gap-0.5">' \
  web/src/lib/interaction.test.tsx 'wrap a long path'
webcontrol "a notice wraps a long path" web/src/components/app/bits.tsx \
  '<p className="min-w-0 flex-1 text-[13px] leading-5 wrap-anywhere">' \
  '<p className="min-w-0 flex-1 text-[13px] leading-5">' \
  web/src/pages/server/world.test.tsx 'previous world is when a restore'
control "no restore starts while another is unsettled" internal/agent/backups.go \
  'if !s.busy() && s.restoreUnsettled() {' \
  'if false && !s.busy() && s.restoreUnsettled() {' \
  ./internal/agent '^TestNoRestoreStartsWhileAnotherIsUnsettled$'
control "a restore from a backup waits for an unsettled one" internal/agent/handlers.go \
  'if err := s.restoreRefusal("restore again"); err != nil {
		s.audit(actor, "restore.staged", b.ID,' \
  'if err := s.restoreRefusal("restore again"); false && err != nil {
		s.audit(actor, "restore.staged", b.ID,' \
  ./internal/agent '^TestNoRestoreStartsWhileAnotherIsUnsettled$'
control "a restore from an upload waits for an unsettled one" internal/agent/handlers.go \
  'if err := target.restoreRefusal("restore again"); err != nil {
			a.auditFor(target.id' \
  'if err := target.restoreRefusal("restore again"); false && err != nil {
			a.auditFor(target.id' \
  ./internal/agent '^TestNoRestoreStartsWhileAnotherIsUnsettled$'
control "a restore from an off-site copy waits for an unsettled one" internal/agent/offsite.go \
  'if err := s.restoreRefusal("restore again"); err != nil {' \
  'if err := s.restoreRefusal("restore again"); false && err != nil {' \
  ./internal/agent '^TestNoRestoreStartsWhileAnotherIsUnsettled$'
control "the status says a restore isn't settled" internal/agent/handlers.go \
  'if s.restoreUnsettled() {
			st.RestoreUnsettled' \
  'if false {
			st.RestoreUnsettled' \
  ./internal/agent '^TestNoRestoreStartsWhileAnotherIsUnsettled$'
control "the audit log says a start put back only the settings of a world moved back by hand" internal/agent/backups.go \
  '"Your previous world was already back in place, and Playkeeper put its settings back", "put the previous world'"'"'s settings back"' \
  '"Your previous world was already back in place, and Playkeeper put its settings back", "put the previous world back"' \
  ./internal/agent '^TestAnAgentStartSettlesAWorldMovedBackByHand$'
webcontrol "a restore says it waits for one that isn't finished" web/src/lib/phase.ts \
  "return worldMissingReason(st) ?? restoreUnsettledReason(st)" \
  "return worldMissingReason(st)" \
  web/src/lib/lib.test.ts 'restore while another restore'
webcontrol "the World tab's restores wait for one that isn't finished" web/src/lib/phase.ts \
  "return worldMissingReason(st) ?? restoreUnsettledReason(st)" \
  "return worldMissingReason(st)" \
  web/src/pages/pages.test.tsx 'offer a restore while another'
webcontrol "the phone's copy rows wait for the missing world folder" web/src/pages/server/world.tsx \
  '<Button size="lg" variant="outline" disabledReason={restoreBlocked} onClick={() => void restore.start(r.copy)}>' \
  '<Button size="lg" variant="outline" onClick={() => void restore.start(r.copy)}>' \
  web/src/pages/pages.test.tsx 'copy from a phone'
webcontrol "the restore sheet's drop zone waits for an unfinished restore" web/src/pages/server/world.tsx \
  '                compact
                disabledReason={restoreBlocked}
' \
  '                compact
' \
  web/src/pages/pages.test.tsx 'restores in the phone'
webcontrol "the restore sheet's list waits for an unfinished restore" web/src/pages/server/world.tsx \
  '                        disabled={!!restoreBlocked}
                        title={restoreBlocked}
' \
  '' \
  web/src/pages/pages.test.tsx 'restores in the phone'
control "the reconcile tick settles a restore once its world folder is back" internal/agent/lifecycle.go \
  '	s.settleWhenBack(ctx)
' \
  '' \
  ./internal/agent '^TestARestoreIsSettledOnceItsWorldIsBackWithoutAnAgentRestart$'
control "Start settles a restore before it reads the settings" internal/agent/handlers.go \
  '	s.settleBeforeStart(ctx)
	if err := s.setDesired(api.DesiredRunning); err != nil {' \
  '	if err := s.setDesired(api.DesiredRunning); err != nil {' \
  ./internal/agent '^TestARestoreIsSettledOnceItsWorldIsBackWithoutAnAgentRestart$'
control "a restore settled without an agent restart doesn't say it restarted" internal/agent/backups.go \
  '	if atStart {
		back, audited = back+" when it started again", audited+" after the Playkeeper agent restarted"
	}' \
  '	back, audited = back+" when it started again", audited+" after the Playkeeper agent restarted"' \
  ./internal/agent '^TestARestoreIsSettledOnceItsWorldIsBackWithoutAnAgentRestart$'
control "no job starts a server whose restore isn't settled" internal/agent/lifecycle.go \
  '	if err := s.startRefusal(h); err != nil {
		markRestoreRefusal(h, err)
		return err
	}
' \
  '' \
  ./internal/agent '^TestNoJobStartsAServerWhoseRestoreIsntSettled$'
control "a start refused for an unsettled restore is marked so" internal/agent/backups.go \
  '(ae.Code == codeWorldMissing || ae.Code == codeRestoreUnsettled)' \
  'ae.Code == codeWorldMissing' \
  ./internal/agent '^TestNoJobStartsAServerWhoseRestoreIsntSettled$'
control "a restore the agent can't settle says why" internal/agent/backups.go \
  '	s.settleProblem = problem
' \
  '	s.settleProblem = ""
' \
  ./internal/agent '^TestARestoreThatCantBeSettledSaysWhy$'
control "a swap journal that can't be read holds no server back" internal/agent/backups.go \
  'if j != nil && j.ServerID == s.id && j.OpID != opID {' \
  'if j == nil || j.ServerID == s.id && j.OpID != opID {' \
  ./internal/agent '^TestAnUnreadableSwapJournalHoldsNoServerBack$'
control "the status says a swap journal can't be read" internal/agent/backups.go \
  'if _, err := readSwapJournal(s.stageDir(stage)); err != nil {' \
  'if _, err := readSwapJournal(s.stageDir(stage)); false && err != nil {' \
  ./internal/agent '^TestAnUnreadableSwapJournalHoldsNoServerBack$'
webcontrol "the World tab says a restore isn't finished, and what finishes it" web/src/pages/server/world.tsx \
  '      <RestoreUnsettledNotice server={s} className={className} />
' \
  '' \
  web/src/pages/pages.test.tsx 'what finishes it'
webcontrol "the unfinished restore's notice says to stop a running server" web/src/components/app/world-missing.tsx \
  "controls(s).canStop ? t('world.unsettledStop', { server: s.name }) : t('world.unsettledSoon')" \
  "t('world.unsettledSoon')" \
  web/src/pages/pages.test.tsx 'what finishes it'
webcontrol "a world copy's Discard waits for an unfinished restore" web/src/pages/server/world.tsx \
  'disabledReason={busyReason(s) ?? restoreUnsettledReason(s)}' \
  'disabledReason={busyReason(s)}' \
  web/src/pages/pages.test.tsx 'Discard off while a restore'
webcontrol "a restore Playkeeper couldn't finish says so" web/src/lib/phase.ts \
  "return st.restoreUnsettled.problem ? t('reason.restoreStuck') : t('reason.restoreUnsettled')" \
  "return t('reason.restoreUnsettled')" \
  web/src/lib/lib.test.ts 'restore while another restore'
webcontrol "a start refused for an unfinished restore goes once it's settled" web/src/lib/phase.ts \
  "if (op.detail?.errorKind === 'restore_unsettled') return !s.restoreUnsettled" \
  "if (op.detail?.errorKind === 'never') return !s.restoreUnsettled" \
  web/src/pages/pages.test.tsx 'refused while a restore wasn'
control "the first admin only on an empty install" internal/panel/auth.go \
  'SELECT ?, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM users)' \
  'SELECT ?, ?, ?, ?, ?' \
  ./internal/panel '^(TestConcurrentSetupsCreateOneAdmin|TestFirstAdminOnlyOnAnEmptyInstall)$' 3
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
  'if err := tally.add(rel, size); err != nil {
		return FileEntry{}, refusal(rel, err)' \
  'if err := tally.add(rel, size); false && err != nil {
		return FileEntry{}, refusal(rel, err)' \
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
control "the reconciler's save-on refuses no action" internal/agent/backups.go \
  'release, ok := s.trySavingLock()' \
  'release, ok := s.holdOpLock()' \
  ./internal/agent '^TestSaveOnRetryRefusesNoAction$'
control "a save-on without an answer holds a backup up for seconds only" internal/agent/backups.go \
  'const resumeWait = 5 * time.Second' \
  'const resumeWait = 30 * time.Second' \
  ./internal/agent '^TestSaveOnRetryRefusesNoAction$'
control "an online backup waits for a save-on before it pauses saving" internal/agent/backups.go \
  'if o.Console != nil {' \
  'if false && o.Console != nil {' \
  ./internal/agent '^TestSavingLockKeepsSaveOnOutOfABackup$'
control "the reconciler's save-on waits for an online backup" internal/agent/backups.go \
  'release, ok := s.trySavingLock()' \
  'release, ok := func() {}, true' \
  ./internal/agent '^TestSavingLockKeepsSaveOnOutOfABackup$'
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
control "lag is explained from the current run's GC pauses only" internal/agent/running.go \
  'gc := pausesSince(s.lag.gc, s.runStartedAt)' \
  'gc := slices.Clone(s.lag.gc)' \
  ./internal/agent '^TestLagCountsOnlyTheCurrentRunsGC$'
control "chunk counts read region folders only" internal/agent/running.go \
  'e.Type().IsRegular() && path.Base(dir) == "region" && strings.HasSuffix(p, ".mca")' \
  'e.Type().IsRegular() && path.Base(dir) != "" && strings.HasSuffix(p, ".mca")' \
  ./internal/agent '^TestNewChunksComeFromRegionFiles$'
control "a chunk count that can't list a folder is not kept" internal/agent/running.go \
  'if !optional || !errors.Is(err, fs.ErrNotExist) {' \
  'if false && (!optional || !errors.Is(err, fs.ErrNotExist)) {' \
  ./internal/agent '^TestAChunkCountThatCannotListTheWorldIsNotKept$'
control "nether and end folders missing beside the world don't void a chunk count" internal/agent/running.go \
  'if !optional || !errors.Is(err, fs.ErrNotExist) {' \
  'if true || !optional || !errors.Is(err, fs.ErrNotExist) {' \
  ./internal/agent '^TestAChunkCountThatCannotListTheWorldIsNotKept$'
control "running out of memory, then Stopping server, is still a crash" internal/agent/collector.go \
  's.sawCrash, s.lastError = true, "Java ran out of memory."' \
  's.sawCrash, s.lastError = false, "Java ran out of memory."' \
  ./internal/agent '^TestAnOutOfMemoryErrorThenStoppingServerIsACrash$'
control "the GC log's folder is given to the game user on every start" internal/agent/lifecycle.go \
  'return f.Chown(uid, gid)' \
  'return nil' \
  ./internal/agent '^TestTheLogsFolderIsGivenToTheGameOnEveryStart$'
control "giving the GC log's folder never follows a link at logs" internal/agent/lifecycle.go \
  'os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK' \
  'os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK' \
  ./internal/agent '^TestTheLogsFolderIsGivenToTheGameOnEveryStart$'
control "a crash that logs Stopping server is still a crash" internal/agent/lifecycle.go \
  'return s.sawStopping && !s.sawCrash' \
  'return s.sawStopping' \
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
control "a start refused for a restore keeps the crash" internal/agent/handlers.go \
  '	h.askedFor = true
' \
  '	h.askedFor = true
	s.forgetCrashes()
' \
  ./internal/agent '^TestAStartForgetsTheCrashOnlyOnceItGoesAhead$'
control "a start that goes ahead forgets the crash" internal/agent/lifecycle.go \
  '	if h.askedFor {
		s.forgetCrashes()
	}' \
  '	if false {
		s.forgetCrashes()
	}' \
  ./internal/agent '^TestAStartForgetsTheCrashOnlyOnceItGoesAhead$'
control "preflight port collision" internal/install/install.go \
  'if sys.Listening(p.port) {' \
  'if false && sys.Listening(p.port) {' \
  ./internal/install '^TestPreflightRefusesEachCollisionWithAFix$'
control "preflight existing Minecraft setups" internal/install/install.go \
  'case len(existing) == 0:' \
  'case true:' \
  ./internal/install '^TestPreflightRefusesEachCollisionWithAFix$'
# Without the lock a start can slip in between the check and the write; the
# sleep holds that gap open so the race shows in most runs, not one in three.
control "start/stop no-op under the operation lock" internal/agent/handlers.go \
  'release, ok := s.holdOpLock()
	if !ok {
		writeError(w, s.busyError())
		return
	}
	_, running, err := s.containerRunning(r.Context())
	if err == nil && !running {' \
  'release, ok := func() { }, true
	if !ok {
		writeError(w, s.busyError())
		return
	}
	_, running, err := s.containerRunning(r.Context())
	if err == nil && !running {
		time.Sleep(50 * time.Millisecond)' \
  ./internal/agent '^TestConcurrentStartAndStopLeaveDesiredMatchingContainer$' 8

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
control "services that don't come up on a machine without a panel point only at the agent's journal" internal/install/install.go \
  'journals = "-u playkeeper-agent"' \
  'journals = "-u playkeeper-agent -u playkeeper-panel"' \
  ./internal/install '^TestAHealthTimeoutNamesOnlyTheUnitsTheMachineRuns$'
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
control "a join without a usable answer is told apart from a refusal" internal/machinelink/link.go \
  'err = errJoinUnanswered(a, err)' \
  '_ = errJoinUnanswered(a, err)' \
  ./internal/machinelink '^TestAJoinWhoseAnswerIsLostFinishesWhenSentAgain$'
control "making a join code waits for a join redeeming one" internal/machinelink/hub.go \
  'h.joinMu.Lock()
	defer h.joinMu.Unlock()
	now := h.now()
	codes, err := h.store.JoinCodes(ctx)' \
  'now := h.now()
	codes, err := h.store.JoinCodes(ctx)' \
  ./internal/machinelink '^TestMakingACodeNeverDropsOneBeingRedeemed$'
control "a join the dashboard may have accepted keeps its key" internal/install/link.go \
  'if kept || machinelink.MayHaveJoined(err) {' \
  'if false && (kept || machinelink.MayHaveJoined(err)) {' \
  ./internal/install '^TestAJoinWhoseAnswerIsLostFinishesWhenRunAgain$'
control "a key kept from an earlier join stays when a later one is refused" internal/install/link.go \
  'if kept || machinelink.MayHaveJoined(err) {' \
  'if machinelink.MayHaveJoined(err) || false && kept {' \
  ./internal/install '^TestAJoinWhoseAnswerIsLostFinishesWhenRunAgain$'
control "a join runs again with the key it kept" internal/install/link.go \
  'if id, err := machinelink.LoadIdentity(path); err == nil {' \
  'if id, err := machinelink.LoadIdentity(path + ".none"); err == nil {' \
  ./internal/install '^TestAJoinWhoseAnswerIsLostFinishesWhenRunAgain$'
control "a refused join leaves no key behind" internal/install/link.go \
  'os.Remove(keyPath)' \
  '_ = keyPath' \
  ./internal/install '^TestAJoinThatFailsLeavesNothingBehind$'
control "a join whose dashboard can't be saved fails" internal/install/link.go \
  'if err = d.Save(dashPath); err == nil {' \
  'if err := d.Save(dashPath); err == nil {' \
  ./internal/install '^TestAJoinThatCantSaveTheDashboardFinishesWhenRunAgain$'
control "a join whose dashboard can't be saved keeps its key" internal/install/link.go \
  'os.Remove(dashPath)' \
  'os.Remove(dashPath); os.Remove(keyPath)' \
  ./internal/install '^TestAJoinThatCantSaveTheDashboardFinishesWhenRunAgain$'
control "install --join says only the join is left after a lost answer" cmd/playkeeper/link.go \
  'case errors.As(err, &unfinished):' \
  'case false && errors.As(err, &unfinished):' \
  ./cmd/playkeeper '^TestInstallingToJoinWithoutAnAnswerSaysOnlyTheJoinIsLeft$'
control "a reply that keeps coming may outlast the link's time limit" internal/machinelink/hub.go \
  'func (w *waitLimit) leave() {
	if w == nil {' \
  'func (w *waitLimit) leave() {
	if true {' \
  ./internal/machinelink '^TestLinkTimeLimitsCountOnlyWaitingOnTheMachine$'
control "a reply that stops coming still ends at the link's time limit" internal/machinelink/hub.go \
  'if w.away--; w.away == 0 && !w.ended {' \
  'if w.away--; false && !w.ended {' \
  ./internal/machinelink '^TestLinkTimeLimitsCountOnlyWaitingOnTheMachine$'
control "waiting for a request body's own source doesn't count against the machine" internal/machinelink/hub.go \
  'b.wait.leave()
	n, err := b.rc.Read(p)
	b.wait.back()' \
  'n, err := b.rc.Read(p)' \
  ./internal/machinelink '^TestLinkTimeLimitsCountOnlyWaitingOnTheMachine$'
control "a joined machine takes data packs bigger than a request" internal/agent/link.go \
  '"POST /v1/servers/{id}/datapacks":             true,' \
  '"POST /v1/servers/{id}/datapacks":             false,' \
  ./internal/panel '^TestAJoinedMachineTakesBigPacks$'
control "a joined machine takes resource packs bigger than a request" internal/agent/link.go \
  '"POST /v1/servers/{id}/resourcepack":          true,' \
  '"POST /v1/servers/{id}/resourcepack":          false,' \
  ./internal/panel '^TestAJoinedMachineTakesBigPacks$'
control "the dashboard keeps wrong join codes in panel.db" internal/panel/linkstore.go \
  '	for _, f := range fails {
		network := ""' \
  '	for _, f := range fails[:0] {
		network := ""' \
  ./internal/panel '^(TestTooManyWrongCodesPauseJoining|TestLinkStoreKeepsJoinFailures)$'
control "a joined machine never gets the dashboard's host as its address" internal/panel/server.go \
  'if withHost && m.Kind != remoteKind {' \
  'if withHost {' \
  ./internal/panel '^TestAJoinedMachinesAddressRoutesCarryNoDashboardHost$'
control "a joined machine never gets the browser's panelHost" internal/panel/server.go \
  '				q.Del("panelHost")' \
  '				_ = q' \
  ./internal/panel '^TestAJoinedMachinesAddressRoutesCarryNoDashboardHost$'
control "a joined machine gets no free name or own domain" internal/panel/server.go \
  'an("/api/machines/{mid}/address/claim", "/v1/address/claim"),' \
  'am("/api/machines/{mid}/address/claim", "/v1/address/claim"),' \
  ./internal/panel '^TestAJoinedMachineGetsNoFreeName$'
control "a friend's invite to a joined machine's server gives its IP and port" internal/panel/friends.go \
  'if m.Kind == remoteKind {
		addr = s.joinedAddress(r.Context(), m, port)' \
  'if false {
		addr = s.joinedAddress(r.Context(), m, port)' \
  ./internal/panel '^TestAnInviteToAJoinedMachinesServerGivesItsIPAndPort$'
# API tokens under Wave 5's team roles.
control "a tool asks whether its caller's account may take its action" internal/mcptools/tools.go \
  'if !access.mayTake(s.act) {' \
  'if false && !access.mayTake(s.act) {' \
  ./internal/mcptools '^TestEveryToolChecksItsActionWithTheCallersAccount$'
control "a token's tools ask permit about its account as it is now" internal/panel/mcp.go \
  'May: func(act string) bool { return permit(account, action(act), "") == nil }}, nil' \
  'May: func(act string) bool { return permit(account, action(act), "") == nil || true }}, nil' \
  ./internal/panel '^TestATokenFollowsItsAccountsRole$'
control "a token stops once its account holds a lower role than when it was made" internal/panel/tokens.go \
  'if grantRank(accountGrant(a)) < grantRank(t.MadeAs) {' \
  'if false && grantRank(accountGrant(a)) < grantRank(t.MadeAs) {' \
  ./internal/panel '^TestATokenFollowsItsAccountsRole$'
control "a lower role on the Team page stops the account's tokens at once" internal/panel/team.go \
  '	s.checkAccountTokens(t.UserID)
' \
  '' \
  ./internal/panel '^TestATokenFollowsItsAccountsRole$'
control "taking someone off the team revokes their tokens" internal/panel/team.go \
  'revokeAccountTokens(t.UserID, sess.User.Username, "its account was removed from the team")' \
  'closeTokenSessions("")' \
  ./internal/panel '^TestATokenFollowsItsAccountsRole$'
control "each tool takes the action of its dashboard route" internal/mcptools/specs.go \
  'name: "create_backup", title: "Make a backup", scope: mcp.ScopeManage, act: ActMakeBackups,' \
  'name: "create_backup", title: "Make a backup", scope: mcp.ScopeManage, act: ActView,' \
  ./internal/panel '^TestEveryToolTakesTheActionOfItsDashboardRoute$'
# Wave 9: search_addons and remove_addon.
control "search results reach the model on one line each" internal/mcptools/addons.go \
  'Summary: oneLine(card.Summary)' \
  'Summary: card.Summary' \
  ./internal/mcptools '^TestSearchAddonsListsWhatInstallAddonTakes$'
control "remove_addon keeps the settings folder" internal/mcptools/addons.go \
  'KeepConfig: true, Actor: c.actor()}' \
  'KeepConfig: false, Actor: c.actor()}' \
  ./internal/mcptools '^TestRemoveAddonRemovesWhatPlaykeeperInstalled$'
control "remove_addon leaves an add-on others need" internal/mcptools/addons.go \
  'case len(p.NeededBy) > 0:' \
  'case false && len(p.NeededBy) > 0:' \
  ./internal/mcptools '^TestRemoveAddonLeavesWhatNeedsAPerson$'
control "remove_addon leaves a file that changed" internal/mcptools/addons.go \
  'case p.Changed:' \
  'case false && p.Changed:' \
  ./internal/mcptools '^TestRemoveAddonLeavesWhatNeedsAPerson$'
control "remove_addon removes only what Playkeeper installed" internal/mcptools/addons.go \
  'if f.Addon != nil && (f.Status == "managed" || f.Status == "modified") {' \
  'if f.Addon != nil {' \
  ./internal/mcptools '^TestRemoveAddonLeavesWhatNeedsAPerson$'
control "remove_addon asks which when two add-ons match" internal/mcptools/addons.go \
  '		case 1:
			return match, nil
		}
		return api.Addon{}, &mcp.ToolError{Kind: "addon_ambiguous"' \
  '		case 1, 2:
			return match, nil
		}
		return api.Addon{}, &mcp.ToolError{Kind: "addon_ambiguous"' \
  ./internal/mcptools '^TestRemoveAddonLeavesWhatNeedsAPerson$'
control "remove_addon takes Admin rights, as the dashboard's Remove does" internal/mcptools/specs.go \
  'name: "remove_addon", title: "Remove a plugin or mod", scope: mcp.ScopeOwner, act: ActManageServers,' \
  'name: "remove_addon", title: "Remove a plugin or mod", scope: mcp.ScopeOwner, act: ActMakeBackups,' \
  ./internal/panel '^TestEveryToolTakesTheActionOfItsDashboardRoute$'
control "a joined machine's pack page gives its IP and port" internal/panel/packshare.go \
  'case fp.m.Kind == remoteKind:' \
  'case false:' \
  ./internal/panel '^TestAJoinedMachinesPackPageGivesItsIPAndPort$'
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
  'if experimental && !req.AcceptExperimental {' \
  'if false && experimental && !req.AcceptExperimental {' \
  ./internal/agent '^TestCatalogIsLiveFromPaperMCAndExperimentalNeedsConsent$'
control "version changes to experimental versions need consent" internal/agent/versions.go \
  'if experimental && !req.AcceptExperimental {' \
  'if false && experimental && !req.AcceptExperimental {' \
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
# Wave 4: the friends' pack page at /packs/<token>.
control "friends' pack links hold at least 128 random bits" internal/modpacks/share/token.go \
  'const TokenLen = 22' \
  'const TokenLen = 12' \
  ./internal/modpacks/share '^TestNewTokenHasTheInviteCodesShape$'
control "friends' pack links favour no letter" internal/modpacks/share/token.go \
  'if b < 248 && len(out) < TokenLen {' \
  'if len(out) < TokenLen {' \
  ./internal/modpacks/share '^TestNewToken(IsUniform|SkipsBiasedBytes)$'
control "stopping sharing forgets the friends' pack link" internal/agent/packshare.go \
  "UPDATE servers SET packs_public = 0, packs_token = '' WHERE id = ?" \
  'UPDATE servers SET packs_public = 0 WHERE id = ?' \
  ./internal/agent '^TestPackShareLinkIsMadeWhenSharedAndReplacedAfterward$'
control "a friends' pack link opens only with its own token" internal/agent/packshare.go \
  'subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1' \
  'subtle.ConstantTimeCompare([]byte(t), []byte(t)) == 1' \
  ./internal/agent '^TestPackLinkAnswersAlikeWhateverTheReason$'
control "a stopped server's pack page is unavailable" internal/agent/packshare.go \
  ' || s.desired() != api.DesiredRunning {' \
  ' {' \
  ./internal/agent '^TestPackLinkAnswersAlikeWhateverTheReason$'
control "server-only mods stay off the friends' pack page" internal/modpacks/share/page.go \
  'if m.InFile || m.ByHand {' \
  'if true {' \
  ./internal/panel '^TestFriendsPackPageIsPublicAndListsOnlyWhatFriendsGet$'
control "every unavailable friends' pack link gets one answer" internal/panel/packshare.go \
  'default:
			packGone(w)' \
  'default:
			http.Error(w, fp.share.Server+" has no such file.", http.StatusNotFound)' \
  ./internal/panel '^TestFriendsPackLinksAnswerAlikeWhateverTheReason$'
control "a machine that can't answer leaves a friends' pack link unavailable" internal/panel/packshare.go \
  'case err != nil:
			packGone(w)' \
  'case errors.Is(err, errPackGone):
			packGone(w)
		case err != nil:
			http.Error(w, "Try again later.", http.StatusServiceUnavailable)' \
  ./internal/panel '^TestFriendsPackLinksAnswerAlikeWhateverTheReason$'
control "the friends' pack page itself never tells a working link from another" internal/panel/packshare.go \
  'if !sub {
			s.packPage(w, r)' \
  'if !sub {
			if _, err := s.friendsPack(r.Context(), token); err != nil {
				packGone(w)
				return
			}
			s.packPage(w, r)' \
  ./internal/panel '^TestFriendsPackLinksAnswerAlikeWhateverTheReason$'
control "friends' pack pages are limited by the connection's address" internal/panel/public.go \
  'key := rt.prefix + " " + addressKey(r.RemoteAddr)' \
  'key := rt.prefix + " " + r.Header.Get("X-Forwarded-For")' \
  ./internal/panel '^TestFriendsPackPagesAreLimitedPerAddress$'
control "game files: a link on the way to a file is refused" internal/gamefiles/gamefiles.go \
  'err = folderError(p, fi)' \
  'err = nil' \
  ./internal/gamefiles '^TestLinksAreRefusedAtEveryStep$'
control "game files: a link or special file is refused before it is opened" internal/gamefiles/gamefiles.go \
  'fi, err := d.root.Lstat(name)
	if err == nil {
		err = fileError(name, fi)' \
  'fi, err := d.root.Lstat(name)
	if err == nil {
		err = nil' \
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
  'fi, err := d.root.Lstat(name)
	if err == nil {
		err = fileError(name, fi)' \
  'fi, err := d.root.Lstat(name)
	if err == nil {
		err = nil' \
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
  'func levelName(dataDir string) (string, error) {
	b, err := readProperties(dataDir)' \
  'func levelName(dataDir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, "server.properties"))' \
  ./internal/backup '^TestLevelNameDoesNotFollowALinkOrWaitOnAPipe$'
control "a backup refuses a server.properties Playkeeper won't read" internal/backup/archive.go \
  'return "world", nil
	}
	if err != nil {
		return "", err
	}' \
  'return "world", nil
	}
	if err != nil {
		return "world", nil
	}' \
  ./internal/backup '^TestLevelNameDoesNotFollowALinkOrWaitOnAPipe$'
control "an online backup refuses a server.properties Playkeeper won't read" internal/backup/staging.go \
  'level, err := levelName(dataDir)' \
  'level, err := LevelName(dataDir), error(nil)' \
  ./internal/backup '^TestRefusedBeforeAnythingIsPaused$'
control "a backup a file Playkeeper won't read stopped says what to do" internal/agent/backups.go \
  'if errors.As(err, &ge) {' \
  'if false && errors.As(err, &ge) {' \
  ./internal/agent '^TestBackupRefusesAServerPropertiesItWontReadBeforeStopping$'
control "the check before a backup stops for a file Playkeeper won't read" internal/agent/backups.go \
  'case gamefiles.KindOf(err) != "":
		err = gameFileError(err, notBackedUp)' \
  'case gamefiles.KindOf(err) != "" && false:
		err = gameFileError(err, notBackedUp)' \
  ./internal/agent '^TestBackupRefusesAServerPropertiesItWontReadBeforeStopping$'
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
control "operations: the audit entry is stored before the operation shows finished" internal/agent/state.go \
  'if err = a.insertAudit(tx, serverID, op.Actor, op.Kind, target, op.Status, op.Error); err == nil {
			err = writeOperation(tx, op)
		}' \
  'if err = writeOperation(tx, op); err == nil {
			err = a.insertAudit(tx, serverID, op.Actor, op.Kind, target, op.Status, op.Error)
		}' \
  ./internal/agent '^TestAFinishedOperationIsAlreadyAudited$'
control "operations: a server's operation stores its end with its audit entry" internal/agent/lifecycle.go \
  's.finishOperation(s.id, "server", &done)' \
  's.saveOperation(&done)
		s.audit(done.Actor, kind, "server", done.Status, done.Error)' \
  ./internal/agent '^TestAFinishedOperationIsAlreadyAudited$'
control "resource packs: a listed pack doesn't wait while the agent is asked about another" internal/panel/packs.go \
  'if wait == nil || known && !started {' \
  'if wait == nil || known && !started && false {' \
  ./internal/panel '^TestListedPacksDontWaitForTheAgent$'
control "add-on installs: a confirmed plan is required" internal/agent/addons.go \
  'key, err := parseAddonKey(req.Source, req.ProjectID)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := confirmedPlan(req.Fingerprint); err != nil {' \
  'key, err := parseAddonKey(req.Source, req.ProjectID)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := confirmedPlan(req.Fingerprint); false && err != nil {' \
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
control "add-on details: a pre-release is never offered to install or update to" internal/agent/addons.go \
  'd.Latest, d.Notes = nil, ""' \
  'd.Notes = ""' \
  ./internal/agent '^TestAddonDetailsOfferNoPrerelease$'
control "add-on notices: only pre-releases doesn't say to allow them" internal/agent/addons.go \
  'hint = onlyPrereleaseHint' \
  'hint = n.Hint' \
  ./internal/agent '^TestOnlyPrereleaseNoticesOfferNothingPlaykeeperCantDo$'
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
control "creating from a template checks the plan the user confirmed" internal/agent/templates.go \
  'if err := p.Confirm(fingerprint); err != nil {' \
  'if err := p.Confirm(p.Fingerprint); err != nil {' \
  ./internal/agent '^TestCreateFromTemplateRefusesAChangedPlan$'
control "a source that does not answer stops a template's first start" internal/agent/templates.go \
  'if sourceDown(res.Reason.Kind) {' \
  'if false && sourceDown(res.Reason.Kind) {' \
  ./internal/agent '^TestCreateFromTemplateTriesAgainOnTheNextStart$'
control "a template decides the type, version and settings" internal/agent/handlers.go \
  'if req.Modpack != nil || req.Type != "" || req.VersionID != "" || req.Build != "" || req.PlayStyle != "" || req.Gameplay != nil || req.MOTD != "" || req.MaxPlayers != 0 {' \
  'if false {' \
  ./internal/agent '^TestTemplateRequestsAreChecked$'
control "a template plan without a version creates nothing" internal/agent/templates.go \
  'if p.Version == nil {' \
  'if false {' \
  ./internal/agent '^TestTemplateCreateNeedsAVersion$'
control "a server made from a template is recorded with what the template adds" internal/agent/handlers.go \
  'record = func(tx *sql.Tx, id string) error { return saveTemplateInstall(tx, id, planned, dataPacks, at) }' \
  'record = func(tx *sql.Tx, id string) error { _, _, _ = planned, dataPacks, at; return nil }' \
  ./internal/agent '^TestTemplateRecordIsNeverLostSilently$'
control "a new server whose record can't be written is not made" internal/agent/servers.go \
  '		if err := spec.record(tx, id); err != nil {
			return nil, nil, err
		}' \
  '		_ = spec.record(tx, id)' \
  ./internal/agent '^TestTemplateRecordIsNeverLostSilently$'
control "a start that finds a template's record gone says so" internal/agent/templates.go \
  '	if !found {
		return s.templateLost(h, sc)' \
  '	if false && !found {
		return s.templateLost(h, sc)' \
  ./internal/agent '^TestTemplateRecordIsNeverLostSilently$'
control "a template's record and settings settle together" internal/agent/templates.go \
  '	defer tx.Rollback()' \
  '	defer tx.Commit()' \
  ./internal/agent '^TestTemplateRecordIsNeverLostSilently$'
control "packs cannot suggest operator or function permission levels" internal/modpacks/rules.go \
  '"force-gamemode", "gamemode",' \
  '"force-gamemode", "function-permission-level", "op-permission-level", "gamemode",' \
  ./internal/modpacks '^TestPacksCannotSuggestPermissionLevels$'
control "a pack file's path with an invisible character is refused before any download" internal/modpacks/mrpack/mrpack.go \
  'if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) || r == utf8.RuneError {' \
  'if unicode.IsControl(r) || r == utf8.RuneError {' \
  ./internal/modpacks '^TestUnsafeIndexPathsAreRefused$'
control "a pack's settings are read and written without following a link" internal/agent/modpacks.go \
  'cur, err := d.ReadProperties()
	if errors.Is(err, fs.ErrNotExist) {
		cur, err = nil, nil
	}
	if err == nil {
		err = d.WriteProperties(mergeProperties(cur, props))
	}' \
  'cur, err := os.ReadFile(filepath.Join(s.dataDir(), "server.properties"))
	if errors.Is(err, fs.ErrNotExist) {
		cur, err = nil, nil
	}
	if err == nil {
		err = os.WriteFile(filepath.Join(s.dataDir(), "server.properties"), mergeProperties(cur, props), 0o640)
	}' \
  ./internal/agent '^TestPackSettingsAreNotReadOrWrittenThroughALink$'
control "a CurseForge key is saved only once CurseForge accepts it" internal/agent/addonsources.go \
  'if err := modpacks.CheckKey(ctx, a.opts.UpstreamClient, key); err != nil {' \
  'if err := modpacks.CheckKey(ctx, a.opts.UpstreamClient, key); false && err != nil {' \
  ./internal/agent '^TestCurseForgeKeyIsCheckedSavedAndRemoved$'
control "the CurseForge key file is readable by root only" internal/agent/addonsources.go \
  'os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)' \
  'os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)' \
  ./internal/agent '^TestCurseForgeKeyIsCheckedSavedAndRemoved$'
control "a member can't change the CurseForge key" internal/panel/server.go \
  'mm("POST", "/api/machines/{mid}/addon-sources/curseforge", "/v1/addon-sources/curseforge", actManageAddonSources),' \
  'mm("POST", "/api/machines/{mid}/addon-sources/curseforge", "/v1/addon-sources/curseforge", actView),' \
  ./internal/panel '^TestOnlyTheOwnerChangesTheCurseForgeKey$'
control "only the owner changes the CurseForge key, not an admin of all servers" internal/panel/server.go \
  'mm("POST", "/api/machines/{mid}/addon-sources/curseforge", "/v1/addon-sources/curseforge", actManageAddonSources),' \
  'mm("POST", "/api/machines/{mid}/addon-sources/curseforge", "/v1/addon-sources/curseforge", actManageMachine),' \
  ./internal/panel '^TestOnlyTheOwnerChangesTheCurseForgeKey$'
control "an exported template names no one" internal/agent/templates.go \
  '	file, err := templates.MarshalFile(t)' \
  '	t.Author = q.Get("author")
	file, err := templates.MarshalFile(t)' \
  ./internal/agent '^TestTemplatesNameNoOne$'
control "voice chat installs only with leave to open its port" internal/agent/addons.go \
  'if voice && !req.OpenPorts {' \
  'if false && voice && !req.OpenPorts {' \
  ./internal/agent '^TestVoiceChatOpensItsPortAndClosesItWhenRemoved$'
control "removing voice chat closes its port" internal/agent/addons.go \
  'if slices.ContainsFunc(drop, voiceChat) {' \
  'if false && slices.ContainsFunc(drop, voiceChat) {' \
  ./internal/agent '^TestVoiceChatOpensItsPortAndClosesItWhenRemoved$'
control "voice chat gets a UDP port nothing on the machine uses" internal/agent/curated.go \
  'return func(p int) bool { return used[p] || a.opts.UDPPortInUse(p) }' \
  'return func(p int) bool { return used[p] }' \
  ./internal/agent '^TestVoiceChatOpensItsPortAndClosesItWhenRemoved$'
control "voice chat that comes with a template gets its UDP port" internal/agent/curated.go \
  'h.phase("opening_port")
	return s.setUpVoiceChat(h, sc, srv, h.op.Actor)' \
  '_ = srv
	return nil' \
  ./internal/agent '^TestTemplateVoiceChatGetsItsPort$'
control "voice chat a template's Try again installs gets its UDP port" internal/agent/templates.go \
  'if err := s.templateVoiceChat(h, sc, planned); err != nil {
		return err
	}
	packSkips, err := s.installTemplatePacks(ctx, h, sc, packTries, still)' \
  'packSkips, err := s.installTemplatePacks(ctx, h, sc, packTries, still)' \
  ./internal/agent '^TestTemplateVoiceChatTriedAgainGetsItsPort$'
control "a template whose modpack is made for another Minecraft version is blocked" internal/agent/templates.go \
  'case v.MinecraftVersion != "" && v.MinecraftVersion != p.Version.MinecraftVersion:' \
  'case false:' \
  ./internal/agent '^TestTemplateModpackRunsOnTheTypeItNames$'
control "an ask for a share that waited reads the setup again" internal/agent/packshare.go \
  '	fs := &s.shares
	for {
		fs.mu.Lock()
		setup, err := s.shareSetup()' \
  '	fs := &s.shares
	setup, err := s.shareSetup()
	for {
		fs.mu.Lock()' \
  ./internal/agent '^TestFriendsShareKeepsTheNewestBuild$'
control "voice chat's port closes in the transaction that drops its record" internal/agent/addons.go \
  'if err := saveConfig(tx, s.id, *sc); err != nil {' \
  'if err := saveConfig(s.db, s.id, *sc); err != nil {' \
  ./internal/agent '^TestVoiceChatRecordAndPortNeverDisagree$'
control "a removal closes voice chat's port only with its record" internal/agent/addons.go \
  '	target := string(key.Source) + ":" + key.ProjectID
	rm, err := lib.Uninstall(' \
  '	_ = s.closeVoiceChat(actor)
	target := string(key.Source) + ":" + key.ProjectID
	rm, err := lib.Uninstall(' \
  ./internal/agent '^TestVoiceChatRecordAndPortNeverDisagree$'
control "each version list is fetched on its own" internal/agent/software.go \
  'entries, at, err := fetchOnce(ctx, &c.mu, &c.catalogFlights, typ, func() ([]api.CatalogEntry, time.Time, error) {' \
  'entries, at, err := fetchOnce(ctx, &c.mu, &c.catalogFlights, "", func() ([]api.CatalogEntry, time.Time, error) {' \
  ./internal/agent '^TestSlowVersionListHoldsUpOnlyItsOwnCallers$'
control "a caller that waited for a build list gets what the fetch found" internal/agent/software.go \
  '	return bs, at, nil
}' \
  '	_, _ = bs, at
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.builds[key].builds, c.builds[key].at, nil
}' \
  ./internal/agent '^TestBuildListWaitersGetWhatTheFetchFound$'
control "a template whose modpack runs on another type is blocked" internal/agent/templates.go \
  'p.Blockers, p.Ready = append(p.Blockers, *n), false' \
  '_ = n' \
  ./internal/agent '^TestTemplateModpackRunsOnTheTypeItNames$'
control "a backup records voice chat's UDP port" internal/agent/backups.go \
  'm.Settings[manifestVoiceChatPort] = strconv.Itoa(sc.VoiceChatPort)' \
  '_ = sc.VoiceChatPort' \
  ./internal/agent '^TestRestoreKeepsVoiceChatsPort$'
control "a backup records its server's modpack" internal/agent/backups.go \
  '	if v := s.packSetting(sc); v != "" {' \
  '	if v := s.packSetting(sc); false && v != "" {' \
  ./internal/agent '^TestRestoreBringsTheBackupsModpack$'
control "a restore takes the modpack from its backup, not the live server" internal/agent/backups.go \
  '	j.RestoredPack = restoredModpack(&j.Restored, setting, recorded)' \
  '	_, _ = setting, recorded
	j.Restored.Modpack = prev.Modpack' \
  ./internal/agent '^TestRestoreBringsTheBackupsModpack$'
control "a restore swaps the modpack's record with the world" internal/agent/backups.go \
  '	err = s.saveWithPack(j.Restored, j.RestoredPack)' \
  '	err = s.saveServerConfig(j.Restored)' \
  ./internal/agent '^TestRestoreBringsTheBackupsModpack$'
control "an undone restore puts the live server's modpack record back" internal/agent/backups.go \
  'return s.saveWithPack(*j.Previous, j.PreviousPack)' \
  'return s.saveWithPack(*j.Previous, nil)' \
  ./internal/agent '^TestRestoreBringsTheBackupsModpack$'
control "a restored server whose backup doesn't record its modpack says so" internal/agent/modpacks.go \
  'sc.ModpackUnknown = true' \
  'sc.ModpackUnknown = false' \
  ./internal/agent '^TestRestoreBringsTheBackupsModpack$'
control "a backup's modpack record must stay inside the server's folder" internal/agent/modpacks.go \
  'if !filepath.IsLocal(f.Path) {' \
  'if false && !filepath.IsLocal(f.Path) {' \
  ./internal/agent '^TestRestoreBringsTheBackupsModpack$'
control "a restore gives voice chat back its UDP port" internal/agent/backups.go \
  'releasePort, err := s.restoredVoiceChat(&j.Restored, prev, m, st.data)' \
  'releasePort, err := func() {}, error(nil)' \
  ./internal/agent '^TestRestoreKeepsVoiceChatsPort$'
# Wave 9: voice chat that comes with a modpack.
control "a pack's voice chat gets its port only with leave from the pack's plan" internal/agent/curated.go \
  'if !p.OpenPorts || sc.VoiceChatPort > 0 || !voiceChatInPack(pl) {' \
  'if sc.VoiceChatPort > 0 || !voiceChatInPack(pl) {' \
  ./internal/agent '^TestAPacksVoiceChatGetsItsPortOnlyWithLeave$'
control "a pack's plan names the port its voice chat would get" internal/agent/curated.go \
  'if p.voiceChat {' \
  'if false {' \
  ./internal/agent '^TestAPacksPreviewNamesVoiceChatsPort$'
control "voice chat in a CurseForge pack is known by CurseForge's project" internal/agent/curated.go \
  'return c.Project == curseForgeVoiceChat' \
  'return false' \
  ./internal/agent '^TestVoiceChatIsFoundInPacksFromEitherSource$'
control "no port for voice chat in a pack whose server can't load it" internal/agent/curated.go \
  'if _, err := addons.TargetFor(pl.Requirements.Type); err != nil {' \
  'if _, err := addons.TargetFor(pl.Requirements.Type); false && err != nil {' \
  ./internal/agent '^(TestAPacksPreviewNamesVoiceChatsPort|TestVoiceChatIsFoundInPacksFromEitherSource)$'
control "voice chat never gets a port held for another server" internal/agent/curated.go \
  'if holder != id {' \
  'if false && holder != id {' \
  ./internal/agent '^TestVoiceChatPortsAreHeldUntilSaved$'
control "a restore holds voice chat's port until the restored settings are saved" internal/agent/backups.go \
  '	defer releasePort()' \
  '	releasePort()' \
  ./internal/agent '^TestVoiceChatPortsAreHeldUntilSaved$'
control "a restore the agent restarted in holds voice chat's port until the restored settings are saved" internal/agent/recovery.go \
  'p.releasePort = a.voicePorts.hold(j.Restored.VoiceChatPort, s.id)' \
  '_ = j.Restored.VoiceChatPort' \
  ./internal/agent '^TestResumedRestoreHoldsVoiceChatsPort$'
control "a resumed restore frees voice chat's port once it's done" internal/agent/recovery.go \
  '		defer p.releasePort()' \
  '		_ = p.releasePort' \
  ./internal/agent '^TestResumedRestoreHoldsVoiceChatsPort$'
control "voice chat's port opens before voice chat installs" internal/agent/curated.go \
  '	if err := s.setUpVoiceChat(h, sc, srv, actor); err != nil {
		return err
	}
	if err := s.installAddons(ctx, h, actor, run); err != nil {' \
  '	if err := s.installAddons(ctx, h, actor, run); err != nil {
		return err
	}
	if err := s.setUpVoiceChat(h, sc, srv, actor); err != nil {' \
  ./internal/agent '^TestVoiceChatInstallOpensItsPortFirst$'
control "a voice chat install that fails closes the port it opened" internal/agent/curated.go \
  '		if opened {
			if cerr := s.closeVoiceChat(actor); cerr != nil {' \
  '		if false && opened {
			if cerr := s.closeVoiceChat(actor); cerr != nil {' \
  ./internal/agent '^TestVoiceChatInstallOpensItsPortFirst$'
control "a running server restarts to publish voice chat's port" internal/agent/curated.go \
  '	if !running {
		if !start {' \
  '	if true || !running {
		if !start {' \
  ./internal/agent '^TestVoiceChatInstallOpensItsPortFirst$'
control "voice chat installed with start starts a stopped server" internal/agent/curated.go \
  'return s.startNow(ctx, h)' \
  'return nil' \
  ./internal/agent '^TestVoiceChatInstallOpensItsPortFirst$'
control "a setup container still running when its output ends fails" internal/agent/software.go \
  'if c.State.Running {' \
  'if false && c.State.Running {' \
  ./internal/agent '^TestSetupStillRunningWhenItsOutputEndsFails$'
control "server software is written inside the data directory's root" internal/minecraft/software/files.go \
  'f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)' \
  'f, err := os.OpenFile(root.Name()+"/"+tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)' \
  ./internal/minecraft/software '^TestDownload$'
control "template data packs never connect to a private address" internal/templates/fetch.go \
  'if err != nil || !allowed(ap) {' \
  'if false && (err != nil || !allowed(ap)) {' \
  ./internal/templates '^TestPackClientRefusesPrivateAddresses$'
control "template data packs never download through a proxy" internal/templates/fetch.go \
  'Proxy:                 nil,' \
  'Proxy:                 http.ProxyFromEnvironment,' \
  ./internal/templates '^TestPackClientIgnoresProxyVariables$'
control "template data packs download over HTTPS only" internal/templates/fetch.go \
  'if r.URL.Scheme != "https" {' \
  'if false {' \
  ./internal/templates '^TestPackClientRefusesPlainHTTP$'
control "each redirect of a template data pack is checked again" internal/templates/fetch.go \
  'if u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") || !publicHost(u.Hostname()) {' \
  'if false {' \
  ./internal/templates '^TestPackRedirectsAreCheckedAgain$'
control "a template data pack must match the template's checksum" internal/templates/fetch.go \
  'if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {' \
  'if got := hex.EncodeToString(h.Sum(nil)); false && !strings.EqualFold(got, want) {' \
  ./internal/templates '^TestFetchPack$'

# Follow-ups after 0.3.0.
control "an interrupted restore gets the previous world back at start" internal/agent/backups.go \
  'if dirExists(aside) {' \
  'if false && dirExists(aside) {' \
  ./internal/agent '^TestInterruptedRestoreIsSettledAtStart$'
control "an interrupted restore gets the previous settings back at start" internal/agent/backups.go \
  'return s.saveWithPack(*j.Previous, j.PreviousPack)' \
  'return nil' \
  ./internal/agent '^TestInterruptedRestoreIsSettledAtStart$'
control "a restore stage is kept while its swap is not settled" internal/agent/backups.go \
  'if err := a.settleSwap(dir, true); err != nil {' \
  'if err := a.settleSwap(dir, true); false && err != nil {' \
  ./internal/agent '^TestTripleFailedRestoreKeepsItsStageUntilThePreviousWorldIsBack$'
control "a restore preview read again keeps its staged source" internal/agent/backups.go \
  'st.preview.Source, st.preview.ReceivedAt = s.Preview.Source, s.Preview.ReceivedAt' \
  'st.preview.ReceivedAt = s.Preview.ReceivedAt' \
  ./internal/agent '^TestRestorePreviewSaysOnceTheBackupWasMadeHere$'
control "no start recreates a world directory a restore moved aside" internal/agent/lifecycle.go \
  'if m := s.worldMissing(); m != nil {
		return errWorldMissing(m, then)' \
  'if m := s.worldMissing(); false && m != nil {
		return errWorldMissing(m, then)' \
  ./internal/agent '^TestTripleFailedRestoreKeepsItsStageUntilThePreviousWorldIsBack$'
control "a world copy is discarded only by its exact name" internal/agent/backups.go \
  'if !reWorldCopy.MatchString(name) {' \
  'if false && !reWorldCopy.MatchString(name) {' \
  ./internal/agent '^TestWorldCopiesAreListedAndDiscarded$'
control "no world copy is discarded while the live world folder is missing" internal/agent/backups.go \
  'if !dirExists(s.dataDir()) {
		writeError(w, errConflict("The world folder is missing' \
  'if false && !dirExists(s.dataDir()) {
		writeError(w, errConflict("The world folder is missing' \
  ./internal/agent '^TestWorldCopiesAreListedAndDiscarded$'
control "a world a restore would refuse is refused before the server stops" internal/agent/backups.go \
  'case errors.As(err, &refused):
		err = s.withRefusalHint(err)' \
  'case errors.As(err, &refused) && false:
		err = s.withRefusalHint(err)' \
  ./internal/agent '^(TestBackupRefusesAWorldARestoreWouldRefuse|TestBackupRefusesAWholeWorldOverALimitBeforeStopping|TestRestoreAndUpdateRefuseAWorldTheirBackupWouldRefuseBeforeStopping)$'
control "an update refuses such a world before the server stops" internal/agent/versions.go \
  'if err := s.archiveRefusal("Nothing was changed."); err != nil {' \
  'if err := s.archiveRefusal("Nothing was changed."); false && err != nil {' \
  ./internal/agent '^TestRestoreAndUpdateRefuseAWorldTheirBackupWouldRefuseBeforeStopping$'
control "the pre-stop check applies the archive limits" internal/backup/archive.go \
  'if err := tally.add(rel, size); err != nil {
			return refusal(rel, err)' \
  'if err := tally.add(rel, size); false && err != nil {
			return refusal(rel, err)' \
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
  'if s.stopping() {
			h.continues = true' \
  'if false && s.stopping() {
			h.continues = true' \
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
  'if err := s.saveWithPack(j.Restored, j.RestoredPack); err != nil {' \
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
control "a failed lookup of a 0.3.0 restore's Paper build is tried again" internal/agent/recovery.go \
  'for _, wait := range lookupRetries {' \
  'for _, wait := range lookupRetries[:0] {' \
  ./internal/agent '^TestA030RestoreIsFinishedThroughAShortPaperMCOutage$'
control "the lookup tried again asks PaperMC, not the cached failure" internal/agent/recovery.go \
  's.forgetFailedBuild(m.MinecraftVersion, m.PaperBuild)' \
  '_ = m' \
  ./internal/agent '^TestA030RestoreIsFinishedThroughAShortPaperMCOutage$'
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
  local name=$1 file=$2 test=$5 shell=sh
  # A bash script is parsed by bash, a POSIX one by sh.
  case $(head -n1 "$file") in *bash*) shell=bash ;; esac
  FROM=$3 TO=$4 perl -0pi -e 's/\Q$ENV{FROM}\E/$ENV{TO}/ or die "guard not found\n"' "$file"
  if ! "$shell" -n "$file" 2>/dev/null; then
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
# shellcheck disable=SC2016
shcontrol "package.sh builds CURSEFORGE_API_KEY into the binary" scripts/package.sh \
  'ldflags+=" -X $curseforge.BuildKey=$CURSEFORGE_API_KEY"' \
  ':' \
  scripts/package_test.sh
# shellcheck disable=SC2016
shcontrol "the VM rehearsal's KVM check gives up on a KVM that hangs" scripts/e2e/vm-rehearsal.sh \
  'timeout --kill-after=10 "$limit" python3' \
  'python3' \
  scripts/e2e/vm-rehearsal_test.sh
# shellcheck disable=SC2016
shcontrol "the VM rehearsal keeps evidence without setup codes" scripts/e2e/vm-rehearsal.sh \
  ' || [ $? = 1 ]; }' \
  '; }' \
  scripts/e2e/vm-rehearsal_test.sh

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
  'cfg.HTTP = &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: noRedirects,' \
  'cfg.HTTP = &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: nil,' \
  ./internal/names/service '^TestTheServiceFollowsNoRedirects$'
control "names claimed from one network are limited" internal/names/service/handlers.go \
  'if n < s.cfg.MaxNamesPerNetwork {' \
  'if n <= s.cfg.MaxNamesPerNetwork {' \
  ./internal/names/service '^TestNamesPerNetworkAreLimited$'
control "a name that moves counts against the network it moves to" internal/names/service/handlers.go \
  'nw := nameNetwork(was, v4, v6)' \
  'nw := was' \
  ./internal/names/service '^TestNamesPerNetworkFollowTheirAddress$'
control "a move into a full network is refused" internal/names/service/handlers.go \
  'if nw != was {' \
  'if false {' \
  ./internal/names/service '^TestNamesPerNetworkFollowTheirAddress$'
control "the network a name moves to is stored" internal/names/service/handlers.go \
  'v4, v6, nw, names.StateActive, now, bump,' \
  'v4, v6, row.Network, names.StateActive, now, bump,' \
  ./internal/names/service '^TestNamesPerNetworkFollowTheirAddress$'
control "a name from before networks were recorded counts against that of its address" internal/names/service/handlers.go \
  'if was == "" {' \
  'if false {' \
  ./internal/names/service '^TestNamesFromBeforeNetworksGetOne$'
control "a name counting in an IPv6 network keeps it when it gets an IPv4 address" internal/names/service/addr.go \
  'err4 != nil || strings.Contains(current, ":")' \
  'err4 != nil || false' \
  ./internal/names/service '^TestANameCountsAgainstTheNetworkOfOneAddress$'
control "a name counting in an IPv4 network keeps it when it gets an IPv6 address" internal/names/service/addr.go \
  'err4 != nil || strings.Contains(current, ":")' \
  'err4 != nil || true' \
  ./internal/names/service '^TestANameCountsAgainstTheNetworkOfOneAddress$'
control "a name that loses its address in one IP version counts against the other" internal/names/service/addr.go \
  'err4 != nil || strings.Contains(current, ":")' \
  'strings.Contains(current, ":")' \
  ./internal/names/service '^TestANameCountsAgainstTheNetworkOfOneAddress$'
control "two names can't both take a network's last place" internal/names/service/handlers.go \
  'if err := s.writeTx(r.Context(), func(q queryer) error {
		return s.setAddress(r.Context(), q, row, c.addr, req.ClearOther, answered)
	}); err != nil {' \
  'if err := func(q queryer) error {
		return s.setAddress(r.Context(), q, row, c.addr, req.ClearOther, answered)
	}(s.db); err != nil {' \
  ./internal/names/service '^TestConcurrentMovesTakeANetworksLastPlaceOnce$' 20
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
control "an alert the webhook hangs up on is logged" internal/names/service/alert.go \
  'a.log.Warn("Could not send an alert to "+EnvAlertWebhook, "error", err)' \
  '_ = err' \
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
control "second step: a request that finds the sign-in passed checks no code" internal/panel/twofactor.go \
  'if n, err := res.RowsAffected(); err != nil || n == 1 {' \
  'if n, err := res.RowsAffected(); err != nil || n >= 0 {' \
  ./internal/panel '^TestConcurrentRightCodesSpendOneCode$'
control "second step: the sign-in is claimed in the transaction that checks the code" internal/panel/twofactor.go \
  '	if s.beforeCodeCheck != nil {
		s.beforeCodeCheck()
	}
	after, err := s.changeFactorWith(p.User.ID, p.IDHash, func(ctx context.Context, q querier) error {
		return usePendingAttempt(ctx, q, p.IDHash)
	}, func(' \
  '	claimed := usePendingAttempt(context.Background(), s.db, p.IDHash)
	if s.beforeCodeCheck != nil {
		s.beforeCodeCheck()
	}
	after, err := s.changeFactorWith(p.User.ID, p.IDHash, func(context.Context, querier) error {
		return claimed
	}, func(' \
  ./internal/panel '^TestConcurrentRightCodesSpendOneCode$'
control "second step: the request that passes ends the pending sign-in" internal/panel/twofactor.go \
  'DELETE FROM pending_logins WHERE id_hash = ?`, p.IDHash)' \
  'DELETE FROM pending_logins WHERE 0 AND id_hash = ?`, p.IDHash)' \
  ./internal/panel '^TestConcurrentRightCodesSpendOneCode$'
control "second step: a session that cannot be stored undoes the code check" internal/panel/twofactor.go \
  'if err := passed(ctx, conn); err != nil {' \
  'if err := passed(ctx, conn); false && err != nil {' \
  ./internal/panel '^TestASessionThatCannotStartSpendsNoCode$'
control "second step: a wrong code keeps the pending sign-in" internal/panel/twofactor.go \
  'if stepErr == nil && passed != nil {' \
  'if passed != nil {' \
  ./internal/panel '^TestOnePasswordBuysTenCodes$'

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
  'if (st.Free.ServersWait != "" || st.Free.ServersFailed > 0) && !now.Before(st.Free.ServersRetry) {' \
  'if st.Free.ServersFailed > 0 && !now.Before(st.Free.ServersRetry) || st.Free.ServersWait != "" {' \
  ./internal/agent '^TestServerAddressesWaitForTheNamesService$'
control "resource pack links: HTTPS only with a certificate players' games trust" internal/certs/store.go \
  'if _, err := e.cert.Leaf.Verify(opts); err != nil {' \
  'if _, err := e.cert.Leaf.Verify(opts); err != nil && at.IsZero() {' \
  ./internal/certs '^TestStoreTrusted$'
control "resource pack links: a look at the certificates in progress doesn't hide a new one" internal/certs/store.go \
  'if every < 0 {' \
  'if every < 0 && false {' \
  ./internal/certs '^TestStoreCanLookEveryTime$'
control "certificate issuance: a long Retry-After waits no longer than maxPollWait" internal/certs/acme.go \
  'if resp.StatusCode < 300 && retryAfter(' \
  'if false && retryAfter(' \
  ./internal/certs '^TestIssueWaitsForTheCertificate$'
control "certificate issuance: a look at the order that gets no answer is tried again" internal/certs/acme.go \
  'case ctx.Err() != nil || !unreachable(err):' \
  'case true:' \
  ./internal/certs '^TestIssueWaitsForTheCertificate$'
control "certificate issuance: running out of time waiting for the certificate is a timeout, not a refusal" internal/certs/acme.go \
  'return nil, newProblem(err, CodeIssuanceTimeout, nil)' \
  'return nil, explain(err, s, is.now())' \
  ./internal/certs '^TestIssueTimesOutWaitingForTheCertificate$'
control "certificate issuance: the finalize request waits validationWait at most for the certificate" internal/certs/acme.go \
  'c.CreateOrderCert(wctx, ready.FinalizeURL, csr, true)' \
  'c.CreateOrderCert(ctx, ready.FinalizeURL, csr, true)' \
  ./internal/certs '^TestIssueTimesOutWaitingForTheCertificate$'
control "certificate issuance: a finalize request that times out still gets the certificate issued just after" internal/certs/acme.go \
  'issued, ferr := is.fetch(ctx, c, kept, order.URI, s)' \
  'issued, ferr := is.fetch(wctx, c, kept, order.URI, s)' \
  ./internal/certs '^TestIssueKeepsTheOrder$/^(a_slow_finalize_answer:_the_certificate_issued_meanwhile_is_fetched|the_wait_of_the_finalize_request_runs_out:_the_certificate_issued_meanwhile_is_fetched)$'
control "certificate issuance: a finalize request that fails without a problem looks for the certificate" internal/certs/acme.go \
  'if err != nil && !errors.As(err, &ae) && !errors.As(err, &oe) {' \
  'if false && !errors.As(err, &ae) && !errors.As(err, &oe) {' \
  ./internal/certs '^TestIssueKeepsTheOrder$/^a_slow_finalize_answer:_the_certificate_issued_meanwhile_is_fetched$'
control "certificate issuance: the next attempt resumes the kept order rather than making a new one" internal/certs/acme.go \
  'order, key, err := is.resume(ctx, c, kept, names, s)' \
  'order, key, err := (*acme.Order)(nil), (*ecdsa.PrivateKey)(nil), error(nil)' \
  ./internal/certs '^TestIssueKeepsTheOrder$/^(a_slow_finalize_answer_and_a_certificate_issued_later:_the_next_attempt_fetches_it|the_finalize_answer_is_lost_before_issuing:_the_next_attempt_finalizes_the_same_order)$'
control "certificate issuance: the order is kept before it is finalized" internal/certs/acme.go \
  'if err := kept.keep(order.URI, ready.Expires, key); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/certs '^TestIssueKeepsTheOrder$/^the_finalize_answer_is_lost_before_issuing:_the_next_attempt_finalizes_the_same_order$'
control "certificate issuance: an order that can't be kept is not finalized" internal/certs/acme.go \
  '		if err := kept.keep(order.URI, ready.Expires, key); err != nil {
			return nil, nil, newProblem(err, CodeSaveFailed, nil)
		}' \
  '		kept.keep(order.URI, ready.Expires, key)' \
  ./internal/certs '^TestIssueKeepsTheOrder$/^keeping_the_order_fails:_it_is_not_finalized$'
control "certificate issuance: the kept order is dropped once the certificate is saved" internal/certs/acme.go \
  '		return nil, newProblem(err, CodeSaveFailed, nil)
	}
	kept.drop()' \
  '		return nil, newProblem(err, CodeSaveFailed, nil)
	}' \
  ./internal/certs '^TestIssueKeepsTheOrder$/^the_finalize_answer_is_lost_before_issuing:_the_next_attempt_finalizes_the_same_order$'
control "certificate issuance: a bad certificate drops the kept order" internal/certs/acme.go \
  '		kept.drop()
		return nil, newProblem(err, CodeBadCertificate, nil)' \
  '		return nil, newProblem(err, CodeBadCertificate, nil)' \
  ./internal/certs '^TestIssueBadChain$'
control "certificate issuance: a finalized kept order gives its certificate without another finalize request" internal/certs/acme.go \
  'if order != nil && (order.Status == acme.StatusProcessing || order.Status == acme.StatusValid) {' \
  'if false {' \
  ./internal/certs '^TestIssueKeepsTheOrder$/^saving_the_certificate_fails:_the_next_attempt_fetches_it_again$'
control "certificate issuance: an invalid kept order is dropped for a new one" internal/certs/acme.go \
  'case o.Status == acme.StatusInvalid || !forNames(o, names):' \
  'case !forNames(o, names):' \
  ./internal/certs '^TestIssueKeepsTheOrder$/^the_kept_order_turned_invalid:_the_next_attempt_makes_a_new_one$'
control "certificate issuance: a kept order for other names is dropped for a new one" internal/certs/acme.go \
  'case o.Status == acme.StatusInvalid || !forNames(o, names):' \
  'case o.Status == acme.StatusInvalid:' \
  ./internal/certs '^TestIssueKeepsTheOrder$/^the_kept_order_is_for_other_names:_the_next_attempt_makes_a_new_one$'
control "certificate issuance: an expired kept order is dropped for a new one" internal/certs/acme.go \
  'if err != nil || (!saved.Expires.IsZero() && !is.now().Before(saved.Expires)) {' \
  'if err != nil {' \
  ./internal/certs '^TestIssueKeepsTheOrder$/^the_kept_order_expired:_the_next_attempt_makes_a_new_one$'
control "certificate issuance: a kept order the client refuses to look at is dropped for a new one" internal/certs/acme.go \
  'case errors.As(err, &guard) || refused(err):' \
  'case false && errors.As(err, &guard) || refused(err):' \
  ./internal/certs '^TestIssueKeepsTheOrder$/^the_kept_order_is_at_another_certificate_authority:_a_new_one_is_made$'
control "certificate issuance: a kept order the certificate authority refuses is dropped for a new one" internal/certs/acme.go \
  'case errors.As(err, &guard) || refused(err):' \
  'case errors.As(err, &guard):' \
  ./internal/certs '^TestIssueKeepsTheOrder$/^the_certificate_authority_no_longer_knows_the_kept_order:_the_next_attempt_makes_a_new_one$'
control "certificate issuance: the kept order outlasts a certificate authority that is unavailable" internal/certs/acme.go \
  'case CodeRateLimited, CodePaused, CodeCAUnavailable:' \
  'case CodeRateLimited, CodePaused:' \
  ./internal/certs '^TestIssueKeepsTheOrder$/^the_certificate_authority_is_unavailable:_the_order_is_kept_for_the_attempt_after$'
control "certificate issuance: the kept order outlasts a rate limit" internal/certs/acme.go \
  'case CodeRateLimited, CodePaused, CodeCAUnavailable:' \
  'case CodePaused, CodeCAUnavailable:' \
  ./internal/certs '^TestIssueKeepsTheOrder$/^a_rate_limit_at_the_certificate_authority:_the_order_is_kept_for_the_attempt_after$'
control "certificate issuance: a refused finalize request drops the order" internal/certs/acme.go \
  '		return nil, nil, is.orderFailed(err, kept, s)
	}
	return der, key, nil' \
  '		return nil, nil, explain(err, s, is.now())
	}
	return der, key, nil' \
  ./internal/certs '^TestIssueKeepsTheOrder$/^the_certificate_authority_refuses_the_finalize_request:_the_next_attempt_makes_a_new_order$'
control "certificate issuance: a finalize request that runs out of time and was never carried out is a timeout" internal/certs/acme.go \
  '		if ctx.Err() == nil && errors.Is(wctx.Err(), context.DeadlineExceeded) {
			return nil, nil, newProblem(err, CodeIssuanceTimeout, nil)' \
  '		if false {
			return nil, nil, newProblem(err, CodeIssuanceTimeout, nil)' \
  ./internal/certs '^TestIssueKeepsTheOrder$/^a_slow_finalize_request_that_is_never_carried_out:_the_next_attempt_finalizes_the_same_order$'
control "certificates: Forget deletes the kept order with the certificate" internal/certs/files.go \
  'for _, file := range []string{n + ".pem", n + orderSuffix} {' \
  'for _, file := range []string{n + ".pem"} {' \
  ./internal/certs '^TestForget$'
control "a name the machine stops using loses its kept certificate order" internal/agent/certificates.go \
  'if err := certs.Forget(a.cfg.CertsDir(), name); err != nil {' \
  'if err := os.Remove(filepath.Join(a.cfg.CertsDir(), name+".pem")); err != nil {' \
  ./internal/agent '^TestOwnDomainChecksTheNameBeforeHTTP01$'
control "DNS-01: a record the names service stored but has not published is waited for" internal/certs/dns01.go \
  'err != nil && !pending(err) {' \
  'err != nil {' \
  ./internal/certs '^TestDNS01WaitsForAChallengeTheNamesServiceStored$'
control "DNS-01: only a record that is pending is waited for after SetTXT fails" internal/certs/dns01.go \
  'return errors.As(err, &p) && p.Pending()' \
  'return errors.As(err, &p)' \
  ./internal/certs '^TestDNS01WaitsForAChallengeTheNamesServiceStored$/^refused$'
control "names client: a challenge Cloudflare has not published yet is pending" internal/names/errors.go \
  'func (e *Error) Pending() bool { return e.Code == CodeDNSPending }' \
  'func (e *Error) Pending() bool { return false }' \
  ./internal/certs '^TestDNS01WaitsForAChallengeTheNamesServiceStored$'
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
control "names client: an answer is awaited longer than the service waits for Cloudflare" internal/names/client.go \
  'ResponseHeaderTimeout: answerWait,' \
  'ResponseHeaderTimeout: 20 * time.Second,' \
  ./internal/names '^TestAnswersAreAwaitedLongerThanTheServiceWaitsForCloudflare$'
control "names client: a request is not cut short while its answer is awaited" internal/names/client.go \
  'Timeout:       answerWait + 30*time.Second,' \
  'Timeout:       30 * time.Second,' \
  ./internal/names '^TestAnswersAreAwaitedLongerThanTheServiceWaitsForCloudflare$'
control "free address change: undone at the names service when it can't be saved" internal/agent/address.go \
  'if _, rerr := c.Release(uctx); rerr != nil && !namesCode(rerr, names.CodeNotClaimed) {' \
  'if rerr := error(nil); rerr != nil {' \
  ./internal/agent '^TestFreeAddressChangeAndRelease$'
control "free address change: the old name is claimed back only once the new one is released" internal/agent/address.go \
  'if _, rerr := c.Release(uctx); rerr != nil && !namesCode(rerr, names.CodeNotClaimed) {' \
  'if _, rerr := c.Release(uctx); false && rerr != nil {' \
  ./internal/agent '^TestFreeAddressChangeAndRelease$'
control "free address change: a claim without a clear answer is looked up at the names service" internal/agent/address.go \
  'if maybeStored(err) {
			l, listed, lerr := listedName(sctx, c, name)' \
  'if false {
			l, listed, lerr := listedName(sctx, c, name)' \
  ./internal/agent '^TestFreeNameChangeWithALostAnswerFollowsTheService$'
control "free address change: a 5xx doesn't say the claim wasn't stored" internal/agent/address.go \
  'return !errors.As(err, &ne) || ne.Status >= 500' \
  'return !errors.As(err, &ne)' \
  ./internal/agent '^TestFreeNameChangeWithALostAnswerFollowsTheService$'
control "free address change: the new name is taken only when the service lists it" internal/agent/address.go \
  'listed && l.State != names.StateReleased' \
  '(listed || true) && l.State != names.StateReleased' \
  ./internal/agent '^TestFreeNameChangeThatFailedGivesTheOldNameBack$'
control "free address change: a new name the service lists as released is not taken" internal/agent/address.go \
  'listed && l.State != names.StateReleased' \
  'listed' \
  ./internal/agent '^TestFreeNameChangeThatFailedGivesTheOldNameBack$'
control "free address change: the old name claimed back gets its servers' records again" internal/agent/address.go \
  '	a.serversChanged()
' \
  '' \
  ./internal/agent '^TestFreeNameChangeThatFailedGivesTheOldNameBack$'
control "free address change: the old name is kept while the service holds it" internal/agent/address.go \
  'if listed || !takenElsewhere(err) {' \
  'if !takenElsewhere(err) {' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^alex_is_refused_as_held_elsewhere_but_listed_for_this_machine$'
control "free address change: the old name is kept while the service can't say" internal/agent/address.go \
  'if lerr != nil {
		_ = a.namesError(lerr)' \
  'if lerr != nil && false {
		_ = a.namesError(lerr)' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^another_install_has_alex,_the_service_holds_bob_and_the_list_fails$'
control "free address change: the old name is given up once the service no longer has it" internal/agent/address.go \
  'if listed || !takenElsewhere(err) {' \
  'if true {' \
  ./internal/agent '^TestFreeNameIsKeptWhileTheServiceHoldsIt$'
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
control "free address: a server change the claim's publish covered doesn't make the loop ask again early" internal/agent/address.go \
  'a.takeServersChanged()
	want := freeServers(a.joinServers())' \
  'want := freeServers(a.joinServers())' \
  ./internal/agent '^TestServerAddressesCoveredByThePublishAreNotAskedAgain$'
control "server addresses that failed are asked for again" internal/agent/address.go \
  'if (st.Free.ServersWait != "" || st.Free.ServersFailed > 0) && !now.Before(st.Free.ServersRetry) {' \
  'if st.Free.ServersWait != "" && !now.Before(st.Free.ServersRetry) {' \
  ./internal/agent '^TestServerAddressesThatFailedAreAskedForAgain$'
control "server addresses that failed are asked for again only when due" internal/agent/address.go \
  'if (st.Free.ServersWait != "" || st.Free.ServersFailed > 0) && !now.Before(st.Free.ServersRetry) {' \
  'if st.Free.ServersWait != "" && !now.Before(st.Free.ServersRetry) || st.Free.ServersFailed > 0 {' \
  ./internal/agent '^TestServerAddressesThatFailedAreAskedForAgain$'
control "server addresses that failed: a claim's publish doesn't wait for them" internal/agent/address.go \
  '!st.Free.serverPublished(s) && st.Free.ServersWait == "" && st.Free.ServersFailed == 0' \
  '!st.Free.serverPublished(s) && st.Free.ServersWait == ""' \
  ./internal/agent '^TestServerAddressesThatFailedAreAskedForAgain$'
control "server addresses that failed: a failed update is recorded" internal/agent/address.go \
  'a.saveServersSync(wait, from, len(errs) > 0)' \
  'a.saveServersSync(wait, from, false)' \
  ./internal/agent '^TestServerAddressesFollowEveryAnswerOfTheNamesService$/^the_record_can.t_be_given$'
control "server addresses that failed: a names key that can't be read is a failure" internal/agent/address.go \
  '		a.saveServersSync("", time.Time{}, true)
' \
  '' \
  ./internal/agent '^TestServerAddressesFollowEveryAnswerOfTheNamesService$/^the_machine.s_key_can.t_be_read$'
control "server addresses that failed: nothing left to update ends the retries" internal/agent/address.go \
  '		a.saveServersSync("", time.Time{}, false)
' \
  '' \
  ./internal/agent '^TestServerAddressesFollowEveryAnswerOfTheNamesService$/^nothing_to_update_after_failures$'
control "server addresses that failed: a first failure is recorded" internal/agent/address.go \
  '(wait == "" && !failed && st.Free.ServersWait == "" && st.Free.ServersFailed == 0)' \
  '(wait == "" && st.Free.ServersWait == "" && st.Free.ServersFailed == 0)' \
  ./internal/agent '^TestServerAddressesFollowEveryAnswerOfTheNamesService$/^the_record_can.t_be_given$'
control "server addresses that failed: an update that works ends the retries" internal/agent/address.go \
  '(wait == "" && !failed && st.Free.ServersWait == "" && st.Free.ServersFailed == 0)' \
  '(wait == "" && !failed && st.Free.ServersWait == "")' \
  ./internal/agent '^TestServerAddressesFollowEveryAnswerOfTheNamesService$/^the_record_is_given_after_failures$'
control "server addresses that failed: failures in a row are counted" internal/agent/address.go \
  'f.ServersFailed = st.Free.ServersFailed + 1' \
  'f.ServersFailed = 1' \
  ./internal/agent '^TestServerAddressesFollowEveryAnswerOfTheNamesService$/^the_record_can.t_be_given_a_second_time$'
control "server addresses that failed: asked for less often while they keep failing" internal/agent/address.go \
  'd *= 2' \
  'd *= 1' \
  ./internal/agent '^TestServerAddressesFollowEveryAnswerOfTheNamesService$/^the_record_can.t_be_given_a_second_time$'
control "server addresses that failed: asked for at least hourly" internal/agent/address.go \
  'if d >= freeRetryEvery {' \
  'if false {' \
  ./internal/agent '^TestServerAddressesFollowEveryAnswerOfTheNamesService$/^the_record_can.t_be_given_a_fifth_time$'
control "server addresses that failed: a failure doesn't end the service's wait" internal/agent/address.go \
  'case !failed:' \
  'case true:' \
  ./internal/agent '^TestServerAddressesFollowEveryAnswerOfTheNamesService$/^the_record_can.t_be_given_during_the_service.s_wait$'
control "server addresses that failed: a failure doesn't bring the service's wait forward" internal/agent/address.go \
  '; retry.After(f.ServersRetry) {' \
  '; true {' \
  ./internal/agent '^TestServerAddressesFollowEveryAnswerOfTheNamesService$/^the_record_can.t_be_given_during_the_service.s_wait$'
control "free address change: a release without a clear answer is undone with time of its own" internal/agent/address.go \
  'rctx, cancel := context.WithTimeout(a.ctx, settleWait)' \
  'rctx, cancel := context.WithTimeout(ctx, settleWait)' \
  ./internal/agent '^TestFreeNameChangeIsUndoneAfterItsTimeRanOut$/^the_release_is_answered_too_late$'
control "free address change: a claim without a clear answer is looked up with time of its own" internal/agent/address.go \
  'sctx, cancel := context.WithTimeout(a.ctx, settleWait)' \
  'sctx, cancel := context.WithTimeout(ctx, settleWait)' \
  ./internal/agent '^TestFreeNameChangeIsUndoneAfterItsTimeRanOut$/^the_claim_is_answered_too_late_and_the_change_can.t_be_saved$'
control "free address change: a change that can't be saved is undone with time of its own" internal/agent/address.go \
  'uctx, cancel := context.WithTimeout(a.ctx, settleWait)' \
  'uctx, cancel := context.WithTimeout(ctx, settleWait)' \
  ./internal/agent '^TestFreeNameChangeIsUndoneAfterItsTimeRanOut$/^the_claim_is_answered_too_late_and_the_change_can.t_be_saved$'
control "free address change: a release without a clear answer claims the old name back" internal/agent/address.go \
  'if maybeStored(err) {
				rctx' \
  'if false {
				rctx' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^the_release_is_carried_out_but_its_answer_is_lost$'
control "free address change: a name the service doesn't hold for the key needs no release" internal/agent/address.go \
  'err != nil && !namesCode(err, names.CodeNotClaimed) && !namesCode(err, names.CodeNotYourName) {' \
  'err != nil && !namesCode(err, names.CodeNotYourName) {' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^the_service_had_alex_released$'
control "free address change: a name another install has needs no release" internal/agent/address.go \
  'err != nil && !namesCode(err, names.CodeNotClaimed) && !namesCode(err, names.CodeNotYourName) {' \
  'err != nil && !namesCode(err, names.CodeNotClaimed) {' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^another_install_had_taken_alex$'
control "free address change: a new name the service holds after all is the change done" internal/agent/address.go \
  'if a.reclaim(sctx, c, old, actor) == name {' \
  'if a.reclaim(sctx, c, old, actor) == name+"-" {' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^bob_is_claimed_but_the_answer_and_the_first_list_are_lost$'
control "free address: a first name whose claim nothing settles is released again" internal/agent/address.go \
  'unsure = lerr != nil' \
  'unsure = lerr != nil && false' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^a_first_name_claimed_but_the_answer_and_the_list_are_lost$'
control "free address: a first claim refused at the limit takes the name the key holds" internal/agent/address.go \
  'case namesCode(err, names.CodeLimitReached):' \
  'case false:' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^the_service_holds_a_name_this_machine_doesn.t_know_of$'
control "free address: the name the service holds for the key becomes the machine's" internal/agent/address.go \
  'if l.Name != old && !a.adoptFree(l, old, actor) {' \
  'if l.Name != old {' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^alex_was_released_and_the_service_holds_bob$'
control "free address: the name the machine leaves for the key's stays claimable" internal/agent/address.go \
  '	released := old
' \
  '	released := ""
' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^alex_was_released_and_the_service_holds_bob$'
control "free address: a first name taken from the service keeps the released one claimable" internal/agent/address.go \
  '	if released == "" {
		released = was.Released
	}
' \
  '' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^the_service_holds_a_name_this_machine_doesn.t_know_of$'
control "free address: the old name's certificate goes when the machine takes the key's name" internal/agent/address.go \
  '	a.forgetCertificate(was.Host)
' \
  '' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^alex_was_released_and_the_service_holds_bob$'
control "free address: taking the name the key holds is recorded as a claim" internal/agent/address.go \
  '"was", old)
	a.audit(actor, "address.claim", host, "succeeded", "")
' \
  '"was", old)
' \
  ./internal/agent '^TestFreeNameChangeTheMachineCouldNotConfirmEndsOnTheNewName$'
control "free address: the name the key holds gets its servers' records at once" internal/agent/address.go \
  '	a.serversChanged()
	return true
' \
  '	return true
' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^alex_was_released,_the_service_holds_bob_and_bob.s_refresh_fails$'
control "free address: the old name is refreshed within the hour while the service can't say" internal/agent/address.go \
  '_ = a.namesError(lerr)
		a.retryFree(old)' \
  '_ = a.namesError(lerr)
		_ = old' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^alex_can.t_be_claimed_back_and_the_lists_fail$'
control "free address: a name the service still lists is refreshed within the hour" internal/agent/address.go \
  'if listed || !takenElsewhere(err) {
		a.retryFree(old)' \
  'if listed || !takenElsewhere(err) {
		_ = old' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^alex_can.t_be_claimed_back$'
control "free address: only a name another key has is given up" internal/agent/address.go \
  'return namesCode(err, names.CodeNameTaken) || namesCode(err, names.CodeNameHeld) || namesCode(err, names.CodeNotYourName)' \
  'return err != nil' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^alex_was_given_back_to_everyone_and_claiming_it_again_fails$'
control "free address: a name another key took is given up" internal/agent/address.go \
  'return namesCode(err, names.CodeNameTaken) || ' \
  'return ' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^alex_was_given_back_and_another_install_takes_it_meanwhile$'
control "free address: a name another key holds is given up" internal/agent/address.go \
  ' || namesCode(err, names.CodeNameHeld)' \
  '' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^alex_was_given_back_and_another_install_holds_it_meanwhile$'
control "free address: a name the service says isn't the key's is given up" internal/agent/address.go \
  ' || namesCode(err, names.CodeNotYourName)' \
  '' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^another_install_has_alex$'
control "free address: a name claimed back and refreshed is next refreshed a day later" internal/agent/address.go \
  'n, next = r, a.now().UTC().Add(freeRefreshEvery)' \
  'n = r' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^alex,_due_for_a_refresh_soon,_is_claimed_back_and_refreshed$'
control "free address: a refresh that fails asks the service which name the key holds" internal/agent/address.go \
  'if err != nil {
		if held, ok := a.followService(' \
  'if false {
		if held, ok := a.followService(' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^the_refresh_fails_and_the_service_holds_bob$'
control "free address: a refresh without an answer asks the service too" internal/agent/address.go \
  'if err != nil {
		if held, ok := a.followService(' \
  'if err != nil && !maybeStored(err) {
		if held, ok := a.followService(' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^the_refresh_fails_and_the_service_lists_alex$'
control "free address: a claim back that fails is what the service is asked about" internal/agent/address.go \
  'if _, err = c.Claim(ctx, c.Name); err == nil {' \
  'if _, cerr := c.Claim(ctx, c.Name); cerr == nil {' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^alex_was_given_back_and_another_install_takes_it_meanwhile$'
control "free address: the name taken from the service is refreshed at once" internal/agent/address.go \
  '			c.Name = held.Name
			n, err = c.Refresh(ctx)
' \
  '			c.Name = held.Name
' \
  ./internal/agent '^TestFreeNameFollowsTheServiceOnEveryErrorPath$/^alex_was_released_and_the_service_holds_bob$'
control "operations: an address operation stores its end with its audit entry" internal/agent/address.go \
  'a.finishOperation("", "machine", &done)' \
  'a.saveOperation(&done)
		a.audit(actor, kind, "machine", done.Status, done.Error)' \
  ./internal/agent '^TestAFinishedAddressOperationIsAlreadyAudited$'
control "free address: publishing waits only for the servers it synced" internal/agent/address.go \
  'for !freePublished(a.address(), synced) && time.Since(start) < publishWait {' \
  'for !freePublished(a.address(), a.joinServers()) && len(synced) >= 0 && time.Since(start) < publishWait {' \
  ./internal/agent '^TestFreeAddressPublishingSkipsServersAddedMeanwhile$'
# Wave 5: roles and server scopes, the team, invite links and Discord.
control "an account uses only its own servers" internal/panel/workspace.go \
  'case serverID != "" && !a.covers(serverID):' \
  'case false && serverID != "" && !a.covers(serverID):' \
  ./internal/panel '^TestEveryServerRouteChecksTheServer$'
control "machine-wide actions need every server" internal/panel/workspace.go \
  'case machineWide[act] && !a.Servers.All:' \
  'case false && machineWide[act] && !a.Servers.All:' \
  ./internal/panel '^TestMachineWideActionsNeedEveryServer$'
control "the server list shows only the account's servers" internal/panel/workspace.go \
  'if !sess.Access.covers(id) {' \
  'if false && !sess.Access.covers(id) {' \
  ./internal/panel '^TestListsShowOnlyTheAccountsServers$'
control "the single-server view shows only the account's servers" internal/panel/workspace.go \
  'if id, _ := sv["id"].(string); sess.Access.covers(id) {' \
  'if id, _ := sv["id"].(string); id != "" || sess.Access.covers(id) {' \
  ./internal/panel '^TestListsShowOnlyTheAccountsServers$'
control "restore uploads check the server" internal/panel/team.go \
  'if err := permit(sess.Access, act, p.ServerID); err != nil {' \
  'if err := permit(sess.Access, act, p.ServerID); false && err != nil {' \
  ./internal/panel '^TestListsShowOnlyTheAccountsServers$'
control "only owners are added to the workspace at start" internal/panel/workspace.go \
  "'*', ? FROM users WHERE role = ?\`" \
  "'*', ? FROM users WHERE role = ? OR 1\`" \
  ./internal/panel '^TestRemovedMembersStayRemovedAfterARestart$'
control "a membership without servers has none" internal/panel/auth.go \
  "ALTER TABLE project_members ADD COLUMN servers TEXT NOT NULL DEFAULT '';" \
  "ALTER TABLE project_members ADD COLUMN servers TEXT NOT NULL DEFAULT '*';" \
  ./internal/panel '^TestRemovedMembersStayRemovedAfterARestart$'
control "removing a member deletes their account" internal/panel/team.go \
  'DELETE FROM users WHERE id = ? AND role = ?' \
  'DELETE FROM project_members WHERE user_id = ? AND role != ?' \
  ./internal/panel '^TestRemovedMembersStayRemovedAfterARestart$'
control "admin rights wait for a confirmation" internal/panel/workspace.go \
  'a.TwoFactor = a.FactorOn && (!invites.RequiresTwoFactor(a.InstallRole, a.ProjectRole) || factor == adminFactor)' \
  'a.TwoFactor = a.FactorOn' \
  ./internal/panel '^TestAdminRightsWaitForConfirmation$'
control "only an admin of the member's servers confirms" internal/panel/team.go \
  'case !t.Servers.Within(a.Servers):' \
  'case false:' \
  ./internal/panel '^TestAdminRightsWaitForConfirmation$'
control "sign-in lockouts are per account and address" internal/panel/server.go \
  'key := account + "@" + s.addressKey(r)' \
  'key := account' \
  ./internal/panel '^TestFailedSignInsLockOnlyTheirOwnAddress$'
control "guesses from many addresses are slowed per account" internal/panel/server.go \
  'locked = !ready' \
  'locked = false && !ready' \
  ./internal/panel '^TestGuessesFromManyAddressesAreSlowedPerAccount$'
control "one owner per install" internal/panel/auth.go \
  'CREATE UNIQUE INDEX users_one_owner' \
  'CREATE INDEX users_one_owner' \
  ./internal/panel '^TestThereIsOnlyEverOneOwner$'
control "team changes follow the rules" internal/panel/team.go \
  'if err := invites.CanEdit(sess.Access.Account, t.Account, req.Role, req.Servers); err != nil {' \
  'if err := invites.CanEdit(sess.Access.Account, t.Account, req.Role, req.Servers); false && err != nil {' \
  ./internal/panel '^TestTeamChangesFollowTheRules$'
control "team removals follow the rules" internal/panel/team.go \
  'if err := invites.CanRemove(sess.Access.Account, t.Account); err != nil {' \
  'if err := invites.CanRemove(sess.Access.Account, t.Account); false && err != nil {' \
  ./internal/panel '^TestTeamChangesFollowTheRules$'
control "public pages never show a username" internal/panel/join.go \
  'a.Name = ""' \
  '_ = a.Name' \
  ./internal/panel '^(TestFriendInviteLetsFriendsIn|TestTeamInvitesMakeMembers)$'
control "invite codes are kept out of the log" internal/panel/server.go \
  '"path", s.public.logPath(invites.RedactPath(r.URL.Path))' \
  '"path", r.URL.Path' \
  ./internal/panel '^TestPublicInvitePagesKeepCodesSafe$'
control "the join page is never stored" internal/panel/public.go \
  '{prefix: invites.JoinPath + "/", limits: joinPageLimits, ownRefusals: true, handler: join},' \
  '{prefix: invites.JoinPath + "/", limits: joinPageLimits, cache: "private, max-age=60", ownRefusals: true, handler: join},' \
  ./internal/panel '^TestPublicInvitePagesKeepCodesSafe$'
control "the invite pages answer their own refusals" internal/panel/public.go \
  '{prefix: joinCallPrefix, limits: joinCallLimits, ownRefusals: true, handler: join},' \
  '{prefix: joinCallPrefix, limits: joinCallLimits, handler: join},' \
  ./internal/panel '^(TestFriendInviteLetsFriendsIn|TestInvitePagesArePublicAndNothingElse)$'
control "the join page is limited per address by the public group" internal/panel/join.go \
  'joinPageLimits = publicLimits{perMinute: 60,' \
  'joinPageLimits = publicLimits{perMinute: 6000,' \
  ./internal/panel '^TestInvitePagesArePublicAndNothingElse$'
control "the join calls need the same-origin marker" internal/panel/join.go \
  '{"POST", joinCallPrefix + "preview", publicMutation, "", s.hJoinPreview},' \
  '{"POST", joinCallPrefix + "preview", public, "", s.hJoinPreview},' \
  ./internal/panel '^TestPublicInvitePagesKeepCodesSafe$'
control "nothing under the join path is stored" internal/panel/server.go \
  'cache = "no-store"' \
  'cache = "no-cache"' \
  ./internal/panel '^TestPublicInvitePagesKeepCodesSafe$'
control "public invite calls are limited per address" internal/panel/join.go \
  'if err := s.joinGuard.Address(ip); err != nil {' \
  'if err := s.joinGuard.Address(ip); false && err != nil {' \
  ./internal/panel '^TestPublicInvitePagesKeepCodesSafe$'
control "the last use of a link goes to one friend" internal/panel/friends.go \
  'AND (max_uses = 0 OR uses < max_uses)' \
  'AND (max_uses = 0 OR 1)' \
  ./internal/panel '^TestTheLastUseGoesToOneFriend$' 3
control "one join request per player" internal/panel/join.go \
  'if waiting > 0 {' \
  'if false && waiting > 0 {' \
  ./internal/panel '^TestJoinRequestsWaitForAYes$'
control "a role change checks the member's links against their new rights" internal/panel/team.go \
  'after, err := s.access(user{ID: t.UserID, Username: t.Name, Role: t.InstallRole})' \
  'after, err := t, error(nil)' \
  ./internal/panel '^TestRoleChangesTurnOffOnlyTheLinksTheNewRightsForbid$'
control "two-factor changes reach Discord whatever the switches say" internal/discord/alerts.go \
  'return a.Has(k) || k.always()' \
  'return a.Has(k)' \
  ./internal/discord '^TestTwoFactorChangesArePostedWhateverTheSwitches$'
control "the agent checks the member name it posts to Discord" internal/agent/discord.go \
  'if invites.ValidUsername(req.Member) != nil {' \
  'if false && invites.ValidUsername(req.Member) != nil {' \
  ./internal/agent '^TestDiscordNotifyTakesTwoFactorChangesWithEveryAlertOff$'
control "a server's state change reaches the live status message within seconds" internal/discord/notifier.go \
  'case states != n.shownStates:' \
  'case false && states != n.shownStates:' \
  ./internal/discord '^TestStateChangesReachTheStatusMessageWithinSeconds$'
control "a failed let in declines a request whose link was turned off meanwhile" internal/panel/friends.go \
  'SELECT EXISTS (SELECT 1 FROM invites WHERE id = ? AND revoked_at = 0)' \
  'SELECT EXISTS (SELECT 1 FROM invites WHERE id = ?)' \
  ./internal/panel '^TestAFailedApprovePutsBackOnlyTheRequestItLeft$'
control "a failed let in puts back only a request still as it left it" internal/panel/friends.go \
  'address = ?
				WHERE id = ? AND state = '"'"'approved'"'"' AND decided_at = ? AND decided_by = ?' \
  'address = ?
				WHERE id = ?' \
  ./internal/panel '^TestAFailedApprovePutsBackOnlyTheRequestItLeft$'
control "the burst guard on live status updates" internal/discord/notifier.go \
  'due = later(due, later(n.statusAt.Add(n.gap), n.burstEnds()))' \
  'due = later(due, n.statusAt.Add(n.gap))' \
  ./internal/discord '^TestStateChangesStayInsideDiscordsRateLimits$'
control "the agent looks at its servers for Discord as often as it reconciles" internal/agent/discord.go \
  't := time.NewTicker(a.opts.ReconcileInterval)' \
  't := time.NewTicker(a.opts.SampleInterval)' \
  ./internal/agent '^TestDiscordLiveStatusShowsCrashesWithinSeconds$'
control "Discord shows a crash the reconcile loop has yet to count" internal/agent/discord.go \
  'crashed := s.crashed || err == nil && !busy && s.pendingCrash(c)' \
  'crashed := s.crashed || false && err == nil && !busy && s.pendingCrash(c)' \
  ./internal/agent '^TestDiscordShowsAnExitAsTheReconcileLoopWillCountIt$'
control "a clean shutdown the reconcile loop has yet to handle is not a crash" internal/agent/discord.go \
  '&& !s.intentional[c.ID] && !s.stoppedCleanly()' \
  '&& !s.intentional[c.ID]' \
  ./internal/agent '^TestDiscordShowsAnExitAsTheReconcileLoopWillCountIt$'
control "the live status tells a clean stop from a crash as the reconcile loop does" internal/agent/discord.go \
  '&& !s.intentional[c.ID] && !s.stoppedCleanly()' \
  '&& !s.intentional[c.ID] && !s.sawStopping' \
  ./internal/agent '^TestDiscordAlertSequences$/^a_crash_that_logged_a_shutdown,_before_the_reconcile_loop_sees_it$'
control "Discord hears a server come online" internal/agent/collector.go \
  '} else if fresh {' \
  '} else if false && fresh {' \
  ./internal/agent '^TestDiscordOptionalAlertsGoOut$'
control "Discord hears Playkeeper stop a server, restarts too" internal/agent/lifecycle.go \
  '	if h.op.Kind != "sleep" {
		s.alert(discord.Stopped())
	}
	return nil' \
  '	return nil' \
  ./internal/agent '^TestDiscordAlertSequences$/^(a_stop|a_restart|a_scheduled_restart)$/^every_alert$'
control "Discord doesn't hear of a server falling asleep" internal/agent/lifecycle.go \
  '	if h.op.Kind != "sleep" {
		s.alert(discord.Stopped())
	}' \
  '	s.alert(discord.Stopped())' \
  ./internal/agent '^TestDiscordAlertSequences$/^falling_asleep$/^every_alert$'
control "Discord hears a clean stop outside Playkeeper" internal/agent/lifecycle.go \
  '		s.alert(discord.Event{Kind: discord.KindStopped, At: fin})
		s.recordEvent(fin, "server_stopped_externally"' \
  '		s.recordEvent(fin, "server_stopped_externally"' \
  ./internal/agent '^TestDiscordAlertSequences$/^a_clean_stop_outside_Playkeeper$/^every_alert$'
control "a start after failed starts is not a recovery" internal/agent/collector.go \
  'recovered := take && s.runCrashed' \
  'recovered := take && s.crashed' \
  ./internal/agent '^TestDiscordAlertSequences$/^a_start_fails,_then_one_works$'
control "a Discord settings save that leaves out live status keeps it" internal/agent/discord.go \
  'if req.LiveStatus != nil {
		s.LiveStatus = *req.LiveStatus
	}' \
  's.LiveStatus = req.LiveStatus != nil && *req.LiveStatus' \
  ./internal/agent '^TestDiscordLiveStatusIsOnUnlessTurnedOff$'
control "Discord's live status is on until the owner turns it off" internal/discord/settings.go \
  'Settings{Alerts: DefaultAlerts(), LiveStatus: true}' \
  'Settings{Alerts: DefaultAlerts()}' \
  ./internal/agent '^TestDiscordLiveStatusIsOnUnlessTurnedOff$'
control "connecting the same Discord webhook again keeps its live status message" internal/agent/discord.go \
  "status_message_id = CASE WHEN webhook_url = excluded.webhook_url THEN status_message_id ELSE '' END," \
  "status_message_id = ''," \
  ./internal/agent '^TestDiscordReconnectKeepsTheLiveStatusMessageOfTheSameWebhook$'
control "the low disk alert without a server's name is about your servers" internal/discord/alerts.go \
  'runs = "your servers"' \
  'runs = name' \
  ./internal/discord '^TestLowDiskAlertWithAndWithoutAServerName$'
control "Discord takes running out of memory, then Stopping server, for a crash" internal/agent/collector.go \
  's.sawCrash, s.lastError = true, "Java ran out of memory."' \
  's.sawCrash, s.lastError = false, "Java ran out of memory."' \
  ./internal/agent '^TestDiscordAlertSequences$/^out_of_memory,_then_Stopping_server'
control "a Done line delivered again changes nothing" internal/agent/collector.go \
  'take := fresh || !s.runReady' \
  'take := true' \
  ./internal/agent '^TestDiscordAlertSequences$/^a_crash,_its_Done_line_delivered_again,_then_a_restart$'
control "a profile shows a player online only from a fresh sample" internal/agent/profile.go \
  'if s.players != nil && s.fresh(s.players.At) {' \
  'if s.players != nil {' \
  ./internal/agent '^TestProfileShowsOnlineOnlyFromAFreshSample$'
control "Discord hears a manual backup finish" internal/agent/backups.go \
  '	s.alert(discord.BackupSucceeded(vb.SizeBytes))
	s.afterBackup(b)' \
  '	s.afterBackup(b)' \
  ./internal/agent '^TestDiscordOptionalAlertsGoOut$/backup$'
control "Discord hears an automatic backup finish" internal/agent/backups.go \
  '	s.alert(discord.BackupSucceeded(vb.SizeBytes))
	return vb, nil' \
  '	return vb, nil' \
  ./internal/agent '^TestDiscordOptionalAlertsGoOut$/^automatic_backup_before_an_update$'
control "a start that never came up is not a crash loop" internal/agent/lifecycle.go \
  's.alert(discord.StartFailed(err.Error()))' \
  's.alert(discord.Crashed("Playkeeper could not start it: "+err.Error(), false))' \
  ./internal/agent '^TestDiscordStartFailuresAreNotCrashLoops$'
control "a crash of a server meant to be off is not a give-up" internal/agent/lifecycle.go \
  'GaveUp: wanted && !restarting' \
  'GaveUp: !restarting' \
  ./internal/agent '^TestDiscordCrashOfAServerMeantToBeOffIsNoGiveUp$'
control "Discord counts a server's slots before its first sample" internal/agent/discord.go \
  'if st.MaxPlayers == 0 && sc != nil {' \
  'if false && st.MaxPlayers == 0 && sc != nil {' \
  ./internal/agent '^TestDiscordLiveStatusCountsSlotsBeforeTheFirstSample$'
control "a Minecraft update alert goes out once per version for each server" internal/agent/discord.go \
  'WHERE id = ? AND minecraft_update_alerted != ?' \
  'WHERE id = ? AND ? IS NOT NULL' \
  ./internal/agent '^TestMinecraftUpdateAlertGoesOutOncePerVersion$'
control "Minecraft update alerts are about stable versions only" internal/agent/versions.go \
  '|| e.Experimental || !e.Supported ||' \
  '|| !e.Supported ||' \
  ./internal/agent '^TestNewerStableMatchesTheDashboard$'
control "a Minecraft update is one of the server's own type" internal/agent/versions.go \
  'if entryType(e) != typ || e.Experimental' \
  'if false && entryType(e) != typ || e.Experimental' \
  ./internal/agent '^TestNewerStableMatchesTheDashboard$'
control "Minecraft update alerts read each server type's own versions" internal/agent/discord.go \
  'versions, _, _ = a.typeCatalog(ctx, typ)' \
  'versions, _, _ = a.versionCatalog(ctx)' \
  ./internal/agent '^TestMinecraftUpdateAlertsReadEachTypesOwnVersions$'
control "the machine's Docker health needs no server" internal/agent/host.go \
  'a.dockerOK = err == nil' \
  '_ = err == nil' \
  ./internal/agent '^TestDockerHealthNeedsNoServer$'
control "a mod loader keeps more memory outside the heap than Paper" internal/minecraft/catalog.go \
  'overhead = max(overhead, min(base+modOverheadMB*max(mods, 0), budgetMB/2))' \
  'overhead = max(overhead, min(base+modOverheadMB*max(mods, 0), 0))' \
  ./internal/minecraft '^TestHeapForModLoaders$'
control "each mod keeps more memory outside the heap" internal/minecraft/catalog.go \
  'overhead = max(overhead, min(base+modOverheadMB*max(mods, 0), budgetMB/2))' \
  'overhead = max(overhead, min(base+modOverheadMB*0, budgetMB/2))' \
  ./internal/minecraft '^TestHeapForModLoaders$'
control "a start sizes a mod loader's heap for the mods it has" internal/agent/lifecycle.go \
  'if err := s.sizeHeap(&sc); err != nil {' \
  'if false {' \
  ./internal/agent '^TestModLoaderHeapLeavesRoomForItsMods$'
control "the container runs the heap sized for its mods" internal/agent/lifecycle.go \
  '"MEMORY="+strconv.Itoa(heapMB(sc))+"M",' \
  '"MEMORY="+strconv.Itoa(minecraft.HeapMB(sc.MemoryMB))+"M",' \
  ./internal/agent '^TestModLoaderHeapLeavesRoomForItsMods$'
control "a start leaves a running mod loader and its heap alone" internal/agent/lifecycle.go \
  'if !(err == nil && c.State.Running && c.Config.Labels[labelSpec] == hash) {' \
  'if true {' \
  ./internal/agent '^(TestAStartLeavesARunningModLoaderAndItsHeapAlone|TestAStartSizesTheHeapOfEveryContainerItMakes)$'
control "a start sizes the heap of a running mod loader it recreates" internal/agent/lifecycle.go \
  'if !(err == nil && c.State.Running && c.Config.Labels[labelSpec] == hash) {' \
  'if !(err == nil && c.State.Running) {' \
  ./internal/agent '^TestAStartSizesTheHeapOfEveryContainerItMakes$'
control "a save with the same memory budget leaves the heap alone" internal/agent/handlers.go \
  'memoryChanged = true
			sc.MemoryMB, sc.HeapMB = *req.MemoryMB, minecraft.HeapFor(*req.MemoryMB, serverTypeOf(*sc), s.modJars(*sc))
		}' \
  'memoryChanged = true
		}
		sc.MemoryMB, sc.HeapMB = *req.MemoryMB, minecraft.HeapFor(*req.MemoryMB, serverTypeOf(*sc), s.modJars(*sc))' \
  ./internal/agent '^TestASaveWithTheSameMemoryLeavesTheHeapAlone$'
control "memory advice reads a mod loader's heap" internal/diagnose/memory.go \
  'return minecraft.HeapFor(budgetMB, in.ServerType, in.Mods)' \
  'return minecraft.HeapMB(budgetMB)' \
  ./internal/diagnose '^TestAdviseMemory$'
control "a server that came back on its own still says why it crashed" internal/agent/collector.go \
  'if s.crash != nil {
					s.recovered = s.crash
				}' \
  'if false {
					s.recovered = s.crash
				}' \
  ./internal/agent '^TestAMemoryKillIsExplainedAfterTheServerComesBack$'
control "why a server that came back on its own crashed is shown for a day at most" internal/agent/handlers.go \
  'if recovered != nil && running && s.now().Sub(recovered.At) < recoveredFor {' \
  'if recovered != nil && running {' \
  ./internal/agent '^TestAMemoryKillIsExplainedAfterTheServerComesBack$'
control "giving a server more memory drops why it crashed" internal/agent/handlers.go \
  'if memoryChanged {
		s.mu.Lock()
		s.recovered = nil' \
  'if false && memoryChanged {
		s.mu.Lock()
		s.recovered = nil' \
  ./internal/agent '^TestAMemoryKillIsExplainedAfterTheServerComesBack$'
control "the activity says a server ran out of memory" internal/agent/analytics.go \
  'e.Kind = "crashed_memory"' \
  'e.Kind = "crashed"' \
  ./internal/agent '^TestAMemoryKillIsExplainedAfterTheServerComesBack$'
# Wave 9: Java running out of memory, as Wave 3's 5f3515a makes a crash, is a crash for memory.
control "a run that logged Java's out-of-memory line crashed for memory" internal/agent/lifecycle.go \
  'case s.sawOOM:
		s.lastError = heapCrash' \
  'case false:
		s.lastError = heapCrash' \
  ./internal/agent '^TestJavaRunningOutOfMemoryIsAMemoryCrash$'
control "the activity says Java ran out of memory" internal/agent/analytics.go \
  '(strings.HasPrefix(e.Detail, oomCrash) || strings.HasPrefix(e.Detail, heapCrash))' \
  'strings.HasPrefix(e.Detail, oomCrash)' \
  ./internal/agent '^TestJavaRunningOutOfMemoryIsAMemoryCrash$'
webcontrol "the console reads Vanilla, Fabric, Quilt and NeoForge lines" web/src/lib/console.ts \
  'const m = reServer.exec(raw) ?? reThread.exec(raw)' \
  'const m = reServer.exec(raw)' \
  web/src/lib/lib.test.ts 'not Paper'
webcontrol "a create that never started can be deleted from its card" web/src/pages/server/overview.tsx \
  'onClick={() => setDeleting(true)}' \
  'onClick={() => setDeleting(false)}' \
  web/src/pages/pages.test.tsx 'create never started'
webcontrol "a mod loader suits fewer players at the same memory" web/src/lib/memory.ts \
  'const mb = memoryMB - (moddedMB[type] ?? 0)' \
  'const mb = memoryMB' \
  web/src/lib/lib.test.ts 'fewer players on a mod loader'
webcontrol "the dashboard gives a mod loader the heap the agent gives it" web/src/components/app/create.tsx \
  'if (base !== undefined) overhead =' \
  'if (base === -1) overhead =' \
  web/src/lib/lib.test.ts 'how much of it Java gets'
webcontrol "the memory step counts for the type and mods the new server runs" web/src/pages/new-server.tsx \
  '<MemoryReadout memoryMB={c.memoryMB} sizing={catalog?.sizing} type={runsType} mods={runsMods}' \
  '<MemoryReadout memoryMB={c.memoryMB} sizing={catalog?.sizing}' \
  web/src/pages/pages.test.tsx 'memory for its type and mods'
webcontrol "Settings › Memory counts friends for the server's type" web/src/pages/server/settings.tsx \
  '{memoryAdviceLine(advice, machineName, catalog?.sizing, s.type)}' \
  '{memoryAdviceLine(advice, machineName, catalog?.sizing)}' \
  web/src/pages/pages.test.tsx 'fewer friends for a mod loader'
webcontrol "the Overview says a server that came back on its own had run out of memory" web/src/pages/server/overview.tsx \
  'const recovered = s.recoveredCrash' \
  'const recovered = s.crash' \
  web/src/pages/pages.test.tsx 'had run out of memory'
webcontrol "more memory from the Overview restarts the server to use it" web/src/components/app/notices.tsx \
  '{ ...plan.body, ...(restart ? { restart: true } : {}) }' \
  '{ ...plan.body }' \
  web/src/pages/pages.test.tsx 'had run out of memory'
webcontrol "the activity says a server ran out of memory" web/src/components/app/activity.tsx \
  "return t('activity.crashedMemory', { server })" \
  "return t('activity.crashed', { server })" \
  web/src/lib/lib.test.ts 'when a server ran out of memory'

# Wave 6: the shared map's link token and its players switch, and the game
# files a world import and the shared map read.
control "shared map link tokens carry at least 128 bits" internal/webmap/share.go \
  'const ShareTokenLen = 22' \
  'const ShareTokenLen = 12' \
  ./internal/webmap '^TestShareTokensAreUnguessable$'
control "every link token character is equally likely" internal/webmap/share.go \
  'if b < 248 && len(token) < ShareTokenLen {' \
  'if len(token) < ShareTokenLen {' \
  ./internal/webmap '^TestShareTokensAreUnguessable$'
control "a shared map needs its sharing switch" internal/agent/maps.go \
  'if err != nil || rec == nil || !rec.public {' \
  'if err != nil || rec == nil {' \
  ./internal/agent '^TestSharedMapAnswersOnlyWhileItsSwitchIsOn$'
control "a new link token each time sharing is switched on" internal/agent/maps.go \
  "WHEN ?1 = 1 AND (public = 0 OR share_token = '') THEN ?2" \
  "WHEN ?1 = 1 AND share_token = '' THEN ?2" \
  ./internal/agent '^TestSharedMapAnswersOnlyWhileItsSwitchIsOn$'
control "the shared map's link token is compared" internal/agent/maps.go \
  'if rows.Scan(&sid, &stored) == nil && webmap.ShareTokenMatches(stored, token) {' \
  'if rows.Scan(&sid, &stored) == nil {' \
  ./internal/agent '^TestSharedMapAnswersOnlyWhileItsSwitchIsOn$'
control "shared players hidden while their switch is off" internal/agent/maps.go \
  'case rest == "players" && !rec.publicPlayers:' \
  'case rest == "players" && !rec.publicPlayers && false:' \
  ./internal/agent '^TestSharedMapAnswersOnlyWhileItsSwitchIsOn$'
control "the panel checks a link token before asking the agent" internal/panel/maps.go \
  'if !webmap.ValidShareToken(token) {
		return "", nil, false' \
  'if false && !webmap.ValidShareToken(token) {
		return "", nil, false' \
  ./internal/panel '^TestSharedMapAnswersTheSameWhenItIsNotAvailable$'
control "a shared map shows faces only of players it lists" internal/panel/maps.go \
  'if !strings.EqualFold(p.Name, name) {' \
  'if false && !strings.EqualFold(p.Name, name) {' \
  ./internal/panel '^TestSharedMapAnswersTheSameWhenItIsNotAvailable$'
control "the shared map is served by the public route group" internal/panel/public.go \
  '		{prefix: mapPagePrefix, limits: mapPageLimits, handler: s.mapPage()},
		{prefix: mapDataPrefix, limits: mapDataLimits, handler: s.mapData()},' \
  '' \
  ./internal/panel '^(TestOnlyThePublicGroupAnswersWithoutSignIn|TestSharedMapAnswersTheSameWhenItIsNotAvailable)$'
control "per-address shared map rate limit" internal/panel/public.go \
  '{prefix: mapDataPrefix, limits: mapDataLimits, handler: s.mapData()}' \
  '{prefix: mapDataPrefix, limits: publicLimits{perMinute: 1 << 30, open: 1 << 30, read: time.Minute, write: time.Minute}, handler: s.mapData()}' \
  ./internal/panel '^TestSharedMapIsRateLimitedPerAddress$'
control "link tokens stay out of the request log" internal/panel/public.go \
  'return rt.prefix + "…"' \
  'return p' \
  ./internal/panel '^TestSharedMapTokensStayOutOfTheLog$'
control "a public path is cleaned before it is logged" internal/panel/public.go \
  'c := path.Clean("/" + p)' \
  'c := p + path.Ext("")' \
  ./internal/panel '^TestSharedMapTokensStayOutOfTheLog$'
control "a world import reads server.properties without following a link" internal/agent/worldimports.go \
  'b, err := d.ReadProperties()' \
  'b, err := os.ReadFile(filepath.Join(s.dataDir(), "server.properties"))' \
  ./internal/agent '^TestAWorldImportNeverFollowsAPlantedServerProperties$'
control "the shared map reads the server's icon without following a link" internal/agent/maps.go \
  'b, err := s.readIcon()' \
  'b, err := os.ReadFile(s.dataDir() + "/" + iconFile)' \
  ./internal/agent '^TestTheSharedMapsIconIsReadWithoutFollowingLinks$'
control "the Plugins tab lists the map's squaremap as the Map's" internal/agent/addons.go \
  'if e.Installed != nil && isMapAddon(mapRecs, e.Installed.Key()) {' \
  'if false && e.Installed != nil && isMapAddon(mapRecs, e.Installed.Key()) {' \
  ./internal/agent '^TestPluginsTabLeavesTheMapsSquaremapToTheMap$'
control "the Plugins tab never offers to manage the map's squaremap" internal/agent/addons.go \
  'withMap := append(slices.Clone(installed), s.mapAddons(installed)...)
	res, err := s.lib().Scan(r.Context(), srv, withMap, false)' \
  'withMap := installed
	res, err := s.lib().Scan(r.Context(), srv, withMap, false)' \
  ./internal/agent '^TestPluginsTabLeavesTheMapsSquaremapToTheMap$'
control "the Plugins tab refuses to change what the map installed" internal/agent/maps.go \
  'if slices.Contains(keys, a.Key()) {' \
  'if false && slices.Contains(keys, a.Key()) {' \
  ./internal/agent '^TestPluginsTabLeavesTheMapsSquaremapToTheMap$'
control "a map whose squaremap is gone counts as off and turns on again" internal/agent/maps.go \
  'if fi, err := root.Lstat(l.Folder + "/" + a.FileName); err != nil || !fi.Mode().IsRegular() {' \
  'if fi, err := root.Lstat(l.Folder + "/" + a.FileName); false && (err != nil || !fi.Mode().IsRegular()) {' \
  ./internal/agent '^TestTurningOnTheMapInstallsAMissingSquaremapAgain$'
control "a start saves the world as uploaded before upgrading it" internal/agent/lifecycle.go \
  'if err := s.ensureOriginalSaved(h, sc); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/agent '^TestAnUpgradedWorldWaitsForTheCopyOfItAsUploaded$'
control "announces take turns with the upload allowance" internal/agent/worldimports.go \
  'a.imports.announce.Lock()
	defer a.imports.announce.Unlock()' \
  '' \
  ./internal/agent '^TestAnnouncesTakeTurnsWithTheUploadAllowance$'
control "the shared map's link waits for the own domain to point here" internal/agent/maps.go \
  'if st.Check == nil || !st.Check.Ready {' \
  'if st.Check == nil {' \
  ./internal/agent '^TestSharedMapLinkWaitsForAWorkingName$'
control "the shared map's link waits for the free name to be published" internal/agent/maps.go \
  'st.Free.Name.State != names.StateActive || st.Free.Name.DNS != names.DNSOK {' \
  'st.Free.Name.State != names.StateActive {' \
  ./internal/agent '^TestSharedMapLinkWaitsForAWorkingName$'
control "the shared map's link waits for a certificate that hasn't expired" internal/agent/maps.go \
  'if row == nil || row.status.Certificate == nil || !row.status.Certificate.NotAfter.After(a.now()) {' \
  'if row == nil || row.status.Certificate == nil {' \
  ./internal/agent '^TestSharedMapLinkWaitsForAWorkingName$'
control "an upload for a new server needs rights over every server" internal/panel/server.go \
  'mm("POST", "/api/machines/{mid}/world-imports", "/v1/world-imports", actCreateServers),' \
  'mm("POST", "/api/machines/{mid}/world-imports", "/v1/world-imports", actManageServers),' \
  ./internal/panel '^TestMachineWideActionsNeedEveryServer$'
control "making a server from an upload needs rights over every server" internal/panel/server.go \
  'needSessionCSRF, actCreateServers, s.forwardLong("/v1/world-imports/{imp}/create")' \
  'needSessionCSRF, actManageServers, s.forwardLong("/v1/world-imports/{imp}/create")' \
  ./internal/panel '^TestMachineWideActionsNeedEveryServer$'
control "turning the map on counts what the Mods tab installed as there" internal/agent/maps.go \
  's.lib().Install(ctx, srv, installed, addons.InstallRequest{Source: addons.Source(l.Source), Project: l.ProjectID})' \
  's.lib().Install(ctx, srv, installed[:0], addons.InstallRequest{Source: addons.Source(l.Source), Project: l.ProjectID})' \
  ./internal/agent '^TestTurningTheMapOffRemovesOnlyWhatItAddedAndNothingElseNeeds$'
control "turning the map off leaves the Mods tab's own files" internal/agent/maps.go \
  'if slices.ContainsFunc(others, func(o addons.Installed) bool { return o.Key() == rec.Key() && o.FileName == rec.FileName }) {' \
  'if false && slices.ContainsFunc(others, func(o addons.Installed) bool { return o.Key() == rec.Key() && o.FileName == rec.FileName }) {' \
  ./internal/agent '^TestTurningTheMapOffRemovesOnlyWhatItAddedAndNothingElseNeeds$'
control "turning the map off keeps what another add-on needs" internal/agent/maps.go \
  'if parent := neededBy(others, rec); parent != "" {' \
  'if parent := neededBy(others, rec); false && parent != "" {' \
  ./internal/agent '^TestTurningTheMapOffRemovesOnlyWhatItAddedAndNothingElseNeeds$'
control "a Nether or End downloaded in the 26.1 layout joins its world" internal/worldimport/detect.go \
  'case d.id == dimNether && (d.folder == "DIM-1" || d.folder == modernFolder(dimNether)):' \
  'case d.id == dimNether && d.folder == "DIM-1":' \
  ./internal/worldimport '^TestSeparateNetherAndEndDownloadsJoinTheirWorld$'
control "a Nether or End in the 26.1 layout without level.dat joins its world" internal/worldimport/detect.go \
  '		{id: dimNether, folder: modernFolder(dimNether), suffix: "_nether"},
' \
  '' \
  ./internal/worldimport '^TestSeparateNetherAndEndDownloadsJoinTheirWorld$'
control "a separate dimension joins a 26.1 world instead of blocking it" internal/worldimport/plan.go \
  '				pl.staleWarning(c.id, pl.inWorld(kept), pl.in.display(c.path))
			}
		}
	} else {' \
  '				pl.staleWarning(c.id, pl.inWorld(kept), pl.in.display(c.path))
			} else {
				pl.spigotLayout()
			}
		}
	} else {' \
  ./internal/worldimport '^TestSeparateNetherAndEndDownloadsJoinTheirWorld$'
control "a 26.1 world takes an older Nether or End into dimensions/minecraft" internal/worldimport/plan.go \
  'if pl.modern && legacyFolder(c.folder) {' \
  'if false && pl.modern && legacyFolder(c.folder) {' \
  ./internal/worldimport '^TestSeparateNetherAndEndDownloadsJoinTheirWorld$'
control "a 26.1 world merges its Nether and End on Paper too" internal/worldimport/plan.go \
  'case pl.fam == familyBukkit && !pl.modern:' \
  'case pl.fam == familyBukkit:' \
  ./internal/worldimport '^TestSeparateNetherAndEndDownloadsJoinTheirWorld$'
control "an older world refuses a Nether or End saved by 26.1" internal/worldimport/plan.go \
  'if vanillaDim(c.id) && !legacyFolder(c.folder) {
				pl.problem(note(KindMixedLayout, "Upload the Nether and the End the server saved together with this world.",' \
  'if false && vanillaDim(c.id) && !legacyFolder(c.folder) {
				pl.problem(note(KindMixedLayout, "Upload the Nether and the End the server saved together with this world.",' \
  ./internal/worldimport '^TestSeparateNetherAndEndDownloadsJoinTheirWorld$'
control "a world no version can load yet is offered none" internal/agent/worldimports.go \
  '			return []api.WorldImportVersion{{CatalogEntry: keep, Keep: true}}, rec, nil
		}
		return nil, rec, nil' \
  '			return []api.WorldImportVersion{{CatalogEntry: keep, Keep: true}}, rec, nil
		}
		return []api.WorldImportVersion{recommended}, rec, nil' \
  ./internal/agent '^TestWorldImportRefusals$'
control "a link named like a custom dimension's folder is refused" internal/worldimport/folders.go \
  'case custom && link && dimensionName(dim):' \
  'case false && custom && link && dimensionName(dim):' \
  ./internal/worldimport '^TestWorldFoldersRefusesLinks$'
control "an import refused over a linked world folder starts the previous world again" internal/agent/worldimports.go \
  '	folders, err := worldimport.WorldFolders(live, level)
	if err != nil {
		s.startPrevious(ctx, h, prev, wasRunning)' \
  '	folders, err := worldimport.WorldFolders(live, level)
	if err != nil {' \
  ./internal/agent '^TestAWorldImportRefusesLinkedWorldFolders$'
control "each run that comes online waits for squaremap afresh" internal/agent/maps.go \
  '	if prev := ms.rendering[s.id]; prev != nil {
		prev.stop()
	}' \
  '	if prev := ms.rendering[s.id]; prev != nil {
		ms.mu.Unlock()
		stop()
		return
	}' \
  ./internal/agent '^TestEveryRunThatComesOnlineGetsTheFirstRender$'
control "turning the map on uses squaremap the Plugins or Mods tab installed" internal/agent/maps.go \
  'if !slices.ContainsFunc(installed, isSquaremap) {' \
  'if true || !slices.ContainsFunc(installed, isSquaremap) {' \
  ./internal/agent '^TestTheMapUsesSquaremapThePluginsTabInstalled$'
control "the map counts squaremap the Plugins or Mods tab manages as its own file" internal/agent/maps.go \
  'if i := slices.IndexFunc(installed, isSquaremap); i >= 0 {' \
  'if i := slices.IndexFunc(installed, isSquaremap); false && i >= 0 {' \
  ./internal/agent '^TestTheMapUsesSquaremapThePluginsTabInstalled$'
control "a sparse member of a tar or tar.gz is refused" internal/worldimport/archive.go \
  '		if sparse(h) {' \
  '		if false && sparse(h) {' \
  ./internal/worldimport '^TestExpansionLimitsHoldForEveryFormat$'
control "staging stops a zip file past its own allowance" internal/worldimport/stage.go \
  'own := &readCap{n: entryAllowance(e.csize, in.lim.MaxRatio), err: arc.err}' \
  'own := &readCap{n: 1 << 62, err: arc.err}' \
  ./internal/worldimport '^TestStagingCountsWhatItWrites$'
control "staging stops an archive past its ratio allowance" internal/worldimport/stage.go \
  'arc := &readCap{n: ratioAllowance(info.Bytes, in.lim.MaxRatio), err: ratioError(info.Name, in.lim.MaxRatio)}' \
  'arc := &readCap{n: 1 << 62, err: ratioError(info.Name, in.lim.MaxRatio)}' \
  ./internal/worldimport '^TestStagingCountsWhatItWrites$'
control "staging counts what it writes against MaxTotalBytes" internal/worldimport/stage.go \
  'if w.b.left -= int64(len(p)); w.b.left < 0 {' \
  'if w.b.left -= int64(len(p)); false && w.b.left < 0 {' \
  ./internal/worldimport '^TestStagingCountsWhatItWrites$'
control "the first render follows every run that comes online, however it started" internal/agent/collector.go \
  '			if take {
				s.mapRunOnline(runStart)
			}' \
  '			if false && take {
				s.mapRunOnline(runStart)
			}' \
  ./internal/agent '^TestEveryRunThatComesOnlineGetsTheFirstRender$'
control "a restart is put off only while squaremap needs it" internal/agent/maps.go \
  'if l := s.mapLive(r.Context(), true); !rec.pendingRestart(l) {' \
  'if l := s.mapLive(r.Context(), true); false && !rec.pendingRestart(l) {' \
  ./internal/agent '^TestRestartLaterOnlyWhileSquaremapNeedsARestart$'
control "a restart put off is dropped once squaremap is loaded" internal/agent/maps.go \
  'if !rec.pendingRestart(l) {' \
  'if false && !rec.pendingRestart(l) {' \
  ./internal/agent '^TestRestartLaterOnlyWhileSquaremapNeedsARestart$'

# Wave 7: who may change where backup copies go and hold the recovery key.
control "an admin needs two-factor on to hold backup keys" internal/panel/workspace.go \
  'return a.owner() || (a.InstallRole == roleMember && a.ProjectRole == invites.RoleAdmin && a.FactorOn && a.TwoFactor)' \
  'return a.owner() || (a.InstallRole == roleMember && a.ProjectRole == invites.RoleAdmin)' \
  ./internal/panel '^TestOneCheckDecidesWhoHoldsBackupKeys$'
control "an admin with two-factor on holds backup keys" internal/panel/workspace.go \
  'return a.owner() || (a.InstallRole == roleMember && a.ProjectRole == invites.RoleAdmin && a.FactorOn && a.TwoFactor)' \
  'return a.owner()' \
  ./internal/panel '^(TestOneCheckDecidesWhoHoldsBackupKeys|TestWaveSevenRoutesFollowTheTeamTable)$'
control "bringing servers back from copies needs every server" internal/panel/workspace.go \
  'if act == actRecoverBackups && !a.owner() && !a.Servers.All {' \
  'if false && act == actRecoverBackups && !a.owner() && !a.Servers.All {' \
  ./internal/panel '^(TestOneCheckDecidesWhoHoldsBackupKeys|TestMachineWideActionsNeedEveryServer)$'
control "changing where copies go is a backup key action" internal/panel/server.go \
  '{"POST", "/api/servers/{id}/offsite", needSessionCSRF, actManageBackupCopies,' \
  '{"POST", "/api/servers/{id}/offsite", needSessionCSRF, actManageServers,' \
  ./internal/panel '^TestOneCheckDecidesWhoHoldsBackupKeys$'
control "the recovery key is a backup key action" internal/panel/server.go \
  '{"GET", "/api/servers/{id}/offsite/recovery-key", needSession, actRecoveryKey,' \
  '{"GET", "/api/servers/{id}/offsite/recovery-key", needSession, actManageServers,' \
  ./internal/panel '^TestOneCheckDecidesWhoHoldsBackupKeys$'
control "restoring from a recovery key is a backup key action" internal/panel/server.go \
  'mm("POST", "/api/machines/{mid}/offsite/recover", "/v1/offsite/recover", actRecoverBackups),' \
  'mm("POST", "/api/machines/{mid}/offsite/recover", "/v1/offsite/recover", actManageMachine),' \
  ./internal/panel '^TestOneCheckDecidesWhoHoldsBackupKeys$'
control "refused recovery key requests are audited" internal/panel/server.go \
  's.audit(sess.User.Username, "offsite.recovery_key", r.PathValue("id"), "refused", "not allowed to hold backup keys")' \
  '_ = 0' \
  ./internal/panel '^TestWaveSevenRoutesFollowTheTeamTable$'
control "refused recoveries from copies are audited" internal/panel/server.go \
  's.audit(sess.User.Username, "offsite.recover", r.PathValue("mid"), "refused", "not allowed to bring servers back from copies")' \
  '_ = 0' \
  ./internal/panel '^TestWaveSevenRoutesFollowTheTeamTable$'
control "the Disk space page needs every server to look at" internal/panel/server.go \
  'actView, everyServer(s.machineProxy("GET", "/v1/disk"))},' \
  'actView, s.machineProxy("GET", "/v1/disk")},' \
  ./internal/panel '^TestMachineWideActionsNeedEveryServer$'
control "the panel never caches the recovery key" internal/panel/automation.go \
  'w.Header().Set("Cache-Control", "no-store")' \
  '_ = 0' \
  ./internal/panel '^TestRecoveryKeyIsNeverCachedAndNamesWhoTookIt$'
control "the agent never caches the recovery key" internal/agent/offsite.go \
  'w.Header().Set("Cache-Control", "no-store")' \
  '_ = 0' \
  ./internal/agent '^TestCopiesSomewhereElseUploadRetryAndFollowTheRules$'
control "recovery key downloads name who took them" internal/agent/offsite.go \
  'actor, err := validActor(r.Header.Get("X-Playkeeper-Actor"))
	if err != nil {' \
  'actor, err := validActor(r.Header.Get("X-Playkeeper-Actor"))
	if false && err != nil {' \
  ./internal/agent '^TestCopiesSomewhereElseUploadRetryAndFollowTheRules$'
control "recovery key downloads are audited" internal/agent/offsite.go \
  's.audit(actor, "offsite.recovery_key.downloaded", "server", "succeeded", f.Name)' \
  '_, _ = actor, f.Name' \
  ./internal/agent '^TestCopiesSomewhereElseUploadRetryAndFollowTheRules$'

# Wave 7 in #15's restore: a restore that isn't over keeps what it may need.
control "a restore from a copy the agent stops in is left to the next start" internal/agent/offsite.go \
  'got, dl, err := s.fetchCopy(ctx, h, dest, archive, dir)
		if err != nil {
			return downloadStopped(s.stopping(), h, err)' \
  'got, dl, err := s.fetchCopy(ctx, h, dest, archive, dir)
		if err != nil {
			return err' \
  ./internal/agent '^TestARestoreFromACopyTheAgentStoppedInIsSettledAtTheNextStart$'
control "the rules keep the rollback archive of a restore that isn't over" internal/agent/backuprules.go \
  'needed[id] = "restore"' \
  '_ = id' \
  ./internal/agent '^TestARestoreThatIsNotOverKeepsItsRollbackArchiveAndStage$'
control "the Disk space page leaves a restore that isn't over alone" internal/agent/disk.go \
  'l.ActiveStages = append(l.ActiveStages, stage)
		journals = append(journals, j)' \
  '_, _ = stage, j' \
  ./internal/agent '^TestARestoreThatIsNotOverKeepsItsRollbackArchiveAndStage$'
control "a swap journal that can't be read may be any server's" internal/agent/backuprules.go \
  'return j == nil || j.ServerID == serverID' \
  'return j != nil && j.ServerID == serverID' \
  ./internal/agent '^TestAnUnreadableSwapJournalKeepsWhatAnyRestoreMayNeed$'
control "the Disk space page counts every server busy for an unreadable swap journal" internal/agent/disk.go \
  'return j.concerns(s.id)' \
  'return j != nil && j.concerns(s.id)' \
  ./internal/agent '^TestAnUnreadableSwapJournalKeepsWhatAnyRestoreMayNeed$'
control "the World tab keeps the world copies of a restore that isn't over" internal/agent/backups.go \
  'if s.restoreUnsettled() {' \
  'if false && s.restoreUnsettled() {' \
  ./internal/agent '^TestTheWorldTabKeepsTheWorldCopiesOfARestoreThatIsNotOver$'
control "the World tab keeps every server's world copies for an unreadable swap journal" internal/agent/backuprules.go \
  'if j.concerns(s.id) {' \
  'if j != nil && j.concerns(s.id) {' \
  ./internal/agent '^TestTheWorldTabKeepsTheWorldCopiesOfARestoreThatIsNotOver$'
control "a swap journal whose restore is gone keeps every rollback archive" internal/agent/backuprules.go \
  'if op != nil {
			add(op)
			continue
		}' \
  'if j != nil {
			if op != nil {
				add(op)
			}
			continue
		}' \
  ./internal/agent '^TestAnUnreadableSwapJournalKeepsWhatAnyRestoreMayNeed$'

# Wave 7: sleeping and waking leave the desired state, the stand-in and the
# sleep setting agreeing.
control "a failed wake sleeps again when the server didn't start" internal/agent/sleeping.go \
  'rerr != nil || !running {' \
  'rerr != nil || false && !running {' \
  ./internal/agent '^TestSleepAndWakeTransitions$/^a_wake_whose_start_fails$'
control "a failed wake sleeps again when Docker can't say whether the server runs" internal/agent/sleeping.go \
  'rerr != nil || !running {' \
  'rerr == nil && !running {' \
  ./internal/agent '^TestSleepAndWakeTransitions$/^a_wake_that_fails_while_Docker_can.t_say_whether_the_server_runs$'
control "a wake waits for a backup to end" internal/agent/sleeping.go \
  'ae.Code != api.CodeBusy || time.Now().After(deadline)' \
  'ae.Code == api.CodeBusy || time.Now().After(deadline)' \
  ./internal/agent '^TestSleepAndWakeTransitions$/^a_player_wakes_it_during_a_backup$'
control "turning sleep off changes nothing while the server is busy" internal/agent/sleeping.go \
  '		if err != nil {
			writeError(w, err)
			return
		}
		resp["operation"] = op' \
  '		if err == nil {
			resp["operation"] = op
		}' \
  ./internal/agent '^TestSleepAndWakeTransitions$/^sleep_turned_off_during_a_backup$'
control "turning sleep off lets go of the game port when the server can't start" internal/agent/sleeping.go \
  '				s.leaveSleep()
				s.startFailed(ctx)' \
  '				s.startFailed(ctx)' \
  ./internal/agent '^TestSleepAndWakeTransitions$/^sleep_turned_off,_and_the_server_can.t_start$'

# Wave 7 after the real-world restore check: the copies at the old place, the
# recovery key's folder, who removed a backup here, the first copy, and
# scheduled backups refused because saving couldn't be paused.
control "a new place asks before forgetting the copies at the old one" internal/agent/offsite.go \
  'if len(forgotten) > 0 && !req.ForgetCopies {' \
  'if false && len(forgotten) > 0 && !req.ForgetCopies {' \
  ./internal/agent '^TestChangingWhereCopiesGoAsksBeforeForgettingTheOldCopies$'
control "the question counts the backups whose only copy is at the old place" internal/agent/offsite.go \
  '		if !c.OnHost {
			n++' \
  '		if c.OnHost {
			n++' \
  ./internal/agent '^TestChangingWhereCopiesGoAsksBeforeForgettingTheOldCopies$'
control "forgetting the copies at the old place is audited" internal/agent/offsite.go \
  's.audit(actor, "offsite.copies_forgotten", "server", "succeeded", forgottenDetail(offsitePlace(row.cfg.Config), forgotten))' \
  '_ = forgotten' \
  ./internal/agent '^TestChangingWhereCopiesGoAsksBeforeForgettingTheOldCopies$'
control "a recovery key file naming another folder asks to be downloaded again" internal/agent/offsite.go \
  'if r.keySavedAt != nil && r.keySavedFolder != nil && !sameFolder(*r.keySavedFolder, kv.Folder) {' \
  'if false && r.keySavedAt != nil && r.keySavedFolder != nil && !sameFolder(*r.keySavedFolder, kv.Folder) {' \
  ./internal/agent '^TestTheRecoveryKeyIsDownloadedAgainWhenCopiesGoToAnotherFolder$'
control "downloading the recovery key records the folder the file names" internal/agent/offsite.go \
  's.now().UnixMilli(), f.Folder, s.id)' \
  's.now().UnixMilli(), "", s.id)' \
  ./internal/agent '^TestTheRecoveryKeyIsDownloadedAgainWhenCopiesGoToAnotherFolder$'
control "looking for copies says the key file's folder isn't there" internal/agent/recover.go \
  'writeError(w, automationError(keyFileFolder(err, req)))' \
  'writeError(w, automationError(err))' \
  ./internal/agent '^TestANewMachineBringsAServerBackFromItsCopiesWithTheRecoveryKey$'
control "a restore says the key file's folder isn't there" internal/agent/recover.go \
  'h, automationError(keyFileFolder(err, req)))' \
  'h, automationError(err))' \
  ./internal/agent '^TestANewMachineBringsAServerBackFromItsCopiesWithTheRecoveryKey$'
control "a folder the user typed isn't blamed on the key file" internal/agent/recover.go \
  'strings.TrimSpace(req.Config.SFTP.Folder) != "" || ' \
  '' \
  ./internal/agent '^TestANewMachineBringsAServerBackFromItsCopiesWithTheRecoveryKey$'
control "looking for copies in a missing folder doesn't say to create it" internal/offsite/sftp.go \
  'if op == opList {' \
  'if false && op == opList {' \
  ./internal/offsite '^TestSFTPList$'
control "a copy says the rules removed its backup only when they did" internal/agent/offsite.go \
  'case removedBy == retentionActor:' \
  'case false:' \
  ./internal/agent '^TestACopyWithoutItsBackupSaysWhoRemovedIt$'
control "the rules note on a copy that they removed its backup" internal/agent/backuprules.go \
  's.noteRemoved(b.ID, actor)' \
  '' \
  ./internal/agent '^TestACopyWithoutItsBackupSaysWhoRemovedIt$'
control "deleting a backup by hand is noted on its copy" internal/agent/handlers.go \
  's.noteRemoved(b.ID, actor)' \
  '' \
  ./internal/agent '^TestACopyWithoutItsBackupSaysWhoRemovedIt$'
control "only the first copy to a place is called the first" internal/agent/offsite.go \
  'v.FirstCopy = v.LastCopy != nil && r.copiesMade == 1' \
  'v.FirstCopy = v.LastCopy != nil && v.Copies == 1' \
  ./internal/agent '^TestOnlyTheFirstCopyToAPlaceIsCalledTheFirst$'
control "each finished copy is counted" internal/agent/offsite.go \
  'copies_made = copies_made + 1 WHERE' \
  'copies_made = copies_made WHERE' \
  ./internal/agent '^TestOnlyTheFirstCopyToAPlaceIsCalledTheFirst$'
control "a new place counts its copies from none" internal/agent/offsite.go \
  'copies_made = 0 WHERE' \
  'copies_made = copies_made WHERE' \
  ./internal/agent '^TestOnlyTheFirstCopyToAPlaceIsCalledTheFirst$'
control "a scheduled backup refused for want of a pause is recorded" internal/agent/schedules.go \
  's.noteBackupRefused(h.op.ID, op.ScheduleID, why, err)' \
  '_ = why' \
  ./internal/agent '^TestARefusedScheduledBackupIsShownUntilABackupSucceeds$'
control "only backups refused for want of a pause count as refused" internal/agent/schedules.go \
  'case pauseRefusals[backup.ErrorKind(ae.Code)]:' \
  'case ae.Code != "":' \
  ./internal/agent '^TestARefusedScheduledBackupIsShownUntilABackupSucceeds$'
control "a scheduled backup refused while the server starts counts" internal/agent/schedules.go \
  'case ae.Reason == refusedNotOnline:' \
  'case false:' \
  ./internal/agent '^TestARefusedScheduledBackupIsShownUntilABackupSucceeds$'
control "a backup refused while the server starts says so" internal/agent/backups.go \
  'e.Reason = refusedNotOnline' \
  '_ = refusedNotOnline' \
  ./internal/agent '^TestARefusedScheduledBackupIsShownUntilABackupSucceeds$'
control "refused scheduled backups in a row count from the first" internal/agent/schedules.go \
  'r.Since, r.Count = prev.Since, prev.Count+1' \
  '_ = prev' \
  ./internal/agent '^TestARefusedScheduledBackupIsShownUntilABackupSucceeds$'
control "a refusal names its operation, so the World tab shows it once" internal/agent/schedules.go \
  'ScheduleID: scheduleID, OperationID: opID}' \
  'ScheduleID: scheduleID}' \
  ./internal/agent '^TestARefusedScheduledBackupIsShownUntilABackupSucceeds$'
control "a refusal keeps the backup's hint" internal/agent/schedules.go \
  'r.Hint = ae.Hint' \
  '_ = ae' \
  ./internal/agent '^TestARefusedScheduledBackupIsShownUntilABackupSucceeds$'
control "a refused scheduled backup gets a line in the recent activity" internal/agent/schedules.go \
  's.recordEvent(now, "backup_refused", "", "playkeeper", why)' \
  '_ = why' \
  ./internal/agent '^TestARefusedScheduledBackupIsShownUntilABackupSucceeds$'
control "the recent activity lists refused scheduled backups" internal/agent/analytics.go \
  ', "backup_refused": "backup_refused",' \
  ',' \
  ./internal/agent '^TestARefusedScheduledBackupIsShownUntilABackupSucceeds$'
control "a server's status carries its refused scheduled backups" internal/agent/automation.go \
  'st.BackupRefused = s.backupRefusal()' \
  '' \
  ./internal/agent '^TestARefusedScheduledBackupIsShownUntilABackupSucceeds$'
control "a backup that succeeds clears the refused scheduled backups" internal/agent/backuprules.go \
  's.clearBackupRefused()' \
  '' \
  ./internal/agent '^TestARefusedScheduledBackupIsShownUntilABackupSucceeds$'
control "a refused scheduled backup sends the backup-failed alert" internal/agent/lifecycle.go \
  'if kind == "backup" && done.Status == api.OpFailed {' \
  'if false && kind == "backup" && done.Status == api.OpFailed {' \
  ./internal/agent '^TestARefusedScheduledBackupIsShownUntilABackupSucceeds$'
# Wave 9: a modpack with only betas has its own line, whatever path its notice takes.
control "a pack's only-pre-release notice has the pack's own line" internal/agent/addons.go \
  'if n.Params["pack"] != "" {' \
  'if false {' \
  ./internal/agent '^TestOnlyPrereleaseNoticesOfferNothingPlaykeeperCantDo$'
control "an add-on or pack error the agent answers with has Playkeeper's hint" internal/agent/addons.go \
  '	n := apiNotice(e.Notice)
	return &apiError{Status: status, Code: string(e.Kind), Msg: n.Message, Hint: n.Hint}' \
  '	return &apiError{Status: status, Code: string(e.Kind), Msg: e.Msg, Hint: e.Hint}' \
  ./internal/agent '^TestOnlyPrereleaseNoticesOfferNothingPlaykeeperCantDo$'

# Wave 7 after Bugbot's findings on d825c69: a running map pre-generation keeps
# an empty server awake, and a backup dropped from a full copy queue discards
# what it left at the destination.
control "a running map pre-generation keeps an empty server awake" internal/agent/sleeping.go \
  'Busy: s.busy() || s.pregenRunning() || s.scheduleWorking(),' \
  'Busy: s.busy() || s.scheduleWorking(),' \
  ./internal/agent '^TestSleepWaitsForTheMapPreGeneration$/^running$'
control "sleep goes by what Chunky reported last about the task" internal/agent/pregen.go \
  'return st == pregen.StateRunning' \
  '_ = st' \
  ./internal/agent '^TestSleepWaitsForTheMapPreGeneration$/^paused_from_the_console$'
control "until Chunky reports, a running task keeps the server awake" internal/agent/pregen.go \
  'return !task.PausedByUser && !task.PausedByPolicy' \
  'return false' \
  ./internal/agent '^TestSleepWaitsForTheMapPreGeneration$/^running,_before_Chunky_reports$'
control "until Chunky reports, a paused task lets the server sleep" internal/agent/pregen.go \
  'return !task.PausedByUser && !task.PausedByPolicy' \
  'return true' \
  ./internal/agent '^TestSleepWaitsForTheMapPreGeneration$/^paused,_before_Chunky_reports$'
control "a finished map pre-generation lets the server sleep" internal/agent/pregen.go \
  '	if !task.unfinished() {
		return false
	}
	if st, ok := s.pg.lastState(); ok {' \
  '	if st, ok := s.pg.lastState(); ok {' \
  ./internal/agent '^TestSleepWaitsForTheMapPreGeneration$/^finished$'
control "a backup dropped from a full copy queue discards what it left at the destination" internal/agent/offsite.go \
  's.discardUploads(dropped)' \
  '_ = dropped' \
  ./internal/agent '^TestABackupDroppedFromAFullQueueDiscardsWhatItLeftAtTheDestination$'
control "only the dropped backups' unfinished copies are discarded" internal/agent/offsite.go \
  's.discardUploads(dropped)' \
  's.discardUploads(append(s.queuedStates(), dropped...))' \
  ./internal/agent '^TestABackupDroppedFromAFullQueueDiscardsWhatItLeftAtTheDestination$/^S3$'

# Wave 7 after Bugbot's finding on ee0e519: the uploader claims the copy it
# picks as it picks it, the queue trim leaves the claimed copy alone, and
# turning copies off between the pick and the upload stops the copy.
control "the queue trim leaves the copy the uploader claimed alone" internal/agent/offsite.go \
  'claimed = c.backupID' \
  '_ = c' \
  ./internal/agent '^TestTheCopyBeingMadeStaysQueuedWhenABackupJoinsAFullQueue$'
control "the uploader's claim names the copy it picked" internal/agent/offsite.go \
  's.auto.claim = &uploadClaim{backupID: j.backupID, cancel: cancel}' \
  's.auto.claim = &uploadClaim{cancel: cancel}' \
  ./internal/agent '^TestTheCopyBeingMadeStaysQueuedWhenABackupJoinsAFullQueue$/^SFTP$'
control "a claimed copy uploads under the claim's cancel" internal/agent/offsite.go \
  'cp, err := dest.Upload(job.ctx,' \
  'cp, err := dest.Upload(ctx,' \
  ./internal/agent '^TestTheCopyBeingMadeStaysQueuedWhenABackupJoinsAFullQueue$/^S3$'
control "turning copies off stops the copy the uploader claimed" internal/agent/offsite.go \
  'func (s *server) stopUpload() {
	s.auto.mu.Lock()
	c := s.auto.claim' \
  'func (s *server) stopUpload() {
	s.auto.mu.Lock()
	var c *uploadClaim' \
  ./internal/agent '^TestTheCopyBeingMadeStaysQueuedWhenABackupJoinsAFullQueue$/^S3$'

# Wave 7 after Bugbot's findings on e6a1dfc7: a scheduled restart's countdown
# keeps an empty server awake, and with the allowlist off anyone who isn't
# banned wakes a sleeping server by joining.
control "a scheduled restart's countdown keeps an empty server awake" internal/agent/sleeping.go \
  'Busy: s.busy() || s.pregenRunning() || s.scheduleWorking(),' \
  'Busy: s.busy() || s.pregenRunning(),' \
  ./internal/agent '^TestSleepWaitsForAScheduledRestartsCountdown$/^a_restart_counting_down$'
control "a restart schedule counts as working while it counts down" internal/agent/schedules.go \
  'return ok && (act.Job.Schedule.Kind == schedule.KindRestart || act.Job.Schedule.Kind == schedule.KindBackup)' \
  'return ok && act.Job.Schedule.Kind == schedule.KindBackup' \
  ./internal/agent '^TestSleepWaitsForAScheduledRestartsCountdown$/^a_restart_counting_down$'
control "with the allowlist off, anyone who isn't banned wakes a sleeping server" internal/agent/sleeping.go \
  'if allowlistOff(readProperties(s.dataDir())) {' \
  'if false && allowlistOff(readProperties(s.dataDir())) {' \
  ./internal/agent '^TestWhoMayWakeASleepingServer$/^allowlist_off$'
control "a banned player doesn't wake a server whose allowlist is off" internal/agent/sleeping.go \
  'if strings.EqualFold(name, player) {' \
  'if false && strings.EqualFold(name, player) {' \
  ./internal/agent '^TestWhoMayWakeASleepingServer$/^allowlist_off$'
control "white-list=true turns the allowlist on in any case" internal/agent/sleeping.go \
  '!strings.EqualFold(props["white-list"], "true")' \
  'props["white-list"] != "true"' \
  ./internal/agent '^TestWhoMayWakeASleepingServer$/^allowlist_on,_in_capitals$'
control "a server.properties that can't be read keeps the allowlist rule" internal/agent/sleeping.go \
  'return props != nil && !strings.EqualFold' \
  'return !strings.EqualFold' \
  ./internal/agent '^TestWhoMayWakeASleepingServer$/^no_server.properties$'
control "a failed lookup of a server's machine sends its requests to no machine, not the dashboard's own" internal/panel/workspace.go \
  'Scan(&owner, &disputedBy)
	if err != nil && !isNoRows(err) {' \
  'Scan(&owner, &disputedBy)
	if false && err != nil && !isNoRows(err) {' \
  ./internal/panel '^TestAServersRequestsGoNowhereWhenItsMachineCantBeLookedUp$'
control "a joined machine's servers still show when their record can't be written" internal/panel/machines.go \
  'return s.unsavedServers(m, servers)' \
  'return nil' \
  ./internal/panel '^TestAJoinedMachinesServersShowWhenTheirRecordCantBeWritten$'
control "a joined machine's server whose record isn't saved goes to no machine, not the dashboard's own" internal/panel/workspace.go \
  'case joinedListed:
		return machine{}, errServerMachine' \
  'case false && joinedListed:
		return machine{}, errServerMachine' \
  ./internal/panel '^TestAFailedClaimShowsNoOtherMachinesServerAndSendsUnsavedOnesNowhere$'
control "a failed claim never shows another machine's server" internal/panel/machines.go \
  'case rec.machineID != m.ID:
			continue' \
  'case false && rec.machineID != m.ID:
			continue' \
  ./internal/panel '^TestAFailedClaimShowsNoOtherMachinesServerAndSendsUnsavedOnesNowhere$'
control "every change on a joined machine names who makes it" internal/panel/server.go \
  'return machinelink.WithActor(ctx, actor)' \
  'return ctx' \
  ./internal/panel '^TestEveryChangeOnAMachineNamesWhoMakesIt$'
control "a join request's alert names its server for the dashboard's agent" internal/panel/friends.go \
  'ServerName: serverName,' \
  '' \
  ./internal/panel '^TestEveryChangeOnAMachineNamesWhoMakesIt$'
control "the dashboard's agent posts a joined machine's join request" internal/agent/discord.go \
  'if err != nil || !reServerID.MatchString(req.ServerID) {' \
  'if true || err != nil || !reServerID.MatchString(req.ServerID) {' \
  ./internal/agent '^TestDiscordNotifyPostsAJoinedMachinesJoinRequestUnderItsName$'
control "a joined machine's server name is checked before it's posted" internal/agent/discord.go \
  'name, err := validName(req.ServerName)' \
  'name, err := req.ServerName, error(nil)' \
  ./internal/agent '^TestDiscordNotifyPostsAJoinedMachinesJoinRequestUnderItsName$'
control "a listing claimed after its machine was removed changes nothing" internal/panel/machines.go \
  'case revoked != 0:
			return errMachineGone' \
  'case false && revoked != 0:
			return errMachineGone' \
  ./internal/panel '^TestServerRecordsFollowWhichMachinesAreStillJoined$'
control "a server made while a listing was on its way keeps its record" internal/panel/machines.go \
  'WHERE machine_id = ? AND seen_at <= ?' \
  'WHERE machine_id = ? AND seen_at <= ? + 1e15' \
  ./internal/panel '^TestServerRecordsFollowWhichMachinesAreStillJoined$'
control "a removed machine's server goes to no machine, not the dashboard's own" internal/panel/workspace.go \
  'case recorded:
		return machine{}, errNotFound' \
  'case false && recorded:
		return machine{}, errNotFound' \
  ./internal/panel '^TestServerRecordsFollowWhichMachinesAreStillJoined$'
control "a machine that lists a removed machine's server takes it over" internal/panel/machines.go \
  'case owner != m.ID && !ownerActive:' \
  'case false && owner != m.ID && !ownerActive:' \
  ./internal/panel '^TestServerRecordsFollowWhichMachinesAreStillJoined$'
control "every server in the list has its own slug" internal/panel/workspace.go \
  'uniqueSlugs(out)' \
  '' \
  ./internal/panel '^TestEveryServerInTheListHasItsOwnSlug$'
control "a duplicate's number skips slugs another server has" internal/panel/workspace.go \
  'if next := fmt.Sprintf("%s-%d", slug, i); !taken[next] {' \
  'if next := fmt.Sprintf("%s-%d", slug, i); true {' \
  ./internal/panel '^TestEveryServerInTheListHasItsOwnSlug$'
control "a joined machine that can't answer holds up no team change" internal/panel/join.go \
  'all, _, err := s.allServers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]serverRef, 0, len(all))
	for _, sv := range all {
		id, _ := sv["id"].(string)
		name, _ := sv["name"].(string)
		out = append(out, serverRef{ID: id, Name: name})
	}
	return out, nil' \
  'list, err := s.machines()
	if err != nil {
		return nil, err
	}
	var out []serverRef
	for _, m := range list {
		var servers []serverRef
		if _, err := m.agent.Do(ctx, "GET", "/v1/servers", nil, nil, &servers); err != nil {
			return nil, err
		}
		out = append(out, servers...)
	}
	return out, nil' \
  ./internal/panel '^TestAMachineThatCantAnswerHoldsUpNoTeamChange$'
control "taking a member's rights away never waits for a machine" internal/panel/team.go \
  'case !invites.Narrows(t.Account, req.Role, req.Servers):' \
  'case true:' \
  ./internal/panel '^TestAMachineThatCantAnswerHoldsUpNoTeamChange$'
control "an away machine's servers are never shown as none when they can't be read" internal/panel/workspace.go \
  'known, err := s.lastKnownServers(m)
			if err != nil {' \
  'known, err := s.lastKnownServers(m)
			if false && err != nil {' \
  ./internal/panel '^TestAnAwayMachinesServersAreNeverShownAsNone$'
control "a removed machine's servers show nowhere as servers" internal/panel/workspace.go \
  'WHERE revoked_at = 0 ORDER BY kind != ?, created_at, id' \
  'WHERE revoked_at >= 0 ORDER BY kind != ?, created_at, id' \
  ./internal/panel '^TestARemovedMachinesServersShowNowhere$'
control "a server made on a machine doesn't take a removed machine's server" internal/panel/machines.go \
  'INSERT OR IGNORE INTO server_machines(server_id, machine_id, seen_at) VALUES(?,?,?)' \
  'INSERT OR REPLACE INTO server_machines(server_id, machine_id, seen_at) VALUES(?,?,?)' \
  ./internal/panel '^TestARemovedMachinesServersShowNowhere$'

# Forge: every file its installer writes is checked against Forge's own
# hashes, its builds and heap follow Forge's lists and a mod loader's needs,
# and crash help reads Forge's console and crash reports.
control "Forge's installer setting names a downloaded jar in the server's folder" internal/minecraft/software/plan.go \
  'case "CUSTOM_SERVER", "NEOFORGE_INSTALLER", "FORGE_INSTALLER":' \
  'case "FORGE_INSTALLER":
			ok = true
		case "CUSTOM_SERVER", "NEOFORGE_INSTALLER":' \
  ./internal/minecraft/software '^TestPlanValidation$'
control "a Forge installer installs the version pinned" internal/minecraft/software/forge.go \
  'if prof.Version != name || prof.Minecraft != pin.MinecraftVersion || ver.ID != name || ver.InheritsFrom != pin.MinecraftVersion {' \
  'if false && (prof.Version != name || prof.Minecraft != pin.MinecraftVersion || ver.ID != name || ver.InheritsFrom != pin.MinecraftVersion) {' \
  ./internal/minecraft/software '^TestInstallForgeRefuses$'
control "Forge's installer looks for Mojang's jar where Playkeeper verified it" internal/minecraft/software/forge.go \
  '; prof.ServerJarPath != want {' \
  '; false && prof.ServerJarPath != want {' \
  ./internal/minecraft/software '^TestInstallForgeRefuses$'
control "every Forge library has a plain path, a SHA-1 and a size" internal/minecraft/software/forge.go \
  'if !ok || a.Size <= 0 || !cleanRel(a.Path, ".jar", ".zip") {' \
  'if false && (!ok || a.Size <= 0 || !cleanRel(a.Path, ".jar", ".zip")) {' \
  ./internal/minecraft/software '^TestInstallForgeRefuses$'
control "a Forge server starts from Forge's shim jar" internal/minecraft/software/forge.go \
  'if prof.Path != shimCoord || !ok || shim == nil || shim.Path != into+"/"+shimRel {' \
  'if false && (prof.Path != shimCoord || !ok || shim == nil || shim.Path != into+"/"+shimRel) {' \
  ./internal/minecraft/software '^TestInstallForgeRefuses$'
control "the shim jar a Forge server starts is checked" internal/minecraft/software/forge.go \
  'checks = append(checks, Check{Path: shimName, Hash: shim.Hash, Size: shim.Size, Origin: Derived, Source: forgeLibrarySource})' \
  '' \
  ./internal/minecraft/software '^TestInstallForgeRefuses$'
control "Forge's launch arguments are where the server container reads them" internal/minecraft/software/forge.go \
  'if !extractsForgeArgs(prof, forgeArgsPath(pin)) {' \
  'if false && !extractsForgeArgs(prof, forgeArgsPath(pin)) {' \
  ./internal/minecraft/software '^TestInstallForgeRefuses$'
control "Forge's launch arguments start its checked shim jar" internal/minecraft/software/forge.go \
  'if !startsJar(string(args), shimName) {' \
  'if false && !startsJar(string(args), shimName) {' \
  ./internal/minecraft/software '^TestInstallForgeRefuses$'
control "the files Forge's installer builds are checked against the SHA-1s it publishes" internal/minecraft/software/forge.go \
  'out = append(out, Check{Path: into + "/" + p, Hash: h, Origin: Derived, Source: forgeOutputSource})' \
  '_, _ = p, h' \
  ./internal/minecraft/software '^TestInstallForgeRefuses$'
control "a Forge installer must publish the patched jar's SHA-1" internal/minecraft/software/forge.go \
  'if !patched {' \
  'if false && !patched {' \
  ./internal/minecraft/software '^TestInstallForgeRefuses$'
control "a Forge pin names a Forge version" internal/minecraft/software/software.go \
  'if !reForgeVersion.MatchString(p.ForgeVersion) {' \
  'if false && !reForgeVersion.MatchString(p.ForgeVersion) {' \
  ./internal/minecraft/software '^TestPinValidate$'
control "Forge builds before the one Forge recommends are beta" internal/minecraft/software/forge.go \
  'if rec, ok := fb.recommended[mc]; ok && compareVersions(v, rec) >= 0 {' \
  'if true {' \
  ./internal/minecraft/software '^TestBuilds$'
control "Forge keeps as much memory outside the heap as NeoForge" internal/minecraft/catalog.go \
  '"neoforge": 1024, "forge": 1024}' \
  '"neoforge": 1024}' \
  ./internal/minecraft '^TestHeapForModLoaders$'
control "a Forge server gets only the Forge build of a mod" internal/addons/target.go \
  'Loaders: []string{"forge"}}' \
  'Loaders: []string{"neoforge"}}' \
  ./internal/addons '^(TestTargetFor|TestPlanInstallPicksTheLoadersVersion)$'
control "Forge's crash report line counts as a crash" internal/minecraft/logparse.go \
  'This crash report has been saved to: |Crash report saved to |' \
  'This crash report has been saved to: |' \
  ./internal/minecraft '^TestParseRecognisesPlayerEvents$'
control "crash help on a Forge server names Forge" internal/diagnose/crashaddons.go \
  'if c.in.ServerType == "forge" {
		return "Forge"' \
  'if false {
		return "Forge"' \
  ./internal/diagnose '^TestExplainCrashRecognisesEachCause$'
control "a jar Forge can't open is explained" internal/diagnose/crashaddons.go \
  '	if d, ok := c.forgeBrokenJar(); ok {
		return d, true
	}
' \
  '' \
  ./internal/diagnose '^TestExplainCrashRecognisesEachCause$'
control "crash help reads what a Forge mod needs from the crash report alone" internal/diagnose/crashaddons.go \
  '	} else {
		f, cur, ok = c.reportPair(reNeoRequires, reNeoCurrently)
	}' \
  '	}' \
  ./internal/diagnose '^TestExplainCrashRecognisesEachCause$'
control "a Forge mod that failed in a deferred task is named" internal/diagnose/crashaddons.go \
  'if f, ok = c.firstIn(reForgeDeferred, 0, len(c.split)); ok {' \
  'if f, ok = c.firstIn(reForgeDeferred, 0, len(c.split)); false && ok {' \
  ./internal/diagnose '^TestExplainCrashRecognisesEachCause$'
control "a failed Forge mod's jar is found from its stack frames" internal/diagnose/crashaddons.go \
  '	if jar == "" && !f.inReport() {
		jar = c.frameAddon(f.idx+1, c.stackEnd(f.idx))
	}
' \
  '' \
  ./internal/diagnose '^TestExplainCrashRecognisesEachCause$'
control "a start stops a server that logged it failed but kept running" internal/agent/lifecycle.go \
  'if err == nil && c.State.Running && !gaveUp.IsZero() && s.now().Sub(gaveUp) >= hungStartWait {' \
  'if false && err == nil && c.State.Running && !gaveUp.IsZero() && s.now().Sub(gaveUp) >= hungStartWait {' \
  ./internal/agent '^TestAStartThatGaveUpButKeptRunningIsStoppedAndExplained$'
webcontrol "the dashboard gives Forge the heap the agent gives it" web/src/components/app/create.tsx \
  'neoforge: 1024, forge: 1024 }' \
  'neoforge: 1024 }' \
  web/src/lib/lib.test.ts 'how much of it Java gets'
webcontrol "Forge suits as many friends as NeoForge at the same memory" web/src/lib/memory.ts \
  'neoforge: 2048, forge: 2048 }' \
  'neoforge: 2048 }' \
  web/src/lib/lib.test.ts 'fewer players on a mod loader'
webcontrol "with seven server types, the rest fill whole rows" web/src/components/app/create.tsx \
  'const wide = i === 0 && types.length % 2 === 1' \
  'const wide = false' \
  web/src/pages/pages.test.tsx 'whole rows'
webcontrol "removing an add-on from a mod loader says a mod" web/src/lib/phase.ts \
  "op.kind === 'remove-addon' && addonKind(server.type) === 'mods' ? 'op.remove-mod'" \
  "op.kind === 'remove-addon' && false ? 'op.remove-mod'" \
  web/src/lib/lib.test.ts 'removed from a mod loader'
control "free addresses: a names service that never answers is reported within the check's wait" internal/agent/address.go \
  'ctx, cancel := context.WithTimeout(ctx, a.opts.NamesCheckWait)' \
  'ctx, cancel := context.WithCancel(ctx)' \
  ./internal/agent '^TestANamesServiceThatNeverAnswersIsReportedInTime$'
webcontrol "free addresses: a names service that can't be used is one quiet line, not a failure block" web/src/pages/machine-settings/free.tsx \
  "const unavailable = failed?.failure.error.code === 'names_unreachable' ? failed : undefined" \
  "const unavailable = failed?.failure.error.code === 'names_unreachable' && false ? failed : undefined" \
  web/src/pages/machine-settings/address.test.tsx 'one quiet line'
webcontrol "free addresses: a claim the service stops answering is the same quiet line" web/src/pages/machine-settings/free.tsx \
  "const unavailable = failed?.failure.error.code === 'names_unreachable' ? failed : undefined" \
  "const unavailable = !claimFailed && failed?.failure.error.code === 'names_unreachable' ? failed : undefined" \
  web/src/pages/machine-settings/address.test.tsx 'between the check and the claim'
webcontrol "free addresses: Claim says why it waits while the service can't be used" web/src/pages/machine-settings/free.tsx \
  "const reason = unavailable ? t('address.unavailable') : claimReason(address, problem, mine, answer)" \
  'const reason = claimReason(address, problem, mine, answer)' \
  web/src/pages/machine-settings/address.test.tsx 'one quiet line'
webcontrol "free addresses: names that can't be had now show as a preview only" web/src/pages/machine-settings/free.tsx \
  'const preview = !name || unavailable' \
  'const preview = !name' \
  web/src/pages/machine-settings/address.test.tsx 'one quiet line'
webcontrol "free addresses: a working name says the service isn't answering" web/src/pages/machine-settings/free.tsx \
  ') : a.names.unreachable ? (' \
  ') : false ? (' \
  web/src/pages/machine-settings/address.test.tsx 'one quiet line when the service'
webcontrol "free addresses: one notice at a time while the service isn't answering" web/src/pages/machine-settings/free.tsx \
  '            <UnreachableNotice />' \
  '            <><UnreachableNotice /><ServersWaitNotice a={a} machine={machine} /></>' \
  web/src/pages/machine-settings/address.test.tsx 'one notice at a time'
webcontrol "free addresses: Release while the service isn't answering says so in the same line" web/src/pages/machine-settings/free.tsx \
  "} else if (e instanceof ApiError && e.code === 'names_unreachable') {" \
  "} else if (e instanceof ApiError && e.code === 'names_unreachable' && false) {" \
  web/src/pages/machine-settings/address.test.tsx 'answers Release with the same one line'
webcontrol "free addresses: a certificate problem is the notice that shows" web/src/pages/machine-settings/free.tsx \
  'certProblemText(a, now) ? (' \
  'certProblemText(a, now) && !a.names.unreachable ? (' \
  web/src/pages/machine-settings/address.test.tsx 'as the one notice'

# Wave 9: the updater's sandbox (the 0.4.0 docs check).
control "the updater can't write the rest of the host" internal/install/units.go \
  'ProtectSystem=strict
ReadWritePaths=/usr/local/bin /etc/systemd/system /etc/playkeeper /var/lib/playkeeper' \
  'ProtectSystem=full
ReadWritePaths=/usr/local/bin /etc/systemd/system /etc/playkeeper /var/lib/playkeeper' \
  ./internal/install '^TestTheUpdaterWritesOnlyItsOwnPaths$'

# Wave 9: fixes from the screen review of Waves 5-8.
webcontrol "the players chart keeps its labels clear of now" web/src/components/app/players-chart.tsx \
  'if (count - index - 0.5 < count * nowReserve) return undefined' \
  'if (false) return undefined' \
  web/src/lib/lib.test.ts 'clear of now'
webcontrol "a joined machine's details say its agent stopped answering" web/src/pages/machines.tsx \
  ": agentSilent(m) ? { title: t('machines.problem.agentDown', { name })" \
  ": false ? { title: t('machines.problem.agentDown', { name })" \
  web/src/pages/pages.test.tsx 'stopped answering, as the sidebar'
webcontrol "a World tab without backups links to backup rules" web/src/pages/server/world-links.tsx \
  '      <DesktopLink server={server} sub="backup-rules" icon={<SlidersHorizontalIcon />} title={t('"'"'world.rules'"'"')} line={t('"'"'world.rulesLine'"'"')} />
' \
  '' \
  web/src/pages/server/world.test.tsx 'backup rules and your own world'
webcontrol "a phone's World tab without backups links to backup rules" web/src/pages/server/world-links.tsx \
  '        <PhoneLink server={server} sub="backup-rules"' \
  '        <PhoneLink server={server} sub="packs"' \
  web/src/pages/server/world.test.tsx 'backup rules and your own world'
webcontrol "the shared map is unavailable, not loading, when its first answer fails" web/src/pages/public-map.tsx \
  'map.error?.status === 404 || (!!map.error && !last)' \
  'map.error?.status === 404' \
  web/src/pages/server/map.test.tsx 'first answer fails'

if [ "$bad" != 0 ]; then
  echo "some guards are not covered by a failing test"
  exit 1
fi
echo "every guard's test failed without it"

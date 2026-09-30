#!/usr/bin/env bash
# Negative controls: removes one safety guard at a time in a throwaway git
# worktree and runs the tests that cover it. Every run must FAIL; a control
# that still passes means the guard is untested. A control whose guard is no
# longer in its file doesn't stop the ones after it: every problem is listed
# again at the end, and the run fails. Nothing is committed.
# The worktree is made from HEAD, so commit changes before running it.
# Usage: scripts/negative-controls.sh
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
export PATH="$root/.tools/go/bin:$root/.tools/node/bin:$PATH" CGO_ENABLED=0
tmp=$(mktemp -d)
wt=$tmp/playkeeper
git -C "$root" worktree add --detach -q "$wt" HEAD
trap 'git -C "$root" worktree remove --force "$wt"; rm -rf "$tmp"' EXIT
cd "$wt"
# The web controls run vitest with the checkout's own dependencies, which
# scripts/setup.sh installs.
if [ -d "$root/web/node_modules" ]; then
  ln -s "$root/web/node_modules" web/node_modules
fi
echo "negative controls at $(git rev-parse --short=12 HEAD)"

bad=0
problems=()
# problem prints what went wrong with a control and keeps it for the end.
problem() { # LINE
  echo "$1"
  problems+=("$1")
  bad=1
}
# mutate removes a control's guard: it replaces the first FROM in FILE with
# TO. When FILE is gone or has no FROM, the code moved under the control, so
# it says so and fails with FILE unchanged, and the next control runs.
mutate() { # NAME FILE FROM TO
  if [ ! -f "$2" ]; then
    problem "STALE    $1: $2 is gone"
    return 1
  fi
  if ! FROM=$3 TO=$4 perl -0pi -e 's/\Q$ENV{FROM}\E/$ENV{TO}/ or die' "$2" 2>/dev/null; then
    git checkout -q -- "$2"
    problem "STALE    $1: its guard is no longer in $2"
    return 1
  fi
}
control() { # NAME FILE FROM TO PACKAGE TESTS [RUNS]
  local name=$1 file=$2 pkg=$5 tests=$6 runs=${7:-1}
  mutate "$name" "$file" "$3" "$4" || return 0
  if ! go vet "$pkg" >/dev/null 2>&1; then
    problem "INVALID  $name: the mutated code does not build"
  elif go test -count="$runs" "$pkg" -run "$tests" >/tmp/negative-control.out 2>&1; then
    problem "MISSED   $name: $tests still pass without the guard"
  else
    echo "caught   $name: $(grep -m1 -E '^\s+[a-z0-9_]+_test\.go:[0-9]+:' /tmp/negative-control.out | sed 's/^\s*//')"
  fi
  git checkout -q -- "$file"
}

# webcontrol is the one web control: TEST-FILE is under web/, written with or
# without that prefix, and TESTS, when given, picks tests by name. Vitest
# decides, not the type checker, since a mutation may leave a name unused.
webcontrol() { # NAME FILE FROM TO TEST-FILE [TESTS]
  local name=$1 file=$2 testfile=${5#web/} tests=${6:-}
  local only=()
  if [ -n "$tests" ]; then only=(-t "$tests"); fi
  if [ ! -e web/node_modules ] && [ -d "$root/web/node_modules" ]; then
    ln -s "$root/web/node_modules" web/node_modules
  fi
  if [ ! -d web/node_modules ]; then
    problem "INVALID  $name: web/node_modules is missing; run scripts/setup.sh"
    return
  fi
  mutate "$name" "$file" "$3" "$4" || return 0
  # Vitest leaves a copy of every module it loaded in a folder under TMPDIR,
  # about 9 MB a run, so each run has a TMPDIR of its own, deleted after it.
  mkdir -p "$tmp/vitest"
  if (cd web && TMPDIR="$tmp/vitest" npx vitest run "$testfile" "${only[@]}" >/tmp/negative-control.out 2>&1); then
    problem "MISSED   $name: ${tests:-$testfile} still passes without the guard"
  elif ! grep -qE 'Tests +[0-9]+ failed' /tmp/negative-control.out; then
    problem "INVALID  $name: no test ran to fail"
  else
    echo "caught   $name: $(grep -m1 -E '^(AssertionError|Error): |^ *(FAIL|×) ' /tmp/negative-control.out | sed 's/^ *//' | cut -c1-200)"
  fi
  rm -rf "$tmp/vitest"
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
webcontrol "a schedule preview that was refused leaves Save on" web/src/pages/server/schedules.tsx \
  'if (!cancelled) setPreview(undefined)' \
  "if (!cancelled) setPreview({ valid: false, nextRuns: [], error: { error: 'refused', code: 'rate_limited' } })" \
  web/src/pages/server/schedules.test.tsx 'leaves Save on for a preview over the rate limit'
control "agent socket peer allowlist" internal/agent/agent.go \
  'if err != nil || !a.allowed[uid] {' \
  'if false && (err != nil || !a.allowed[uid]) {' \
  ./internal/agent '^TestSocketRejectsUnlistedPeers$'
control "agent closed operation list" internal/agent/agent.go \
  'writeErr(w, http.StatusNotFound, api.CodeNotFound, "Unknown agent operation.", "")' \
  'writeJSON(w, http.StatusOK, map[string]any{"ok": true})' \
  ./internal/agent '^TestInvalidInputsAndUnknownVerbsAreRejected$'
control "the agent closes its database once no connection is in use" internal/agent/agent.go \
  '	a.waitDBIdle(5 * time.Second)
	a.db.Close()' \
  '	a.db.Close()' \
  ./internal/agent '^TestCloseWaitsForTheDatabaseConnectionsInUse$'
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
control "a settle tried again says Playkeeper put the world back" internal/agent/backups.go \
  'movedBack := j.MovedBack || dirExists(s.copyPath(j.Aside))' \
  'movedBack := dirExists(s.copyPath(j.Aside))' \
  ./internal/agent '^TestASettleTriedAgainSaysPlaykeeperPutTheWorldBack$'
control "the journal records that Playkeeper moved the world back" internal/agent/backups.go \
  '		j.MovedBack = true
		if err := writeSwapJournal(stageDir, j); err != nil {' \
  '		j.MovedBack = true
		if err := error(nil); err != nil {' \
  ./internal/agent '^TestASettleTriedAgainSaysPlaykeeperPutTheWorldBack$'
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
webcontrol "New server suggests the memory a pack needs" web/src/pages/new-server.tsx \
  'if (step >= 3 && packMemory > 0 && !memoryPicked)' \
  'if (false && step >= 3 && packMemory > 0 && !memoryPicked)' \
  web/src/pages/new-server.test.tsx 'suggests the memory a pack needs'
webcontrol "New server warns when the machine can't give a pack the memory it needs" web/src/pages/new-server.tsx \
  'packMB > largest ? pack : undefined' \
  'packMB > largest && false ? pack : undefined' \
  web/src/pages/new-server.test.tsx 'give a pack the memory it needs'
webcontrol "a memory the user picked stays when the pack's plan answers later" web/src/pages/new-server.tsx \
  '                        setMemoryPicked(true)
' \
  '' \
  web/src/pages/new-server.test.tsx 'keeps a memory picked before the plan'
webcontrol "New server's memory step waits for the pack's plan before going on" web/src/pages/new-server.tsx \
  "t('new.noMemoryTitle')) : planPending ? t('reason.checkingPack') : undefined" \
  "t('new.noMemoryTitle')) : undefined" \
  web/src/pages/new-server.test.tsx 'holds Next on memory until'
webcontrol "a pack only picked on the first step doesn't size a server type" web/src/pages/new-server.tsx \
  'if (step >= 3 && packMemory > 0 && !memoryPicked)' \
  'if (packMemory > 0 && !memoryPicked)' \
  web/src/pages/new-server.test.tsx 'after a pack was only picked'
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
webcontrol "pre-generating's Start goes by the server's own machine" web/src/pages/server/world-pregen.tsx \
  "'pregen', place.offline)" \
  "'pregen', undefined)" \
  web/src/pages/server/joined-machine.test.tsx 'pre-generating and Save schedule say why they wait'
webcontrol "pre-generating's Pause and Cancel go by the server's own machine" web/src/pages/server/world-pregen.tsx \
  'const blocked = offline ?? (acting' \
  'const blocked = undefined ?? (acting' \
  web/src/pages/server/joined-machine.test.tsx 'pre-generating and Save schedule say why they wait'
webcontrol "a new schedule's Save goes by the server's own machine" web/src/pages/server/schedules.tsx \
  'const cantSave = offline ?? (' \
  'const cantSave = undefined ?? (' \
  web/src/pages/server/joined-machine.test.tsx 'pre-generating and Save schedule say why they wait'
webcontrol "New schedule goes by the server's own machine" web/src/pages/server/schedules.tsx \
  'if (offline) return offline' \
  'if (!offline && offline) return offline' \
  web/src/pages/server/joined-machine.test.tsx 'pre-generating and Save schedule say why they wait'
webcontrol "a pack's plan names the voice chat port the agent works out now" web/src/api/modpacks.ts \
  'if (hit && !fresh && Date.now() - hit.at < maxAge && tick === 0) {' \
  'if (hit && Date.now() - hit.at < maxAge && tick === 0) {' \
  web/src/api/modpacks.test.tsx 'asked for again'
webcontrol "New server's modpack search asks CurseForge once the machine offers it" web/src/api/modpacks.ts \
  "const curseforge = useModpacks(sources?.includes('curseforge') ? machineId : undefined, q, sort, 'curseforge')" \
  "const curseforge = useModpacks(undefined, q, sort, 'curseforge')" \
  web/src/pages/pages.test.tsx 'once the machine offers CurseForge'
webcontrol "a search the add-on library finds nothing for says where modpacks are chosen" web/src/pages/server/plugins/browse.tsx \
  "{text.trim() && canCreate(ws.me) && (" \
  "{false && text.trim() && canCreate(ws.me) && (" \
  web/src/pages/server/plugins/plugins.test.tsx 'sends a search for a modpack'
webcontrol "a page whose code doesn't load keeps the dashboard on screen" web/src/App.tsx \
  '      <LoadBoundary resetKey={JSON.stringify(route)}>
        <Suspense fallback={<PageSkeleton />}>
          <Appear>{page(route)}</Appear>
        </Suspense>
      </LoadBoundary>' \
  '      <Suspense fallback={<PageSkeleton />}>
        <Appear>{page(route)}</Appear>
      </Suspense>' \
  web/src/components/app/load-boundary.test.tsx 'page whose code never loads'
webcontrol "a server tab whose code doesn't load keeps the server page on screen" web/src/pages/server/index.tsx \
  '        <LoadBoundary>
          <Suspense fallback={<TabSkeleton />}>
            <Appear>{body}</Appear>
          </Suspense>
        </LoadBoundary>' \
  '        <Suspense fallback={<TabSkeleton />}>
          <Appear>{body}</Appear>
        </Suspense>' \
  web/src/components/app/load-boundary.test.tsx 'server tab whose code never loads'
webcontrol "every server tab's code loads after sign-in" web/src/App.tsx \
  '  void pages.server().then((m) => m.preloadTabs(), () => {})
' \
  '' \
  web/src/pages/server/preload.test.tsx 'every server tab'
webcontrol "the command palette's code stays out of the first screens" web/src/components/app/shell.tsx \
  "export const loadPalette = () => import('@/components/app/command-palette')" \
  "export const loadPalette = ((p) => () => p)(import('@/components/app/command-palette'))" \
  web/src/pages/server/preload.test.tsx 'every server tab'
webcontrol "the command palette's code loads after sign-in" web/src/App.tsx \
  'for (const load of [...Object.values(pages), loadPalette]) void load().catch(() => {})' \
  'for (const load of Object.values(pages)) void load().catch(() => {})' \
  web/src/pages/server/preload.test.tsx 'every server tab'
webcontrol "the command palette opens once its code loads" web/src/components/app/shell.tsx \
  '{used && (' \
  '{false && (' \
  web/src/pages/pages.test.tsx 'opens from the sidebar once its code loads'
webcontrol "the first screens' budget stays where it is" web/src/lib/first-load.ts \
  "bytes: 900_000, gzipBytes: 285_000" \
  "bytes: 910_000, gzipBytes: 285_000" \
  web/src/lib/first-load.test.ts 'over its budget'
webcontrol "any page's budget stays where it is" web/src/lib/first-load.ts \
  "bytes: 1_200_000, gzipBytes: 380_000" \
  "bytes: 1_210_000, gzipBytes: 380_000" \
  web/src/lib/first-load.test.ts 'over its budget'
webcontrol "code an update replaced reloads the page" web/src/components/app/load-boundary.tsx \
  '        sessionStorage.setItem(reloadedAt, String(Date.now()))
        window.location.reload()' \
  '        sessionStorage.setItem(reloadedAt, String(Date.now()))' \
  web/src/components/app/load-boundary.test.tsx 'at most once a minute'
webcontrol "a page that still can't load doesn't reload over and over" web/src/components/app/load-boundary.tsx \
  'if (!replaced || Date.now() - Number(sessionStorage.getItem(reloadedAt) ?? 0) < 60_000) return' \
  'if (!replaced) return' \
  web/src/components/app/load-boundary.test.tsx 'at most once a minute'
webcontrol "code that didn't load for another reason, as when Safari leaves the page, doesn't reload it" web/src/components/app/load-boundary.tsx \
  'void codeReplaced()' \
  'void Promise.resolve(true)' \
  web/src/components/app/load-boundary.test.tsx 'for another reason'
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
  "whyNot({ ...s, operation: otherJob }, 'pregen', place.offline)" \
  "whyNot({ ...s, operation: otherJob }, 'change', place.offline)" \
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
  '	if unsettled, _ := s.restoreUnsettled(); unsettled {
		return &apiError{Status: http.StatusConflict, Code: codeRestoreUnsettled,' \
  '	if unsettled, _ := s.restoreUnsettled(); unsettled && false {
		return &apiError{Status: http.StatusConflict, Code: codeRestoreUnsettled,' \
  ./internal/agent '^TestNoRestoreStartsWhileAnotherIsUnsettled$'
control "a restore from a backup waits for an unsettled one" internal/agent/handlers.go \
  'if err := s.restoreRefusal("restore again"); err != nil {
		s.audit(actor, "restore.staged", b.ID,' \
  'if err := s.restoreRefusal("restore again"); false && err != nil {
		s.audit(actor, "restore.staged", b.ID,' \
  ./internal/agent '^TestNoRestoreStartsWhileAnotherIsUnsettled$'
control "a restore from an upload waits for an unsettled one" internal/agent/handlers.go \
  'err := target.restoreRefusal("restore again")
		if err == nil && r.ContentLength > 0 {' \
  'err := error(nil)
		if err == nil && r.ContentLength > 0 {' \
  ./internal/agent '^TestNoRestoreStartsWhileAnotherIsUnsettled$'
control "a restore from an off-site copy waits for an unsettled one" internal/agent/offsite.go \
  'if err := s.restoreRefusal("restore again"); err != nil {' \
  'if err := s.restoreRefusal("restore again"); false && err != nil {' \
  ./internal/agent '^TestNoRestoreStartsWhileAnotherIsUnsettled$'
control "the status says a restore isn't settled" internal/agent/handlers.go \
  'if unsettled, _ := s.restoreUnsettled(); unsettled {
			st.RestoreUnsettled' \
  'if unsettled, _ := s.restoreUnsettled(); unsettled && false {
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
control "nothing makes a world folder while the restored world is only in its stage" internal/agent/lifecycle.go \
  'if staged := s.stagedRestoredWorld(); staged != "" {' \
  'if staged := s.stagedRestoredWorld(); false && staged != "" {' \
  ./internal/agent '^TestNothingMakesAWorldFolderWhileTheRestoredWorldIsInTheStage$'
control "a restored world still in its stage is never settled away" internal/agent/backups.go \
  '			case !dirExists(staged):
				return nil' \
  '			case !dirExists(staged) || dirExists(s.dataDir()):
				return nil' \
  ./internal/agent '^TestARestoredWorldInTheStageIsNeverSettledAway$'
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
  'case err == nil && (c.Config.Labels[labelSpec] != hash || c.Config.Labels[labelGCLog] != gcLogVersion || c.HostConfig.NanoCPUs != spec.HostConfig.NanoCPUs):' \
  'case err == nil && (c.Config.Labels[labelSpec] != hash || c.HostConfig.NanoCPUs != spec.HostConfig.NanoCPUs):' \
  ./internal/agent '^TestGCLogFlagAppliesFromTheNextStart$'
control "a GC log line still being written is not read" internal/agent/running.go \
  "end := bytes.LastIndexByte(buf, '\n')" \
  "end := max(bytes.LastIndexByte(buf, '\n'), len(buf))" \
  ./internal/agent '^TestGCLogIsReadOnce$'
control "the GC log cursor is stored with the pauses it read" internal/agent/running.go \
  'UPDATE servers SET gc_cursor = ? WHERE id = ?`, string(b), s.id)' \
  'UPDATE servers SET gc_cursor = ? WHERE id = ?`, string(b), "")' \
  ./internal/agent '^TestGCLogIsReadOnce$'
control "storing GC pauses waits for a write in progress" internal/agent/state.go \
  'c.ExecContext(ctx, `BEGIN IMMEDIATE' \
  'c.ExecContext(ctx, `BEGIN' \
  ./internal/agent '^TestStoringGCWaitsForAWriteInProgress$'
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
control "a later release of a supported distribution counts as newer, not unsupported" internal/platform/platform.go \
  'case c > 0:' \
  'case false:' \
  ./internal/platform '^TestLaterReleasesOfASupportedDistributionAreNewerNotRefused$'
control "the preflight refuses a release older than the oldest supported one" internal/install/oscheck.go \
  '	case v.Distro != nil:
		c.Status = "fail"' \
  '	case v.Distro != nil:
		c.Status = "warn"' \
  ./internal/install '^TestPreflightRefusesOlderReleasesAndOtherSystemsUnlessAllowed$'
control "Debian 13 gets the docker command, which it packages on its own" internal/install/packages.go \
  'if aptCandidate(sys, p) {' \
  'if false && aptCandidate(sys, p) {' \
  ./internal/install '^TestDebianGetsDockerFromItsOwnArchiveWithTheDockerCommand$'
# After the OS matrix's Debian 13 run on 6a8b8a11: apt is asked for the docker
# command only once its lists are fresh, as a new server's are empty.
control "apt is asked about the docker command after apt-get update" internal/install/packages.go \
  '	if _, err := aptGet(sys, out, "update"); err != nil {
		return err
	}
	for _, p := range optional {' \
  '	for _, p := range optional {
		if aptCandidate(sys, p) {
			pkgs = append(slices.Clone(pkgs), p)
		}
	}
	if _, err := aptGet(sys, out, "update"); err != nil {
		return err
	}
	for _, p := range optional[:0] {' \
  ./internal/install '^TestDebianGetsDockerFromItsOwnArchiveWithTheDockerCommand$'
control "a package that is only a name docker.io provides isn't installed" internal/install/oscheck.go \
  'return v != "" && v != "(none)"' \
  'return v != ""' \
  ./internal/install '^TestUbuntuDockerIOAlreadyHasTheDockerCommand$'
control "the preflight names an nftables firewall that drops incoming connections" internal/install/firewall.go \
  'strings.Contains(line, "policy drop")' \
  'strings.Contains(line, "policy dropped")' \
  ./internal/install '^TestPreflightNamesAFirewallOtherThanUFWThatDropsIncomingConnections$'
control "the sizing guide leaves out the memory Ubuntu sets aside for crash dumps" internal/sizing/numbers.go \
  'return min(mb*reportedPercent/100, mb*kernelPercent/100-crashKernelMB(memoryGB))' \
  'return mb * reportedPercent / 100' \
  ./internal/sizing '^TestReportedMemoryLeavesOutUbuntusCrashDumpMemory$'
control "preflight refuses what installing Docker CE would replace" internal/install/distro.go \
  'if installed[name] {' \
  'if false && installed[name] {' \
  ./internal/install '^TestPreflightRefusesWhatInstallingDockerCEWouldBreak$'
control "preflight refuses a Docker socket that answers as Podman" internal/install/install.go \
  'podman := derr == nil && di.Podman' \
  'podman := false' \
  ./internal/install '^TestPreflightRefusesWhatInstallingDockerCEWouldBreak$'
# After Bugbot's finding on b1557e2e: the fix for Podman's Docker socket names
# the host's package manager, not dnf on every system.
control "the Podman socket's fix names the host's package manager" internal/install/install.go \
  'how = " (" + fam.pm.uninstallHint("podman-docker") + ")"' \
  'how = " (" + dnf{}.uninstallHint("podman-docker") + ")"' \
  ./internal/install '^TestThePodmanSocketFixUsesTheHostsPackageManager$'
control "the RHEL family trusts only the Docker key Playkeeper carries" internal/install/distro.go \
  'gpgcheck=1\ngpgkey=file://%s\n' \
  'gpgcheck=0\ngpgkey=file://%s\n' \
  ./internal/install '^TestDockerCEInstallsFromDockersRepositoryAndLeavesWithIt$'
control "uninstall keeps the packages of Docker's that other software needs" internal/install/uninstall.go \
  'if len(kept) > 0 {' \
  'if false && len(kept) > 0 {' \
  ./internal/install '^TestUninstallKeepsWhatOtherSoftwareNeedsOfDocker$'
control "uninstall keeps a Docker that other software needs" internal/install/uninstall.go \
  'if slices.ContainsFunc(kept, func(p string) bool { return slices.Contains(dockerEngines, p) }) {' \
  'if false && slices.ContainsFunc(kept, func(p string) bool { return slices.Contains(dockerEngines, p) }) {' \
  ./internal/install '^TestUninstallKeepsDockerWhenOtherSoftwareNeedsIt$'
control "dnf removes only what rpm says nothing else needs" internal/install/packages.go \
  '	if o, err := sys.Run("rpm", append([]string{"-e", "--test"}, pkgs...)...); err != nil {
		if n := needs(o + "\n" + err.Error()); len(n) > 0 {' \
  '	if o, err := sys.Run("rpm", append([]string{"-e", "--test"}, pkgs...)...); false && err != nil {
		if n := needs(o + "\n" + err.Error()); len(n) > 0 {' \
  ./internal/install '^TestDNFRemoveRefusesWhatOtherSoftwareNeeds$'
control "firewalld: uninstall leaves the rules the admin had" internal/install/firewall.go \
  'added := strings.TrimSpace(out) != "yes"' \
  'added := strings.TrimSpace(out) != "yes" || true' \
  ./internal/install '^TestFirewalldOpensPortsInTheZoneAndUninstallLeavesTheAdminsRules$'
control "firewalld: Docker's zone and policy go with Docker" internal/install/install.go \
  'errs = append(errs, removeFiles(sys, m.DockerRepoFiles), removeFirewalld(sys, m.DockerFirewalld))' \
  'errs = append(errs, removeFiles(sys, m.DockerRepoFiles))' \
  ./internal/install '^TestFirewalldOpensPortsInTheZoneAndUninstallLeavesTheAdminsRules$'
control "sudo finds playkeeper where its path leaves out /usr/local/bin" internal/install/install.go \
  'f.SudoLink = sudoMissesBin(sys)' \
  'f.SudoLink = false && sudoMissesBin(sys)' \
  ./internal/install '^TestDockerCEInstallsFromDockersRepositoryAndLeavesWithIt$'
control "RHEL 10 gets the running kernel's netfilter modules for Docker" internal/install/distro.go \
  'if major == "10" {' \
  'if false && major == "10" {' \
  ./internal/install '^TestEL10GetsTheRunningKernelsNetfilterModulesForDocker$'
control "what Docker added to firewalld is recorded when it then fails to start" internal/install/install.go \
  '			if zonesBefore != nil {
				now := firewalldHas(sys)' \
  '			if err == nil && zonesBefore != nil {
				now := firewalldHas(sys)' \
  ./internal/install '^TestADockerThatFailsToStartLeavesFirewalldAsItWas$'
control "uninstall leaves firewalld's zone as it was" internal/install/uninstall.go \
  '	note(fw.tidy(sys, *m))' \
  '' \
  ./internal/install '^TestAZoneFirewalldReadFromItsDefaultsIsLeftAsItWas$'
control "a zone the admin changed after the install keeps its settings" internal/install/firewall.go \
  'err == nil && now == m.FirewallZoneBefore {' \
  'err == nil && (now == m.FirewallZoneBefore || true) {' \
  ./internal/install '^TestAZoneFirewalldReadFromItsDefaultsIsLeftAsItWas$'
control "firewalld's backups of Docker's zone and policy go too" internal/install/firewall.go \
  'removeIfExists(sys.P(dir+"/"+name+".xml.old"))' \
  'removeIfExists(sys.P(dir+"/"+name+".xml.missing"))' \
  ./internal/install '^TestFirewalldOpensPortsInTheZoneAndUninstallLeavesTheAdminsRules$'
control "a failed Docker install removes the repository it added" internal/install/install.go \
  '				return removeFiles(sys, in.m.DockerRepoFiles)' \
  '				return nil' \
  ./internal/install '^TestAFailedDockerInstallRemovesTheRepositoryItAdded$'
control "Docker's repository and key go with Docker" internal/install/install.go \
  'errs = append(errs, removeFiles(sys, m.DockerRepoFiles), removeFirewalld(sys, m.DockerFirewalld))' \
  'errs = append(errs, removeFirewalld(sys, m.DockerFirewalld))' \
  ./internal/install '^TestDockerCEInstallsFromDockersRepositoryAndLeavesWithIt$'
control "a Docker that labels containers for SELinux relabels a server's binds" internal/agent/lifecycle.go \
  'if s.selinuxLabels() {' \
  'if false && s.selinuxLabels() {' \
  ./internal/agent '^TestBindsAreRelabelledOnlyForADockerThatUsesSELinux$'
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
# Asked before the backup looks at the server, busy misses a stop or restart
# that begins meanwhile and is why it isn't online; the sleep holds that gap
# open.
control "a backup racing a stop or restart answers busy" internal/agent/handlers.go \
  'if !req.Stopped {
		if _, running, err := s.containerRunning(r.Context()); err == nil && running && !s.online(r.Context()) && !s.busy() {' \
  'if !req.Stopped && !s.busy() {
		time.Sleep(50 * time.Millisecond)
		if _, running, err := s.containerRunning(r.Context()); err == nil && running && !s.online(r.Context()) {' \
  ./internal/agent '^TestConcurrentOperationsAreSerialized$' 3

control "release manifest signature" internal/update/manifest.go \
  'if !verified {' \
  'if false && !verified {' \
  ./internal/update '^TestOnlyManifestsSignedByATrustedKeyAreAccepted$'
control "an update takes this platform's tarball" internal/update/manifest.go \
  'case a.File != TarballName(platform):' \
  'case false && a.File != TarballName(platform):' \
  ./internal/update '^TestSignedButMalformedManifestsAreRefused$'
control "a release names a build for every platform" internal/update/manifest.go \
  'if !slices.Equal(got, Platforms) {' \
  'if false && !slices.Equal(got, Platforms) {' \
  ./internal/update '^TestManifestsFromLaterReleasesStillUpdateThisPlatform$'
control "the preflight refuses a 32-bit system on a 64-bit CPU" internal/install/install.go \
  'case archNames[arch] != "" && userland32(sys):' \
  'case false && archNames[arch] != "" && userland32(sys):' \
  ./internal/install '^TestPreflightRefusesA32BitSystemOnA64BitCPU$'
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
# Checks for a new release in the background (0.4.9): when the agent starts
# and about every 30 minutes, asking only whether the release changed, one
# at a time, off with the owner's switch, and the notice only for those who
# can install it.
control "a check asks only whether the release changed" internal/update/fetch.go \
  'req.Header.Set(k, v)' \
  '_, _ = k, v' \
  ./internal/update '^TestAReleaseThatDidNotChangeCostsOneNotModified$'
control "the same signature again spares the manifest" internal/update/fetch.go \
  'if sig == nil || (have && bytes.Equal(sig, prev.Signature)) {' \
  'if sig == nil || (false && bytes.Equal(sig, prev.Signature)) {' \
  ./internal/update '^TestTheSameSignatureAgainSparesTheManifest$'
control "a busy release location's Retry-After is kept" internal/update/fetch.go \
  'se.RetryAfter = retryAfter(resp.Header, time.Now())' \
  'se.RetryAfter = 0' \
  ./internal/update '^TestALocationsRetryAfterIsKept$'
control "checks drift apart at random" internal/agent/update.go \
  'wait = time.Duration(float64(wait) * (1 - checkJitter + 2*checkJitter*r))' \
  'wait = time.Duration(float64(wait) * (1 - checkJitter + 2*checkJitter*0.5))' \
  ./internal/agent '^TestChecksWaitAboutTheIntervalGiveOrTakeAFifth$'
control "failed checks back off" internal/agent/update.go \
  'wait *= 2' \
  'wait *= 1' \
  ./internal/agent '^(TestFailedChecksBackOff|TestAFailingSourceIsAskedLessOften)$'
control "a check waits at least a source's Retry-After" internal/agent/update.go \
  'return max(wait, retryAfter)' \
  'return wait' \
  ./internal/agent '^(TestFailedChecksBackOff|TestAFailingSourceIsAskedLessOften)$'
control "the first check comes at a random moment of the first minute" internal/agent/update.go \
  'u.next = a.now().Add(time.Duration(a.opts.UpdateJitter() * float64(a.opts.UpdateCheckFirst)))' \
  'u.next = a.now().Add(a.opts.UpdateCheckFirst)' \
  ./internal/agent '^TestTheFirstCheckComesAtARandomMomentOfTheFirstMinute$'
control "one check however many tabs are open" internal/agent/update.go \
  'if running := u.checking; running != nil {' \
  'if running := u.checking; false && running != nil {' \
  ./internal/agent '^TestOneCheckHoweverManyTabsAreOpen$'
control "the automatic check asks only whether the release changed" internal/agent/update.go \
  'a.checkUpdate(true)' \
  'a.checkUpdate(false)' \
  ./internal/agent '^TestTheAgentChecksWhenItStartsAndAboutEveryInterval$'
control "turned off, the agent doesn't check by itself" internal/agent/update.go \
  'due := a.opts.UpdateCheckInterval > 0 && !u.off && !a.now().Before(u.next)' \
  'due := a.opts.UpdateCheckInterval > 0 && !a.now().Before(u.next)' \
  ./internal/agent '^TestTurnedOffTheAgentChecksOnlyWhenAsked$'
control "a joined machine leaves the checks to its dashboard" internal/agent/update.go \
  'return due && a.updatesUnsupported() == "" && !a.joined()' \
  'return due && a.updatesUnsupported() == ""' \
  ./internal/agent '^TestAJoinedMachineLeavesTheChecksToItsDashboard$'
control "checks ask GitHub when playkeeper.io doesn't answer" internal/agent/update.go \
  'return []update.Source{{BaseURL: update.CheckURL, Client: a.opts.HTTPClient}, a.updateSource()}' \
  'return []update.Source{{BaseURL: update.CheckURL, Client: a.opts.HTTPClient}}' \
  ./internal/agent '^TestChecksAskPlaykeeperIoThenGitHub$'
control "playkeeper.io failing backs checks off, though GitHub answered" internal/agent/update.go \
  'firstFailed = firstFailed || i == 0' \
  'firstFailed = firstFailed && i == 0' \
  ./internal/agent '^TestChecksAskPlaykeeperIoThenGitHub$'
control "what a check kept survives a restart" internal/agent/update.go \
  'u.latest, u.checkedAt, u.checkErr, u.cached = s.Latest, s.CheckedAt, s.Error, s.Cached' \
  'u.latest, u.checkedAt, u.checkErr = s.Latest, s.CheckedAt, s.Error' \
  ./internal/agent '^TestAfterARestartTheCheckAsksOnlyWhetherTheReleaseChanged$'
control "creators and customers never hear of a release" internal/panel/workspace.go \
  'if permit(sess.Access, actViewMachines, "") != nil {' \
  'if false {' \
  ./internal/panel '^TestOnlyThoseWhoCanUpdateHearOfARelease$'
control "the automatic check's switch needs the rights to manage the machine" internal/panel/server.go \
  'mm("PUT", "/api/machines/{mid}/update/auto", "/v1/update/auto", actManageMachine),' \
  'mm("PUT", "/api/machines/{mid}/update/auto", "/v1/update/auto", actView),' \
  ./internal/panel '^TestTheAutomaticCheckSwitchIsForThoseWhoCanUpdate$'
control "playkeeper.io puts the manifest in place before its signature" cmd/release-mirror/main.go \
  '	if err := m.rename(tmpManifest, update.ManifestFile); err != nil {
		return "", false, err
	}
	if err := m.rename(tmpSig, update.SignatureFile); err != nil {' \
  '	if err := m.rename(tmpSig, update.SignatureFile); err != nil {
		return "", false, err
	}
	if err := m.rename(tmpManifest, update.ManifestFile); err != nil {' \
  ./cmd/release-mirror '^TestTheSiteServesOnlyReleasesThatVerify$'
control "playkeeper.io leaves a release that didn't change alone" cmd/release-mirror/main.go \
  'if had && bytes.Equal(was.manifest, rel.Raw) && bytes.Equal(was.signature, rel.Signature) {' \
  'if false && bytes.Equal(was.manifest, rel.Raw) && bytes.Equal(was.signature, rel.Signature) {' \
  ./cmd/release-mirror '^TestTheSiteServesOnlyReleasesThatVerify$'
webcontrol "only those who can install a release are told of it" web/src/components/app/update.tsx \
  "ws.prefsLoading || !can(ws.me, 'machine.manage') || ws.prefs[dismissedKey] === version" \
  "ws.prefsLoading || ws.prefs[dismissedKey] === version" \
  web/src/components/app/update-notice.test.tsx 'never shows to'
webcontrol "a dismissed notice stays away for its release" web/src/components/app/update.tsx \
  "ws.prefs[dismissedKey] === version) return undefined" \
  "ws.prefs[dismissedKey] === 'never') return undefined" \
  web/src/components/app/update-notice.test.tsx 'goes when dismissed'
webcontrol "a dismissed notice doesn't flash while the preferences load" web/src/components/app/update.tsx \
  "ws.updating || ws.prefsLoading ||" \
  "ws.updating ||" \
  web/src/components/app/update-notice.test.tsx 'waits for the preferences'
webcontrol "the phone's More dot is the notice's" web/src/components/app/shell.tsx \
  "const updateDot = !!notice || (!!ws.updating && can(ws.me, 'machine.manage'))" \
  "const updateDot = !!ws.machine?.live?.updateAvailable || !!ws.updating" \
  web/src/components/app/update-notice.test.tsx 'More tab'
webcontrol "the automatic check's switch is only for those who manage the machine" web/src/pages/settings.tsx \
  "{manage && info?.supported && (" \
  "{info?.supported && (" \
  web/src/pages/settings.test.tsx 'there for those who'
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
  '"POST /v1/servers/{id}/datapacks":                   true,' \
  '"POST /v1/servers/{id}/datapacks":                   false,' \
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
  'func(act string) bool { return permit(account, action(act), "") == nil },' \
  'func(act string) bool { return permit(account, action(act), "") == nil || true },' \
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
# The Files tab: every path stays inside the server's folder, nothing is
# reached through a link or special file on the way, the world waits for the
# game to stop, and only admins get in.
control "file browser: a folder is listed only through real folders" internal/gamefiles/browse.go \
  'fi, err := d.folder(name)' \
  'fi, err := d.root.Lstat(name)' \
  ./internal/gamefiles '^TestBrowsingRefusesLinksOnTheWay$'
control "file browser: a new folder is made only through real folders" internal/gamefiles/browse.go \
  'if _, err := d.folders(ps[:len(ps)-1], true); err != nil {' \
  'if _, err := d.folders(ps[:len(ps)-1], true); false && err != nil {' \
  ./internal/gamefiles '^TestBrowsingRefusesLinksOnTheWay$'
control "file browser: a move starts only from behind real folders" internal/gamefiles/browse.go \
  'ffi, err := d.folders(fps[:len(fps)-1], false)
	if err != nil {' \
  'ffi, err := d.folders(fps[:len(fps)-1], false)
	if false && err != nil {' \
  ./internal/gamefiles '^TestBrowsingRefusesLinksOnTheWay$'
control "file browser: a move goes only into real folders" internal/gamefiles/browse.go \
  'tfi, err := d.folders(tps[:len(tps)-1], false)
	if err != nil {' \
  'tfi, err := d.folders(tps[:len(tps)-1], false)
	if false && err != nil {' \
  ./internal/gamefiles '^TestBrowsingRefusesLinksOnTheWay$'
control "file browser: a delete reaches only through real folders" internal/gamefiles/browse.go \
  'if _, err := d.folders(ps[:len(ps)-1], false); err != nil {' \
  'if _, err := d.folders(ps[:len(ps)-1], false); false && err != nil {' \
  ./internal/gamefiles '^TestBrowsingRefusesLinksOnTheWay$'
control "file browser: an upload goes in only through real folders" internal/gamefiles/browse.go \
  'pfi, err := d.folders(ps[:len(ps)-1], true)' \
  'pfi, err := d.root.Lstat(path.Join(ps[:len(ps)-1]...))' \
  ./internal/gamefiles '^TestLinksAreRefusedAtEveryStep$'
control "file browser: an upload never replaces a link or special file" internal/gamefiles/browse.go \
  'if err := fileError(name, fi); err != nil {' \
  'if err := fileError(name, fi); false && err != nil {' \
  ./internal/gamefiles '^TestLinksAreRefusedAtEveryStep$'
control "file browser: a rename never replaces what appeared at its name meanwhile" internal/gamefiles/place_linux.go \
  'unix.RENAME_NOREPLACE)' \
  '0)' \
  ./internal/gamefiles '^TestChangesNeverReplaceWhatAppearsMeanwhile$'
control "file browser: an upload that may not replace renames without replacing" internal/gamefiles/browse.go \
  'err = renameInto(staged, pf, path.Base(name), replace)' \
  'err = renameInto(staged, pf, path.Base(name), true)' \
  ./internal/gamefiles '^TestChangesNeverReplaceWhatAppearsMeanwhile$'
control "file browser: a new file renames without replacing" internal/gamefiles/gamefiles.go \
  'if fresh {
			err = d.renameNew(tmp, pfi, name, pfi)' \
  'if false && fresh {
			err = d.renameNew(tmp, pfi, name, pfi)' \
  ./internal/gamefiles '^TestChangesNeverReplaceWhatAppearsMeanwhile$'
control "file browser: a zip's files are counted only up to its limit" internal/gamefiles/browse.go \
  'if more {
		*n = limit + 1' \
  'if false && more {
		*n = limit + 1' \
  ./internal/gamefiles '^TestCountStopsPastTheLimit$'
control "file browser: a zip's count stops far down" internal/gamefiles/browse.go \
  'if depth > maxDepth {
		return tooDeepError(start, maxDepth)
	}
	entries, more, err := d.List(p, limit-*n)' \
  'if false && depth > maxDepth {
		return tooDeepError(start, maxDepth)
	}
	entries, more, err := d.List(p, limit-*n)' \
  ./internal/gamefiles '^TestWalkAndCountStopFarDown$'
control "file browser: a zip's walk stops far down" internal/gamefiles/browse.go \
  'if depth > maxDepth {
		return tooDeepError(start, maxDepth)
	}
	entries, more, err := d.List(p, limit)' \
  'if false && depth > maxDepth {
		return tooDeepError(start, maxDepth)
	}
	entries, more, err := d.List(p, limit)' \
  ./internal/gamefiles '^TestWalkAndCountStopFarDown$'
control "game files: the hidden file a write goes through fits the longest name" internal/gamefiles/gamefiles.go \
  'if len(tmp) > maxNameBytes {' \
  'if false && len(tmp) > maxNameBytes {' \
  ./internal/gamefiles '^TestLongNamesCanBeWritten$'
control "file browser: a path from the dashboard is relative and has no dot segments" internal/agent/files.go \
  'case len(raw) > maxPathBytes || !fs.ValidPath(raw) || strings.ContainsRune(raw, 0):' \
  'case len(raw) > maxPathBytes || strings.ContainsRune(raw, 0):' \
  ./internal/agent '^TestFileBrowserPathsStayInsideTheServersFolder$'
control "file browser: the world can't change while the game runs" internal/agent/files.go \
  'if inWorld(p, worlds) && s.gameRunning(ctx) {' \
  'if false && inWorld(p, worlds) && s.gameRunning(ctx) {' \
  ./internal/agent '^TestTheWorldIsReadOnlyWhileTheGameRuns$'
control "file browser: the editor opens world files read-only while the game runs" internal/agent/files.go \
  'if running && inWorld(p, s.worldFolders(d)) {' \
  'if false && running && inWorld(p, s.worldFolders(d)) {' \
  ./internal/agent '^TestTheWorldIsReadOnlyWhileTheGameRuns$'
control "file browser: a plugin's world is read-only while the game runs too" internal/agent/files.go \
  'if fi, err := d.Lstat(e.Name + "/level.dat"); err == nil && fi.Mode().IsRegular() {' \
  'if fi, err := d.Lstat(e.Name + "/level.dat"); false && err == nil && fi.Mode().IsRegular() {' \
  ./internal/agent '^TestTheWorldIsReadOnlyWhileTheGameRuns$'
control "file browser: nothing changes while the server is busy" internal/agent/files.go \
  'if s.busy() {' \
  'if false && s.busy() {' \
  ./internal/agent '^TestFileBrowserWaitsForTheServersOperation$'
control "file browser: a change holds off the server's operations until it is done" internal/agent/files.go \
  'rel, ok := s.holdOpLock()' \
  'rel, ok := func() {}, true' \
  ./internal/agent '^TestFileChangesHoldOffOperations$'
control "file browser: changes share the hold rather than refuse each other" internal/agent/files.go \
  'if h.n == 0 {' \
  'if true {' \
  ./internal/agent '^TestFileChangesHoldOffOperations$'
control "file browser: a move holds off operations until it is done" internal/agent/files.go \
  'release, err := s.holdFiles(r.Context(), d, all...)' \
  'release, err := func() {}, s.changeRefusal(r.Context(), d, all...)' \
  ./internal/agent '^TestFileChangesHoldOffOperations$'
control "file browser: a delete holds off operations until it is done" internal/agent/files.go \
  'release, err := s.holdFiles(r.Context(), d, paths...)' \
  'release, err := func() {}, s.changeRefusal(r.Context(), d, paths...)' \
  ./internal/agent '^TestFileChangesHoldOffOperations$'
control "file browser: an upload holds off operations while it is put in place" internal/agent/fileuploads.go \
  'release, err := s.holdFiles(ctx, d, dest)' \
  'release, err := func() {}, s.changeRefusal(ctx, d, dest)' \
  ./internal/agent '^TestFileChangesHoldOffOperations$'
control "file browser: a long delete holds off operations after its answer too" internal/agent/files.go \
  'Continuing: true})' \
  'Continuing: true})
		release()' \
  ./internal/agent '^TestALongDeleteCarriesOnAfterItsAnswer$'
control "file browser: the editor saves only over the version it opened" internal/agent/files.go \
  'if expect != "" && contentVersion(cur) != expect {' \
  'if false && expect != "" && contentVersion(cur) != expect {' \
  ./internal/agent '^TestFileBrowserListsOpensAndSavesTheServersFiles$'
control "file browser: of two saves of one version the second is refused" internal/agent/files.go \
  's.fileHold.save.Lock()
		defer s.fileHold.save.Unlock()' \
  '_ = &s.fileHold.save' \
  ./internal/agent '^TestTwoSavesOfOneVersionCantBothWin$'
control "file browser: a zip too large for the agent to keep track of is refused before it starts" internal/agent/files.go \
  'if total += n; total > maxZipped {' \
  'if total += n; false && total > maxZipped {' \
  ./internal/agent '^TestAZipOfTooManyFilesIsRefusedBeforeItStarts$'
control "file browser: a zip's files are named safely for any unpacker" internal/agent/files.go \
  'Name: zipName(rel), Modified: st.ModTime()' \
  'Name: rel, Modified: st.ModTime()' \
  ./internal/agent '^TestZipNamesUnpackSafelyAnywhere$'
control "file browser: a zip's folders are named safely for any unpacker" internal/agent/files.go \
  'Name: zipName(rel) + "/"' \
  'Name: rel + "/"' \
  ./internal/agent '^TestZipNamesUnpackSafelyAnywhere$'
control "file browser: a download stops at a file that got shorter" internal/agent/files.go \
  'if err == nil && n != size {' \
  'if false && err == nil && n != size {' \
  ./internal/agent '^TestADownloadStopsAtAFileThatGotShorter$'
control "file browser: an upload replaces a file only when asked" internal/agent/fileuploads.go \
  'case !replace:' \
  'case false && !replace:' \
  ./internal/agent '^(TestFileUploadsCarryOnAfterADroppedConnection|TestAnUploadThatCantBePutInPlaceCanBeTriedAgain)$'
control "file browser: an upload carries on only from the byte it has" internal/agent/fileuploads.go \
  'if offset != f.received {' \
  'if false && offset != f.received {' \
  ./internal/agent '^TestFileUploadsCarryOnAfterADroppedConnection$'
control "file browser: one server's uploads can't take them all" internal/agent/fileuploads.go \
  'if all < maxFileUploads && mine < maxServerUploads {' \
  'if all < maxFileUploads {' \
  ./internal/agent '^TestUploadsAreSharedOutBetweenServers$'
control "file browser: a finished upload doesn't count against the limits" internal/agent/fileuploads.go \
  'if up.unfinished() {' \
  'if true {' \
  ./internal/agent '^TestUploadsAreSharedOutBetweenServers$'
control "file browser: a file is put in place once" internal/agent/fileuploads.go \
  'f.placed || f.placing || f.received < f.size' \
  'f.placed || f.received < f.size' \
  ./internal/agent '^TestAFileIsPutInPlaceOnce$'
control "file browser: a cancelled upload puts nothing more in place" internal/agent/fileuploads.go \
  'up.gone || f.placed' \
  'f.placed' \
  ./internal/agent '^TestACancelledUploadPutsNothingInPlace$'
control "recent activity reads on past a big upload" internal/agent/analytics.go \
  'if len(out) > limit || len(rows) < n || n >= maxActivityRows {' \
  'if true || len(out) > limit || len(rows) < n || n >= maxActivityRows {' \
  ./internal/agent '^TestABigUploadDoesntHideOlderActivity$'
control "recent activity reads no more rows than its cap" internal/agent/analytics.go \
  'n = min(n*4, maxActivityRows) {' \
  'n *= 4 {' \
  ./internal/agent '^TestRecentActivityReadsNoMoreThanItsRows$'
control "recent activity names the server's folder as the file browser does" internal/agent/analytics.go \
  'folder := shown(path.Dir(e.Detail))' \
  'folder := path.Dir(e.Detail)' \
  ./internal/agent '^TestFileChangesAreAuditedAndShownAsActivity$'
control "file browser: only admins see a server's files" internal/panel/workspace.go \
  'actViewFiles:        invites.RoleAdmin,' \
  'actViewFiles:        invites.RoleViewer,' \
  ./internal/panel '^TestTheFileBrowserIsForAdmins$'
control "file browser: only admins change a server's files" internal/panel/workspace.go \
  'actEditFiles:        invites.RoleAdmin,' \
  'actEditFiles:        invites.RoleModerator,' \
  ./internal/panel '^TestTheFileBrowserIsForAdmins$'
control "file browser: a download can't render as a page of the panel" internal/panel/files.go \
  'h.Set("Content-Type", "application/octet-stream")' \
  'h.Set("Content-Type", resp.Header.Get("Content-Type"))' \
  ./internal/panel '^TestFileDownloadsAreNamedAndTypedByThePanel$'
control "file browser: a download runs sandboxed if a browser shows it anyway" internal/panel/files.go \
  'h.Set("Content-Security-Policy", "sandbox")' \
  'h.Set("X-Sandbox", "off")' \
  ./internal/panel '^TestFileDownloadsAreNamedAndTypedByThePanel$'
control "file browser: a download's check never sends the file" internal/panel/files.go \
  'case resp.StatusCode != want:' \
  'case false && resp.StatusCode != want:' \
  ./internal/panel '^TestFileDownloadsAreNamedAndTypedByThePanel$'
control "file browser: a joined machine's upload pieces aren't held to the link's smaller bodies" internal/agent/link.go \
  '"PUT /v1/servers/{id}/files/uploads/{up}/files/{n}": true,' \
  '"PUT /v1/servers/{id}/files/uploads/{up}/files/{n}": false,' \
  ./internal/panel '^TestAJoinedMachinesFilesGoThroughItsLink$'
control "file browser: a joined machine's downloads aren't held to the link's smaller answers" internal/agent/link.go \
  '"GET /v1/servers/{id}/files/download":               true,' \
  '"GET /v1/servers/{id}/files/download":               false,' \
  ./internal/panel '^TestAJoinedMachinesFilesGoThroughItsLink$'
webcontrol "the Files tab shows only to those who may see a server's files" web/src/components/app/server-tabs.tsx \
  "(x.tab === 'files' ? can(me, 'files.view') :" \
  "(x.tab === 'files' ? true :" \
  web/src/pages/server/files/files.test.tsx 'shows only to those who may see'
webcontrol "the Files tab offers no change to someone who may only look" web/src/pages/server/files/folder.tsx \
  "const canEdit = can(ws.me, 'files.edit')" \
  "const canEdit = can(ws.me, 'files.view')" \
  web/src/pages/server/files/files.test.tsx 'may only look'
webcontrol "the Files tab keeps the world's rows from changing while the game runs" web/src/pages/server/files/folder.tsx \
  'const worldEntry = (e: FileEntry) => running && inWorld(joinPath(path, e.name), worlds)' \
  'const worldEntry = (e: FileEntry) => false && running && inWorld(joinPath(path, e.name), worlds)' \
  web/src/pages/server/files/files.test.tsx 'keeps the world'
webcontrol "the Files tab checks a folder's download before following its link" web/src/pages/server/files/folder.tsx \
  "download: '', onLink: checkedDownload(server.id, [p]) })" \
  "download: '' })" \
  web/src/pages/server/files/files.test.tsx 'checks a folder'
webcontrol "the editor saves over the version it opened" web/src/pages/server/files/editor.tsx \
  "const info = await saveFile(server.id, path, sent, force ? '' : version)" \
  "const info = await saveFile(server.id, path, sent, '')" \
  web/src/pages/server/files/files.test.tsx 'saves over the version it opened'
webcontrol "an address that climbs out of a server's files opens the Files tab's top" web/src/lib/router.ts \
  "if (name === '' || name === '.' || name === '..' || name.includes('/') || name.includes('\\0')) return undefined" \
  "if (name === '' || name === '.' || name.includes('/') || name.includes('\\0')) return undefined" \
  web/src/lib/lib.test.ts 'names no path inside the server'
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
control "CurseForge's modpack logos load through the icon proxy" internal/addons/addons.go \
  'return fetch.Hosts{modrinth.CDNHost, hangar.CDNHost, CurseForgeLogoHost}' \
  'return fetch.Hosts{modrinth.CDNHost, hangar.CDNHost}' \
  ./internal/addons '^TestIconsComeOnlyFromTheSourcesHosts$'
control "icons come from no CurseForge host but its logos'" internal/addons/addons.go \
  'return fetch.Hosts{modrinth.CDNHost, hangar.CDNHost, CurseForgeLogoHost}' \
  'return fetch.Hosts{modrinth.CDNHost, hangar.CDNHost, CurseForgeLogoHost, "edge.forgecdn.net"}' \
  ./internal/addons '^TestIconsComeOnlyFromTheSourcesHosts$'
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
control "an add-on file may be as large as Pixelmon's jar" internal/addons/addons.go \
  'DefaultMaxFileSize = 512 << 20' \
  'DefaultMaxFileSize = 256 << 20' \
  ./internal/addons '^TestPlanInstallTakesFilesUpTo512MiB$'
control "an add-on file larger than 512 MiB is refused" internal/addons/addons.go \
  'DefaultMaxFileSize = 512 << 20' \
  'DefaultMaxFileSize = 1 << 62' \
  ./internal/addons '^TestPlanInstallTakesFilesUpTo512MiB$'
control "an add-on's download must match the hash its library publishes" internal/addons/fetch/download.go \
  'if got := hex.EncodeToString(v.hs[i].Sum(nil)); got != strings.ToLower(s.Hash) {' \
  'if got := hex.EncodeToString(v.hs[i].Sum(nil)); false && got != strings.ToLower(s.Hash) {' \
  ./internal/addons '^TestInstallRefusesBadDownloads$'
control "Playkeeper's own plugins install only on the server types they run on" internal/addons/firstparty.go \
  'if !fp.RunsOn(t.Type) {' \
  'if false && !fp.RunsOn(t.Type) {' \
  ./internal/addons '^TestPlaykeeperRefusesOtherServerTypes$'
control "a template's plugin of Playkeeper's own must be one this Playkeeper carries" internal/templates/validate.go \
  'if src == addons.Playkeeper && firstparty.Lookup(project) == nil {' \
  'if false && src == addons.Playkeeper && firstparty.Lookup(project) == nil {' \
  ./internal/templates '^TestValidateRefusesPlaykeeperPlugins$'
control "a template lists a plugin of Playkeeper's own only for the server types it runs on" internal/templates/validate.go \
  'return fp != nil && fp.RunsOn(target.Type)' \
  'return fp != nil' \
  ./internal/templates '^TestValidateRefusesPlaykeeperPlugins$'
control "a template's add-on installs only when the file has its pinned hash" internal/templates/install.go \
  'if p := a.Pin; p != nil && !a.Unpinned && (s.VersionID != p.VersionID || s.HashAlgo != p.HashAlgo || strings.ToLower(s.Hash) != p.Hash) {' \
  'if p := a.Pin; p != nil && !a.Unpinned && (s.VersionID != p.VersionID || s.HashAlgo != p.HashAlgo) {' \
  ./internal/templates '^TestInstallPlaykeeperPlugin$'
control "the site asks no source for an icon of Playkeeper's own plugins" internal/site/icons.go \
  'if !seen[p.key()] && p.Source != string(addons.Playkeeper) {' \
  'if !seen[p.key()] && addons.Playkeeper != "" {' \
  ./internal/site '^TestPlaykeepersOwnPluginsAreListedWithoutARegistry$'
control "a template page's Docker command asks Modrinth for none of Playkeeper's own plugins" internal/site/library.go \
  'if a.Source == addons.Playkeeper {' \
  'if false && a.Source == addons.Playkeeper {' \
  ./internal/site '^TestPlaykeepersOwnPluginsAreListedWithoutARegistry$'
control "a library page's plugin facts come from a check of the template's own source" internal/site/library.go \
  'p.Name != a.Name || p.Source != string(a.Source) || p.Slug != a.Slug' \
  'p.Name != a.Name || p.Slug != a.Slug' \
  ./internal/site '^TestLibraryFactsTheTemplateCantBackStopTheBuild$'
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
control "mods CurseForge won't let Playkeeper download come from the pack's server files" internal/modpacks/resolve.go \
  'if err := l.fillFromServerFiles(ctx, p, mod, file, lim); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/modpacks '^TestCurseForgeModsFromServerFiles$'
control "a mod from the server files must have the SHA-1 CurseForge lists" internal/modpacks/resolve.go \
  'if sums["sha1"] != sha1 {' \
  'if false && sums["sha1"] != sha1 {' \
  ./internal/modpacks '^TestCurseForgeModsFromServerFiles$'
control "only the server files of the pack's own version are used" internal/modpacks/resolve.go \
  'sp.ParentProjectFileID == nil || *sp.ParentProjectFileID != file.ID || ' \
  '' \
  ./internal/modpacks '^TestCurseForgeModsFromServerFiles$'
control "Missing Mods Checker stays off the server" internal/modpacks/missingmods.go \
  'delete(p.files, jar)' \
  '_ = jar' \
  ./internal/modpacks '^TestMissingModsCheckerStaysOffAndItsModsComeFromCurseForge$'
control "the mods Missing Mods Checker lists come from CurseForge" internal/modpacks/missingmods.go \
  'p.files[target] = &packFile{' \
  '_ = &packFile{' \
  ./internal/modpacks '^TestMissingModsCheckerStaysOffAndItsModsComeFromCurseForge$'
control "a mod Missing Mods Checker lists that CurseForge flagged as malware stops the install" internal/modpacks/missingmods.go \
  'case f.FileStatus == curseforge.StatusMalwareDetected:' \
  'case false && f.FileStatus == curseforge.StatusMalwareDetected:' \
  ./internal/modpacks '^TestMissingModsCheckerStaysOffAndItsModsComeFromCurseForge$'
control "a mod Missing Mods Checker lists that CurseForge no longer offers gets a step" internal/modpacks/missingmods.go \
  'if !seen[id] {' \
  'if false && !seen[id] {' \
  ./internal/modpacks '^TestMissingModsCheckerStaysOffAndItsModsComeFromCurseForge$'
control "a mod Missing Mods Checker lists that CurseForge tags for players stays off" internal/modpacks/missingmods.go \
  'case f.ClientOnly():' \
  'case false && f.ClientOnly():' \
  ./internal/modpacks '^TestMissingModsCheckerStaysOffAndItsModsComeFromCurseForge$'
control "a CurseForge pack's mods Modrinth lists as client-only stay off the server" internal/modpacks/resolve.go \
  'case (f.ClientOnly() || p.modrinthClient[f.SHA1()]) && !p.needed[f.SHA1()]:' \
  'case f.ClientOnly() && !p.needed[f.SHA1()]:' \
  ./internal/modpacks '^TestCurseForgeClientModsModrinthKnowsStayOff$'
control "a CurseForge pack's client-only mod another of its mods requires goes on the server" internal/modpacks/resolve.go \
  'case (f.ClientOnly() || p.modrinthClient[f.SHA1()]) && !p.needed[f.SHA1()]:' \
  'case f.ClientOnly() || p.modrinthClient[f.SHA1()]:' \
  ./internal/modpacks '^TestCurseForgeClientModAServerModRequiresGoesOn$'
control "an optional CurseForge file's dependencies don't keep a client-only mod" internal/modpacks/resolve.go \
  '		case mf.Required:
			onServer = append(onServer, f.SHA1())' \
  '		default:
			onServer = append(onServer, f.SHA1())' \
  ./internal/modpacks '^TestCurseForgeClientModAServerModRequiresGoesOn$'
control "only a CurseForge file's required dependencies keep a client-only mod" internal/modpacks/resolve.go \
  'if d.RelationType == curseforge.RequiredDependency {' \
  'if d.RelationType != 0 {' \
  ./internal/modpacks '^TestCurseForgeClientModAServerModRequiresGoesOn$'
control "a Modrinth pack's mods Modrinth lists as client-only stay off the server" internal/modpacks/resolve.go \
  'case env == mrpack.Unsupported || p.modrinthClient[sha1]:' \
  'case env == mrpack.Unsupported:' \
  ./internal/modpacks '^TestModrinthPackModsBySide$'
control "a Modrinth pack's client-only mod a mod on the server requires goes on" internal/modpacks/resolve.go \
  'case p.needed[sha1]:' \
  'case false && p.needed[sha1]:' \
  ./internal/modpacks '^TestModrinthPackModsBySide$'
control "what a kept client-only mod requires in turn stays off" internal/modpacks/resolve.go \
  'for _, m := range onServer {' \
  'for _, m := range append(onServer, clientOnly...) {' \
  ./internal/modpacks '^TestModrinthPackModsBySide$'
control "only a Modrinth version's required dependencies keep a client-only mod" internal/modpacks/resolve.go \
  'if d.DependencyType == modrinth.Required && pr != "" {' \
  'if pr != "" {' \
  ./internal/modpacks '^TestModrinthPackModsBySide$'
control "a mod version from before Modrinth's environment field goes by its project's server side" internal/modpacks/resolve.go \
  'if r, ok := runs[v.ProjectID]; ok {' \
  'if r, ok := runs[v.ProjectID]; false && ok {' \
  ./internal/modpacks '^TestCurseForgeClientModsModrinthKnowsStayOff$'
control "a client-only mod the pack's own files use stays on the server" internal/modpacks/resolve.go \
  'delete(p.modrinthClient, sha1)' \
  '_ = sha1' \
  ./internal/modpacks '^TestCurseForgeClientModsModrinthKnowsStayOff$'
control "only a pack's text files are read for the mods they use" internal/modpacks/resolve.go \
  'if !mentionsFile(rel) || !serverData(rel) || e.UncompressedSize64 > maxMentionsFile {' \
  'if (rel == "" && !mentionsFile(rel)) || !serverData(rel) || e.UncompressedSize64 > maxMentionsFile {' \
  ./internal/modpacks '^TestCurseForgeClientModsModrinthKnowsStayOff$'
control "only a pack's data and server scripts keep a client-only mod they name" internal/modpacks/resolve.go \
  'if !mentionsFile(rel) || !serverData(rel) || e.UncompressedSize64 > maxMentionsFile {' \
  'if !mentionsFile(rel) || e.UncompressedSize64 > maxMentionsFile {' \
  ./internal/modpacks '^TestModrinthPackModsBySide$'
control "a CurseForge pack whose mods Modrinth can't be asked about says so" internal/modpacks/resolve.go \
  'if err != nil {
		p.warn(notice(KindUnverifiedEnv, kv("pack", p.info.Name),' \
  'if err != nil {
		_ = (notice(KindUnverifiedEnv, kv("pack", p.info.Name),' \
  ./internal/modpacks '^TestCurseForgeClientModsModrinthKnowsStayOff$'
control "a modpack's file may be as large as Pixelmon's jar" internal/modpacks/modpacks.go \
  'File: addons.DefaultMaxFileSize,' \
  'File: 256 << 20,' \
  ./internal/modpacks '^TestDefaultFileLimit$'
control "a modpack's file larger than 512 MiB is refused, listed or not" internal/modpacks/modpacks.go \
  'File: addons.DefaultMaxFileSize,' \
  'File: 1 << 62,' \
  ./internal/modpacks '^TestDefaultFileLimit$'
control "a pack file's download stops at one file's limit, even when the pack lists it smaller" internal/modpacks/apply.go \
  'room := min(lim.File, lim.Downloads-total)' \
  'room := lim.Downloads - total' \
  ./internal/modpacks '^TestDefaultFileLimit$'
control "a modpack may put as many files on the server as the largest real packs" internal/modpacks/modpacks.go \
  'Index: 16 << 20, Files: 20000,' \
  'Index: 16 << 20, Files: 5000,' \
  ./internal/modpacks '^TestDefaultFileCount$'
control "a modpack that would put more than 20,000 files on the server is refused" internal/modpacks/modpacks.go \
  'Index: 16 << 20, Files: 20000,' \
  'Index: 16 << 20, Files: 1 << 30,' \
  ./internal/modpacks '^TestDefaultFileCount$'
control "a pack's default-server.properties never goes on the server" internal/modpacks/rules.go \
  '"eula.txt", "server.properties", DefaultPropertiesName, "ops.json",' \
  '"eula.txt", "server.properties", "ops.json",' \
  ./internal/modpacks '^TestDefaultServerPropertiesStayOffTheServer$'
control "a pack's default-server.properties settings are taken like its server.properties" internal/modpacks/resolve.go \
  'if e := entries[DefaultPropertiesName]; e != nil {' \
  'if e := entries[DefaultPropertiesName]; false && e != nil {' \
  ./internal/modpacks '^TestDefaultServerPropertiesStayOffTheServer$'
control "a default-server.properties on a modded server is settled before the start" internal/agent/lifecycle.go \
  'if err := s.keepDefaultProperties(sc); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/agent '^TestDefaultServerPropertiesKeepPlaykeepersSettings$'
control "the Default Server Properties mod's marker says its file was used" internal/agent/modpacks.go \
  'err = d.WriteFile(defaultPropertiesUsed, nil, 0o640)' \
  'err = nil' \
  ./internal/agent '^TestDefaultServerPropertiesKeepPlaykeepersSettings$'
control "a server a pack's mod started without the console and allowlist is restarted after an update" internal/agent/modpacks.go \
  'if off {' \
  'if false && off {' \
  ./internal/agent '^TestAnUpdateRestartsAServerAPackModSwitchedTheAllowlistOffFor$'
control "the settings check waits a minute after the pack's mod writes its marker" internal/agent/modpacks.go \
  'if err != nil || s.now().Sub(used.ModTime()) < defaultPropertiesSettle {' \
  'if err != nil || used == nil {' \
  ./internal/agent '^TestAnUpdateDuringAFirstStartLooksAgainOnceThePackModHasRun$'
control "the settings check looks again while the pack's mod hasn't used its file" internal/agent/modpacks.go \
  'if err != nil || s.now().Sub(used.ModTime()) < defaultPropertiesSettle {
		return false, false
	}' \
  'if err != nil {
		return false, true
	}
	if s.now().Sub(used.ModTime()) < defaultPropertiesSettle {
		return false, false
	}' \
  ./internal/agent '^TestAnUpdateDuringAFirstStartLooksAgainOnceThePackModHasRun$'
control "a modpack's downloads must match the hashes the pack lists" internal/addons/fetch/download.go \
  'if got := hex.EncodeToString(v.hs[i].Sum(nil)); got != strings.ToLower(s.Hash) {' \
  'if got := hex.EncodeToString(v.hs[i].Sum(nil)); false && got != strings.ToLower(s.Hash) {' \
  ./internal/modpacks '^TestDownloadsMustMatchThePacksHashes$'
control "a pack's settings are read and written without following a link" internal/agent/modpacks.go \
  'if err := setProperties(d, props); err != nil {
		return gameFileError(err, "The modpack'"'"'s settings could not be saved, so the server was not started.")' \
  'cur, _ := os.ReadFile(filepath.Join(s.dataDir(), "server.properties"))
	if err := os.WriteFile(filepath.Join(s.dataDir(), "server.properties"), mergeProperties(cur, props), 0o640); err != nil {
		return gameFileError(err, "The modpack'"'"'s settings could not be saved, so the server was not started.")' \
  ./internal/agent '^TestPackSettingsAreNotReadOrWrittenThroughALink$'
control "a CurseForge key is saved only once CurseForge accepts it" internal/agent/addonsources.go \
  'if err := modpacks.CheckKey(ctx, a.opts.UpstreamClient, key); err != nil {' \
  'if err := modpacks.CheckKey(ctx, a.opts.UpstreamClient, key); false && err != nil {' \
  ./internal/agent '^TestCurseForgeKeyIsCheckedSavedAndRemoved$'
control "the Java heap a pack's user_jvm_args.txt asks for is read" internal/modpacks/resolve.go \
  '			case "user_jvm_args.txt":
				p.readJVMArgs(e)
' \
  '' \
  ./internal/modpacks '^TestPackHeapFromItsServerSettings$'
control "the Java heap a CurseForge manifest recommends is read" internal/modpacks/resolve.go \
  'p.heapMB = plausibleHeap(int(m.Minecraft.RecommendedRAM))' \
  'p.heapMB = 0' \
  ./internal/modpacks '^TestCurseForgePackHeap$'
control "a pack's memory need covers the Java heap it asks for" internal/minecraft/catalog.go \
  '	if heapMB > 0 {
		need = max(need,' \
  '	if false && heapMB > 0 {
		need = max(need,' \
  ./internal/agent '^TestModpackPreviewSizesMemory$'
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
control "Home's activity waits only so long for a machine that hangs" internal/panel/team.go \
  'ctx, cancel := context.WithTimeout(r.Context(), activityTimeout)' \
  'ctx, cancel := context.WithTimeout(r.Context(), machineTimeout)' \
  ./internal/panel '^TestHomesActivityWaitsOnlySoLongForAMachineThatHangs$'
webcontrol "Home shows the servers while the activity is on its way" web/src/pages/home.tsx \
  'const activity = usePoll<Activity[] | undefined>(() => (grouped ? Promise.resolve(undefined) : recentActivity()), 10000, String(grouped))' \
  'const activity = usePoll<Activity[] | undefined>(() => (grouped ? Promise.resolve(undefined) : recentActivity()), 10000, String(grouped))
  if (!activity.data) return null' \
  web/src/pages/pages.test.tsx 'still on its way'
control "only the owner removes any machine's CurseForge key, not an admin of all servers" internal/panel/server.go \
  'mm("DELETE", "/api/machines/{mid}/addon-sources/curseforge", "/v1/addon-sources/curseforge", actManageAddonSources),' \
  'mm("DELETE", "/api/machines/{mid}/addon-sources/curseforge", "/v1/addon-sources/curseforge", actManageMachine),' \
  ./internal/panel '^TestOnlyTheOwnerChanges(TheCurseForgeKey|AJoinedMachinesCurseForgeKey)$'
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
# Crossplay: Floodgate comes from GeyserMC only for GeyserMC's own projects,
# with the hash GeyserMC publishes, and a Bedrock name reaches Floodgate's
# console command only as a plain gamertag.
control "only GeyserMC's own projects follow a link to its newest build" internal/addons/resolve.go \
  'ok && project == strings.ToLower(p.Slug) && slices.Contains(geysermc.Projects, project) {' \
  'ok {' \
  ./internal/addons '^TestOnlyGeyserMCsOwnProjectsFollowItsLinks$'
control "a link to GeyserMC's newest build is on GeyserMC's own host" internal/addons/geysermc/geysermc.go \
  'u.Host != Host ||' \
  '!strings.HasPrefix(u.Host, Host) ||' \
  ./internal/addons/geysermc '^TestParseLatestLink$'
control "GeyserMC's build installs only with the hash GeyserMC publishes" internal/addons/resolve.go \
  '"sha256", strings.ToLower(d.SHA256), size' \
  '"", "", size' \
  ./internal/addons '^(TestGeyserMCProjectsComeFromGeyserMC|TestGeyserMCDownloadsAreChecked)$'
control "a Bedrock name reaches Floodgate's command only as a plain gamertag" internal/agent/players.go \
  'if !ok || !reGamertag.MatchString(tag) {' \
  'if !ok {' \
  ./internal/agent '^TestBedrockPlayersJoinTheAllowlistThroughFloodgate$'
control "crossplay uses a Geyser put there by hand, known only by its file name" internal/agent/crossplay.go \
  '(n.Kind != addons.KindDuplicate && n.Kind != addons.KindFileExists)' \
  'n.Kind != addons.KindDuplicate' \
  ./internal/agent '^TestCrossplayUsesPluginsPutThereByHand$'
control "crossplay off without Docker's answer changes nothing" internal/agent/crossplay.go \
  'if _, _, err := s.containerRunning(ctx); err != nil {' \
  'if _, _, err := s.containerRunning(ctx); false && err != nil {' \
  ./internal/agent '^TestCrossplayOffThatCantCheckTheServerChangesNothing$'
control "a server with crossplay doesn't fall asleep" internal/agent/sleeping.go \
  '|| s.scheduleWorking() || s.hasCrossplay(), StartedAt: startedAt}' \
  '|| s.scheduleWorking(), StartedAt: startedAt}' \
  ./internal/agent '^TestCrossplayKeepsTheServerAwake$'
control "turning crossplay on wakes a sleeping server" internal/agent/crossplay.go \
  'if !running && s.desired() == api.DesiredSleeping {' \
  'if false && !running && s.desired() == api.DesiredSleeping {' \
  ./internal/agent '^TestCrossplayKeepsTheServerAwake$'
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
control "a type's kept version list is offered while its source fails" internal/agent/software.go \
  '		if a.savedSoftwareList(typ, "", &saved) {' \
  '		if false && a.savedSoftwareList(typ, "", &saved) {' \
  ./internal/agent '^TestATypesListsOutliveARestartWhileItsSourceFails$'
control "a type's kept build list is offered while its source fails" internal/agent/software.go \
  '		if a.savedSoftwareList(typ, mc, &saved) {' \
  '		if false && a.savedSoftwareList(typ, mc, &saved) {' \
  ./internal/agent '^TestATypesListsOutliveARestartWhileItsSourceFails$'
control "only a supported type names a kept list's file" internal/agent/software.go \
  '	if !software.Supported(typ) {
		return "", false
	}
	var name string' \
  '	var name string' \
  ./internal/agent '^TestAKeptListIsNamedOnlyByATypeAndARelease$'
control "no build list is taken for the version list" internal/agent/software.go \
  '	case savedBuilds, *savedBuilds:
		if !reListedRelease.MatchString(mc) {' \
  '	case savedBuilds, *savedBuilds:
		if mc == "" {
			name = "catalog-" + typ
			break
		}
		if !reListedRelease.MatchString(mc) {' \
  ./internal/agent '^(TestAKeptListIsNamedOnlyByATypeAndARelease|TestATypesListsOutliveARestartWhileItsSourceFails)$'
control "only a Minecraft release names a kept build list's file" internal/agent/software.go \
  '		if !reListedRelease.MatchString(mc) {' \
  '		if false {' \
  ./internal/agent '^TestAKeptListIsNamedOnlyByATypeAndARelease$'
control "NeoForge's Maven is asked again after a 5xx" internal/minecraft/software/fetch.go \
  '	case http.StatusNotFound, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:' \
  '	case http.StatusNotFound:' \
  ./internal/minecraft/software '^TestUpstreamAsksNeoForgeAgainAfterA404OrA5xx$'
control "a template whose modpack runs on another type is blocked" internal/agent/templates.go \
  'p.Blockers, p.Ready = append(p.Blockers, *n), false' \
  '_ = n' \
  ./internal/agent '^TestTemplateModpackRunsOnTheTypeItNames$'
control "a template's modpack plans on the Minecraft version it's made for" internal/agent/templates.go \
  'if same < 0 && t.Modpack != nil && typ == t.Server.Type {' \
  'if false {' \
  ./internal/agent '^TestTemplateModpackPlansOnThePacksOwnVersion$'
control "a template's CurseForge modpack needs this machine's own CurseForge key" internal/agent/templates.go \
  'if m.Source == modpacks.CurseForge && !slices.Contains(a.packs().Sources(), modpacks.CurseForge) {' \
  'if false {' \
  ./internal/agent '^TestTemplateCarriesACurseForgeModpack$'
control "a template whose modpack version its source doesn't list is blocked" internal/agent/templates.go \
  '	case i < 0:
		return &addons.Notice{Kind: kindTemplatePackMissing,' \
  '	case false:
		return &addons.Notice{Kind: kindTemplatePackMissing,' \
  ./internal/agent '^TestTemplateCarriesACurseForgeModpack$'
control "a template whose modpack version can't be installed is blocked" internal/agent/templates.go \
  '	case v.Unsupported != nil:
		return packNotice(*v.Unsupported, hint)' \
  '	case false:
		return packNotice(*v.Unsupported, hint)' \
  ./internal/agent '^TestTemplateCarriesACurseForgeModpack$'
control "creating from a template checks its modpack's pin when the plan couldn't" internal/agent/handlers.go \
  '		if tpl != nil {
			n, err := a.packFit(r.Context(), tpl.p)' \
  '		if false {
			n, err := a.packFit(r.Context(), tpl.p)' \
  ./internal/agent '^TestTemplateCreateChecksThePinThePlanCouldNot$'
control "a template whose modpack isn't the file its source offers is blocked" internal/agent/templates.go \
  'case v.Hash != "" && (v.HashAlgo != m.Pin.HashAlgo || v.Hash != m.Pin.Hash):' \
  'case false:' \
  ./internal/agent '^TestTemplate(ModpackRunsOnTheTypeItNames|CarriesACurseForgeModpack)$'
control "an exported modpack server's own mods travel with its pack" internal/agent/templates.go \
  'if st.Modpack.Files, err = s.packFiles(st.Folder.Folder); err != nil {' \
  'if _, err = s.packFiles(st.Folder.Folder); err != nil {' \
  ./internal/agent '^TestTemplate(OfAModpackServerCarriesThePack|CarriesACurseForgeModpack)$'
control "templates carry CurseForge modpacks" internal/templates/validate.go \
  'modpackSources = []addons.Source{addons.Modrinth, modpacks.CurseForge}' \
  'modpackSources = []addons.Source{addons.Modrinth}' \
  ./internal/templates '^Test(FixturesAreCanonical|TemplateCarriesACurseForgeModpack|ExportCurseForgeModpack)$'
control "a template's CurseForge modpack is pinned by the SHA-1 CurseForge publishes" internal/templates/validate.go \
  '	case modpacks.CurseForge:
		return "sha1"' \
  '' \
  ./internal/templates '^Test(ValidateRefuses|TemplateCarriesACurseForgeModpack)$'
control "a modpack's own files aren't reported as added by hand" internal/templates/export.go \
  'case e.Installed == nil && x.packFiles[e.FileName]:' \
  'case false:' \
  ./internal/templates '^TestExportLeavesAModpacksOwnFilesToIt$'
control "a CurseForge pack's versions carry the SHA-1 CurseForge publishes" internal/modpacks/browse.go \
  ' HashAlgo: "sha1", Hash: f.SHA1(),' \
  '' \
  ./internal/modpacks '^TestVersions$'
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
control "a pack's plan reads the held voice chat ports under their lock" internal/agent/curated.go \
  'if port, err := a.freeVoicePort("", curated.VoiceChatPort); err == nil {' \
  'if port, err := curated.PickPort(curated.VoiceChatPort, a.voicePortTaken("")); err == nil {' \
  ./internal/agent '^TestAPacksPreviewReadsHeldVoicePortsUnderTheirLock$'
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
  ./internal/agent '^TestAWorldFolderARestoreLeftMissingIsShownUntilItIsBack$'
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
  'if !running || !ok || started.Before(op.StartedAt) || started.Truncate(time.Millisecond).After(*op.FinishedAt) {' \
  'if !running || !ok || started.IsZero() {' \
  ./internal/agent '^TestRestoreUndoneBy030StoppingIsTidiedUp$/^the_server_was_restarted_since$'
control "a restored world started in the millisecond its restore was undone in was started during it" internal/agent/recovery.go \
  'started.Truncate(time.Millisecond).After(*op.FinishedAt)' \
  'started.After(*op.FinishedAt)' \
  ./internal/agent '^TestRestoreUndoneBy030StoppingIsTidiedUp$/^the_server_was_started_as_the_restore_was_undone$'
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
  mutate "$name" "$file" "$3" "$4" || return 0
  # A bash script is parsed by bash, a POSIX one by sh.
  case $(head -n1 "$file") in *bash*) shell=bash ;; esac
  if ! "$shell" -n "$file" 2>/dev/null; then
    problem "INVALID  $name: the mutated script does not parse"
  elif bash "$test" >/tmp/negative-control.out 2>&1; then
    problem "MISSED   $name: $test still passes without the guard"
  else
    echo "caught   $name: $(grep -m1 '^FAIL: ' /tmp/negative-control.out | cut -c1-200)"
  fi
  git checkout -q -- "$file"
}
control "the installer deletes get.sh's download when it stops at its flags" cmd/playkeeper/main.go \
  '	defer removeGetDir()
' \
  '' \
  ./cmd/playkeeper '^TestTheInstallerDeletesGetShsDownloadWhenItStopsEarly$'
# shellcheck disable=SC2016
shcontrol "get.sh hands over to the installer, so sudo-rs resumes it when it asks" packaging/get.sh \
  'exec "$dir/install.sh" "$@" </dev/tty' \
  '"$dir/install.sh" "$@" </dev/tty' \
  packaging/get_test.sh
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
shcontrol "get.sh downloads the arm64 tarball on 64-bit ARM" packaging/get.sh \
  'aarch64 | arm64) arch=arm64 ;;' \
  'aarch64 | arm64) arch=amd64 ;;' \
  packaging/get_test.sh
# shellcheck disable=SC2016
shcontrol "get.sh refuses a 32-bit system on a 64-bit CPU" packaging/get.sh \
  '[ "$(getconf LONG_BIT 2>/dev/null || echo 64)" = 64 ] ||' \
  'true ||' \
  packaging/get_test.sh
# shellcheck disable=SC2016
shcontrol "install.sh runs only the build for this CPU" packaging/install.sh \
  '!= "$machine" ]; then' \
  '= "never" ]; then' \
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
# shellcheck disable=SC2016
shcontrol "the site check looks for the demo's marker in chunks other chunks load" scripts/demo-marker.sh \
  '    queue+=("$dir/$name")' \
  '    [ "$chunk" != "$entry" ] || queue+=("$dir/$name")' \
  scripts/demo-marker_test.sh
# shellcheck disable=SC2016
shcontrol "the site check fetches each chunk once looking for the demo's marker" scripts/demo-marker.sh \
  'case $seen in *" $dir/$name "*) continue ;; esac' \
  ':' \
  scripts/demo-marker_test.sh
shcontrol "the site check looks for the demo's marker in 200 chunks at most" scripts/demo-marker.sh \
  'limit=200' \
  'limit=100000' \
  scripts/demo-marker_test.sh
shcontrol "the site check goes on past a chunk that loads no other chunk" scripts/demo-marker.sh \
  ' | sort -u) || true' \
  ' | sort -u)' \
  scripts/demo-marker_test.sh
shcontrol "each CI shard of a package's tests runs its own share" scripts/go-test-shard.sh \
  "'NR % n == k % n'" \
  "'NR % n == 1'" \
  scripts/go-test-shard_test.sh
# shellcheck disable=SC2016
shcontrol "the shards' check fails when no shard ran a test" scripts/go-test-shard.sh \
  'missed=$(comm -23 <(sort -u "$dir/all-1.txt") <(sort -u "$dir"/ran-*.txt))' \
  'missed=' \
  scripts/go-test-shard_test.sh
# shellcheck disable=SC2016
shcontrol "the shards' check fails when shards listed other tests" scripts/go-test-shard.sh \
  'if ! cmp -s "$dir/all-1.txt" "$dir/all-$k.txt"; then' \
  'if false; then' \
  scripts/go-test-shard_test.sh
# shellcheck disable=SC2016
shcontrol "a shard runs the tests a panic kept from starting" scripts/go-test-shard.sh \
  'todo=$(grep -vxF -e "$started" <<<"$todo" || true)' \
  'todo=' \
  scripts/go-test-shard_test.sh

control "names service owns only records with the name's marker" internal/names/service/dns.go \
  'if names.CheckName(name) != nil || names.Reserved(name) || r.Comment != marker(name) {' \
  'if names.CheckName(name) != nil || names.Reserved(name) {' \
  ./internal/names/service '^(TestOwnsOnlyMarkedRecordsInTheServicesOwnPatterns|TestTheGuardRefusesEveryChangeOutsideItsPatterns|TestRecordsTheServiceDoesNotManageAreNeverTouched)$'
control "names service checks a zone it couldn't check at startup before the first change" internal/names/service/dns.go \
  'if s.zoneOK.Load() {' \
  'if true || s.zoneOK.Load() {' \
  ./internal/names/service '^TestStartupWithoutCloudflareChecksTheZoneBeforeTheFirstChange$'
control "names service never owns records of reserved names" internal/names/service/dns.go \
  'if names.CheckName(name) != nil || names.Reserved(name) || r.Comment != marker(name) {' \
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
  'if names.Reserved(name) || s.block.has(name) {' \
  'if s.block.has(name) {' \
  ./internal/names/service '^TestReservedAndBlocklistedNamesCannotBeClaimed$'
control "blocklisted names cannot be claimed" internal/names/service/handlers.go \
  'if names.Reserved(name) || s.block.has(name) {' \
  'if names.Reserved(name) {' \
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
control "the HTTP-01 responder stops listening when its last check is released" internal/certs/http01.go \
  'if h.pending == 0 && h.srv != nil {' \
  'if false && h.pending == 0 && h.srv != nil {' \
  ./internal/certs '^TestHTTP01ListensWhilePending$'
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
  'for _, file := range []string{fileStem(n) + ".pem", fileStem(n) + orderSuffix} {' \
  'for _, file := range []string{fileStem(n) + ".pem"} {' \
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
  'Since: a.now().UTC(), IP: st.IP, Released: st.Released, ServerAddresses: st.ServerAddresses}' \
  'Since: a.now().UTC(), IP: st.IP, ServerAddresses: st.ServerAddresses}' \
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
  'if err := s.permitOn(sess.Access, act, p.ServerID); err != nil {' \
  'if err := s.permitOn(sess.Access, act, p.ServerID); false && err != nil {' \
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
control "the world import guides are the sources the import screen offers" internal/worldimport/guides.go \
  'minehutGuide(), otherHostGuide()}' \
  'otherHostGuide()}' \
  ./internal/worldimport '^TestGuidesAreTheSourcesTheImportScreenOffers$'
control "the fake panel refuses an invalid request with the panel's code" test/e2e/ui/fakes.ts \
  "  return { status: 400, body: { error, code: 'invalid_request' } }" \
  "  return { status: 400, body: { error, code: 'invalid' } }" \
  ./internal/panel '^TestTheFakePanelRefusesWithCodesThePanelSends$'
control "a server has at most 20 friend links that work" internal/panel/friends.go \
  'if working >= invites.MaxWorkingPlayerInvites {' \
  'if false && working >= invites.MaxWorkingPlayerInvites {' \
  ./internal/panel '^TestFriendLinksThatWorkAreCappedPerServer$'
control "a friend link turned off makes room for another" internal/panel/friends.go \
  'AND server_id = ? AND revoked_at = 0
			AND (expires_at' \
  'AND server_id = ?
			AND (expires_at' \
  ./internal/panel '^TestFriendLinksThatWorkAreCappedPerServer$'
control "an expired friend link makes room for another" internal/panel/friends.go \
  'revoked_at = 0
			AND (expires_at = 0 OR expires_at > ?)' \
  'revoked_at = 0
			AND (expires_at = 0 OR expires_at > ? OR 1)' \
  ./internal/panel '^TestFriendLinksThatWorkAreCappedPerServer$'
control "a used-up friend link makes room for another" internal/panel/friends.go \
  '			AND (expires_at = 0 OR expires_at > ?) AND (max_uses = 0 OR uses < max_uses)' \
  '			AND (expires_at = 0 OR expires_at > ?) AND (max_uses = 0 OR uses <= max_uses)' \
  ./internal/panel '^TestFriendLinksThatWorkAreCappedPerServer$'
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
  'GaveUp: wanted && counted' \
  'GaveUp: counted || !restarting' \
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
control "memory advice reads the heap the server has, not today's mods'" internal/diagnose/memory.go \
  'if budgetMB == in.BudgetMB && in.HeapMB > 0 {' \
  'if false {' \
  ./internal/agent '^TestMemoryAdviceReadsTheHeapTheServerRunsWith$'
control "memory advice reads the heap of the server's container" internal/agent/memory.go \
  'budget, heap := s.runMemory(ctx, *sc)' \
  'budget, heap := sc.MemoryMB, heapMB(*sc)' \
  ./internal/agent '^TestMemoryAdviceReadsTheHeapTheServerRunsWith$'
control "memory advice reads a budget saved since against its restart's heap" internal/agent/memory.go \
  '	if budget != sc.MemoryMB {
		heap = heapMB(*sc)' \
  '	if budget < 0 {
		heap = heapMB(*sc)' \
  ./internal/agent '^TestMemoryAdviceReadsTheHeapTheServerRunsWith$'
control "crash help explains the heap the server ran with" internal/agent/crash.go \
  'budget, heap := s.runMemory(ctx, *sc)' \
  'budget, heap := sc.MemoryMB, heapMB(*sc)' \
  ./internal/agent '^TestCrashHelpExplainsTheMemoryTheServerRanWith$'
control "crash help explains the memory limit the server ran with" internal/agent/heap.go \
  'budgetMB = int(c.HostConfig.Memory >> 20)' \
  '_ = c.HostConfig.Memory' \
  ./internal/agent '^TestCrashHelpExplainsTheMemoryTheServerRanWith$'
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
  '{memoryAdviceLine(advice, machineName, catalog?.sizing, s.type, planMaxMB)}' \
  '{memoryAdviceLine(advice, machineName, catalog?.sizing, undefined, planMaxMB)}' \
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
  '{"POST", "/api/machines/{mid}/world-imports", needSessionCSRF, actCreateOwnServers, s.hWorldImportOpen},' \
  '{"POST", "/api/machines/{mid}/world-imports", needSessionCSRF, actManageServers, s.hWorldImportOpen},' \
  ./internal/panel '^TestMachineWideActionsNeedEveryServer$'
control "making a server from an upload needs rights over every server" internal/panel/server.go \
  'needSessionCSRF, actCreateOwnServers, s.importGuard(actCreateServers, s.hWorldImportCreate)' \
  'needSessionCSRF, actManageServers, s.importGuard(actCreateServers, s.hWorldImportCreate)' \
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
control "turning the map on doesn't take a failed look at the server for a stopped one" internal/agent/maps.go \
  '	if err != nil {
		return restartUnchecked("squaremap is installed",' \
  '	if false && err != nil {
		return restartUnchecked("squaremap is installed",' \
  ./internal/agent '^TestAMapChangeThatCantCheckTheServerSaysToRestart$'
control "turning the map off doesn't take a failed look at the server for a stopped one" internal/agent/maps.go \
  'if _, running, err = s.containerRunning(ctx); err != nil {' \
  'if _, running, err = s.containerRunning(ctx); false && err != nil {' \
  ./internal/agent '^TestAMapChangeThatCantCheckTheServerSaysToRestart$'
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
control "an unfinished upload is forgotten after an hour" internal/agent/worldimports.go \
  '		if !imp.complete() {
			idle = incompleteImportIdle
		}' \
  '' \
  ./internal/agent '^TestStaleUploadsMakeWayForNewOnes$'
control "a finished upload is kept for a day" internal/agent/worldimports.go \
  'idle := worldImportIdle' \
  'idle := incompleteImportIdle' \
  ./internal/agent '^TestStaleUploadsMakeWayForNewOnes$'
control "an announce forgets the stale uploads before it counts the space" internal/agent/worldimports.go \
  '	a.imports.mu.Lock()
	stale := a.staleImports(a.now(), imp)
	a.imports.mu.Unlock()
	removeImports(stale)
' \
  '' \
  ./internal/agent '^TestStaleUploadsMakeWayForNewOnes$'
control "the upload being added to is never forgotten as stale" internal/agent/worldimports.go \
  'stale := a.staleImports(a.now(), imp)' \
  'stale := a.staleImports(a.now(), nil)' \
  ./internal/agent '^TestStaleUploadsMakeWayForNewOnes$'
control "a cancel marks the upload gone as it checks it" internal/agent/worldimports.go \
  '	if !inUse {
		imp.gone = true
	}' \
  '' \
  ./internal/agent '^TestCancellingAnUploadNeverDeletesItFromUnderAnOperation$'
control "an imported world moves back only once the server stopped" internal/agent/worldimports.go \
  '	if err := s.stopServer(ctx, h); err != nil {
		if s.stopping() {' \
  '	if err := s.stopServer(ctx, h); false && err != nil {
		if s.stopping() {' \
  ./internal/agent '^TestAnImportedWorldMovesBackOnlyOnceTheServerStopped$'
control "an import the agent stopped during says so" internal/agent/worldimports.go \
  '		if s.stopping() {
			return false, &apiError{' \
  '		if false {
			return false, &apiError{' \
  ./internal/agent '^TestAnImportedWorldMovesBackOnlyOnceTheServerStopped$'
control "a create that can't move the world in drops the upload" internal/agent/worldimports.go \
  '		// upload again, so it goes with what is left of its unpacked copy.
		s.dropImport(imp)' \
  '		// upload again, so it goes with what is left of its unpacked copy.
		imp.release()' \
  ./internal/agent '^TestACreateThatCantMoveTheWorldInDropsTheUpload$'
control "imports count the space the others claimed" internal/agent/worldimports.go \
  'if err == nil && free-claimed < need+minFreeAfterBackup {' \
  'if err == nil && free < need+minFreeAfterBackup {' \
  ./internal/agent '^TestImportsClaimDiskSpaceInTurn$'
control "an import records the space it claimed" internal/agent/worldimports.go \
  '	imp.reserved = need
' \
  '' \
  ./internal/agent '^TestImportsClaimDiskSpaceInTurn$'
control "a create refused for space doesn't keep the upload" internal/agent/worldimports.go \
  '	if err := a.reserveImportSpace(imp, need); err != nil {
		imp.release()' \
  '	if err := a.reserveImportSpace(imp, need); err != nil {' \
  ./internal/agent '^TestImportsClaimDiskSpaceInTurn$'
control "turning the map off stops squaremap before deleting what it drew" internal/agent/maps.go \
  '		if running {
			h.phase("stopping")
			if err := s.stopServer(ctx, h); err != nil {
				return err
			}
		}
	}
	startAgain := func() error {
		if !running {
			return nil
		}
		h.phase("starting")' \
  '	}
	startAgain := func() error {
		if !running {
			return nil
		}
		h.phase("starting")
		if err := s.stopServer(ctx, h); err != nil {
			return err
		}' \
  ./internal/agent '^TestTurningTheMapOffStopsSquaremapFirst$'
control "the map's record goes even when some of the drawing can't be deleted" internal/agent/maps.go \
  'leftover = &apiError{Msg: "The map is off, but' \
  'return &apiError{Msg: "The map is off, but' \
  ./internal/agent '^TestTurningTheMapOffStopsSquaremapFirst$'
control "turning the map off leaves the Plugins tab's squaremap and its folder" internal/agent/maps.go \
  'owned := len(rec.addons) > 0' \
  'owned := true' \
  ./internal/agent '^TestTurningTheMapOffStopsSquaremapFirst$'
control "a replaced world's drawn map is deleted" internal/agent/worldimports.go \
  '	s.forgetDrawnMap()
' \
  '' \
  ./internal/agent '^TestAReplacedWorldIsDrawnAfresh$'
control "a replaced world is drawn again once it is online" internal/agent/maps.go \
  'UPDATE maps SET first_render_at = NULL WHERE server_id = ?' \
  'UPDATE maps SET first_render_at = first_render_at WHERE server_id = ?' \
  ./internal/agent '^TestAReplacedWorldIsDrawnAfresh$'
control "an import leaves squaremap's folder alone while the map is off" internal/agent/maps.go \
  '	if err != nil || rec == nil {
		return
	}
	l, err := webmap.LayoutFor(s.serverType(nil))
	if err != nil {
		return
	}
	for _, rel := range' \
  '	if err != nil || rec == nil && false {
		return
	}
	l, err := webmap.LayoutFor(s.serverType(nil))
	if err != nil {
		return
	}
	for _, rel := range' \
  ./internal/agent '^TestAReplacedWorldIsDrawnAfresh$'
webcontrol "leaving the page cancels the upload" web/src/components/app/world-import.tsx \
  "window.addEventListener('pagehide', leave)" \
  "window.addEventListener('pageshow', leave)" \
  web/src/pages/new-server.test.tsx
webcontrol "the request cancelling an upload outlives the page" web/src/components/app/world-import.tsx \
  'keepalive: true' \
  'keepalive: false' \
  web/src/pages/new-server.test.tsx
webcontrol "leaving the page keeps an upload a server was made from" web/src/components/app/world-import.tsx \
  'if (!j.upload || !machineId || kept.current) return' \
  'if (!j.upload || !machineId) return' \
  web/src/pages/new-server.test.tsx
# shellcheck disable=SC2016
webcontrol "carrying on with an upload asks the machine which files it has" web/src/lib/upload.ts \
  'let imp = seen(o.resume ? await get<T>(`${o.base}/${o.resume.id}`) : await kind.open())' \
  'let imp = seen((o.resume as T | undefined) ?? (await kind.open()))' \
  web/src/lib/upload.test.ts
webcontrol "carrying on refuses an upload whose files differ" web/src/lib/upload.ts \
  ' || imp.files.some(differs)' \
  '' \
  web/src/lib/upload.test.ts
webcontrol "Try again carries on with the upload as the machine last described it" web/src/components/app/world-import.tsx \
  '          j.upload = imp
        },' \
  '          j.upload ??= imp
        },' \
  web/src/pages/new-server.test.tsx

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
  'actViewMachines, everyServer(s.machineProxy("GET", "/v1/disk"))},' \
  'actViewMachines, s.machineProxy("GET", "/v1/disk")},' \
  ./internal/panel '^TestMachineWideActionsNeedEveryServer$'
control "the panel never caches the recovery key" internal/panel/automation.go \
  'h.Set("Cache-Control", "no-store")' \
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
  '} else if unsettled {' \
  '} else if false && unsettled {' \
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
  'ae.Code != api.CodeBusy {' \
  'ae.Code == api.CodeBusy {' \
  ./internal/agent '^TestSleepAndWakeTransitions$/^a_player_wakes_it_during_a_backup$'
control "turning sleep off lets go of the game port when the server can't start" internal/agent/lifecycle.go \
  '		_ = s.setDesired(api.DesiredStopped)
	}
	s.leaveSleep()' \
  '		_ = s.setDesired(api.DesiredStopped)
	}' \
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
  's.now().UnixMilli(), f.Folder, s.id, row.keysRead)' \
  's.now().UnixMilli(), "", s.id, row.keysRead)' \
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
  'Busy: s.busy() || s.pregenRunning() || s.scheduleWorking() || s.hasCrossplay(),' \
  'Busy: s.busy() || s.scheduleWorking() || s.hasCrossplay(),' \
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
  'all, _ := s.queuedStates()
	s.discardUploads(append(all, dropped...))' \
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
  ./internal/agent '^TestCopiesTurnedOffStopTheCopyBeingMade$'
control "turning copies off stops the copy the uploader claimed" internal/agent/offsite.go \
  'func (s *server) stopUpload() {
	s.auto.mu.Lock()
	c := s.auto.claim' \
  'func (s *server) stopUpload() {
	s.auto.mu.Lock()
	var c *uploadClaim' \
  ./internal/agent '^TestCopiesTurnedOffStopTheCopyBeingMade$'

# Wave 7 before Bugbot: a schedule lists the retry after a run skipped for
# players exactly while the runner plans it.
control "every change to a schedule drops its retry, as the planner does" internal/agent/schedules.go \
  "THEN json_remove(last_run, '\$.retryAt') ELSE" \
  'THEN last_run ELSE' \
  ./internal/agent '^TestAScheduleListsItsRetryOnlyWhileTheRunnerPlansIt$'
webcontrol "the schedule list promises a retry only while it is the next run" web/src/pages/server/schedules.tsx \
  'if (!at || !s.nextRun || new Date(s.nextRun).getTime() !== new Date(at).getTime()) return undefined' \
  'if (!at) return undefined' \
  web/src/pages/server/schedules.test.tsx 'list a retry the agent no longer plans'

# Wave 7 before Bugbot: a schedule changed from a dashboard in another time
# zone keeps its moments.
control "the automatic backups keep their time zone while they keep their time" internal/agent/backuprules.go \
  '	} else if tz != "" {' \
  '	}
	if tz != "" {' \
  ./internal/agent '^TestAutomaticBackupsKeepTheirTimeZoneWhileTheyKeepTheirTime$'
webcontrol "the schedule dialog keeps the saved time zone while the time and days stay" web/src/pages/server/schedules.tsx \
  'const kept = existing && opened && opened.often === form.often && opened.at === form.at ? existing.timing : undefined' \
  'const kept = undefined' \
  web/src/pages/server/schedules.test.tsx 'every day, in another zone'
webcontrol "the schedule dialog shows the time on the viewer's clock" web/src/pages/server/schedules.tsx \
  'const here = shownIn(s.timing, timeZone)' \
  "const here = { at: s.timing.at ?? '', days: s.timing.days }" \
  web/src/pages/server/schedules.test.tsx 'saves a new time in the viewer'
webcontrol "the schedule list names the days on the viewer's clock" web/src/pages/server/schedules.tsx \
  'const days = weekdays.filter((d) => here.days?.includes(d))' \
  'const days = weekdays.filter((d) => timing.days?.includes(d))' \
  web/src/pages/server/schedules.test.tsx 'days that fall on others'

# Wave 7 before Bugbot: a new key reaches the copy being made and the copies
# waiting, and stopping a copy for it isn't a failed try.
control "a new key stops the copy being made" internal/agent/offsite.go \
  '	s.stopUpload()
	s.kickOffsite()
	s.audit(actor, "offsite.key_rotated"' \
  '	s.kickOffsite()
	s.audit(actor, "offsite.key_rotated"' \
  ./internal/agent '^TestANewKeyReachesTheCopyBeingMade$/^during_a_copy,_with_another_backup_waiting$'
control "a new key starts the uploader again" internal/agent/offsite.go \
  '	s.stopUpload()
	s.kickOffsite()
	s.audit(actor, "offsite.key_rotated"' \
  '	s.stopUpload()
	s.audit(actor, "offsite.key_rotated"' \
  ./internal/agent '^TestANewKeyReachesTheCopyBeingMade$/^during_a_copy_that_saved_where_it_stopped$'
control "a copy picked after a new key isn't encrypted to the old one" internal/agent/offsite.go \
  'err != nil || !row.enabled || row.keys.Current.Recipient != at.keys.Current.Recipient || !sameConnection(row, at) {' \
  'err != nil || !row.enabled || false && row.keys.Current.Recipient != at.keys.Current.Recipient || !sameConnection(row, at) {' \
  ./internal/agent '^TestANewKeyReachesTheCopyBeingMade$/^while_the_next_copy_is_picked$'
# A new key is also a save of the settings since the claim, which a failed
# try doesn't count either; the agent stopping reaches this guard alone.
control "a copy stopped as the agent stops isn't a failed try" internal/agent/offsite.go \
  '	if ctx.Err() != nil {
		// Playkeeper is stopping' \
  '	if false {
		// Playkeeper is stopping' \
  ./internal/agent '^TestACopyTheAgentStoppedInResumesFromItsSavedPart$'

# Wave 7 before Bugbot: restoring a copy takes as long as the copy takes to
# come, and every other operation keeps its deadline.
control "a restore of a copy has no fixed deadline" internal/agent/lifecycle.go \
  'var noDeadline = map[string]bool{"offsite-restore": true, "offsite-check": true, "offsite-recover": true}' \
  'var noDeadline = map[string]bool{"offsite-check": true, "offsite-recover": true}' \
  ./internal/agent '^TestRestoringOrCheckingACopyOutlastsTheOperationDeadline$/^restoring_a_copy$'
control "a restore from a recovery key has no fixed deadline" internal/agent/lifecycle.go \
  'var noDeadline = map[string]bool{"offsite-restore": true, "offsite-check": true, "offsite-recover": true}' \
  'var noDeadline = map[string]bool{"offsite-restore": true, "offsite-check": true}' \
  ./internal/agent '^TestRestoringOrCheckingACopyOutlastsTheOperationDeadline$/^restoring_from_a_recovery_key$'
control "a server's other operations keep their deadline" internal/agent/lifecycle.go \
  '	if noDeadline[kind] {' \
  '	if true || noDeadline[kind] {' \
  ./internal/agent '^TestRestoringOrCheckingACopyOutlastsTheOperationDeadline$/^a_backup$'
control "machine operations keep their deadline" internal/agent/agent.go \
  'ctx, cancel := opContext(a.ctx, kind)' \
  'ctx, cancel := context.WithCancel(a.ctx)' \
  ./internal/agent '^TestRestoringOrCheckingACopyOutlastsTheOperationDeadline$/^a_machine_operation$'

# Wave 7 before Bugbot: restoring from a recovery key holds no server.
control "a restore from a recovery key holds no server" internal/agent/agent.go \
  '	if stagingOps[kind] {' \
  '	if false && stagingOps[kind] {' \
  ./internal/agent '^TestARestoreFromARecoveryKeyHoldsNoServer$'
control "what waits for a restore from a recovery key says what for" internal/agent/lifecycle.go \
  '	"offsite-recover": "restoring a server from a recovery key",
' \
  '' \
  ./internal/agent '^TestARestoreFromARecoveryKeyHoldsNoServer$'

# Wave 7 before Bugbot: deleting a server asks before it deletes the only key
# to its copies somewhere else.
control "a delete that deletes the only key to the copies is refused" internal/agent/handlers.go \
  '	if err := s.keyNotSaved(); err != nil && !req.ForgetKey {' \
  '	if err := s.keyNotSaved(); false && err != nil && !req.ForgetKey {' \
  ./internal/agent '^TestDeletingAServerAsksBeforeItDeletesTheOnlyKeyToItsCopies$/^a_copy_kept,_the_key_never_downloaded$'
control "a confirmed delete goes ahead without the key" internal/agent/handlers.go \
  '	if err := s.keyNotSaved(); err != nil && !req.ForgetKey {' \
  '	if err := s.keyNotSaved(); err != nil {' \
  ./internal/agent '^TestDeletingAServerAsksBeforeItDeletesTheOnlyKeyToItsCopies$/^a_copy_kept,_the_key_never_downloaded,_and_the_delete_confirmed$'
control "a key never downloaded is at risk before the first copy" internal/agent/automation.go \
  '	if !row.hasKeys || row.keySavedAt != nil {' \
  '	if !row.hasKeys || row.keySavedAt != nil || row.copiesMade == 0 {' \
  ./internal/agent '^TestDeletingAServerAsksBeforeItDeletesTheOnlyKeyToItsCopies$/^copies_on_but_none_made_yet'
control "a downloaded key lets the delete go ahead" internal/agent/automation.go \
  '	if !row.hasKeys || row.keySavedAt != nil {' \
  '	if !row.hasKeys {' \
  ./internal/agent '^TestDeletingAServerAsksBeforeItDeletesTheOnlyKeyToItsCopies$/^a_copy_kept,_the_key_downloaded$'
webcontrol "the delete dialog warns while the recovery key was never downloaded" web/src/components/app/delete-server.tsx \
  'return !!v?.key && !v.key.savedAt' \
  'return false' \
  web/src/pages/server/settings.test.tsx 'warns while the recovery key was never downloaded'
webcontrol "the delete dialog waits for the box before deleting without the key" web/src/components/app/delete-server.tsx \
  ": keyRisk && !withoutKey ? t(keyUnknown ? 'settings.deleteKeyUnknownFirst' : 'settings.deleteKeyFirst') : undefined}" \
  ': undefined}' \
  web/src/pages/server/settings.test.tsx 'warns while the recovery key was never downloaded'
webcontrol "the delete dialog confirms deleting without the key" web/src/components/app/delete-server.tsx \
  "keyRisk && withoutKey ? { confirm: typed.trim(), forgetKey: true } : { confirm: typed.trim() }" \
  '{ confirm: typed.trim() }' \
  web/src/pages/server/settings.test.tsx 'refusal when the page didn'

# Wave 7 after Bugbot's finding on d0492a3a: a copy that was made is recorded
# when the settings can't be read after it.
control "a made copy is recorded when the settings can't be read after it" internal/agent/offsite.go \
  'if lerr == nil && (!row.enabled || offsiteIdentity(row.cfg.Config) != offsiteIdentity(at.cfg.Config)) {' \
  'if lerr != nil || !row.enabled || offsiteIdentity(row.cfg.Config) != offsiteIdentity(at.cfg.Config) {' \
  ./internal/agent "^TestAMadeCopyIsRecordedUnlessTheSettingsReadAfterItChanged$/^the_settings_can't_be_read_once_it_is_made$"
control "a copy recorded without its settings says where it was made" internal/agent/offsite.go \
  '		row = at
	}
	s.copyDone(ctx, dest, row, b, cp)' \
  '	}
	s.copyDone(ctx, dest, row, b, cp)' \
  ./internal/agent "^TestAMadeCopyIsRecordedUnlessTheSettingsReadAfterItChanged$/^the_settings_can't_be_read_once_it_is_made$"
control "a copy recorded without its settings is logged" internal/agent/offsite.go \
  "s.log.Warn(\"the settings for copies somewhere else can't be read; the copy just made is recorded with those it was made with\", \"server\", s.id, \"backup\", b.ID, \"err\", lerr)" \
  '_ = lerr' \
  ./internal/agent "^TestAMadeCopyIsRecordedUnlessTheSettingsReadAfterItChanged$/^the_keys_can't_be_read_once_it_is_made$"

# Wave 7 after Bugbot's finding on d0492a3a: saving the settings for copies
# never puts an old encryption key back.
control "saving the settings for copies never writes the keys" internal/agent/offsite.go \
  '			private_key = excluded.private_key, ssh_public = excluded.ssh_public, updated_at = excluded.updated_at`,
		s.id, boolInt(r.enabled), string(b), r.secret, r.password, r.privateKey, r.sshPublic, s.now().UnixMilli())' \
  '			private_key = excluded.private_key, ssh_public = excluded.ssh_public, updated_at = excluded.updated_at, keys = ?`,
		s.id, boolInt(r.enabled), string(b), r.secret, r.password, r.privateKey, r.sshPublic, s.now().UnixMilli(), encodeKeys(r.keys))' \
  ./internal/agent '^TestSavingCopySettingsNeverPutsAnOldKeyBack$/^a_save_of_the_settings_racing_a_new_key$'
control "the first keys are stored only while there are none" internal/agent/offsite.go \
  "UPDATE offsite SET keys = ? WHERE server_id = ? AND keys = ''" \
  'UPDATE offsite SET keys = ? WHERE server_id = ?' \
  ./internal/agent '^TestSavingCopySettingsNeverPutsAnOldKeyBack$/^first_keys_stored_while_another_request_stored_its_own$'

# Wave 7 after Bugbot's findings on e6a1dfc7: a scheduled restart's countdown
# keeps an empty server awake, and with the allowlist off anyone who isn't
# banned wakes a sleeping server by joining.
control "a scheduled restart's countdown keeps an empty server awake" internal/agent/sleeping.go \
  'Busy: s.busy() || s.pregenRunning() || s.scheduleWorking() || s.hasCrossplay(),' \
  'Busy: s.busy() || s.pregenRunning() || s.hasCrossplay(),' \
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
		return machine{}, errUnknownServer' \
  'case false && recorded:
		return machine{}, errUnknownServer' \
  ./internal/panel '^TestServerRecordsFollowWhichMachinesAreStillJoined$'
control "a machine that lists a removed machine's server takes it over" internal/panel/machines.go \
  '				if !ownerActive && !unsure {' \
  '				if false && !ownerActive && !unsure {' \
  ./internal/panel '^TestServerRecordsFollowWhichMachinesAreStillJoined$'
control "every server in the list has its own slug" internal/panel/workspace.go \
  's.stableSlugs(ctx, out, list)' \
  '' \
  ./internal/panel '^TestEveryServerInTheListHasItsOwnSlug$'
control "a duplicate's number skips slugs another server has" internal/panel/workspace.go \
  'if next := fmt.Sprintf("%s-%d", base, i); !taken[next] && !avoid[next] {' \
  'if next := fmt.Sprintf("%s-%d", base, i); true {' \
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
  'if prof.Version != name || prof.Minecraft != b.mc || ver.ID != name || ver.InheritsFrom != b.mc {' \
  'if false && (prof.Version != name || prof.Minecraft != b.mc || ver.ID != name || ver.InheritsFrom != b.mc) {' \
  ./internal/minecraft/software '^TestInstallForgeRefuses$'
control "Forge's installer looks for Mojang's jar where Playkeeper verified it" internal/minecraft/software/forge.go \
  '.Replace(prof.ServerJarPath) != b.serverJarPath() {' \
  '.Replace(prof.ServerJarPath) != b.serverJarPath() && false {' \
  ./internal/minecraft/software '^TestInstallForgeRefuses$'
control "NeoForge's installers for Minecraft 1.21 to 1.21.11 install, NeoForm's data checked like the libraries" internal/minecraft/software/neoforge.go \
  'var neoforgeLibraryExts = []string{".jar", ".zip", ".tsrg.lzma"}' \
  'var neoforgeLibraryExts = []string{".jar"}' \
  ./internal/minecraft/software '^TestInstallNeoForgeForMinecraft121'
control "a NeoForge library that is neither a jar nor NeoForm's data is refused" internal/minecraft/software/neoforge.go \
  '!cleanRel(a.Path, neoforgeLibraryExts...)' \
  '!cleanRel(a.Path)' \
  ./internal/minecraft/software '^TestInstallNeoForgeRefuses$'
control "every Forge library has a plain path, a SHA-1 and a size" internal/minecraft/software/forge.go \
  'if !ok || a.Size <= 0 || !cleanRel(a.Path, ".jar", ".zip") {' \
  'if false && (!ok || a.Size <= 0 || !cleanRel(a.Path, ".jar", ".zip")) {' \
  ./internal/minecraft/software '^TestInstallForgeRefuses$'
control "a Forge server starts from Forge's shim jar" internal/minecraft/software/forge.go \
  'if prof.Path != shimCoord || !ok || shim == nil || shim.Path != into+"/"+shimRel {' \
  'if false && (prof.Path != shimCoord || !ok || shim == nil || shim.Path != into+"/"+shimRel) {' \
  ./internal/minecraft/software '^TestInstallForgeRefuses$'
control "the shim jar a Forge server starts is checked" internal/minecraft/software/forge.go \
  'checks = append(checks, Check{Path: shimName, Hash: shim.Hash, Size: shim.Size, Origin: Derived, Source: libs})' \
  '' \
  ./internal/minecraft/software '^TestInstallForgeRefuses$'
control "Forge's launch arguments are where the server container reads them" internal/minecraft/software/forge.go \
  'if !extractsForgeArgs(prof, b.argsPath()) {' \
  'if false && !extractsForgeArgs(prof, b.argsPath()) {' \
  ./internal/minecraft/software '^TestInstallForgeRefuses$'
control "Forge's launch arguments start its checked shim jar" internal/minecraft/software/forge.go \
  'case shimName != "" && !startsJar(string(args), shimName):' \
  'case false && !startsJar(string(args), shimName):' \
  ./internal/minecraft/software '^TestInstallForgeRefuses$'
control "the files Forge's installer builds are checked against the SHA-1s it publishes" internal/minecraft/software/forge.go \
  'out = append(out, Check{Path: into + "/" + p, Hash: h, Origin: Derived, Source: source})' \
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

# Minecraft 1.20.1: the oldest release offered, Forge's installers from
# before its shim jar, NeoForge's builds under Forge's name, and the packs
# that name them.
control "Playkeeper offers Minecraft from 1.20.1 on, not 1.20" internal/minecraft/software/catalog.go \
  'minecraft.CompareMinecraft(mc, minecraft.OldestRelease) >= 0' \
  'minecraft.CompareMinecraft(mc, "1.20") >= 0' \
  ./internal/minecraft/software '^(TestOffered|TestPinValidate|TestBuildsErrors)$'
control "Paper's catalog starts at Minecraft 1.20.1" internal/minecraft/fill.go \
  'CompareMinecraft(id, OldestRelease) < 0' \
  'CompareMinecraft(id, "1.20") < 0' \
  ./internal/minecraft '^TestCatalogStartsAtTheOldestRelease$'
control "Forge's installers before Minecraft 1.20.3 get Mojang's jar under its plain name" internal/minecraft/software/forge.go \
  'minecraft.CompareMinecraft(b.mc, "1.20.3") >= 0' \
  'minecraft.CompareMinecraft(b.mc, "1.20.3") >= -1' \
  ./internal/minecraft/software '^(TestInstallForgeForMinecraft1201|TestResolveForge)$'
control "a Forge server without a shim jar starts from the libraries its installer checked" internal/minecraft/software/forge.go \
  'case shimName == "" && (prof.Path != "" || slices.Contains(strings.Fields(string(args)), "-jar")):' \
  'case false:' \
  ./internal/minecraft/software '^TestInstallForgeForMinecraft1201Refuses$'
control "NeoForge's builds for Minecraft 1.20.1 are checked as the Forge installers they are" internal/minecraft/software/neoforge.go \
  'derive, args = DeriveForge, b.argsPath()' \
  '_ = b' \
  ./internal/minecraft/software '^(TestInstallNeoForgeForMinecraft1201|TestResolveNeoForge)$'
control "NeoForge's builds for Minecraft 1.20.1 come from its Forge-named artifact" internal/minecraft/software/neoforge.go \
  'fileURL = neoforgeForgeMaven + "/" + b.id() + "/" + name' \
  'fileURL = neoforgeMaven + "/" + b.id() + "/" + name' \
  ./internal/minecraft/software '^TestResolveNeoForge$'
control "NeoForge's list for Minecraft 1.20.1 keeps only its builds for 1.20.1" internal/minecraft/software/neoforge.go \
  'ok && reNeoForgeForge.MatchString(v)' \
  '(ok || true) && reNeoForgeForge.MatchString(v)' \
  ./internal/minecraft/software '^TestBuilds$'
control "Forge's checks are only for a build Forge's installer makes" internal/minecraft/software/plan.go \
  'd.Kind == DeriveForge && !ok {' \
  'd.Kind == DeriveForge && !ok && false {' \
  ./internal/minecraft/software '^TestPlanValidation$'
control "CurseForge's NeoForge builds for Minecraft 1.20.1 lose the version in front" internal/modpacks/requirements.go \
  'if t == "forge" || t == "neoforge" {' \
  'if t == "forge" {' \
  ./internal/modpacks '^TestPackDependenciesDecideTheServer$'
control "server files in a folder of their own give their mods" internal/modpacks/resolve.go \
  'if root := p.server.root(); root != "" && root != dir {' \
  'if root := p.server.root(); false && root != dir {' \
  ./internal/modpacks '^TestCurseForgeModsFromServerFiles$'
control "server files that hold only a mods folder keep it" internal/modpacks/resolve.go \
  'if root := p.server.root(); root != "" && root != dir {' \
  'if root := p.server.root(); root != "" {' \
  ./internal/modpacks '^TestCurseForgeModsFromServerFiles$'
control "mods a pack's server files leave out stay off the server" internal/modpacks/resolve.go \
  '	if holds {' \
  '	if holds && false {' \
  ./internal/modpacks '^TestCurseForgeModsFromServerFiles$'
control "server files that hold few of the pack's mods don't decide what the server gets" internal/modpacks/resolve.go \
  'return len(pack) > 0 && 2*held >= len(pack)' \
  'return len(pack) > 0' \
  ./internal/modpacks '^TestCurseForgeModsFromServerFiles$'
control "a mod CurseForge tags for players' games goes on the server when its server files have it" internal/modpacks/resolve.go \
  'if err := p.keepWhatServerFilesHave(mods, lim); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/modpacks '^TestCurseForgeModsFromServerFiles$'
control "a mod the server files put back isn't listed as left out" internal/modpacks/resolve.go \
  'p.skipped = slices.DeleteFunc(p.skipped, func(s Skipped) bool { return s.Path == c.path && s.Reason == addons.KindClientOnly })' \
  '_ = c.path' \
  ./internal/modpacks '^TestCurseForgeModsFromServerFiles$'
control "a CurseForge manifest's odd recommendedRam doesn't refuse the pack" internal/modpacks/curseforge/manifest.go \
  '	if err != nil || n < 0 {
		n = 0
	}' \
  '	if err != nil || n < 0 {
		return err
	}' \
  ./internal/modpacks/curseforge '^TestRecommendedRAM$'
control "a Forge server gets only the Forge build of a mod" internal/addons/target.go \
  'Loaders: []string{"forge"}, own: 1}' \
  'Loaders: []string{"neoforge"}, own: 1}' \
  ./internal/addons '^(TestTargetFor|TestPlanInstallPicksTheLoadersVersion)$'
control "Forge's crash report line counts as a crash" internal/minecraft/logparse.go \
  'This crash report has been saved to: |Crash report saved to |' \
  'This crash report has been saved to: |' \
  ./internal/minecraft '^TestParseRecognisesPlayerEvents$'
control "a player typing Java's out-of-memory error doesn't count" internal/minecraft/logparse.go \
  '^(?:\[\d{2}:\d{2}:\d{2}(?: (?:WARN|ERROR|FATAL)\]' \
  '(?:\[\d{2}:\d{2}:\d{2}(?: (?:WARN|ERROR|FATAL)\]' \
  ./internal/minecraft '^TestParseTakesOutOfMemoryOnlyFromLinesPlayersCantWrite$'
control "an INFO entry never says Java ran out of memory" internal/minecraft/logparse.go \
  '^(?:\[\d{2}:\d{2}:\d{2}(?: (?:WARN|ERROR|FATAL)\]' \
  '^(?:\[\d{2}:\d{2}:\d{2}(?: (?:INFO|WARN|ERROR|FATAL)\]' \
  ./internal/minecraft '^TestParseTakesOutOfMemoryOnlyFromLinesPlayersCantWrite$'
control "a player's chat turns no stop or crash into one for memory" internal/minecraft/logparse.go \
  '^(?:\[\d{2}:\d{2}:\d{2}(?: (?:WARN|ERROR|FATAL)\]' \
  '(?:\[\d{2}:\d{2}:\d{2}(?: (?:WARN|ERROR|FATAL)\]' \
  ./internal/agent '^TestOnlyJavaSaysItRanOutOfMemory$'
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
webcontrol "a machine to connect may run either supported system" web/src/pages/machines.tsx \
  "version: s.version })), 'or')" \
  "version: s.version })), 'and')" \
  web/src/pages/pages.test.tsx 'says how to get a command'
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
# Wave 8 after the joined-machine bug hunt on 1062eae9: friends' pack links
# and shared maps go to the machine that made them, resource packs stay with
# the dashboard's machine, New server never falls back to it, and each
# machine's audit rows are its own.
control "a recorded friends' pack link is asked only of the machine that made it" internal/panel/packshare.go \
  'm, serverID, err := s.linkMachine(packLink, token)' \
  'm, serverID, err := machine{}, "", errNoLinkRecord' \
  ./internal/panel '^TestAFriendsPackLinkOpensOnlyOnTheMachineThatMadeIt$'
control "sharing a server's pack records its link" internal/panel/packshare.go \
  's.recordLink(packLink, ps.Token, serverID, m)' \
  '_ = ps.Token' \
  ./internal/panel '^TestAFriendsPackLinkOpensOnlyOnTheMachineThatMadeIt$'
control "a recorded link opens only its own server's pack" internal/panel/packshare.go \
  'case fp.link.Server != serverID:' \
  'case false && fp.link.Server != serverID:' \
  ./internal/panel '^TestAFriendsPackLinkOpensOnlyOnTheMachineThatMadeIt$/answers_for_another_server$'
control "a link with no record that two machines open opens on neither" internal/panel/packshare.go \
  'case len(found) > 1:' \
  'case false && len(found) > 1:' \
  ./internal/panel '^TestAFriendsPackLinkOpensOnlyOnTheMachineThatMadeIt$/two_machines_open'
control "a link with no record opens on none while a machine can't answer" internal/panel/packshare.go \
  'case failed != nil:' \
  'case false && failed != nil:' \
  ./internal/panel '^TestAFriendsPackLinkOpensOnlyOnTheMachineThatMadeIt$/a_machine_can.t_answer$'
control "a shared map is asked of the machine that shared it" internal/panel/maps.go \
  'agent, ok := s.mapAgent(token)' \
  'agent, ok := s.agent, true' \
  ./internal/panel '^TestASharedMapOpensOnTheMachineThatSharedIt$/joined_machine'
control "sharing a map records its link" internal/panel/maps.go \
  's.recordLink(mapLink, token, serverID, m)' \
  '_ = token' \
  ./internal/panel '^TestASharedMapOpensOnTheMachineThatSharedIt$/joined_machine'
control "a joined machine's server refuses a resource pack" internal/panel/packs.go \
  'if m.Kind == remoteKind {' \
  'if false && m.Kind == remoteKind {' \
  ./internal/panel '^TestResourcePacksAreForTheDashboardsMachine$'
control "a machine installed to join a dashboard refuses a resource pack" internal/agent/packs.go \
  'if s.cfg.NoPanel {' \
  'if false && s.cfg.NoPanel {' \
  ./internal/agent '^TestAMachineWithoutADashboardRefusesResourcePacks$'
webcontrol "the Packs page holds resource packs back on a joined machine's server" web/src/pages/server/world-packs.tsx \
  'resource: joined ?' \
  'resource: joined && false ?' \
  src/pages/server/world.test.tsx 'holds resource pack uploads back'
webcontrol "New server never swaps a machine that's away for the dashboard's" web/src/pages/new-server.tsx \
  'const target = asked ? ws.machines.find((m) => m.id === asked) : ws.machine' \
  'const target = ws.machines.filter((m) => !isAway(m)).find((m) => m.id === asked) ?? ws.machine' \
  src/pages/new-server.test.tsx 'New server on a joined machine'
webcontrol "New server holds Create back while its machine is away" web/src/pages/new-server.tsx \
  'if (away) return away' \
  'if (false) return away' \
  src/pages/new-server.test.tsx 'New server on a joined machine'
webcontrol "New server keeps its machine once the flow starts" web/src/pages/new-server.tsx \
  'const choices = started || !chooser ?' \
  'const choices = !chooser ?' \
  src/pages/new-server.test.tsx 'keeps the machine once the flow starts'
control "the audit log takes at most 200 rows from each machine" internal/panel/server.go \
  'if len(out) == maxMachineAudit {' \
  'if false && len(out) == maxMachineAudit {' \
  ./internal/panel '^TestTheAuditLogKeepsEachMachineToItsShare$/500_rows'
control "the audit log dates no machine's row after now" internal/panel/server.go \
  'if a.TS.After(now) {' \
  'if false && a.TS.After(now) {' \
  ./internal/panel '^TestTheAuditLogKeepsEachMachineToItsShare$/500_rows'
control "the audit log takes each of a machine's rows once" internal/panel/server.go \
  'if seen[a.ID] {' \
  'if false && seen[a.ID] {' \
  ./internal/panel '^TestTheAuditLogKeepsEachMachineToItsShare$/again_and_again'
webcontrol "the audit log keys each row by its machine" web/src/pages/settings.tsx \
  "e.machineId ?? ''}" \
  "''}" \
  src/pages/settings.test.tsx 'numbered alike'
webcontrol "the audit log names the machine of an agent's row" web/src/pages/settings.tsx \
  'ws.machines.length > 1 ?' \
  'ws.machines.length > 99 ?' \
  src/pages/settings.test.tsx 'numbered alike'

# Wave 8, second bug hunt: New server sizes templates and packs by what they
# run, a team join shows once on Home, the CurseForge key goes to the machine
# the card names, and Discord's preview lists only what the message posts.
control "the catalog sizes a new server by the plugins its template brings" internal/agent/handlers.go \
  'if mods >= 0 || plugins >= 0 {' \
  'if mods >= 0 {' \
  ./internal/agent '^TestTheCatalogSizesMemoryForWhatTheServerRuns$'
control "a template without memory gets the sizing guide's suggestion" internal/templates/plan.go \
  'p.MemoryMB = fitting(opts, suggestedMemory(t, p.Type.ID))' \
  'p.MemoryMB = opts[0]' \
  ./internal/templates '^TestPlanMemoryFollowsTheSizingGuide$'
webcontrol "New server asks the catalog for what a template or pack runs" web/src/pages/new-server.tsx \
  "{ ...catalogFor(from, c?.type ?? 'paper', pack && packMods ? { ...pack, mods: packMods } : pack, tpl, types), fresh: true }" \
  "{ type: c?.type ?? 'paper', fresh: true }" \
  src/pages/new-server.test.tsx 'sizes a shared template'
webcontrol "New server counts a Paper template's plugins" web/src/pages/new-server.tsx \
  "return { type: plan.type, plugins: n }" \
  "return { type: plan.type }" \
  src/pages/new-server.test.tsx 'asks the catalog for'
control "only the dashboard's machine lists the team's joins" internal/panel/team.go \
  'if m.Kind == localKind {' \
  'if true {' \
  ./internal/panel '^TestHomeShowsEachTeamJoinOnce$'
control "Home's activity carries on while the dashboard's agent is down" internal/panel/team.go \
  'if !answered && len(machines) > 0 {' \
  'if errs[0] != nil || !answered && len(machines) > 0 {' \
  ./internal/panel '^TestHomeShowsEachTeamJoinOnce$'
webcontrol "the add-on sources card shows the machine it was opened for" web/src/components/app/addon-sources.tsx \
  'const target = machine ? ws.machines.find((m) => m.id === machine) : ws.machine' \
  'const target = ws.machine' \
  src/pages/pages.test.tsx 'shows and saves the key of'
webcontrol "the CurseForge key is saved on the machine the card shows" web/src/components/app/addon-sources.tsx \
  "post<AddonSources>(machineApi(machineId, '/addon-sources/curseforge')" \
  "post<AddonSources>(machineApi('m2345abcde', '/addon-sources/curseforge')" \
  src/pages/pages.test.tsx 'shows and saves the key of'
webcontrol "only the owner changes a machine's CurseForge key" web/src/components/app/addon-sources.tsx \
  "const locked = ws.me.user.role === 'owner' ? undefined : t('reason.ownerOnly')" \
  'const locked = undefined' \
  src/pages/pages.test.tsx 'shows and saves the key of'
webcontrol "New server's CurseForge link chooses its machine" web/src/components/app/modpacks.tsx \
  "linkProps({ name: 'addon-sources', machine: machineId })" \
  "linkProps({ name: 'addon-sources' })" \
  src/pages/pages.test.tsx 'sends whoever needs a CurseForge key'
webcontrol "Discord's preview lists only the dashboard machine's servers" web/src/pages/discord.tsx \
  "return servers.filter((x) => machineOf(x, machines)?.kind !== 'remote')" \
  'return servers' \
  src/pages/pages.test.tsx 'previews the live status'

# Wave 7: the sleep operation looks again right before it stops the server,
# and saving the sleep setting takes the operation lock.
control "a sleep decided with another setting is called off" internal/agent/sleeping.go \
  's.desired() != api.DesiredRunning || s.sleepSettings() != set' \
  's.desired() != api.DesiredRunning' \
  ./internal/agent '^TestSleepLooksAgainBeforeItStopsTheServer$'
control "a sleep is called off when someone joined since it decided" internal/agent/sleeping.go \
  'if !s.nobodyOn() || s.pregenRunning() || s.scheduleWorking() || s.hasCrossplay() {' \
  'if false {' \
  ./internal/agent '^TestSleepLooksAgainBeforeItStopsTheServer$/^someone_joined_as_it_looks_again$'
# The same lock refuses turning sleep off during a backup; that test can't be
# the control, because the start begun without the lock hangs its cleanup.
control "saving the sleep setting takes the operation lock" internal/agent/sleeping.go \
  'release, ok := s.holdOpLock()
	if !ok {
		return nil, s.busyError()' \
  'release, ok := func() {}, true
	if !ok {
		return nil, s.busyError()' \
  ./internal/agent '^TestSleepLooksAgainBeforeItStopsTheServer$/^sleep_turned_off_while_it_runs$'

# Wave 7: a staging folder that can't be read may hold any server's swap
# journal, so each caller keeps what a restore may need and says why.
control "a staging folder that can't be read may hold any server's swap journal" internal/agent/backuprules.go \
  'if err != nil && !errors.Is(err, fs.ErrNotExist) {' \
  'if err != nil && !errors.Is(err, fs.ErrNotExist) && false {' \
  ./internal/agent '^TestAnUnreadableStagingFolderKeepsWhatAnyRestoreMayNeed$'
control "the rules keep every rollback archive while the staging folder can't be read" internal/agent/backuprules.go \
  'swaps = map[string]*swapJournal{"": nil}' \
  'swaps = nil' \
  ./internal/agent '^TestAnUnreadableStagingFolderKeepsWhatAnyRestoreMayNeed$/^backup_rules$'
control "the Disk space page counts every server busy while the staging folder can't be read" internal/agent/disk.go \
  '		journals = append(journals, nil)
' \
  '' \
  ./internal/agent '^TestAnUnreadableStagingFolderKeepsWhatAnyRestoreMayNeed$/^Disk_space$'
control "the Disk space page says the staging folder can't be read" internal/agent/disk.go \
  'diskusage.Problem{Code: diskusage.CodeRestoresUnknown' \
  'diskusage.Problem{Code: "other"' \
  ./internal/agent '^TestAnUnreadableStagingFolderKeepsWhatAnyRestoreMayNeed$/^Disk_space$'
control "the World tab keeps every world copy while the staging folder can't be read" internal/agent/backuprules.go \
  'if err != nil {
		return true, err
	}' \
  'if err != nil {
		return false, nil
	}' \
  ./internal/agent '^TestAnUnreadableStagingFolderKeepsWhatAnyRestoreMayNeed$/^World_tab$'
control "the World tab says the staging folder can't be read" internal/agent/backups.go \
  'if unsettled, err := s.restoreUnsettled(); err != nil {' \
  'if unsettled, err := s.restoreUnsettled(); err != nil && false {' \
  ./internal/agent '^TestAnUnreadableStagingFolderKeepsWhatAnyRestoreMayNeed$/^World_tab$'

# Wave 7, from the bug hunt: failed starts and wakes leave nothing answering
# for a server that isn't asleep, bans hold with the allowlist on, every
# operation has a busy label, and unreadable backup rules delete nothing.
control "a failed start closes the stand-in" internal/agent/lifecycle.go \
  '		_ = s.setDesired(api.DesiredStopped)
	}
	s.leaveSleep()' \
  '		_ = s.setDesired(api.DesiredStopped)
	}' \
  ./internal/agent '^TestAFailedStartLeavesNothingAnsweringForTheServer$'
control "a sleeping server started outside Playkeeper lets go of the stand-in" internal/agent/sleeping.go \
  '_ = s.setDesired(api.DesiredRunning)
		s.leaveSleep()
		return' \
  '_ = s.setDesired(api.DesiredRunning)
		s.endSleepPeriod(s.now().UTC(), "")
		return' \
  ./internal/agent '^TestSleepAndWakeTransitions$/^started_outside_Playkeeper$'
control "pruning leaves the copies alone while one is being downloaded" internal/agent/offsite.go \
  '	if !s.copyReads.TryLock() {
		s.log.Info("a copy is being downloaded, so the backup rules delete copies after the next one", "server", s.id)
		return
	}
	defer s.copyReads.Unlock()
' \
  '' \
  ./internal/agent '^TestPruningLeavesACopyThatIsBeingDownloaded$/^a_restore_is_downloading_it$'
control "a restore from a copy holds the copy downloads' lock" internal/agent/offsite.go \
  '	s.copyReads.RLock()
	got, err := dest.Download(ctx, dl)
	s.copyReads.RUnlock()' \
  '	got, err := dest.Download(ctx, dl)' \
  ./internal/agent '^TestPruningLeavesACopyThatIsBeingDownloaded$/^a_restore_is_downloading_it$'
control "a join's wake waits for the operation however long it runs" internal/agent/sleeping.go \
  '	for {
		if s.ctx.Err() != nil || s.desired() != api.DesiredSleeping {
			return
		}
		_, err := s.beginOp("wake"' \
  '	giveUp := time.Now().Add(wakeRetry)
	for {
		if s.ctx.Err() != nil || s.desired() != api.DesiredSleeping || time.Now().After(giveUp) {
			return
		}
		_, err := s.beginOp("wake"' \
  ./internal/agent '^TestSleepAndWakeTransitions$/^a_player_wakes_it_during_a_backup_that_outlasts_its_retries$'
control "one join's wake waits at a time" internal/agent/sleeping.go \
  '	if s.auto.wakePending {' \
  '	if false && s.auto.wakePending {' \
  ./internal/agent '^TestSleepAndWakeTransitions$/^a_player_wakes_it_during_a_backup_that_outlasts_its_retries$'
control "a wake whose start stopped the server leaves it stopped" internal/agent/sleeping.go \
  'if d := s.desired(); d != api.DesiredRunning && d != api.DesiredSleeping {
				s.leaveSleep()' \
  'if d := s.desired(); false && d != api.DesiredRunning && d != api.DesiredSleeping {
				s.leaveSleep()' \
  ./internal/agent '^TestSleepAndWakeTransitions$/^a_wake_that_finds_the_server_software_changed$'
control "a banned player doesn't wake a server whose allowlist is on" internal/agent/sleeping.go \
  'if strings.EqualFold(name, player) {' \
  'if false && strings.EqualFold(name, player) {' \
  ./internal/agent '^TestWhoMayWakeASleepingServer$/^allowlist_on,_banned_though_listed_or_an_operator$'
control "restoring from a recovery key has a busy label" internal/agent/lifecycle.go \
  '"offsite-recover": "restoring a server from a recovery key",' \
  '' \
  ./internal/agent '^TestEveryOperationHasABusyLabel$'
control "backup rules that can't be read are an error, not the defaults" internal/agent/backuprules.go \
  'SELECT backup_rules FROM servers WHERE id = ?`, s.id).Scan(&raw); err != nil {' \
  'SELECT backup_rules FROM servers WHERE id = ?`, s.id).Scan(&raw); false && err != nil {' \
  ./internal/agent '^TestBackupRulesThatCantBeReadDeleteNothing$/^the_rules_can.t_be_read$'
control "saved backup rules that don't parse are an error" internal/agent/backuprules.go \
  'return retention.Settings{}, nil, false, fmt.Errorf("the saved backup rules are not valid: %w", err)' \
  'return retention.DefaultSettings(), time.UTC, false, nil' \
  ./internal/agent '^TestBackupRulesThatCantBeReadDeleteNothing$/^rules_that_don.t_parse$'
control "a copy queue that can't be read is an error" internal/agent/backuprules.go \
  'return nil, fmt.Errorf("the copy queue could not be read: %w", err)
	}
	defer rows.Close()' \
  'return map[string]bool{}, nil
	}
	defer rows.Close()' \
  ./internal/agent '^TestBackupRulesThatCantBeReadDeleteNothing$/^the_copy_queue_can.t_be_read$'
control "the catalog sizes memory for what the server runs, not always vanilla" internal/agent/handlers.go \
  'Sizing: memorySizing(a.catalogWorkload(forServer, typ, mods, plugins), opts),' \
  'Sizing: memorySizing(sizing.Vanilla, opts),' \
  ./internal/agent '^TestTheCatalogSizesMemoryForWhatTheServerRuns$'
control "a new server from a pack is sized by the pack's mods" internal/agent/handlers.go \
  'if mods >= 0 || plugins >= 0 {
		return sizing.WorkloadFor(max(mods, 0), max(plugins, 0))' \
  'if false && (mods >= 0 || plugins >= 0) {
		return sizing.WorkloadFor(max(mods, 0), max(plugins, 0))' \
  ./internal/agent '^TestTheCatalogSizesMemoryForWhatTheServerRuns$'
control "an existing server's plugins count as plugins and its mods as mods" internal/agent/handlers.go \
  'if addonDir(*sc) == "plugins" {' \
  'if addonDir(*sc) != "plugins" {' \
  ./internal/agent '^TestTheCatalogSizesMemoryForWhatTheServerRuns$'
control "a disputed server is answered as disputed, not as an agent that's down" internal/panel/machines.go \
  'if errors.Is(err, errDisputed) {' \
  'if false && errors.Is(err, errDisputed) {' \
  ./internal/panel '^TestJoinPathsSayWhyAServerCantBeReached$'
control "a server no machine runs is answered as not found, not as an agent that's down" internal/panel/machines.go \
  'if errors.Is(err, errUnknownServer) {' \
  'if false && errors.Is(err, errUnknownServer) {' \
  ./internal/panel '^TestJoinPathsSayWhyAServerCantBeReached$'

# Wave 8, second bug hunt: what a machine sends reaches the browser as a file
# to save, a refusal never signs it out, and the web follows a machine's link
# only when it is an https: link to another site. A removed machine's servers
# keep their invites, the join page says why a server can't be asked, and a
# server keeps the slug it was shown with.
control "the recovery key downloads as bytes, whatever type its machine gives" internal/panel/automation.go \
  'h.Set("Content-Type", "application/octet-stream")' \
  'h.Set("Content-Type", resp.Header.Get("Content-Type"))' \
  ./internal/panel '^TestAMachinesDownloadsAreFilesToSaveNeverAPage$'
# shellcheck disable=SC2016
control "the recovery key is a file to save, never shown in the tab" internal/panel/automation.go \
  'h.Set("Content-Disposition", `attachment; filename="`+recoveryKeyFileName' \
  'h.Set("Content-Disposition", `inline; filename="`+recoveryKeyFileName' \
  ./internal/panel '^TestAMachinesDownloadsAreFilesToSaveNeverAPage$'
control "the recovery key runs no script, even when opened" internal/panel/automation.go \
  'h.Set("Content-Security-Policy", "sandbox")
	h.Set("X-Content-Type-Options", "nosniff")' \
  'h.Set("X-Content-Type-Options", "nosniff")' \
  ./internal/panel '^TestAMachinesDownloadsAreFilesToSaveNeverAPage$'
control "the recovery key keeps only a name its agent would give" internal/panel/automation.go \
  'err == nil && reRecoveryKeyFile.MatchString(params["filename"]) {' \
  'err == nil && params["filename"] != "" {' \
  ./internal/panel '^TestAMachinesDownloadsAreFilesToSaveNeverAPage$'
control "a machine's refusal of the recovery key never signs the browser out" internal/panel/automation.go \
  'if resp.StatusCode >= 400 {
		s.agentFailure(w, agentclient.DecodeError(resp))' \
  'if resp.StatusCode >= 400 {
		w.WriteHeader(resp.StatusCode)' \
  ./internal/panel '^TestAMachinesDownloadsAreFilesToSaveNeverAPage$'
control "the friends' pack file is a file to save, never shown in the tab" internal/panel/packshare.go \
  'mime.FormatMediaType("attachment", map[string]string{"filename": packFileName(' \
  'mime.FormatMediaType("inline", map[string]string{"filename": packFileName(' \
  ./internal/panel '^TestAMachinesDownloadsAreFilesToSaveNeverAPage$'
control "the friends' pack file runs no script, even when opened" internal/panel/packshare.go \
  'h.Set("Content-Security-Policy", "sandbox")
	h.Set("Cache-Control", "no-store")
	io.Copy(w, io.LimitReader(resp.Body, maxFriendsPackFile))' \
  'h.Set("Cache-Control", "no-store")
	io.Copy(w, io.LimitReader(resp.Body, maxFriendsPackFile))' \
  ./internal/panel '^TestAMachinesDownloadsAreFilesToSaveNeverAPage$'
control "the friends' pack file keeps only a slug's name" internal/panel/packshare.go \
  'ok && share.ValidSlug(slug) {' \
  'ok && slug != "" {' \
  ./internal/panel '^TestAMachinesDownloadsAreFilesToSaveNeverAPage$'
control "a machine's refusal of the friends' pack file never signs the browser out" internal/panel/packshare.go \
  'if resp.StatusCode >= 400 {
		s.agentFailure(w, agentclient.DecodeError(resp))
		return
	}
	h := w.Header()
	h.Set("Content-Type", share.ContentType)' \
  'if resp.StatusCode >= 400 {
		w.WriteHeader(resp.StatusCode)
		return
	}
	h := w.Header()
	h.Set("Content-Type", share.ContentType)' \
  ./internal/panel '^TestAMachinesDownloadsAreFilesToSaveNeverAPage$'
control "a machine's refusal of an add-on's icon never signs the browser out" internal/panel/addons.go \
  'w.Header().Set("Cache-Control", "no-store")
		s.agentFailure(w, agentclient.DecodeError(resp))' \
  'w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(resp.StatusCode)
		_ = agentclient.DecodeError' \
  ./internal/panel '^TestAMachinesRefusalNeverSignsTheBrowserOut$'
control "a machine's refusal of its map never signs the browser out" internal/panel/maps.go \
  'status := resp.StatusCode
		if status >= 400 {' \
  'status := resp.StatusCode
		if false && status >= 400 {' \
  ./internal/panel '^TestAMachinesRefusalNeverSignsTheBrowserOut$'
control "a machine's refusal of a world import never signs the browser out" internal/panel/worldimports.go \
  'status := resp.StatusCode
	if status >= 400 {' \
  'status := resp.StatusCode
	if false && status >= 400 {' \
  ./internal/panel '^TestAMachinesRefusalNeverSignsTheBrowserOut$'
webcontrol "a machine's link is followed only to another site" web/src/lib/links.ts \
  "u.protocol === 'https:' && u.origin !== window.location.origin ? u.href : undefined" \
  "u.protocol === 'https:' ? u.href : undefined" \
  web/src/lib/links.test.ts
webcontrol "a machine's link is followed only when it is https:" web/src/lib/links.ts \
  "u.protocol === 'https:' && u.origin !== window.location.origin" \
  "u.origin !== window.location.origin" \
  web/src/lib/links.test.ts
webcontrol "an add-on's source link shows only for another site" web/src/pages/server/plugins/detail.tsx \
  'const link = externalLink(href)' \
  'const link = href' \
  web/src/pages/server/plugins/detail.test.tsx
webcontrol "an add-on only on its author's site links only to another site" web/src/pages/server/plugins/detail.tsx \
  '{externalLink(f.url) && (
              <Button variant="outline" size={size} className="w-full" render={<a href={externalLink(f.url)}' \
  '{f.url && (
              <Button variant="outline" size={size} className="w-full" render={<a href={f.url}' \
  web/src/pages/server/plugins/plugins.test.tsx 'sends people to the author.s site at'
webcontrol "a dependency from another site links only to another site" web/src/pages/server/plugins/detail.tsx \
  '{externalLink(f.url) && (
              <Button
                variant="outline"
                size={size}
                className="w-full"
                render={<a href={externalLink(f.url)}' \
  '{f.url && (
              <Button
                variant="outline"
                size={size}
                className="w-full"
                render={<a href={f.url}' \
  web/src/pages/server/plugins/plugins.test.tsx 'links a dependency another site names to'
webcontrol "an installed add-on's source page opens only on another site" web/src/pages/server/plugins/state.tsx \
  'const link = externalLink(d.card.pageUrl)' \
  'const link = d.card.pageUrl' \
  web/src/pages/server/plugins/plugins.test.tsx 'opens an installed add-on.s source page at'
webcontrol "a modpack links only to its page on another site" web/src/components/app/modpacks.tsx \
  '{externalLink(card.pageUrl) && (
                <a href={externalLink(card.pageUrl)}' \
  '{card.pageUrl && (
                <a href={card.pageUrl}' \
  web/src/pages/pages.test.tsx 'links a pack to'
control "a removed machine's servers keep their invites for when it joins again" internal/panel/workspace.go \
  'm.revoked_at = 0))`' \
  'm.revoked_at >= 0))`' \
  ./internal/panel '^TestARemovedMachinesServersKeepTheirInvites$'
control "a server deleted on a machine that is still joined loses its invites" internal/panel/workspace.go \
  'if everyMachine && len(ids) > 0 {' \
  'if false && everyMachine && len(ids) > 0 {' \
  ./internal/panel '^TestARemovedMachinesServersKeepTheirInvites$'
control "the join page answers a server two machines list as a link that doesn't work for now" internal/panel/join.go \
  'case errors.Is(err, errDisputed):' \
  'case false && errors.Is(err, errDisputed):' \
  ./internal/panel '^TestTheJoinPageSaysWhyAServerCantBeReached$'
control "the join page answers a failed machine lookup as one, not as an agent that's down" internal/panel/join.go \
  'case errors.Is(err, errServerMachine):' \
  'case false && errors.Is(err, errServerMachine):' \
  ./internal/panel '^TestTheJoinPageSaysWhyAServerCantBeReached$'
control "the join page logs a server no machine runs by its own reason" internal/panel/join.go \
  'e.Reason = "no machine runs the invite'"'"'s server"' \
  '_ = "no machine runs the invite'"'"'s server"' \
  ./internal/panel '^TestTheJoinPageSaysWhyAServerCantBeReached$'
control "the dashboard's machine keeps its own slugs, which its Discord links use" internal/panel/workspace.go \
  'case local.ID != "" && sv["machineId"] == local.ID:' \
  'case false && sv["machineId"] == local.ID:' \
  ./internal/panel '^TestAServerKeepsTheSlugItWasShownWith$'
control "a server keeps the slug it was shown with" internal/panel/workspace.go \
  'choose(e, e.kept)' \
  'choose(e, e.agentSlug)' \
  ./internal/panel '^TestAServerKeepsTheSlugItWasShownWith$'
control "servers new at once get slugs in the order the dashboard first saw them" internal/panel/workspace.go \
  'SELECT server_id, machine_id, slug, status FROM server_machines ORDER BY rowid`' \
  'SELECT server_id, machine_id, slug, status FROM server_machines ORDER BY rowid DESC`' \
  ./internal/panel '^TestAServerKeepsTheSlugItWasShownWith$'
control "a removed machine's servers keep their slugs while it's away" internal/panel/workspace.go \
  'e := &entry{id: id, kept: kept, recorded: true, sv: elsewhere[id]}' \
  'e := &entry{id: id, kept: kept, recorded: true, sv: elsewhere[id]}
			if e.sv == nil {
				continue
			}' \
  ./internal/panel '^TestAServerKeepsTheSlugItWasShownWith$'
control "a numbered slug skips every slug a server has or its agent gave it" internal/panel/workspace.go \
  '!taken[next] && !avoid[next]' \
  '!taken[next]' \
  ./internal/panel '^TestEveryServerInTheListHasItsOwnSlug$'
control "the dashboard's machine hears the slugs shown for other machines' servers" internal/panel/workspace.go \
  'if list == s.toldSlugs.list {' \
  'if list == s.toldSlugs.list || true {' \
  ./internal/panel '^TestAServerKeepsTheSlugItWasShownWith$'
control "a new server gets none of the slugs shown for other machines' servers" internal/agent/servers.go \
  'if elsewhere[s] {' \
  'if false && elsewhere[s] {' \
  ./internal/agent '^TestANewServerSkipsTheSlugsOfServersElsewhere$'
control "the dashboard can only hold back slugs a server could have" internal/agent/servers.go \
  'if !reSlug.MatchString(s) {' \
  'if false && !reSlug.MatchString(s) {' \
  ./internal/agent '^TestANewServerSkipsTheSlugsOfServersElsewhere$'

# Wave 7 second bug hunt and Bugbot on eb3d7540: deleting a server asks
# whenever keys exist and the recovery key was never downloaded, a copy
# forgotten by a change of place included, and when the settings for copies
# can't be read.
control "a key never downloaded is at risk when copies are off and none is recorded" internal/agent/automation.go \
  '	if !row.hasKeys || row.keySavedAt != nil {' \
  '	if !row.hasKeys || row.keySavedAt != nil || !row.enabled {' \
  ./internal/agent '^TestDeletingAServerAsksBeforeItDeletesTheOnlyKeyToItsCopies$/^copies_turned_off,_then_forgotten_by_a_change_of_place'
control "a delete asks when the settings for copies can't be read" internal/agent/automation.go \
  '	if err != nil {
		return s.keyUnknown(err)
	}' \
  '	if err != nil {
		return nil
	}' \
  ./internal/agent '^TestDeletingAServerAsksBeforeItDeletesTheOnlyKeyToItsCopies$/^the_settings_for_copies_unreadable$'
control "a confirmed delete goes ahead when the settings can't be read" internal/agent/handlers.go \
  '	if err := s.keyNotSaved(); err != nil && !req.ForgetKey {' \
  '	if err := s.keyNotSaved(); err != nil && (!req.ForgetKey || err.(*apiError).Reason == reasonKeyUnknown) {' \
  ./internal/agent '^TestDeletingAServerAsksBeforeItDeletesTheOnlyKeyToItsCopies$/^the_settings_for_copies_unreadable,_and_the_delete_confirmed$'
control "a delete refused for unreadable settings is audited as such" internal/agent/handlers.go \
  '			detail = "the settings for its copies couldn'"'"'t be read"' \
  '' \
  ./internal/agent '^TestDeletingAServerAsksBeforeItDeletesTheOnlyKeyToItsCopies$/^the_settings_for_copies_unreadable$'
control "a count that fails claims no number of copies" internal/agent/automation.go \
  '		s.log.Warn("the recorded copies couldn'"'"'t be counted", "server", s.id, "err", err)' \
  '		params["copies"] = 0' \
  ./internal/agent '^TestDeletingAServerAsksBeforeItDeletesTheOnlyKeyToItsCopies$/^the_copies_can.t_be_counted,_the_key_never_downloaded$'
webcontrol "the delete dialog warns while copies are off and none is recorded" web/src/components/app/delete-server.tsx \
  'return !!v?.key && !v.key.savedAt' \
  'return !!v?.key && !v.key.savedAt && (v.copies > 0 || v.enabled)' \
  web/src/pages/server/settings.test.tsx 'warns while copies are off and none is recorded'
webcontrol "the delete dialog takes the refusal when the settings can't be read" web/src/components/app/delete-server.tsx \
  "const keyRefusals = ['recovery_key_not_saved', 'recovery_key_unknown']" \
  "const keyRefusals = ['recovery_key_not_saved']" \
  web/src/pages/server/settings.test.tsx 'read the settings for copies'
webcontrol "the delete dialog says it couldn't read the settings" web/src/components/app/delete-server.tsx \
  "title={t(keyUnknown ? 'settings.deleteKeyUnknownTitle' : 'settings.deleteKeyTitle')}" \
  "title={t('settings.deleteKeyTitle')}" \
  web/src/pages/server/settings.test.tsx 'read the settings for copies'

# Downloading the recovery key records only the keys in the file as saved.
control "downloading the recovery key records only the keys in the file" internal/agent/offsite.go \
  'WHERE server_id = ? AND keys = ?`, s.now().UnixMilli(), f.Folder, s.id, row.keysRead)' \
  'WHERE server_id = ?`, s.now().UnixMilli(), f.Folder, s.id)' \
  ./internal/agent '^TestDownloadingTheRecoveryKeySavesOnlyTheKeysInTheFile$/^a_new_key_made_as_the_file_was_sent$'

# Checking a copy has no fixed deadline, can be cancelled, and pins the
# copies against pruning while it downloads.
control "a check of a copy has no fixed deadline" internal/agent/lifecycle.go \
  'var noDeadline = map[string]bool{"offsite-restore": true, "offsite-check": true, "offsite-recover": true}' \
  'var noDeadline = map[string]bool{"offsite-restore": true, "offsite-recover": true}' \
  ./internal/agent '^TestRestoringOrCheckingACopyOutlastsTheOperationDeadline$/^checking_a_copy$'
control "a check of a copy can be cancelled" internal/agent/offsite.go \
  '	op, err := s.beginOp("offsite-check", actor, func(ctx context.Context, h *opHandle) error {
		h.allowCancel()
' \
  '	op, err := s.beginOp("offsite-check", actor, func(ctx context.Context, h *opHandle) error {
' \
  ./internal/agent '^TestRestoringOrCheckingACopyOutlastsTheOperationDeadline$/^a_check_cancelled_while_it_downloads$'
control "a check's cancel stops a check" internal/agent/offsite.go \
  's.cancelOp("offsite-check", req.OperationID)' \
  's.cancelOp("offsite-restore", req.OperationID)' \
  ./internal/agent '^TestRestoringOrCheckingACopyOutlastsTheOperationDeadline$/^a_check_cancelled_while_it_downloads$'
control "a check cancelled as its copy finishes coming records nothing" internal/agent/offsite.go \
  '		got, _, err := s.fetchCopy(ctx, h, dest, archive, dir)
		if !h.commit() {
			return context.Canceled
		}' \
  '		got, _, err := s.fetchCopy(ctx, h, dest, archive, dir)
		h.commit()' \
  ./internal/agent '^TestRestoringOrCheckingACopyOutlastsTheOperationDeadline$/^a_check_cancelled_as_its_copy_finishes_coming$'
control "a check holds the copy downloads' lock" internal/agent/offsite.go \
  '	s.copyReads.RLock()
	got, err := dest.Download(ctx, dl)
	s.copyReads.RUnlock()' \
  '	got, err := dest.Download(ctx, dl)' \
  ./internal/agent '^TestPruningLeavesACopyThatIsBeingDownloaded$/^a_check_is_downloading_it$'
control "the dashboard reaches a check's cancel" internal/panel/server.go \
  '		smAs(actMakeBackups, "POST", "/api/servers/{id}/offsite/check/cancel", "/v1/servers/{id}/offsite/check/cancel"),
' \
  '' \
  ./internal/panel '^TestWaveSevenRoutesReachTheAgent$'
webcontrol "a copy's menu cancels its check while it runs" web/src/pages/server/copy-restore.tsx \
  "const checking = s.operation?.kind === 'offsite-check' && s.operation.detail?.name === c.name ? s.operation : undefined" \
  'const checking = undefined as Operation | undefined' \
  web/src/pages/pages.test.tsx 'cancels a check of a backup kept only somewhere else'

# A join whose wake never starts gives the stand-in its wake back, so joins
# while a long operation holds the server don't use up the wakes an hour
# allows.
control "a join whose wake doesn't start gives the wake back" internal/agent/sleeping.go \
  '		if !began {
			s.wakeCalledOff()
		}' \
  '		if false && !began {
			s.wakeCalledOff()
		}' \
  ./internal/agent '^TestSleepAndWakeTransitions$/^players_keep_joining_during_a_restore_of_a_copy$'
control "the stand-in forgets a wake given back" internal/sleep/limit.go \
  '	if n := len(l.recent); n > 0 {
		l.recent = l.recent[:n-1]
	}' \
  '' \
  ./internal/sleep '^(TestWakeLimitGivesBackAWakeThatNeverStarted|TestWakesCalledOffAreGivenBack)$'
control "a wake given back ends its waking window" internal/sleep/limit.go \
  '	}
	l.wakingUntil = time.Time{}
}

func (l *wakeLimit) forget' \
  '	}
}

func (l *wakeLimit) forget' \
  ./internal/sleep '^TestWakeLimitGivesBackAWakeThatNeverStarted$'

# Wave 7, from the second bug hunt: only a backup that is gone leaves the
# copy queue, the stale-upload clean-up waits for a queue that was read, new
# credentials stop the upload, scheduled work waits for a busy server as long
# as it may, automatic backups that can't be read are refused, and an edit
# keeps the run the runner saved.
control "a backup whose record can't be read stays queued" internal/agent/offsite.go \
  "		s.uploadFailed(job.ctx, job, fmt.Errorf(\"the backup's record can't be read: %w\", err))
		return false" \
  '		return gone()' \
  ./internal/agent '^TestOnlyABackupThatIsGoneLeavesTheCopyQueue$/^its_record_can.t_be_read$'
control "a backup whose archive can't be opened stays queued" internal/agent/offsite.go \
  "		s.uploadFailed(job.ctx, job, fmt.Errorf(\"the backup's archive can't be opened: %w\", err))
		return false" \
  '		return gone()' \
  ./internal/agent '^TestOnlyABackupThatIsGoneLeavesTheCopyQueue$/^its_archive_can.t_be_opened$'
control "a backup with no record leaves the queue" internal/agent/offsite.go \
  'return errors.As(err, &ae) && (ae.Code == api.CodeNotFound || ae.Code == api.CodeInvalid)' \
  'return errors.As(err, &ae) && ae.Code == api.CodeInvalid' \
  ./internal/agent '^TestOnlyABackupThatIsGoneLeavesTheCopyQueue$/^its_record_deleted$'
control "a backup whose archive doesn't exist leaves the queue" internal/agent/offsite.go \
  '	case errors.Is(err, fs.ErrNotExist):' \
  '	case false && errors.Is(err, fs.ErrNotExist):' \
  ./internal/agent '^TestOnlyABackupThatIsGoneLeavesTheCopyQueue$/^its_archive_deleted$'
control "a backup is queued when whether copies are on can't be read" internal/agent/offsite.go \
  "		s.log.Warn(\"whether copies somewhere else are on can't be read, so the backup is queued for its copy anyway\", \"server\", s.id, \"backup\", backupID, \"err\", err)" \
  '		return' \
  ./internal/agent '^TestABackupIsQueuedWhenWhetherCopiesAreOnCantBeRead$/^copies_on$'
control "a backup queued while copies were never on leaves the queue" internal/agent/offsite.go \
  '		if row.exists && !row.enabled {' \
  '		if false {' \
  ./internal/agent '^TestABackupIsQueuedWhenWhetherCopiesAreOnCantBeRead$/^copies_never_turned_on$'
control "unfinished uploads are cleaned up only from a queue that was read" internal/agent/offsite.go \
  '		if keep, err := s.queuedStates(); err != nil {' \
  '		if keep, _ := s.queuedStates(); false {' \
  ./internal/agent '^TestUnfinishedUploadsAreCleanedUpOnlyFromAQueueThatWasRead$/^the_queue_can.t_be_read$'
control "a copy queue that can't be read is an error, not empty" internal/agent/offsite.go \
  '	if err != nil {
		return nil, err
	}
	states, _, err := scanStates(rows)' \
  '	if err != nil {
		return nil, nil
	}
	states, _, err := scanStates(rows)' \
  ./internal/agent '^TestUnfinishedUploadsAreCleanedUpOnlyFromAQueueThatWasRead$/^the_queue_can.t_be_read$'
control "a copy queue that fails part way is an error" internal/agent/offsite.go \
  '	return states, n, rows.Err()' \
  '	return states, n, nil' \
  ./internal/agent '^TestUnfinishedUploadsAreCleanedUpOnlyFromAQueueThatWasRead$/^the_queue_fails_part_way$'
control "new credentials stop the upload still using the old ones" internal/agent/offsite.go \
  '	if moved || !next.enabled || !sameConnection(row, next) {' \
  '	if moved || !next.enabled {' \
  ./internal/agent '^TestNewCredentialsStopTheUploadStillUsingTheOldOnes$/^the_S3_secret_changed$'
control "a new S3 secret is new credentials" internal/agent/offsite.go \
  ' && a.secret == b.secret' \
  '' \
  ./internal/agent '^TestNewCredentialsStopTheUploadStillUsingTheOldOnes$/^the_S3_secret_changed$'
control "a new SFTP password is new credentials" internal/agent/offsite.go \
  ' && a.password == b.password' \
  '' \
  ./internal/agent '^TestNewCredentialsStopTheUploadStillUsingTheOldOnes$/^the_SFTP_password_changed$'
control "a round whose settings changed before the claim uploads nothing more" internal/agent/offsite.go \
  'at.keys.Current.Recipient || !sameConnection(row, at) {' \
  'at.keys.Current.Recipient {' \
  ./internal/agent '^TestARoundWhoseSettingsChangedUploadsNothingMore$'
control "copies turned off between two copies stop the next one" internal/agent/offsite.go \
  'err != nil || !row.enabled || row.keys' \
  'err != nil || row.keys' \
  ./internal/agent '^TestCopiesTurnedOffBetweenTwoCopiesStopTheNext$'
control "a try that fails once the settings were saved since the claim doesn't count" internal/agent/offsite.go \
  'AND updated_at >= ?)' \
  'AND updated_at >= ? AND 0)' \
  ./internal/agent '^TestNewCredentialsStopTheUploadStillUsingTheOldOnes$/^the_try_fails_after_a_save$'
control "a failed try counts from the queue's count" internal/agent/offsite.go \
  'attempts = attempts + 1, next_attempt' \
  'attempts = 1, next_attempt' \
  ./internal/agent '^TestAFailedTryCountsFromTheQueue$/^the_third_try$'
control "the wait after a failed try doubles with each one before it" internal/agent/offsite.go \
  'MIN(? << MIN(attempts, 8), ?)' \
  'MIN(? << 0, ?)' \
  ./internal/agent '^TestAFailedTryCountsFromTheQueue$/^the_third_try$'
control "a scheduled backup waits for a busy server until its grace ends" internal/schedule/runner.go \
  ', j.Due.Add(j.Schedule.Grace()))' \
  ', r.cfg.Now().Add(r.cfg.BusyGiveUp))' \
  ./internal/schedule '^TestRunnerWaitsForABusyServer$/^a_backup,_busy_for_40_minutes$'
control "a restart says it restarts now only once the server isn't busy" internal/schedule/runner.go \
  '	if res, stop := r.waitIdle(ctx, j, warned, giveUp); stop {
		return res
	}' \
  '' \
  ./internal/schedule '^TestRunnerWaitsForABusyServer$/^a_restart,_busy_for_40_minutes$'
control "a restart waiting for a busy server gives up" internal/schedule/runner.go \
  '		if !r.cfg.Now().Before(giveUp) {
			if warned {' \
  '		if false {
			if warned {' \
  ./internal/schedule '^TestRunnerWaitsForABusyServer$/^a_restart,_busy_for_40_minutes$'
control "the runner hears when another operation holds the server" internal/agent/schedules.go \
  'st := schedule.ServerState{Running: online, Busy: s.busy()}' \
  'st := schedule.ServerState{Running: online}' \
  ./internal/agent '^TestTheRunnerHearsWhenAnotherOperationHoldsTheServer$'
control "backup schedules that can't be read are an error, not none" internal/agent/backuprules.go \
  '		return scheduleRow{}, false, fmt.Errorf("the backup schedules could not be read: %w", err)' \
  '		return scheduleRow{}, false, nil' \
  ./internal/agent '^TestAutomaticBackupsThatCantBeReadAreNeitherShownNorSaved$/^turned_on$'
control "saving automatic backups that can't be read is refused" internal/agent/backuprules.go \
  '	row, ok, err := s.automaticSchedule(ctx)
	if err != nil {' \
  '	row, ok, err := s.automaticSchedule(ctx)
	if err != nil && false {' \
  ./internal/agent '^TestAutomaticBackupsThatCantBeReadAreNeitherShownNorSaved$/^every_few_hours_instead$'
control "the Backup rules page isn't shown while the automatic backups can't be read" internal/agent/backuprules.go \
  '	auto, err := s.automaticBackups(ctx)
	if err != nil {
		return backupRulesView{}, errAutomaticUnread(err)
	}' \
  '	auto, _ := s.automaticBackups(ctx)' \
  ./internal/agent '^TestAutomaticBackupsThatCantBeReadAreNeitherShownNorSaved$/^turned_off$'
control "the rules estimate isn't made while the automatic backups can't be read" internal/agent/backuprules.go \
  '	auto, err := s.automaticBackups(r.Context())
	if err != nil {
		writeError(w, errAutomaticUnread(err))
		return
	}' \
  '	auto, _ := s.automaticBackups(r.Context())' \
  ./internal/agent '^TestAutomaticBackupsThatCantBeReadAreNeitherShownNorSaved$/^turned_on$'
control "an edit of a schedule never writes back the run it read" internal/agent/schedules.go \
  "			last_run = CASE WHEN json_valid(last_run) THEN json_remove(last_run, '\$.retryAt') ELSE last_run END
		WHERE id = ? AND server_id = ?\`,
		sc.Name, string(sc.Kind), string(timing), string(payload), boolInt(sc.Enabled), now.UnixMilli(), actor, sc.ID, s.id)" \
  "			last_run = ?
		WHERE id = ? AND server_id = ?\`,
		sc.Name, string(sc.Kind), string(timing), string(payload), boolInt(sc.Enabled), now.UnixMilli(), actor, func() string { b, _ := json.Marshal(sc.LastRun); return string(b) }(), sc.ID, s.id)" \
  ./internal/agent '^TestEditingAScheduleKeepsTheRunTheRunnerSaved$/^renamed_as_its_run_finishes$'

# Wave 9 after 8462e667's click-through: as an add-on job finishes, the
# dialog's Close stays the button someone focused or is pressing.
webcontrol "an add-on job's Close becomes Later, not Restart now, when it finishes" web/src/pages/server/plugins/dialogs.tsx \
  '<Button key="dismiss" variant="ghost" size={size} className="sm:mr-auto" onClick={a.closeJob}>
              {t('"'"'common.later'"'"')}' \
  '<Button variant="ghost" size={size} className="sm:mr-auto" onClick={a.closeJob}>
              {t('"'"'common.later'"'"')}' \
  web/src/pages/server/plugins/plugins.test.tsx 'keeps focus on Close as the job finishes and a restart is needed'
webcontrol "an add-on job's Close becomes Done when it finishes with nothing to restart" web/src/pages/server/plugins/dialogs.tsx \
  '<Button key="dismiss" size={size} onClick={a.closeJob}>
            {t('"'"'common.done'"'"')}' \
  '<Button size={size} onClick={a.closeJob}>
            {t('"'"'common.done'"'"')}' \
  web/src/pages/server/plugins/plugins.test.tsx 'keeps focus on Close as the job finishes with nothing to restart'

# The map's area: choosing it takes the map on and an admin, and what was
# generated stays; squaremap draws a finished pre-generation while the map
# is on; the world border is filled where Chunky finds it.
control "choosing the map's area needs the map on" internal/agent/maparea.go \
  '	} else if rec == nil {
		writeError(w, errConflict("Turn on the map first.", ""))' \
  '	} else if false && rec == nil {
		writeError(w, errConflict("Turn on the map first.", ""))' \
  ./internal/agent '^TestMapAreaRefusals$'
control "a map area past the world border is refused" internal/agent/maparea.go \
  '	case opt.PastBorder:' \
  '	case false && opt.PastBorder:' \
  ./internal/agent '^TestTheMapAreaFillsUpToTheWorldBorder$'
control "sizes bigger than the world border count as past it" internal/agent/maparea.go \
  'PastBorder: border > 0 && c.ID != api.MapAreaBorder && c.Radius > border,' \
  'PastBorder: false,' \
  ./internal/agent '^TestTheMapAreaFillsUpToTheWorldBorder$'
control "an area the map has can't be chosen again" internal/agent/maparea.go \
  '	case opt.Done:' \
  '	case false && opt.Done:' \
  ./internal/agent '^TestTheMapAreaFillsInABiggerAreaThatSquaremapThenDraws$'
control "Explored only can't be chosen once an area is filled in" internal/agent/maparea.go \
  '		case cur.Fill.State == "finished":' \
  '		case false && cur.Fill.State == "finished":' \
  ./internal/agent '^TestTheMapAreaFillsInABiggerAreaThatSquaremapThenDraws$'
control "a replaced area carries on as it was when the new one doesn't start" internal/agent/pregen.go \
  '			s.keepPregen(ctx, ctrl, old, paused)' \
  '			_ = paused' \
  ./internal/agent '^TestReplacingTheMapAreaKeepsTheOldOneUntilTheNewOneStarts$'
control "a replacement that can't be recorded ends the old area" internal/agent/pregen.go \
  '			if eerr := s.endPregen(old, pregenCancelled, nil, nil); eerr != nil {' \
  '			if eerr := error(nil); eerr != nil {' \
  ./internal/agent '^TestAReplacementThatCantBeRecordedEndsTheOldArea$'
control "a finished area stays done after a bigger one is stopped" internal/agent/pregen.go \
  '	if err == nil && how == pregenFinished {' \
  '	if false && err == nil && how == pregenFinished {' \
  ./internal/agent '^TestTheLargestFinishedAreaStaysDone$'
control "the largest finished area is done, not the latest" internal/agent/pregen.go \
  'done_radius = MAX(done_radius, radius),' \
  'done_radius = radius,' \
  ./internal/agent '^TestTheLargestFinishedAreaStaysDone$'
control "the world border is done only as far as it was filled" internal/agent/maparea.go \
  '			done = c.Radius <= doneBorder' \
  '			done = doneBorder > 0' \
  ./internal/agent '^TestTheMapAreaFillsUpToTheWorldBorder$'
control "squaremap draws a finished pre-generation" internal/agent/pregen.go \
  '		s.drawPregenerated(ctx)
		return nil, nil' \
  '		return nil, nil' \
  ./internal/agent '^TestTheMapAreaFillsInABiggerAreaThatSquaremapThenDraws$'
control "squaremap is asked to draw only while the map is on" internal/agent/maparea.go \
  'if err != nil || rec == nil || rec.pendingRestart(s.mapLive(ctx, true)) {' \
  'if err != nil || rec.pendingRestart(s.mapLive(ctx, true)) {' \
  ./internal/agent '^TestPreGeneratingOnTheWorldTabGrowsTheMap$'
control "the world border is filled where Chunky finds it" internal/agent/pregen.go \
  '		plan.Radius = started.Radius' \
  '		_ = started.Radius' \
  ./internal/agent '^TestTheMapAreaFillsUpToTheWorldBorder$'
control "a task a restart or a crash dropped is sent to Chunky again" internal/agent/pregen.go \
  '	if task != nil && st != nil && !s.pregenAfterRestart(ctx, p, task, st) {' \
  '	if task != nil && st != nil {' \
  ./internal/agent '^TestPregenATaskA(Restart|Crash)DroppedIsStartedAgain$'
control "a dropped task is sent again once a run" internal/agent/pregen.go \
  '	tried := s.pg.resumedRun.Equal(run)' \
  '	tried := s.pg.resumedRun.Equal(run) && run.IsZero()' \
  ./internal/agent '^TestPregenATaskARestartDroppedIsStartedAgain$'
control "a task the server ran out of memory with twice is paused, not sent again" internal/agent/pregen.go \
  '	if s.pregenMemoryKills(task) >= 2 {' \
  '	if false {' \
  ./internal/agent '^TestPregenATaskACrashDroppedIsStartedAgain$'
control "a task paused for memory reads so" internal/agent/pregen.go \
  '	case task.PausedByUser && task.PausedFor == pregenPausedForMemory:' \
  '	case false:' \
  ./internal/agent '^TestPregenATaskACrashDroppedIsStartedAgain$'
control "a task paused for memory doesn't resume when the server restarts" internal/agent/pregen.go \
  '		if err := ctrl.Configure(cctx, pregen.Config{ContinueOnRestart: false, UpdateInterval: pregenUpdateInterval}); err != nil {' \
  '		if err := ctrl.Configure(cctx, pregen.Config{ContinueOnRestart: true, UpdateInterval: pregenUpdateInterval}); err != nil {' \
  ./internal/agent '^TestPregenATaskACrashDroppedIsStartedAgain$'
control "Resume starts a task Chunky lost again" internal/agent/pregen.go \
  '			if plan, ok := taskPlan(task); ok && errors.Is(err, pregen.ErrNothingToContinue) {' \
  '			if plan, ok := taskPlan(task); false && ok && errors.Is(err, pregen.ErrNothingToContinue) {' \
  ./internal/agent '^TestPregenATaskACrashDroppedIsStartedAgain$'
control "a task paused for memory isn't taken as gone while Chunky has none" internal/agent/pregen.go \
  '		if st.State == pregen.StateIdle && task.PausedFor == pregenPausedForMemory {' \
  '		if false {' \
  ./internal/agent '^TestPregenATaskACrashDroppedIsStartedAgain$'
control "memory kills are counted from the last resume" internal/agent/pregen.go \
  '(SELECT MAX(ts) FROM audit WHERE server_id = ?' \
  '(SELECT MAX(ts) FROM audit WHERE 0 AND server_id = ?' \
  ./internal/agent '^TestPregenATaskACrashDroppedIsStartedAgain$'
control "only a task a restart dropped is sent again" internal/agent/pregen.go \
  '	if run.IsZero() || !run.After(task.StartedAt) || tried {' \
  '	if run.IsZero() || tried {' \
  ./internal/agent '^TestPregenCancel$'
control "a task someone paused isn't sent again after a restart" internal/agent/pregen.go \
  '	if (st.State != pregen.StateIdle && st.State != pregen.StatePaused) || task.PausedByUser || task.PausedByPolicy {' \
  '	if st.State != pregen.StateIdle && st.State != pregen.StatePaused {' \
  ./internal/agent '^TestPregenAcrossServerStops$'
control "a task Chunky can't be asked about, or has lost, reads unknown, not paused" internal/agent/pregen.go \
  '	case st == nil || st.State != pregen.StatePaused:' \
  '	case false:' \
  ./internal/agent '^(TestPregenCancel|TestPregenFinishesWhenChunkyLogsIt)$'
webcontrol "the page says it's checking on a task Chunky can't be asked about" web/src/pages/server/world-pregen.tsx \
  ": unknown ? t('pregen.unknown') :" \
  ":" \
  web/src/pages/server/world.test.tsx 'says it is checking'
control "choosing the map's area takes an admin" internal/panel/server.go \
  '{"POST", "/api/servers/{id}/map/area", needSessionCSRF, actManageServers, s.hMapAreaSet},' \
  '{"POST", "/api/servers/{id}/map/area", needSessionCSRF, actView, s.hMapAreaSet},' \
  ./internal/panel '^TestOnlyAdminsChooseTheMapArea$'
control "a world border that isn't a square is refused" internal/pregen/controller.go \
  '	case sized && size.Kind == EventRadiiSet, reshaped && shape.Shape != string(Square):' \
  '	case false && (sized && size.Kind == EventRadiiSet || reshaped && shape.Shape != string(Square)):' \
  ./internal/pregen '^TestStartOnBorderRefused$'
control "a world border further out than Playkeeper pre-generates is refused" internal/pregen/controller.go \
  '	case r > MaxRadius:' \
  '	case false && r > MaxRadius:' \
  ./internal/pregen '^TestStartOnBorderRefused$'
webcontrol "the Map area is left out for those who can't change the map" web/src/pages/server/map.tsx \
  "{changes && <MenuItem onClick={() => setAreaOpen(true)}>{t('mapArea.menu')}</MenuItem>}" \
  "<MenuItem onClick={() => setAreaOpen(true)}>{t('mapArea.menu')}</MenuItem>" \
  web/src/pages/server/map.test.tsx 'is left out for those who can'
webcontrol "the phone's Map area row is left out for those who can't change the map" web/src/pages/server/map.tsx \
  '            {changes && (
              <>
                <MapAreaRow' \
  '            {true && (
              <>
                <MapAreaRow' \
  web/src/pages/server/map.test.tsx 'is left out for those who can'
webcontrol "Explored only is out of reach once an area is filled in" web/src/pages/server/map-area.tsx \
  '<ChoiceCard value="explored" disabled={locked}' \
  '<ChoiceCard value="explored" disabled={false}' \
  web/src/pages/server/map.test.tsx 'keeps what the map has and sizes past the border out of reach'
webcontrol "Start waits for another area to be chosen" web/src/pages/server/map-area.tsx \
  "(unchanged ? t('mapArea.unchanged') : undefined)" \
  "(undefined)" \
  web/src/pages/server/map.test.tsx 'with what it takes shown first'

# Docker Hub now and then refuses or drops a pull it answers a moment later.
control "a failed image pull is tried again" internal/agent/lifecycle.go \
  '	for _, wait := range a.opts.PullBackoff {' \
  '	for _, wait := range a.opts.PullBackoff[:0] {' \
  ./internal/agent '^TestAnImagePullDockerHubRefusesOrDropsIsTriedAgain$'
control "a pull of an image the registry doesn't have isn't tried again" internal/agent/lifecycle.go \
  ' && !docker.IsNotFound(err) && ' \
  ' && ' \
  ./internal/agent '^TestAnImagePullDockerHubRefusesOrDropsIsTriedAgain$'
control "a pull onto a full disk isn't tried again" internal/agent/lifecycle.go \
  ' && !strings.Contains(err.Error(), "no space left on device")' \
  '' \
  ./internal/agent '^TestAnImagePullDockerHubRefusesOrDropsIsTriedAgain$'

# playkeeper.io says a release is out only once it's published.
control "the site describes the published release, not CHANGELOG.md's newest section" internal/site/build.go \
  '			return r.Version, nil' \
  '			return string(reRelease.FindSubmatch(c)[1]), nil' \
  ./internal/site '^TestTheSiteShowsOnlyThePublishedRelease$'
control "the published release has a CHANGELOG.md section" internal/site/build.go \
  '		if string(m[1]) == r.Version {' \
  '		if string(m[1]) == r.Version || true {' \
  ./internal/site '^TestTheSiteShowsOnlyThePublishedRelease$'
# 0.4.3: the public page at the machine's address, and the ports it takes.
control "the public page names players only while the owner shows them" internal/agent/publicpage.go \
  '		if set.Players && len(st.Players.Names) > 0 {' \
  '		if len(st.Players.Names) > 0 {' \
  ./internal/agent '^TestThePublicPageShowsAServerButNotWhosPlaying$'
control "a server turned off leaves the public page" internal/agent/publicpage.go \
  'set := s.publicPageSettings()
	if !set.Enabled {' \
  'set := s.publicPageSettings()
	if false && !set.Enabled {' \
  ./internal/agent '^TestAServerTurnedOffLeavesThePageAndTheOthersStay$'
control "the public page answers only for the machine's address" internal/agent/publicpage.go \
  '	if !sameHost(host, st.Host) {
		if only = a.ownPageServer(host); only == nil {' \
  '	if false && !sameHost(host, st.Host) {
		if only = a.ownPageServer(host); only == nil {' \
  ./internal/agent '^TestAPublicPageThatIsOffAnswersLikeAnUnknownAddress$'
control "the page leaves ports 443 and 80 alone while the machine starts" internal/agent/pageports.go \
  'if a.opts.Uptime() < pageSettle {' \
  'if false && a.opts.Uptime() < pageSettle {' \
  ./internal/agent '^TestThePageLeavesPortsToWhatStartsWithTheMachine$'
control "a stopped Docker container keeps the ports it names" internal/agent/pageports.go \
  'if c.State == "running" || inspected >= maxPageContainers {' \
  'if true {' \
  ./internal/agent '^TestThePageNeverTakesAPortSomethingElseUsesOrWillUse$'
control "a web server set to start with the machine keeps ports 443 and 80" internal/agent/pageports.go \
  'if a.enabledService(unit) {' \
  'if false && a.enabledService(unit) {' \
  ./internal/agent '^TestThePageNeverTakesAPortSomethingElseUsesOrWillUse$'
control "a port found busy isn't tried again until the owner asks" internal/agent/pageports.go \
  'if p, ok := a.pagePorts.busy[addr]; ok {' \
  'if p, ok := a.pagePorts.busy[addr]; false && ok {' \
  ./internal/agent '^TestThePageNeverTakesAPortSomethingElseUsesOrWillUse$'
control "the agent's own HTTP-01 check never makes port 80 count as busy" internal/agent/pageports.go \
  'if port == addrPort(a.opts.HTTP01Addr) {' \
  'if false && port == addrPort(a.opts.HTTP01Addr) {' \
  ./internal/agent '^TestAnHTTP01CheckDoesntMakePort80Busy$'
control "a hand-over asked for over anything but the agent's socket tries no port" internal/agent/pageports.go \
  'if _, ok := r.Context().Value(connKey{}).(*net.UnixConn); !ok {' \
  'if _, ok := r.Context().Value(connKey{}).(*net.UnixConn); false && !ok {' \
  ./internal/agent '^TestThePagesPortsReachThePanelOnlyOverTheAgentSocket$'
control "the agent lets go of the page's ports before the panel has the answer" internal/agent/pageports.go \
  '		closeFiles()
		conn.Close()' \
  '		conn.Close()
		time.Sleep(200 * time.Millisecond)
		closeFiles()' \
  ./internal/agent '^TestThePagesPortsReachThePanelOnlyOverTheAgentSocket$'
control "machine links don't carry the public page's ports" internal/agent/link.go \
  '"POST " + pagePortsPath: true,' \
  '"POST " + pagePortsPath: false,' \
  ./internal/agent '^TestLinkRoutesAreTheRouteTable$'
control "an HTTP-01 check goes ahead on a busy port only when its holder passes it on" internal/certs/http01.go \
  'if errors.Is(err, syscall.EADDRINUSE) && h.Shared != nil && h.Shared(token, keyAuth) {' \
  'if errors.Is(err, syscall.EADDRINUSE) {' \
  ./internal/certs '^TestHTTP01GoesAheadOnlyWhenThePortsHolderPassesChecksOn$'
control "the page's ports answer only the machine's address" internal/panel/serverpage.go \
  'if !check && !s.page.answers(r.Host) {' \
  'if false && !check && !s.page.answers(r.Host) {' \
  ./internal/panel '^TestThePagesPortsServeThePageAndNothingElse$'
control "the page escapes what the owner typed" internal/panel/serverpage.go \
  'head := "<title>" + html.EscapeString(title) + "</title>"' \
  'head := "<title>" + title + "</title>"' \
  ./internal/panel '^TestThePagesPortsServeThePageAndNothingElse$'
control "the page serves faces only of players it lists" internal/panel/serverpage.go \
  '	}
	http.NotFound(w, r)
}

// hPageCard serves' \
  '	}
	if st, img := s.head(r.Context(), name, ""); st == headOK {
		writePNG(w, img)
		return
	}
	http.NotFound(w, r)
}

// hPageCard serves' \
  ./internal/panel '^TestThePageNamesAndFacesOnlyPlayersTheOwnerShows$'
control "visitors share one question to the agent" internal/panel/serverpage.go \
  '	if a, ok := lookup(); ok {
		return a, a.ok
	}
	p.fetch.Lock()
	defer p.fetch.Unlock()
	if a, ok := lookup(); ok {
		return a, a.ok
	}' \
  '	_ = lookup
	p.fetch.Lock()
	defer p.fetch.Unlock()' \
  ./internal/panel '^TestManyVisitorsAskTheAgentOnceAndEachAddressIsLimited$'
control "port 443 never serves the self-signed certificate" internal/panel/pageports.go \
  '	return s.pageCerts.GetCertificate(hello)
}' \
  '	tc, err := s.tlsConfig()
	if err != nil {
		return nil, err
	}
	return tc.GetCertificate(hello)
}' \
  ./internal/panel '^TestThePagesPort80RedirectsOnlyWhileHTTPSServesWithACertificate$'
control "port 80 redirects only while port 443 serves the page" internal/panel/pageports.go \
  'if https == nil || host == "" || s.pageCerts == nil {' \
  'if _ = https; host == "" || s.pageCerts == nil {' \
  ./internal/panel '^TestThePagesPort80RedirectsOnlyWhileHTTPSServesWithACertificate$'
control "turning the page off gives the ports back" internal/panel/pageports.go \
  'if !st.On {
		s.closePagePorts(api.PortOff)
		return
	}' \
  'if !st.On {
		return
	}' \
  ./internal/panel '^TestTheKeeperHoldsThePortsOnlyWhileThePageIsOn$'
control "the keeper never asks for a port it holds" internal/panel/pageports.go \
  'return p.held[i] == nil && !now.Before(p.next[i]) && port.Port != s.cfg.PanelPort' \
  'return !now.Before(p.next[i]) && port.Port != s.cfg.PanelPort' \
  ./internal/panel '^TestTheKeeperHoldsThePortsOnlyWhileThePageIsOn$'
control "Let's Encrypt's check for a new address passes port 80 before the page follows it" internal/panel/serverpage.go \
  'if !check && !s.page.answers(r.Host) {' \
  'if !s.page.answers(r.Host) {' \
  ./internal/panel '^TestLetsEncryptsCheckForANewNameReachesTheAgentBeforeThePageCatchesUp$'
control "a changed address has the page's keeper look again" internal/panel/server.go \
  'if method != http.MethodGet {
		then = func(' \
  'if false && method != http.MethodGet {
		then = func(' \
  ./internal/panel '^TestAChangedAddressHasThePageLookAgain$'
control "a port the keeper didn't ask for keeps its holder and its wait" internal/panel/pageports.go \
  'if p.held[i] != nil || !asked[i] {' \
  'if p.held[i] != nil || false && !asked[i] {' \
  ./internal/panel '^TestAPortNotAskedForKeepsItsHolderAndItsWait$'
# What the owner adds to the page: About, a stream and a status board.
control "a page's stream is only a Twitch or YouTube channel" internal/agent/publicblocks.go \
  'case host == "twitch.tv" && len(parts) == 1 && reTwitchLogin.MatchString(parts[0]):' \
  'case len(parts) == 1 && reTwitchLogin.MatchString(parts[0]):' \
  ./internal/agent '^TestTheOwnersWordsAndStreamShowOnThePage$'
control "the page's About keeps out control and direction-changing characters" internal/agent/publicblocks.go \
  "return unicode.IsPrint(r) || r == '\\u200d'" \
  'return r != 0' \
  ./internal/agent '^TestTheOwnersWordsAndStreamShowOnThePage$'
control "a board with more numbers than the page shows is refused" internal/agent/publicblocks.go \
  'if len(req.Stats) > api.BoardStatsMax {' \
  'if false && len(req.Stats) > api.BoardStatsMax {' \
  ./internal/agent '^TestTheStatusBoardShowsWhatTheToolsPostWithinItsBounds$'
control "a board's next session is within a month" internal/agent/publicblocks.go \
  'if next.Before(now.Add(-24*time.Hour)) || next.After(now.Add(api.BoardNextWithin)) {' \
  'if next.Before(now.Add(-24 * time.Hour)) {' \
  ./internal/agent '^TestTheStatusBoardShowsWhatTheToolsPostWithinItsBounds$'
control "posting the status board needs the right to run the server" internal/panel/server.go \
  '{"PUT", "/api/servers/{id}/public-page/board", needSessionCSRF, actRunServers,' \
  '{"PUT", "/api/servers/{id}/public-page/board", needSessionCSRF, actView,' \
  ./internal/panel '^TestEveryToolTakesTheActionOfItsDashboardRoute$'
control "only the page's ports may frame the stream players" internal/panel/server.go \
  "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; font-src 'self'; object-src 'none';" \
  "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; font-src 'self'; frame-src https://player.twitch.tv; object-src 'none';" \
  ./internal/panel '^TestThePageHasALiveShareCardAndMayFrameAStream$'

# Usage stats (internal/usage): nothing is sent that its check refuses, while
# they're off, or before the installer has said so; root's choices outrank
# the switch, which takes someone who manages the machine; the project's own
# installs are marked and never counted; the stats service limits what one
# address or install can send, and only its proxy may say whom a request is
# from.
control "the usage stats client sends no heartbeat its check refuses" internal/usage/client.go \
  '	if err := h.Check(); err != nil {
		return err
	}
	return c.post(ctx, PathHeartbeat, h)' \
  '	return c.post(ctx, PathHeartbeat, h)' \
  ./internal/usage '^TestTheClientSendsNothingItsCheckRefuses$'
control "the usage stats client follows no redirect" internal/usage/client.go \
  '	nc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
' \
  '' \
  ./internal/usage '^TestTheClientFollowsNoRedirectAndFailsOnARefusal$'
control "the usage stats client uses plain http:// only on this machine" internal/usage/client.go \
  'host == "localhost" || (ip != nil && ip.IsLoopback())' \
  'true || host == "localhost" || (ip != nil && ip.IsLoopback())' \
  ./internal/usage '^TestTheServiceIsReachedOverHTTPSOrOnThisMachine$'
control "the stats service refuses a heartbeat its check refuses" internal/usage/service/handlers.go \
  '!s.checked(w, h.ID, h.Check())' \
  '!s.checked(w, h.ID, nil)' \
  ./internal/usage/service '^TestReportsThatArentOneAreRefused$'
control "the stats service limits each address" internal/usage/service/handlers.go \
  'if ok, wait := s.perIP.allow(addrBucket(addr)); !ok {' \
  'if ok, wait := s.perIP.allow(addrBucket(addr)); false && !ok {' \
  ./internal/usage/service '^TestEachAddressAndEachInstallHasALimit$'
control "the stats service limits each install" internal/usage/service/handlers.go \
  'if ok, wait := s.perID.allow(id); !ok {' \
  'if ok, wait := s.perID.allow(id); false && !ok {' \
  ./internal/usage/service '^TestEachAddressAndEachInstallHasALimit$'
control "the stats service takes a bounded number of new installs a day" internal/usage/service/store.go \
  'if ok, _ := s.newIDs.allow(""); !ok {' \
  'if ok, _ := s.newIDs.allow(""); false && !ok {' \
  ./internal/usage/service '^TestNewInstallsPerDayAreBounded$'
control "only a trusted proxy says whom a stats request is from" internal/usage/service/service.go \
  '	if !s.trusted(addr) {
		if r.Header.Get("X-Forwarded-For")' \
  '	if false && !s.trusted(addr) {
		if r.Header.Get("X-Forwarded-For")' \
  ./internal/usage/service '^TestOnlyATrustedProxyNamesTheClient$'
control "a stats request is from the address the proxy saw, not one the client made up" internal/usage/service/service.go \
  'for i := len(hops) - 1; i >= 0; i-- {' \
  'for i := 0; i < len(hops); i++ {' \
  ./internal/usage/service '^TestOnlyATrustedProxyNamesTheClient$'
control "the usage counts need the read token" internal/usage/service/handlers.go \
  'if !ok || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(s.cfg.ReadToken)) != 1 {' \
  'if false && (!ok || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(s.cfg.ReadToken)) != 1) {' \
  ./internal/usage/service '^TestTheCountsNeedTheReadToken$'
control "test installs aren't counted as running" internal/usage/service/summary.go \
  'WHERE test = 0 AND last_seen != 0 AND last_seen >= ?' \
  'WHERE last_seen != 0 AND last_seen >= ?' \
  ./internal/usage/service '^TestTestInstallsAreKeptOutOfEveryCount$'
control "test installs aren't counted as installs" internal/usage/service/summary.go \
  'WHERE test = 0 AND (started_at >= ? OR outcome_at >= ?)' \
  'WHERE (started_at >= ? OR outcome_at >= ?)' \
  ./internal/usage/service '^TestTestInstallsAreKeptOutOfEveryCount$'
control "a heartbeat without the test mark doesn't clear it" internal/usage/service/store.go \
  '			test = MAX(installs.test, excluded.test)`,
		h.ID,' \
  '			test = excluded.test`,
		h.ID,' \
  ./internal/usage/service '^TestTestInstallsAreKeptOutOfEveryCount$'
control "an install from within the last day counts in it" internal/usage/service/summary.go \
  'in, err := s.installs(ctx, now, hour(now)-int64(w.span/time.Second))' \
  'in, err := s.installs(ctx, now, now.Add(-w.span).Unix())' \
  ./internal/usage/service '^TestAnInstallFromTheLastDayCountsInIt$'
control "a daily snapshot that failed is tried again the next hour" internal/usage/service/service.go \
  '	done := s.lastBackup == day
	s.mu.Unlock()' \
  '	done := s.lastBackup == day
	s.lastBackup = day
	s.mu.Unlock()' \
  ./internal/usage/service '^TestAFailedSnapshotIsTriedAgainTheNextHour$'
control "playkeeper dev sends no usage stats" internal/agent/usage.go \
  '	if a.cfg.Dev {
		return false, api.UsageDev, ""
	}
' \
  '' \
  ./internal/agent '^TestTheSwitchChangesUsageStatsUnlessRootChose$'
control "DO_NOT_TRACK in the agent's environment decides" internal/agent/usage.go \
  'usage.FromEnv(a.opts.Getenv)' \
  'usage.FromEnv(func(string) string { return "" })' \
  ./internal/agent '^TestTheSwitchChangesUsageStatsUnlessRootChose$'
control "usage stats turned off at install stay off" internal/agent/usage.go \
  '	if a.cfg.UsageStats == "off" {
		return false, api.UsageInstall, ""
	}
' \
  '' \
  ./internal/agent '^TestTheSwitchChangesUsageStatsUnlessRootChose$'
control "the switch doesn't change usage stats turned off at install" internal/agent/usage.go \
  '	case reason == api.UsageInstall && !on:' \
  '	case reason == api.UsageInstall && !on && false:' \
  ./internal/agent '^TestTheSwitchChangesUsageStatsUnlessRootChose$'
control "with usage stats off, the agent sends nothing" internal/agent/usage.go \
  '	if on, _, _ := a.usageDecision(); !on {
		return nil
	}
' \
  '' \
  ./internal/agent '^TestTurnedOffNothingIsSentAndTurningThemOnSendsAtOnce$'
control "turning usage stats on sends a heartbeat at once" internal/agent/usage.go \
  '	if req.On {
		select {' \
  '	if false && req.On {
		select {' \
  ./internal/agent '^TestTurnedOffNothingIsSentAndTurningThemOnSendsAtOnce$'
control "an agent keeps the usage ID it made" internal/agent/usage.go \
  'err != nil || (ok && usage.IsID(id))' \
  'err != nil || (false && ok && usage.IsID(id))' \
  ./internal/agent '^TestAnInstallWithoutAUsageIDGetsOneOfItsOwnOnce$'
control "an agent on a machine running an Actions job marks its heartbeats as a test" internal/agent/usage.go \
  ' || usage.ActionsJob(a.opts.Processes())' \
  '' \
  ./internal/agent '^TestTestInstallsAndJoinedMachinesSayWhatTheyAre$'
control "the end-to-end tests' offline harness marks its heartbeats as a test" internal/agent/usage.go \
  'a.cfg.UsageTest || a.offline() || ' \
  'a.cfg.UsageTest || ' \
  ./internal/agent '^TestTestInstallsAndJoinedMachinesSayWhatTheyAre$'
control "the installer says usage stats are on before anything else" internal/install/install.go \
  '	for _, line := range o.Usage.notice(o.Usage.state("")) {' \
  '	for _, line := range o.Usage.notice(o.Usage.state(""))[:0] {' \
  ./internal/install '^TestAnInstallReportsItsStartAndEndUnderTheIDItLeavesTheAgent$'
control "an upgrade says usage stats are on before its plan" internal/install/inplace.go \
  '	for _, line := range o.Usage.notice(o.Usage.upgradeState(ctx, sys, cfg)) {' \
  '	for _, line := range o.Usage.notice(o.Usage.upgradeState(ctx, sys, cfg))[:0] {' \
  ./internal/install '^TestAnUpgradeFromBeforeUsageStatsSaysTheyreOn$'
control "an install sends nothing before its plan is accepted" internal/install/install.go \
  '	if !o.Yes {
		fmt.Fprint(out, "Proceed? [y/N] ")' \
  '	rep.send(ctx, usage.EventStarted, "")
	if !o.Yes {
		fmt.Fprint(out, "Proceed? [y/N] ")' \
  ./internal/install '^TestDecliningThePlanSendsNothing$'
control "an install with DO_NOT_TRACK sends nothing" internal/install/usage.go \
  '	if !u.On() || u.Send == nil {' \
  '	if u.Send == nil {' \
  ./internal/install '^TestDoNotTrackSendsNothingAndTheAgentKeepsItOff$'
control "an install while an Actions job runs is a test" internal/install/usage.go \
  '	return usage.ActionsJob(sys.Processes()) || os.Getenv(FailStepEnv) != ""' \
  '	return os.Getenv(FailStepEnv) != ""' \
  ./internal/install '^TestAnInstallDuringAnActionsJobIsATest$'
control "a failed install names the step that failed" internal/install/install.go \
  '		in.failed = s.code
' \
  '' \
  ./internal/install '^TestAFailedInstallSaysWhichStepFailedAndCountsAsATest$'
control "an upgrade records usage stats DO_NOT_TRACK turned off" internal/install/inplace.go \
  '	c := o.Usage.record(cfg, sys)' \
  '	c := cfg' \
  ./internal/install '^TestAnUpgradeSaysSoAndRecordsDoNotTrack$'
control "an upgrade says usage stats turned off at install are still off" internal/install/usage.go \
  '	case setting == "off":' \
  '	case false && setting == "off":' \
  ./internal/install '^TestAnUpgradeSaysSoAndRecordsDoNotTrack$'
control "the installer takes DO_NOT_TRACK from its environment" cmd/playkeeper/main.go \
  'choice, why := usagestats.FromEnv(getenv)' \
  'choice, why := usagestats.FromEnv(func(string) string { return "" })' \
  ./cmd/playkeeper '^TestTheInstallTakesUsageStatsFromTheEnvironment$'
control "only those who manage the machine use the usage stats switch" internal/panel/server.go \
  '{"PUT", "/api/usage-stats", needSessionCSRF, actManageMachine, s.hUsageStatsSet}' \
  '{"PUT", "/api/usage-stats", needSessionCSRF, actView, s.hUsageStatsSet}' \
  ./internal/panel '^TestOnlyThoseWhoManageTheMachineUseTheSwitch$'
control "the usage stats switch passes on only on or off" internal/panel/usage.go \
  '	dec.DisallowUnknownFields()
' \
  '' \
  ./internal/panel '^TestTheSwitchSetsUsageStatsOnEveryMachine$'
control "a machine that was away gets usage stats off when it connects" internal/panel/machines.go \
  '		go s.carryUsageOff(e.MachineID)
' \
  '' \
  ./internal/panel '^TestAMachineThatWasAwayIsTurnedOffWhenItConnects$'
control "a machine that connects after the switch turned usage stats on keeps its own" internal/panel/usage.go \
  'if here.On || here.Reason != api.UsageSettings {' \
  'if here.Reason != api.UsageSettings {' \
  ./internal/panel '^TestAMachineThatWasAwayIsTurnedOffWhenItConnects$'
control "an off the dashboard's machine has for itself isn't carried to a machine that connects" internal/panel/usage.go \
  'if here.On || here.Reason != api.UsageSettings {' \
  'if here.On || here.Reason == api.UsageDev {' \
  ./internal/panel '^TestAMachineThatWasAwayIsTurnedOffWhenItConnects$'
# shellcheck disable=SC2016
shcontrol "get.sh says it came from GitHub's release" packaging/get.sh \
  '    [ "$base" != "$default_base" ] || PLAYKEEPER_INSTALL_SOURCE=github
' \
  '' \
  packaging/get_test.sh
# shellcheck disable=SC2016
shcontrol "get.sh passes on what playkeeper.io's command said" packaging/get.sh \
  '  if [ -z "${PLAYKEEPER_INSTALL_SOURCE:-}" ]; then' \
  '  if true; then' \
  packaging/get_test.sh
control "every install CI runs sends no usage stats" .github/workflows/e2e.yml \
  'sudo DO_NOT_TRACK=1 ./install.sh --yes | tee /tmp/evidence/install.txt' \
  'sudo ./install.sh --yes | tee /tmp/evidence/install.txt' \
  ./internal/usage '^TestEveryInstallTheProjectRunsSendsNoUsageStats$'
# shellcheck disable=SC2016
control "README.md lists every field usage stats send" README.md \
  '| `arch` | the CPU type, `amd64` or `arm64` |
' \
  '' \
  ./internal/usage '^TestTheReadmeListsEveryFieldSent$'
webcontrol "the usage stats switch is left out for those who can't manage the machine" web/src/pages/settings.tsx \
  '{manage && st && <Switch' \
  '{st && <Switch' \
  web/src/pages/settings.test.tsx 'no switch to those who can'
webcontrol "the usage stats switch stays still where root or playkeeper dev decides" web/src/pages/settings.tsx \
  'disabled={!!locked} title={locked}' \
  'disabled={false} title={locked}' \
  web/src/pages/settings.test.tsx 'keeps the switch still'

# The stats dashboard: it loads only its own files and asks only its own
# service for the counts; the days leave test installs out and count an
# install whose first report was lost as started the day it ended.
control "the stats dashboard is sent with its Content-Security-Policy" internal/usage/service/dashboard.go \
  '		h.Set("Content-Security-Policy", dashboardCSP)
' \
  '' \
  ./internal/usage/service '^TestTheDashboardLoadsOnlyItsOwnFilesAndAsksOnlyForTheSummary$'
control "a sign-in to the stats dashboard without its script can't put the token in an address" internal/usage/service/dashboard.go \
  "form-action 'none'; " \
  "" \
  ./internal/usage/service '^TestTheDashboardLoadsOnlyItsOwnFilesAndAsksOnlyForTheSummary$'
control "the stats dashboard asks only its own service for the counts" internal/usage/service/dashboard/app.js \
  "fetch('/v1/summary'," \
  "fetch('https://stats.example/v1/summary'," \
  ./internal/usage/service '^TestTheDashboardLoadsOnlyItsOwnFilesAndAsksOnlyForTheSummary$'
control "test installs aren't counted in the days" internal/usage/service/summary.go \
  'WHERE test = 0 AND (started_at >= ? OR outcome_at >= ?)`, first*86400' \
  'WHERE (started_at >= ? OR outcome_at >= ?)`, first*86400' \
  ./internal/usage/service '^TestTestInstallsAreKeptOutOfEveryCount$'
control "an install whose first report was lost started the day it ended" internal/usage/service/summary.go \
  '		case outcome == usage.EventSucceeded || outcome == usage.EventFailed:
			add(outcomeAt, source, Outcomes{Started: 1})
' \
  '' \
  ./internal/usage/service '^TestSummaryCountsInstallsAndActiveInstallsByWindow$'

# The stats funnel: counts come from playkeeper.io's pages alone, as one
# event, limited for each address; the funnel follows playkeeper.io installs
# alone, leaves test installs out, and counts as still running only those
# with a heartbeat in the last day; the site's analytics are read with the
# key as a bearer token, kept for a while, not asked again at once after a
# failure, and what they answer is passed on only as a plain code.
control "site counts come from playkeeper.io's pages alone" internal/usage/service/site.go \
  '	if r.Header.Get("Origin") != SiteOrigin {' \
  '	if false && r.Header.Get("Origin") != SiteOrigin {' \
  ./internal/usage/service '^TestSiteCountsComeOnlyFromThePagesAndSayNothingElse$'
control "a site count is answered to playkeeper.io's pages" internal/usage/service/site.go \
  '	w.Header().Set("Access-Control-Allow-Origin", SiteOrigin)
' \
  '' \
  ./internal/usage/service '^TestSiteCountsComeOnlyFromThePagesAndSayNothingElse$'
control "a site count is a copy of the install command and nothing else" internal/usage/service/site.go \
  '	if e.Event != EventInstallCopied {' \
  '	if false && e.Event != EventInstallCopied {' \
  ./internal/usage/service '^TestSiteCountsComeOnlyFromThePagesAndSayNothingElse$'
control "site counts from one address are limited" internal/usage/service/site.go \
  'if ok, wait := s.perIPSite.allow(addrBucket(addr)); !ok {' \
  'if ok, wait := s.perIPSite.allow(addrBucket(addr)); false && !ok {' \
  ./internal/usage/service '^TestSiteCountsComeOnlyFromThePagesAndSayNothingElse$'
control "the funnel follows installs made with the playkeeper.io command alone" internal/usage/service/summary.go \
  'AND source = ? AND' \
  'AND (source = ? OR 1) AND' \
  ./internal/usage/service '^TestTheFunnelFollowsPlaykeeperIoFromTheSitesVisitorsToInstallsThatStillRun$'
control "the funnel leaves test installs out" internal/usage/service/summary.go \
  'WHERE test = 0 AND source = ?' \
  'WHERE source = ?' \
  ./internal/usage/service '^TestTheFunnelFollowsPlaykeeperIoFromTheSitesVisitorsToInstallsThatStillRun$'
control "still running means a heartbeat in the last day" internal/usage/service/summary.go \
  '			if lastSeen >= runningSince {' \
  '			if lastSeen >= runningSince-7*86400 {' \
  ./internal/usage/service '^TestTheFunnelFollowsPlaykeeperIoFromTheSitesVisitorsToInstallsThatStillRun$'
control "the site's analytics are kept for a quarter of an hour" internal/usage/service/site.go \
  'fresh := !r.readAt.IsZero() && now.Sub(r.readAt) < siteEvery' \
  'fresh := false && !r.readAt.IsZero() && now.Sub(r.readAt) < siteEvery' \
  ./internal/usage/service '^TestTheFunnelFollowsPlaykeeperIoFromTheSitesVisitorsToInstallsThatStillRun$'
control "a failed read of the site's analytics isn't tried again at once" internal/usage/service/site.go \
  'if !fresh && (r.triedAt.IsZero() || now.Sub(r.triedAt) >= siteRetry) {' \
  'if !fresh {' \
  ./internal/usage/service '^TestTheFunnelFollowsPlaykeeperIoFromTheSitesVisitorsToInstallsThatStillRun$'
control "a read of the site's analytics outlives a dropped request" internal/usage/service/site.go \
  'r.read(context.WithoutCancel(ctx), now)' \
  'r.read(ctx, now)' \
  ./internal/usage/service '^TestADroppedRequestDoesntStopTheSitesNumbersBeingRead$'
control "the site's read key goes to Open Analytics as a bearer token" internal/usage/service/site.go \
  '	req.Header.Set("Authorization", "Bearer "+r.key)' \
  '	req.Header.Set("Authorization", r.key)' \
  ./internal/usage/service '^TestTheFunnelFollowsPlaykeeperIoFromTheSitesVisitorsToInstallsThatStillRun$'
control "the summary passes on only a plain code from Open Analytics' errors" internal/usage/service/site.go \
  'if code := reErrorCode.FindString(e.Error.Code); code != "" {' \
  'if code := e.Error.Code; code != "" {' \
  ./internal/usage/service '^TestTheFunnelFollowsPlaykeeperIoFromTheSitesVisitorsToInstallsThatStillRun$'
# A server's own address under the machine's own domain.
control "an own address is only for a machine with its own domain" internal/agent/ownaddress.go \
  'if st.Kind != api.AddressOwn {
		return "", errConflict(' \
  'if false && st.Kind != api.AddressOwn {
		return "", errConflict(' \
  ./internal/agent '^TestAServerGetsAnAddressOfItsOwnUnderTheOwnDomain$'
control "an own address is never the machine's name" internal/agent/ownaddress.go \
  'if name == st.Host {' \
  'if false && name == st.Host {' \
  ./internal/agent '^TestAServerGetsAnAddressOfItsOwnUnderTheOwnDomain$'
control "two servers never share an own address" internal/agent/ownaddress.go \
  'if js.id != s.id && js.own == name {' \
  'if false && js.id != s.id && js.own == name {' \
  ./internal/agent '^TestAServerGetsAnAddressOfItsOwnUnderTheOwnDomain$'
control "own addresses get a few certificates a day" internal/agent/ownaddress.go \
  'if a.ownCertsToday(st) >= ownCertsPerDay {' \
  'if false && a.ownCertsToday(st) >= ownCertsPerDay {' \
  ./internal/agent '^TestOwnAddressesGetAFewCertificatesADay$'
control "an own address's page shows only its server" internal/agent/publicpage.go \
  'if only != nil && j.ServerID != only.id {' \
  'if false && only != nil && j.ServerID != only.id {' \
  ./internal/agent '^TestAnOwnAddressOpensOnlyItsServersPage$'
control "an own address's page answers only while its server is on the page" internal/agent/ownaddress.go \
  'if s := a.serverByID(js.id); s != nil && s.publicPageSettings().Enabled {
			return s
		}' \
  'if s := a.serverByID(js.id); s != nil {
			return s
		}' \
  ./internal/agent '^TestAnOwnAddressOpensOnlyItsServersPage$'
control "a server's own address doesn't hold the domain back" internal/certs/join.go \
  '			rc.Own = true
			pc.Records = append(pc.Records, rc)
			continue' \
  '			rc.Own = true
			pc.Records = append(pc.Records, rc)
			pc.Ready = pc.Ready && rc.OK
			continue' \
  ./internal/certs '^TestCheckPlanChecksAServersOwnAddress$'
control "looking at the machine's name alone leaves own addresses out of Ready" internal/agent/certificates.go \
  'return !r.OK && !r.Own' \
  'return !r.OK' \
  ./internal/agent '^TestAServerGetsAnAddressOfItsOwnUnderTheOwnDomain$'
control "an own address works whatever the machine's name does" internal/agent/address.go \
  'j.Published = st.Check != nil && ownOK(st.Check, s.id)' \
  'j.Published = st.Check != nil && st.Check.Name.OK && ownOK(st.Check, s.id)' \
  ./internal/agent '^TestAnOwnAddressWorksWithoutTheMachinesName$'
control "an own address gets its certificate while the machine's name points elsewhere" internal/agent/address.go \
  '			handed = a.startOwnCertificate(st)
			return
		}' \
  '			return
		}' \
  ./internal/agent '^TestAnOwnAddressWorksWithoutTheMachinesName$'
control "Bedrock players join a server at its working own address" internal/agent/publicpage.go \
  'if j.OwnAddress != "" && j.Published {' \
  'if false && j.OwnAddress != "" && j.Published {' \
  ./internal/agent '^TestAnOwnAddressWorksWithoutTheMachinesName$'
control "the machine's domain is never a server's own address" internal/agent/ownaddress.go \
  'if js.own == domain {' \
  'if false && js.own == domain {' \
  ./internal/agent '^TestAServerGetsAnAddressOfItsOwnUnderTheOwnDomain$'
control "giving a server its own address needs the right to manage the machine" internal/panel/server.go \
  '{"POST", "/api/servers/{id}/own-address", needSessionCSRF, actManageMachine,' \
  '{"POST", "/api/servers/{id}/own-address", needSessionCSRF, actManageServers,' \
  ./internal/panel '^TestMachineWideActionsNeedEveryServer$'

# playkeeper.io's copy count: pages reach the stats service only as the
# policy allows, and the share page never names it. The script's own guards,
# once a page view and none with Global Privacy Control or Do Not Track, are
# held by site.spec.ts, which this script doesn't run.
control "pages reach the stats service only as the policy allows" internal/site/nginx.go \
  'origin(s.Stats), ' \
  '' \
  ./internal/site '^TestTheCopyCountIsOneSetting$'
control "the share page doesn't name the stats service" site/layouts/base.html \
  '{{if and (ne $.Page.Path "/t") (ne $.Page.Layout "open")}}<meta name="playkeeper-stats"' \
  '{{if true}}<meta name="playkeeper-stats"' \
  ./internal/site '^TestTheCopyCountIsOneSetting$'

# An own domain under playkeeper.me: only a name nobody can claim, and only
# under the current base.
control "own domain: a playkeeper.me name nobody can claim is accepted" internal/agent/address.go \
  'return names.CheckName(name) != nil || names.Reserved(name)' \
  'return false && names.Reserved(name)' \
  ./internal/agent '^(TestOwnDomainsNobodyCanClaimUnderTheFreeBaseAreAccepted|TestTheManagedBetaMachineCanBeBetaPlaykeeperMe)$'
control "own domain: a playkeeper.me name someone can claim stays refused" internal/agent/address.go \
  'return names.CheckName(name) != nil || names.Reserved(name)' \
  'return true || names.Reserved(name)' \
  ./internal/agent '^TestOwnDomainsUnderEitherFreeBaseAreRefused$'
control "own domain: only the name right before playkeeper.me decides" internal/agent/address.go \
  "name := rest[strings.LastIndexByte(rest, '.')+1:]" \
  "name := rest[:strings.IndexByte(rest+\".\", '.')]" \
  ./internal/agent '^TestOwnDomainsUnderEitherFreeBaseAreRefused$'
control "own domain: names under playkeeper.io stay refused" internal/agent/address.go \
  'rest, ok := strings.CutSuffix(domain, "."+names.DefaultBase)' \
  'rest, ok := strings.CutSuffix(domain, "."+names.BaseOf(domain))' \
  ./internal/agent '^TestOwnDomainsUnderEitherFreeBaseAreRefused$'
control "own addresses: records that don't work yet are looked at every minute" internal/agent/address.go \
  'if check.Ready && !slices.ContainsFunc(check.Records, func(rc api.RecordCheck) bool { return rc.Own && !rc.OK }) && (check.PortFree || !answering) {' \
  'if check.Ready && (check.PortFree || !answering) {' \
  ./internal/agent '^TestAnOwnAddressIsLookedAtEveryMinuteUntilItsRecordsWork$'

# Creator invites (the managed beta): the owner's alone, for an Admin with
# no servers and an allowance, and nobody else on the team sees them.
control "creator invites: only the owner can give an allowance" internal/invites/allowance.go \
  'if a.InstallRole != InstallOwner {' \
  'if false {' \
  ./internal/invites '^TestNewCreatorInvite$'
control "creator invites: a creator starts with no servers" internal/invites/invites.go \
  'if spec.Role != RoleAdmin || spec.Servers.All || len(spec.Servers.Servers) > 0 {' \
  'if spec.Role != RoleAdmin {' \
  ./internal/invites '^TestNewCreatorInvite$'
control "creator invites: one stops working when its creator couldn't make it as it stands" internal/invites/member.go \
  'if inviter.UserID != inv.CreatedBy || CanGrantAllowance(inviter, inv.Allowance) != nil || inv.Role != RoleAdmin {' \
  'if inviter.UserID != inv.CreatedBy {' \
  ./internal/invites '^TestAcceptCreatorInvite$'
control "creator invites: only the owner may change or turn one off" internal/panel/team.go \
  'return invites.CanGrantAllowance(a.Account, inv.Allowance)' \
  'return nil' \
  ./internal/panel '^TestCreatorInvitesAreTheOwnersAlone$'
control "creator invites: the rest of the team doesn't see them" internal/panel/team.go \
  'return canChangeInvite(a, inv) == nil' \
  'return true' \
  ./internal/panel '^TestCreatorInvitesAreTheOwnersAlone$'
control "creator invites: the member keeps the allowance" internal/panel/join.go \
  'grant.Servers.String(), grant.Allowance.Servers, grant.Allowance.MemoryMB, grant.Allowance.DiskGB, now)' \
  'grant.Servers.String(), 0, 0, grant.Allowance.DiskGB, now)' \
  ./internal/panel '^TestCreatorInvitesAreTheOwnersAlone$'
control "creators: their role and servers aren't changed on the Team page" internal/panel/team.go \
  "if !t.Allowance.IsZero() {
		writeErr(w, http.StatusConflict, api.CodeConflict, \"A creator's servers are the ones they create.\"" \
  "if false {
		writeErr(w, http.StatusConflict, api.CodeConflict, \"A creator's servers are the ones they create.\"" \
  ./internal/panel '^TestCreatorInvitesAreTheOwnersAlone$'

# Creators' servers: created inside the allowance, one change at a time,
# joining their servers; memory changes and restores stay inside it; only
# their own servers are theirs to delete.
control "creators: an admin of some servers without an allowance creates none" internal/panel/workspace.go \
  'case act == actCreateOwnServers && !a.Servers.All && a.Allowance.IsZero():' \
  'case act == actCreateOwnServers && false:' \
  ./internal/panel '^TestCreatorsCreateTheirOwnServersInsideTheirAllowance$'
control "creators: no more servers than the allowance" internal/panel/creators.go \
  'if use.servers >= a.Allowance.Servers {' \
  'if false {' \
  ./internal/panel '^TestCreatorsCreateTheirOwnServersInsideTheirAllowance$'
control "creators: no more memory than the allowance" internal/panel/creators.go \
  'if left := al.MemoryMB - use.memoryMB; memoryMB > left {' \
  'if left := al.MemoryMB - use.memoryMB; false && memoryMB > left {' \
  ./internal/panel '^TestCreatorsCreateTheirOwnServersInsideTheirAllowance$'
control "creators: a server the owner gave them keeps its memory" internal/panel/creators.go \
  'if memoryMB != cur {' \
  'if false && memoryMB != cur {' \
  ./internal/panel '^TestCreatorsCreateTheirOwnServersInsideTheirAllowance$'
control "creators: they delete only the servers they created" internal/panel/creators.go \
  'if !slices.Contains(owned, r.PathValue("id")) {' \
  'if false && !slices.Contains(owned, r.PathValue("id")) {' \
  ./internal/panel '^TestCreatorsCreateTheirOwnServersInsideTheirAllowance$'
control "creators: a new server joins their servers" internal/panel/creators.go \
  'if !sc.All && !slices.Contains(sc.Servers, id) {' \
  'if false {' \
  ./internal/panel '^TestCreatorsCreateTheirOwnServersInsideTheirAllowance$'
control "creators: memory changes in settings stay inside the allowance" internal/panel/creators.go \
  'if !s.creatorMemoryFits(w, r, sess.Access, r.PathValue("id"), mb) {' \
  'if false {' \
  ./internal/panel '^TestCreatorsCreateTheirOwnServersInsideTheirAllowance$'
control "creators: a restore's memory stays inside the allowance" internal/panel/team.go \
  'if method == "POST" && sess.Access.creator() {' \
  'if false {' \
  ./internal/panel '^TestCreatorsCreateTheirOwnServersInsideTheirAllowance$'
control "creators: the catalog offers only the memory their allowance has left" internal/panel/team.go \
  'if sess.Access.hidesMachines() {' \
  'if false {' \
  ./internal/panel '^TestCreatorsCreateTheirOwnServersInsideTheirAllowance$'
control "creators: two creates at once are checked one after the other" internal/panel/creators.go \
  'use, err := s.allowanceUse(r.Context(), a, m, "")' \
  's.creators.Unlock()
	defer s.creators.Lock()
	use, err := s.allowanceUse(r.Context(), a, m, "")' \
  ./internal/panel '^TestTwoCreatesAtOnceCantBothFitTheAllowance$'
control "creators: pre-generation only up to 2,500 blocks" internal/panel/creators.go \
  'return p.Radius <= creatorPregenRadius' \
  'return p.Radius > 0' \
  ./internal/panel '^TestCreatorsPreGenerateUpTo2500Blocks$'
control "creators: a larger pre-generation isn't started" internal/panel/creators.go \
  'if !creatorPreset(req.Preset) {' \
  'if false && !creatorPreset(req.Preset) {' \
  ./internal/panel '^TestCreatorsPreGenerateUpTo2500Blocks$'
control "creators: the larger sizes aren't offered" internal/panel/creators.go \
  'if p.Radius <= creatorPregenRadius {' \
  'if p.Radius > 0 {' \
  ./internal/panel '^TestCreatorsPreGenerateUpTo2500Blocks$'
control "creators: the Map tab fills in only the areas they may pre-generate" internal/panel/creators.go \
  'return id == api.MapAreaExplored || creatorPreset(id)' \
  'return id != ""' \
  ./internal/panel '^TestCreatorsPreGenerateUpTo2500Blocks$'
control "creators: a bigger map area isn't started" internal/panel/creators.go \
  'if !creatorArea(req.Area) {' \
  'if false && !creatorArea(req.Area) {' \
  ./internal/panel '^TestCreatorsPreGenerateUpTo2500Blocks$'
control "creators: a new server starts with backups" internal/panel/creators.go \
  's.startCreatorBackups(r.Context(), m, sess.Access, id)' \
  '_ = id' \
  ./internal/panel '^TestCreatorsCreateTheirOwnServersInsideTheirAllowance$'
control "creators: the backups a new server starts with are on" internal/panel/creators.go \
  '"automatic": map[string]any{"enabled": true, "everyHours": 24, "onlyIfPlayed": true},' \
  '"automatic": map[string]any{"enabled": false, "everyHours": 24, "onlyIfPlayed": true},' \
  ./internal/panel '^TestCreatorsCreateTheirOwnServersInsideTheirAllowance$'

# An address for each server: one wildcard record under the own domain.
control "server addresses: the wildcard record is among the records to add" internal/certs/join.go \
  'out = append(out, p.addrRecords("", p.wildcardName())...)' \
  '_ = p.wildcardName()' \
  ./internal/certs '^TestPlanWithAWildcard$'
control "server addresses: a server reached through the wildcard needs no SRV record" internal/certs/join.go \
  'return s.Host != "" && !s.Wild && ' \
  'return s.Host != "" && ' \
  ./internal/certs '^TestPlanWithAWildcard$'
control "server addresses: each look at the wildcard asks for a name of its own" internal/certs/join.go \
  '_, _ = rand.Read(b)' \
  '_, _ = rand.Read(b[:0])' \
  ./internal/certs '^TestCheckPlanChecksTheWildcard$'
control "server addresses: the wildcard's note names the record to fix" internal/certs/join.go \
  'params["name"] = wild' \
  '_ = params' \
  ./internal/certs '^TestCheckPlanChecksTheWildcard$'
control "server addresses: players type the port" internal/agent/address.go \
  'j.Address, j.OwnAddress = hostPort(s.own, s.port), s.own' \
  'j.Address, j.OwnAddress = s.own, s.own' \
  ./internal/agent '^TestEveryServerGetsAnAddressUnderTheWildcard$'
control "server addresses: an address from the wildcard works once the wildcard does" internal/agent/ownaddress.go \
  'if js.wild {' \
  'if false && js.wild {' \
  ./internal/agent '^TestEveryServerGetsAnAddressUnderTheWildcard$'
control "server addresses: a name given to one server isn't another's too" internal/agent/address.go \
  'js.own == "" && !given[name] && ' \
  'js.own == "" && ' \
  ./internal/agent '^TestANameGivenBeforeTheWildcardStaysItsServers$'
control "server addresses: no server is given another's address from the wildcard" internal/agent/ownaddress.go \
  'if js.id != s.id && js.own == name {' \
  'if false && js.id != s.id && js.own == name {' \
  ./internal/agent '^TestServersGivenAnAddressKeepItUnderTheWildcard$'
control "server addresses: under playkeeper.me, names Playkeeper keeps get none" internal/agent/address.go \
  '!(official && names.Reserved(js.slug))' \
  '!(official && false)' \
  ./internal/agent '^TestUnderPlaykeeperMeOfficialNamesGetNoAddress$'
control "server addresses: turning it off keeps the certificates" internal/agent/ownaddress.go \
  'a.audit(actor, "address.server_addresses",' \
  'a.forgetOwnCertificates(st, true); a.audit(actor, "address.server_addresses",' \
  ./internal/agent '^TestTheWildcardsCertificatesLastUntilTheirServerOrDomainGoes$'
control "server addresses: a deleted server's certificate goes" internal/agent/servers.go \
  's.forgetCertificate(wild)' \
  '_ = wild' \
  ./internal/agent '^TestTheWildcardsCertificatesLastUntilTheirServerOrDomainGoes$'
control "server addresses: an old domain's certificates go" internal/agent/address.go \
  'a.forgetOwnCertificates(st, true)' \
  '_ = st' \
  ./internal/agent '^TestTheWildcardsCertificatesLastUntilTheirServerOrDomainGoes$'
control "server addresses: a domain's certificates go while the switch is off too" internal/agent/ownaddress.go \
  'a.forgetCertificate(automaticName(st, servers, js))' \
  'if st.ServerAddresses { a.forgetCertificate(automaticName(st, servers, js)) }' \
  ./internal/agent '^TestStoppingTheDomainTakesTheWildcardsCertificatesWhileItsOff$'
control "server addresses: a name given by hand keeps its certificate when the domain moves" internal/agent/ownaddress.go \
  'func(o joinServer) bool { return o.own == name }' \
  'func(o joinServer) bool { return o.id != js.id && o.own == name }' \
  ./internal/agent '^TestAGivenSlugAddressKeepsItsCertificateWhenTheDomainMoves$'
control "server addresses: a server's leftover certificate from the wildcard goes with the domain" internal/agent/ownaddress.go \
  'js.slug == "" || ' \
  'js.slug == "" || js.own != "" || ' \
  ./internal/agent '^TestALeftoverCertificateFromTheWildcardGoesWithTheDomain$'
control "server addresses: a server given another address drops its certificate from the wildcard" internal/agent/ownaddress.go \
  'if name != "" && wild != name {' \
  'if false && name != "" && wild != name {' \
  ./internal/agent '^TestGivingAServerAnAddressForgetsItsCertificateFromTheWildcard$'
control "server addresses: a server given the name it has keeps its certificate" internal/agent/ownaddress.go \
  'if name != "" && wild != name {' \
  'if name != "" {' \
  ./internal/agent '^TestGivingAServerAnAddressForgetsItsCertificateFromTheWildcard$'
control "server addresses: each certificate request is logged for the day's count" internal/agent/ownaddress.go \
  'a.noteOwnCertAttempt()' \
  '_ = ctx' \
  ./internal/agent '^TestTheDaysCertificatesCountWhateverBecameOfTheirNames$'
control "server addresses: the day's count holds once the certificates are forgotten" internal/agent/ownaddress.go \
  'return max(logged, kept)' \
  'return kept' \
  ./internal/agent '^TestTheDaysCertificatesCountWhateverBecameOfTheirNames$'
control "server addresses: certificates asked before the log count too" internal/agent/ownaddress.go \
  'name != st.Host && !strings.HasPrefix(name, "*.") && fromMillis(last).After(since)' \
  'false && name != st.Host && !strings.HasPrefix(name, "*.") && fromMillis(last).After(since)' \
  ./internal/agent '^TestOwnAddressesGetAFewCertificatesADay$'
control "server addresses: a deleted server's certificate goes while the switch is off too" internal/agent/ownaddress.go \
  'return automaticName(s.address(), servers, js)' \
  'if st := s.address(); st.ServerAddresses { return automaticName(st, servers, js) }' \
  ./internal/agent '^TestTheWildcardsCertificatesLastUntilTheirServerOrDomainGoes$'
control "server addresses: only an admin of every server turns it on" internal/panel/server.go \
  'am("/api/machines/{mid}/address/server-addresses", "/v1/address/server-addresses"),' \
  '{"POST", "/api/machines/{mid}/address/server-addresses", needSessionCSRF, actView, s.addressProxy("POST", "/v1/address/server-addresses")},' \
  ./internal/panel '^TestMachineWideActionsNeedEveryServer$'
webcontrol "server addresses: the switch asks the agent" web/src/pages/machine-settings/own.tsx \
  "machineApi(id, '/address/server-addresses')" \
  "machineApi(id, '/address/server-address')" \
  src/pages/machine-settings/address.test.tsx 'lets the owner turn on an address for each server'
webcontrol "server addresses: an address from the wildcard leaves the field for one of its own" web/src/pages/machine-settings/own.tsx \
  "const saved = s.automatic ? '' : (s.ownAddress ?? '')" \
  "const saved = s.ownAddress ?? ''" \
  src/pages/machine-settings/address.test.tsx 'gives every server an address with one wildcard record'

# The network guard (internal/netguard): servers can't reach the machine or
# link-local addresses.
control "network guard: the rules go first in their chains" internal/netguard/netguard.go \
  'script = append(script, "-I "+chain+" 1 "+strings.Join(w[i], " "))' \
  'script = append(script, "-A "+chain+" "+strings.Join(w[i], " "))' \
  ./internal/netguard '^TestApplyPutsTheRulesFirstInTheirChains$'
control "network guard: buried rules move back to the top" internal/netguard/netguard.go \
  'top = top && i < len(w)' \
  'top = top || i < len(w)' \
  ./internal/netguard '^TestApplyMovesBuriedRulesBackToTheTop$'
control "network guard: the rules for an earlier bridge come out" internal/netguard/netguard.go \
  'script = append(script, "-D"+strings.TrimPrefix(r, "-A"))' \
  '_ = r' \
  ./internal/netguard '^TestApplyFollowsANewBridge$'
control "network guard: rules found at the top are checked for the bridge" internal/netguard/netguard.go \
  'if len(ours) == len(w) && top && present(ctx, run, f, chain, w) {' \
  'if len(ours) == len(w) && top {' \
  ./internal/netguard '^TestApplyFollowsANewBridge$'
control "network guard: new connections to the machine are refused, replies aren't" internal/netguard/netguard.go \
  '"!", "--ctstate", "ESTABLISHED,RELATED"' \
  '"--ctstate", "ESTABLISHED,RELATED"' \
  ./internal/netguard '^TestApplyPutsTheRulesFirstInTheirChains$'
control "network guard: DNS to the machine stays open" internal/netguard/netguard.go \
  '"-p", proto, "!", "--dport", "53",' \
  '"-p", proto,' \
  ./internal/netguard '^TestApplyPutsTheRulesFirstInTheirChains$'
control "network guard: every link-local address is out of reach" internal/netguard/netguard.go \
  '"-d", "169.254.0.0/16"' \
  '"-d", "169.254.169.254/32"' \
  ./internal/netguard '^TestApplyPutsTheRulesFirstInTheirChains$'
control "network guard: the metadata rule goes in DOCKER-USER when Docker has it" internal/netguard/netguard.go \
  'if t.chains["DOCKER-USER"] {' \
  'if false && t.chains["DOCKER-USER"] {' \
  ./internal/netguard '^TestApplyPutsTheRulesFirstInTheirChains$'
control "network guard: a restore leaves the other rules in place" internal/netguard/netguard.go \
  'f.restore, "-w", "5", "--noflush")' \
  'f.restore, "-w", "5")' \
  ./internal/netguard '^TestApplyPutsTheRulesFirstInTheirChains$'
control "network guard: a restore never declares, so empties, a chain" internal/netguard/netguard.go \
  'for i := len(w) - 1; i >= 0; i-- {' \
  'script = append(script, ":"+chain+" - [0:0]"); for i := len(w) - 1; i >= 0; i-- {' \
  ./internal/netguard '^TestApplyPutsTheRulesFirstInTheirChains$'
control "network guard: an unusable interface name is refused" internal/netguard/netguard.go \
  'if !ifname.MatchString(n.Bridge) {' \
  'if false && !ifname.MatchString(n.Bridge) {' \
  ./internal/netguard '^TestApplyRefusesAnUnusableInterfaceName$'
control "network guard: an IPv6 network is kept from the machine too" internal/netguard/netguard.go \
  'if !n.IPv6 {' \
  'if true || !n.IPv6 {' \
  ./internal/netguard '^TestAnIPv6NetworkIsKeptFromTheMachineToo$'
# shellcheck disable=SC2016
control "network guard: rules listed with quoted comments are the guard's" internal/netguard/netguard.go \
  'strings.Trim(w[i+1], `"`) == Tag' \
  'w[i+1] == Tag' \
  ./internal/netguard '^TestQuotedCommentsAreTheGuards$'
control "network guard: Remove takes out every rule of the guard's" internal/netguard/netguard.go \
  'for _, f := range []family{ipv4, ipv6} {' \
  'for _, f := range []family{} {' \
  ./internal/netguard '^TestRemoveTakesOutEveryRuleOfTheGuards$'
control "network guard: in place before a server's container starts" internal/agent/lifecycle.go \
  'a.guardNetwork(ctx)' \
  '_ = ctx' \
  ./internal/agent '^TestTheGuardIsInPlaceBeforeAServersContainerStarts$'
control "network guard: the agent puts back rules something removed" internal/agent/guard.go \
  't := time.NewTicker(a.opts.GuardInterval)' \
  't := time.NewTicker(time.Hour)' \
  ./internal/agent '^TestTheGuardPutsBackRulesSomethingRemoved$'
control "network guard: servers may reach the machine unless its owner keeps them away" internal/agent/guard.go \
  'a.keepAway = v == "on"' \
  'a.keepAway = v == "on" || true' \
  ./internal/agent '^TestByDefaultServersKeepOnlyOutOfTheMetadataService$'
control "network guard: the owner's switch keeps servers away from the machine" internal/agent/guard.go \
  'host := a.guardHost()' \
  'host := false' \
  ./internal/agent '^TestKeepingServersAwayIsTheOwnersSwitch$'
control "network guard: the owner's switch lasts when the agent restarts" internal/agent/guard.go \
  'a.keepAway = v == "on"' \
  'a.keepAway = v == "on" && false' \
  ./internal/agent '^TestKeepingServersAwayIsTheOwnersSwitch$'
control "network guard: the machine's status shows the owner's switch" internal/agent/guard.go \
  'g := api.NetworkGuard{Host: a.keepAway}' \
  'g := api.NetworkGuard{Host: false}' \
  ./internal/agent '^TestKeepingServersAwayIsTheOwnersSwitch$'
control "network guard: a database error leaves servers kept away" internal/agent/guard.go \
  'host := a.guardHost()' \
  'v, _, _ := a.kvGet(kvGuardHost); host := v == "on"' \
  ./internal/agent '^TestADatabaseErrorLeavesServersKeptAway$'
control "network guard: the owner's switch changes only once it's stored" internal/agent/guard.go \
  '[on]); err != nil {' \
  '[on]); false && err != nil {' \
  ./internal/agent '^TestADatabaseErrorLeavesServersKeptAway$'
control "network guard: no agent starts without reading the owner's switch" internal/agent/agent.go \
  'if err := a.loadGuard(); err != nil {' \
  'if err := a.loadGuard(); false && err != nil {' \
  ./internal/agent '^TestTheAgentWontStartWithoutReadingTheSwitch$'
control "network guard: no server starts while it can't be kept away" internal/agent/guard.go \
  'if g == nil || !g.Host || g.On {' \
  'if g == nil || !g.Host || true {' \
  ./internal/agent '^TestAServerWontStartWhileItCantBeKeptAway$'
control "network guard: with the switch off, servers start without the rules" internal/agent/guard.go \
  'if g == nil || !g.Host || g.On {' \
  'if g == nil || g.On {' \
  ./internal/agent '^TestAServerStartsWhenTheGuardCant$'
control "network guard: a creator invite keeps servers away from the machine first" internal/panel/team.go \
  'if err := s.keepServersAway(r.Context(), sess.User.Username); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/panel '^TestACreatorInviteKeepsServersAwayFromTheMachine$'
control "network guard: no creator invite until the agent keeps servers away" internal/panel/creators.go \
  'if err == nil && !g.Host {' \
  'if false && err == nil && !g.Host {' \
  ./internal/panel '^TestACreatorInviteKeepsServersAwayFromTheMachine$'
control "network guard: servers stay away while there are creators" internal/panel/creators.go \
  'if has {' \
  'if has && false {' \
  ./internal/panel '^TestServersStayAwayWhileThereAreCreators$'
control "network guard: a creator invite that still works counts as a creator" internal/panel/creators.go \
  'AND revoked_at = 0 AND uses < max_uses AND expires_at > ?)' \
  'AND 0 AND ?)' \
  ./internal/panel '^TestServersStayAwayWhileThereAreCreators$'
webcontrol "network guard: the switch in Machine settings asks the agent" web/src/pages/machine-settings/guard.tsx \
  "machineApi(id, '/network-guard')" \
  "machineApi(id, '/network-guards')" \
  src/pages/machine-settings/guard.test.tsx 'keeps servers away from the machine when the owner turns it on'
control "network guard: the machine's status says what went wrong" internal/agent/guard.go \
  'st.Problem = err.Error()' \
  '_ = err' \
  ./internal/agent '^TestAServerStartsWhenTheGuardCant$'
control "network guard: rules are for the network's own interface" internal/agent/guard.go \
  'if b := n.Options["com.docker.network.bridge.name"]; b != "" {' \
  'if b := ""; b != "" {' \
  ./internal/agent '^TestTheGuardUsesTheNetworksOwnInterfaceName$'
control "network guard: an IPv6 network gets the ip6tables rules" internal/agent/guard.go \
  'IPv6: n.EnableIPv6}' \
  'IPv6: false}' \
  ./internal/agent '^TestTheGuardKeepsAnIPv6NetworkFromTheMachine$'
control "network guard: a network that isn't a bridge isn't taken for one" internal/agent/guard.go \
  'if n.Driver != "bridge" {' \
  'if false && n.Driver != "bridge" {' \
  ./internal/agent '^TestTheGuardNeedsABridge$'
control "network guard: playkeeper dev leaves the machine's firewall alone" internal/agent/guard.go \
  'if cfg.Dev || euid != 0 {' \
  'if euid != 0 {' \
  ./internal/agent '^TestNoGuardInDevMode$'
control "network guard: the uninstall takes the rules out" internal/install/uninstall.go \
  'if sys.Firewall != nil {' \
  'if sys.Firewall == nil {' \
  ./internal/install '^TestUninstallTakesOutTheNetworkGuardsRules$'
control "network guard: the agent's unit lets it run iptables" internal/install/units.go \
  'CAP_NET_BIND_SERVICE CAP_NET_ADMIN CAP_NET_RAW' \
  'CAP_NET_BIND_SERVICE' \
  ./internal/install '^TestTheAgentMayOpenPort80AndChangeTheFirewallAndNothingMore$'
control "network guard: the agent's unit allows the netlink sockets iptables uses" internal/install/units.go \
  'AF_INET6 AF_NETLINK' \
  'AF_INET6' \
  ./internal/install '^TestTheAgentMayOpenPort80AndChangeTheFirewallAndNothingMore$'

# Playkeeper Cloud's disk limits (internal/agent/disklimits.go): what a
# customer's servers may take between them.
control "disk limits: they last when the agent restarts" internal/agent/disklimits.go \
  'return json.Unmarshal([]byte(v), &a.limits.limits)' \
  'return json.Unmarshal([]byte(v), &[]api.DiskLimit{})' \
  ./internal/agent '^TestDiskLimitsLastAndSayWhatTheirServersTake$'
control "disk limits: a change is kept" internal/agent/disklimits.go \
  'if err := a.kvSet(kvDiskLimits, string(raw)); err != nil {' \
  'if err := a.kvSet(kvDiskLimits+"-lost", string(raw)); err != nil {' \
  ./internal/agent '^TestDiskLimitsLastAndSayWhatTheirServersTake$'
control "disk limits: a server counts against one limit at most" internal/agent/disklimits.go \
  'if !reServerID.MatchString(id) || servers[id] {' \
  'if !reServerID.MatchString(id) {' \
  ./internal/agent '^TestDiskLimitsLastAndSayWhatTheirServersTake$'
control "disk limits: a limit is more than nothing" internal/agent/disklimits.go \
  'case l.LimitBytes <= 0 || l.LimitBytes > maxDiskLimitBytes:' \
  'case l.LimitBytes < 0 || l.LimitBytes > maxDiskLimitBytes:' \
  ./internal/agent '^TestDiskLimitsLastAndSayWhatTheirServersTake$'
control "disk limits: what an operation holds counts" internal/agent/disklimits.go \
  'used := usedBy(rep, l.Servers) + a.onTheWay(l) + a.limits.held[l.ID]' \
  'used := usedBy(rep, l.Servers) + a.onTheWay(l)' \
  ./internal/agent '^TestWhatAnOperationHoldsCountsAgainstTheLimit$'
control "disk limits: pre-generation under way counts" internal/agent/disklimits.go \
  ' + a.limits.held[l.ID] + a.pregenOnTheWay(l.Servers)' \
  ' + a.limits.held[l.ID]' \
  ./internal/agent '^TestPregenUnderWayCountsAgainstTheLimit$'
control "disk limits: pre-generation stops counting once it ends" internal/agent/disklimits.go \
  'case !t.unfinished():' \
  'case false && !t.unfinished():' \
  ./internal/agent '^TestPregenUnderWayCountsAgainstTheLimit$'
control "disk limits: what was just written counts until a scan finds it" internal/agent/disklimits.go \
  'if w.limit == l.ID && !w.at.Before(rep.ScannedAt) {' \
  'if false && w.limit == l.ID && !w.at.Before(rep.ScannedAt) {' \
  ./internal/agent '^TestBackupsStopAtTheDiskLimit$'
control "disk limits: files on their way count" internal/agent/disklimits.go \
  'if !up.gone && !f.placed {' \
  'if false && !up.gone && !f.placed {' \
  ./internal/agent '^TestUploadsStopAtTheDiskLimit$'
control "disk limits: a backup stops at the limit" internal/agent/disklimits.go \
  'return s.holdDiskLimit(ctx, s.id, need)' \
  'return s.holdDiskLimit(ctx, s.id, 0*need)' \
  ./internal/agent '^TestBackupsStopAtTheDiskLimit$'
control "disk limits: a backup that can't be measured still counts" internal/agent/disklimits.go \
  'return usedBy(rep, []string{s.id}), nil' \
  'return 0*usedBy(rep, []string{s.id}), nil' \
  ./internal/agent '^TestABackupThatCantBeMeasuredStillCounts$'
control "disk limits: a backup on request or on schedule is held" internal/agent/backups.go \
  'done, err := s.holdBackup(ctx)' \
  'done, err := func(bool) {}, error(nil)' \
  ./internal/agent '^TestBackupsStopAtTheDiskLimit$'
control "disk limits: a backup counts once it's written" internal/agent/backups.go \
  'done(err == nil)' \
  'done(false && err == nil)' \
  ./internal/agent '^TestBackupsStopAtTheDiskLimit$'
control "disk limits: the rollback archive before a restore, update or import is held" internal/agent/backups.go \
  'done, err := s.holdBackup(s.ctx)' \
  'done, err := func(bool) {}, error(nil)' \
  ./internal/agent '^TestARestoresRollbackArchiveHasToFit$'
control "disk limits: a refused scheduled backup shows on the World tab" internal/agent/schedules.go \
  'if errors.As(err, &ae) && ae.Code == api.CodeDiskLimit {' \
  'if false && errors.As(err, &ae) && ae.Code == api.CodeDiskLimit {' \
  ./internal/agent '^TestBackupsStopAtTheDiskLimit$'
control "disk limits: a file stops at the limit" internal/agent/fileuploads.go \
  'if err := s.diskLimitRefusal(ctx, s.id, size); err != nil {' \
  'if err := s.diskLimitRefusal(ctx, s.id, 0); err != nil {' \
  ./internal/agent '^TestUploadsStopAtTheDiskLimit$'
control "disk limits: a file just put in place counts" internal/agent/fileuploads.go \
  's.noteDiskWrite(s.id, size)' \
  's.noteDiskWrite(s.id, 0*size)' \
  ./internal/agent '^TestUploadsStopAtTheDiskLimit$'
control "disk limits: a world to import stops at the limit" internal/agent/worldimports.go \
  'if err := a.diskLimitRefusal(ctx, imp.serverID, size); err != nil {' \
  'if err := a.diskLimitRefusal(ctx, imp.serverID, 0); err != nil {' \
  ./internal/agent '^TestImportsPacksAndPregenStopAtTheDiskLimit$'
control "disk limits: applying an imported world holds what it adds" internal/agent/worldimports.go \
  'done, err := s.holdDiskLimit(r.Context(), s.id, pv.Preview.SizeBytes)' \
  'done, err := s.holdDiskLimit(r.Context(), s.id, 0)' \
  ./internal/agent '^TestAnAppliedImportHoldsTheWorldItAdds$'
control "disk limits: a restore upload stops at the limit" internal/agent/handlers.go \
  'err = target.diskLimitRefusal(r.Context(), target.id, r.ContentLength)' \
  'err = target.diskLimitRefusal(r.Context(), target.id, 0)' \
  ./internal/agent '^TestImportsPacksAndPregenStopAtTheDiskLimit$'
control "disk limits: a staged restore counts as the world it unpacks to" internal/agent/handlers.go \
  'err = target.diskLimitRefusal(r.Context(), target.id, unpackedBytes(f.Manifest))' \
  'err = target.diskLimitRefusal(r.Context(), target.id, 0*unpackedBytes(f.Manifest))' \
  ./internal/agent '^TestRestoresCountTheWorldTheyUnpackTo$'
control "disk limits: a restore refused once it's staged leaves no stage" internal/agent/handlers.go \
  'err = target.diskLimitRefusal(r.Context(), target.id, unpackedBytes(f.Manifest))
		}
		if err != nil {
			os.RemoveAll(a.stageDir(p.ID))' \
  'err = target.diskLimitRefusal(r.Context(), target.id, unpackedBytes(f.Manifest))
		}
		if err != nil {
			_ = p.ID' \
  ./internal/agent '^TestRestoresCountTheWorldTheyUnpackTo$'
control "disk limits: a world's size is its files, not what its manifest claims" internal/agent/disklimits.go \
  'n += f.Size' \
  'n = m.TotalBytes + 0*f.Size' \
  ./internal/agent '^TestRestoresCountTheWorldTheyUnpackTo$'
control "disk limits: a refused restore leaves no stage" internal/agent/backups.go \
  'return fail(&apiError{Status: http.StatusInsufficientStorage, Code: api.CodeDiskLimit,' \
  'return nil, (&apiError{Status: http.StatusInsufficientStorage, Code: api.CodeDiskLimit,' \
  ./internal/agent '^TestRestoresCountTheWorldTheyUnpackTo$'
control "disk limits: applying a restore holds the world it unpacks to" internal/agent/handlers.go \
  'target.holdDiskLimit(r.Context(), target.id, unpackedBytes(st.manifest))' \
  'target.holdDiskLimit(r.Context(), target.id, 0*unpackedBytes(st.manifest))' \
  ./internal/agent '^TestRestoresCountTheWorldTheyUnpackTo$'
control "disk limits: restoring a backup of the server's own is held too" internal/agent/handlers.go \
  'target.holdDiskLimit(r.Context(), target.id, unpackedBytes(st.manifest))' \
  'target.holdDiskLimit(r.Context(), target.id, map[bool]int64{true: unpackedBytes(st.manifest)}[strings.HasPrefix(p.Source, "upload")])' \
  ./internal/agent '^TestRestoresCountTheWorldTheyUnpackTo$'
control "disk limits: a data or resource pack stops at the limit" internal/agent/packs.go \
  'if err := s.diskLimitRefusal(r.Context(), s.id, n); err != nil {' \
  'if err := s.diskLimitRefusal(r.Context(), s.id, 0); err != nil {' \
  ./internal/agent '^TestImportsPacksAndPregenStopAtTheDiskLimit$'
control "disk limits: an installed data pack counts" internal/agent/packs.go \
  's.noteDiskWrite(s.id, n)' \
  's.noteDiskWrite(s.id, 0*n)' \
  ./internal/agent '^TestAnInstalledDataPackCounts$'
control "disk limits: pre-generation stops at the limit" internal/agent/pregen.go \
  'done, err := s.holdDiskLimit(s.ctx, s.id, est.DiskHigh)' \
  'done, err := s.holdDiskLimit(s.ctx, s.id, 0*est.DiskHigh)' \
  ./internal/agent '^TestImportsPacksAndPregenStopAtTheDiskLimit$'
control "disk limits: a pre-generation start holds its area until its task is recorded" internal/agent/pregen.go \
  'defer done(false)' \
  'done(false)' \
  ./internal/agent '^TestPregenHoldsItsAreaFromTheCheck$'
control "disk limits: a refused pre-generation start leaves the running one's reservation" internal/agent/pregen.go \
  'if task.unfinished() && !forMap {' \
  'if task != nil { s.notePregen(s.id, est.DiskHigh) }; if task.unfinished() && !forMap {' \
  ./internal/agent '^TestPregenHoldsItsAreaFromTheCheck$'
control "disk limits: pre-generation reserves what it may write" internal/agent/pregen.go \
  's.notePregen(s.id, est.DiskHigh)' \
  's.notePregen(s.id, 0*est.DiskHigh)' \
  ./internal/agent '^TestPregenUnderWayCountsAgainstTheLimit$'
webcontrol "disk limits: a backup refused at the limit offers no button" web/src/components/app/backup-refused.tsx \
  '!full &&' \
  '(true || !full) &&' \
  src/pages/pages.test.tsx 'says what to do when the disk limit stops scheduled backups, with no button'

# Playkeeper Cloud's processor shares (internal/agent/disklimits.go): a
# customer's servers get a share of a core for each GB of memory.
control "processor shares: a share is kept" internal/agent/disklimits.go \
  'Servers: list, CPUMilliPerGB: l.CPUMilliPerGB, Hold: l.Hold})' \
  'Servers: list, Hold: l.Hold})' \
  ./internal/agent '^TestCustomersServersGetTheirShareOfTheProcessor$'
control "processor shares: a new share is a change" internal/agent/disklimits.go \
  ' && x.CPUMilliPerGB == y.CPUMilliPerGB' \
  '' \
  ./internal/agent '^TestCustomersServersGetTheirShareOfTheProcessor$'
control "processor shares: a share has bounds" internal/agent/disklimits.go \
  'case l.CPUMilliPerGB != 0 && (l.CPUMilliPerGB < minCPUMilliPerGB || l.CPUMilliPerGB > maxCPUMilliPerGB):' \
  'case l.CPUMilliPerGB < 0:' \
  ./internal/agent '^TestCustomersServersGetTheirShareOfTheProcessor$'
control "processor shares: a running server's cap changes at once" internal/agent/disklimits.go \
  'a.recapCPUs(ctx, a.diskLimits())' \
  '_ = ctx' \
  ./internal/agent '^TestCustomersServersGetTheirShareOfTheProcessor$'
control "processor shares: a cap one set missed is put right by the next" internal/agent/disklimits.go \
  'a.recapCPUs(ctx, a.diskLimits())' \
  'if changed { a.recapCPUs(ctx, a.diskLimits()) }' \
  ./internal/agent '^TestACapAChangeMissedIsPutRightByTheNextSet$'
control "processor shares: the cap reaches Docker" internal/docker/client.go \
  'map[string]int64{"NanoCpus": nanoCPUs}' \
  'map[string]int64{"CpuShares": nanoCPUs}' \
  ./internal/agent '^TestCustomersServersGetTheirShareOfTheProcessor$'
control "processor shares: lifting a limit gives every core back" internal/agent/disklimits.go \
  'want = all' \
  'want = 0 * all' \
  ./internal/agent '^TestCustomersServersGetTheirShareOfTheProcessor$'
control "processor shares: a new container gets its cap" internal/agent/lifecycle.go \
  'cfg.HostConfig.NanoCPUs = s.cpuCap(sc.MemoryMB)' \
  'cfg.HostConfig.NanoCPUs = 0 * s.cpuCap(sc.MemoryMB)' \
  ./internal/agent '^TestCustomersServersGetTheirShareOfTheProcessor$'
control "processor shares: a stopped container with another cap is made again" internal/agent/lifecycle.go \
  ' || c.HostConfig.NanoCPUs != spec.HostConfig.NanoCPUs):' \
  '):' \
  ./internal/agent '^TestCustomersServersGetTheirShareOfTheProcessor$'
control "processor shares: no cap passes the machine's cores" internal/agent/disklimits.go \
  ', int64(numCPU())*1_000_000_000)' \
  ', int64(numCPU())*1_000_000_000*1000)' \
  ./internal/agent '^TestCustomersServersGetTheirShareOfTheProcessor$'
control "processor shares: the dashboard gives each creator's servers half a core per GB" internal/panel/disklimits.go \
  'Servers: ids, CPUMilliPerGB: cpuMilliPerGB, Hold: in.holds[uid]})' \
  'Servers: ids, Hold: in.holds[uid]})' \
  ./internal/panel '^TestEachCreatorsServersGetTheirAllowancesDisk$'

# Playkeeper Cloud's disk limits, the dashboard's half
# (internal/panel/disklimits.go): each creator's servers get their
# allowance's disk between them on their machine.
control "dashboard disk limits: an invite keeps its disk" internal/panel/friends.go \
  'inv.Allowance.Servers, inv.Allowance.MemoryMB, inv.Allowance.DiskGB)' \
  'inv.Allowance.Servers, inv.Allowance.MemoryMB, 0*inv.Allowance.DiskGB)' \
  ./internal/panel '^TestACreatorInvitesDiskGoesWithIt$'
control "dashboard disk limits: the account an invite makes keeps its disk" internal/panel/join.go \
  'grant.Allowance.Servers, grant.Allowance.MemoryMB, grant.Allowance.DiskGB, now)' \
  'grant.Allowance.Servers, grant.Allowance.MemoryMB, 0*grant.Allowance.DiskGB, now)' \
  ./internal/panel '^TestACreatorInvitesDiskGoesWithIt$'
control "dashboard disk limits: an account's access reads its disk" internal/panel/workspace.go \
  '&a.Allowance.Servers, &a.Allowance.MemoryMB, &a.Allowance.DiskGB, &customer)' \
  '&a.Allowance.Servers, &a.Allowance.MemoryMB, new(int), &customer)' \
  ./internal/panel '^TestACreatorInvitesDiskGoesWithIt$'
control "dashboard disk limits: the default disk is 7.5 GB per GB of memory" internal/invites/allowance.go \
  'return int64(al.MemoryMB) * 15 << 19' \
  'return int64(al.MemoryMB) * 16 << 19' \
  ./internal/panel '^TestEachCreatorsServersGetTheirAllowancesDisk$'
control "dashboard disk limits: an allowance's own disk is what it gives" internal/invites/allowance.go \
  'return int64(al.DiskGB) << 30' \
  'return int64(al.DiskGB) << 29' \
  ./internal/panel '^TestEachCreatorsServersGetTheirAllowancesDisk$'
control "dashboard disk limits: an allowance's disk has a ceiling" internal/invites/allowance.go \
  'case al.DiskGB < 0 || al.DiskGB > MaxAllowanceDiskGB:' \
  'case al.DiskGB < 0:' \
  ./internal/invites '^TestNewCreatorInvite$'
control "dashboard disk limits: the limits carry each account's own disk" internal/panel/disklimits.go \
  'if err := rows.Scan(&uid, &al.Servers, &al.MemoryMB, &al.DiskGB); err != nil {' \
  'if err := rows.Scan(&uid, &al.Servers, &al.MemoryMB, new(int)); err != nil {' \
  ./internal/panel '^TestEachCreatorsServersGetTheirAllowancesDisk$'
control "dashboard disk limits: a creator's new server sends them at once" internal/panel/creators.go \
  's.kickDiskLimits()' \
  '_ = s.kickDiskLimits' \
  ./internal/panel '^TestEachCreatorsServersGetTheirAllowancesDisk$'
control "dashboard disk limits: removing a creator sends them at once" internal/panel/team.go \
  's.kickDiskLimits()' \
  '_ = s.kickDiskLimits' \
  ./internal/panel '^TestEachCreatorsServersGetTheirAllowancesDisk$'
control "dashboard disk limits: the Team page shows what a creator's servers take" internal/panel/team.go \
  'row.DiskUsedBytes = s.diskUsed(t.UserID)' \
  '_ = s.diskUsed(t.UserID)' \
  ./internal/panel '^TestEachCreatorsServersGetTheirAllowancesDisk$'
control "dashboard disk limits: what they take comes from the machine" internal/panel/disklimits.go \
  'used[uid] = l.UsedBytes' \
  'used[uid] = 0*l.UsedBytes' \
  ./internal/panel '^TestEachCreatorsServersGetTheirAllowancesDisk$'
webcontrol "dashboard disk limits: the Team page says what a creator's servers take" web/src/pages/team.tsx \
  "m.diskUsedBytes === undefined ? t('team.diskOf', { limit }) : t('team.diskUsed', { used: formatBytes(m.diskUsedBytes), limit })" \
  "t('team.diskOf', { limit })" \
  src/pages/pages.test.tsx 'disk, and how much their servers take once'
webcontrol "dashboard disk limits: the Team page's default disk matches the dashboard's" web/src/lib/access.ts \
  'al.memoryMB * 7.5' \
  'al.memoryMB * 8' \
  src/pages/pages.test.tsx 'disk, and how much their servers take once'

# Playkeeper Cloud's customer accounts (internal/panel/customers.go): each
# customer gets an account of their own, found by provider, store and id
# alone.
control "customers: an account is found by provider, store and id, and made once" internal/panel/customers.go \
  'WHERE c.provider = ? AND c.store = ? AND c.subject = ?`' \
  'WHERE c.provider = ? AND c.store = ? AND c.subject = ? AND 0`' \
  ./internal/panel '^TestACustomerGetsAnAccountOfTheirOwn$'
control "customers: a name an account has is never taken" internal/panel/customers.go \
  'if n == 0 {' \
  'if n >= 0 {' \
  ./internal/panel '^TestACustomerNamedLikeTheOwnerGetsAnAccountOfTheirOwn$'
control "customers: a reserved name is never taken" internal/panel/customers.go \
  'if reservedNames[name] {' \
  'if false && reservedNames[name] {' \
  ./internal/panel '^TestCustomersNamesNeverTakeAnother$'
control "customers: a name is plain lower-case letters, digits and dashes" internal/panel/customers.go \
  "case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':" \
  'case unicode.IsLetter(r) || unicode.IsDigit(r):' \
  ./internal/panel '^TestCustomersNamesNeverTakeAnother$'
control "customers: password sign-in refuses a customer" internal/panel/auth.go \
  'if h == "" || s.isCustomer(u.ID) {' \
  'if h == "" {' \
  ./internal/panel '^TestPasswordSignInRefusesCustomers$'
control "customers: a customer is Admin with no two-factor sign-in of ours" internal/panel/workspace.go \
  'a.FactorOn, a.TwoFactor = true, true' \
  'a.FactorOn = true' \
  ./internal/panel '^TestACustomerGetsAnAccountOfTheirOwn$'
control "customers: a customer's provider and id are checked" internal/panel/customers.go \
  'if cust.Provider == "" || len(cust.Provider) > maxCustomerProvider || cust.Subject == "" || len(cust.Subject) > maxCustomerSubject ||' \
  'if false && (cust.Provider == "" || len(cust.Provider) > maxCustomerProvider || cust.Subject == "" || len(cust.Subject) > maxCustomerSubject) ||' \
  ./internal/panel '^TestTheCoreRefusesWhatIsntACustomer$'
control "customers: a plan no account may have is refused" internal/panel/customers.go \
  'if err := al.Check(); err != nil {' \
  'if err := al.Check(); false && err != nil {' \
  ./internal/panel '^TestTheCoreRefusesWhatIsntACustomer$'
control "customers: a new plan gives its allowance" internal/panel/customers.go \
  'al.Servers, al.MemoryMB, al.DiskGB, info.UserID, al.Servers, al.MemoryMB, al.DiskGB)' \
  'al.Servers, al.MemoryMB, al.DiskGB, 0*info.UserID, al.Servers, al.MemoryMB, al.DiskGB)' \
  ./internal/panel '^TestACustomerGetsAnAccountOfTheirOwn$'
control "customers: a customer waiting for room is placed once there's room" internal/panel/customers.go \
  "WHERE c.state = ? AND (COALESCE(h.machine_id, '') = '' OR c.told_ready = 0 OR c.told_waiting != 0) ORDER BY c.created_at" \
  'WHERE c.state = ? AND 0 ORDER BY c.created_at' \
  ./internal/panel '^TestACustomerWaitingForRoomIsPlacedOnceThereIsRoom$'
control "ready server: a ready message that failed after placing is sent later" internal/panel/customers.go \
  ' OR c.told_ready = 0 OR' \
  ' OR' \
  ./internal/panel '^TestAReadyMessageThatFailedAfterPlacingIsSentLater$'
control "removing a machine: a message that there's room again, which failed, is sent later" internal/panel/customers.go \
  ' OR c.told_waiting != 0)' \
  ')' \
  ./internal/panel '^TestACustomerWhoLostTheirMachineIsToldThereIsNoRoomNotThatTheirServerIsBeingSetUp$'
control "customers: a paused customer waiting isn't placed" internal/panel/readyserver.go \
  'if CustomerState(state) != CustomerActive {' \
  'if false {' \
  ./internal/panel '^TestACustomerWaitingForRoomIsPlacedOnceThereIsRoom$'
control "customers: the owner can't remove a customer" internal/panel/team.go \
  'writeRefusal(w, errCustomerStays)' \
  '_ = errCustomerStays' \
  ./internal/panel '^TestTheTeamPageKeepsACustomersAccount$'
control "customers: the Team page offers no removal of a customer" internal/panel/team.go \
  ' && t.Customer == ""' \
  '' \
  ./internal/panel '^TestTheTeamPageKeepsACustomersAccount$'
control "customers: the Team page marks a customer" internal/panel/team.go \
  'Scan(&row.Customer, &row.Handle, &row.CustomerState' \
  'Scan(new(string), new(string), &row.CustomerState' \
  ./internal/panel '^TestACustomerGetsAnAccountOfTheirOwn$'
webcontrol "customers: the Team page says a customer signs in with Whop" web/src/pages/team.tsx \
  "m.customer === 'whop' ? t('team.signsInWithWhop', { handle: m.handle ?? '' }) : " \
  '' \
  src/pages/pages.test.tsx 'shows a customer as one, signing in with Whop'
webcontrol "customers: the Team page names a customer's role" web/src/pages/team.tsx \
  "if (m.customer) return t('team.customer')" \
  '' \
  src/pages/pages.test.tsx 'shows a customer as one, signing in with Whop'

# Customers per store (the hosted blueprint's 1.2): someone who buys from two
# stores has two accounts that share nothing, found only with their store;
# what they're told goes only to that store's chat, and Sign in with Whop
# opens the account of the store the sign-in is for.
control "customers per store: an account is found only with its store" internal/panel/customers.go \
  'WHERE c.provider = ? AND c.store = ? AND c.subject = ?`' \
  'WHERE c.provider = ? AND (c.store = ? OR 1) AND c.subject = ?`' \
  ./internal/panel '^TestACustomerOfTwoStoresHasTwoAccounts$'
control "customers per store: a new account keeps its store" internal/panel/customers.go \
  'id, cust.Provider, cust.Store, cust.Subject, cust.Handle, p.ID, string(CustomerActive), now, now); err != nil {' \
  'id, cust.Provider, cust.Store[:0], cust.Subject, cust.Handle, p.ID, string(CustomerActive), now, now); err != nil {' \
  ./internal/panel '^TestACustomerOfTwoStoresHasTwoAccounts$'
control "customers per store: a customer needs a store" internal/panel/customers.go \
  'cust.Store == "" || len(cust.Store) > maxCustomerStore ||' \
  'false && cust.Store == "" || len(cust.Store) > maxCustomerStore ||' \
  ./internal/panel '^TestTheCoreRefusesWhatIsntACustomer$'
control "customers per store: no customer is found without a store" internal/panel/customers.go \
  'if store == "" {' \
  'if false {' \
  ./internal/panel '^TestTheStoreConnectedNextTakesOnTheCustomersFromBeforeStores$'
control "customers per store: a customer from before stores is in no store" internal/panel/customers.go \
  "AND store != '' ORDER BY created_at, user_id" \
  'ORDER BY created_at, user_id' \
  ./internal/panel '^TestTheStoreConnectedNextTakesOnTheCustomersFromBeforeStores$'
control "customers per store: a Whop user has an account at each store they buy from" internal/panel/auth.go \
  '  UNIQUE(provider, store, subject)' \
  '  UNIQUE(provider, subject)' \
  ./internal/panel '^TestTheMigrationGivesCustomersTheirStore$'
control "customers per store: never two accounts at one store" internal/panel/auth.go \
  '  UNIQUE(provider, store, subject)' \
  '  UNIQUE(provider, store, subject, user_id)' \
  ./internal/panel '^TestTheMigrationGivesCustomersTheirStore$'
control "customers per store: the migration gives Whop customers the store sold for" internal/panel/auth.go \
  "CASE WHEN provider = 'whop' THEN COALESCE((SELECT account_id FROM whop_account WHERE id = 1), '') ELSE '' END" \
  "''" \
  ./internal/panel '^TestTheMigrationGivesCustomersTheirStore$'
control "customers per store: only customers of no store are taken on" internal/panel/whop.go \
  "customers SET store = ? WHERE provider = ? AND store = ''" \
  'customers SET store = ? WHERE provider = ? AND store = store' \
  ./internal/panel '^TestTheStoreConnectedNextTakesOnTheCustomersFromBeforeStores$'
control "customers per store: the Whop side starts each customer at its store" internal/panel/whop_customers.go \
  'cust := Customer{Provider: whopProvider, Store: store, Subject: wc.WhopUserID, Handle: wc.Handle}' \
  'cust := Customer{Provider: whopProvider, Store: store[:0], Subject: wc.WhopUserID, Handle: wc.Handle}' \
  ./internal/panel '^TestAConfirmedMembershipStartsTheCustomerAndTheCoresMessageReachesThem$'
control "customers per store: a message goes only to its customer's store's chat" internal/panel/whop_customers.go \
  'SELECT store_id, ?, ?, ?, ? FROM whop_stores WHERE store_id = ?`' \
  "SELECT store_id, ?, ?, ?, ? FROM whop_stores WHERE via = 'key' AND ? != ''\`" \
  ./internal/panel '^TestTheNotifierTakesOnlyWhopCustomersAndSendsTheirMessagesInOrder$'
control "customers per store: nothing is taken for a customer of no store" internal/panel/whop_customers.go \
  'return errNotThisStore' \
  'return nil' \
  ./internal/panel '^TestTheStoreConnectedNextTakesOnTheCustomersFromBeforeStores$'
control "customers per store: a waiting customer's message names their store" internal/panel/readyserver.go \
  'Scan(&cust.Provider, &cust.Store, &cust.Subject, &cust.Handle, &state, &planID, &al.Servers, &al.MemoryMB, &al.DiskGB)' \
  'Scan(&cust.Provider, new(string), &cust.Subject, &cust.Handle, &state, &planID, &al.Servers, &al.MemoryMB, &al.DiskGB)' \
  ./internal/panel '^TestACustomerWaitingForRoomIsPlacedOnceThereIsRoom$'
control "customers per store: a lapsed customer's message names their store" internal/panel/deletion.go \
  'Scan(&info.customer.Provider, &info.customer.Store, &info.customer.Subject' \
  'Scan(&info.customer.Provider, new(string), &info.customer.Subject' \
  ./internal/panel '^TestALapsedCustomersServersGoWithAFinalBackupKept$'
control "customers per store: a sign-in keeps the store it's for" internal/panel/whop_signin.go \
  'tokenHash(state), verifier, now.UnixMilli(), store); err != nil {' \
  'tokenHash(state), verifier, now.UnixMilli(), store[:0]); err != nil {' \
  ./internal/panel '^TestSignInWithWhopOpensTheStoresAccount$'
control "customers per store: coming back from Whop opens the store's account" internal/panel/whop_signin.go \
  'acct, ok, err := s.signInAccount(ctx, store, who.Subject)' \
  'acct, ok, err := s.signInAccount(ctx, "", who.Subject)' \
  ./internal/panel '^TestSignInWithWhopOpensTheStoresAccount$'
control "customers per store: with accounts at two stores and none named, neither opens" internal/panel/whop_signin.go \
  'case len(stores) > 1:' \
  'case false:' \
  ./internal/panel '^TestSignInWithWhopOpensTheStoresAccount$'
control "customers per store: an account on its way is on its way at its own store" internal/panel/whop_signin.go \
  "WHERE (? = '' OR m.store_id = ?) AND m.whop_user_id = ?" \
  "WHERE (? = '' OR ? != '') AND m.whop_user_id = ?" \
  ./internal/panel '^TestSignInWithWhopOpensTheStoresAccount$'
webcontrol "customers per store: the sign-in page signs in for its link's store" web/src/pages/login.tsx \
  'render={<a href={whopSignInFor(whopStore)} />}' \
  'render={<a href={whopSignInFor()} />}' \
  src/pages/login.test.tsx 'signs in for the store a customer'

# Stores (the hosted blueprint's 1.1): one dashboard sells for many Whop
# businesses (internal/panel/whop_stores.go), and each store's plans,
# memberships, customers, messages and stock are its own.
control "stores: a membership stays with its store" internal/panel/whop_customers.go \
  'WHERE whop_memberships.store_id = excluded.store_id' \
  'WHERE whop_memberships.store_id = excluded.store_id OR 1' \
  ./internal/panel '^TestAMembershipStaysWithItsStore$'
control "stores: the key store's webhook keeps no other business's event" internal/panel/whop_customers.go \
  'if ev.AccountID == "" || ev.AccountID == account {' \
  'if true {' \
  ./internal/panel '^TestAMembershipStaysWithItsStore$'
control "stores: a plan stays with its store" internal/panel/whop.go \
  'free = excluded.free WHERE whop_plans.store_id = excluded.store_id' \
  'free = excluded.free WHERE 1' \
  ./internal/panel '^TestReadingOneStoreLeavesAnothersPlans$'
control "stores: reading a store leaves another's plans" internal/panel/whop.go \
  'DELETE FROM whop_plans WHERE store_id = ? AND plan_id NOT IN' \
  "DELETE FROM whop_plans WHERE ? != '' AND plan_id NOT IN" \
  ./internal/panel '^TestReadingOneStoreLeavesAnothersPlans$'
control "stores: a store's customers have its own memberships" internal/panel/whop_customers.go \
  'LEFT JOIN whop_plans p ON p.store_id = m.store_id AND p.plan_id = m.plan_id WHERE m.store_id = ? ORDER BY' \
  'LEFT JOIN whop_plans p ON p.store_id = m.store_id AND p.plan_id = m.plan_id WHERE m.store_id = ? OR 1 ORDER BY' \
  ./internal/panel '^TestEachStoreStartsItsOwnCustomers$'
control "stores: a store's customers are its own" internal/panel/whop_customers.go \
  'problem, updated_at FROM whop_customers WHERE store_id = ?`' \
  'problem, updated_at FROM whop_customers WHERE store_id = ? OR 1`' \
  ./internal/panel '^TestEachStoreStartsItsOwnCustomers$'
control "stores: Settings › Sell on Whop shows the key store's plans alone" internal/panel/whop.go \
  "FROM whop_plans WHERE store_id = ? AND visibility != 'archived' ORDER BY position" \
  "FROM whop_plans WHERE (store_id = ? OR 1) AND visibility != 'archived' ORDER BY position" \
  ./internal/panel '^TestEachStoreStartsItsOwnCustomers$'
control "stores: a store's pass sends its own messages alone" internal/panel/whop_customers.go \
  'WHERE w.store_id = ? AND w.sent_at = 0' \
  'WHERE (w.store_id = ? OR 1) AND w.sent_at = 0' \
  ./internal/panel '^TestAStoresMessagesGoOutInItsOwnChats$'
control "stores: a message goes out as its own store's owner" internal/panel/whop_stores.go \
  'if st.Via == whopViaApp {' \
  'if false {' \
  ./internal/panel '^TestAStoresMessagesGoOutInItsOwnChats$'
control "stores: a cancellation is reminded at its own store" internal/panel/whop_customers.go \
  'WHERE o.store_id = m.store_id AND o.whop_user_id = m.whop_user_id' \
  'WHERE o.whop_user_id = m.whop_user_id' \
  ./internal/panel '^TestACancellationAtOneStoreIsRemindedThere$'
control "stores: each plan's stock is kept for its store" internal/panel/whop_stock.go \
  'SELECT store_id, plan_id, ?, ? FROM whop_plans WHERE plan_id = ?' \
  "SELECT '', plan_id, ?, ? FROM whop_plans WHERE plan_id = ?" \
  ./internal/panel '^TestEachStoresStockIsItsOwn$'
control "stores: a store's stock counts its own customers' purchases" internal/panel/whop_stock.go \
  "FROM whop_customers WHERE store_id = ? AND paused = 0 AND applied != ''" \
  "FROM whop_customers WHERE (store_id = ? OR 1) AND paused = 0 AND applied != ''" \
  ./internal/panel '^TestEachStoresStockIsItsOwn$'
control "stores: a taken-over store's plans take no room" internal/panel/whop_stock.go \
  "AND st.taken_over_by = '' AND st.suspended_at = 0 AND st.left_at = 0" \
  'AND st.suspended_at = 0 AND st.left_at = 0' \
  ./internal/panel '^TestATakenOverStoresPlansTakeNoRoom$'
control "stores: disconnecting forgets the key store's plans alone" internal/panel/whop.go \
  'DELETE FROM whop_plans WHERE store_id = ?`' \
  'DELETE FROM whop_plans WHERE store_id = ? OR 1`' \
  ./internal/panel '^TestDisconnectingTheKeyStoreLeavesTheOtherStores$'
control "stores: disconnecting forgets the key store's memberships alone" internal/panel/whop.go \
  'DELETE FROM whop_memberships WHERE store_id = ?`' \
  'DELETE FROM whop_memberships WHERE store_id = ? OR 1`' \
  ./internal/panel '^TestDisconnectingTheKeyStoreLeavesTheOtherStores$'
control "stores: disconnecting forgets the key store's customers alone" internal/panel/whop.go \
  'DELETE FROM whop_customers WHERE store_id = ?`' \
  'DELETE FROM whop_customers WHERE store_id = ? OR 1`' \
  ./internal/panel '^TestDisconnectingTheKeyStoreLeavesTheOtherStores$'
control "stores: disconnecting forgets the key store's messages alone" internal/panel/whop.go \
  'DELETE FROM whop_messages WHERE store_id = ?`' \
  'DELETE FROM whop_messages WHERE store_id = ? OR 1`' \
  ./internal/panel '^TestDisconnectingTheKeyStoreLeavesTheOtherStores$'
control "stores: disconnecting forgets the key store's stock alone" internal/panel/whop.go \
  'DELETE FROM whop_stock WHERE store_id = ?`' \
  'DELETE FROM whop_stock WHERE store_id = ? OR 1`' \
  ./internal/panel '^TestDisconnectingTheKeyStoreLeavesTheOtherStores$'
control "stores: disconnecting forgets the key store alone" internal/panel/whop.go \
  'DELETE FROM whop_stores WHERE store_id = ?`' \
  'DELETE FROM whop_stores WHERE store_id = ? OR 1`' \
  ./internal/panel '^TestDisconnectingTheKeyStoreLeavesTheOtherStores$'
control "stores: Sign in with Whop stays while a store sells" internal/panel/whop.go \
  "UPDATE whop_app SET client_id = '', client_secret = '' WHERE NOT EXISTS (SELECT 1 FROM whop_stores)" \
  "UPDATE whop_app SET client_id = '', client_secret = '' WHERE 1" \
  ./internal/panel '^TestDisconnectingTheKeyStoreLeavesTheOtherStores$'
control "stores: a business selling through the app takes no key" internal/panel/whop.go \
  'case ok && app.Via != whopViaKey:' \
  'case ok && app.Via != whopViaKey && false:' \
  ./internal/panel '^TestDisconnectingTheKeyStoreLeavesTheOtherStores$'
control "stores: one key store at most" internal/panel/auth.go \
  "CREATE UNIQUE INDEX whop_stores_one_key ON whop_stores(via) WHERE via = 'key';" \
  "CREATE INDEX whop_stores_one_key ON whop_stores(via) WHERE via = 'key';" \
  ./internal/panel '^TestTheMigrationMakesTheStoreTheKeyStore$'
control "stores: the migration gives the store it sold for its records" internal/panel/auth.go \
  "UPDATE whop_memberships SET store_id = COALESCE((SELECT account_id FROM whop_account), '');" \
  '' \
  ./internal/panel '^TestTheMigrationMakesTheStoreTheKeyStore$'
control "stores: a kick hurries its own store's pass alone" internal/panel/whop_customers.go \
  'if only == nil || only[st.ID] {' \
  'if true {' \
  ./internal/panel '^TestAKickHurriesItsOwnStoresPass$'
control "stores: the key store's delivery kicks the key store alone" internal/panel/whop_customers.go \
  's.kickWhopStore(account)' \
  's.kickWhop()' \
  ./internal/panel '^TestAKickHurriesItsOwnStoresPass$'
control "stores: an app store whose grant is gone changes nothing" internal/panel/whop_customers.go \
  'if problem := whopGrantProblem(ctx, c, st); problem != "" {' \
  'if problem := whopGrantProblem(ctx, c, st); false && problem != "" {' \
  ./internal/panel '^TestAStoreWhoseGrantIsGoneChangesNothing$'
control "stores: a read the app's grant lacks is why a store changes nothing" internal/panel/whop_stores.go \
  'if slices.Contains(whopStoreReads, a) {' \
  'if false && slices.Contains(whopStoreReads, a) {' \
  ./internal/panel '^TestAStoreWhoseGrantIsGoneChangesNothing$'
control "stores: a grant that can't be checked changes nothing" internal/panel/whop_stores.go \
  "return \"Playkeeper couldn't check the Playkeeper Cloud app's grant on this store, so nothing changed here: \" + whopProblem(err)" \
  'return ""' \
  ./internal/panel '^TestAStoreWhoseGrantIsGoneChangesNothing$'
control "stores: a store that needed a look is read again once it can be" internal/panel/whop_customers.go \
  'SET problem = ?, synced_at = 0, polled_at = 0 WHERE' \
  'SET problem = ?, polled_at = 0 WHERE' \
  ./internal/panel '^TestAStoreWhoseGrantIsGoneChangesNothing$'
control "stores: a store that needed a look reads every membership again once it can" internal/panel/whop_customers.go \
  'SET problem = ?, synced_at = 0, polled_at = 0 WHERE' \
  'SET problem = ?, synced_at = 0 WHERE' \
  ./internal/panel '^TestAStoreWhoseGrantIsGoneChangesNothing$'
control "stores: an app store waits for the app's key" internal/panel/whop_stores.go \
  'if key == "" {' \
  'if false {' \
  ./internal/panel '^TestAnAppStoreWaitsForTheAppsKey$'

# The Playkeeper Cloud app (the hosted blueprint's 1.4,
# internal/panel/whop_app.go): its webhook keeps each event for the app
# store of the business it names and no other, an app store is read at once
# when the webhook comes and every ten minutes with it, its owner comes from
# one of its products, and the app's key never shows. (1.1's "an app store
# whose grant is gone changes nothing" guards unapproved businesses.)
control "app webhook: an event is kept only for an app store" internal/panel/whop_app.go \
  'if !ok || st.Via != whopViaApp {' \
  'if !ok {' \
  ./internal/panel '^TestTheAppsWebhookKeepsEachEventForItsAppStore$'
control "app webhook: an event is kept for the business it names" internal/panel/whop_app.go \
  's.whopStoreByID(r.Context(), ev.AccountID)' \
  's.whopStoreByID(r.Context(), "biz_other")' \
  ./internal/panel '^TestTheAppsWebhookKeepsEachEventForItsAppStore$'
control "app webhook: a delivery hurries its own store's pass alone" internal/panel/whop_app.go \
  's.kickWhopStore(st.ID)' \
  's.kickWhop()' \
  ./internal/panel '^TestTheAppsWebhookKeepsEachEventForItsAppStore$'
control "app webhook: an app store is read at once when it comes" internal/panel/whop_app.go \
  'case st.PolledAt.Before(app.HookedAt):' \
  'case false:' \
  ./internal/panel '^TestAnAppStoreIsReadAtOnceWhenTheAppsWebhookComesThenEveryTenMinutes$'
control "app webhook: with it, an app store is read every ten minutes" internal/panel/whop_app.go \
  'return whopPollEvery' \
  'return whopPollUnhooked' \
  ./internal/panel '^TestAnAppStoreIsReadAtOnceWhenTheAppsWebhookComesThenEveryTenMinutes$'
control "app key: Settings shows its ending alone" internal/panel/whop_app.go \
  'KeyEnding: whop.Ending(app.Key)' \
  'KeyEnding: app.Key' \
  ./internal/panel '^TestOnlyTheOwnerSetsTheAppsKeyAndOnlyItsEndingShows$'
control "app key: the audit log keeps its ending alone" internal/panel/whop_app.go \
  'return what + ", ending " + whop.Ending(secret)' \
  'return what + ", ending " + secret' \
  ./internal/panel '^TestOnlyTheOwnerSetsTheAppsKeyAndOnlyItsEndingShows$'
control "app stores: the owner comes from one of the business's products" internal/whop/members.go \
  '"/products/"+url.PathEscape(ps[0].ID)' \
  '"/accounts/"+url.PathEscape(accountID)' \
  ./internal/panel '^TestAStoresMessagesGoOutInItsOwnChats$'

# Whop's user tokens (the hosted blueprint's 2.1, internal/whop/usertoken.go):
# a seller's page and its calls are taken only with a token Whop signed for
# this app, for a user, and not expired, and Whop's keys aren't read at
# every request.
control "user tokens: only Whop's signature" internal/whop/usertoken.go \
  'if ecdsa.Verify(k, sum[:], r, s) {' \
  'if ecdsa.Verify(k, sum[:], r, s) || true {' \
  ./internal/whop '^TestUserTokensAreWhopsForTheAppAndUnexpired$'
control "user tokens: only for this app" internal/whop/usertoken.go \
  'aud != app || app == "" || ' \
  '' \
  ./internal/whop '^TestUserTokensAreWhopsForTheAppAndUnexpired$'
control "user tokens: only unexpired" internal/whop/usertoken.go \
  'claims.Exp == nil || !now.Before(time.Unix(int64(*claims.Exp), 0))' \
  'claims.Exp == nil' \
  ./internal/whop '^TestUserTokensAreWhopsForTheAppAndUnexpired$'
control "user tokens: only Whop's proxy issues them" internal/whop/usertoken.go \
  'claims.Iss != userTokenIssuer || ' \
  '' \
  ./internal/whop '^TestUserTokensAreWhopsForTheAppAndUnexpired$'
control "user tokens: only ES256" internal/whop/usertoken.go \
  'head.Alg != "ES256"' \
  'false' \
  ./internal/whop '^TestUserTokensAreWhopsForTheAppAndUnexpired$'
control "user tokens: Whop's keys aren't read at every request" internal/whop/usertoken.go \
  ' && (u.triedAt.IsZero() || now.Sub(u.triedAt) >= keysCooldown)' \
  '' \
  ./internal/whop '^TestUserTokensFollowWhopsKeysWithoutAskingAtEveryRequest$'
control "user tokens: a read of Whop's keys outlives the request that started it" internal/whop/usertoken.go \
  'context.WithoutCancel(ctx)' \
  'ctx' \
  ./internal/whop '^TestAReadOfWhopsKeysOutlivesTheRequestThatStartedIt$'

# Playkeeper Cloud's ready server (internal/panel/readyserver.go): a
# customer is told once that their server is ready, or being set up.
control "ready server: ready is said once" internal/panel/readyserver.go \
  'case toldReady != 0:' \
  'case false:' \
  ./internal/panel '^TestACustomerIsToldOnceTheirServerIsReady$'
control "ready server: a ready message is kept only once it's sent" internal/panel/readyserver.go \
  'if err := s.notifier.Notify(ctx, cust, CustomerMessage{Kind: messageReady, Text: s.readyText(ctx, cust)}); err != nil {' \
  'if err := s.notifier.Notify(ctx, cust, CustomerMessage{Kind: messageReady, Text: s.readyText(ctx, cust)}); false && err != nil {' \
  ./internal/panel '^TestACustomerIsToldOnceTheirServerIsReady$'
control "ready server: a start whose message wasn't sent says so" internal/panel/customers.go \
  'if err := s.tellPlaced(ctx, cust, info.UserID, placed); err != nil {' \
  'if err := s.tellPlaced(ctx, cust, info.UserID, placed); false && err != nil {' \
  ./internal/panel '^TestACustomerIsToldOnceTheirServerIsReady$'
control "ready server: being set up is said once a wait" internal/panel/readyserver.go \
  'case toldWaiting == 0:' \
  'case true:' \
  ./internal/panel '^TestACustomerWaitingForRoomIsToldAndStartedWhenRoomAppears$'
control "ready server: a customer with no home machine waits" internal/panel/readyserver.go \
  'return err != nil || machineID == ""' \
  'return err != nil' \
  ./internal/panel '^TestACustomerWaitingForRoomIsToldAndStartedWhenRoomAppears$'
control "ready server: a waiting customer creates no server" internal/panel/server.go \
  'if err := s.waitingRefusal(r.Context(), acct); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/panel '^TestACustomerWaitingForRoomIsToldAndStartedWhenRoomAppears$'
control "ready server: the dashboard says a customer is waiting" internal/panel/server.go \
  'WaitingForRoom: s.customerWaiting(context.Background(), a), ' \
  '' \
  ./internal/panel '^TestACustomerWaitingForRoomIsToldAndStartedWhenRoomAppears$'
control "ready server: only an active customer is told it's ready" internal/panel/customers.go \
  'if info.State == CustomerActive {' \
  'if true {' \
  ./internal/panel '^TestOnlyAnActiveCustomerIsToldTheirServerIsReady$'
control "ready server: a paused customer waiting gets no server" internal/panel/readyserver.go \
  'if CustomerState(state) != CustomerActive {' \
  'if false {' \
  ./internal/panel '^TestOnlyAnActiveCustomerIsToldTheirServerIsReady$'
webcontrol "ready server: Home says a waiting customer's server is being set up" web/src/pages/home.tsx \
  'const waiting = !!ws.me.access.waitingForRoom' \
  'const waiting = false' \
  src/pages/pages.test.tsx 'being set up while it waits for room'

# Pausing a customer whose plan ended (internal/panel/pausing.go): their
# servers stop, and they may only look and download until they renew.
control "pausing: a paused customer only looks, downloads and looks after their account" internal/panel/workspace.go \
  'case a.Customer == CustomerPaused && !pausedMay[act]:' \
  'case false:' \
  ./internal/panel '^TestAPausedCustomerSeesTheirServersButRunsNothing$'
control "pausing: a suspended customer does nothing" internal/panel/workspace.go \
  'case a.Customer == CustomerSuspended:' \
  'case false:' \
  ./internal/panel '^TestNothingABillingProviderDoesLiftsASuspension$'
control "pausing: a paused customer's servers stop" internal/panel/pausing.go \
  's.stopCustomerServers(ctx, info.UserID, cust.Provider)' \
  '_ = info.UserID' \
  ./internal/panel '^TestAPausedCustomerSeesTheirServersButRunsNothing$'
control "pausing: a paused customer's tokens are revoked" internal/panel/pausing.go \
  's.revokeAccountTokens(info.UserID, cust.Provider, "their plan ended")' \
  '_ = info.UserID' \
  ./internal/panel '^TestAPausedCustomerSeesTheirServersButRunsNothing$'
control "pausing: only an active customer is paused, once" internal/panel/pausing.go \
  'case CustomerActive:' \
  'case CustomerActive, CustomerPaused:' \
  ./internal/panel '^TestAPausedCustomerSeesTheirServersButRunsNothing$'
control "pausing: a paused customer is told until when" internal/panel/pausing.go \
  'if err := s.notifier.Notify(ctx, cust, CustomerMessage{Kind: messagePaused, Text: pausedText(until)}); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/panel '^TestAPausedCustomerSeesTheirServersButRunsNothing$'
control "pausing: renewing brings a paused customer back" internal/panel/customers.go \
  'case info.State == CustomerPaused:' \
  'case false:' \
  ./internal/panel '^TestAPausedCustomerSeesTheirServersButRunsNothing$'
control "pausing: the dashboard says until when" internal/panel/server.go \
  ' PausedUntil: s.pausedUntil(a),' \
  '' \
  ./internal/panel '^TestAPausedCustomerSeesTheirServersButRunsNothing$'
webcontrol "pausing: Home tells a paused customer their plan ended" web/src/pages/home.tsx \
  "if (access.pausedUntil) return <TwoLineNotice tone=\"warning\" title={t('home.pausedTitle')} body={t('home.pausedBody', { date: formatLongDate(access.pausedUntil) })} />" \
  '' \
  src/pages/pages.test.tsx 'tells a paused customer their plan has ended'
webcontrol "pausing: an empty Home tells a paused customer their plan ended" web/src/pages/home.tsx \
  'const paused = deleted || !!ws.me.access.pausedUntil' \
  'const paused = deleted' \
  src/pages/pages.test.tsx 'with no servers that their plan has ended'
control "pausing: a paused customer makes no new token" internal/panel/tokens.go \
  'if sess.Access.Customer == CustomerPaused {' \
  'if false {' \
  ./internal/panel '^TestAPausedCustomerSeesTheirServersButRunsNothing$'
control "pausing: only an active customer waits for room" internal/panel/readyserver.go \
  'if a.Customer != CustomerActive {' \
  'if a.Customer == "" {' \
  ./internal/panel '^TestAPausedCustomerSeesTheirServersButRunsNothing$'
control "pausing: pausing sends the machines the hold at once" internal/panel/pausing.go \
  's.kickDiskLimits()' \
  '' \
  ./internal/panel '^TestAPausedCustomersServerIsHeldForWhoeverTheyShareItWith$'
control "pausing: a paused customer's limit holds their servers" internal/panel/disklimits.go \
  'CPUMilliPerGB: cpuMilliPerGB, Hold: in.holds[uid]}' \
  'CPUMilliPerGB: cpuMilliPerGB}' \
  ./internal/panel '^TestAPausedCustomersServerIsHeldForWhoeverTheyShareItWith$'
control "pausing: a suspended customer's hold says so" internal/panel/disklimits.go \
  'if CustomerState(state) == CustomerSuspended {' \
  'if false {' \
  ./internal/panel '^TestAPausedCustomersServerIsHeldForWhoeverTheyShareItWith$'
control "pausing: others may not run or change a paused customer's server" internal/panel/pausing.go \
  'return errServerPaused' \
  'return nil' \
  ./internal/panel '^TestAPausedCustomersServerIsHeldForWhoeverTheyShareItWith$'
control "pausing: others may only look at a suspended customer's server" internal/panel/pausing.go \
  'return errServerSuspended' \
  'return nil' \
  ./internal/panel '^TestAPausedCustomersServerIsHeldForWhoeverTheyShareItWith$'
control "pausing: others may still look at a held server" internal/panel/pausing.go \
  'if serverID == "" || act == actView || a.owner() || a.Servers.All {' \
  'if serverID == "" || a.owner() || a.Servers.All {' \
  ./internal/panel '^TestAPausedCustomersServerIsHeldForWhoeverTheyShareItWith$'
control "pausing: whoever runs the Playkeeper still looks after a held server" internal/panel/pausing.go \
  'if serverID == "" || act == actView || a.owner() || a.Servers.All {' \
  'if serverID == "" || act == actView {' \
  ./internal/panel '^TestAPausedCustomersServerIsHeldForWhoeverTheyShareItWith$'
control "pausing: permitOn asks whether the server is held" internal/panel/workspace.go \
  'return s.heldRefusal(a, act, serverID)' \
  'return nil' \
  ./internal/panel '^TestAPausedCustomersServerIsHeldForWhoeverTheyShareItWith$'
control "pausing: every dashboard route asks whether its server is held" internal/panel/server.go \
  'if err := s.permitOn(acct, rt.Act, r.PathValue("id")); err != nil {' \
  'if err := permit(acct, rt.Act, r.PathValue("id")); err != nil {' \
  ./internal/panel '^TestAPausedCustomersServerIsHeldForWhoeverTheyShareItWith$'
control "pausing: a restore asks whether its server is held" internal/panel/team.go \
  'if err := s.permitOn(sess.Access, act, p.ServerID); err != nil {' \
  'if err := permit(sess.Access, act, p.ServerID); err != nil {' \
  ./internal/panel '^TestAPausedCustomersServerIsHeldForWhoeverTheyShareItWith$'
control "pausing: a world import asks whether its server is held" internal/panel/worldimports.go \
  'if err := s.permitOn(sess.Access, need, imp.ServerID); err != nil {' \
  'if err := permit(sess.Access, need, imp.ServerID); err != nil {' \
  ./internal/panel '^TestAPausedCustomersServerIsHeldForWhoeverTheyShareItWith$'
control "pausing: a token's tools ask whether their server is held" internal/panel/mcp.go \
  'return mcpRefusal(act, b.s.heldRefusal(account, action(act), id))' \
  'return mcpRefusal(act, nil)' \
  ./internal/panel '^TestAPausedCustomersServerIsHeldForWhoeverTheyShareItWith$'

# Suspending a customer or a store (internal/panel/suspension.go, task 3.2
# of the hosted blueprint): only the owner suspends, with a reason; a
# suspended account's servers stop and its tokens and sessions go, while its
# plan goes on underneath until the owner lifts it; a suspended store sells
# nothing and suspends its own customers alone.
control "suspending: only the owner suspends" internal/panel/workspace.go \
  'actManageTeam:       invites.RoleAdmin,' \
  'actManageTeam:       invites.RoleAdmin, actSuspendCustomers: invites.RoleAdmin,' \
  ./internal/panel '^TestOnlyTheOwnerSuspendsACustomerWithAReason$'
control "suspending: a suspension says why" internal/panel/suspension.go \
  'if reason == "" || len(reason) > maxSuspendReason || !printable(reason) {' \
  'if false {' \
  ./internal/panel '^TestOnlyTheOwnerSuspendsACustomerWithAReason$'
control "suspending: a suspended customer's servers stop" internal/panel/suspension.go \
  's.stopCustomerServers(ctx, userID, actor)' \
  '_ = actor' \
  ./internal/panel '^TestASuspendedCustomerCanDoNothingUntilTheOwnerLiftsIt$'
control "suspending: a suspended customer's tokens are revoked" internal/panel/suspension.go \
  's.revokeAccountTokens(userID, actor, "their account was suspended")' \
  '_ = actor' \
  ./internal/panel '^TestASuspendedCustomerCanDoNothingUntilTheOwnerLiftsIt$'
control "suspending: a suspended customer is signed out" internal/panel/suspension.go \
  's.deleteUserSessions(userID)' \
  '_ = userID' \
  ./internal/panel '^TestASuspendedCustomerCanDoNothingUntilTheOwnerLiftsIt$'
control "suspending: machines hold a suspended customer's servers at once" internal/panel/suspension.go \
  's.kickDiskLimits()' \
  '_ = s' \
  ./internal/panel '^TestASuspendedCustomerCanDoNothingUntilTheOwnerLiftsIt$'
control "suspending: suspending again changes nothing" internal/panel/suspension.go \
  'case withStore && store || !withStore && self:' \
  'case false:' \
  ./internal/panel '^TestASuspendedCustomerCanDoNothingUntilTheOwnerLiftsIt$'
control "suspending: a lifted customer is told" internal/panel/suspension.go \
  'if err := s.notifier.Notify(ctx, cust, msg); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/panel '^TestASuspendedCustomerCanDoNothingUntilTheOwnerLiftsIt$'
control "suspending: a plan that ends while suspended waits for the lift" internal/panel/pausing.go \
  'return s.pauseUnderSuspension(ctx, cust, info, reason)' \
  'return nil' \
  ./internal/panel '^TestASuspendedCustomersPlanGoesOnUnderneath$'
control "suspending: a plan that starts again while suspended waits for the lift" internal/panel/customers.go \
  'err = s.resumeUnderSuspension(ctx, cust, info)' \
  'err = nil' \
  ./internal/panel '^TestASuspendedCustomersPlanGoesOnUnderneath$'
control "suspending: lifting leaves a customer whose plan ended paused" internal/panel/suspension.go \
  'if pausedAt != 0 {' \
  'if false {' \
  ./internal/panel '^TestASuspendedCustomersPlanGoesOnUnderneath$'
control "suspending: lifting gives a paused customer a fresh grace period" internal/panel/suspension.go \
  'until, msg = t.UnixMilli(), CustomerMessage{Kind: messagePaused, Text: pausedText(t)}' \
  'msg = CustomerMessage{Kind: messagePaused, Text: pausedText(t)}' \
  ./internal/panel '^TestASuspendedCustomersPlanGoesOnUnderneath$'
control "suspending: lifting one suspension leaves the other" internal/panel/suspension.go \
  'if self && store || CustomerState(state) != CustomerSuspended {' \
  'if CustomerState(state) != CustomerSuspended {' \
  ./internal/panel '^TestTwoSuspensionsAreLiftedApart$'
control "suspending: a store suspends its own customers alone" internal/panel/suspension.go \
  'SELECT user_id FROM customers WHERE provider = ? AND store = ? ORDER BY user_id' \
  'SELECT user_id FROM customers WHERE provider = ? AND (store = ? OR 1) ORDER BY user_id' \
  ./internal/panel '^TestSuspendingAStoreSuspendsItsOwnCustomersAlone$'
control "suspending: a store's customers are suspended with it" internal/panel/suspension.go \
  'did, err := s.suspendCustomer(ctx, id, true, actor, reason)' \
  'did, err := id < 0, error(nil)' \
  ./internal/panel '^TestSuspendingAStoreSuspendsItsOwnCustomersAlone$'
control "suspending: lifting a store lifts its customers" internal/panel/whop_customers.go \
  's.liftWithStore(ctx, st)' \
  '_ = st' \
  ./internal/panel '^TestSuspendingAStoreSuspendsItsOwnCustomersAlone$'
control "suspending: a lifted store's customers wait for its memberships to be read" internal/panel/whop_customers.go \
  'if read == nil {' \
  'if true {' \
  ./internal/panel '^TestALiftedStoresCustomersComeBackAsTheirPlansSay$'
control "suspending: a customer whose last call failed waits to be lifted" internal/panel/suspension.go \
  'pending[wc.WhopUserID] = wc.Unconfirmed > 0 || wc.NextTryAt > now' \
  'pending[wc.WhopUserID] = wc.Unconfirmed > 0 || now < 0' \
  ./internal/panel '^TestALiftedStoresCustomersComeBackAsTheirPlansSay$'
control "suspending: a customer whose new membership isn't confirmed waits to be lifted" internal/panel/suspension.go \
  'pending[wc.WhopUserID] = wc.Unconfirmed > 0 || wc.NextTryAt > now' \
  'pending[wc.WhopUserID] = wc.NextTryAt > now' \
  ./internal/panel '^TestALiftedStoresCustomersComeBackAsTheirPlansSay$'
control "suspending: only an app store is suspended" internal/panel/suspension.go \
  'case st.Via != whopViaApp:' \
  'case false:' \
  ./internal/panel '^TestOnlyAnAppStoreCanBeSuspended$'
control "suspending: a suspended store's plans take no room" internal/panel/whop_stock.go \
  'AND st.suspended_at = 0 AND st.left_at = 0' \
  'AND st.left_at = 0' \
  ./internal/panel '^TestASuspendedStoreSellsNothing$'
control "suspending: a suspended store's pass starts nobody" internal/panel/whop_customers.go \
  'if left || !st.SuspendedAt.IsZero() {' \
  'if left {' \
  ./internal/panel '^TestASuspendedStoreSellsNothing$'
control "suspending: a suspended store's stock goes to 0" internal/panel/whop_customers.go \
  's.stopWhopSales(ctx, st.ID)' \
  '_ = st.ID' \
  ./internal/panel '^TestASuspendedStoreSellsNothing$'
control "suspending: a lifted store is read again at once" internal/panel/suspension.go \
  "SET suspended_at = 0, suspend_reason = '', synced_at = 0, polled_at = 0 WHERE" \
  "SET suspended_at = 0, suspend_reason = '' WHERE" \
  ./internal/panel '^TestASuspendedStoreSellsNothing$'
control "suspending: the Team page shows a customer's store" internal/panel/team.go \
  '&row.Store, &row.StoreName' \
  'new(string), new(string)' \
  ./internal/panel '^TestTheTeamPageShowsACustomersStoreAndSuspension$'
control "suspending: the Team page says who may suspend" internal/panel/team.go \
  'row.CanSuspend = permit(a, actSuspendCustomers, "") == nil' \
  'row.CanSuspend = true' \
  ./internal/panel '^TestTheTeamPageShowsACustomersStoreAndSuspension$'
control "suspending: a suspension from before stays the owner's own" internal/panel/auth.go \
  "UPDATE customers SET suspended_self = 1 WHERE state = 'suspended';" \
  '' \
  ./internal/panel '^TestTheMigrationKeepsASuspensionAsTheOwnersOwn$'
webcontrol "suspending: the Team page offers the owner Suspend" web/src/pages/team.tsx \
  '{m.canSuspend &&' \
  '{false &&' \
  src/pages/pages.test.tsx 'lets the owner suspend them with a reason or lift it'
webcontrol "suspending: a store's suspension says why" web/src/pages/whop-stores.tsx \
  'await post(path, { reason: reason.trim() })' \
  'await post(path, { reason })' \
  src/pages/pages.test.tsx 'lets the owner suspend one with a reason or lift it'

# A store leaving (internal/panel/leaving.go, task 3.2 of the hosted
# blueprint): only the Whop side's call makes a store leave; a store that
# left ends its own customers' plans from what the dashboard kept, whether
# or not Whop still answers for it, sells nothing and tells its customers
# while it can; added again, it's back and read again at once.
control "leaving: a store that left ends its customers' plans" internal/panel/whop_customers.go \
  's.endLeftStorePlans(ctx, st)' \
  '_ = st' \
  ./internal/panel '^TestOnlyTheWhopSidesCallMakesAStoreLeave$'
control "leaving: lifting a store that left leaves its customers paused" internal/panel/whop_customers.go \
  'if st.SuspendedAt.IsZero() {' \
  'if false {' \
  ./internal/panel '^TestLiftingAStoreThatLeftLeavesItsCustomersPaused$'
control "leaving: an unconfirmed membership doesn't hold up lifting a store that left" internal/panel/suspension.go \
  'pending[wc.WhopUserID] = !wc.Paused' \
  'pending[wc.WhopUserID] = wc.Unconfirmed > 0 || !wc.Paused' \
  ./internal/panel '^TestLiftingAStoreThatLeftLeavesItsCustomersPaused$'
control "leaving: a paused customer isn't paused again" internal/panel/leaving.go \
  'if wc.Applied == "" || wc.Paused || wc.NextTryAt > now {' \
  'if wc.Applied == "" || wc.NextTryAt > now {' \
  ./internal/panel '^TestOnlyTheWhopSidesCallMakesAStoreLeave$'
control "leaving: a customer's pause is kept" internal/panel/leaving.go \
  'if err := s.recordWhopCustomer(cust, wc.Applied, true, at); err != nil {' \
  'if err := s.recordWhopCustomer(cust, wc.Applied, false, at); err != nil {' \
  ./internal/panel '^TestOnlyTheWhopSidesCallMakesAStoreLeave$'
control "leaving: the pause says the store left" internal/panel/leaving.go \
  'reason := "their store left Playkeeper Cloud"' \
  'reason := "their Whop membership ended"' \
  ./internal/panel '^TestOnlyTheWhopSidesCallMakesAStoreLeave$'
control "leaving: leaving again changes nothing" internal/panel/leaving.go \
  'UPDATE whop_stores SET left_at = ?, left_why = ? WHERE store_id = ? AND left_at = 0' \
  'UPDATE whop_stores SET left_at = ?, left_why = ? WHERE store_id = ?' \
  ./internal/panel '^TestOnlyTheWhopSidesCallMakesAStoreLeave$'
control "leaving: only an app store leaves" internal/panel/leaving.go \
  'case st.Via != whopViaApp:' \
  'case false:' \
  ./internal/panel '^TestOnlyAnAppStoreLeaves$'
control "leaving: a store that left sells nothing" internal/panel/whop_stock.go \
  'AND st.left_at = 0' \
  '' \
  ./internal/panel '^TestAStoreThatLeftSellsNothingAndTellsItsCustomers$'
control "leaving: a store that left starts nobody" internal/panel/whop_customers.go \
  'if left || !st.SuspendedAt.IsZero() {' \
  'if !st.SuspendedAt.IsZero() {' \
  ./internal/panel '^TestAStoreThatLeftSellsNothingAndTellsItsCustomers$'
control "leaving: a store that left tells its customers while it can" internal/panel/whop_customers.go \
  's.sendWhopMessages(ctx, c, st)' \
  '_ = c' \
  ./internal/panel '^TestAStoreThatLeftSellsNothingAndTellsItsCustomers$'
control "leaving: the owner's list says a store left" internal/panel/suspension.go \
  'if !st.LeftAt.IsZero() {' \
  'if false {' \
  ./internal/panel '^TestAStoreThatLeftSellsNothingAndTellsItsCustomers$'
control "leaving: a store that left is back when added again" internal/panel/whop_stores.go \
  'return s.bringBackWhopStore(ctx, a)' \
  'return false, nil' \
  ./internal/panel '^TestAStoreThatLeftIsBackOnceAddedAgain$'
control "leaving: a store that's back is read again at once" internal/panel/leaving.go \
  'SET left_at = 0, left_why = '"''"', synced_at = 0, polled_at = 0,' \
  'SET left_at = 0, left_why = '"''"',' \
  ./internal/panel '^TestAStoreThatLeftIsBackOnceAddedAgain$'
webcontrol "leaving: the owner's list says a store left and why" web/src/pages/whop-stores.tsx \
  "if (s.leftAt) return s.leftWhy ? t('whop.stores.left', { why: s.leftWhy }) : t('whop.stores.leftNoWhy')" \
  '' \
  src/pages/pages.test.tsx 'lets the owner suspend one with a reason or lift it'
webcontrol "leaving: a store that left can't be suspended" web/src/pages/whop-stores.tsx \
  '!s.leftAt && (' \
  '(' \
  src/pages/pages.test.tsx 'lets the owner suspend one with a reason or lift it'

# Closing an app store (internal/panel/closing.go, for the hosted
# blueprint's 2.1 to 2.3): a new app store, and one back after leaving, are
# closed as not open yet; a closed store sells nothing and starts nobody,
# while its customers' plans still end; each reason opens it alone.
control "closing: a new app store is closed until its seller opens it" internal/panel/whop_stores.go \
  'a.ID, whopNotOpenYet, whopNotOpenYetWhy, now)' \
  'a.ID+"-x", whopNotOpenYet, whopNotOpenYetWhy, now)' \
  ./internal/panel '^TestANewAppStoreSellsNothingUntilItsSellerOpensIt$'
control "closing: a store back after leaving is closed until opened" internal/panel/leaving.go \
  'a.ID, whopNotOpenYet, whopNotOpenYetWhy, s.now().UnixMilli())' \
  'a.ID+"-x", whopNotOpenYet, whopNotOpenYetWhy, s.now().UnixMilli())' \
  ./internal/panel '^TestAStoreThatLeftIsBackOnceAddedAgain$'
control "closing: a closed store's plans take no room" internal/panel/whop_stock.go \
  'AND NOT EXISTS (SELECT 1 FROM whop_store_closures c WHERE c.store_id = st.store_id)' \
  '' \
  ./internal/panel '^TestANewAppStoreSellsNothingUntilItsSellerOpensIt$'
control "closing: a closed store's stock goes to 0" internal/panel/whop_customers.go \
  'if st.ClosedWhy != "" {' \
  'if false {' \
  ./internal/panel '^TestANewAppStoreSellsNothingUntilItsSellerOpensIt$'
control "closing: a closed store starts nobody" internal/panel/whop_customers.go \
  'case has && (wc.Applied == "" || wc.Paused) && st.ClosedWhy != "":' \
  'case false:' \
  ./internal/panel '^TestAClosedStoresCustomersGoOnButNobodyStarts$'
control "closing: a store opens for its own reason alone" internal/panel/closing.go \
  'DELETE FROM whop_store_closures WHERE store_id = ? AND closed_by = ?' \
  'DELETE FROM whop_store_closures WHERE store_id = ? AND closed_by != ?' \
  ./internal/panel '^TestAStoreOpensOnceNoReasonHoldsItClosed$'
control "closing: only an app store is opened or closed" internal/panel/closing.go \
  'case st.Via != whopViaApp:' \
  'case st.Via == "none":' \
  ./internal/panel '^TestAStoreOpensOnceNoReasonHoldsItClosed$'
control "closing: a reason has a proper name" internal/panel/closing.go \
  'if !reClosedBy.MatchString(by) || why == "" {' \
  'if why == "" {' \
  ./internal/panel '^TestAStoreOpensOnceNoReasonHoldsItClosed$'
control "closing: the owner's list says why a store is closed" internal/panel/suspension.go \
  ', ClosedWhy: st.ClosedWhy}' \
  '}' \
  ./internal/panel '^TestANewAppStoreSellsNothingUntilItsSellerOpensIt$'
webcontrol "closing: the owner's list says a store isn't open yet" web/src/pages/whop-stores.tsx \
  "if (s.closedWhy) return t('whop.stores.closed', { why: s.closedWhy })" \
  '' \
  src/pages/pages.test.tsx 'lets the owner suspend one with a reason or lift it'
control "mcp tools: a tool on one server asks about that server" internal/mcptools/tools.go \
  'if err := access.onServer(s.act, c.server.ID); err != nil {' \
  'if err := access.onServer(s.act, c.server.ID); false && err != nil {' \
  ./internal/mcptools '^TestEachToolOnAServerAsksAboutThatServer$'

# A disk limit's hold (internal/agent/disklimits.go): the machine starts a
# held customer's servers for nobody.
control "disk limits: a held server doesn't start" internal/agent/disklimits.go \
  'if l := s.diskLimitOf(s.id); l != nil && l.Hold != "" {' \
  'if l := s.diskLimitOf(s.id); l != nil && false {' \
  ./internal/agent '^TestAHeldServerDoesntStart$'
control "disk limits: every start asks about the hold" internal/agent/lifecycle.go \
  'if err := s.holdRefusal(); err != nil {' \
  'if err := s.holdRefusal(); false && err != nil {' \
  ./internal/agent '^TestAHeldServerDoesntStart$'
control "disk limits: the hold is kept" internal/agent/disklimits.go \
  'CPUMilliPerGB: l.CPUMilliPerGB, Hold: l.Hold})' \
  'CPUMilliPerGB: l.CPUMilliPerGB})' \
  ./internal/agent '^TestAHeldServerDoesntStart$'
control "disk limits: a hold that changes alone is a change" internal/agent/disklimits.go \
  'x.CPUMilliPerGB == y.CPUMilliPerGB && x.Hold == y.Hold' \
  'x.CPUMilliPerGB == y.CPUMilliPerGB' \
  ./internal/agent '^TestAHeldServerDoesntStart$'
control "disk limits: a hold's reason is bounded" internal/agent/disklimits.go \
  'case len(l.Hold) > maxDiskLimitHold || ' \
  'case ' \
  ./internal/agent '^TestAHeldServerDoesntStart$'
control "disk limits: a hold's reason is printable" internal/agent/disklimits.go \
  'func(r rune) bool { return !unicode.IsPrint(r) }' \
  'func(r rune) bool { return false && !unicode.IsPrint(r) }' \
  ./internal/agent '^TestAHeldServerDoesntStart$'

# Kept backups (internal/agent/keptbackups.go): a deleted server's final
# backup, kept for a while after it.
control "kept backups: a delete asks whether to keep a final backup" internal/agent/handlers.go \
  'keep, err := checkKeep(req)' \
  'keep, err := keepFinal{}, error(nil)' \
  ./internal/agent '^TestDeletingAServerKeepsAFinalBackup$'
control "kept backups: how long one is kept is checked" internal/agent/keptbackups.go \
  'case req.KeepFinalBackupDays < 1 || req.KeepFinalBackupDays > maxKeepDays:' \
  'case false:' \
  ./internal/agent '^TestDeletingAServerKeepsAFinalBackup$'
control "kept backups: a kept backup's label is checked" internal/agent/keptbackups.go \
  'case req.KeptFor != "" && !reDiskLimitID.MatchString(req.KeptFor):' \
  'case false:' \
  ./internal/agent '^TestDeletingAServerKeepsAFinalBackup$'
control "kept backups: the final backup is a new one" internal/agent/keptbackups.go \
  '	b, err = s.finalArchive(actor, whole)' \
  '	err = errors.New("skipped")' \
  ./internal/agent '^TestDeletingAServerKeepsAFinalBackup$'
control "kept backups: a new final backup needs the room for it" internal/agent/keptbackups.go \
  'err == nil && free < need+minFreeAfterBackup {' \
  'err == nil && free < need*0 {' \
  ./internal/agent '^TestAServerNoBackupOfWhichCanBeKeptStays$'
control "kept backups: without room, the newest backup is kept" internal/agent/keptbackups.go \
  'for _, old := range list {' \
  'for _, old := range list[:0] {' \
  ./internal/agent '^TestAServerNoBackupOfWhichCanBeKeptStays$'
control "kept backups: only a backup that reads back is kept" internal/agent/keptbackups.go \
  'if b.Verified == nil || !*b.Verified {' \
  'if false {' \
  ./internal/agent '^TestAServerNoBackupOfWhichCanBeKeptStays$'
control "kept backups: a fresh final backup goes with a failed delete" internal/agent/servers.go \
  'if !moved {' \
  'if !moved && false {' \
  ./internal/agent '^TestADeletionThatFailsLeavesNoFinalBackup$'
control "kept backups: the kept archive stays when the server goes" internal/agent/servers.go \
  'if kept != nil && b.ID == kept.ID {' \
  'if false {' \
  ./internal/agent '^TestDeletingAServerKeepsAFinalBackup$'
control "kept backups: a label lists only its own" internal/agent/keptbackups.go \
  'if keptFor != "" {' \
  'if false {' \
  ./internal/agent '^TestDeletingAServerKeepsAFinalBackup$'
control "kept backups: a listed label is checked" internal/agent/keptbackups.go \
  'if keptFor != "" && !reDiskLimitID.MatchString(keptFor) {' \
  'if false {' \
  ./internal/agent '^TestAServerNoBackupOfWhichCanBeKeptStays$'
control "kept backups: one is kept until its time is up" internal/agent/keptbackups.go \
  'if now.Before(k.ExpiresAt) {' \
  'if now.Before(k.ExpiresAt) && false {' \
  ./internal/agent '^TestAKeptBackupGoesWhenItsTimeIsUp$'

# Deleting a paused customer's servers once the grace period ends
# (internal/panel/deletion.go), each with a final backup kept 30 days.
control "customer deletion: not before the grace period ends" internal/panel/deletion.go \
  '&& deleteAfter <= s.now().UnixMilli() &&' \
  '&&' \
  ./internal/panel '^TestOnlyALapsedCustomerIsDeleted$'
control "customer deletion: a suspended customer's servers stay" internal/panel/deletion.go \
  'return info, CustomerState(state) == CustomerPaused && ' \
  'return info, ' \
  ./internal/panel '^TestOnlyALapsedCustomerIsDeleted$'
control "customer deletion: servers deleted once aren't again" internal/panel/deletion.go \
  '&& deletedAt == 0, nil' \
  ', nil' \
  ./internal/panel '^TestOnlyALapsedCustomerIsDeleted$'
control "customer deletion: a deletion starts only while the customer is lapsed" internal/panel/deletion.go \
  '} else if !lapsed {' \
  '} else if !lapsed && false {' \
  ./internal/panel '^TestOnlyALapsedCustomerIsDeleted$'
control "customer deletion: each server's final backup is kept 30 days" internal/panel/deletion.go \
  'KeepFinalBackupDays: finalBackupDays,' \
  'KeepFinalBackupDays: 0,' \
  ./internal/panel '^TestALapsedCustomersServersGoWithAFinalBackupKept$'
control "customer deletion: under the account's label" internal/panel/deletion.go \
  'KeptFor: keptFor(userID)}' \
  'KeptFor: ""}' \
  ./internal/panel '^TestALapsedCustomersServersGoWithAFinalBackupKept$'
control "customer deletion: a deletion that failed isn't taken for done" internal/panel/deletion.go \
  'if op.Status != api.OpSucceeded {' \
  'if false {' \
  ./internal/panel '^TestADeletionThatFailsIsTriedAgain$'
control "customer deletion: a deleted server's creator is forgotten" internal/panel/deletion.go \
  'op.Error)
	}
	s.forgetCreatorServer(id)' \
  'op.Error)
	}' \
  ./internal/panel '^TestALapsedCustomersServersGoWithAFinalBackupKept$'
control "customer deletion: the home machine is freed" internal/panel/deletion.go \
  'if err := s.setHome(ctx, userID, ""); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/panel '^TestALapsedCustomersServersGoWithAFinalBackupKept$'
control "customer deletion: a customer who comes back is told their server is ready again" internal/panel/deletion.go \
  'told_ready = 0, told_waiting = 0, ' \
  '' \
  ./internal/panel '^TestARenewalKeepsWhatsLeft$'
control "customer deletion: the customer is told" internal/panel/deletion.go \
  'if err := s.notifier.Notify(ctx, info.customer, CustomerMessage{Kind: messageDeleted, Text: s.deletedText(ctx, info.customer, until, deleted > 0)}); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/panel '^TestALapsedCustomersServersGoWithAFinalBackupKept$'
control "customer deletion: the limits go out at once" internal/panel/deletion.go \
  's.kickDiskLimits()' \
  '' \
  ./internal/panel '^TestALapsedCustomersServersGoWithAFinalBackupKept$'
control "customer deletion: the dashboard stops giving a date once the servers are gone" internal/panel/pausing.go \
  '|| ms == 0 || deleted != 0 {' \
  '|| ms == 0 {' \
  ./internal/panel '^TestALapsedCustomersServersGoWithAFinalBackupKept$'
control "customer deletion: the dashboard says the servers are gone" internal/panel/server.go \
  'ServersDeleted: s.serversDeleted(a), ' \
  '' \
  ./internal/panel '^TestALapsedCustomersServersGoWithAFinalBackupKept$'
control "customer deletion: renewing clears it" internal/panel/pausing.go \
  "pause_reason = '', servers_deleted_at = 0, updated_at = ?" \
  "pause_reason = '', updated_at = ?" \
  ./internal/panel '^TestARenewalKeepsWhatsLeft$'
control "final backups: the machine is asked for the account's own" internal/panel/deletion.go \
  'url.Values{"keptFor": {keptFor(userID)}}' \
  'url.Values{}' \
  ./internal/panel '^TestACustomerDownloadsOnlyTheirOwnFinalBackups$'
control "final backups: another account's aren't listed" internal/panel/deletion.go \
  'return slices.DeleteFunc(list, func(k api.KeptBackup) bool { return k.KeptFor != keptFor(userID) }), m, nil' \
  'return list, m, nil' \
  ./internal/panel '^TestACustomerDownloadsOnlyTheirOwnFinalBackups$'
control "final backups: only the account's own download" internal/panel/deletion.go \
  'if !slices.ContainsFunc(list, func(k api.KeptBackup) bool { return k.ID == kid }) {' \
  'if len(list) < 0 {' \
  ./internal/panel '^TestACustomerDownloadsOnlyTheirOwnFinalBackups$'
webcontrol "customer deletion: Home offers the final backups" web/src/pages/home.tsx \
  '{ws.me.access.finalBackups && <FinalBackups className="mt-6 w-full max-w-[420px]" />}' \
  '' \
  src/pages/pages.test.tsx 'offers a customer whose servers were deleted their final backups'
webcontrol "customer deletion: Home still offers a renewed customer their final backups" web/src/pages/home.tsx \
  '{ws.me.access.finalBackups && <FinalBackups />}' \
  '' \
  src/pages/pages.test.tsx 'still offers a customer who renewed'
control "customer deletion: the machine keeping the final backups is recorded before a deletion starts" internal/panel/deletion.go \
  ', m.ID, userID); err != nil {' \
  ', m.ID, -userID); err != nil {' \
  ./internal/panel '^TestARenewalDuringADeletionKeepsItsFinalBackup$'
control "customer deletion: the dashboard says a machine keeps final backups" internal/panel/server.go \
  'FinalBackups: s.hasFinalBackups(a), ' \
  '' \
  ./internal/panel '^TestARenewalDuringADeletionKeepsItsFinalBackup$'
webcontrol "customer deletion: Home says the plan ended once the servers are gone" web/src/pages/home.tsx \
  'const paused = deleted || !!ws.me.access.pausedUntil' \
  'const paused = !!ws.me.access.pausedUntil' \
  src/pages/pages.test.tsx 'offers a customer whose servers were deleted their final backups'
control "new level.dat: offered next to the restore" internal/diagnose/crashrules.go \
  '	d.Fixes = append(d.Fixes, rebuild)
' \
  '' \
  ./internal/diagnose '^TestExplainCrashRecognisesEachCause$'
control "new level.dat: Minecraft 26.1's last line, which names no file, is recognised" internal/diagnose/crashrules.go \
  'Failed to load world data(?: from \S{1,300} and \S{1,300})?\. World files may be corrupted' \
  'Failed to load world data from \S{1,300} and \S{1,300}\. World files may be corrupted' \
  ./internal/diagnose '^TestExplainCrashRecognisesEachCause$'
control "new level.dat: a world whose level.dat can be read is refused" internal/agent/leveldat.go \
  'if _, err := worldimport.ParseLevel(b, levelDecode); err == nil {' \
  'if _, err := worldimport.ParseLevel(b, levelDecode); false && err == nil {' \
  ./internal/agent '^TestANewLevelDatIsRefusedWhereItDoesnHelp$'
control "new level.dat: the world is backed up before anything changes" internal/agent/leveldat.go \
  '		if err := s.backupOp(ctx, h, actor, "Before a new level.dat", false); err != nil {
			return err
		}
' \
  '' \
  ./internal/agent '^TestANewLevelDatKeepsTheWorldAndTakesItsSeedFromABackup$'
control "new level.dat: the world's seed goes into server.properties" internal/agent/leveldat.go \
  '	if seed != "" {
		cur, err := d.ReadProperties()' \
  '	if false && seed != "" {
		cur, err := d.ReadProperties()' \
  ./internal/agent '^TestANewLevelDatTakesTheSeedFromTheWorldSince26$'
control "new level.dat: nothing changes without the seed the owner was told it keeps" internal/agent/leveldat.go \
  'if req.SeedFrom != nil && keepsSeed(*req.SeedFrom) && !keepsSeed(p.from) {' \
  'if false && req.SeedFrom != nil && keepsSeed(*req.SeedFrom) && !keepsSeed(p.from) {' \
  ./internal/agent '^TestANewLevelDatIsntMadeWithoutTheSeedItSaidItKeeps$'
control "new level.dat: a refused one's fix says what a new level.dat does now" internal/agent/leveldat.go \
  '			s.refreshLevelFix(p)
' \
  '' \
  ./internal/agent '^TestANewLevelDatIsntMadeWithoutTheSeedItSaidItKeeps$'
control "new level.dat: a backup's seed is read before the fix says it keeps it" internal/agent/leveldat.go \
  '		if seed := s.archiveSeed(b, world); seed != "" {
			return seed, &b
		}' \
  '		if seed := s.archiveSeed(b, world); true {
			return cmp.Or(seed, "0"), &b
		}' \
  ./internal/agent '^TestANewLevelDatIsRefusedWhereItDoesnHelp$'
control "new level.dat: a backup from before a restore gives no seed" internal/agent/leveldat.go \
  's.worldPlacedAt().UnixMilli()' \
  'time.Time{}.UnixMilli()' \
  ./internal/agent '^TestANewLevelDatTakesTheSeedOnlyFromThisWorldsBackups$'
control "new level.dat: a 26.1 backup's world_gen_settings.dat gives the seed" internal/agent/leveldat.go \
  '	files := append(slices.Clone(gen), world+"/level.dat", world+"/level.dat_old")' \
  '	files := []string{world + "/level.dat", world + "/level.dat_old"}' \
  ./internal/agent '^TestANewLevelDatTakesTheSeedOnlyFromThisWorldsBackups$'
control "new level.dat: a backup is read no further than its world_gen_settings.dat" internal/agent/leveldat.go \
  '		return read[gen[0]] != nil || read[gen[1]] != nil' \
  '		return false' \
  ./internal/agent '^TestANewLevelDatTakesTheSeedOnlyFromThisWorldsBackups$'
control "new level.dat: a pass through a backup ends once it has enough" internal/backup/archive.go \
  '		if enough != nil && enough(out) {' \
  '		if false && enough != nil && enough(out) {' \
  ./internal/backup '^TestReadFilesStopsOncePastTheFiles$'
control "new level.dat: a server that started with another seed fails it" internal/agent/leveldat.go \
  '	if m[1] != seed {
		s.log.Warn' \
  '	if false {
		s.log.Warn' \
  ./internal/agent '^TestANewLevelDatChecksTheServerKeptTheSeed$'
control "new level.dat: a backup's seed is read without the rest of its archive" internal/backup/archive.go \
  '			if sorted && hdr.Name > last {' \
  '			if false && sorted && hdr.Name > last {' \
  ./internal/backup '^TestReadFilesStopsOncePastTheFiles$'
control "new level.dat: game rules kept in their own file since 26.1 don't reset" internal/agent/leveldat.go \
  'if !has("data/minecraft/game_rules.dat", own+"game_rules.dat") {' \
  'if true {' \
  ./internal/agent '^TestANewLevelDatTakesTheSeedFromTheWorldSince26$'
control "new level.dat: only an admin makes one" internal/panel/server.go \
  'smAs(actRestore, "POST", "/api/servers/{id}/world/rebuild-level", "/v1/servers/{id}/world/rebuild-level"),' \
  'smAs(actRunServers, "POST", "/api/servers/{id}/world/rebuild-level", "/v1/servers/{id}/world/rebuild-level"),' \
  ./internal/panel '^TestOnlyAnAdminMakesANewLevelDat$'
webcontrol "new level.dat: nothing is sent before the owner confirms in the dialog" web/src/pages/server/overview.tsx \
  "        case 'rebuild-level':
          setRebuild(plan)
          return" \
  "        case 'rebuild-level':
          await post(serverApi(s.id, '/world/rebuild-level'), { world: plan.world, start: true })
          break" \
  src/pages/pages.test.tsx 'makes a new level.dat only once the owner has read what resets'
webcontrol "new level.dat: the card warns when only server.properties has a seed" web/src/lib/crash.ts \
  "  if (seedFrom === 'world' || seedFrom === 'backup') return hint" \
  "  if (seedFrom) return hint" \
  web/src/lib/lib.test.ts 'says what a new level.dat resets, and whether new terrain will match'
webcontrol "new level.dat: the dialog keeps the seed only when it was found" web/src/components/app/rebuild-level.tsx \
  "  backup: 'rebuildLevel.seedBackup',
}" \
  "  backup: 'rebuildLevel.seedBackup',
  properties: 'rebuildLevel.seedWorld',
}" \
  src/pages/pages.test.tsx 'says plainly that new terrain'
webcontrol "new level.dat: the dialog sends what it said about the seed" web/src/components/app/rebuild-level.tsx \
  "{ world: plan.world, seedFrom: plan.seedFrom, start: true }" \
  "{ world: plan.world, start: true }" \
  src/pages/pages.test.tsx 'makes a new level.dat only once the owner has read what resets'
control "ticking entities: what crashes each time it ticks is named" internal/diagnose/crashrules.go \
  '	{(*crashCtx).tickingEntity, true},
' \
  '' \
  ./internal/diagnose '^TestExplainCrashNamesWhatCrashesEachTimeItTicks$'
control "ticking entities: no plain restart, which runs it again" internal/diagnose/crashticking.go \
  '	d.Fixes = []Action{fix}' \
  '	d.Fixes = []Action{fix, restartFix()}' \
  ./internal/diagnose '^TestExplainCrashNamesWhatCrashesEachTimeItTicks$'
control "ticking entities: only the entity at the exact place goes" internal/region/region.go \
  'len(pos) == 3 && math.Abs(v-pos[i]) > 0.0051' \
  'false && math.Abs(v-pos[i]) > 0.0051' \
  ./internal/region '^TestRemoveEntityTakesOnlyTheOneNamed$'
control "ticking entities: what rode it stays" internal/region/region.go \
  '					freed = append(freed, riders.Items...)
' \
  '' \
  ./internal/region '^TestRemoveEntityTakesOnlyTheOneNamed$'
control "ticking entities: the world is backed up before it changes" internal/agent/removeentity.go \
  '		if err := s.backupOp(ctx, h, actor, "Before removing the "+fix.label(), false); err != nil {
			return err
		}
' \
  '' \
  ./internal/agent '^TestRemovingWhatCrashedTakesOnlyThatOneOutOfTheWorld$'
control "ticking entities: nothing starts when it isn't there" internal/agent/removeentity.go \
  '	if _, err := s.planEntityFix(*sc, req); err != nil {
		writeError(w, err)
		return
	}' \
  '	if false {
		writeError(w, err)
		return
	}' \
  ./internal/agent '^TestRemovingWhatCrashedIsRefusedWhereItCantHelp$'
control "ticking entities: Paper's own Nether folder is looked in" internal/agent/removeentity.go \
  '	if own && paper != "" {' \
  '	if false && paper != "" {' \
  ./internal/agent '^TestRemovingWhatCrashedFindsItInEachLayout$'
control "ticking entities: Paper's own folder comes before a copy in the world folder" internal/agent/removeentity.go \
  '	if own && paper != "" {
		out = append(out, paper)
	}
	if world != "" {
		out = append(out, world)
	}' \
  '	if world != "" {
		out = append(out, world)
	}
	if own && paper != "" {
		out = append(out, paper)
	}' \
  ./internal/agent '^TestRemovingWhatCrashedLooksInTheWorldThatSavesIt$'
control "ticking entities: other types never look in Paper's folders" internal/agent/removeentity.go \
  '	level, own := s.levelName(sc), takesPlugins(sc)' \
  '	level, own := s.levelName(sc), true' \
  ./internal/agent '^TestRemovingWhatCrashedLooksInTheWorldThatSavesIt$'
control "ticking entities: a file without it doesn't end the search" internal/agent/removeentity.go \
  '		looked = append(looked, file)
' \
  '		looked = append(looked, file)
		break
' \
  ./internal/agent '^TestRemovingWhatCrashedLooksInTheWorldThatSavesIt$'
control "ticking entities: a world a plugin made isn't taken for this one" internal/agent/removeentity.go \
  '	if own && req.Level != "" && req.Level != level' \
  '	if false && own && req.Level != "" && req.Level != level' \
  ./internal/agent '^TestRemovingWhatCrashedLooksInTheWorldThatSavesIt$'
control "ticking entities: a data pack's dimension, a world of its own on Paper, is this one" internal/agent/removeentity.go \
  '&& req.Level != paperWorld(level, req.Dimension) {' \
  '&& req.Level != level+"_nether" && req.Level != level+"_the_end" {' \
  ./internal/agent '^TestRemovingWhatCrashedLooksInTheWorldThatSavesIt$'
control "ticking entities: a level that isn't a world's name is refused" internal/agent/removeentity.go \
  '	case req.Level != "" && !reLevelName.MatchString(req.Level):' \
  '	case false && req.Level != "" && !reLevelName.MatchString(req.Level):' \
  ./internal/agent '^TestRemovingWhatCrashedLooksInTheWorldThatSavesIt$'
control "ticking entities: the crash report's world goes with the fix" internal/diagnose/crashticking.go \
  '		fix.Params["level"] = t.level' \
  '		_ = t.level' \
  ./internal/diagnose '^TestExplainCrashNamesWhatCrashesEachTimeItTicks$'
control "ticking entities: a dimension can't climb out of the world" internal/agent/removeentity.go \
  '		if part == "" || part == "." || part == ".." {' \
  '		if false && (part == "" || part == "." || part == "..") {' \
  ./internal/agent '^TestRemovingWhatCrashedIsRefusedWhereItCantHelp$'
control "ticking entities: a position rounded to the next block still names this one" internal/agent/removeentity.go \
  'if math.IsNaN(v) || v < b-0.01 || v > b+1.01 {' \
  'if math.IsNaN(v) || int(math.Floor(v)) != int(b) {' \
  ./internal/agent '^TestRemovingWhatCrashedIsRefusedWhereItCantHelp$'
control "ticking entities: a fix that can't work isn't offered" internal/agent/crash.go \
  '} else if _, err := s.planEntityFix(*sc, req); err != nil {' \
  '} else if _, err := s.planEntityFix(*sc, req); false && err != nil {' \
  ./internal/agent '^TestRemovingWhatCrashedIsRefusedWhereItCantHelp$'
control "ticking entities: only an admin takes something out of a world" internal/panel/server.go \
  'smAs(actRestore, "POST", "/api/servers/{id}/world/remove-entity", "/v1/servers/{id}/world/remove-entity"),' \
  'smAs(actRunServers, "POST", "/api/servers/{id}/world/remove-entity", "/v1/servers/{id}/world/remove-entity"),' \
  ./internal/panel '^TestOnlyAnAdminTakesSomethingOutOfAWorld$'
webcontrol "ticking entities: the dashboard names it and where" web/src/lib/crash.ts \
  "      return t('crash.ticking', { what: thingName(type), x, y, z })" \
  "      return t('crash.tickingPlain')" \
  web/src/lib/lib.test.ts 'names what crashes the server each time it ticks'
webcontrol "ticking entities: with nothing Playkeeper can do, starting again isn't what it recommends" web/src/lib/crash.ts \
  "    if (c.kind === 'ticking_entity') out.push(" \
  "    if (false) out.push(" \
  web/src/lib/lib.test.ts 'names what crashes the server each time it ticks'
control "client-only mods: a mod for players' games that stopped the server is recognised" internal/diagnose/crashrules.go \
  '	{(*crashCtx).clientOnly, true},
' \
  '' \
  ./internal/diagnose '^TestExplainCrashRecognisesEachCause$'
control "client-only mods: a client class the server started past is not blamed" internal/diagnose/crashaddons.go \
  'if _, started := c.consoleIn(reDone, last.idx+1, len(c.split)); started || c.errorAfter(last.idx) {' \
  'if _, started := c.consoleIn(reDone, last.idx+1, len(c.split)); false && started || c.errorAfter(last.idx) {' \
  ./internal/diagnose '^TestExplainCrashPassesOverAClientClassTheServerStartedPast$'
control "client-only mods: NeoForge's early window plugin names the mod" internal/diagnose/crashaddons.go \
  'if p, ok := c.consoleIn(reGraphicsPlugin, start-10, start+1); ok {' \
  'if p, ok := c.consoleIn(reGraphicsPlugin, start-10, start+1); false && ok {' \
  ./internal/diagnose '^TestExplainCrashRecognisesEachCause$'
control "client-only mods: a mod's own frames name it, past the loader's" internal/diagnose/crashaddons.go \
  'if jar := c.modJar(m[1], ""); jar != "" {' \
  'if jar := m[1]; jar != "" {' \
  ./internal/diagnose '^TestExplainCrashRecognisesEachCause$'
control "client-only mods: a library named after the loader isn't taken for the loader's frames" internal/diagnose/crashaddons.go \
  'm != nil && !loaderModules[m[1]] {' \
  'm != nil {' \
  ./internal/diagnose '^TestExplainCrashRecognisesEachCause$'
control "client-only mods: a later error that stopped the server wins over an earlier client class" internal/diagnose/crashaddons.go \
  'if _, started := c.consoleIn(reDone, last.idx+1, len(c.split)); started || c.errorAfter(last.idx) {' \
  'if _, started := c.consoleIn(reDone, last.idx+1, len(c.split)); started {' \
  ./internal/diagnose '^TestExplainCrashBlamesTheClientClassOnlyWhenItStoppedTheServer$'
webcontrol "client-only mods: the dashboard says the mod only runs in players' games" web/src/lib/crash.ts \
  "if (str(p, 'reason') !== 'client_only') return c.explanation" \
  'return c.explanation' \
  web/src/lib/lib.test.ts 'only runs in players'
control "repeating crashes: a crash that repeats at every start isn't restarted" internal/agent/lifecycle.go \
  '	if len(s.crashes) < maxCrashes && s.crash != nil && s.crash.Repeats {' \
  '	if false && len(s.crashes) < maxCrashes && s.crash != nil && s.crash.Repeats {' \
  ./internal/agent '^TestACrashThatRepeatsIsNotRestarted$'
control "repeating crashes: a start while the crash was explained moves on from it" internal/agent/lifecycle.go \
  '	if !wanted || s.runs != run {' \
  '	if !wanted {' \
  ./internal/agent '^TestAStartLetsGoOfACrashThatRepeats$'
control "repeating crashes: a stop while the crash was explained keeps it off" internal/agent/lifecycle.go \
  '		wanted := s.desired() == api.DesiredRunning' \
  '		wanted := desired == api.DesiredRunning' \
  ./internal/agent '^TestAStopWhileTheCrashIsExplainedKeepsItOff$'
control "repeating crashes: a start lets go of a crash that held the server" internal/agent/lifecycle.go \
  '	s.runs++
	s.repeats = false
' \
  '	s.runs++
' \
  ./internal/agent '^TestAStartLetsGoOfACrashThatRepeats$'
control "repeating crashes: automatic starts stay off after one" internal/agent/lifecycle.go \
  'return len(s.crashes) >= maxCrashes || s.repeats' \
  'return len(s.crashes) >= maxCrashes' \
  ./internal/agent '^TestACrashThatRepeatsIsNotRestarted$'
control "repeating crashes: a start that stops the same way each time isn't tried again" internal/agent/lifecycle.go \
  'if s.crash != nil && s.crash.Repeats && n < maxCrashes {' \
  'if false && s.crash != nil && s.crash.Repeats && n < maxCrashes {' \
  ./internal/agent '^TestAStartThatStopsTheSameWayEachTimeIsNotTriedAgain$'
control "repeating crashes: both level.dat files damaged repeats" internal/diagnose/crashrules.go \
  'Params: map[string]any{"file": "level.dat"}, Repeats: true,' \
  'Params: map[string]any{"file": "level.dat"},' \
  ./internal/diagnose '^TestExplainCrashRecognisesEachCause$'
control "repeating crashes: a mod for players' games repeats" internal/diagnose/crashaddons.go \
  '"class": class}, Repeats: true, Evidence:' \
  '"class": class}, Evidence:' \
  ./internal/diagnose '^TestExplainCrashRecognisesEachCause$'
control "repeating crashes: something that crashes each time it's ticked repeats" internal/diagnose/crashticking.go \
  'Params: map[string]any{"what": "entity"}, Repeats: true,' \
  'Params: map[string]any{"what": "entity"},' \
  ./internal/diagnose '^TestExplainCrashNamesWhatCrashesEachTimeItTicks$'
control "repeating crashes: Discord says it stays off without a restart" internal/discord/alerts.go \
  '		case e.Repeats:' \
  '		case false:' \
  ./internal/discord '^TestAlertEmbedsReadWell$'
control "repeating crashes: the alert isn't swallowed by an earlier crash's" internal/discord/alerts.go \
  '		if e.Repeats {
			return string(e.Kind) + ":repeats"
		}' \
  '' \
  ./internal/discord '^TestRepeatedAlertsAreThrottled$'
control "a third crash of a server meant to be off is no give-up either" internal/agent/lifecycle.go \
  'GaveUp: wanted && counted' \
  'GaveUp: counted' \
  ./internal/agent '^TestDiscordCrashOfAServerMeantToBeOffIsNoGiveUp$'
control "a server meant to be off doesn't say Playkeeper stopped restarting it" internal/agent/lifecycle.go \
  '	case wanted:
		s.lastError += fmt.Sprintf(" Playkeeper stopped restarting it' \
  '	default:
		s.lastError += fmt.Sprintf(" Playkeeper stopped restarting it' \
  ./internal/agent '^TestDiscordCrashOfAServerMeantToBeOffIsNoGiveUp$'

# Uploads for a new server counted against a named disk limit, as a
# creator's are (internal/agent/worldimports.go, handlers.go, backups.go),
# and every customer upload treated as hostile.
control "customer uploads: a new server's upload names a limit the machine has" internal/agent/worldimports.go \
  'if account = a.namedLimit(limit); account == nil {' \
  'if account = a.namedLimit(limit); account == nil && false {' \
  ./internal/agent '^TestAWorldForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: one open world upload per account" internal/agent/worldimports.go \
  'mine := account != nil && slices.ContainsFunc(' \
  'mine := false && account != nil && slices.ContainsFunc(' \
  ./internal/agent '^TestAWorldForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: a named upload's archives count against its limit" internal/agent/worldimports.go \
  'if err := a.namedLimitRefusal(ctx, imp.limit, size); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/agent '^TestAWorldForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: a named upload's unsent archives count as on their way" internal/agent/disklimits.go \
  ' || imp.serverID == "" && imp.limit == l.ID' \
  '' \
  ./internal/agent '^TestAWorldForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: creating a server holds its world against the limit" internal/agent/worldimports.go \
  'if done, err = a.holdNamedLimit(r.Context(), imp.limit, need); err != nil {' \
  'if done, err = func(bool) {}, error(nil); err != nil {' \
  ./internal/agent '^TestAWorldForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: a server made from a world joins the limit" internal/agent/worldimports.go \
  'if err := a.joinDiskLimit(imp.limit, s.id); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/agent '^TestAWorldForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: only an upload for a new server names a limit" internal/agent/worldimports.go \
  'case req.DiskLimit != "" && serverID != "":' \
  'case false:' \
  ./internal/agent '^TestAWorldForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: a world upload's limit is named properly" internal/agent/worldimports.go \
  'case req.DiskLimit != "" && !reDiskLimitID.MatchString(req.DiskLimit):' \
  'case false:' \
  ./internal/agent '^TestAWorldForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: only a restore for a new server names a limit" internal/agent/handlers.go \
  'case limit != "" && target != nil:' \
  'case false:' \
  ./internal/agent '^TestABackupForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: a restore upload's limit is named properly" internal/agent/handlers.go \
  'case limit != "" && !reDiskLimitID.MatchString(limit):' \
  'case false:' \
  ./internal/agent '^TestABackupForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: a backup's size is checked against the limit's room" internal/agent/handlers.go \
  'if err == nil && r.ContentLength > room {' \
  'if err == nil && false {' \
  ./internal/agent '^TestABackupForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: a backup is read no further than the limit's room" internal/agent/handlers.go \
  'most = min(most, room)' \
  '_ = room' \
  ./internal/agent '^TestABackupForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: a staged backup remembers its limit" internal/agent/backups.go \
  'f.DiskLimit, f.Preview.DiskLimit = limit, limit' \
  'f.Preview.DiskLimit = limit' \
  ./internal/agent '^TestABackupForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: an account stages one backup at a time" internal/agent/backups.go \
  'err == nil && o.DiskLimit == limit {' \
  'err == nil && o.DiskLimit == limit && false {' \
  ./internal/agent '^TestABackupForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: a staged backup's limit is read back" internal/agent/backups.go \
  'st.limit, st.preview.DiskLimit = s.DiskLimit, s.DiskLimit' \
  'st.preview.DiskLimit = s.DiskLimit' \
  ./internal/agent '^TestABackupForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: restoring a new server holds its world against the limit" internal/agent/handlers.go \
  'if done, err = a.holdNamedLimit(r.Context(), st.limit, unpackedBytes(st.manifest)); err != nil {' \
  'if done, err = func(bool) {}, error(nil); err != nil {' \
  ./internal/agent '^TestABackupForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: a restored new server joins the limit" internal/agent/handlers.go \
  'if err := a.joinDiskLimit(st.limit, s.id); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/agent '^TestABackupForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: a backup unpacks no further than its limit's room" internal/agent/backups.go \
  'lim.MaxTotalBytes = min(lim.MaxTotalBytes, room)' \
  '_ = room' \
  ./internal/agent '^TestACustomersBackupUploadIsTreatedAsHostile$'
control "customer uploads: a backup past its limit's room says so" internal/agent/backups.go \
  'if errors.Is(err, backup.ErrTooLarge) && room == lim.MaxTotalBytes {' \
  'if false {' \
  ./internal/agent '^TestACustomersBackupUploadIsTreatedAsHostile$'
control "customer uploads: a backup into a server unpacks no further than its limit's room" internal/agent/backups.go \
  'return a.namedLimitRoom(a.ctx, l.ID)' \
  'return -1, nil' \
  ./internal/agent '^TestACustomersBackupUploadIsTreatedAsHostile$'
control "customer uploads: an archive's total size refusal is told apart" internal/backup/archive.go \
  ', err: ErrTooLarge}' \
  '}' \
  ./internal/agent '^TestACustomersBackupUploadIsTreatedAsHostile$'
control "customer uploads: a restore claims the upload it applies" internal/agent/handlers.go \
  'release, err := a.claimStage(r.PathValue("id"))' \
  'release, err := func() {}, error(nil)' \
  ./internal/agent '^TestARestoreKeepsTheUploadItApplies$'
control "customer uploads: a claimed upload is in use" internal/agent/backups.go \
  'if a.stages.ids[id] {' \
  'if a.stages.ids[id] && false {' \
  ./internal/agent '^TestARestoreKeepsTheUploadItApplies$'
control "customer uploads: one restore at a time claims an upload" internal/agent/backups.go \
  'if a.stageInUse(id) {' \
  'if a.stageInUse(id) && false {' \
  ./internal/agent '^TestARestoreKeepsTheUploadItApplies$'
control "customer uploads: an upload a restore claimed isn't discarded" internal/agent/handlers.go \
  'busy := a.stageInUse(id)' \
  'busy := a.stageInUse(id) && false' \
  ./internal/agent '^TestARestoreKeepsTheUploadItApplies$'
control "customer uploads: a restore gives its upload up when it's over" internal/agent/handlers.go \
  '			defer release()' \
  '			defer func() {}()' \
  ./internal/agent '^TestARestoreKeepsTheUploadItApplies$'
control "customer uploads: a refused restore gives its upload up" internal/agent/handlers.go \
  'if !started {' \
  'if !started && false {' \
  ./internal/agent '^TestABackupForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: an upload whose restore left its journal isn't replaced" internal/agent/backups.go \
  'swapJournalFile)); !errors.Is(err, os.ErrNotExist) {' \
  'swapJournalFile)); !errors.Is(err, os.ErrNotExist) && false {' \
  ./internal/agent '^TestARestoreKeepsTheUploadItApplies$'
control "customer uploads: a replaced upload is deleted, not just moved aside" internal/agent/backups.go \
  'for _, dir := range gone {' \
  'for _, dir := range gone[:0] {' \
  ./internal/agent '^TestABackupForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: a discarded upload is deleted, not just moved aside" internal/agent/handlers.go \
  '		os.RemoveAll(aside)' \
  '		_ = aside' \
  ./internal/agent '^TestARestoreKeepsTheUploadItApplies$'
control "customer uploads: a backup staged for a new server counts against its limit" internal/agent/disklimits.go \
  'n := a.stagedFor(l.ID)' \
  'n := int64(0)' \
  ./internal/agent '^TestABackupForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: a newer backup is read into the room of the one it replaces" internal/agent/handlers.go \
  'room, err := a.roomReplacingStage(r.Context(), limit)' \
  'room, err := a.namedLimitRoom(r.Context(), limit)' \
  ./internal/agent '^TestABackupForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: a restore into a server isn't charged its archive besides its world" internal/agent/backups.go \
  '		if target == nil {
			room = max(room-n, 0)' \
  '		if true {
			room = max(room-n, 0)' \
  ./internal/agent '^TestACustomersBackupUploadIsTreatedAsHostile$'
control "customer uploads: the staged archive takes some of the room its world unpacks into" internal/agent/backups.go \
  'room = max(room-n, 0)' \
  'room = max(room, 0)' \
  ./internal/agent '^TestABackupForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: a newer backup unpacks into the room of the one it replaces" internal/agent/backups.go \
  'return a.roomReplacingStage(a.ctx, named)' \
  'return a.namedLimitRoom(a.ctx, named)' \
  ./internal/agent '^TestABackupForANewServerCountsAgainstItsDiskLimit$'
control "customer uploads: the replaced backup's room counts before the room is floored at nothing" internal/agent/disklimits.go \
  'return max(l.LimitBytes-a.limitUsed(rep, l)+staged, 0), nil' \
  'return max(l.LimitBytes-a.limitUsed(rep, l), 0) + staged, nil' \
  ./internal/agent '^TestABackupForANewServerCountsAgainstItsDiskLimit$'

# The machine answers DNS for the zone the dashboard sets, for port-free
# addresses (internal/dnszone, internal/agent/dns.go): authoritative only,
# and every query from the internet parsed with bounds.
control "dns: names outside the zone are refused" internal/dnszone/dnszone.go \
  'case !inside || qs.class != classIN:' \
  'case false && (!inside || qs.class != classIN):' \
  ./internal/dnszone '^TestRefusesWhatIsntItsZone$'
control "dns: the zone ends at a dot" internal/dnszone/dnszone.go \
  'strings.HasSuffix(qs.name, "."+zone)' \
  'strings.HasSuffix(qs.name, zone)' \
  ./internal/dnszone '^TestRefusesWhatIsntItsZone$'
control "dns: ANY is not implemented" internal/dnszone/dnszone.go \
  'case qs.qtype == typeANY:' \
  'case false:' \
  ./internal/dnszone '^TestRefusesWhatIsntItsZone$'
control "dns: a reply too large for UDP is truncated" internal/dnszone/dnszone.go \
  'if len(out) > max {' \
  'if false {' \
  ./internal/dnszone '^TestALargeReplyIsTruncated$'
control "dns: a query asks one question" internal/dnszone/dnszone.go \
  'if q[2]&0x80 != 0 || binary.BigEndian.Uint16(q[4:6]) != 1 {' \
  'if q[2]&0x80 != 0 {' \
  ./internal/dnszone '^TestMalformedQueries$'
control "dns: a label is at most 63 bytes" internal/dnszone/dnszone.go \
  'if n > 63 || i+1+n > len(q) || i+1+n-headerLen > 255 {' \
  'if i+1+n > len(q) || i+1+n-headerLen > 255 {' \
  ./internal/dnszone '^TestMalformedQueries$'
control "dns: a name is at most 255 bytes" internal/dnszone/dnszone.go \
  'if n > 63 || i+1+n > len(q) || i+1+n-headerLen > 255 {' \
  'if n > 63 || i+1+n > len(q) {' \
  ./internal/dnszone '^TestMalformedQueries$'
control "dns: a reply gets no answer" internal/dnszone/dnszone.go \
  'if len(q) < headerLen || q[2]&0x80 != 0 {' \
  'if len(q) < headerLen {' \
  ./internal/dnszone '^TestMalformedQueries$'
control "dns: only queries are answered" internal/dnszone/dnszone.go \
  'if opcode := q[2] >> 3 & 0x0f; opcode != 0 {' \
  'if opcode := q[2] >> 3 & 0x0f; false && opcode != 0 {' \
  ./internal/dnszone '^TestMalformedQueries$'
control "dns: a missing name is said to be missing" internal/dnszone/dnszone.go \
  'if !there {
			rcode = rcodeNXDomain' \
  'if false {
			rcode = rcodeNXDomain' \
  ./internal/dnszone '^TestMissingNamesAndTypes$'
control "dns: a zone isn't a top-level domain" internal/dnszone/dnszone.go \
  'if !strings.Contains(z.Name, ".") {' \
  'if false {' \
  ./internal/dnszone '^TestZoneCheck$'
control "dns: host names have no underscores" internal/dnszone/dnszone.go \
  "case c == '_' && underscores && i == 0:" \
  "case c == '_':" \
  ./internal/dnszone '^TestZoneCheck$'
control "dns: an address fits its record's type" internal/dnszone/dnszone.go \
  'if err != nil || ip.Is4() != (r.Type == TypeA) || ip.Zone() != "" {' \
  'if err != nil || ip.Zone() != "" {' \
  ./internal/dnszone '^TestZoneCheck$'
control "dns: an SRV record has a port" internal/dnszone/dnszone.go \
  'if r.Port < 1 || r.Port > 65535 {' \
  'if false {' \
  ./internal/dnszone '^TestZoneCheck$'
control "dns: a TXT record is printable and short" internal/dnszone/dnszone.go \
  'if len(r.Value) > 255 || strings.ContainsFunc(r.Value, func(c rune) bool { return c < 0x20 || c > 0x7e }) {' \
  'if false {' \
  ./internal/dnszone '^TestZoneCheck$'
control "dns: only the types answered" internal/dnszone/dnszone.go \
  'return fmt.Errorf("the record %q has the type %q", r.Name, r.Type)' \
  'return nil' \
  ./internal/dnszone '^TestZoneCheck$'
control "dns: a record's time to live is bounded" internal/dnszone/dnszone.go \
  'if r.TTL != 0 && (r.TTL < 30 || r.TTL > 86400) {' \
  'if false {' \
  ./internal/dnszone '^TestZoneCheck$'
control "dns: a zone's size is bounded" internal/dnszone/dnszone.go \
  'if len(z.Records) > MaxRecords {' \
  'if false {' \
  ./internal/dnszone '^TestZoneCheck$'
control "dns: the machine checks the zone it's sent" internal/agent/dns.go \
  'if err := z.Check(); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/agent '^TestABadZoneIsRefused$'
control "dns: a zone is numbered when it changes" internal/agent/dns.go \
  'z.Serial = max(old.Serial+1, uint32(a.now().Unix()))' \
  'z.Serial = old.Serial' \
  ./internal/agent '^TestTheMachineAnswersTheZoneTheDashboardSets$'
control "dns: an unchanged zone keeps its number" internal/agent/dns.go \
  'changed := z.Name != old.Name || z.Nameserver != old.Nameserver || !slices.Equal(z.Records, old.Records)' \
  'changed := true || z.Name != old.Name || z.Nameserver != old.Nameserver || !slices.Equal(z.Records, old.Records)' \
  ./internal/agent '^TestTheMachineAnswersTheZoneTheDashboardSets$'
control "dns: the zone lasts across a restart" internal/agent/dns.go \
  'if err := a.kvSet(kvDNSZone, string(raw)); err != nil {' \
  'if err := error(nil); raw == nil && err != nil {' \
  ./internal/agent '^TestTheMachineAnswersTheZoneTheDashboardSets$'
control "dns: the zone is read back at start" internal/agent/dns.go \
  'a.dns.answerer.Set(z)
	return nil' \
  '_ = z
	return nil' \
  ./internal/agent '^TestTheMachineAnswersTheZoneTheDashboardSets$'
control "dns: turning the answers off stops them" internal/agent/dns.go \
  'if changed && (z.Name == "") != (old.Name == "") || z.Name != "" && len(a.dnsStatus().Listening) == 0 {' \
  'if z.Name != "" && len(a.dnsStatus().Listening) == 0 {' \
  ./internal/agent '^TestTheMachineAnswersTheZoneTheDashboardSets$'
control "dns: no zone, no answers" internal/agent/dns.go \
  'if d.answerer.Zone().Name == "" {' \
  'if false {' \
  ./internal/agent '^TestTheMachineAnswersTheZoneTheDashboardSets$'
control "dns: an address in use is said" internal/agent/dns.go \
  'problems = append(problems, dnsBindProblem(addr, err))' \
  '_ = addr' \
  ./internal/agent '^TestAnAddressInUseIsSaid$'
control "dns: a port it can't have over TCP lets its UDP side go" internal/agent/dns.go \
  'pc.Close()' \
  '_ = pc' \
  ./internal/agent '^TestDNSListensOnOnePortOverUDPAndTCP$'
control "dns: a wildcard answers names the zone hasn't got" internal/dnszone/dnszone.go \
  'recs, there = a.names["*."+ce]' \
  'recs, there = nil, false' \
  ./internal/dnszone '^TestAWildcardAnswersNamesTheZoneHasnt$'
control "dns: a name the zone has isn't the wildcard's" internal/dnszone/dnszone.go \
  'recs, there := a.names[qs.name], a.nodes[qs.name]' \
  'recs, there := a.names[qs.name], false' \
  ./internal/dnszone '^TestAWildcardAnswersNamesTheZoneHasnt$'
control "dns: a wildcard answers only below its closest encloser" internal/dnszone/dnszone.go \
  'for !a.nodes[ce] {' \
  'for ce != a.zone.Name {' \
  ./internal/dnszone '^TestAWildcardAnswersNamesTheZoneHasnt$'
control "dns: a star is nowhere but a wildcard's first label" internal/dnszone/dnszone.go \
  "case c == '_' && underscores && i == 0:" \
  "case c == '_' && underscores && i == 0, c == '*' && underscores:" \
  ./internal/dnszone '^TestZoneCheck$'
control "dns: a wildcard below the zone names what's below its star" internal/dnszone/dnszone.go \
  '} else if rest, ok := strings.CutPrefix(name, "*."); ok && rest != "" {' \
  '} else if rest, ok := strings.CutPrefix(name, "*."); ok {' \
  ./internal/dnszone '^TestZoneCheck$'

# The dashboard's DNS answers for its machine's own domain (port-free
# addresses): who may set them, what goes in the zone, and when the zone
# stays or goes.
control "dns answers: only an admin of every server reads them" internal/panel/server.go \
  '{"GET", "/api/dns-answers", needSession, actManageMachine, s.hDNSAnswers},' \
  '{"GET", "/api/dns-answers", needSession, actView, s.hDNSAnswers},' \
  ./internal/panel '^TestOnlyAnAdminOfEveryServerSetsTheDNSAnswers$'
control "dns answers: only an admin of every server sets them" internal/panel/server.go \
  '{"PUT", "/api/dns-answers", needSessionCSRF, actManageMachine, s.hDNSAnswersSet},' \
  '{"PUT", "/api/dns-answers", needSessionCSRF, actView, s.hDNSAnswersSet},' \
  ./internal/panel '^TestOnlyAnAdminOfEveryServerSetsTheDNSAnswers$'
control "dns answers: only an own domain is answered" internal/panel/dnsanswers.go \
  'case addr.Kind != api.AddressOwn || host == "":' \
  'case host == "":' \
  ./internal/panel '^TestTheDashboardAnswersDNSForItsOwnDomain$'
control "dns answers: off takes the zone away" internal/panel/dnsanswers.go \
  'if _, err := s.agent.Do(ctx, "GET", "/v1/dns-zone", nil, nil, &st); err != nil || st.Zone.Name == "" {' \
  'if _, err := s.agent.Do(ctx, "GET", "/v1/dns-zone", nil, nil, &st); true || err != nil || st.Zone.Name == "" {' \
  ./internal/panel '^TestTheDashboardAnswersDNSForItsOwnDomain$'
control "dns answers: an address that can't be read keeps the zone" internal/panel/dnsanswers.go \
  '		if _, err := s.agent.Do(ctx, "GET", "/v1/address", nil, nil, &addr); err != nil {' \
  '		if _, err := s.agent.Do(ctx, "GET", "/v1/address", nil, nil, &addr); err != nil && false {' \
  ./internal/panel '^TestTheDashboardAnswersDNSForItsOwnDomain$'
control "dns answers: an address with no IP address keeps the zone" internal/panel/dnsanswers.go \
  'case api.DNSUnavailableAddress:' \
  'case "never":' \
  ./internal/panel '^TestTheDashboardAnswersDNSForItsOwnDomain$'
control "dns answers: a record the zone can't hold is left out, not the zone refused" internal/panel/dnsanswers.go \
  'if one.Check() == nil {' \
  'if one.Check() == nil || true {' \
  ./internal/panel '^TestTheDashboardAnswersDNSForItsOwnDomain$'
control "dns answers: records outside the zone stay out of it" internal/panel/dnsanswers.go \
  'rel, ok := relName(r.Name, host)' \
  'rel, ok := relName(r.Name, host); ok = true' \
  ./internal/panel '^TestTheDashboardAnswersDNSForItsOwnDomain$'
control "dns answers: a server's name keeps its own address record" internal/panel/dnsanswers.go \
  'p.addRecord(dnszone.Record{Name: j.Label, Type: m.Type, Value: m.Value})' \
  '_ = m' \
  ./internal/panel '^TestTheDashboardAnswersDNSForItsOwnDomain$'
control "dns answers: a domain that can't be answered still says which" internal/panel/dnsanswers.go \
  'return dnsPlan{zone: dnszone.Zone{Name: host}, unavailable: api.DNSUnavailableSubdomain}' \
  'return dnsPlan{unavailable: api.DNSUnavailableSubdomain}' \
  ./internal/panel '^TestTheDashboardAnswersDNSForItsOwnDomain$'
control "dns answers: a domain right under a public suffix can't be handed over" internal/panel/dnsanswers.go \
  'if suffix, icann := publicsuffix.PublicSuffix(parent); icann && suffix == parent {' \
  'if suffix, icann := publicsuffix.PublicSuffix(parent); icann && suffix == parent && false {' \
  ./internal/panel '^TestTheNameserverIsBesideTheDomainAtItsParent$'
webcontrol "dns answers: the switch is only on the dashboard's own machine" web/src/pages/machine-settings/own.tsx \
  '...(local ? [<DNSAnswersRow key="dns" />] : []),' \
  '...[<DNSAnswersRow key="dns" />],' \
  web/src/pages/machine-settings/address.test.tsx 'is only on the dashboard'
webcontrol "dns answers: the records to add show only once the machine answers" web/src/pages/machine-settings/own.tsx \
  '{v.on && !v.unavailable && (' \
  '{!v.unavailable && (' \
  web/src/pages/machine-settings/address.test.tsx 'lets the owner turn them on'
webcontrol "dns answers: turning them off asks first" web/src/pages/machine-settings/own.tsx \
  'onCheckedChange={(on) => (on ? void set(true) : setStopping(true))}' \
  'onCheckedChange={(on) => void set(on)}' \
  web/src/pages/machine-settings/address.test.tsx 'asks before it stops answering'
webcontrol "dns answers: they can't be turned on for a domain that can't be answered" web/src/pages/machine-settings/own.tsx \
  'disabled={locked || busy || (!v.on && !!v.unavailable)}' \
  'disabled={locked || busy}' \
  web/src/pages/machine-settings/address.test.tsx 'says why a domain with nothing above it'

# Wildcard certificates (internal/certs): proven only over DNS-01, and
# served for the names just below them.
control "certs: a wildcard is asked for only with DNS-01" internal/certs/acme.go \
  'names, err := normalizeNames(req.Names, req.DNS01 != nil && req.DNS01.Challenger != nil)' \
  'names, err := normalizeNames(req.Names, true)' \
  ./internal/certs '^TestIssueAWildcardOverDNS01$'
control "certs: a wildcard serves only the names just below it" internal/certs/store.go \
  'return ok && slices.Contains(names, "*."+above)' \
  'return ok && above != "" && slices.ContainsFunc(names, func(n string) bool { return strings.HasPrefix(n, "*.") && strings.HasSuffix(name, n[1:]) })' \
  ./internal/certs '^TestIssueAWildcardOverDNS01$'
control "certs: a wildcard doesn't serve the name it's below" internal/certs/store.go \
  'return ok && slices.Contains(names, "*."+above)' \
  'return slices.Contains(names, "*."+name) || ok && slices.Contains(names, "*."+above)' \
  ./internal/certs '^TestIssueAWildcardOverDNS01$'
control "certs: the certificate has the wildcard asked for" internal/certs/acme.go \
  'covered := slices.Contains(leaf.DNSNames, n)' \
  'covered := true' \
  ./internal/certs '^TestCheckChain$'

# Servers joined with no port once the machine answers DNS for its own domain
# and the domain's parent hands the domain to it (internal/agent/address.go).
control "port-free: public DNS has to give the zone's SRV record" internal/agent/address.go \
  'return err == nil && slices.ContainsFunc(found, func(s *net.SRV) bool {' \
  'return err != nil || slices.ContainsFunc(found, func(s *net.SRV) bool {' \
  ./internal/agent '^TestServersJoinWithNoPortOnceTheDomainIsHandedOver$'
control "port-free: the public SRV record has the zone's port" internal/agent/address.go \
  'return strings.TrimSuffix(s.Target, ".") == r.Value && int(s.Port) == r.Port' \
  'return strings.TrimSuffix(s.Target, ".") == r.Value' \
  ./internal/agent '^TestServersJoinWithNoPortOnceTheDomainIsHandedOver$'
control "port-free: only the zone for the domain counts" internal/agent/address.go \
  'if z.Name != host {' \
  'if false {' \
  ./internal/agent '^TestServersJoinWithNoPortOnceTheDomainIsHandedOver$'
control "port-free: a server joins with no port only with its SRV record in the domain's zone" internal/agent/address.go \
  'return z.Name == host && slices.ContainsFunc(z.Records, func(r dnszone.Record) bool {' \
  'return slices.ContainsFunc(z.Records, func(r dnszone.Record) bool {' \
  ./internal/agent '^TestServersJoinWithNoPortOnceTheDomainIsHandedOver$'
control "port-free: a server the zone has no SRV record for keeps its port" internal/agent/address.go \
  'if st.Check != nil && st.Check.PortFree && a.answersSRV(st.Host, s.slug, s.port) {' \
  'if st.Check != nil && st.Check.PortFree {' \
  ./internal/agent '^TestServersJoinWithNoPortOnceTheDomainIsHandedOver$'
control "port-free: waiting for the parent looks again soon" internal/agent/address.go \
  '&& (check.PortFree || !answering) {' \
  '&& (check.PortFree || !answering || true) {' \
  ./internal/agent '^TestServersJoinWithNoPortOnceTheDomainIsHandedOver$'
control "port-free: a new zone brings the next look forward" internal/agent/dns.go \
  '		a.recheckOwnSoon()' \
  '		_ = a.recheckOwnSoon' \
  ./internal/agent '^TestServersJoinWithNoPortOnceTheDomainIsHandedOver$'

# The own domain's wildcard certificate (internal/agent/certificates.go,
# dns.go): got once the domain is handed to the machine, proven from its own
# zone, and in place of the servers' own certificates.
control "wildcard certificate: only once the domain is handed to the machine" internal/agent/certificates.go \
  'return st.Kind == api.AddressOwn && st.ServerAddresses && st.Check != nil && st.Check.PortFree' \
  'return st.Kind == api.AddressOwn && st.ServerAddresses' \
  ./internal/agent '^TestTheServersShareOneWildcardCertificate$'
control "wildcard certificate: the servers' addresses it serves get none of their own" internal/agent/ownaddress.go \
  'if !ownNameOK(st, js) || js.wild && wildcard {' \
  'if !ownNameOK(st, js) || js.wild && wildcard && false {' \
  ./internal/agent '^TestTheServersShareOneWildcardCertificate$'
control "wildcard certificate: it takes none of the day's certificates for servers" internal/agent/ownaddress.go \
  'name != st.Host && !strings.HasPrefix(name, "*.") && fromMillis(last).After(since) {' \
  'name != st.Host && fromMillis(last).After(since) {' \
  ./internal/agent '^TestTheServersShareOneWildcardCertificate$'
control "wildcard certificate: it goes with its domain" internal/agent/ownaddress.go \
  'a.forgetCertificate(wildcardName(st.Host))' \
  '_ = wildcardName(st.Host)' \
  ./internal/agent '^TestTheServersShareOneWildcardCertificate$'
control "wildcard certificate: the check's record goes after the check" internal/agent/dns.go \
  'values := slices.DeleteFunc(d.challenges[rel], func(v string) bool { return v == value })' \
  'values := d.challenges[rel]' \
  ./internal/agent '^TestTheServersShareOneWildcardCertificate$'
control "wildcard certificate: a check's record only in the machine's zone" internal/agent/dns.go \
  'if zone == "" || !ok {' \
  'if zone == "" || !ok && false {' \
  ./internal/agent '^TestTheServersShareOneWildcardCertificate$'
control "dns: extra records are answered besides the zone" internal/dnszone/dnszone.go \
  'for _, r := range slices.Concat(a.zone.Records, a.extra) {' \
  'for _, r := range a.zone.Records {' \
  ./internal/dnszone '^TestExtraRecordsAreAnsweredBesideTheZone$'

# Creators make a new server from an uploaded world or backup, inside their
# allowance and against their disk limit (internal/panel/customeruploads.go).
control "creator uploads: a server from a backup that doesn't say its memory needs one chosen" internal/panel/team.go \
  'if mb <= 0 {' \
  'if mb < 0 {' \
  ./internal/panel '^TestACreatorMakesAServerFromABackupTheyUpload$'
control "creator uploads: a creator's world upload names their own limit" internal/panel/customeruploads.go \
  'body["diskLimit"] = accountLimit(a.UserID)' \
  '_ = a' \
  ./internal/panel '^TestACreatorMakesAServerFromAWorldTheyUpload$'
control "creator uploads: a world upload names no limit the browser sent" internal/panel/customeruploads.go \
  'r.Body = io.NopCloser(bytes.NewReader(b))' \
  '_ = io.NopCloser(bytes.NewReader(b))' \
  ./internal/panel '^TestACreatorMakesAServerFromAWorldTheyUpload$'
control "creator uploads: a creator's backup upload names their own limit" internal/panel/customeruploads.go \
  'q.Set("diskLimit", accountLimit(a.UserID))' \
  'q.Set("diskLimit", r.URL.Query().Get("diskLimit"))' \
  ./internal/panel '^TestACreatorMakesAServerFromABackupTheyUpload$'
control "creator uploads: a world upload for a new server needs room in the allowance" internal/panel/customeruploads.go \
  'if s.refuseNewServer(w, r, a, m, 0) {
			return' \
  'if s.refuseNewServer(w, r, a, m, 0) && false {
			return' \
  ./internal/panel '^TestACreatorMakesAServerFromAWorldTheyUpload$'
control "creator uploads: a backup upload for a new server needs room in the allowance" internal/panel/customeruploads.go \
  'if s.refuseNewServer(w, r, a, m, 0) {
			s.creators.Unlock()' \
  'if false {
			s.creators.Unlock()' \
  ./internal/panel '^TestACreatorMakesAServerFromABackupTheyUpload$'
control "creator uploads: a server from a world fits the allowance" internal/panel/customeruploads.go \
  'if s.refuseNewServer(w, r, a, m, mb) {' \
  'if s.refuseNewServer(w, r, a, m, mb) && false {' \
  ./internal/panel '^TestACreatorMakesAServerFromAWorldTheyUpload$'
control "creator uploads: a server from a backup fits the allowance" internal/panel/team.go \
  'if s.refuseNewServer(w, r, sess.Access, m, mb) {' \
  'if false {' \
  ./internal/panel '^TestACreatorMakesAServerFromABackupTheyUpload$'
control "creator uploads: only on their machine" internal/panel/customeruploads.go \
  'if err := s.homeRefusal(ctx, a, m); err != nil {' \
  'if err := s.homeRefusal(ctx, a, m); false && err != nil {' \
  ./internal/panel '^TestACreatorUploadsForANewServerOnlyToTheirMachine$'
control "creator uploads: not while waiting for room" internal/panel/server.go \
  'if err := s.waitingRefusal(r.Context(), acct); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/panel '^TestACustomerWaitingForRoomUploadsNothingForANewServer$'
control "creator uploads: the allowance's server count" internal/panel/customeruploads.go \
  'if use.servers >= a.Allowance.Servers {' \
  'if false {' \
  ./internal/panel '^TestACreatorMakesAServerFromAWorldTheyUpload$'
control "creator uploads: the allowance's memory" internal/panel/customeruploads.go \
  'if msg, hint := memoryRefusal(a.Allowance, use, "", memoryMB); msg != "" {' \
  'if msg, hint := memoryRefusal(a.Allowance, use, "", memoryMB); false && msg != "" {' \
  ./internal/panel '^TestACreatorMakesAServerFromAWorldTheyUpload$'
control "creator uploads: a server made from a world joins theirs" internal/panel/customeruploads.go \
  's.forwardLongThen("/v1/world-imports/{imp}/create", s.creatorMade(r.Context()))(w, r, sess)' \
  's.forwardLongThen("/v1/world-imports/{imp}/create", nil)(w, r, sess)' \
  ./internal/panel '^TestACreatorMakesAServerFromAWorldTheyUpload$'
control "creator uploads: a server restored from a backup joins theirs" internal/panel/team.go \
  's.forwardTo(method, pattern, false, s.creatorMade(r.Context()))(w, r, sess)' \
  's.forwardTo(method, pattern, false, nil)(w, r, sess)' \
  ./internal/panel '^TestACreatorMakesAServerFromABackupTheyUpload$'
control "creator uploads: the server they make is recorded as theirs" internal/panel/customeruploads.go \
  's.claimForCreator(sess.Access, id)' \
  '' \
  ./internal/panel '^TestACreatorMakesAServerFromAWorldTheyUpload$'
control "creator uploads: the server they make gets a creator's backups" internal/panel/customeruploads.go \
  's.startCreatorBackups(ctx, m, sess.Access, id)' \
  '' \
  ./internal/panel '^TestACreatorMakesAServerFromAWorldTheyUpload$'
control "creator uploads: only a creator's own upload is theirs" internal/panel/customeruploads.go \
  'if a.creator() && limit == accountLimit(a.UserID) {' \
  'if a.creator() {' \
  ./internal/panel '^TestACreatorMakesAServerFromAWorldTheyUpload$'
control "creator uploads: a world upload's guard asks whose it is" internal/panel/worldimports.go \
  'need = uploadCreates(sess.Access, imp.DiskLimit)' \
  'need = actCreateOwnServers' \
  ./internal/panel '^TestACreatorMakesAServerFromAWorldTheyUpload$'
control "creator uploads: a backup's guard asks whose it is" internal/panel/team.go \
  'act = uploadCreates(sess.Access, p.DiskLimit)' \
  'act = actCreateOwnServers' \
  ./internal/panel '^TestACreatorMakesAServerFromABackupTheyUpload$'
control "creator uploads: a creator's limit reaches their machine before their first server" internal/panel/disklimits.go \
  'if _, ok := byAccount[uid]; !ok && (placed && home.machineID == m.ID || !placed && m.Kind == localKind) {' \
  'if _, ok := byAccount[uid]; !ok && (placed && home.machineID == m.ID || !placed && m.Kind == localKind) && false {' \
  ./internal/panel '^TestACreatorsLimitReachesTheirMachineBeforeTheirFirstServer$'
control "creator uploads: a customer without a machine has no limit on one" internal/panel/disklimits.go \
  '(placed && home.machineID == m.ID || !placed && m.Kind == localKind)' \
  '(placed && home.machineID != "-" || !placed && m.Kind == localKind)' \
  ./internal/panel '^TestACreatorsLimitReachesTheirMachineBeforeTheirFirstServer$'
webcontrol "creator uploads: a creator starts from a world or a backup" web/src/pages/new-server.tsx \
  'const importer = canCreate(ws.me)' \
  "const importer = can(ws.me, 'servers.create')" \
  src/pages/new-server.test.tsx 'a world or a backup, only on the machine their servers go on'
webcontrol "creator uploads: a creator's new server stays on the machine their servers go on" web/src/pages/new-server.tsx \
  'const asked = chooser ? machine : ws.me.access.home' \
  'const asked = importer ? machine : ws.me.access.home' \
  src/pages/new-server.test.tsx 'a world or a backup, only on the machine their servers go on'
webcontrol "creator uploads: a customer's new server goes on the joined machine they were placed on" web/src/pages/new-server.tsx \
  'const asked = chooser ? machine : ws.me.access.home' \
  'const asked = chooser ? machine : undefined' \
  src/pages/new-server.test.tsx 'on the joined machine they were placed on'

# Joined machines take customers only once the owner confirms them, which
# keeps servers away from them first (internal/panel/machinecustomers.go).
control "confirming: a joined machine takes customers only once confirmed" internal/panel/placement.go \
  'case m.Kind == remoteKind && !m.customersAt.IsZero():' \
  'case m.Kind == remoteKind:' \
  ./internal/panel '^(TestJoinedMachinesTakeNoCustomersUntilConfirmed|TestAJoinedMachineTakesCustomersOnceTheOwnerConfirmsIt)$'
control "confirming: only the owner confirms a machine" internal/panel/server.go \
  'actTakeCustomers, s.hMachineCustomers},' \
  'actManageMachine, s.hMachineCustomers},' \
  ./internal/panel '^TestAJoinedMachineTakesCustomersOnceTheOwnerConfirmsIt$'
control "confirming: servers are kept away from the machine first" internal/panel/machinecustomers.go \
  'if on {
		g, err := setNetworkGuard(ctx, m, actor, true)' \
  'if false && on {
		g, err := setNetworkGuard(ctx, m, actor, true)' \
  ./internal/panel '^(TestAJoinedMachineTakesCustomersOnceTheOwnerConfirmsIt|TestConfirmingKeepsServersAwayFromTheMachineFirst|TestAJoinedMachineInTheOwnersHetznerProjectIsConfirmedByItself)$'
control "confirming: a machine that leaves servers free to reach it isn't confirmed" internal/panel/machinecustomers.go \
  'if !g.Host {' \
  'if false && !g.Host {' \
  ./internal/panel '^TestConfirmingKeepsServersAwayFromTheMachineFirst$'
control "confirming: stopping a machine takes effect" internal/panel/machinecustomers.go \
  'if m.customersAt.IsZero() != on {' \
  'if m.customersAt.IsZero() != on || !on {' \
  ./internal/panel '^TestAJoinedMachineTakesCustomersOnceTheOwnerConfirmsIt$'
control "confirming: stopping waits for a customer being placed" internal/panel/machinecustomers.go \
  's.placeMu.Lock()
	defer s.placeMu.Unlock()
	m, err := s.machineByID(id)' \
  'm, err := s.machineByID(id)' \
  ./internal/panel '^TestAJoinedMachineTakesCustomersOnceTheOwnerConfirmsIt$'
control "confirming: a placed customer's machine gets their disk limit at once" internal/panel/placement.go \
  's.kickDiskLimits()' \
  '_ = s.kickDiskLimits' \
  ./internal/panel '^TestPlacingACustomerSendsTheDiskLimitsAtOnce$'
control "confirming: the customers waiting for room are placed" internal/panel/machinecustomers.go \
  's.kickRoom()' \
  '_ = s.kickRoom' \
  ./internal/panel '^TestAJoinedMachineTakesCustomersOnceTheOwnerConfirmsIt$'
control "confirming: servers stay away while it takes customers" internal/panel/machinecustomers.go \
  'case at != 0:' \
  'case false:' \
  ./internal/panel '^TestServersStayAwayFromAJoinedMachineWhileItTakesCustomers$'
control "confirming: servers stay away while it has customers" internal/panel/machinecustomers.go \
  'case n > 0:' \
  'case n > 0 && false:' \
  ./internal/panel '^TestServersStayAwayFromAJoinedMachineWhileItTakesCustomers$'
control "confirming: the guard's refusal is kept" internal/panel/creators.go \
  'if msg != "" {' \
  'if false && msg != "" {' \
  ./internal/panel '^TestServersStayAwayFromAJoinedMachineWhileItTakesCustomers$'
control "confirming: a creator creates servers only on their machine" internal/panel/creators.go \
  'case !ok || home != m.ID:' \
  'case !ok || home == "":' \
  ./internal/panel '^(TestACreatorUploadsForANewServerOnlyToTheirMachine|TestACustomerCreatesServersOnTheJoinedMachineTheyrePlacedOn)$'
control "confirming: a creator creates servers on their machine" internal/panel/creators.go \
  'if err := s.homeRefusal(r.Context(), a, m); err != nil {' \
  'if err := s.homeRefusal(r.Context(), a, m); false && err != nil {' \
  ./internal/panel '^TestACustomerCreatesServersOnTheJoinedMachineTheyrePlacedOn$'
control "confirming: another machine's catalog offers a creator no memory for a new server" internal/panel/creators.go \
  'case server == "" && s.homeRefusal(ctx, a, m) != nil:' \
  'case false:' \
  ./internal/panel '^TestACustomerCreatesServersOnTheJoinedMachineTheyrePlacedOn$'
control "confirming: the dashboard says which machine a creator's servers go on" internal/panel/server.go \
  'Home: s.creatorHome(a), ' \
  '' \
  ./internal/panel '^TestACustomerCreatesServersOnTheJoinedMachineTheyrePlacedOn$'
webcontrol "confirming: New server here is only on the machine a creator's servers go on" web/src/lib/access.ts \
  '(!m || m.id === me.access.home)' \
  '(!m || !!m.id)' \
  src/lib/lib.test.ts 'a creator only on the one their servers go on'
webcontrol "confirming: the card is the owner's alone" web/src/pages/machines.tsx \
  "{can(ws.me, 'machines.customers') && <CustomersCard machine={m} onChange={() => void events.refresh()} />}" \
  '<CustomersCard machine={m} onChange={() => void events.refresh()} />' \
  src/pages/pages.test.tsx 'and waits for a machine'
webcontrol "confirming: the owner checks the machine is theirs first" web/src/pages/machines.tsx \
  'onClick={() => setConfirming(true)}' \
  'onClick={() => void set(true)}' \
  src/pages/pages.test.tsx 'places customers on a joined machine once the owner checks'
webcontrol "confirming: the machine's events show the change at once" web/src/pages/machines.tsx \
  '      onChange()' \
  '      void onChange' \
  src/pages/pages.test.tsx 'places customers on a joined machine once the owner checks'
webcontrol "confirming: removing a machine says its customers are on it" web/src/pages/machines.tsx \
  '{!!m.customers && (' \
  '{false && (' \
  src/pages/pages.test.tsx 'warns before removing a machine customers are on'

# Creators and customers see their servers, never the machines: no machine's
# name or details, not how many there are, and no machine but the one their
# servers go on (internal/panel/hiddenmachines.go).
control "hidden machines: creators and customers may not look at the machines" internal/panel/workspace.go \
  'case act == actViewMachines && a.hidesMachines():' \
  'case act == actViewMachines && false:' \
  ./internal/panel '^(TestOnlyTheTeamSeesTheMachines|TestACustomerSeesTheirServersAndNeverTheMachines)$'
control "hidden machines: a customer whose plan ended sees none either" internal/panel/workspace.go \
  'return a.creator() || a.Customer != ""' \
  'return a.creator()' \
  ./internal/panel '^TestOnlyTheTeamSeesTheMachines$'
control "hidden machines: their machine list names none" internal/panel/workspace.go \
  'if permit(sess.Access, actViewMachines, "") != nil {' \
  'if false {' \
  ./internal/panel '^TestACustomerSeesTheirServersAndNeverTheMachines$'
control "hidden machines: their machine list has only the machines they use" internal/panel/hiddenmachines.go \
  'if !shown[v.ID] {' \
  'if false && !shown[v.ID] {' \
  ./internal/panel '^(TestACustomerSeesTheirServersAndNeverTheMachines|TestAnInvitedCreatorSeesOnlyTheirOwnMachine)$'
control "hidden machines: a path naming another machine isn't found" internal/panel/server.go \
  'mid != "" && !s.machineShown(r.Context(), acct, mid) {' \
  'mid != "" && false {' \
  ./internal/panel '^(TestACustomerSeesTheirServersAndNeverTheMachines|TestACustomerCreatesServersOnTheJoinedMachineTheyrePlacedOn)$'
control "hidden machines: a machine's page is the team's" internal/panel/server.go \
  '{"GET", "/api/machines/{mid}", needSession, actViewMachines, s.hMachine},' \
  '{"GET", "/api/machines/{mid}", needSession, actView, s.hMachine},' \
  ./internal/panel '^TestACustomerSeesTheirServersAndNeverTheMachines$'
control "hidden machines: a machine's events are the team's" internal/panel/server.go \
  '{"GET", "/api/machines/{mid}/events", needSession, actViewMachines, s.hMachineEvents},' \
  '{"GET", "/api/machines/{mid}/events", needSession, actView, s.hMachineEvents},' \
  ./internal/panel '^TestACustomerSeesTheirServersAndNeverTheMachines$'
control "hidden machines: joining machines is the team's" internal/panel/server.go \
  '{"GET", "/api/machines/link", needSession, actViewMachines, s.hMachineLink},' \
  '{"GET", "/api/machines/link", needSession, actView, s.hMachineLink},' \
  ./internal/panel '^TestACustomerSeesTheirServersAndNeverTheMachines$'
control "hidden machines: usage stats are the team's" internal/panel/server.go \
  '{"GET", "/api/usage-stats", needSession, actViewMachines, s.hUsageStats},' \
  '{"GET", "/api/usage-stats", needSession, actView, s.hUsageStats},' \
  ./internal/panel '^TestACustomerSeesTheirServersAndNeverTheMachines$'
control "hidden machines: the 0.2.0 status of the dashboard's machine is the team's" internal/panel/server.go \
  '{"GET", "/api/server", needSession, actViewMachines, s.hLegacyStatus},' \
  '{"GET", "/api/server", needSession, actView, s.hLegacyStatus},' \
  ./internal/panel '^TestACustomerSeesTheirServersAndNeverTheMachines$'
control "hidden machines: their answers are marked" internal/panel/server.go \
  'w = &blindWriter{ResponseWriter: w}' \
  '_ = &blindWriter{ResponseWriter: w}' \
  ./internal/panel '^TestACustomerSeesTheirServersAndNeverTheMachines$'
control "hidden machines: a machine that can't be reached is their server that can't be" internal/panel/server.go \
  'if blind(w) {' \
  'if false && blind(w) {' \
  ./internal/panel '^TestACustomerSeesTheirServersAndNeverTheMachines$'
control "hidden machines: their catalog's memory is their plan's" internal/panel/creators.go \
  'c["hostMemoryMB"], _ = json.Marshal(a.Allowance.MemoryMB)' \
  '_ = a.Allowance.MemoryMB' \
  ./internal/panel '^(TestACustomerSeesTheirServersAndNeverTheMachines|TestAnInvitedCreatorSeesOnlyTheirOwnMachine)$'
control "hidden machines: their server list shows their plan" internal/panel/workspace.go \
  'if sess.Access.hidesMachines() {
		used := s.ownMemory(sess.Access, out)' \
  'if false {
		used := s.ownMemory(sess.Access, out)' \
  ./internal/panel '^TestACustomerSeesTheirServersAndNeverTheMachines$'
control "hidden machines: one server's status shows their plan" internal/panel/hiddenmachines.go \
  'if !sess.Access.hidesMachines() {
		s.serverProxy(' \
  'if true {
		s.serverProxy(' \
  ./internal/panel '^TestACustomerSeesTheirServersAndNeverTheMachines$'
control "hidden machines: a crashed server's room is their plan's" internal/panel/hiddenmachines.go \
  'if mb, ok := c["roomMB"].(float64); ok && int(mb) > room {' \
  'if mb, ok := c["roomMB"].(float64); ok && int(mb) < 0 {' \
  ./internal/panel '^TestACustomerSeesTheirServersAndNeverTheMachines$'
control "hidden machines: a crash fix past their plan isn't offered" internal/panel/hiddenmachines.go \
  'fix["kind"] == "raise_memory" && int(to-memoryMB) > room {' \
  'fix["kind"] == "raise_memory" && int(to-memoryMB) > room+1<<30 {' \
  ./internal/panel '^TestACustomerSeesTheirServersAndNeverTheMachines$'
control "hidden machines: their servers' disk is their plan's" internal/panel/hiddenmachines.go \
  'r["diskFreeBytes"], r["diskTotalBytes"] = s.planDisk(a, int64(machineFree))' \
  '_ = machineFree' \
  ./internal/panel '^TestACustomerSeesTheirServersAndNeverTheMachines$'
control "hidden machines: pre-generating shows their plan's disk" internal/panel/creators.go \
  'v["diskFreeBytes"], _ = json.Marshal(free)' \
  '_ = free' \
  ./internal/panel '^TestACustomerSeesTheirServersAndNeverTheMachines$'
control "hidden machines: the resource pack refusal names no machine" internal/panel/packs.go \
  'if sess.Access.hidesMachines() {
			msg, hint = ' \
  'if false {
			msg, hint = ' \
  ./internal/panel '^TestACustomerSeesTheirServersAndNeverTheMachines$'
control "hidden machines: the sign-in page names no machine once customers sign in there" internal/panel/server.go \
  'if !st.WhopSignIn {' \
  'if true {' \
  ./internal/panel '^TestTheSignInPageNamesNoMachineOnceCustomersSignInThere$'
control "hidden machines: their AI agents list no machine" internal/mcptools/tools.go \
  's.MachineID, s.MachineName = "", ""' \
  '_ = s.MachineID' \
  ./internal/panel '^TestACustomerSeesTheirServersAndNeverTheMachines$'
control "hidden machines: their AI agents hear of no machine's trouble" internal/mcptools/tools.go \
  'if c.seesMachines() || !errors.As(err, &te) || !machineKinds[te.Kind] {' \
  'if c.seesMachines() || !errors.As(err, &te) || true {' \
  ./internal/panel '^TestACustomerSeesTheirServersAndNeverTheMachines$'
webcontrol "hidden machines: a customer's machines come without names" web/src/api/workspace.tsx \
  'machines.data?.map(nameless)' \
  'machines.data' \
  src/pages/hidden-machines.test.tsx 'while its machine is away, never which machine'
webcontrol "hidden machines: Home doesn't group a customer's servers by machine" web/src/pages/home.tsx \
  'const grouped = sees && ws.machines.length > 1' \
  'const grouped = ws.machines.length > 1' \
  src/pages/hidden-machines.test.tsx 'on Home under no machine'
webcontrol "hidden machines: Home shows a customer no machine's card" web/src/pages/home.tsx \
  '{sees && <MachineCard />}' \
  '<MachineCard />' \
  src/pages/hidden-machines.test.tsx 'on Home under no machine'
webcontrol "hidden machines: a customer's New server card says what their plan has left" web/src/pages/home.tsx \
  'planFreeMB={sees ? undefined : catalog?.memoryFreeMB}' \
  'planFreeMB={undefined}' \
  src/pages/hidden-machines.test.tsx 'on Home under no machine'
webcontrol "hidden machines: an away machine is a server that can't be reached" web/src/lib/machines.ts \
  "return away.name ? t('machines.away.pill', { name: away.name }) : t('status.unreachable')" \
  "return t('machines.away.pill', { name: away.name })" \
  src/pages/hidden-machines.test.tsx 'while its machine is away, never which machine'
webcontrol "hidden machines: the away view names no machine to a customer" web/src/pages/server/overview.tsx \
  "if (!sees) title = t('machines.away.pillHidden')" \
  'if (false) title = ""' \
  src/pages/hidden-machines.test.tsx 'while its machine is away, never which machine'
webcontrol "hidden machines: the sidebar puts no machine over a customer's servers" web/src/components/app/shell.tsx \
  'const shared = sees && ws.machines.length > 1' \
  'const shared = ws.machines.length > 1' \
  src/pages/hidden-machines.test.tsx 'in the sidebar under no machine'
webcontrol "hidden machines: the sidebar puts no machine over one machine's servers either" web/src/components/app/shell.tsx \
  '{ws.machine && sees && <MachineRow machine={ws.machine} route={route} />}' \
  '{ws.machine && <MachineRow machine={ws.machine} route={route} />}' \
  src/pages/hidden-machines.test.tsx 'in the sidebar under no machine'
webcontrol "hidden machines: the command palette offers a customer no machine" web/src/components/app/command-palette.tsx \
  "for (const m of can(ws.me, 'machines.view') ? machines : []) {" \
  'for (const m of machines) {' \
  src/pages/hidden-machines.test.tsx 'no machine in the command palette'
webcontrol "hidden machines: Settings has no Machines section for a customer" web/src/lib/access.ts \
  "label: 'global.nav.machines', act: 'machines.view' }" \
  "label: 'global.nav.machines', act: 'view' }" \
  src/pages/hidden-machines.test.tsx 'no machine in the command palette'
webcontrol "hidden machines: Settings shows a customer no usage stats" web/src/pages/settings.tsx \
  "{can(ws.me, 'machines.view') ? (" \
  '{true ? (' \
  src/pages/hidden-machines.test.tsx 'no machine in the command palette'
webcontrol "hidden machines: a machine's page sends a customer Home" web/src/App.tsx \
  "const hidden = !can(me, 'machines.view') && machinePages.includes(asked.name)" \
  'const hidden = false' \
  src/pages/hidden-machines.test.tsx 'opens a machine'
webcontrol "hidden machines: New server names no machine to a customer" web/src/pages/new-server.tsx \
  '{sees && machineName && (' \
  '{machineName && (' \
  src/pages/new-server.test.tsx 'never naming it'

# Room for sale: each plan's stock on Whop is how many more of it the
# machines can take at once (internal/panel/saleroom.go).
control "room for sale: paid plans get room before free ones" internal/panel/saleroom.go \
  'cmp.Or(compareBool(a.Free, b.Free), cmp.Compare(a.MemoryMB, b.MemoryMB), cmp.Compare(a.ID, b.ID))' \
  'cmp.Or(cmp.Compare(a.MemoryMB, b.MemoryMB), cmp.Compare(a.ID, b.ID))' \
  ./internal/panel '^TestRoomGoesToEachPlanInTurnPaidFirstAndSmallestFirst$'
control "room for sale: the smallest plans get room first" internal/panel/saleroom.go \
  'cmp.Compare(a.MemoryMB, b.MemoryMB), cmp.Compare(a.ID, b.ID))' \
  'cmp.Compare(b.MemoryMB, a.MemoryMB), cmp.Compare(a.ID, b.ID))' \
  ./internal/panel '^TestRoomGoesToEachPlanInTurnPaidFirstAndSmallestFirst$'
control "room for sale: customers waiting for room get theirs first" internal/panel/saleroom.go \
  'for _, mb := range waiting {' \
  'for _, mb := range waiting[:0] {' \
  ./internal/panel '^(TestRoomGoesToEachPlanInTurnPaidFirstAndSmallestFirst|TestEachPlansStockFollowsTheMachinesRoom)$'
control "room for sale: the customers waiting are counted" internal/panel/saleroom.go \
  'waiting, err := s.waitingMemory(ctx)' \
  'waiting, err := []int(nil), error(nil)' \
  ./internal/panel '^TestEachPlansStockFollowsTheMachinesRoom$'
control "room for sale: the billing side hears the numbers" internal/panel/saleroom.go \
  'if err := s.sales.SetAvailability(ctx, room.left); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/panel '^TestEachPlansStockFollowsTheMachinesRoom$'
control "room for sale: placing a customer changes the room" internal/panel/placement.go \
  's.kickDiskLimits()
		s.kickSaleRoom()' \
  's.kickDiskLimits()' \
  ./internal/panel '^TestEachPlansStockFollowsTheMachinesRoom$'
control "room for sale: a customer set waiting changes the room" internal/panel/placement.go \
  's.kickSaleRoom()
			if !waiting {' \
  'if !waiting {' \
  ./internal/panel '^TestEachPlansStockFollowsTheMachinesRoom$'
control "room for sale: a customer's plan changing changes the room" internal/panel/customers.go \
  's.kickDiskLimits()
		s.kickSaleRoom()' \
  's.kickDiskLimits()' \
  ./internal/panel '^TestEachPlansStockFollowsTheMachinesRoom$'
control "room for sale: a server made changes the room" internal/panel/server.go \
  's.claimCreated(m, raw)
	s.kickSaleRoom()' \
  's.claimCreated(m, raw)' \
  ./internal/panel '^TestTheRoomIsWorkedOutAgainWhenItChanges$'
control "room for sale: a server resized changes the room" internal/panel/creators.go \
  's.forwardThen("POST", "/v1/servers/{id}/settings", s.roomChanged)' \
  's.forwardThen("POST", "/v1/servers/{id}/settings", nil)' \
  ./internal/panel '^TestTheRoomIsWorkedOutAgainWhenItChanges$'
control "room for sale: a server deleted changes the room" internal/panel/creators.go \
  's.forwardThen("POST", "/v1/servers/{id}/delete", s.roomChanged)' \
  's.forwardThen("POST", "/v1/servers/{id}/delete", nil)' \
  ./internal/panel '^TestTheRoomIsWorkedOutAgainWhenItChanges$'
control "room for sale: a plan's allowance changing changes the room" internal/panel/whop.go \
  'fmt.Sprintf("%s: %s", cmpOr(title, id), detail))
	s.kickSaleRoom()' \
  'fmt.Sprintf("%s: %s", cmpOr(title, id), detail))' \
  ./internal/panel '^TestTheRoomIsWorkedOutAgainWhenItChanges$'
control "room for sale: a creator removed changes the room" internal/panel/team.go \
  '"team.remove", t.Name, "succeeded", "")
	s.kickDiskLimits()
	s.kickSaleRoom()' \
  '"team.remove", t.Name, "succeeded", "")
	s.kickDiskLimits()' \
  ./internal/panel '^TestTheRoomIsWorkedOutAgainWhenItChanges$'
control "room for sale: confirming or stopping a machine changes the room" internal/panel/machinecustomers.go \
  'cmp.Or(m.Name, m.ID), result, detail)
	s.kickSaleRoom()' \
  'cmp.Or(m.Name, m.ID), result, detail)' \
  ./internal/panel '^TestAJoinedMachinesCustomersChangeTheRoom$'
control "room for sale: a machine connecting, going away or removed changes the room" internal/panel/machines.go \
  '// A machine that comes or goes brings or takes its room.
		s.kickSaleRoom()' \
  '// A machine that comes or goes brings or takes its room.' \
  ./internal/panel '^TestAJoinedMachinesCustomersChangeTheRoom$'
control "room for sale: a creator joining changes the room" internal/panel/join.go \
  '// A creator'"'"'s allowance is set aside on the dashboard'"'"'s machine.
		s.kickSaleRoom()' \
  '// A creator'"'"'s allowance is set aside on the dashboard'"'"'s machine.' \
  ./internal/panel '^TestACreatorJoiningChangesTheRoom$'
control "room for sale: the room for customers is the owner's" internal/panel/server.go \
  '{"GET", "/api/machines/room", needSession, actTakeCustomers, s.hSaleRoom},' \
  '{"GET", "/api/machines/room", needSession, actView, s.hSaleRoom},' \
  ./internal/panel '^TestOnlyTheOwnerSeesTheRoomForCustomers$'
# Confirming by itself: a joined machine connected from a server in the
# owner's Hetzner project takes customers as if confirmed by hand
# (internal/panel/autoconfirm.go).
control "confirming by itself: only a machine connected from a server in the project" internal/panel/autoconfirm.go \
  'return sv.Has(m.ip) })' \
  'return true })' \
  ./internal/panel '^TestAJoinedMachineInTheOwnersHetznerProjectIsConfirmedByItself$'
control "confirming by itself: a server's IPv4 address is the machine's only when it's the same" internal/hetzner/hetzner.go \
  's.IPv4.IsValid() && s.IPv4 == addr' \
  's.IPv4.IsValid()' \
  ./internal/hetzner '^TestServersReadsEveryPageAndEachServersAddresses$'
control "confirming by itself: a server's IPv6 network is the machine's only when it holds the address" internal/hetzner/hetzner.go \
  's.IPv6.IsValid() && s.IPv6.Contains(addr)' \
  's.IPv6.IsValid()' \
  ./internal/hetzner '^TestServersReadsEveryPageAndEachServersAddresses$'
control "confirming by itself: a project with too many servers isn't read" internal/hetzner/hetzner.go \
  'case *next > maxServerPages:' \
  'case false:' \
  ./internal/hetzner '^TestServersStopsAtAPageThatGoesBackOrTooFar$'
control "confirming by itself: the stock watch looks" internal/panel/hetzner.go \
  'if err := s.confirmFromHetzner(qctx, c); err != nil {' \
  'if err := error(nil); qctx == nil && err != nil {' \
  ./internal/panel '^TestAJoinedMachineInTheOwnersHetznerProjectIsConfirmedByItself$'
control "confirming by itself: the Hetzner server stands for the owner in the log" internal/panel/autoconfirm.go \
  's.confirmFound(ctx, m.id, hetznerActor+name)' \
  's.confirmFound(ctx, m.id, placementActor)' \
  ./internal/panel '^TestAJoinedMachineInTheOwnersHetznerProjectIsConfirmedByItself$'
control "confirming by itself: stopping a machine is remembered" internal/panel/machinecustomers.go \
  'at, by, stopped, result, detail := int64(0), "", millis(s.now()), "stopped"' \
  'at, by, stopped, result, detail := int64(0), "", int64(0), "stopped"' \
  ./internal/panel '^TestAMachineTheOwnerStoppedIsntConfirmedAgain$'
control "confirming by itself: a machine the owner stopped isn't looked for" internal/panel/autoconfirm.go \
  '!m.customersStopped.IsZero() || l == nil' \
  'l == nil' \
  ./internal/panel '^TestAMachineTheOwnerStoppedIsntConfirmedAgain$'
control "confirming by itself: a machine the owner stopped meanwhile isn't confirmed" internal/panel/autoconfirm.go \
  'case m.Kind != remoteKind || !m.customersAt.IsZero() || !m.customersStopped.IsZero():' \
  'case m.Kind != remoteKind || !m.customersAt.IsZero():' \
  ./internal/panel '^TestAMachineTheOwnerStoppedIsntConfirmedAgain$'
control "confirming by itself: only a connected machine is looked for" internal/panel/autoconfirm.go \
  'l == nil || l.State != machinelink.StateConnected' \
  'l == nil || l.State == machinelink.State("")' \
  ./internal/panel '^TestOnlyAConnectedMachineIsConfirmedAndOnlyWithTheOwnersToken$'
control "confirming by itself: a machine that couldn't be confirmed waits" internal/panel/autoconfirm.go \
  'if at, ok := s.confirmFailed[m.ID]; ok && s.now().Sub(at) < confirmRetry {' \
  'if at, ok := s.confirmFailed[m.ID]; ok && false && s.now().Sub(at) < confirmRetry {' \
  ./internal/panel '^TestAMachineThatWontKeepServersAwayWaitsBeforeTheNextTry$'
control "confirming by itself: a failed try is remembered" internal/panel/autoconfirm.go \
  's.confirmFailed[m.id] = s.now()' \
  '_ = s.now()' \
  ./internal/panel '^TestAMachineThatWontKeepServersAwayWaitsBeforeTheNextTry$'
webcontrol "confirming by itself: its events say it was found in the Hetzner project" web/src/lib/machines.ts \
  "if (found) return t('machines.event.customersFound', { server: found })" \
  "if (false) return t('machines.event.customersFound', { server: found })" \
  src/lib/lib.test.ts 'confirmed a machine itself'
webcontrol "confirming by itself: the Customers card says it was found in the Hetzner project" web/src/pages/machines.tsx \
  "return found ? t('machines.customers.foundHint', { server: found, date }) :" \
  "return false ? t('machines.customers.foundHint', { server: found, date }) :" \
  src/pages/pages.test.tsx 'confirmed a joined machine itself'
webcontrol "room for sale: the card is the owner's alone" web/src/pages/machines.tsx \
  "{can(ws.me, 'machines.customers') && <SaleRoomCard />}" \
  '<SaleRoomCard />' \
  src/pages/pages.test.tsx 'for the owner alone, and only while plans are on sale'
webcontrol "room for sale: no card without plans on sale" web/src/pages/sale-room.tsx \
  'if (r && r.plans.length === 0) return null' \
  'if (false) return null' \
  src/pages/pages.test.tsx 'for the owner alone, and only while plans are on sale'

# Step 8 of the fleet plan: a server moved in from another machine keeps its
# id, and only the dashboard's move-in picks one.
control "moving in: an id a server here has is refused" internal/agent/servers.go \
  'case a.idTaken(id):' \
  'case false:' \
  ./internal/agent '^TestAServerMovedInKeepsWhatItHadWhereItWas$'
control "moving in: files of a server where its would go keep the id taken" internal/agent/servers.go \
  'return !errors.Is(err, os.ErrNotExist)' \
  'return false && !errors.Is(err, os.ErrNotExist)' \
  ./internal/agent '^TestAServerMovedInKeepsWhatItHadWhereItWas$'
control "moving in: only an id of a server's shape" internal/agent/movein.go \
  'if !reServerID.MatchString(req.ServerID) {' \
  'if false {' \
  ./internal/agent '^TestAMoveInTakesOnlyWhatTheDashboardSends$'
control "moving in: only a slug of a slug's shape" internal/agent/movein.go \
  'if req.Slug != "" && !reSlug.MatchString(req.Slug) {' \
  'if false {' \
  ./internal/agent '^TestAMoveInTakesOnlyWhatTheDashboardSends$'
control "moving in: only a play style Playkeeper knows" internal/agent/movein.go \
  'if !playStyles[req.PlayStyle] {' \
  'if false {' \
  ./internal/agent '^TestAMoveInTakesOnlyWhatTheDashboardSends$'
control "moving in: only with the EULA acceptance the server had" internal/agent/movein.go \
  'if err != nil || req.EULAAcceptedAt.IsZero() {' \
  'if false {' \
  ./internal/agent '^TestAMoveInTakesOnlyWhatTheDashboardSends$'
control "moving in: an upload into a server here isn't moved in" internal/agent/movein.go \
  'if st.preview.ServerID != "" {' \
  'if false {' \
  ./internal/agent '^TestAMoveInTakesOnlyWhatTheDashboardSends$'
control "moving in: the world has to fit the disk limit its upload named" internal/agent/movein.go \
  'if done, err = a.holdNamedLimit(r.Context(), st.limit, unpackedBytes(st.manifest)); err != nil {' \
  'if done, err = func(bool) {}, error(nil); err != nil {' \
  ./internal/agent '^TestAMoveInTakesOnlyWhatTheDashboardSends$'
control "moving in: the server joins the disk limit its upload named" internal/agent/movein.go \
  'if err := a.joinDiskLimit(st.limit, s.id); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/agent '^TestAServerMovedInKeepsWhatItHadWhereItWas$'
control "moving in: it keeps the EULA acceptance, creation time and play style it had" internal/agent/movein.go \
  'op, err := a.newFromStage(st, in.spec, in.mem, &in.prev, func(' \
  'op, err := a.newFromStage(st, in.spec, in.mem, nil, func(' \
  ./internal/agent '^TestAServerMovedInKeepsWhatItHadWhereItWas$'
control "moving in: a name a server here has gets a number" internal/agent/movein.go \
  'name = a.uniqueName(name)' \
  '_ = name' \
  ./internal/agent '^TestAServerMovedInThatRanThereStartsHere$'
control "moving in: it keeps its slug" internal/agent/servers.go \
  'slug := spec.slug' \
  'slug := ""' \
  ./internal/agent '^TestAServerMovedInKeepsWhatItHadWhereItWas$'
control "moving in: a slug a server here has gets another" internal/agent/servers.go \
  'if slug == "" || a.slugTaken(slug) {' \
  'if slug == "" {' \
  ./internal/agent '^TestAServerMovedInThatRanThereStartsHere$'
control "moving in: a server that didn't run there stays stopped" internal/agent/movein.go \
  'st.stopped = !in.start' \
  'st.stopped = false' \
  ./internal/agent '^TestAServerMovedInKeepsWhatItHadWhereItWas$'
control "moving in: the restore's journal keeps it stopped" internal/agent/backups.go \
  'State: swapMoving, Stopped: st.stopped,' \
  'State: swapMoving,' \
  ./internal/agent '^TestAServerMovedInKeepsWhatItHadWhereItWas$'
control "moving in: a restore kept stopped doesn't start" internal/agent/backups.go \
  'return s.keepStopped(h, stageDir, j)' \
  '_ = s.keepStopped' \
  ./internal/agent '^TestAServerMovedInKeepsWhatItHadWhereItWas$'
# shellcheck disable=SC2016
control "moving in: it stays stopped after the agent restarts" internal/agent/backups.go \
  'Stopped bool `json:"stopped,omitempty"`' \
  'Stopped bool `json:"-"`' \
  ./internal/agent '^TestAMoveInFinishedAfterARestartStaysStopped$'
control "moving in: its stage says it stays stopped before its journal does" internal/agent/movein.go \
  'if err := a.stageStopped(rid, st.stopped); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/agent '^TestAMoveInFinishedAfterARestartStaysStopped$'
control "moving in: an agent that dies before its journal keeps it stopped" internal/agent/recovery.go \
  'Stopped: f.Stopped,' \
  '' \
  ./internal/agent '^TestAMoveInFinishedAfterARestartStaysStopped$'

# Step 8 of the fleet plan: the owner moves a customer, and their servers
# follow, keeping their ids.
control "moving customers: only the owner moves customers" internal/panel/server.go \
  '{"POST", "/api/customers/{uid}/move", needSessionCSRF, actTakeCustomers, s.hCustomerMove},' \
  '{"POST", "/api/customers/{uid}/move", needSessionCSRF, actViewMachines, s.hCustomerMove},' \
  ./internal/panel '^TestOnlyTheOwnerMovesCustomers$'
control "moving customers: only the owner sees whose servers go on a machine" internal/panel/server.go \
  '{"GET", "/api/machines/{mid}/customers", needSession, actTakeCustomers, s.hMachineCustomerList},' \
  '{"GET", "/api/machines/{mid}/customers", needSession, actViewMachines, s.hMachineCustomerList},' \
  ./internal/panel '^TestOnlyTheOwnerMovesCustomers$'
control "moving customers: a customer whose plan ended isn't moved" internal/panel/moves.go \
  'case !moving && (deletedAt != 0 || CustomerState(state) == CustomerPaused && deleteAfter > 0 && deleteAfter <= s.now().UnixMilli()):' \
  'case false:' \
  ./internal/panel '^TestAMoveGoesOnlyWhereTheCustomerFits$'
control "moving customers: one whose plan ended is moved again while a move of theirs has stopped" internal/panel/moves.go \
  'case !moving && (deletedAt != 0 ||' \
  'case (deletedAt != 0 ||' \
  ./internal/panel '^TestALapsedCustomerBeingMovedIsDeletedLater$'
control "moving customers: a customer waiting for room isn't moved" internal/panel/moves.go \
  'return "", errMoveWaiting' \
  '_ = errMoveWaiting' \
  ./internal/panel '^TestAMoveGoesOnlyWhereTheCustomerFits$'
control "moving customers: moving to where they are only brings back what a move left" internal/panel/moves.go \
  'case to == home && !moving:' \
  'case false:' \
  ./internal/panel '^TestAMoveGoesOnlyWhereTheCustomerFits$'
control "moving customers: only to a machine that takes customers" internal/panel/moves.go \
  'case !target.Takes:' \
  'case false:' \
  ./internal/panel '^TestAMoveGoesOnlyWhereTheCustomerFits$'
control "moving customers: only to a machine with room for their plan" internal/panel/moves.go \
  'case target.FreeMB < planMB:' \
  'case false:' \
  ./internal/panel '^TestAMoveGoesOnlyWhereTheCustomerFits$'
control "moving customers: only to a machine that keeps servers away from itself" internal/panel/moves.go \
  'if err == nil && !g.Host {' \
  'if false && err == nil && !g.Host {' \
  ./internal/panel '^TestAMoveGoesOnlyWhereTheCustomerFits$'
control "moving customers: a move to the fullest machine passes over one it can't guard" internal/panel/moves.go \
  'others = slices.DeleteFunc(others, func(r machineRoom) bool { return r.ID == target.ID })' \
  'return "", errMoveUnguarded' \
  ./internal/panel '^TestAMoveToTheFullestPassesOverAMachineItCantGuard$'
control "moving customers: no request reaches a server being moved" internal/panel/workspace.go \
  'if moving > 0 {' \
  'if moving > 0 && false {' \
  ./internal/panel '^TestNothingReachesAServerWhileItMoves$'
control "moving customers: a customer is told their server is being moved, naming no machine" internal/panel/hiddenmachines.go \
  'if errors.As(err, &ae) || errors.Is(err, errServerMoving) {' \
  'if errors.As(err, &ae) {' \
  ./internal/panel '^TestNothingReachesAServerWhileItMoves$'
control "moving customers: an AI agent reaches no server being moved" internal/panel/mcp.go \
  'case errors.Is(err, errServerMoving):' \
  'case false:' \
  ./internal/panel '^TestNothingReachesAServerWhileItMoves$'
control "moving customers: a server being moved is listed as moving" internal/panel/moves.go \
  'sv["moving"] = true' \
  '_ = sv' \
  ./internal/panel '^TestNothingReachesAServerWhileItMoves$'
control "moving customers: a customer makes no new server while theirs move" internal/panel/creators.go \
  'if s.customerMoving(ctx, a.UserID) {' \
  'if false {' \
  ./internal/panel '^TestNothingReachesAServerWhileItMoves$'
control "moving customers: a customer being moved isn't deleted once their grace period ends" internal/panel/deletion.go \
  'if s.customerMoving(ctx, userID) && (placed || s.moves.running(userID)) {' \
  'if s.customerMoving(ctx, userID) && placed && false {' \
  ./internal/panel '^TestALapsedCustomerBeingMovedIsDeletedLater$'
control "moving customers: a customer with no machine whose move stopped is deleted where their servers are" internal/panel/deletion.go \
  '(placed || s.moves.running(userID))' \
  '(placed || true)' \
  ./internal/panel '^TestALapsedCustomerWhoseMoveCantGoOnIsDeletedWhereTheirServersAre$'
control "moving customers: a move that couldn't go on ends with its customer's deletion" internal/panel/deletion.go \
  'DELETE FROM customer_moves WHERE user_id = ?' \
  'DELETE FROM customer_moves WHERE user_id = ? AND 0' \
  ./internal/panel '^TestALapsedCustomerWhoseMoveCantGoOnIsDeletedWhereTheirServersAre$'
control "removing a machine: a lapsed customer's server left on it isn't taken for deleted" internal/panel/deletion.go \
  'if at = cmp.Or(at, home); at != "" {' \
  'if at = cmp.Or(home, at); at != "" {' \
  ./internal/panel '^TestALapsedCustomersServersLeftOnARemovedMachineArentTakenForDeleted$'
control "removing a machine: no final backups are promised on a machine that didn't delete anything" internal/panel/deletion.go \
  'UPDATE customers SET servers_deleted_at = ?, told_ready = 0,' \
  "UPDATE customers SET servers_deleted_at = ?, final_backups_machine = 'x', told_ready = 0," \
  ./internal/panel '^TestALapsedCustomersServersLeftOnARemovedMachineArentTakenForDeleted$'
control "removing a machine: a lapsed customer's server left on it is forgotten" internal/panel/deletion.go \
  'for _, id := range gone {' \
  'for _, id := range gone[:0] {' \
  ./internal/panel '^TestALapsedCustomersServersLeftOnARemovedMachineArentTakenForDeleted$'
control "pausing: a paused customer's server stops where a stopped move left it" internal/panel/pausing.go \
  'm, err := s.machineByID(cmp.Or(at, home))' \
  'm, err := s.machineByID(cmp.Or(home, at))' \
  ./internal/panel '^TestPausingStopsTheServersAStoppedMoveLeftBehind$'
control "moving customers: the copy a machine makes of a server moving to it isn't the server" internal/panel/moves.go \
  'SELECT server_id, 0 FROM server_moves WHERE to_machine = ?' \
  'SELECT server_id, 0 FROM server_moves WHERE to_machine = ? AND 0' \
  ./internal/panel '^TestTheCopiesAMoveMakesAndLeavesDontCountAsTheServer$'
control "moving customers: a copy a move left isn't the server" internal/panel/moves.go \
  'UNION ALL SELECT server_id, left_at FROM left_copies WHERE machine_id = ?' \
  'UNION ALL SELECT server_id, left_at FROM left_copies WHERE machine_id = ? AND 0' \
  ./internal/panel '^TestTheCopiesAMoveMakesAndLeavesDontCountAsTheServer$'
control "moving customers: a listing asked for after a copy went counts again" internal/panel/moves.go \
  'return ok && (left == 0 || millis(listedAt) <= left)' \
  'return ok && (left == 0 || millis(listedAt) > 0)' \
  ./internal/panel '^TestTheCopiesAMoveMakesAndLeavesDontCountAsTheServer$'
control "moving customers: a copy still there keeps its record" internal/panel/moves.go \
  'WHERE machine_id = ? AND left_at > 0 AND left_at < ?' \
  'WHERE machine_id = ? AND left_at >= 0 AND left_at < ?' \
  ./internal/panel '^TestTheCopiesAMoveMakesAndLeavesDontCountAsTheServer$'
control "moving customers: a copy that went stays recorded a while, for listings that arrive late" internal/panel/moves.go \
  'millis(listedAt.Add(-leftCopyKept)))' \
  'millis(listedAt))' \
  ./internal/panel '^TestALateListingStillLeavesOutACopyThatWent$'
control "moving customers: a moved server counts against its customer's disk limit before its requests go there" internal/panel/moves.go \
  'if err := s.sendLimitsTo(ctx, to, mv.serverID); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/panel '^TestTheOwnerMovesACustomerAndTheirServerFollows$'
control "moving customers: the server's folder doesn't count against the customer's disk limit beside their server" internal/panel/moves.go \
  '"/v1/restore/upload", nil, io.TeeReader(down.Body, sent),' \
  '"/v1/restore/upload", url.Values{"diskLimit": {"account-1"}}, io.TeeReader(down.Body, sent),' \
  ./internal/panel '^TestTheOwnerMovesACustomerAndTheirServerFollows$'
control "moving customers: a paused customer's server is made stopped where it moves" internal/panel/moves.go \
  'start := mv.ran && !s.customerHeld(ctx, userID)' \
  'start := mv.ran' \
  ./internal/panel '^TestAPausedCustomersServerMovesStopped$'
control "moving customers: a server whose customer was paused while it moved stops where it went" internal/panel/moves.go \
  'if mv.ran && s.customerHeld(ctx, userID) {' \
  'if false {' \
  ./internal/panel '^TestAPausedCustomersServerMovesStopped$'
control "moving customers: a copy that started before a restart stops once its customer is paused" internal/panel/moves.go \
  'if mv.ran && s.customerHeld(ctx, userID) {' \
  'if start && s.customerHeld(ctx, userID) {' \
  ./internal/panel '^TestAPausedCustomersServerMovesStopped$'
control "moving customers: a server moved after a restart gets the backup rules it had" internal/panel/moves.go \
  's.copyBackupRules(ctx, id, from, to)' \
  '_ = from' \
  ./internal/panel '^TestAMoveCarriesOnAfterARestart$'
control "moving customers: a folder that arrives changed isn't moved in" internal/panel/moves.go \
  'if hex.EncodeToString(sent.Sum(nil)) != p.SHA256 {' \
  'if hex.EncodeToString(sent.Sum(nil)) == "" {' \
  ./internal/panel '^TestAFolderThatArrivesChangedIsntMovedIn$'
control "moving customers: a move takes the server's whole folder" internal/panel/moves.go \
  '"/v1/servers/"+id+"/move-out"' \
  '"/v1/servers/"+id+"/backups"' \
  ./internal/panel '^TestTheOwnerMovesACustomerAndTheirServerFollows$'
control "moving customers: an upload no move-in took goes" internal/panel/moves.go \
  '		discardUpload(ctx, to, rid)
		return fmt.Errorf("%s couldn'"'"'t make it from its folder: %w", machineLabel(to), err)' \
  '		return fmt.Errorf("%s couldn'"'"'t make it from its folder: %w", machineLabel(to), err)' \
  ./internal/panel '^TestAFailedMoveLeavesTheServerWhereItWas$'
control "moving customers: a move waits for a server the customer is making" internal/panel/moves.go \
  '	s.creators.Lock()
	defer s.creators.Unlock()
	err = s.immediate(' \
  '	err = s.immediate(' \
  ./internal/panel '^TestAServerMadeAsAMoveStartsIsntLeftBehind$'
control "moving customers: a server that turns up during a move is moved too" internal/panel/moves.go \
  'for pass := 1; err == nil && pass < 3 && s.serversApart(ctx, userID); pass++ {' \
  'for pass := 1; err == nil && pass < 1; pass++ {' \
  ./internal/panel '^TestAServerThatTurnsUpDuringAMoveIsMovedToo$'
control "moving customers: servers apart keep their customer moving" internal/panel/moves.go \
  'return err != nil || n > 0 || s.serversApart(ctx, userID)' \
  'return err != nil || n > 0' \
  ./internal/panel '^TestServersApartAreBroughtTogether$'
control "moving customers: a server its machine no longer has loses its record there" internal/panel/moves.go \
  'DELETE FROM server_machines WHERE server_id = ? AND machine_id = ?' \
  'DELETE FROM server_machines WHERE server_id = ? AND machine_id = ? AND 0' \
  ./internal/panel '^TestAMoveThatCantGoOnLeavesItsServerWhereItWas$'
control "moving customers: a machine a move left servers on still has their customer" internal/panel/machinecustomers.go \
  'JOIN customers c ON c.user_id = cs.user_id WHERE sm.machine_id = ?' \
  'JOIN customers c ON c.user_id = cs.user_id WHERE sm.machine_id = ? AND 0' \
  ./internal/panel '^TestAMachineAMoveLeftServersOnCountsTheirCustomer$'
control "moving customers: a moved server keeps what its agent kept about it" internal/panel/moves.go \
  'err = s.copyMoveState(ctx, id, from, to)' \
  'err = nil' \
  ./internal/panel '^TestTheOwnerMovesACustomerAndTheirServerFollows$'
control "moving customers: a server whose settings don't arrive isn't moved" internal/panel/moves.go \
  'return fmt.Errorf("%s didn'"'"'t take what %s kept about it: %w", machineLabel(to), machineLabel(from), err)' \
  'return nil' \
  ./internal/panel '^TestAMoveWhoseSettingsDontArriveIsUndone$'
control "moving in: a server keeps its copies' keys, so they still open" internal/agent/movestate.go \
  '"ssh_public", "keys", "key_saved_at",' \
  '"ssh_public", "key_saved_at",' \
  ./internal/agent '^TestAServerMovedInKeepsItsSettings$'
control "moving in: a server off the public page stays off it" internal/agent/movestate.go \
  '{"servers", []string{"sleep", "public_page", ' \
  '{"servers", []string{"sleep", ' \
  ./internal/agent '^TestAServerMovedInKeepsItsSettings$'
control "moving in: an own address that doesn't fit the machine is left out" internal/agent/movestate.go \
  'if fit, err := s.validOwnAddress(name); err != nil || fit != name {' \
  'if false {' \
  ./internal/agent '^TestAServerMovedInKeepsItsSettings$'
control "moving in: a server's sleep runs by the setting it had, not one read before it arrived" internal/agent/movestate.go \
  '	s.reloadSleep()' \
  '	// s.reloadSleep()' \
  ./internal/agent '^TestAServerMovedInKeepsItsSettings$'
control "moving out: a folder a move can't carry is refused before its first byte" internal/agent/moveout.go \
  '	if _, err := backup.MeasureWhole(s.dataDir(), archiveLimits()); err != nil {' \
  '	if _, err := backup.MeasureWhole(s.dataDir(), archiveLimits()); err != nil && false {' \
  ./internal/agent '^TestAFolderAMoveCantCarryIsRefusedBeforeItStreams$'
control "moving out: the check before a move holds the folder to what a move carries" internal/agent/moveout.go \
  '	size, err := backup.MeasureWhole(s.dataDir(), archiveLimits())' \
  '	size, err := backup.MeasureWhole(s.dataDir(), backup.Limits{})' \
  ./internal/agent '^TestAFolderAMoveCantCarryIsRefusedBeforeItStreams$'
control "whole folders: measured as they arrive, every file a move carries" internal/backup/staging.go \
  '	rels, err := wholeFiles(dataDir)' \
  '	rels, err := archivedFiles(dataDir)' \
  ./internal/backup '^TestAWholeFolderIsMeasuredAsItArrives$'
control "moving customers: a server a move can't carry refuses the move, saying why" internal/panel/moves.go \
  '	case errors.As(err, &ae) && ae.Status == http.StatusConflict:' \
  '	case false && errors.As(err, &ae) && ae.Status == http.StatusConflict:' \
  ./internal/panel '^TestAMoveChecksEveryServerBeforeAnyStops$'
control "moving customers: a machine that doesn't answer for a server's check refuses the move" internal/panel/moves.go \
  '	return uncheckedRefusal(m, name)' \
  '	return nil' \
  ./internal/panel '^TestAMoveChecksEveryServerBeforeAnyStops$'
control "moving customers: a server already where they go doesn't keep the rest from following" internal/panel/moves.go \
  '		if sz := sizes[sid]; sz.refused != nil && sz.machineID != id {' \
  '		if sz := sizes[sid]; sz.refused != nil {' \
  ./internal/panel '^TestAServerAlreadyWhereTheyGoDoesntKeepTheRestFromFollowing$'
control "moving customers: a server that has to go and can't refuses a move to the machine named" internal/panel/moves.go \
  '		if refused := carryRefusal(sizes, target.ID); refused != nil {
			return "", refused
		}
		if need := diskNeed(sizes, target.ID); !diskFits(target, need) {' \
  '		if need := diskNeed(sizes, target.ID); !diskFits(target, need) {' \
  ./internal/panel '^TestAServerAlreadyWhereTheyGoDoesntKeepTheRestFromFollowing$'
control "moving customers: the dashboard's pick passes over a machine a server that can't go would have to go to" internal/panel/moves.go \
  '			if why := carryRefusal(sizes, r.ID); why != nil {' \
  '			if why := carryRefusal(sizes, r.ID); why != nil && false {' \
  ./internal/panel '^(TestTheMachineAServerThatCantGoIsOnStillTakesTheRest|TestAMoveChecksEveryServerBeforeAnyStops)$'
control "moving customers: with no machine left for the dashboard's pick, a server that can't go says why" internal/panel/moves.go \
  '			if target, ok = chooseMachine(others, planMB); !ok && refused != nil {' \
  '			if target, ok = chooseMachine(others, planMB); !ok && refused != nil && false {' \
  ./internal/panel '^TestAMoveChecksEveryServerBeforeAnyStops$'
control "moving customers: a move goes only to a machine with room on its disk" internal/panel/moves.go \
  '		if need := diskNeed(sizes, target.ID); !diskFits(target, need) {' \
  '		if need := diskNeed(sizes, target.ID); false && !diskFits(target, need) {' \
  ./internal/panel '^TestAMoveGoesOnlyWhereTheirServersFitOnDisk$'
control "moving customers: the fullest machine without room on its disk is passed over" internal/panel/moves.go \
  '			return !diskFits(r, diskNeed(sizes, r.ID))' \
  '			return false' \
  ./internal/panel '^TestAMoveGoesOnlyWhereTheirServersFitOnDisk$'
control "moving customers: a machine's disk keeps what it keeps free beside a move" internal/panel/moves.go \
  '	return need == 0 || r.DiskFree != nil && *r.DiskFree >= need+moveDiskReserve' \
  '	return need == 0 || r.DiskFree != nil && *r.DiskFree >= need' \
  ./internal/panel '^TestAMoveGoesOnlyWhereTheirServersFitOnDisk$'
control "moving customers: a move's disk room counts the largest server's upload" internal/panel/moves.go \
  '	return need + upload' \
  '	return need' \
  ./internal/panel '^TestAMoveGoesOnlyWhereTheirServersFitOnDisk$'
control "moving customers: a server that no longer fits by its turn isn't stopped" internal/panel/moves.go \
  '	if err := checkRoomFor(ctx, from, to, id); err != nil {' \
  '	if err := checkRoomFor(ctx, from, to, id); err != nil && false {' \
  ./internal/panel '^TestAServerThatNoLongerFitsIsntStopped$'
control "kept backups: the copy a move left keeps its whole folder" internal/agent/keptbackups.go \
  '		create, need = backup.CreateWhole, size.ArchiveBytes()' \
  '		create, need = backup.Create, size.ArchiveBytes()' \
  ./internal/agent '^TestACopyAMoveLeftKeepsItsWholeFolder$'
control "kept backups: the copy a move left goes even when nothing of it can be kept" internal/agent/servers.go \
  '		case err != nil && keep.whole:' \
  '		case err != nil && keep.whole && false:' \
  ./internal/agent '^TestACopyAMoveLeftKeepsItsWholeFolder$'
control "kept backups: the copy a move left keeps its whole folder or none of its backups" internal/agent/keptbackups.go \
  '	case whole:' \
  '	case whole && false:' \
  ./internal/agent '^TestACopyAMoveLeftKeepsItsWholeFolder$'
control "kept backups: a whole folder is kept only with the days to keep it" internal/agent/keptbackups.go \
  '	case req.KeepFinalBackupDays == 0 && req.KeptFor == "" && !req.KeepWhole:' \
  '	case req.KeepFinalBackupDays == 0 && req.KeptFor == "":' \
  ./internal/agent '^TestDeletingAServerKeepsAFinalBackup$'
control "moving customers: whoever starts or stops a server since its failed move decides whether it runs" internal/panel/moves.go \
  '		s.forwardThen(http.MethodPost, pattern, func(machine, *session, json.RawMessage) { s.forgetRestart(id) })(w, r, sess)' \
  '		s.forwardThen(http.MethodPost, pattern, func(machine, *session, json.RawMessage) { _ = id })(w, r, sess)' \
  ./internal/panel '^TestAServerStoppedSinceItsFailedMoveStaysStopped$'
control "whole folders: a folder whose manifest a move can't carry is refused when measured" internal/backup/staging.go \
  '	if _, err := marshalManifest(m, lim); err != nil {' \
  '	if _, err := marshalManifest(m, lim); err != nil && false {' \
  ./internal/backup '^TestAWholeFolderIsMeasuredAsItArrives$'
control "moving customers: an AI agent's start or stop since a failed move decides whether the server runs" internal/panel/mcp.go \
  '	if server != nil && runTools[tool] {' \
  '	if server != nil && runTools[tool] && false {' \
  ./internal/panel '^TestAServerStoppedSinceItsFailedMoveStaysStopped$'
control "moving customers: a copy a failed move left on the machine counts as the room it frees" internal/panel/moves.go \
  '		if !slices.Contains(sz.copiesOn, id) {' \
  '		if true {' \
  ./internal/panel '^TestACopyAMoveLeftCountsAsTheRoomItFrees$'
control "moving customers: the copy a move left is deleted keeping its whole folder" internal/panel/moves.go \
  '		req.KeepFinalBackupDays, req.KeptFor, req.KeepWhole = days, movedKeptFor(userID), true' \
  '		req.KeepFinalBackupDays, req.KeptFor, req.KeepWhole = days, movedKeptFor(userID), false' \
  ./internal/panel '^TestTheOwnerMovesACustomerAndTheirServerFollows$'
control "moving in: a server keeps Playkeeper's record of the add-ons it installed" internal/agent/movestate.go \
  '	{"addons", []string{' \
  '	// {"addons", []string{' \
  ./internal/agent '^TestAServerMovedInKeepsItsSettings$'
control "moving in: a server is off the public page until its settings arrive" internal/agent/movein.go \
  'name: name, actor: actor, record: offThePage},' \
  'name: name, actor: actor},' \
  ./internal/agent '^TestAServerMovesWithItsWholeFolder$'
control "moving out: a move takes the whole folder, not a backup's" internal/agent/moveout.go \
  'backup.CreateWhole(out,' \
  'backup.Create(out,' \
  ./internal/agent '^TestAServerMovesWithItsWholeFolder$'
control "moving out: only a stopped server's folder goes" internal/agent/moveout.go \
  '} else if running {' \
  '} else if running && false {' \
  ./internal/agent '^TestAServerMovesWithItsWholeFolder$'
control "moving out: a restore refuses a server's whole folder" internal/agent/handlers.go \
  'if st.manifest.Whole {' \
  'if st.manifest.Whole && false {' \
  ./internal/agent '^TestAServerMovesWithItsWholeFolder$'
control "whole archives: a move leaves the RCON password's files behind" internal/backup/archive.go \
  'return name == "eula.txt" || strings.HasPrefix(name, ".rcon-cli") || strings.HasSuffix(name, ".jar")' \
  'return name == "eula.txt" || strings.HasSuffix(name, ".jar")' \
  ./internal/backup '^TestAWholeArchiveCarriesTheWholeServerFolder$'
control "whole archives: a move leaves what's downloaded again behind" internal/backup/archive.go \
  'case e.IsDir() && (skipDirNames[e.Name()] || top && wholeSkippedDirs[e.Name()]):' \
  'case e.IsDir() && skipDirNames[e.Name()]:' \
  ./internal/backup '^TestAWholeArchiveCarriesTheWholeServerFolder$'
control "whole archives: one needs no world" internal/backup/archive.go \
  'if !m.Whole && !containsPrefix(sortedKeys(seen), m.LevelName+"/") {' \
  'if !containsPrefix(sortedKeys(seen), m.LevelName+"/") {' \
  ./internal/backup '^TestAWholeArchiveCarriesTheWholeServerFolder$'
control "whole archives: only one says so needs no world" internal/backup/archive.go \
  'if !m.Whole && !containsPrefix(sortedKeys(seen), m.LevelName+"/") {' \
  'if false && !containsPrefix(sortedKeys(seen), m.LevelName+"/") {' \
  ./internal/backup '^TestMaliciousArchivesAreRefused$'
control "moving customers: an old copy on the machine a server goes to isn't taken for it" internal/panel/moves.go \
  'resume && op != nil && op.ID == mv.madeBy && op.Status == api.OpSucceeded' \
  'op != nil && op.Status == api.OpSucceeded' \
  ./internal/panel '^TestAMoveDeletesAnOldCopyOnTheMachineItGoesTo$'
control "moving customers: the zone names no copy a move makes or leaves on the dashboard's machine" internal/panel/dnsanswers.go \
  'return copyHidden(copies, j.ServerID, now) })' \
  'return copyHidden(copies, j.ServerID, now) && false })' \
  ./internal/panel '^TestTheZoneLeavesOutCopiesAMoveMakesOrLeaves$'
control "moving customers: a move a restart stopped carries on only with the copy it made" internal/panel/moves.go \
  'resume && op != nil && op.ID == mv.madeBy && op.Status == api.OpSucceeded' \
  'resume && op != nil && op.Status == api.OpSucceeded' \
  ./internal/panel '^TestAResumedMoveTakesOnlyTheCopyItMade$'
control "moving customers: the operation making a copy is recorded before it's waited for" internal/panel/moves.go \
  'UPDATE server_moves SET made_by = ? WHERE server_id = ?' \
  'UPDATE server_moves SET made_by = '"''"' WHERE made_by = ? AND server_id = ?' \
  ./internal/panel '^TestNothingReachesAServerWhileItMoves$'
control "moving customers: a move a restart stopped waits for the joined machines it needs" internal/panel/moves.go \
  'if s.moveMachinesUp(ctx, id) {' \
  'if true {' \
  ./internal/panel '^TestAMoveFromAJoinedMachineWaitsForItAfterARestart$'
control "moving customers: a joined machine connecting carries on the moves that waited for it" internal/panel/machines.go \
  's.movesReconnected(e.MachineID)' \
  '_ = e.MachineID' \
  ./internal/panel '^TestAMoveFromAJoinedMachineWaitsForItAfterARestart$'
control "moving customers: a sleeping server is up where it moves" internal/panel/moves.go \
  '(st.Desired == api.DesiredRunning || st.Desired == api.DesiredSleeping) && st.Phase != api.PhaseCrashed' \
  'st.Desired == api.DesiredRunning && st.Phase != api.PhaseCrashed' \
  ./internal/panel '^TestASleepingServerIsUpWhereItMoves$'
control "moving customers: a server a failed move couldn't start again is to start again" internal/panel/moves.go \
  's.pendRestart(ctx, mv)' \
  '_ = mv' \
  ./internal/panel '^TestAServerWhoseMoveFailedStartsAgainOnceItCan$'
control "moving customers: a server to start again starts where it moves" internal/panel/moves.go \
  ' || s.restartPending(ctx, id, from.ID)' \
  '' \
  ./internal/panel '^TestAServerWhoseMoveFailedStartsAgainOnceItCan$'
control "moving customers: a server to start again is started once its machine answers" internal/panel/moves.go \
  '} else if err := startOn(ctx, m, mv.serverID); err == nil {' \
  '} else if startOn(ctx, m, mv.serverID) != nil || true {' \
  ./internal/panel '^TestAServerWhoseMoveFailedStartsAgainOnceItCan$'
control "moving customers: the server's requests go where it moved" internal/panel/moves.go \
  'mv.serverID, to.ID, kept, now); err != nil {' \
  'mv.serverID, mv.from, kept, now); err != nil {' \
  ./internal/panel '^TestTheOwnerMovesACustomerAndTheirServerFollows$'
# shellcheck disable=SC2016
control "moving customers: a moved server's links go with it" internal/panel/moves.go \
  '`UPDATE public_links SET machine_id = ? WHERE server_id = ?`, to.ID, mv.serverID' \
  '`UPDATE public_links SET machine_id = ? WHERE server_id = ?`, mv.from, mv.serverID' \
  ./internal/panel '^TestTheOwnerMovesACustomerAndTheirServerFollows$'
# shellcheck disable=SC2016
control "moving customers: a copy left where a server moves goes with its record" internal/panel/moves.go \
  '`DELETE FROM left_copies WHERE server_id = ? AND machine_id = ?`, mv.serverID, to.ID' \
  '`DELETE FROM left_copies WHERE server_id = ? AND machine_id = ?`, mv.serverID, mv.from' \
  ./internal/panel '^TestAMoveDeletesAnOldCopyOnTheMachineItGoesTo$'
control "moving customers: the machine a server left deletes its copy" internal/panel/moves.go \
  'if err := leftCopy(ctx, c, mv.serverID, mv.from, mv.userID, movedBackupDays, now); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/panel '^TestTheOwnerMovesACustomerAndTheirServerFollows$'
control "moving customers: the copy a server left keeps its final backup a week" internal/panel/moves.go \
  'if err := deleteOn(ctx, m, id, st.Name, days, userID); err != nil {' \
  'if err := deleteOn(ctx, m, id, st.Name, 0, userID); err != nil {' \
  ./internal/panel '^TestTheOwnerMovesACustomerAndTheirServerFollows$'
control "moving customers: nothing deletes the server as a copy left" internal/panel/moves.go \
  '	case busy > 0:' \
  '	case false:' \
  ./internal/panel '^TestALeftCopyThatIsTheServerIsNeverDeleted$'
control "moving customers: a server whose move failed starts again where it was" internal/panel/moves.go \
  'if mv.ran && !s.customerHeld(ctx, mv.userID) {' \
  'if false {' \
  ./internal/panel '^TestAFailedMoveLeavesTheServerWhereItWas$'
control "moving customers: a paused customer's server whose move failed isn't started again" internal/panel/moves.go \
  'if mv.ran && !s.customerHeld(ctx, mv.userID) {' \
  'if mv.ran {' \
  ./internal/panel '^TestAPausedCustomersServerMovesStopped$'
control "moving customers: the machine a failed move was going to deletes its copy" internal/panel/moves.go \
  'if err := leftCopy(ctx, c, mv.serverID, mv.to, mv.userID, 0, 0); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/panel '^TestAFailedMoveLeavesTheServerWhereItWas$'
# shellcheck disable=SC2016
control "moving customers: a listing of the dashboard's machine asked before a server moved there keeps its record" internal/panel/machines.go \
  '`DELETE FROM server_machines WHERE server_id = ? AND machine_id = ? AND seen_at <= ?`, id, m.ID, millis(listedAt)' \
  '`DELETE FROM server_machines WHERE server_id = ? AND machine_id = ? AND 0 <= ?`, id, m.ID, millis(listedAt)' \
  ./internal/panel '^TestTheCopiesAMoveMakesAndLeavesDontCountAsTheServer$'
control "moving customers: a copy a move left on the dashboard's machine gets no requests" internal/panel/machines.go \
  's.listings.note(m.ID, shown)' \
  's.listings.note(m.ID, all)' \
  ./internal/panel '^TestACopyLeftOnTheDashboardsMachineIsNeverTheServer$'
control "removing a machine: its customers are placed again once it's removed" internal/panel/machines.go \
  's.rehomeStranded(context.Background())' \
  '_ = context.Background()' \
  ./internal/panel '^TestARemovedMachinesCustomersGetRoomElsewhere$'
# shellcheck disable=SC2016
control "removing a machine: its customers lose it as their machine" internal/panel/placement.go \
  '_, err = s.db.ExecContext(ctx, `UPDATE customer_homes SET machine_id = '"''"', placed_at = ? WHERE `+stranded, s.now().UnixMilli())' \
  '_ = stranded' \
  ./internal/panel '^TestARemovedMachinesCustomersGetRoomElsewhere$'
control "moving customers: a move with nowhere to go leaves its servers where they were" internal/panel/moves.go \
  's.undoMoves(ctx, userID)' \
  '_ = userID' \
  ./internal/panel '^TestAMoveThatCantGoOnLeavesItsServerWhereItWas$'
control "moving customers: a move a restart stopped whose server's machine doesn't answer leaves it where it was" internal/panel/moves.go \
  'if moving && ctx.Err() == nil {' \
  'if false && ctx.Err() == nil {' \
  ./internal/panel '^TestAMoveThatCantGoOnLeavesItsServerWhereItWas$'
control "moving customers: the copy a move made of a server deleted meanwhile goes" internal/panel/moves.go \
  '// Deleted meanwhile, so the copy this move made goes too.
			s.dropMove(ctx, mv)' \
  '// Deleted meanwhile, so the copy this move made goes too.' \
  ./internal/panel '^TestAMoveThatCantGoOnLeavesItsServerWhereItWas$'
control "removing a machine: a server left on it doesn't stop its customer's move" internal/panel/moves.go \
  'case errors.Is(err, errNotFound):' \
  'case false:' \
  ./internal/panel '^TestARemovedMachinesCustomersGetRoomElsewhere$'
control "moving customers: a customer whose servers are apart gives none more memory" internal/panel/creators.go \
  '(!ok || memoryMB > cur) && s.customerMoving(r.Context(), a.UserID)' \
  '(!ok || memoryMB > cur) && false' \
  ./internal/panel '^TestServersApartAreBroughtTogether$'
control "moving customers: the backups a move keeps aren't a deleted server's" internal/panel/moves.go \
  'req.KeptFor, req.KeepWhole = days, movedKeptFor(userID), true' \
  'req.KeptFor, req.KeepWhole = days, keptFor(userID), true' \
  ./internal/panel '^TestTheOwnerMovesACustomerAndTheirServerFollows$'
control "moving customers: a moved server is listed as it last was until its machine lists it" internal/panel/moves.go \
  'seen_at = excluded.seen_at, disputed_by = '"''" \
  'seen_at = excluded.seen_at, status = '"''"', disputed_by = '"''" \
  ./internal/panel '^TestTheCopiesAMoveMakesAndLeavesDontCountAsTheServer$'
control "removing a machine: only the owner removes one customers are on" internal/panel/machines.go \
  '} else if n > 0 && !sess.Access.owner() {' \
  '} else if n > 0 && false {' \
  ./internal/panel '^TestOnlyTheOwnerMovesCustomers$'
control "moving customers: a move whose switch fails leaves its server where it was" internal/panel/moves.go \
  '		err = s.switchTo(ctx, mv, to, slug)
	}
	if err != nil {
		if ctx.Err() == nil {' \
  '		err = s.switchTo(ctx, mv, to, slug)
	}
	if err != nil {
		if false {' \
  ./internal/panel '^TestAMoveWhoseSwitchFailsLeavesTheServerWhereItWas$'
control "moving customers: a copy left on a removed machine stays recorded" internal/panel/moves.go \
  '	case busy > 0:' \
  '	case busy > 0 || errors.Is(err, errNotFound):' \
  ./internal/panel '^TestACopyLeftOnARemovedMachineIsntTakenForTheServer$'
control "moving customers: a removed machine's host joining again lists its copy as one, not the server" internal/panel/machines.go \
  '				adopted, err := adoptLeftCopy(ctx, c, id, m.ID, sv, ownerOnline)' \
  '				adopted, err := ownerOnline && false, error(nil)' \
  ./internal/panel '^TestACopyLeftOnARemovedMachineIsntTakenForTheServer$'
control "removing a machine: a customer who lost theirs is told there's no room for their servers" internal/panel/readyserver.go \
  '	case toldReady != 0 && !placed && toldWaiting == 0:' \
  '	case false && toldReady != 0 && !placed && toldWaiting == 0:' \
  ./internal/panel '^TestACustomerWhoLostTheirMachineIsToldThereIsNoRoomNotThatTheirServerIsBeingSetUp$'
control "removing a machine: a customer who lost theirs is told once there's room again" internal/panel/readyserver.go \
  '	case toldReady != 0 && placed && toldWaiting != 0:' \
  '	case false && toldReady != 0 && placed && toldWaiting != 0:' \
  ./internal/panel '^TestACustomerWhoLostTheirMachineIsToldThereIsNoRoomNotThatTheirServerIsBeingSetUp$'
control "removing a machine: the round for customers waiting tells one who lost theirs there's no room" internal/panel/readyserver.go \
  '		return s.tellPlaced(ctx, cust, userID, false)' \
  '		return nil' \
  ./internal/panel '^TestACustomerWhoLostTheirMachineIsToldThereIsNoRoomNotThatTheirServerIsBeingSetUp$'
control "removing a machine: a customer who lost theirs is refused a server as having no room, not as being set up" internal/panel/readyserver.go \
  '		return errNoRoomAgain' \
  '		return errWaitingForRoom' \
  ./internal/panel '^TestACustomerWhoLostTheirMachineIsToldThereIsNoRoomNotThatTheirServerIsBeingSetUp$'
control "removing a machine: a customer who lost theirs has a dashboard that says so" internal/panel/server.go \
  'WaitingAgain: s.waitingAgain(context.Background(), a),' \
  'WaitingAgain: false,' \
  ./internal/panel '^TestACustomerWhoLostTheirMachineIsToldThereIsNoRoomNotThatTheirServerIsBeingSetUp$'
webcontrol "removing a machine: Home tells a customer who lost theirs there's no room, not that a server is being set up" web/src/pages/home.tsx \
  "t(again ? 'home.noRoomTitle' : 'home.settingUpTitle')" \
  "t('home.settingUpTitle')" \
  src/pages/pages.test.tsx 'lost their machine'
control "moving customers: a customer whose servers are apart gets their disk once between the machines" internal/panel/disklimits.go \
  '		if in.split[uid] {' \
  '		if false {' \
  ./internal/panel '^TestACustomerWhoseServersAreApartGetsTheirDiskOnce$'
control "moving customers: a copy a move is making isn't its customer's on the machine making it" internal/panel/disklimits.go \
  'if sv.ID != switching && copyHidden(copies, sv.ID, now) {' \
  'if false && sv.ID != switching && copyHidden(copies, sv.ID, now) {' \
  ./internal/panel '^TestACustomerWhoseServersAreApartGetsTheirDiskOnce$'
control "moving customers: the copy about to become the server counts against its customer's limit there" internal/panel/disklimits.go \
  'if sv.ID != switching && copyHidden(copies, sv.ID, now) {' \
  'if copyHidden(copies, sv.ID, now) {' \
  ./internal/panel '^TestTheOwnerMovesACustomerAndTheirServerFollows$'
control "moving customers: a customer's disk isn't split while their move is under way" internal/panel/disklimits.go \
  'return s.serversApart(ctx, userID) && !s.moveUnderWay(ctx, userID)' \
  'return s.serversApart(ctx, userID)' \
  ./internal/panel '^TestACustomerWhoseServersAreApartGetsTheirDiskOnce$'
control "moving customers: a move that stops has what its customer's servers take counted again" internal/panel/moves.go \
  's.audit(placementActor, "customer.move", name, "failed", err.Error())
		s.recountDisk()' \
  's.audit(placementActor, "customer.move", name, "failed", err.Error())' \
  ./internal/panel '^TestAFailedMoveLeavesTheServerWhereItWas$'
control "disk limits: counting again means the next sync counts" internal/panel/disklimits.go \
  '	s.diskUse.at = time.Time{}' \
  '	_ = time.Time{}' \
  ./internal/panel '^TestACustomerWhoseServersAreApartGetsTheirDiskOnce$'
control "disk limits: a sync that counts has a split made from its counts at once" internal/panel/disklimits.go \
  '			if split {' \
  '			if split && false {' \
  ./internal/panel '^TestACustomerWhoseServersAreApartGetsTheirDiskOnce$'
control "disk limits: a count under way when a move ends doesn't stand for the count it asks for" internal/panel/disklimits.go \
  '		if s.diskUse.recounts == recounts {' \
  '		if s.diskUse.recounts == recounts || true {' \
  ./internal/panel '^TestACountUnderWayWhenAMoveEndsIsntTakenForTheCountAfter$'
control "moving customers: a sync of the disk limits during a switch doesn't leave the server out of its limit" internal/panel/disklimits.go \
  '		s.diskSending.Lock()
		got, err := s.sendDiskLimits(ctx, m, in, count, "")
		s.diskSending.Unlock()' \
  '		got, err := s.sendDiskLimits(ctx, m, in, count, "")' \
  ./internal/panel '^TestASyncDuringASwitchLeavesTheServerInItsLimit$'
control "moving customers: a copy left on a removed machine, stopped before its server moved, isn't taken over" internal/panel/moves.go \
  ' AND switched_at > ? ORDER BY switched_at, machine_id LIMIT 1' \
  ' AND switched_at > ? AND 0 = 1 ORDER BY switched_at, machine_id LIMIT 1' \
  ./internal/panel '^TestAServerBothOfWhoseMachinesWereRemovedIsTakenForWhatItIs$'
control "moving customers: the server where it moved, stopped since, isn't taken for its copy" internal/panel/moves.go \
  ' AND switched_at > ? ORDER BY' \
  ' AND switched_at > 0 AND ? > 0 ORDER BY' \
  ./internal/panel '^(TestAServerBothOfWhoseMachinesWereRemovedIsTakenForWhatItIs|TestEachCopyLeftOnARemovedMachineIsTakenOnce)$'
control "moving customers: a server that isn't stopped is never taken for a copy a move left" internal/panel/moves.go \
  '	if sv["phase"] != string(api.PhaseStopped) || err != nil {' \
  '	if err != nil {' \
  ./internal/panel '^TestAServerBothOfWhoseMachinesWereRemovedIsTakenForWhatItIs$'
control "moving customers: a listing is taken for a copy whatever it says only while the server's own machine is online" internal/panel/moves.go \
  '	if isNoRows(err) && ownerOnline {' \
  '	if isNoRows(err) {' \
  ./internal/panel '^(TestAFailedMovesCopyNeverGetsTheServerItselfDeleted|TestEachCopyLeftOnARemovedMachineIsTakenOnce)$'
control "moving customers: while the server's own machine is online, a copy a move left is taken for one whatever it says" internal/panel/machines.go \
  '				ownerOnline := ownerActive && (ownerKind == localKind || s.hub.Connected(owner))' \
  '				ownerOnline := false && ownerActive && (ownerKind == localKind || s.hub.Connected(owner))' \
  ./internal/panel '^TestACopyLeftOnARemovedMachineIsntTakenForTheServer$'
control "moving customers: a disconnected machine's server isn't taken for a copy by what a listing doesn't say" internal/panel/machines.go \
  '				ownerOnline := ownerActive && (ownerKind == localKind || s.hub.Connected(owner))' \
  '				ownerOnline := ownerActive' \
  ./internal/panel '^TestEachCopyLeftOnARemovedMachineIsTakenOnce$'
control "moving customers: a listing that can't surely be told from a copy a move left is disputed, not taken over" internal/panel/machines.go \
  '				if !ownerActive && !unsure {' \
  '				if !ownerActive {' \
  ./internal/panel '^(TestAFailedMovesCopyNeverGetsTheServerItselfDeleted|TestAServerBothOfWhoseMachinesWereRemovedIsTakenForWhatItIs)$'
# shellcheck disable=SC2016
control "moving customers: each copy left on a removed machine is taken once, the others kept" internal/panel/moves.go \
  '	_, err = q.ExecContext(ctx, `DELETE FROM left_copies WHERE server_id = ? AND machine_id = ?`, id, from)' \
  '	_, err = q.ExecContext(ctx, `DELETE FROM left_copies WHERE `+leftOnRemoved+` OR machine_id = ?`, id, from)' \
  ./internal/panel '^TestEachCopyLeftOnARemovedMachineIsTakenOnce$'
control "moving customers: a machine listing a copy it deleted again takes up its own record, not another host's" internal/panel/moves.go \
  '	if added == 0 {' \
  '	if added == 0 && false {' \
  ./internal/panel '^TestAMachineListingItsDeletedCopyAgainKeepsAnotherHostsCopy$'
# shellcheck disable=SC2016
control "moving customers: a copy a machine deleted and lists again is deleted once more" internal/panel/moves.go \
  '		_, err = q.ExecContext(ctx, `UPDATE left_copies SET left_at = 0 WHERE server_id = ? AND machine_id = ?`, id, machineID)' \
  '		_, err = q.ExecContext(ctx, `UPDATE left_copies SET left_at = left_at WHERE server_id = ? AND machine_id = ?`, id, machineID)' \
  ./internal/panel '^TestAMachineListingItsDeletedCopyAgainKeepsAnotherHostsCopy$'
control "moving customers: a customer's disk isn't split while it can't be told whether a move of theirs is under way" internal/panel/moves.go \
  '	return err != nil || n > 0
}' \
  '	return err == nil && n > 0
}' \
  ./internal/panel '^TestADiskSplitNeedsToKnowNoMoveIsUnderWay$'

# AI keys (0.4.9): only admins see, save and remove them; a key must look
# like its provider's; its file and folder are the game user's alone, beside
# data/; only a server with the folder mounts it, read-only, so no other
# server's container changes; the plugin's server gets the folder before it
# starts; a running container without the mount says it waits for a restart.
control "seeing whether a server has an AI key needs the Files tab's rights" internal/panel/aikeys.go \
  '{"GET", "/api/servers/{id}/ai-keys", needSession, actViewFiles,' \
  '{"GET", "/api/servers/{id}/ai-keys", needSession, actView,' \
  ./internal/panel '^TestAIKeysAreForAdmins$'
control "saving an AI key needs the Files tab's rights" internal/panel/aikeys.go \
  '{"PUT", "/api/servers/{id}/ai-keys/{provider}", needSessionCSRF, actEditFiles,' \
  '{"PUT", "/api/servers/{id}/ai-keys/{provider}", needSessionCSRF, actRunServers,' \
  ./internal/panel '^TestAIKeysAreForAdmins$'
control "removing an AI key needs the Files tab's rights" internal/panel/aikeys.go \
  '{"DELETE", "/api/servers/{id}/ai-keys/{provider}", needSessionCSRF, actEditFiles,' \
  '{"DELETE", "/api/servers/{id}/ai-keys/{provider}", needSessionCSRF, actRunServers,' \
  ./internal/panel '^TestAIKeysAreForAdmins$'
control "an AI key that isn't its provider's is refused" internal/agent/aikeys.go \
  'case !strings.HasPrefix(key, p.prefix):' \
  'case false:' \
  ./internal/agent '^TestAnAIKeyIsCheckedWithoutBeingQuoted$'
control "an AI key with spaces or other characters is refused" internal/agent/aikeys.go \
  "case strings.ContainsFunc(key, func(r rune) bool { return r < '!' || r > '~' }):" \
  'case false:' \
  ./internal/agent '^TestAnAIKeyIsCheckedWithoutBeingQuoted$'
control "an AI key's file is the game user's alone to read" internal/agent/aikeys.go \
  'err = f.Chmod(0o400)' \
  'err = f.Chmod(0o644)' \
  ./internal/agent '^TestAnAIKeyIsKeptBesideTheWorldForTheGameUserAlone$'
control "the secrets folder is the game user's alone" internal/agent/aikeys.go \
  'return s.setSecretsMode(0o500)' \
  'return s.setSecretsMode(0o755)' \
  ./internal/agent '^TestAServerWithThePluginMountsItsSecretsFolderFromItsStart$'
control "only a server with its secrets folder mounts it" internal/agent/lifecycle.go \
  'if !setupOnly && s.hasSecretsDir() {' \
  'if !setupOnly {' \
  ./internal/agent '^TestOnlyAServerWithASecretsFolderMountsIt$'
control "the secrets folder is mounted read-only" internal/agent/lifecycle.go \
  'keys := s.secretsDir() + ":" + secretsMount + ":ro"' \
  'keys := s.secretsDir() + ":" + secretsMount' \
  ./internal/agent '^TestOnlyAServerWithASecretsFolderMountsIt$'
control "a server with the AI Build Battle plugin gets its secrets folder before it starts" internal/agent/lifecycle.go \
  'if err := s.prepareSecrets(); err != nil {' \
  'if err := error(nil); err != nil {' \
  ./internal/agent '^TestAServerWithThePluginMountsItsSecretsFolderFromItsStart$'
control "a key saved while the container lacks the mount waits for a restart" internal/agent/aikeys.go \
  'out.Pending = set && s.secretsPending(ctx)' \
  'out.Pending = false' \
  ./internal/agent '^TestAnAIKeyIsKeptBesideTheWorldForTheGameUserAlone$'

if [ "$bad" != 0 ]; then
  echo
  echo "problems: ${#problems[@]} (a STALE control's guard moved, a MISSED one's test passes without it, an INVALID one doesn't build or run)"
  printf '%s\n' "${problems[@]}"
  exit 1
fi
echo "every guard's test failed without it"

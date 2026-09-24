# Verification contract

Do not call Playkeeper working from docs, mock UI, unit tests alone, or a server running only in a developer worktree. The lead agent records exact tested commit, commands, host environment, observed results, screenshots and known gaps in `CURRENT_STATE.md` or a linked PR. Test data is allowed in tests and clearly labelled demos; the production dashboard must not fabricate activity.

## Required stages

1. **Local code quality:** relevant lint, typecheck, build and focused tests for auth/session/CSRF, control-agent allowlist, invalid paths/verbs, concurrent state transitions, EULA gate, preflight refusal and rollback, event deduplication/gaps, backup corruption and restore refusal. Record actual commands once a stack exists.
2. **Isolated integration:** packaged panel+agent+Minecraft runtime from a clean checkout; Docker privileges and ports bounded as designed. Unauthenticated actions fail; game listener and management interface are separately inspected. Confirm startup/restart/reboot, player snapshot and event capture against real server output.
3. **Real play:** client connects to the Minecraft Java server, places a distinctive block/state, friend or second client joins, console and charts reflect observed play. If no real client is available, label this stage blocked—not passed from a status probe.
4. **Recovery:** stop-and-archive with integrity record; export to a second isolated host; restore with safe preview; join and verify distinctive world state. Record actual downtime, archive location, source/destination host and any unrecovered settings (without private addresses/secrets).
5. **Install/UX:** fresh supported Linux VPS preflight/install/uninstall, no interference with existing services, HTTPS+auth, browser walkthrough desktop/narrow, empty/error states, keyboard and basic accessibility checks. Document resource minimums from real hosts.
6. **Release readiness:** security/exposure review, dependency/source licence review, clean backup/rollback, actual installer documentation. Public repo, domain deployment or release requires owner approval.

Each stage may independently be `not started`, `passed with evidence`, or `blocked with reason`. Mocked tests are valid at stage 1, not substitutes for stages 2–5. No paid external infrastructure without authorization.

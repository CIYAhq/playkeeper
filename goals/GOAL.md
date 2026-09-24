# Goal: build Playkeeper's first usable release

You are the lead Cursor Cloud Agent for this private CIYA repository. First read `README.md`, `CURRENT_STATE.md`, `docs/PRODUCT.md`, `docs/ARCHITECTURE.md`, `docs/DESIGN.md`, `docs/VERIFY.md`, `docs/LICENSING.md` and `SECURITY.md`. This is a **spec-only** repository. You own stack choice, implementation and integration, with one canonical implementation branch/PR. Make sensible decisions autonomously; do not stop after writing a plan, a skeleton or a visual mock.

## Outcome

A user with a clean supported Linux VPS can install Playkeeper, sign in over HTTPS, accept Minecraft's EULA, create and run one Minecraft Java/Paper server, invite a friend, view true operating/player activity, create a consistent portable backup, and restore the same world on a second isolated host. The installation and everyday UI should be unusually simple for non-sysadmins; commands and contribution workflow should be reproducible from a fresh checkout. Do not deploy to a real CIYA VPS or publish before approval.

## How to proceed

1. Timebox a risk spike: prove a least-privilege local agent can start/stop/query an isolated Minecraft container and obtain trustworthy player events. Choose one minimal stack and record the decision. No arbitrary shell or web-exposed Docker socket; no generic multi-game abstraction in v1.
2. Deliver a coherent end-to-end vertical slice before expanding: preflight and first-run auth, EULA gate, create/start/join, truthful overview and operational controls. Use focused failing tests before changing behavior; run the actual integrated artifact, not only mocked tests.
3. Add bounded logs/console, metrics and player analytics with provenance and offline gaps. Do not fake production data or overclaim session accuracy.
4. Implement stop-and-archive backup with visible downtime, archive integrity, clear on-host/off-host distinction, guarded restore and rollback archive. Prove a distinctive world state survives restore on a second isolated machine if authorized resources are available. If not, label the exact E2E gate unverified and deliver a repeatable runbook.
5. Polish the *working* Overview, Console, Players, World and onboarding flows using the independently implemented OpenAnalytics-inspired visual direction. Verify desktop and narrow browser screenshots, errors/empty states, and keyboard usability.
6. Update README with exact tested install and developer/contributor commands. Run the validation matrix in `docs/VERIFY.md`, inspect the packaged artifact outside the checkout, record actual observed evidence in `CURRENT_STATE.md`, and return one reviewable PR with concise status, screenshots, commands/results, and blockers.

Optional independent parallel workers may research a bounded area or implement nonoverlapping components, but you remain integration owner. Do not create competing writers against the same branch. Keep commits small and useful; test the merged result.

## Boundaries and stop conditions

No real CIYA VPS/DNS/firewall changes, spending, existing-world migration, public visibility, release, external communications, weakening safeguards or secret sharing without Siya's explicit approval. Use no-cost local/isolated resources only. No third-party CSS/components/assets copied from OpenAnalytics; select a Playkeeper licence with the owner before public contribution/release or protected source reuse. Never invent successful tests, player joins, backup safety or deployment state.

Stop only when the agreed private product is genuinely verified within authorized resources, or a concrete external/resource blocker remains. A Cloud Agent environment lacking Docker or a second host is an evidence boundary, not permission to mark that workflow passed. Ask only for consequential decisions or unavoidable access; ordinary engineering choices are yours.

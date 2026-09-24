# Current state

**As of 2026-09-24 (Asia/Nicosia): FOUNDATION ONLY.**

- Brand: Playkeeper; owner reported purchasing `playkeeper.io`.
- Repository: [CIYAhq/playkeeper](https://github.com/CIYAhq/playkeeper), `main`, verified private on GitHub after the foundation push. The first foundation commit is `0b6ef717f6feddac947348194a5dbe3054cd08b9`.
- Application code: none. Build/test/CI/deployment: none. No game server or VPS was touched.
- Scope and outcome: [product brief](docs/PRODUCT.md); autonomous implementation mandate: [goal](goals/GOAL.md).
- Main unresolved decisions: product licence before public release/outside contributions; supported distro/resource minimum after a spike; off-host backup storage/retention; isolated host access for real second-machine recovery proof.
- Next outcome: one Cursor Cloud Agent lead implements a tested first vertical slice in a PR. It must not claim full product completion from a mocked or localhost-only test.

## Evidence ledger

| Stage | Evidence | Status |
| --- | --- | --- |
| Spec foundation | [Versioned docs on `main`](https://github.com/CIYAhq/playkeeper/tree/main) | Verified private remote, first commit `0b6ef717` |
| Local app build/tests | Commands and logs linked to commit | Not started |
| Isolated Minecraft join | Client/protocol evidence and screenshots | Not started |
| Backup + second-host restore | Archive integrity + distinctive world state after join | Not started |
| Fresh-VPS install/uninstall | Supported host details, smoke and rollback | Not started |
| Public release | Explicit owner approval + licence | Not approved |

Update stage rows only after observing evidence. A planned test is not a passed test. Never put secrets, private server addresses, or player personal data here.

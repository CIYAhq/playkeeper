# Security

Playkeeper will operate game servers and world data on the user's host. Treat the web panel as a sensitive administrative interface, not a public demo. There is no software release or vulnerability-reporting inbox configured yet.

Do **not** publish working exploits, secrets, world backups, player identifiers or host addresses in a GitHub issue. While the repo is private, contact the repository owner through a private channel already available to you. Before public release, the owner should enable GitHub private vulnerability reporting (or publish a dedicated private contact) and update this file with exact supported versions and response expectations.

Implementation constraints: authenticated HTTPS management, fail-closed authorization, local allowlisted privilege boundary, no public Docker socket/host shell/RCON, redacted logs, bounded player-data retention, explicit restore preview and rollback, safe installation on previously unmodified hosts. Security tests and exposure review are described in [docs/VERIFY.md](docs/VERIFY.md).

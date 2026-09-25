// Package mcp is Playkeeper's Model Context Protocol server: the JSON-RPC
// core, a registry of tools with permission scopes, and the Streamable HTTP
// and stdio transports, written from the MCP specification without an SDK.
//
// # Protocol revisions
//
// The server is dual-era. A request whose params._meta names protocol
// 2026-07-28 is served statelessly: it carries its own version and client
// capabilities, server/discover describes the server, and results carry
// resultType "complete" and caching hints. A client that sends initialize
// instead negotiates one of the legacy revisions 2025-11-25, 2025-06-18,
// 2025-03-26 or 2024-11-05 for the rest of the stdio process or HTTP
// session, and gets tool definitions and results shaped for it: tool
// annotations from 2025-03-26 on, titles, output schemas and structured
// content from 2025-06-18 on, and JSON-RPC batches only in 2025-03-26 and
// 2024-11-05. Both eras can be served at the same time.
//
// # Tools
//
// A Tool declares its input schema (a subset of JSON Schema, see Schema), an
// optional output schema, its Effect, and the Scope a caller needs. Scopes
// are hierarchical, owner over manage over read: tools/list shows a caller
// only the tools it may call, and tools/call checks again. Arguments are
// validated before the handler runs. Whatever the model can act on (invalid
// arguments, a missing scope, a rate limit, a timeout or a ToolError from the
// handler) comes back as a result with isError set; unknown tools and
// malformed requests are JSON-RPC errors. Arguments are never logged, and
// handler errors other than ToolError reach the client only as a generic
// message.
//
// # Transports
//
// NewHTTPHandler serves Streamable HTTP on one endpoint. Every request needs
// a bearer token that an Authenticator accepts, browser origins must be
// allowed explicitly, and bodies are size-limited. Responses are single JSON
// objects: the server sends no notifications, so it never needs a stream.
// ServeStdio serves newline-delimited JSON on a pair of streams, for
// "playkeeper mcp" run over SSH.
//
// # Playkeeper tools
//
// This package holds no Playkeeper tools; the panel registers them. The
// proposed set follows, with its scope, effect and the agent route (as of
// v0.3.0) each one wraps. Tools about one server take a required "server"
// argument, the server's id or slug as in decision 0004.
//
//	list_servers           read    read-only    GET /v1/servers, only servers the token's user can see
//	get_server_status      read    read-only    GET /v1/servers/{id}
//	start_server           manage  additive     POST /v1/servers/{id}/start (idempotent)
//	stop_server            manage  destructive  POST /v1/servers/{id}/stop (idempotent)
//	restart_server         manage  destructive  POST /v1/servers/{id}/restart
//	read_console           read    read-only    GET /v1/servers/{id}/logs
//	send_chat_message      manage  additive     POST /v1/servers/{id}/command, "say" only
//	run_console_command    owner   destructive  POST /v1/servers/{id}/command
//	list_online_players    read    read-only    GET /v1/servers/{id}, players
//	list_whitelist         read    read-only    GET /v1/servers/{id}/whitelist
//	add_to_whitelist       manage  additive     POST /v1/servers/{id}/whitelist (idempotent)
//	remove_from_whitelist  manage  destructive  DELETE /v1/servers/{id}/whitelist/{name} (idempotent)
//	list_backups           read    read-only    GET /v1/servers/{id}/backups
//	create_backup          manage  additive     POST /v1/servers/{id}/backups
//	get_operation          read    read-only    GET /v1/operations/{id}, progress of the calls above
//	get_lag_report         read    read-only    wave 3 lag helper, GET /v1/servers/{id}/metrics
//	explain_crash          read    read-only    wave 3 crash helper, GET /v1/servers/{id}/events
//	search_addons          read    read-only    internal/addons (open world), later
//	install_addon          owner   additive     internal/addons (open world), later
//	remove_addon           owner   destructive  internal/addons, later
//
// Restoring or deleting backups, changing settings or versions, and applying
// updates are left out on purpose until clients can ask the user to confirm.
package mcp

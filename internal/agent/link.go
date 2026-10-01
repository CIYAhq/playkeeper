package agent

import "github.com/CIYAhq/playkeeper/internal/machinelink"

// streamed are the routes whose bodies are large or open-ended, or that can
// take more than a minute to answer: backup downloads and uploads, a server's
// folder on its way to another machine, data pack uploads, world uploads,
// checking or using an uploaded world, which reads all of it, the file
// browser's downloads, uploads and saves (up to maxEditBytes), and deleting
// an off-site copy, which waits up to two minutes on its store. Resource
// packs stay with the dashboard's machine, so their upload keeps the link's
// limits.
var streamed = map[string]bool{
	"GET /v1/servers/{id}/files/download":               true,
	"PUT /v1/servers/{id}/files/content":                true,
	"PUT /v1/servers/{id}/files/uploads/{up}/files/{n}": true,
	"GET /v1/servers/{id}/backups/{bid}/download":       true,
	"GET /v1/servers/{id}/move-out":                     true,
	"GET /v1/kept-backups/{kid}/download":               true,
	"POST /v1/servers/{id}/restore/upload":              true,
	"POST /v1/restore/upload":                           true,
	"POST /v1/servers/{id}/datapacks":                   true,
	"PUT /v1/world-imports/{imp}/files/{n}":             true,
	"POST /v1/world-imports/{imp}/inspect":              true,
	"POST /v1/world-imports/{imp}/preview":              true,
	"POST /v1/world-imports/{imp}/apply":                true,
	"POST /v1/world-imports/{imp}/create":               true,
	"DELETE /v1/servers/{id}/offsite/copies/{name}":     true,
}

// socketOnly are the routes only the agent's own socket can answer: the
// public page's ports travel as listening sockets passed over it, which a
// machine link can't carry, and a joined machine runs no panel to serve
// them, nor a dashboard to put on port 443. The Discord webhook's URL goes
// only from the dashboard's machine's agent to its own panel, so no link
// reads a machine's webhook.
var socketOnly = map[string]bool{
	"POST " + pagePortsPath:           true,
	"PUT " + dashboard443Path:         true,
	"POST " + dashboard443ReachedPath: true,
	"GET " + discordWebhookPath:       true,
}

// LinkRoutes is the route table as data, for machine links: a dashboard may
// send a joined machine exactly the requests its own agent serves, less the
// socket-only ones.
func LinkRoutes() []machinelink.Route {
	table := (&Agent{}).routeTable()
	out := make([]machinelink.Route, 0, len(table))
	for _, rt := range table {
		key := rt.Method + " " + rt.Pattern
		if socketOnly[key] {
			continue
		}
		out = append(out, machinelink.Route{Method: rt.Method, Pattern: rt.Pattern, Stream: streamed[key]})
	}
	return out
}

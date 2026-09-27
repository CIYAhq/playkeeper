package agent

import "github.com/CIYAhq/playkeeper/internal/machinelink"

// streamed are the routes whose bodies are large or open-ended, or that can
// take more than a minute to answer: backup downloads and uploads, data and
// resource pack uploads (up to packs.ResourcePackMaxBytes), world uploads,
// and checking or using an uploaded world, which reads all of it.
var streamed = map[string]bool{
	"GET /v1/servers/{id}/backups/{bid}/download": true,
	"POST /v1/servers/{id}/restore/upload":        true,
	"POST /v1/restore/upload":                     true,
	"POST /v1/servers/{id}/datapacks":             true,
	"POST /v1/servers/{id}/resourcepack":          true,
	"PUT /v1/world-imports/{imp}/files/{n}":       true,
	"POST /v1/world-imports/{imp}/inspect":        true,
	"POST /v1/world-imports/{imp}/preview":        true,
	"POST /v1/world-imports/{imp}/apply":          true,
	"POST /v1/world-imports/{imp}/create":         true,
}

// LinkRoutes is the route table as data, for machine links: a dashboard may
// send a joined machine exactly the requests its own agent serves.
func LinkRoutes() []machinelink.Route {
	table := (&Agent{}).routeTable()
	out := make([]machinelink.Route, 0, len(table))
	for _, rt := range table {
		out = append(out, machinelink.Route{Method: rt.Method, Pattern: rt.Pattern, Stream: streamed[rt.Method+" "+rt.Pattern]})
	}
	return out
}

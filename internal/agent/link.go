package agent

import "github.com/CIYAhq/playkeeper/internal/machinelink"

// streamed are the routes whose bodies are large or open-ended: backup
// downloads and uploads.
var streamed = map[string]bool{
	"GET /v1/servers/{id}/backups/{bid}/download": true,
	"POST /v1/servers/{id}/restore/upload":        true,
	"POST /v1/restore/upload":                     true,
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

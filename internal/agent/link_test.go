package agent

import (
	"testing"

	"github.com/CIYAhq/playkeeper/internal/machinelink"
)

func TestLinkRoutesAreTheRouteTable(t *testing.T) {
	routes := LinkRoutes()
	var table []Route
	for _, rt := range (&Agent{}).Routes() {
		if !socketOnly[rt.Method+" "+rt.Pattern] {
			table = append(table, rt)
		}
	}
	if len(routes) != len(table) {
		t.Fatalf("%d link routes for %d agent routes that aren't socket-only", len(routes), len(table))
	}
	for _, r := range routes {
		if socketOnly[r.Method+" "+r.Pattern] {
			t.Fatalf("a machine link offers %s %s, which only the agent's socket can carry", r.Method, r.Pattern)
		}
	}
	if !socketOnly["POST "+pagePortsPath] {
		t.Fatal("the public page's ports aren't socket-only")
	}
	streams := map[string]bool{}
	update := false
	for i, r := range routes {
		if r.Method != table[i].Method || r.Pattern != table[i].Pattern {
			t.Fatalf("link route %d is %s %s, the agent's is %s %s", i, r.Method, r.Pattern, table[i].Method, table[i].Pattern)
		}
		if r.Stream {
			streams[r.Method+" "+r.Pattern] = true
		}
		update = update || (r.Method == "POST" && r.Pattern == "/v1/update/apply")
	}
	if !update {
		t.Error("POST /v1/update/apply is missing, so joined machines could not be updated")
	}
	if len(streams) != len(streamed) {
		t.Errorf("streamed routes %v, want %v", streams, streamed)
	}
	for key := range streamed {
		if !streams[key] {
			t.Errorf("%s is not a streamed route of the table", key)
		}
	}
	id, err := machinelink.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := machinelink.NewHub(machinelink.HubOptions{Identity: id, Store: machinelink.NewMemoryStore(), Routes: routes}); err != nil {
		t.Fatalf("the hub refuses the route table: %v", err)
	}
}

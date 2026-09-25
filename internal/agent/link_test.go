package agent

import (
	"testing"

	"github.com/CIYAhq/playkeeper/internal/machinelink"
)

func TestLinkRoutesAreTheRouteTable(t *testing.T) {
	routes := LinkRoutes()
	table := (&Agent{}).Routes()
	if len(routes) != len(table) {
		t.Fatalf("%d link routes for %d agent routes", len(routes), len(table))
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

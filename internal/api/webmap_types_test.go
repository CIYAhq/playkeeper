package api_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/webmap"
)

// The map's worlds and players come from internal/webmap, which imports
// this package, so they are checked from outside it.
func TestTheDashboardDeclaresOnlyMapFieldsTheAPISends(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "api", "types.ts"))
	if err != nil {
		t.Fatal(err)
	}
	api.CheckDashboardFields(t, string(src), map[string]any{
		"MapWorld": webmap.World{}, "MapWorlds": webmap.Worlds{}, "MapPlayer": webmap.Player{}, "MapPlayers": webmap.Players{},
	}, nil)
}

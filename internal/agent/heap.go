package agent

import (
	"strings"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// maxModJars bounds the mods folder listing that sizes a mod loader's heap.
const maxModJars = 4096

// modJars counts the jars in a mod loader's mods folder: 0 for other types,
// and for a folder that is missing or that Playkeeper won't list.
func (s *server) modJars(sc api.ServerConfig) int {
	if !minecraft.ModLoader(serverTypeOf(sc)) {
		return 0
	}
	d, err := s.gameFiles()
	if err != nil {
		return 0
	}
	defer d.Close()
	es, err := d.ReadDir("mods", maxModJars)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range es {
		if e.Type().IsRegular() && strings.HasSuffix(strings.ToLower(e.Name()), ".jar") {
			n++
		}
	}
	return n
}

// heapMB is the Java heap the server's container gets: for a mod loader the
// one sizeHeap recorded, for Paper, Purpur and Vanilla minecraft.HeapMB's,
// as it always was.
func heapMB(sc api.ServerConfig) int {
	if minecraft.ModLoader(serverTypeOf(sc)) && sc.HeapMB > 0 {
		return sc.HeapMB
	}
	return minecraft.HeapMB(sc.MemoryMB)
}

// sizeHeap sizes a mod loader's heap for the mods it has now and records it,
// before a start defines the container. The container keeps it until the
// next start, so a mod installed while the server runs doesn't change the
// running server's definition.
func (s *server) sizeHeap(sc *api.ServerConfig) error {
	if !minecraft.ModLoader(serverTypeOf(*sc)) {
		return nil
	}
	heap := minecraft.HeapFor(sc.MemoryMB, serverTypeOf(*sc), s.modJars(*sc))
	if heap == sc.HeapMB {
		return nil
	}
	sc.HeapMB = heap
	return s.saveServerConfig(*sc)
}

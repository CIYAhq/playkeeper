package agent

import (
	"context"
	"strconv"
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

// runMemory is the memory budget and Java heap the server's container was
// made with: what a running or crashed server really has, whatever its
// settings say since. Without a container, it's what its settings give the
// next start.
func (s *server) runMemory(ctx context.Context, sc api.ServerConfig) (budgetMB, heap int) {
	budgetMB, heap = sc.MemoryMB, heapMB(sc)
	c, err := s.docker.ContainerInspect(ctx, s.containerName())
	if err != nil {
		return budgetMB, heap
	}
	if c.HostConfig.Memory > 0 {
		budgetMB = int(c.HostConfig.Memory >> 20)
	}
	for _, e := range c.Config.Env {
		if v, ok := strings.CutPrefix(e, "MEMORY="); ok {
			if mb, err := strconv.Atoi(strings.TrimSuffix(v, "M")); err == nil && mb > 0 {
				heap = mb
			}
		}
	}
	return budgetMB, heap
}

// sizeHeap sizes a mod loader's heap for the mods it has now and records it,
// for a start that is about to make the server's container. A start that
// keeps a running container as it is doesn't call it: that container keeps
// the heap it was made with until the next restart, whatever mods were
// installed meanwhile, so the start neither bounces it nor asks for a restart.
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

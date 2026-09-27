package agent

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/pregen"
	"github.com/CIYAhq/playkeeper/internal/webmap"
)

// The map's area. squaremap draws the land that exists, which on a new
// server is little more than spawn. To show more, Playkeeper pre-generates
// a bigger area with Chunky (pregen.go), and squaremap draws its chunks as
// they appear. The area is the server's latest pre-generation, so a size
// chosen on the World tab counts too; choosing one on the Map tab starts,
// replaces or stops it.

// borderFresh is how long a look at the world border stands.
const borderFresh = 5 * time.Minute

// borderLook is how far a server's world border reached when the agent
// last asked.
type borderLook struct {
	radius int
	at     time.Time
}

var (
	reBorderWidth = regexp.MustCompile(`The world border is currently ([0-9]+(?:\.[0-9]+)?) block`)
	reColorCode   = regexp.MustCompile(`§.`)
)

// borderRadius reads the server's answer to "worldborder get", such as
// "The world border is currently 6000 block(s) wide": half the border's
// width, rounded out to whole blocks, or 0 for the border every world
// starts with, at the edge of the world.
func borderRadius(reply string) int {
	m := reBorderWidth.FindStringSubmatch(reColorCode.ReplaceAllString(reply, ""))
	if m == nil {
		return 0
	}
	width, err := strconv.ParseFloat(m[1], 64)
	if err != nil || width <= 0 || width >= 2*pregen.WorldLimit {
		return 0
	}
	return int(math.Ceil(width / 2))
}

// worldBorder is how far the overworld's border reaches from its center,
// in blocks: 0 while it is where every world's starts, or the server can't
// be asked. A look stands for borderFresh.
func (s *server) worldBorder(ctx context.Context) int {
	ms := &s.maps
	ms.mu.Lock()
	b, ok := ms.border[s.id]
	ms.mu.Unlock()
	if ok && s.now().Sub(b.at) < borderFresh {
		return b.radius
	}
	if !s.online(ctx) {
		return 0
	}
	cmd := "worldborder get"
	switch s.serverType(nil) {
	case "paper", "purpur":
		// Paper names Minecraft's own commands so that plugins can't replace them.
		cmd = "minecraft:" + cmd
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := s.rconExec(cctx, cmd)
	if err != nil {
		return 0
	}
	r := borderRadius(out)
	ms.mu.Lock()
	if ms.border == nil {
		ms.border = map[string]borderLook{}
	}
	ms.border[s.id] = borderLook{radius: r, at: s.now()}
	ms.mu.Unlock()
	return r
}

// mapArea is how much of the world the map shows, and the bigger areas it
// could show with what filling each in would take.
func (s *server) mapArea(ctx context.Context) (api.MapArea, error) {
	fill, err := s.pregenView(ctx)
	if err != nil {
		return api.MapArea{}, err
	}
	sc, p, world, err := s.pregenContext()
	if err != nil {
		return api.MapArea{}, err
	}
	task, err := s.lastPregen()
	if err != nil {
		return api.MapArea{}, err
	}
	rate, free, doneRadius, doneBorder := 0.0, int64(-1), 0, 0
	if task != nil {
		rate, doneRadius, doneBorder = task.Rate, task.DoneRadius, task.DoneBorder
	}
	if fill.DiskFreeBytes != nil {
		free = *fill.DiskFreeBytes
	}
	out := api.MapArea{Area: api.MapAreaExplored, Fill: fill, Options: []api.MapAreaOption{}}
	if fill.State != "idle" && fill.Preset != "" {
		out.Area, out.Radius = fill.Preset, fill.Radius
	}
	border := s.worldBorder(ctx)
	costs := slices.Clone(fill.Presets)
	if border >= pregen.MinRadius && border <= pregen.MaxRadius {
		costs = append(costs, pregenCost(api.MapAreaBorder, pregen.BorderPlan(world, border), pregen.DimensionOf(p, s.levelName(*sc), world), rate, free))
	}
	for _, c := range costs {
		// An area stays generated whatever later tasks do, and filling up
		// to the border also fills every size inside it.
		done := c.Radius <= doneRadius
		if c.ID == api.MapAreaBorder {
			done = c.Radius <= doneBorder
		}
		out.Options = append(out.Options, api.MapAreaOption{
			ID: c.ID, Radius: c.Radius, Chunks: c.Chunks, Seconds: c.Seconds, DiskBytes: c.DiskBytes, Fits: c.Fits,
			Done:       done,
			PastBorder: border > 0 && c.ID != api.MapAreaBorder && c.Radius > border,
		})
	}
	return out, nil
}

func (s *server) hMapArea(w http.ResponseWriter, r *http.Request) {
	out, err := s.mapArea(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// hMapAreaSet chooses the map's area while the map is on. Explored only
// stops the area being filled in; a bigger area starts filling it in, in
// place of one under way once it has started. What was generated stays,
// so an area the map has can't be chosen again, and Explored only can't
// be once one is done.
func (s *server) hMapAreaSet(w http.ResponseWriter, r *http.Request) {
	var req api.MapAreaRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	if _, ok := pregen.PresetPlan(req.Area, ""); !ok && req.Area != api.MapAreaExplored && req.Area != api.MapAreaBorder {
		writeError(w, errInvalid("Choose Explored only, a size or the world border."))
		return
	}
	if rec, err := s.activeMap(); err != nil {
		writeError(w, err)
		return
	} else if rec == nil {
		writeError(w, errConflict("Turn on the map first.", ""))
		return
	}
	cur, err := s.mapArea(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	filling := cur.Fill.State == "starting" || cur.Fill.State == "running" || cur.Fill.State == "paused"
	if req.Area == api.MapAreaExplored {
		switch {
		case cur.Fill.State == "finished":
			writeError(w, &apiError{Status: http.StatusConflict, Code: api.CodeAreaOnMap, Msg: "The land generated so far stays on the map.", Hint: "Choose a bigger area to see more of the world."})
			return
		case !filling:
			writeJSON(w, http.StatusOK, cur)
			return
		}
		if err := s.stopFill(r.Context(), actor, cur.Fill.State); err != nil {
			writeError(w, err)
			return
		}
		s.audit(actor, "map.area", "map", "changed", areaText(api.MapAreaExplored, 0))
		out, err := s.mapArea(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	i := slices.IndexFunc(cur.Options, func(o api.MapAreaOption) bool { return o.ID == req.Area })
	if i < 0 {
		writeError(w, &apiError{Status: http.StatusConflict, Code: api.CodeNoBorder, Msg: "This world has no border to fill up to.", Hint: "Choose a size around spawn."})
		return
	}
	opt := cur.Options[i]
	switch {
	case opt.PastBorder:
		writeError(w, &apiError{Status: http.StatusConflict, Code: api.CodePastBorder, Msg: "That area reaches past the world border.", Hint: "Fill up to the border instead."})
		return
	case opt.Done:
		writeError(w, &apiError{Status: http.StatusConflict, Code: api.CodeAreaOnMap, Msg: "The map has that area already.", Hint: "Choose a bigger one to see more of the world."})
		return
	case filling && cur.Area == req.Area:
		writeError(w, &apiError{Status: http.StatusConflict, Code: pregen.CodeAlreadyRunning, Msg: "The map is already being filled in to that area.", Hint: "It pauses while people play and carries on by itself."})
		return
	}
	sc, p, world, err := s.pregenContext()
	if err != nil {
		writeError(w, err)
		return
	}
	plan, _ := pregen.PresetPlan(req.Area, world)
	if req.Area == api.MapAreaBorder {
		plan = pregen.BorderPlan(world, opt.Radius)
	}
	op, err := s.beginPregen(actor, sc, p, plan, req.Area, req.PauseForPlayers, true)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

// stopFill cancels the pre-generation filling the map's area in; what it
// generated stays. One that is still starting has to finish starting.
func (s *server) stopFill(ctx context.Context, actor, state string) error {
	if state == "starting" {
		return s.busyError()
	}
	_, p, _, err := s.pregenContext()
	if err != nil {
		return err
	}
	release, ok := s.holdOpLock()
	if !ok {
		return s.busyError()
	}
	defer release()
	return s.pregenAct(ctx, p, actor, "cancel")
}

// areaText describes a map area for the audit log.
func areaText(area string, radius int) string {
	switch area {
	case api.MapAreaExplored:
		return "explored only"
	case api.MapAreaBorder:
		return fmt.Sprintf("up to the world border, %d blocks", radius)
	}
	return fmt.Sprintf("%s: %d blocks around spawn", area, radius)
}

// drawPregenerated asks squaremap, while the map is on, to draw the land a
// pre-generation just finished. squaremap draws new chunks by itself as
// they are generated, but no more than a few hundred every half minute; a
// full render catches up with the rest, and the Map tab shows its
// progress.
func (s *server) drawPregenerated(ctx context.Context) {
	rec, err := s.activeMap()
	if err != nil || rec == nil || rec.pendingRestart(s.mapLive(ctx, true)) {
		return
	}
	if err := webmap.StartDrawing(ctx, rconConsole{s}, "minecraft:overworld"); err != nil {
		s.log.Warn("could not ask squaremap to draw the pre-generated land", "server", s.id, "err", err)
		return
	}
	s.recordEvent(s.now().UTC(), "map_drawing", "", "playkeeper", "squaremap draws the pre-generated land")
}

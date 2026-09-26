package pregen

import (
	"bufio"
	"os"
	"reflect"
	"testing"
)

// The testdata log is verbatim from a Paper server running Chunky 1.5.3 on
// a square of radius 640. The other lines are Chunky 1.5.3's English
// messages (lang/en.json) behind each platform's log layout.

func TestParsePaperLog(t *testing.T) {
	f, err := os.Open("testdata/paper-chunky.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var evs []Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		e, ok := ParseLine(sc.Text())
		if !ok {
			t.Fatalf("line not recognised: %q", sc.Text())
		}
		evs = append(evs, e)
	}
	if len(evs) != 13 {
		t.Fatalf("got %d events, want 13", len(evs))
	}
	if evs[0].Kind != EventOther || evs[0].Text != "Loading server plugin Chunky v1.5.3" || evs[0].Failed() {
		t.Errorf("plugin loading line = %+v", evs[0])
	}
	want := Progress{World: "world", Chunks: 2, Percent: 0.03, Rate: 1.5, ETASeconds: 1*3600 + 13*60 + 47, ChunkX: 1, ChunkZ: 1}
	if evs[2].Kind != EventProgress || evs[2].World != "world" || *evs[2].Progress != want {
		t.Errorf("first update = %+v %+v, want %+v", evs[2], evs[2].Progress, want)
	}
	if p := evs[5].Progress; p == nil || p.ChunkX != 32 || p.ChunkZ != -1 || p.Chunks != 1313 || p.Percent != 20.01 {
		t.Errorf("update with a negative chunk = %+v", p)
	}
	var last int64
	for _, e := range evs[2:12] {
		if e.Kind != EventProgress || e.Progress.Chunks <= last || e.Progress.ETASeconds < 0 || e.Progress.Finished {
			t.Errorf("update out of order or incomplete: %+v", e.Progress)
		}
		last = e.Progress.Chunks
	}
	done := Progress{World: "world", Chunks: 6561, Percent: 100, ElapsedSeconds: 154, Finished: true}
	if e := evs[12]; e.Kind != EventFinished || e.World != "world" || *e.Progress != done || e.Failed() {
		t.Errorf("finish line = %+v %+v, want %+v", e, e.Progress, done)
	}
}

func TestParseLineLayouts(t *testing.T) {
	started := Event{Kind: EventStarted, World: "world", Shape: "square", Radius: 2500, Text: "Task started in world for the square region centered at 0, 0 with radius 2500."}
	cases := []struct {
		name, line string
		want       Event
	}{
		{"vanilla", "[17:21:18] [Server thread/INFO]: [Chunky] " + started.Text, started},
		{"paper", "[17:21:18 INFO]: [Chunky] " + started.Text, started},
		{"paper console colors", "\x1b[0;37m[17:21:18 INFO]: [Chunky] " + started.Text + "\x1b[m", started},
		{"windows line end", "[17:21:18 INFO]: [Chunky] " + started.Text + "\r\n", started},
		{"no prefix", "[Chunky] " + started.Text, started},
		{
			"fabric",
			"[17:21:18] [Server thread/INFO] (Minecraft) [Chunky] Task started in minecraft:overworld for the circle region centered at 120.5, -64 with radius 1000.",
			Event{Kind: EventStarted, World: "minecraft:overworld", Shape: "circle", CenterX: 120.5, CenterZ: -64, Radius: 1000, Text: "Task started in minecraft:overworld for the circle region centered at 120.5, -64 with radius 1000."},
		},
		{
			"neoforge",
			"[25Sep2026 17:55:41.013] [Server thread/INFO] [net.minecraft.server.MinecraftServer/]: [Chunky] Task finished for minecraft:the_nether. Processed: 99225 chunks (100.00%), Total time: 0:34:22",
			Event{Kind: EventFinished, World: "minecraft:the_nether", Progress: &Progress{World: "minecraft:the_nether", Chunks: 99225, Percent: 100, ElapsedSeconds: 34*60 + 22, Finished: true}, Text: "Task finished for minecraft:the_nether. Processed: 99225 chunks (100.00%), Total time: 0:34:22"},
		},
	}
	for _, c := range cases {
		got, ok := ParseLine(c.line)
		if !ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: ParseLine = %+v, %v\nwant %+v", c.name, got, ok, c.want)
		}
	}
}

func TestParseLineIgnoresOtherLines(t *testing.T) {
	msg := "[Chunky] Task finished for world. Processed: 6561 chunks (100.00%), Total time: 0:02:34"
	for _, line := range []string{
		"",
		"[22:29:05 INFO]: Done (3.021s)! For help, type \"help\"",
		"[22:29:05 INFO]: <Steve> " + msg,
		"[22:29:05 INFO]: [Not Secure] <Steve> " + msg,
		"[22:29:05 INFO]: [Steve] " + msg,
		"[22:29:05] [Server thread/INFO]: <Steve> " + msg,
		"[22:29:05] [Server thread/INFO]: [Steve] " + msg,
		"[22:29:05] [Server thread/INFO] (Minecraft) <Steve> " + msg,
		"[25Sep2026 22:29:05.001] [Server thread/INFO] [net.minecraft.server.MinecraftServer/]: <Steve> " + msg,
		"[22:29:05 INFO]: Steve issued server command: /say " + msg,
		"[Chunky]",
		"[Chunky] ",
		"xx [Chunky] Task paused for world.",
	} {
		if e, ok := ParseLine(line); ok {
			t.Errorf("ParseLine(%q) = %+v, want no Chunky message", line, e)
		}
	}
}

func TestParseMessages(t *testing.T) {
	cases := []struct {
		msg  string
		want Event
	}{
		{"Task started in world for the rectangle region centered at 0, 0 with radius 100, 200.", Event{Kind: EventStarted, World: "world", Shape: "rectangle", Radius: 100}},
		{"Task started in world for the square region centered at 120,5, -64,25 with radius 1000,5.", Event{Kind: EventStarted, World: "world", Shape: "square", CenterX: 120.5, CenterZ: -64.25, Radius: 1000.5}},
		{"Task continuing for world.", Event{Kind: EventContinued, World: "world"}},
		{"Task paused for my.world.", Event{Kind: EventPaused, World: "my.world"}},
		{"Task stopped for world_nether.", Event{Kind: EventStopped, World: "world_nether"}},
		{"Task cancelled for minecraft:the_end.", Event{Kind: EventCancelled, World: "minecraft:the_end"}},
		{"Cancelling all tasks.", Event{Kind: EventCancelled}},
		{"Task already started for world!", Event{Kind: EventAlreadyRunning, World: "world"}},
		{"A task was already started for this world. To continue running it, type '/chunky continue'. To start a new task, type '/chunky confirm'.", Event{Kind: EventConfirmStart}},
		{"Cancelled tasks cannot be continued. If you are sure you want to cancel, type '/chunky confirm'.", Event{Kind: EventConfirmCancel}},
		{"Nothing to confirm!", Event{Kind: EventNothingToConfirm}},
		{"No tasks to pause.", Event{Kind: EventNoTasks, Text: "pause"}},
		{"No tasks to continue.", Event{Kind: EventNoTasks, Text: "continue"}},
		{"No tasks to cancel.", Event{Kind: EventNoTasks, Text: "cancel"}},
		{"No tasks running.", Event{Kind: EventNoTasks, Text: "progress"}},
		{"Your host has limited the maximum pre-generation radius to 10000 to avoid excessive disk space usage. Reduce the radius or contact your host if you wish to have this limit removed.", Event{Kind: EventRadiusLimit, Limit: 10000}},
		{"World changed to world_the_end.", Event{Kind: EventWorldSet, World: "world_the_end"}},
		{"Center changed to 120.5, -64.", Event{Kind: EventCenterSet, CenterX: 120.5, CenterZ: -64}},
		{"Pattern changed to region.", Event{Kind: EventPatternSet, Text: "region"}},
		{"Successfully reloaded configuration.", Event{Kind: EventReloaded}},
		{"The configuration cannot be reloaded while tasks are running.", Event{Kind: EventOther}},
		{"Task running for world. Processed: 0 chunks (0.00%), ETA: 0:00:00, Rate: 0.0 cps, Current: 0, 0", Event{Kind: EventProgress, World: "world", Progress: &Progress{World: "world", ETASeconds: -1}}},
		// Chunky's ETA when nothing was generated during its sample window:
		// Long.MAX_VALUE seconds.
		{"Task running for world. Processed: 12 chunks (0.00%), ETA: 2562047788015215:30:07, Rate: 0.0 cps, Current: -3, 7", Event{Kind: EventProgress, World: "world", Progress: &Progress{World: "world", Chunks: 12, ETASeconds: -1, ChunkX: -3, ChunkZ: 7}}},
		{"Task running for my world. Processed: 1500 chunks (15,12%), ETA: 12:03:09, Rate: 0,4 cps, Current: 10, -2", Event{Kind: EventProgress, World: "my world", Progress: &Progress{World: "my world", Chunks: 1500, Percent: 15.12, Rate: 0.4, ETASeconds: 12*3600 + 3*60 + 9, ChunkX: 10, ChunkZ: -2}}},
	}
	for _, c := range cases {
		got, ok := ParseLine("[Chunky] " + c.msg)
		if c.want.Text == "" {
			c.want.Text = c.msg
		}
		if !ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("message %q\n got %+v %+v\nwant %+v %+v", c.msg, got, got.Progress, c.want, c.want.Progress)
		}
	}
	if e, _ := ParseLine("[Chunky] Your host has limited the maximum pre-generation radius to 5000 to avoid excessive disk space usage."); !e.Failed() {
		t.Error("radius limit does not count as a failure")
	}
}

func TestParseReply(t *testing.T) {
	cases := []struct {
		name, reply string
		want        []EventKind
		worlds      []string
	}{
		{"empty", "", nil, nil},
		{
			"paper confirmation",
			"[Chunky] A task was already started for this world. To continue running it, type '/chunky continue'. To start a new task, type '/chunky confirm'.\n",
			[]EventKind{EventConfirmStart}, []string{""},
		},
		{"paper usage with colors", "§2chunky world <world>§r - Set the world target\n", []EventKind{EventUsage}, []string{""}},
		{"fabric usage", "chunky start - Start a new chunk generation task", []EventKind{EventUsage}, []string{""}},
		{
			"paper pause of every task",
			"[Chunky] Task paused for world.\n[Chunky] Task paused for world_nether.\n",
			[]EventKind{EventPaused, EventPaused}, []string{"world", "world_nether"},
		},
		{
			"vanilla console joins messages",
			"[Chunky] Task paused for minecraft:overworld.[Chunky] Task paused for minecraft:the_nether.",
			[]EventKind{EventPaused, EventPaused}, []string{"minecraft:overworld", "minecraft:the_nether"},
		},
		{
			"fabric progress for two worlds",
			"[Chunky] Task running for minecraft:overworld. Processed: 4410 chunks (4.44%), ETA: 0:40:55, Rate: 38.6 cps, Current: -12, 30[Chunky] Task running for minecraft:the_nether. Processed: 120 chunks (0.12%), ETA: 1:02:03, Rate: 26.1 cps, Current: 2, -5",
			[]EventKind{EventProgress, EventProgress}, []string{"minecraft:overworld", "minecraft:the_nether"},
		},
		{
			"cancel confirmed",
			"[Chunky] Task cancelled for world.\n",
			[]EventKind{EventCancelled}, []string{"world"},
		},
		{"unknown command", "Unknown or incomplete command. See below for error\nchunky progress<--[HERE]", []EventKind{EventUnknownCommand, EventOther}, []string{"", ""}},
		{"unknown command joined", "Unknown or incomplete command. See below for errorchunky progress<--[HERE]", []EventKind{EventUnknownCommand}, []string{""}},
	}
	for _, c := range cases {
		evs := ParseReply(c.reply)
		if len(evs) != len(c.want) {
			t.Errorf("%s: ParseReply = %+v, want kinds %v", c.name, evs, c.want)
			continue
		}
		for i, e := range evs {
			if e.Kind != c.want[i] || e.World != c.worlds[i] {
				t.Errorf("%s: event %d = %s %q, want %s %q", c.name, i, e.Kind, e.World, c.want[i], c.worlds[i])
			}
		}
	}
	evs := ParseReply("[Chunky] Task running for minecraft:overworld. Processed: 4410 chunks (4.44%), ETA: 0:40:55, Rate: 38.6 cps, Current: -12, 30[Chunky] No tasks running.")
	if len(evs) != 2 || evs[0].Progress == nil || evs[0].Progress.ChunkZ != 30 || evs[1].Kind != EventNoTasks {
		t.Errorf("joined progress = %+v", evs)
	}
	if evs := ParseReply("§2chunky world <world>§r - Set the world target"); !evs[0].Failed() {
		t.Error("usage line does not count as a failure")
	}
}

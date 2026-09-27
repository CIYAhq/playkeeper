package pregen

import (
	"regexp"
	"strconv"
	"strings"
)

// EventKind is what a Chunky console message says.
type EventKind string

const (
	// EventProgress is Chunky's periodic update while a task runs.
	EventProgress EventKind = "progress"
	// EventFinished means a task generated its whole area.
	EventFinished  EventKind = "finished"
	EventStarted   EventKind = "started"
	EventContinued EventKind = "continued"
	EventPaused    EventKind = "paused"
	// EventStopped means a task stopped running and was saved: paused,
	// cancelled, or the server stopping.
	EventStopped EventKind = "stopped"
	// EventCancelled has an empty World when every task was cancelled.
	EventCancelled      EventKind = "cancelled"
	EventAlreadyRunning EventKind = "already_running"
	// EventConfirmStart means the world has a saved, unfinished task;
	// "chunky confirm" replaces it with the new one.
	EventConfirmStart EventKind = "confirm_start"
	// EventConfirmCancel asks for "chunky confirm" to cancel for good.
	EventConfirmCancel    EventKind = "confirm_cancel"
	EventNothingToConfirm EventKind = "nothing_to_confirm"
	// EventNoTasks means there was nothing to act on; Text is "pause",
	// "continue", "cancel" or "progress".
	EventNoTasks EventKind = "no_tasks"
	// EventRadiusLimit means the host capped the radius at Limit blocks.
	EventRadiusLimit EventKind = "radius_limit"
	EventWorldSet    EventKind = "world_set"
	EventCenterSet   EventKind = "center_set"
	EventPatternSet  EventKind = "pattern_set"
	EventReloaded    EventKind = "reloaded"
	// EventUsage is a command's usage line, which Chunky prints when it
	// rejects the arguments (such as a world it doesn't know).
	EventUsage EventKind = "usage"
	// EventUnknownCommand means the server has no chunky command.
	EventUnknownCommand EventKind = "unknown_command"
	EventOther          EventKind = "other"
)

// Event is one Chunky message from the console, with the values it carries.
type Event struct {
	Kind  EventKind `json:"kind"`
	World string    `json:"world,omitempty"`
	// Progress is set for EventProgress and EventFinished.
	Progress *Progress `json:"progress,omitempty"`
	// Shape and Radius are set for EventStarted, CenterX and CenterZ for
	// EventStarted and EventCenterSet.
	Shape   string  `json:"shape,omitempty"`
	CenterX float64 `json:"centerX,omitempty"`
	CenterZ float64 `json:"centerZ,omitempty"`
	Radius  float64 `json:"radius,omitempty"`
	// Limit is the host's radius cap in blocks, for EventRadiusLimit.
	Limit float64 `json:"limit,omitempty"`
	// Text is the message without Chunky's prefix and colors.
	Text string `json:"text,omitempty"`
}

// Failed reports whether the message means Chunky couldn't do what it was
// asked: it rejected the arguments, the host limits the radius, or the
// server has no Chunky. Chunky never reports chunks that fail to generate.
func (e Event) Failed() bool {
	return e.Kind == EventUsage || e.Kind == EventRadiusLimit || e.Kind == EventUnknownCommand
}

// Progress is where a task stands, as Chunky last reported it.
type Progress struct {
	World string `json:"world"`
	// Chunks counts the chunks processed so far, including chunks that
	// already existed and were skipped.
	Chunks  int64   `json:"chunks"`
	Percent float64 `json:"percent"`
	// Rate is in chunks a second over the last few seconds.
	Rate float64 `json:"rate"`
	// ETASeconds is Chunky's estimate of the time left, or -1 when it
	// can't tell yet.
	ETASeconds int64 `json:"etaSeconds"`
	// ElapsedSeconds is the task's total run time, reported when it
	// finishes.
	ElapsedSeconds int64 `json:"elapsedSeconds,omitempty"`
	Finished       bool  `json:"finished"`
	// ChunkX and ChunkZ are the chunk processed last, while running.
	ChunkX int `json:"chunkX"`
	ChunkZ int `json:"chunkZ"`
}

const (
	num  = `(-?[0-9]+(?:[.,][0-9]+)?)`
	unum = `([0-9]+(?:[.,][0-9]+)?)`
	hms  = `([0-9]+):([0-9]{2}):([0-9]{2})`
)

var (
	reANSI  = regexp.MustCompile("\x1b\\[[0-9;?]*[A-Za-z]")
	reColor = regexp.MustCompile(`§[0-9A-FK-ORXa-fk-orx]`)
	// A server log line carrying a Chunky message: Paper's "[12:00:00
	// INFO]: ", vanilla's "[12:00:00] [Server thread/INFO]: ", Fabric's
	// "... (Minecraft) " and NeoForge's "... [minecraft/MinecraftServer]: ".
	// The message must follow the prefix directly, so chat ("<Steve> [Chunky]
	// …") doesn't match.
	reLogLine = regexp.MustCompile(`^(?:\[(?:[0-9]{2}[A-Za-z]{3}[0-9]{4} )?[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{3})?(?: [A-Z]+)?\](?: \[[^\]]*\]){0,2}(?: \([^)]*\))?:? )?\[Chunky\] (.+)$`)

	reUpdate           = regexp.MustCompile(`^Task running for (.+)\. Processed: ([0-9]+) chunks \(` + unum + `%\), ETA: ` + hms + `, Rate: ` + unum + ` cps, Current: (-?[0-9]+), (-?[0-9]+)$`)
	reDone             = regexp.MustCompile(`^Task finished for (.+)\. Processed: ([0-9]+) chunks \(` + unum + `%\), Total time: ` + hms + `$`)
	reStarted          = regexp.MustCompile(`^Task started in (.+) for the ([a-z]+) region centered at ` + num + `, ` + num + ` with radius ` + unum + `(?:, ` + unum + `)?\.$`)
	reContinued        = regexp.MustCompile(`^Task continuing for (.+)\.$`)
	rePaused           = regexp.MustCompile(`^Task paused for (.+)\.$`)
	reStopped          = regexp.MustCompile(`^Task stopped for (.+)\.$`)
	reCancelled        = regexp.MustCompile(`^Task cancelled for (.+)\.$`)
	reAlready          = regexp.MustCompile(`^Task already started for (.+)!$`)
	reNoTasks          = regexp.MustCompile(`^No tasks (?:to (pause|continue|cancel)|(running))\.$`)
	reLimit            = regexp.MustCompile(`^Your host has limited the maximum pre-generation radius to ` + unum + ` `)
	reWorldSet         = regexp.MustCompile(`^World changed to (.+)\.$`)
	reCenterSet        = regexp.MustCompile(`^Center changed to ` + num + `, ` + num + `\.$`)
	rePatternSet       = regexp.MustCompile(`^Pattern changed to ([a-z]+)\.$`)
	reUsage            = regexp.MustCompile(`^chunky [a-z]+(?: \S.*)? - \S.*$`)
	reUnknownCommand   = regexp.MustCompile(`^Unknown (?:or incomplete )?command`)
	chunkyPrefix       = "[Chunky] "
	confirmStartText   = "A task was already started for this world."
	confirmCancelText  = "Cancelled tasks cannot be continued."
	nothingConfirmText = "Nothing to confirm!"
	cancelAllText      = "Cancelling all tasks."
	reloadedText       = "Successfully reloaded configuration."
)

// clean removes terminal colors, Minecraft formatting codes and line ends.
func clean(s string) string {
	s = reANSI.ReplaceAllString(s, "")
	s = reColor.ReplaceAllString(s, "")
	return strings.TrimRight(s, " \r\n")
}

// ParseLine reads one line of the server log (or console). It reports
// false for lines that are not Chunky messages; Chunky messages it doesn't
// know come back as EventOther.
func ParseLine(line string) (Event, bool) {
	m := reLogLine.FindStringSubmatch(clean(line))
	if m == nil {
		return Event{}, false
	}
	return parseMessage(m[1]), true
}

// ParseReply reads the output of a chunky console command, which may hold
// several messages: one per line on Paper, run together on Fabric and
// NeoForge, whose remote console doesn't separate messages.
func ParseReply(reply string) []Event {
	var out []Event
	for _, line := range strings.Split(clean(reply), "\n") {
		line = clean(line)
		for len(line) > 0 {
			var piece string
			if rest, ok := strings.CutPrefix(line, chunkyPrefix); ok {
				piece, line = cutNext(rest)
				out = append(out, parseMessage(piece))
				continue
			}
			piece, line = cutNext(line)
			if piece = strings.TrimSpace(piece); piece != "" {
				out = append(out, parseBare(piece))
			}
		}
	}
	return out
}

// cutNext splits s before the next Chunky prefix.
func cutNext(s string) (piece, rest string) {
	if i := strings.Index(s, chunkyPrefix); i >= 0 {
		return strings.TrimRight(s[:i], " "), s[i:]
	}
	return s, ""
}

// parseBare reads output without Chunky's prefix: usage lines and the
// server's own errors.
func parseBare(s string) Event {
	switch {
	case reUsage.MatchString(s):
		return Event{Kind: EventUsage, Text: s}
	case reUnknownCommand.MatchString(s):
		return Event{Kind: EventUnknownCommand, Text: s}
	}
	return Event{Kind: EventOther, Text: s}
}

func parseMessage(msg string) Event {
	e := Event{Kind: EventOther, Text: msg}
	if m := reUpdate.FindStringSubmatch(msg); m != nil {
		p := &Progress{World: m[1], Chunks: atoi64(m[2]), Percent: atof(m[3]), Rate: atof(m[7]), ChunkX: int(atoi64(m[8])), ChunkZ: int(atoi64(m[9]))}
		p.ETASeconds = seconds(m[4], m[5], m[6])
		if p.Rate <= 0 {
			p.ETASeconds = -1
		}
		e.Kind, e.World, e.Progress = EventProgress, m[1], p
		return e
	}
	if m := reDone.FindStringSubmatch(msg); m != nil {
		p := &Progress{World: m[1], Chunks: atoi64(m[2]), Percent: atof(m[3]), Finished: true}
		p.ElapsedSeconds = max(seconds(m[4], m[5], m[6]), 0)
		e.Kind, e.World, e.Progress = EventFinished, m[1], p
		return e
	}
	if m := reStarted.FindStringSubmatch(msg); m != nil {
		e.Kind, e.World, e.Shape = EventStarted, m[1], m[2]
		e.CenterX, e.CenterZ, e.Radius = atof(m[3]), atof(m[4]), atof(m[5])
		return e
	}
	if m := reCenterSet.FindStringSubmatch(msg); m != nil {
		e.Kind, e.CenterX, e.CenterZ = EventCenterSet, atof(m[1]), atof(m[2])
		return e
	}
	if m := reNoTasks.FindStringSubmatch(msg); m != nil {
		e.Kind, e.Text = EventNoTasks, m[1]
		if m[2] != "" {
			e.Text = "progress"
		}
		return e
	}
	if m := reLimit.FindStringSubmatch(msg); m != nil {
		e.Kind, e.Limit = EventRadiusLimit, atof(m[1])
		return e
	}
	if m := rePatternSet.FindStringSubmatch(msg); m != nil {
		e.Kind, e.Text = EventPatternSet, m[1]
		return e
	}
	for _, w := range []struct {
		re   *regexp.Regexp
		kind EventKind
	}{
		{reContinued, EventContinued}, {rePaused, EventPaused}, {reStopped, EventStopped},
		{reCancelled, EventCancelled}, {reAlready, EventAlreadyRunning}, {reWorldSet, EventWorldSet},
	} {
		if m := w.re.FindStringSubmatch(msg); m != nil {
			e.Kind, e.World = w.kind, m[1]
			return e
		}
	}
	switch {
	case strings.HasPrefix(msg, confirmStartText):
		e.Kind = EventConfirmStart
	case strings.HasPrefix(msg, confirmCancelText):
		e.Kind = EventConfirmCancel
	case msg == nothingConfirmText:
		e.Kind = EventNothingToConfirm
	case msg == cancelAllText:
		e.Kind = EventCancelled
	case msg == reloadedText:
		e.Kind = EventReloaded
	}
	return e
}

// atof reads a number Chunky formatted in the server's locale, which may
// use a decimal comma.
func atof(s string) float64 {
	f, _ := strconv.ParseFloat(strings.Replace(s, ",", ".", 1), 64)
	return f
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// seconds adds up Chunky's h:mm:ss, or -1 when the hours are absurd:
// Chunky prints an ETA of about 292 million years when nothing has been
// generated for a while.
func seconds(h, m, s string) int64 {
	hours := atoi64(h)
	if hours < 0 || hours > 100_000 || len(h) > 12 {
		return -1
	}
	return hours*3600 + atoi64(m)*60 + atoi64(s)
}

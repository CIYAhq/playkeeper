package diagnose

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// TickStats is how fast the game loop runs. The game wants TargetTPS ticks
// per second (normally 20), so each tick has 1000/TargetTPS milliseconds;
// MSPT is how long a tick actually takes on average.
type TickStats struct {
	TPS       float64 `json:"tps"`
	MSPT      float64 `json:"mspt,omitempty"`     // 0 when unknown
	MaxMSPT   float64 `json:"max_mspt,omitempty"` // slowest recent tick, 0 when unknown
	TargetTPS float64 `json:"target_tps"`
	Frozen    bool    `json:"frozen,omitempty"`
	Sprinting bool    `json:"sprinting,omitempty"`
}

const (
	cmdTPS       = "tps"
	cmdMSPT      = "mspt"
	cmdTickQuery = "tick query"
)

// TickCommands returns the RCON commands whose replies ReadTicks
// understands. Paper and Purpur answer "tps" and "mspt"; vanilla and modded
// servers answer "tick query" from Minecraft 1.20.3 on. Nil means the server
// has no tick command, and only its "Can't keep up!" warnings tell.
func TickCommands(serverType, mcVersion string) []string {
	switch serverType {
	case "paper", "purpur":
		return []string{cmdTPS, cmdMSPT}
	}
	if mcVersion != "" && minecraft.CompareMinecraft(mcVersion, "1.20.3") >= 0 {
		return []string{cmdTickQuery}
	}
	return nil
}

// ReadTicks turns the replies to TickCommands, keyed by command, into tick
// statistics. ok is false when no reply could be read.
func ReadTicks(replies map[string]string) (st TickStats, ok bool) {
	st.TargetTPS = 20
	if tps, good := ParsePaperTPS(replies[cmdTPS]); good {
		st.TPS, ok = tps[0], true
	}
	if times, good := ParsePaperMSPT(replies[cmdMSPT]); good {
		st.MSPT, st.MaxMSPT = times[2].Avg, times[2].Max
		if !ok {
			st.TPS = tpsFromMSPT(st.MSPT, st.TargetTPS)
		}
		ok = true
	}
	if ok {
		return st, true
	}
	q, good := ParseTickQuery(replies[cmdTickQuery])
	if !good {
		return TickStats{}, false
	}
	st = TickStats{
		MSPT:      q.AvgMS,
		MaxMSPT:   q.P99,
		TargetTPS: q.TargetTPS,
		Frozen:    q.Status == "frozen",
		Sprinting: q.Status == "sprinting",
	}
	st.TPS = tpsFromMSPT(q.AvgMS, q.TargetTPS)
	return st, true
}

func tpsFromMSPT(mspt, target float64) float64 {
	if mspt <= 0 || mspt <= 1000/target {
		return target
	}
	return 1000 / mspt
}

const (
	localeDecimal = `(\d+(?:[.,]\d+)?)`
	msptTriple    = localeDecimal + `/` + localeDecimal + `/` + localeDecimal
)

var (
	reSectionCode = regexp.MustCompile(`§.`)
	rePaperTPS    = regexp.MustCompile(`TPS from last 1m, 5m, 15m: ([^\n]*)`)
	reTPSValue    = regexp.MustCompile(`\*?` + localeDecimal + `\*?`)
	reMSPTHeader  = regexp.MustCompile(`Server tick times \(avg/min/max\) from last 5s, 10s, 1m:`)
	reMSPTValues  = regexp.MustCompile(msptTriple + `,\s*` + msptTriple + `,\s*` + msptTriple)
)

// stripCodes removes the legacy § colour codes Paper puts in RCON replies.
func stripCodes(s string) string { return reSectionCode.ReplaceAllString(s, "") }

// decimal parses numbers from Java's DecimalFormat, which uses the server's
// locale ("19.9" or "19,9").
func decimal(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.Replace(s, ",", ".", 1), 64)
	return v, err == nil
}

// ParsePaperTPS reads the reply to Paper's "tps": the average ticks per
// second over the last 1, 5 and 15 minutes. Older Paper and Spigot builds
// mark values above 20 with "*".
func ParsePaperTPS(reply string) (tps [3]float64, ok bool) {
	m := rePaperTPS.FindStringSubmatch(stripCodes(reply))
	if m == nil {
		return tps, false
	}
	vals := reTPSValue.FindAllStringSubmatch(m[1], -1)
	if len(vals) != 3 {
		return tps, false
	}
	for i, v := range vals {
		if tps[i], ok = decimal(v[1]); !ok {
			return [3]float64{}, false
		}
	}
	return tps, true
}

// TickTimes are the average, fastest and slowest tick in milliseconds.
type TickTimes struct{ Avg, Min, Max float64 }

// ParsePaperMSPT reads the reply to Paper's "mspt": tick times over the last
// 5 seconds, 10 seconds and 1 minute.
func ParsePaperMSPT(reply string) (times [3]TickTimes, ok bool) {
	s := stripCodes(reply)
	h := reMSPTHeader.FindStringIndex(s)
	if h == nil {
		return times, false
	}
	m := reMSPTValues.FindStringSubmatch(s[h[1]:])
	if m == nil {
		return times, false
	}
	var v [9]float64
	for i := range v {
		if v[i], ok = decimal(m[i+1]); !ok {
			return times, false
		}
	}
	for i := range times {
		times[i] = TickTimes{Avg: v[3*i], Min: v[3*i+1], Max: v[3*i+2]}
	}
	return times, true
}

// TickQuery is the reply to vanilla's "tick query" (Minecraft 1.20.3+).
type TickQuery struct {
	Status        string // running, lagging, frozen or sprinting
	TargetTPS     float64
	AvgMS         float64
	TargetMS      float64 // 0 while sprinting
	P50, P95, P99 float64
	Samples       int
}

// Vanilla formats these numbers with Locale.ROOT. Over RCON its messages are
// concatenated without separators; Paper may put line breaks between them.
var (
	reQueryStatus  = regexp.MustCompile(`The game is (running normally|running, but can't keep up with the target tick rate|frozen|sprinting)`)
	reQueryRunning = regexp.MustCompile(`Target tick rate: (\d+(?:\.\d+)?) per second\.\s*Average time per tick: (\d+(?:\.\d+)?)ms \(Target: (\d+(?:\.\d+)?)ms\)`)
	reQuerySprint  = regexp.MustCompile(`Target tick rate: (\d+(?:\.\d+)?) per second \(ignored, reference only\)\.\s*Average time per tick: (\d+(?:\.\d+)?)ms`)
	reQueryPercent = regexp.MustCompile(`Percentiles: P50: (\d+(?:\.\d+)?)ms P95: (\d+(?:\.\d+)?)ms P99: (\d+(?:\.\d+)?)ms(?:, sample|\. Sample): (\d+)`)
)

// ParseTickQuery reads the reply to "tick query". Minecraft 1.20.3 to 1.20.4
// end the percentiles with ", sample:", later versions with ". Sample:".
func ParseTickQuery(reply string) (q TickQuery, ok bool) {
	s := stripCodes(reply)
	if m := reQueryRunning.FindStringSubmatch(s); m != nil {
		q.TargetTPS, _ = strconv.ParseFloat(m[1], 64)
		q.AvgMS, _ = strconv.ParseFloat(m[2], 64)
		q.TargetMS, _ = strconv.ParseFloat(m[3], 64)
	} else if m := reQuerySprint.FindStringSubmatch(s); m != nil {
		q.TargetTPS, _ = strconv.ParseFloat(m[1], 64)
		q.AvgMS, _ = strconv.ParseFloat(m[2], 64)
		q.Status = "sprinting"
	} else {
		return TickQuery{}, false
	}
	if q.TargetTPS <= 0 {
		return TickQuery{}, false
	}
	if m := reQueryStatus.FindStringSubmatch(s); m != nil && q.Status == "" {
		switch {
		case strings.HasPrefix(m[1], "running normally"):
			q.Status = "running"
		case strings.HasPrefix(m[1], "running, but"):
			q.Status = "lagging"
		default:
			q.Status = m[1]
		}
	}
	if m := reQueryPercent.FindStringSubmatch(s); m != nil {
		q.P50, _ = strconv.ParseFloat(m[1], 64)
		q.P95, _ = strconv.ParseFloat(m[2], 64)
		q.P99, _ = strconv.ParseFloat(m[3], 64)
		q.Samples, _ = strconv.Atoi(m[4])
	}
	return q, true
}

// Overload is a "Can't keep up!" warning: the server fell Behind, skipping
// Ticks ticks to catch up. Vanilla, Fabric, NeoForge and Forge log it; current Paper
// does not.
type Overload struct {
	At     time.Time
	Behind time.Duration
	Ticks  int
}

var reOverload = regexp.MustCompile(`^Can't keep up! Is the server overloaded\? Running (\d{1,9})ms or (\d{1,9}) ticks behind$`)

// ParseOverload reads a "Can't keep up!" warning. Only WARN lines count, so
// players cannot fake one from chat.
func ParseOverload(l ConsoleLine) (Overload, bool) {
	ll := splitLog(l.Text)
	if !ll.prefixed || ll.level != "WARN" {
		return Overload{}, false
	}
	m := reOverload.FindStringSubmatch(ll.msg)
	if m == nil {
		return Overload{}, false
	}
	ms, _ := strconv.Atoi(m[1])
	ticks, _ := strconv.Atoi(m[2])
	return Overload{At: l.At, Behind: time.Duration(ms) * time.Millisecond, Ticks: ticks}, true
}

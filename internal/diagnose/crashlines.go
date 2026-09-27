package diagnose

import (
	"regexp"
	"slices"
	"strings"
)

// ShownLine is a console line shown with a crash. The lines of a stack trace
// take the time and level of the log entry they belong to.
type ShownLine struct {
	Time  string `json:"time,omitempty"`  // as the server printed it, e.g. "18:52:40"
	Level string `json:"level,omitempty"` // WARN, ERROR or FATAL; "" for INFO and lines without one
	Text  string `json:"text"`
}

const maxShownLines = 4

var (
	reLogTime  = regexp.MustCompile(`^\[(\d{2}:\d{2}:\d{2})`)
	reStopping = regexp.MustCompile(`^Stopping (?:the )?server$`)
)

// shownLines picks the last lines before the server stopped: the console
// lines the diagnosis quotes, oldest first, then the "Stopping server" that
// followed them. Without quoted lines they are the last lines with a log
// prefix, and a Docker error stands in for a server that printed nothing.
func (c *crashCtx) shownLines(d CrashDiagnosis) []ShownLine {
	idx := slices.Compact(slices.Sorted(slices.Values(c.shown)))
	if len(idx) > maxShownLines-1 {
		idx = idx[len(idx)-(maxShownLines-1):]
	}
	if len(idx) == 0 {
		for i := len(c.split) - 1; i >= 0 && len(idx) < maxShownLines; i-- {
			if c.split[i].prefixed && strings.TrimSpace(c.split[i].msg) != "" {
				idx = append([]int{i}, idx...)
			}
		}
	} else {
		for i := idx[len(idx)-1] + 1; i < len(c.split); i++ {
			if c.split[i].prefixed && reStopping.MatchString(c.split[i].msg) {
				idx = append(idx, i)
				break
			}
		}
	}
	out := make([]ShownLine, 0, len(idx))
	for _, i := range idx {
		out = append(out, c.shownLine(i))
	}
	if len(out) == 0 {
		for _, e := range d.Evidence {
			if e.Kind == EvidenceDockerError {
				out = append(out, ShownLine{Level: "ERROR", Text: e.Text})
			}
		}
	}
	return out
}

func (c *crashCtx) shownLine(i int) ShownLine {
	l := ShownLine{Text: truncate(redact(strings.TrimSpace(c.split[i].msg)), maxEvidenceLen)}
	head := i
	if !c.split[i].prefixed {
		head = c.entryStart(i)
	}
	if h := c.split[head]; h.prefixed {
		if m := reLogTime.FindStringSubmatch(c.lines[head]); m != nil {
			l.Time = m[1]
		}
		if h.level != "INFO" {
			l.Level = h.level
		}
	}
	return l
}

package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronSpec is a parsed five-field cron expression. Matching follows Vixie
// cron: when both day of month and day of week are restricted (neither starts
// with *), a day matches if either does.
type cronSpec struct {
	minute  [60]bool
	hour    [24]bool
	dom     [32]bool
	month   [13]bool
	dow     [7]bool
	domStar bool
	dowStar bool
}

const maxCronLen = 100

var cronMacros = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

type cronField struct {
	name     string
	min, max int
	names    []string
}

var cronFields = [5]cronField{
	{name: "minute", min: 0, max: 59},
	{name: "hour", min: 0, max: 23},
	{name: "day of month", min: 1, max: 31},
	{name: "month", min: 1, max: 12, names: []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}},
	{name: "day of week", min: 0, max: 7, names: []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}},
}

func cronError(format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	return &ValidationError{Field: "timing.cron", Code: "cron_invalid", Msg: msg,
		Hint:   "Cron expressions have five fields: minute, hour, day of month, month and day of week, for example 0 4 * * 1 for Mondays at 04:00.",
		Params: map[string]any{"detail": msg}}
}

func parseCron(expr string) (*cronSpec, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, cronError("Enter a cron expression.")
	}
	if len(expr) > maxCronLen {
		return nil, cronError("Cron expressions can be at most %d characters.", maxCronLen)
	}
	if strings.HasPrefix(expr, "@") {
		macro, ok := cronMacros[strings.ToLower(expr)]
		if !ok {
			if strings.EqualFold(expr, "@reboot") {
				return nil, cronError("@reboot is not a time. Choose a time instead.")
			}
			return nil, cronError("%s is not a cron shortcut. Use @hourly, @daily, @weekly, @monthly or @yearly.", quote(expr))
		}
		expr = macro
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, cronError("A cron expression needs five fields: minute, hour, day of month, month and day of week.")
	}
	var c cronSpec
	sets := [5][]bool{c.minute[:], c.hour[:], c.dom[:], c.month[:], make([]bool, 8)}
	for i, f := range fields {
		if err := parseCronField(f, cronFields[i], sets[i]); err != nil {
			return nil, err
		}
	}
	for d := range 7 {
		c.dow[d] = sets[4][d]
	}
	if sets[4][7] {
		c.dow[0] = true
	}
	c.domStar = strings.HasPrefix(fields[2], "*")
	c.dowStar = strings.HasPrefix(fields[4], "*")
	return &c, nil
}

func parseCronField(s string, f cronField, set []bool) error {
	for item := range strings.SplitSeq(s, ",") {
		if item == "" {
			return cronError("The %s field has an empty item.", f.name)
		}
		rangePart, stepPart, hasStep := strings.Cut(item, "/")
		lo, hi := f.min, f.max
		switch {
		case rangePart == "*":
		case strings.Contains(rangePart, "-"):
			a, b, _ := strings.Cut(rangePart, "-")
			var err error
			if lo, err = cronValue(a, f); err != nil {
				return err
			}
			if hi, err = cronValue(b, f); err != nil {
				return err
			}
			if lo > hi {
				return cronError("The range %s in the %s field goes backwards. Write it from low to high.", quote(rangePart), f.name)
			}
		default:
			v, err := cronValue(rangePart, f)
			if err != nil {
				return err
			}
			lo, hi = v, v
			if hasStep {
				hi = f.max
			}
		}
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepPart)
			if err != nil || n < 1 || n > f.max {
				return cronError("%s is not a valid step for the %s field.", quote(stepPart), f.name)
			}
			step = n
		}
		for v := lo; v <= hi; v += step {
			set[v] = true
		}
	}
	return nil
}

func cronValue(s string, f cronField) (int, error) {
	for i, n := range f.names {
		if strings.EqualFold(s, n) {
			return i + f.min, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < f.min || v > f.max || len(s) > 2 {
		return 0, cronError("%s is not a valid %s (%d to %d).", quote(s), f.name, f.min, f.max)
	}
	return v, nil
}

func (c *cronSpec) dayMatches(day time.Time) bool {
	dom := c.dom[day.Day()]
	dow := c.dow[day.Weekday()]
	if !c.domStar && !c.dowStar {
		return dom || dow
	}
	return dom && dow
}

func (c *cronSpec) next(after time.Time, loc *time.Location) time.Time {
	a := after.In(loc)
	y, m, d := a.Date()
	startHour, startMin := a.Hour(), a.Minute()
	for i := range cronSearchDays {
		day := time.Date(y, m, d+i, 12, 0, 0, 0, time.UTC)
		if !c.month[day.Month()] || !c.dayMatches(day) {
			continue
		}
		// Wall times before after's own resolve to instants no later than
		// after, so the first day starts at after's hour and minute.
		h0, m0 := 0, 0
		if i == 0 {
			h0, m0 = startHour, startMin
		}
		for h := h0; h < 24; h++ {
			if !c.hour[h] {
				continue
			}
			mi := 0
			if h == h0 {
				mi = m0
			}
			for ; mi < 60; mi++ {
				if !c.minute[mi] {
					continue
				}
				if t := resolve(day.Year(), day.Month(), day.Day(), h, mi, loc); t.After(after) {
					return t
				}
			}
		}
	}
	return time.Time{}
}

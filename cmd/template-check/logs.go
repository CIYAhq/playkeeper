package main

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var reDone = regexp.MustCompile(`Done \((\d+(?:\.\d+)?)s\)!`)

// failures are log lines that mean a plugin or mod didn't load, whatever
// the server did next: Paper's, Fabric Loader's and NeoForge's wording.
var failures = []*regexp.Regexp{
	regexp.MustCompile(`Error occurred while enabling`),
	regexp.MustCompile(`Could not load '`),
	regexp.MustCompile(`UnknownDependencyException`),
	regexp.MustCompile(`UnsupportedClassVersionError`),
	regexp.MustCompile(`NoClassDefFoundError`),
	regexp.MustCompile(`Incompatible mods? (?:found|set)`),
	regexp.MustCompile(`requires .* which is missing`),
	regexp.MustCompile(`Mod resolution (?:failed|encountered)`),
	regexp.MustCompile(`Missing or unsupported mandatory dependencies`),
	regexp.MustCompile(`Mod loading has failed`),
	regexp.MustCompile(`This crash report has been saved to`),
}

// harmless are lines that match a failure but don't mean anything failed to
// load, each with why. They never fail a check.
var harmless = []*regexp.Regexp{
	// Lithium looks through each block class for the methods it speeds up.
	// A class it can't read, such as one naming a class only players' game
	// has, only means it leaves that block as it is. Prominence II's BCLib
	// blocks log 36 of these, and its server starts.
	regexp.MustCompile(`Lithium Class Analysis Error: Class \S+ cannot be analysed`),
}

var reError = regexp.MustCompile(`\b(?:ERROR|SEVERE)\]|\[[^\]]*/(?:ERROR|SEVERE)\]`)

// logReport is what a server's log says about a check.
type logReport struct {
	// done is the seconds in "Done (12.005s)!", 0 when it never said it.
	done float64
	// failures are the lines that fail the check; errors, other error
	// lines, only warn.
	failures, errors []string
}

func readLog(lines []string) logReport {
	var r logReport
	for _, line := range lines {
		if m := reDone.FindStringSubmatch(line); m != nil && r.done == 0 {
			r.done, _ = strconv.ParseFloat(m[1], 64)
		}
		failed := false
		if !slices.ContainsFunc(harmless, func(h *regexp.Regexp) bool { return h.MatchString(line) }) {
			for _, f := range failures {
				if f.MatchString(line) {
					r.failures = append(r.failures, clip(line))
					failed = true
					break
				}
			}
		}
		if !failed && reError.MatchString(line) {
			r.errors = append(r.errors, clip(line))
		}
	}
	return r
}

func clip(line string) string {
	line = strings.TrimSpace(line)
	if len(line) > 240 {
		return line[:240] + "…"
	}
	return line
}

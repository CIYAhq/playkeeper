package diskusage

import (
	"cmp"
	"slices"
	"strings"
)

// WayID names a row of "Ways to free space".
type WayID string

const (
	WayOldBackups      WayID = "old_backups"       // backups the backup rules would delete
	WayOldLogs         WayID = "old_logs"          // finished logs older than Options.LogAge
	WayOldCrashReports WayID = "old_crash_reports" // crash reports, Java error logs and memory dumps older than Options.CrashAge
	WayUnusedSoftware  WayID = "unused_software"   // server software for versions the servers no longer run
	WayDownloads       WayID = "downloads"         // files Playkeeper downloads again when they are needed
	WaySetAside        WayID = "set_aside"         // copies of servers' folders that restores and version changes set aside
	WayUnfinished      WayID = "unfinished"        // partial files and restore stages left by operations that stopped
)

// Action is what a way's one button does.
type Action string

const (
	ActionReview Action = "review" // lists the way's candidates, and deletes those the user confirms
	ActionDelete Action = "delete" // deletes all of the way's candidates
	ActionClear  Action = "clear"  // deletes all of them too, for files Playkeeper gets back by itself
)

// SoftwareVersion is a version of server software, such as Paper 1.21.3.
type SoftwareVersion struct {
	Software string `json:"software"`
	Version  string `json:"version"` // the Minecraft version
	// Build is set only for an older build of the version a server runs.
	Build string `json:"build,omitempty"`
}

// Way is a row of "Ways to free space": candidates the page offers
// together, with one button. Nothing is deleted before it is pressed; it
// passes CandidateIDs to Clean, after a review for ActionReview.
type Way struct {
	ID     WayID  `json:"id"`
	Action Action `json:"action"`
	Bytes  int64  `json:"bytes"` // what deleting all of it frees
	// CandidateIDs are its candidates, in the order of Report.Candidates.
	CandidateIDs []string `json:"candidateIds"`
	// ServerIDs are the servers they are from, in the layout's order, and
	// EveryServer says that this is every server, of several.
	ServerIDs   []string `json:"serverIds,omitempty"`
	EveryServer bool     `json:"everyServer,omitempty"`
	// Versions are what unused_software's candidates are for, newest first.
	Versions []SoftwareVersion `json:"versions,omitempty"`
	Params   map[string]string `json:"params,omitempty"`
	Title    string            `json:"title"`
	Text     string            `json:"text"`
}

// wayOf is the row a candidate is in, or "" for a reason no row takes.
func wayOf(r Reason) WayID {
	switch r {
	case ReasonPrunedBackup:
		return WayOldBackups
	case ReasonOldLog:
		return WayOldLogs
	case ReasonOldCrashReport, ReasonJavaErrorLog, ReasonMemoryDump:
		return WayOldCrashReports
	case ReasonUnusedSoftware:
		return WayUnusedSoftware
	case ReasonDownloaded:
		return WayDownloads
	case ReasonLeftoverCopy:
		return WaySetAside
	case ReasonPartialFile, ReasonStaleStage:
		return WayUnfinished
	}
	return ""
}

// actionOf is a row's button: a review for what can't be got back.
func actionOf(id WayID) Action {
	switch id {
	case WayOldLogs, WayOldCrashReports, WayUnusedSoftware, WayUnfinished:
		return ActionDelete
	case WayDownloads:
		return ActionClear
	}
	return ActionReview
}

// ways gathers the candidates into the rows of "Ways to free space", in the
// page's order and leaving out empty rows, and adds up what they free.
func ways(cs []Candidate, servers []Server, o Options) ([]Way, int64) {
	in := map[WayID][]*Candidate{}
	for i := range cs {
		id := wayOf(cs[i].Reason)
		in[id] = append(in[id], &cs[i])
	}
	out := []Way{}
	var freeable int64
	for _, id := range [...]WayID{WayOldBackups, WayOldLogs, WayOldCrashReports, WayUnusedSoftware, WayDownloads, WaySetAside, WayUnfinished} {
		if len(in[id]) == 0 {
			continue
		}
		w := Way{ID: id, Action: actionOf(id), CandidateIDs: make([]string, 0, len(in[id]))}
		from := map[string]bool{}
		for _, c := range in[id] {
			w.Bytes += c.Bytes
			w.CandidateIDs = append(w.CandidateIDs, c.ID)
			from[c.ServerID] = true
		}
		var names []string
		for _, sv := range servers {
			if from[sv.ID] {
				w.ServerIDs = append(w.ServerIDs, sv.ID)
				names = append(names, sv.name())
			}
		}
		w.EveryServer = len(servers) > 1 && len(w.ServerIDs) == len(servers)
		if id == WayUnusedSoftware {
			w.Versions = versionsOf(in[id])
		}
		w.Params, w.Title, w.Text = wayText(w, names, o)
		freeable += w.Bytes
		out = append(out, w)
	}
	return out, freeable
}

// versionsOf is what the unused software cs is for, newest first.
func versionsOf(cs []*Candidate) []SoftwareVersion {
	var vs []SoftwareVersion
	for _, c := range cs {
		v := SoftwareVersion{Software: c.Params["software"], Version: c.Params["version"], Build: c.Params["build"]}
		if v.Version != "" && !slices.Contains(vs, v) {
			vs = append(vs, v)
		}
	}
	slices.SortFunc(vs, func(a, b SoftwareVersion) int {
		return cmp.Or(compareVersions(b.Version, a.Version), compareVersions(b.Build, a.Build), strings.Compare(a.Software, b.Software))
	})
	return vs
}

// compareVersions orders versions part by part, numbers as numbers: 1.21.10
// comes after 1.21.9, and 1.21.4 after 1.21.4-pre1.
func compareVersions(a, b string) int {
	for a != "" || b != "" {
		var x, y string
		x, a, _ = strings.Cut(a, ".")
		y, b, _ = strings.Cut(b, ".")
		if c := comparePart(x, y); c != 0 {
			return c
		}
	}
	return 0
}

func comparePart(x, y string) int {
	nx, rx := splitNumber(x)
	ny, ry := splitNumber(y)
	if c := cmp.Or(cmp.Compare(len(nx), len(ny)), strings.Compare(nx, ny)); c != 0 {
		return c
	}
	switch {
	case rx == ry:
		return 0
	case rx == "":
		return 1
	case ry == "":
		return -1
	}
	return strings.Compare(rx, ry)
}

// splitNumber splits the digits s starts with, without leading zeros, from
// the rest.
func splitNumber(s string) (number, rest string) {
	i := len(s) - len(strings.TrimLeftFunc(s, isDigit))
	return strings.TrimLeft(s[:i], "0"), s[i:]
}

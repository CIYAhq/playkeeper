package diskusage

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"
)

// pageWay is the row of "Ways to free space" each reason is in.
var pageWay = map[Reason]WayID{
	ReasonPrunedBackup:   WayOldBackups,
	ReasonOldLog:         WayOldLogs,
	ReasonOldCrashReport: WayOldCrashReports,
	ReasonJavaErrorLog:   WayOldCrashReports,
	ReasonMemoryDump:     WayOldCrashReports,
	ReasonUnusedSoftware: WayUnusedSoftware,
	ReasonDownloaded:     WayDownloads,
	ReasonLeftoverCopy:   WaySetAside,
	ReasonPartialFile:    WayUnfinished,
	ReasonStaleStage:     WayUnfinished,
}

func TestWays(t *testing.T) {
	m := newMachine(t)
	rep := scanOK(t, m.l, m.options())
	reasons := map[Reason]bool{}
	for _, c := range rep.Candidates {
		reasons[c.Reason] = true
	}
	if got, want := slices.Sorted(maps.Keys(reasons)), slices.Sorted(maps.Keys(pageWay)); !slices.Equal(got, want) {
		t.Fatalf("the machine offers %v, want every reason %v", got, want)
	}

	tests := []struct {
		id      WayID
		action  Action
		servers []string
		every   bool
		params  map[string]string
		title   string
		text    string
	}{
		{WayOldBackups, ActionReview, []string{"a", "b"}, true, map[string]string{"count": "2"}, "Backups beyond your keep rules", "2 old backups. The newest stay."},
		{WayOldLogs, ActionDelete, []string{"a", "b"}, true, map[string]string{"days": "30"}, "Logs older than 30 days", "From every server"},
		{WayOldCrashReports, ActionDelete, []string{"a"}, false, map[string]string{"days": "30"}, "Crash reports older than 30 days", "From Survival"},
		{WayUnusedSoftware, ActionDelete, []string{"a"}, false, nil, "Server versions nobody uses", "Paper 1.21.4 build 200, 1.21.3 and 1.21.1, replaced by updates"},
		{WayDownloads, ActionClear, nil, false, nil, "Downloaded add-on files", "Playkeeper downloads them again when needed"},
		{WaySetAside, ActionReview, []string{"a"}, false, nil, "Server folders set aside by restores and updates", "From Survival"},
		{WayUnfinished, ActionDelete, nil, false, nil, "Unfinished backups and restores", "Left by a backup, download or restore that stopped"},
	}
	if len(rep.Ways) != len(tests) {
		t.Fatalf("%d ways, want %d: %+v", len(rep.Ways), len(tests), rep.Ways)
	}
	in := map[string]WayID{}
	var freeable, all int64
	for i, tt := range tests {
		w := rep.Ways[i]
		if w.ID != tt.id || w.Action != tt.action || !slices.Equal(w.ServerIDs, tt.servers) || w.EveryServer != tt.every ||
			fmt.Sprint(w.Params) != fmt.Sprint(tt.params) || w.Title != tt.title || w.Text != tt.text {
			t.Errorf("way %d:\n got %s %s from %v (every %v) %v %q / %q\nwant %s %s from %v (every %v) %v %q / %q", i,
				w.ID, w.Action, w.ServerIDs, w.EveryServer, w.Params, w.Title, w.Text,
				tt.id, tt.action, tt.servers, tt.every, tt.params, tt.title, tt.text)
		}
		var ids []string
		var bytes int64
		for _, c := range rep.Candidates {
			if pageWay[c.Reason] == w.ID {
				ids = append(ids, c.ID)
				bytes += c.Bytes
			}
		}
		if !slices.Equal(w.CandidateIDs, ids) || w.Bytes != bytes {
			t.Errorf("way %s: %v of %d bytes, want %v of %d", w.ID, w.CandidateIDs, w.Bytes, ids, bytes)
		}
		for _, id := range w.CandidateIDs {
			if other, ok := in[id]; ok {
				t.Errorf("candidate %s is in ways %s and %s", id, other, w.ID)
			}
			in[id] = w.ID
		}
		freeable += w.Bytes
	}
	for _, c := range rep.Candidates {
		all += c.Bytes
	}
	if len(in) != len(rep.Candidates) || rep.Freeable != all || freeable != all {
		t.Errorf("%d of %d candidates in ways, freeable %d, ways add up to %d; want every candidate once, freeing %d", len(in), len(rep.Candidates), rep.Freeable, freeable, all)
	}
	wantVersions := []SoftwareVersion{{"Paper", "1.21.4", "200"}, {"Paper", "1.21.3", ""}, {"Paper", "1.21.1", ""}}
	if got := rep.Ways[3].Versions; !slices.Equal(got, wantVersions) {
		t.Errorf("versions = %v, want %v", got, wantVersions)
	}

	b, err := json.Marshal(rep.Ways[3])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	want := []string{"action", "bytes", "candidateIds", "id", "serverIds", "text", "title", "versions"}
	if got := slices.Sorted(maps.Keys(fields)); !slices.Equal(got, want) {
		t.Errorf("way JSON has %v, want %v: %s", got, want, b)
	}
}

func TestWayTexts(t *testing.T) {
	servers := []Server{{ID: "a", Name: "Survival"}, {ID: "b", Name: "Creative"}, {ID: "c"}, {ID: "d", Name: "Skyblock"}, {ID: "e", Name: "Lobby"}}
	n := 0
	c := func(server string, reason Reason, params map[string]string) Candidate {
		n++
		return Candidate{ID: fmt.Sprintf("%032x", n), ServerID: server, Reason: reason, Usage: Usage{Bytes: int64(n) * 1000}, Params: params}
	}
	sw := func(software, version, build string) map[string]string {
		p := map[string]string{"path": "x"}
		for k, v := range map[string]string{"software": software, "version": version, "build": build} {
			if v != "" {
				p[k] = v
			}
		}
		return p
	}
	paper := func(versions ...string) []Candidate {
		var cs []Candidate
		for _, v := range versions {
			cs = append(cs, c("a", ReasonUnusedSoftware, sw("Paper", v, "")))
		}
		return cs
	}
	tests := []struct {
		name    string
		servers []Server
		cs      []Candidate
		o       Options
		params  map[string]string
		title   string
		text    string
	}{
		{"one old backup", servers, []Candidate{c("b", ReasonPrunedBackup, nil)}, Options{},
			map[string]string{"count": "1"}, "Backups beyond your keep rules", "1 old backup. The newest stay."},
		{"logs of the only server", servers[:1], []Candidate{c("a", ReasonOldLog, nil)}, Options{},
			map[string]string{"days": "30"}, "Logs older than 30 days", "From Survival"},
		{"logs of both servers", servers[:2], []Candidate{c("b", ReasonOldLog, nil), c("a", ReasonOldLog, nil), c("b", ReasonOldLog, nil)}, Options{},
			map[string]string{"days": "30"}, "Logs older than 30 days", "From every server"},
		{"logs of two servers of three", servers[:3], []Candidate{c("c", ReasonOldLog, nil), c("a", ReasonOldLog, nil)}, Options{},
			map[string]string{"days": "30"}, "Logs older than 30 days", "From Survival and c"},
		{"logs of three servers of five", servers, []Candidate{c("d", ReasonOldLog, nil), c("a", ReasonOldLog, nil), c("b", ReasonOldLog, nil)}, Options{},
			map[string]string{"days": "30"}, "Logs older than 30 days", "From Survival, Creative and Skyblock"},
		{"logs of four servers of five", servers, []Candidate{c("a", ReasonOldLog, nil), c("b", ReasonOldLog, nil), c("c", ReasonOldLog, nil), c("e", ReasonOldLog, nil)}, Options{},
			map[string]string{"days": "30"}, "Logs older than 30 days", "From Survival, Creative, c and 1 more"},
		{"logs a day old", servers[:1], []Candidate{c("a", ReasonOldLog, nil)}, Options{LogAge: 24 * time.Hour},
			map[string]string{"days": "1"}, "Logs older than 1 day", "From Survival"},
		{"logs 36 hours old", servers[:1], []Candidate{c("a", ReasonOldLog, nil)}, Options{LogAge: 36*time.Hour + 30*time.Minute},
			map[string]string{"hours": "36"}, "Logs older than 36 hours", "From Survival"},
		{"crash reports of every kind", servers[:1], []Candidate{c("a", ReasonJavaErrorLog, nil), c("a", ReasonMemoryDump, nil), c("a", ReasonOldCrashReport, nil)}, Options{CrashAge: 7 * 24 * time.Hour},
			map[string]string{"days": "7"}, "Crash reports older than 7 days", "From Survival"},
		{"versions newest first", servers, append(paper("1.21.9", "1.21.10", "1.21"), c("a", ReasonUnusedSoftware, sw("Paper", "1.21.10", ""))), Options{},
			nil, "Server versions nobody uses", "Paper 1.21.10, 1.21.9 and 1.21, replaced by updates"},
		{"the design's versions", servers, paper("26.1", "26.1.1"), Options{},
			nil, "Server versions nobody uses", "Paper 26.1.1 and 26.1, replaced by updates"},
		{"many versions", servers, paper("1.21.1", "1.21.2", "1.21.3", "1.21.4", "1.21.5"), Options{},
			nil, "Server versions nobody uses", "Paper 1.21.5, 1.21.4, 1.21.3 and 2 more, replaced by updates"},
		{"a release and its pre-release", servers, paper("1.21.4-pre1", "1.21.4"), Options{},
			nil, "Server versions nobody uses", "Paper 1.21.4 and 1.21.4-pre1, replaced by updates"},
		{"older builds", servers, []Candidate{
			c("a", ReasonUnusedSoftware, sw("Paper", "1.21.3", "")),
			c("a", ReasonUnusedSoftware, sw("Paper", "1.21.4", "180")),
			c("a", ReasonUnusedSoftware, sw("Paper", "1.21.4", "200")),
		}, Options{}, nil, "Server versions nobody uses", "Paper 1.21.4 build 200, 1.21.4 build 180 and 1.21.3, replaced by updates"},
		{"several kinds of software", servers, []Candidate{
			c("a", ReasonUnusedSoftware, sw("Paper", "1.21.2", "")),
			c("b", ReasonUnusedSoftware, sw("Purpur", "1.21.3", "")),
			c("a", ReasonUnusedSoftware, sw("Paper", "1.21.3", "")),
		}, Options{}, nil, "Server versions nobody uses", "Paper 1.21.3, Purpur 1.21.3 and Paper 1.21.2, replaced by updates"},
		{"versions not known", servers, []Candidate{c("a", ReasonUnusedSoftware, sw("", "", ""))}, Options{},
			nil, "Server versions nobody uses", "Replaced by updates"},
		{"copies set aside", servers, []Candidate{c("e", ReasonLeftoverCopy, nil)}, Options{},
			nil, "Server folders set aside by restores and updates", "From Lobby"},
		{"unfinished files", servers, []Candidate{c("", ReasonStaleStage, nil), c("", ReasonPartialFile, nil)}, Options{},
			nil, "Unfinished backups and restores", "Left by a backup, download or restore that stopped"},
	}
	for _, tt := range tests {
		ws, freeable := ways(tt.cs, tt.servers, tt.o.withDefaults())
		var bytes int64
		for _, c := range tt.cs {
			bytes += c.Bytes
		}
		if len(ws) != 1 || freeable != bytes || ws[0].Bytes != bytes || len(ws[0].CandidateIDs) != len(tt.cs) {
			t.Errorf("%s: ways %+v freeing %d; want one way of %d candidates freeing %d", tt.name, ws, freeable, len(tt.cs), bytes)
			continue
		}
		w := ws[0]
		if w.Title != tt.title || w.Text != tt.text || fmt.Sprint(w.Params) != fmt.Sprint(tt.params) {
			t.Errorf("%s:\n got %q / %q %v\nwant %q / %q %v", tt.name, w.Title, w.Text, w.Params, tt.title, tt.text, tt.params)
		}
	}

	ws, freeable := ways([]Candidate{}, servers, Options{}.withDefaults())
	if ws == nil || len(ws) != 0 || freeable != 0 {
		t.Errorf("no candidates: ways %#v freeing %d, want an empty list", ws, freeable)
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.21.10", "1.21.9", 1},
		{"1.21", "1.21.1", -1},
		{"26.1.1", "26.1", 1},
		{"1.21.4", "1.21.4-pre1", 1},
		{"1.21.4-pre2", "1.21.4-pre1", 1},
		{"1.21.04", "1.21.4", 0},
		{"200", "180", 1},
		{"", "180", -1},
		{"1.21.4", "1.21.4", 0},
	}
	for _, tt := range tests {
		if got := compareVersions(tt.a, tt.b); got != tt.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := compareVersions(tt.b, tt.a); got != -tt.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.b, tt.a, got, -tt.want)
		}
	}
}

func TestSoftwareNames(t *testing.T) {
	tests := []struct{ jar, name, version, build string }{
		{"paper-1.21.4-232.jar", "Paper", "1.21.4", "232"},
		{"purpur-1.21.3-2000.jar", "Purpur", "1.21.3", "2000"},
		{"paper-26.1.1.jar", "Paper", "26.1.1", ""},
		{"Folia-1.21.4-10.jar", "Folia", "1.21.4", "10"},
		{"neoforge-21.4.1-server.jar", "NeoForge", "21.4.1", ""},
		{"fabric-server-mc.1.21.4-loader.0.16.10-launcher.1.0.1.jar", "Fabric", "1.21.4", ""},
		{"minecraft_server.1.21.4.jar", "Minecraft", "1.21.4", ""},
		{"paper-1.21.4-!.jar", "Paper", "1.21.4", ""},
	}
	for _, tt := range tests {
		prefix := softwarePrefix(Server{Jar: tt.jar, MinecraftVersion: "1.21.4"})
		if got := softwareName(prefix); got != tt.name {
			t.Errorf("%s: software %q, want %q", tt.jar, got, tt.name)
		}
		if version, build := jarVersion(tt.jar, prefix); version != tt.version || build != tt.build {
			t.Errorf("%s: version %q build %q, want %q and %q", tt.jar, version, build, tt.version, tt.build)
		}
	}
	if got := softwareName("-"); got != "" {
		t.Errorf("softwareName(%q) = %q, want none", "-", got)
	}
}

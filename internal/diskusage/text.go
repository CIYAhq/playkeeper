package diskusage

import (
	"strconv"
	"strings"
	"time"
)

const (
	dayText   = "Monday 2 January 2006"
	dateParam = "2006-01-02"
)

func oldText(reason Reason, name string, at time.Time) (map[string]string, string) {
	params := map[string]string{"file": name, "date": at.Format(dateParam)}
	day := at.Format(dayText)
	switch reason {
	case ReasonOldLog:
		return params, "A server log last written on " + day + ". Old logs only help to look into past problems."
	case ReasonJavaErrorLog:
		return params, "A Java error log from " + day + ", written when the server's Java process crashed. It only helps to look into that crash."
	case ReasonMemoryDump:
		return params, "A memory dump from " + day + ", written when the server ran out of memory. It only helps to look into that problem."
	}
	return params, "A crash report from " + day + ". It only helps to look into that crash."
}

func unusedText(rel, software, version, build string) (map[string]string, string) {
	params := map[string]string{"path": rel}
	if software != "" {
		params["software"] = software
	}
	what := "Server software this server no longer uses"
	switch {
	case build != "":
		params["version"], params["build"] = version, build
		what = software + " " + version + " build " + build + ", which this server no longer uses"
	case version != "":
		params["version"] = version
		what = "Minecraft " + version + " server software this server no longer uses"
	}
	return params, what + " (" + rel + "). It is downloaded again if it's ever needed."
}

func downloadText(name string, at time.Time) (map[string]string, string) {
	return map[string]string{"file": name, "date": at.Format(dateParam)},
		"Downloaded by Playkeeper on " + at.Format(dayText) + ". It is downloaded again if it's ever needed."
}

func leftoverText(why string, at time.Time) (map[string]string, string) {
	params := map[string]string{"why": why, "date": at.Format(dateParam)}
	day := at.Format(dayText)
	switch why {
	case "failed_restore":
		return params, "A restored world that didn't start, set aside on " + day + " so it could be looked into."
	case "failed_update":
		return params, "The server's files as a failed version change left them, set aside on " + day + ". The backup from before the change was put back."
	}
	return params, "The server's files from before a restore on " + day + ". Playkeeper normally deletes this copy once the restore has finished."
}

func partialText(name string, at time.Time) (map[string]string, string) {
	return map[string]string{"file": name, "date": at.Format(dateParam)},
		"An unfinished file from a backup or download that stopped, last written on " + at.Format(dayText) + "."
}

func stageText(id string, at time.Time) (map[string]string, string) {
	return map[string]string{"stage": id, "date": at.Format(dateParam)},
		"A backup unpacked on " + at.Format(dayText) + " for a restore that nobody applied or cancelled."
}

func prunedText(b Backup, loc *time.Location) (map[string]string, string) {
	at := b.CreatedAt.In(loc)
	return map[string]string{"backupId": b.ID, "createdAt": b.CreatedAt.UTC().Format(time.RFC3339)},
		"The backup from " + at.Format(dayText) + " at " + at.Format("15:04") + ", which the backup rules would delete."
}

// wayText is what a row of "Ways to free space" says, as the page words
// it: a title, and a line below it with no full stop at the end. names
// are the servers the row's candidates are from.
func wayText(w Way, names []string, o Options) (params map[string]string, title, text string) {
	from := ""
	switch {
	case w.EveryServer:
		from = "From every server"
	case len(names) > 0:
		from = "From " + listText(names)
	}
	switch w.ID {
	case WayOldBackups:
		n := int64(len(w.CandidateIDs))
		return map[string]string{"count": strconv.FormatInt(n, 10)}, "Backups beyond your keep rules",
			plural(n, "old backup") + ". The newest stay."
	case WayOldLogs:
		p, age := ageText(o.LogAge)
		return p, "Logs older than " + age, from
	case WayOldCrashReports:
		p, age := ageText(o.CrashAge)
		return p, "Crash reports older than " + age, from
	case WayUnusedSoftware:
		return nil, "Server versions nobody uses", versionsText(w.Versions)
	case WayDownloads:
		return nil, "Downloaded add-on files", "Playkeeper downloads them again when needed"
	case WaySetAside:
		return nil, "Server folders set aside by restores and updates", from
	case WayUnfinished:
		return nil, "Unfinished backups and restores", "Left by a backup, download or restore that stopped"
	}
	return nil, "", ""
}

// ageText says an age in days, or in hours when it isn't whole days,
// rounded down so that all that is offered is older than it says.
func ageText(d time.Duration) (map[string]string, string) {
	hours := max(int64(d/time.Hour), 1)
	if hours%24 == 0 {
		days := hours / 24
		return map[string]string{"days": strconv.FormatInt(days, 10)}, plural(days, "day")
	}
	return map[string]string{"hours": strconv.FormatInt(hours, 10)}, plural(hours, "hour")
}

func plural(n int64, unit string) string {
	s := strconv.FormatInt(n, 10) + " " + unit
	if n != 1 {
		s += "s"
	}
	return s
}

// versionsText names software versions: "Paper 1.21.3 and 1.21.1", or with
// several kinds of software "Paper 1.21.3 and Purpur 1.21.1".
func versionsText(vs []SoftwareVersion) string {
	if len(vs) == 0 {
		return "Replaced by updates"
	}
	labels := make([]string, len(vs))
	same := true
	for i, v := range vs {
		labels[i] = v.Version
		if v.Build != "" {
			labels[i] += " build " + v.Build
		}
		same = same && v.Software == vs[0].Software
	}
	if same {
		return strings.TrimSpace(vs[0].Software+" "+listText(labels)) + ", replaced by updates"
	}
	for i, v := range vs {
		labels[i] = strings.TrimSpace(v.Software + " " + labels[i])
	}
	return listText(labels) + ", replaced by updates"
}

// listText joins names as a sentence does, naming at most three: "A and
// B", "A, B and C", "A, B, C and 2 more".
func listText(names []string) string {
	const most = 3
	switch n := len(names); {
	case n == 0:
		return ""
	case n == 1:
		return names[0]
	case n > most:
		return strings.Join(names[:most], ", ") + " and " + strconv.Itoa(n-most) + " more"
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

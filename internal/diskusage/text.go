package diskusage

import "time"

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

func unusedText(rel, version string) (map[string]string, string) {
	params := map[string]string{"path": rel}
	what := "Server software"
	if version != "" {
		params["version"] = version
		what = "Minecraft " + version + " server software"
	}
	return params, what + " this server no longer uses (" + rel + "). It is downloaded again if it's ever needed."
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

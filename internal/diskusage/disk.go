package diskusage

import "os"

// Group is a slice of the Disk space page's bar, and a column of its
// per-server table.
type Group string

const (
	GroupBackups     Group = "backups"      // backups, restore staging, and what backups and restores left behind
	GroupWorlds      Group = "worlds"       // world folders, with all that is in them
	GroupServerFiles Group = "server_files" // server software, add-ons, caches, settings, downloads and the Docker image
	GroupLogs        Group = "logs"         // logs and crash reports
	GroupOther       Group = "other"        // the rest of the used space: other files, and space kept for the system
	GroupFree        Group = "free"         // what can still be written
)

// GroupUsage is the bytes in one group.
type GroupUsage struct {
	Group Group `json:"group"`
	Bytes int64 `json:"bytes"`
}

// Disk is the disk the report measures, in bytes, as the operating system
// reports it and the machine's Disk meter shows it.
type Disk struct {
	Dir   string `json:"dir"` // the folder measured on it
	Total int64  `json:"total"`
	Free  int64  `json:"free"` // what Playkeeper and the servers can still write
	Used  int64  `json:"used"` // Total - Free
	// Bar is the page's stacked bar, in its order: backups, worlds, server
	// files and add-ons, logs, other and free. Only what is on this disk
	// counts in it.
	Bar []GroupUsage `json:"bar"`
}

// group is where the page shows a kind. Settings and other files in a
// server's folders are its server files; other files in the machine's
// shared folders are left to the bar's other.
func group(k Kind, ofServer bool) Group {
	switch k {
	case KindWorld:
		return GroupWorlds
	case KindBackups, KindLeftovers, KindStaging:
		return GroupBackups
	case KindLogs, KindCrashReports:
		return GroupLogs
	case KindSoftware, KindAddons, KindCaches, KindDownloads, KindDockerImage:
		return GroupServerFiles
	case KindOther:
		if ofServer {
			return GroupServerFiles
		}
	}
	return GroupOther
}

func groupBytes(sums map[Group]int64, k kinds, ofServer bool) {
	for kind, u := range k {
		sums[group(kind, ofServer)] += u.Bytes
	}
}

func usageOf(sums map[Group]int64, groups ...Group) []GroupUsage {
	out := make([]GroupUsage, len(groups))
	for i, g := range groups {
		out[i] = GroupUsage{Group: g, Bytes: sums[g]}
	}
	return out
}

// serverGroups is what a server takes in the columns of the per-server
// table.
func serverGroups(k kinds) []GroupUsage {
	sums := map[Group]int64{}
	groupBytes(sums, k, true)
	return usageOf(sums, GroupBackups, GroupWorlds, GroupServerFiles, GroupLogs)
}

// fillBar fills the bar from what the servers and the machine take on the
// disk.
func (d *Disk) fillBar(servers []kinds, machine kinds) {
	sums := map[Group]int64{}
	for _, k := range servers {
		groupBytes(sums, k, true)
	}
	groupBytes(sums, machine, false)
	counted := sums[GroupBackups] + sums[GroupWorlds] + sums[GroupServerFiles] + sums[GroupLogs]
	sums[GroupOther] = max(d.Used-counted, 0)
	sums[GroupFree] = d.Free
	d.Bar = usageOf(sums, GroupBackups, GroupWorlds, GroupServerFiles, GroupLogs, GroupOther, GroupFree)
}

// diskDir is the folder on the disk the report measures.
func (l Layout) diskDir() string {
	switch {
	case l.DiskDir != "":
		return l.DiskDir
	case len(l.Servers) > 0:
		return l.Servers[0].DataDir
	}
	for _, dir := range []string{l.BackupsDir, l.StagingDir, l.DownloadsDir} {
		if dir != "" {
			return dir
		}
	}
	return ""
}

// measureDisk reads how large and how full the disk is, and which device
// it is, so that only what is on it goes into the bar.
func (s *scan) measureDisk(l Layout) {
	dir := l.diskDir()
	if dir == "" {
		return
	}
	free, total, err := s.o.DiskSpace(dir)
	switch {
	case err != nil:
		s.problem("disk_space", dir, "Playkeeper couldn't read the size of the disk that holds "+dir+" ("+why(err)+").")
		return
	case total <= 0:
		s.problem("disk_space", dir, "The disk that holds "+dir+" reported no size, so how full it is isn't known.")
		return
	}
	free = min(max(free, 0), total)
	s.rep.Disk = &Disk{Dir: dir, Total: total, Free: free, Used: total - free}
	if fi, err := os.Stat(dir); err == nil {
		if st := statOf(fi); st.hasDev {
			s.diskDev, s.diskHasDev = st.key.dev, true
		}
	}
}

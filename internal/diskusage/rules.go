package diskusage

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Reason is why a candidate can be deleted.
type Reason string

const (
	ReasonOldLog         Reason = "old_log"          // a finished server log older than Options.LogAge
	ReasonOldCrashReport Reason = "old_crash_report" // a crash report older than Options.CrashAge
	ReasonJavaErrorLog   Reason = "java_error_log"   // a Java crash log (hs_err_pid…) older than Options.CrashAge
	ReasonMemoryDump     Reason = "memory_dump"      // a Java memory dump (.hprof) older than Options.CrashAge
	ReasonUnusedSoftware Reason = "unused_software"  // server software for a version the server no longer runs
	ReasonLeftoverCopy   Reason = "leftover_copy"    // a copy of a server's folder a restore or version change set aside
	ReasonPartialFile    Reason = "partial_file"     // an unfinished file from a backup or download that stopped
	ReasonStaleStage     Reason = "stale_stage"      // a backup unpacked for a restore nobody applied or cancelled
	ReasonPrunedBackup   Reason = "pruned_backup"    // a backup the backup rules would delete
	ReasonDownloaded     Reason = "downloaded"       // a file Playkeeper downloads again when it is needed
)

// Risk is how careful to be before deleting a candidate.
type Risk string

const (
	// RiskLow: the server doesn't need it, or gets it back by itself.
	RiskLow Risk = "low"
	// RiskMedium: it can't be got back, but nothing should need it. A world
	// in use is never a candidate, so there is no higher risk.
	RiskMedium Risk = "medium"
)

func (r Risk) rank() int {
	if r == RiskLow {
		return 0
	}
	return 1
}

// Candidate is something Clean can delete to free space.
type Candidate struct {
	// ID names the candidate for Clean. It stays the same from scan to scan
	// while the file or folder is unchanged.
	ID       string `json:"id"`
	ServerID string `json:"serverId,omitempty"` // empty for what belongs to no one server
	Kind     Kind   `json:"kind"`
	Reason   Reason `json:"reason"`
	Risk     Risk   `json:"risk"`
	Path     string `json:"path"`
	BackupID string `json:"backupId,omitempty"` // for a pruned backup
	Usage           // what deleting it frees
	// ModifiedAt is the latest change to anything in it.
	ModifiedAt time.Time         `json:"modifiedAt"`
	Params     map[string]string `json:"params,omitempty"`
	Text       string            `json:"text"`

	root     string      // the layout folder it was found under
	rootInfo fs.FileInfo // that folder, as the scan saw it
	rel      string      // its path from root, with forward slashes
	info     fs.FileInfo // itself, as the scan saw it
	guard    bool        // in a server's data folder: no folder on the way may hold a world
}

// pending is a candidate before its size is known.
type pending struct {
	c      Candidate
	minAge time.Duration // nothing in it may have changed more recently
}

func (s *scan) candidate(serverID string, kind Kind, reason Reason, risk Risk, params map[string]string, text string) *pending {
	return &pending{c: Candidate{ServerID: serverID, Kind: kind, Reason: reason, Risk: risk, Params: params, Text: text}}
}

// offer makes p a candidate for the entry e, which holds es, unless it
// wasn't counted completely, is or holds a world, is neither a plain file
// nor a folder, or changed too recently.
func (s *scan) offer(p *pending, e *entry, es sub) {
	mode := e.info.Mode()
	switch {
	case es.partial || es.world || es.worlds:
		return
	case !mode.IsRegular() && !mode.IsDir():
		return
	case p.minAge > 0 && s.now.Sub(es.newest) < p.minAge:
		return
	}
	c := p.c
	c.Path = s.path(e.rel)
	c.Usage, c.ModifiedAt = es.Usage, es.newest
	c.root, c.rootInfo, c.rel, c.info, c.guard = s.root, s.rootInfo, e.rel, e.info, s.worlds
	c.ID = c.makeID()
	s.rep.Candidates = append(s.rep.Candidates, c)
}

func (c *Candidate) makeID() string {
	st := statOf(c.info)
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%d",
		c.Reason, c.root, c.rel, c.BackupID, st.key.dev, st.key.ino, c.info.Size(), c.info.ModTime().UnixNano()))
	return hex.EncodeToString(sum[:16])
}

func validID(id string) bool { return len(id) == 32 && lowerHex(id) }

func lowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		if !(s[i] >= '0' && s[i] <= '9' || s[i] >= 'a' && s[i] <= 'f') {
			return false
		}
	}
	return true
}

// sortCandidates puts the safest first, then the largest.
func sortCandidates(cs []Candidate) {
	slices.SortFunc(cs, func(a, b Candidate) int {
		return cmp.Or(
			cmp.Compare(a.Risk.rank(), b.Risk.rank()),
			cmp.Compare(b.Bytes, a.Bytes),
			strings.Compare(a.Path, b.Path),
			strings.Compare(a.ID, b.ID),
		)
	})
}

// dataVisit says what the entries of a server's data folder are, and which
// can go. A busy server's are only counted.
func (s *scan) dataVisit(sv Server) visitFunc {
	v := s.dataRules(sv)
	if sv.Busy {
		return countOnly(v)
	}
	return v
}

// countOnly visits like v but offers nothing.
func countOnly(v visitFunc) visitFunc {
	return func(e *entry) (Kind, visitFunc, *pending) {
		k, next, _ := v(e)
		if next != nil {
			next = countOnly(next)
		}
		return k, next, nil
	}
}

func (s *scan) dataRules(sv Server) visitFunc {
	prefix := softwarePrefix(sv)
	return func(e *entry) (Kind, visitFunc, *pending) {
		if e.info.IsDir() {
			switch e.name {
			case "logs":
				return KindLogs, s.logsVisit(sv), nil
			case "debug":
				return KindLogs, nil, nil
			case "crash-reports":
				return KindCrashReports, s.crashVisit(sv), nil
			case "libraries":
				return KindSoftware, nil, nil
			case "versions":
				if prefix == "" {
					return KindSoftware, nil, nil
				}
				return KindSoftware, s.versionsVisit(sv), nil
			case "cache":
				if prefix == "" {
					return KindCaches, nil, nil
				}
				return KindCaches, s.cacheVisit(sv), nil
			case "plugins", "mods":
				return KindAddons, nil, nil
			}
			return KindOther, nil, nil
		}
		regular := e.info.Mode().IsRegular()
		switch {
		case strings.HasSuffix(e.name, ".jar"):
			if regular && prefix != "" && e.name != sv.Jar && sameSoftware(e.name, prefix) {
				version, build := jarVersion(e.name, prefix)
				if version != sv.MinecraftVersion {
					build = ""
				}
				return KindSoftware, nil, s.unused(sv, KindSoftware, e, version, build)
			}
			return KindSoftware, nil, nil
		case (strings.HasPrefix(e.name, "hs_err_pid") || strings.HasPrefix(e.name, "replay_pid")) && strings.HasSuffix(e.name, ".log"):
			return KindCrashReports, nil, s.old(sv, ReasonJavaErrorLog, e)
		case strings.HasSuffix(e.name, ".hprof"):
			return KindCrashReports, nil, s.old(sv, ReasonMemoryDump, e)
		}
		return KindOther, nil, nil
	}
}

// logsVisit offers finished logs; latest.log and debug.log are still
// written to.
func (s *scan) logsVisit(sv Server) visitFunc {
	return func(e *entry) (Kind, visitFunc, *pending) {
		if e.info.Mode().IsRegular() && e.name != "latest.log" && e.name != "debug.log" &&
			(strings.HasSuffix(e.name, ".log") || strings.HasSuffix(e.name, ".gz")) {
			return KindLogs, nil, s.old(sv, ReasonOldLog, e)
		}
		return KindLogs, nil, nil
	}
}

func (s *scan) crashVisit(sv Server) visitFunc {
	return func(e *entry) (Kind, visitFunc, *pending) {
		if e.info.Mode().IsRegular() {
			return KindCrashReports, nil, s.old(sv, ReasonOldCrashReport, e)
		}
		return KindCrashReports, nil, nil
	}
}

// versionsVisit offers the server software Paper unpacked for other
// Minecraft versions (versions/1.21.3).
func (s *scan) versionsVisit(sv Server) visitFunc {
	return func(e *entry) (Kind, visitFunc, *pending) {
		if e.info.IsDir() && versionLike(e.name) && e.name != sv.MinecraftVersion {
			return KindSoftware, nil, s.unused(sv, KindSoftware, e, e.name, "")
		}
		return KindSoftware, nil, nil
	}
}

// cacheVisit offers the Minecraft server jars Paper keeps for other
// versions (cache/mojang_1.21.3.jar).
func (s *scan) cacheVisit(sv Server) visitFunc {
	return func(e *entry) (Kind, visitFunc, *pending) {
		if v := cachedVersion(e.name); v != "" && v != sv.MinecraftVersion && e.info.Mode().IsRegular() {
			return KindCaches, nil, s.unused(sv, KindCaches, e, v, "")
		}
		return KindCaches, nil, nil
	}
}

// old offers the file e once it is older than its reason's age.
func (s *scan) old(sv Server, reason Reason, e *entry) *pending {
	kind, age := KindCrashReports, s.o.CrashAge
	if reason == ReasonOldLog {
		kind, age = KindLogs, s.o.LogAge
	}
	params, text := oldText(reason, e.name, e.info.ModTime().In(s.o.Location))
	p := s.candidate(sv.ID, kind, reason, RiskLow, params, text)
	p.minAge = age
	return p
}

// unused offers server software for the Minecraft version given, if known,
// and with a build only for an older build of the version the server runs.
func (s *scan) unused(sv Server, kind Kind, e *entry, version, build string) *pending {
	params, text := unusedText(e.rel, softwareName(softwarePrefix(sv)), version, build)
	return s.candidate(sv.ID, kind, ReasonUnusedSoftware, RiskLow, params, text)
}

// softwarePrefix is what comes before the version in the name of the
// server's software ("paper-" in paper-1.21.4-232.jar), or "" when the
// software or its Minecraft version isn't known.
func softwarePrefix(sv Server) string {
	if sv.Jar == "" || sv.MinecraftVersion == "" {
		return ""
	}
	i := strings.IndexFunc(sv.Jar, isDigit)
	if i <= 0 {
		return ""
	}
	return sv.Jar[:i]
}

// sameSoftware reports whether the jar name is the software prefix names,
// in some version.
func sameSoftware(name, prefix string) bool {
	v, ok := strings.CutPrefix(name, prefix)
	return ok && v != "" && isDigit(rune(v[0]))
}

// softwareName is what the software whose jars start with prefix is
// called: Paper for paper-, NeoForge for neoforge-.
func softwareName(prefix string) string {
	word := strings.ToLower(prefix)
	if i := strings.IndexAny(word, "-_. "); i >= 0 {
		word = word[:i]
	}
	switch {
	case word == "":
		return ""
	case word == "neoforge":
		return "NeoForge"
	case word[0] >= 'a' && word[0] <= 'z':
		return string(word[0]-'a'+'A') + word[1:]
	}
	return word
}

// jarVersion is the Minecraft version and the build in the name of a server
// jar that starts with prefix: 1.21.3 and 100 in paper-1.21.3-100.jar. The
// build is "" unless it is a number.
func jarVersion(name, prefix string) (version, build string) {
	rest := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".jar")
	version, build, _ = strings.Cut(rest, "-")
	if !versionLike(version) {
		return "", ""
	}
	if build == "" || strings.ContainsFunc(build, func(r rune) bool { return !isDigit(r) }) {
		build = ""
	}
	return version, build
}

// cachedVersion is the Minecraft version of a server jar Paper keeps in its
// cache folder (mojang_1.21.4.jar), or "".
func cachedVersion(name string) string {
	for _, prefix := range [...]string{"mojang_", "patched_"} {
		if v, ok := strings.CutPrefix(name, prefix); ok {
			if v, ok = strings.CutSuffix(v, ".jar"); ok && versionLike(v) {
				return v
			}
		}
	}
	return ""
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

// leftovers counts the copies of a server's folder that a restore or a
// version change set aside next to it (DataDir.replaced-<time> and the
// like), and offers them once they are a day old, provided the server's
// folder holds a world again and nothing is running on the server.
func (s *scan) leftovers(l Layout, sv Server, to *owner, world bool) {
	r, ok := s.begin(filepath.Dir(sv.DataDir), false)
	if !ok {
		return
	}
	defer r.Close()
	base := filepath.Base(sv.DataDir)
	s.each(r, "", 0, func(e *entry) {
		why, at, ok := leftoverName(e.name, base)
		if !ok || l.overlaps(s.path(e.rel)) {
			return
		}
		es := s.count(r, e, KindLeftovers, to, nil)
		if sv.Busy || !world || !e.info.IsDir() || s.now.Sub(at) < s.o.StageAge {
			return
		}
		params, text := leftoverText(why, at.In(s.o.Location))
		s.offer(s.candidate(sv.ID, KindLeftovers, ReasonLeftoverCopy, RiskMedium, params, text), e, es)
	})
}

// leftoverName parses the name of a copy of the folder base that a restore
// or a version change set aside: base.replaced-20060102-150405,
// base.failed-restore-… or base.failed-update-….
func leftoverName(name, base string) (why string, at time.Time, ok bool) {
	for _, w := range [...]string{"replaced", "failed-restore", "failed-update"} {
		if ts, found := strings.CutPrefix(name, base+"."+w+"-"); found {
			t, err := time.Parse("20060102-150405", ts)
			return strings.ReplaceAll(w, "-", "_"), t, err == nil
		}
	}
	return "", time.Time{}, false
}

// backups counts the backups folder: each archive and its checksum file for
// the archive's server, the rest for the machine. It offers partial files
// nothing has written to for a while, and backups the rules would delete.
// Partial files aren't offered while any server is busy, since a backup
// may be writing one.
func (s *scan) backups(l Layout, owned map[string]*owner, machine *owner, anyBusy bool) {
	r, ok := s.begin(l.BackupsDir, false)
	if !ok {
		return
	}
	defer r.Close()
	byName := make(map[string]Backup, len(l.Backups))
	for _, b := range l.Backups {
		byName[b.FileName] = b
	}
	whose := func(b Backup) *owner {
		if o, ok := owned[b.ServerID]; ok {
			return o
		}
		return machine
	}
	type archive struct {
		e   *entry
		sub sub
	}
	archives := map[string]archive{}
	sums := map[string]sub{}
	s.each(r, "", 0, func(e *entry) {
		regular := e.info.Mode().IsRegular()
		if b, ok := byName[e.name]; ok && regular {
			archives[e.name] = archive{e, s.count(r, e, KindBackups, whose(b), nil)}
			return
		}
		if name, ok := strings.CutSuffix(e.name, ".sha256"); ok && regular {
			if b, ok := byName[name]; ok {
				sums[name] = s.count(r, e, KindBackups, whose(b), nil)
				return
			}
		}
		switch {
		case regular && isPartial(e.name):
			es := s.count(r, e, KindLeftovers, machine, nil)
			if !anyBusy {
				params, text := partialText(e.name, e.info.ModTime().In(s.o.Location))
				p := s.candidate("", KindLeftovers, ReasonPartialFile, RiskLow, params, text)
				p.minAge = s.o.PartialAge
				s.offer(p, e, es)
			}
		case regular && (strings.HasSuffix(e.name, ".tar.gz") || strings.HasSuffix(e.name, ".tar.gz.sha256")):
			s.count(r, e, KindBackups, machine, nil)
		default:
			s.count(r, e, KindOther, machine, nil)
		}
	})
	servers := make(map[string]Server, len(l.Servers))
	for _, sv := range l.Servers {
		servers[sv.ID] = sv
	}
	for _, b := range l.Backups {
		a, found := archives[b.FileName]
		sv, known := servers[b.ServerID]
		if !b.Prune || !found || !known || sv.Busy {
			continue
		}
		es := a.sub
		es.add(sums[b.FileName])
		params, text := prunedText(b, s.o.Location)
		p := s.candidate(b.ServerID, KindBackups, ReasonPrunedBackup, RiskMedium, params, text)
		p.c.BackupID = b.ID
		s.offer(p, a.e, es)
	}
}

// isPartial reports whether name is a file Playkeeper writes under a
// temporary name before moving it into place: .playkeeper-….tar.gz.partial
// for a backup, .offsite-….partial for a download.
func isPartial(name string) bool {
	return len(name) > len("..partial") && strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".partial")
}

// staging counts restore stages and offers those no restore is using that
// haven't changed for a while. None is offered while a server is busy,
// since its operation may be using one.
func (s *scan) staging(l Layout, machine *owner, anyBusy bool) {
	r, ok := s.begin(l.StagingDir, false)
	if !ok {
		return
	}
	defer r.Close()
	s.each(r, "", 0, func(e *entry) {
		es := s.count(r, e, KindStaging, machine, nil)
		if anyBusy || !e.info.IsDir() || slices.Contains(l.ActiveStages, e.name) {
			return
		}
		params, text := stageText(e.name, e.info.ModTime().In(s.o.Location))
		p := s.candidate("", KindStaging, ReasonStaleStage, RiskLow, params, text)
		p.minAge = s.o.StageAge
		s.offer(p, e, es)
	})
}

// downloads counts the downloads folder and offers what in it hasn't
// changed for PartialAge. Nothing is offered while a server is busy, since
// its operation may be using a download.
func (s *scan) downloads(l Layout, machine *owner, anyBusy bool) {
	r, ok := s.beginIfThere(l.DownloadsDir)
	if !ok {
		return
	}
	defer r.Close()
	s.each(r, "", 0, func(e *entry) {
		es := s.count(r, e, KindDownloads, machine, nil)
		if anyBusy {
			return
		}
		params, text := downloadText(e.name, es.newest.In(s.o.Location))
		p := s.candidate("", KindDownloads, ReasonDownloaded, RiskLow, params, text)
		p.minAge = s.o.PartialAge
		s.offer(p, e, es)
	})
}

// spool counts a server's off-site spool folder: its copies being
// encrypted or waiting to be sent as the server's backups, anything else
// as its other files. Nothing in it is offered; the offsite package's
// AbortStale knows which copies an upload will resume.
func (s *scan) spool(sv Server, to *owner) {
	r, ok := s.begin(sv.SpoolDir, false)
	if !ok {
		return
	}
	defer r.Close()
	s.each(r, "", 0, func(e *entry) {
		kind := KindOther
		if e.info.Mode().IsRegular() && isSpoolFile(e.name) {
			kind = KindBackups
		}
		s.count(r, e, kind, to, nil)
	})
}

// isSpoolFile reports whether name is an off-site copy being encrypted or
// waiting to be sent: .offsite-<16 hex digits>.partial or .age.
func isSpoolFile(name string) bool {
	id, ok := strings.CutPrefix(name, ".offsite-")
	if !ok {
		return false
	}
	for _, suffix := range [...]string{".age", ".partial"} {
		if v, ok := strings.CutSuffix(id, suffix); ok {
			return len(v) == 16 && lowerHex(v)
		}
	}
	return false
}

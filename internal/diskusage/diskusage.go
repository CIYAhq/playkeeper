// Package diskusage shows what takes up disk space on a machine running
// Playkeeper: how full the disk is, and what each server takes per kind
// (worlds, backups, logs, crash reports, server software, add-ons, caches,
// copies a restore or version change left behind, restore staging,
// downloads and the Docker image), in the groups the Disk space page's bar
// and table show. It proposes files that can be deleted to free space, each
// with its size, a plain description and a risk level, gathered into the
// page's "Ways to free space".
//
// Clean deletes only candidates that a fresh scan still offers, and checks
// every path again right before removing it. A world in use is never a
// candidate.
//
// The folders a Layout names are trusted configuration; nothing found
// inside them is. Scans never follow symbolic links, stay on the
// filesystem each folder is on, stop at a cap on entries and depth, and can
// be cancelled. Every file is reached through an os.Root, one folder at a
// time, so a link planted in a server's folder (which the game process can
// write) can't send a scan or a delete anywhere else.
package diskusage

import (
	"cmp"
	"context"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Kind is what a file or folder is for.
type Kind string

const (
	KindWorld        Kind = "world"         // world folders (with a level.dat)
	KindBackups      Kind = "backups"       // backup archives and their checksum files
	KindLogs         Kind = "logs"          // the server's logs folder
	KindCrashReports Kind = "crash_reports" // crash reports, Java error logs and memory dumps
	KindSoftware     Kind = "software"      // server jars, libraries and unpacked versions
	KindAddons       Kind = "addons"        // plugins and mods, with their data
	KindCaches       Kind = "caches"        // caches the server fills again itself
	KindLeftovers    Kind = "leftovers"     // copies a restore or version change left behind, and partial files
	KindStaging      Kind = "staging"       // backups prepared for a restore
	KindDownloads    Kind = "downloads"     // files Playkeeper downloaded and fetches again when needed
	KindDockerImage  Kind = "docker_image"  // the Minecraft server image
	KindOther        Kind = "other"         // settings and everything else
)

// Layout is where one machine's servers and shared folders are. The caller
// builds it from its configuration and database. Paths are absolute, and
// none of the folders is inside another.
type Layout struct {
	Servers []Server
	// BackupsDir is the folder backup archives are written to. Several
	// servers may share it; Backups says which archive is whose.
	BackupsDir string
	Backups    []Backup
	// StagingDir holds restore stages, one folder each.
	StagingDir string
	// ActiveStages names stages a restore preview or restore is using.
	ActiveStages []string
	// DownloadsDir holds files Playkeeper downloaded, such as add-on files,
	// and downloads again when they are needed; empty if there is none. It
	// may not exist until the first download.
	DownloadsDir string
	// DockerImageBytes is the size of the Minecraft server image, if known.
	// It counts as on the disk the report measures.
	DockerImageBytes int64
	// DiskDir is a folder on the disk the report measures, the one the
	// machine's Disk meter shows. If empty, it is the first of the servers'
	// data folders, the backups, staging and downloads folders that is set.
	// It may be inside one of them.
	DiskDir string
}

// Server is one Minecraft server on the machine.
type Server struct {
	ID string
	// Name is what the user calls the server, for the report's texts; the
	// id if empty.
	Name string
	// DataDir is the server's folder of world and server files. Copies a
	// restore leaves behind sit next to it, named DataDir.replaced-<time>
	// and DataDir.failed-restore-<time>.
	DataDir string
	// SpoolDir is the folder the server's off-site copies are encrypted in
	// before they are sent (the offsite package's Options.SpoolDir), if
	// any. Its copies count as the server's backups and are never offered:
	// offsite's AbortStale knows which ones an upload will resume.
	SpoolDir string
	// Jar is the file name in DataDir of the server software in use, such as
	// paper-1.21.4-232.jar, and MinecraftVersion its Minecraft version.
	// Software for other versions is only offered when both are set.
	Jar              string
	MinecraftVersion string
	// Busy means an operation other than the clean-up itself (a backup,
	// restore, version change…) is running on the server, so nothing of it
	// is offered, nor any partial file or restore stage.
	Busy bool
}

// Backup is a backup archive the database knows.
type Backup struct {
	ID        string
	ServerID  string
	FileName  string // in BackupsDir; its checksum file is FileName.sha256
	CreatedAt time.Time
	// Prune means the backup rules would delete it (see package retention).
	Prune bool
}

// Options tune a scan. The zero value uses the defaults.
type Options struct {
	Now      func() time.Time // time.Now if nil
	Location *time.Location   // for dates in descriptions; UTC if nil
	// Rotated logs older than LogAge are offered (30 days if 0), and crash
	// reports, Java error logs and memory dumps older than CrashAge (30
	// days if 0).
	LogAge   time.Duration
	CrashAge time.Duration
	// PartialAge is how long a partial file from a backup or download must
	// be untouched to count as left over (1 hour if 0). StageAge is the
	// same for a restore stage, and how old a copy a restore set aside must
	// be (1 day if 0).
	PartialAge time.Duration
	StageAge   time.Duration
	// MaxEntries caps the files and folders counted under each folder a
	// scan starts from (a server's data folder, the folder it is in, the
	// backups and the staging folder; 1,000,000 if 0), and MaxDepth how
	// deep below it the scan goes (64 if 0).
	MaxEntries int
	MaxDepth   int
	// DiskSpace reports the bytes a process without root can still write,
	// and the bytes in all, on the disk that holds a folder; the operating
	// system's figures if nil.
	DiskSpace func(dir string) (free, total int64, err error)
}

func (o Options) withDefaults() Options {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.DiskSpace == nil {
		o.DiskSpace = diskSpace
	}
	if o.Location == nil {
		o.Location = time.UTC
	}
	if o.LogAge <= 0 {
		o.LogAge = 30 * 24 * time.Hour
	}
	if o.CrashAge <= 0 {
		o.CrashAge = 30 * 24 * time.Hour
	}
	if o.PartialAge <= 0 {
		o.PartialAge = time.Hour
	}
	if o.StageAge <= 0 {
		o.StageAge = 24 * time.Hour
	}
	if o.MaxEntries <= 0 {
		o.MaxEntries = 1_000_000
	}
	if o.MaxDepth <= 0 {
		o.MaxDepth = 64
	}
	return o
}

// Usage is the space some files take.
type Usage struct {
	Bytes int64 `json:"bytes"` // on disk, counting a hard-linked file once
	Files int64 `json:"files"` // files and folders
}

func (u *Usage) add(v Usage) {
	u.Bytes += v.Bytes
	u.Files += v.Files
}

// KindUsage is the space one kind of file takes.
type KindUsage struct {
	Kind Kind `json:"kind"`
	Usage
}

// ServerUsage is what one server takes, largest kind first.
type ServerUsage struct {
	ID    string      `json:"id"`
	Name  string      `json:"name"`
	Total Usage       `json:"total"`
	Kinds []KindUsage `json:"kinds"`
	// Groups is Total in the groups of the Disk space page's table:
	// backups, worlds, server files and add-ons, and logs, in that order.
	Groups []GroupUsage `json:"groups"`
}

// Report is the outcome of a scan.
type Report struct {
	ScannedAt time.Time `json:"scannedAt"`
	// Disk is the disk the report measures; nil if the layout names no
	// folder, or the disk's size couldn't be read (see Problems).
	Disk    *Disk         `json:"disk"`
	Servers []ServerUsage `json:"servers"`
	// Machine is what belongs to no one server: backups of no server in the
	// layout, partial files, restore staging, downloads, anything else in
	// the backups, staging and downloads folders, and the Docker image.
	Machine []KindUsage `json:"machine"`
	Total   Usage       `json:"total"`
	// Candidates can be deleted, safest and largest first.
	Candidates []Candidate `json:"candidates"`
	// Ways gathers every candidate into the rows of "Ways to free space",
	// and Freeable is what they free in all, in bytes.
	Ways     []Way `json:"ways"`
	Freeable int64 `json:"freeable"`
	// Truncated means a cap stopped part of the scan, so sizes are at least
	// what they say.
	Truncated    bool      `json:"truncated"`
	Problems     []Problem `json:"problems,omitempty"`
	MoreProblems int       `json:"moreProblems,omitempty"` // problems not listed
}

// Problem is a place the scan couldn't fully count.
type Problem struct {
	// Code is unreadable, missing, other_filesystem, too_deep,
	// too_many_files, changed or disk_space (the disk's size is unknown).
	Code string `json:"code"`
	Path string `json:"path"`
	Text string `json:"text"`
}

// Error is what Scan and Clean return for an invalid layout or ids, or a
// cancelled context.
type Error struct {
	Kind  string // invalid_layout, invalid_ids or canceled
	Field string // for invalid_layout and invalid_ids: the setting at fault
	Msg   string
	Hint  string
	Err   error
}

func (e *Error) Error() string { return e.Msg }

func (e *Error) Unwrap() error { return e.Err }

func invalid(field, msg string) *Error {
	return &Error{Kind: "invalid_layout", Field: field, Msg: msg}
}

// Scan measures the layout's servers and shared folders and finds what can
// be deleted. It fails only for an invalid layout or a cancelled context;
// places it can't read are listed in the report's Problems.
func Scan(ctx context.Context, l Layout, o Options) (*Report, error) {
	if err := l.validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, cancelled(err)
	}
	s := newScan(ctx, o.withDefaults())
	rep := s.run(l)
	if s.cancelled != nil {
		return nil, cancelled(s.cancelled)
	}
	return rep, nil
}

func cancelled(err error) *Error {
	return &Error{Kind: "canceled", Msg: "The disk scan was cancelled before it finished.", Err: err}
}

func (l Layout) validate() error {
	ids := map[string]bool{}
	for _, sv := range l.Servers {
		switch {
		case sv.ID == "" || len(sv.ID) > 64 || !printable(sv.ID):
			return invalid("servers", "A server's id is missing or not valid.")
		case ids[sv.ID]:
			return invalid("servers", "Two servers have the id "+sv.ID+".")
		case !validName(sv.Name):
			return invalid("name", "The name of server "+sv.ID+" is not valid.")
		case !folderPath(sv.DataDir):
			return invalid("dataDir", "The data folder of server "+sv.ID+" must be an absolute path.")
		case sv.SpoolDir != "" && !folderPath(sv.SpoolDir):
			return invalid("spoolDir", "The folder where server "+sv.ID+"'s off-site copies are encrypted must be an absolute path.")
		case sv.Jar != "" && (!validFileName(sv.Jar) || !strings.HasSuffix(sv.Jar, ".jar")):
			return invalid("jar", "The server software of server "+sv.ID+" must be a .jar file name.")
		case sv.MinecraftVersion != "" && !versionLike(sv.MinecraftVersion):
			return invalid("minecraftVersion", "The Minecraft version of server "+sv.ID+" is not valid.")
		}
		ids[sv.ID] = true
	}
	if l.BackupsDir != "" && !folderPath(l.BackupsDir) {
		return invalid("backupsDir", "The backups folder must be an absolute path.")
	}
	if l.StagingDir != "" && !folderPath(l.StagingDir) {
		return invalid("stagingDir", "The staging folder must be an absolute path.")
	}
	if l.DownloadsDir != "" && !folderPath(l.DownloadsDir) {
		return invalid("downloadsDir", "The downloads folder must be an absolute path.")
	}
	if l.DiskDir != "" && !folderPath(l.DiskDir) {
		return invalid("diskDir", "The folder on the disk to measure must be an absolute path.")
	}
	roots := l.roots()
	for i, a := range roots {
		for _, b := range roots[i+1:] {
			if within(a, b) || within(b, a) {
				return invalid("layout", "The folders "+a+" and "+b+" overlap, so their files would be counted twice.")
			}
		}
	}
	files := map[string]bool{}
	for _, b := range l.Backups {
		switch {
		case b.ID == "" || len(b.ID) > 128 || !printable(b.ID):
			return invalid("backups", "A backup's id is missing or not valid.")
		case !validFileName(b.FileName):
			return invalid("backups", "Backup "+b.ID+" has a file name that is not valid.")
		case files[b.FileName]:
			return invalid("backups", "Two backups have the file name "+b.FileName+".")
		}
		files[b.FileName] = true
	}
	if l.DockerImageBytes < 0 {
		return invalid("dockerImageBytes", "The Docker image size can't be negative.")
	}
	return nil
}

// roots are the folders the layout names, DiskDir aside.
func (l Layout) roots() []string {
	var roots []string
	for _, sv := range l.Servers {
		roots = append(roots, sv.DataDir)
		if sv.SpoolDir != "" {
			roots = append(roots, sv.SpoolDir)
		}
	}
	for _, dir := range []string{l.BackupsDir, l.StagingDir, l.DownloadsDir} {
		if dir != "" {
			roots = append(roots, dir)
		}
	}
	return roots
}

func (sv Server) name() string { return cmp.Or(sv.Name, sv.ID) }

// overlaps reports whether the path p is, holds or is inside a folder the
// layout names.
func (l Layout) overlaps(p string) bool {
	return slices.ContainsFunc(l.roots(), func(dir string) bool { return within(p, dir) || within(dir, p) })
}

// folderPath reports whether p is an absolute, clean path other than the
// filesystem root.
func folderPath(p string) bool {
	return p != "" && filepath.IsAbs(p) && filepath.Clean(p) == p && filepath.Dir(p) != p
}

// within reports whether path p is dir or inside it.
func within(p, dir string) bool {
	if p == dir {
		return true
	}
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func printable(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] >= 0x7f {
			return false
		}
	}
	return true
}

// validName reports whether name can be shown as a server's name: empty,
// or at most 100 bytes of UTF-8 text without control characters.
func validName(name string) bool {
	return len(name) <= 100 && utf8.ValidString(name) && !strings.ContainsFunc(name, unicode.IsControl)
}

func validFileName(name string) bool {
	return name != "" && len(name) <= 255 && name != "." && name != ".." &&
		!strings.ContainsAny(name, "/\\\x00")
}

func versionLike(v string) bool {
	if v == "" || len(v) > 64 || v[0] < '0' || v[0] > '9' {
		return false
	}
	return !strings.ContainsFunc(v, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '-' || r == '_' || r == '+')
	})
}

// kinds collects usage per kind.
type kinds map[Kind]Usage

func (k kinds) add(kind Kind, u Usage) {
	if u == (Usage{}) {
		return
	}
	v := k[kind]
	v.add(u)
	k[kind] = v
}

// list is the usage per kind, largest first, and in total.
func (k kinds) list() ([]KindUsage, Usage) {
	out := make([]KindUsage, 0, len(k))
	var total Usage
	for kind, u := range k {
		out = append(out, KindUsage{Kind: kind, Usage: u})
		total.add(u)
	}
	slices.SortFunc(out, func(a, b KindUsage) int {
		return cmp.Or(cmp.Compare(b.Bytes, a.Bytes), strings.Compare(string(a.Kind), string(b.Kind)))
	})
	return out, total
}

// owner is what a server, or the machine, takes: everywhere, and on the
// disk the report measures.
type owner struct{ all, onDisk kinds }

func newOwner() *owner { return &owner{all: kinds{}, onDisk: kinds{}} }

package diskusage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"
)

const (
	batch       = 256 // folder entries read at a time
	maxProblems = 50
)

// errChanged means a file or folder is no longer the one first seen there.
var errChanged = errors.New("changed while being checked")

// fileKey identifies a file on the machine.
type fileKey struct{ dev, ino uint64 }

// fileStat is what a scan needs to know about a file beyond fs.FileInfo.
type fileStat struct {
	bytes  int64 // on disk
	key    fileKey
	linked bool // a file with other hard links
	hasDev bool // key is known
}

// entry is a file or folder a scan meets.
type entry struct {
	name  string
	rel   string // from the scan root, with forward slashes
	info  fs.FileInfo
	depth int // 1 for the scan root's own entries
}

// visitFunc says what an entry of a server's data folder is: its kind, how
// to visit its own entries if it is a folder (nil counts them all as the
// same kind), and whether it can be deleted.
type visitFunc func(e *entry) (Kind, visitFunc, *pending)

// sub is what a scan found in a folder, or in a single file.
type sub struct {
	Usage
	newest  time.Time // the latest change to anything in it
	partial bool      // something in it wasn't counted
	world   bool      // the folder itself holds a world
	worlds  bool      // a world is somewhere inside it
}

func (a *sub) add(b sub) {
	a.Usage.add(b.Usage)
	if b.newest.After(a.newest) {
		a.newest = b.newest
	}
	a.partial = a.partial || b.partial
	a.worlds = a.worlds || b.world || b.worlds
}

type scan struct {
	ctx       context.Context
	o         Options
	now       time.Time
	cancelled error
	rep       Report
	seen      map[fileKey]bool // hard-linked files already counted

	// The disk the report measures, when its device is known.
	diskDev    uint64
	diskHasDev bool

	// The folder from the layout being scanned.
	root     string
	rootInfo fs.FileInfo
	rootStat fileStat
	worlds   bool // a server's data folder: a folder holding a level.dat is a world
	onDisk   bool // it is on the disk the report measures, or which disk it is on is unknown
	entries  int
	capped   bool
}

func newScan(ctx context.Context, o Options) *scan {
	return &scan{ctx: ctx, o: o, now: o.Now(), seen: map[fileKey]bool{}}
}

func (s *scan) run(l Layout) *Report {
	s.rep = Report{ScannedAt: s.now, Servers: []ServerUsage{}, Candidates: []Candidate{}, Ways: []Way{}}
	s.measureDisk(l)
	anyBusy := slices.ContainsFunc(l.Servers, func(sv Server) bool { return sv.Busy })
	owned := make(map[string]*owner, len(l.Servers))
	machine := newOwner()
	for _, sv := range l.Servers {
		to := newOwner()
		owned[sv.ID] = to
		if found, world := s.server(sv, to); found {
			s.leftovers(l, sv, to, world)
		}
		if sv.SpoolDir != "" {
			s.spool(sv, to)
		}
	}
	if l.BackupsDir != "" {
		s.backups(l, owned, machine, anyBusy)
	}
	if l.StagingDir != "" {
		s.staging(l, machine, anyBusy)
	}
	if l.DownloadsDir != "" {
		s.downloads(l, machine, anyBusy)
	}
	image := Usage{Bytes: l.DockerImageBytes}
	machine.all.add(KindDockerImage, image)
	machine.onDisk.add(KindDockerImage, image)
	onDisk := make([]kinds, 0, len(l.Servers))
	for _, sv := range l.Servers {
		to := owned[sv.ID]
		ks, total := to.all.list()
		s.rep.Servers = append(s.rep.Servers, ServerUsage{ID: sv.ID, Name: sv.name(), Total: total, Kinds: ks, Groups: serverGroups(to.all)})
		s.rep.Total.add(total)
		onDisk = append(onDisk, to.onDisk)
	}
	var total Usage
	s.rep.Machine, total = machine.all.list()
	s.rep.Total.add(total)
	if s.rep.Disk != nil {
		s.rep.Disk.fillBar(onDisk, machine.onDisk)
	}
	sortCandidates(s.rep.Candidates)
	s.rep.Ways, s.rep.Freeable = ways(s.rep.Candidates, l.Servers, s.o)
	return &s.rep
}

// server counts a server's data folder and offers what can go from it. It
// reports whether the folder is there and whether it holds a world.
func (s *scan) server(sv Server, to *owner) (found, world bool) {
	r, ok := s.begin(sv.DataDir, true)
	if !ok {
		return false, false
	}
	defer r.Close()
	in := s.walk(r, "", 0, KindOther, to, s.dataVisit(sv))
	return true, in.world || in.worlds
}

// begin starts counting under dir, a folder the layout names.
func (s *scan) begin(dir string, worlds bool) (*os.Root, bool) {
	s.root, s.worlds, s.entries, s.capped = dir, worlds, 0, false
	if s.stop() {
		return nil, false
	}
	r, err := os.OpenRoot(dir)
	if err == nil {
		if s.rootInfo, err = r.Stat("."); err != nil {
			r.Close()
		}
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		s.problem("missing", dir, "The folder "+dir+" doesn't exist.")
	case err != nil:
		s.unreadable("", err)
	default:
		s.rootStat = statOf(s.rootInfo)
		s.onDisk = !s.diskHasDev || !s.rootStat.hasDev || s.rootStat.key.dev == s.diskDev
		return r, true
	}
	return nil, false
}

// beginIfThere is begin for a folder that may not have been made yet, and
// then holds nothing.
func (s *scan) beginIfThere(dir string) (*os.Root, bool) {
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil, false
	}
	return s.begin(dir, false)
}

// walk counts what is in the folder r as kind into to, asking visit (when
// not nil) what each entry is. In a server's data folder, a folder holding
// a level.dat is a world, with everything in it, and nothing in a world is
// offered.
func (s *scan) walk(r *os.Root, rel string, depth int, kind Kind, to *owner, visit visitFunc) sub {
	var in sub
	if s.worlds && kind != KindWorld && isWorld(r) {
		kind, visit, in.world = KindWorld, nil, true
	}
	complete := s.each(r, rel, depth, func(e *entry) {
		k, v, p := kind, visitFunc(nil), (*pending)(nil)
		if visit != nil {
			k, v, p = visit(e)
		}
		es := s.count(r, e, k, to, v)
		in.add(es)
		if p != nil {
			s.offer(p, e, es)
		}
	})
	in.partial = in.partial || !complete
	return in
}

// each calls fn for each entry of the folder r, reading it in batches, until
// it ends, a cap is reached or the scan is cancelled. It reports whether
// every entry was seen. Entries are described by r.Lstat, so a link is
// never followed.
func (s *scan) each(r *os.Root, rel string, depth int, fn func(e *entry)) bool {
	f, err := r.Open(".")
	if err != nil {
		s.unreadable(rel, err)
		return false
	}
	defer f.Close()
	complete := true
	for {
		des, err := f.ReadDir(batch)
		for _, de := range des {
			if s.stop() {
				return false
			}
			e := &entry{name: de.Name(), rel: join(rel, de.Name()), depth: depth + 1}
			fi, lerr := r.Lstat(e.name)
			switch {
			case errors.Is(lerr, fs.ErrNotExist):
				continue
			case lerr != nil:
				s.unreadable(e.rel, lerr)
				complete = false
				continue
			}
			e.info = fi
			s.entries++
			fn(e)
		}
		switch {
		case err == io.EOF:
			return complete
		case err != nil:
			s.unreadable(rel, err)
			return false
		case len(des) == 0:
			return complete
		}
	}
}

// count counts the entry e of the folder r as kind into to, with everything
// in it if it is a folder.
func (s *scan) count(r *os.Root, e *entry, kind Kind, to *owner, visit visitFunc) sub {
	st := statOf(e.info)
	own := sub{Usage: Usage{Bytes: st.bytes, Files: 1}, newest: e.info.ModTime()}
	if st.linked {
		if s.seen[st.key] {
			own.Bytes = 0
		}
		s.seen[st.key] = true
	}
	if !e.info.IsDir() {
		s.add(to, kind, own.Usage)
		return own
	}
	in := s.dir(r, e, kind, to, visit)
	if in.world {
		kind = KindWorld
	}
	s.add(to, kind, own.Usage)
	in.add(own)
	return in
}

// add counts u as kind for to, and for the disk too when the folder being
// scanned is on it.
func (s *scan) add(to *owner, kind Kind, u Usage) {
	to.all.add(kind, u)
	if s.onDisk {
		to.onDisk.add(kind, u)
	}
}

func (s *scan) dir(r *os.Root, e *entry, kind Kind, to *owner, visit visitFunc) sub {
	path := s.path(e.rel)
	if e.depth >= s.o.MaxDepth {
		s.rep.Truncated = true
		s.problem("too_deep", path, fmt.Sprintf("%s is more than %d folders deep, so what's in it isn't counted.", path, s.o.MaxDepth))
		return sub{partial: true}
	}
	if st := statOf(e.info); st.hasDev && s.rootStat.hasDev && st.key.dev != s.rootStat.key.dev {
		s.problem("other_filesystem", path, path+" is on another disk or mount, so what's in it isn't counted here.")
		return sub{partial: true}
	}
	c, err := openDir(r, e.name, e.info)
	switch {
	case errors.Is(err, errChanged):
		s.problem("changed", path, path+" changed while it was being counted, so what's in it isn't counted.")
		return sub{partial: true}
	case errors.Is(err, fs.ErrNotExist):
		return sub{partial: true}
	case err != nil:
		s.unreadable(e.rel, err)
		return sub{partial: true}
	}
	defer c.Close()
	return s.walk(c, e.rel, e.depth, kind, to, visit)
}

// openDir opens the folder name in r, which fi (from r.Lstat) describes,
// and makes sure it is still that folder: a link put in its place is
// refused, not followed.
func openDir(r *os.Root, name string, fi fs.FileInfo) (*os.Root, error) {
	if !fi.IsDir() {
		return nil, errChanged
	}
	c, err := r.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	st, err := c.Stat(".")
	if err == nil && !os.SameFile(fi, st) {
		err = errChanged
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// isWorld reports whether the folder r holds a world, or might: it has a
// level.dat, a level.dat_old (all there is while Minecraft replaces
// level.dat) or a session.lock, or they can't be ruled out.
func isWorld(r *os.Root) bool {
	for _, name := range [...]string{"level.dat", "level.dat_old", "session.lock"} {
		if _, err := r.Lstat(name); !errors.Is(err, fs.ErrNotExist) {
			return true
		}
	}
	return false
}

// stop reports whether counting the current folder must stop: the scan was
// cancelled, or the folder reached its cap on entries.
func (s *scan) stop() bool {
	switch {
	case s.cancelled != nil || s.capped:
		return true
	case s.ctx.Err() != nil:
		s.cancelled = s.ctx.Err()
		return true
	case s.entries >= s.o.MaxEntries:
		s.capped, s.rep.Truncated = true, true
		s.problem("too_many_files", s.root, "Counting stopped after "+thousands(s.o.MaxEntries)+" files and folders in "+s.root+", so the sizes shown for it are lower than the real ones.")
		return true
	}
	return false
}

func (s *scan) problem(code, path, text string) {
	if len(s.rep.Problems) >= maxProblems {
		s.rep.MoreProblems++
		return
	}
	s.rep.Problems = append(s.rep.Problems, Problem{Code: code, Path: path, Text: text})
}

func (s *scan) unreadable(rel string, err error) {
	path := s.path(rel)
	s.problem("unreadable", path, "Playkeeper couldn't read "+path+" ("+why(err)+"), so what's in it isn't counted.")
}

func (s *scan) path(rel string) string {
	return filepath.Join(s.root, filepath.FromSlash(rel))
}

func join(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// why is the cause in err, without the path the text around it names.
func why(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return err.Error()
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

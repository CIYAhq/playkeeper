package addons

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons/modrinth"
)

// FileStatus is what Playkeeper knows about a jar in the add-on folder.
type FileStatus string

const (
	// FileManaged is a file Playkeeper installed, unchanged since.
	FileManaged FileStatus = "managed"
	// FileModified is a file Playkeeper installed that has changed since.
	FileModified FileStatus = "modified"
	// FileIdentified was added by hand, and Modrinth knows it by its hash.
	FileIdentified FileStatus = "identified"
	// FileUnknown was added by hand, and Playkeeper cannot tell what it is.
	FileUnknown FileStatus = "unknown"
)

// ScanEntry is one jar in the add-on folder.
type ScanEntry struct {
	FileName string     `json:"fileName"`
	Size     int64      `json:"size"`
	Status   FileStatus `json:"status"`
	// Installed is the record of a managed or modified file.
	Installed *Installed `json:"installed,omitempty"`
	// Identified is what Modrinth knows an identified file as: the record
	// to store to let Playkeeper update and remove it from now on.
	Identified *Installed `json:"identified,omitempty"`
	// Meta is what the jar says about itself.
	Meta JarMeta `json:"meta"`
}

// ScanResult is the state of a server's add-on folder.
type ScanResult struct {
	Folder  string      `json:"folder"`
	Entries []ScanEntry `json:"entries"`
	// Missing are installed add-ons whose file is no longer in the folder.
	Missing  []Installed `json:"missing"`
	Warnings []Notice    `json:"warnings"`
}

// Scan sorts the jars in the server's plugins or mods folder into files
// Playkeeper installed (unchanged or modified), files added by hand that
// Modrinth identifies by their hash (only when identify is set; one
// request), and unknown files. It never changes the folder.
func (l *Library) Scan(ctx context.Context, srv Server, installed []Installed, identify bool) (*ScanResult, error) {
	t, err := TargetFor(srv.Type)
	if err != nil {
		return nil, err
	}
	inv, warnings, err := l.inventory(ctx, srv, t, installed, identify, true)
	if err != nil {
		return nil, err
	}
	res := &ScanResult{Folder: t.Folder, Entries: []ScanEntry{}, Missing: inv.missing, Warnings: warnings}
	now := l.now()
	for _, lf := range inv.files {
		e := ScanEntry{FileName: lf.name, Size: lf.size, Status: FileUnknown, Meta: lf.meta}
		switch {
		case lf.rec != nil && lf.modified:
			e.Status, e.Installed = FileModified, lf.rec
		case lf.rec != nil:
			e.Status, e.Installed = FileManaged, lf.rec
		case lf.ident != nil:
			rec := lf.identified(now)
			e.Status, e.Identified = FileIdentified, &rec
			if other := inv.managed[rec.Key()]; other != nil && other.FileName != lf.name {
				res.Warnings = append(res.Warnings, notice(KindDuplicate, kv("name", rec.Name, "file", lf.name, "other", other.FileName, "folder", t.Folder),
					fmt.Sprintf("%s is in the %s folder twice: %s and %s.", rec.Name, t.Folder, other.FileName, lf.name),
					"Remove one of them; the server loads only one copy."))
			}
		}
		res.Entries = append(res.Entries, e)
	}
	return res, nil
}

// maxFolderEntries bounds how much of an add-on folder is read.
const maxFolderEntries = 2000

// inventory is what an add-on folder holds.
type inventory struct {
	names   map[string]bool // every entry, jar or not
	files   []*localFile    // the jars, by name
	byName  map[string]*localFile
	managed map[Key]*Installed
	missing []Installed
}

type localFile struct {
	name string
	size int64
	meta JarMeta
	// rec is the record of the add-on Playkeeper installed as this file;
	// modified is set when the file no longer matches it.
	rec      *Installed
	modified bool
	sha512   string
	ident    *modrinth.Version
	identP   *modrinth.Project
}

// inventory reads the add-on folder. verify hashes the files Playkeeper
// installed; identify asks Modrinth about the others, and a failure there
// becomes a warning.
func (l *Library) inventory(ctx context.Context, srv Server, t Target, installed []Installed, identify, verify bool) (*inventory, []Notice, error) {
	inv := &inventory{names: map[string]bool{}, byName: map[string]*localFile{}, managed: map[Key]*Installed{}, missing: []Installed{}}
	byFile := map[string]*Installed{}
	for i := range installed {
		rec := &installed[i]
		inv.managed[rec.Key()] = rec
		if validFileName(rec.FileName) {
			byFile[rec.FileName] = rec
		}
	}
	warnings := []Notice{}
	root, err := openFolder(srv, t, false)
	if err != nil {
		return nil, nil, err
	}
	if root != nil {
		defer root.Close()
		entries, err := readDir(root, maxFolderEntries+1)
		if err != nil {
			return nil, nil, folderError(t, err)
		}
		if len(entries) > maxFolderEntries {
			entries = entries[:maxFolderEntries]
			warnings = append(warnings, notice(KindTooLarge, kv("folder", t.Folder, "limit", strconv.Itoa(maxFolderEntries)),
				fmt.Sprintf("The %s folder has more than %d entries, so Playkeeper looked at only %d of them.", t.Folder, maxFolderEntries, maxFolderEntries),
				"Tidy up the folder in the file manager."))
		}
		for _, e := range entries {
			name := e.Name()
			inv.names[name] = true
			if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".jar") || !e.Type().IsRegular() {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			lf := &localFile{name: name, size: fi.Size(), rec: byFile[name]}
			l.readLocal(ctx, root, lf, identify, verify)
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			inv.files = append(inv.files, lf)
			inv.byName[name] = lf
		}
	}
	for _, rec := range installed {
		if lf := inv.byName[rec.FileName]; lf == nil || lf.rec == nil || lf.rec.Key() != rec.Key() {
			inv.missing = append(inv.missing, rec)
		}
	}
	if identify {
		if err := l.identify(ctx, inv); err != nil {
			var e *Error
			if !errors.As(err, &e) {
				return nil, nil, err
			}
			warnings = append(warnings, notice(e.Kind, e.Params,
				e.Msg+" Files added by hand were not identified.", e.Hint))
		}
	}
	return inv, warnings, nil
}

// readLocal reads what lf's jar says about itself and, as asked, checks it
// against its record or hashes it to identify it, all through one handle.
func (l *Library) readLocal(ctx context.Context, root *os.Root, lf *localFile, identify, verify bool) {
	f, st, err := openFile(root, lf.name)
	if err != nil {
		lf.modified = lf.rec != nil && verify
		return
	}
	defer f.Close()
	lf.size, lf.meta = st.Size(), readJarMeta(f, st.Size())
	switch {
	case lf.rec != nil && verify:
		same, err := unchanged(ctx, f, lf.size, *lf.rec, l.maxFileSize())
		lf.modified = err != nil || !same
	case lf.rec == nil && identify && lf.size <= l.maxFileSize():
		if sums, err := sumFile(ctx, f, lf.size, "sha512"); err == nil {
			lf.sha512 = sums["sha512"]
		}
	}
}

// identify asks Modrinth which of the files added by hand it knows.
func (l *Library) identify(ctx context.Context, inv *inventory) error {
	var hashes []string
	for _, lf := range inv.files {
		if lf.rec == nil && lf.sha512 != "" {
			hashes = append(hashes, lf.sha512)
		}
	}
	if len(hashes) == 0 {
		return nil
	}
	vs, err := l.Modrinth.VersionsFromHashes(ctx, "sha512", hashes)
	if err != nil {
		return upstream(Modrinth, err)
	}
	var ids []string
	for _, lf := range inv.files {
		if v, ok := vs[lf.sha512]; ok && lf.rec == nil && lf.sha512 != "" {
			lf.ident = &v
			if !slices.Contains(ids, v.ProjectID) && validRef(v.ProjectID) {
				ids = append(ids, v.ProjectID)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	ps, err := l.Modrinth.Projects(ctx, ids)
	if err != nil {
		return upstream(Modrinth, err)
	}
	for _, lf := range inv.files {
		for i := range ps {
			if lf.ident != nil && ps[i].ID == lf.ident.ProjectID {
				lf.identP = &ps[i]
			}
		}
	}
	return nil
}

func readDir(r *os.Root, n int) ([]fs.DirEntry, error) {
	d, err := r.Open(".")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	entries, err := d.ReadDir(n)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}

// identified is the record that lets Playkeeper manage an identified file.
func (lf *localFile) identified(now time.Time) Installed {
	v := lf.ident
	rec := Installed{
		Source: Modrinth, ProjectID: v.ProjectID, Name: lf.display(), VersionID: v.ID, VersionNumber: v.VersionNumber,
		Channel: modrinthChannel(v.VersionType), Published: v.DatePublished,
		FileName: lf.name, HashAlgo: "sha512", Hash: lf.sha512, Size: lf.size, InstalledAt: now,
	}
	if p := lf.identP; p != nil {
		rec.Slug, rec.Summary, rec.IconURL = p.Slug, p.Description, p.IconURL
	}
	for _, d := range v.Dependencies {
		if d.DependencyType == modrinth.Required && d.ProjectID != "" && !slices.Contains(rec.Requires, d.ProjectID) {
			rec.Requires = append(rec.Requires, d.ProjectID)
		}
	}
	return rec
}

// display is the best name for the file's add-on.
func (lf *localFile) display() string {
	switch {
	case lf.rec != nil:
		return lf.rec.Name
	case lf.identP != nil:
		return lf.identP.Title
	case lf.meta.Name != "":
		return lf.meta.Name
	case lf.meta.ID != "":
		return lf.meta.ID
	}
	return lf.name
}

// provider is the file on the server that provides a project.
type provider struct {
	name, file string
	managed    bool
	source     Source
}

func (lf *localFile) provider() provider {
	p := provider{name: lf.display(), file: lf.name}
	if lf.rec != nil {
		p.managed, p.source = true, lf.rec.Source
	}
	return p
}

// provides finds the file on the server that is project key: installed by
// Playkeeper, identified by Modrinth, or else a plugin or mod with one of
// names, from any source or added by hand.
func (inv *inventory) provides(key Key, names ...string) (provider, bool) {
	if rec := inv.managed[key]; rec != nil {
		if lf := inv.byName[rec.FileName]; lf != nil && lf.rec == rec {
			return lf.provider(), true
		}
	}
	if key.Source == Modrinth {
		for _, lf := range inv.files {
			if lf.rec == nil && lf.ident != nil && lf.ident.ProjectID == key.ProjectID {
				return lf.provider(), true
			}
		}
	}
	var want []string
	for _, n := range names {
		if k := norm(n); k != "" && !slices.Contains(want, k) {
			want = append(want, k)
		}
	}
	if len(want) == 0 {
		return provider{}, false
	}
	match := func(ss ...string) bool {
		return slices.ContainsFunc(ss, func(s string) bool {
			k := norm(s)
			return k != "" && slices.Contains(want, k)
		})
	}
	for _, lf := range inv.files {
		var ok bool
		switch {
		case lf.rec != nil:
			ok = match(lf.rec.Name, lf.rec.Slug, lf.meta.ID)
		case lf.identP != nil:
			ok = match(lf.identP.Title, lf.identP.Slug, lf.meta.ID)
		default:
			ok = match(lf.meta.ID, lf.meta.Name)
		}
		if ok {
			return lf.provider(), true
		}
	}
	return provider{}, false
}

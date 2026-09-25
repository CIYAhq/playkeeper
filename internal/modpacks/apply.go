package modpacks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
)

// Result is what an install or update did.
type Result struct {
	// Record replaces what the caller stored for the server's pack.
	Record Record              `json:"record"`
	Manual []addons.ManualStep `json:"manual"`
	// Properties are the server.properties settings the pack suggests;
	// nothing wrote them.
	Properties map[string]string `json:"properties,omitempty"`
	Warnings   []addons.Notice   `json:"warnings"`
	// Kept lists the user's versions of pack files that were left as they
	// are.
	Kept          []string `json:"kept"`
	RestartNeeded bool     `json:"restartNeeded"`
}

// Install puts a pack on the server. It plans again, refuses when the plan
// differs from the one the user confirmed (req.Fingerprint), downloads and
// checks every file, and only then writes to the server's folder; if any
// write fails, the folder is put back as it was. The server should be
// stopped. The caller stores the Record and sets the server's type and
// versions to its Requirements before starting it.
func (l *Library) Install(ctx context.Context, srv Server, current *Record, req InstallRequest) (*Result, error) {
	pl, err := l.planInstall(ctx, srv, current, req)
	if err != nil {
		return nil, err
	}
	defer pl.pk.close()
	if req.Fingerprint != "" && req.Fingerprint != pl.Fingerprint {
		return nil, planChanged()
	}
	return l.apply(ctx, srv, pl)
}

// Update moves the pack on the server to another version like Install,
// touching only the files the pack put there. The caller backs the server
// up first, as the plan may overwrite files the user changed.
func (l *Library) Update(ctx context.Context, srv Server, rec Record, req UpdateRequest) (*Result, error) {
	pl, err := l.planUpdate(ctx, srv, rec, req)
	if err != nil {
		return nil, err
	}
	defer pl.pk.close()
	if req.Fingerprint != "" && req.Fingerprint != pl.Fingerprint {
		return nil, planChanged()
	}
	return l.apply(ctx, srv, pl)
}

func planChanged() *addons.Error {
	return fail(addons.KindPlanChanged, nil, "What this would do has changed since you confirmed it.", "Review the new plan and confirm again.")
}

func (l *Library) apply(ctx context.Context, srv Server, pl *Plan) (*Result, error) {
	if len(pl.Blockers) > 0 {
		return nil, &addons.Error{Notice: pl.Blockers[0]}
	}
	lim := l.limits()
	stage, err := os.MkdirTemp(l.TempDir, "playkeeper-modpack-files-")
	if err != nil {
		return nil, tempError(err)
	}
	defer os.RemoveAll(stage)
	staged := map[string]string{}
	var total int64
	for _, o := range pl.ops {
		if o.file == nil || o.file.origin != Download || o.action != ActionAdd && o.action != ActionReplace {
			continue
		}
		file, n, err := l.download(ctx, pl.Pack.Name, o.file, stage, lim, total)
		if err != nil {
			return nil, err
		}
		staged[o.path], total = file, total+n
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	root, err := openServer(srv)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	tx := &txn{root: root, owner: srv.Owner, pack: pl.Pack.Name, max: lim.File}
	written, err := tx.run(pl.ops, staged)
	if err != nil {
		tx.rollback()
		return nil, err
	}
	tx.commit()

	rec := Record{Pack: pl.Pack, Requirements: pl.Requirements, Files: []File{}, Excluded: pl.excluded}
	rec.Pack.InstalledAt = l.now()
	res := &Result{Manual: pl.Manual, Properties: pl.Properties, Warnings: pl.Warnings, Kept: []string{}, RestartNeeded: true}
	for _, o := range pl.ops {
		if o.action == ActionKeep && o.yours {
			res.Kept = append(res.Kept, o.path)
		}
		f := o.file
		if f == nil || f.world || o.action == ActionRemove {
			continue
		}
		size, ok := written[o.path]
		switch {
		case ok:
		case o.yours:
			size = f.size
		default:
			size = o.size
		}
		algo := f.algo()
		rec.Files = append(rec.Files, File{
			Path: o.path, HashAlgo: algo, Hash: f.sums[algo], Size: size, Origin: f.origin, Project: f.project,
			Optional: f.optional, Preexisting: o.preexisting,
		})
	}
	res.Record = rec
	return res, nil
}

// download fetches one pack file into dir from the first of its addresses
// that delivers it with the pack's hashes. total is what the pack has
// downloaded so far.
func (l *Library) download(ctx context.Context, pack string, f *packFile, dir string, lim Limits, total int64) (string, int64, error) {
	room := min(lim.File, lim.Downloads-total)
	if room <= 0 {
		return "", 0, packTooLarge(pack, lim.Downloads)
	}
	algo := f.algo()
	want := fetch.Want{Algo: algo, Hash: f.sums[algo], Max: room}
	if algo == "sha512" && f.sums["sha1"] != "" {
		want.Also = []fetch.Sum{{Algo: "sha1", Hash: f.sums["sha1"]}}
	}
	var first error
	for _, u := range f.urls {
		file, err := fetch.Download(ctx, l.HTTP, f.hosts, l.userAgent(), u, dir, want)
		if err == nil {
			fi, err := os.Stat(file)
			if err != nil {
				return "", 0, tempError(err)
			}
			return file, fi.Size(), nil
		}
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		if first == nil {
			first = err
		}
	}
	var tl *fetch.TooLargeError
	if errors.As(first, &tl) && room < lim.File {
		return "", 0, packTooLarge(pack, lim.Downloads)
	}
	return "", 0, downloadError(pack, f.path, "the pack", first, room)
}

func packTooLarge(pack string, max int64) *addons.Error {
	return fail(addons.KindTooLarge, kv("pack", pack, "limit", fetch.Size(max)),
		fmt.Sprintf("%s would download more than the %s Playkeeper accepts for one pack.", pack, fetch.Size(max)),
		"Choose a smaller pack. Nothing was installed.")
}

// txn writes a plan into the server's folder and can undo it. Files are
// replaced by name and never rewritten in place, and what they replace is
// kept hidden until everything is in place.
type txn struct {
	root    *os.Root
	owner   *addons.Owner
	pack    string
	max     int64
	created []string    // folders, in the order they were made
	placed  []string    // files
	moved   [][2]string // path, hidden name
}

// run moves away what the plan replaces or removes, after checking each
// file is still what the plan saw, then puts the new files in place. It
// returns the size of each file written.
func (tx *txn) run(ops []op, staged map[string]string) (map[string]int64, error) {
	for _, o := range ops {
		if o.action == ActionReplace || o.action == ActionRemove {
			if err := tx.hide(o); err != nil {
				return nil, err
			}
		}
	}
	written := map[string]int64{}
	for _, o := range ops {
		if o.action != ActionAdd && o.action != ActionReplace {
			continue
		}
		if err := tx.mkdirs(path.Dir(o.path)); err != nil {
			return nil, err
		}
		n, err := tx.place(o, staged[o.path])
		if err != nil {
			return nil, err
		}
		written[o.path] = n
	}
	return written, nil
}

func (tx *txn) hide(o op) error {
	st, err := inspect(tx.root, o.path, o.algo)
	switch {
	case err != nil:
		return serverFolderError(err)
	case !st.regular || st.sums[o.algo] != o.sum:
		return planChanged()
	}
	hidden := hiddenName(o.path, "old")
	if err := tx.root.Rename(o.path, hidden); err != nil {
		return serverFolderError(err)
	}
	tx.moved = append(tx.moved, [2]string{o.path, hidden})
	return nil
}

// mkdirs makes the folders above a pack file that do not exist yet.
func (tx *txn) mkdirs(dir string) error {
	if dir == "." {
		return nil
	}
	parts := strings.Split(dir, "/")
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		fi, err := tx.root.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := tx.root.Mkdir(p, 0o750); err != nil {
				return serverFolderError(err)
			}
			tx.created = append(tx.created, p)
			if err := tx.chown(p); err != nil {
				return serverFolderError(err)
			}
		case err != nil:
			return serverFolderError(err)
		case !fi.IsDir():
			return &addons.Error{Notice: inTheWay(tx.pack, p, dir)}
		}
	}
	return nil
}

// place writes one file under a hidden temporary name, then links it to its
// name, which must not exist.
func (tx *txn) place(o op, staged string) (int64, error) {
	tmp := hiddenName(o.path, "new")
	out, err := tx.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return 0, serverFolderError(err)
	}
	defer tx.root.Remove(tmp)
	n, err := tx.copy(out, o.file, staged)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = tx.chown(tmp)
	}
	if err != nil {
		var e *addons.Error
		if errors.As(err, &e) {
			return 0, err
		}
		return 0, serverFolderError(err)
	}
	err = tx.root.Link(tmp, o.path)
	if err != nil && !errors.Is(err, fs.ErrExist) {
		// Some filesystems have no hard links.
		if _, lerr := tx.root.Lstat(o.path); errors.Is(lerr, fs.ErrNotExist) {
			err = tx.root.Rename(tmp, o.path)
		}
	}
	switch {
	case errors.Is(err, fs.ErrExist):
		return 0, planChanged()
	case err != nil:
		return 0, serverFolderError(err)
	}
	tx.placed = append(tx.placed, o.path)
	return n, nil
}

// copy writes a downloaded file, or copies an override out of the pack's
// archive and checks it is still what the plan hashed.
func (tx *txn) copy(w io.Writer, f *packFile, staged string) (int64, error) {
	if f.origin == Download {
		in, err := os.Open(staged)
		if err != nil {
			return 0, err
		}
		defer in.Close()
		return io.Copy(w, in)
	}
	sums, n, err := copyEntry(f.entry, w, tx.max)
	if err != nil {
		return 0, badPack(tx.pack, fmt.Sprintf("is damaged at %q (%v)", printable(f.path), err))
	}
	if sums["sha512"] != f.sums["sha512"] {
		return 0, badPack(tx.pack, fmt.Sprintf("changed at %q while it was being installed", printable(f.path)))
	}
	return n, nil
}

func (tx *txn) chown(p string) error {
	if tx.owner == nil {
		return nil
	}
	return tx.root.Lchown(p, tx.owner.UID, tx.owner.GID)
}

func (tx *txn) rollback() {
	for _, p := range slices.Backward(tx.placed) {
		tx.root.Remove(p)
	}
	for _, m := range slices.Backward(tx.moved) {
		tx.root.Rename(m[1], m[0])
	}
	for _, d := range slices.Backward(tx.created) {
		tx.root.Remove(d)
	}
}

// commit deletes what was moved away and the folders that removals left
// empty, except top-level ones.
func (tx *txn) commit() {
	for _, m := range tx.moved {
		tx.root.Remove(m[1])
	}
	for _, m := range tx.moved {
		for d := path.Dir(m[0]); strings.Contains(d, "/"); d = path.Dir(d) {
			if fi, err := tx.root.Lstat(d); err != nil || !fi.IsDir() || tx.root.Remove(d) != nil {
				break
			}
		}
	}
}

// hiddenName is a hidden name next to p that nothing else uses.
func hiddenName(p, tag string) string {
	dir, base := path.Split(p)
	b := make([]byte, 6)
	rand.Read(b)
	return dir + "." + base + ".playkeeper-" + tag + "-" + hex.EncodeToString(b)
}

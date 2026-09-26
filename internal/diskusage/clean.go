package diskusage

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"
)

// BackupDeleter deletes a backup the way the rest of Playkeeper does, so
// its record goes with its files.
type BackupDeleter interface {
	DeleteBackup(ctx context.Context, id string) error
}

// Status is what became of a candidate Clean was asked to delete.
type Status string

const (
	StatusDeleted    Status = "deleted"
	StatusGone       Status = "gone"        // it was already gone
	StatusNotOffered Status = "not_offered" // the fresh scan didn't offer it
	StatusChanged    Status = "changed"     // it changed after the fresh scan, so it was left alone
	StatusFailed     Status = "failed"
	StatusSkipped    Status = "skipped" // cleaning was cancelled first
)

// Outcome is what became of one candidate.
type Outcome struct {
	ID     string `json:"id"`
	Status Status `json:"status"`
	Path   string `json:"path,omitempty"`
	Bytes  int64  `json:"bytes"` // freed
	Text   string `json:"text"`
}

// CleanResult is what Clean did, in the order of the ids it was given.
type CleanResult struct {
	Outcomes []Outcome `json:"outcomes"`
	Freed    int64     `json:"freed"` // bytes
}

const maxCleanIDs = 100_000

// Clean deletes the candidates with the given ids. It scans afresh first
// and deletes only what that scan still offers, checking each path again,
// one folder at a time, right before deleting it. Pruned backups are
// deleted through backups, so their records go too.
//
// Clean fails only for an invalid layout or ids, or when ctx is cancelled
// before the scan finishes. Once deleting has started, a cancelled ctx
// leaves the rest skipped.
func Clean(ctx context.Context, l Layout, o Options, ids []string, backups BackupDeleter) (*CleanResult, error) {
	if len(ids) > maxCleanIDs {
		return nil, &Error{Kind: "invalid_ids", Field: "ids", Msg: "Too many items were chosen at once; choose at most " + thousands(maxCleanIDs) + "."}
	}
	for _, id := range ids {
		if !validID(id) {
			return nil, &Error{Kind: "invalid_ids", Field: "ids", Msg: "One of the chosen items is not valid.", Hint: "Scan again and choose from the new list."}
		}
	}
	rep, err := Scan(ctx, l, o)
	if err != nil {
		return nil, err
	}
	offered := make(map[string]*Candidate, len(rep.Candidates))
	for i := range rep.Candidates {
		offered[rep.Candidates[i].ID] = &rep.Candidates[i]
	}
	res := &CleanResult{Outcomes: []Outcome{}}
	done := map[string]bool{}
	for _, id := range ids {
		if done[id] {
			continue
		}
		done[id] = true
		var out Outcome
		switch c := offered[id]; {
		case c == nil:
			out = Outcome{ID: id, Status: StatusNotOffered, Text: "It is no longer offered: it changed, is in use, or no longer matches the clean-up rules. Scan again to see what can be deleted now."}
		case ctx.Err() != nil:
			out = Outcome{ID: id, Status: StatusSkipped, Path: c.Path, Text: "Cleaning was cancelled before it got to this."}
		default:
			out = clean(ctx, c, backups)
		}
		res.Outcomes = append(res.Outcomes, out)
		res.Freed += out.Bytes
	}
	return res, nil
}

func clean(ctx context.Context, c *Candidate, backups BackupDeleter) Outcome {
	out := Outcome{ID: c.ID, Path: c.Path}
	if c.Reason == ReasonPrunedBackup {
		out.Status, out.Text = result(c.check())
		if out.Status == StatusDeleted {
			if backups == nil {
				out.Status, out.Text = StatusFailed, "Backups can't be deleted from here."
			} else if err := backups.DeleteBackup(ctx, c.BackupID); err != nil {
				out.Status, out.Text = StatusFailed, "The backup couldn't be deleted: "+err.Error()
			}
		}
	} else {
		out.Status, out.Text = result(c.remove())
	}
	if out.Status == StatusDeleted {
		out.Bytes = c.Bytes
	}
	return out
}

func result(err error) (Status, string) {
	switch {
	case err == nil:
		return StatusDeleted, "Deleted."
	case errors.Is(err, errChanged):
		return StatusChanged, "It changed after the scan, so it was left alone. Scan again to see what can be deleted now."
	case errors.Is(err, fs.ErrNotExist):
		return StatusGone, "It was already gone."
	}
	return StatusFailed, "It couldn't be deleted (" + why(err) + ")."
}

// remove deletes c once reach has checked the way to it.
func (c *Candidate) remove() error {
	r, err := c.reach()
	if err != nil {
		return err
	}
	defer r.Close()
	name := path.Base(c.rel)
	if !c.info.IsDir() {
		return r.Remove(name)
	}
	if c.guard {
		d, err := openDir(r, name, c.info)
		if err != nil {
			return err
		}
		world := isWorld(d)
		d.Close()
		if world {
			return errChanged
		}
	}
	return r.RemoveAll(name)
}

// check makes sure c is still what the scan saw, without deleting it.
func (c *Candidate) check() error {
	r, err := c.reach()
	if err == nil {
		r.Close()
	}
	return err
}

// reach opens the folder c is in, one folder at a time from the layout
// folder it was found under, and checks that c is still what the scan saw.
// That layout folder must be the same one; every folder on the way must be
// a folder, not a link; and in a server's data folder none may hold a
// world. The caller closes the returned root.
func (c *Candidate) reach() (*os.Root, error) {
	r, err := os.OpenRoot(c.root)
	if err != nil {
		return nil, err
	}
	if fi, err := r.Stat("."); err != nil || !os.SameFile(fi, c.rootInfo) {
		r.Close()
		return nil, cmp.Or(err, errChanged)
	}
	names := strings.Split(c.rel, "/")
	for _, name := range names[:len(names)-1] {
		next, err := c.step(r, name)
		r.Close()
		if err != nil {
			return nil, err
		}
		r = next
	}
	if c.guard && isWorld(r) {
		r.Close()
		return nil, errChanged
	}
	fi, err := r.Lstat(names[len(names)-1])
	if err == nil && !sameEntry(fi, c.info) {
		err = errChanged
	}
	if err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

func (c *Candidate) step(r *os.Root, name string) (*os.Root, error) {
	if c.guard && isWorld(r) {
		return nil, errChanged
	}
	fi, err := r.Lstat(name)
	if err != nil {
		return nil, err
	}
	return openDir(r, name, fi)
}

// sameEntry reports whether a and b describe the same file, unchanged.
func sameEntry(a, b fs.FileInfo) bool {
	return os.SameFile(a, b) && a.Mode().Type() == b.Mode().Type() &&
		a.ModTime().Equal(b.ModTime()) && (a.IsDir() || a.Size() == b.Size())
}

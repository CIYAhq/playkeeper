package addons

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"

	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
)

// Result is what an install or update did.
type Result struct {
	// Installed are the records to store, each replacing any record with
	// the same key.
	Installed []Installed `json:"installed"`
	// Replaced are the records of the versions an update replaced.
	Replaced []Installed  `json:"replaced"`
	Manual   []ManualStep `json:"manual"`
	Warnings []Notice     `json:"warnings"`
	// RestartNeeded is always set: servers load add-ons when they start.
	RestartNeeded bool `json:"restartNeeded"`
}

// Progress is how an install or update is going, for showing it.
type Progress struct {
	// Plan is the plan being carried out.
	Plan *Plan
	// Step indexes Plan.Steps: the file being downloaded, or -1 before
	// the first download starts.
	Step int
	// Received counts the bytes of that file downloaded so far.
	Received int64
	// Verified is set once the file matched its published size and hash.
	Verified bool
}

// Install carries out PlanInstall's plan. Every file is downloaded from the
// source's own hosts into TempDir and checked against the published size and
// hash before anything is written to the server's folder; then the files are
// put in place without overwriting anything. It refuses when req.Fingerprint
// is set and the plan has changed since.
func (l *Library) Install(ctx context.Context, srv Server, installed []Installed, req InstallRequest) (*Result, error) {
	p, err := l.PlanInstall(ctx, srv, installed, req)
	if err != nil {
		return nil, err
	}
	if req.Fingerprint != "" && req.Fingerprint != p.Fingerprint {
		return nil, planChanged()
	}
	return l.apply(ctx, srv, p, req.OnProgress)
}

func planChanged() *Error {
	return fail(KindPlanChanged, nil, "What this would do has changed since you confirmed it.", "Review the new plan and confirm again.")
}

func (l *Library) apply(ctx context.Context, srv Server, p *Plan, progress func(Progress)) (*Result, error) {
	if progress == nil {
		progress = func(Progress) {}
	}
	if len(p.Blockers) > 0 {
		return nil, &Error{Notice: p.Blockers[0]}
	}
	if len(p.Steps) == 0 {
		if len(p.Manual) > 0 {
			return nil, &Error{Notice: p.Manual[0].Notice}
		}
		return nil, fail(KindUpToDate, nil, "There is nothing to install.", "")
	}
	max := l.maxFileSize()
	for _, s := range p.Steps {
		switch {
		case !validFileName(s.FileName):
			return nil, badFileName(s)
		case s.Size > max:
			return nil, tooLarge(s, max)
		}
		if _, err := l.fileHosts(s.Source).Check(s.url); err != nil {
			return nil, downloadError(s, err, max)
		}
	}

	stage, err := os.MkdirTemp(l.TempDir, "playkeeper-addons-")
	if err != nil {
		return nil, &Error{Notice: notice(KindFolderUnusable, kv("folder", "temp"),
			"Playkeeper could not make a temporary folder for downloads: "+err.Error()+".",
			"Check that the machine has free disk space."), Err: err}
	}
	defer os.RemoveAll(stage)
	staged := make([]string, len(p.Steps))
	progress(Progress{Plan: p, Step: -1})
	for i, s := range p.Steps {
		var received int64
		progress(Progress{Plan: p, Step: i})
		path, err := fetch.Download(ctx, l.HTTP, l.fileHosts(s.Source), l.userAgent(), s.url, stage,
			fetch.Want{Algo: s.HashAlgo, Hash: s.Hash, Size: s.Size, Max: max, Progress: func(n int64) {
				received = n
				progress(Progress{Plan: p, Step: i, Received: n})
			}})
		if err != nil {
			return nil, downloadError(s, err, max)
		}
		staged[i] = path
		progress(Progress{Plan: p, Step: i, Received: received, Verified: true})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	root, err := openFolder(srv, p.Target, true)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	tx := &txn{root: root, t: p.Target, owner: srv.Owner, max: max}
	if err := tx.run(ctx, p.Steps, staged); err != nil {
		tx.rollback()
		return nil, err
	}
	tx.commit()

	now := l.now()
	res := &Result{Installed: []Installed{}, Replaced: []Installed{}, Manual: p.Manual, Warnings: p.Warnings, RestartNeeded: true}
	for _, s := range p.Steps {
		res.Installed = append(res.Installed, s.record(now))
		if s.Replaces != nil {
			res.Replaced = append(res.Replaced, *s.Replaces)
		}
	}
	return res, nil
}

// txn puts verified files into the folder and can take them out again.
// Files are replaced by name and never rewritten in place, so a running
// server keeps reading the jars it has open.
type txn struct {
	root   *os.Root
	t      Target
	owner  *Owner
	max    int64 // the largest file hashed for a record without a size
	placed []string
	moved  [][2]string // original name, hidden name
}

func (tx *txn) run(ctx context.Context, steps []Step, staged []string) error {
	for _, s := range steps {
		old := s.Replaces
		if old == nil || !validFileName(old.FileName) {
			continue
		}
		if err := tx.check(ctx, *old, s.replaceChanged); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		hidden := "." + old.FileName + ".playkeeper-old-" + randomHex()
		if err := tx.root.Rename(old.FileName, hidden); err != nil {
			return folderError(tx.t, err)
		}
		tx.moved = append(tx.moved, [2]string{old.FileName, hidden})
	}
	for i, s := range steps {
		if err := place(tx.root, staged[i], s.FileName, tx.owner); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return &Error{Notice: fileExists(s, tx.t), Err: err}
			}
			return folderError(tx.t, err)
		}
		tx.placed = append(tx.placed, s.FileName)
	}
	return nil
}

// check refuses to replace old's file when it changed since the install,
// unless the user agreed to replace a changed file; a folder or link in its
// place is refused either way.
func (tx *txn) check(ctx context.Context, old Installed, changed bool) error {
	f, st, err := openFile(tx.root, old.FileName)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return err
	case errors.Is(err, errNotRegular):
		return &Error{Notice: modified(old, "replace"), Err: err}
	case err != nil:
		return folderError(tx.t, err)
	}
	defer f.Close()
	if changed {
		return nil
	}
	same, err := unchanged(ctx, f, st.Size(), old, tx.max)
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case err != nil:
		return folderError(tx.t, err)
	case !same:
		return &Error{Notice: modified(old, "replace")}
	}
	return nil
}

func (tx *txn) rollback() {
	for _, name := range tx.placed {
		tx.root.Remove(name)
	}
	for _, m := range slices.Backward(tx.moved) {
		tx.root.Rename(m[1], m[0])
	}
}

func (tx *txn) commit() {
	for _, m := range tx.moved {
		tx.root.Remove(m[1])
	}
}

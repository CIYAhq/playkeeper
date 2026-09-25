package addons

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"

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
	return l.apply(ctx, srv, p)
}

func planChanged() *Error {
	return fail(KindPlanChanged, nil, "What this would do has changed since you confirmed it.", "Review the new plan and confirm again.")
}

func (l *Library) apply(ctx context.Context, srv Server, p *Plan) (*Result, error) {
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
	for i, s := range p.Steps {
		path, err := fetch.Download(ctx, l.HTTP, l.fileHosts(s.Source), l.userAgent(), s.url, stage,
			fetch.Want{Algo: s.HashAlgo, Hash: s.Hash, Size: s.Size, Max: max})
		if err != nil {
			return nil, downloadError(s, err, max)
		}
		staged[i] = path
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	root, err := openFolder(srv, p.Target, true)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	tx := &txn{root: root, t: p.Target, owner: srv.Owner}
	if err := tx.run(p.Steps, staged); err != nil {
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
	placed []string
	moved  [][2]string // original name, hidden name
}

func (tx *txn) run(steps []Step, staged []string) error {
	for _, s := range steps {
		old := s.Replaces
		if old == nil || !validFileName(old.FileName) {
			continue
		}
		sums, size, err := sumFile(tx.root, old.FileName, old.HashAlgo)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case !validHash(old.HashAlgo, old.Hash) || errors.Is(err, errNotRegular):
			return &Error{Notice: modified(*old, "replace"), Err: err}
		case err != nil:
			return folderError(tx.t, err)
		case sums[old.HashAlgo] != strings.ToLower(old.Hash) || old.Size > 0 && size != old.Size:
			return &Error{Notice: modified(*old, "replace")}
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

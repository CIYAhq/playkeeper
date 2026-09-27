package modpacks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/modpacks/mrpack"
)

// RemovePlan is what removing a pack would do, for the user to confirm.
type RemovePlan struct {
	Pack addons.Installed `json:"pack"`
	// Changes lists the files removed, and the pack files kept because the
	// user changed them or had them before the pack (Yours).
	Changes     []Change        `json:"changes"`
	Warnings    []addons.Notice `json:"warnings"`
	Fingerprint string          `json:"fingerprint"`

	ops []op
}

// RemoveResult is what a removal did. The caller deletes its Record.
type RemoveResult struct {
	Removed       []string `json:"removed"`
	Kept          []string `json:"kept"`
	RestartNeeded bool     `json:"restartNeeded"`
}

// recorded reports whether a record's file is one Playkeeper may touch: a
// record is only trusted as far as the rules for pack files go.
func recorded(f *File, world string) bool {
	return (f.HashAlgo == "sha512" || f.HashAlgo == "sha1") && mrpack.CheckPath(f.Path) == nil && classify(f.Path, world) == classPack
}

// PlanRemove works out what removing the pack would do: the pack's files
// that are as it left them are removed; files the user changed, files that
// were on the server before the pack, the world and everything else stay.
func (l *Library) PlanRemove(srv Server, rec Record) (*RemovePlan, error) {
	root, err := openServer(srv)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name, r := rec.Pack.Name, rec.Requirements
	pl := &RemovePlan{Pack: rec.Pack, Changes: []Change{}, Warnings: []addons.Notice{
		notice(KindModdedWorld, kv("pack", name, "type", r.Type, "minecraft", r.MinecraftVersion),
			fmt.Sprintf("The server stays a %s server on Minecraft %s, and its world stays as it is.", typeName(r.Type), printable(r.MinecraftVersion)),
			"Blocks and items from the pack's mods disappear from the world when it starts without them. Take a backup first."),
	}}
	files := slices.Clone(rec.Files)
	slices.SortFunc(files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	for i := range files {
		f := &files[i]
		if !recorded(f, srv.world()) {
			continue
		}
		st, err := inspect(root, f.Path, f.HashAlgo)
		c := Change{Path: f.Path, Origin: f.Origin, Project: f.Project, Optional: f.Optional, Size: st.size}
		switch {
		case err != nil:
			return nil, serverFolderError(err)
		case !st.exists || st.blocked != "":
			continue
		case !st.regular || f.Preexisting || st.sums[f.HashAlgo] != strings.ToLower(f.Hash):
			c.Action, c.Yours = ActionKeep, true
		default:
			c.Action = ActionRemove
			pl.ops = append(pl.ops, op{path: f.Path, action: ActionRemove, algo: f.HashAlgo, sum: st.sums[f.HashAlgo], size: st.size})
		}
		pl.Changes = append(pl.Changes, c)
	}
	b, _ := json.Marshal(struct {
		Pack    addons.Installed
		Changes []Change
	}{pl.Pack, pl.Changes})
	sum := sha256.Sum256(b)
	pl.Fingerprint = hex.EncodeToString(sum[:16])
	return pl, nil
}

// Remove takes the pack off the server as PlanRemove plans it, refusing
// when the plan differs from the one the user confirmed. If a file cannot
// be removed, the others are put back. The server should be stopped.
func (l *Library) Remove(srv Server, rec Record, fingerprint string) (*RemoveResult, error) {
	pl, err := l.PlanRemove(srv, rec)
	if err != nil {
		return nil, err
	}
	if fingerprint != "" && fingerprint != pl.Fingerprint {
		return nil, planChanged()
	}
	root, err := openServer(srv)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	tx := &txn{root: root, owner: srv.Owner, pack: rec.Pack.Name}
	if _, err := tx.run(pl.ops, nil); err != nil {
		tx.rollback()
		return nil, err
	}
	tx.commit()
	res := &RemoveResult{Removed: []string{}, Kept: []string{}, RestartNeeded: true}
	for _, c := range pl.Changes {
		if c.Action == ActionRemove {
			res.Removed = append(res.Removed, c.Path)
		} else {
			res.Kept = append(res.Kept, c.Path)
		}
	}
	return res, nil
}

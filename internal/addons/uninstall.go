package addons

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

// UninstallOptions tune a removal.
type UninstallOptions struct {
	// RemoveConfig also deletes a plugin's settings folder
	// (plugins/<plugin name>/). It is kept by default, so reinstalling
	// picks up where the server left off.
	RemoveConfig bool `json:"removeConfig,omitempty"`
	// Force removes the add-on even when installed add-ons need it.
	Force bool `json:"force,omitempty"`
}

// Removal is what Uninstall did.
type Removal struct {
	// Removed is the record to delete from the addons table.
	Removed Installed `json:"removed"`
	// ConfigFolder is the plugin's settings folder inside plugins/, when it
	// has one; ConfigRemoved says whether it was deleted.
	ConfigFolder  string `json:"configFolder,omitempty"`
	ConfigRemoved bool   `json:"configRemoved"`
	// Orphans are dependencies that were there only for the removed add-on;
	// nothing else installed needs them now. The UI can offer to remove them
	// as well.
	Orphans       []Installed `json:"orphans"`
	Warnings      []Notice    `json:"warnings"`
	RestartNeeded bool        `json:"restartNeeded"`
}

// Uninstall removes an add-on Playkeeper installed. The file is deleted only
// while it is unchanged since the install; the removal is refused while
// installed add-ons need it, unless forced.
func (l *Library) Uninstall(srv Server, installed []Installed, key Key, opts UninstallOptions) (*Removal, error) {
	t, err := TargetFor(srv.Type)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(installed, func(rec Installed) bool { return rec.Key() == key })
	if i < 0 {
		return nil, fail(KindNotManaged, kv("source", key.Source.Name(), "project", printable(key.ProjectID)),
			"Playkeeper did not install this add-on, so it will not delete it.",
			"Delete its file in the file manager instead.")
	}
	rec := installed[i]
	if !validFileName(rec.FileName) {
		return nil, fail(KindBadFileName, kv("name", rec.Name, "file", printable(rec.FileName)),
			fmt.Sprintf("The record of %s names the file \"%s\", which Playkeeper will not touch.", rec.Name, printable(rec.FileName)),
			"Delete the file in the file manager, then remove the add-on from the list.")
	}
	if needers := neededBy(installed, rec); len(needers) > 0 && !opts.Force {
		list := joinNames(needers)
		return nil, fail(KindNeededBy, kv("name", rec.Name, "dependents", list),
			fmt.Sprintf("%s is needed by %s.", rec.Name, list),
			fmt.Sprintf("Remove %s first, or remove %s anyway.", list, rec.Name))
	}

	out := &Removal{Removed: rec, Orphans: orphans(installed, rec), Warnings: []Notice{}, RestartNeeded: true}
	gone := notice(KindNotFound, kv("name", rec.Name, "file", rec.FileName, "folder", t.Folder),
		fmt.Sprintf("%s was already gone from the %s folder.", rec.FileName, t.Folder), "")
	root, err := openFolder(srv, t, false)
	if err != nil {
		return nil, err
	}
	if root == nil {
		out.Warnings = append(out.Warnings, gone)
		return out, nil
	}
	defer root.Close()

	sums, size, err := sumFile(root, rec.FileName, rec.HashAlgo)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		out.Warnings = append(out.Warnings, gone)
		return out, nil
	case !validHash(rec.HashAlgo, rec.Hash) || errors.Is(err, errNotRegular):
		return nil, &Error{Notice: modified(rec, "delete"), Err: err}
	case err != nil:
		return nil, folderError(t, err)
	case sums[rec.HashAlgo] != strings.ToLower(rec.Hash) || rec.Size > 0 && size != rec.Size:
		return nil, &Error{Notice: modified(rec, "delete")}
	}
	meta := metaIn(root, rec.FileName)
	if err := root.Remove(rec.FileName); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, folderError(t, err)
	}

	if t.Kind != "plugin" || !validFolderName(meta.ID) {
		return out, nil
	}
	if fi, err := root.Lstat(meta.ID); err != nil || !fi.IsDir() {
		return out, nil
	}
	out.ConfigFolder = meta.ID
	if opts.RemoveConfig {
		if err := root.RemoveAll(meta.ID); err != nil {
			out.Warnings = append(out.Warnings, notice(KindFolderUnusable, kv("folder", t.Folder+"/"+meta.ID),
				fmt.Sprintf("%s was removed, but its settings folder %s/%s could not be deleted: %s.", rec.Name, t.Folder, meta.ID, err),
				"Delete it in the file manager."))
		} else {
			out.ConfigRemoved = true
		}
	}
	return out, nil
}

// neededBy names the installed add-ons (same source) that need rec.
func neededBy(installed []Installed, rec Installed) []string {
	var out []string
	for _, o := range installed {
		if o.Key() == rec.Key() || o.Source != rec.Source {
			continue
		}
		if slices.Contains(o.Requires, rec.ProjectID) {
			out = append(out, o.Name)
		}
	}
	return out
}

// orphans are the dependencies rec kept on the server, installed for it or
// needed by it after the add-on they came with was removed, that nothing
// else installed needs.
func orphans(installed []Installed, rec Installed) []Installed {
	out := []Installed{}
	for _, o := range installed {
		if o.Source != rec.Source || o.DependencyOf == "" || o.Key() == rec.Key() {
			continue
		}
		forRec := o.DependencyOf == rec.ProjectID
		parentGone := forRec || !slices.ContainsFunc(installed, func(x Installed) bool {
			return x.Source == o.Source && x.ProjectID == o.DependencyOf
		})
		needed := slices.ContainsFunc(installed, func(x Installed) bool {
			return x.Key() != rec.Key() && x.Key() != o.Key() && x.Source == o.Source && slices.Contains(x.Requires, o.ProjectID)
		})
		if parentGone && (forRec || slices.Contains(rec.Requires, o.ProjectID)) && !needed {
			out = append(out, o)
		}
	}
	return out
}

func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

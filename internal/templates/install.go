package templates

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/CIYAhq/playkeeper/internal/addons"
)

// Installer is the part of the add-on library an import uses.
type Installer interface {
	PlanInstall(ctx context.Context, srv addons.Server, installed []addons.Installed, req addons.InstallRequest) (*addons.Plan, error)
	Install(ctx context.Context, srv addons.Server, installed []addons.Installed, req addons.InstallRequest) (*addons.Result, error)
}

var _ Installer = (*addons.Library)(nil)

// AddonStatus is what happened to one add-on of an import.
type AddonStatus string

const (
	AddonInstalled AddonStatus = "installed"
	// AddonPresent is an add-on the server already has, usually because an
	// add-on installed before it needed it.
	AddonPresent AddonStatus = "present"
	AddonSkipped AddonStatus = "skipped"
)

// AddonResult is what InstallAddon did.
type AddonResult struct {
	Status AddonStatus `json:"status"`
	Addon  Addon       `json:"addon"`
	// Records are the add-on library's records to store: the add-on and
	// any dependencies it brought.
	Records []addons.Installed `json:"records"`
	// Reason says why the add-on was skipped.
	Reason   *Notice             `json:"reason,omitempty"`
	Manual   []addons.ManualStep `json:"manual"`
	Warnings []Notice            `json:"warnings"`
}

// InstallAddon installs one add-on of a confirmed import on the new server,
// through the add-on library, which downloads it from its source and
// verifies it against the hash the source publishes. Before that, the
// source's answer must match the template: the same project and name and,
// when pinned, the same version and hash. An add-on that cannot be
// installed is skipped with the reason; err is only for failures that
// should stop the import, such as a cancelled context. installed are the
// server's add-ons so far, including the Records of earlier calls.
func InstallAddon(ctx context.Context, lib Installer, srv addons.Server, installed []addons.Installed, a PlannedAddon) (*AddonResult, error) {
	res := &AddonResult{Addon: a.Addon, Records: []addons.Installed{}, Manual: []addons.ManualStep{}, Warnings: []Notice{}}
	if slices.ContainsFunc(installed, func(rec addons.Installed) bool { return rec.Key() == a.Key() }) {
		res.Status = AddonPresent
		return res, nil
	}
	skip := func(n Notice) (*AddonResult, error) {
		res.Status, res.Reason = AddonSkipped, &n
		return res, nil
	}
	req := request(a.Addon, a.Unpinned)
	plan, err := lib.PlanInstall(ctx, srv, installed, req)
	if err != nil {
		if n, ok := refusal(err); ok {
			return skip(n)
		}
		return nil, err
	}
	res.Warnings = append(res.Warnings, plan.Warnings...)
	if len(plan.Steps) == 0 {
		res.Manual = append(res.Manual, plan.Manual...)
		switch {
		case len(plan.Blockers) > 0:
			return skip(plan.Blockers[0])
		case len(plan.Manual) > 0:
			return skip(plan.Manual[0].Notice)
		}
		return skip(notice(addons.KindUpToDate, nil, "There is nothing to install.", ""))
	}
	s := plan.Steps[0]
	if s.Source != a.Source || s.ProjectID != a.Project || !sameName(a.Addon, s) {
		return skip(projectMismatch(a.Addon, s))
	}
	if p := a.Pin; p != nil && !a.Unpinned && (s.VersionID != p.VersionID || s.HashAlgo != p.HashAlgo || strings.ToLower(s.Hash) != p.Hash) {
		return skip(pinMismatch(a.Addon, s))
	}
	if !plan.Ready {
		res.Manual = append(res.Manual, plan.Manual...)
		return skip(plan.Blockers[0])
	}
	req.Fingerprint = plan.Fingerprint
	out, err := lib.Install(ctx, srv, installed, req)
	if err != nil {
		if n, ok := refusal(err); ok {
			return skip(n)
		}
		return nil, err
	}
	res.Status = AddonInstalled
	res.Records = append(res.Records, out.Installed...)
	res.Manual = append(res.Manual, out.Manual...)
	res.Warnings = append(res.Warnings[:0], out.Warnings...)
	return res, nil
}

// LinkDependencies returns the records to store again once every add-on of
// an import is installed. An add-on the template marks as needed by
// another is installed first, on its own, so its record says the user
// chose it; this marks it as that add-on's dependency again, where the
// other add-on's record says it requires it.
func LinkDependencies(installed []addons.Installed, p *Plan) []addons.Installed {
	parents := map[addons.Key]string{}
	for _, a := range p.Addons {
		if a.DependencyOf != "" {
			parents[a.Key()] = a.DependencyOf
		}
	}
	out := []addons.Installed{}
	for _, rec := range installed {
		parent, ok := parents[rec.Key()]
		if !ok || rec.DependencyOf != "" {
			continue
		}
		if slices.ContainsFunc(installed, func(o addons.Installed) bool {
			return o.Source == rec.Source && o.ProjectID == parent && slices.Contains(o.Requires, rec.ProjectID)
		}) {
			rec.DependencyOf = parent
			out = append(out, rec)
		}
	}
	return out
}

// refusal is the notice of the add-on library's refusal in err.
func refusal(err error) (Notice, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Notice, true
	}
	return Notice{}, false
}

// sameName is true when the source's project carries the template's name
// or slug, ignoring case, spaces and punctuation.
func sameName(a Addon, s addons.Step) bool {
	return norm(a.Name) == norm(s.Name) || a.Slug != "" && norm(a.Slug) == norm(s.Slug)
}

func norm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func projectMismatch(a Addon, s addons.Step) Notice {
	name, found := printable(a.Name), printable(s.Name)
	return notice(KindProjectMismatch, kv("name", name, "found", found, "source", a.Source.Name()),
		fmt.Sprintf("The template calls this add-on %s, but %s's project %s is %s, so Playkeeper did not install it.", name, a.Source.Name(), a.Project, found),
		"The template may be out of date or altered. Install the add-on you want from the library.")
}

func pinMismatch(a Addon, s addons.Step) Notice {
	name := printable(a.Name)
	return notice(KindPinMismatch, kv("name", name, "version", printable(a.Pin.VersionNumber), "source", a.Source.Name()),
		fmt.Sprintf("The file %s offers for %s %s is not the one the template names, so Playkeeper did not install it.", a.Source.Name(), name, printable(a.Pin.VersionNumber)),
		"The template may be out of date or altered. Install "+name+" from the library if you want it.")
}

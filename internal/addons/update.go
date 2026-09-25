package addons

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/addons/modrinth"
)

// UpdateRequest asks to update add-ons on one server.
type UpdateRequest struct {
	// Keys are the add-ons to update; empty means every one with an update.
	Keys []Key `json:"keys,omitempty"`
	// VersionID moves the one add-on in Keys to that version, older or
	// newer.
	VersionID string `json:"versionId,omitempty"`
	// AllowPrerelease offers beta and alpha versions to add-ons installed
	// as releases too.
	AllowPrerelease bool `json:"allowPrerelease,omitempty"`
	// Fingerprint is the Plan.Fingerprint the user confirmed; see
	// InstallRequest.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// UpdateStatus says whether a newer version of an installed add-on fits the
// server.
type UpdateStatus struct {
	Source    Source      `json:"source"`
	ProjectID string      `json:"projectId"`
	Name      string      `json:"name"`
	Current   VersionInfo `json:"current"`
	// Latest is the newest version that fits the server; nil when none does.
	Latest    *VersionInfo `json:"latest,omitempty"`
	Available bool         `json:"available"`
	// Notice explains a missing Latest, e.g. no version for the server's
	// Minecraft version yet.
	Notice *Notice `json:"notice,omitempty"`
}

func (i Installed) info() VersionInfo {
	return VersionInfo{VersionID: i.VersionID, VersionNumber: i.VersionNumber, Channel: i.Channel, Published: i.Published, FileName: i.FileName, Size: i.Size}
}

// CheckUpdates looks for newer versions of the installed add-ons that fit
// the server. Add-ons installed as releases are offered releases; those
// installed as pre-releases are offered newer pre-releases too. Modrinth is
// asked in one batch by file hash, Hangar once per add-on.
func (l *Library) CheckUpdates(ctx context.Context, srv Server, installed []Installed) ([]UpdateStatus, error) {
	t, err := TargetFor(srv.Type)
	if err != nil {
		return nil, err
	}
	if err := checkMinecraft(srv); err != nil {
		return nil, err
	}
	latest, err := l.latest(ctx, srv, t, installed, func(rec Installed) bool { return rec.Channel != release })
	if err != nil {
		return nil, err
	}
	out := []UpdateStatus{}
	for _, rec := range installed {
		st := UpdateStatus{Source: rec.Source, ProjectID: rec.ProjectID, Name: rec.Name, Current: rec.info()}
		if lr := latest[rec.Key()]; lr.c != nil {
			v := lr.c.info()
			st.Latest, st.Available = &v, newer(*lr.c, rec)
		} else {
			st.Notice = lr.notice
		}
		out = append(out, st)
	}
	return out, nil
}

// PlanUpdate works out what updating installed add-ons would do: the newest
// version of each that fits the server (see CheckUpdates; AllowPrerelease
// widens it), new required dependencies, conflicts, and files that changed
// since they were installed. An add-on whose file has gone missing is
// installed again.
func (l *Library) PlanUpdate(ctx context.Context, srv Server, installed []Installed, req UpdateRequest) (*Plan, error) {
	t, err := TargetFor(srv.Type)
	if err != nil {
		return nil, err
	}
	if err := checkMinecraft(srv); err != nil {
		return nil, err
	}
	targets, err := updateTargets(installed, req)
	if err != nil {
		return nil, err
	}
	identify := slices.ContainsFunc(targets, func(rec Installed) bool { return rec.Source == Modrinth })
	r, err := l.resolver(ctx, srv, t, installed, identify, true, req.AllowPrerelease)
	if err != nil {
		return nil, err
	}
	missing := func(rec Installed) bool {
		lf := r.inv.byName[rec.FileName]
		return lf == nil || lf.rec == nil || lf.rec.Key() != rec.Key()
	}

	if req.VersionID != "" {
		rec := targets[0]
		if err := t.supports(rec.Source); err != nil {
			return nil, err
		}
		c, err := l.choose(ctx, srv, t, recProject(rec), req.VersionID, true)
		if err != nil {
			return nil, err
		}
		if c.VersionID == rec.VersionID && !missing(rec) {
			return nil, upToDate(rec)
		}
		r.add(c, ActionUpdate, nil, &rec)
	} else {
		latest, err := l.latest(ctx, srv, t, targets, func(rec Installed) bool { return req.AllowPrerelease || rec.Channel != release })
		if err != nil {
			return nil, err
		}
		for _, rec := range targets {
			lr := latest[rec.Key()]
			switch {
			case lr.c != nil && (newer(*lr.c, rec) || missing(rec)):
				r.add(*lr.c, ActionUpdate, nil, &rec)
			case lr.notice != nil && len(req.Keys) > 0:
				r.warn(*lr.notice)
			}
		}
		if len(r.plan.Steps) == 0 && len(r.plan.Manual) == 0 {
			if len(targets) == 1 {
				if n := latest[targets[0].Key()].notice; n != nil {
					return nil, &Error{Notice: *n}
				}
				return nil, upToDate(targets[0])
			}
			return nil, fail(KindUpToDate, nil, "Every add-on is up to date.", "")
		}
	}
	if err := r.resolve(ctx); err != nil {
		return nil, err
	}
	return r.finish(), nil
}

// Update carries out PlanUpdate's plan the way Install does, refusing when
// req.Fingerprint is set and the plan has changed since. A replaced file is
// deleted only when it is unchanged since it was installed.
func (l *Library) Update(ctx context.Context, srv Server, installed []Installed, req UpdateRequest) (*Result, error) {
	p, err := l.PlanUpdate(ctx, srv, installed, req)
	if err != nil {
		return nil, err
	}
	if req.Fingerprint != "" && req.Fingerprint != p.Fingerprint {
		return nil, planChanged()
	}
	return l.apply(ctx, srv, p)
}

func updateTargets(installed []Installed, req UpdateRequest) ([]Installed, error) {
	if req.VersionID != "" && len(req.Keys) != 1 {
		return nil, fail(KindInvalid, kv("field", "versionId"), "A version can only be chosen for one add-on at a time.", "")
	}
	if req.VersionID != "" && !validRef(req.VersionID) {
		return nil, fail(KindInvalid, kv("field", "versionId"), "That is not a valid version id.", "Pick the version from the list.")
	}
	if len(req.Keys) == 0 {
		return installed, nil
	}
	var out []Installed
	for _, k := range req.Keys {
		i := slices.IndexFunc(installed, func(rec Installed) bool { return rec.Key() == k })
		if i < 0 {
			return nil, notManaged(k)
		}
		if !slices.ContainsFunc(out, func(rec Installed) bool { return rec.Key() == k }) {
			out = append(out, installed[i])
		}
	}
	return out, nil
}

func notManaged(k Key) *Error {
	return fail(KindNotManaged, kv("source", k.Source.Name(), "project", printable(k.ProjectID)),
		"Playkeeper did not install this add-on, so it cannot manage it.",
		"Scan the folder to let Playkeeper identify files added by hand.")
}

func upToDate(rec Installed) *Error {
	return fail(KindUpToDate, kv("name", rec.Name, "version", rec.VersionNumber),
		fmt.Sprintf("%s is already at version %s.", rec.Name, rec.VersionNumber), "")
}

// newer reports whether c is an update for rec: another version, published
// later (a newer release never loses to an older pre-release).
func newer(c candidate, rec Installed) bool {
	return c.VersionID != rec.VersionID && (rec.Published.IsZero() || c.Published.After(rec.Published))
}

func recProject(rec Installed) *project {
	return &project{Source: rec.Source, ID: rec.ProjectID, Slug: rec.Slug, Name: rec.Name, IconURL: rec.IconURL}
}

type latestResult struct {
	c      *candidate
	notice *Notice
}

// latest finds, for each installed add-on, the newest version that fits the
// server; pre says whose pre-releases count.
func (l *Library) latest(ctx context.Context, srv Server, t Target, recs []Installed, pre func(Installed) bool) (map[Key]latestResult, error) {
	out := map[Key]latestResult{}
	var batch [2][]Installed // releases only, any channel
	for _, rec := range recs {
		if err := t.supports(rec.Source); err != nil {
			var e *Error
			errors.As(err, &e)
			out[rec.Key()] = latestResult{notice: &e.Notice}
			continue
		}
		if rec.Source == Modrinth && rec.HashAlgo == "sha512" && validHash("sha512", rec.Hash) {
			i := 0
			if pre(rec) {
				i = 1
			}
			batch[i] = append(batch[i], rec)
			continue
		}
		lr, err := l.latestOne(ctx, srv, t, rec, pre(rec))
		if err != nil {
			return nil, err
		}
		out[rec.Key()] = lr
	}

	mc := srv.MinecraftVersion
	for i, group := range batch {
		if len(group) == 0 {
			continue
		}
		var hashes []string
		for _, rec := range group {
			hashes = append(hashes, strings.ToLower(rec.Hash))
		}
		types := []string{release}
		if i == 1 {
			types = nil
		}
		m, err := l.Modrinth.LatestVersionsFromHashes(ctx, "sha512", hashes, modrinth.VersionFilter{Loaders: t.Loaders, GameVersions: []string{mc}}, types)
		if err != nil {
			return nil, upstream(Modrinth, err)
		}
		for _, rec := range group {
			if v, ok := m[strings.ToLower(rec.Hash)]; ok && v.ProjectID == rec.ProjectID {
				if c, fits := modrinthCandidate(recProject(rec), &v, t, mc); fits && (i == 1 || c.Channel == release) {
					out[rec.Key()] = latestResult{c: &c}
					continue
				}
			}
			// Modrinth no longer knows the file, or its newest version does
			// not run on this server: look through the project's versions.
			lr, err := l.latestOne(ctx, srv, t, rec, i == 1)
			if err != nil {
				return nil, err
			}
			out[rec.Key()] = lr
		}
	}
	return out, nil
}

func (l *Library) latestOne(ctx context.Context, srv Server, t Target, rec Installed, pre bool) (latestResult, error) {
	mc := srv.MinecraftVersion
	cands, err := l.candidates(ctx, t, mc, recProject(rec))
	if KindOf(err) == KindNotFound {
		n := notice(KindNotFound, kv("name", rec.Name, "source", rec.Source.Name()),
			fmt.Sprintf("%s is no longer listed on %s.", rec.Name, rec.Source.Name()),
			"It keeps working as it is, but Playkeeper cannot update it.")
		return latestResult{notice: &n}, nil
	}
	if err != nil {
		return latestResult{}, err
	}
	for _, c := range cands {
		if pre || c.Channel == release {
			return latestResult{c: &c}, nil
		}
	}
	var n Notice
	if len(cands) > 0 {
		n = notice(KindOnlyPrerelease, kv("name", rec.Name, "version", cands[0].Number, "channel", cands[0].Channel, "minecraft", mc),
			fmt.Sprintf("%s has only pre-release versions for Minecraft %s (newest: %s).", rec.Name, mc, cands[0].Number),
			"Allow pre-releases to update to one.")
	} else {
		n = notice(KindNoVersion, kv("name", rec.Name, "minecraft", mc, "type", t.Type),
			fmt.Sprintf("%s has no version for %s servers on Minecraft %s.", rec.Name, t.Name(), mc),
			"The installed version may not work on this Minecraft version. Check the add-on's page, or remove it.")
	}
	return latestResult{notice: &n}, nil
}

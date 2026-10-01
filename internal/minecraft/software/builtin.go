package software

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// BuiltInLists are the version lists built into Playkeeper, for a machine
// with no list of its own while a type's upstream can't be reached: each
// type's releases as Catalog offered them, and what Resolve found for each
// release's pin, as cmd/version-lists wrote them (make version-lists).
// Reading them needs no network, Mojang's included: each release carries
// the Java its Minecraft version needs and each resolved pin Mojang's
// server jar, so a type whose metadata API is down still installs its
// recommended builds from its download hosts.
type BuiltInLists struct {
	Types map[string]BuiltInType `json:"types"`
}

// BuiltInType is one type's list, as its upstream listed it at MadeAt.
type BuiltInType struct {
	MadeAt   time.Time  `json:"madeAt"`
	Releases []Release  `json:"releases"`
	Resolved []Resolved `json:"resolved"`
}

//go:embed builtin/lists.json
var builtinListsJSON []byte

var builtinLists = sync.OnceValues(func() (BuiltInLists, error) { return ParseBuiltInLists(builtinListsJSON) })

// ParseBuiltInLists reads lists cmd/version-lists wrote, refusing any a
// machine couldn't use as they are: a release or a resolved pin that
// doesn't validate, a type whose releases have no resolved pin, a download
// on a host its upstream doesn't serve from.
func ParseBuiltInLists(b []byte) (BuiltInLists, error) {
	var l BuiltInLists
	if err := json.Unmarshal(b, &l); err != nil {
		return BuiltInLists{}, fmt.Errorf("the version lists built into Playkeeper can't be read: %w", err)
	}
	for typ, t := range l.Types {
		if err := t.check(typ); err != nil {
			return BuiltInLists{}, fmt.Errorf("the %s list built into Playkeeper can't be used: %w", typeName(typ), err)
		}
	}
	return l, nil
}

func (t BuiltInType) check(typ string) error {
	if !Supported(typ) || t.MadeAt.IsZero() || len(t.Releases) == 0 {
		return errors.New("it is empty")
	}
	recommended := 0
	for _, r := range t.Releases {
		if r.Type != typ || r.Pin.Type != typ || r.ID != typ+"-"+r.MinecraftVersion || r.Pin.MinecraftVersion != r.MinecraftVersion {
			return fmt.Errorf("its release %q isn't one of its own", r.ID)
		}
		if err := r.Pin.Validate(); err != nil {
			return err
		}
		if r.Recommended {
			recommended++
		}
		if !slices.ContainsFunc(t.Resolved, func(res Resolved) bool { return res.Pin == r.Pin }) {
			return fmt.Errorf("nothing is resolved for %s", r.ID)
		}
	}
	if recommended != 1 {
		return fmt.Errorf("%d releases are recommended", recommended)
	}
	for _, res := range t.Resolved {
		if res.Pin.Type != typ {
			return fmt.Errorf("it resolves a %s pin", res.Pin.Type)
		}
		if err := res.Check(); err != nil {
			return err
		}
	}
	return nil
}

// BuiltInCatalog is Catalog from the list built into Playkeeper, and when
// that list was made.
func (Sources) BuiltInCatalog(typeID string) ([]Release, time.Time, error) {
	l, err := builtinLists()
	if err != nil {
		return nil, time.Time{}, err
	}
	return l.Catalog(typeID)
}

// BuiltInResolved is what Resolve found for pin when the list built into
// Playkeeper was made, if it has the pin.
func (Sources) BuiltInResolved(pin Pin) (Resolved, time.Time, bool) {
	l, err := builtinLists()
	if err != nil {
		return Resolved{}, time.Time{}, false
	}
	return l.ResolvedFor(pin)
}

// Catalog is a type's releases in these lists, and when they were made.
func (l BuiltInLists) Catalog(typeID string) ([]Release, time.Time, error) {
	t, ok := l.Types[typeID]
	if !ok {
		return nil, time.Time{}, ErrNoBuiltInList
	}
	return slices.Clone(t.Releases), t.MadeAt, nil
}

// ResolvedFor is what Resolve found for pin when these lists were made.
func (l BuiltInLists) ResolvedFor(pin Pin) (Resolved, time.Time, bool) {
	t := l.Types[pin.Type]
	i := slices.IndexFunc(t.Resolved, func(r Resolved) bool { return r.Pin == pin })
	if i < 0 {
		return Resolved{}, time.Time{}, false
	}
	return t.Resolved[i], t.MadeAt, true
}

// Check refuses a resolved pin read back from storage that Playkeeper
// couldn't install as it is: one Plan refuses, or one with a download from
// a host its upstream doesn't serve from.
func (r Resolved) Check() error {
	if _, err := r.Plan(); err != nil {
		return err
	}
	artifacts := slices.Clone(r.Libraries)
	artifacts = append(artifacts, r.Server)
	if r.Software != nil {
		artifacts = append(artifacts, *r.Software)
	}
	for _, a := range artifacts {
		if !servesFrom(a.Upstream, a.Hosts) {
			return &Error{Kind: KindHostNotAllowed, Msg: fmt.Sprintf("The install plan for %s downloads %s from %v, where %s doesn't serve it.", typeName(r.Pin.Type), a.Path, a.Hosts, a.Upstream),
				Hint: "Choose the version again.", Params: map[string]string{"type": r.Pin.Type, "upstream": a.Upstream}}
		}
	}
	return nil
}

// servesFrom reports whether every host is one the upstream named name
// serves from, as its readers here know them.
func servesFrom(name string, hosts []string) bool {
	var known []string
	for _, u := range []upstream{mojangUpstream(nil), purpurUpstream(nil), fabricUpstream(nil), fabricMaven(nil), quiltUpstream(nil), quiltMaven(nil),
		neoforgeUpstream(nil), forgeMavenUpstream(nil), forgeFilesUpstream(nil)} {
		if u.name == name {
			known = append(known, u.hosts...)
		}
	}
	if len(hosts) == 0 {
		return false
	}
	for _, h := range hosts {
		if !slices.Contains(known, h) {
			return false
		}
	}
	return true
}

// RecordBuiltIn reads one type's list to build into Playkeeper: its
// releases and each release's resolved pin, checked as a machine checks
// them.
func (s Sources) RecordBuiltIn(ctx context.Context, typeID string, now time.Time) (BuiltInType, error) {
	rels, err := s.Catalog(ctx, typeID)
	if err != nil {
		return BuiltInType{}, err
	}
	t := BuiltInType{MadeAt: now.UTC().Truncate(time.Second), Releases: rels}
	for _, r := range rels {
		res, err := s.Resolve(ctx, r.Pin)
		if err != nil {
			return BuiltInType{}, err
		}
		t.Resolved = append(t.Resolved, res)
	}
	if err := t.check(typeID); err != nil {
		return BuiltInType{}, err
	}
	return t, nil
}

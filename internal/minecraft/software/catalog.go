package software

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// Channel says how finished a build is. Purpur calls its unfinished builds
// experimental; Fabric, Quilt and NeoForge call them beta.
type Channel string

const (
	Stable       Channel = "stable"
	Beta         Channel = "beta"
	Experimental Channel = "experimental"
)

// Note is the machine-readable kind of a release's Notes.
type Note string

const (
	NoteRecommended  Note = "recommended"
	NoteStable       Note = "stable"
	NoteExperimental Note = "experimental"
)

// Release is one Minecraft version a type offers, with the build of the
// type's software to pin for it.
type Release struct {
	ID               string `json:"id"`
	Type             string `json:"type"`
	Label            string `json:"label"`
	MinecraftVersion string `json:"minecraftVersion"`
	// Java is the Java version Mojang says this Minecraft version needs.
	Java         int     `json:"java"`
	Channel      Channel `json:"channel"`
	Experimental bool    `json:"experimental"`
	Recommended  bool    `json:"recommended"`
	Pin          Pin     `json:"pin"`
	Note         Note    `json:"note"`
	Notes        string  `json:"notes"`
}

// Build is one build of a type's software for a Minecraft version: a Purpur
// build, a Fabric or Quilt loader, or a NeoForge version.
type Build struct {
	Version     string  `json:"version"`
	Channel     Channel `json:"channel"`
	Recommended bool    `json:"recommended"`
	Pin         Pin     `json:"pin"`
}

var reRelease = regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}$`)

// family is a version's first two parts: 26.2 for 26.2.1, 1.21 for 1.21.11.
func family(v string) string {
	p := strings.SplitN(v, ".", 3)
	if len(p) < 2 {
		return v
	}
	return p[0] + "." + p[1]
}

// offeredFamily reports whether mc is a release Playkeeper offers at all:
// 1.21 and newer, like Paper.
func offeredFamily(mc string) bool {
	return len(mc) <= 16 && reRelease.MatchString(mc) && minecraft.CompareMinecraft(family(mc), "1.21") >= 0
}

// compareVersions compares build versions such as "0.30.1", "0.31.0-beta.4"
// and "26.2.0.88": numbers first, then a version without a suffix above the
// same numbers with one, then beta below pre below rc, then the numbers in
// the suffixes. Versions that only differ in how they are written, such as
// "1.0" and "1.0.0", are ordered by their text, so the order never depends
// on the upstream's.
func compareVersions(a, b string) int {
	an, as, _ := strings.Cut(a, "-")
	bn, bs, _ := strings.Cut(b, "-")
	if c := minecraft.CompareMinecraft(an, bn); c != 0 {
		return c
	}
	switch {
	case as == bs:
		return strings.Compare(a, b)
	case as == "":
		return 1
	case bs == "":
		return -1
	}
	al, anum, _ := strings.Cut(as, ".")
	bl, bnum, _ := strings.Cut(bs, ".")
	if c := cmp.Compare(suffixRank(al), suffixRank(bl)); c != 0 {
		return c
	}
	if c := minecraft.CompareMinecraft(anum, bnum); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

func suffixRank(label string) int {
	return slices.Index([]string{"alpha", "beta", "pre", "rc"}, label)
}

// choice is what a type offers for one Minecraft version.
type choice struct {
	pin     Pin
	channel Channel
}

// offer applies Paper's catalog rules to one type. Versions are taken newest
// first, whatever order the upstream lists them in. Only releases from 1.21
// on that Mojang lists and whose Java fits the image are offered, one per
// version family: the newest with a stable build. A newer version with only
// unfinished builds is offered too, flagged experimental, as long as nothing
// stable is newer. The newest stable entry is recommended.
func offer(ctx context.Context, hc *http.Client, man map[string]mojangEntry, typeID string, versions []string, pick func(mc string) (choice, bool, error)) ([]Release, error) {
	var list []string
	for _, v := range versions {
		if offeredFamily(v) && !slices.Contains(list, v) {
			list = append(list, v)
		}
	}
	slices.Sort(list)
	slices.SortStableFunc(list, func(a, b string) int { return minecraft.CompareMinecraft(b, a) })
	stableFamily := map[string]bool{}
	var out []Release
	for _, mc := range list {
		fam := family(mc)
		e, listed := man[mc]
		if stableFamily[fam] || !listed || e.Type != "release" {
			continue
		}
		mv, err := mojangVersionFor(ctx, hc, e)
		if err != nil {
			return nil, err
		}
		if mv.Java > minecraft.ImageJava {
			continue
		}
		c, ok, err := pick(mc)
		if err != nil {
			return nil, err
		}
		experimental := c.channel != Stable
		if !ok || (experimental && len(stableFamily) > 0) {
			continue
		}
		if !experimental {
			stableFamily[fam] = true
		}
		out = append(out, Release{ID: typeID + "-" + mc, Type: typeID, Label: typeName(typeID) + " " + mc, MinecraftVersion: mc,
			Java: mv.Java, Channel: c.channel, Experimental: experimental, Pin: c.pin})
	}
	for i := range out {
		if !out[i].Experimental {
			out[i].Recommended = true
			break
		}
	}
	for i := range out {
		out[i].Note, out[i].Notes = notes(out[i])
	}
	if len(out) == 0 {
		return nil, &Error{Kind: KindNoVersions, Msg: fmt.Sprintf("%s lists no version Playkeeper can run.", typeName(typeID)),
			Hint: "Try again later, or choose another server type.", Params: map[string]string{"type": typeID}}
	}
	return out, nil
}

func notes(r Release) (Note, string) {
	switch {
	case r.Experimental:
		return NoteExperimental, fmt.Sprintf("Experimental: %s only has %s builds for Minecraft %s so far. It may crash or damage your world, and a world opened with it cannot go back to an older version.",
			typeName(r.Type), r.Channel, r.MinecraftVersion)
	case r.Recommended:
		return NoteRecommended, fmt.Sprintf("Recommended: the newest stable %s release. Java Edition %s clients can join.", typeName(r.Type), r.MinecraftVersion)
	}
	return NoteStable, "Stable. Java Edition " + r.MinecraftVersion + " clients can join."
}

// builds turns versions, sorted newest first, into a build list with the
// newest stable one recommended.
func builds(pin Pin, versions []string, channel func(string) Channel, set func(*Pin, string)) []Build {
	out := make([]Build, 0, len(versions))
	recommended := false
	for _, v := range versions {
		p := pin
		set(&p, v)
		b := Build{Version: v, Channel: channel(v), Pin: p}
		if b.Channel == Stable && !recommended {
			b.Recommended, recommended = true, true
		}
		out = append(out, b)
	}
	return out
}

func noBuilds(typeID, mc string) error {
	return &Error{Kind: KindNoVersions, Msg: fmt.Sprintf("%s has no build for Minecraft %s that Playkeeper can install.", typeName(typeID), mc),
		Hint: "Choose another Minecraft version.", Params: map[string]string{"type": typeID, "minecraftVersion": mc}}
}

func isNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == KindNotFound
}

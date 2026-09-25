// Package update checks Playkeeper releases for authenticity. The release
// workflow signs each release's manifest with an Ed25519 key; an installed
// Playkeeper only accepts releases signed by a key compiled into it, so a
// release location that serves something else cannot get it installed.
package update

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a release version: MAJOR.MINOR.PATCH with an optional
// pre-release. Build metadata after "+" is ignored when comparing.
type Version struct {
	Major, Minor, Patch int
	Pre                 []string
}

// ParseVersion reads "1.2.3", "1.2.3-rc.1" or "1.2.3-dev+abc", with or
// without a leading "v".
func ParseVersion(s string) (Version, error) {
	core := strings.TrimPrefix(strings.TrimSpace(s), "v")
	core, _, _ = strings.Cut(core, "+")
	core, pre, hasPre := strings.Cut(core, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("%q is not a release version (MAJOR.MINOR.PATCH)", s)
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 || (len(p) > 1 && p[0] == '0') {
			return Version{}, fmt.Errorf("%q is not a release version (MAJOR.MINOR.PATCH)", s)
		}
		n[i] = v
	}
	v := Version{Major: n[0], Minor: n[1], Patch: n[2]}
	if hasPre {
		if pre == "" {
			return Version{}, fmt.Errorf("%q has an empty pre-release", s)
		}
		v.Pre = strings.Split(pre, ".")
		for _, id := range v.Pre {
			if id == "" {
				return Version{}, fmt.Errorf("%q has an empty pre-release identifier", s)
			}
		}
	}
	return v, nil
}

// Compare returns -1, 0 or 1 as v is older than, equal to or newer than o,
// following semantic versioning: a pre-release is older than its release.
func (v Version) Compare(o Version) int {
	for _, d := range [][2]int{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if d[0] != d[1] {
			return sign(d[0] - d[1])
		}
	}
	switch {
	case len(v.Pre) == 0 && len(o.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1
	case len(o.Pre) == 0:
		return -1
	}
	for i := 0; i < len(v.Pre) && i < len(o.Pre); i++ {
		if c := comparePre(v.Pre[i], o.Pre[i]); c != 0 {
			return c
		}
	}
	return sign(len(v.Pre) - len(o.Pre))
}

func comparePre(a, b string) int {
	an, aerr := strconv.Atoi(a)
	bn, berr := strconv.Atoi(b)
	switch {
	case aerr == nil && berr == nil:
		return sign(an - bn)
	case aerr == nil:
		return -1
	case berr == nil:
		return 1
	}
	return strings.Compare(a, b)
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// CompareVersions compares two version strings.
func CompareVersions(a, b string) (int, error) {
	va, err := ParseVersion(a)
	if err != nil {
		return 0, err
	}
	vb, err := ParseVersion(b)
	if err != nil {
		return 0, err
	}
	return va.Compare(vb), nil
}

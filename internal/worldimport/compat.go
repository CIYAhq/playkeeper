package worldimport

import (
	"fmt"
	"strconv"
	"strings"
)

// Compat is how a world's Minecraft version relates to the server's.
type Compat string

const (
	// CompatSame: the world was saved by the server's version.
	CompatSame Compat = "same"
	// CompatUpgrade: the world is older; the server upgrades it on its
	// first start and it can't be opened in the older version again.
	CompatUpgrade Compat = "upgrade"
	// CompatNewer: the world is newer than the server, which can't load it.
	CompatNewer Compat = "newer"
	// CompatUnknown: Playkeeper can't tell.
	CompatUnknown Compat = "unknown"
)

// VersionCheck compares a world with the Minecraft version of a server.
type VersionCheck struct {
	Compat      Compat    `json:"compat"`
	World       string    `json:"world,omitempty"`
	DataVersion int       `json:"dataVersion,omitempty"`
	Target      string    `json:"target"`
	TargetData  int       `json:"targetDataVersion,omitempty"`
	Problem     *Message  `json:"problem,omitempty"`
	Warnings    []Message `json:"warnings,omitempty"`
}

// newestKnown is the newest release releaseData knows.
const newestKnown = "26.3"

// modernLayoutData is the data version of 26.1 Snapshot 6, which moved
// every dimension under dimensions/ and player data under players/.
const modernLayoutData = 4774

// releaseData returns the data version of a Minecraft release. Every Java
// Edition version since 15w32a (1.9) has one: an ever-increasing number the
// game writes into level.dat (Data.DataVersion and Data.Version.Id) and,
// since 18w47b, into the "world_version" field of the version.json inside
// the server jar. Minecraft upgrades worlds with a lower number and refuses
// ones with a higher number. Numbers from https://minecraft.wiki/w/Data_version
// (release versions, checked up to 26.3).
func releaseData(v string) (int, bool) {
	switch v {
	case "1.16":
		return 2566, true
	case "1.16.1":
		return 2567, true
	case "1.16.2":
		return 2578, true
	case "1.16.3":
		return 2580, true
	case "1.16.4":
		return 2584, true
	case "1.16.5":
		return 2586, true
	case "1.17":
		return 2724, true
	case "1.17.1":
		return 2730, true
	case "1.18":
		return 2860, true
	case "1.18.1":
		return 2865, true
	case "1.18.2":
		return 2975, true
	case "1.19":
		return 3105, true
	case "1.19.1":
		return 3117, true
	case "1.19.2":
		return 3120, true
	case "1.19.3":
		return 3218, true
	case "1.19.4":
		return 3337, true
	case "1.20":
		return 3463, true
	case "1.20.1":
		return 3465, true
	case "1.20.2":
		return 3578, true
	case "1.20.3":
		return 3698, true
	case "1.20.4":
		return 3700, true
	case "1.20.5":
		return 3837, true
	case "1.20.6":
		return 3839, true
	case "1.21":
		return 3953, true
	case "1.21.1":
		return 3955, true
	case "1.21.2":
		return 4080, true
	case "1.21.3":
		return 4082, true
	case "1.21.4":
		return 4189, true
	case "1.21.5":
		return 4325, true
	case "1.21.6":
		return 4435, true
	case "1.21.7":
		return 4438, true
	case "1.21.8":
		return 4440, true
	case "1.21.9":
		return 4554, true
	case "1.21.10":
		return 4556, true
	case "1.21.11":
		return 4671, true
	case "26.1":
		return 4786, true
	case "26.1.1":
		return 4788, true
	case "26.1.2":
		return 4790, true
	case "26.2":
		return 4903, true
	case "26.3":
		return 5023, true
	}
	return 0, false
}

// CheckVersion compares a world with the Minecraft version of the server
// it goes to.
func CheckVersion(lv *Level, target string) VersionCheck {
	c := VersionCheck{World: lv.Version, DataVersion: lv.DataVersion, Target: target}
	c.TargetData, _ = releaseData(target)
	world := worldVersionName(lv)
	switch {
	case lv.Series != "" && lv.Series != "main":
		c.Compat = CompatUnknown
		c.Problem = ptr(note(KindWorldSeries, "Upload a world saved by a normal Minecraft release or snapshot.",
			fmt.Sprintf("This world was saved by an experimental Minecraft build (the %q series), which servers can't load.", lv.Series),
			"world", world, "series", lv.Series))
		return c
	case lv.DataVersion <= 0:
		c.Compat = CompatUpgrade
		c.Warnings = append(c.Warnings, note(KindNoVersion, "Keep your upload as a copy in case the upgrade goes wrong.",
			fmt.Sprintf("level.dat doesn't say which Minecraft version saved this world, so it is probably older than Minecraft 1.9 (2016). The server will try to upgrade it to %s, which can't be undone.", target)))
		return c
	}
	c.Compat = compare(lv, target, c.TargetData)
	switch c.Compat {
	case CompatNewer:
		c.Problem = ptr(note(KindWorldNewer,
			fmt.Sprintf("Choose Minecraft %s or newer for this server, or upload a world saved by Minecraft %s or older.", world, target),
			fmt.Sprintf("This world was saved by Minecraft %s, which is newer than this server's Minecraft %s. Minecraft can't load worlds from newer versions.", world, target),
			"world", world, "target", target))
	case CompatUpgrade:
		c.Warnings = append(c.Warnings, note(KindUpgrade, "Keep your upload as a copy in case you want to go back.",
			fmt.Sprintf("This world was saved by Minecraft %s. The server upgrades it to %s when it first starts, and an upgraded world can't be opened in %s again.", world, target, world),
			"world", world, "target", target))
	case CompatUnknown:
		c.Warnings = append(c.Warnings, note(KindUnknownVersion, "Check that the server runs the Minecraft version that last saved the world, or a newer one.",
			fmt.Sprintf("Playkeeper can't tell whether this world (data version %d) loads on Minecraft %s. Minecraft upgrades worlds from older versions but refuses worlds from newer ones.", lv.DataVersion, target),
			"dataVersion", lv.DataVersion, "target", target))
	}
	if lv.Snapshot {
		c.Warnings = append(c.Warnings, note(KindSnapshot, "If the world uses anything from that snapshot, open it in the matching release first.",
			fmt.Sprintf("This world was last saved by a snapshot, Minecraft %s. Snapshots can add blocks and features a release doesn't have, and those may be lost.", world),
			"world", world))
	}
	return c
}

func compare(lv *Level, target string, targetData int) Compat {
	if targetData > 0 {
		return order(lv.DataVersion, targetData)
	}
	if !isRelease(target) {
		return CompatUnknown
	}
	if newest, _ := releaseData(newestKnown); compareVersions(target, newestKnown) > 0 && lv.DataVersion <= newest {
		return CompatUpgrade
	}
	if isRelease(lv.Version) {
		return order(compareVersions(lv.Version, target), 0)
	}
	if oldest, _ := releaseData("1.16"); compareVersions(target, "1.16") < 0 && lv.DataVersion >= oldest {
		return CompatNewer
	}
	return CompatUnknown
}

func order(a, b int) Compat {
	switch {
	case a > b:
		return CompatNewer
	case a < b:
		return CompatUpgrade
	}
	return CompatSame
}

// worldVersionName names the version that saved a world for a message.
func worldVersionName(lv *Level) string {
	if lv.Version != "" {
		return lv.Version
	}
	return fmt.Sprintf("data version %d", lv.DataVersion)
}

// isRelease reports a release version number such as "1.21.4" or "26.1".
func isRelease(v string) bool {
	parts := strings.Split(v, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return false
	}
	for _, p := range parts {
		if p == "" || len(p) > 4 || strings.Trim(p, "0123456789") != "" {
			return false
		}
	}
	return true
}

// compareVersions compares versions by the numbers they start with, part by
// part: "1.21.10" > "1.21.9", and a missing part counts as 0.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		x, y := leadingNumber(pa, i), leadingNumber(pb, i)
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func leadingNumber(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	p := parts[i]
	end := 0
	for end < len(p) && end < 9 && p[end] >= '0' && p[end] <= '9' {
		end++
	}
	n, _ := strconv.Atoi(p[:end])
	return n
}

func ptr[T any](v T) *T { return &v }

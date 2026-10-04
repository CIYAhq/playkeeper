// Package usage is what a Playkeeper install tells the project's stats service
// (services/stats) and how: a random install ID made on the machine, the
// Playkeeper version, the system and CPU, how Playkeeper got onto the
// machine, the kind of address it has, how many Minecraft servers it runs and
// the furthest setup step it reached. Never an IP address, a host or server
// name, or a player's name. README.md ("Usage stats") says what is sent,
// when, and how to turn it off.
//
// The installer sends an Install when a plan is accepted and when the
// install ends; the agent sends a Heartbeat a minute after it starts, right
// after each setup step it reaches for the first time, and twice a day after
// that. The service checks every field with the same Check methods, so the
// client can't send what it would refuse.
package usage

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// DefaultOn says whether usage stats are on when nobody chose. Set it to
// false to make them opt-in: the installer and the agent then send nothing
// until the owner turns them on in Settings, or installs with
// PLAYKEEPER_USAGE_STATS=on.
const DefaultOn = true

// DefaultURL is the project's stats service.
const DefaultURL = "https://stats.playkeeper.io"

// Environment variables read by the installer, and by the agent in its own
// environment (a systemd drop-in).
const (
	// EnvDoNotTrack is the Console Do Not Track convention
	// (consoledonottrack.com): set to anything but 0 or false, it turns
	// usage stats off, whatever else is set.
	EnvDoNotTrack = "DO_NOT_TRACK"
	// EnvSwitch is off or on.
	EnvSwitch = "PLAYKEEPER_USAGE_STATS"
	// EnvURL sends usage stats to another service: a test's recorder, or a
	// fork's own service.
	EnvURL = "PLAYKEEPER_STATS_URL"
	// EnvSource and EnvChannel are set by the scripts that run the
	// installer: how it got onto the machine, and the playkeeper.io channel
	// code of the install command (/install/<code>).
	EnvSource  = "PLAYKEEPER_INSTALL_SOURCE"
	EnvChannel = "PLAYKEEPER_INSTALL_CHANNEL"
	// EnvTest set to 1 marks an install as a test (see System.Test), for a
	// machine the project tests on that should still send.
	EnvTest = "PLAYKEEPER_USAGE_TEST"
)

// How Playkeeper got onto the machine. SourceSite is "on our domain"; the
// others are not.
const (
	// SourceSite is the install command on playkeeper.io, which runs get.sh.
	SourceSite = "playkeeper.io"
	// SourceGitHub is get.sh straight from the GitHub release.
	SourceGitHub = "github"
	// SourceMirror is get.sh downloading from another release location.
	SourceMirror = "mirror"
	// SourceTarball is install.sh from a downloaded tarball, or the binary's
	// own install command.
	SourceTarball = "tarball"
	// SourceBuild is a build that isn't a release, made from the source.
	SourceBuild = "source"
)

// What an install is: one with a dashboard, or a machine joined to another
// install's dashboard.
const (
	KindDashboard = "dashboard"
	KindJoined    = "joined"
)

// The kind of address a machine has: a free yourname.playkeeper.me name, its
// own domain, or none (players join at its IP address).
const (
	AddressFree = "free"
	AddressOwn  = "own"
	AddressIP   = "ip"
)

// What the installer reports. Started is sent when the plan is accepted,
// then Succeeded or Failed when the install ends; Refused when the check of
// the machine turned it away before a plan was shown.
const (
	EventStarted   = "started"
	EventSucceeded = "succeeded"
	EventFailed    = "failed"
	EventRefused   = "refused"
)

// MaxServers bounds the server counts a heartbeat may carry.
const MaxServers = 10000

// System is what every report says about the install.
type System struct {
	Version   string `json:"version"`
	OS        string `json:"os"`
	OSVersion string `json:"osVersion"`
	Arch      string `json:"arch"`
	// Source is empty for an install made before usage stats existed.
	Source  string `json:"source"`
	Channel string `json:"channel,omitempty"`
	Kind    string `json:"kind"`
	// Test marks the project's own test installs, which the service keeps
	// out of every count: made while a GitHub Actions job ran on the
	// machine, by the end-to-end tests' offline harness, or with
	// PLAYKEEPER_USAGE_TEST=1. The project's CI turns usage stats off
	// altogether; this catches an install that forgets to.
	Test bool `json:"test,omitempty"`
}

// Install is what the installer sends.
type Install struct {
	ID    string `json:"id"`
	Event string `json:"event"`
	// Step is the step that failed, or the checks that refused the install
	// joined with "+", like "memory+port".
	Step string `json:"step,omitempty"`
	System
}

// Heartbeat is what a running install sends.
type Heartbeat struct {
	ID string `json:"id"`
	System
	Address string `json:"address"`
	Servers int    `json:"servers"`
	Running int    `json:"running"`
	// Reached is the furthest setup step the machine has reached, one of
	// the Reached steps: empty before its first account, and from versions
	// before 0.4.18. The service takes any token, so a later step needs no
	// change to it.
	Reached string `json:"reached,omitempty"`
}

// The setup steps a heartbeat says a machine reached (Heartbeat.Reached), in
// order: the dashboard's first account, its first server online, a first
// player's session on any of its servers (the owner's included), and a
// second, different player's.
const (
	ReachedAccount = "account"
	ReachedServer  = "server"
	ReachedPlayed  = "played"
	ReachedFriends = "friends"
)

var (
	reID        = regexp.MustCompile(`^[0-9a-f]{32}$`)
	reVersion   = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,40}$`)
	reOS        = regexp.MustCompile(`^[a-z0-9._-]{1,32}$`)
	reOSVersion = regexp.MustCompile(`^[0-9a-z._-]{0,32}$`)
	reArch      = regexp.MustCompile(`^[a-z0-9]{1,16}$`)
	reChannel   = regexp.MustCompile(`^[a-z0-9-]{0,32}$`)
	reStep      = regexp.MustCompile(`^[a-z0-9-]{1,32}(\+[a-z0-9-]{1,32}){0,7}$`)
	reReached   = regexp.MustCompile(`^[a-z0-9-]{0,32}$`)
)

var (
	sources   = map[string]bool{SourceSite: true, SourceGitHub: true, SourceMirror: true, SourceTarball: true, SourceBuild: true}
	kinds     = map[string]bool{KindDashboard: true, KindJoined: true}
	addresses = map[string]bool{AddressFree: true, AddressOwn: true, AddressIP: true}
	events    = map[string]bool{EventStarted: true, EventSucceeded: true, EventFailed: true, EventRefused: true}
)

// ErrInvalid is wrapped by every Check error. The errors name the field,
// never its value.
var ErrInvalid = errors.New("not a usage report")

func invalid(field string) error { return fmt.Errorf("%w: %s", ErrInvalid, field) }

func (s System) check() error {
	switch {
	case !reVersion.MatchString(s.Version):
		return invalid("version")
	case !reOS.MatchString(s.OS):
		return invalid("os")
	case !reOSVersion.MatchString(s.OSVersion):
		return invalid("osVersion")
	case !reArch.MatchString(s.Arch):
		return invalid("arch")
	case s.Source != "" && !sources[s.Source]:
		return invalid("source")
	case !reChannel.MatchString(s.Channel):
		return invalid("channel")
	case !kinds[s.Kind]:
		return invalid("kind")
	}
	return nil
}

// Check reports whether the service takes e.
func (e Install) Check() error {
	switch {
	case !reID.MatchString(e.ID):
		return invalid("id")
	case !events[e.Event]:
		return invalid("event")
	case e.Step != "" && !reStep.MatchString(e.Step):
		return invalid("step")
	case e.Source == "":
		return invalid("source")
	}
	return e.System.check()
}

// Check reports whether the service takes h.
func (h Heartbeat) Check() error {
	switch {
	case !reID.MatchString(h.ID):
		return invalid("id")
	case !addresses[h.Address]:
		return invalid("address")
	case h.Servers < 0 || h.Servers > MaxServers:
		return invalid("servers")
	case h.Running < 0 || h.Running > h.Servers:
		return invalid("running")
	case !reReached.MatchString(h.Reached):
		return invalid("reached")
	}
	return h.System.check()
}

// Choice is what the environment says about usage stats.
type Choice int

const (
	Unset Choice = iota
	Off
	On
)

// FromEnv reads DO_NOT_TRACK and PLAYKEEPER_USAGE_STATS, and names the
// variable that decided. DO_NOT_TRACK wins; a value of either that means
// neither yes nor no is ignored.
func FromEnv(getenv func(string) string) (Choice, string) {
	if v := strings.ToLower(strings.TrimSpace(getenv(EnvDoNotTrack))); v != "" && v != "0" && v != "false" && v != "no" {
		return Off, EnvDoNotTrack
	}
	switch strings.ToLower(strings.TrimSpace(getenv(EnvSwitch))) {
	case "off", "0", "false", "no":
		return Off, EnvSwitch
	case "on", "1", "true", "yes":
		return On, EnvSwitch
	}
	return Unset, ""
}

// reRelease is a published release's version; any other is a build made
// from the source.
var reRelease = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// IsRelease reports whether version is a published release's.
func IsRelease(version string) bool { return reRelease.MatchString(version) }

// SourceFor is how a build of version got onto the machine, given what the
// script that ran it said (EnvSource): a build that isn't a release comes
// from the source whatever ran it, and a release run by nothing that said
// is a tarball's.
func SourceFor(said, version string) string {
	switch {
	case !IsRelease(version):
		return SourceBuild
	case sources[said] && said != SourceBuild:
		return said
	}
	return SourceTarball
}

// CleanSource is s if it is a source, else "".
func CleanSource(s string) string {
	if sources[s] {
		return s
	}
	return ""
}

// CleanChannel is a channel code as the service takes it: lowercased, or ""
// if it isn't one.
func CleanChannel(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	if c != "" && reChannel.MatchString(c) {
		return c
	}
	return ""
}

// CleanOS turns an os-release ID and VERSION_ID into values the service
// takes: "unknown" for a system that doesn't say, and no version where it
// isn't one.
func CleanOS(id, version string) (string, string) {
	id, version = strings.ToLower(strings.TrimSpace(id)), strings.ToLower(strings.TrimSpace(version))
	if !reOS.MatchString(id) {
		return "unknown", ""
	}
	if !reOSVersion.MatchString(version) {
		version = ""
	}
	return id, version
}

// CleanArch is a CPU architecture as Go names it, or "other".
func CleanArch(goarch string) string {
	if reArch.MatchString(goarch) {
		return goarch
	}
	return "other"
}

// IsID reports whether id is an install ID NewID could have made.
func IsID(id string) bool { return reID.MatchString(id) }

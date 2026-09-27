package discord

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Kind is what an alert is about. Kinds are stable: they are stored in
// Settings and the UI translates them.
type Kind string

const (
	KindCrash           Kind = "crash"
	KindRecovered       Kind = "recovered"
	KindLowDisk         Kind = "low_disk"
	KindBackupFailed    Kind = "backup_failed"
	KindBackupSucceeded Kind = "backup_succeeded"
	KindStarted         Kind = "started"
	KindStopped         Kind = "stopped"
	KindUpdateAvailable Kind = "update_available"
	KindPlayerJoined    Kind = "player_joined"
	KindPlayerLeft      Kind = "player_left"
	KindJoinRequested   Kind = "join_requested"
	// KindTwoFactor is a team member turning two-factor sign-in on or off.
	// It changes who can run the dashboard, so it is always posted and
	// never one of the switches.
	KindTwoFactor Kind = "two_factor"
	// KindAdminConfirmed is an admin other than the owner confirming a team
	// member's Admin rights. It is always posted too, so the owner hears of
	// every new admin.
	KindAdminConfirmed Kind = "admin_confirmed"
)

// kindInfo is what Playkeeper knows about each kind, in the order the
// settings screen lists them. After an alert, another of the same kind about
// the same thing (the same player, the same version) is dropped for quiet.
var kindInfo = []struct {
	kind  Kind
	on    bool
	quiet time.Duration
}{
	{KindCrash, true, 5 * time.Minute},
	{KindRecovered, true, 5 * time.Minute},
	{KindLowDisk, true, time.Hour},
	{KindBackupFailed, true, 5 * time.Minute},
	{KindBackupSucceeded, false, 5 * time.Minute},
	{KindStarted, false, 5 * time.Minute},
	{KindStopped, false, 5 * time.Minute},
	{KindUpdateAvailable, true, 24 * time.Hour},
	{KindPlayerJoined, false, 5 * time.Minute},
	{KindPlayerLeft, false, 5 * time.Minute},
	{KindJoinRequested, true, 5 * time.Minute},
}

// Kinds lists every kind of alert, in the order to show them.
func Kinds() []Kind {
	out := make([]Kind, len(kindInfo))
	for i, k := range kindInfo {
		out[i] = k.kind
	}
	return out
}

// Valid reports whether k is a kind this Playkeeper knows.
func (k Kind) Valid() bool { return slices.Contains(Kinds(), k) }

// DefaultOn reports whether alerts of kind k are on until the admin changes
// them: crashes, recoveries, low disk space and failed backups are.
func (k Kind) DefaultOn() bool {
	for _, i := range kindInfo {
		if i.kind == k {
			return i.on
		}
	}
	return false
}

// always reports whether alerts of kind k are posted whatever the switches
// say.
func (k Kind) always() bool { return k == KindTwoFactor || k == KindAdminConfirmed }

func (k Kind) quiet() time.Duration {
	if k.always() {
		return 0
	}
	for _, i := range kindInfo {
		if i.kind == k {
			return i.quiet
		}
	}
	return 5 * time.Minute
}

// Alerts is the set of kinds a server posts alerts for. It is stored as a
// comma-separated list (String and ParseAlerts).
type Alerts []Kind

// DefaultAlerts are the kinds that are on by default.
func DefaultAlerts() Alerts {
	var a Alerts
	for _, i := range kindInfo {
		if i.on {
			a = append(a, i.kind)
		}
	}
	return a
}

// ParseAlerts reads a comma-separated list of kinds. It ignores kinds this
// Playkeeper doesn't know, so settings saved by a newer version still load.
func ParseAlerts(s string) Alerts {
	var a Alerts
	for _, p := range strings.Split(s, ",") {
		a = append(a, Kind(strings.TrimSpace(p)))
	}
	return a.normal()
}

// Has reports whether alerts of kind k are on.
func (a Alerts) Has(k Kind) bool { return slices.Contains(a, k) }

// posts reports whether alerts of kind k go out with these switches.
func (a Alerts) posts(k Kind) bool { return a.Has(k) || k.always() }

// String lists the kinds, comma-separated, in the order of Kinds.
func (a Alerts) String() string {
	var s []string
	for _, k := range a.normal() {
		s = append(s, string(k))
	}
	return strings.Join(s, ",")
}

// normal is a copy of a in the order of Kinds, without unknown kinds or
// repeats. It is never nil, so it marshals as [] rather than null.
func (a Alerts) normal() Alerts {
	out := Alerts{}
	for _, k := range Kinds() {
		if a.Has(k) {
			out = append(out, k)
		}
	}
	return out
}

// An Event is something that happened to the server that may become an
// alert. Make one with the functions below; the Notifier posts it if its
// kind is turned on.
type Event struct {
	Kind Kind
	// At is when it happened; Notify uses the current time if it is zero.
	At time.Time
	// Detail is the explanation of a crash or of a failed backup.
	Detail string
	// Restarting says, for a crash, whether Playkeeper is restarting the
	// server, and GaveUp whether it stopped trying after repeated crashes.
	// StartFailed says Playkeeper stopped trying to start a server that
	// never came up. A crash with none of these is of a server that wasn't
	// meant to be running, which stays off.
	Restarting  bool
	GaveUp      bool
	StartFailed bool
	// Bytes is the free disk space (low disk) or the backup's size (backup
	// succeeded); 0 if unknown.
	Bytes int64
	// Player is the player who joined, left or asks to join.
	Player string
	// Version is the Playkeeper version that is available or, with
	// Minecraft set, the Minecraft version the server can be updated to.
	Version   string
	Minecraft bool
	// Member is the team member who turned two-factor sign-in on or off;
	// On says which, and Admin whether they are an admin. For a
	// confirmation, By is the admin who confirmed Member's Admin rights.
	Member string
	On     bool
	Admin  bool
	By     string
	// Server is the server the event is about, for a Notifier that posts
	// about several; zero means the Notifier's own server.
	Server ServerInfo
}

// JoinRequested is a player asking to join through an invite link that needs
// the admin's yes. It never names the link: its name is private.
func JoinRequested(player string) Event { return Event{Kind: KindJoinRequested, Player: player} }

// Crashed is a crash of the server. explanation is the crash helper's short,
// plain-English account of what went wrong ("" if there is none); restarting
// is false once Playkeeper has stopped restarting the server.
func Crashed(explanation string, restarting bool) Event {
	return Event{Kind: KindCrash, Detail: explanation, Restarting: restarting, GaveUp: !restarting}
}

// StartFailed is Playkeeper giving up on automatic starts of a server that
// never came up. reason is why the last start failed ("" if unknown).
func StartFailed(reason string) Event {
	return Event{Kind: KindCrash, Detail: reason, StartFailed: true}
}

// Recovered is the server coming back online after a crash.
func Recovered() Event { return Event{Kind: KindRecovered} }

// LowDisk is the machine running low on disk space; freeBytes is what is
// left (0 if unknown).
func LowDisk(freeBytes int64) Event { return Event{Kind: KindLowDisk, Bytes: freeBytes} }

// BackupFailed is a backup that failed, with the reason shown to the admin.
func BackupFailed(reason string) Event { return Event{Kind: KindBackupFailed, Detail: reason} }

// BackupSucceeded is a finished backup of sizeBytes (0 if unknown).
func BackupSucceeded(sizeBytes int64) Event {
	return Event{Kind: KindBackupSucceeded, Bytes: sizeBytes}
}

// Started is the server coming online after a start.
func Started() Event { return Event{Kind: KindStarted} }

// Stopped is the server having stopped.
func Stopped() Event { return Event{Kind: KindStopped} }

// UpdateAvailable is a newer Playkeeper release being available.
func UpdateAvailable(version string) Event {
	return Event{Kind: KindUpdateAvailable, Version: version}
}

// MinecraftUpdateAvailable is a newer Minecraft version the server can be
// updated to, as its Settings tab offers.
func MinecraftUpdateAvailable(version string) Event {
	return Event{Kind: KindUpdateAvailable, Version: version, Minecraft: true}
}

// PlayerJoined is a player joining the server.
func PlayerJoined(name string) Event { return Event{Kind: KindPlayerJoined, Player: name} }

// PlayerLeft is a player leaving the server.
func PlayerLeft(name string) Event { return Event{Kind: KindPlayerLeft, Player: name} }

// TwoFactorChanged is a team member turning two-factor sign-in on or off.
// An admin who turns it on has Moderator rights until the owner or an admin
// confirms their Admin rights on the Team page.
func TwoFactorChanged(member string, on, admin bool) Event {
	return Event{Kind: KindTwoFactor, Member: member, On: on, Admin: admin}
}

// AdminConfirmed is the admin by, who isn't the owner, confirming member's
// Admin rights after member turned on two-factor sign-in.
func AdminConfirmed(member, by string) Event {
	return Event{Kind: KindAdminConfirmed, Member: member, By: by}
}

// subject is what the quiet period of an alert applies to: its kind, plus
// the player or version it is about. A crash after which Playkeeper gives up
// has its own subject, so it is never swallowed by earlier crash alerts.
func (e Event) subject() string {
	s := e.subjectInServer()
	if id := oneLine(e.Server.ID); id != "" {
		s = id + "/" + s
	}
	return s
}

func (e Event) subjectInServer() string {
	switch e.Kind {
	case KindPlayerJoined, KindPlayerLeft, KindJoinRequested:
		return string(e.Kind) + ":" + strings.ToLower(oneLine(e.Player))
	case KindUpdateAvailable:
		if e.Minecraft {
			return string(e.Kind) + ":minecraft:" + oneLine(e.Version)
		}
		return string(e.Kind) + ":" + oneLine(e.Version)
	case KindTwoFactor, KindAdminConfirmed:
		return string(e.Kind) + ":" + strings.ToLower(oneLine(e.Member))
	case KindCrash:
		if e.GaveUp {
			return string(e.Kind) + ":gave_up"
		}
		if e.StartFailed {
			return string(e.Kind) + ":start_failed"
		}
	}
	return string(e.Kind)
}

// embed renders the alert. Only fixed text goes in the title; names and
// other text from users go in the description, escaped, and in the author
// line, which Discord shows without markdown.
func (e Event) embed(info ServerInfo) embed {
	name := info.boldName()
	var title, text string
	switch e.Kind {
	case KindCrash:
		switch {
		case e.Restarting:
			title, text = "Server crashed", name+" stopped unexpectedly. Playkeeper is restarting it."
		case e.GaveUp:
			title, text = "Server crashed and stays off", name+" kept crashing, so Playkeeper stopped restarting it. Open the dashboard to see what went wrong."
		case e.StartFailed:
			title, text = "Server didn't start", "Playkeeper couldn't start "+name+", so it stopped trying. Open the dashboard to see what went wrong."
		default:
			title, text = "Server crashed", name+" stopped unexpectedly. Open the dashboard to see what went wrong."
		}
		text += detail(e.Detail)
	case KindRecovered:
		title, text = "Back online", name+" is running again after the crash."
	case KindLowDisk:
		title = "Low disk space"
		// The disk is the machine's, so without a server's name the alert
		// is about all of them.
		runs := name
		if userText(info.Name, 100) == "" {
			runs = "your servers"
		}
		if e.Bytes > 0 {
			text = "Only " + formatBytes(e.Bytes) + " of disk space is left on the machine that runs " + runs + "."
		} else {
			text = "The machine that runs " + runs + " is almost out of disk space."
		}
		text += " Backups and world saves fail when the disk is full: delete old backups or free up space."
	case KindBackupFailed:
		title, text = "Backup failed", "A backup of "+name+" failed."+detail(e.Detail)
	case KindBackupSucceeded:
		title, text = "Backup finished", name+" was backed up."
		if e.Bytes > 0 {
			text = name + " was backed up (" + formatBytes(e.Bytes) + ")."
		}
	case KindStarted:
		title, text = "Server started", name+" is online."
		if addr := joinAddress(info.Address); addr != "" {
			text += " Join at " + addr + "."
		}
	case KindStopped:
		title, text = "Server stopped", name+" has stopped."
	case KindUpdateAvailable:
		v := userText(e.Version, 40)
		switch {
		case e.Minecraft && v != "":
			title, text = "Minecraft update available", name+" can be updated to Minecraft "+v+" on the Settings tab."
		case e.Minecraft:
			title, text = "Minecraft update available", name+" can be updated to a newer Minecraft version on the Settings tab."
		case v != "":
			title, text = "Playkeeper update available", "Playkeeper "+v+" is available. You can update it from the dashboard."
		default:
			title, text = "Playkeeper update available", "A new version of Playkeeper is available. You can update it from the dashboard."
		}
	case KindPlayerJoined:
		title, text = "Player joined", player(e.Player)+" joined "+name+"."
	case KindPlayerLeft:
		title, text = "Player left", player(e.Player)+" left "+name+"."
	case KindJoinRequested:
		title, text = "Join request", player(e.Player)+" wants to join "+name+". Let them in or say no on the Players tab."
	case KindTwoFactor:
		who := member(e.Member)
		switch {
		case e.On && e.Admin:
			title, text = "Two-factor sign-in turned on", who+" turned on two-factor sign-in. Their Admin rights wait until you confirm them on the Team page. If it wasn't them, remove them from the team."
		case e.On:
			title, text = "Two-factor sign-in turned on", who+" turned on two-factor sign-in."
		case e.Admin:
			title, text = "Two-factor sign-in turned off", who+" turned off two-factor sign-in. They have Moderator rights until it's back on and confirmed."
		default:
			title, text = "Two-factor sign-in turned off", who+" turned off two-factor sign-in."
		}
	case KindAdminConfirmed:
		title, text = "Admin rights confirmed", member(e.By)+" gave "+member(e.Member)+" Admin rights after they turned on two-factor sign-in."
	default:
		title, text = "Server alert", name+"."
	}
	return info.card(title, text, colorGreen, e.At)
}

// testEmbed is the message the settings screen's test button sends.
func testEmbed(info ServerInfo) embed {
	if userText(info.Name, 100) == "" {
		return info.card("Test message", "Discord is connected. Playkeeper will post alerts about your servers in this channel.", colorGreen, time.Time{})
	}
	return info.card("Test message", "Discord is connected. Playkeeper will post alerts for "+info.boldName()+" in this channel.", colorGreen, time.Time{})
}

// card is an embed in Playkeeper's layout: the server's name as the author,
// linking to its dashboard; a title; text with the dashboard link; and a
// footer.
func (info ServerInfo) card(title, text string, color int, at time.Time) embed {
	link := dashboardLink(info.DashboardURL)
	e := embed{Title: title, Description: text, Color: color, Footer: &embedFooter{Text: "Playkeeper"}}
	if name := info.plainName(); name != "" {
		e.Author = &embedAuthor{Name: name, URL: link}
	}
	if link != "" {
		e.Description += "\n\n[Open the dashboard](" + link + ")"
	}
	if !at.IsZero() {
		e.Timestamp = at.UTC().Format(time.RFC3339)
	}
	e.fit()
	return e
}

func detail(s string) string {
	if s = userText(s, 1000); s == "" {
		return ""
	}
	return "\n\n" + s
}

func player(name string) string {
	if name = userText(name, 32); name == "" {
		return "A player"
	}
	return "**" + name + "**"
}

func member(name string) string {
	if name = userText(name, 64); name == "" {
		return "A team member"
	}
	return "**" + name + "**"
}

// formatBytes matches the dashboard's formatBytes: powers of 1024, one
// decimal below 10.
func formatBytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v, i := float64(n), 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if v >= 10 || i == 0 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

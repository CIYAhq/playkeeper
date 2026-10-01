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
	// KindInStock is machines the owner watches for coming into stock at
	// their cloud provider. Watching is its own switch, so it is always
	// posted.
	KindInStock Kind = "in_stock"
	// The fleet's room and health, which a dashboard with joined machines or
	// a store watches: a machine off the dashboard and back, room running
	// out across every store, a disk filling up, a machine busy at its
	// busiest hour, and a server lagging. The watch is the dashboard's, so
	// they are always posted.
	KindMachineOff  Kind = "machine_off"
	KindMachineBack Kind = "machine_back"
	KindLowRoom     Kind = "low_room"
	KindDiskFilling Kind = "disk_filling"
	KindBusyCPU     Kind = "busy_cpu"
	KindSlowTicks   Kind = "slow_ticks"
	// KindOverbooked is a machine whose customers' plans set aside more
	// memory than it has, as when a plan grew past what it can hold.
	KindOverbooked Kind = "overbooked"
	// KindDashboardDown and KindDashboardBack are a joined machine's watch on
	// its dashboard: it can't reach the dashboard, and reaches it again. A
	// machine posts them with the dashboard's webhook, which only a machine
	// the dashboard confirmed gets, so they are always posted.
	KindDashboardDown Kind = "dashboard_down"
	KindDashboardBack Kind = "dashboard_back"
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
func (k Kind) always() bool {
	switch k {
	case KindTwoFactor, KindAdminConfirmed, KindInStock, KindMachineOff, KindMachineBack, KindLowRoom, KindDiskFilling, KindBusyCPU, KindSlowTicks, KindOverbooked,
		KindDashboardDown, KindDashboardBack:
		return true
	}
	return false
}

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
	// server, GaveUp whether it stopped trying after repeated crashes, and
	// Repeats whether it didn't restart it because the crash repeats at
	// every start. StartFailed says Playkeeper stopped trying to start a
	// server that never came up. A crash with none of these is of a server
	// that wasn't meant to be running, which stays off.
	Restarting  bool
	GaveUp      bool
	Repeats     bool
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
	// Provider and Machine say which machines came into stock, such as
	// "Hetzner" and "CX53", and Places where, each with its link to buy one.
	// For the fleet's alerts, Machine is the machine's name on the
	// dashboard.
	Provider string
	Machine  string
	Places   []Place
	// The fleet's alerts: Minutes a machine has been off, or was, or a
	// server lagged; Percent a disk's share full or a machine's CPU at its
	// busiest hour, Days running; Room more servers of MemoryMB each and
	// Waiting customers waiting for room; ServerName, MSPT milliseconds a
	// tick and Players playing.
	Minutes    int
	Percent    int
	Days       int
	Room       int
	MemoryMB   int
	Waiting    int
	ServerName string
	MSPT       int
	Players    int
	// Server is the server the event is about, for a Notifier that posts
	// about several; zero means the Notifier's own server.
	Server ServerInfo
}

// Place is where machines are in stock, and the link that buys one there.
type Place struct {
	Name string
	Link string
}

// InStock is machines coming into stock at the owner's cloud provider.
func InStock(provider, machine string, places []Place) Event {
	return Event{Kind: KindInStock, Provider: provider, Machine: machine, Places: places}
}

// MachineOff is machine, joined to the dashboard, not connected to it for
// minutes.
func MachineOff(machine string, minutes int) Event {
	return Event{Kind: KindMachineOff, Machine: machine, Minutes: minutes}
}

// MachineBack is machine connected to the dashboard again after minutes off.
func MachineBack(machine string, minutes int) Event {
	return Event{Kind: KindMachineBack, Machine: machine, Minutes: minutes}
}

// LowRoom is the machines having room for only room more servers of the
// smallest plan on sale, of memoryMB each, across every store, while
// waiting customers wait for room.
func LowRoom(room, memoryMB, waiting int) Event {
	return Event{Kind: KindLowRoom, Room: room, MemoryMB: memoryMB, Waiting: waiting}
}

// DiskFilling is machine's disk passing percent full.
func DiskFilling(machine string, percent int) Event {
	return Event{Kind: KindDiskFilling, Machine: machine, Percent: percent}
}

// BusyCPU is machine using more than 70% of its CPU in its busiest hour
// days running, percent at the last.
func BusyCPU(machine string, percent, days int) Event {
	return Event{Kind: KindBusyCPU, Machine: machine, Percent: percent, Days: days}
}

// SlowTicks is server, on machine, taking mspt milliseconds a tick on
// average for minutes while players played.
func SlowTicks(server, machine string, mspt, players, minutes int) Event {
	return Event{Kind: KindSlowTicks, ServerName: server, Machine: machine, MSPT: mspt, Players: players, Minutes: minutes}
}

// Overbooked is machine's customers' plans setting aside memoryMB more
// memory than it has.
func Overbooked(machine string, memoryMB int) Event {
	return Event{Kind: KindOverbooked, Machine: machine, MemoryMB: memoryMB}
}

// DashboardDown is machine, joined to the dashboard, unable to reach it for
// minutes.
func DashboardDown(machine string, minutes int) Event {
	return Event{Kind: KindDashboardDown, Machine: machine, Minutes: minutes}
}

// DashboardBack is machine reaching the dashboard again after minutes.
func DashboardBack(machine string, minutes int) Event {
	return Event{Kind: KindDashboardBack, Machine: machine, Minutes: minutes}
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

// Repeating is a crash that would happen again at every start, so
// Playkeeper didn't restart the server. explanation says what crashed it.
func Repeating(explanation string) Event {
	return Event{Kind: KindCrash, Detail: explanation, Repeats: true}
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
// the player or version it is about. A crash after which Playkeeper gives up,
// or doesn't restart the server, has its own subject, so it is never
// swallowed by earlier crash alerts.
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
	case KindInStock:
		var places []string
		for _, p := range e.Places {
			places = append(places, oneLine(p.Name))
		}
		return string(e.Kind) + ":" + oneLine(e.Machine) + ":" + strings.Join(places, ",")
	case KindCrash:
		if e.GaveUp {
			return string(e.Kind) + ":gave_up"
		}
		if e.Repeats {
			return string(e.Kind) + ":repeats"
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
		case e.Repeats:
			title, text = "Server crashed and stays off", name+" would crash the same way if it started again, so Playkeeper didn't restart it. Open the dashboard to see how to fix it."
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
	case KindInStock:
		title, text = "Machines in stock", inStockText(e)
	case KindMachineOff:
		title, text = "Machine off the dashboard", machineName(e.Machine)+" hasn't been connected to the dashboard for "+count(e.Minutes, "minute")+", so the dashboard can't reach its servers."
	case KindMachineBack:
		title, text = "Machine back", machineName(e.Machine)+" is connected to the dashboard again, after "+count(e.Minutes, "minute")+" off."
	case KindLowRoom:
		size := memorySize(e.MemoryMB)
		if e.Room > 0 {
			text = "The machines have room for " + count(e.Room, "more "+size+" server") + ", across every store."
		} else {
			text = "The machines have no room for another " + size + " server, across every store."
		}
		switch {
		case e.Waiting == 1:
			text += " 1 customer waits for room."
		case e.Waiting > 1:
			text += fmt.Sprintf(" %d customers wait for room.", e.Waiting)
		}
		title, text = "Room running out", text+" Buy a machine when your provider has one in stock: the stock alert posts here when it does."
	case KindDiskFilling:
		title, text = "Disk filling up", machineName(e.Machine)+fmt.Sprintf("'s disk is %d%% full. Delete old backups there, or add a machine for new customers, before it fills.", e.Percent)
	case KindBusyCPU:
		title, text = "Machine busy at peak", machineName(e.Machine)+fmt.Sprintf(" used more than 70%% of its CPU in its busiest hour %d days running, %d%% the last. Add a machine and move some customers to it.", e.Days, e.Percent)
	case KindSlowTicks:
		server := "A server"
		if s := userText(e.ServerName, 64); s != "" {
			server = "**" + s + "**"
		}
		title, text = "Server lagging", server+" on "+machineName(e.Machine)+fmt.Sprintf(" took %d ms a tick on average for %s, with %d playing. At 50 ms it keeps full speed, so its machine may be too busy.", e.MSPT, count(e.Minutes, "minute"), e.Players)
	case KindOverbooked:
		title, text = "Machine overbooked", "The customers on "+machineName(e.Machine)+" have plans that set aside "+memorySize(e.MemoryMB)+" more memory than it has. A plan grew past what it can hold: move a customer to another machine on its page in Settings › Machines."
	case KindDashboardDown:
		title, text = "Dashboard can't be reached", machineName(e.Machine)+" hasn't reached the dashboard for "+count(e.Minutes, "minute")+". Its servers keep running, out of the dashboard's reach until the two connect again."
	case KindDashboardBack:
		title, text = "Dashboard reachable again", machineName(e.Machine)+" reaches the dashboard again, after "+count(e.Minutes, "minute")+"."
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

// maxPlaces bounds the places one in-stock alert names.
const maxPlaces = 10

// inStockText says which machines came into stock where, linking each place
// to buying one there: "Hetzner has CX53 machines in stock. Buy one in
// Falkenstein or Helsinki."
func inStockText(e Event) string {
	provider := userText(e.Provider, 40)
	if provider == "" {
		provider = "Your cloud provider"
	}
	what := "the machines you watch"
	if m := userText(e.Machine, 40); m != "" {
		what = "**" + m + "** machines"
	}
	var names []string
	linked := true
	for _, p := range e.Places {
		if len(names) == maxPlaces {
			break
		}
		name := userText(p.Name, 40)
		if name == "" {
			continue
		}
		if l := plainLink(p.Link); l != "" {
			name = "[" + name + "](" + l + ")"
		} else {
			linked = false
		}
		names = append(names, name)
	}
	switch {
	case len(names) == 0:
		return provider + " has " + what + " in stock."
	case linked:
		return provider + " has " + what + " in stock. Buy one in " + orList(names) + "."
	default:
		return provider + " has " + what + " in stock in " + orList(names) + "."
	}
}

// orList joins items as "A", "A or B", or "A, B or C".
func orList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
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

func machineName(name string) string {
	if name = userText(name, 64); name == "" {
		return "A machine"
	}
	return "**" + name + "**"
}

// memorySize is a plan's memory as its store names it: "4 GB", "1.5 GB".
func memorySize(mb int) string {
	if mb%1024 == 0 {
		return fmt.Sprintf("%d GB", mb/1024)
	}
	return fmt.Sprintf("%.1f GB", float64(mb)/1024)
}

// count is "1 minute" or "12 minutes": n of what, in the plural past one.
func count(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
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

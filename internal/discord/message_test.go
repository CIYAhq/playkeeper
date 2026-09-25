package discord

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

var t0 = time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)

var survival = ServerInfo{
	Name:         "Survival",
	DashboardURL: "https://panel.example.com/servers/k3j9x2",
	Address:      "play.example.com",
	Version:      "1.21.8",
	MOTD:         "§aA §lfriendly§r server",
}

func TestEscapeKeepsMarkdownAndMentionsInert(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"@everyone", `\@everyone`},
		{"@here", `\@here`},
		{"<@123456789012345678>", `\<\@123456789012345678\>`},
		{"**bold** _it_ ~~gone~~", `\*\*bold\*\* \_it\_ \~\~gone\~\~`},
		{"||spoiler|| `code`", "\\|\\|spoiler\\|\\| \\`code\\`"},
		{"[free nitro](https://evil.example)", `\[free nitro\]\(https\://evil.example\)`},
		{"# Big", `\# Big`},
		{"-# small", `\-\# small`},
		{"> quote", `\> quote`},
		{`back\slash`, `back\\slash`},
		{"Steve_123", `Steve\_123`},
		{"<t:0:R>", `\<t\:0\:R\>`},
		{"Ünïcødé 名前 1.21.8", "Ünïcødé 名前 1.21.8"},
	} {
		if got := escape(c.in); got != c.want {
			t.Errorf("escape(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestUserTextCleansAndShortens(t *testing.T) {
	for _, c := range []struct {
		in    string
		limit int
		want  string
	}{
		{"Line one\nLine two\t\tend", 100, "Line one Line two end"},
		{"\u202Eevil\u2066name\u200F", 100, "evilname"},
		{"§aGreen §lBold§r", 100, "Green Bold"},
		{"bad\xffbyte", 100, "bad\uFFFDbyte"},
		{"\x00\x07 bell ", 100, "bell"},
		{"Survival_World", 8, `Surviva…`},
		{"ab_cdefgh", 4, `ab\_…`},
	} {
		if got := userText(c.in, c.limit); got != c.want {
			t.Errorf("userText(%q, %d) = %q, want %q", c.in, c.limit, got, c.want)
		}
	}
}

func TestClipCountsLikeDiscordAndCutsSafely(t *testing.T) {
	for _, c := range []struct {
		in    string
		limit int
		want  string
	}{
		{"hello", 5, "hello"},
		{"hello!", 5, "hell…"},
		{"  padded  ", 6, "padded"},
		{"word1 word2", 7, "word1…"},
		{strings.Repeat("😀", 10), 5, "😀😀…"},
		{`abc\_def`, 5, "abc…"},
		{`ab\\cd`, 5, `ab\\…`},
		{"anything", 0, ""},
	} {
		got := clip(c.in, c.limit)
		if got != c.want {
			t.Errorf("clip(%q, %d) = %q, want %q", c.in, c.limit, got, c.want)
		}
		if textLen(got) > c.limit {
			t.Errorf("clip(%q, %d) is %d long", c.in, c.limit, textLen(got))
		}
	}
	if n := textLen("a😀é"); n != 4 {
		t.Errorf("textLen counts UTF-16 units like Discord's limits: got %d, want 4", n)
	}
}

func allKindsEvents() []Event {
	return []Event{
		Crashed("The server ran out of memory and was killed.", true),
		Crashed("It crashed 3 times in 10 minutes.", false),
		Recovered(),
		LowDisk(1536 << 20),
		BackupFailed("There is not enough free disk space for a backup."),
		BackupSucceeded(812 << 20),
		Started(),
		Stopped(),
		UpdateAvailable("0.4.0"),
		PlayerJoined("Steve_123"),
		PlayerLeft("Steve_123"),
	}
}

func TestAlertEmbedsReadWell(t *testing.T) {
	want := []struct{ title, text string }{
		{"Server crashed", "**Survival** stopped unexpectedly. Playkeeper is restarting it.\n\nThe server ran out of memory and was killed."},
		{"Server crashed and stays off", "**Survival** kept crashing, so Playkeeper stopped restarting it. Open the dashboard to see what went wrong.\n\nIt crashed 3 times in 10 minutes."},
		{"Back online", "**Survival** is running again after the crash."},
		{"Low disk space", "Only 1.5 GB of disk space is left on the machine that runs **Survival**. Backups and world saves fail when the disk is full: delete old backups or free up space."},
		{"Backup failed", "A backup of **Survival** failed.\n\nThere is not enough free disk space for a backup."},
		{"Backup finished", "**Survival** was backed up (812 MB)."},
		{"Server started", "**Survival** is online. Join at `play.example.com`."},
		{"Server stopped", "**Survival** has stopped."},
		{"Playkeeper update available", "Playkeeper 0.4.0 is available. You can update it from the dashboard."},
		{"Player joined", `**Steve\_123** joined **Survival**.`},
		{"Player left", `**Steve\_123** left **Survival**.`},
	}
	for i, e := range allKindsEvents() {
		e.At = t0
		em := e.embed(survival)
		text := want[i].text + "\n\n[Open the dashboard](https://panel.example.com/servers/k3j9x2)"
		if em.Title != want[i].title || em.Description != text {
			t.Errorf("%s: got\n%q\n%q\nwant\n%q\n%q", e.Kind, em.Title, em.Description, want[i].title, text)
		}
		if em.Color != colorGreen || em.Author == nil || em.Author.Name != "Survival" || em.Author.URL != "https://panel.example.com/servers/k3j9x2" ||
			em.Footer == nil || em.Footer.Text != "Playkeeper" || em.Timestamp != "2026-09-25T14:00:00Z" {
			t.Errorf("%s: wrong frame: %+v", e.Kind, em)
		}
	}
}

func TestAlertEmbedsWithoutOptionalDetails(t *testing.T) {
	info := ServerInfo{Name: "Creative"}
	for _, c := range []struct {
		e    Event
		want string
	}{
		{Crashed("", true), "**Creative** stopped unexpectedly. Playkeeper is restarting it."},
		{LowDisk(0), "The machine that runs **Creative** is almost out of disk space. Backups and world saves fail when the disk is full: delete old backups or free up space."},
		{BackupSucceeded(0), "**Creative** was backed up."},
		{Started(), "**Creative** is online."},
		{UpdateAvailable(""), "A new version of Playkeeper is available. You can update it from the dashboard."},
		{PlayerJoined(""), "A player joined **Creative**."},
	} {
		em := c.e.embed(info)
		if em.Description != c.want || em.Timestamp != "" {
			t.Errorf("%s: got %q (timestamp %q), want %q", c.e.Kind, em.Description, em.Timestamp, c.want)
		}
	}
	if em := Started().embed(ServerInfo{Address: "bad`address"}); em.Author != nil || !strings.HasPrefix(em.Description, "The Minecraft server is online.") {
		t.Errorf("without a name or a valid address: %+v", em)
	}
	if em := Stopped().embed(ServerInfo{Name: "x", DashboardURL: "http://panel.example.com"}); em.Author.URL != "" || strings.Contains(em.Description, "dashboard") {
		t.Errorf("a dashboard link that isn't https must be left out: %+v", em)
	}
}

func TestUserTextCannotPingOrBreakTheEmbed(t *testing.T) {
	evil := ServerInfo{
		Name:         "@everyone **Free** [nitro](https://evil.example) <@&123456789012345678>\n# Big",
		DashboardURL: "https://panel.example.com/servers/a(b)c",
		Address:      "play.example.com`<@1>`",
		Version:      "1.21.8 @here",
		MOTD:         "§k@everyone§r\n> not a quote\nthird line",
	}
	em := PlayerJoined("`<@123>`_x").embed(evil)
	st := Status{State: StateOnline, Players: []string{"@everyone", "<@123>"}}.embed(evil)
	rendered := em.Description + "\n" + st.Description
	for _, f := range st.Fields {
		rendered += "\n" + f.Value
	}
	for _, bad := range []string{"@everyone", "@here", "<@", "[nitro]", "](https://evil", "**Free**", "`<@", "_x"} {
		if bare(rendered, bad) {
			t.Errorf("unescaped %q in %q", bad, rendered)
		}
	}
	for _, bad := range []string{"\n# ", "\n> not a quote", "third line"} {
		if strings.Contains(rendered, bad) {
			t.Errorf("%q in %q", bad, rendered)
		}
	}
	if !strings.Contains(em.Description, "[Open the dashboard](https://panel.example.com/servers/a%28b%29c)") {
		t.Errorf("the dashboard link must not be ended early by a parenthesis: %q", em.Description)
	}
	if em.Author.Name != "@everyone **Free** [nitro](https://evil.example) <@&123456789012345678> # Big" {
		t.Errorf("the author line, which Discord shows without markdown, has the name as plain text on one line: %q", em.Author.Name)
	}
	if slices.ContainsFunc(st.Fields, func(f embedField) bool { return f.Name == "Address" }) {
		t.Errorf("an address that isn't a plain host and port must be left out: %+v", st.Fields)
	}
}

// bare reports whether needle occurs in s without a backslash before it.
func bare(s, needle string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], needle)
		if j < 0 {
			return false
		}
		if k := i + j; k == 0 || s[k-1] != '\\' {
			return true
		}
		i += j + 1
	}
}

func TestEmbedsStayWithinDiscordLimits(t *testing.T) {
	long := strings.Repeat("_*@<", 3000)
	info := ServerInfo{Name: long, DashboardURL: "https://panel.example.com/" + strings.Repeat("p", 400), Version: long, MOTD: long + "\n" + long}
	var events []Event
	for _, e := range allKindsEvents() {
		e.Detail, e.Player, e.Version = long, long, long
		events = append(events, e)
	}
	var embeds []embed
	for _, e := range events {
		embeds = append(embeds, e.embed(info))
	}
	var names []string
	for i := range 500 {
		names = append(names, fmt.Sprintf("Player_%09d", i))
	}
	status := Status{State: StateOnline, Since: t0, PlayersOnline: 500, MaxPlayers: 1000, Players: names}.embed(info)
	embeds = append(embeds, status, testEmbed(info))
	for _, em := range embeds {
		checkLimits(t, em)
	}
	online := status.Fields[len(status.Fields)-1]
	shown := strings.Count(online.Value, "Player")
	more, _ := strconv.Atoi(strings.Fields(online.Value[strings.LastIndex(online.Value, " and ")+5:])[0])
	if online.Name != "Online now" || !strings.HasSuffix(online.Value, " more") || shown+more != 500 {
		t.Errorf("500 names should be cut with the right count: %d shown + %d more in %q", shown, more, online.Value)
	}

	n := New(Options{Settings: Settings{Webhook: testWebhook(t, ""), Alerts: Alerts(Kinds())}, Server: info, Now: func() time.Time { return t0 }})
	for i := range 30 {
		e := PlayerJoined(fmt.Sprintf("%s%d", long, i))
		e.At = t0
		n.queue = append(n.queue, e)
	}
	j := n.alertJob(n.settings.Webhook)
	total := 0
	for _, em := range j.msg.Embeds {
		total += em.size()
	}
	if len(j.msg.Embeds) == 0 || len(j.msg.Embeds) > maxEmbeds || total > maxEmbedsTotal || j.count != len(j.msg.Embeds) {
		t.Errorf("a batch has %d embeds and %d characters", len(j.msg.Embeds), total)
	}
}

func checkLimits(t *testing.T, e embed) {
	t.Helper()
	check := func(what, s string, limit int) {
		if textLen(s) > limit {
			t.Errorf("%s is %d long, over Discord's %d", what, textLen(s), limit)
		}
		if strings.TrimSpace(s) != s {
			t.Errorf("%s has whitespace Discord would trim: %q", what, s)
		}
	}
	check("title", e.Title, maxTitle)
	check("description", e.Description, maxDescription)
	if e.Author != nil {
		check("author", e.Author.Name, maxAuthorName)
	}
	if e.Footer != nil {
		check("footer", e.Footer.Text, maxFooter)
	}
	if len(e.Fields) > maxFields {
		t.Errorf("%d fields", len(e.Fields))
	}
	for _, f := range e.Fields {
		check("field name", f.Name, maxFieldName)
		check("field value", f.Value, maxFieldValue)
		if f.Name == "" || f.Value == "" {
			t.Errorf("empty field %+v", f)
		}
	}
	if e.size() > maxEmbedsTotal {
		t.Errorf("embed has %d characters", e.size())
	}
}

func TestFitCutsOversizedEmbeds(t *testing.T) {
	fields := func() []embedField {
		var f []embedField
		for i := range 30 {
			f = append(f, embedField{Name: strconv.Itoa(i) + strings.Repeat("n", 300), Value: strings.Repeat("v", 2000)})
		}
		return append(f, embedField{Name: "empty", Value: "  "})
	}
	e := embed{Title: strings.Repeat("t", 300), Description: strings.Repeat("d", 5000), Author: &embedAuthor{Name: " "}, Footer: &embedFooter{Text: "Playkeeper"}, Fields: fields()}
	e.fit()
	checkLimits(t, e)
	if e.Author != nil || len(e.Fields) != 1 || e.Fields[0].Name[0] != '0' || textLen(e.Description) != maxDescription {
		t.Errorf("fit should drop the blank author, then fields from the end until the embed fits: %d fields, author %+v", len(e.Fields), e.Author)
	}

	e = embed{Title: strings.Repeat("t", 300), Description: strings.Repeat("d", 5000), Footer: &embedFooter{Text: strings.Repeat("f", 3000)}, Fields: fields()}
	e.fit()
	checkLimits(t, e)
	if len(e.Fields) != 0 || e.size() != maxEmbedsTotal || textLen(e.Footer.Text) != maxFooter {
		t.Errorf("when title, description and footer alone are too long, the description gives way: %d fields, %d characters", len(e.Fields), e.size())
	}
}

func TestStatusEmbed(t *testing.T) {
	since := t0.Add(-2 * time.Hour)
	on := Status{State: StateOnline, Since: since, PlayersOnline: 3, MaxPlayers: 20, Players: []string{"Steve_1", "alex", "Zed"}}
	e := on.embed(survival)
	stamp := "<t:" + strconv.FormatInt(since.Unix(), 10) + ":R>"
	wantText := "Online since " + stamp + ".\n\n> A friendly server\n\n[Open the dashboard](https://panel.example.com/servers/k3j9x2)"
	if e.Title != "Online" || e.Color != colorGreen || e.Description != wantText {
		t.Errorf("online: %q %x %q", e.Title, e.Color, e.Description)
	}
	wantFields := []embedField{
		{Name: "Players", Value: "3 of 20", Inline: true},
		{Name: "Address", Value: "`play.example.com`", Inline: true},
		{Name: "Version", Value: "1.21.8", Inline: true},
		{Name: "Online now", Value: `alex, Steve\_1, Zed`},
	}
	if !slices.Equal(e.Fields, wantFields) {
		t.Errorf("online fields: %+v", e.Fields)
	}
	if e.Footer.Text != "Live status from Playkeeper" || e.Author.Name != "Survival" {
		t.Errorf("frame: %+v %+v", e.Footer, e.Author)
	}
	for _, c := range []struct {
		s     Status
		title string
		text  string
		color int
	}{
		{Status{State: StateStarting, Since: since}, "Starting", "Starting up since " + stamp + ".", colorAmber},
		{Status{State: StateOffline}, "Offline", "Offline.", colorGrey},
		{Status{State: StateCrashed, Since: since}, "Crashed", "Stopped unexpectedly " + stamp + ".", colorRed},
		{Status{State: "unknown"}, "Offline", "Offline.", colorGrey},
	} {
		e := c.s.embed(ServerInfo{Name: "Survival", Version: "1.21.8"})
		if e.Title != c.title || e.Description != c.text || e.Color != c.color {
			t.Errorf("%s: %q %q %x", c.s.State, e.Title, e.Description, e.Color)
		}
		if len(e.Fields) != 1 || e.Fields[0].Name != "Version" {
			t.Errorf("%s: only the version field applies: %+v", c.s.State, e.Fields)
		}
	}
	reordered := on
	reordered.Players = []string{"Zed", "Steve_1", "alex"}
	if on.embed(survival).key() != reordered.embed(survival).key() {
		t.Error("the order players are listed in must not change the message")
	}
	e1, e2 := on.embed(survival), on.embed(survival)
	e1.Timestamp, e2.Timestamp = "2026-09-25T14:00:00Z", "2026-09-25T15:00:00Z"
	if e1.key() != e2.key() {
		t.Error("the timestamp must not count as a change")
	}
}

// TestPayloadsMatchGolden pins the exact JSON Playkeeper sends. Run with
// -update to rewrite the files after a deliberate change.
func TestPayloadsMatchGolden(t *testing.T) {
	crash := Crashed("The server ran out of memory and was killed.", true)
	crash.At = t0
	status := Status{State: StateOnline, Since: t0.Add(-2 * time.Hour), PlayersOnline: 2, MaxPlayers: 20, Players: []string{"Steve_1", "alex"}}.embed(survival)
	status.Timestamp = t0.Format(time.RFC3339)
	statusPost := newMessage(status)
	statusPost.Flags = flagSuppressNotifications
	for name, m := range map[string]message{
		"crash_alert.golden.json": newMessage(crash.embed(survival)),
		"live_status.golden.json": statusPost,
	} {
		got, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, '\n')
		path := filepath.Join("testdata", name)
		if *update {
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from what Playkeeper sends now:\n%s", path, got)
		}
	}
}

func TestAlertsSet(t *testing.T) {
	if got := DefaultAlerts().String(); got != "crash,recovered,low_disk,backup_failed,update_available,join_requested" {
		t.Errorf("default alerts: %s", got)
	}
	for _, k := range Kinds() {
		if !k.Valid() || k.DefaultOn() != DefaultAlerts().Has(k) {
			t.Errorf("%s: valid %v, default %v", k, k.Valid(), k.DefaultOn())
		}
	}
	a := ParseAlerts(" player_joined, crash,from_the_future,crash,")
	if a.String() != "crash,player_joined" || !a.Has(KindPlayerJoined) || a.Has(KindPlayerLeft) {
		t.Errorf("ParseAlerts: %v", a)
	}
	if b, _ := json.Marshal(ParseAlerts("")); string(b) != "[]" {
		t.Errorf("no alerts marshal as %s", b)
	}
	if Kind("nope").Valid() || Kind("nope").DefaultOn() {
		t.Error("unknown kinds are neither valid nor on")
	}
}

func TestSettingsValidate(t *testing.T) {
	ok := DefaultSettings()
	ok.StatusMessageID = testThread
	if err := ok.Validate(); err != nil {
		t.Errorf("valid settings: %v", err)
	}
	for _, bad := range []Settings{
		{Alerts: Alerts{KindCrash, "reboot"}},
		{StatusMessageID: "12345"},
	} {
		var e *Error
		if err := bad.Validate(); !errors.As(err, &e) || e.Code != CodeInvalidSettings || e.Msg == "" {
			t.Errorf("%+v: got %v", bad, err)
		}
	}
	if c := (Settings{StatusMessageID: "not an id", Alerts: Alerts{KindLowDisk, KindCrash, "x"}}).clean(); c.StatusMessageID != "" || c.Alerts.String() != "crash,low_disk" {
		t.Errorf("clean: %+v", c)
	}
}

func TestFormatBytesMatchesTheDashboard(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1536: "1.5 KB", 812 << 20: "812 MB", 5 << 30: "5.0 GB", 12 << 30: "12 GB"} {
		if got := formatBytes(n); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

package discord

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

var dashboard = ServerInfo{DashboardURL: "https://panel.example.com"}

func newDashboardHarness(t *testing.T, s Settings) *harness {
	h := newHarness(t, s)
	h.Notifier = New(Options{
		Settings:        s,
		Server:          dashboard,
		Client:          h.fake.client(),
		Now:             h.clock.Now,
		Logger:          slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		StatusInterval:  30 * time.Second,
		OnStatusMessage: func(id string) { h.ids = append(h.ids, id) },
		OnDelivery:      func(d Delivery) { h.deliveries = append(h.deliveries, d) },
	})
	return h
}

func threeServers() Board {
	return Board{Servers: []BoardServer{
		{Server: ServerInfo{ID: "srv1", Name: "Survival", Address: "survival.alex.playkeeper.io"}, Status: Status{State: StateOnline, PlayersOnline: 3, MaxPlayers: 10, Players: []string{"tobi2009", "mara_k", "JunoFox"}}},
		{Server: ServerInfo{ID: "srv2", Name: "Creative", Address: "203.0.113.10:25566"}, Status: Status{State: StateOffline}},
		{Server: ServerInfo{ID: "srv3", Name: "Cobblemon", Address: "203.0.113.10:25567"}, Status: Status{State: StateOnline, PlayersOnline: 2, MaxPlayers: 10, Players: []string{"lena", "PixelPia"}}},
	}}
}

func TestBoardListsEveryServerInOneMessage(t *testing.T) {
	h := newDashboardHarness(t, Settings{Webhook: testWebhook(t, ""), LiveStatus: true})
	h.UpdateBoard(threeServers())
	h.sendDue()
	reqs := h.fake.take()
	if len(reqs) != 1 || reqs[0].Msg.Flags != flagSuppressNotifications {
		t.Fatalf("one quiet status message, got %q", calls(reqs))
	}
	e := reqs[0].Msg.Embeds[0]
	want := "🟢 **Survival** · Online · 3 of 10 · JunoFox, mara\\_k, tobi2009\n" +
		"⚪ **Creative** · Stopped\n" +
		"🟢 **Cobblemon** · Online · 2 of 10 · lena, PixelPia\n\n" +
		"[Open the dashboard](https://panel.example.com)"
	if e.Description != want {
		t.Errorf("description:\n%s\nwant:\n%s", e.Description, want)
	}
	if e.Footer == nil || e.Footer.Text != "Join: survival.alex.playkeeper.io" || e.Color != colorGreen || e.Timestamp == "" || e.Author != nil {
		t.Errorf("embed: %+v footer %+v", e, e.Footer)
	}

	b := threeServers()
	b.Servers[0].Status = Status{State: StateCrashed}
	h.clock.Add(time.Minute)
	h.UpdateBoard(b)
	h.sendDue()
	reqs = h.fake.take()
	if got := calls(reqs); len(got) != 1 || !strings.HasPrefix(got[0], "PATCH /messages/") {
		t.Fatalf("a change edits the same message: %q", got)
	}
	e = reqs[0].Msg.Embeds[0]
	if !strings.HasPrefix(e.Description, "🔴 **Survival** · Crashed\n") || e.Footer.Text != "Join: 203.0.113.10:25567" || e.Color != colorRed {
		t.Errorf("after the crash: %q, footer %q, colour %x", e.Description, e.Footer.Text, e.Color)
	}

	h.clock.Add(time.Minute)
	h.UpdateBoard(b)
	if next := h.sendDue(); !next.IsZero() || len(h.fake.take()) != 0 {
		t.Error("an unchanged board is not sent again")
	}
}

func TestBoardSaysHowManyServersDidNotFit(t *testing.T) {
	var b Board
	for range maxBoardServers + 3 {
		b.Servers = append(b.Servers, BoardServer{Server: ServerInfo{Name: "World"}, Status: Status{State: StateStarting}})
	}
	e := b.embed(ServerInfo{})
	lines := strings.Split(e.Description, "\n")
	if len(lines) != maxBoardServers+1 || lines[len(lines)-1] != "and 3 more" || e.Color != colorAmber || e.Footer.Text != "Live status from Playkeeper" {
		t.Errorf("%d lines, last %q, colour %x, footer %q", len(lines), lines[len(lines)-1], e.Color, e.Footer.Text)
	}
	if e := (Board{}).embed(ServerInfo{}); e.Description != "No servers yet." {
		t.Errorf("empty board: %q", e.Description)
	}
}

func TestAlertsForDifferentServersAreNotThrottledTogether(t *testing.T) {
	h := newDashboardHarness(t, Settings{Webhook: testWebhook(t, ""), Alerts: DefaultAlerts()})
	a, b := Crashed("", true), Crashed("", true)
	a.Server = ServerInfo{ID: "srv1", Name: "Survival"}
	b.Server = ServerInfo{ID: "srv2", Name: "Creative"}
	h.Notify(a)
	h.Notify(b)
	h.Notify(a)
	h.sendDue()
	reqs := h.fake.take()
	if len(reqs) != 1 || len(reqs[0].Msg.Embeds) != 2 {
		t.Fatalf("both servers' crashes go out, the repeat doesn't: %+v", reqs)
	}
	for i, name := range []string{"Survival", "Creative"} {
		e := reqs[0].Msg.Embeds[i]
		if e.Author == nil || e.Author.Name != name || e.Author.URL != "https://panel.example.com" || !strings.Contains(e.Description, "**"+name+"** stopped unexpectedly") {
			t.Errorf("embed %d: %+v", i, e)
		}
	}
}

func TestJoinRequestAlertNamesThePlayerAndServerButNotTheLink(t *testing.T) {
	h := newDashboardHarness(t, Settings{Webhook: testWebhook(t, ""), Alerts: DefaultAlerts()})
	e := JoinRequested("Pixel_Pia")
	e.Server = ServerInfo{ID: "srv1", Name: "Survival"}
	h.Notify(e)
	h.Notify(e)
	other := JoinRequested("lena")
	other.Server = e.Server
	h.Notify(other)
	h.sendDue()
	reqs := h.fake.take()
	if len(reqs) != 1 || len(reqs[0].Msg.Embeds) != 2 {
		t.Fatalf("one alert per player: %+v", reqs)
	}
	got := reqs[0].Msg.Embeds[0]
	if got.Title != "Join request" || !strings.HasPrefix(got.Description, "**Pixel\\_Pia** wants to join **Survival**. Let them in or say no on the Players tab.") {
		t.Errorf("join request: %+v", got)
	}
}

func TestDashboardTestMessageTalksAboutEveryServer(t *testing.T) {
	h := newDashboardHarness(t, Settings{Webhook: testWebhook(t, "")})
	if err := h.SendTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := h.fake.take()
	if len(reqs) != 1 || !strings.HasPrefix(reqs[0].Msg.Embeds[0].Description, "Discord is connected. Playkeeper will post alerts about your servers in this channel.") {
		t.Errorf("test message: %+v", reqs)
	}
}

func TestWebhookNameOnlyReads(t *testing.T) {
	f := newFakeDiscord(t)
	name, err := WebhookName(context.Background(), f.client(), testWebhook(t, ""))
	if err != nil || name != "Playkeeper" {
		t.Fatalf("name %q, err %v", name, err)
	}
	reqs := f.take()
	if len(reqs) != 1 || reqs[0].Method != "GET" || reqs[0].Path != "/api/v10/webhooks/"+testID+"/"+testToken {
		t.Errorf("requests: %q", calls(reqs))
	}

	f.deleteWebhook(testID)
	_, err = WebhookName(context.Background(), f.client(), testWebhook(t, ""))
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeWebhookGone || strings.Contains(err.Error(), testToken) {
		t.Errorf("gone webhook: %v", err)
	}
	if _, err := WebhookName(context.Background(), f.client(), Webhook{}); !errors.As(err, &de) || de.Code != CodeNotConnected {
		t.Errorf("no webhook: %v", err)
	}
}

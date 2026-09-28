package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

func TestTheOwnersWordsAndStreamShowOnThePage(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.withPageAddress()
	about := "  Help Claude or sabotage it.\r\nNo TNT near Claude.\n\n\n\nUnofficial. Not affiliated with Anthropic or Mojang.  "
	code, out := e.call("POST", e.sp("/public-page"), map[string]any{"about": about, "stream": "twitch.tv/Example_Channel", "actor": "admin"})
	if code != 200 || out["about"] != "Help Claude or sabotage it.\nNo TNT near Claude.\n\nUnofficial. Not affiliated with Anthropic or Mojang." || out["stream"] != "https://www.twitch.tv/example_channel" {
		t.Fatalf("saving the page's words and stream: %d %v", code, out)
	}
	_, p, _ := e.page(pageTestHost)
	s := p.Servers[0]
	if s.About != out["about"] || s.Stream == nil || *s.Stream != (api.PublicStream{Site: "twitch", Channel: "example_channel", URL: "https://www.twitch.tv/example_channel"}) {
		t.Fatalf("the page shows %q and %+v", s.About, s.Stream)
	}
	yt := "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv"
	if code, out := e.call("POST", e.sp("/public-page"), map[string]any{"stream": yt, "actor": "admin"}); code != 200 || out["stream"] != yt {
		t.Fatalf("a YouTube channel: %d %v", code, out)
	}
	for _, bad := range []string{"https://evil.example/claude", "https://www.youtube.com/@claude", "javascript:alert(1)", "https://twitch.tv/a", "https://twitch.tv/claude/videos", "https://user@twitch.tv/claude", "https://twitch.tv:8443/claude"} {
		if code, out := e.call("POST", e.sp("/public-page"), map[string]any{"stream": bad, "actor": "admin"}); code != 400 || out["code"] != api.CodeInvalid {
			t.Errorf("stream %q: %d %v", bad, code, out)
		}
	}
	for name, bad := range map[string]string{
		"too long":          strings.Repeat("a", api.PublicAboutMax+1),
		"too many rows":     strings.Repeat("a\n", api.PublicAboutLines) + "a",
		"control":           "bell\a",
		"direction changed": "No griefing \u202egnihtyna",
	} {
		if code, _ := e.call("POST", e.sp("/public-page"), map[string]any{"about": bad, "actor": "admin"}); code != 400 {
			t.Errorf("about %s: %d", name, code)
		}
	}
	if code, out := e.call("POST", e.sp("/public-page"), map[string]any{"about": "Rules:\tbe kind,\u00a0have fun 👩‍💻", "stream": "https://evil.example/claude", "actor": "admin"}); code != 400 || out["code"] != api.CodeInvalid {
		t.Fatalf("a good About with a bad stream: %d %v", code, out)
	}
	if _, out := e.call("GET", e.sp("/public-page"), nil); out["about"] != s.About || out["stream"] != yt {
		t.Fatalf("a refused change saved part of itself: %v", out)
	}
	if code, out := e.call("POST", e.sp("/public-page"), map[string]any{"about": "Rules:\tbe kind,\u00a0have fun 👩‍💻", "actor": "admin"}); code != 200 || out["about"] != "Rules: be kind, have fun 👩‍💻" {
		t.Fatalf("pasted spaces and an emoji: %d %v", code, out)
	}
	if code, out := e.call("POST", e.sp("/public-page"), map[string]any{"about": "", "stream": "", "actor": "admin"}); code != 200 || out["about"] != "" || out["stream"] != "" {
		t.Fatalf("clearing: %d %v", code, out)
	}
	if _, p, _ := e.page(pageTestHost); p.Servers[0].About != "" || p.Servers[0].Stream != nil {
		t.Fatalf("cleared words or stream still show: %+v", p.Servers[0])
	}
}

func TestTheStatusBoardShowsWhatTheToolsPostWithinItsBounds(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.withPageAddress()
	next := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)
	board := map[string]any{"headline": " Day 3 · Nether reached ", "live": true, "next": next.Format(time.RFC3339),
		"stats":     []map[string]any{{"label": "Deaths", "value": "5"}, {"label": "Spent", "value": "$64.10"}},
		"checklist": []map[string]any{{"label": "Iron pickaxe", "done": true}, {"label": "Ender Dragon killed", "done": false}}, "actor": "harness"}
	if code, out := e.call("PUT", e.sp("/public-page/board"), board); code != 200 || out["headline"] != "Day 3 · Nether reached" || out["updatedAt"] == nil {
		t.Fatalf("posting a board: %d %v", code, out)
	}
	_, p, _ := e.page(pageTestHost)
	b := p.Servers[0].Board
	if b == nil || !b.Live || b.Next == nil || !b.Next.Equal(next) || len(b.Stats) != 2 || b.Stats[1] != (api.BoardStat{Label: "Spent", Value: "$64.10"}) || len(b.Checklist) != 2 || !b.Checklist[0].Done {
		t.Fatalf("the page shows %+v", b)
	}
	if _, out := e.call("GET", e.sp("/public-page"), nil); out["board"] == nil {
		t.Fatal("Settings don't see the board")
	}
	e.call("PUT", e.sp("/public-page/board"), board)

	many := func(n int, item map[string]any, key string) []map[string]any {
		var out []map[string]any
		for i := range n {
			it := map[string]any{}
			for k, v := range item {
				it[k] = v
			}
			it[key] = strings.Repeat("x", i+1)
			out = append(out, it)
		}
		return out
	}
	for name, bad := range map[string]map[string]any{
		"a long headline":      {"headline": strings.Repeat("h", api.BoardHeadlineMax+1)},
		"too many numbers":     {"stats": many(api.BoardStatsMax+1, map[string]any{"value": "1"}, "label")},
		"a long number":        {"stats": []map[string]any{{"label": "Tokens", "value": strings.Repeat("9", api.BoardStatValueMax+1)}}},
		"an empty label":       {"stats": []map[string]any{{"label": " ", "value": "1"}}},
		"the same label twice": {"checklist": []map[string]any{{"label": "Iron", "done": true}, {"label": "iron", "done": false}}},
		"too many goals":       {"checklist": many(api.BoardItemsMax+1, map[string]any{"done": false}, "label")},
		"a goal on two lines":  {"checklist": []map[string]any{{"label": "Iron\npickaxe", "done": false}}},
		"a session far off":    {"next": time.Now().Add(40 * 24 * time.Hour).UTC().Format(time.RFC3339)},
		"no actor":             {"headline": "hi", "actor": ""},
	} {
		body := map[string]any{"actor": "harness"}
		for k, v := range bad {
			body[k] = v
		}
		if code, _ := e.call("PUT", e.sp("/public-page/board"), body); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if code, _ := e.call("DELETE", e.sp("/public-page/board")+"?actor=harness", nil); code != 200 {
		t.Fatalf("clearing: %d", code)
	}
	e.call("DELETE", e.sp("/public-page/board")+"?actor=harness", nil)
	if _, p, _ := e.page(pageTestHost); p.Servers[0].Board != nil {
		t.Fatalf("a cleared board still shows: %+v", p.Servers[0].Board)
	}
	var posted, cleared int
	e.a.db.QueryRow(`SELECT COUNT(*) FROM audit WHERE action = 'public_page.board_posted'`).Scan(&posted)
	e.a.db.QueryRow(`SELECT COUNT(*) FROM audit WHERE action = 'public_page.board_cleared'`).Scan(&cleared)
	if posted != 1 || cleared != 1 {
		t.Fatalf("audited %d posts and %d clears; want the first post and the one clear", posted, cleared)
	}
}

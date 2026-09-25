package packs

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const unknownCommand = "Unknown or incomplete command. See below for error"

// fakeConsole answers each command with its replies in turn, repeating the
// last, and records the commands sent. Commands without replies get the
// game's answer to an unknown command.
type fakeConsole struct {
	mu      sync.Mutex
	replies map[string][]string
	err     error
	sent    []string
}

func (f *fakeConsole) Command(_ context.Context, cmd string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, cmd)
	if f.err != nil {
		return "", f.err
	}
	r := f.replies[cmd]
	if len(r) == 0 {
		return unknownCommand, nil
	}
	if len(r) > 1 {
		f.replies[cmd] = r[1:]
	}
	return r[0], nil
}

func (f *fakeConsole) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

// consolePrefixes are what Console must put before Minecraft's commands, by
// server type.
var consolePrefixes = map[string]string{
	"paper":    "minecraft:",
	"purpur":   "minecraft:",
	"vanilla":  "",
	"fabric":   "",
	"quilt":    "",
	"neoforge": "",
}

func TestConsoleEnable(t *testing.T) {
	const id = "file/terralith.zip"
	for _, tc := range []struct {
		name, reply string
		code, msg   string
		params      map[string]any
	}{
		{name: "enabled", reply: "Enabling data pack [file/terralith.zip (world)]"},
		{name: "formatted", reply: "§7Enabling data pack §r[§afile/terralith.zip (world)§r]\n"},
		{name: "already enabled", reply: "Pack 'file/terralith.zip' is already enabled!"},
		{
			name: "unknown pack", reply: "Unknown data pack 'file/terralith.zip'",
			code: CodeUnknownPack, msg: `The server doesn't know the data pack "file/terralith.zip".`,
			params: map[string]any{"id": id},
		},
		{
			name:   "needs features",
			reply:  "Pack 'file/terralith.zip' cannot be enabled, since required flags are not enabled in this world: minecraft:trade_rebalance, minecraft:minecart_improvements!",
			code:   CodeNeedsFeatures,
			msg:    `The data pack "file/terralith.zip" needs experimental features this world was created without: minecraft:trade_rebalance, minecraft:minecart_improvements.`,
			params: map[string]any{"id": id, "features": []string{"minecraft:trade_rebalance", "minecraft:minecart_improvements"}},
		},
		{
			name: "another pack", reply: "Enabling data pack [file/terralith.zip.old (world)]",
			code: CodeUnexpectedReply, msg: `The server replied in a way Playkeeper didn't expect: "Enabling data pack [file/terralith.zip.old (world)]".`,
			params: map[string]any{"reply": "Enabling data pack [file/terralith.zip.old (world)]"},
		},
		{
			name: "unknown command", reply: unknownCommand,
			code: CodeUnexpectedReply, msg: `The server replied in a way Playkeeper didn't expect: "Unknown or incomplete command. See below for error".`,
			params: map[string]any{"reply": unknownCommand},
		},
		{
			name: "silence", reply: "",
			code: CodeUnexpectedReply, msg: "The server didn't reply to the command.",
			params: map[string]any{"reply": ""},
		},
	} {
		for typ, p := range consolePrefixes {
			t.Run(tc.name+"/"+typ, func(t *testing.T) {
				rescan, enable := p+"datapack list available", p+`datapack enable "file/terralith.zip"`
				f := &fakeConsole{replies: map[string][]string{
					rescan: {"There are 1 data pack(s) available: [file/terralith.zip (world)]"},
					enable: {tc.reply},
				}}
				err := Console{Commander: f, ServerType: typ}.Enable(context.Background(), id)
				if got := f.commands(); !slices.Equal(got, []string{rescan, enable}) {
					t.Errorf("sent %q, want %q", got, []string{rescan, enable})
				}
				if tc.code == "" {
					if err != nil {
						t.Fatalf("Enable = %v", err)
					}
					return
				}
				e := wantCode(t, err, tc.code)
				if e.Msg != tc.msg || !reflect.DeepEqual(e.Params, tc.params) {
					t.Errorf("Enable = %q %v, want %q %v", e.Msg, e.Params, tc.msg, tc.params)
				}
			})
		}
	}
	err := Console{Commander: &fakeConsole{replies: map[string][]string{
		`datapack enable "file/terralith.zip"`: {"Unknown data pack 'file/terralith.zip'"},
	}}, ServerType: "vanilla"}.Enable(context.Background(), id)
	if !errors.Is(err, ErrUnknownPack) {
		t.Errorf("Enable = %v, want an error matching ErrUnknownPack", err)
	}
}

func TestConsoleDisable(t *testing.T) {
	for _, tc := range []struct {
		name, id, reply string
		code, msg       string
	}{
		{name: "disabled", id: "file/terralith.zip", reply: "Disabling data pack [file/terralith.zip (world)]"},
		{name: "not enabled", id: "file/terralith.zip", reply: "Pack 'file/terralith.zip' is not enabled!"},
		{
			name: "feature pack", id: "trade_rebalance",
			reply: "Pack 'trade_rebalance' cannot be disabled, since it is part of an enabled flag!",
			code:  CodeFeaturePack, msg: `The data pack "trade_rebalance" belongs to an experimental feature this world uses, so it can't be disabled.`,
		},
		{
			name: "unknown pack", id: "file/terralith.zip", reply: "Unknown data pack 'file/terralith.zip'",
			code: CodeUnknownPack, msg: `The server doesn't know the data pack "file/terralith.zip".`,
		},
		{
			name: "enabled instead", id: "file/terralith.zip", reply: "Enabling data pack [file/terralith.zip (world)]",
			code: CodeUnexpectedReply, msg: `The server replied in a way Playkeeper didn't expect: "Enabling data pack [file/terralith.zip (world)]".`,
		},
	} {
		for typ, p := range consolePrefixes {
			t.Run(tc.name+"/"+typ, func(t *testing.T) {
				disable := p + `datapack disable "` + tc.id + `"`
				f := &fakeConsole{replies: map[string][]string{disable: {tc.reply}}}
				err := Console{Commander: f, ServerType: typ}.Disable(context.Background(), tc.id)
				if got := f.commands(); !slices.Equal(got, []string{disable}) {
					t.Errorf("sent %q, want %q", got, []string{disable})
				}
				if tc.code == "" {
					if err != nil {
						t.Fatalf("Disable = %v", err)
					}
					return
				}
				if e := wantCode(t, err, tc.code); e.Msg != tc.msg {
					t.Errorf("Disable = %q, want %q", e.Msg, tc.msg)
				}
			})
		}
	}
}

func TestConsoleIDs(t *testing.T) {
	ctx := context.Background()
	for id, arg := range map[string]string{
		`file/we"ird\pack.zip`:      `"file/we\"ird\\pack.zip"`,
		"file/Zażółć gęślą.zip":     `"file/Zażółć gęślą.zip"`,
		"minecraft:trade_rebalance": `"minecraft:trade_rebalance"`,
		strings.Repeat("a", 256):    `"` + strings.Repeat("a", 256) + `"`,
	} {
		f := &fakeConsole{replies: map[string][]string{
			"datapack enable " + arg:  {"Enabling data pack [" + id + " (world)]"},
			"datapack disable " + arg: {"Disabling data pack [" + id + " (world)]"},
		}}
		c := Console{Commander: f, ServerType: "vanilla"}
		if err := c.Enable(ctx, id); err != nil {
			t.Errorf("Enable(%q) = %v", id, err)
		}
		if err := c.Disable(ctx, id); err != nil {
			t.Errorf("Disable(%q) = %v", id, err)
		}
	}
	for _, id := range []string{
		"", "a\nb", "a\rb", "a\x00b", "a\tb", "\x1b[31mred", "a\u0085b", "a\u2028b", "a\u2029b",
		"bad\xff.zip", strings.Repeat("a", 257),
	} {
		f := &fakeConsole{}
		c := Console{Commander: f, ServerType: "paper"}
		for name, err := range map[string]error{"Enable": c.Enable(ctx, id), "Disable": c.Disable(ctx, id)} {
			e := wantCode(t, err, CodeInvalidID)
			if e.Params["id"] != shortName(id) {
				t.Errorf("%s(%q) has params %v", name, id, e.Params)
			}
		}
		if got := f.commands(); len(got) != 0 {
			t.Errorf("an invalid ID %q sent %q", id, got)
		}
	}
	e := wantCode(t, Console{Commander: &fakeConsole{}, ServerType: "paper"}.Enable(ctx, "a\nb"), CodeInvalidID)
	if want := `"a\nb" isn't a data pack ID Playkeeper can send to the server.`; e.Msg != want {
		t.Errorf("message %q, want %q", e.Msg, want)
	}
}

func TestConsoleUnsupported(t *testing.T) {
	ctx := context.Background()
	for typ, msg := range map[string]string{
		"forge":  `Playkeeper can't manage data packs on "forge" servers.`,
		"spigot": `Playkeeper can't manage data packs on "spigot" servers.`,
		"":       "Playkeeper can't manage data packs on this server.",
	} {
		f := &fakeConsole{}
		c := Console{Commander: f, ServerType: typ}
		_, enabledErr := c.Enabled(ctx)
		_, availableErr := c.Available(ctx)
		for name, err := range map[string]error{
			"Enable":      c.Enable(ctx, "file/a.zip"),
			"Disable":     c.Disable(ctx, "file/a.zip"),
			"Enabled":     enabledErr,
			"Available":   availableErr,
			"Reload":      c.Reload(ctx),
			"WaitEnabled": c.WaitEnabled(ctx, "file/a.zip", true, time.Millisecond),
		} {
			e := wantCode(t, err, CodeUnsupportedServer)
			if e.Msg != msg || e.Params["type"] != typ {
				t.Errorf("%s on %q = %q %v, want %q", name, typ, e.Msg, e.Params, msg)
			}
		}
		if got := f.commands(); len(got) != 0 {
			t.Errorf("an unsupported server %q was sent %q", typ, got)
		}
	}
}

func TestConsoleLists(t *testing.T) {
	for _, tc := range []struct {
		name      string
		available bool
		reply     string
		want      []string
	}{
		{
			name:  "enabled",
			reply: "There are 3 data pack(s) enabled: [vanilla (built-in)], [fabric (Fabric mod)], [file/My Pack (1).zip (world)]",
			want:  []string{"vanilla", "fabric", "file/My Pack (1).zip"},
		},
		{
			name:  "formatted",
			reply: "§eThere are 2 data pack(s) enabled: §r[§avanilla (built-in)§r], [§afile/terralith.zip (world)§r]",
			want:  []string{"vanilla", "file/terralith.zip"},
		},
		{
			name:  "terminal colors",
			reply: "\x1b[0;32;1mThere are 1 data pack(s) enabled: [vanilla (built-in)]\x1b[m",
			want:  []string{"vanilla"},
		},
		{
			name:  "more lines",
			reply: "Loaded 7 recipes\nThere are 2 data pack(s) enabled: [vanilla (built-in)], [file/b.zip (world)]\n",
			want:  []string{"vanilla", "file/b.zip"},
		},
		{name: "none enabled", reply: "There are no data packs enabled", want: []string{}},
		{
			name: "available", available: true,
			reply: "There are 2 data pack(s) available: [file/a.zip (world)], [trade_rebalance (feature)]",
			want:  []string{"file/a.zip", "trade_rebalance"},
		},
		{name: "none available", available: true, reply: "There are no more data packs available", want: []string{}},
		{name: "unknown command", reply: unknownCommand},
		{name: "silence", reply: ""},
		{name: "the other list", reply: "There are 1 data pack(s) available: [file/a.zip (world)]"},
		{name: "the other empty list", available: true, reply: "There are no data packs enabled"},
	} {
		for typ, p := range consolePrefixes {
			t.Run(tc.name+"/"+typ, func(t *testing.T) {
				cmd := p + "datapack list enabled"
				list := Console.Enabled
				if tc.available {
					cmd, list = p+"datapack list available", Console.Available
				}
				f := &fakeConsole{replies: map[string][]string{cmd: {tc.reply}}}
				got, err := list(Console{Commander: f, ServerType: typ}, context.Background())
				if sent := f.commands(); !slices.Equal(sent, []string{cmd}) {
					t.Errorf("sent %q, want %q", sent, []string{cmd})
				}
				if tc.want == nil {
					wantCode(t, err, CodeUnexpectedReply)
					if got != nil {
						t.Errorf("got %q with an error", got)
					}
					return
				}
				if err != nil || got == nil || !slices.Equal(got, tc.want) {
					t.Errorf("got %q, %v, want %q", got, err, tc.want)
				}
			})
		}
	}
}

func TestConsoleReload(t *testing.T) {
	for _, tc := range []struct{ name, reply, code string }{
		{"reloading", "Reloading!", ""},
		{"formatted", "§aReloading!", ""},
		{"unknown command", unknownCommand, CodeUnexpectedReply},
		{"silence", "", CodeUnexpectedReply},
	} {
		for typ, p := range consolePrefixes {
			f := &fakeConsole{replies: map[string][]string{p + "reload": {tc.reply}}}
			err := Console{Commander: f, ServerType: typ}.Reload(context.Background())
			if got := f.commands(); !slices.Equal(got, []string{p + "reload"}) {
				t.Errorf("%s on %s sent %q, want %q", tc.name, typ, got, []string{p + "reload"})
			}
			if tc.code == "" {
				if err != nil {
					t.Errorf("%s on %s: Reload = %v", tc.name, typ, err)
				}
				continue
			}
			wantCode(t, err, tc.code)
		}
	}
	long := strings.Repeat("x", 1000)
	f := &fakeConsole{replies: map[string][]string{"reload": {long}}}
	e := wantCode(t, Console{Commander: f, ServerType: "vanilla"}.Reload(context.Background()), CodeUnexpectedReply)
	if want := strings.Repeat("x", 150) + "…" + strings.Repeat("x", 140); e.Params["reply"] != want || !strings.Contains(e.Msg, want) {
		t.Errorf("a long reply became %q in %q", e.Params["reply"], e.Msg)
	}
	if !errors.Is(e, ErrUnexpectedReply) {
		t.Error("the error doesn't match ErrUnexpectedReply")
	}
}

func TestConsoleErrors(t *testing.T) {
	inner := errors.New("dial tcp 172.18.0.2:25575: connect: connection refused")
	f := &fakeConsole{err: inner}
	e := wantCode(t, Console{Commander: f, ServerType: "paper"}.Enable(context.Background(), "file/a.zip"), CodeConsole)
	if e.Msg != "Playkeeper couldn't send a command to the server's console." || e.Hint == "" || !errors.Is(e, inner) {
		t.Errorf("console error = %q, hint %q, wrapping %v", e.Msg, e.Hint, e.Err)
	}
	if got := f.commands(); len(got) != 1 {
		t.Errorf("sent %q after the console failed", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := Console{Commander: &fakeConsole{err: errors.New("use of closed network connection")}, ServerType: "paper"}
	_, enabledErr := c.Enabled(ctx)
	for name, err := range map[string]error{
		"Enable":  c.Enable(ctx, "file/a.zip"),
		"Disable": c.Disable(ctx, "file/a.zip"),
		"Enabled": enabledErr,
		"Reload":  c.Reload(ctx),
	} {
		if err != context.Canceled {
			t.Errorf("%s with a canceled context = %v, want context.Canceled", name, err)
		}
	}
}

func TestWaitEnabled(t *testing.T) {
	const (
		id      = "file/terralith.zip"
		list    = "minecraft:datapack list enabled"
		none    = "There are no data packs enabled"
		vanilla = "There are 1 data pack(s) enabled: [vanilla (built-in)]"
		both    = "There are 2 data pack(s) enabled: [vanilla (built-in)], [file/terralith.zip (world)]"
	)
	for _, tc := range []struct {
		name    string
		want    bool
		replies []string
		polls   int
	}{
		{"enabled", true, []string{none, vanilla, both}, 3},
		{"enabled already", true, []string{both}, 1},
		{"disabled", false, []string{both, both, vanilla}, 3},
		{"disabled already", false, []string{none}, 1},
	} {
		f := &fakeConsole{replies: map[string][]string{list: tc.replies}}
		if err := (Console{Commander: f, ServerType: "paper"}).WaitEnabled(context.Background(), id, tc.want, time.Millisecond); err != nil {
			t.Errorf("%s: WaitEnabled = %v", tc.name, err)
		}
		if got := f.commands(); len(got) != tc.polls {
			t.Errorf("%s: sent %q, want %d polls", tc.name, got, tc.polls)
		}
	}

	for want, reply := range map[bool]string{true: vanilla, false: both} {
		verb := map[bool]string{true: "enabling", false: "disabling"}[want]
		f := &fakeConsole{replies: map[string][]string{list: {reply}}}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		err := Console{Commander: f, ServerType: "paper"}.WaitEnabled(ctx, id, want, 5*time.Millisecond)
		cancel()
		e := wantCode(t, err, CodeNotApplied)
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrNotApplied) {
			t.Errorf("timeout error %v doesn't match DeadlineExceeded and ErrNotApplied", err)
		}
		msg := "The server didn't finish " + verb + ` the data pack "file/terralith.zip" in time.`
		if e.Msg != msg || !reflect.DeepEqual(e.Params, map[string]any{"id": id, "enabled": want}) {
			t.Errorf("timeout = %q %v, want %q", e.Msg, e.Params, msg)
		}
	}

	f := &fakeConsole{replies: map[string][]string{list: {none, both}}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	wantCode(t, Console{Commander: f, ServerType: "paper"}.WaitEnabled(ctx, id, true, 0), CodeNotApplied)
	if got := f.commands(); len(got) != 1 {
		t.Errorf("with no interval, polled %d times in 100ms, want once a second", len(got))
	}

	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	err := Console{Commander: &fakeConsole{err: errors.New("use of closed network connection")}, ServerType: "paper"}.WaitEnabled(canceled, id, true, time.Millisecond)
	wantCode(t, err, CodeNotApplied)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("canceled wait = %v, want it to match context.Canceled", err)
	}

	f = &fakeConsole{err: errors.New("connection refused")}
	wantCode(t, Console{Commander: f, ServerType: "paper"}.WaitEnabled(context.Background(), id, true, time.Millisecond), CodeConsole)
	if got := f.commands(); len(got) != 1 {
		t.Errorf("kept polling after the console failed: %q", got)
	}
	wantCode(t, Console{Commander: &fakeConsole{}, ServerType: "paper"}.WaitEnabled(context.Background(), id, true, time.Millisecond), CodeUnexpectedReply)
}

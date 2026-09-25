package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/mcp"
)

type serverItem struct {
	ID            string `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	Machine       string `json:"machine,omitempty"`
	Status        string `json:"status"`
	PlayersOnline *int   `json:"playersOnline,omitempty"`
	MaxPlayers    *int   `json:"maxPlayers,omitempty"`
	Version       string `json:"version,omitempty"`
	// LastKnownStatus is the status its machine last reported, when the
	// machine can't be reached now.
	LastKnownStatus string     `json:"lastKnownStatus,omitempty"`
	LastKnownAt     *time.Time `json:"lastKnownAt,omitempty"`
}

func listServers(ctx context.Context, c *call) (*mcp.Result, error) {
	list, _, err := c.covered(ctx, "")
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	switch len(list) {
	case 0:
		b.WriteString("There are no servers you can use.")
	case 1:
		b.WriteString("1 server:")
	default:
		fmt.Fprintf(&b, "%d servers:", len(list))
	}
	items := make([]serverItem, 0, len(list))
	for _, s := range list {
		it := serverItem{ID: s.ID, Slug: s.Slug, Name: s.Name, Machine: s.MachineName, Status: phase(s.ServerStatus)}
		if s.Config != nil {
			it.Version = software(s.Config)
		}
		fmt.Fprintf(&b, "\n- %s (id %s, slug %s): ", describe(&s), s.ID, s.Slug)
		if s.LastKnownAt != nil {
			it.Status, it.LastKnownStatus, it.LastKnownAt = "unreachable", phase(s.ServerStatus), s.LastKnownAt
			fmt.Fprintf(&b, "its machine can't be reached; it was %s as of %s", it.LastKnownStatus, when(*s.LastKnownAt))
			items = append(items, it)
			continue
		}
		b.WriteString(it.Status)
		if s.Players != nil && s.Phase == api.PhaseOnline {
			it.PlayersOnline, it.MaxPlayers = &s.Players.Online, &s.Players.Max
			fmt.Fprintf(&b, ", %d of %d players online", s.Players.Online, s.Players.Max)
		}
		if it.Version != "" {
			b.WriteString(", " + it.Version)
		}
		items = append(items, it)
	}
	return &mcp.Result{Text: b.String(), Structured: map[string]any{"servers": items}}, nil
}

type playersOut struct {
	Online int      `json:"online"`
	Max    int      `json:"max"`
	Names  []string `json:"names"`
}

type operationOut struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	Phase      string     `json:"phase,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Error      string     `json:"error,omitempty"`
	Hint       string     `json:"hint,omitempty"`
}

func operationOf(op *api.Operation) *operationOut {
	if op == nil {
		return nil
	}
	return &operationOut{ID: op.ID, Kind: op.Kind, Status: op.Status, Phase: op.Phase, StartedAt: op.StartedAt,
		FinishedAt: op.FinishedAt, Error: clip(op.Error), Hint: clip(op.Hint)}
}

type statusOut struct {
	ID               string        `json:"id"`
	Name             string        `json:"name"`
	Slug             string        `json:"slug"`
	Machine          string        `json:"machine,omitempty"`
	Status           string        `json:"status"`
	Detail           string        `json:"detail,omitempty"`
	Wanted           string        `json:"wanted"`
	Reachable        bool          `json:"reachable"`
	GamePort         int           `json:"gamePort,omitempty"`
	Players          *playersOut   `json:"players,omitempty"`
	Version          string        `json:"version,omitempty"`
	MemoryMB         int           `json:"memoryMB,omitempty"`
	TPS              *float64      `json:"tps,omitempty"`
	CPUPercent       *float64      `json:"cpuPercent,omitempty"`
	MemoryUsedBytes  *int64        `json:"memoryUsedBytes,omitempty"`
	MemoryLimitBytes *int64        `json:"memoryLimitBytes,omitempty"`
	LastBackupAt     *time.Time    `json:"lastBackupAt,omitempty"`
	Problem          string        `json:"problem,omitempty"`
	Hint             string        `json:"hint,omitempty"`
	RecentCrashes    int           `json:"recentCrashes,omitempty"`
	Operation        *operationOut `json:"operation,omitempty"`
}

func getServerStatus(ctx context.Context, c *call) (*mcp.Result, error) {
	st, err := c.status(ctx)
	if err != nil {
		return nil, err
	}
	out := statusOut{ID: c.server.ID, Name: st.Name, Slug: st.Slug, Machine: c.server.MachineName, Status: phase(st), Detail: clip(st.PhaseDetail),
		Wanted: st.Desired, Reachable: st.Reachable, GamePort: st.GamePort, Problem: clip(st.LastError), Hint: clip(st.LastErrorHint),
		RecentCrashes: st.CrashCount, Operation: operationOf(st.Operation)}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (id %s) is %s", describe(c.server), c.server.ID, out.Status)
	if out.Detail != "" {
		fmt.Fprintf(&b, " (%s)", out.Detail)
	}
	b.WriteString(".")
	if st.Players != nil && st.Phase == api.PhaseOnline {
		out.Players = &playersOut{Online: st.Players.Online, Max: st.Players.Max, Names: nonNil(st.Players.Names)}
		fmt.Fprintf(&b, "\nPlayers: %s.", playersText(out.Players))
	}
	if st.Config != nil {
		out.Version, out.MemoryMB = software(st.Config), st.Config.MemoryMB
		fmt.Fprintf(&b, "\nSoftware: %s, with %d MB of memory.", out.Version, out.MemoryMB)
	}
	if r := st.Resources; r != nil && st.Phase == api.PhaseOnline {
		out.TPS, out.CPUPercent, out.MemoryUsedBytes, out.MemoryLimitBytes = r.TPS, r.CPUPercent, r.MemBytes, r.MemLimitBytes
		if now := resourcesText(r); now != "" {
			b.WriteString("\nNow: " + now + ".")
		}
	}
	if st.LastBackup != nil {
		out.LastBackupAt = &st.LastBackup.CreatedAt
		fmt.Fprintf(&b, "\nLast backup: %s.", when(st.LastBackup.CreatedAt))
	} else {
		b.WriteString("\nNo backups yet.")
	}
	if out.Problem != "" {
		b.WriteString("\nProblem: " + sentence(out.Problem, out.Hint))
	}
	if op := out.Operation; op != nil {
		fmt.Fprintf(&b, "\nIn progress: %s (operation %s", op.Kind, op.ID)
		if op.Phase != "" {
			b.WriteString(", " + op.Phase)
		}
		b.WriteString(").")
	}
	return &mcp.Result{Text: b.String(), Structured: out}, nil
}

func startServer(ctx context.Context, c *call) (*mcp.Result, error) {
	return c.begin(ctx, "/start", nil, "Starting")
}

func stopServer(ctx context.Context, c *call) (*mcp.Result, error) {
	return c.begin(ctx, "/stop", nil, "Stopping")
}

func restartServer(ctx context.Context, c *call) (*mcp.Result, error) {
	return c.begin(ctx, "/restart", nil, "Restarting")
}

func createBackup(ctx context.Context, c *call) (*mcp.Result, error) {
	var args struct {
		Server string `json:"server"`
		Note   string `json:"note"`
	}
	if err := c.Bind(&args); err != nil {
		return nil, err
	}
	return c.begin(ctx, "/backups", map[string]any{"note": strings.TrimSpace(args.Note)}, "Backing up")
}

// begin posts a request that starts an operation on the server, and
// describes the operation, or the agent's answer that there was nothing to
// do.
func (c *call) begin(ctx context.Context, suffix string, body map[string]any, verb string) (*mcp.Result, error) {
	if body == nil {
		body = map[string]any{}
	}
	body["actor"] = c.actor()
	var raw json.RawMessage
	status, err := c.do(ctx, "POST", c.path(suffix), nil, body, &raw)
	if err != nil {
		return nil, err
	}
	if status == http.StatusOK {
		var noop struct {
			Noop    bool   `json:"noop"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &noop) == nil && noop.Noop {
			msg := clip(noop.Message)
			return &mcp.Result{Text: msg, Structured: map[string]any{"noop": true, "message": msg}}, nil
		}
	}
	var op api.Operation
	if err := json.Unmarshal(raw, &op); err != nil || op.ID == "" {
		return nil, agentError(agentclient.ErrBadAnswer)
	}
	text := fmt.Sprintf("%s %s. It runs in the background as operation %s: call get_operation with server %q and that id to follow it.",
		verb, c.server.Name, op.ID, c.server.ID)
	return &mcp.Result{Text: text, Structured: map[string]any{"operation": operationOf(&op)}}, nil
}

// maxLineRunes bounds each console line put in front of the model.
const maxLineRunes = 1000

type lineOut struct {
	TS   time.Time `json:"ts"`
	Text string    `json:"text"`
}

// consoleLines reads the newest lines of the server's console.
func (c *call) consoleLines(ctx context.Context, n int) ([]lineOut, error) {
	var logs api.LogsResponse
	if _, err := c.do(ctx, "GET", c.path("/logs"), url.Values{"limit": {strconv.Itoa(n)}}, nil, &logs); err != nil {
		return nil, err
	}
	lines := logs.Lines
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]lineOut, 0, len(lines))
	for _, l := range lines {
		t := strings.ToValidUTF8(l.Text, "")
		if r := []rune(t); len(r) > maxLineRunes {
			t = string(r[:maxLineRunes]) + "…"
		}
		out = append(out, lineOut{TS: l.TS, Text: t})
	}
	return out, nil
}

func linesText(lines []lineOut) string {
	var b strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&b, "\n[%s] %s", l.TS.UTC().Format("15:04:05"), l.Text)
	}
	return b.String()
}

func readConsole(ctx context.Context, c *call) (*mcp.Result, error) {
	var args struct {
		Server string `json:"server"`
		Lines  int    `json:"lines"`
	}
	if err := c.Bind(&args); err != nil {
		return nil, err
	}
	if args.Lines == 0 {
		args.Lines = 50
	}
	lines, err := c.consoleLines(ctx, args.Lines)
	if err != nil {
		return nil, err
	}
	text := fmt.Sprintf("%s's console is empty: the server hasn't printed anything since Playkeeper last started.", c.server.Name)
	if len(lines) > 0 {
		text = fmt.Sprintf("The last %s of %s's console (times in UTC):", plural(len(lines), "line"), c.server.Name) + linesText(lines)
	}
	return &mcp.Result{Text: text, Structured: map[string]any{"lines": lines}}, nil
}

// command runs one console command on the server and returns its output.
func (c *call) command(ctx context.Context, cmd string) (string, error) {
	var out api.CommandResponse
	if _, err := c.do(ctx, "POST", c.path("/command"), nil, api.CommandRequest{Command: cmd, Actor: c.actor()}, &out); err != nil {
		return "", err
	}
	return clip(out.Output), nil
}

func sendChatMessage(ctx context.Context, c *call) (*mcp.Result, error) {
	var args struct {
		Server  string `json:"server"`
		Message string `json:"message"`
	}
	if err := c.Bind(&args); err != nil {
		return nil, err
	}
	msg := strings.TrimSpace(args.Message)
	if msg == "" {
		return nil, &mcp.ToolError{Kind: mcp.KindInvalidArguments, Msg: "The message is empty.", Hint: "Write what the players should read."}
	}
	if _, err := c.command(ctx, "say "+msg); err != nil {
		return nil, err
	}
	return &mcp.Result{Text: fmt.Sprintf("Sent to everyone on %s: %s", c.server.Name, msg), Structured: map[string]any{"sent": msg}}, nil
}

func runConsoleCommand(ctx context.Context, c *call) (*mcp.Result, error) {
	var args struct {
		Server  string `json:"server"`
		Command string `json:"command"`
	}
	if err := c.Bind(&args); err != nil {
		return nil, err
	}
	out, err := c.command(ctx, args.Command)
	if err != nil {
		return nil, err
	}
	text := fmt.Sprintf("%s ran the command and printed nothing.", c.server.Name)
	if strings.TrimSpace(out) != "" {
		text = fmt.Sprintf("%s printed:\n%s", c.server.Name, out)
	}
	return &mcp.Result{Text: text, Structured: map[string]any{"output": out}}, nil
}

func listOnlinePlayers(ctx context.Context, c *call) (*mcp.Result, error) {
	st, err := c.status(ctx)
	if err != nil {
		return nil, err
	}
	out := playersOut{Names: []string{}}
	var text string
	switch {
	case st.Phase != api.PhaseOnline:
		text = fmt.Sprintf("%s is %s, so nobody is online.", c.server.Name, phase(st))
	case st.Players == nil:
		return nil, &mcp.ToolError{Kind: "players_unknown", Msg: fmt.Sprintf("Playkeeper doesn't know yet who is online on %s.", c.server.Name),
			Hint: "The server has just started; try again in a minute."}
	default:
		out = playersOut{Online: st.Players.Online, Max: st.Players.Max, Names: nonNil(st.Players.Names)}
		text = fmt.Sprintf("On %s: %s.", c.server.Name, playersText(&out))
	}
	return &mcp.Result{Text: text, Structured: out}, nil
}

func listWhitelist(ctx context.Context, c *call) (*mcp.Result, error) {
	var list []api.WhitelistEntry
	if _, err := c.do(ctx, "GET", c.path("/whitelist"), nil, nil, &list); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(list))
	for _, e := range list {
		names = append(names, e.Name)
	}
	on := c.server.Config != nil && c.server.Config.Whitelist
	state := "off, so anyone can join"
	if on {
		state = "on, so only the players on it can join"
	}
	text := fmt.Sprintf("%s's whitelist is %s. It lists nobody.", c.server.Name, state)
	if len(names) > 0 {
		text = fmt.Sprintf("%s's whitelist is %s. It lists %s: %s.", c.server.Name, state, plural(len(names), "player"), strings.Join(names, ", "))
	}
	return &mcp.Result{Text: text, Structured: map[string]any{"enabled": on, "players": names}}, nil
}

func addToWhitelist(ctx context.Context, c *call) (*mcp.Result, error) {
	return c.whitelist(ctx, "POST")
}

func removeFromWhitelist(ctx context.Context, c *call) (*mcp.Result, error) {
	return c.whitelist(ctx, "DELETE")
}

func (c *call) whitelist(ctx context.Context, method string) (*mcp.Result, error) {
	var args struct {
		Server string `json:"server"`
		Player string `json:"player"`
	}
	if err := c.Bind(&args); err != nil {
		return nil, err
	}
	var out struct {
		Message string `json:"message"`
	}
	var err error
	if method == "POST" {
		_, err = c.do(ctx, "POST", c.path("/whitelist"), nil, api.WhitelistRequest{Name: args.Player, Actor: c.actor()}, &out)
	} else {
		_, err = c.do(ctx, "DELETE", c.path("/whitelist/"+url.PathEscape(args.Player)), url.Values{"actor": {c.actor()}}, nil, &out)
	}
	if err != nil {
		return nil, err
	}
	msg := clip(out.Message)
	return &mcp.Result{Text: fmt.Sprintf("%s answered: %s", c.server.Name, msg), Structured: map[string]any{"message": msg}}, nil
}

type backupOut struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	SizeBytes int64     `json:"sizeBytes"`
	Kind      string    `json:"kind"`
	Note      string    `json:"note,omitempty"`
	Verified  *bool     `json:"verified,omitempty"`
}

// maxBackups is how many backups list_backups shows.
const maxBackups = 50

func listBackups(ctx context.Context, c *call) (*mcp.Result, error) {
	var list []api.Backup
	if _, err := c.do(ctx, "GET", c.path("/backups"), nil, nil, &list); err != nil {
		return nil, err
	}
	slices.SortStableFunc(list, func(a, b api.Backup) int { return b.CreatedAt.Compare(a.CreatedAt) })
	shown := list[:min(len(list), maxBackups)]
	out := make([]backupOut, 0, len(shown))
	var b strings.Builder
	switch len(list) {
	case 0:
		fmt.Fprintf(&b, "%s has no backups yet.", c.server.Name)
	case len(shown):
		fmt.Fprintf(&b, "%s has %s, newest first:", c.server.Name, plural(len(list), "backup"))
	default:
		fmt.Fprintf(&b, "%s has %d backups; the newest %d:", c.server.Name, len(list), len(shown))
	}
	for _, bk := range shown {
		o := backupOut{ID: bk.ID, CreatedAt: bk.CreatedAt, SizeBytes: bk.SizeBytes, Kind: bk.Kind, Note: clip(bk.Note), Verified: bk.Verified}
		out = append(out, o)
		fmt.Fprintf(&b, "\n- %s, %s, id %s", when(bk.CreatedAt), size(bk.SizeBytes), bk.ID)
		if bk.Kind != "" && bk.Kind != "manual" {
			b.WriteString(", " + bk.Kind)
		}
		if o.Note != "" {
			fmt.Fprintf(&b, ", note: %s", o.Note)
		}
	}
	return &mcp.Result{Text: b.String(), Structured: map[string]any{"backups": out}}, nil
}

func getOperation(ctx context.Context, c *call) (*mcp.Result, error) {
	var args struct {
		Server    string `json:"server"`
		Operation string `json:"operation"`
	}
	if err := c.Bind(&args); err != nil {
		return nil, err
	}
	var op api.Operation
	_, err := c.do(ctx, "GET", "/v1/operations/"+url.PathEscape(args.Operation), nil, nil, &op)
	if err != nil && !isKind(err, api.CodeNotFound) {
		return nil, err
	}
	if err != nil || op.ServerID != c.server.ID || op.ID != args.Operation {
		return nil, &mcp.ToolError{Kind: "operation_not_found",
			Msg:  fmt.Sprintf("There is no operation %s on %s.", args.Operation, c.server.Name),
			Hint: "Use the id and server that start_server, stop_server, restart_server or create_backup returned."}
	}
	o := operationOf(&op)
	var text string
	switch op.Status {
	case api.OpRunning:
		text = fmt.Sprintf("The %s of %s is still running", op.Kind, c.server.Name)
		if op.Phase != "" {
			text += " (" + op.Phase + ")"
		}
		text += "; check again in a few seconds."
	case api.OpSucceeded:
		text = fmt.Sprintf("The %s of %s succeeded", op.Kind, c.server.Name)
		if op.FinishedAt != nil {
			text += " at " + when(*op.FinishedAt)
		}
		text += "."
	case api.OpFailed:
		text = fmt.Sprintf("The %s of %s failed: %s", op.Kind, c.server.Name, sentence(o.Error, o.Hint))
	default:
		text = fmt.Sprintf("The %s of %s is %s.", op.Kind, c.server.Name, op.Status)
	}
	return &mcp.Result{Text: text, Structured: o}, nil
}

func isKind(err error, kind string) bool {
	var te *mcp.ToolError
	return errors.As(err, &te) && te.Kind == kind
}

func getLagReport(ctx context.Context, c *call) (*mcp.Result, error) {
	st, err := c.status(ctx)
	if err != nil {
		return nil, err
	}
	if st.Phase != api.PhaseOnline {
		text := fmt.Sprintf("%s is %s, so it can't lag right now.", c.server.Name, phase(st))
		return &mcp.Result{Text: text, Structured: map[string]any{"status": phase(st)}}, nil
	}
	out := map[string]any{"status": phase(st)}
	var b strings.Builder
	fmt.Fprintf(&b, "%s is online.", c.server.Name)
	if r := st.Resources; r != nil {
		out["tps"], out["cpuPercent"], out["memoryUsedBytes"], out["memoryLimitBytes"] = r.TPS, r.CPUPercent, r.MemBytes, r.MemLimitBytes
		if now := resourcesText(r); now != "" {
			b.WriteString("\nNow: " + now + ".")
		}
		for _, v := range verdicts(r) {
			b.WriteString("\n" + v)
		}
	} else {
		b.WriteString("\nPlaykeeper has no measurements yet; try again in a minute.")
	}
	var m api.MetricsResponse
	if _, err := c.do(ctx, "GET", c.path("/metrics"), url.Values{"range": {"1h"}}, nil, &m); err == nil {
		if busy := busiest(m); busy != nil {
			out["lastHour"] = busy
			b.WriteString("\n" + busy.text())
		}
	}
	return &mcp.Result{Text: b.String(), Structured: out}, nil
}

// verdicts says in words what the numbers mean for lag.
func verdicts(r *api.Resources) []string {
	var out []string
	if r.TPS != nil {
		switch tps := *r.TPS; {
		case tps >= 19.5:
			out = append(out, "The game runs at full speed (20 TPS is full speed).")
		case tps >= 15:
			out = append(out, "The game runs a little behind full speed (20 TPS); players may notice.")
		default:
			out = append(out, "The game is lagging well behind full speed (20 TPS).")
		}
	}
	if r.CPUPercent != nil && *r.CPUPercent >= 90 {
		out = append(out, "The CPU is the likely cause: the server uses nearly all it may.")
	}
	if r.MemBytes != nil && r.MemLimitBytes != nil && *r.MemLimitBytes > 0 && float64(*r.MemBytes) >= 0.9*float64(*r.MemLimitBytes) {
		out = append(out, "Memory is nearly full, which slows Java down; more memory in the server's settings may help.")
	}
	return out
}

type busyHour struct {
	PeakCPUPercent *float64   `json:"peakCpuPercent,omitempty"`
	PeakCPUAt      *time.Time `json:"peakCpuAt,omitempty"`
	PeakMemBytes   *int64     `json:"peakMemoryBytes,omitempty"`
	PeakPlayers    *int       `json:"peakPlayers,omitempty"`
}

// busiest finds the busiest moments in a metrics range.
func busiest(m api.MetricsResponse) *busyHour {
	var out busyHour
	for _, bk := range m.Buckets {
		if bk.State != "online" {
			continue
		}
		if bk.CPUAvg != nil && (out.PeakCPUPercent == nil || *bk.CPUAvg > *out.PeakCPUPercent) {
			out.PeakCPUPercent, out.PeakCPUAt = bk.CPUAvg, &bk.Start
		}
		if bk.MemAvg != nil && (out.PeakMemBytes == nil || *bk.MemAvg > *out.PeakMemBytes) {
			out.PeakMemBytes = bk.MemAvg
		}
		if bk.PlayersMax != nil && (out.PeakPlayers == nil || *bk.PlayersMax > *out.PeakPlayers) {
			out.PeakPlayers = bk.PlayersMax
		}
	}
	if out.PeakCPUPercent == nil && out.PeakMemBytes == nil && out.PeakPlayers == nil {
		return nil
	}
	return &out
}

func (h *busyHour) text() string {
	var parts []string
	if h.PeakCPUPercent != nil {
		parts = append(parts, fmt.Sprintf("CPU peaked at %.0f%% around %s", *h.PeakCPUPercent, h.PeakCPUAt.UTC().Format("15:04 UTC")))
	}
	if h.PeakMemBytes != nil {
		parts = append(parts, "memory at "+size(*h.PeakMemBytes))
	}
	if h.PeakPlayers != nil {
		parts = append(parts, plural(*h.PeakPlayers, "player")+" at most")
	}
	return "In the last hour: " + strings.Join(parts, ", ") + "."
}

// crashLines is how many console lines explain_crash shows.
const crashLines = 30

func explainCrash(ctx context.Context, c *call) (*mcp.Result, error) {
	st, err := c.status(ctx)
	if err != nil {
		return nil, err
	}
	var events []api.Event
	if _, err := c.do(ctx, "GET", c.path("/events"), url.Values{"limit": {"200"}}, nil, &events); err != nil {
		return nil, err
	}
	var crash *api.Event
	for i := range events {
		if events[i].Kind == "server_crashed" && (crash == nil || events[i].TS.After(crash.TS)) {
			crash = &events[i]
		}
	}
	out := map[string]any{"status": phase(st), "recentCrashes": st.CrashCount}
	var b strings.Builder
	if crash == nil && st.Phase != api.PhaseCrashed {
		fmt.Fprintf(&b, "Playkeeper hasn't seen %s crash recently. It is %s.", c.server.Name, phase(st))
	} else {
		if crash != nil {
			out["crashedAt"], out["detail"] = crash.TS, clip(crash.Detail)
			fmt.Fprintf(&b, "%s last crashed at %s: %s", c.server.Name, when(crash.TS), clip(crash.Detail))
		} else {
			fmt.Fprintf(&b, "%s has crashed.", c.server.Name)
		}
		if st.CrashCount > 1 {
			fmt.Fprintf(&b, "\nIt crashed %d times recently.", st.CrashCount)
		}
		if st.LastError != "" {
			out["problem"], out["hint"] = clip(st.LastError), clip(st.LastErrorHint)
			b.WriteString("\nWhat Playkeeper says now: " + sentence(clip(st.LastError), clip(st.LastErrorHint)))
		}
		fmt.Fprintf(&b, "\nIt is %s now.", phase(st))
	}
	lines, err := c.consoleLines(ctx, crashLines)
	if err != nil {
		return nil, err
	}
	out["lines"] = lines
	if len(lines) > 0 {
		b.WriteString("\n\nThe console's last lines (times in UTC):" + linesText(lines))
	}
	return &mcp.Result{Text: b.String(), Structured: out}, nil
}

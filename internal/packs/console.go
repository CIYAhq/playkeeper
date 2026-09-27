package packs

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Commander sends a command to a server's console and returns its reply.
type Commander interface {
	Command(ctx context.Context, cmd string) (string, error)
}

// Console enables and disables a running server's data packs through its
// console, tells whether one is enabled, and reloads the server's data. It
// matches the server's English replies, which don't depend on players'
// languages.
type Console struct {
	Commander Commander
	// ServerType is the server's type, such as "paper" or "fabric".
	ServerType string
}

// reFormatting matches formatting codes and terminal escapes in replies.
var reFormatting = regexp.MustCompile(`§.|\x1b\[[0-9;?]*[ -/]*[@-~]`)

// Enable enables the installed data pack id, such as
// "file/terralith.zip". It first makes the server rescan its datapacks
// folder, so a pack just installed is found. The server then reloads its
// data in the background; WaitEnabled confirms the pack is active.
// Enabling an enabled pack succeeds.
func (c Console) Enable(ctx context.Context, id string) error {
	p, q, err := c.prepare(id)
	if err != nil {
		return err
	}
	if _, err := c.run(ctx, p+"datapack list available"); err != nil {
		return err
	}
	reply, err := c.run(ctx, p+"datapack enable "+q)
	if err != nil {
		return err
	}
	if strings.Contains(reply, "Enabling data pack ["+id+" (") || strings.Contains(reply, "Pack '"+id+"' is already enabled!") {
		return nil
	}
	return packError(id, reply)
}

// Disable disables the data pack id. The server then reloads its data in
// the background; WaitEnabled confirms the pack is inactive. Disabling a
// disabled pack succeeds.
func (c Console) Disable(ctx context.Context, id string) error {
	p, q, err := c.prepare(id)
	if err != nil {
		return err
	}
	reply, err := c.run(ctx, p+"datapack disable "+q)
	if err != nil {
		return err
	}
	if strings.Contains(reply, "Disabling data pack ["+id+" (") || strings.Contains(reply, "Pack '"+id+"' is not enabled!") {
		return nil
	}
	return packError(id, reply)
}

// IsEnabled reports whether the server has the data pack id enabled. The
// server's lists of packs can't tell: Paper renders at most 33 pieces of a
// message's text, so its answer to "datapack list" names four packs or so
// and ends in "...". Instead IsEnabled asks to enable the pack after one
// named "", which never exists. The server checks the pack first and then
// fails to find the other, so the command changes nothing.
func (c Console) IsEnabled(ctx context.Context, id string) (bool, error) {
	p, q, err := c.prepare(id)
	if err != nil {
		return false, err
	}
	reply, err := c.run(ctx, p+"datapack enable "+q+` after ""`)
	if err != nil {
		return false, err
	}
	switch {
	case strings.Contains(reply, "Pack '"+id+"' is already enabled!"):
		return true, nil
	case strings.Contains(reply, "Unknown data pack ''"), strings.Contains(reply, "Unknown data pack '"+id+"'"),
		strings.Contains(reply, "Pack '"+id+"' cannot be enabled, since required flags are not enabled in this world: "):
		return false, nil
	}
	return false, unexpectedReply(reply)
}

// Reload makes the server reload its data packs, as after a pack's files
// change. It runs Minecraft's own reload on every type of server: Paper's
// plain reload command reloads plugins too, which breaks many of them.
func (c Console) Reload(ctx context.Context) error {
	p, err := c.prefix()
	if err != nil {
		return err
	}
	reply, err := c.run(ctx, p+"reload")
	if err != nil {
		return err
	}
	if strings.Contains(reply, "Reloading!") {
		return nil
	}
	return unexpectedReply(reply)
}

// WaitEnabled asks the server every interval (a second if not positive)
// whether the data pack id is enabled, until the answer is want or ctx
// ends, when it fails with an error matching ErrNotApplied. After Enable
// and Disable the server reloads its data in the background, and when a
// pack breaks the reload the server keeps its old data and packs.
func (c Console) WaitEnabled(ctx context.Context, id string, want bool, every time.Duration) error {
	if every <= 0 {
		every = time.Second
	}
	for {
		enabled, err := c.IsEnabled(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				return notApplied(id, want, ctx.Err())
			}
			return err
		}
		if enabled == want {
			return nil
		}
		t := time.NewTimer(every)
		select {
		case <-ctx.Done():
			t.Stop()
			return notApplied(id, want, ctx.Err())
		case <-t.C:
		}
	}
}

// prefix is what goes before the datapack and reload commands: Paper
// names Minecraft's own commands "minecraft:…", so that plugins can't
// replace them, while other servers don't know that form.
func (c Console) prefix() (string, error) {
	switch c.ServerType {
	case "paper", "purpur":
		return "minecraft:", nil
	case "vanilla", "fabric", "quilt", "neoforge", "forge":
		return "", nil
	}
	what := "this server"
	if c.ServerType != "" {
		what = shortQuote(c.ServerType) + " servers"
	}
	return "", &Error{
		Code:   CodeUnsupportedServer,
		Params: map[string]any{"type": c.ServerType},
		Msg:    fmt.Sprintf("Playkeeper can't manage data packs on %s.", what),
	}
}

// prepare returns the command prefix and id quoted as a command argument.
func (c Console) prepare(id string) (prefix, quoted string, err error) {
	if prefix, err = c.prefix(); err != nil {
		return "", "", err
	}
	if id == "" || len(id) > 256 || !utf8.ValidString(id) || strings.ContainsFunc(id, func(r rune) bool {
		return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
	}) {
		return "", "", &Error{
			Code:   CodeInvalidID,
			Params: map[string]any{"id": shortName(id)},
			Msg:    fmt.Sprintf("%s isn't a data pack ID Playkeeper can send to the server.", shortQuote(id)),
		}
	}
	return prefix, `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(id) + `"`, nil
}

func (c Console) run(ctx context.Context, cmd string) (string, error) {
	reply, err := c.Commander.Command(ctx, cmd)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", &Error{
			Code: CodeConsole,
			Msg:  "Playkeeper couldn't send a command to the server's console.",
			Hint: "Check that the server is running, then try again.",
			Err:  err,
		}
	}
	return strings.TrimSpace(reFormatting.ReplaceAllString(reply, "")), nil
}

// packError is the error for the server's refusal to enable or disable the
// data pack id.
func packError(id, reply string) error {
	q := shortQuote(id)
	needs := "Pack '" + id + "' cannot be enabled, since required flags are not enabled in this world: "
	switch {
	case strings.Contains(reply, "Unknown data pack '"+id+"'"):
		return &Error{
			Code:   CodeUnknownPack,
			Params: map[string]any{"id": id},
			Msg:    fmt.Sprintf("The server doesn't know the data pack %s.", q),
			Hint:   "Check that the pack is installed in the world the server runs. The server's log says why it skips a pack it can't read.",
		}
	case strings.Contains(reply, needs):
		rest := reply[strings.Index(reply, needs)+len(needs):]
		rest, _, _ = strings.Cut(rest, "\n")
		features := strings.Split(strings.TrimSuffix(strings.TrimSpace(rest), "!"), ", ")
		return &Error{
			Code:   CodeNeedsFeatures,
			Params: map[string]any{"id": id, "features": features},
			Msg:    fmt.Sprintf("The data pack %s needs experimental features this world was created without: %s.", q, strings.Join(features, ", ")),
			Hint:   "Experimental features can only be turned on when a world is created. Create a new world with them, or use a version of the pack that doesn't need them.",
		}
	case strings.Contains(reply, "Pack '"+id+"' cannot be disabled, since it is part of an enabled flag!"):
		return &Error{
			Code:   CodeFeaturePack,
			Params: map[string]any{"id": id},
			Msg:    fmt.Sprintf("The data pack %s belongs to an experimental feature this world uses, so it can't be disabled.", q),
		}
	}
	return unexpectedReply(reply)
}

func unexpectedReply(reply string) *Error {
	reply = elide(reply, 300)
	msg := "The server didn't reply to the command."
	if reply != "" {
		msg = fmt.Sprintf("The server replied in a way Playkeeper didn't expect: %s.", strconv.Quote(reply))
	}
	return &Error{
		Code:   CodeUnexpectedReply,
		Params: map[string]any{"reply": reply},
		Msg:    msg,
		Hint:   "Check the server's console for errors, then try again.",
	}
}

func notApplied(id string, want bool, err error) *Error {
	verb := "enabling"
	if !want {
		verb = "disabling"
	}
	return &Error{
		Code:   CodeNotApplied,
		Params: map[string]any{"id": id, "enabled": want},
		Msg:    fmt.Sprintf("The server didn't finish %s the data pack %s in time.", verb, shortQuote(id)),
		Hint:   "Check the server's console: when a data pack has errors, the reload fails and the server keeps its previous data packs.",
		Err:    err,
	}
}

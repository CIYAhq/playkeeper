package mcp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func pingLine(id any) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%v,"method":"ping"}`, id)
}

func slowLine(id any) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%v,"method":"tools/call","params":{"name":"slow"}}`, id) + "\n"
}

// silent checks that the server writes nothing for a short while.
func (sp *stdioPipe) silent(t *testing.T) {
	t.Helper()
	select {
	case line, ok := <-sp.lines:
		if !ok {
			t.Fatal("the server closed its output")
		}
		t.Fatalf("unexpected output from the server: %s", line)
	case <-time.After(20 * time.Millisecond):
	}
}

// wantLine checks the next line the server writes.
func (sp *stdioPipe) wantLine(t *testing.T, want string) {
	t.Helper()
	if got := string(sp.next(t)); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestStdioFraming(t *testing.T) {
	f := newFixture(t, nil)
	sp := startStdio(t, f.srv, principal(ScopeRead))
	msg := pingLine(1)
	for i := range len(msg) {
		sp.write(t, msg[i:i+1])
	}
	sp.silent(t)
	sp.write(t, "\n")
	sp.wantLine(t, `{"jsonrpc":"2.0","id":1,"result":{}}`)

	// Blank lines, spaces and CRLF line ends.
	sp.write(t, "\r\n\n  \t\n  "+pingLine(2)+"  \r\n")
	sp.wantLine(t, `{"jsonrpc":"2.0","id":2,"result":{}}`)

	// Two messages in one write, then one split across writes.
	sp.write(t, pingLine(3)+"\n"+pingLine(4)+"\n"+`{"jsonrpc":"2.0",`)
	sp.wantLine(t, `{"jsonrpc":"2.0","id":3,"result":{}}`)
	sp.wantLine(t, `{"jsonrpc":"2.0","id":4,"result":{}}`)
	sp.silent(t)
	sp.write(t, `"id":5,"method":"ping"}`+"\n")
	sp.wantLine(t, `{"jsonrpc":"2.0","id":5,"result":{}}`)

	// A newline inside a string is escaped, so every message is one line.
	c := &stdioClient{pipe: sp}
	start(t, c, version20251125)
	r := c.call(t, "tools/call", map[string]any{"name": "echo", "arguments": map[string]any{"text": "a\nb"}})
	if got := resultText(t, r.wantResult(t)); got != "a\nb" {
		t.Errorf("echo gave %q", got)
	}
}

func TestStdioOverlongLines(t *testing.T) {
	const tooLong = `{"jsonrpc":"2.0","error":{"code":-32600,"message":"The message is longer than 64 bytes."}}`
	f := newFixture(t, func(o *Options) { o.MaxMessageBytes = 64 })
	sp := startStdio(t, f.srv, principal(ScopeRead))
	exact := pingLine(1) + strings.Repeat(" ", 64-len(pingLine(1)))
	sp.write(t, exact+"\n")
	sp.wantLine(t, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	sp.write(t, exact+" \n")
	sp.wantLine(t, tooLong)
	// A long line is skipped whole, however many reads it takes, and the
	// next line is read as usual.
	sp.write(t, strings.Repeat("x", 10000)+"\n"+pingLine(2)+"\n")
	sp.wantLine(t, tooLong)
	sp.wantLine(t, `{"jsonrpc":"2.0","id":2,"result":{}}`)
}

func TestStdioEndOfInput(t *testing.T) {
	t.Run("a final line without a newline", func(t *testing.T) {
		f := newFixture(t, nil)
		sp := startStdio(t, f.srv, principal(ScopeRead))
		sp.write(t, pingLine(1))
		sp.silent(t)
		sp.in.Close()
		sp.wantLine(t, `{"jsonrpc":"2.0","id":1,"result":{}}`)
		if err := sp.wait(t); err != nil {
			t.Errorf("ServeStdio returned %v", err)
		}
	})
	t.Run("requests in progress", func(t *testing.T) {
		f := newFixture(t, nil)
		c := newStdioClient(t, f.srv, principal(ScopeRead), version20251125)
		c.pipe.write(t, slowLine(`"slow"`))
		f.gate.waitStarted(t)
		c.pipe.in.Close()
		c.pipe.stillRunning(t)
		f.gate.releaseOne(t)
		r := parseResp(t, c.pipe.next(t))
		if got := resultText(t, r.wantResult(t)); got != "released" || r.id() != `"slow"` {
			t.Errorf("id %s: %q", r.id(), got)
		}
		if err := c.pipe.wait(t); err != nil {
			t.Errorf("ServeStdio returned %v", err)
		}
	})
}

func TestStdioStopsWhenTheContextEnds(t *testing.T) {
	f := newFixture(t, nil)
	c := newStdioClient(t, f.srv, principal(ScopeRead), version20251125)
	c.pipe.write(t, slowLine(9))
	f.gate.waitStarted(t)
	c.pipe.cancel()
	r := parseResp(t, c.pipe.next(t))
	if wantMessage(t, r, codeInternalError, "The request was cancelled.", ""); r.id() != "9" {
		t.Errorf("response id %s", r.id())
	}
	if err := c.pipe.wait(t); !errors.Is(err, context.Canceled) {
		t.Errorf("ServeStdio returned %v", err)
	}
	if err := f.gate.waitEnded(t); !errors.Is(err, context.Canceled) {
		t.Errorf("the call ended with %v", err)
	}
	if got := f.outcomes(); !slices.Equal(got, []Outcome{OutcomeCancelled}) {
		t.Errorf("outcomes %v", got)
	}
}

func TestStdioRequestsAreCancelledByNotification(t *testing.T) {
	for _, v := range []string{version20251125, version20260728} {
		t.Run(v, func(t *testing.T) {
			f := newFixture(t, nil)
			c := newStdioClient(t, f.srv, principal(ScopeRead), v)
			c.pipe.write(t, string(c.message(77, "tools/call", map[string]any{"name": "slow"}))+"\n")
			f.gate.waitStarted(t)
			c.notify(t, "notifications/cancelled", map[string]any{"requestId": 77, "reason": "Not needed any more."})
			if err := f.gate.waitEnded(t); !errors.Is(err, context.Canceled) {
				t.Errorf("the call ended with %v", err)
			}
			// The cancelled request goes unanswered: the next line answers
			// the next request.
			c.quiet(t)
			c.pipe.in.Close()
			if err := c.pipe.wait(t); err != nil {
				t.Errorf("ServeStdio returned %v", err)
			}
			if got := f.outcomes(); !slices.Equal(got, []Outcome{OutcomeCancelled}) {
				t.Errorf("outcomes %v", got)
			}
		})
	}
	t.Run("in a batch", func(t *testing.T) {
		f := newFixture(t, nil)
		c := newStdioClient(t, f.srv, principal(ScopeRead), version20250326)
		c.pipe.write(t, "["+strings.TrimSuffix(slowLine(1), "\n")+","+pingLine(2)+"]\n")
		f.gate.waitStarted(t)
		// Lines are still read while the batch runs.
		c.notify(t, "notifications/cancelled", map[string]any{"requestId": 1})
		c.pipe.wantLine(t, `[{"jsonrpc":"2.0","id":2,"result":{}}]`)
	})
}

func TestStdioRequestsRunConcurrently(t *testing.T) {
	f := newFixture(t, nil)
	c := newStdioClient(t, f.srv, principal(ScopeRead), version20251125)
	c.pipe.write(t, slowLine(101))
	f.gate.waitStarted(t)
	c.pipe.write(t, slowLine(102))
	f.gate.waitStarted(t)
	r := c.call(t, "tools/call", map[string]any{"name": "echo", "arguments": map[string]any{"text": "meanwhile"}})
	if got := resultText(t, r.wantResult(t)); got != "meanwhile" {
		t.Errorf("echo gave %q", got)
	}
	var ids []string
	for range 2 {
		f.gate.releaseOne(t)
		r := parseResp(t, c.pipe.next(t))
		if got := resultText(t, r.wantResult(t)); got != "released" {
			t.Errorf("id %s: %q", r.id(), got)
		}
		ids = append(ids, r.id())
	}
	if slices.Sort(ids); !slices.Equal(ids, []string{"101", "102"}) {
		t.Errorf("answered ids %v", ids)
	}
}

func TestStdioCapsRequestsInProgress(t *testing.T) {
	f := newFixture(t, nil)
	c := newStdioClient(t, f.srv, principal(ScopeRead), version20250326)
	for i := range maxStdioInFlight {
		c.pipe.write(t, slowLine(1000+i))
		f.gate.waitStarted(t)
	}
	busy := fmt.Sprintf("The server is already handling %d requests from this client.", maxStdioInFlight)
	wantMessage(t, c.call(t, "ping", nil), codeInternalError, busy, "Wait for one to finish, then send the request again.")
	r := c.send(t, "["+pingLine(50)+"]", 1)[0]
	if wantMessage(t, r, codeInternalError, busy, "Wait for one to finish, then send the batch again."); r.id() != "" {
		t.Errorf("the batch error has id %s", r.id())
	}
	f.gate.releaseAll()
	for range maxStdioInFlight {
		parseResp(t, c.pipe.next(t)).wantResult(t)
	}
	c.call(t, "ping", nil).wantResult(t)
}

func TestStdioRefusesADuplicateID(t *testing.T) {
	const inProgress = "A request with this id is already in progress."
	f := newFixture(t, nil)
	c := newStdioClient(t, f.srv, principal(ScopeRead), version20251125)
	c.pipe.write(t, slowLine(`"a"`))
	f.gate.waitStarted(t)
	for _, raw := range []string{pingLine(`"a"`), pingLine(`"\u0061"`)} {
		r := c.send(t, raw, 1)[0]
		if wantMessage(t, r, codeInvalidRequest, inProgress, "Give every request its own id."); r.id() != `"a"` {
			t.Errorf("%s: response id %s", raw, r.id())
		}
	}
	// 5 and "5" are different ids.
	c.pipe.write(t, slowLine(5))
	f.gate.waitStarted(t)
	r := c.send(t, pingLine(`"5"`), 1)[0]
	if r.wantResult(t); r.id() != `"5"` {
		t.Errorf("response id %s", r.id())
	}
	f.gate.releaseAll()
	ids := []string{parseResp(t, c.pipe.next(t)).id(), parseResp(t, c.pipe.next(t)).id()}
	if slices.Sort(ids); !slices.Equal(ids, []string{`"a"`, "5"}) {
		t.Errorf("answered ids %v", ids)
	}
	// Once answered, an id can be used again.
	c.send(t, pingLine(`"a"`), 1)[0].wantResult(t)
}

func TestStdioInitializeOnlyOnce(t *testing.T) {
	f := newFixture(t, nil)
	c := newStdioClient(t, f.srv, principal(ScopeOwner), version20250326)
	wantMessage(t, c.call(t, "initialize", initParams(version20251125)), codeInvalidRequest,
		"initialize was already sent on this connection.", "Start the server again to begin a new session.")
	if echo := dig(t, c.call(t, "tools/list", nil).wantResult(t), "tools", 0); has(echo, "title") {
		t.Errorf("the second initialize changed the revision: %v", echo)
	}
}

func TestStdioIgnoresMessagesThatNeedNoAnswer(t *testing.T) {
	f := newFixture(t, nil)
	c := newStdioClient(t, f.srv, principal(ScopeRead), version20251125)
	for _, raw := range []string{
		`{"jsonrpc":"2.0","method":5}`,
		`{"jsonrpc":"2.0","method":"notifications/whatever","params":[1]}`,
		`{"jsonrpc":"2.0","id":9,"result":{}}`,
		`{"jsonrpc":"2.0","id":9,"error":{"code":1,"message":"No."}}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":{"n":1}}}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled"}`,
	} {
		c.pipe.write(t, raw+"\n")
	}
	c.quiet(t)
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestStdioStopsWhenItsOutputFails(t *testing.T) {
	f := newFixture(t, nil)
	inR, inW := io.Pipe()
	t.Cleanup(func() { inW.Close() })
	done := make(chan error, 1)
	go func() { done <- f.srv.ServeStdio(context.Background(), principal(ScopeRead), inR, failingWriter{}) }()
	if _, err := io.WriteString(inW, pingLine(1)+"\n"); err != nil {
		t.Fatal(err)
	}
	if err := receive(t, done); err == nil || err.Error() != "mcp: writing to the client: broken pipe" {
		t.Errorf("ServeStdio returned %v", err)
	}
}

func TestStdioReportsReadErrors(t *testing.T) {
	f := newFixture(t, nil)
	in := io.MultiReader(strings.NewReader(pingLine(1)+"\n"), iotest.ErrReader(errors.New("disk on fire")))
	var out bytes.Buffer
	err := f.srv.ServeStdio(context.Background(), principal(ScopeRead), in, &out)
	if err == nil || err.Error() != "mcp: reading from the client: disk on fire" {
		t.Errorf("ServeStdio returned %v", err)
	}
	if got := out.String(); got != `{"jsonrpc":"2.0","id":1,"result":{}}`+"\n" {
		t.Errorf("output %q", got)
	}
}

func TestStdioWritesOnlyMessagesToItsOutput(t *testing.T) {
	logR, logW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = logW
	srv, err := New(Options{Tools: fakeTools(&fixture{gate: newGate()})})
	os.Stderr = stderr
	if err != nil {
		t.Fatal(err)
	}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"offline","_meta":` +
		`{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}` + "\n")
	var out bytes.Buffer
	if err := srv.ServeStdio(context.Background(), principal(ScopeRead), in, &out); err != nil {
		t.Fatal(err)
	}
	logW.Close()
	logged, err := io.ReadAll(logR)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), "mcp: tool call") {
		t.Errorf("standard error has no log of the call: %q", logged)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("the output has %d lines: %q", len(lines), out.String())
	}
	if res := parseResp(t, []byte(lines[0])).wantResult(t); res["isError"] != true {
		t.Errorf("result %v", res)
	}
}

func TestReadLine(t *testing.T) {
	const max = 30
	input := "short\n" + strings.Repeat("a", 40) + "\n" + strings.Repeat("b", 20) + "\n" +
		strings.Repeat("c", max) + "\n" + strings.Repeat("d", max+1) + "\n\nlast"
	r := bufio.NewReaderSize(strings.NewReader(input), 16)
	for _, want := range []struct {
		line    string
		tooLong bool
		err     error
	}{
		{"short", false, nil},
		{"", true, nil},
		{strings.Repeat("b", 20), false, nil},
		{strings.Repeat("c", max), false, nil},
		{"", true, nil},
		{"", false, nil},
		{"last", false, io.EOF},
		{"", false, io.EOF},
	} {
		line, tooLong, err := readLine(r, max)
		if string(line) != want.line || tooLong != want.tooLong || err != want.err {
			t.Errorf("readLine = %q, %v, %v, want %q, %v, %v", line, tooLong, err, want.line, want.tooLong, want.err)
		}
	}
}

package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// maxStdioInFlight caps the requests one stdio client can have running.
const maxStdioInFlight = 16

// ServeStdio serves one client over the stdio transport: newline-delimited
// JSON-RPC messages read from in, with answers written to out one per line.
// Nothing else is ever written to out; logs go to the server's Logger.
//
// Every request runs as p. The transport has no authentication of its own,
// so whoever can start the process acts as p; the caller must choose p to
// match, such as owner scope for root on the host.
//
// ServeStdio returns nil once in reaches end of file and the requests in
// progress have been answered. When ctx ends or out fails it cancels the
// requests in progress and returns the cause, without waiting for a read
// from in that is still pending.
func (s *Server) ServeStdio(ctx context.Context, p Principal, in io.Reader, out io.Writer) error {
	ctx, fail := context.WithCancelCause(ctx)
	defer fail(nil)
	c := &stdioConn{
		srv: s, principal: p, ctx: ctx, fail: fail, reqs: newRequests(ctx),
		slots: make(chan struct{}, maxStdioInFlight), out: out,
	}
	lines := make(chan stdioLine)
	go c.read(bufio.NewReader(in), lines)
	for {
		select {
		case <-ctx.Done():
			c.stop()
			return context.Cause(ctx)
		case l := <-lines:
			if ctx.Err() != nil {
				continue
			}
			switch {
			case l.tooLong:
				c.write(reply(nil, nil, newError(codeInvalidRequest,
					"The message is longer than "+sizeText(int64(s.maxMessage))+".", "")))
			case l.line != nil:
				c.handleLine(l.line)
			}
			if l.err != nil {
				c.stop()
				if err := context.Cause(ctx); err != nil {
					return err
				}
				if errors.Is(l.err, io.EOF) {
					return nil
				}
				return fmt.Errorf("mcp: reading from the client: %w", l.err)
			}
		}
	}
}

type stdioConn struct {
	srv       *Server
	principal Principal
	ctx       context.Context
	fail      context.CancelCauseFunc
	reqs      requests
	slots     chan struct{}
	wg        sync.WaitGroup

	mu      sync.Mutex // guards version and client
	version string     // negotiated by initialize; empty before
	client  ClientInfo

	outMu  sync.Mutex
	out    io.Writer
	closed bool
}

type stdioLine struct {
	line    []byte
	tooLong bool
	err     error
}

func (c *stdioConn) read(r *bufio.Reader, lines chan<- stdioLine) {
	for {
		line, tooLong, err := readLine(r, c.srv.maxMessage)
		select {
		case lines <- stdioLine{line: line, tooLong: tooLong, err: err}:
		case <-c.ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

// readLine reads one line of at most max bytes and returns it without the
// newline. A longer line is consumed and reported as tooLong. At the end of
// the input, a final line without a newline is returned with the error.
func readLine(r *bufio.Reader, max int) (line []byte, tooLong bool, err error) {
	for {
		chunk, err := r.ReadSlice('\n')
		n := len(chunk)
		if err == nil {
			n--
		}
		if !tooLong {
			if len(line)+n > max {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk[:n]...)
			}
		}
		if err != bufio.ErrBufferFull {
			return line, tooLong, err
		}
	}
}

// stop waits for the requests in progress, which ctx cancels if it has
// ended, and closes the output.
func (c *stdioConn) stop() {
	c.wg.Wait()
	c.outMu.Lock()
	c.closed = true
	c.outMu.Unlock()
}

func (c *stdioConn) handleLine(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	if !json.Valid(line) {
		c.write(reply(nil, nil, newError(codeParseError, "The message is not valid JSON.", "")))
		return
	}
	if line[0] == '[' {
		c.batch(line)
		return
	}
	m, errResp := decodeMessage(line)
	switch {
	case errResp != nil:
		c.write(errResp)
	case m == nil, m.kind == kindResponse:
		// Malformed notifications and responses to requests the server
		// never sends get no answer.
	case m.kind == kindNotification:
		c.srv.notify(&c.reqs, m)
	default:
		c.request(m)
	}
}

// request answers initialize, and pings before it, right away, so that the
// negotiated revision applies to every later line; other requests run
// concurrently.
func (c *stdioConn) request(m *message) {
	cl, resp := c.route(m)
	if resp != nil {
		c.write(resp)
		return
	}
	select {
	case c.slots <- struct{}{}:
	default:
		c.write(reply(m.id, nil, newError(codeInternalError,
			fmt.Sprintf("The server is already handling %d requests from this client.", maxStdioInFlight),
			"Wait for one to finish, then send the request again.")))
		return
	}
	ctx, finish, ok := c.reqs.begin(m.id)
	if !ok {
		<-c.slots
		c.write(reply(m.id, nil, errDuplicateID()))
		return
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() { <-c.slots }()
		result, e := c.srv.handle(ctx, cl, m)
		if !finish() {
			c.write(reply(m.id, result, e))
		}
	}()
}

// route works out which revision a request uses. It returns the response
// to send instead when the request needs no handler.
func (c *stdioConn) route(m *message) (caller, *response) {
	modern, version, e := requestEra(m.params)
	if e != nil {
		return caller{}, reply(m.id, nil, e)
	}
	if modern {
		cl, e := modernCaller(c.principal, version, m.params)
		if e != nil {
			return caller{}, reply(m.id, nil, e)
		}
		return cl, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case m.method == "initialize":
		return caller{}, c.initializeLocked(m)
	case c.version != "":
		return caller{principal: c.principal, version: c.version, client: c.client}, nil
	case m.method == "ping":
		return caller{}, reply(m.id, map[string]any{}, nil)
	}
	return caller{}, reply(m.id, nil, errNoContext())
}

func (c *stdioConn) initializeLocked(m *message) *response {
	if c.version != "" {
		return reply(m.id, nil, newError(codeInvalidRequest,
			"initialize was already sent on this connection.",
			"Start the server again to begin a new session."))
	}
	version, client, result, e := c.srv.initialize(m.params)
	if e != nil {
		return reply(m.id, nil, e)
	}
	c.version, c.client = version, client
	return reply(m.id, result, nil)
}

// batch answers a JSON-RPC batch with one array once all its messages are
// done. It runs in the background like a single request, so that the next
// lines, notifications/cancelled among them, are still read.
func (c *stdioConn) batch(line []byte) {
	c.mu.Lock()
	cl := caller{principal: c.principal, version: c.version, client: c.client}
	c.mu.Unlock()
	raws, e := checkBatch(line, cl.version)
	if e != nil {
		c.write(reply(nil, nil, e))
		return
	}
	select {
	case c.slots <- struct{}{}:
	default:
		c.write(reply(nil, nil, newError(codeInternalError,
			fmt.Sprintf("The server is already handling %d requests from this client.", maxStdioInFlight),
			"Wait for one to finish, then send the batch again.")))
		return
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() { <-c.slots }()
		if out := c.srv.batch(&c.reqs, cl, raws); len(out) > 0 {
			c.write(out)
		}
	}()
}

// write sends one message as one line. json.Marshal escapes newlines inside
// strings, so a message can never span lines.
func (c *stdioConn) write(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		c.srv.log.Error("mcp: encoding a response", "err", err)
		b, _ = json.Marshal(reply(nil, nil, newError(codeInternalError, "The server could not encode its response.", "")))
	}
	b = append(b, '\n')
	c.outMu.Lock()
	defer c.outMu.Unlock()
	if c.closed {
		return
	}
	if _, err := c.out.Write(b); err != nil {
		c.closed = true
		c.fail(fmt.Errorf("mcp: writing to the client: %w", err))
	}
}

package minecraft

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	rconAuth         = 3
	rconExec         = 2
	rconMarkerType   = 200 // unknown type; the server answers it in order, marking the end of a reply
	rconChunk        = 4096
	maxRCONPayload   = 1446
	maxRCONReplySize = 1 << 20
	// rconProbe is how long a command waits to see whether the server closed
	// an idle connection before writing to it.
	rconProbe = 5 * time.Millisecond
	// rconDefaultTimeout bounds a command whose context has no deadline.
	rconDefaultTimeout = 30 * time.Second
)

var ErrRCONAuth = errors.New("rcon: authentication failed")

// ErrNotSent marks a command that never reached the server: the connection
// was found closed, or failed before any of the command was written. Only
// such a command may be sent again, on a new connection. After any other
// error the server may have run it.
var ErrNotSent = errors.New("rcon: the command was not sent")

type notSentError struct{ err error }

func (e notSentError) Error() string   { return e.err.Error() }
func (e notSentError) Unwrap() []error { return []error{ErrNotSent, e.err} }

func notSent(err error) error { return notSentError{err} }

// RCON is a minimal client for the Minecraft remote console protocol. It is
// only ever dialled by the root agent over the private Docker bridge.
type RCON struct {
	mu     sync.Mutex
	conn   net.Conn
	br     *bufio.Reader
	nextID int32
}

func DialRCON(addr, password string, timeout time.Duration) (*RCON, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return DialRCONContext(ctx, addr, password)
}

// DialRCONContext connects and authenticates within ctx's deadline.
func DialRCONContext(ctx context.Context, addr, password string) (*RCON, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	r := &RCON{conn: conn, br: bufio.NewReader(conn), nextID: 1}
	stop := r.bound(ctx)
	defer stop()
	if _, err := r.write(r.nextID, rconAuth, password); err != nil {
		conn.Close()
		return nil, r.ctxErr(ctx, err)
	}
	for {
		id, typ, _, err := r.read()
		if err != nil {
			conn.Close()
			return nil, r.ctxErr(ctx, err)
		}
		if id == -1 {
			conn.Close()
			return nil, ErrRCONAuth
		}
		if typ == rconExec && id == r.nextID {
			break
		}
	}
	r.nextID++
	return r, nil
}

func (r *RCON) Close() error { return r.conn.Close() }

// Command runs one console command and returns the full (possibly
// multi-packet) reply. See CommandContext.
func (r *RCON) Command(cmd string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return r.CommandContext(ctx, cmd)
}

// CommandContext runs one console command within ctx's deadline and gives up
// as soon as ctx is cancelled. The command is written at most once; an error
// that does not wrap ErrNotSent means the server may have run it. A
// connection that failed this way is out of step and must be closed.
func (r *RCON) CommandContext(ctx context.Context, cmd string) (string, error) {
	if len(cmd) > maxRCONPayload {
		return "", notSent(fmt.Errorf("rcon: command longer than %d bytes", maxRCONPayload))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", notSent(err)
	}
	if err := r.stale(); err != nil {
		return "", notSent(err)
	}
	stop := r.bound(ctx)
	defer stop()
	id, marker := r.nextID, r.nextID+1
	r.nextID += 2
	// Paper closes the connection if a second packet arrives in the same read,
	// so requests are never pipelined. Replies longer than one chunk are
	// terminated by a marker request sent only after the first chunk arrived.
	if n, err := r.write(id, rconExec, cmd); err != nil {
		if n == 0 {
			return "", notSent(r.ctxErr(ctx, err))
		}
		return "", r.ctxErr(ctx, err)
	}
	var sb strings.Builder
	markerSent := false
	for {
		gotID, _, body, err := r.read()
		if err != nil {
			return "", r.ctxErr(ctx, err)
		}
		switch gotID {
		case id:
			if sb.Len()+len(body) > maxRCONReplySize {
				return "", errors.New("rcon: reply too large")
			}
			sb.WriteString(body)
			if !markerSent {
				if len(body) < rconChunk {
					return sb.String(), nil
				}
				if _, err := r.write(marker, rconMarkerType, ""); err != nil {
					return "", r.ctxErr(ctx, err)
				}
				markerSent = true
			}
		case marker:
			return sb.String(), nil
		}
	}
}

// bound applies ctx's deadline to the connection, and cuts it short when
// ctx is cancelled, until the returned function is called.
func (r *RCON) bound(ctx context.Context) (stop func()) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(rconDefaultTimeout)
	}
	_ = r.conn.SetDeadline(deadline)
	fired := make(chan struct{})
	cancel := context.AfterFunc(ctx, func() {
		_ = r.conn.SetDeadline(time.Now())
		close(fired)
	})
	return func() {
		if !cancel() {
			<-fired
		}
		_ = r.conn.SetDeadline(time.Time{})
	}
}

// ctxErr reports a cancelled or expired ctx instead of the I/O error it
// caused.
func (r *RCON) ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("rcon: no reply in time: %w", ctx.Err())
	}
	return err
}

// stale reports why an idle connection can't take a command: the server
// closed it, or sent something no command asked for.
func (r *RCON) stale() error {
	if r.br.Buffered() > 0 {
		return errors.New("rcon: the server sent data nobody asked for")
	}
	_ = r.conn.SetReadDeadline(time.Now().Add(rconProbe))
	_, err := r.br.Peek(1)
	_ = r.conn.SetReadDeadline(time.Time{})
	var ne net.Error
	switch {
	case err == nil:
		return errors.New("rcon: the server sent data nobody asked for")
	case errors.As(err, &ne) && ne.Timeout():
		return nil
	case errors.Is(err, io.EOF):
		return errors.New("rcon: the server closed the connection")
	default:
		return err
	}
}

func (r *RCON) write(id, typ int32, body string) (int, error) {
	n := 4 + 4 + len(body) + 2
	buf := make([]byte, 4+n)
	binary.LittleEndian.PutUint32(buf[0:], uint32(n))
	binary.LittleEndian.PutUint32(buf[4:], uint32(id))
	binary.LittleEndian.PutUint32(buf[8:], uint32(typ))
	copy(buf[12:], body)
	return r.conn.Write(buf)
}

func (r *RCON) read() (id, typ int32, body string, err error) {
	var hdr [4]byte
	if _, err = io.ReadFull(r.br, hdr[:]); err != nil {
		return
	}
	n := int32(binary.LittleEndian.Uint32(hdr[:]))
	if n < 10 || n > 1<<16 {
		err = fmt.Errorf("rcon: invalid packet length %d", n)
		return
	}
	buf := make([]byte, n)
	if _, err = io.ReadFull(r.br, buf); err != nil {
		return
	}
	id = int32(binary.LittleEndian.Uint32(buf[0:]))
	typ = int32(binary.LittleEndian.Uint32(buf[4:]))
	body = strings.TrimRight(string(buf[8:n-2]), "\x00")
	return
}

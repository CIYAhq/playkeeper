package minecraft

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
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
	// rconProbeWait is how long Exec listens for a hang-up before it
	// writes: a closed connection answers at once, a live one stays silent.
	rconProbeWait = time.Millisecond
)

var ErrRCONAuth = errors.New("rcon: authentication failed")

// ErrUnsent marks a failed command of which nothing was written: the
// connection was already closed, or time ran out, before it went out. Only
// such a command is safe to send again; any other failure may have come
// after the server ran it.
var ErrUnsent = errors.New("rcon: command not sent")

// RCON is a minimal client for the Minecraft remote console protocol. It is
// only ever dialled by the root agent over the private Docker bridge.
type RCON struct {
	mu     sync.Mutex
	conn   net.Conn
	br     *bufio.Reader
	nextID int32
}

func DialRCON(addr, password string, timeout time.Duration) (*RCON, error) {
	return DialRCONContext(context.Background(), addr, password, timeout)
}

// DialRCONContext is DialRCON giving up early when ctx ends.
func DialRCONContext(ctx context.Context, addr, password string, timeout time.Duration) (*RCON, error) {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	r := &RCON{conn: conn, br: bufio.NewReader(conn), nextID: 1}
	release := r.bound(ctx, timeout)
	defer release()
	if _, err := r.write(r.nextID, rconAuth, password); err != nil {
		conn.Close()
		return nil, err
	}
	for {
		id, typ, _, err := r.read()
		if err != nil {
			conn.Close()
			return nil, err
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

// Command runs one console command and returns the full (possibly multi-packet) reply.
func (r *RCON) Command(cmd string, timeout time.Duration) (string, error) {
	return r.Exec(context.Background(), cmd, timeout)
}

// Exec runs one console command and returns the full (possibly
// multi-packet) reply. It gives up after timeout or as soon as ctx is done,
// whichever comes first. Errors wrap ErrUnsent only when nothing was
// written; after any other error the connection is unusable and the
// command must not be sent again.
func (r *RCON) Exec(ctx context.Context, cmd string, timeout time.Duration) (string, error) {
	if len(cmd) > maxRCONPayload {
		return "", fmt.Errorf("rcon: command longer than %d bytes", maxRCONPayload)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnsent, err)
	}
	if err := r.probe(); err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnsent, err)
	}
	release := r.bound(ctx, timeout)
	defer release()
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnsent, err)
	}
	id, marker := r.nextID, r.nextID+1
	r.nextID += 2
	// Paper closes the connection if a second packet arrives in the same read,
	// so requests are never pipelined. Replies longer than one chunk are
	// terminated by a marker request sent only after the first chunk arrived.
	if n, err := r.write(id, rconExec, cmd); err != nil {
		if n == 0 {
			return "", fmt.Errorf("%w: %w", ErrUnsent, err)
		}
		return "", err
	}
	var sb strings.Builder
	markerSent := false
	for {
		gotID, _, body, err := r.read()
		if err != nil {
			return "", err
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
					return "", err
				}
				markerSent = true
			}
		case marker:
			return sb.String(), nil
		}
	}
}

// probe finds a connection the server has closed, or one holding bytes
// nobody asked for, without writing to it.
func (r *RCON) probe() error {
	if r.br.Buffered() > 0 {
		return errors.New("rcon: unexpected data on the connection")
	}
	_ = r.conn.SetReadDeadline(time.Now().Add(rconProbeWait))
	_, err := r.br.Peek(1)
	switch {
	case err == nil:
		return errors.New("rcon: unexpected data on the connection")
	case errors.Is(err, os.ErrDeadlineExceeded):
		return nil
	default:
		return err
	}
}

// bound limits the connection's reads and writes to timeout and to ctx's
// life. The returned func lifts the limit once the exchange is over.
func (r *RCON) bound(ctx context.Context, timeout time.Duration) (release func()) {
	_ = r.conn.SetDeadline(time.Now().Add(timeout))
	cancelled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = r.conn.SetDeadline(time.Unix(1, 0))
		close(cancelled)
	})
	return func() {
		if !stop() {
			<-cancelled
		}
		_ = r.conn.SetDeadline(time.Time{})
	}
}

// write sends one packet and returns how many of its bytes went out.
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

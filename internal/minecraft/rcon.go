package minecraft

import (
	"bufio"
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
)

var ErrRCONAuth = errors.New("rcon: authentication failed")

// RCON is a minimal client for the Minecraft remote console protocol. It is
// only ever dialled by the root agent over the private Docker bridge.
type RCON struct {
	mu     sync.Mutex
	conn   net.Conn
	br     *bufio.Reader
	nextID int32
}

func DialRCON(addr, password string, timeout time.Duration) (*RCON, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	r := &RCON{conn: conn, br: bufio.NewReader(conn), nextID: 1}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if err := r.write(r.nextID, rconAuth, password); err != nil {
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
	_ = conn.SetDeadline(time.Time{})
	return r, nil
}

func (r *RCON) Close() error { return r.conn.Close() }

// Command runs one console command and returns the full (possibly multi-packet) reply.
func (r *RCON) Command(cmd string, timeout time.Duration) (string, error) {
	if len(cmd) > maxRCONPayload {
		return "", fmt.Errorf("rcon: command longer than %d bytes", maxRCONPayload)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.conn.SetDeadline(time.Now().Add(timeout))
	defer func() { _ = r.conn.SetDeadline(time.Time{}) }()
	id, marker := r.nextID, r.nextID+1
	r.nextID += 2
	// Paper closes the connection if a second packet arrives in the same read,
	// so requests are never pipelined. Replies longer than one chunk are
	// terminated by a marker request sent only after the first chunk arrived.
	if err := r.write(id, rconExec, cmd); err != nil {
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
				if err := r.write(marker, rconMarkerType, ""); err != nil {
					return "", err
				}
				markerSent = true
			}
		case marker:
			return sb.String(), nil
		}
	}
}

func (r *RCON) write(id, typ int32, body string) error {
	n := 4 + 4 + len(body) + 2
	buf := make([]byte, 4+n)
	binary.LittleEndian.PutUint32(buf[0:], uint32(n))
	binary.LittleEndian.PutUint32(buf[4:], uint32(id))
	binary.LittleEndian.PutUint32(buf[8:], uint32(typ))
	copy(buf[12:], body)
	_, err := r.conn.Write(buf)
	return err
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

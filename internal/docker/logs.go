package docker

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"time"
)

// maxLineBytes bounds a single log line; longer lines are truncated so a
// misbehaving server cannot exhaust agent memory.
const maxLineBytes = 16 << 10

// LogLine is one line of container output with Docker's receive timestamp.
type LogLine struct {
	TS     time.Time
	Stream int // 1 stdout, 2 stderr
	Text   string
	Raw    string // timestamp + text, used for de-duplication
}

// LogScanner demultiplexes Docker's framed log stream into lines.
type LogScanner struct {
	rc      io.ReadCloser
	br      *bufio.Reader
	tty     bool
	pending [3]bytes.Buffer
	queue   []LogLine
	err     error
}

func NewLogScanner(rc io.ReadCloser, tty bool) *LogScanner {
	return &LogScanner{rc: rc, br: bufio.NewReaderSize(rc, 32<<10), tty: tty}
}

func (s *LogScanner) Close() error { return s.rc.Close() }

// Next returns the next complete line, or io.EOF when the stream ends.
func (s *LogScanner) Next() (LogLine, error) {
	for len(s.queue) == 0 {
		if s.err != nil {
			// Flush partial trailing lines once the stream is over.
			for i := range s.pending {
				if s.pending[i].Len() > 0 {
					s.emit(i, s.pending[i].String())
					s.pending[i].Reset()
				}
			}
			if len(s.queue) > 0 {
				break
			}
			return LogLine{}, s.err
		}
		s.fill()
	}
	l := s.queue[0]
	s.queue = s.queue[1:]
	return l, nil
}

func (s *LogScanner) fill() {
	if s.tty {
		chunk := make([]byte, 32<<10)
		n, err := s.br.Read(chunk)
		s.push(1, chunk[:n])
		if err != nil {
			s.err = err
		}
		return
	}
	var hdr [8]byte
	if _, err := io.ReadFull(s.br, hdr[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			err = io.EOF
		}
		s.err = err
		return
	}
	stream := int(hdr[0])
	if stream < 0 || stream > 2 {
		stream = 1
	}
	size := binary.BigEndian.Uint32(hdr[4:])
	payload := make([]byte, size)
	if _, err := io.ReadFull(s.br, payload); err != nil {
		s.err = io.EOF
		s.push(stream, payload)
		return
	}
	s.push(stream, payload)
}

func (s *LogScanner) push(stream int, b []byte) {
	buf := &s.pending[stream]
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			if buf.Len() < maxLineBytes {
				room := maxLineBytes - buf.Len()
				if len(b) > room {
					b = b[:room]
				}
				buf.Write(b)
			}
			return
		}
		if buf.Len() < maxLineBytes {
			part := b[:i]
			if room := maxLineBytes - buf.Len(); len(part) > room {
				part = part[:room]
			}
			buf.Write(part)
		}
		s.emit(stream, buf.String())
		buf.Reset()
		b = b[i+1:]
	}
}

func (s *LogScanner) emit(stream int, raw string) {
	raw = strings.TrimRight(raw, "\r")
	l := LogLine{Stream: stream, Raw: raw, Text: raw}
	if sp := strings.IndexByte(raw, ' '); sp > 0 {
		if ts, err := time.Parse(time.RFC3339Nano, raw[:sp]); err == nil {
			l.TS = ts
			l.Text = raw[sp+1:]
		}
	}
	s.queue = append(s.queue, l)
}

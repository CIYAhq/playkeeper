package docker

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"
)

func frame(stream byte, payload string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(payload)))
	return append(h, payload...)
}

func TestLogScannerDemuxesFramesAndSplitLines(t *testing.T) {
	var b bytes.Buffer
	b.Write(frame(1, "2026-09-24T13:35:18.245633689Z [13:35:18 INFO]: PkBot joi"))
	b.Write(frame(2, "2026-09-24T13:35:18.300000000Z warn line\n"))
	b.Write(frame(1, "ned the game\n2026-09-24T13:35:21.486576616Z [13:35:21 INFO]: PkBot left the game\n"))
	b.Write(frame(1, "2026-09-24T13:35:22.000000000Z trailing without newline"))
	s := NewLogScanner(io.NopCloser(&b), false)
	var got []LogLine
	for {
		l, err := s.Next()
		if err != nil {
			break
		}
		got = append(got, l)
	}
	if len(got) != 4 {
		t.Fatalf("got %d lines: %+v", len(got), got)
	}
	if got[0].Stream != 2 || got[0].Text != "warn line" {
		t.Fatalf("stderr line: %+v", got[0])
	}
	if got[1].Text != "[13:35:18 INFO]: PkBot joined the game" || got[1].TS.Nanosecond() != 245633689 {
		t.Fatalf("reassembled line: %+v", got[1])
	}
	if got[3].Text != "trailing without newline" {
		t.Fatalf("partial trailing line: %+v", got[3])
	}
	if !strings.HasPrefix(got[2].Raw, "2026-09-24T13:35:21.486576616Z ") {
		t.Fatalf("raw keeps the timestamp for de-duplication: %q", got[2].Raw)
	}
}

func TestLogScannerBoundsLineLength(t *testing.T) {
	long := strings.Repeat("A", 100_000)
	s := NewLogScanner(io.NopCloser(bytes.NewReader(frame(1, long+"\nshort\n"))), false)
	l, err := s.Next()
	if err != nil || len(l.Raw) > maxLineBytes {
		t.Fatalf("line not bounded: len %d err %v", len(l.Raw), err)
	}
	if l2, _ := s.Next(); l2.Raw != "short" {
		t.Fatalf("next line after truncation: %q", l2.Raw)
	}
}

func TestSplitRef(t *testing.T) {
	cases := map[string][2]string{
		"docker.io/itzg/minecraft-server@sha256:abc": {"docker.io/itzg/minecraft-server", "sha256:abc"},
		"localhost:5000/img:1.2":                     {"localhost:5000/img", "1.2"},
		"alpine":                                     {"alpine", "latest"},
	}
	for in, want := range cases {
		if r, tag := splitRef(in); r != want[0] || tag != want[1] {
			t.Errorf("splitRef(%q) = %q %q", in, r, tag)
		}
	}
}

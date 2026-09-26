package minecraft

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// rconPeer lets one RCON client in on a local port and hands the test the
// server's end of the connection, so it can hang up or stay silent.
func rconPeer(t *testing.T) (*RCON, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	peer := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(peer)
			return
		}
		id, _, _, err := readPacket(c)
		if err != nil {
			c.Close()
			close(peer)
			return
		}
		writePacket(c, id, rconExec, "")
		peer <- c
	}()
	r, err := DialRCON(ln.Addr().String(), "secret", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := <-peer
	if !ok {
		t.Fatal("the client never logged in")
	}
	t.Cleanup(func() { r.Close(); c.Close() })
	return r, c
}

func readPacket(c net.Conn) (id, typ int32, body string, err error) {
	var hdr [4]byte
	if _, err = io.ReadFull(c, hdr[:]); err != nil {
		return
	}
	buf := make([]byte, binary.LittleEndian.Uint32(hdr[:]))
	if _, err = io.ReadFull(c, buf); err != nil {
		return
	}
	return int32(binary.LittleEndian.Uint32(buf)), int32(binary.LittleEndian.Uint32(buf[4:])), string(bytes.TrimRight(buf[8:], "\x00")), nil
}

func writePacket(c net.Conn, id, typ int32, s string) {
	p := make([]byte, 14+len(s))
	binary.LittleEndian.PutUint32(p, uint32(10+len(s)))
	binary.LittleEndian.PutUint32(p[4:], uint32(id))
	binary.LittleEndian.PutUint32(p[8:], uint32(typ))
	copy(p[12:], s)
	c.Write(p)
}

func TestRCONCommandOnClosedConnectionIsNotSent(t *testing.T) {
	r, c := rconPeer(t)
	c.Close()
	time.Sleep(50 * time.Millisecond)
	_, err := r.Command("chunky confirm", time.Second)
	if !errors.Is(err, ErrNotSent) {
		t.Fatalf("a connection the server closed must fail before writing: %v", err)
	}
}

func TestRCONCommandAfterWriteMayHaveRun(t *testing.T) {
	r, c := rconPeer(t)
	got := make(chan string, 1)
	go func() {
		_, _, body, _ := readPacket(c)
		got <- body
		c.Close()
	}()
	_, err := r.Command("chunky confirm", time.Second)
	if err == nil || errors.Is(err, ErrNotSent) {
		t.Fatalf("a hang-up after the command went out must not look unsent: %v", err)
	}
	if body := <-got; body != "chunky confirm" {
		t.Fatalf("the server got %q", body)
	}
}

func TestRCONCommandContextHonoursTheDeadline(t *testing.T) {
	r, c := rconPeer(t)
	go io.Copy(io.Discard, c)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := r.CommandContext(ctx, "chunky start")
	if err == nil || errors.Is(err, ErrNotSent) {
		t.Fatalf("a silent server must time out after the write: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the command waited %v, past the context's deadline", d)
	}
}

func TestRCONCommandContextStopsWhenCancelled(t *testing.T) {
	r, c := rconPeer(t)
	go io.Copy(io.Discard, c)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	if _, err := r.CommandContext(ctx, "chunky start"); err == nil {
		t.Fatal("a cancelled command must fail")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the command waited %v after cancellation", d)
	}
	if _, err := r.CommandContext(ctx, "list"); !errors.Is(err, ErrNotSent) || !errors.Is(err, context.Canceled) {
		t.Fatalf("a command on a cancelled context must not be sent: %v", err)
	}
}

func TestRCONCommandContextKeepsWorkingAfterAnAnswer(t *testing.T) {
	r, c := rconPeer(t)
	go func() {
		for {
			id, _, body, err := readPacket(c)
			if err != nil {
				return
			}
			writePacket(c, id, 0, "ran: "+body)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < 3; i++ {
		if out, err := r.CommandContext(ctx, "list"); err != nil || out != "ran: list" {
			t.Fatalf("command %d: %q %v", i, out, err)
		}
	}
	cancel()
	time.Sleep(20 * time.Millisecond)
	if out, err := r.Command("tps", time.Second); err != nil || out != "ran: tps" {
		t.Fatalf("a finished context must not poison the next command: %q %v", out, err)
	}
}

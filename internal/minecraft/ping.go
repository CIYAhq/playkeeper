package minecraft

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// Status is the subset of the Server List Ping response Playkeeper uses.
type Status struct {
	VersionName string
	Protocol    int
	Online      int
	Max         int
	Sample      []string
}

// Ping performs a Java Edition Server List Ping against addr ("host:port").
// Success proves the game port accepts Minecraft protocol connections.
func Ping(addr string, timeout time.Duration) (Status, error) {
	var st Status
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return st, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return st, err
	}
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return st, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	var hs bytes.Buffer
	writeVarInt(&hs, 0x00)
	writeVarInt(&hs, -1)
	writeString(&hs, host)
	_ = binary.Write(&hs, binary.BigEndian, uint16(port))
	writeVarInt(&hs, 1)
	var out bytes.Buffer
	writeVarInt(&out, int32(hs.Len()))
	out.Write(hs.Bytes())
	out.Write([]byte{0x01, 0x00})
	if _, err := conn.Write(out.Bytes()); err != nil {
		return st, err
	}

	br := bufio.NewReader(conn)
	length, err := readVarInt(br)
	if err != nil {
		return st, err
	}
	if length <= 0 || length > 1<<20 {
		return st, fmt.Errorf("ping: invalid response length %d", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(br, payload); err != nil {
		return st, err
	}
	pr := bytes.NewReader(payload)
	id, err := readVarInt(pr)
	if err != nil || id != 0 {
		return st, errors.New("ping: unexpected packet")
	}
	strLen, err := readVarInt(pr)
	if err != nil || strLen < 0 || int(strLen) > pr.Len() {
		return st, errors.New("ping: bad status string")
	}
	raw := make([]byte, strLen)
	if _, err := io.ReadFull(pr, raw); err != nil {
		return st, err
	}
	var resp struct {
		Version struct {
			Name     string `json:"name"`
			Protocol int    `json:"protocol"`
		} `json:"version"`
		Players struct {
			Max    int `json:"max"`
			Online int `json:"online"`
			Sample []struct {
				Name string `json:"name"`
			} `json:"sample"`
		} `json:"players"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return st, fmt.Errorf("ping: %w", err)
	}
	st = Status{VersionName: resp.Version.Name, Protocol: resp.Version.Protocol, Online: resp.Players.Online, Max: resp.Players.Max}
	for _, s := range resp.Players.Sample {
		st.Sample = append(st.Sample, s.Name)
	}
	return st, nil
}

func writeVarInt(w *bytes.Buffer, v int32) {
	u := uint32(v)
	for {
		if u&^0x7F == 0 {
			w.WriteByte(byte(u))
			return
		}
		w.WriteByte(byte(u&0x7F | 0x80))
		u >>= 7
	}
}

func writeString(w *bytes.Buffer, s string) {
	writeVarInt(w, int32(len(s)))
	w.WriteString(s)
}

func readVarInt(r io.ByteReader) (int32, error) {
	var v uint32
	for i := 0; i < 5; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		v |= uint32(b&0x7F) << (7 * i)
		if b&0x80 == 0 {
			return int32(v), nil
		}
	}
	return 0, errors.New("varint too long")
}

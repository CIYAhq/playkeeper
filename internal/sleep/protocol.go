package sleep

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf16"
	"unicode/utf8"
)

// The stand-in speaks the plain start of the Minecraft Java Edition
// protocol, before compression and encryption: the handshake, the status
// exchange and the first login packet. Clients only send small packets
// there, so every read is capped far below what the protocol allows.
const (
	intentStatus   = 1
	intentLogin    = 2
	intentTransfer = 3

	maxHandshake   = 1 << 10 // id, protocol, a 255-character host, port, intent
	maxLoginStart  = 8 << 10 // 1.19 clients send about 4.6 KiB of key data after the name
	maxSmallPacket = 16      // status request and ping
	maxHostChars   = 255
	maxNameChars   = 16

	// maxStatusJSON is the longest status response clients accept, in
	// characters; counting bytes instead is stricter.
	maxStatusJSON = 32767
	// legacyProtocol is what current servers put in legacy ping replies:
	// newer than any client that sends one.
	legacyProtocol = 127

	sleepingText = "Sleeping, join to wake it up"
)

var errMalformed = errors.New("malformed packet")

type packet struct {
	id int32
	r  *bytes.Reader
}

// readVarInt reads a VarInt of at most maxBytes bytes.
func readVarInt(r io.ByteReader, maxBytes int) (int32, error) {
	var v uint32
	for i := range maxBytes {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		v |= uint32(b&0x7F) << (7 * i)
		if b&0x80 == 0 {
			return int32(v), nil
		}
	}
	return 0, errMalformed
}

// readFrame reads one packet: a length of at most three bytes, then the
// packet id and data. The length is checked against max before anything
// is allocated.
func readFrame(r *bufio.Reader, max int) (packet, error) {
	n, err := readVarInt(r, 3)
	if err != nil {
		return packet{}, err
	}
	if n < 1 || int(n) > max {
		return packet{}, errMalformed
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return packet{}, err
	}
	p := packet{r: bytes.NewReader(b)}
	if p.id, err = p.varInt(); err != nil {
		return packet{}, err
	}
	return p, nil
}

func (p *packet) varInt() (int32, error) {
	v, err := readVarInt(p.r, 5)
	if err != nil {
		return 0, errMalformed
	}
	return v, nil
}

// string reads a string of at most maxChars UTF-16 code units, which is
// how Java counts, so of at most three bytes per unit.
func (p *packet) string(maxChars int) (string, error) {
	n, err := p.varInt()
	if err != nil {
		return "", err
	}
	if n < 0 || int(n) > 3*maxChars || int(n) > p.r.Len() {
		return "", errMalformed
	}
	b := make([]byte, n)
	_, _ = io.ReadFull(p.r, b)
	if !utf8.Valid(b) || utf16Len(b) > maxChars {
		return "", errMalformed
	}
	return string(b), nil
}

func utf16Len(b []byte) int {
	n := 0
	for _, r := range string(b) {
		n += utf16.RuneLen(r)
	}
	return n
}

type handshake struct {
	protocol int32
	intent   int32
}

func parseHandshake(p packet) (handshake, error) {
	var h handshake
	var err error
	if p.id != 0x00 {
		return h, errMalformed
	}
	if h.protocol, err = p.varInt(); err != nil {
		return h, err
	}
	if _, err = p.string(maxHostChars); err != nil {
		return h, err
	}
	var port [2]byte
	if _, err = io.ReadFull(p.r, port[:]); err != nil {
		return h, errMalformed
	}
	if h.intent, err = p.varInt(); err != nil {
		return h, err
	}
	if p.r.Len() != 0 {
		return h, errMalformed
	}
	return h, nil
}

// parseLoginStart returns the name a joining client claims. What follows
// it, the player's UUID and, from 1.19 clients, key data, is not needed.
func parseLoginStart(p packet) (string, error) {
	if p.id != 0x00 {
		return "", errMalformed
	}
	return p.string(maxNameChars)
}

func appendVarInt(b []byte, v int32) []byte {
	u := uint32(v)
	for u >= 0x80 {
		b = append(b, byte(u)|0x80)
		u >>= 7
	}
	return append(b, byte(u))
}

func appendString(b []byte, s string) []byte {
	return append(appendVarInt(b, int32(len(s))), s...)
}

// frame builds a packet: its length, then the id and data.
func frame(id int32, data []byte) []byte {
	body := append(appendVarInt(nil, id), data...)
	return append(appendVarInt(make([]byte, 0, len(body)+5), int32(len(body))), body...)
}

type textComponent struct {
	Text  string          `json:"text"`
	Color string          `json:"color,omitempty"`
	Extra []textComponent `json:"extra,omitempty"`
}

// statusJSON is the server list entry. It reports the client's own
// protocol, so the entry never looks incompatible and the player tries to
// join; clients that send none (pingers send -1) get the server's.
func statusJSON(st Status, protocol int32) string {
	if protocol <= 0 && st.Protocol > 0 {
		protocol = int32(st.Protocol)
	}
	desc := textComponent{Text: sleepingText, Color: "gray"}
	if st.Name != "" {
		desc = textComponent{Text: st.Name + "\n", Extra: []textComponent{desc}}
	}
	type version struct {
		Name     string `json:"name"`
		Protocol int32  `json:"protocol"`
	}
	type players struct {
		Max    int `json:"max"`
		Online int `json:"online"`
	}
	v := struct {
		Version     version       `json:"version"`
		Players     players       `json:"players"`
		Description textComponent `json:"description"`
		Favicon     string        `json:"favicon,omitempty"`
	}{version{st.Version, protocol}, players{Max: st.MaxPlayers}, desc, st.Icon}
	b, _ := json.Marshal(v)
	if len(b) > maxStatusJSON {
		v.Favicon = ""
		b, _ = json.Marshal(v)
	}
	return string(b)
}

// disconnectJSON is the reason in a login disconnect packet, which is JSON
// in every version, unlike disconnects later in the connection.
func disconnectJSON(msg string) string {
	b, _ := json.Marshal(textComponent{Text: msg})
	return string(b)
}

// legacyStatus is the server list entry for clients older than 1.7: beta
// 1.8 to 1.3 read "motd§online§max", 1.4 to 1.6 a string that starts with
// "§1" and separates its fields with NUL. Status text never contains either.
func legacyStatus(st Status, beta bool) string {
	motd := sleepingText
	if st.Name != "" {
		motd = st.Name + " - " + sleepingText
	}
	if beta {
		return fmt.Sprintf("%s§0§%d", motd, st.MaxPlayers)
	}
	return fmt.Sprintf("§1\x00%d\x00%s\x00%s\x000\x00%d", legacyProtocol, st.Version, motd, st.MaxPlayers)
}

// legacyKick encodes a legacy reply: a kick packet whose reason is the
// status, in UTF-16 with its length in code units.
func legacyKick(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, 3+2*len(u))
	b = append(b, 0xFF)
	b = binary.BigEndian.AppendUint16(b, uint16(len(u)))
	for _, c := range u {
		b = binary.BigEndian.AppendUint16(b, c)
	}
	return b
}

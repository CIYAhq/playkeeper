package packs

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

const (
	maxPromptRunes = 200
	maxURLBytes    = 2048
)

// Store keeps resource packs in a folder, each named by its SHA-1 hash, for
// NewHandler to serve.
type Store struct {
	// Dir is the folder, created when missing. It and its files are
	// readable by all users, so that the unprivileged panel can serve them.
	Dir string
	// Limits bound the packs Put accepts.
	Limits Limits
}

// Put checks a resource pack with Inspect and stores it. Storing a pack the
// store already holds only returns its Info.
func (s Store) Put(ctx context.Context, src io.ReaderAt, size int64) (Info, error) {
	info, err := Inspect(ctx, src, size, Resource, s.Limits)
	if err != nil {
		return Info{}, err
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return Info{}, fileFailed("create the resource pack folder", err)
	}
	// The agent runs with umask 077, which would keep the panel out.
	if err := os.Chmod(s.Dir, 0o755); err != nil {
		return Info{}, fileFailed("make the resource pack folder readable", err)
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return Info{}, fileFailed("open the resource pack folder", err)
	}
	defer root.Close()
	name := info.SHA1 + ".zip"
	if st, err := root.Lstat(name); err == nil && st.Mode().IsRegular() && st.Size() == info.Size {
		return info, nil
	}
	err = writeAtomic(root, name, func(w io.Writer) error {
		h := sha1.New()
		if _, err := io.Copy(io.MultiWriter(w, h), ctxReader{ctx, io.NewSectionReader(src, 0, size)}); err != nil {
			return err
		}
		if hex.EncodeToString(h.Sum(nil)) != info.SHA1 {
			return errors.New("the pack changed while it was being copied")
		}
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return Info{}, ctx.Err()
		}
		return Info{}, fileFailed("store the resource pack", err)
	}
	return info, nil
}

// writeAtomic replaces name inside root with what write writes: it writes a
// new file next to it, makes it readable by the panel, syncs it and renames
// it over the old one.
func writeAtomic(root *os.Root, name string, write func(io.Writer) error) error {
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	tmp := path.Join(path.Dir(name), "."+path.Base(name)+".playkeeper-"+hex.EncodeToString(rnd[:]))
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			root.Remove(tmp)
		}
	}()
	if err := write(f); err != nil {
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := root.Rename(tmp, name); err != nil {
		return err
	}
	ok = true
	return nil
}

// Remove deletes the pack whose SHA-1 hash is sum. Removing a pack the
// store doesn't hold succeeds.
func (s Store) Remove(sum string) error {
	if !validSHA1(sum) {
		return invalidSHA1(sum)
	}
	root, err := os.OpenRoot(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fileFailed("open the resource pack folder", err)
	}
	defer root.Close()
	if err := root.Remove(sum + ".zip"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fileFailed("remove the resource pack", err)
	}
	return nil
}

// open opens the stored pack whose SHA-1 hash is sum. Opening without
// blocking and checking the type afterwards keeps a FIFO or device planted
// in the folder from hanging or misleading the handler.
func (s Store) open(sum string) (*os.File, fs.FileInfo, error) {
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	f, err := root.OpenFile(sum+".zip", os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err == nil && !st.Mode().IsRegular() {
		err = errors.New("not a regular file")
	}
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, st, nil
}

// Offer is the resource pack a server offers players when they join.
type Offer struct {
	// URL is where players' games download the pack; see Origin.PackURL.
	URL string
	// SHA1 is the pack's SHA-1 hash, which players' games check the
	// download against.
	SHA1 string
	// Required disconnects players who decline the pack.
	Required bool
	// Prompt is an optional message of at most 200 characters, shown when
	// players are asked to accept the pack.
	Prompt string
}

// Setting is a server setting: a key in server.properties and the
// environment variable of the itzg/minecraft-server image that sets it.
type Setting struct {
	Property string `json:"property"`
	Env      string `json:"env"`
	Value    string `json:"value"`
}

// Settings are the server settings that offer the pack. The image expands
// "%NAME%" in values to environment variables, which could put the server's
// secrets in a URL every player fetches, so values never contain "%".
func (o Offer) Settings() ([]Setting, error) {
	if err := checkPackURL(o.URL); err != nil {
		return nil, err
	}
	if !validSHA1(o.SHA1) {
		return nil, invalidSHA1(o.SHA1)
	}
	prompt := ""
	if o.Prompt != "" {
		if err := checkPrompt(o.Prompt); err != nil {
			return nil, err
		}
		// The game reads the prompt as a text component, of which a JSON
		// string is the plain kind.
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(o.Prompt); err != nil {
			return nil, err
		}
		prompt = strings.TrimSuffix(b.String(), "\n")
	}
	return []Setting{
		{"resource-pack", "RESOURCE_PACK", o.URL},
		{"resource-pack-sha1", "RESOURCE_PACK_SHA1", o.SHA1},
		{"require-resource-pack", "RESOURCE_PACK_ENFORCE", strconv.FormatBool(o.Required)},
		{"resource-pack-prompt", "RESOURCE_PACK_PROMPT", prompt},
		{"resource-pack-id", "RESOURCE_PACK_ID", ResourcePackID(o.SHA1)},
	}, nil
}

// ClearSettings are the settings that stop offering a resource pack. The
// image leaves a property alone when its variable is unset, so each is set
// to empty instead.
func ClearSettings() []Setting {
	return []Setting{
		{"resource-pack", "RESOURCE_PACK", ""},
		{"resource-pack-sha1", "RESOURCE_PACK_SHA1", ""},
		{"require-resource-pack", "RESOURCE_PACK_ENFORCE", ""},
		{"resource-pack-prompt", "RESOURCE_PACK_PROMPT", ""},
		{"resource-pack-id", "RESOURCE_PACK_ID", ""},
	}
}

// ResourcePackID is the ID a server gives the resource pack whose SHA-1
// hash is sum: a UUID derived from the hash, so players' games recognize a
// pack they already downloaded, and download a changed pack again.
func ResourcePackID(sum string) string {
	urlNamespace := [16]byte{0x6b, 0xa7, 0xb8, 0x11, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8}
	id := uuidV5(uuidV5(urlNamespace, "https://playkeeper.io/ns/resource-pack"), sum)
	u := hex.EncodeToString(id[:])
	return u[:8] + "-" + u[8:12] + "-" + u[12:16] + "-" + u[16:20] + "-" + u[20:]
}

// uuidV5 is the name-based UUID of name in namespace ns (RFC 9562).
func uuidV5(ns [16]byte, name string) [16]byte {
	h := sha1.New()
	h.Write(ns[:])
	h.Write([]byte(name))
	var u [16]byte
	copy(u[:], h.Sum(nil))
	u[6] = u[6]&0x0f | 0x50
	u[8] = u[8]&0x3f | 0x80
	return u
}

// Origin is where players' games reach the panel.
type Origin struct {
	// Host is a DNS name or an IP address.
	Host string
	Port int
	// HTTPS is set when players' games trust the panel's certificate, as
	// they do one from Let's Encrypt. They refuse self-signed certificates,
	// so otherwise packs are downloaded over plain HTTP.
	HTTPS bool
}

// PackURL is the URL from which players' games download the stored pack
// whose SHA-1 hash is sum. The host must be one players can reach:
// loopback, link-local, multicast and unspecified addresses, and
// localhost, are refused.
func (o Origin) PackURL(sum string) (string, error) {
	if !validSHA1(sum) {
		return "", invalidSHA1(sum)
	}
	host, err := publicHost(o.Host)
	if err != nil {
		return "", err
	}
	if o.Port < 1 || o.Port > 65535 {
		return "", &Error{
			Code:   CodeInvalidHost,
			Params: map[string]any{"port": o.Port, "problem": "port"},
			Msg:    fmt.Sprintf("%d isn't a valid port number.", o.Port),
		}
	}
	scheme, defaultPort := "http", 80
	if o.HTTPS {
		scheme, defaultPort = "https", 443
	}
	hostport := net.JoinHostPort(host, strconv.Itoa(o.Port))
	if o.Port == defaultPort {
		hostport = strings.TrimSuffix(hostport, ":"+strconv.Itoa(o.Port))
	}
	return scheme + "://" + hostport + PackPath(sum), nil
}

// publicHost checks that players could reach host and returns it in
// canonical form.
func publicHost(host string) (string, error) {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	bad := func(problem string) error {
		msg := fmt.Sprintf("%s isn't a valid host name or IP address.", shortQuote(host))
		hint := "Enter the server's public domain name or IP address."
		switch problem {
		case "local":
			msg = fmt.Sprintf("%s only reaches the machine it is used on, so players' games couldn't download the pack from it.", shortQuote(host))
		case "unreachable":
			msg = fmt.Sprintf("%s isn't an address players' games can download the pack from.", shortQuote(host))
		}
		return &Error{
			Code:   CodeInvalidHost,
			Params: map[string]any{"host": shortName(host), "problem": problem},
			Msg:    msg,
			Hint:   hint,
		}
	}
	if a, err := netip.ParseAddr(h); err == nil {
		a = a.Unmap()
		switch {
		case a.Zone() != "":
			return "", bad("malformed")
		case a.IsLoopback():
			return "", bad("local")
		case a.IsUnspecified() || a.IsMulticast() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
			a.IsInterfaceLocalMulticast() || a == netip.AddrFrom4([4]byte{255, 255, 255, 255}):
			return "", bad("unreachable")
		}
		return a.String(), nil
	}
	if !validDNSName(h) {
		return "", bad("malformed")
	}
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return "", bad("local")
	}
	return h, nil
}

// validDNSName accepts a lowercase host name. A name ending in a number is
// refused: games read forms like "127.1" as IP addresses.
func validDNSName(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	labels := strings.Split(h, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			if c := l[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	last := labels[len(labels)-1]
	return strings.Trim(last, "0123456789") != ""
}

// checkPackURL checks a pack URL for the settings: an absolute http or
// https URL without credentials, in printable ASCII without "%" or "\".
func checkPackURL(raw string) error {
	ok := raw != "" && len(raw) <= maxURLBytes && !strings.ContainsAny(raw, "%\\ \"<>`") &&
		!strings.ContainsFunc(raw, func(r rune) bool { return r < 0x21 || r > 0x7e })
	if ok {
		u, err := url.Parse(raw)
		ok = err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && u.Opaque == ""
	}
	if ok {
		return nil
	}
	return &Error{
		Code:   CodeInvalidOffer,
		Params: map[string]any{"field": "url"},
		Msg:    fmt.Sprintf("%s isn't a resource pack URL a server can offer.", shortQuote(raw)),
		Hint:   "Use an http:// or https:// address of at most 2,048 characters, without spaces, percent signs or backslashes.",
	}
}

// checkPrompt checks a resource pack prompt: at most 200 characters on one
// line, without "%" or "\".
func checkPrompt(p string) error {
	if utf8.ValidString(p) && utf8.RuneCountInString(p) <= maxPromptRunes && !strings.ContainsAny(p, `%\`) &&
		!strings.ContainsFunc(p, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' }) {
		return nil
	}
	return &Error{
		Code:   CodeInvalidPrompt,
		Params: map[string]any{"max": maxPromptRunes},
		Msg:    fmt.Sprintf("The message shown to players must be one line of at most %d characters, without percent signs or backslashes.", maxPromptRunes),
	}
}

func validSHA1(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func invalidSHA1(value string) *Error {
	return &Error{
		Code:   CodeInvalidOffer,
		Params: map[string]any{"field": "sha1"},
		Msg:    fmt.Sprintf("%s isn't a SHA-1 hash: 40 lowercase hexadecimal characters.", shortQuote(value)),
	}
}

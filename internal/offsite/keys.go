package offsite

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"filippo.io/age"
)

const (
	maxRecoveryFile = 1 << 20
	maxRecoveryKeys = 1000
)

// Identity is one of a server's encryption keys, an age X25519 key.
// Secret is the private key ("AGE-SECRET-KEY-1…"), which opens copies and
// is stored in agent.db; Recipient is the public key ("age1…"), which
// copies are encrypted to and which is safe to show.
type Identity struct {
	Secret    Secret    `json:"-"`
	Recipient string    `json:"recipient"`
	CreatedAt time.Time `json:"createdAt"`
}

// NewIdentity makes a new random key.
func NewIdentity(now time.Time) (Identity, error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return Identity{}, &Error{Kind: KindUnexpected, Op: opSetup, Err: err,
			Msg: "Playkeeper couldn't make an encryption key.", Hint: "Try again."}
	}
	return Identity{Secret: NewSecret(id.String()), Recipient: id.Recipient().String(), CreatedAt: now.UTC()}, nil
}

// ParseIdentity reads a stored key back.
func ParseIdentity(secret Secret, createdAt time.Time) (Identity, error) {
	id, err := parseX25519(secret.Reveal())
	if err != nil {
		return Identity{}, err
	}
	return Identity{Secret: secret, Recipient: id.Recipient().String(), CreatedAt: createdAt.UTC()}, nil
}

// parseX25519 never returns age's error, which may quote the key.
func parseX25519(s string) (*age.X25519Identity, error) {
	if len(s) > 128 || !strings.HasPrefix(s, "AGE-SECRET-KEY-1") {
		return nil, badKey()
	}
	id, err := age.ParseX25519Identity(s)
	if err != nil {
		return nil, badKey()
	}
	return id, nil
}

func badKey() *Error {
	return invalid("encryptionKey", "A stored encryption key is not an age key Playkeeper can use.",
		"Copies made with it can still be opened with the recovery key file. Restore the keys from that file.")
}

// Keys are one server's encryption keys: Current, which new copies are
// encrypted to, and the Old ones earlier rotations kept, newest first, so
// the copies made with them still open.
type Keys struct {
	Current Identity   `json:"current"`
	Old     []Identity `json:"old,omitempty"`
}

// NewKeys makes a server's first key.
func NewKeys(now time.Time) (Keys, error) {
	id, err := NewIdentity(now)
	if err != nil {
		return Keys{}, err
	}
	return Keys{Current: id}, nil
}

// Validate checks that every key is an age key that matches its public key.
func (k Keys) Validate() error {
	_, err := k.identities()
	return err
}

func (k Keys) all() []Identity { return append([]Identity{k.Current}, k.Old...) }

// Has reports whether recipient is one of k's public keys.
func (k Keys) Has(recipient string) bool {
	for _, id := range k.all() {
		if recipient != "" && id.Recipient == recipient {
			return true
		}
	}
	return false
}

// keyed is a parsed key with its public key, to tell which key opened a copy.
type keyed struct {
	id        *age.X25519Identity
	recipient string
}

func (k Keys) identities() ([]keyed, error) {
	var out []keyed
	for _, i := range k.all() {
		id, err := parseX25519(i.Secret.Reveal())
		if err != nil {
			return nil, err
		}
		if id.Recipient().String() != i.Recipient {
			return nil, invalid("encryptionKey", "A stored encryption key doesn't match its public key.",
				"Restore the keys from the recovery key file.")
		}
		out = append(out, keyed{id: id, recipient: i.Recipient})
	}
	return out, nil
}

// tracked is a key that notes its public key in hit when it opens a copy.
type tracked struct {
	keyed
	hit *string
}

func (t tracked) Unwrap(stanzas []*age.Stanza) ([]byte, error) {
	key, err := t.id.Unwrap(stanzas)
	if err == nil {
		*t.hit = t.recipient
	}
	return key, err
}

// Rotation explains a key rotation, for the dashboard.
type Rotation struct {
	Recipient    string            `json:"recipient"`    // the new key, for new copies
	OldRecipient string            `json:"oldRecipient"` // the key it replaced, kept for older copies
	OldKeys      int               `json:"oldKeys"`      // how many old keys are kept
	Code         string            `json:"code"`         // "key_rotated"
	Params       map[string]string `json:"params"`
	Msg          string            `json:"msg"`
	Hint         string            `json:"hint"`
}

// Rotate makes a new current key and keeps the others, so copies already
// made still open. It returns the keys to store and what to tell the
// user, who needs to download the recovery key file again: the old file
// doesn't open new copies.
func (k Keys) Rotate(now time.Time) (Keys, Rotation, error) {
	if err := k.Validate(); err != nil {
		return Keys{}, Rotation{}, err
	}
	id, err := NewIdentity(now)
	if err != nil {
		return Keys{}, Rotation{}, err
	}
	next := Keys{Current: id, Old: k.all()}
	r := Rotation{
		Recipient: id.Recipient, OldRecipient: k.Current.Recipient, OldKeys: len(next.Old), Code: "key_rotated",
		Params: map[string]string{"recipient": id.Recipient, "oldRecipient": k.Current.Recipient, "oldKeys": strconv.Itoa(len(next.Old))},
		Msg:    "New copies are encrypted with a new key. Copies made before stay readable, because Playkeeper keeps the old key.",
		Hint: "Download the recovery key file again: the old one doesn't open new copies. If the old file was lost or someone else saw it, " +
			"copy your backups again and then delete the older copies, so none depends on the old key.",
	}
	return next, r, nil
}

// RecoveryFile is the text file the user downloads to keep a server's
// keys somewhere else. It is also an age identity file, so the age tool
// opens copies with it. Content holds the private keys.
type RecoveryFile struct {
	Name    string `json:"name"`
	Content Secret `json:"-"`
}

// RecoveryFile writes k as the recovery key file of the server named
// server, with plain instructions.
func (k Keys) RecoveryFile(server string, now time.Time) (RecoveryFile, error) {
	return k.RecoveryFileFor(server, "", now)
}

// RecoveryFileFor is RecoveryFile that also notes folder, the bucket
// prefix or SFTP folder the copies go to, so a new machine finds them.
func (k Keys) RecoveryFileFor(server, folder string, now time.Time) (RecoveryFile, error) {
	if err := k.Validate(); err != nil {
		return RecoveryFile{}, err
	}
	name := "playkeeper-recovery-key-" + slug(server) + ".txt"
	example := "playkeeper-world-20260925-120000-a1b2c3.tar.gz"
	var b strings.Builder
	line := func(s ...string) {
		for _, l := range s {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	line("# Playkeeper recovery key for "+displayName(server),
		"#",
		"# This file opens the copies of this server's backups that Playkeeper keeps",
		"# somewhere else. The copies are encrypted: if this machine is lost, they",
		"# can't be opened without this file.",
		"#",
		"# Keep it private, and keep it somewhere other than this machine, for example",
		"# in a password manager. Anyone who has this file and the copies can open them.",
		"#",
		"# To restore with Playkeeper: on the new machine, choose \"Restore from a",
		"# recovery key\", give it this file and the storage or SFTP settings, and pick",
		"# a copy.",
		"#",
		"# To restore with the age tool (https://age-encryption.org), download a copy",
		"# (a file ending in .tar.gz.age) and run, for example:",
		"#",
		"#   age --decrypt --identity "+name+" --output "+example+" "+CopyName(example),
		"#",
		"# then unpack the .tar.gz file into the server's folder.",
		"#",
		"# Made "+now.UTC().Format("2006-01-02 15:04")+" UTC. The newest key comes first; older keys open copies",
		"# made before the key was changed.")
	if folder != "" && len(folder) <= 256 && printable(folder) {
		line("#", "# folder: "+folder)
	}
	for _, id := range k.all() {
		line("")
		if !id.CreatedAt.IsZero() {
			line("# created: " + id.CreatedAt.UTC().Format(time.RFC3339))
		}
		line("# public key: "+id.Recipient, id.Secret.Reveal())
	}
	return RecoveryFile{Name: name, Content: NewSecret(b.String())}, nil
}

// ParseRecoveryFile reads the keys back from a recovery key file, the
// first as Current. Its errors never quote the file.
func ParseRecoveryFile(r io.Reader) (Keys, error) {
	rec, err := ReadRecoveryFile(r)
	return rec.Keys, err
}

// Recovery is what a recovery key file holds: the keys, and what it says
// about the server they belong to.
type Recovery struct {
	Keys   Keys
	Server string    // the server's name, from the file's first line
	Folder string    // the bucket prefix or SFTP folder of its copies, if the file says
	Made   time.Time // when the file was made, if it says
}

// ReadRecoveryFile is ParseRecoveryFile with the rest of what the file
// says. Its errors never quote the file.
func ReadRecoveryFile(r io.Reader) (Recovery, error) {
	var rec Recovery
	fail := func(msg string) (Recovery, error) {
		return Recovery{}, invalid("recoveryKey", msg, "Use the recovery key file Playkeeper made for this server, unchanged.")
	}
	data, err := io.ReadAll(io.LimitReader(r, maxRecoveryFile+1))
	switch {
	case err != nil:
		return fail("Playkeeper couldn't read the recovery key file.")
	case len(data) > maxRecoveryFile:
		return fail("The file is too large to be a recovery key file.")
	case !utf8.Valid(data):
		return fail("The file isn't a recovery key file: it isn't plain text.")
	}
	var (
		keys    []Identity
		seen    = map[string]bool{}
		created time.Time
	)
	for i, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if c, ok := strings.CutPrefix(l, "#"); ok {
			c = strings.TrimSpace(c)
			if v, ok := strings.CutPrefix(c, "created:"); ok {
				created, _ = time.Parse(time.RFC3339, strings.TrimSpace(v))
			} else if v, ok := strings.CutPrefix(c, "Playkeeper recovery key for "); ok && rec.Server == "" && len(keys) == 0 {
				rec.Server = displayName(v)
			} else if v, ok := strings.CutPrefix(c, "folder:"); ok && len(keys) == 0 {
				if v = strings.TrimSpace(v); len(v) <= 256 && printable(v) {
					rec.Folder = v
				}
			} else if v, ok := strings.CutPrefix(c, "Made "); ok && len(v) >= 16 && len(keys) == 0 {
				rec.Made, _ = time.Parse("2006-01-02 15:04", v[:16])
			}
			continue
		}
		id, err := parseX25519(l)
		if err != nil {
			return fail(fmt.Sprintf("Line %d of the file isn't an age key Playkeeper can use.", i+1))
		}
		rcpt := id.Recipient().String()
		if !seen[rcpt] {
			seen[rcpt] = true
			keys = append(keys, Identity{Secret: NewSecret(l), Recipient: rcpt, CreatedAt: created.UTC()})
		}
		created = time.Time{}
		if len(keys) > maxRecoveryKeys {
			return fail("The file has more keys than a recovery key file can.")
		}
	}
	if len(keys) == 0 {
		return fail("The file has no age keys in it.")
	}
	rec.Keys = Keys{Current: keys[0], Old: keys[1:]}
	return rec, nil
}

// slug makes a server's name safe for a file name or a key comment:
// lowercase letters, digits and single hyphens, at most 40 characters.
func slug(name string) string {
	var b strings.Builder
	gap := false
	for _, r := range strings.ToLower(name) {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			gap = true
			continue
		}
		if gap && b.Len() > 0 {
			b.WriteByte('-')
		}
		gap = false
		b.WriteRune(r)
	}
	s := b.String()
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	if s == "" {
		return "server"
	}
	return s
}

// displayName makes a server's name safe for a comment line of the
// recovery file: printable, on one line, at most 64 characters.
func displayName(name string) string {
	s := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return ' '
		}
		return r
	}, name)), " ")
	if r := []rune(s); len(r) > 64 {
		s = strings.TrimSpace(string(r[:64]))
	}
	if s == "" {
		return "this server"
	}
	return s
}

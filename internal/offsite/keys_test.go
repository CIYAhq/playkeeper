package offsite

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

var keyTime = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func newKeys(t *testing.T) Keys {
	t.Helper()
	k, err := NewKeys(keyTime)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// sealWith encrypts data to recipient with the age library.
func sealWith(t *testing.T, data []byte, recipient string) []byte {
	t.Helper()
	r, err := age.ParseX25519Recipient(recipient)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	w, err := age.Encrypt(&out, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// openWith decrypts data with the age library and the given identities.
func openWith(t *testing.T, data []byte, ids ...Identity) ([]byte, error) {
	t.Helper()
	var list []age.Identity
	for _, i := range ids {
		id, err := age.ParseX25519Identity(i.Secret.Reveal())
		if err != nil {
			t.Fatal(err)
		}
		list = append(list, id)
	}
	r, err := age.Decrypt(bytes.NewReader(data), list...)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func TestNewKeys(t *testing.T) {
	k := newKeys(t)
	id := k.Current
	if !strings.HasPrefix(id.Secret.Reveal(), "AGE-SECRET-KEY-1") || !strings.HasPrefix(id.Recipient, "age1") || !id.CreatedAt.Equal(keyTime) || len(k.Old) != 0 {
		t.Fatalf("keys %+v", k)
	}
	if err := k.Validate(); err != nil {
		t.Fatal(err)
	}
	if other := newKeys(t); other.Current.Recipient == id.Recipient {
		t.Error("two new keys are the same")
	}

	got, err := ParseIdentity(id.Secret, keyTime.In(time.FixedZone("CEST", 2*3600)))
	if err != nil || got.Recipient != id.Recipient || got.CreatedAt.Location() != time.UTC {
		t.Fatalf("ParseIdentity = %+v, %v", got, err)
	}
	plain := []byte("a world's backup")
	back, err := openWith(t, sealWith(t, plain, id.Recipient), got)
	if err != nil || !bytes.Equal(back, plain) {
		t.Fatalf("round trip = %q, %v", back, err)
	}
}

func TestParseIdentityRefusesBadKeys(t *testing.T) {
	good := newKeys(t).Current.Secret.Reveal()
	for name, s := range map[string]string{
		"empty":         "",
		"public key":    newKeys(t).Current.Recipient,
		"lower case":    strings.ToLower(good),
		"truncated":     good[:len(good)-4],
		"bad checksum":  good[:len(good)-1] + map[bool]string{true: "Q", false: "P"}[good[len(good)-1] == 'P'],
		"too long":      good + strings.Repeat("Q", 100),
		"post-quantum":  "AGE-SECRET-KEY-PQ-1" + good[len("AGE-SECRET-KEY-1"):],
		"with a space":  " " + good,
		"ssh key":       "-----BEGIN OPENSSH PRIVATE KEY-----",
		"plugin":        "AGE-PLUGIN-YUBIKEY-1QQQQQQ",
		"line break in": good[:20] + "\n" + good[20:],
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseIdentity(NewSecret(s), keyTime)
			e := wantKind(t, err, KindInvalidConfig)
			if e.Field != "encryptionKey" || e.Hint == "" || e.Err != nil {
				t.Errorf("error %+v", e)
			}
			if s != "" && strings.Contains(e.Msg+e.Hint+e.Error(), s) {
				t.Error("the error quotes the key")
			}
		})
	}
}

func TestKeysValidate(t *testing.T) {
	a, b := newKeys(t), newKeys(t)
	mixed := Keys{Current: Identity{Secret: a.Current.Secret, Recipient: b.Current.Recipient}}
	e := wantKind(t, mixed.Validate(), KindInvalidConfig)
	if e.Msg != "A stored encryption key doesn't match its public key." || e.Field != "encryptionKey" {
		t.Errorf("error %+v", e)
	}
	withBadOld := Keys{Current: a.Current, Old: []Identity{{Secret: NewSecret("AGE-SECRET-KEY-1NOPE"), Recipient: "age1nope"}}}
	wantKind(t, withBadOld.Validate(), KindInvalidConfig)
	if (Keys{}).Validate() == nil {
		t.Error("keys without a key are valid")
	}
	if !a.Has(a.Current.Recipient) || a.Has(b.Current.Recipient) || a.Has("") {
		t.Error("Has is wrong")
	}
}

func TestRotate(t *testing.T) {
	first := newKeys(t)
	second, r1, err := first.Rotate(keyTime.Add(24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	third, r2, err := second.Rotate(keyTime.Add(48 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if third.Current.Recipient == second.Current.Recipient || len(third.Old) != 2 ||
		third.Old[0].Recipient != second.Current.Recipient || third.Old[1].Recipient != first.Current.Recipient {
		t.Fatalf("after two rotations: %+v", third)
	}
	if !third.Current.CreatedAt.Equal(keyTime.Add(48*time.Hour)) || !third.Old[1].CreatedAt.Equal(keyTime) {
		t.Errorf("dates %v, %v", third.Current.CreatedAt, third.Old[1].CreatedAt)
	}
	if r1.OldKeys != 1 || r2.OldKeys != 2 || r2.Recipient != third.Current.Recipient || r2.OldRecipient != second.Current.Recipient || r2.Code != "key_rotated" {
		t.Errorf("rotation %+v", r2)
	}
	if r2.Params["recipient"] != r2.Recipient || r2.Params["oldRecipient"] != r2.OldRecipient || r2.Params["oldKeys"] != "2" {
		t.Errorf("params %v", r2.Params)
	}
	if !strings.Contains(r2.Msg, "Copies made before stay readable") || !strings.Contains(r2.Hint, "Download the recovery key file again") {
		t.Errorf("explanation %q, %q", r2.Msg, r2.Hint)
	}

	plain := []byte("made before the first rotation")
	old := sealWith(t, plain, first.Current.Recipient)
	ids, err := third.identities()
	if err != nil || len(ids) != 3 || ids[0].recipient != third.Current.Recipient {
		t.Fatalf("identities %v, %v", ids, err)
	}
	if back, err := openWith(t, old, third.all()...); err != nil || !bytes.Equal(back, plain) {
		t.Errorf("the oldest copy doesn't open with the rotated keys: %v", err)
	}
	if _, err := openWith(t, sealWith(t, plain, third.Current.Recipient), first.Current); err == nil {
		t.Error("a new copy opens with the old key alone")
	}

	if _, _, err := (Keys{}).Rotate(keyTime); err == nil {
		t.Error("rotated keys that aren't valid")
	}
}

func TestRecoveryFile(t *testing.T) {
	first := newKeys(t)
	keys, _, err := first.Rotate(keyTime.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	rf, err := keys.RecoveryFile("Survival world", time.Date(2026, 9, 25, 12, 30, 0, 0, time.FixedZone("CEST", 2*3600)))
	if err != nil {
		t.Fatal(err)
	}
	text := rf.Content.Reveal()
	if rf.Name != "playkeeper-recovery-key-survival-world.txt" || rf.Content.String() != "[hidden]" {
		t.Errorf("name %q, content %v", rf.Name, rf.Content)
	}
	for _, want := range []string{
		"# Playkeeper recovery key for Survival world\n",
		"if this machine is lost, they\n# can't be opened without this file.",
		"Keep it private",
		`"Restore from a` + "\n# recovery key\"",
		"#   age --decrypt --identity playkeeper-recovery-key-survival-world.txt --output " + testName + " " + testCopy + "\n",
		"# Made 2026-09-25 10:30 UTC.",
		"# created: 2026-09-01T13:00:00Z\n# public key: " + keys.Current.Recipient + "\n" + keys.Current.Secret.Reveal() + "\n",
		"# created: 2026-09-01T12:00:00Z\n# public key: " + first.Current.Recipient + "\n" + first.Current.Secret.Reveal() + "\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the file lacks %q:\n%s", want, text)
		}
	}
	for i, l := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		// The command stays on one line so it can be copied into any shell.
		if len(l) > 80 && !strings.HasPrefix(l, "#   age --decrypt ") {
			t.Errorf("line %d is %d characters long", i+1, len(l))
		}
	}

	// The age tool reads identity files with this parser.
	ids, err := age.ParseIdentities(strings.NewReader(text))
	if err != nil || len(ids) != 2 {
		t.Fatalf("age.ParseIdentities = %d identities, %v", len(ids), err)
	}
	plain := []byte("world data")
	for _, rcpt := range []string{keys.Current.Recipient, first.Current.Recipient} {
		r, err := age.Decrypt(bytes.NewReader(sealWith(t, plain, rcpt)), ids...)
		if err != nil {
			t.Fatalf("the recovery file doesn't open a copy for %s: %v", rcpt, err)
		}
		if back, _ := io.ReadAll(r); !bytes.Equal(back, plain) {
			t.Error("wrong plaintext")
		}
	}

	back, err := ParseRecoveryFile(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	if back.Current.Recipient != keys.Current.Recipient || len(back.Old) != 1 || back.Old[0].Recipient != first.Current.Recipient ||
		!back.Current.CreatedAt.Equal(keys.Current.CreatedAt) || !back.Old[0].CreatedAt.Equal(first.Current.CreatedAt) || back.Validate() != nil {
		t.Errorf("parsed back %+v", back)
	}
}

func TestRecoveryFileServerNames(t *testing.T) {
	keys := newKeys(t)
	for _, tc := range []struct {
		server, file, title string
	}{
		{"Survival", "playkeeper-recovery-key-survival.txt", "Survival"},
		{"", "playkeeper-recovery-key-server.txt", "this server"},
		{"../../etc/passwd", "playkeeper-recovery-key-etc-passwd.txt", "../../etc/passwd"},
		{"Créatif — été", "playkeeper-recovery-key-cr-atif-t.txt", "Créatif — été"},
		{"Line\nAGE-SECRET-KEY-1INJECTED\r\n\u2028more", "playkeeper-recovery-key-line-age-secret-key-1injected-more.txt", "Line AGE-SECRET-KEY-1INJECTED more"},
		{strings.Repeat("a", 30) + " " + strings.Repeat("b", 30), "playkeeper-recovery-key-" + strings.Repeat("a", 30) + "-" + strings.Repeat("b", 9) + ".txt",
			strings.Repeat("a", 30) + " " + strings.Repeat("b", 30)},
		{strings.Repeat("x", 39) + " y", "playkeeper-recovery-key-" + strings.Repeat("x", 39) + ".txt", strings.Repeat("x", 39) + " y"},
		{strings.Repeat("é", 70), "playkeeper-recovery-key-server.txt", strings.Repeat("é", 64)},
	} {
		rf, err := keys.RecoveryFile(tc.server, keyTime)
		if err != nil {
			t.Fatal(err)
		}
		text := rf.Content.Reveal()
		if rf.Name != tc.file {
			t.Errorf("%q: name %q, want %q", tc.server, rf.Name, tc.file)
		}
		if first, _, _ := strings.Cut(text, "\n"); first != "# Playkeeper recovery key for "+tc.title {
			t.Errorf("%q: first line %q", tc.server, first)
		}
		back, err := ParseRecoveryFile(strings.NewReader(text))
		if err != nil || back.Current.Recipient != keys.Current.Recipient || len(back.Old) != 0 {
			t.Errorf("%q: parsed back %+v, %v", tc.server, back, err)
		}
	}
}

func TestParseRecoveryFileRefusals(t *testing.T) {
	a, b := newKeys(t), newKeys(t)
	secret := a.Current.Secret.Reveal()
	many := &strings.Builder{}
	for range maxRecoveryKeys + 1 {
		k, err := NewIdentity(keyTime)
		if err != nil {
			t.Fatal(err)
		}
		many.WriteString(k.Secret.Reveal() + "\n")
	}
	for _, tc := range []struct {
		name, text, msg string
	}{
		{"empty", "", "The file has no age keys in it."},
		{"only comments", "# Playkeeper recovery key\n#\n", "The file has no age keys in it."},
		{"a public key", "# my key\n\n" + a.Current.Recipient + "\n", "Line 3 of the file isn't an age key Playkeeper can use."},
		{"a damaged key", secret + "\n" + secret[:len(secret)-3] + "QQQ\n", "Line 2 of the file isn't an age key Playkeeper can use."},
		{"an SSH key", "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n", "Line 1 of the file isn't an age key Playkeeper can use."},
		{"a password", "hunter2-" + secret[16:30] + "\n", "Line 1 of the file isn't an age key Playkeeper can use."},
		{"not text", "\xff\xfe" + secret, "The file isn't a recovery key file: it isn't plain text."},
		{"too large", secret + "\n#" + strings.Repeat(" ", maxRecoveryFile), "The file is too large to be a recovery key file."},
		{"too many keys", many.String(), "The file has more keys than a recovery key file can."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRecoveryFile(strings.NewReader(tc.text))
			e := wantKind(t, err, KindInvalidConfig)
			if e.Msg != tc.msg || e.Field != "recoveryKey" || e.Hint == "" {
				t.Errorf("error %+v, want %q", e, tc.msg)
			}
			if strings.Contains(e.Msg+e.Hint, secret[16:30]) {
				t.Error("the error quotes the file")
			}
		})
	}

	_, err := ParseRecoveryFile(errReader{})
	if e := wantKind(t, err, KindInvalidConfig); e.Msg != "Playkeeper couldn't read the recovery key file." {
		t.Errorf("error %q", e.Msg)
	}

	// Line endings from another system, spaces, repeats and a bad date are fine.
	text := "  # created: yesterday\r\n" + b.Current.Secret.Reveal() + "  \r\n\r\n\t" + secret + "\r\n" + b.Current.Secret.Reveal() + "\r\n"
	got, err := ParseRecoveryFile(strings.NewReader(text))
	if err != nil || got.Current.Recipient != b.Current.Recipient || len(got.Old) != 1 || got.Old[0].Recipient != a.Current.Recipient || !got.Current.CreatedAt.IsZero() {
		t.Errorf("parsed %+v, %v", got, err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("disk error") }

func TestRecoveryFileSaysWhoseKeysAndWhereTheCopiesAre(t *testing.T) {
	keys, _, err := newKeys(t).Rotate(keyTime.Add(24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	made := time.Date(2026, 9, 24, 18, 47, 0, 0, time.UTC)
	rf, err := keys.RecoveryFileFor("Survival", "playkeeper/survival/", made)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := ReadRecoveryFile(strings.NewReader(rf.Content.Reveal()))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Server != "Survival" || rec.Folder != "playkeeper/survival/" || !rec.Made.Equal(made) || len(rec.Keys.Old) != 1 || rec.Keys.Current.Recipient != keys.Current.Recipient {
		t.Fatalf("read back %q %q %v, %d old keys", rec.Server, rec.Folder, rec.Made, len(rec.Keys.Old))
	}

	for _, folder := range []string{"", "backups/\nsurvival", strings.Repeat("a", 257)} {
		rf, err := keys.RecoveryFileFor("Survival", folder, made)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(rf.Content.Reveal(), "# folder:") {
			t.Errorf("folder %q was written", folder)
		}
	}

	// A folder line below the keys isn't the file's own.
	text := rf.Content.Reveal() + "\n# folder: elsewhere/\n"
	if rec, err := ReadRecoveryFile(strings.NewReader(text)); err != nil || rec.Folder != "playkeeper/survival/" {
		t.Fatalf("folder after the keys: %q %v", rec.Folder, err)
	}
}

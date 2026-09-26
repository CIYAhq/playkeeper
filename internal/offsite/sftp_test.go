package offsite

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func parseKey(t *testing.T, line string) ssh.PublicKey {
	t.Helper()
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func removeAll(t *testing.T, p string) {
	t.Helper()
	if err := os.RemoveAll(p); err != nil {
		t.Fatal(err)
	}
}

// place writes a file as someone on the other machine would, last
// modified at mtime unless it is zero.
func place(t *testing.T, p string, data []byte, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

func flipByte(t *testing.T, p string, off int64) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b := make([]byte, 1)
	if _, err := f.ReadAt(b, off); err != nil {
		t.Fatal(err)
	}
	b[0] ^= 0xff
	if _, err := f.WriteAt(b, off); err != nil {
		t.Fatal(err)
	}
}

func httpReply(status, contentType, body string) string {
	return "HTTP/1.1 " + status + "\r\nContent-Type: " + contentType + "\r\nContent-Length: " + strconv.Itoa(len(body)) +
		"\r\nConnection: close\r\n\r\n" + body
}

func TestSFTPRoundTrip(t *testing.T) {
	ctx := context.Background()
	srv := newSSHServer(t, nil)
	keys := newKeys(t)
	d, waits := srv.open(keys, srv.settings())
	file := newTestFile(300<<10, 21)
	up := file.upload(testName)
	var reports []Progress
	up.Progress = func(p Progress) { reports = append(reports, p) }
	cp, err := d.Upload(ctx, up)
	if err != nil {
		t.Fatal(err)
	}

	at := filepath.Join(srv.folder(), testCopy)
	stored, err := os.ReadFile(at)
	if err != nil {
		t.Fatal(err)
	}
	if cp.Name != testCopy || cp.Archive != testName || cp.ArchiveSHA256 != file.sha() || cp.Recipient != keys.Current.Recipient ||
		cp.Key != testFolder+"/"+testCopy || cp.Size != int64(len(stored)) || cp.SHA256 != sum256(stored) || cp.Checked != CheckedSampled ||
		cp.VerifiedAt.IsZero() || !cp.Uploaded {
		t.Errorf("copy %+v", cp)
	}
	if !bytes.HasPrefix(stored, []byte("age-encryption.org/v1\n-> X25519 ")) || bytes.Contains(stored, file.data[:32]) {
		t.Error("the other machine holds the backup unencrypted")
	}
	if back, err := openWith(t, stored, keys.Current); err != nil || !bytes.Equal(back, file.data) {
		t.Fatalf("the age library doesn't open the copy: %v", err)
	}
	if len(reports) != 5 {
		t.Fatalf("%d progress reports, want one per 64 KiB segment", len(reports))
	}
	for i, p := range reports {
		sent := min(int64(i+1)<<16, cp.Size)
		want := SFTPUpload{Partial: "." + testCopy + partialSuffix, Written: sent}
		if p.Sent != sent || p.Total != cp.Size || p.State == nil || p.State.SFTP == nil || *p.State.SFTP != want {
			t.Errorf("progress %d: %+v, state %+v", i, p, p.State)
		}
	}
	if names := dirNames(t, srv.folder()); !slices.Equal(names, []string{testCopy}) {
		t.Errorf("the folder holds %q", names)
	}
	if names := dirNames(t, d.spool); len(names) != 0 {
		t.Errorf("the spool folder still holds %q", names)
	}
	if got := srv.copyOf(&srv.logins); !slices.Equal(got, []string{"password ok"}) {
		t.Errorf("sign-ins %q", got)
	}
	if got := srv.copyOf(&srv.dialed); !slices.Equal(got, []string{"backup.example:2222"}) {
		t.Errorf("dialed %q", got)
	}
	if len(*waits) != 0 {
		t.Errorf("waited %v", *waits)
	}

	v, err := d.Verify(ctx, Copy{Name: cp.Name, Size: cp.Size})
	if err != nil || v.Key != cp.Key || v.VerifiedAt.IsZero() {
		t.Errorf("Verify = %+v, %v", v, err)
	}
	fi, err := os.Stat(at)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := d.List(ctx)
	if err != nil || len(objs) != 1 {
		t.Fatalf("List = %+v, %v", objs, err)
	}
	if o := objs[0]; o.Name != testCopy || o.Archive != testName || o.Key != cp.Key || o.Size != cp.Size ||
		!o.LastModified.Equal(fi.ModTime().Truncate(time.Second)) {
		t.Errorf("listed %+v", o)
	}

	dir := t.TempDir()
	a, err := d.Download(ctx, restore(cp, dir))
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != testName || a.Path != filepath.Join(dir, testName) || a.Size != int64(len(file.data)) || a.SHA256 != file.sha() ||
		a.Recipient != keys.Current.Recipient || !a.Matched {
		t.Errorf("archive %+v", a)
	}
	if got, err := os.ReadFile(a.Path); err != nil || !bytes.Equal(got, file.data) {
		t.Fatal("the restored backup differs")
	}
	if names := dirNames(t, dir); !slices.Equal(names, []string{testName}) {
		t.Errorf("the restore folder holds %q", names)
	}

	again, err := d.Upload(ctx, file.upload(testName))
	if err != nil {
		t.Fatal(err)
	}
	if again.Uploaded || again.Checked != CheckedDecrypted || again.Key != cp.Key || again.Size != cp.Size || again.SHA256 != cp.SHA256 {
		t.Errorf("uploading again gave %+v", again)
	}
	if now, err := os.ReadFile(at); err != nil || !bytes.Equal(now, stored) {
		t.Error("uploading again changed the copy")
	}
	if names := dirNames(t, d.spool); len(names) != 0 {
		t.Errorf("the spool folder still holds %q", names)
	}

	for range 2 {
		if err := d.Delete(ctx, testCopy); err != nil {
			t.Fatal(err)
		}
	}
	_, err = d.Verify(ctx, cp)
	if e := wantKind(t, err, KindNotFound); e.Msg != "The copy "+testCopy+" is not on the other machine." || e.Retry {
		t.Errorf("error %+v", e)
	}
	if objs, err := d.List(ctx); err != nil || len(objs) != 0 {
		t.Errorf("List = %+v, %v", objs, err)
	}
}

func TestSFTPConnectionTest(t *testing.T) {
	const allSteps = "connect folder write rename read list delete"
	for _, tc := range []struct {
		name    string
		disk    *memDisk // keeps the files in memory if set, else in a folder
		server  func(s *sshServer)
		ok      string
		failed  string
		kind    Kind
		msg     string // how the failed step's message starts
		warning string
	}{
		{name: "everything works", disk: &memDisk{free: 1 << 40, room: -1}, ok: allSteps},
		{name: "little space left", disk: &memDisk{free: 500 << 20, room: -1}, ok: allSteps,
			warning: "Only 524 MB free on {host}"},
		{name: "the disk is full", disk: &memDisk{}, ok: "connect folder", failed: "write", kind: KindStorageFull,
			msg:     "The other machine's disk has no room for " + probePrefix,
			warning: "Only 0 bytes free on {host}"},
		{name: "the disk keeps other bytes", disk: &memDisk{free: 1 << 40, room: -1, corrupt: true},
			ok: "connect folder write rename list delete", failed: "read", kind: KindVerifyFailed,
			msg: "The test file came back different from what was written."},
		{name: "the user may not write in the folder", disk: &memDisk{free: 1 << 40, room: -1, readOnly: true},
			ok: "connect folder", failed: "write", kind: KindPermission,
			msg: "The user playkeeper may not use files in the folder backups/survival on the other machine."},
		{name: "no folder", server: func(s *sshServer) { removeAll(s.t, s.folder()) }, ok: "connect", failed: "folder",
			kind: KindNoSuchFolder, msg: "No folder backups/survival on "},
		{name: "a file instead of the folder", server: func(s *sshServer) {
			removeAll(s.t, s.folder())
			place(s.t, s.folder(), []byte("not a folder"), time.Time{})
		}, ok: "connect", failed: "folder", kind: KindNoSuchFolder, msg: "backups/survival on the other machine is a file, not a folder."},
		{name: "a wrong password", server: func(s *sshServer) { s.password = "Tr0ub4dor&3" }, failed: "connect",
			kind: KindLoginRefused, msg: "The other machine refused the password for the user playkeeper."},
		{name: "no SFTP", server: func(s *sshServer) { s.noSFTP = true }, failed: "connect", kind: KindUnexpected,
			msg: "The other machine accepted the sign-in but doesn't offer SFTP."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newSSHServer(t, func(s *sshServer) {
				if tc.disk != nil {
					inMemory(tc.disk)(s)
				}
				if tc.server != nil {
					tc.server(s)
				}
			})
			d, _ := srv.open(newKeys(t), srv.settings())
			res := d.Test(context.Background())
			ok, failed := steps(res)
			if strings.Join(ok, " ") != tc.ok || strings.Join(failed, " ") != tc.failed || res.OK != (tc.failed == "") {
				t.Fatalf("passed %q, failed %q: %+v", ok, failed, res)
			}
			if res.HostKey == nil || res.HostKey.Fingerprint != fingerprint("ed25519") {
				t.Errorf("host key %+v", res.HostKey)
			}
			if want := strings.ReplaceAll(tc.warning, "{host}", srv.settings().Host); res.Warning != want {
				t.Errorf("warning %q, want %q", res.Warning, want)
			}
			for _, ch := range res.Checks {
				switch {
				case ch.OK && (ch.Msg == "" || ch.Kind != "" || ch.Hint != "" || ch.Params != nil):
					t.Errorf("passed check %+v", ch)
				case !ch.OK && (ch.Kind != tc.kind || !strings.HasPrefix(ch.Msg, tc.msg) || ch.Hint == "" || ch.Params["op"] != opTest):
					t.Errorf("failed check %+v", ch)
				}
			}
			if tc.failed == "folder" {
				return
			}
			left := dirNames(t, srv.folder())
			if tc.disk != nil {
				left = tc.disk.names(t)
			}
			if len(left) != 0 {
				t.Errorf("the test left %q in the folder", left)
			}
		})
	}
}

func TestSFTPHostKeys(t *testing.T) {
	ctx := context.Background()
	keys := newKeys(t)

	t.Run("the first connection shows the fingerprint to confirm", func(t *testing.T) {
		srv := newSSHServer(t, nil)
		d, _ := srv.open(keys, sftpSettings(nil))
		res := d.Test(ctx)
		fp := fingerprint("ed25519")
		want := HostKey{Key: authorizedKey(hostKey("ed25519").PublicKey()), Type: ssh.KeyAlgoED25519, Fingerprint: fp}
		if res.OK || res.HostKey == nil || *res.HostKey != want || len(res.Checks) != 1 {
			t.Fatalf("test %+v", res)
		}
		ch := res.Checks[0]
		if ch.Step != "connect" || ch.Kind != KindHostKeyUnknown || ch.Msg != "Check that the other machine's host key fingerprint is "+fp+", then confirm it." ||
			ch.Params["fingerprint"] != fp || ch.Params["keyType"] != ssh.KeyAlgoED25519 || ch.Params["field"] != "hostKey" {
			t.Errorf("check %+v", ch)
		}
		if got := srv.copyOf(&srv.passwords); len(got) != 0 {
			t.Error("the password was sent before the host key was confirmed")
		}
		js, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(js), `"hostKey":{"key":"ssh-ed25519 AAAA`) {
			t.Errorf("JSON %s", js)
		}

		cfg := sftpSettings(nil)
		cfg.HostKey = res.HostKey.Key
		d, _ = srv.open(keys, cfg)
		if res := d.Test(ctx); !res.OK {
			t.Errorf("with the key confirmed: %+v", res)
		}
	})

	t.Run("an upload waits for the confirmed key", func(t *testing.T) {
		srv := newSSHServer(t, nil)
		d, _ := srv.open(keys, sftpSettings(nil))
		file := newTestFile(10<<10, 22)
		_, err := d.Upload(ctx, file.upload(testName))
		e := wantKind(t, err, KindHostKeyUnknown)
		if e.Msg != "The other machine's host key isn't confirmed yet, so nothing was copied." || e.Field != "hostKey" || e.Resume != nil {
			t.Errorf("error %+v", e)
		}
		if file.total != 0 || len(dirNames(t, d.spool)) != 0 || len(srv.copyOf(&srv.dialed)) != 0 {
			t.Errorf("read %d bytes of the backup, spooled %q and dialed %q", file.total, dirNames(t, d.spool), srv.copyOf(&srv.dialed))
		}
	})

	t.Run("each kind of host key", func(t *testing.T) {
		for _, k := range []struct{ name, typ string }{
			{"ed25519", ssh.KeyAlgoED25519}, {"ecdsa", ssh.KeyAlgoECDSA256}, {"rsa", ssh.KeyAlgoRSA},
		} {
			srv := newSSHServer(t, func(s *sshServer) { s.hostKeys = signers(k.name) })
			d, _ := srv.open(keys, sftpSettings(nil))
			res := d.Test(ctx)
			if res.HostKey == nil || res.HostKey.Type != k.typ || res.HostKey.Fingerprint != fingerprint(k.name) {
				t.Fatalf("%s: host key %+v", k.name, res.HostKey)
			}
			d, _ = srv.open(keys, srv.settings())
			if res := d.Test(ctx); !res.OK {
				t.Errorf("%s confirmed: %+v", k.name, res)
			}
		}
	})

	t.Run("a machine with several host keys presents the confirmed one", func(t *testing.T) {
		srv := newSSHServer(t, func(s *sshServer) { s.hostKeys = signers("ecdsa", "rsa", "ed25519") })
		d, _ := srv.open(keys, sftpSettings(nil))
		if res := d.Test(ctx); res.HostKey == nil || res.HostKey.Fingerprint != fingerprint("ed25519") {
			t.Errorf("before confirming, the machine presented %+v", res.HostKey)
		}
		for _, name := range []string{"ecdsa", "rsa", "ed25519"} {
			d, _ := srv.open(keys, sftpSettings(hostKey(name)))
			if res := d.Test(ctx); !res.OK || res.HostKey == nil || res.HostKey.Fingerprint != fingerprint(name) {
				t.Errorf("with the %s key confirmed: %+v", name, res)
			}
		}
	})

	t.Run("a changed host key is refused before signing in", func(t *testing.T) {
		srv := newSSHServer(t, func(s *sshServer) { s.hostKeys = signers("ed25519 reinstalled") })
		d, waits := srv.open(keys, sftpSettings(hostKey("ed25519")))
		_, err := d.Upload(ctx, newTestFile(10<<10, 23).upload(testName))
		e := wantKind(t, err, KindHostKeyChanged)
		old, now := fingerprint("ed25519"), fingerprint("ed25519 reinstalled")
		if e.Msg != "The other machine presented a different host key ("+now+") than the one you confirmed ("+old+"), so Playkeeper didn't connect." ||
			e.Pinned != old || e.Params()["pinnedFingerprint"] != old || e.HostKey == nil || e.HostKey.Fingerprint != now ||
			e.Field != "hostKey" || !strings.Contains(e.Hint, "reinstalled") || e.Retry {
			t.Errorf("error %+v", e)
		}
		if e.Resume == nil || !slices.Equal(dirNames(t, d.spool), []string{e.Resume.Spool}) {
			t.Errorf("resume %+v, spool folder %q", e.Resume, dirNames(t, d.spool))
		}
		if got := srv.copyOf(&srv.passwords); len(got) != 0 || srv.connections() != 1 || len(*waits) != 0 {
			t.Errorf("sent %d passwords over %d connections and waited %v", len(got), srv.connections(), *waits)
		}
		if res := d.Test(ctx); res.OK || len(res.Checks) != 1 || res.Checks[0].Kind != KindHostKeyChanged {
			t.Errorf("test %+v", res)
		}
	})

	t.Run("a confirmed key the machine no longer offers", func(t *testing.T) {
		srv := newSSHServer(t, func(s *sshServer) { s.hostKeys = signers("ecdsa") })
		d, _ := srv.open(keys, sftpSettings(hostKey("ed25519")))
		_, err := d.List(ctx)
		e := wantKind(t, err, KindHostKeyChanged)
		if e.Msg != "The other machine no longer offers the host key you confirmed ("+fingerprint("ed25519")+"), so Playkeeper didn't connect." ||
			e.HostKey != nil || e.Pinned != fingerprint("ed25519") || e.Op != opList || e.Retry {
			t.Errorf("error %+v", e)
		}
	})

	t.Run("an old short RSA key", func(t *testing.T) {
		srv := newSSHServer(t, func(s *sshServer) { s.hostKeys = signers("rsa 1024") })
		d, waits := srv.open(keys, sftpSettings(nil))
		_, err := d.List(ctx)
		e := wantKind(t, err, KindUnexpected)
		if e.Msg != "The other machine's host key is an old or short kind of key (ssh-rsa) that Playkeeper doesn't trust." ||
			e.Field != "host" || e.Retry || e.HostKey == nil || e.HostKey.Type != ssh.KeyAlgoRSA || len(*waits) != 0 {
			t.Errorf("error %+v", e)
		}
	})
}

func TestSFTPSignIn(t *testing.T) {
	key, err := NewSSHKey("survival")
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := NewSSHKey("creative")
	if err != nil {
		t.Fatal(err)
	}
	pub, strangerPub := parseKey(t, key.PublicKey), parseKey(t, stranger.PublicKey)
	const (
		refused  = "The other machine refused the password for the user playkeeper."
		moreThan = "The other machine asks for more than a password, such as a one-time code, which Playkeeper can't answer."
	)
	for _, tc := range []struct {
		name   string
		server func(s *sshServer)
		key    bool   // signs in with the key instead of the password
		logins string // the sign-ins the server answered, in order
		sent   int    // how many times the password reached the server
		field  string // at fault if signing in fails
		msg    string
	}{
		{name: "a password", logins: "password ok", sent: 1},
		{name: "a password asked by keyboard-interactive", server: func(s *sshServer) { s.methods = []string{"keyboard-interactive"} },
			logins: "keyboard-interactive ok", sent: 1},
		{name: "keyboard-interactive with an empty round first", server: func(s *sshServer) {
			s.methods, s.kbd = []string{"keyboard-interactive"}, [][]string{{}, {"Password: "}}
		}, logins: "keyboard-interactive ok", sent: 1},
		{name: "a key", server: func(s *sshServer) { s.methods, s.keys = []string{"publickey"}, []ssh.PublicKey{pub} }, key: true,
			logins: "publickey ok"},
		{name: "a key where passwords work too", server: func(s *sshServer) {
			s.methods, s.keys = []string{"password", "publickey"}, []ssh.PublicKey{pub}
		}, key: true, logins: "publickey ok"},
		{name: "a wrong password", server: func(s *sshServer) { s.password = "Tr0ub4dor&3" },
			logins: "password refused", sent: 1, field: "password", msg: refused},
		{name: "a wrong password asked by keyboard-interactive", server: func(s *sshServer) {
			s.methods, s.password = []string{"keyboard-interactive"}, "Tr0ub4dor&3"
		}, logins: "keyboard-interactive refused", sent: 1, field: "password", msg: refused},
		{name: "a wrong password both ways", server: func(s *sshServer) {
			s.methods, s.password = []string{"password", "keyboard-interactive"}, "Tr0ub4dor&3"
		}, logins: "password refused, keyboard-interactive refused", sent: 2, field: "password", msg: refused},
		{name: "an unknown key", server: func(s *sshServer) { s.methods, s.keys = []string{"publickey"}, []ssh.PublicKey{strangerPub} },
			key: true, logins: "publickey refused", field: "privateKey",
			msg: "The other machine didn't accept Playkeeper's key for the user playkeeper."},
		{name: "keys not allowed", key: true, field: "privateKey", msg: "The other machine doesn't accept keys for signing in."},
		{name: "passwords not allowed", server: func(s *sshServer) { s.methods = []string{"publickey"} },
			field: "password", msg: "The other machine doesn't accept passwords for signing in."},
		{name: "a one-time code after the password", server: func(s *sshServer) { s.more = true },
			logins: "password partial", sent: 1, field: "password", msg: moreThan},
		{name: "a one-time code after the key", server: func(s *sshServer) {
			s.methods, s.keys, s.more = []string{"publickey"}, []ssh.PublicKey{pub}, true
		}, key: true, logins: "publickey partial", field: "privateKey",
			msg: "The other machine accepted Playkeeper's key but asks for more, such as a password or a one-time code, which Playkeeper can't give."},
		{name: "two questions at once", server: func(s *sshServer) {
			s.methods, s.kbd = []string{"keyboard-interactive"}, [][]string{{"Password: ", "Verification code: "}}
		}, field: "password", msg: moreThan},
		{name: "a question that shows the answer as it is typed", server: func(s *sshServer) {
			s.methods, s.kbd, s.echo = []string{"keyboard-interactive"}, [][]string{{"Verification code: "}}, true
		}, field: "password", msg: moreThan},
		{name: "a second question after the password", server: func(s *sshServer) {
			s.methods, s.kbd = []string{"keyboard-interactive"}, [][]string{{"Password: "}, {"Verification code: "}}
		}, sent: 1, field: "password", msg: moreThan},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newSSHServer(t, tc.server)
			cfg := srv.settings()
			if tc.key {
				cfg.Password, cfg.PrivateKey = Secret{}, key.PrivateKey
			}
			d, waits := srv.open(newKeys(t), cfg)
			_, err := d.List(context.Background())
			if got := strings.Join(srv.copyOf(&srv.logins), ", "); got != tc.logins {
				t.Errorf("sign-ins %q, want %q", got, tc.logins)
			}
			passwords := srv.copyOf(&srv.passwords)
			if len(passwords) != tc.sent || slices.ContainsFunc(passwords, func(p string) bool { return p != testPassword }) {
				t.Errorf("the password reached the server %d times, want %d", len(passwords), tc.sent)
			}
			if n := srv.connections(); n != 1 || len(*waits) != 0 {
				t.Errorf("%d connections, waited %v", n, *waits)
			}
			if tc.msg == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			e := wantKind(t, err, KindLoginRefused)
			if e.Msg != tc.msg || e.Field != tc.field || e.User != "playkeeper" || e.Op != opList || e.Retry || e.Hint == "" {
				t.Errorf("error %+v", e)
			}
		})
	}
}

func TestSFTPUploadResumes(t *testing.T) {
	const written = 128 << 10 // two 64 KiB segments
	resumed := func(t *testing.T, srv *sshServer, d *Destination, keys Keys, file *testFile, cp Copy) {
		t.Helper()
		stored, err := os.ReadFile(filepath.Join(srv.folder(), testCopy))
		if err != nil {
			t.Fatal(err)
		}
		if back, err := openWith(t, stored, keys.Current); err != nil || !bytes.Equal(back, file.data) {
			t.Fatalf("the resumed copy doesn't open to the backup: %v", err)
		}
		if cp.SHA256 != sum256(stored) || cp.Size != int64(len(stored)) || !cp.Uploaded || cp.Checked != CheckedSampled {
			t.Errorf("copy %+v", cp)
		}
		if file.total != int64(len(file.data)) {
			t.Errorf("read %d bytes of the backup, want it encrypted once", file.total)
		}
		if n := srv.connections(); n != 2 {
			t.Fatalf("%d connections, want 2", n)
		}
		if got := srv.received(2); got < cp.Size-written || got >= cp.Size-written+64<<10 {
			t.Errorf("the second connection carried %d bytes, want the %d not yet written and the SSH overhead", got, cp.Size-written)
		}
		if names := dirNames(t, srv.folder()); !slices.Equal(names, []string{testCopy}) {
			t.Errorf("the folder holds %q", names)
		}
		if names := dirNames(t, d.spool); len(names) != 0 {
			t.Errorf("the spool folder still holds %q", names)
		}
	}

	t.Run("on another connection after this one dropped", func(t *testing.T) {
		srv := newSSHServer(t, func(s *sshServer) { s.fault = dropFirst(150 << 10) })
		keys := newKeys(t)
		d, waits := srv.open(keys, srv.settings())
		file := newTestFile(300<<10, 24)
		cp := mustUpload(t, d, file, testName)
		resumed(t, srv, d, keys, file, cp)
		if !slices.Equal(*waits, []time.Duration{time.Second}) {
			t.Errorf("waited %v", *waits)
		}
	})

	t.Run("from the state the failed upload returned", func(t *testing.T) {
		srv := newSSHServer(t, func(s *sshServer) { s.fault = dropFirst(150 << 10) })
		keys := newKeys(t)
		d, waits := srv.open(keys, srv.settings())
		c := d.b.(*sftpClient)
		c.attempts = 1
		file := newTestFile(300<<10, 25)
		_, err := d.Upload(context.Background(), file.upload(testName))
		e := wantKind(t, err, KindNetwork)
		if !e.Retry || e.Resume == nil || e.Resume.SFTP == nil || e.Resume.SFTP.Written != written {
			t.Fatalf("error %+v with resume %+v", e, e.Resume)
		}
		saved, err := json.Marshal(e.Resume)
		if err != nil {
			t.Fatal(err)
		}
		var st UploadState
		if err := json.Unmarshal(saved, &st); err != nil {
			t.Fatal(err)
		}

		c.attempts = 4
		up := file.upload(testName)
		up.Resume = &st
		cp, err := d.Upload(context.Background(), up)
		if err != nil {
			t.Fatal(err)
		}
		resumed(t, srv, d, keys, file, cp)
		if len(*waits) != 0 {
			t.Errorf("waited %v", *waits)
		}
	})

	t.Run("from the last progress after Playkeeper stopped", func(t *testing.T) {
		srv := newSSHServer(t, nil)
		keys := newKeys(t)
		d, _ := srv.open(keys, srv.settings())
		file := newTestFile(300<<10, 26)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var last Progress
		up := file.upload(testName)
		up.Progress = func(p Progress) {
			last = p
			if p.Sent == written {
				cancel()
			}
		}
		_, err := d.Upload(ctx, up)
		wantKind(t, err, KindCanceled)
		if last.State == nil || last.State.SFTP == nil || last.State.SFTP.Written != written {
			t.Fatalf("last progress %+v", last)
		}

		again, err := Open(Config{Type: TypeSFTP, SFTP: srv.settings()}, keys, Options{SpoolDir: d.spool, Dial: srv.dial})
		if err != nil {
			t.Fatal(err)
		}
		quickSFTP(again.b.(*sftpClient))
		up = file.upload(testName)
		up.Resume = last.State
		cp, err := again.Upload(context.Background(), up)
		if err != nil {
			t.Fatal(err)
		}
		resumed(t, srv, again, keys, file, cp)
	})
}

func TestSFTPUploadFailures(t *testing.T) {
	ctx := context.Background()
	kept := func(t *testing.T, d *Destination, e *Error, written int64) {
		t.Helper()
		if e.Resume == nil || e.Resume.SFTP == nil || e.Resume.SFTP.Partial != "."+testCopy+partialSuffix || e.Resume.SFTP.Written != written {
			t.Fatalf("resume %+v, want %d bytes written", e.Resume, written)
		}
		if names := dirNames(t, d.spool); !slices.Equal(names, []string{e.Resume.Spool}) {
			t.Errorf("the spool folder holds %q, want the encrypted copy %q", names, e.Resume.Spool)
		}
	}
	discarded := func(t *testing.T, d *Destination, e *Error) {
		t.Helper()
		if e.Resume != nil {
			t.Errorf("resume %+v", e.Resume)
		}
		if names := dirNames(t, d.spool); len(names) != 0 {
			t.Errorf("the spool folder still holds %q", names)
		}
	}

	t.Run("no room on the other machine", func(t *testing.T) {
		disk := &memDisk{free: 100 << 10, room: -1}
		srv := newSSHServer(t, inMemory(disk))
		d, _ := srv.open(newKeys(t), srv.settings())
		_, err := d.Upload(ctx, newTestFile(300<<10, 27).upload(testName))
		e := wantKind(t, err, KindStorageFull)
		kept(t, d, e, 0)
		if e.Msg != "The other machine's disk has no room for "+testCopy+": it needs 307 kB, and 102 kB is free." ||
			e.Need != e.Resume.Size || e.Free != 100<<10 || e.Params()["freeBytes"] != "102400" || e.Op != opUpload || e.Retry {
			t.Errorf("error %+v", e)
		}
		if slices.Contains(disk.names(t), testCopy) {
			t.Error("the copy was stored")
		}
	})

	t.Run("the other machine's disk fills up", func(t *testing.T) {
		disk := &memDisk{free: 1 << 40, room: 100 << 10}
		srv := newSSHServer(t, inMemory(disk))
		d, _ := srv.open(newKeys(t), srv.settings())
		_, err := d.Upload(ctx, newTestFile(300<<10, 28).upload(testName))
		e := wantKind(t, err, KindStorageFull)
		kept(t, d, e, 64<<10)
		if e.Msg != "The other machine's disk has no room for "+testCopy+": it needs 242 kB, and 4 kB is free." ||
			e.Need != e.Resume.Size-64<<10 || e.Free != 4096 || e.Op != opUpload {
			t.Errorf("error %+v", e)
		}
	})

	t.Run("the connection stalls", func(t *testing.T) {
		srv := newSSHServer(t, func(s *sshServer) { s.fault = func(int) faults { return faults{hangAt: 100 << 10} } })
		d, waits := srv.open(newKeys(t), srv.settings())
		c := d.b.(*sftpClient)
		c.stall, c.attempts = time.Second, 1
		_, err := d.Upload(ctx, newTestFile(300<<10, 29).upload(testName))
		e := wantKind(t, err, KindNetwork)
		kept(t, d, e, 64<<10)
		if e.Msg != "The connection to the other machine stalled: no data moved for 1 second." || !e.Retry || len(*waits) != 0 {
			t.Errorf("error %+v, waited %v", e, *waits)
		}
	})

	t.Run("no folder", func(t *testing.T) {
		srv := newSSHServer(t, func(s *sshServer) { removeAll(s.t, s.folder()) })
		d, _ := srv.open(newKeys(t), srv.settings())
		_, err := d.Upload(ctx, newTestFile(10<<10, 30).upload(testName))
		e := wantKind(t, err, KindNoSuchFolder)
		kept(t, d, e, 0)
		if e.Msg != "No folder backups/survival on "+srv.settings().Host || e.Field != "folder" || e.Folder != testFolder || e.Hint != "Create it there first. Without a leading slash, it's inside playkeeper's home folder." {
			t.Errorf("error %+v", e)
		}
	})

	t.Run("a folder the user may not write in", func(t *testing.T) {
		srv := newSSHServer(t, func(s *sshServer) { s.readOnly = true })
		d, _ := srv.open(newKeys(t), srv.settings())
		_, err := d.Upload(ctx, newTestFile(10<<10, 31).upload(testName))
		e := wantKind(t, err, KindPermission)
		kept(t, d, e, 0)
		if e.Msg != "The user playkeeper may not write files in the folder backups/survival on the other machine." ||
			!strings.Contains(e.Hint, "chown") || e.User != "playkeeper" {
			t.Errorf("error %+v", e)
		}
	})

	t.Run("the encrypted copy changes while it is sent", func(t *testing.T) {
		srv := newSSHServer(t, nil)
		d, _ := srv.open(newKeys(t), srv.settings())
		up := newTestFile(300<<10, 32).upload(testName)
		up.Progress = func(p Progress) {
			if p.Sent == 64<<10 {
				flipByte(t, filepath.Join(d.spool, p.State.Spool), 200<<10)
			}
		}
		_, err := d.Upload(ctx, up)
		e := wantKind(t, err, KindLocalChanged)
		if e.Msg != "The encrypted copy "+testCopy+" changed on this machine while it was being sent, so the upload was stopped." {
			t.Errorf("error %+v", e)
		}
		discarded(t, d, e)
		if names := dirNames(t, srv.folder()); len(names) != 0 {
			t.Errorf("the folder holds %q", names)
		}
	})

	t.Run("the other machine keeps other bytes", func(t *testing.T) {
		disk := &memDisk{free: 1 << 40, room: -1, corrupt: true}
		srv := newSSHServer(t, inMemory(disk))
		d, _ := srv.open(newKeys(t), srv.settings())
		_, err := d.Upload(ctx, newTestFile(300<<10, 33).upload(testName))
		e := wantKind(t, err, KindVerifyFailed)
		if e.Msg != "The copy "+testCopy+" on the other machine didn't match what was sent, so it was deleted." || e.Op != opVerify {
			t.Errorf("error %+v", e)
		}
		discarded(t, d, e)
		if names := disk.names(t); len(names) != 0 {
			t.Errorf("the other machine kept %q", names)
		}
	})
}

func TestSFTPNeverOverwrites(t *testing.T) {
	other := newKeys(t)
	file := newTestFile(100<<10, 34)
	const damaged = "A file named " + testCopy + " is already on the other machine, and it is damaged or isn't a Playkeeper copy."
	for _, tc := range []struct {
		name   string
		stored func(t *testing.T, own Keys) []byte
		// late puts the file there only once the copy was sent.
		late bool
		msg  string
	}{
		{"a copy of another backup", func(t *testing.T, own Keys) []byte {
			return sealWith(t, newTestFile(1000, 35).data, own.Current.Recipient)
		}, false, "A copy of a different backup named " + testCopy + " is already on the other machine."},
		{"a copy made with another key", func(t *testing.T, _ Keys) []byte { return sealWith(t, file.data, other.Current.Recipient) }, false,
			"A copy named " + testCopy + " is already on the other machine, made with an encryption key this server doesn't have."},
		{"a file that isn't a copy", func(*testing.T, Keys) []byte { return []byte("some other program's file") }, false, damaged},
		{"a file that appears while the copy is sent", func(*testing.T, Keys) []byte { return []byte("some other program's file") }, true, damaged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newSSHServer(t, nil)
			keys := newKeys(t)
			d, _ := srv.open(keys, srv.settings())
			stored, at := tc.stored(t, keys), filepath.Join(srv.folder(), testCopy)
			up := file.upload(testName)
			if tc.late {
				up.Progress = func(p Progress) {
					if p.Sent == p.Total {
						place(t, at, stored, time.Time{})
					}
				}
			} else {
				place(t, at, stored, time.Time{})
			}
			_, err := d.Upload(context.Background(), up)
			e := wantKind(t, err, KindConflict)
			if e.Msg != tc.msg || !strings.Contains(e.Hint, "never overwrites") {
				t.Errorf("error %q, %q", e.Msg, e.Hint)
			}
			if got, err := os.ReadFile(at); err != nil || !bytes.Equal(got, stored) {
				t.Error("the file was changed")
			}
			if names := dirNames(t, srv.folder()); !slices.Equal(names, []string{testCopy}) {
				t.Errorf("the folder holds %q", names)
			}
			if names := dirNames(t, d.spool); len(names) != 0 {
				t.Errorf("the spool folder still holds %q", names)
			}
		})
	}
}

func TestSFTPDownloadRetries(t *testing.T) {
	for _, how := range []string{"after the connection dropped", "after a read ended early"} {
		t.Run(how, func(t *testing.T) {
			disk := &memDisk{free: 1 << 40, room: -1}
			srv := newSSHServer(t, func(s *sshServer) {
				if how == "after a read ended early" {
					inMemory(disk)(s)
					return
				}
				s.fault = func(conn int) faults {
					if conn == 2 {
						return faults{dropOutAt: 100 << 10}
					}
					return faults{}
				}
			})
			d, waits := srv.open(newKeys(t), srv.settings())
			file := newTestFile(200<<10, 36)
			cp := mustUpload(t, d, file, testName)
			if how == "after a read ended early" {
				disk.mu.Lock()
				disk.cut = 100 << 10
				disk.mu.Unlock()
			}

			dir := t.TempDir()
			a, err := d.Download(context.Background(), restore(cp, dir))
			if err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(a.Path); err != nil || !bytes.Equal(got, file.data) || !a.Matched {
				t.Errorf("restored %+v", a)
			}
			if n := srv.connections(); n != 3 || !slices.Equal(*waits, []time.Duration{time.Second}) {
				t.Errorf("%d connections, waited %v; want the download tried again once", n, *waits)
			}
			if names := dirNames(t, dir); !slices.Equal(names, []string{testName}) {
				t.Errorf("the folder holds %q", names)
			}
		})
	}
}

func TestSFTPAbort(t *testing.T) {
	ctx := context.Background()
	srv := newSSHServer(t, func(s *sshServer) { s.fault = dropFirst(150 << 10) })
	d, _ := srv.open(newKeys(t), srv.settings())
	d.b.(*sftpClient).attempts = 1
	_, err := d.Upload(ctx, newTestFile(300<<10, 37).upload(testName))
	st := wantKind(t, err, KindNetwork).Resume
	partial := "." + testCopy + partialSuffix
	if st == nil || !slices.Equal(dirNames(t, srv.folder()), []string{partial}) {
		t.Fatalf("resume %+v, folder %q", st, dirNames(t, srv.folder()))
	}

	notes := ".notes.txt" + partialSuffix
	place(t, filepath.Join(srv.folder(), notes), []byte("not Playkeeper's"), time.Time{})
	before := srv.connections()
	for _, bad := range []string{notes, "../" + partial, testCopy, partial + "/.."} {
		err := d.Abort(ctx, &UploadState{Name: testCopy, SFTP: &SFTPUpload{Partial: bad}})
		if e := wantKind(t, err, KindUnexpected); e.Msg != "The saved upload doesn't name a file Playkeeper would have uploaded." {
			t.Errorf("aborting %q: %+v", bad, e)
		}
	}
	if srv.connections() != before || !slices.Equal(dirNames(t, srv.folder()), []string{notes, partial}) {
		t.Fatalf("crafted states connected or deleted: the folder holds %q", dirNames(t, srv.folder()))
	}

	for range 2 {
		if err := d.Abort(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	if names := dirNames(t, srv.folder()); !slices.Equal(names, []string{notes}) {
		t.Errorf("the folder holds %q", names)
	}
	if names := dirNames(t, d.spool); len(names) != 0 {
		t.Errorf("the spool folder still holds %q", names)
	}
	for _, st := range []*UploadState{nil, {}, {SFTP: &SFTPUpload{}}} {
		if err := d.Abort(ctx, st); err != nil {
			t.Errorf("Abort(%+v) = %v", st, err)
		}
	}
}

func TestSFTPAbortStale(t *testing.T) {
	ctx := context.Background()
	srv := newSSHServer(t, nil)
	d, _ := srv.open(newKeys(t), srv.settings())
	now := time.Now()
	old, recent, olderThan := now.Add(-48*time.Hour), now.Add(-time.Hour), now.Add(-24*time.Hour)
	partial := func(archive string) string { return "." + CopyName(archive) + partialSuffix }
	stale, kept, fresh := partial(otherName), partial(testName), partial("playkeeper-world-20260923-120000-0a0b0c.tar.gz")
	probe := "." + probePrefix + "0123456789abcdef.age" + partialSuffix
	notes := ".notes.txt" + partialSuffix
	for name, mtime := range map[string]time.Time{stale: old, kept: old, fresh: recent, probe: old, testCopy: old, notes: old} {
		place(t, filepath.Join(srv.folder(), name), []byte("x"), mtime)
	}
	folder := partial("playkeeper-world-20260922-120000-0d0e0f.tar.gz")
	if err := os.Mkdir(filepath.Join(srv.folder(), folder), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(srv.folder(), folder), old, old); err != nil {
		t.Fatal(err)
	}
	keptSpool, recentSpool := spoolPrefix+"fedcba9876543210.age", spoolPrefix+"00112233445566ff.age"
	for name, mtime := range map[string]time.Time{
		spoolPrefix + "0123456789abcdef.age": old, spoolPrefix + "aaaaaaaaaaaaaaaa" + partialSuffix: old,
		keptSpool: old, recentSpool: recent, "notes.txt": old,
	} {
		place(t, filepath.Join(d.spool, name), []byte("x"), mtime)
	}

	n, err := d.AbortStale(ctx, olderThan, []*UploadState{nil, {Spool: keptSpool, SFTP: &SFTPUpload{Partial: kept}}})
	if err != nil || n != 4 {
		t.Fatalf("AbortStale = %d, %v", n, err)
	}
	want := []string{kept, fresh, folder, testCopy, notes}
	slices.Sort(want)
	if got := dirNames(t, srv.folder()); !slices.Equal(got, want) {
		t.Errorf("the folder holds %q, want %q", got, want)
	}
	want = []string{keptSpool, recentSpool, "notes.txt"}
	slices.Sort(want)
	if got := dirNames(t, d.spool); !slices.Equal(got, want) {
		t.Errorf("the spool folder holds %q, want %q", got, want)
	}

	removeAll(t, srv.folder())
	n, err = d.AbortStale(ctx, olderThan, nil)
	if e := wantKind(t, err, KindNoSuchFolder); e.Op != opAbort || n != 1 {
		t.Errorf("AbortStale = %d, %+v", n, e)
	}
	if got := dirNames(t, d.spool); !slices.Equal(got, []string{recentSpool, "notes.txt"}) {
		t.Errorf("the spool folder holds %q", got)
	}
}

func TestSFTPList(t *testing.T) {
	ctx := context.Background()
	srv := newSSHServer(t, nil)
	d, _ := srv.open(newKeys(t), srv.settings())
	if objs, err := d.List(ctx); err != nil || len(objs) != 0 {
		t.Fatalf("List of an empty folder = %+v, %v", objs, err)
	}

	at, other := time.Date(2026, 9, 25, 12, 0, 30, 0, time.UTC), CopyName(otherName)
	place(t, filepath.Join(srv.folder(), testCopy), make([]byte, 3000), at)
	place(t, filepath.Join(srv.folder(), other), make([]byte, 2000), at.Add(-24*time.Hour))
	for _, name := range []string{"." + testCopy + partialSuffix, "notes.txt", testName} {
		place(t, filepath.Join(srv.folder(), name), []byte("x"), time.Time{})
	}
	if err := os.Mkdir(filepath.Join(srv.folder(), CopyName("playkeeper-world-20260923-120000-0a0b0c.tar.gz")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(testCopy, filepath.Join(srv.folder(), CopyName("playkeeper-world-20260922-120000-0d0e0f.tar.gz"))); err != nil {
		t.Fatal(err)
	}

	objs, err := d.List(ctx)
	if err != nil || len(objs) != 2 {
		t.Fatalf("List = %+v, %v", objs, err)
	}
	for i, want := range []Object{
		{Name: other, Archive: otherName, Key: testFolder + "/" + other, Size: 2000, LastModified: at.Add(-24 * time.Hour)},
		{Name: testCopy, Archive: testName, Key: testFolder + "/" + testCopy, Size: 3000, LastModified: at},
	} {
		if got := objs[i]; got.Name != want.Name || got.Archive != want.Archive || got.Key != want.Key || got.Size != want.Size ||
			!got.LastModified.Equal(want.LastModified) {
			t.Errorf("object %d: %+v, want %+v", i, got, want)
		}
	}

	removeAll(t, srv.folder())
	_, err = d.List(ctx)
	// Copies are listed to be read back, and a folder made now holds none.
	if e := wantKind(t, err, KindNoSuchFolder); e.Op != opList || e.Field != "folder" || !strings.HasPrefix(e.Msg, "No folder backups/survival on ") ||
		!strings.HasPrefix(e.Hint, "Check the folder's name.") {
		t.Errorf("error %+v", e)
	}
}

func TestSFTPConnectErrors(t *testing.T) {
	const notSSH = "Something other than an SSH server answered on port 2222 of backup.example."
	nginx := httpReply("400 Bad Request", "text/html", "<html>\r\n<head><title>400 Bad Request</title></head>\r\n<body>\r\n"+
		"<center><h1>400 Bad Request</h1></center>\r\n<hr><center>nginx/1.24.0 (Ubuntu)</center>\r\n</body>\r\n</html>\r\n")
	apiBody := `{"timestamp":"2026-09-25T12:00:00.000+00:00","status":400,"error":"Bad Request",` +
		`"message":"Invalid character found in method name [SSH-2.0-Playkeeper0x0d0x0a]. HTTP method names must be tokens",` +
		`"path":"/","requestId":"7f3c9a2e-4b1d-4e8f-9a6c-2d5e8f1b3c7a","traceId":"4bf92f3577b34da6a3ce929d0e0e4736"}`
	if len(apiBody) <= 255 {
		t.Fatalf("the API's reply is %d bytes on one line, want more than an SSH version line may have", len(apiBody))
	}
	postfix := func(c net.Conn) {
		io.WriteString(c, "220 mail.example ESMTP Postfix (Ubuntu)\r\n")
		r := bufio.NewReader(c)
		if _, err := r.ReadString('\n'); err == nil {
			io.WriteString(c, "502 5.5.2 Error: command not recognized\r\n")
		}
		io.Copy(io.Discard, r)
	}
	for _, tc := range []struct {
		name   string
		dial   dialer
		serve  func(c net.Conn)   // answers on the port instead of an SSH server
		server func(s *sshServer) // changes the SSH server
		stall  time.Duration
		kind   Kind
		retry  bool
		field  string
		msg    string
	}{
		{name: "nothing listens", dial: closedPort(t), kind: KindNetwork, field: "port",
			msg: "Nothing accepted the connection on port 2222 of backup.example (connection refused)."},
		{name: "an unknown host name", dial: failing(&net.DNSError{Err: "no such host", Name: testHost, IsNotFound: true}),
			kind: KindNetwork, field: "host", msg: "The host name backup.example could not be found."},
		{name: "DNS fails", dial: failing(&net.DNSError{Err: "server misbehaving", Name: testHost, Server: "127.0.0.53:53", IsTemporary: true}),
			kind: KindNetwork, retry: true, field: "host", msg: "This machine couldn't look up the other machine's address (DNS)."},
		{name: "no route to the network", dial: failing(&net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ENETUNREACH)}),
			kind: KindNetwork, retry: true, msg: "This machine can't reach the other machine's network."},
		{name: "connecting takes too long", dial: failing(&net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}),
			kind: KindNetwork, retry: true, msg: "The other machine didn't answer in time."},
		{name: "the port stays silent", serve: func(c net.Conn) { io.Copy(io.Discard, c) }, stall: 200 * time.Millisecond,
			kind: KindNetwork, retry: true, msg: "The other machine didn't answer in time."},
		{name: "the port closes at once", serve: func(net.Conn) {}, kind: KindNetwork, retry: true,
			msg: "The connection to the other machine broke."},
		{name: "a web server", serve: replying(nginx), kind: KindUnexpected, field: "port", msg: notSSH},
		{name: "a web API with a long line", serve: replying(httpReply("400 Bad Request", "application/json", apiBody)),
			kind: KindUnexpected, field: "port", msg: notSSH},
		{name: "a mail server waiting for a command", serve: postfix, stall: 200 * time.Millisecond,
			kind: KindUnexpected, field: "port", msg: notSSH},
		{name: "an SSH version line that is too long", serve: replying("SSH-2.0-" + strings.Repeat("x", 300) + "\r\n"),
			kind: KindUnexpected, field: "port", msg: notSSH},
		{name: "no SFTP", server: func(s *sshServer) { s.noSFTP = true }, kind: KindUnexpected, field: "host",
			msg: "The other machine accepted the sign-in but doesn't offer SFTP."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dial := tc.dial
			switch {
			case tc.serve != nil:
				dial = listening(t, tc.serve)
			case dial == nil:
				dial = newSSHServer(t, tc.server).dial
			}
			dials := 0
			c, err := newSFTP(sftpSettings(hostKey("ed25519")), func(ctx context.Context, network, addr string) (net.Conn, error) {
				dials++
				return dial(ctx, network, addr)
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			waits := quickSFTP(c)
			if tc.stall > 0 {
				c.stall = tc.stall
			}
			_, err = c.list(context.Background())
			e := wantKind(t, err, tc.kind)
			if e.Msg != tc.msg || e.Field != tc.field || e.Retry != tc.retry || e.Op != opList || e.Hint == "" {
				t.Errorf("error %+v", e)
			}
			wantDials, wantWaits := 1, []time.Duration(nil)
			if tc.retry {
				wantDials, wantWaits = 4, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
			}
			if dials != wantDials || !slices.Equal(*waits, wantWaits) {
				t.Errorf("dialled %d times and waited %v", dials, *waits)
			}
		})
	}
}

func TestSFTPDefaultDialerRefusesAddresses(t *testing.T) {
	// The dialer refuses before connecting, and neither address leaves the
	// machine even if it didn't.
	for _, addr := range []string{"224.0.0.251:22", "0.0.0.0:22"} {
		t.Run(addr, func(t *testing.T) {
			c, err := newSFTP(sftpSettings(hostKey("ed25519")), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			dial := c.dial
			c.dial = func(ctx context.Context, network, _ string) (net.Conn, error) { return dial(ctx, network, addr) }
			c.sleep = func(context.Context, time.Duration) error {
				t.Error("tried again")
				return nil
			}
			_, err = c.list(context.Background())
			e := wantKind(t, err, KindInvalidConfig)
			ip, _, _ := net.SplitHostPort(addr)
			if e.Field != "host" || e.Retry ||
				e.Msg != "The host name points to "+ip+", a link-local, multicast, unspecified or cloud metadata address Playkeeper never connects to." {
				t.Errorf("error %+v", e)
			}
		})
	}
}

func TestNotSSH(t *testing.T) {
	for head, want := range map[string]bool{
		"":                                    false,
		"SSH-2.0-OpenSSH_9.6\r\n":             false,
		"SSH-":                                false,
		"SS":                                  false,
		"Welcome\r\nSSH-2":                    false,
		"SSH-2.0-" + strings.Repeat("x", 300): false,
		"Welcome to backup.example\r\nSSH-2.0-OpenSSH_9.6\r\n": false,
		"Welcome\r\n":                        true,
		"HTTP/1.1 400 Bad Request\r\n":       true,
		"HTTP/1.1 4":                         true,
		"220 mail.example ESMTP Postfix\r\n": true,
		"\x15\x03\x01\x00\x02\x02\x46":       true,
		strings.Repeat("x", 300):             true,
	} {
		c := &stallConn{}
		c.keep([]byte(head[:len(head)/2]))
		c.keep([]byte(head[len(head)/2:]))
		if got := c.notSSH(); got != want {
			t.Errorf("notSSH after %q = %v, want %v", head, got, want)
		}
	}
}

func TestSFTPConfigValidate(t *testing.T) {
	key, err := NewSSHKey("survival")
	if err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "playkeeper-survival", []byte("a passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	locked := NewSecret(string(pem.EncodeToMemory(block)))
	pinned := func(name string) string { return authorizedKey(hostKey(name).PublicKey()) }
	base := sftpSettings(hostKey("ed25519"))
	for _, tc := range []struct {
		name  string
		mod   func(c *SFTPConfig)
		field string // the setting at fault; valid if empty
	}{
		{"the usual settings", func(*SFTPConfig) {}, ""},
		{"no host", func(c *SFTPConfig) { c.Host = "" }, "host"},
		{"an IPv4 address", func(c *SFTPConfig) { c.Host = "203.0.113.9" }, ""},
		{"an IPv6 address", func(c *SFTPConfig) { c.Host = "2001:db8::1" }, ""},
		{"localhost", func(c *SFTPConfig) { c.Host = "localhost" }, ""},
		{"the cloud metadata address", func(c *SFTPConfig) { c.Host = "169.254.169.254" }, "host"},
		{"AWS's IPv6 metadata address", func(c *SFTPConfig) { c.Host = "fd00:ec2::254" }, "host"},
		{"a link-local address", func(c *SFTPConfig) { c.Host = "fe80::1" }, "host"},
		{"a multicast address", func(c *SFTPConfig) { c.Host = "224.0.0.251" }, "host"},
		{"the unspecified address", func(c *SFTPConfig) { c.Host = "0.0.0.0" }, "host"},
		{"a user name in the host", func(c *SFTPConfig) { c.Host = "playkeeper@backup.example" }, "host"},
		{"a port in the host", func(c *SFTPConfig) { c.Host = "backup.example:22" }, "host"},
		{"a folder in the host", func(c *SFTPConfig) { c.Host = "backup.example/backups" }, "host"},
		{"an address with sftp://", func(c *SFTPConfig) { c.Host = "sftp://backup.example" }, "host"},
		{"an underscore in the host", func(c *SFTPConfig) { c.Host = "backup_1.example" }, "host"},
		{"two dots in the host", func(c *SFTPConfig) { c.Host = "backup..example" }, "host"},
		{"port 0 is 22", func(c *SFTPConfig) { c.Port = 0 }, ""},
		{"port 65535", func(c *SFTPConfig) { c.Port = 65535 }, ""},
		{"a negative port", func(c *SFTPConfig) { c.Port = -1 }, "port"},
		{"port 65536", func(c *SFTPConfig) { c.Port = 65536 }, "port"},
		{"no user", func(c *SFTPConfig) { c.User = "" }, "user"},
		{"a space in the user", func(c *SFTPConfig) { c.User = "play keeper" }, "user"},
		{"an accent in the user", func(c *SFTPConfig) { c.User = "joué" }, "user"},
		{"a user of 65 characters", func(c *SFTPConfig) { c.User = strings.Repeat("a", 65) }, "user"},
		{"dots and hyphens in the user", func(c *SFTPConfig) { c.User = "back.up-1" }, ""},
		{"an absolute folder", func(c *SFTPConfig) { c.Folder = "/srv/backups/playkeeper" }, ""},
		{"a trailing slash", func(c *SFTPConfig) { c.Folder = "backups/survival/" }, ""},
		{"spaces in the folder", func(c *SFTPConfig) { c.Folder = "Minecraft backups/survival" }, ""},
		{"no folder", func(c *SFTPConfig) { c.Folder = "" }, "folder"},
		{"the root folder", func(c *SFTPConfig) { c.Folder = "/" }, "folder"},
		{"the root folder with a dot", func(c *SFTPConfig) { c.Folder = "/." }, "folder"},
		{"two slashes for the root", func(c *SFTPConfig) { c.Folder = "//" }, "folder"},
		{"the root folder's parent", func(c *SFTPConfig) { c.Folder = "/../" }, "folder"},
		{"a tilde", func(c *SFTPConfig) { c.Folder = "~/backups" }, "folder"},
		{"two slashes in a row", func(c *SFTPConfig) { c.Folder = "backups//survival" }, "folder"},
		{"a dot folder", func(c *SFTPConfig) { c.Folder = "backups/./survival" }, "folder"},
		{"a parent folder", func(c *SFTPConfig) { c.Folder = "backups/../survival" }, "folder"},
		{"a backslash", func(c *SFTPConfig) { c.Folder = `backups\survival` }, "folder"},
		{"a line break in the folder", func(c *SFTPConfig) { c.Folder = "backups\nsurvival" }, "folder"},
		{"a folder that isn't UTF-8", func(c *SFTPConfig) { c.Folder = "backups/\xff" }, "folder"},
		{"a folder of 1025 characters", func(c *SFTPConfig) { c.Folder = "/" + strings.Repeat("a", 1024) }, "folder"},
		{"accents in the password", func(c *SFTPConfig) { c.Password = NewSecret("mot de passe à rallonge") }, ""},
		{"the key instead of a password", func(c *SFTPConfig) { c.Password, c.PrivateKey = Secret{}, key.PrivateKey }, ""},
		{"neither a password nor a key", func(c *SFTPConfig) { c.Password = Secret{} }, "password"},
		{"a line break in the password", func(c *SFTPConfig) { c.Password = NewSecret("hunter2\n") }, "password"},
		{"a tab in the password", func(c *SFTPConfig) { c.Password = NewSecret("hunter\t2") }, "password"},
		{"a password of 1025 characters", func(c *SFTPConfig) { c.Password = NewSecret(strings.Repeat("a", 1025)) }, "password"},
		{"a password that isn't UTF-8", func(c *SFTPConfig) { c.Password = NewSecret("\xff\xfe") }, "password"},
		{"both a password and a key", func(c *SFTPConfig) { c.PrivateKey = key.PrivateKey }, "privateKey"},
		{"a key with a passphrase", func(c *SFTPConfig) { c.Password, c.PrivateKey = Secret{}, locked }, "privateKey"},
		{"the public key as the private key", func(c *SFTPConfig) { c.Password, c.PrivateKey = Secret{}, NewSecret(key.PublicKey) }, "privateKey"},
		{"a password as the private key", func(c *SFTPConfig) { c.Password, c.PrivateKey = Secret{}, NewSecret("hunter2") }, "privateKey"},
		{"no host key yet", func(c *SFTPConfig) { c.HostKey = "" }, ""},
		{"an ECDSA host key", func(c *SFTPConfig) { c.HostKey = pinned("ecdsa") }, ""},
		{"an RSA host key", func(c *SFTPConfig) { c.HostKey = pinned("rsa") }, ""},
		{"a host key with a comment", func(c *SFTPConfig) { c.HostKey = pinned("ed25519") + " root@backup" }, ""},
		{"a known_hosts line", func(c *SFTPConfig) { c.HostKey = "backup.example " + pinned("ed25519") }, "hostKey"},
		{"a host key with options", func(c *SFTPConfig) { c.HostKey = "restrict " + pinned("ed25519") }, "hostKey"},
		{"two host keys", func(c *SFTPConfig) { c.HostKey = pinned("ed25519") + "\n" + pinned("ecdsa") }, "hostKey"},
		{"a short RSA host key", func(c *SFTPConfig) { c.HostKey = pinned("rsa 1024") }, "hostKey"},
		{"a fingerprint instead of the key", func(c *SFTPConfig) { c.HostKey = fingerprint("ed25519") }, "hostKey"},
		{"a host key over 16 KiB", func(c *SFTPConfig) { c.HostKey = pinned("ed25519") + " " + strings.Repeat("a", 16<<10) }, "hostKey"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mod(&cfg)
			err := cfg.Validate()
			_, nerr := newSFTP(cfg, nil, nil)
			if tc.field == "" {
				if err != nil || nerr != nil {
					t.Fatalf("Validate = %v, newSFTP = %v", err, nerr)
				}
				return
			}
			e := wantKind(t, err, KindInvalidConfig)
			if e.Field != tc.field || e.Op != opSetup || !strings.HasSuffix(e.Msg, ".") {
				t.Errorf("error %+v", e)
			}
			if ne := wantKind(t, nerr, KindInvalidConfig); ne.Field != tc.field {
				t.Errorf("newSFTP blames %q", ne.Field)
			}
		})
	}

	if err := (Config{Type: TypeSFTP, SFTP: base}).Validate(); err != nil {
		t.Errorf("Config.Validate = %v", err)
	}
	cfg := base
	cfg.Port = 0
	c, err := newSFTP(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.port != 22 {
		t.Errorf("port %d, want 22", c.port)
	}
	cfg = base
	cfg.Password, cfg.PrivateKey = Secret{}, locked
	if e := wantKind(t, cfg.Validate(), KindInvalidConfig); e.Msg != "The private key is protected by a passphrase, which Playkeeper can't type." {
		t.Errorf("error %q", e.Msg)
	}
}

func TestNewSSHKey(t *testing.T) {
	k, err := NewSSHKey("Survival World")
	if err != nil {
		t.Fatal(err)
	}
	pub, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(k.AuthorizedKey))
	if err != nil || comment != "playkeeper-survival-world" || !slices.Equal(options, []string{"restrict"}) || len(rest) != 0 {
		t.Fatalf("authorized_keys line %q: %v", k.AuthorizedKey, err)
	}
	if k.AuthorizedKey != "restrict "+k.PublicKey || pub.Type() != ssh.KeyAlgoED25519 || k.Fingerprint != ssh.FingerprintSHA256(pub) {
		t.Errorf("key %+v", k)
	}
	priv := k.PrivateKey.Reveal()
	if !strings.HasPrefix(priv, "-----BEGIN OPENSSH PRIVATE KEY-----\n") {
		t.Error("the private key isn't in OpenSSH's format")
	}
	signer, err := ssh.ParsePrivateKey([]byte(priv))
	if err != nil || !bytes.Equal(signer.PublicKey().Marshal(), pub.Marshal()) {
		t.Fatalf("the private key doesn't match the public key: %v", err)
	}
	js, err := json.Marshal(k)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(js), "PRIVATE") || strings.Contains(string(js), "privateKey") {
		t.Errorf("the JSON has the private key: %s", js)
	}
	again, err := NewSSHKey("Survival World")
	if err != nil {
		t.Fatal(err)
	}
	if again.PublicKey == k.PublicKey {
		t.Error("two keys are the same")
	}
	cfg := sftpSettings(hostKey("ed25519"))
	cfg.Password, cfg.PrivateKey = Secret{}, k.PrivateKey
	if err := cfg.Validate(); err != nil {
		t.Errorf("settings with the key: %v", err)
	}
}

func TestSFTPSecretsNeverShown(t *testing.T) {
	ctx := context.Background()
	key, err := NewSSHKey("survival")
	if err != nil {
		t.Fatal(err)
	}
	keys := newKeys(t)
	cfg := sftpSettings(hostKey("ed25519"))
	withKey := cfg
	withKey.Password, withKey.PrivateKey = Secret{}, key.PrivateKey

	var out bytes.Buffer
	for _, v := range []any{cfg, withKey, Config{Type: TypeSFTP, SFTP: withKey}, key, keys} {
		fmt.Fprintf(&out, "%v %+v %#v\n", v, v, v)
		js, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(js)
		slog.New(slog.NewJSONHandler(&out, nil)).Info("settings", "v", v)
		slog.New(slog.NewTextHandler(&out, nil)).Info("settings", "v", v)
	}
	for _, settings := range []SFTPConfig{cfg, withKey} {
		srv := newSSHServer(t, func(s *sshServer) { s.methods, s.password = []string{"password", "publickey"}, "Tr0ub4dor&3" })
		d, _ := srv.open(keys, settings)
		_, err := d.List(ctx)
		e := wantKind(t, err, KindLoginRefused)
		fmt.Fprintf(&out, "%v %+v %#v %v\n", err, e, e, e.Params())
		js, err := json.Marshal(d.Test(ctx))
		if err != nil {
			t.Fatal(err)
		}
		out.Write(js)
		fmt.Fprintf(&out, "%v %+v %#v\n", d.b, d.b, d.b)
	}

	s := out.String()
	if !strings.Contains(s, "[hidden]") {
		t.Error("the secrets aren't marked hidden")
	}
	for _, bad := range []string{`"password":`, `"privateKey":`, testPassword, keys.Current.Secret.Reveal()} {
		if strings.Contains(s, bad) {
			t.Errorf("the output shows %q", bad)
		}
	}
	for _, line := range strings.Split(key.PrivateKey.Reveal(), "\n") {
		if len(line) >= 16 && !strings.HasPrefix(line, "-----") && strings.Contains(s, line) {
			t.Error("the output shows the private key")
		}
	}
}

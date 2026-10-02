package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/panel"
)

// People run the install command again where Playkeeper already runs when
// its link didn't open, or to update. It said "Nothing to do" and stopped. It
// now gives the dashboard's link again, with a new setup code while the
// dashboard has no admin account (the install's works once and for 24
// hours), and changes nothing else.
func TestTheInstallCommandRunAgainGivesTheLinkAgain(t *testing.T) {
	type run struct {
		res     *Result
		out     string
		asked   []string
		owned   []string
		changed []string
	}
	again := func(t *testing.T, h *fakeHost, root bool, needs func() (bool, error)) run {
		t.Helper()
		var r run
		sys := h.system(t)
		sys.IsRoot = func() bool { return root }
		sys.NeedsSetup = func(_ context.Context, cert string, port int) (bool, error) {
			r.asked = append(r.asked, strings.TrimPrefix(cert, h.root)+" "+strconv.Itoa(port))
			return needs()
		}
		sys.Chown = func(p string, _, _ int) error {
			r.owned = append(r.owned, strings.TrimPrefix(p, h.root))
			return nil
		}
		before := snapshot(t, h.root)
		o := opts("")
		res, err := Run(context.Background(), sys, o, "0.4.17")
		if err != nil {
			t.Fatalf("running it again: %v\n%s", err, o.Out)
		}
		r.res, r.out, r.changed = res, o.Out.(*bytes.Buffer).String(), diff(before, snapshot(t, h.root))
		return r
	}
	installed := func(t *testing.T) (*fakeHost, config.Config) {
		t.Helper()
		h := newFakeHost(t)
		cfg := installedAt(t, h, "0.4.17", true)
		h.users[config.DefaultPanelUser] = true
		return h, cfg
	}
	yes := func() (bool, error) { return true, nil }
	no := func() (bool, error) { return false, nil }

	t.Run("before setup", func(t *testing.T) {
		h, cfg := installed(t)
		r := again(t, h, true, yes)
		if !r.res.UpToDate || r.res.SetupCode == "" || r.res.ExistingAdm {
			t.Fatalf("result: %+v", r.res)
		}
		if !strings.HasPrefix(r.res.URL, "https://") || !strings.HasSuffix(r.res.URL, ":8443") || r.res.PanelPort != 8443 || r.res.GamePort != 25565 {
			t.Errorf("the link, and the ports Won't open? names: %+v", r.res)
		}
		if !strings.Contains(r.out, "Playkeeper 0.4.17 is already installed on this server.\n") || strings.Contains(r.out, "Nothing to do") {
			t.Errorf("output:\n%s", r.out)
		}
		if want := []string{filepath.Join(cfg.TLSDir(), "cert.pem") + " 8443"}; !slices.Equal(r.asked, want) {
			t.Errorf("asked the panel %v, want %v", r.asked, want)
		}
		token := cfg.SetupTokenPath()
		if !slices.Equal(r.owned, []string{token}) || !slices.Equal(r.changed, []string{"added " + strings.TrimPrefix(token, "/")}) {
			t.Errorf("chowned %v; changed %v", r.owned, r.changed)
		}
		b, err := os.ReadFile(filepath.Join(h.root, token))
		var tok panel.SetupToken
		if err != nil || json.Unmarshal(b, &tok) != nil {
			t.Fatalf("the new code's file: %v %s", err, b)
		}
		sum := sha256.Sum256([]byte(r.res.SetupCode))
		if tok.Hash != hex.EncodeToString(sum[:]) || tok.ExpiresAt.Before(time.Now().Add(23*time.Hour)) {
			t.Errorf("the file isn't the printed code's for 24 hours: %+v", tok)
		}
	})
	t.Run("with its admin", func(t *testing.T) {
		h, _ := installed(t)
		r := again(t, h, true, no)
		if !r.res.ExistingAdm || r.res.SetupCode != "" || r.res.URL == "" || len(r.owned) != 0 || len(r.changed) != 0 {
			t.Errorf("result %+v; chowned %v; changed %v", r.res, r.owned, r.changed)
		}
	})
	t.Run("a panel that doesn't answer", func(t *testing.T) {
		h, _ := installed(t)
		r := again(t, h, true, func() (bool, error) { return false, errors.New("connection refused") })
		if r.res.ExistingAdm || r.res.SetupCode != "" || r.res.URL == "" || len(r.changed) != 0 {
			t.Errorf("result %+v; changed %v", r.res, r.changed)
		}
	})
	t.Run("without root", func(t *testing.T) {
		h, _ := installed(t)
		r := again(t, h, false, yes)
		if len(r.asked) != 0 || r.res.SetupCode != "" || r.res.URL == "" || len(r.changed) != 0 {
			t.Errorf("asked %v; result %+v; changed %v", r.asked, r.res, r.changed)
		}
	})
	t.Run("a joined machine", func(t *testing.T) {
		h, cfg := installed(t)
		cfg.NoPanel = true
		if err := cfg.Save(filepath.Join(h.root, ConfigDir, "config.json")); err != nil {
			t.Fatal(err)
		}
		r := again(t, h, true, yes)
		if !r.res.UpToDate || !r.res.NoPanel || r.res.URL != "" || len(r.asked) != 0 || len(r.changed) != 0 {
			t.Errorf("asked %v; result %+v; changed %v", r.asked, r.res, r.changed)
		}
	})
}

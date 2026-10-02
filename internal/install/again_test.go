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
	"github.com/CIYAhq/playkeeper/internal/usage"
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

// The install command run again where Playkeeper runs reports an install
// refused because it's installed, once it has said usage stats are on, so
// the stats tell those runs from the ones another check turned away. A
// machine whose stats are off, or a run that turns them off, sends nothing.
func TestTheInstallCommandRunAgainReportsItWasInstalled(t *testing.T) {
	for _, c := range []struct {
		name, setting string
		joined, off   bool
		want          []string
		kind          string
	}{
		{name: "usage stats on", want: []string{"refused:installed"}, kind: usage.KindDashboard},
		{name: "a joined machine", joined: true, want: []string{"refused:installed"}, kind: usage.KindJoined},
		{name: "off on the machine", setting: "off"},
		{name: "off for this run", off: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newFakeHost(t)
			cfg := installedAt(t, h, "0.4.17", true)
			cfg.UsageStats, cfg.NoPanel = c.setting, c.joined
			if err := cfg.Save(filepath.Join(h.root, ConfigDir, "config.json")); err != nil {
				t.Fatal(err)
			}
			var r reports
			o := withUsage(opts(""), &r, func(u *Usage) {
				if c.off {
					u.Choice, u.Why = usage.Off, usage.EnvSwitch
				}
			})
			if _, err := Run(context.Background(), h.system(t), o, "0.4.17"); err != nil {
				t.Fatalf("running it again: %v\n%s", err, o.Out)
			}
			if got := r.events(); !slices.Equal(got, c.want) {
				t.Fatalf("sent %v, want %v", got, c.want)
			}
			out := o.Out.(*bytes.Buffer).String()
			if said := strings.Index(out, "Anonymous usage stats are "); said < 0 || said > strings.Index(out, "is already installed") {
				t.Errorf("usage stats weren't named first:\n%s", out)
			}
			if c.want != nil {
				if e := r.all()[0]; e.Kind != c.kind || e.Version != "0.4.17" || e.Source != usage.SourceSite || e.Test {
					t.Errorf("report: %+v", e)
				}
			}
		})
	}
}

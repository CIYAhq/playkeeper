package install

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// The last lines before the question are a few plain ones, so what people
// see as they decide is what the install does, not the plan's details.
func TestTheQuestionComesRightAfterTheShortPlan(t *testing.T) {
	for _, c := range []struct {
		name string
		host func(*fakeHost)
		opts func(*Options)
		want []string
	}{
		{"a new server", nil, nil, []string{
			"In short:",
			"  • installs Docker and Playkeeper",
			"  • your dashboard on port 8443, and your first Minecraft server on 25565",
			"  • nothing else changes; undo it any time: sudo playkeeper uninstall (keeps your worlds and backups)",
		}},
		{"Docker already here, and ufw on", func(h *fakeHost) { h.dockerPresent, h.ufwActive = true, true }, nil, []string{
			"In short:",
			"  • installs Playkeeper",
			"  • your dashboard on port 8443, and your first Minecraft server on 25565",
			"  • opens ports 8443, 25565, 443 and 80 in ufw",
			"  • nothing else changes; undo it any time: sudo playkeeper uninstall (keeps your worlds and backups)",
		}},
		{"other ports", nil, func(o *Options) { o.PanelPort, o.GamePort = 9443, 25570 }, []string{
			"In short:",
			"  • installs Docker and Playkeeper",
			"  • your dashboard on port 9443, and your first Minecraft server on 25570",
			"  • nothing else changes; undo it any time: sudo playkeeper uninstall (keeps your worlds and backups)",
		}},
	} {
		h := newFakeHost(t)
		if c.host != nil {
			c.host(h)
		}
		o := opts("n\n")
		if c.opts != nil {
			c.opts(&o)
		}
		var out bytes.Buffer
		o.Out = &out
		if _, err := Run(context.Background(), h.system(t), o, "test"); !errors.Is(err, errDeclined) {
			t.Fatalf("%s: %v", c.name, err)
		}
		text := out.String()
		short, question := strings.Index(text, "\nIn short:\n"), strings.Index(text, "Proceed? [y/N] ")
		if short < 0 || question < short || short < strings.Index(text, "Playkeeper will make these changes:") {
			t.Fatalf("%s: no short plan between the plan and the question:\n%s", c.name, text)
		}
		if got := strings.Split(strings.TrimSpace(text[short:question]), "\n"); strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%s: right before the question:\n%s\nwant:\n%s", c.name, strings.Join(got, "\n"), strings.Join(c.want, "\n"))
		}
	}
}

func TestAJoinedMachinesShortPlanNamesItsDashboard(t *testing.T) {
	o := opts("")
	o.Join = "alex.playkeeper.me:8443"
	got := strings.Join(Short(Facts{DockerPresent: true}, o), "\n")
	if !strings.Contains(got, "  • joins your dashboard at alex.playkeeper.me:8443; Minecraft servers here use port 25565") || strings.Contains(got, "your dashboard on port") {
		t.Errorf("%s", got)
	}
	if got := strings.Join(Short(Facts{ReuseData: true}, opts("")), "\n"); !strings.Contains(got, "  • keeps the worlds and backups already here") {
		t.Errorf("a reinstall's short plan doesn't say it keeps the worlds:\n%s", got)
	}
}

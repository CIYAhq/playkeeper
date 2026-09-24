package install

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

type UninstallOptions struct {
	Yes bool
	// Purge also deletes /var/lib/playkeeper (worlds and backups). It needs
	// its own confirmation: typing the phrase, or PurgeConfirmed.
	Purge          bool
	PurgeConfirmed bool
	KeepDocker     bool
	In             io.Reader
	Out            io.Writer
}

const PurgePhrase = "delete my worlds"

func loadManifest(sys System) (*Manifest, error) {
	b, err := os.ReadFile(sys.P(filepath.Join(config.DefaultDataDir, "install-manifest.json")))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Uninstall removes everything the manifest lists. Worlds, backups and
// settings in /var/lib/playkeeper are kept unless purge is confirmed.
func Uninstall(ctx context.Context, sys System, o UninstallOptions) error {
	out := o.Out
	if !sys.IsRoot() {
		return errors.New("run the uninstaller as root (sudo playkeeper uninstall)")
	}
	m, err := loadManifest(sys)
	if err != nil {
		return fmt.Errorf("no install manifest found (%v); Playkeeper does not look installed here", err)
	}
	rd := bufio.NewReader(o.In)
	fmt.Fprintln(out, "Playkeeper uninstall will remove:")
	fmt.Fprintln(out, "  • services: "+strings.Join(m.Units, ", "))
	fmt.Fprintln(out, "  • the Minecraft container 'playkeeper-minecraft', the 'playkeeper' Docker network and the pinned server image")
	for _, f := range m.FilesCreated {
		fmt.Fprintln(out, "  • "+f)
	}
	if len(m.UsersCreated) > 0 {
		fmt.Fprintln(out, "  • users: "+strings.Join(m.UsersCreated, ", "))
	}
	if len(m.FirewallRules) > 0 {
		fmt.Fprintln(out, "  • ufw rules: "+strings.Join(m.FirewallRules, ", "))
	}
	if len(m.PackagesInstalled) > 0 && !o.KeepDocker {
		fmt.Fprintln(out, "  • packages Playkeeper installed: "+strings.Join(m.PackagesInstalled, " ")+" (only if no other containers use Docker)")
	}
	if o.Purge {
		fmt.Fprintln(out, "  • EVERYTHING in /var/lib/playkeeper, including your worlds and backups (--purge)")
	} else {
		fmt.Fprintln(out, "It will keep /var/lib/playkeeper (your worlds, backups and settings), so a reinstall can pick them up.")
	}
	if !o.Yes {
		fmt.Fprint(out, "Proceed? [y/N] ")
		ans, _ := rd.ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
			return errors.New("uninstall cancelled; nothing was changed")
		}
	}
	if o.Purge && !o.PurgeConfirmed {
		fmt.Fprintf(out, "Deleting worlds cannot be undone. Type %q to confirm: ", PurgePhrase)
		ans, _ := rd.ReadString('\n')
		if strings.TrimSpace(ans) != PurgePhrase {
			return errors.New("purge not confirmed; nothing was changed")
		}
	}
	var problems []string
	note := func(err error) {
		if err != nil {
			problems = append(problems, err.Error())
		}
	}
	for _, u := range []string{PanelUnit, AgentUnit} {
		if contains(m.Units, u) {
			_, err := sys.Run("systemctl", "disable", "--now", u)
			note(err)
		}
	}
	foreign := removeDockerObjects(ctx, sys, note)
	for _, r := range m.FirewallRules {
		_, err := sys.Run("ufw", "delete", "allow", r)
		note(err)
	}
	for _, f := range m.FilesCreated {
		note(removeIfExists(sys.P(f)))
	}
	if len(m.Units) > 0 {
		_, err := sys.Run("systemctl", "daemon-reload")
		note(err)
	}
	for _, u := range m.UsersCreated {
		_, err := sys.Run("userdel", u)
		note(err)
	}
	for _, g := range m.GroupsCreated {
		if _, err := sys.Run("groupdel", g); err != nil && !strings.Contains(err.Error(), "does not exist") {
			note(err)
		}
	}
	if len(m.PackagesInstalled) > 0 && !o.KeepDocker {
		if foreign > 0 {
			fmt.Fprintf(out, "Keeping Docker: %d other container(s) still use it.\n", foreign)
		} else {
			note(purgeDocker(sys, m.PackagesInstalled))
		}
	}
	note(removeIfExists(sys.P(ConfigDir)))
	note(removeIfExists(sys.P(filepath.Join(config.DefaultDataDir, "install-manifest.json"))))
	if o.Purge {
		note(os.RemoveAll(sys.P(config.DefaultDataDir)))
		fmt.Fprintln(out, "Deleted /var/lib/playkeeper.")
	} else {
		fmt.Fprintln(out, "Kept /var/lib/playkeeper (worlds in server/data, backups in backups/).")
	}
	if len(problems) > 0 {
		return fmt.Errorf("uninstall finished with problems:\n  - %s", strings.Join(problems, "\n  - "))
	}
	fmt.Fprintln(out, "Playkeeper was removed.")
	return nil
}

// removeDockerObjects deletes only Playkeeper-labelled objects and returns how
// many unrelated containers remain.
func removeDockerObjects(ctx context.Context, sys System, note func(error)) int {
	if sys.Root != "/" {
		return 0
	}
	c := docker.New("/var/run/docker.sock")
	if _, err := c.Negotiate(ctx); err != nil {
		return 0
	}
	foreign := 0
	list, err := c.ContainerList(ctx, true)
	if err != nil {
		note(err)
		return 0
	}
	for _, ct := range list {
		if ct.Labels["io.playkeeper.managed"] == "true" {
			note(c.ContainerRemove(ctx, ct.ID, true))
		} else {
			foreign++
		}
	}
	if n, err := c.NetworkInspect(ctx, "playkeeper"); err == nil && n.Labels["io.playkeeper.managed"] == "true" {
		note(c.NetworkRemove(ctx, "playkeeper"))
	}
	if err := c.ImageRemove(ctx, minecraft.Image); err != nil && !docker.IsNotFound(err) {
		note(err)
	}
	return foreign
}

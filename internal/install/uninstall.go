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
	"slices"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/machinelink"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/netguard"
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

// dockerEngines are the packages that are Docker itself; the others it came
// with may stay for software that needs them without keeping Docker.
var dockerEngines = []string{"docker.io", "docker-ce", "docker"}

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
	cfg := config.Default()
	joined, _ := machinelink.LoadDashboard(sys.P(cfg.LinkDashboardPath()))
	fmt.Fprintln(out, "Playkeeper uninstall will remove:")
	if joined.Address != "" {
		fmt.Fprintln(out, "  • this machine from the dashboard at "+joined.Address+", where it is "+joined.Name+" (it leaves first)")
	}
	if len(m.Units) > 0 {
		fmt.Fprintln(out, "  • services: "+strings.Join(m.Units, ", "))
	}
	fmt.Fprintln(out, "  • Playkeeper's Minecraft containers, the 'playkeeper' Docker network with the firewall rules that keep its servers from this machine, and the pinned server image")
	for _, f := range m.FilesCreated {
		fmt.Fprintln(out, "  • "+f)
	}
	if len(m.UsersCreated) > 0 {
		fmt.Fprintln(out, "  • users: "+strings.Join(m.UsersCreated, ", "))
	}
	fw, pm := firewallOf(*m), packageManagerOf(*m)
	if len(m.FirewallRules) > 0 {
		fmt.Fprintln(out, "  • "+fw.short()+" rules"+fw.where()+": "+strings.Join(m.FirewallRules, ", "))
	}
	removeDocker := len(m.PackagesInstalled) > 0 && !o.KeepDocker
	if removeDocker {
		fmt.Fprintln(out, "  • packages Playkeeper installed: "+strings.Join(m.PackagesInstalled, " ")+" (only if no other containers use Docker)")
		fmt.Fprintln(out, "    and with them Docker's firewall rules and bridges; IP forwarding and the FORWARD policy go back to how they were")
		if len(m.DockerDirsCreated) > 0 {
			fmt.Fprintln(out, "    and the folders installing Docker created, with everything in them: "+strings.Join(m.DockerDirsCreated, ", "))
		}
		if len(m.DockerRepoFiles) > 0 {
			fmt.Fprintln(out, "    and the repository and signing key Docker came from: "+strings.Join(m.DockerRepoFiles, ", "))
		}
		if len(m.DockerFirewalld) > 0 {
			fmt.Fprintln(out, "    and what Docker added to firewalld: "+strings.Join(m.DockerFirewalld, ", "))
		}
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
	if removeDocker {
		if err := waitForPackageLock(sys, out, pm, sys.Now().Add(lockWait)); err != nil {
			return fmt.Errorf("%w. Nothing was changed", err)
		}
	}
	var problems []string
	note := func(err error) {
		if err != nil {
			problems = append(problems, err.Error())
		}
	}
	if joined.Address != "" {
		fmt.Fprintln(out, "Leaving the dashboard at "+joined.Address+"…")
		left, err := Leave(ctx, sys, cfg, true)
		if left.Untold != nil {
			problems = append(problems, fmt.Sprintf("the dashboard at %s was not told that this machine left (%v); remove %s there, in Settings › Machines",
				joined.Address, left.Untold, joined.Name))
		}
		if !errors.Is(err, ErrNotJoined) {
			note(err)
		}
	}
	// The updater's units and the link's are removed whenever they exist:
	// an upgrade may not have recorded them in the manifest, and joining a
	// dashboard installs the link's.
	extra := map[string]bool{}
	for _, u := range []string{LinkUnit, UpdatePathUnit, UpdateServiceUnit} {
		if _, err := os.Stat(sys.P(UnitDir + "/" + u)); err == nil {
			extra[u] = true
		}
	}
	for _, u := range []string{LinkUnit, UpdatePathUnit, UpdateServiceUnit, PanelUnit, AgentUnit} {
		if contains(m.Units, u) || extra[u] {
			_, err := sys.Run("systemctl", "disable", "--now", u)
			note(err)
		}
	}
	for u := range extra {
		note(removeIfExists(sys.P(UnitDir + "/" + u)))
	}
	foreign := removeDockerObjects(ctx, sys, note)
	if sys.Firewall != nil {
		note(netguard.Remove(ctx, sys.Firewall))
	}
	for _, r := range m.FirewallRules {
		note(fw.remove(sys, r))
	}
	note(fw.tidy(sys, *m))
	// The binary, and the link sudo finds it by, stay until Docker is gone.
	for _, f := range m.FilesCreated {
		if f != BinPath && f != SudoLink {
			note(removeIfExists(sys.P(f)))
		}
	}
	// Staged updates and the copy of the previous version are not user data.
	note(os.RemoveAll(sys.P(UpdateDir(config.Default()))))
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
	keptRepo := ""
	if len(m.DockerRepoFiles) > 0 {
		keptRepo = " Docker keeps getting updates from " + m.DockerRepoFiles[len(m.DockerRepoFiles)-1] + ", which stays as well."
	}
	switch {
	case len(m.PackagesInstalled) == 0:
		// The Docker that was there already doesn't need the repository.
		note(removeFiles(sys, m.DockerRepoFiles))
	case o.KeepDocker:
		fmt.Fprintln(out, "Keeping Docker (--keep-docker), and with it its firewall rules and docker0 bridge."+keptRepo)
	case foreign > 0:
		fmt.Fprintf(out, "Keeping Docker: %d other container(s) still use it, so its firewall rules and docker0 bridge stay too.%s\n", foreign, keptRepo)
	default:
		remove, kept, why := pm.removable(sys, m.PackagesInstalled)
		if slices.ContainsFunc(kept, func(p string) bool { return slices.Contains(dockerEngines, p) }) {
			fmt.Fprintf(out, "Keeping Docker: other software needs it (%s), so its firewall rules and docker0 bridge stay too.%s\n", why, keptRepo)
			break
		}
		if len(kept) > 0 {
			fmt.Fprintf(out, "Keeping %s, which Docker came with: %s.\n", strings.Join(kept, ", "), why)
			m.PackagesInstalled = remove
		}
		left, err := purgeDocker(sys, out, m)
		problems = append(problems, left...)
		if err == nil {
			note(removeDockerLeftovers(sys, m))
		}
		if err != nil {
			// Keep what a second run needs: this binary and a manifest that
			// now lists only Docker.
			rest := Manifest{Version: m.Version, InstalledAt: m.InstalledAt, InstallID: m.InstallID, PanelPort: m.PanelPort, GamePort: m.GamePort,
				PackagesInstalled: m.PackagesInstalled, PackageManager: m.PackageManager, NetBeforeDocker: m.NetBeforeDocker, KeptOnUninstall: m.KeptOnUninstall,
				DockerGroupCreated: m.DockerGroupCreated, DockerDirsCreated: m.DockerDirsCreated, DockerRepoFiles: m.DockerRepoFiles, DockerFirewalld: m.DockerFirewalld}
			for _, f := range []string{BinPath, SudoLink} {
				if contains(m.FilesCreated, f) {
					rest.FilesCreated = append(rest.FilesCreated, f)
				}
			}
			b, _ := json.MarshalIndent(rest, "", "  ")
			note(os.WriteFile(sys.P(filepath.Join(config.DefaultDataDir, "install-manifest.json")), append(b, '\n'), 0o600))
			problems = append(problems, fmt.Sprintf("Docker was not removed: %v", err),
				"everything else was removed; run `sudo playkeeper uninstall` again to finish, or remove Docker by hand: "+pm.removeHint(m.PackagesInstalled))
			return fmt.Errorf("uninstall finished with problems:\n  - %s", strings.Join(problems, "\n  - "))
		}
	}
	for _, f := range []string{SudoLink, BinPath} {
		if contains(m.FilesCreated, f) {
			note(removeIfExists(sys.P(f)))
		}
	}
	note(removeIfExists(sys.P(ConfigDir)))
	note(removeIfExists(sys.P(filepath.Join(config.DefaultDataDir, "install-manifest.json"))))
	if o.Purge {
		note(os.RemoveAll(sys.P(config.DefaultDataDir)))
		fmt.Fprintln(out, "Deleted /var/lib/playkeeper.")
	} else {
		fmt.Fprintln(out, "Kept /var/lib/playkeeper (worlds in servers/, and server/data from before 0.3.0; backups in backups/).")
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
	for _, img := range minecraft.Runtimes() {
		if err := c.ImageRemove(ctx, img); err != nil && !docker.IsNotFound(err) {
			note(err)
		}
	}
	return foreign
}

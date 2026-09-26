// Package config loads the host configuration shared by the agent and panel.
//
// The file lives at /etc/playkeeper/config.json on installed hosts. It holds
// no secrets: secrets are generated on the host and kept in per-service
// directories under DataDir with restrictive permissions.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	DefaultPath         = "/etc/playkeeper/config.json"
	DefaultDataDir      = "/var/lib/playkeeper"
	DefaultSocketPath   = "/run/playkeeper/agent.sock"
	DefaultDockerSocket = "/var/run/docker.sock"
	DefaultPanelPort    = 8443
	DefaultGamePort     = 25565
	DefaultPanelUser    = "playkeeper"
	DefaultGameUser     = "playkeeper-mc"
)

type Config struct {
	PanelPort int `json:"panelPort"`
	// PanelBind is the listen address for the panel; empty means all interfaces.
	PanelBind string `json:"panelBind,omitempty"`
	GamePort  int    `json:"gamePort"`
	// Domain enables automatic Let's Encrypt certificates (panel must own :443).
	Domain           string `json:"domain,omitempty"`
	ACMEEmail        string `json:"acmeEmail,omitempty"`
	ACMEDirectoryURL string `json:"acmeDirectoryURL,omitempty"`
	DataDir          string `json:"dataDir"`
	SocketPath       string `json:"socketPath"`
	DockerSocket     string `json:"dockerSocket"`
	// PanelUser is the only non-root account allowed to call the agent socket.
	PanelUser string `json:"panelUser"`
	GameUID   int    `json:"gameUID"`
	GameGID   int    `json:"gameGID"`
	InstallID string `json:"installID"`
	// ReleaseURL is where the agent looks for updates; empty means the latest
	// GitHub release. Updates are only installed if they are signed.
	ReleaseURL string `json:"releaseURL,omitempty"`
	// Dev relaxes host checks for `playkeeper dev`; never set by the installer.
	Dev bool `json:"dev,omitempty"`
}

func Default() Config {
	return Config{
		PanelPort:    DefaultPanelPort,
		GamePort:     DefaultGamePort,
		DataDir:      DefaultDataDir,
		SocketPath:   DefaultSocketPath,
		DockerSocket: DefaultDockerSocket,
		PanelUser:    DefaultPanelUser,
	}
}

func Load(path string) (Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		return c, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("parse config %s: %w", path, err)
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	var errs []error
	if c.PanelPort < 1 || c.PanelPort > 65535 {
		errs = append(errs, fmt.Errorf("panelPort %d out of range", c.PanelPort))
	}
	if c.GamePort < 1 || c.GamePort > 65535 {
		errs = append(errs, fmt.Errorf("gamePort %d out of range", c.GamePort))
	}
	if c.PanelPort == c.GamePort {
		errs = append(errs, errors.New("panelPort and gamePort must differ"))
	}
	if !filepath.IsAbs(c.DataDir) {
		errs = append(errs, fmt.Errorf("dataDir must be absolute, got %q", c.DataDir))
	}
	if !filepath.IsAbs(c.SocketPath) {
		errs = append(errs, fmt.Errorf("socketPath must be absolute, got %q", c.SocketPath))
	}
	return errors.Join(errs...)
}

func (c Config) Save(path string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func (c Config) AgentDir() string       { return filepath.Join(c.DataDir, "agent") }
func (c Config) PanelDir() string       { return filepath.Join(c.DataDir, "panel") }
func (c Config) ServerDataDir() string  { return filepath.Join(c.DataDir, "server", "data") }
func (c Config) BackupsDir() string     { return filepath.Join(c.DataDir, "backups") }
func (c Config) StagingDir() string     { return filepath.Join(c.DataDir, "restore-staging") }
func (c Config) RCONSecretPath() string { return filepath.Join(c.AgentDir(), "rcon.secret") }
func (c Config) TLSDir() string         { return filepath.Join(c.PanelDir(), "tls") }
func (c Config) SetupTokenPath() string { return filepath.Join(c.PanelDir(), "setup-token.sha256") }
func (c Config) ManifestPath() string   { return filepath.Join(c.DataDir, "install-manifest.json") }

// ResourcePacksDir holds the resource packs servers offer players. The agent
// writes it; the panel serves the packs from it, so it is readable by all.
func (c Config) ResourcePacksDir() string { return filepath.Join(c.DataDir, "resourcepacks") }

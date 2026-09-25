package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/user"
	"regexp"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/mcp"
	"github.com/CIYAhq/playkeeper/internal/mcptools"
	"github.com/CIYAhq/playkeeper/internal/version"
)

// "playkeeper mcp" serves Playkeeper's tools to one AI assistant over stdin
// and stdout, for an assistant that reaches this machine over SSH:
//
//	ssh alice@my-vps sudo playkeeper mcp
//
// It has no sign-in of its own: whoever runs it as root may do everything to
// this machine's servers, console included, as root could anyway. A sudo
// rule for it therefore makes that account an owner. As root it takes no
// arguments and reads nothing from the environment that could point it at
// another config, socket or identity; only the account that ran sudo is
// read, to name it in the activity log.

// reCLIUser is an account name fit for an actor label.
var reCLIUser = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,31}$`)

// mcpCaller is who "playkeeper mcp" acts as, and the config it reads.
type mcpCaller struct {
	root       bool
	configPath string
	actor      string
}

// mcpCallerFor works out who runs "playkeeper mcp". Root gets the installed
// config and no arguments at all. Anyone else must name a config, which
// mcpConfig requires to be a dev one: only the agent of "playkeeper dev"
// lets its user in, and that agent is the user's own.
func mcpCallerFor(args []string, euid int, getenv func(string) string, username string) (mcpCaller, error) {
	if euid == 0 {
		if len(args) > 0 {
			return mcpCaller{}, errors.New("as root, playkeeper mcp takes no arguments: sudo playkeeper mcp")
		}
		actor := "cli:root"
		if u := getenv("SUDO_USER"); reCLIUser.MatchString(u) {
			actor = "cli:" + u
		}
		return mcpCaller{root: true, configPath: config.DefaultPath, actor: actor}, nil
	}
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("config", "", "the config of a dev agent")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || *path == "" {
		return mcpCaller{}, errors.New("run as root: sudo playkeeper mcp")
	}
	actor := "cli:dev"
	if reCLIUser.MatchString(username) {
		actor = "cli:" + username
	}
	return mcpCaller{configPath: *path, actor: actor}, nil
}

func mcpConfig(c mcpCaller) (config.Config, error) {
	cfg, err := config.Load(c.configPath)
	if err != nil {
		return cfg, err
	}
	if !c.root && !cfg.Dev {
		return cfg, errors.New("run as root: sudo playkeeper mcp (without root, only a dev config works)")
	}
	return cfg, nil
}

func runMCP(args []string) error {
	var username string
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	c, err := mcpCallerFor(args, os.Geteuid(), os.Getenv, username)
	if err != nil {
		return err
	}
	cfg, err := mcpConfig(c)
	if err != nil {
		return err
	}
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		fmt.Fprintln(os.Stderr, "playkeeper mcp talks to an AI assistant over stdin and stdout; add it to the assistant's MCP settings. Ctrl-D stops it.")
	}
	ctx, cancel := signalContext()
	defer cancel()
	return serveMCP(ctx, cfg.SocketPath, c.actor, os.Stdin, os.Stdout, logger())
}

// serveMCP serves the tools over in and out, with an owner's rights on the
// servers of the agent at socket. Logs go to log, never to out.
func serveMCP(ctx context.Context, socket, actor string, in io.Reader, out io.Writer, log *slog.Logger) error {
	srv, err := mcp.New(mcp.Options{Name: "playkeeper", Version: version.Version, Instructions: mcptools.Instructions,
		Tools: mcptools.Tools(mcptools.NewLocal(agentclient.New(socket))), Logger: log})
	if err != nil {
		return err
	}
	return srv.ServeStdio(ctx, mcp.Principal{ID: actor, Name: "command line", Scopes: []mcp.Scope{mcp.ScopeOwner}}, in, out)
}

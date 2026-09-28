// Command template-check creates a server from each template in
// site/data/templates on a running Playkeeper, starts it, reads its log and
// its add-ons, and records what happened in site/data/checks. A template
// passes when its server reaches Done with every add-on installed and no
// plugin or mod failing to load.
//
// CI runs it on every template PR, nightly and on each release
// (.github/workflows/templates.yml). Agents run it against `playkeeper dev`
// before they open a template PR:
//
//	playkeeper dev --dir /tmp/pk &
//	go run ./cmd/template-check -socket /tmp/pk/agent.sock -only towny -write -pin
//
// -pin rewrites each passing template's add-ons to the exact versions that
// installed. -bump first moves them to the newest versions that fit, so the
// weekly run finds the updates worth a PR.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/templates/checks"
	"github.com/CIYAhq/playkeeper/internal/update"
)

func main() {
	socket := flag.String("socket", "", "the Playkeeper agent's socket, such as /tmp/pk/agent.sock for `playkeeper dev --dir /tmp/pk`")
	root := flag.String("root", ".", "the repository")
	only := flag.String("only", "", "template ids to check, comma-separated; every template when empty")
	shard := flag.String("shard", "", "check only shard k of n (k/n), the templates split by memory")
	out := flag.String("out", "", "write the results as JSON to this file")
	write := flag.Bool("write", false, "write each template's check to site/data/checks")
	pin := flag.Bool("pin", false, "rewrite each passing template's add-ons to the exact versions that installed")
	bump := flag.Bool("bump", false, "move each template's add-ons to their newest versions first, then check and pin them")
	verify := flag.Bool("verify", false, "fail when a template's check in site/data/checks disagrees with this run")
	flag.Parse()
	if *socket == "" {
		fmt.Fprintln(os.Stderr, "template-check: -socket is the agent's socket, such as /tmp/pk/agent.sock")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	opts := options{root: *root, only: split(*only), shard: *shard, write: *write, pin: *pin || *bump, bump: *bump, verify: *verify}
	c := &checker{agent: agentclient.New(*socket), sources: newSources(), actor: "template-check", now: time.Now, poll: 2 * time.Second}
	results, err := run(ctx, c, opts)
	if err == nil && *out != "" {
		err = writeResults(*out, c.release, results)
	}
	summary(os.Stdout, c.release, results)
	if f := os.Getenv("GITHUB_STEP_SUMMARY"); f != "" {
		if s, e := os.OpenFile(f, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644); e == nil {
			summary(s, c.release, results)
			s.Close()
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "template-check:", err)
		os.Exit(1)
	}
	for _, r := range results {
		if r.Status == statusFailing {
			os.Exit(1)
		}
	}
}

type options struct {
	root                     string
	only                     []string
	shard                    string
	write, pin, bump, verify bool
}

// run checks the templates the options pick, one after another, and writes
// what they ask for.
func run(ctx context.Context, c *checker, o options) ([]result, error) {
	var info api.UpdateInfo
	if _, err := c.agent.Do(ctx, "GET", "/v1/update", nil, nil, &info); err != nil {
		return nil, fmt.Errorf("the agent at the socket doesn't answer: %w", err)
	}
	c.release = info.Current
	released := isRelease(c.release)
	if (o.write || o.verify) && !released {
		return nil, fmt.Errorf("checks name the release they ran on, and %q isn't one: run against a released Playkeeper", c.release)
	}
	all, err := loadTemplates(o.root)
	if err != nil {
		return nil, err
	}
	ids, err := pick(all, o.only, o.shard)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(o.root, "site", "data", "checks")
	var committed map[string]*checks.Check
	if o.verify {
		if committed, err = checks.Read(os.DirFS(o.root), "site/data/checks"); err != nil {
			return nil, err
		}
	}
	var results []result
	for _, id := range ids {
		t := all[id]
		if t.opensFrom != "" && released && newer(t.opensFrom, c.release) {
			results = append(results, result{ID: id, Status: statusSkipped, Failure: "opens from Playkeeper " + t.opensFrom})
			continue
		}
		fmt.Fprintf(os.Stderr, "checking %s…\n", id)
		r := c.check(ctx, id, t, o.bump, o.pin)
		if ctx.Err() != nil {
			return results, ctx.Err()
		}
		if o.verify {
			if msg := disagree(committed[id], r); msg != "" {
				r.Status, r.Failure = statusFailing, msg
			}
		}
		results = append(results, r)
		if o.write && r.Check != nil {
			if err := checks.Write(dir, id, r.Check); err != nil {
				return results, err
			}
		}
		if o.pin && r.pinned != nil {
			if err := os.WriteFile(t.path, r.pinned, 0o644); err != nil {
				return results, err
			}
		}
	}
	return results, nil
}

// disagree says how a template's committed check differs from this run in
// what the check stands for: whether it passes and which versions install.
// Days, seconds, builds and downloads move on their own.
func disagree(was *checks.Check, r result) string {
	if r.Check == nil {
		return ""
	}
	switch {
	case was == nil:
		return "site/data/checks has no check for it: run template-check with -write and commit the file"
	case was.Status != r.Check.Status:
		return fmt.Sprintf("site/data/checks says %s, and it's %s now", was.Status, r.Check.Status)
	}
	versions := func(c *checks.Check) []string {
		var v []string
		for _, a := range c.Addons {
			v = append(v, a.Slug+" "+a.Version)
		}
		if c.Modpack != nil {
			v = append(v, c.Modpack.Name+" "+c.Modpack.Version)
		}
		return v
	}
	if a, b := versions(was), versions(r.Check); !slices.Equal(a, b) {
		return fmt.Sprintf("site/data/checks lists %s, and %s installed: run template-check with -write and commit the file", strings.Join(a, ", "), strings.Join(b, ", "))
	}
	return ""
}

func isRelease(v string) bool {
	_, err := update.ParseVersion(v)
	return err == nil
}

// newer reports whether release a comes after b.
func newer(a, b string) bool {
	c, err := update.CompareVersions(a, b)
	return err == nil && c > 0
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func writeResults(file, release string, results []result) error {
	b, err := json.MarshalIndent(struct {
		Release   string   `json:"release"`
		Templates []result `json:"templates"`
	}{release, results}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, append(b, '\n'), 0o644)
}

// summary writes the results as a Markdown table, for the terminal and the
// workflow's summary.
func summary(w io.Writer, release string, results []result) {
	if len(results) == 0 {
		fmt.Fprintln(w, "No templates to check.")
		return
	}
	fmt.Fprintf(w, "\n### Templates on Playkeeper %s\n\n| Template | Result | Ready in | What went wrong |\n|---|---|---|---|\n", release)
	for _, r := range results {
		ready := ""
		if r.Check != nil && r.Check.DoneSeconds > 0 {
			ready = fmt.Sprintf("%.0f s", r.Check.DoneSeconds)
		}
		fmt.Fprintf(w, "| %s | %s | %s | %s |\n", r.ID, r.Status, ready, strings.ReplaceAll(r.Failure, "|", "/"))
	}
	fmt.Fprintln(w)
}

var errNoTemplates = errors.New("no templates match")

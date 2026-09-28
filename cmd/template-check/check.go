package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/templates"
	"github.com/CIYAhq/playkeeper/internal/templates/checks"
)

const (
	statusPassing = "passing"
	statusFailing = "failing"
	statusSkipped = "skipped"
)

// checker creates, starts and removes servers through the agent's API.
type checker struct {
	agent   *agentclient.Client
	sources *sources
	actor   string
	now     func() time.Time
	poll    time.Duration
	// retryAfter is how long to wait before checking a template again that
	// failed for a reason that passes, like a source's rate limit.
	retryAfter time.Duration
	// release is the agent's version, as it answered.
	release string
}

// result is one template's check.
type result struct {
	ID      string        `json:"id"`
	Status  string        `json:"status"`
	Failure string        `json:"failure,omitempty"`
	Check   *checks.Check `json:"check,omitempty"`
	// Warnings are error lines in the log that didn't make it fail.
	Warnings []string `json:"warnings,omitempty"`
	// CrossplayFailure says why crossplay didn't turn on for a passing
	// template's server, where the release offers it.
	CrossplayFailure string  `json:"crossplayFailure,omitempty"`
	Seconds          float64 `json:"seconds"`
	// pinned is the template file with the exact versions that installed,
	// when -pin asked for it.
	pinned []byte
}

// check runs one template: plan, create, start, read the log and the
// add-ons, and remove the server whatever happened.
func (c *checker) check(ctx context.Context, id string, f *templateFile, bump, pin bool) (r result) {
	began := c.now()
	r = result{ID: id}
	defer func() { r.Seconds = c.now().Sub(began).Round(time.Second).Seconds() }()
	fail := func(format string, a ...any) result {
		r.Status, r.Failure, r.pinned = statusFailing, fmt.Sprintf(format, a...), nil
		r.Check = &checks.Check{Status: checks.Failing, Failure: r.Failure, Checked: c.now().UTC().Format(time.DateOnly), Release: c.release}
		return r
	}
	t := f.t
	file := f.raw
	if bump {
		t = floated(f.t)
		b, err := json.Marshal(t)
		if err != nil {
			return fail("%v", err)
		}
		file = b
	}
	plan, err := c.plan(ctx, file)
	if err != nil {
		return fail("planning it: %v", err)
	}
	if !plan.Ready {
		var why []string
		for _, b := range plan.Blockers {
			why = append(why, b.Message)
		}
		return fail("the plan isn't ready: %s", strings.Join(why, " "))
	}
	name := serverName(id)
	if err := c.removeNamed(ctx, name); err != nil {
		return fail("removing a server left from an earlier check: %v", err)
	}
	var op api.Operation
	req := api.CreateServerRequest{Name: name, AcceptEULA: true, MemoryMB: plan.MemoryMB, Template: &api.TemplateRef{Fingerprint: plan.Fingerprint}, Actor: c.actor}
	if _, err := c.agent.Do(ctx, "POST", "/v1/servers", nil, req, &op); err != nil {
		return fail("creating it: %v", err)
	}
	done, err := c.wait(ctx, op.ID, 30*time.Minute)
	if done.ServerID != "" {
		defer c.remove(context.WithoutCancel(ctx), done.ServerID, name)
	}
	if err != nil {
		why := ""
		if done.ServerID != "" {
			lines, _ := c.logs(ctx, done.ServerID)
			why = explain(lines)
		}
		return fail("creating it: %v%s", err, why)
	}
	sid := done.ServerID
	if _, err := c.agent.Do(ctx, "POST", "/v1/servers/"+sid+"/start", nil, map[string]string{"actor": c.actor}, nil); err != nil {
		return fail("starting it: %v", err)
	}
	limit := 5 * time.Minute
	if minecraft.ModLoader(plan.Type) {
		limit = 20 * time.Minute
	}
	if err := c.waitOnline(ctx, sid, limit); err != nil {
		lines, _ := c.logs(ctx, sid)
		return fail("%v%s", err, lastLines(lines, 3))
	}
	lines, err := c.logs(ctx, sid)
	if err != nil {
		return fail("reading its log: %v", err)
	}
	log := readLog(lines)
	r.Warnings = log.errors
	if len(log.failures) > 0 {
		return fail("its log says: %s", strings.Join(log.failures, " / "))
	}
	if log.done <= 0 {
		return fail("its log never said Done%s", lastLines(lines, 3))
	}
	var installed api.Addons
	if _, err := c.agent.Do(ctx, "GET", "/v1/servers/"+sid+"/addons", nil, nil, &installed); err != nil {
		return fail("listing its add-ons: %v", err)
	}
	// The check lists the template as it will be committed: with -pin, its
	// pinned add-ons and the dependencies the server installed for them.
	recorded := t
	if pin {
		p, err := c.pinned(ctx, sid, f.t)
		if err != nil {
			return fail("pinning its versions: %v", err)
		}
		if r.pinned, err = formatTemplate(p); err != nil {
			return fail("pinning its versions: %v", err)
		}
		recorded = p
	}
	facts := &checks.Check{Status: checks.Passing, Checked: c.now().UTC().Format(time.DateOnly), Release: c.release, Build: plan.Build, DoneSeconds: log.done}
	for _, a := range recorded.Addons {
		got := findAddon(installed, a)
		if got == nil {
			return fail("%s didn't install", a.Name)
		}
		info := c.sources.project(ctx, string(a.Source), a.Project, a.Slug)
		facts.Addons = append(facts.Addons, checks.Addon{Name: a.Name, Source: string(a.Source), Slug: firstOf(a.Slug, got.Slug), Version: got.VersionNumber, Licence: info.licence, Downloads: info.downloads})
	}
	if pack := recorded.Modpack; pack != nil {
		m := checks.Modpack{Name: pack.Name, Version: pack.Pin.VersionNumber}
		if installed.Modpack != nil {
			m.Version, m.Mods = installed.Modpack.VersionNumber, installed.Modpack.Mods
		}
		m.Downloads = c.sources.project(ctx, string(pack.Source), pack.Project, pack.Slug).downloads
		facts.Modpack = &m
	}
	facts.Crossplay, r.CrossplayFailure = c.crossplay(ctx, sid)
	if r.CrossplayFailure != "" && transient(r.CrossplayFailure) && sleep(ctx, c.retryAfter) == nil {
		facts.Crossplay, r.CrossplayFailure = c.crossplay(ctx, sid)
	}
	r.Status, r.Check = statusPassing, facts
	return r
}

// reGeyserStarted is Geyser's line once Bedrock players can connect.
var reGeyserStarted = regexp.MustCompile(`Started Geyser on UDP port \d+`)

// crossplay turns on crossplay for a passing template's server, where the
// release offers it, and reports whether Geyser and Floodgate started beside
// the template's add-ons, or why not. A release without crossplay, and a
// server it isn't offered for, give false and no reason.
func (c *checker) crossplay(ctx context.Context, sid string) (bool, string) {
	var cp api.Crossplay
	if status, err := c.agent.Do(ctx, "GET", "/v1/servers/"+sid+"/crossplay", nil, nil, &cp); err != nil {
		if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
			return false, ""
		}
		return false, err.Error()
	}
	if cp.On {
		return false, "crossplay was on before the check turned it on"
	}
	if !cp.Available {
		return false, ""
	}
	var op api.Operation
	if _, err := c.agent.Do(ctx, "POST", "/v1/servers/"+sid+"/crossplay", nil, api.CrossplayRequest{On: true, Actor: c.actor}, &op); err != nil {
		return false, err.Error()
	}
	if _, err := c.wait(ctx, op.ID, 10*time.Minute); err != nil {
		return false, err.Error()
	}
	if err := c.waitOnline(ctx, sid, 5*time.Minute); err != nil {
		return false, err.Error()
	}
	lines, err := c.logs(ctx, sid)
	if err != nil {
		return false, err.Error()
	}
	if log := readLog(lines); len(log.failures) > 0 {
		return false, strings.Join(log.failures, " / ")
	}
	if !slices.ContainsFunc(lines, reGeyserStarted.MatchString) {
		return false, "Geyser never said it started"
	}
	return true, ""
}

// plan asks the agent what the template would make.
func (c *checker) plan(ctx context.Context, file []byte) (*api.TemplatePlan, error) {
	resp, err := c.agent.Raw(ctx, "POST", "/v1/templates/plan", nil, bytes.NewReader(file), map[string]string{"Content-Type": "text/plain"}, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, agentclient.DecodeError(resp)
	}
	var p api.TemplatePlan
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// wait follows an operation until it ends.
func (c *checker) wait(ctx context.Context, id string, limit time.Duration) (api.Operation, error) {
	deadline := c.now().Add(limit)
	for {
		var op api.Operation
		if _, err := c.agent.Do(ctx, "GET", "/v1/operations/"+url.PathEscape(id), nil, nil, &op); err != nil {
			return op, err
		}
		switch op.Status {
		case "succeeded":
			return op, nil
		case "failed", "canceled", "cancelled":
			return op, errors.New(strings.TrimSpace(op.Error + " " + op.Hint))
		}
		if c.now().After(deadline) {
			return op, fmt.Errorf("still %s after %s", op.Phase, limit)
		}
		if err := sleep(ctx, c.poll); err != nil {
			return op, err
		}
	}
}

// waitOnline waits for the server to be online, and stops waiting when it
// crashes or stops.
func (c *checker) waitOnline(ctx context.Context, sid string, limit time.Duration) error {
	deadline := c.now().Add(limit)
	for {
		var s api.ServerStatus
		if _, err := c.agent.Do(ctx, "GET", "/v1/servers/"+sid, nil, nil, &s); err != nil {
			return err
		}
		switch s.Phase {
		case api.PhaseOnline:
			return nil
		case api.PhaseCrashed, api.PhaseDockerUnavailable:
			return fmt.Errorf("it %s before it was online: %s", strings.ReplaceAll(string(s.Phase), "_", " "), s.PhaseDetail)
		}
		if c.now().After(deadline) {
			return fmt.Errorf("it wasn't online after %s (%s)", limit, strings.ReplaceAll(string(s.Phase), "_", " "))
		}
		if err := sleep(ctx, c.poll); err != nil {
			return err
		}
	}
}

func (c *checker) logs(ctx context.Context, sid string) ([]string, error) {
	var l api.LogsResponse
	if _, err := c.agent.Do(ctx, "GET", "/v1/servers/"+sid+"/logs", url.Values{"limit": {"5000"}}, nil, &l); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(l.Lines))
	for _, line := range l.Lines {
		out = append(out, line.Text)
	}
	return out, nil
}

// pinned is the template with each add-on at the exact version the server
// installed, as the server's own export writes them.
func (c *checker) pinned(ctx context.Context, sid string, t *templates.Template) (*templates.Template, error) {
	var exp api.TemplateExport
	if _, err := c.agent.Do(ctx, "GET", "/v1/servers/"+sid+"/template", nil, nil, &exp); err != nil {
		return nil, err
	}
	e, err := templates.Decode([]byte(exp.File))
	if err != nil {
		return nil, err
	}
	return pinFrom(t, e)
}

// removeNamed deletes a server with this name, left by a check that didn't
// finish.
func (c *checker) removeNamed(ctx context.Context, name string) error {
	var list []api.ServerStatus
	if _, err := c.agent.Do(ctx, "GET", "/v1/servers", nil, nil, &list); err != nil {
		return err
	}
	for _, s := range list {
		if s.Name == name {
			return c.remove(ctx, s.ID, name)
		}
	}
	return nil
}

// remove stops and deletes a check's server, and forgets its key.
func (c *checker) remove(ctx context.Context, sid, name string) error {
	var op api.Operation
	if _, err := c.agent.Do(ctx, "POST", "/v1/servers/"+sid+"/stop", nil, map[string]string{"actor": c.actor}, &op); err == nil && op.ID != "" {
		_, _ = c.wait(ctx, op.ID, 3*time.Minute)
	}
	op = api.Operation{}
	if _, err := c.agent.Do(ctx, "POST", "/v1/servers/"+sid+"/delete", nil, api.DeleteServerRequest{Confirm: name, Actor: c.actor, ForgetKey: true}, &op); err != nil {
		return err
	}
	if op.ID != "" {
		_, err := c.wait(ctx, op.ID, 3*time.Minute)
		return err
	}
	return nil
}

// floated is the template with its add-ons at their newest versions, for
// -bump. Dependencies the last pin brought in are left for the plan to find
// again.
func floated(t *templates.Template) *templates.Template {
	out := *t
	out.Addons = nil
	for _, a := range t.Addons {
		if a.DependencyOf != "" {
			continue
		}
		a.Pin, a.Latest = nil, true
		out.Addons = append(out.Addons, a)
	}
	return &out
}

// pinFrom is t with each add-on pinned to the version in the server's
// export, then the dependencies the server installed for them, pinned too.
func pinFrom(t, exported *templates.Template) (*templates.Template, error) {
	byKey := map[addons.Key]templates.Addon{}
	for _, a := range exported.Addons {
		byKey[a.Key()] = a
	}
	out := *t
	out.Addons = nil
	for _, a := range t.Addons {
		if a.DependencyOf != "" {
			continue
		}
		e, ok := byKey[a.Key()]
		if !ok || e.Pin == nil {
			return nil, fmt.Errorf("the server's export has no exact version of %s", a.Name)
		}
		a.Pin, a.Latest = e.Pin, false
		out.Addons = append(out.Addons, a)
		delete(byKey, a.Key())
	}
	for _, e := range exported.Addons {
		if _, left := byKey[e.Key()]; left && e.DependencyOf != "" && e.Pin != nil {
			out.Addons = append(out.Addons, e)
		}
	}
	return &out, nil
}

func findAddon(installed api.Addons, a templates.Addon) *api.Addon {
	for _, f := range installed.Files {
		if f.Addon != nil && f.Addon.Source == string(a.Source) && f.Addon.ProjectID == a.Project {
			return f.Addon
		}
	}
	return nil
}

// serverName is the check's server's name: short, and the same each run, so
// a server a stopped run left behind is found and removed.
func serverName(id string) string {
	n := "check-" + id
	if len(n) > 32 {
		n = n[:32]
	}
	return strings.TrimRight(n, "-")
}

// explain is what a failed server's log says went wrong: its failure lines,
// or else its last lines.
func explain(lines []string) string {
	if f := readLog(lines).failures; len(f) > 0 {
		if len(f) > 3 {
			f = f[:3]
		}
		return ". Its log says: " + strings.Join(f, " / ")
	}
	return lastLines(lines, 3)
}

func lastLines(lines []string, n int) string {
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return ". Its log ended: " + strings.Join(lines, " / ")
}

func firstOf(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

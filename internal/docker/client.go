// Package docker is a deliberately small Docker Engine API client used only by
// the root agent. It speaks HTTP over the local Unix socket and covers exactly
// the calls Playkeeper needs; the web panel never links against it.
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MinAPIVersion is the oldest Engine API we accept (Docker 20.10).
const MinAPIVersion = "1.41"

type Client struct {
	socket string
	hc     *http.Client
	stream *http.Client

	mu         sync.Mutex
	apiVersion string
}

func New(socket string) *Client {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}
	return &Client{
		socket: socket,
		hc:     &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{DialContext: dial, MaxIdleConns: 4}},
		stream: &http.Client{Transport: &http.Transport{DialContext: dial, DisableKeepAlives: true}},
	}
}

type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("docker: %s (HTTP %d)", e.Message, e.Status) }

func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

func IsConflict(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusConflict
}

type VersionInfo struct {
	Version       string `json:"Version"`
	APIVersion    string `json:"ApiVersion"`
	MinAPIVersion string `json:"MinAPIVersion"`
	Os            string `json:"Os"`
	Arch          string `json:"Arch"`
	KernelVersion string `json:"KernelVersion"`
}

// Negotiate queries the unversioned /version endpoint and pins the API version
// used for later calls to the daemon's current version.
func (c *Client) Negotiate(ctx context.Context) (VersionInfo, error) {
	var v VersionInfo
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/version", nil)
	if err != nil {
		return v, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return v, fmt.Errorf("docker socket %s: %w", c.socket, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return v, decodeError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return v, err
	}
	if compareVersions(v.APIVersion, MinAPIVersion) < 0 {
		return v, fmt.Errorf("docker API %s is older than the minimum supported %s; upgrade Docker", v.APIVersion, MinAPIVersion)
	}
	c.mu.Lock()
	c.apiVersion = v.APIVersion
	c.mu.Unlock()
	return v, nil
}

func (c *Client) base(ctx context.Context) (string, error) {
	c.mu.Lock()
	v := c.apiVersion
	c.mu.Unlock()
	if v == "" {
		if _, err := c.Negotiate(ctx); err != nil {
			return "", err
		}
		c.mu.Lock()
		v = c.apiVersion
		c.mu.Unlock()
	}
	return "http://docker/v" + v, nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, q url.Values, body any) (*http.Request, error) {
	base, err := c.base(ctx)
	if err != nil {
		return nil, err
	}
	u := base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body, out any) (int, error) {
	req, err := c.newRequest(ctx, method, path, q, body)
	if err != nil {
		return 0, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return resp.StatusCode, decodeError(resp)
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil && !errors.Is(err, io.EOF) {
			return resp.StatusCode, err
		}
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	return resp.StatusCode, nil
}

func decodeError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &m) == nil && m.Message != "" {
		return &APIError{Status: resp.StatusCode, Message: m.Message}
	}
	return &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(b))}
}

type Info struct {
	ServerVersion     string   `json:"ServerVersion"`
	NCPU              int      `json:"NCPU"`
	MemTotal          int64    `json:"MemTotal"`
	DockerRootDir     string   `json:"DockerRootDir"`
	Driver            string   `json:"Driver"`
	Containers        int      `json:"Containers"`
	ContainersRunning int      `json:"ContainersRunning"`
	CgroupVersion     string   `json:"CgroupVersion"`
	SecurityOptions   []string `json:"SecurityOptions"`
}

func (c *Client) Info(ctx context.Context) (Info, error) {
	var i Info
	_, err := c.do(ctx, http.MethodGet, "/info", nil, nil, &i)
	return i, err
}

type ImageInfo struct {
	ID          string   `json:"Id"`
	RepoDigests []string `json:"RepoDigests"`
}

func (c *Client) ImageInspect(ctx context.Context, ref string) (ImageInfo, error) {
	var i ImageInfo
	_, err := c.do(ctx, http.MethodGet, "/images/"+ref+"/json", nil, nil, &i)
	return i, err
}

// PullProgress is one decoded progress message from an image pull.
type PullProgress struct {
	Status  string
	ID      string
	Current int64
	Total   int64
}

// ImagePull pulls ref ("repo@sha256:..." or "repo:tag"). Errors reported inside
// the 200 progress stream are returned as errors.
func (c *Client) ImagePull(ctx context.Context, ref string, onProgress func(PullProgress)) error {
	repo, tag := splitRef(ref)
	q := url.Values{"fromImage": {repo}, "tag": {tag}}
	req, err := c.newRequest(ctx, http.MethodPost, "/images/create", q, nil)
	if err != nil {
		return err
	}
	resp, err := c.stream.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return decodeError(resp)
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var m struct {
			Status         string `json:"status"`
			ID             string `json:"id"`
			Error          string `json:"error"`
			ProgressDetail struct {
				Current int64 `json:"current"`
				Total   int64 `json:"total"`
			} `json:"progressDetail"`
		}
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if m.Error != "" {
			return fmt.Errorf("pull %s: %s", ref, m.Error)
		}
		if onProgress != nil {
			onProgress(PullProgress{Status: m.Status, ID: m.ID, Current: m.ProgressDetail.Current, Total: m.ProgressDetail.Total})
		}
	}
}

func splitRef(ref string) (repo, tag string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	// A colon after the last slash separates the tag (ports appear before it).
	slash := strings.LastIndex(ref, "/")
	if i := strings.LastIndex(ref, ":"); i > slash {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}

func (c *Client) ImageRemove(ctx context.Context, ref string) error {
	_, err := c.do(ctx, http.MethodDelete, "/images/"+ref, nil, nil, nil)
	return err
}

type NetworkInfo struct {
	ID     string            `json:"Id"`
	Name   string            `json:"Name"`
	Labels map[string]string `json:"Labels"`
}

func (c *Client) NetworkInspect(ctx context.Context, name string) (NetworkInfo, error) {
	var n NetworkInfo
	_, err := c.do(ctx, http.MethodGet, "/networks/"+url.PathEscape(name), nil, nil, &n)
	return n, err
}

func (c *Client) NetworkCreate(ctx context.Context, name string, labels map[string]string) (string, error) {
	body := map[string]any{
		"Name":           name,
		"Driver":         "bridge",
		"CheckDuplicate": true,
		"Labels":         labels,
		"Options":        map[string]string{"com.docker.network.bridge.enable_icc": "false"},
	}
	var out struct {
		ID string `json:"Id"`
	}
	_, err := c.do(ctx, http.MethodPost, "/networks/create", nil, body, &out)
	return out.ID, err
}

func (c *Client) NetworkRemove(ctx context.Context, name string) error {
	_, err := c.do(ctx, http.MethodDelete, "/networks/"+url.PathEscape(name), nil, nil, nil)
	return err
}

type PortBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type LogConfig struct {
	Type   string            `json:"Type"`
	Config map[string]string `json:"Config"`
}

type RestartPolicy struct {
	Name string `json:"Name"`
}

type HostConfig struct {
	Binds          []string                 `json:"Binds,omitempty"`
	PortBindings   map[string][]PortBinding `json:"PortBindings,omitempty"`
	RestartPolicy  RestartPolicy            `json:"RestartPolicy"`
	Memory         int64                    `json:"Memory,omitempty"`
	MemorySwap     int64                    `json:"MemorySwap,omitempty"`
	PidsLimit      *int64                   `json:"PidsLimit,omitempty"`
	CapDrop        []string                 `json:"CapDrop,omitempty"`
	CapAdd         []string                 `json:"CapAdd,omitempty"`
	SecurityOpt    []string                 `json:"SecurityOpt,omitempty"`
	LogConfig      LogConfig                `json:"LogConfig"`
	NetworkMode    string                   `json:"NetworkMode,omitempty"`
	Init           *bool                    `json:"Init,omitempty"`
	ReadonlyRootfs bool                     `json:"ReadonlyRootfs,omitempty"`
	Tmpfs          map[string]string        `json:"Tmpfs,omitempty"`
}

type ContainerConfig struct {
	Image        string              `json:"Image"`
	Env          []string            `json:"Env,omitempty"`
	Labels       map[string]string   `json:"Labels,omitempty"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
	User         string              `json:"User,omitempty"`
	StopSignal   string              `json:"StopSignal,omitempty"`
	StopTimeout  *int                `json:"StopTimeout,omitempty"`
	HostConfig   HostConfig          `json:"HostConfig"`
}

func (c *Client) ContainerCreate(ctx context.Context, name string, cfg ContainerConfig) (string, error) {
	var out struct {
		ID string `json:"Id"`
	}
	_, err := c.do(ctx, http.MethodPost, "/containers/create", url.Values{"name": {name}}, cfg, &out)
	return out.ID, err
}

type ContainerState struct {
	Status     string `json:"Status"`
	Running    bool   `json:"Running"`
	Restarting bool   `json:"Restarting"`
	OOMKilled  bool   `json:"OOMKilled"`
	Dead       bool   `json:"Dead"`
	Pid        int    `json:"Pid"`
	ExitCode   int    `json:"ExitCode"`
	Error      string `json:"Error"`
	StartedAt  string `json:"StartedAt"`
	FinishedAt string `json:"FinishedAt"`
}

func (s ContainerState) Started() (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, s.StartedAt)
	if err != nil || t.Year() < 2000 {
		return time.Time{}, false
	}
	return t, true
}

func (s ContainerState) Finished() (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, s.FinishedAt)
	if err != nil || t.Year() < 2000 {
		return time.Time{}, false
	}
	return t, true
}

type ContainerJSON struct {
	ID     string         `json:"Id"`
	Name   string         `json:"Name"`
	Image  string         `json:"Image"`
	State  ContainerState `json:"State"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

func (c *Client) ContainerInspect(ctx context.Context, name string) (ContainerJSON, error) {
	var j ContainerJSON
	_, err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", nil, nil, &j)
	return j, err
}

type ContainerSummary struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
	Ports  []struct {
		PublicPort int    `json:"PublicPort"`
		Type       string `json:"Type"`
	} `json:"Ports"`
}

func (c *Client) ContainerList(ctx context.Context, all bool) ([]ContainerSummary, error) {
	q := url.Values{}
	if all {
		q.Set("all", "1")
	}
	var out []ContainerSummary
	_, err := c.do(ctx, http.MethodGet, "/containers/json", q, nil, &out)
	return out, err
}

func (c *Client) ContainerStart(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, nil, nil)
	return err
}

// ContainerStop asks the container to stop and waits up to timeout for the
// server's own shutdown (world save) before Docker kills it.
func (c *Client) ContainerStop(ctx context.Context, id string, timeout time.Duration) error {
	q := url.Values{"t": {strconv.Itoa(int(timeout.Seconds()))}}
	req, err := c.newRequest(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/stop", q, nil)
	if err != nil {
		return err
	}
	resp, err := c.stream.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return decodeError(resp)
	}
	return nil
}

func (c *Client) ContainerRemove(ctx context.Context, id string, force bool) error {
	q := url.Values{}
	if force {
		q.Set("force", "1")
	}
	_, err := c.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id), q, nil, nil)
	return err
}

type LogsOptions struct {
	Follow bool
	Since  time.Time
	Tail   string
}

// ContainerLogs returns a line scanner over the container's stdout/stderr with
// Docker-provided timestamps.
func (c *Client) ContainerLogs(ctx context.Context, id string, o LogsOptions) (*LogScanner, error) {
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"}}
	if o.Follow {
		q.Set("follow", "1")
	}
	if !o.Since.IsZero() {
		q.Set("since", fmt.Sprintf("%d.%09d", o.Since.Unix(), o.Since.Nanosecond()))
	}
	if o.Tail != "" {
		q.Set("tail", o.Tail)
	}
	req, err := c.newRequest(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/logs", q, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.stream.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, decodeError(resp)
	}
	tty := resp.Header.Get("Content-Type") == "application/vnd.docker.raw-stream"
	return NewLogScanner(resp.Body, tty), nil
}

type Stats struct {
	Read     time.Time `json:"read"`
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs     uint32 `json:"online_cpus"`
	} `json:"cpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
}

// MemoryUsed mirrors `docker stats`: usage minus reclaimable page cache.
func (s Stats) MemoryUsed() uint64 {
	u := s.MemoryStats.Usage
	if v, ok := s.MemoryStats.Stats["inactive_file"]; ok && v < u {
		return u - v
	}
	return u
}

func (c *Client) ContainerStats(ctx context.Context, id string) (Stats, error) {
	var s Stats
	_, err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/stats", url.Values{"stream": {"false"}, "one-shot": {"true"}}, nil, &s)
	return s, err
}

func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

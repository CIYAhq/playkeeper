// Command playkeeper-stats is the stats service behind stats.playkeeper.io:
// it takes the anonymous install and usage reports Playkeeper installs send
// (internal/usage) and answers counts of them. It runs on the project's own
// server (services/stats/README.md has the setup); it is not part of the
// Playkeeper release.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	stdlog "log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/CIYAhq/playkeeper/internal/usage/service"
)

const usage = `Usage:
  playkeeper-stats serve
      Runs the service. It is configured with environment variables:
        STATS_DATA_DIR              database and daily snapshots (default /data)
        STATS_LISTEN                listen address (default :8080)
        STATS_TRUSTED_PROXIES       the reverse proxy's own address, e.g. 10.0.1.5 (default none)
        STATS_READ_TOKEN            opens GET /v1/summary to Authorization: Bearer <token> (optional)
        STATS_NEW_INSTALLS_PER_DAY  install IDs heard of for the first time per day, everyone together (default 5000)
        STATS_OA_KEY                an Open Analytics read key for playkeeper.io, for the funnel's visitors and demo opens (optional)
        STATS_OA_API                Open Analytics' API (default https://analytics-api.ciya.so)
  playkeeper-stats summary
      Prints the counts as JSON from the database in STATS_DATA_DIR, without the read token.
  playkeeper-stats healthcheck
      Exits 0 if the service on STATS_LISTEN answers /healthz (Docker's HEALTHCHECK).
`

func main() {
	if len(os.Args) != 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve()
	case "summary":
		err = summary()
	case "healthcheck":
		err = healthcheck()
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "playkeeper-stats:", err)
		os.Exit(1)
	}
}

func serve() error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := service.FromEnv(os.Getenv)
	if err != nil {
		return err
	}
	cfg.Log = log
	svc, err := service.New(cfg)
	if err != nil {
		return err
	}
	defer svc.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// No access log, and none of net/http's own lines, some of which name
	// the client's address: the service keeps no request's address, in its
	// database or its log.
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           svc.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          stdlog.New(io.Discard, "", 0),
	}
	jobs := make(chan struct{})
	go func() {
		svc.Run(ctx)
		close(jobs)
	}()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("playkeeper-stats is listening", "address", cfg.Listen, "trusted_proxies", len(cfg.TrustedProxies), "summary", cfg.ReadToken != "", "site_numbers", cfg.OAKey != "")
	if len(cfg.TrustedProxies) == 0 {
		log.Warn(service.EnvTrustedProxies + " is empty, so X-Forwarded-For is ignored; behind a reverse proxy, set it to the proxy's own address")
	}
	select {
	case err = <-errc:
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err = srv.Shutdown(shutdown)
	}
	stop()
	<-jobs
	return err
}

func summary() error {
	cfg, err := service.FromEnv(os.Getenv)
	if err != nil {
		return err
	}
	svc, err := service.New(cfg)
	if err != nil {
		return err
	}
	defer svc.Close()
	sum, err := svc.Summary(context.Background())
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(sum)
}

func healthcheck() error {
	listen := os.Getenv(service.EnvListen)
	if listen == "" {
		listen = service.DefaultListen
	}
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("%s is not host:port: %w", service.EnvListen, err)
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort("127.0.0.1", port) + "/healthz")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/healthz answered HTTP %d", resp.StatusCode)
	}
	return nil
}

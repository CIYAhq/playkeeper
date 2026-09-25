// Command playkeeper-names is the service behind free yourname.playkeeper.io
// addresses: installs claim a name with a signed request, and the service
// points the name's DNS records in Cloudflare at the address the request
// came from. It runs on the project's own server (services/names/README.md
// has the setup); it is not part of the Playkeeper release.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/CIYAhq/playkeeper/internal/names/service"
)

const usage = `Usage:
  playkeeper-names serve
      Runs the service. It is configured with environment variables:
        NAMES_CLOUDFLARE_API_TOKEN      Cloudflare API token with DNS edit rights on the zone (required)
        NAMES_CLOUDFLARE_ZONE_ID        the zone's ID (required)
        NAMES_BASE_DOMAIN               domain names live under (default playkeeper.io)
        NAMES_DATA_DIR                  database and daily snapshots (default /data)
        NAMES_LISTEN                    listen address (default :8080)
        NAMES_TRUSTED_PROXIES           networks of the reverse proxy, e.g. 10.0.1.0/24 (default none)
        NAMES_MAX_NAMES_PER_KEY         names one install may hold (default 1)
        NAMES_MAX_NAMES_PER_NETWORK     names one IPv4 /24 or IPv6 /48 may hold (default 3)
        NAMES_CLAIMS_PER_DAY            new names per day across everyone (default 30)
        NAMES_RECORD_RESERVE            DNS records the service always leaves free in the zone (default 10)
        NAMES_RECORD_QUOTA              most DNS records the zone may hold (default 200, Cloudflare Free)
        NAMES_NEW_CERTIFICATES_PER_WEEK names that may get their first certificate in 7 days, 1 to 50 (default 40)
        NAMES_BLOCKLIST_FILE            file of names nobody may have, one per line (optional)
        NAMES_ALERT_WEBHOOK_URL         https:// webhook, e.g. Discord's, for the owner's alerts (optional)
  playkeeper-names healthcheck
      Exits 0 if the service on NAMES_LISTEN answers /healthz (Docker's HEALTHCHECK).
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
	case "healthcheck":
		err = healthcheck()
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "playkeeper-names:", err)
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	svc, err := service.New(startCtx, cfg)
	cancel()
	if err != nil {
		return err
	}
	defer svc.Close()
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           svc.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
	}
	jobs := make(chan struct{})
	go func() {
		svc.Run(ctx)
		close(jobs)
	}()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("playkeeper-names is listening", "address", cfg.Listen, "base", cfg.Base, "trusted_proxies", len(cfg.TrustedProxies))
	if len(cfg.TrustedProxies) == 0 {
		log.Warn(service.EnvTrustedProxies + " is empty, so X-Forwarded-For is ignored; behind a reverse proxy, set it to the proxy's network")
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

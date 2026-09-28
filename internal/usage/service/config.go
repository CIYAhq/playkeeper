package service

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/usage"
)

// Environment variables the service reads; services/stats/README.md
// explains each one for the owner.
const (
	EnvDataDir        = "STATS_DATA_DIR"
	EnvListen         = "STATS_LISTEN"
	EnvTrustedProxies = "STATS_TRUSTED_PROXIES"
	EnvReadToken      = "STATS_READ_TOKEN"
	EnvNewPerDay      = "STATS_NEW_INSTALLS_PER_DAY"
	EnvOAKey          = "STATS_OA_KEY"
	EnvOAAPI          = "STATS_OA_API"
)

// Defaults of the settings that have one.
const (
	DefaultDataDir   = "/data"
	DefaultListen    = ":8080"
	DefaultNewPerDay = 5000
	// DefaultOAAPI is Open Analytics, where playkeeper.io counts its visits.
	DefaultOAAPI = "https://analytics-api.ciya.so"
)

// Config is what the service needs to run.
type Config struct {
	// DataDir holds stats.db and its daily snapshots.
	DataDir string
	// Listen is the address main listens on; the service itself ignores it.
	Listen string
	// TrustedProxies are the reverse proxy's own addresses, or a network
	// only it shares with the service; only requests from them may name the
	// client address in X-Forwarded-For. The address is used for the rate
	// limits alone and never kept.
	TrustedProxies []netip.Prefix
	// ReadToken opens GET /v1/summary; empty closes it.
	ReadToken string
	// NewPerDay bounds the install IDs the service hears of for the first
	// time in a day, everyone together, so a flood of made-up IDs can't fill
	// the disk.
	NewPerDay int
	// OAKey is a read key for playkeeper.io's site in Open Analytics, at
	// OAAPI: with it, the funnel starts with the site's visitors and demo
	// opens. The key goes to Open Analytics alone and is never shown.
	OAKey, OAAPI string
	// OAClient reads Open Analytics; nil uses one with a 10-second timeout.
	OAClient *http.Client

	Log *slog.Logger
	Now func() time.Time
}

var (
	reToken = regexp.MustCompile(`^[A-Za-z0-9_-]{32,200}$`)
	reOAKey = regexp.MustCompile(`^[A-Za-z0-9_-]{16,200}$`)
)

// FromEnv reads the configuration from environment variables. Errors name
// the variable, never its value, so the token cannot leak into logs.
func FromEnv(getenv func(string) string) (Config, error) {
	get := func(k, def string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return def
	}
	cfg := Config{
		DataDir:   get(EnvDataDir, DefaultDataDir),
		Listen:    get(EnvListen, DefaultListen),
		ReadToken: get(EnvReadToken, ""),
		OAKey:     get(EnvOAKey, ""),
		OAAPI:     get(EnvOAAPI, DefaultOAAPI),
	}
	var errs []error
	if !filepath.IsAbs(cfg.DataDir) {
		errs = append(errs, fmt.Errorf("%s must be an absolute path", EnvDataDir))
	}
	if cfg.ReadToken != "" && !reToken.MatchString(cfg.ReadToken) {
		errs = append(errs, fmt.Errorf("%s must be 32 to 200 letters, digits, - and _, such as the output of: openssl rand -hex 32", EnvReadToken))
	}
	var err error
	if cfg.TrustedProxies, err = ParsePrefixes(get(EnvTrustedProxies, "")); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvTrustedProxies, err))
	}
	n, err := strconv.Atoi(get(EnvNewPerDay, strconv.Itoa(DefaultNewPerDay)))
	if err != nil || n < 1 || n > 1000000 {
		errs = append(errs, fmt.Errorf("%s must be a whole number from 1 to 1000000", EnvNewPerDay))
	}
	cfg.NewPerDay = n
	if cfg.OAKey != "" && !reOAKey.MatchString(cfg.OAKey) {
		errs = append(errs, fmt.Errorf("%s must be an Open Analytics read key: 16 to 200 letters, digits, - and _", EnvOAKey))
	}
	if _, err := usage.CheckURL(cfg.OAAPI); err != nil {
		errs = append(errs, fmt.Errorf("%s must be an https:// address without a path", EnvOAAPI))
	}
	return cfg, errors.Join(errs...)
}

// ParsePrefixes reads a list of addresses ("10.0.1.5, fd00::7") or networks
// ("10.0.1.0/24"), separated by commas or spaces. A prefix that covers every
// address is refused: trusting everyone would let anyone name any address.
func ParsePrefixes(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		p, err := netip.ParsePrefix(f)
		if err != nil {
			a, aerr := netip.ParseAddr(f)
			if aerr != nil {
				return nil, fmt.Errorf("%q is not an address like 10.0.1.5 or a network like 10.0.1.0/24", f)
			}
			p = netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())
		}
		if p.Bits() == 0 {
			return nil, fmt.Errorf("%s would trust every address; list only the proxy's own address", f)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

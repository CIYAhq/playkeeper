package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/names"
)

// Environment variables the service reads; services/names/README.md
// explains each one for the owner.
const (
	EnvBase               = "NAMES_BASE_DOMAIN"
	EnvToken              = "NAMES_CLOUDFLARE_API_TOKEN"
	EnvZone               = "NAMES_CLOUDFLARE_ZONE_ID"
	EnvDataDir            = "NAMES_DATA_DIR"
	EnvListen             = "NAMES_LISTEN"
	EnvTrustedProxies     = "NAMES_TRUSTED_PROXIES"
	EnvMaxNamesPerKey     = "NAMES_MAX_NAMES_PER_KEY"
	EnvMaxNamesPerNetwork = "NAMES_MAX_NAMES_PER_NETWORK"
	EnvClaimsPerDay       = "NAMES_CLAIMS_PER_DAY"
	EnvRecordReserve      = "NAMES_RECORD_RESERVE"
	EnvRecordQuota        = "NAMES_RECORD_QUOTA"
	EnvBlocklist          = "NAMES_BLOCKLIST_FILE"
	EnvAlertWebhook       = "NAMES_ALERT_WEBHOOK_URL"
)

// Defaults of the settings that have one.
const (
	DefaultDataDir            = "/data"
	DefaultListen             = ":8080"
	DefaultMaxNamesPerKey     = 1
	DefaultMaxNamesPerNetwork = 3
	DefaultClaimsPerDay       = 30
	DefaultRecordReserve      = 10
	DefaultRecordQuota        = 200
)

// Config is what the service needs to run.
type Config struct {
	// Base is the domain names live under; Cloudflare's zone must have
	// exactly this name.
	Base            string
	CloudflareToken string
	CloudflareZone  string
	// DataDir holds names.db and its daily backups.
	DataDir string
	// Listen is the address main listens on; the service itself ignores it.
	Listen string
	// TrustedProxies are the networks of the reverse proxy in front of the
	// service; only requests from them may name the client address in
	// X-Forwarded-For.
	TrustedProxies []netip.Prefix
	MaxNamesPerKey int
	// MaxNamesPerNetwork bounds the names claimed from one network (see
	// network) that are not released.
	MaxNamesPerNetwork int
	// ClaimsPerDay bounds new names per day across everyone.
	ClaimsPerDay int
	// RecordReserve is how many of the zone's DNS records the service
	// always leaves free for the owner.
	RecordReserve int
	// RecordQuota is the most records the zone may hold. Cloudflare's own
	// quota wins when it is lower.
	RecordQuota int
	// BlocklistFile optionally lists names nobody may claim, one per line;
	// claimed names on it are taken away.
	BlocklistFile string
	// AlertWebhook optionally receives the owner's alerts as Discord's
	// {"content": "..."} JSON. Its path is a secret.
	AlertWebhook string

	Log  *slog.Logger
	Now  func() time.Time
	HTTP *http.Client

	cloudflareAPI  string
	pageSize       int
	alertTransport http.RoundTripper
	// dialAlive connects the liveness checks to a name's address.
	dialAlive func(ctx context.Context, network, addr string) (net.Conn, error)
}

var (
	reZoneID = regexp.MustCompile(`^[0-9a-f]{32}$`)
	reToken  = regexp.MustCompile(`^[A-Za-z0-9_-]{20,200}$`)
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
		Base:            strings.ToLower(strings.TrimSuffix(get(EnvBase, names.DefaultBase), ".")),
		CloudflareToken: get(EnvToken, ""),
		CloudflareZone:  strings.ToLower(get(EnvZone, "")),
		DataDir:         get(EnvDataDir, DefaultDataDir),
		Listen:          get(EnvListen, DefaultListen),
		BlocklistFile:   get(EnvBlocklist, ""),
		AlertWebhook:    get(EnvAlertWebhook, ""),
	}
	var errs []error
	if err := checkBase(cfg.Base); err != nil {
		errs = append(errs, err)
	}
	if err := checkWebhook(cfg.AlertWebhook); err != nil {
		errs = append(errs, err)
	}
	if !reToken.MatchString(cfg.CloudflareToken) {
		errs = append(errs, fmt.Errorf("%s must hold the Cloudflare API token (letters, digits, - and _)", EnvToken))
	}
	if !reZoneID.MatchString(cfg.CloudflareZone) {
		errs = append(errs, fmt.Errorf("%s must hold the zone's ID: 32 characters 0-9 and a-f, from the zone's Overview page", EnvZone))
	}
	if !filepath.IsAbs(cfg.DataDir) {
		errs = append(errs, fmt.Errorf("%s must be an absolute path", EnvDataDir))
	}
	if cfg.BlocklistFile != "" && !filepath.IsAbs(cfg.BlocklistFile) {
		errs = append(errs, fmt.Errorf("%s must be an absolute path", EnvBlocklist))
	}
	var err error
	if cfg.TrustedProxies, err = ParsePrefixes(get(EnvTrustedProxies, "")); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvTrustedProxies, err))
	}
	number := func(k string, def, lo, hi int) int {
		n, err := strconv.Atoi(get(k, strconv.Itoa(def)))
		if err != nil || n < lo || n > hi {
			errs = append(errs, fmt.Errorf("%s must be a whole number from %d to %d", k, lo, hi))
		}
		return n
	}
	cfg.MaxNamesPerKey = number(EnvMaxNamesPerKey, DefaultMaxNamesPerKey, 1, 100)
	cfg.MaxNamesPerNetwork = number(EnvMaxNamesPerNetwork, DefaultMaxNamesPerNetwork, 1, 10000)
	cfg.ClaimsPerDay = number(EnvClaimsPerDay, DefaultClaimsPerDay, 1, 100000)
	cfg.RecordReserve = number(EnvRecordReserve, DefaultRecordReserve, 0, 100000)
	cfg.RecordQuota = number(EnvRecordQuota, DefaultRecordQuota, 1, 1000000)
	return cfg, errors.Join(errs...)
}

// checkWebhook accepts an empty setting or an https:// URL without a user
// name or password. The error never shows the URL, since its path is the
// webhook's secret.
func checkWebhook(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("%s must be an https:// address, like the Discord webhook URL", EnvAlertWebhook)
	}
	return nil
}

func checkBase(base string) error {
	labels := strings.Split(base, ".")
	if len(base) > 200 || len(labels) < 2 {
		return fmt.Errorf("%s must be a domain like playkeeper.io", EnvBase)
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || strings.Trim(l, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" || l[0] == '-' || l[len(l)-1] == '-' {
			return fmt.Errorf("%s must be a domain like playkeeper.io", EnvBase)
		}
	}
	return nil
}

// ParsePrefixes reads a list of networks ("10.0.1.0/24, fd00::/64") or
// single addresses, separated by commas or spaces. A prefix that covers
// every address is refused: trusting everyone would let anyone name any
// address.
func ParsePrefixes(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		p, err := netip.ParsePrefix(f)
		if err != nil {
			a, aerr := netip.ParseAddr(f)
			if aerr != nil {
				return nil, fmt.Errorf("%q is not a network like 10.0.1.0/24 or an address", f)
			}
			p = netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())
		}
		if p.Bits() == 0 {
			return nil, fmt.Errorf("%s would trust every address; list only the proxy's own network", f)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

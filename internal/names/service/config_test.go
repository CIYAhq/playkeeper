package service

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestFromEnvReadsSettingsAndDefaults(t *testing.T) {
	cfg, err := FromEnv(envOf(map[string]string{
		EnvToken: testToken,
		EnvZone:  " " + strings.ToUpper(testZoneID) + "\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Base != "playkeeper.io" || cfg.CloudflareToken != testToken || cfg.CloudflareZone != testZoneID ||
		cfg.DataDir != DefaultDataDir || cfg.Listen != DefaultListen || cfg.TrustedProxies != nil || cfg.BlocklistFile != "" ||
		cfg.MaxNamesPerKey != DefaultMaxNamesPerKey || cfg.ClaimsPerDay != DefaultClaimsPerDay || cfg.RecordReserve != DefaultRecordReserve ||
		cfg.MaxNamesPerNetwork != DefaultMaxNamesPerNetwork || cfg.RecordQuota != DefaultRecordQuota || cfg.AlertWebhook != "" ||
		cfg.NewCertificates != DefaultNewCertificates {
		t.Errorf("defaults: %+v", cfg)
	}

	cfg, err = FromEnv(envOf(map[string]string{
		EnvBase:               "Example.COM.",
		EnvToken:              testToken,
		EnvZone:               testZoneID,
		EnvDataDir:            "/srv/names",
		EnvListen:             "127.0.0.1:9000",
		EnvTrustedProxies:     "10.0.1.0/24, 172.18.0.1",
		EnvMaxNamesPerKey:     "3",
		EnvMaxNamesPerNetwork: "5",
		EnvClaimsPerDay:       "500",
		EnvRecordReserve:      "0",
		EnvRecordQuota:        "1000",
		EnvNewCertificates:    "50",
		EnvBlocklist:          "/data/blocklist.txt",
		EnvAlertWebhook:       "https://discord.com/api/webhooks/123/abc",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{netip.MustParsePrefix("10.0.1.0/24"), netip.MustParsePrefix("172.18.0.1/32")}
	if cfg.Base != "example.com" || cfg.DataDir != "/srv/names" || cfg.Listen != "127.0.0.1:9000" || !slices.Equal(cfg.TrustedProxies, want) ||
		cfg.MaxNamesPerKey != 3 || cfg.ClaimsPerDay != 500 || cfg.RecordReserve != 0 || cfg.BlocklistFile != "/data/blocklist.txt" ||
		cfg.MaxNamesPerNetwork != 5 || cfg.RecordQuota != 1000 || cfg.AlertWebhook != "https://discord.com/api/webhooks/123/abc" ||
		cfg.NewCertificates != 50 {
		t.Errorf("settings: %+v", cfg)
	}
}

func TestFromEnvNamesTheVariableButNeverTheValue(t *testing.T) {
	valid := func(changes map[string]string) map[string]string {
		m := map[string]string{EnvToken: testToken, EnvZone: testZoneID}
		for k, v := range changes {
			m[k] = v
		}
		return m
	}
	for _, tc := range []struct {
		env  map[string]string
		want []string
	}{
		{map[string]string{}, []string{EnvToken, EnvZone}},
		{valid(map[string]string{EnvToken: "Bearer " + testToken}), []string{EnvToken}},
		{valid(map[string]string{EnvToken: "short-secret-9"}), []string{EnvToken}},
		{valid(map[string]string{EnvZone: "playkeeper.io"}), []string{EnvZone}},
		{valid(map[string]string{EnvZone: testZoneID + "0"}), []string{EnvZone}},
		{valid(map[string]string{EnvBase: "localhost"}), []string{EnvBase}},
		{valid(map[string]string{EnvBase: "-bad.io"}), []string{EnvBase}},
		{valid(map[string]string{EnvBase: "play keeper.io"}), []string{EnvBase}},
		{valid(map[string]string{EnvBase: "playkeeper..io"}), []string{EnvBase}},
		{valid(map[string]string{EnvDataDir: "data"}), []string{EnvDataDir}},
		{valid(map[string]string{EnvBlocklist: "blocklist.txt"}), []string{EnvBlocklist}},
		{valid(map[string]string{EnvTrustedProxies: "0.0.0.0/0"}), []string{EnvTrustedProxies, "every address"}},
		{valid(map[string]string{EnvTrustedProxies: "10.0.1.0/24 ::/0"}), []string{EnvTrustedProxies, "every address"}},
		{valid(map[string]string{EnvTrustedProxies: "traefik"}), []string{EnvTrustedProxies}},
		{valid(map[string]string{EnvMaxNamesPerKey: "0"}), []string{EnvMaxNamesPerKey, "1 to 100"}},
		{valid(map[string]string{EnvMaxNamesPerKey: "two"}), []string{EnvMaxNamesPerKey}},
		{valid(map[string]string{EnvClaimsPerDay: "-5"}), []string{EnvClaimsPerDay}},
		{valid(map[string]string{EnvRecordReserve: "1e3"}), []string{EnvRecordReserve}},
		{valid(map[string]string{EnvMaxNamesPerNetwork: "0"}), []string{EnvMaxNamesPerNetwork, "1 to 10000"}},
		{valid(map[string]string{EnvRecordQuota: "0"}), []string{EnvRecordQuota}},
		{valid(map[string]string{EnvNewCertificates: "51"}), []string{EnvNewCertificates, "1 to 50"}},
		{valid(map[string]string{EnvNewCertificates: "0"}), []string{EnvNewCertificates}},
		{valid(map[string]string{EnvAlertWebhook: "http://discord.com/api/webhooks/123/secret-part"}), []string{EnvAlertWebhook, "https://"}},
		{valid(map[string]string{EnvAlertWebhook: "https://user:secret-part@discord.com/api/webhooks/123"}), []string{EnvAlertWebhook}},
		{valid(map[string]string{EnvAlertWebhook: "discord.com/api/webhooks/123/secret-part"}), []string{EnvAlertWebhook}},
	} {
		_, err := FromEnv(envOf(tc.env))
		if err == nil {
			t.Errorf("%v: no error", tc.env)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%v: %q does not mention %s", tc.env, err, w)
			}
		}
		for k, v := range tc.env {
			if (k == EnvToken || k == EnvZone || k == EnvAlertWebhook) && strings.Contains(err.Error(), v) {
				t.Errorf("the error shows the value of %s: %q", k, err)
			}
		}
		if strings.Contains(err.Error(), "secret-part") || strings.Contains(err.Error(), "/api/webhooks") {
			t.Errorf("the error shows part of the webhook URL: %q", err)
		}
	}
}

func TestParsePrefixes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"10.0.1.0/24", []string{"10.0.1.0/24"}},
		{"10.0.1.7/24", []string{"10.0.1.0/24"}},
		{"172.18.0.1", []string{"172.18.0.1/32"}},
		{"::ffff:172.18.0.1", []string{"172.18.0.1/32"}},
		{"2a01:4f8:c012:6f3a::1", []string{"2a01:4f8:c012:6f3a::1/128"}},
		{"10.0.1.0/24,fd00::/64\t10.0.2.0/24\n 10.0.3.1", []string{"10.0.1.0/24", "fd00::/64", "10.0.2.0/24", "10.0.3.1/32"}},
	} {
		got, err := ParsePrefixes(tc.in)
		var s []string
		for _, p := range got {
			s = append(s, p.String())
		}
		if err != nil || !slices.Equal(s, tc.want) {
			t.Errorf("ParsePrefixes(%q) = %v, %v; want %v", tc.in, s, err, tc.want)
		}
	}
	for _, in := range []string{"0.0.0.0/0", "::/0", "10.0.1.0/24, 0.0.0.0/0", "10.0.0.0/33", "10.0.0", "traefik", "10.0.1.0/24;10.0.2.0/24"} {
		if got, err := ParsePrefixes(in); err == nil {
			t.Errorf("ParsePrefixes(%q) = %v, want an error", in, got)
		}
	}
}

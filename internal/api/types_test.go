package api

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api/apitest"
	"github.com/CIYAhq/playkeeper/internal/backup/retention"
	"github.com/CIYAhq/playkeeper/internal/certs"
	"github.com/CIYAhq/playkeeper/internal/diskusage"
	"github.com/CIYAhq/playkeeper/internal/gamefiles"
	"github.com/CIYAhq/playkeeper/internal/names"
	"github.com/CIYAhq/playkeeper/internal/offsite"
	"github.com/CIYAhq/playkeeper/internal/pregen"
	"github.com/CIYAhq/playkeeper/internal/twofactor"
)

const webSrc = "../../web/src"

// A field the dashboard declares that the API never sends is always
// undefined in the browser.
func TestTheDashboardDeclaresOnlyFieldsTheAPISends(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(webSrc, "api", "types.ts"))
	if err != nil {
		t.Fatal(err)
	}
	// The agent's own types, and those of packages that import this one, are
	// checked by TestTheDashboardDeclaresOnlyFieldsTheAgentSends in
	// internal/agent.
	sent := map[string]any{
		"Activity": Activity{}, "AuditEntry": AuditEntry{}, "Backup": Backup{}, "Catalog": Catalog{}, "CatalogEntry": CatalogEntry{},
		"DailyActivity": DailyActivity{}, "FirstSteps": FirstSteps{}, "Gameplay": Gameplay{}, "Gap": Gap{}, "LogLine": LogLine{},
		"LogsResponse": LogsResponse{}, "Machine": Machine{}, "ManifestSummary": ManifestSummary{}, "MetricsBucket": MetricsBucket{},
		"MetricsResponse": MetricsResponse{}, "Operation": Operation{}, "OperatorEntry": OperatorEntry{}, "PlayerSnapshot": PlayerSnapshot{},
		"PlayersSummary": PlayersSummary{}, "PlayerStat": PlayerStat{}, "Preflight": Preflight{}, "PreflightCheck": PreflightCheck{},
		"Resources": Resources{}, "RestorePreview": RestorePreview{}, "ServerConfig": ServerConfig{}, "ServerMemory": ServerMemory{},
		"ServerStatus": ServerStatus{}, "ServerType": ServerType{}, "Session": Session{}, "SessionsResponse": SessionsResponse{}, "SetupStatus": SetupStatus{},
		"UpdateInfo": UpdateInfo{}, "UpdateResult": UpdateResult{}, "WhitelistEntry": WhitelistEntry{}, "WorldCopy": WorldCopy{},
		"Addon": Addon{}, "AddonBrowse": AddonBrowse{}, "AddonCard": AddonCard{}, "AddonChecks": AddonChecks{}, "AddonDetails": AddonDetails{},
		"AddonFile": AddonFile{}, "AddonKey": AddonKey{}, "AddonNotice": AddonNotice{}, "AddonPlan": AddonPlan{}, "AddonProgress": AddonProgress{},
		"AddonRemovePreview": AddonRemovePreview{}, "AddonRemoval": AddonRemoval{}, "Addons": Addons{}, "AddonStep": AddonStep{},
		"AddonTarget": AddonTarget{}, "AddonUpdate": AddonUpdate{}, "AddonVersion": AddonVersion{}, "DataPack": DataPack{}, "DataPacks": DataPacks{},
		"Pregen": Pregen{}, "PregenPreset": PregenPreset{}, "ResourcePack": ResourcePack{}, "ResourcePackOffer": ResourcePackOffer{},
		"Address": Address{}, "AddressCheck": AddressCheck{}, "AddressPlan": AddressPlan{}, "AddrRecord": AddrRecord{},
		"CertificateStatus": CertificateStatus{}, "DNSRecord": DNSRecord{}, "FreeAddress": FreeAddress{}, "JoinAddress": JoinAddress{},
		"NameAvailability": NameAvailability{}, "NamesService": NamesService{}, "Note": Note{}, "SRVParts": SRVParts{},
		"Challenge": twofactor.Challenge{}, "SignInNotice": twofactor.Notice{}, "TwoFactorSetup": twofactor.Setup{}, "TwoFactorStatus": twofactor.Status{},
		"Crash": Crash{}, "CrashLine": CrashLine{}, "DiagnosisAction": DiagnosisAction{}, "DiagnosisEvidence": DiagnosisEvidence{}, "FileRefusal": FileRefusal{},
		"LagCause": LagCause{}, "MemoryAdvice": MemoryAdvice{}, "MemoryDay": MemoryDay{}, "MemoryOption": MemoryOption{}, "Running": Running{},
		"AddonSources": AddonSources{}, "CurseForgeSource": CurseForgeSource{},
		"AddonPort": AddonPort{}, "CuratedAddons": CuratedAddons{}, "CuratedAddon": CuratedAddon{},
		"SoftwarePin": SoftwarePin{}, "SoftwareBuild": SoftwareBuild{}, "SoftwareBuilds": SoftwareBuilds{}, "SoftwareChange": SoftwareChange{},
		"ModpackCard": ModpackCard{}, "ModpackResults": ModpackResults{}, "ModpackVersion": ModpackVersion{}, "ModpackPreview": ModpackPreview{},
		"ModpackRef": ModpackRef{}, "ServerModpack": ServerModpack{}, "ServerTemplate": ServerTemplate{}, "TemplateSettings": TemplateSettings{},
		"TemplateAddon": TemplateAddon{}, "TemplateContents": TemplateContents{}, "TemplateExport": TemplateExport{}, "TemplatePlan": TemplatePlan{},
		"PackShare":    PackShare{},
		"ApiErrorBody": Error{}, "SleepStatus": SleepStatus{}, "RetentionEstimate": retention.Estimate{}, "RetentionRules": retention.Rules{},
		"RetentionSettings": retention.Settings{}, "RetentionText": retention.Text{}, "OffsiteCheck": offsite.Check{}, "OffsiteProvider": offsite.Provider{},
		"OffsiteTestResult": offsite.TestResult{}, "DiskCandidate": diskusage.Candidate{}, "DiskReport": diskusage.Report{},
		"DiskServer": diskusage.ServerUsage{}, "DiskUsage": diskusage.Usage{}, "DiskWay": diskusage.Way{},
	}
	addedByPanel := map[string]bool{"ServerStatus.machineId": true, "AuditEntry.source": true}
	for _, p := range apitest.Undeclared(string(src), sent, addedByPanel) {
		t.Error(p)
	}
}

// The dashboard shows an operation by its status, so it must know each one
// the API sends, a cancelled operation too.
func TestTheDashboardKnowsEveryOperationStatus(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(webSrc, "api", "types.ts"))
	if err != nil {
		t.Fatal(err)
	}
	op := regexp.MustCompile(`(?ms)^export interface Operation \{\n(.*?)^\}`).FindSubmatch(src)
	if op == nil {
		t.Fatal("web/src/api/types.ts has no interface Operation")
	}
	status := regexp.MustCompile(`(?m)^  status: (.*)$`).FindSubmatch(op[1])
	if status == nil {
		t.Fatal("web/src/api/types.ts: Operation has no status")
	}
	var declared []string
	for _, m := range regexp.MustCompile(`'(\w+)'`).FindAllSubmatch(status[1], -1) {
		declared = append(declared, string(m[1]))
	}
	sent := []string{OpRunning, OpSucceeded, OpFailed, OpCancelled}
	slices.Sort(declared)
	slices.Sort(sent)
	if !slices.Equal(declared, sent) {
		t.Errorf("web/src/api/types.ts: Operation.status is %s, but the API sends %q", status[1], sent)
	}
}

func TestErrorCodesTheDashboardChecksForExist(t *testing.T) {
	codes := map[string]bool{}
	sent := []string{CodeInvalid, CodeEULARequired, CodeBusy, CodeNotFound, CodeConflict, CodeNotCreated, CodeDockerUnavailable, CodeForbidden, CodeUnauthorized, CodeRateLimited, CodeInternal, CodeAgentUnavailable, CodeInsufficientSpace, CodeIconInvalid, pregen.CodeUnsupportedServer,
		CodeNamesUnreachable, CodeRetryLater, names.CodeInvalidName, names.CodeNotAnswering, certs.CodePort80Unreachable, certs.CodeCertificateLimit,
		string(twofactor.KindPasswordWrong), CodePlanChanged, CodeKeyRefused,
		diskusage.CodeDiskSpace, retention.CodeEstimateOff}
	for _, k := range []gamefiles.Kind{gamefiles.KindLink, gamefiles.KindSpecial, gamefiles.KindNotFile, gamefiles.KindNotFolder, gamefiles.KindTooLarge, gamefiles.KindTooMany, gamefiles.KindChanged, gamefiles.KindBadName} {
		sent = append(sent, string(k))
	}
	for _, c := range sent {
		if codes[c] {
			t.Errorf("error code %q is used twice", c)
		}
		codes[c] = true
	}
	checked := regexp.MustCompile(`\bcode [!=]== '(\w+)'`)
	found := 0
	err := filepath.WalkDir(webSrc, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.Contains(path, ".test.") || !strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".tsx") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range checked.FindAllStringSubmatch(string(b), -1) {
			found++
			if !codes[m[1]] {
				t.Errorf("%s checks for error code %q, which the API does not have", path, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("found no error code checks in web/src, so this test no longer looks where the dashboard makes them")
	}
}

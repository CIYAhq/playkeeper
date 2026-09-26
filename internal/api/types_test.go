package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api/apitest"
	"github.com/CIYAhq/playkeeper/internal/backup/retention"
	"github.com/CIYAhq/playkeeper/internal/certs"
	"github.com/CIYAhq/playkeeper/internal/diskusage"
	"github.com/CIYAhq/playkeeper/internal/gamefiles"
	"github.com/CIYAhq/playkeeper/internal/machinelink"
	"github.com/CIYAhq/playkeeper/internal/names"
	"github.com/CIYAhq/playkeeper/internal/offsite"
	"github.com/CIYAhq/playkeeper/internal/pregen"
	"github.com/CIYAhq/playkeeper/internal/twofactor"
	"github.com/CIYAhq/playkeeper/internal/worldimport"
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
		"NameCheck": NameCheck{}, "RecordCheck": RecordCheck{}, "CertificateProblem": CertificateProblem{},
		"Challenge": twofactor.Challenge{}, "SignInNotice": twofactor.Notice{}, "TwoFactorSetup": twofactor.Setup{}, "TwoFactorStatus": twofactor.Status{},
		"Crash": Crash{}, "CrashLine": CrashLine{}, "DiagnosisAction": DiagnosisAction{}, "DiagnosisEvidence": DiagnosisEvidence{}, "FileRefusal": FileRefusal{},
		"LagCause": LagCause{}, "MemoryAdvice": MemoryAdvice{}, "MemoryDay": MemoryDay{}, "MemoryOption": MemoryOption{}, "Running": Running{},
		"AddonSources": AddonSources{}, "CurseForgeSource": CurseForgeSource{},
		"AddonPort": AddonPort{}, "CuratedAddons": CuratedAddons{}, "CuratedAddon": CuratedAddon{},
		"SoftwarePin": SoftwarePin{}, "SoftwareBuild": SoftwareBuild{}, "SoftwareBuilds": SoftwareBuilds{}, "SoftwareChange": SoftwareChange{},
		"ModpackCard": ModpackCard{}, "ModpackResults": ModpackResults{}, "ModpackVersion": ModpackVersion{}, "ModpackPreview": ModpackPreview{},
		"ModpackRef": ModpackRef{}, "ServerModpack": ServerModpack{}, "ServerTemplate": ServerTemplate{}, "TemplateSettings": TemplateSettings{},
		"TemplateAddon": TemplateAddon{}, "TemplateContents": TemplateContents{}, "TemplateExport": TemplateExport{}, "TemplatePlan": TemplatePlan{},
		"PackShare": PackShare{}, "ModpackDetail": ModpackDetail{},
		"ApiErrorBody": Error{}, "DiscordDelivery": DiscordDelivery{}, "DiscordSettings": DiscordSettings{}, "Phrase": Phrase{},
		"PlayerDay": PlayerDay{}, "PlayerProfile": PlayerProfile{},
		"MapInfo": MapInfo{}, "MapProgress": MapProgress{}, "PublicMap": PublicMap{}, "WorldImport": WorldImport{}, "WorldImportFile": WorldImportFile{},
		"WorldImportPreview": WorldImportPreview{}, "WorldImportVersion": WorldImportVersion{}, "ImportMessage": worldimport.Message{},
		"ImportLevel": worldimport.Level{}, "ImportWorld": worldimport.World{}, "ImportPreview": worldimport.Preview{},
		"SleepStatus": SleepStatus{}, "BackupRefusal": BackupRefusal{}, "RetentionEstimate": retention.Estimate{}, "RetentionRules": retention.Rules{},
		"RetentionSettings": retention.Settings{}, "RetentionText": retention.Text{}, "OffsiteCheck": offsite.Check{}, "OffsiteProvider": offsite.Provider{},
		"OffsiteTestResult": offsite.TestResult{}, "DiskCandidate": diskusage.Candidate{}, "DiskReport": diskusage.Report{},
		"DiskServer": diskusage.ServerUsage{}, "DiskUsage": diskusage.Usage{}, "DiskWay": diskusage.Way{},
		"MemoryBudget": MemoryBudget{}, "MemorySizing": MemorySizing{}, "MemorySuggestion": MemorySuggestion{},
		"LinkProblem": machinelink.Problem{}, "MachineLink": machinelink.Status{},
	}
	// Fields the panel adds to what the agent sends, and rttMs, which
	// machinelink.Status's MarshalJSON adds.
	addedByPanel := map[string]bool{
		"ServerStatus.machineId": true, "ServerStatus.lastKnownAt": true, "ServerStatus.disputed": true,
		"AuditEntry.source": true, "AuditEntry.machineId": true, "AuditEntry.actorKind": true, "AuditEntry.actorName": true,
		"Activity.actorKind": true, "Activity.actorName": true, "MachineLink.rttMs": true,
	}
	CheckDashboardFields(t, string(src), sent, addedByPanel)
}

// CheckDashboardFields reports each field of web/src/api/types.ts that
// apitest.Undeclared finds the API doesn't send. It is also used by the
// external test for types whose packages import this one.
func CheckDashboardFields(t *testing.T, src string, sent map[string]any, addedByPanel map[string]bool) {
	t.Helper()
	for _, p := range apitest.Undeclared(src, sent, addedByPanel) {
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
		string(twofactor.KindPasswordWrong), CodePlanChanged, CodeKeyRefused, CodeAdminUnconfirmed,
		diskusage.CodeDiskSpace, retention.CodeEstimateOff,
		machinelink.ProblemVersion, machinelink.CodeDropped, machinelink.CodeHeartbeatTimeout}
	for _, k := range []gamefiles.Kind{gamefiles.KindLink, gamefiles.KindSpecial, gamefiles.KindNotFile, gamefiles.KindNotFolder, gamefiles.KindTooLarge, gamefiles.KindTooMany, gamefiles.KindChanged, gamefiles.KindBadName} {
		sent = append(sent, string(k))
	}
	for _, c := range sent {
		if codes[c] {
			t.Errorf("error code %q is used twice", c)
		}
		codes[c] = true
	}
	// The invite pages check the invites package's codes. That package
	// imports this one (through internal/minecraft), so its constants are
	// read from its source.
	for _, c := range sourceCodes(t, "../invites") {
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

// sourceCodes are the string constants named Code… in the Go files of dir.
func sourceCodes(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, spec := range g.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if !strings.HasPrefix(name.Name, "Code") || i >= len(vs.Values) {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if v, err := strconv.Unquote(lit.Value); err == nil {
							out = append(out, v)
						}
					}
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("found no Code constants in %s", dir)
	}
	return out
}

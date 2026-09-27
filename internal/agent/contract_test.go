package agent

import (
	"os"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api/apitest"
	"github.com/CIYAhq/playkeeper/internal/schedule"
)

// The agent sends some of the dashboard's types as its own, or as those of
// packages internal/api can't import, so TestTheDashboardDeclaresOnlyFieldsTheAPISends
// can't check them.
func TestTheDashboardDeclaresOnlyFieldsTheAgentSends(t *testing.T) {
	src, err := os.ReadFile("../../web/src/api/types.ts")
	if err != nil {
		t.Fatal(err)
	}
	sent := map[string]any{
		"SleepView": sleepView{}, "Schedule": scheduleView{}, "ScheduleLastRun": schedule.Run{}, "SchedulePayload": schedule.Payload{},
		"SchedulePreview": schedulePreview{}, "ScheduleRun": scheduleRunView{}, "SchedulesResponse": schedulesResponse{},
		"ScheduleTiming": schedule.Timing{}, "AutomaticBackups": automaticBackups{}, "BackupRulesView": backupRulesView{},
		"OffsiteCopy": offsiteCopy{}, "OffsiteNewKey": offsiteNewKey{}, "OffsitePending": pendingView{}, "OffsiteS3": s3View{},
		"OffsiteSFTP": sftpView{}, "OffsiteView": offsiteView{}, "RecoverView": recoverView{},
	}
	for _, p := range apitest.Undeclared(string(src), sent, nil) {
		t.Error(p)
	}
}

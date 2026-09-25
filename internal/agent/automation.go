package agent

// Wave 7 (0.4.0): schedules, sleep when nobody's playing, backup rules with
// encrypted copies somewhere else, and disk space. This file holds what they
// share: a server's automation state, the loops that start with the server,
// what a deleted server leaves behind, and the routes.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"sync"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/backup/retention"
	"github.com/CIYAhq/playkeeper/internal/diskusage"
	"github.com/CIYAhq/playkeeper/internal/offsite"
	"github.com/CIYAhq/playkeeper/internal/schedule"
	"github.com/CIYAhq/playkeeper/internal/sleep"
)

// automation is a server's schedules, sleep and off-site state. Its mutex
// guards it; none of it is held across a Docker call or an operation.
type automation struct {
	mu     sync.Mutex
	runner *schedule.Runner

	sleepSet *sleep.Settings
	tracker  *sleep.Tracker
	decision sleep.Decision
	standIn  *sleep.Manager
	falling  bool

	kick   chan struct{}
	upload *uploadProgress
}

// startAutomation runs the server's schedule runner, its sleep watch and its
// off-site uploader until the server's context ends.
func (s *server) startAutomation() {
	for _, fn := range []func(context.Context){s.scheduleLoop, s.sleepLoop, s.offsiteLoop} {
		fn := fn
		s.wg.Add(1)
		s.loops.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.loops.Done()
			fn(s.ctx)
		}()
	}
}

// forgetAutomation deletes a deleted server's schedules, sleep periods and
// off-site records in the transaction that deletes the server. Copies at the
// destination stay there; the recovery key file opens them.
func (s *server) forgetAutomation(tx *sql.Tx) error {
	for _, q := range []string{
		`DELETE FROM schedules WHERE server_id = ?`, `DELETE FROM schedule_runs WHERE server_id = ?`,
		`DELETE FROM sleep_periods WHERE server_id = ?`, `DELETE FROM offsite WHERE server_id = ?`,
		`DELETE FROM offsite_copies WHERE server_id = ?`, `DELETE FROM offsite_uploads WHERE server_id = ?`,
	} {
		if _, err := tx.Exec(q, s.id); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(s.spoolDir()); err != nil {
		s.log.Warn("could not remove a deleted server's off-site spool", "server", s.id, "err", err)
	}
	return nil
}

// automationStatus adds the sleep setting to a server's status, and shows a
// server stopped to sleep as asleep.
func (s *server) automationStatus(st *api.ServerStatus) {
	st.Sleep = s.sleepStatus(st.Desired)
	if st.Desired == api.DesiredSleeping && st.Operation == nil && st.Phase == api.PhaseStopped {
		st.Phase = api.PhaseAsleep
	}
}

// machineAutomation adds what the wave counts for the whole machine.
func (a *Agent) machineAutomation(m *api.Machine) {
	m.SleepingMemoryMB = a.sleepingMemoryMB()
}

// backupKind is the kind of backup an operation's actor makes.
func backupKind(actor string) string {
	if _, ok := schedule.ParseActor(actor); ok {
		return retention.KindScheduled
	}
	return retention.KindManual
}

// automationError turns an error from the schedule, sleep, retention,
// offsite or diskusage package into the agent's API error, with the field at
// fault and the package's reason code and values for the dashboard.
func automationError(err error) error {
	var (
		ve *schedule.ValidationError
		se *sleep.Error
		re *retention.Error
		oe *offsite.Error
		de *diskusage.Error
	)
	switch {
	case errors.As(err, &ve):
		return &apiError{Status: http.StatusBadRequest, Code: api.CodeInvalid, Msg: ve.Msg, Hint: ve.Hint, Field: ve.Field, Reason: ve.Code, Params: ve.Params}
	case errors.As(err, &se):
		status := http.StatusBadRequest
		if se.Code == "port_in_use" || se.Code == "listen_failed" {
			status = http.StatusConflict
		}
		return &apiError{Status: status, Code: api.CodeInvalid, Msg: se.Msg, Hint: se.Hint, Reason: se.Code, Params: se.Params}
	case errors.As(err, &re):
		return &apiError{Status: http.StatusBadRequest, Code: api.CodeInvalid, Msg: re.Msg, Hint: re.Hint, Field: re.Field, Reason: re.Kind,
			Params: map[string]any{"value": re.Value, "min": re.Min, "max": re.Max}}
	case errors.As(err, &oe):
		status := http.StatusBadGateway
		switch oe.Kind {
		case offsite.KindInvalidConfig:
			status = http.StatusBadRequest
		case offsite.KindCanceled:
			status = http.StatusServiceUnavailable
		case offsite.KindNotEnoughSpace:
			status = http.StatusInsufficientStorage
		}
		params := map[string]any{}
		for k, v := range oe.Params() {
			params[k] = v
		}
		if oe.HostKey != nil {
			params["hostKey"] = oe.HostKey.Key
			params["hostKeyType"] = oe.HostKey.Type
			params["fingerprint"] = oe.HostKey.Fingerprint
		}
		code := api.CodeInvalid
		if status != http.StatusBadRequest {
			code = api.CodeConflict
		}
		return &apiError{Status: status, Code: code, Msg: oe.Msg, Hint: oe.Hint, Field: oe.Field, Reason: string(oe.Kind), Params: params}
	case errors.As(err, &de):
		status := http.StatusBadRequest
		if de.Kind == "canceled" {
			status = http.StatusServiceUnavailable
		}
		return &apiError{Status: status, Code: api.CodeInvalid, Msg: de.Msg, Hint: de.Hint, Field: de.Field, Reason: de.Kind}
	}
	return err
}

func (a *Agent) automationRoutes() []Route {
	srv := a.withServer
	return []Route{
		{"GET", "/v1/servers/{id}/schedules", srv((*server).hSchedules)},
		{"POST", "/v1/servers/{id}/schedules", srv((*server).hScheduleCreate)},
		{"POST", "/v1/servers/{id}/schedules/preview", srv((*server).hSchedulePreview)},
		{"GET", "/v1/servers/{id}/schedules/runs", srv((*server).hScheduleRuns)},
		{"POST", "/v1/servers/{id}/schedules/{sid}", srv((*server).hScheduleUpdate)},
		{"DELETE", "/v1/servers/{id}/schedules/{sid}", srv((*server).hScheduleDelete)},
		{"GET", "/v1/servers/{id}/sleep", srv((*server).hSleep)},
		{"POST", "/v1/servers/{id}/sleep", srv((*server).hSleepSet)},
		{"GET", "/v1/servers/{id}/backup-rules", srv((*server).hBackupRules)},
		{"POST", "/v1/servers/{id}/backup-rules", srv((*server).hBackupRulesSet)},
		{"POST", "/v1/servers/{id}/backup-rules/estimate", srv((*server).hBackupRulesEstimate)},
		{"GET", "/v1/servers/{id}/offsite", srv((*server).hOffsite)},
		{"POST", "/v1/servers/{id}/offsite", srv((*server).hOffsiteSet)},
		{"POST", "/v1/servers/{id}/offsite/test", srv((*server).hOffsiteTest)},
		{"POST", "/v1/servers/{id}/offsite/ssh-key", srv((*server).hOffsiteSSHKey)},
		{"POST", "/v1/servers/{id}/offsite/retry", srv((*server).hOffsiteRetry)},
		{"GET", "/v1/servers/{id}/offsite/recovery-key", srv((*server).hOffsiteRecoveryKey)},
		{"POST", "/v1/servers/{id}/offsite/new-key", srv((*server).hOffsiteNewKey)},
		{"GET", "/v1/servers/{id}/offsite/copies", srv((*server).hOffsiteCopies)},
		{"POST", "/v1/servers/{id}/offsite/restore", srv((*server).hOffsiteRestore)},
		{"GET", "/v1/disk", a.hDisk},
		{"POST", "/v1/disk/clean", a.hDiskClean},
	}
}

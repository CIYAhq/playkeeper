package agent

// Wave 7 (0.4.0): schedules, sleep when nobody's playing, backup rules with
// encrypted copies somewhere else, and disk space. This file holds what they
// share: a server's automation state, the loops that start with the server,
// what a deleted server leaves behind, and the routes.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	// wakePending is set while a join's wake waits for another operation.
	wakePending bool

	kick   chan struct{}
	claim  *uploadClaim
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

// keyNotSaved refuses deleting a server that has encryption keys while its
// recovery key was never downloaded: forgetAutomation deletes the key with
// the server, and nothing would open the copies made with it again. Keys
// exist once copies were turned on, and the copies they open aren't only
// the recorded ones: a change of place forgets those, and they stay where
// they were. When the settings for copies can't be read, it can't tell, so
// it refuses as well, with reason recovery_key_unknown.
func (s *server) keyNotSaved() error {
	row, err := s.loadOffsite()
	if err != nil {
		return s.keyUnknown(err)
	}
	if !row.hasKeys || row.keySavedAt != nil {
		return nil
	}
	place := offsitePlace(row.cfg.Config)
	params := map[string]any{"place": place}
	hint := "Nothing else opens the copies made with it, even ones Playkeeper no longer lists."
	if copies, err := recordedCopies(s); err != nil {
		s.log.Warn("the recorded copies couldn't be counted", "server", s.id, "err", err)
	} else {
		params["copies"] = copies
		if copies > 0 {
			hint = "Nothing else opens its copies on " + place + "."
		}
	}
	return &apiError{Status: http.StatusConflict, Code: api.CodeConflict, Reason: "recovery_key_not_saved",
		Msg:    fmt.Sprintf("Deleting %s deletes its recovery key, which was never downloaded.", s.name()),
		Hint:   hint + " Download the recovery key first, or confirm deleting the server without it.",
		Params: params}
}

// recordedCopies counts the copies of s recorded where copies go now. Tests
// make it fail.
var recordedCopies = func(s *server) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM offsite_copies WHERE server_id = ?`, s.id).Scan(&n)
	return n, err
}

// reasonKeyUnknown is the refusal of a delete that can't tell whether it
// would delete the only key to the server's copies.
const reasonKeyUnknown = "recovery_key_unknown"

// keyUnknown refuses deleting a server whose settings for copies couldn't
// be read, as keyNotSaved refuses when the key was never downloaded: the
// same confirmation deletes it.
func (s *server) keyUnknown(err error) error {
	s.log.Warn("the settings for copies couldn't be read, so deleting the server needs confirming", "server", s.id, "err", err)
	return &apiError{Status: http.StatusConflict, Code: api.CodeConflict, Reason: reasonKeyUnknown,
		Msg:  fmt.Sprintf("Playkeeper couldn't read the settings for %s's copies, so it can't tell whether deleting it deletes the only key to them.", s.name()),
		Hint: "Try again in a moment, or confirm deleting the server without its recovery key."}
}

// automationStatus adds the sleep setting and the refused scheduled backups
// to a server's status, and shows a server stopped to sleep as asleep.
func (s *server) automationStatus(st *api.ServerStatus) {
	st.Sleep = s.sleepStatus(st.Desired)
	st.BackupRefused = s.backupRefusal()
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
		{"POST", "/v1/servers/{id}/offsite/copies/{name}/check", srv((*server).hOffsiteCheck)},
		{"DELETE", "/v1/servers/{id}/offsite/copies/{name}", srv((*server).hOffsiteCopyDelete)},
		{"POST", "/v1/servers/{id}/offsite/restore", srv((*server).hOffsiteRestore)},
		{"POST", "/v1/servers/{id}/offsite/restore/cancel", srv((*server).hOffsiteRestoreCancel)},
		{"POST", "/v1/offsite/recover", a.hRecoverList},
		{"POST", "/v1/offsite/recover/restore", a.hRecoverRestore},
		{"GET", "/v1/disk", a.hDisk},
		{"POST", "/v1/disk/clean", a.hDiskClean},
	}
}

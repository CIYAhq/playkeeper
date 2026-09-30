package agent

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/backup"
)

// Kept backups: a deleted server's final backup, kept for a while after it.
// The dashboard deletes a Playkeeper Cloud customer's servers once their
// grace period ends, and keeps each one's final backup for them to download.
// The archive stays in the backups folder under its name; only its record
// moves here from the server's backups, which go with the server.

// maxKeepDays is the longest a final backup is kept.
const maxKeepDays = 90

// keepFinal is how long a deleted server's final backup is kept, and its
// label; days is 0 when none is kept. whole keeps the server's whole folder
// (see api.DeleteServerRequest.KeepWhole).
type keepFinal struct {
	days    int
	keptFor string
	whole   bool
}

func checkKeep(req api.DeleteServerRequest) (keepFinal, error) {
	switch {
	case req.KeepFinalBackupDays == 0 && req.KeptFor == "" && !req.KeepWhole:
		return keepFinal{}, nil
	case req.KeepFinalBackupDays < 1 || req.KeepFinalBackupDays > maxKeepDays:
		return keepFinal{}, errInvalid("A final backup is kept from 1 to %d days.", maxKeepDays)
	case req.KeptFor != "" && !reDiskLimitID.MatchString(req.KeptFor):
		return keepFinal{}, errInvalid("A kept backup's label is up to 64 lower-case letters, digits and dashes.")
	}
	return keepFinal{days: req.KeepFinalBackupDays, keptFor: req.KeptFor, whole: req.KeepWhole}, nil
}

// finalBackup is the backup of the stopped server to keep once it's
// deleted: a new one, or its newest that reads back whole when the machine
// hasn't the room for a new one or it doesn't read back. With whole it's a
// new one of its whole folder or none, since its backups lack what a move
// carried beyond them. fresh says it's new, so it's the deletion's to drop
// if the server stays after all.
func (s *server) finalBackup(actor string, whole bool) (b *api.Backup, fresh bool, err error) {
	b, err = s.finalArchive(actor, whole)
	switch {
	case err == nil:
		return b, true, nil
	case whole:
		return nil, false, err
	}
	s.log.Warn("could not make a deleted server's final backup; keeping its newest instead", "err", err)
	list, lerr := s.listBackups("")
	if lerr != nil {
		return nil, false, lerr
	}
	for _, old := range list {
		if b, verr := s.soundBackup(old.ID); verr == nil {
			return b, false, nil
		}
	}
	return nil, false, fmt.Errorf("no backup of it could be kept (%s)", clause(err))
}

// finalArchive makes an archive of the stopped server, of its whole folder
// when whole is set, and reads it back. One that doesn't read back is
// dropped.
func (s *server) finalArchive(actor string, whole bool) (*api.Backup, error) {
	sc, err := s.serverConfig()
	if err != nil {
		return nil, err
	}
	if sc == nil {
		return nil, errNotCreated()
	}
	create, need := archiver(backup.Create), allowlistedSize(s.dataDir())
	if whole {
		size, err := backup.MeasureWhole(s.dataDir(), archiveLimits())
		if err != nil {
			return nil, err
		}
		create, need = backup.CreateWhole, size.ArchiveBytes()
	}
	if free, _, err := s.opts.DiskUsage(s.cfg.BackupsDir()); err == nil && free < need+minFreeAfterBackup {
		return nil, errors.New("the machine hasn't the room for another backup")
	}
	b, err := s.writeArchive(*sc, "final", actor, "kept after the server was deleted", create)
	if err != nil {
		return nil, err
	}
	sound, err := s.soundBackup(b.ID)
	if err != nil {
		s.dropBackup(b)
		return nil, err
	}
	return sound, nil
}

// dropBackup removes a backup that's no use, its archive and its record.
func (s *server) dropBackup(b *api.Backup) {
	os.Remove(s.backupPath(b.FileName))
	os.Remove(s.backupPath(b.FileName) + ".sha256")
	_, _ = s.db.Exec(`DELETE FROM backups WHERE id = ?`, b.ID)
}

// soundBackup reads the backup back, and returns it once it's whole.
func (s *server) soundBackup(id string) (*api.Backup, error) {
	b, err := s.verifyBackup(id)
	if err != nil {
		return nil, err
	}
	if b.Verified == nil || !*b.Verified {
		return nil, fmt.Errorf("backup %s failed verification: %s", b.ID, b.VerifyError)
	}
	return b, nil
}

// keep records b as the server's kept final backup.
func (s *server) keep(b *api.Backup, keep keepFinal) (*api.KeptBackup, error) {
	k := &api.KeptBackup{ID: b.ID, ServerID: s.id, ServerName: s.name(), KeptFor: keep.keptFor, FileName: b.FileName, SizeBytes: b.SizeBytes,
		SHA256: b.SHA256, MadeAt: b.CreatedAt, ExpiresAt: s.now().UTC().Add(time.Duration(keep.days) * 24 * time.Hour)}
	_, err := s.db.Exec(`INSERT INTO kept_backups(id, server_id, server_name, kept_for, file_name, size_bytes, sha256, made_at, expires_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		k.ID, k.ServerID, k.ServerName, k.KeptFor, k.FileName, k.SizeBytes, k.SHA256, k.MadeAt.UnixMilli(), k.ExpiresAt.UnixMilli())
	if err != nil {
		return nil, err
	}
	return k, nil
}

// keptBackups lists the kept backups, newest first, those kept for keptFor
// when it's set.
func (a *Agent) keptBackups(keptFor string) ([]api.KeptBackup, error) {
	q := `SELECT id, server_id, server_name, kept_for, file_name, size_bytes, sha256, made_at, expires_at FROM kept_backups`
	var args []any
	if keptFor != "" {
		q += ` WHERE kept_for = ?`
		args = append(args, keptFor)
	}
	rows, err := a.db.Query(q+` ORDER BY made_at DESC, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.KeptBackup{}
	for rows.Next() {
		var k api.KeptBackup
		var made, expires int64
		if err := rows.Scan(&k.ID, &k.ServerID, &k.ServerName, &k.KeptFor, &k.FileName, &k.SizeBytes, &k.SHA256, &made, &expires); err != nil {
			return nil, err
		}
		k.MadeAt, k.ExpiresAt = time.UnixMilli(made).UTC(), time.UnixMilli(expires).UTC()
		out = append(out, k)
	}
	return out, rows.Err()
}

func (a *Agent) keptBackup(id string) (*api.KeptBackup, error) {
	if err := validBackupID(id); err != nil {
		return nil, err
	}
	list, err := a.keptBackups("")
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i], nil
		}
	}
	return nil, errNotFound("Kept backup")
}

// forgetKept removes a kept backup, its archive and its record.
func (a *Agent) forgetKept(k *api.KeptBackup) error {
	for _, p := range []string{a.backupPath(k.FileName), a.backupPath(k.FileName) + ".sha256"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	_, err := a.db.Exec(`DELETE FROM kept_backups WHERE id = ?`, k.ID)
	return err
}

// pruneKeptBackups removes the kept backups whose time is up.
func (a *Agent) pruneKeptBackups() {
	list, err := a.keptBackups("")
	if err != nil {
		a.log.Error("could not list the kept backups", "err", err)
		return
	}
	now := a.now()
	for i := range list {
		k := &list[i]
		if now.Before(k.ExpiresAt) {
			continue
		}
		if err := a.forgetKept(k); err != nil {
			a.log.Warn("could not remove a kept backup whose time is up", "backup", k.ID, "err", err)
			continue
		}
		a.audit("playkeeper", "kept_backup.expired", k.ID, "succeeded", k.ServerName+", "+k.FileName)
	}
}

func (a *Agent) hKeptBackups(w http.ResponseWriter, r *http.Request) {
	keptFor := r.URL.Query().Get("keptFor")
	if keptFor != "" && !reDiskLimitID.MatchString(keptFor) {
		writeError(w, errInvalid("A kept backup's label is up to 64 lower-case letters, digits and dashes."))
		return
	}
	list, err := a.keptBackups(keptFor)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *Agent) hKeptBackupDownload(w http.ResponseWriter, r *http.Request) {
	k, err := a.keptBackup(r.PathValue("kid"))
	if err != nil {
		writeError(w, err)
		return
	}
	f, err := os.Open(a.backupPath(k.FileName))
	if err != nil {
		writeError(w, errNotFound("Kept backup file"))
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+k.FileName+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(k.SizeBytes, 10))
	w.Header().Set("X-Playkeeper-SHA256", k.SHA256)
	if n, err := io.Copy(w, f); err == nil && n == k.SizeBytes {
		a.audit(actorFromHeader(r), "kept_backup.downloaded", k.ID, "succeeded", k.FileName)
	}
}

func (a *Agent) hKeptBackupDelete(w http.ResponseWriter, r *http.Request) {
	actor, err := validActor(r.URL.Query().Get("actor"))
	if err != nil {
		writeError(w, err)
		return
	}
	k, err := a.keptBackup(r.PathValue("kid"))
	if err != nil {
		writeError(w, err)
		return
	}
	if err := a.forgetKept(k); err != nil {
		writeError(w, err)
		return
	}
	a.audit(actor, "kept_backup.deleted", k.ID, "succeeded", k.ServerName+", "+k.FileName)
	w.WriteHeader(http.StatusNoContent)
}

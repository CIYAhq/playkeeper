package agent

// Wave 7 (0.4.0): bringing a server back on a new machine from its copies
// somewhere else, opened with the server's recovery key file.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/offsite"
)

// recoverRequest says where a server's copies are and holds the recovery
// key file that opens them. Nothing is saved: the new machine only reads.
type recoverRequest struct {
	Actor       string         `json:"actor"`
	RecoveryKey string         `json:"recoveryKey"`
	Config      offsite.Config `json:"config"`
	SecretKey   string         `json:"secretKey,omitempty"`
	Password    string         `json:"password,omitempty"`
	// HostKey is the SFTP host key the user confirmed, as the refusal
	// with reason host_key_unknown returned it.
	HostKey string `json:"hostKey,omitempty"`
	// Name is the copy to bring back.
	Name string `json:"name,omitempty"`
}

type recoverCopy struct {
	Name      string    `json:"name"`
	SizeBytes int64     `json:"sizeBytes"`
	CreatedAt time.Time `json:"createdAt"`
}

type recoverView struct {
	Server string        `json:"server"`
	Keys   int           `json:"keys"`
	MadeAt *time.Time    `json:"madeAt,omitempty"`
	Place  string        `json:"place"`
	Copies []recoverCopy `json:"copies"`
}

var reCopyTime = regexp.MustCompile(`-(\d{8}-\d{6})-`)

// copyTime is when a copy's backup was made: from its name, which holds
// the backup's id, or else when the copy was written.
func copyTime(o offsite.Object) time.Time {
	if m := reCopyTime.FindStringSubmatch(o.Archive); m != nil {
		if t, err := time.Parse("20060102-150405", m[1]); err == nil {
			return t.UTC()
		}
	}
	return o.LastModified.UTC()
}

// recoverDest opens the destination req names with the keys in its
// recovery key file. Over S3 the copies are in the folder the file names,
// or else in the one Playkeeper gives a server of that name.
func (a *Agent) recoverDest(req recoverRequest, spool string) (offsiteDest, offsite.Recovery, offsite.Config, error) {
	rec, err := offsite.ReadRecoveryFile(strings.NewReader(req.RecoveryKey))
	if err != nil {
		return nil, rec, offsite.Config{}, err
	}
	c := offsite.Config{Type: req.Config.Type}
	switch c.Type {
	case offsite.TypeS3:
		prefix := rec.Folder
		if prefix == "" {
			prefix = "playkeeper/" + slugFor(rec.Server) + "/"
		}
		c.S3 = normalizeS3(req.Config.S3, prefix)
		c.S3.SecretKey = offsite.NewSecret(strings.TrimSpace(req.SecretKey))
	case offsite.TypeSFTP:
		sc := req.Config.SFTP
		sc.Host, sc.User, sc.Folder = strings.TrimSpace(sc.Host), strings.TrimSpace(sc.User), strings.TrimSpace(sc.Folder)
		if sc.Folder == "" {
			sc.Folder = rec.Folder
		}
		sc.HostKey = strings.TrimSpace(req.HostKey)
		sc.Password = offsite.NewSecret(req.Password)
		c.SFTP = sc
	}
	if err := c.Validate(); err != nil {
		return nil, rec, c, err
	}
	dest, err := openOffsite(c, rec.Keys, offsite.Options{
		SpoolDir: spool,
		Now:      a.now,
		DiskFree: func(d string) (int64, error) {
			free, _, err := a.opts.DiskUsage(d)
			return free, err
		},
	})
	return dest, rec, c, err
}

func (a *Agent) recoverSpool() (string, error) {
	dir := filepath.Join(a.cfg.StagingDir(), "recover-"+randomSecret(8))
	return dir, os.MkdirAll(dir, 0o700)
}

func (a *Agent) decodeRecover(w http.ResponseWriter, r *http.Request) (recoverRequest, string, bool) {
	var req recoverRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return req, "", false
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return req, "", false
	}
	return req, actor, true
}

// hRecoverList opens the copies with the recovery key file and lists them,
// newest first.
func (a *Agent) hRecoverList(w http.ResponseWriter, r *http.Request) {
	req, actor, ok := a.decodeRecover(w, r)
	if !ok {
		return
	}
	spool, err := a.recoverSpool()
	if err != nil {
		writeError(w, err)
		return
	}
	defer os.RemoveAll(spool)
	dest, rec, c, err := a.recoverDest(req, spool)
	if err != nil {
		writeError(w, automationError(err))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), offsiteTestTimeout)
	defer cancel()
	objs, err := dest.List(ctx)
	if err != nil {
		var oe *offsite.Error
		detail := "error"
		if errors.As(err, &oe) {
			detail = string(oe.Kind)
		}
		a.auditFor("", actor, "offsite.recover.listed", offsitePlace(c), "failed", detail)
		writeError(w, automationError(err))
		return
	}
	view := recoverView{Server: rec.Server, Keys: 1 + len(rec.Keys.Old), Place: offsitePlace(c), Copies: []recoverCopy{}}
	if !rec.Made.IsZero() {
		view.MadeAt = &rec.Made
	}
	for _, o := range objs {
		view.Copies = append(view.Copies, recoverCopy{Name: o.Name, SizeBytes: o.Size, CreatedAt: copyTime(o)})
	}
	slices.SortFunc(view.Copies, func(x, y recoverCopy) int {
		return cmp.Or(y.CreatedAt.Compare(x.CreatedAt), strings.Compare(y.Name, x.Name))
	})
	a.auditFor("", actor, "offsite.recover.listed", view.Place, "succeeded", fmt.Sprintf("%d copies of %s", len(view.Copies), rec.Server))
	writeJSON(w, http.StatusOK, view)
}

// hRecoverRestore downloads a copy, decrypts and checks it, and stages it
// as a new server: nothing is made until the restore is confirmed.
func (a *Agent) hRecoverRestore(w http.ResponseWriter, r *http.Request) {
	req, actor, ok := a.decodeRecover(w, r)
	if !ok {
		return
	}
	archive, ok := strings.CutSuffix(strings.TrimSpace(req.Name), ".age")
	if !ok || !offsite.ValidName(archive) {
		writeError(w, &apiError{Status: http.StatusBadRequest, Code: api.CodeInvalid, Field: "name", Msg: "That is not the name of a backup's copy."})
		return
	}
	spool, err := a.recoverSpool()
	if err != nil {
		writeError(w, err)
		return
	}
	dest, rec, c, err := a.recoverDest(req, spool)
	if err != nil {
		os.RemoveAll(spool)
		writeError(w, automationError(err))
		return
	}
	name := offsite.CopyName(archive)
	op, err := a.beginMachineOp("offsite-recover", actor, func(ctx context.Context, h *opHandle) error {
		defer os.RemoveAll(spool)
		h.set("name", name)
		h.set("server", rec.Server)
		h.set("place", offsitePlace(c))
		h.phase("listing")
		objs, err := dest.List(ctx)
		if err != nil {
			return downloadStopped(a.ctx.Err() != nil, h, automationError(err))
		}
		dl := offsite.Download{Name: name, Dir: spool}
		for _, o := range objs {
			if o.Name == name {
				dl.Size = o.Size
			}
		}
		if dl.Size == 0 {
			return errNotFound("Copy")
		}
		h.set("size", dl.Size)
		h.phase("downloading")
		got, err := dest.Download(ctx, dl)
		if err != nil {
			return downloadStopped(a.ctx.Err() != nil, h, automationError(err))
		}
		h.phase("checking")
		f, err := os.Open(got.Path)
		if err != nil {
			return err
		}
		defer f.Close()
		p, err := a.stageArchive(f, "copy "+got.Name, a.uploadLimit(), nil)
		if err != nil {
			a.auditFor("", actor, "restore.staged", archive, "refused", err.Error())
			return err
		}
		a.auditFor("", actor, "restore.staged", archive, "validated", "sha256 "+p.SHA256+" from "+offsitePlace(c))
		h.set("restoreId", p.ID)
		return nil
	})
	if err != nil {
		os.RemoveAll(spool)
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/CIYAhq/playkeeper/internal/names"
)

// Settings the service keeps in names.db.
const (
	// settingBase is the base domain the names' records are published under.
	settingBase = "base"
	// settingPreviousBase is the base domain before the last move.
	settingPreviousBase = "previous_base"
)

func getSetting(ctx context.Context, q queryer, key string) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func setSetting(ctx context.Context, q queryer, key, value string) error {
	_, err := q.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// adoptBase moves the names to Config.Base when their records were
// published under another base domain ("Moving names to another domain" in
// services/names/README.md). Names keep their keys, addresses, servers and
// liveness checks. Every active name is marked for the retry job, which
// writes its records into the new zone within minutes. Challenges and the
// certificate history go, since every certificate under the new base is
// new to Let's Encrypt. The old zone is never touched: its records keep
// answering until the owner deletes them. Requests signed for the old base
// are answered with names.CodeUpdateRequired from then on.
func (s *Service) adoptBase(ctx context.Context) error {
	var from string
	var moved int64
	err := s.writeTx(ctx, func(q queryer) error {
		stored, err := getSetting(ctx, q, settingBase)
		switch {
		case err != nil:
			return err
		case stored == s.base:
			return nil
		case stored == "":
			return setSetting(ctx, q, settingBase, s.base)
		}
		for _, query := range []string{`DELETE FROM challenges`, `DELETE FROM cert_sets`} {
			if _, err := q.ExecContext(ctx, query); err != nil {
				return err
			}
		}
		res, err := q.ExecContext(ctx, `UPDATE names SET version = version + 1, sync_failures = 0, sync_after = 0 WHERE state = ?`, names.StateActive)
		if err != nil {
			return err
		}
		if moved, err = res.RowsAffected(); err != nil {
			return err
		}
		if err := setSetting(ctx, q, settingPreviousBase, stored); err != nil {
			return err
		}
		from = stored
		return setSetting(ctx, q, settingBase, s.base)
	})
	if err != nil {
		return err
	}
	if from != "" {
		s.log.Info("The base domain changed: names keep their owners, their records are published in the new zone, and requests signed for the old base are answered with update_required",
			"from", from, "to", s.base, "names_to_publish", moved)
	}
	s.previous, err = getSetting(ctx, s.db, settingPreviousBase)
	return err
}

// signedForPrevious reports whether a request that failed the signature
// check with err is signed for the previous base domain, by an install
// from before the move.
func (s *Service) signedForPrevious(r *http.Request, body []byte, err error) bool {
	var ne *names.Error
	if s.previous == "" || !errors.As(err, &ne) || ne.Code != names.CodeBadSignature {
		return false
	}
	_, err = names.VerifyRequest(r.Header, r.Method, r.URL.RequestURI(), body, s.previous, s.now())
	return err == nil
}

// updateRequired is the answer to a request signed for the previous base
// domain. An install from before the move shows its message and hint as
// they are.
func (s *Service) updateRequired() error {
	return &names.Error{Status: http.StatusGone, Code: names.CodeUpdateRequired, Params: map[string]any{"base": s.base},
		Message: fmt.Sprintf("Free addresses now end in .%s.", s.base),
		Hint:    "Update Playkeeper to get one or keep yours."}
}

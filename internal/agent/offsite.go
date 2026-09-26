package agent

// Wave 7 (0.4.0): copies somewhere else. After each verified backup the
// server's uploader encrypts the archive to the server's key and copies it to
// S3-compatible storage or to another machine over SFTP, carrying on where an
// upload stopped. Storage secrets, the SFTP key and the encryption keys stay
// in agent.db; only the recovery key route returns the keys, as the file the
// owner keeps.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/offsite"
	"golang.org/x/crypto/ssh"
)

const (
	offsiteTestTimeout = 45 * time.Second
	offsiteCheck       = time.Minute
	// offsiteStaleAfter is how old an unfinished upload nothing will resume
	// must be before the uploader cleans it up.
	offsiteStaleAfter   = 24 * time.Hour
	offsiteFirstBackoff = 5 * time.Minute
	offsiteMaxBackoff   = 6 * time.Hour
	// offsiteMaxQueue is how many backups can wait for their copy. Waiting
	// backups are kept on this machine, so a destination that stays down
	// must not keep every backup here.
	offsiteMaxQueue  = 12
	offsiteAbortWait = 30 * time.Second
)

// offsiteDest is what the agent uses of a destination; tests replace
// openOffsite with a fake.
type offsiteDest interface {
	Upload(ctx context.Context, up offsite.Upload) (offsite.Copy, error)
	Delete(ctx context.Context, name string) error
	Abort(ctx context.Context, st *offsite.UploadState) error
	AbortStale(ctx context.Context, olderThan time.Time, keep []*offsite.UploadState) (int, error)
	Test(ctx context.Context) offsite.TestResult
	List(ctx context.Context) ([]offsite.Object, error)
	Download(ctx context.Context, dl offsite.Download) (offsite.Archive, error)
}

var openOffsite = func(cfg offsite.Config, keys offsite.Keys, o offsite.Options) (offsiteDest, error) {
	d, err := offsite.Open(cfg, keys, o)
	if err != nil {
		return nil, err
	}
	return d, nil
}

// uploadClaimed runs as the uploader starts on an upload it claimed, before
// it looks at the backup; tests hold it there.
var uploadClaimed = func(uploadJob) {}

// Sign-in methods over SFTP.
const (
	sftpAuthKey      = "key"
	sftpAuthPassword = "password"
)

// storedOffsite is the config column: the settings without their secrets,
// and how the server signs in over SFTP.
type storedOffsite struct {
	offsite.Config
	SFTPAuth string `json:"sftpAuth,omitempty"`
}

type storedIdentity struct {
	Secret    string    `json:"secret"`
	CreatedAt time.Time `json:"createdAt"`
}

// storedKeys is the keys column: the server's encryption keys, newest first.
type storedKeys struct {
	Current storedIdentity   `json:"current"`
	Old     []storedIdentity `json:"old,omitempty"`
}

func encodeKeys(k offsite.Keys) string {
	sk := storedKeys{Current: storedIdentity{Secret: k.Current.Secret.Reveal(), CreatedAt: k.Current.CreatedAt}}
	for _, id := range k.Old {
		sk.Old = append(sk.Old, storedIdentity{Secret: id.Secret.Reveal(), CreatedAt: id.CreatedAt})
	}
	b, _ := json.Marshal(sk)
	return string(b)
}

func decodeKeys(raw string) (offsite.Keys, error) {
	var sk storedKeys
	if err := json.Unmarshal([]byte(raw), &sk); err != nil {
		return offsite.Keys{}, errors.New("the stored encryption keys can't be read")
	}
	cur, err := offsite.ParseIdentity(offsite.NewSecret(sk.Current.Secret), sk.Current.CreatedAt)
	if err != nil {
		return offsite.Keys{}, err
	}
	k := offsite.Keys{Current: cur}
	for _, o := range sk.Old {
		id, err := offsite.ParseIdentity(offsite.NewSecret(o.Secret), o.CreatedAt)
		if err != nil {
			return offsite.Keys{}, err
		}
		k.Old = append(k.Old, id)
	}
	return k, nil
}

// offsiteRow is a server's saved off-site settings, with their secrets.
type offsiteRow struct {
	exists     bool
	enabled    bool
	cfg        storedOffsite
	secret     string // the S3 secret key
	password   string // the SFTP password
	privateKey string // the key Playkeeper made to sign in over SFTP
	sshPublic  string
	keys       offsite.Keys
	hasKeys    bool
	keySavedAt *time.Time
	// keySavedFolder is the folder the downloaded recovery key file names,
	// nil when not known.
	keySavedFolder *string
	copiesMade     int // to the place copies go to now
}

func (r offsiteRow) configured() bool { return r.cfg.Type != "" }

// config is the settings with the secrets the chosen sign-in uses.
func (r offsiteRow) config() offsite.Config {
	c := r.cfg.Config
	switch c.Type {
	case offsite.TypeS3:
		c.S3.SecretKey = offsite.NewSecret(r.secret)
	case offsite.TypeSFTP:
		if r.cfg.SFTPAuth == sftpAuthPassword {
			c.SFTP.Password = offsite.NewSecret(r.password)
		} else {
			c.SFTP.PrivateKey = offsite.NewSecret(r.privateKey)
		}
	}
	return c
}

func (s *server) loadOffsite() (offsiteRow, error) {
	var r offsiteRow
	var enabled int
	var config, keys string
	var saved sql.NullInt64
	var savedFolder sql.NullString
	err := s.db.QueryRow(`SELECT enabled, config, secret, password, private_key, ssh_public, keys, key_saved_at, key_saved_folder, copies_made FROM offsite WHERE server_id = ?`, s.id).
		Scan(&enabled, &config, &r.secret, &r.password, &r.privateKey, &r.sshPublic, &keys, &saved, &savedFolder, &r.copiesMade)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	r.exists, r.enabled = true, enabled == 1
	if err := json.Unmarshal([]byte(config), &r.cfg); err != nil {
		return r, errors.New("the saved settings for copies somewhere else can't be read")
	}
	if keys != "" {
		k, err := decodeKeys(keys)
		if err != nil {
			return r, err
		}
		r.keys, r.hasKeys = k, true
	}
	if saved.Valid {
		t := time.UnixMilli(saved.Int64).UTC()
		r.keySavedAt = &t
	}
	if savedFolder.Valid {
		r.keySavedFolder = &savedFolder.String
	}
	return r, nil
}

// keyFolder is the folder a recovery key file made now names: where the
// server's copies go.
func keyFolder(c storedOffsite) string {
	if c.Type == offsite.TypeSFTP {
		return c.SFTP.Folder
	}
	return c.S3.Prefix
}

// sameFolder says whether two folders a recovery key file names are one.
func sameFolder(a, b string) bool { return strings.TrimRight(a, "/") == strings.TrimRight(b, "/") }

// saveOffsite writes the settings and secrets; the keys and when the
// recovery key was saved are written by their own routes.
func (s *server) saveOffsite(r offsiteRow) error {
	cfg := r.cfg
	cfg.S3.SecretKey, cfg.SFTP.Password, cfg.SFTP.PrivateKey = offsite.Secret{}, offsite.Secret{}, offsite.Secret{}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	keys := ""
	if r.hasKeys {
		keys = encodeKeys(r.keys)
	}
	_, err = s.db.Exec(`INSERT INTO offsite(server_id, enabled, config, secret, password, private_key, ssh_public, keys, updated_at) VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(server_id) DO UPDATE SET enabled = excluded.enabled, config = excluded.config, secret = excluded.secret, password = excluded.password,
			private_key = excluded.private_key, ssh_public = excluded.ssh_public, keys = excluded.keys, updated_at = excluded.updated_at`,
		s.id, boolInt(r.enabled), string(b), r.secret, r.password, r.privateKey, r.sshPublic, keys, s.now().UnixMilli())
	return err
}

// spoolDir is where the server's copies are encrypted before they are sent.
func (s *server) spoolDir() string { return filepath.Join(s.cfg.DataDir, "offsite-spool", s.id) }

func (s *server) openDest(r offsiteRow, keys offsite.Keys) (offsiteDest, error) {
	dir := s.spoolDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return openOffsite(r.config(), keys, offsite.Options{
		SpoolDir: dir,
		Now:      s.now,
		DiskFree: func(d string) (int64, error) {
			free, _, err := s.opts.DiskUsage(d)
			return free, err
		},
	})
}

// offsiteIdentity is where copies end up: a change makes the recorded
// copies someone else's.
func offsiteIdentity(c offsite.Config) string {
	switch c.Type {
	case offsite.TypeS3:
		return "s3|" + strings.ToLower(c.S3.Endpoint) + "|" + c.S3.Bucket + "|" + strings.Trim(c.S3.Prefix, "/")
	case offsite.TypeSFTP:
		return fmt.Sprintf("sftp|%s|%d|%s|%s", strings.ToLower(c.SFTP.Host), sftpPort(c.SFTP), c.SFTP.User, strings.TrimRight(c.SFTP.Folder, "/"))
	}
	return ""
}

func sftpPort(c offsite.SFTPConfig) int {
	if c.Port == 0 {
		return 22
	}
	return c.Port
}

// offsitePlace names the destination the way the page does: the storage
// service, or the other machine's name.
func offsitePlace(c offsite.Config) string {
	switch c.Type {
	case offsite.TypeS3:
		if c.S3.Provider != "other" {
			for _, p := range offsite.Providers() {
				if p.ID == c.S3.Provider {
					return p.Name
				}
			}
		}
		if u, err := url.Parse(c.S3.Endpoint); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
		return "S3 storage"
	case offsite.TypeSFTP:
		return c.SFTP.Host
	}
	return ""
}

var s3Regions = map[string]*regexp.Regexp{
	"aws":     regexp.MustCompile(`(?:^|\.)s3[.-](?:dualstack\.)?([a-z0-9-]+)\.amazonaws\.com$`),
	"b2":      regexp.MustCompile(`(?:^|\.)s3\.([a-z0-9-]+)\.backblazeb2\.com$`),
	"wasabi":  regexp.MustCompile(`(?:^|\.)s3\.([a-z0-9-]+)\.wasabisys\.com$`),
	"hetzner": regexp.MustCompile(`^([a-z0-9-]+)\.your-objectstorage\.com$`),
}

func s3ProviderOf(host string) string {
	for suffix, id := range map[string]string{
		".amazonaws.com": "aws", ".backblazeb2.com": "b2", ".r2.cloudflarestorage.com": "r2",
		".wasabisys.com": "wasabi", ".your-objectstorage.com": "hetzner",
	} {
		if strings.HasSuffix(host, suffix) {
			return id
		}
	}
	return "other"
}

// normalizeS3 fills in what the page doesn't ask for: https:// on the
// endpoint, the service, its region, path-style addressing when the address
// needs it, and a folder of the server's own in the bucket.
func normalizeS3(c offsite.S3Config, prefix string) offsite.S3Config {
	c.Endpoint = strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	if c.Endpoint != "" && !strings.Contains(c.Endpoint, "://") {
		c.Endpoint = "https://" + c.Endpoint
	}
	c.Bucket = strings.TrimSpace(c.Bucket)
	c.AccessKeyID = strings.TrimSpace(c.AccessKeyID)
	c.Region = strings.ToLower(strings.TrimSpace(c.Region))
	c.Prefix = strings.TrimSpace(c.Prefix)
	host := ""
	if u, err := url.Parse(c.Endpoint); err == nil {
		host = strings.ToLower(u.Hostname())
	}
	if c.Provider == "" {
		c.Provider = s3ProviderOf(host)
	}
	var prov offsite.Provider
	for _, p := range offsite.Providers() {
		if p.ID == c.Provider {
			prov = p
		}
	}
	if c.Region == "" {
		if re := s3Regions[c.Provider]; re != nil {
			if m := re.FindStringSubmatch(host); m != nil {
				c.Region = m[1]
			}
		}
	}
	if c.Region == "" {
		c.Region = prov.Region
	}
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	c.PathStyle = prov.PathStyle || net.ParseIP(host) != nil || strings.Contains(c.Bucket, ".")
	if c.Prefix == "" {
		c.Prefix = prefix
	}
	return c
}

// offsitePrefix is the folder a server's copies get in a bucket.
func (s *server) offsitePrefix() string {
	if row, err := s.row(); err == nil && row.Slug != "" {
		return "playkeeper/" + row.Slug + "/"
	}
	return "playkeeper/" + s.id + "/"
}

// hostKeyInfo is a pinned host key's type and fingerprint.
func hostKeyInfo(key string) (typ, fingerprint string) {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil {
		return "", ""
	}
	return k.Type(), ssh.FingerprintSHA256(k)
}

// --- the view ---

type s3View struct {
	offsite.S3Config
	SecretKeySet bool `json:"secretKeySet"`
}

type sftpView struct {
	offsite.SFTPConfig
	Auth               string `json:"auth"`
	PasswordSet        bool   `json:"passwordSet"`
	HostKeyType        string `json:"hostKeyType,omitempty"`
	HostKeyFingerprint string `json:"hostKeyFingerprint,omitempty"`
}

// sshKeyView is the key Playkeeper made to sign in over SFTP, without its
// private half.
type sshKeyView struct {
	PublicKey     string `json:"publicKey"`
	AuthorizedKey string `json:"authorizedKey"`
	Fingerprint   string `json:"fingerprint"`
}

func sshKeyOf(public string) *sshKeyView {
	if public == "" {
		return nil
	}
	v := &sshKeyView{PublicKey: public, AuthorizedKey: "restrict " + public}
	if k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(public)); err == nil {
		v.Fingerprint = ssh.FingerprintSHA256(k)
	}
	return v
}

// keyView is the server's encryption key, without its secret.
type keyView struct {
	Recipient string     `json:"recipient"`
	CreatedAt time.Time  `json:"createdAt"`
	OldKeys   int        `json:"oldKeys"`
	SavedAt   *time.Time `json:"savedAt,omitempty"`
	FileName  string     `json:"fileName"`
	// Folder is where the recovery key file says the copies are.
	Folder string `json:"folder,omitempty"`
	// Stale says the downloaded file names SavedFolder, where copies no
	// longer go, so a new machine would look for them in the wrong place.
	Stale       bool   `json:"stale,omitempty"`
	SavedFolder string `json:"savedFolder,omitempty"`
}

// offsiteCopy is a recorded copy at the destination.
type offsiteCopy struct {
	BackupID         string    `json:"backupId"`
	Kind             string    `json:"kind"`
	CreatedAt        time.Time `json:"createdAt"`
	FileName         string    `json:"fileName"`
	Name             string    `json:"name"`
	SizeBytes        int64     `json:"sizeBytes"`
	CopySizeBytes    int64     `json:"copySizeBytes"`
	MinecraftVersion string    `json:"minecraftVersion"`
	LevelName        string    `json:"levelName"`
	CopiedAt         time.Time `json:"copiedAt"`
	Checked          string    `json:"checked"`
	OnHost           bool      `json:"onHost"`
	SHA256           string    `json:"sha256,omitempty"`     // the backup's, as recorded
	CheckError       string    `json:"checkError,omitempty"` // why the last check found the copy missing or damaged
	// Removed says who removed the backup from this machine once only the
	// copy is left: "rules", or "person" with their name in RemovedBy.
	// Empty when not known.
	Removed   string `json:"removed,omitempty"`
	RemovedBy string `json:"removedBy,omitempty"`
}

// copyRecord is a copy as offsite_copies keeps it: what its upload reported,
// and why the last check found it missing or damaged, if it did.
type copyRecord struct {
	offsite.Copy
	CheckError string `json:"checkError,omitempty"`
}

// copyBackup is the ID of the backup a recorded copy was made from.
func (s *server) copyBackup(archive string) (string, bool) {
	var id string
	return id, s.db.QueryRow(`SELECT backup_id FROM offsite_copies WHERE server_id = ? AND file_name = ?`, s.id, archive).Scan(&id) == nil
}

func (s *server) copyRecord(archive string) (copyRecord, bool) {
	var raw string
	if s.db.QueryRow(`SELECT copy FROM offsite_copies WHERE server_id = ? AND file_name = ?`, s.id, archive).Scan(&raw) != nil {
		return copyRecord{}, false
	}
	var cp copyRecord
	return cp, json.Unmarshal([]byte(raw), &cp) == nil
}

// pendingView is the upload in progress, or the next one waiting.
type pendingView struct {
	BackupID    string            `json:"backupId"`
	FileName    string            `json:"fileName"`
	Uploading   bool              `json:"uploading"`
	Sent        int64             `json:"sent"`
	Total       int64             `json:"total"`
	BytesPerSec int64             `json:"bytesPerSec,omitempty"`
	Attempts    int               `json:"attempts"`
	NextAttempt *time.Time        `json:"nextAttempt,omitempty"`
	Error       string            `json:"error,omitempty"`
	Hint        string            `json:"hint,omitempty"`
	ErrorKind   string            `json:"errorKind,omitempty"`
	Params      map[string]string `json:"params,omitempty"`
	// BackupCreatedAt is when the backup being copied was made, for "Copying
	// today's 18:47 backup".
	BackupCreatedAt *time.Time `json:"backupCreatedAt,omitempty"`
}

type offsiteView struct {
	Enabled     bool               `json:"enabled"`
	Configured  bool               `json:"configured"`
	Type        string             `json:"type"`
	Place       string             `json:"place"`
	S3          *s3View            `json:"s3,omitempty"`
	SFTP        *sftpView          `json:"sftp,omitempty"`
	SSHKey      *sshKeyView        `json:"sshKey,omitempty"`
	Key         *keyView           `json:"key,omitempty"`
	LastCopy    *offsiteCopy       `json:"lastCopy,omitempty"`
	FirstCopy   bool               `json:"firstCopy,omitempty"` // LastCopy is the first made to this place
	Copies      int                `json:"copies"`
	CopiesBytes int64              `json:"copiesBytes"`
	Pending     *pendingView       `json:"pending,omitempty"`
	Queued      int                `json:"queued"`
	Providers   []offsite.Provider `json:"providers"`
}

func (s *server) offsiteView(r offsiteRow) offsiteView {
	v := offsiteView{Enabled: r.enabled, Configured: r.configured(), Type: r.cfg.Type, Place: offsitePlace(r.cfg.Config),
		SSHKey: sshKeyOf(r.sshPublic), Providers: offsite.Providers()}
	switch r.cfg.Type {
	case offsite.TypeS3:
		v.S3 = &s3View{S3Config: r.cfg.S3, SecretKeySet: r.secret != ""}
	case offsite.TypeSFTP:
		sv := &sftpView{SFTPConfig: r.cfg.SFTP, Auth: r.cfg.SFTPAuth, PasswordSet: r.password != ""}
		if sv.Auth == "" {
			sv.Auth = sftpAuthKey
		}
		sv.Port = sftpPort(r.cfg.SFTP)
		sv.HostKeyType, sv.HostKeyFingerprint = hostKeyInfo(r.cfg.SFTP.HostKey)
		v.SFTP = sv
	}
	if r.hasKeys {
		kv := &keyView{Recipient: r.keys.Current.Recipient, CreatedAt: r.keys.Current.CreatedAt, OldKeys: len(r.keys.Old), SavedAt: r.keySavedAt}
		if f, err := r.keys.RecoveryFileFor(s.name(), keyFolder(r.cfg), s.now()); err == nil {
			kv.FileName, kv.Folder = f.Name, f.Folder
		}
		if r.keySavedAt != nil && r.keySavedFolder != nil && !sameFolder(*r.keySavedFolder, kv.Folder) {
			kv.Stale, kv.SavedFolder = true, *r.keySavedFolder
		}
		v.Key = kv
	}
	copies, _ := s.offsiteCopies()
	v.Copies = len(copies)
	for i := range copies {
		v.CopiesBytes += copies[i].CopySizeBytes
		if v.LastCopy == nil || copies[i].CopiedAt.After(v.LastCopy.CopiedAt) {
			v.LastCopy = &copies[i]
		}
	}
	v.FirstCopy = v.LastCopy != nil && r.copiesMade == 1
	v.Pending, v.Queued = s.pendingUpload()
	return v
}

func (s *server) offsiteCopies() ([]offsiteCopy, error) {
	onHost := map[string]bool{}
	if list, err := s.listBackups(""); err == nil {
		for _, b := range list {
			onHost[b.ID] = true
		}
	}
	rows, err := s.db.Query(`SELECT backup_id, kind, backup_created_at, file_name, size_bytes, minecraft_version, level_name, copy, copied_at, removed_by
		FROM offsite_copies WHERE server_id = ? ORDER BY backup_created_at DESC`, s.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []offsiteCopy{}
	for rows.Next() {
		var c offsiteCopy
		var created, copied int64
		var raw, removedBy string
		if err := rows.Scan(&c.BackupID, &c.Kind, &created, &c.FileName, &c.SizeBytes, &c.MinecraftVersion, &c.LevelName, &raw, &copied, &removedBy); err != nil {
			return nil, err
		}
		var cp copyRecord
		_ = json.Unmarshal([]byte(raw), &cp)
		c.CreatedAt, c.CopiedAt = time.UnixMilli(created).UTC(), time.UnixMilli(copied).UTC()
		c.Name, c.CopySizeBytes, c.Checked, c.OnHost = offsite.CopyName(c.FileName), cp.Size, cp.Checked, onHost[c.BackupID]
		c.SHA256, c.CheckError = cp.ArchiveSHA256, cp.CheckError
		switch {
		case c.OnHost || removedBy == "":
		case removedBy == retentionActor:
			c.Removed = "rules"
		default:
			c.Removed, c.RemovedBy = "person", removedBy
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// uploadProgress is the upload running now.
type uploadProgress struct {
	backupID  string
	sent      int64
	total     int64
	startSent int64
	started   time.Time
}

// uploadClaim is the upload the uploader claimed, until it is done with the
// upload's row: trimming the queue leaves that row alone, and stopUpload
// cancels the upload through cancel, whether it has started or not.
type uploadClaim struct {
	backupID string
	cancel   context.CancelFunc
}

// storedBytes is how much of an unfinished upload the destination has.
func storedBytes(st *offsite.UploadState) int64 {
	switch {
	case st == nil:
		return 0
	case st.S3 != nil:
		var n int64
		for _, p := range st.S3.Parts {
			n += p.Size
		}
		return n
	case st.SFTP != nil:
		return st.SFTP.Written
	}
	return 0
}

func (s *server) pendingUpload() (*pendingView, int) {
	var queued int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM offsite_uploads WHERE server_id = ?`, s.id).Scan(&queued)
	if queued == 0 {
		return nil, 0
	}
	s.auto.mu.Lock()
	up := s.auto.upload
	var cur uploadProgress
	if up != nil {
		cur = *up
	}
	s.auto.mu.Unlock()
	q := `SELECT backup_id, state, attempts, next_attempt, last_error, error_hint, error_kind, error_params FROM offsite_uploads WHERE server_id = ?`
	args := []any{s.id}
	if up != nil {
		q += ` AND backup_id = ?`
		args = append(args, cur.backupID)
	}
	var p pendingView
	var state, params string
	var next int64
	if err := s.db.QueryRow(q+` ORDER BY created_at DESC LIMIT 1`, args...).Scan(&p.BackupID, &state, &p.Attempts, &next, &p.Error, &p.Hint, &p.ErrorKind, &params); err != nil {
		return nil, queued
	}
	if b, err := s.getBackup(p.BackupID); err == nil {
		created := b.CreatedAt.UTC()
		p.FileName, p.Total, p.BackupCreatedAt = b.FileName, b.SizeBytes, &created
	}
	if params != "" {
		_ = json.Unmarshal([]byte(params), &p.Params)
	}
	if up != nil {
		p.Uploading, p.Sent, p.Total = true, cur.sent, cur.total
		if el := s.now().Sub(cur.started); el > 2*time.Second && cur.sent > cur.startSent {
			p.BytesPerSec = int64(float64(cur.sent-cur.startSent) / el.Seconds())
		}
		return &p, queued
	}
	var st offsite.UploadState
	if state != "" && json.Unmarshal([]byte(state), &st) == nil {
		p.Sent, p.Total = storedBytes(&st), st.Size
	}
	if next > 0 {
		t := time.UnixMilli(next).UTC()
		p.NextAttempt = &t
	}
	return &p, queued
}

func (s *server) hOffsite(w http.ResponseWriter, r *http.Request) {
	row, err := s.loadOffsite()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.offsiteView(row))
}

// offsiteRequest changes the settings, or tests them without saving. Left
// out, a secret keeps its saved value; a secret is never returned.
type offsiteRequest struct {
	Actor     string          `json:"actor"`
	Enabled   *bool           `json:"enabled,omitempty"`
	Config    *offsite.Config `json:"config,omitempty"`
	SFTPAuth  string          `json:"sftpAuth,omitempty"`
	SecretKey *string         `json:"secretKey,omitempty"`
	Password  *string         `json:"password,omitempty"`
	// HostKey is the SFTP host key the user confirmed, as the connection
	// test returned it.
	HostKey *string `json:"hostKey,omitempty"`
	// ForgetCopies confirms a change of place that stops listing the copies
	// recorded at the old one, as the refusal with reason copies_recorded
	// asked.
	ForgetCopies bool `json:"forgetCopies,omitempty"`
}

// merge applies req to the saved settings. The password stays only while
// the machine and user it signs in to stay the same, and a confirmed host
// key only while the address does.
func (s *server) merge(row offsiteRow, req offsiteRequest) (offsiteRow, error) {
	next := row
	if req.Config != nil {
		c := *req.Config
		next.cfg = storedOffsite{Config: offsite.Config{Type: c.Type}, SFTPAuth: row.cfg.SFTPAuth}
		switch c.Type {
		case offsite.TypeS3:
			next.cfg.S3 = normalizeS3(c.S3, s.offsitePrefix())
			if row.cfg.Type != offsite.TypeS3 || row.cfg.S3.AccessKeyID != next.cfg.S3.AccessKeyID {
				next.secret = ""
			}
		case offsite.TypeSFTP:
			sc := c.SFTP
			sc.Host, sc.User, sc.Folder = strings.TrimSpace(sc.Host), strings.TrimSpace(sc.User), strings.TrimSpace(sc.Folder)
			old := row.cfg.SFTP
			sameMachine := row.cfg.Type == offsite.TypeSFTP && strings.EqualFold(old.Host, sc.Host) && sftpPort(old) == sftpPort(sc)
			sc.HostKey = ""
			if sameMachine {
				sc.HostKey = old.HostKey
			}
			if !sameMachine || old.User != sc.User {
				next.password = ""
			}
			next.cfg.SFTP = sc
		default:
			return row, automationError(next.cfg.Validate())
		}
	}
	if req.SFTPAuth != "" {
		if req.SFTPAuth != sftpAuthKey && req.SFTPAuth != sftpAuthPassword {
			return row, &apiError{Status: http.StatusBadRequest, Code: api.CodeInvalid, Field: "sftpAuth", Reason: "invalid_config", Msg: "Sign in with a key Playkeeper made, or with a password."}
		}
		next.cfg.SFTPAuth = req.SFTPAuth
	}
	if next.cfg.Type == offsite.TypeSFTP && next.cfg.SFTPAuth == "" {
		next.cfg.SFTPAuth = sftpAuthKey
	}
	if req.SecretKey != nil {
		next.secret = strings.TrimSpace(*req.SecretKey)
	}
	if req.Password != nil {
		next.password = *req.Password
	}
	if req.HostKey != nil {
		if next.cfg.Type != offsite.TypeSFTP {
			return row, errInvalid("A host key is only for copies over SFTP.")
		}
		key := strings.TrimSpace(*req.HostKey)
		if typ, _ := hostKeyInfo(key); typ == "" {
			return row, &apiError{Status: http.StatusBadRequest, Code: api.CodeInvalid, Field: "hostKey", Reason: "invalid_config",
				Msg: "The host key is not one Playkeeper can check.", Hint: "Run the connection test again and confirm the fingerprint it shows."}
		}
		next.cfg.SFTP.HostKey = key
	}
	return next, nil
}

// checkOffsite validates settings about to be used. A key Playkeeper makes
// has to exist before the machine can be told about it.
func checkOffsite(r offsiteRow) error {
	if r.cfg.Type == offsite.TypeSFTP && r.cfg.SFTPAuth == sftpAuthKey && r.privateKey == "" {
		return &apiError{Status: http.StatusBadRequest, Code: api.CodeInvalid, Field: "privateKey", Reason: "ssh_key_missing",
			Msg: "Make the key Playkeeper signs in with first.", Hint: "Then add its line to authorized_keys on the other machine."}
	}
	if err := r.config().Validate(); err != nil {
		return automationError(err)
	}
	return nil
}

func offsiteDetail(r offsiteRow) string {
	d := "off"
	if r.enabled {
		d = "on"
	}
	switch r.cfg.Type {
	case offsite.TypeS3:
		d += " · " + offsitePlace(r.cfg.Config) + " · bucket " + r.cfg.S3.Bucket
	case offsite.TypeSFTP:
		d += fmt.Sprintf(" · SFTP %s@%s:%d %s", r.cfg.SFTP.User, r.cfg.SFTP.Host, sftpPort(r.cfg.SFTP), r.cfg.SFTP.Folder)
	}
	return d
}

// onlyThere counts the copies whose backup is no longer on this machine.
func onlyThere(copies []offsiteCopy) int {
	n := 0
	for _, c := range copies {
		if !c.OnHost {
			n++
		}
	}
	return n
}

// errCopiesRecorded refuses a change of place while copies are recorded at
// the old one. They stay there, but this server stops listing, restoring
// and deleting them, so the change waits for ForgetCopies.
func errCopiesRecorded(place string, copies []offsiteCopy) error {
	n, only := len(copies), onlyThere(copies)
	msg := fmt.Sprintf("Changing where copies go forgets the %d copies on %s.", n, place)
	hint := "They stay there, but this server stops listing them, so the World tab can't restore them and the backup rules don't delete old ones there."
	if n == 1 {
		msg = fmt.Sprintf("Changing where copies go forgets the copy on %s.", place)
		hint = "It stays there, but this server stops listing it, so the World tab can't restore it."
	}
	switch {
	case only == 1 && n == 1:
		hint += " It's the only copy of its backup."
	case only == 1:
		hint += " One of them is the only copy of its backup."
	case only > 1:
		hint += fmt.Sprintf(" %d of them are the only copy of their backup.", only)
	}
	return &apiError{Status: http.StatusConflict, Code: api.CodeConflict, Reason: "copies_recorded", Msg: msg, Hint: hint + " Confirm the change to go ahead.",
		Params: map[string]any{"place": place, "copies": n, "onlyThere": only}}
}

func forgottenDetail(place string, copies []offsiteCopy) string {
	return fmt.Sprintf("%d copies on %s · %d only there", len(copies), place, onlyThere(copies))
}

func (s *server) hOffsiteSet(w http.ResponseWriter, r *http.Request) {
	var req offsiteRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	row, err := s.loadOffsite()
	if err != nil {
		writeError(w, err)
		return
	}
	next, err := s.merge(row, req)
	if err != nil {
		writeError(w, err)
		return
	}
	if req.Enabled != nil {
		next.enabled = *req.Enabled
	}
	if next.enabled || req.Config != nil || req.HostKey != nil {
		if err := checkOffsite(next); err != nil {
			writeError(w, err)
			return
		}
	}
	if next.enabled && !next.hasKeys {
		k, err := offsite.NewKeys(s.now())
		if err != nil {
			writeError(w, automationError(err))
			return
		}
		next.keys, next.hasKeys = k, true
	}
	moved := row.configured() && offsiteIdentity(row.cfg.Config) != offsiteIdentity(next.cfg.Config)
	var forgotten []offsiteCopy
	if moved {
		if forgotten, err = s.offsiteCopies(); err != nil {
			writeError(w, err)
			return
		}
		if len(forgotten) > 0 && !req.ForgetCopies {
			writeError(w, errCopiesRecorded(offsitePlace(row.cfg.Config), forgotten))
			return
		}
	}
	if err := s.saveOffsite(next); err != nil {
		writeError(w, err)
		return
	}
	if moved {
		// The recorded copies stay where they were; the rules no longer
		// reach them from here.
		_, _ = s.db.Exec(`DELETE FROM offsite_copies WHERE server_id = ?`, s.id)
		_, _ = s.db.Exec(`UPDATE offsite SET copies_made = 0 WHERE server_id = ?`, s.id)
		next.copiesMade = 0
		if len(forgotten) > 0 {
			s.audit(actor, "offsite.copies_forgotten", "server", "succeeded", forgottenDetail(offsitePlace(row.cfg.Config), forgotten))
		}
	}
	if moved || !next.enabled {
		s.stopUpload()
	}
	if next.enabled {
		_, _ = s.db.Exec(`UPDATE offsite_uploads SET next_attempt = 0, attempts = 0 WHERE server_id = ?`, s.id)
		if !row.enabled || moved {
			if list, err := s.listBackups(`verified = 1 AND kind IN ('manual', 'scheduled')`); err == nil && len(list) > 0 {
				s.queueOffsite(list[0].ID)
			}
		}
	}
	if next.cfg.Type == offsite.TypeSFTP && next.cfg.SFTP.HostKey != "" && next.cfg.SFTP.HostKey != row.cfg.SFTP.HostKey {
		_, fp := hostKeyInfo(next.cfg.SFTP.HostKey)
		s.audit(actor, "offsite.host_key_confirmed", next.cfg.SFTP.Host, "succeeded", fp)
	}
	s.audit(actor, "offsite.changed", "server", "succeeded", offsiteDetail(next))
	s.kickOffsite()
	writeJSON(w, http.StatusOK, s.offsiteView(next))
}

// hOffsiteTest tries the settings on the page, saved or not, with the
// server's key or, before copies are on, a key made for the test.
func (s *server) hOffsiteTest(w http.ResponseWriter, r *http.Request) {
	var req offsiteRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	row, err := s.loadOffsite()
	if err != nil {
		writeError(w, err)
		return
	}
	next, err := s.merge(row, req)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := checkOffsite(next); err != nil {
		writeError(w, err)
		return
	}
	keys := next.keys
	if !next.hasKeys {
		if keys, err = offsite.NewKeys(s.now()); err != nil {
			writeError(w, automationError(err))
			return
		}
	}
	dest, err := s.openDest(next, keys)
	if err != nil {
		writeError(w, automationError(err))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), offsiteTestTimeout)
	defer cancel()
	res := dest.Test(ctx)
	result, detail := "succeeded", offsitePlace(next.cfg.Config)
	if !res.OK {
		result = "failed"
		for _, c := range res.Checks {
			if !c.OK {
				detail += " · " + c.Step + ": " + string(c.Kind)
				break
			}
		}
	}
	s.audit(actor, "offsite.tested", "server", result, detail)
	writeJSON(w, http.StatusOK, res)
}

// hOffsiteSSHKey makes the key Playkeeper signs in with over SFTP, or
// returns the one it made. Only its public half leaves the agent.
func (s *server) hOffsiteSSHKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Actor   string `json:"actor"`
		Replace bool   `json:"replace,omitempty"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	row, err := s.loadOffsite()
	if err != nil {
		writeError(w, err)
		return
	}
	if row.privateKey != "" && !req.Replace {
		writeJSON(w, http.StatusOK, sshKeyOf(row.sshPublic))
		return
	}
	k, err := offsite.NewSSHKey(s.name())
	if err != nil {
		writeError(w, automationError(err))
		return
	}
	row.privateKey, row.sshPublic = k.PrivateKey.Reveal(), k.PublicKey
	if err := s.saveOffsite(row); err != nil {
		writeError(w, err)
		return
	}
	s.audit(actor, "offsite.ssh_key_made", "server", "succeeded", k.Fingerprint)
	writeJSON(w, http.StatusOK, sshKeyOf(k.PublicKey))
}

// hOffsiteRecoveryKey returns the recovery key file. The panel lets only
// the owner fetch it and names who did; the response is never cached, and
// the audit line, written before the key leaves, never holds its content.
func (s *server) hOffsiteRecoveryKey(w http.ResponseWriter, r *http.Request) {
	actor, err := validActor(r.Header.Get("X-Playkeeper-Actor"))
	if err != nil {
		writeError(w, err)
		return
	}
	row, err := s.loadOffsite()
	if err != nil {
		writeError(w, err)
		return
	}
	if !row.hasKeys {
		writeError(w, errConflict("There is no recovery key yet.", "Turn on copies somewhere else first; the key is made then."))
		return
	}
	f, err := row.keys.RecoveryFileFor(s.name(), keyFolder(row.cfg), s.now())
	if err != nil {
		writeError(w, automationError(err))
		return
	}
	body := f.Content.Reveal()
	s.audit(actor, "offsite.recovery_key.downloaded", "server", "succeeded", f.Name)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+f.Name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(body)); err != nil {
		return
	}
	_, _ = s.db.Exec(`UPDATE offsite SET key_saved_at = ?, key_saved_folder = ? WHERE server_id = ?`, s.now().UnixMilli(), f.Folder, s.id)
}

// hOffsiteNewKey makes a new encryption key for new copies and keeps the
// old ones for older copies, so the recovery key has to be saved again.
func (s *server) hOffsiteNewKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Actor string `json:"actor"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	row, err := s.loadOffsite()
	if err != nil {
		writeError(w, err)
		return
	}
	if !row.hasKeys {
		writeError(w, errConflict("There is no key to replace yet.", "Turn on copies somewhere else first."))
		return
	}
	keys, rot, err := row.keys.Rotate(s.now())
	if err != nil {
		writeError(w, automationError(err))
		return
	}
	if _, err := s.db.Exec(`UPDATE offsite SET keys = ?, key_saved_at = NULL, key_saved_folder = NULL, updated_at = ? WHERE server_id = ?`, encodeKeys(keys), s.now().UnixMilli(), s.id); err != nil {
		writeError(w, err)
		return
	}
	row.keys, row.keySavedAt, row.keySavedFolder = keys, nil, nil
	// The copy being made is encrypted to the old key: it starts over with
	// the new one, and so does the rest of the queue.
	s.stopUpload()
	s.kickOffsite()
	s.audit(actor, "offsite.key_rotated", "server", "succeeded", "new key "+rot.Recipient)
	writeJSON(w, http.StatusOK, offsiteNewKey{Rotation: rot, Offsite: s.offsiteView(row)})
}

// offsiteNewKey is what making a new copy key changed, with the copies'
// settings after it.
type offsiteNewKey struct {
	Rotation offsite.Rotation `json:"rotation"`
	Offsite  offsiteView      `json:"offsite"`
}

// hOffsiteRetry tries waiting copies now.
func (s *server) hOffsiteRetry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Actor string `json:"actor"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if _, err := validActor(req.Actor); err != nil {
		writeError(w, err)
		return
	}
	if _, err := s.db.Exec(`UPDATE offsite_uploads SET next_attempt = 0 WHERE server_id = ?`, s.id); err != nil {
		writeError(w, err)
		return
	}
	s.kickOffsite()
	row, err := s.loadOffsite()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.offsiteView(row))
}

func (s *server) hOffsiteCopies(w http.ResponseWriter, r *http.Request) {
	list, err := s.offsiteCopies()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"copies": list})
}

// hOffsiteRestore downloads a copy, decrypts and checks it, and stages it
// like any archive: nothing changes until the restore is confirmed.
func (s *server) hOffsiteRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Actor string `json:"actor"`
		Name  string `json:"name"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	archive, ok := copyArchive(req.Name)
	if !ok {
		writeError(w, notACopy())
		return
	}
	dest, err := s.copyDest()
	if err != nil {
		writeError(w, err)
		return
	}
	op, err := s.beginOp("offsite-restore", actor, func(ctx context.Context, h *opHandle) error {
		h.allowCancel()
		h.set("name", offsite.CopyName(archive))
		dir := filepath.Join(s.cfg.StagingDir(), randomSecret(8))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		got, dl, err := s.fetchCopy(ctx, h, dest, archive, dir)
		if err != nil {
			return downloadStopped(s.stopping(), h, err)
		}
		h.phase("checking")
		f, err := os.Open(got.Path)
		if err != nil {
			return err
		}
		defer f.Close()
		p, err := s.stageArchive(ctxReader{ctx, f}, "copy "+got.Name, s.uploadLimit(), s)
		if err != nil {
			if ctx.Err() == nil {
				s.audit(actor, "restore.staged", archive, "refused", err.Error())
			}
			return downloadStopped(s.stopping(), h, err)
		}
		if dl.ArchiveSHA256 != "" && !strings.EqualFold(p.SHA256, dl.ArchiveSHA256) {
			os.RemoveAll(s.stageDir(p.ID))
			return &apiError{Status: http.StatusUnprocessableEntity, Code: api.CodeInvalid, Msg: "The backup inside the copy doesn't match its record.", Hint: "Nothing was changed. Restore another copy."}
		}
		if !h.commit() {
			os.RemoveAll(s.stageDir(p.ID))
			return context.Canceled
		}
		s.audit(actor, "restore.staged", archive, "validated", "sha256 "+p.SHA256)
		h.set("restoreId", p.ID)
		return nil
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

// hOffsiteRestoreCancel stops a restore from a copy before it hands over
// what it staged: the download so far is deleted and nothing else changes.
func (s *server) hOffsiteRestoreCancel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Actor       string `json:"actor"`
		OperationID string `json:"operationId"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	op, err := s.cancelOp("offsite-restore", req.OperationID)
	if err != nil {
		writeError(w, err)
		return
	}
	name, _ := op.Detail["name"].(string)
	s.audit(actor, "offsite.restore_cancelled", "server", "succeeded", name)
	writeJSON(w, http.StatusAccepted, op)
}

// downloadStopped is err, unless the agent is stopping: a restore from a copy
// then stays running for the next start, which records it as interrupted
// and deletes what staging holds. It staged nothing the restore could use
// and wrote no swap journal, so there is nothing to finish.
func downloadStopped(stopping bool, h *opHandle, err error) error {
	if !stopping {
		return err
	}
	h.continues = true
	return nil
}

// ctxReader stops a copy when its context ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// copyArchive is the backup's file name inside a copy's name, if it is one.
func copyArchive(name string) (string, bool) {
	archive, ok := strings.CutSuffix(strings.TrimSpace(name), ".age")
	return archive, ok && offsite.ValidName(archive)
}

func notACopy() error {
	return &apiError{Status: http.StatusBadRequest, Code: api.CodeInvalid, Field: "name", Msg: "That is not the name of a backup's copy."}
}

// copyDest opens where the server's copies are, for a request about one.
func (s *server) copyDest() (offsiteDest, error) {
	row, err := s.loadOffsite()
	if err != nil {
		return nil, err
	}
	if !row.configured() || !row.hasKeys {
		return nil, errConflict("Copies somewhere else aren't set up for this server.", "")
	}
	dest, err := s.openDest(row, row.keys)
	if err != nil {
		return nil, automationError(err)
	}
	return dest, nil
}

// fetchCopy downloads a copy into dir, decrypting it and checking it against
// its record on the way.
func (s *server) fetchCopy(ctx context.Context, h *opHandle, dest offsiteDest, archive, dir string) (offsite.Archive, offsite.Download, error) {
	dl := offsite.Download{Name: offsite.CopyName(archive), Dir: dir}
	if cp, ok := s.copyRecord(archive); ok {
		dl.Size, dl.SHA256, dl.Recipient, dl.ArchiveSHA256 = cp.Size, cp.SHA256, cp.Recipient, cp.ArchiveSHA256
	}
	if dl.Size == 0 {
		h.phase("listing")
		objs, err := dest.List(ctx)
		if err != nil {
			return offsite.Archive{}, dl, automationError(err)
		}
		for _, o := range objs {
			if o.Name == dl.Name {
				dl.Size = o.Size
			}
		}
		if dl.Size == 0 {
			return offsite.Archive{}, dl, errNotFound("Copy")
		}
	}
	h.phase("downloading")
	got, err := dest.Download(ctx, dl)
	if err != nil {
		return offsite.Archive{}, dl, automationError(err)
	}
	return got, dl, nil
}

// copyAtFault is true when a failed download says the copy itself is missing
// or damaged, rather than the way to it.
func copyAtFault(err error) bool {
	var ae *apiError
	if !errors.As(err, &ae) {
		return false
	}
	switch offsite.Kind(ae.Reason) {
	case offsite.KindVerifyFailed, offsite.KindNotFound, offsite.KindKeyMismatch:
		return true
	}
	return ae.Code == api.CodeNotFound
}

// hOffsiteCheck downloads a copy, decrypts it and checks it against its
// record as a restore would, then deletes the download. Nothing else changes.
func (s *server) hOffsiteCheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Actor string `json:"actor"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	archive, ok := copyArchive(r.PathValue("name"))
	if !ok {
		writeError(w, notACopy())
		return
	}
	backupID, ok := s.copyBackup(archive)
	if !ok {
		writeError(w, errNotFound("Copy"))
		return
	}
	dest, err := s.copyDest()
	if err != nil {
		writeError(w, err)
		return
	}
	op, err := s.beginOp("offsite-check", actor, func(ctx context.Context, h *opHandle) error {
		name := offsite.CopyName(archive)
		h.set("name", name)
		dir := filepath.Join(s.cfg.StagingDir(), randomSecret(8))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		got, _, err := s.fetchCopy(ctx, h, dest, archive, dir)
		switch {
		case err == nil:
			s.noteCopyCheck(archive, func(cp *copyRecord) {
				cp.CheckError = ""
				if got.Matched {
					cp.Checked, cp.VerifiedAt = offsite.CheckedDecrypted, s.now().UTC()
				}
			})
			s.audit(actor, "offsite.copy_checked", backupID, "succeeded", name)
		case copyAtFault(err):
			s.noteCopyCheck(archive, func(cp *copyRecord) { cp.CheckError = err.Error() })
			s.audit(actor, "offsite.copy_checked", backupID, "failed", err.Error())
		}
		return err
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

func (s *server) noteCopyCheck(archive string, note func(cp *copyRecord)) {
	cp, ok := s.copyRecord(archive)
	if !ok {
		return
	}
	note(&cp)
	raw, _ := json.Marshal(cp)
	if _, err := s.db.Exec(`UPDATE offsite_copies SET copy = ? WHERE server_id = ? AND file_name = ?`, string(raw), s.id, archive); err != nil {
		s.log.Warn("a copy's check could not be recorded", "server", s.id, "copy", archive, "err", err)
	}
}

// hOffsiteCopyDelete deletes a copy where it is kept, then its record.
func (s *server) hOffsiteCopyDelete(w http.ResponseWriter, r *http.Request) {
	actor, err := validActor(r.URL.Query().Get("actor"))
	if err != nil {
		writeError(w, err)
		return
	}
	archive, ok := copyArchive(r.PathValue("name"))
	if !ok {
		writeError(w, notACopy())
		return
	}
	backupID, ok := s.copyBackup(archive)
	if !ok {
		writeError(w, errNotFound("Copy"))
		return
	}
	if s.busy() {
		writeError(w, s.busyError())
		return
	}
	dest, err := s.copyDest()
	if err != nil {
		writeError(w, err)
		return
	}
	name := offsite.CopyName(archive)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := dest.Delete(ctx, name); err != nil {
		s.audit(actor, "offsite.copy_deleted", backupID, "failed", err.Error())
		writeError(w, automationError(err))
		return
	}
	if _, err := s.db.Exec(`DELETE FROM offsite_copies WHERE server_id = ? AND backup_id = ?`, s.id, backupID); err != nil {
		writeError(w, err)
		return
	}
	s.audit(actor, "offsite.copy_deleted", backupID, "succeeded", name)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": name})
}

// --- the uploader ---

func (s *server) offsiteKick() chan struct{} {
	s.auto.mu.Lock()
	defer s.auto.mu.Unlock()
	if s.auto.kick == nil {
		s.auto.kick = make(chan struct{}, 1)
	}
	return s.auto.kick
}

func (s *server) kickOffsite() {
	select {
	case s.offsiteKick() <- struct{}{}:
	default:
	}
}

// stopUpload cancels the upload the uploader claimed, if any.
func (s *server) stopUpload() {
	s.auto.mu.Lock()
	c := s.auto.claim
	s.auto.mu.Unlock()
	if c != nil {
		c.cancel()
	}
}

// queueOffsite queues a verified backup for its copy, when copies are on.
// Only the newest waiting backups stay queued, besides the one the uploader
// claimed; what the dropped ones left unfinished is discarded.
func (s *server) queueOffsite(backupID string) {
	var enabled int
	if s.db.QueryRow(`SELECT enabled FROM offsite WHERE server_id = ?`, s.id).Scan(&enabled) != nil || enabled != 1 {
		return
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO offsite_uploads(server_id, backup_id, created_at) VALUES(?, ?, ?)`, s.id, backupID, s.now().UnixMilli()); err != nil {
		s.log.Warn("could not queue a backup's copy", "server", s.id, "backup", backupID, "err", err)
		return
	}
	// The uploader picks and claims an upload under the same lock, so the
	// claim read here names any upload it picked before the delete.
	s.auto.mu.Lock()
	claimed := ""
	if c := s.auto.claim; c != nil {
		claimed = c.backupID
	}
	var dropped []*offsite.UploadState
	n := 0
	rows, err := s.db.Query(`DELETE FROM offsite_uploads WHERE server_id = ? AND backup_id != ? AND backup_id NOT IN
		(SELECT backup_id FROM offsite_uploads WHERE server_id = ? ORDER BY created_at DESC LIMIT ?) RETURNING state`, s.id, claimed, s.id, offsiteMaxQueue)
	if err == nil {
		dropped, n = scanStates(rows)
	}
	s.auto.mu.Unlock()
	if n > 0 {
		s.log.Warn("older backups waiting for their copy were dropped from the queue", "server", s.id, "count", n)
	}
	s.discardUploads(dropped)
	s.kickOffsite()
}

// discardUploads discards, in the background, what uploads dropped from the
// queue left at the destination and in the spool folder, as far as the
// destination answers: it may be why they waited.
func (s *server) discardUploads(states []*offsite.UploadState) {
	if len(states) == 0 {
		return
	}
	row, err := s.loadOffsite()
	if err != nil || !row.configured() || !row.hasKeys {
		return
	}
	dest, err := s.openDest(row, row.keys)
	if err != nil {
		s.log.Warn("unfinished copies could not be discarded", "server", s.id, "count", len(states), "err", err)
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.abandon(s.ctx, dest, states)
	}()
}

// uploadJob is an upload the uploader claimed. It runs under ctx, which
// stopUpload cancels from the moment of the claim.
type uploadJob struct {
	backupID string
	state    *offsite.UploadState
	attempts int
	ctx      context.Context
}

func (s *server) queuedStates() []*offsite.UploadState {
	rows, err := s.db.Query(`SELECT state FROM offsite_uploads WHERE server_id = ? AND state != ''`, s.id)
	if err != nil {
		return nil
	}
	states, _ := scanStates(rows)
	return states
}

// scanStates reads the saved states of queued uploads, skipping those with
// none, and counts the rows. It closes rows.
func scanStates(rows *sql.Rows) (states []*offsite.UploadState, n int) {
	defer rows.Close()
	for rows.Next() {
		n++
		var raw string
		var st offsite.UploadState
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &st) == nil {
			states = append(states, &st)
		}
	}
	return states, n
}

func (s *server) offsiteLoop(ctx context.Context) {
	kick := s.offsiteKick()
	t := time.NewTicker(offsiteCheck)
	defer t.Stop()
	var prev offsiteDest
	prevIdent := ""
	tidied := map[string]bool{}
	for {
		prev, prevIdent = s.offsiteRound(ctx, prev, prevIdent, tidied)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-kick:
		}
	}
}

// offsiteRound uploads what is due, encrypted to the key current as it
// starts: a new key ends the round. When the destination changed or copies
// were turned off, it first discards what the old destination holds of
// unfinished uploads.
func (s *server) offsiteRound(ctx context.Context, prev offsiteDest, prevIdent string, tidied map[string]bool) (offsiteDest, string) {
	row, err := s.loadOffsite()
	if err != nil || !row.configured() || !row.hasKeys {
		return prev, prevIdent
	}
	ident := offsiteIdentity(row.cfg.Config)
	if !row.enabled {
		if states := s.queuedStates(); len(states) > 0 {
			old := prev
			if old == nil || prevIdent != ident {
				old, _ = s.openDest(row, row.keys)
			}
			s.abandon(ctx, old, states)
		}
		_, _ = s.db.Exec(`DELETE FROM offsite_uploads WHERE server_id = ?`, s.id)
		return nil, ""
	}
	if prev != nil && prevIdent != ident {
		s.abandon(ctx, prev, s.queuedStates())
		_, _ = s.db.Exec(`UPDATE offsite_uploads SET state = '' WHERE server_id = ?`, s.id)
	}
	dest, err := s.openDest(row, row.keys)
	if err != nil {
		s.log.Warn("copies somewhere else can't start", "server", s.id, "err", err)
		return nil, ""
	}
	if !tidied[ident] {
		tctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		n, err := dest.AbortStale(tctx, s.now().Add(-offsiteStaleAfter), s.queuedStates())
		cancel()
		if err == nil {
			tidied[ident] = true
			if n > 0 {
				s.log.Info("cleaned up unfinished copies", "server", s.id, "count", n)
			}
		}
	}
	for ctx.Err() == nil {
		job, ok := s.claimUpload(ctx)
		if !ok || !s.uploadOne(ctx, dest, ident, row.keys.Current.Recipient, job) {
			break
		}
	}
	return dest, ident
}

// abandon discards unfinished uploads at a destination nothing copies to
// any more, as far as it answers.
func (s *server) abandon(ctx context.Context, dest offsiteDest, states []*offsite.UploadState) {
	if dest == nil {
		return
	}
	for _, st := range states {
		actx, cancel := context.WithTimeout(ctx, offsiteAbortWait)
		if err := dest.Abort(actx, st); err != nil {
			s.log.Warn("an unfinished copy could not be discarded", "server", s.id, "copy", st.Name, "err", err)
		}
		cancel()
	}
}

// claimUpload picks the newest upload that is due and claims it, holding
// the lock queueOffsite trims the queue under from the pick to the claim.
func (s *server) claimUpload(ctx context.Context) (uploadJob, bool) {
	s.auto.mu.Lock()
	defer s.auto.mu.Unlock()
	var j uploadJob
	var raw string
	err := s.db.QueryRow(`SELECT backup_id, state, attempts FROM offsite_uploads WHERE server_id = ? AND next_attempt <= ? ORDER BY created_at DESC LIMIT 1`,
		s.id, s.now().UnixMilli()).Scan(&j.backupID, &raw, &j.attempts)
	if err != nil {
		return j, false
	}
	if raw != "" {
		var st offsite.UploadState
		if json.Unmarshal([]byte(raw), &st) == nil {
			j.state = &st
		}
	}
	var cancel context.CancelFunc
	j.ctx, cancel = context.WithCancel(ctx)
	s.auto.claim = &uploadClaim{backupID: j.backupID, cancel: cancel}
	return j, true
}

// releaseUpload lets go of the claimed upload.
func (s *server) releaseUpload() {
	s.auto.mu.Lock()
	c := s.auto.claim
	s.auto.claim = nil
	s.auto.mu.Unlock()
	if c != nil {
		c.cancel()
	}
}

func (s *server) dropUpload(backupID string) {
	_, _ = s.db.Exec(`DELETE FROM offsite_uploads WHERE server_id = ? AND backup_id = ?`, s.id, backupID)
}

// uploadOne copies the backup of an upload the uploader claimed to dest,
// which encrypts to recipient, and lets go of the claim once it is done with
// the upload's row. It reports whether the next one can go.
//
// A new key saved before the claim is seen here; one saved after it stops
// the claim. Either way nothing more is encrypted to the old key.
func (s *server) uploadOne(ctx context.Context, dest offsiteDest, ident, recipient string, job uploadJob) bool {
	defer s.releaseUpload()
	uploadClaimed(job)
	if row, err := s.loadOffsite(); err != nil || row.keys.Current.Recipient != recipient {
		return false
	}
	b, err := s.getBackup(job.backupID)
	if err != nil || b.Verified == nil || !*b.Verified {
		s.dropUpload(job.backupID)
		if job.state != nil {
			s.abandon(ctx, dest, []*offsite.UploadState{job.state})
		}
		return true
	}
	f, err := os.Open(s.backupPath(b.FileName))
	if err != nil {
		s.dropUpload(job.backupID)
		if job.state != nil {
			s.abandon(ctx, dest, []*offsite.UploadState{job.state})
		}
		return true
	}
	defer f.Close()
	sent := storedBytes(job.state)
	s.auto.mu.Lock()
	s.auto.upload = &uploadProgress{backupID: b.ID, sent: sent, total: b.SizeBytes, startSent: sent, started: s.now()}
	s.auto.mu.Unlock()
	cp, err := dest.Upload(job.ctx, offsite.Upload{Name: b.FileName, File: f, Size: b.SizeBytes, SHA256: b.SHA256, Resume: job.state,
		Progress: func(p offsite.Progress) {
			s.auto.mu.Lock()
			if up := s.auto.upload; up != nil {
				up.sent, up.total = p.Sent, p.Total
			}
			s.auto.mu.Unlock()
			if p.State != nil {
				if raw, err := json.Marshal(p.State); err == nil {
					_, _ = s.db.Exec(`UPDATE offsite_uploads SET state = ? WHERE server_id = ? AND backup_id = ?`, string(raw), s.id, b.ID)
				}
			}
		}})
	s.auto.mu.Lock()
	s.auto.upload = nil
	s.auto.mu.Unlock()
	row, lerr := s.loadOffsite()
	if lerr != nil || !row.enabled || offsiteIdentity(row.cfg.Config) != ident {
		// The settings changed meanwhile; the next round sorts it out.
		return false
	}
	if err != nil {
		s.uploadFailed(job.ctx, b, job, err)
		return false
	}
	s.copyDone(ctx, dest, row, b, cp)
	return true
}

func (s *server) uploadFailed(ctx context.Context, b *api.Backup, job uploadJob, err error) {
	var oe *offsite.Error
	errors.As(err, &oe)
	// The state the upload's progress saved stays, as NULL leaves it, unless
	// the error carries a newer one.
	var state any
	if oe != nil && oe.Resume != nil {
		if raw, jerr := json.Marshal(oe.Resume); jerr == nil {
			state = string(raw)
		}
	}
	if ctx.Err() != nil {
		// Playkeeper is stopping, or stopUpload stopped the upload for new
		// settings or a new key: it isn't a failed try, and goes on, or
		// starts over, next time.
		_, _ = s.db.Exec(`UPDATE offsite_uploads SET state = COALESCE(?, state) WHERE server_id = ? AND backup_id = ?`, state, s.id, b.ID)
		return
	}
	msg, hint, kind, params := err.Error(), "", "", ""
	if oe != nil {
		msg, hint, kind = oe.Msg, oe.Hint, string(oe.Kind)
		if raw, jerr := json.Marshal(oe.Params()); jerr == nil {
			params = string(raw)
		}
		if oe.Kind == offsite.KindLocalChanged || oe.Kind == offsite.KindTooLarge {
			// Trying again can't help: this backup is never copied.
			s.dropUpload(b.ID)
			s.audit("playkeeper", "offsite.copy_failed", b.ID, "failed", msg)
			return
		}
	}
	attempts := job.attempts + 1
	wait := offsiteMaxBackoff
	if attempts <= 8 {
		wait = min(offsiteFirstBackoff<<(attempts-1), offsiteMaxBackoff)
	}
	_, _ = s.db.Exec(`UPDATE offsite_uploads SET state = COALESCE(?, state), attempts = ?, next_attempt = ?, last_error = ?, error_hint = ?, error_kind = ?, error_params = ?
		WHERE server_id = ? AND backup_id = ?`, state, attempts, s.now().Add(wait).UnixMilli(), msg, hint, kind, params, s.id, b.ID)
	if attempts == 1 {
		s.audit("playkeeper", "offsite.copy_failed", b.ID, "failed", msg)
	}
	s.log.Warn("a copy somewhere else failed", "server", s.id, "backup", b.ID, "kind", kind, "attempt", attempts)
}

// copyDone records a finished copy, then applies the rules at the
// destination and, when nothing else runs, on this machine.
func (s *server) copyDone(ctx context.Context, dest offsiteDest, row offsiteRow, b *api.Backup, cp offsite.Copy) {
	raw, _ := json.Marshal(cp)
	if _, err := s.db.Exec(`INSERT OR REPLACE INTO offsite_copies(server_id, backup_id, kind, backup_created_at, file_name, size_bytes, minecraft_version, level_name, copy, copied_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, s.id, b.ID, b.Kind, b.CreatedAt.UnixMilli(), b.FileName, b.SizeBytes, b.MinecraftVersion, b.LevelName, string(raw), s.now().UnixMilli()); err != nil {
		s.log.Warn("a finished copy could not be recorded", "server", s.id, "backup", b.ID, "err", err)
		return
	}
	_, _ = s.db.Exec(`UPDATE offsite SET copies_made = copies_made + 1 WHERE server_id = ?`, s.id)
	s.dropUpload(b.ID)
	s.audit("playkeeper", "offsite.copied", b.ID, "succeeded", cp.Name+" · "+offsitePlace(row.cfg.Config))
	s.pruneOffsite(ctx, dest)
	if !s.busy() {
		if release, ok := s.holdOpLock(); ok {
			s.applyRetention()
			release()
		}
	}
}

// pruneOffsite deletes the copies the rules no longer keep.
func (s *server) pruneOffsite(ctx context.Context, dest offsiteDest) {
	res, err := s.retentionPlan()
	if err != nil {
		return
	}
	for _, id := range res.OffSite.DeleteIDs() {
		var archive string
		if s.db.QueryRow(`SELECT file_name FROM offsite_copies WHERE server_id = ? AND backup_id = ?`, s.id, id).Scan(&archive) != nil {
			continue
		}
		name := offsite.CopyName(archive)
		dctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := dest.Delete(dctx, name)
		cancel()
		if err != nil {
			s.log.Warn("a copy the rules no longer keep could not be deleted", "server", s.id, "copy", name, "err", err)
			continue
		}
		_, _ = s.db.Exec(`DELETE FROM offsite_copies WHERE server_id = ? AND backup_id = ?`, s.id, id)
		s.audit(retentionActor, "offsite.copy_deleted", id, "succeeded", name)
	}
}

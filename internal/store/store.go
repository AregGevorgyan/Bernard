// Package store is Bernard's persistence layer: a single SQLite file holding
// ads, sessions and play events.
//
// It uses modernc.org/sqlite, a pure-Go driver, so the whole server compiles
// with CGO_ENABLED=0 into one static binary that can be scp'd to the kiosk.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

// Ad statuses.
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusRejected = "rejected"
)

// Ad kinds.
const (
	KindImage = "image"
	KindVideo = "video"
	KindHTML  = "html"
)

// Ad is one slide in the rotation.
//
// An ad appears on the TV when Status is approved, Enabled is true, and now
// falls inside [StartsAt, EndsAt]. Keeping "approved" and "playing" as two
// independent flags means an admin can pull a slide off the screen without
// discarding the review decision.
type Ad struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Kind       string `json:"kind"`
	MediaFile  string `json:"mediaFile,omitempty"` // basename under the media dir
	HTML       string `json:"html,omitempty"`
	MIME       string `json:"mime,omitempty"`
	Bytes      int64  `json:"bytes,omitempty"`
	DurationMs int    `json:"durationMs"`
	Fit        string `json:"fit"`        // contain | cover
	Background string `json:"background"` // CSS color behind a contained asset

	Status    string `json:"status"`
	Enabled   bool   `json:"enabled"`
	SortOrder int    `json:"sortOrder"`

	SubmitterEmail string `json:"submitterEmail"`
	SubmitterName  string `json:"submitterName"`

	ReviewNote string `json:"reviewNote,omitempty"`
	ReviewedBy string `json:"reviewedBy,omitempty"`

	StartsAt *time.Time `json:"startsAt,omitempty"`
	EndsAt   *time.Time `json:"endsAt,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Playing reports whether the ad should be in the playlist at time now.
func (a *Ad) Playing(now time.Time) bool {
	if a.Status != StatusApproved || !a.Enabled {
		return false
	}
	if a.StartsAt != nil && now.Before(*a.StartsAt) {
		return false
	}
	if a.EndsAt != nil && now.After(*a.EndsAt) {
		return false
	}
	return true
}

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	// WAL keeps the display's playlist reads from ever blocking on an admin
	// write; busy_timeout covers the brief writer lock.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// modernc's driver is safe for concurrent use but a single writer avoids
	// SQLITE_BUSY churn entirely; reads still fan out under WAL.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS ads (
  id              TEXT PRIMARY KEY,
  title           TEXT NOT NULL,
  kind            TEXT NOT NULL,
  media_file      TEXT NOT NULL DEFAULT '',
  html            TEXT NOT NULL DEFAULT '',
  mime            TEXT NOT NULL DEFAULT '',
  bytes           INTEGER NOT NULL DEFAULT 0,
  duration_ms     INTEGER NOT NULL,
  fit             TEXT NOT NULL DEFAULT 'contain',
  background      TEXT NOT NULL DEFAULT '#000000',
  status          TEXT NOT NULL,
  enabled         INTEGER NOT NULL DEFAULT 1,
  sort_order      INTEGER NOT NULL DEFAULT 0,
  submitter_email TEXT NOT NULL,
  submitter_name  TEXT NOT NULL,
  review_note     TEXT NOT NULL DEFAULT '',
  reviewed_by     TEXT NOT NULL DEFAULT '',
  starts_at       TEXT,
  ends_at         TEXT,
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ads_status_idx    ON ads(status, enabled, sort_order);
CREATE INDEX IF NOT EXISTS ads_submitter_idx ON ads(submitter_email, created_at DESC);

CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  email      TEXT NOT NULL,
  name       TEXT NOT NULL,
  is_admin   INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_expiry_idx ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS play_events (
  id       INTEGER PRIMARY KEY AUTOINCREMENT,
  ad_id    TEXT NOT NULL,
  played_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS play_events_ad_idx ON play_events(ad_id, played_at DESC);
`

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// ─── Ads ──────────────────────────────────────────────────────────────────────

const adColumns = `id, title, kind, media_file, html, mime, bytes, duration_ms, fit,
	background, status, enabled, sort_order, submitter_email, submitter_name,
	review_note, reviewed_by, starts_at, ends_at, created_at, updated_at`

func scanAd(sc interface{ Scan(...any) error }) (*Ad, error) {
	var a Ad
	var startsAt, endsAt sql.NullString
	var createdAt, updatedAt string
	if err := sc.Scan(&a.ID, &a.Title, &a.Kind, &a.MediaFile, &a.HTML, &a.MIME, &a.Bytes,
		&a.DurationMs, &a.Fit, &a.Background, &a.Status, &a.Enabled, &a.SortOrder,
		&a.SubmitterEmail, &a.SubmitterName, &a.ReviewNote, &a.ReviewedBy,
		&startsAt, &endsAt, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	var err error
	if a.StartsAt, err = parseNullTime(startsAt); err != nil {
		return nil, err
	}
	if a.EndsAt, err = parseNullTime(endsAt); err != nil {
		return nil, err
	}
	if a.CreatedAt, err = time.Parse(time.RFC3339, createdAt); err != nil {
		return nil, err
	}
	if a.UpdatedAt, err = time.Parse(time.RFC3339, updatedAt); err != nil {
		return nil, err
	}
	return &a, nil
}

func parseNullTime(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, ns.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func formatNullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// CreateAd inserts a new submission at the end of the rotation.
func (s *Store) CreateAd(ctx context.Context, a *Ad) error {
	now := time.Now().UTC()
	a.CreatedAt, a.UpdatedAt = now, now
	if a.SortOrder == 0 {
		var maxOrder sql.NullInt64
		if err := s.db.QueryRowContext(ctx, `SELECT MAX(sort_order) FROM ads`).Scan(&maxOrder); err != nil {
			return fmt.Errorf("max sort_order: %w", err)
		}
		a.SortOrder = int(maxOrder.Int64) + 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO ads (`+adColumns+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Title, a.Kind, a.MediaFile, a.HTML, a.MIME, a.Bytes, a.DurationMs, a.Fit,
		a.Background, a.Status, a.Enabled, a.SortOrder, a.SubmitterEmail, a.SubmitterName,
		a.ReviewNote, a.ReviewedBy, formatNullTime(a.StartsAt), formatNullTime(a.EndsAt),
		a.CreatedAt.Format(time.RFC3339), a.UpdatedAt.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("insert ad: %w", err)
	}
	return nil
}

func (s *Store) GetAd(ctx context.Context, id string) (*Ad, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+adColumns+` FROM ads WHERE id = ?`, id)
	a, err := scanAd(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

func (s *Store) queryAds(ctx context.Context, q string, args ...any) ([]*Ad, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Ad{}
	for rows.Next() {
		a, err := scanAd(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListAds returns every ad, newest submissions first within each status.
func (s *Store) ListAds(ctx context.Context) ([]*Ad, error) {
	return s.queryAds(ctx, `SELECT `+adColumns+` FROM ads ORDER BY sort_order ASC, created_at ASC`)
}

func (s *Store) ListByStatus(ctx context.Context, status string) ([]*Ad, error) {
	return s.queryAds(ctx, `SELECT `+adColumns+` FROM ads WHERE status = ? ORDER BY sort_order ASC, created_at ASC`, status)
}

func (s *Store) ListBySubmitter(ctx context.Context, email string) ([]*Ad, error) {
	return s.queryAds(ctx, `SELECT `+adColumns+` FROM ads WHERE submitter_email = ? ORDER BY created_at DESC`, email)
}

// Playlist returns the ads that should be on screen right now, in rotation order.
func (s *Store) Playlist(ctx context.Context, now time.Time) ([]*Ad, error) {
	all, err := s.queryAds(ctx, `SELECT `+adColumns+`
		FROM ads WHERE status = ? AND enabled = 1 ORDER BY sort_order ASC, created_at ASC`, StatusApproved)
	if err != nil {
		return nil, err
	}
	// The date-window filter runs in Go rather than SQL so that the comparison
	// uses the same time zone rules everywhere.
	out := make([]*Ad, 0, len(all))
	for _, a := range all {
		if a.Playing(now) {
			out = append(out, a)
		}
	}
	return out, nil
}

// AdUpdate carries the fields an admin may change. Nil means "leave alone",
// which lets one endpoint serve every partial edit the console makes.
type AdUpdate struct {
	Title      *string
	DurationMs *int
	Fit        *string
	Background *string
	Enabled    *bool
	SortOrder  *int
	Status     *string
	ReviewNote *string
	ReviewedBy *string
	StartsAt   **time.Time // pointer-to-pointer: set to a *nil to clear the field
	EndsAt     **time.Time
}

func (s *Store) UpdateAd(ctx context.Context, id string, u AdUpdate) (*Ad, error) {
	set := []string{}
	args := []any{}
	add := func(col string, v any) { set = append(set, col+" = ?"); args = append(args, v) }

	if u.Title != nil {
		add("title", *u.Title)
	}
	if u.DurationMs != nil {
		add("duration_ms", *u.DurationMs)
	}
	if u.Fit != nil {
		add("fit", *u.Fit)
	}
	if u.Background != nil {
		add("background", *u.Background)
	}
	if u.Enabled != nil {
		add("enabled", *u.Enabled)
	}
	if u.SortOrder != nil {
		add("sort_order", *u.SortOrder)
	}
	if u.Status != nil {
		add("status", *u.Status)
	}
	if u.ReviewNote != nil {
		add("review_note", *u.ReviewNote)
	}
	if u.ReviewedBy != nil {
		add("reviewed_by", *u.ReviewedBy)
	}
	if u.StartsAt != nil {
		add("starts_at", formatNullTime(*u.StartsAt))
	}
	if u.EndsAt != nil {
		add("ends_at", formatNullTime(*u.EndsAt))
	}
	if len(set) == 0 {
		return s.GetAd(ctx, id)
	}
	add("updated_at", time.Now().UTC().Format(time.RFC3339))

	q := `UPDATE ads SET `
	for i, c := range set {
		if i > 0 {
			q += ", "
		}
		q += c
	}
	q += ` WHERE id = ?`
	args = append(args, id)

	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("update ad: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.GetAd(ctx, id)
}

// DeleteAd removes the row and returns the media basename so the caller can
// unlink the file. Returning it rather than deleting here keeps the store free
// of filesystem concerns.
func (s *Store) DeleteAd(ctx context.Context, id string) (mediaFile string, err error) {
	a, err := s.GetAd(ctx, id)
	if err != nil {
		return "", err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM ads WHERE id = ?`, id); err != nil {
		return "", fmt.Errorf("delete ad: %w", err)
	}
	return a.MediaFile, nil
}

// Reorder writes a new rotation order in one transaction. IDs absent from the
// list keep their existing order but sort after everything listed.
func (s *Store) Reorder(ctx context.Context, ids []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `UPDATE ads SET sort_order = ?, updated_at = ? WHERE id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := time.Now().UTC().Format(time.RFC3339)
	for i, id := range ids {
		if _, err := stmt.ExecContext(ctx, i+1, now, id); err != nil {
			return fmt.Errorf("reorder %s: %w", id, err)
		}
	}
	return tx.Commit()
}

// ─── Sessions ─────────────────────────────────────────────────────────────────

type Session struct {
	Email     string
	Name      string
	IsAdmin   bool
	ExpiresAt time.Time
}

func (s *Store) CreateSession(ctx context.Context, tokenHash, email, name string, isAdmin bool, ttl time.Duration) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, email, name, is_admin, created_at, expires_at) VALUES (?,?,?,?,?,?)`,
		tokenHash, email, name, isAdmin, now.Format(time.RFC3339), now.Add(ttl).Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (s *Store) GetSession(ctx context.Context, tokenHash string) (*Session, error) {
	var sess Session
	var expires string
	err := s.db.QueryRowContext(ctx,
		`SELECT email, name, is_admin, expires_at FROM sessions WHERE token_hash = ?`, tokenHash).
		Scan(&sess.Email, &sess.Name, &sess.IsAdmin, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if sess.ExpiresAt, err = time.Parse(time.RFC3339, expires); err != nil {
		return nil, err
	}
	if time.Now().After(sess.ExpiresAt) {
		_ = s.DeleteSession(ctx, tokenHash)
		return nil, ErrNotFound
	}
	return &sess, nil
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

func (s *Store) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ─── Play events ──────────────────────────────────────────────────────────────

// RecordPlay logs that an ad reached the screen. Submitters see the count on
// their submission, which is the question they always ask next.
func (s *Store) RecordPlay(ctx context.Context, adID string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO play_events (ad_id, played_at) VALUES (?, ?)`,
		adID, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) PlayCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ad_id, COUNT(*) FROM play_events GROUP BY ad_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// PruneOldPlayEvents keeps the events table from growing without bound; a
// kiosk running for a year would otherwise accumulate millions of rows.
func (s *Store) PruneOldPlayEvents(ctx context.Context, keep time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-keep).Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, `DELETE FROM play_events WHERE played_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountsByStatus powers the admin console's header.
func (s *Store) CountsByStatus(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM ads GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

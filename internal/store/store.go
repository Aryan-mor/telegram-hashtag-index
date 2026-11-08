// Package store keeps the links and the allowlist in SQLite.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Aryan-mor/telegram-hashtag-index/internal/link"
	"github.com/Aryan-mor/telegram-hashtag-index/internal/tag"
)

// Link status values.
const (
	StatusPending  = "pending"
	StatusVerified = "verified"
	StatusDead     = "dead"
)

// Kinds a post can have. The verifier fills it in.
var Kinds = []string{"video", "audio", "voice", "document", "photo", "animation", "sticker", "text", "other"}

// ValidKind reports whether k is one of Kinds.
func ValidKind(k string) bool {
	for _, v := range Kinds {
		if v == k {
			return true
		}
	}
	return false
}

// Member roles are bits.
const (
	RoleWrite = 1
	RoleRead  = 2
	// RoleBanned silences a sender whatever the other bits say.
	RoleBanned = 4
)

// Link is one stored post.
type Link struct {
	ID        int64
	Tag       string
	Chan      string
	Msg       int64
	Status    string
	Kind      string
	Size      int64
	Duration  int64
	Mime      string
	AddedBy   int64
	AddedAt   int64
	CheckedAt int64
	Tries     int
}

// Ref is the post the link points at.
func (l Link) Ref() link.Ref { return link.Ref{Chan: l.Chan, Msg: l.Msg} }

// AddResult tells what Add did.
type AddResult int

const (
	Added AddResult = iota
	Duplicate
	Full
	// Busy means too many links wait for a check.
	Busy
)

// Check is what the verifier learned about a post.
type Check struct {
	Kind     string
	Size     int64
	Duration int64
	Mime     string
}

// Query selects links for a search.
type Query struct {
	Pattern tag.Pattern
	// Kinds limits the result to these kinds. Empty means all.
	Kinds []string
	// Before returns only links with a smaller id, for paging. Zero means from the newest.
	Before int64
	Limit  int
	// Pending also returns links that are not verified yet.
	Pending bool
}

// Store is the database handle.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS links(
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  tag        TEXT    NOT NULL,
  chan       TEXT    NOT NULL,
  msg        INTEGER NOT NULL,
  status     TEXT    NOT NULL DEFAULT 'pending',
  kind       TEXT    NOT NULL DEFAULT '',
  size       INTEGER NOT NULL DEFAULT 0,
  duration   INTEGER NOT NULL DEFAULT 0,
  mime       TEXT    NOT NULL DEFAULT '',
  added_by   INTEGER NOT NULL,
  added_at   INTEGER NOT NULL,
  checked_at INTEGER NOT NULL DEFAULT 0,
  tries      INTEGER NOT NULL DEFAULT 0,
  UNIQUE(tag, chan, msg)
);
CREATE INDEX IF NOT EXISTS links_tag ON links(tag, id);
CREATE INDEX IF NOT EXISTS links_pending ON links(status, id);
CREATE TABLE IF NOT EXISTS members(
  id   INTEGER PRIMARY KEY,
  role INTEGER NOT NULL
);
`

// Open opens or creates the database file.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection keeps writes in order and avoids lock errors.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Add stores a pending link. A tag holds at most maxPerTag links, and at most maxPending
// links may wait for a check at once (zero means no limit).
func (s *Store) Add(ctx context.Context, t string, ref link.Ref, by int64, maxPerTag, maxPending int, now time.Time) (AddResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM links WHERE tag=? AND chan=? AND msg=?`, t, ref.Chan, ref.Msg).Scan(&one)
	switch {
	case err == nil:
		return Duplicate, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM links WHERE tag=?`, t).Scan(&n); err != nil {
		return 0, err
	}
	if n >= maxPerTag {
		return Full, nil
	}
	if maxPending > 0 {
		var p int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM links WHERE status='pending'`).Scan(&p); err != nil {
			return 0, err
		}
		if p >= maxPending {
			return Busy, nil
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO links(tag, chan, msg, added_by, added_at) VALUES(?,?,?,?,?)`,
		t, ref.Chan, ref.Msg, by, now.Unix()); err != nil {
		return 0, err
	}
	return Added, tx.Commit()
}

const cols = `id, tag, chan, msg, status, kind, size, duration, mime, added_by, added_at, checked_at, tries`

func scan(rows *sql.Rows) ([]Link, error) {
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.ID, &l.Tag, &l.Chan, &l.Msg, &l.Status, &l.Kind, &l.Size, &l.Duration,
			&l.Mime, &l.AddedBy, &l.AddedAt, &l.CheckedAt, &l.Tries); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Find returns links for a search, newest first. The second result reports that more
// links follow after the last one returned.
func (s *Store) Find(ctx context.Context, q Query) ([]Link, bool, error) {
	if q.Limit <= 0 {
		q.Limit = 30
	}
	var where []string
	var args []any
	if q.Pattern.Wild {
		where = append(where, "tag >= ? AND tag < ? AND tag GLOB ?")
		args = append(args, q.Pattern.Prefix, q.Pattern.UpperBound(), q.Pattern.Text)
	} else {
		where = append(where, "tag = ?")
		args = append(args, q.Pattern.Text)
	}
	if q.Pending {
		where = append(where, "status IN ('verified','pending')")
	} else {
		where = append(where, "status = 'verified'")
	}
	if len(q.Kinds) > 0 {
		marks := strings.TrimSuffix(strings.Repeat("?,", len(q.Kinds)), ",")
		where = append(where, "kind IN ("+marks+")")
		for _, k := range q.Kinds {
			args = append(args, k)
		}
	}
	if q.Before > 0 {
		where = append(where, "id < ?")
		args = append(args, q.Before)
	}
	args = append(args, q.Limit+1)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+cols+` FROM links WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, false, err
	}
	out, err := scan(rows)
	if err != nil {
		return nil, false, err
	}
	more := len(out) > q.Limit
	if more {
		out = out[:q.Limit]
	}
	return out, more, nil
}

// Pending returns the oldest links that still wait for a check.
func (s *Store) Pending(ctx context.Context, limit int) ([]Link, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+cols+` FROM links WHERE status='pending' ORDER BY checked_at, id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return scan(rows)
}

// MarkVerified records that a post exists and what it holds.
func (s *Store) MarkVerified(ctx context.Context, id int64, c Check, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE links SET status='verified', kind=?, size=?, duration=?, mime=?, checked_at=?, tries=tries+1 WHERE id=?`,
		c.Kind, c.Size, c.Duration, c.Mime, now.Unix(), id)
	return err
}

// MarkDead records that a post is gone or unreadable for good.
func (s *Store) MarkDead(ctx context.Context, id int64, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE links SET status='dead', checked_at=?, tries=tries+1 WHERE id=?`, now.Unix(), id)
	return err
}

// Retry notes a failed check that may pass later. After maxTries the link is dead.
func (s *Store) Retry(ctx context.Context, id int64, maxTries int, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE links SET tries=tries+1, checked_at=?,
		   status=CASE WHEN tries+1 >= ? THEN 'dead' ELSE status END WHERE id=?`,
		now.Unix(), maxTries, id)
	return err
}

// Delete removes one link. It reports whether a row was removed.
func (s *Store) Delete(ctx context.Context, t string, ref link.Ref) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM links WHERE tag=? AND chan=? AND msg=?`, t, ref.Chan, ref.Msg)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DeleteBy removes every link a member added. It returns how many were removed.
func (s *Store) DeleteBy(ctx context.Context, id int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM links WHERE added_by=?`, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Role returns the role bits of a member. Zero means not a member.
func (s *Store) Role(ctx context.Context, id int64) (int, error) {
	var r int
	err := s.db.QueryRowContext(ctx, `SELECT role FROM members WHERE id=?`, id).Scan(&r)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return r, err
}

// SetRole adds or changes a member. Role zero removes the member.
func (s *Store) SetRole(ctx context.Context, id int64, role int) error {
	if role == 0 {
		_, err := s.db.ExecContext(ctx, `DELETE FROM members WHERE id=?`, id)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO members(id, role) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET role=excluded.role`, id, role)
	return err
}

// Counts returns how many links hold each status.
func (s *Store) Counts(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM links GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// Package store keeps brews and their curves in SQLite, the exporter's source of truth.
//
// Every upsert bumps the brew's version. The sync loop sends brews whose version is
// greater than sent_version and then records the version it sent, so a brew replaced
// after it was sent (or while it was being sent) is sent again.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/MateuxLucax/timemore-dot-prometheus-exporter/internal/brew"

	_ "modernc.org/sqlite" // pure-Go driver, registers "sqlite"
)

// migrations are applied in order; the index + 1 is the schema version stored in
// PRAGMA user_version. Append new steps, never edit applied ones.
var migrations = []string{
	`CREATE TABLE brews (
		id                   TEXT PRIMARY KEY,
		mode                 TEXT NOT NULL,
		started_at_ms        INTEGER NOT NULL,
		ended_at_ms          INTEGER NOT NULL,
		dose_g               REAL NOT NULL,
		yield_g              REAL NOT NULL,
		time_to_first_drop_s REAL,
		flow_avg_gps         REAL NOT NULL,
		flow_peak_gps        REAL NOT NULL,
		interrupted          INTEGER NOT NULL,
		received_at_ms       INTEGER NOT NULL,
		version              INTEGER NOT NULL,
		sent_version         INTEGER NOT NULL DEFAULT 0,
		sent_at_ms           INTEGER,
		sync_error           TEXT
	);
	CREATE TABLE samples (
		brew_id  TEXT NOT NULL REFERENCES brews(id),
		seq      INTEGER NOT NULL,
		t_ms     INTEGER NOT NULL,
		weight_g REAL NOT NULL,
		flow_gps REAL NOT NULL,
		PRIMARY KEY (brew_id, seq)
	);
	CREATE INDEX brews_pending ON brews(sent_version, version);`,
}

// Store is a SQLite database. It is safe for concurrent use.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and applies pending migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// SchemaVersion returns the applied migration count.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var v int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this binary (%d)", v, len(migrations))
	}
	for i := v; i < len(migrations); i++ {
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", len(migrations))); err != nil {
		return err
	}
	return tx.Commit()
}

// Upsert stores b and its samples in one transaction, replacing any brew with the same id
// and all of its samples. It reports whether the brew existed. It returns only after commit.
func (s *Store) Upsert(ctx context.Context, b *brew.Brew) (existed bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var version int64
	switch err := tx.QueryRowContext(ctx, "SELECT version FROM brews WHERE id = ?", b.ID).Scan(&version); {
	case err == nil:
		existed = true
	case errors.Is(err, sql.ErrNoRows):
	default:
		return false, err
	}
	// Samples go first (explicitly, not via cascade) so the brew row can be replaced.
	if _, err := tx.ExecContext(ctx, "DELETE FROM samples WHERE brew_id = ?", b.ID); err != nil {
		return false, err
	}
	var ttfd any
	if b.TimeToFirstDropS != nil {
		ttfd = *b.TimeToFirstDropS
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO brews (id, mode, started_at_ms, ended_at_ms, dose_g, yield_g,
		time_to_first_drop_s, flow_avg_gps, flow_peak_gps, interrupted, received_at_ms, version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET mode = excluded.mode, started_at_ms = excluded.started_at_ms,
			ended_at_ms = excluded.ended_at_ms, dose_g = excluded.dose_g, yield_g = excluded.yield_g,
			time_to_first_drop_s = excluded.time_to_first_drop_s, flow_avg_gps = excluded.flow_avg_gps,
			flow_peak_gps = excluded.flow_peak_gps, interrupted = excluded.interrupted,
			received_at_ms = excluded.received_at_ms, version = excluded.version, sync_error = NULL`,
		b.ID, b.Mode, b.StartedAtMs, b.EndedAtMs, b.DoseG, b.YieldG, ttfd, b.FlowAvgGps, b.FlowPeakGps,
		b.Interrupted, time.Now().UnixMilli(), version+1)
	if err != nil {
		return false, err
	}
	if len(b.Samples) > 0 {
		stmt, err := tx.PrepareContext(ctx, "INSERT INTO samples (brew_id, seq, t_ms, weight_g, flow_gps) VALUES (?, ?, ?, ?, ?)")
		if err != nil {
			return false, err
		}
		defer stmt.Close()
		for i, sm := range b.Samples {
			if _, err := stmt.ExecContext(ctx, b.ID, i, sm.TMs, sm.WeightG, sm.FlowGps); err != nil {
				return false, err
			}
		}
	}
	return existed, tx.Commit()
}

// Summary is a brew without its samples, as listed by GET /api/v1/brews.
type Summary struct {
	ID               string   `json:"id"`
	Mode             string   `json:"mode"`
	StartedAtMs      int64    `json:"started_at_ms"`
	EndedAtMs        int64    `json:"ended_at_ms"`
	DoseG            float64  `json:"dose_g"`
	YieldG           float64  `json:"yield_g"`
	TimeToFirstDropS *float64 `json:"time_to_first_drop_s"`
	FlowAvgGps       float64  `json:"flow_avg_gps"`
	FlowPeakGps      float64  `json:"flow_peak_gps"`
	Interrupted      bool     `json:"interrupted"`
	SampleCount      int      `json:"sample_count"`
}

// List returns every brew without samples, newest first.
func (s *Store) List(ctx context.Context) ([]Summary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT b.id, b.mode, b.started_at_ms, b.ended_at_ms, b.dose_g, b.yield_g,
		b.time_to_first_drop_s, b.flow_avg_gps, b.flow_peak_gps, b.interrupted,
		(SELECT COUNT(*) FROM samples s WHERE s.brew_id = b.id)
		FROM brews b ORDER BY b.started_at_ms DESC, b.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		var x Summary
		var ttfd sql.NullFloat64
		if err := rows.Scan(&x.ID, &x.Mode, &x.StartedAtMs, &x.EndedAtMs, &x.DoseG, &x.YieldG, &ttfd,
			&x.FlowAvgGps, &x.FlowPeakGps, &x.Interrupted, &x.SampleCount); err != nil {
			return nil, err
		}
		if ttfd.Valid {
			x.TimeToFirstDropS = &ttfd.Float64
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Pending is a brew the sync loop has not sent in its current version.
type Pending struct {
	Brew    brew.Brew
	Version int64
}

// Pending returns up to limit brews whose current version has not been sent, oldest end time first.
func (s *Store) Pending(ctx context.Context, limit int) ([]Pending, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id FROM brews WHERE version > sent_version ORDER BY ended_at_ms, id LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Pending, len(ids))
	for i, id := range ids {
		if err := s.load(ctx, id, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// load reads brew id, its samples and its version in one read transaction, so they always match.
func (s *Store) load(ctx context.Context, id string, p *Pending) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var b brew.Brew
	var ttfd sql.NullFloat64
	if err := tx.QueryRowContext(ctx, `SELECT mode, started_at_ms, ended_at_ms, dose_g, yield_g,
		time_to_first_drop_s, flow_avg_gps, flow_peak_gps, interrupted, version FROM brews WHERE id = ?`, id).Scan(
		&b.Mode, &b.StartedAtMs, &b.EndedAtMs, &b.DoseG, &b.YieldG, &ttfd, &b.FlowAvgGps, &b.FlowPeakGps,
		&b.Interrupted, &p.Version); err != nil {
		return err
	}
	b.ID = id
	if ttfd.Valid {
		b.TimeToFirstDropS = &ttfd.Float64
	}
	rows, err := tx.QueryContext(ctx, "SELECT t_ms, weight_g, flow_gps FROM samples WHERE brew_id = ? ORDER BY seq", b.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	b.Samples = []brew.Sample{}
	for rows.Next() {
		var sm brew.Sample
		if err := rows.Scan(&sm.TMs, &sm.WeightG, &sm.FlowGps); err != nil {
			return err
		}
		b.Samples = append(b.Samples, sm)
	}
	p.Brew = b
	return rows.Err()
}

// MarkSent records that version of brew id was handled by the sync loop. syncErr is empty when
// Prometheus accepted it, or the reason it was rejected for good. If the brew was replaced in
// the meantime its newer version stays pending.
func (s *Store) MarkSent(ctx context.Context, id string, version int64, syncErr string) error {
	var e any
	if syncErr != "" {
		e = syncErr
	}
	_, err := s.db.ExecContext(ctx, `UPDATE brews SET sent_version = ?, sent_at_ms = ?, sync_error = ?
		WHERE id = ? AND version = ?`, version, time.Now().UnixMilli(), e, id, version)
	return err
}

// ResetSent marks every brew as not sent, so the next sync sends everything again.
func (s *Store) ResetSent(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, "UPDATE brews SET sent_version = 0, sent_at_ms = NULL, sync_error = NULL")
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Counts returns the number of brews, of samples, and of brews pending sync.
func (s *Store) Counts(ctx context.Context) (brews, samples, pending int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM brews), (SELECT COUNT(*) FROM samples),
		(SELECT COUNT(*) FROM brews WHERE version > sent_version)`).Scan(&brews, &samples, &pending)
	return
}

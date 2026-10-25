// Package store keeps check results and incidents in SQLite (pure Go driver, no cgo).
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
)

type Store struct{ db *sql.DB }

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS checks (
	id          INTEGER PRIMARY KEY,
	target      TEXT    NOT NULL,
	location    TEXT    NOT NULL,
	at          INTEGER NOT NULL, -- unix ms
	status      TEXT    NOT NULL,
	latency_ms  INTEGER NOT NULL,
	http_status INTEGER NOT NULL,
	code        TEXT    NOT NULL,
	reason      TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS checks_target_at ON checks(target, at);
CREATE INDEX IF NOT EXISTS checks_at ON checks(at);
-- one row per target per decision: the combined view of all locations (internal/quorum).
-- Uptime and the day bars come from here; raw per-location checks stay in checks.
CREATE TABLE IF NOT EXISTS verdicts (
	id      INTEGER PRIMARY KEY,
	target  TEXT    NOT NULL,
	at      INTEGER NOT NULL,
	status  TEXT    NOT NULL,
	reason  TEXT    NOT NULL,
	voters  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS verdicts_target_at ON verdicts(target, at);
CREATE INDEX IF NOT EXISTS verdicts_at ON verdicts(at);
-- newest check per target and location, newest verdict per target: kept on write so the
-- dashboard reads a row instead of a MAX over the history
CREATE TABLE IF NOT EXISTS latest_checks (
	target      TEXT    NOT NULL,
	location    TEXT    NOT NULL,
	at          INTEGER NOT NULL,
	status      TEXT    NOT NULL,
	latency_ms  INTEGER NOT NULL,
	http_status INTEGER NOT NULL,
	code        TEXT    NOT NULL,
	reason      TEXT    NOT NULL,
	PRIMARY KEY (target, location)
);
CREATE TABLE IF NOT EXISTS latest_verdicts (
	target TEXT    PRIMARY KEY,
	at     INTEGER NOT NULL,
	status TEXT    NOT NULL,
	reason TEXT    NOT NULL,
	voters INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS incidents (
	id          INTEGER PRIMARY KEY,
	target      TEXT    NOT NULL,
	started     INTEGER NOT NULL,
	ended       INTEGER NOT NULL DEFAULT 0,
	severity    INTEGER NOT NULL,
	status      TEXT    NOT NULL,
	reason      TEXT    NOT NULL,
	last_reason TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS incidents_target ON incidents(target, started);
-- schema-change detection: every field path each API has answered with, and the changes
CREATE TABLE IF NOT EXISTS schema_paths (
	target     TEXT    NOT NULL,
	path       TEXT    NOT NULL,
	first_seen INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL,
	PRIMARY KEY (target, path)
);
CREATE TABLE IF NOT EXISTS schema_events (
	id     INTEGER PRIMARY KEY,
	target TEXT    NOT NULL,
	at     INTEGER NOT NULL,
	kind   TEXT    NOT NULL, -- added | removed
	paths  TEXT    NOT NULL  -- newline-separated
);
CREATE INDEX IF NOT EXISTS schema_events_at ON schema_events(at);
-- what the operator learned or did during an incident (shown in the incident view and
-- the postmortem draft)
CREATE TABLE IF NOT EXISTS incident_notes (
	id       INTEGER PRIMARY KEY,
	incident INTEGER NOT NULL,
	at       INTEGER NOT NULL,
	author   TEXT    NOT NULL,
	text     TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS incident_notes_incident ON incident_notes(incident, at);
-- silences: an operator mutes an API's pages for a while (incidents and SLOs unchanged,
-- unlike maintenance)
CREATE TABLE IF NOT EXISTS silences (
	id      INTEGER PRIMARY KEY,
	target  TEXT    NOT NULL,
	created INTEGER NOT NULL,
	until   INTEGER NOT NULL,
	author  TEXT    NOT NULL,
	reason  TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS silences_until ON silences(until);
-- timeout follow-ups (probe.FollowUps): did a timed-out request come back when retried?
CREATE TABLE IF NOT EXISTS followups (
	target     TEXT    NOT NULL,
	at         INTEGER NOT NULL,
	answered   INTEGER NOT NULL,
	after_ms   INTEGER NOT NULL,
	http       INTEGER NOT NULL,
	wait_ms    INTEGER NOT NULL,
	timeout_ms INTEGER NOT NULL,
	stalled    TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS followups_target_at ON followups(target, at);
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; SQLite serialises anyway
	if _, err := db.Exec(schema + rollupSchema + officialSchema + typesSchema); err != nil {
		db.Close()
		return nil, err
	}
	// request phases came later (milliseconds; 0 when the phase did not happen)
	for _, col := range []string{"dns_ms", "connect_ms", "tls_ms", "wait_ms", "reused"} {
		db.Exec(`ALTER TABLE checks ADD COLUMN ` + col + ` INTEGER NOT NULL DEFAULT 0`)
	}
	// incident_notes.kind came later: "note", or "ack" (someone took the incident)
	db.Exec(`ALTER TABLE incident_notes ADD COLUMN kind TEXT NOT NULL DEFAULT 'note'`)
	// incidents.flaps came later: add it to older databases (an error means it is there)
	db.Exec(`ALTER TABLE incidents ADD COLUMN flaps INTEGER NOT NULL DEFAULT 0`)
	// field paths learned from the data.go.kr gateway's error envelope (before Shape skipped
	// it): they are not the API's fields and would come back as "removed"
	db.Exec(`DELETE FROM schema_paths WHERE path LIKE 'OpenAPI_ServiceResponse%'`)
	db.Exec(`DELETE FROM schema_events WHERE paths LIKE 'OpenAPI_ServiceResponse%'`)
	// "added" events from a target's first day of learning, recorded before the warm-up rule
	// (they were time-of-day fields, not changes): the rule applied to the history
	db.Exec(`DELETE FROM schema_events WHERE kind='added' AND at < 86400000 +
		(SELECT MIN(first_seen) FROM schema_paths p WHERE p.target = schema_events.target)`)
	// rows written before the zero-time fix: open incidents stored as year-1 milliseconds
	if _, err := db.Exec(`UPDATE incidents SET ended=0 WHERE ended<0`); err != nil {
		db.Close()
		return nil, err
	}
	st := &Store{db: db}
	if err := st.backfillLatest(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	if err := st.backfillRollups(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return st, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Snapshot writes a consistent, compacted copy of the database to path (WAL content
// included) after checking it is not corrupt, so a backup is never a half-written file.
func (s *Store) Snapshot(ctx context.Context, path string) error {
	var ok string
	if err := s.db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&ok); err != nil {
		return err
	}
	if ok != "ok" {
		return fmt.Errorf("database check: %s", ok)
	}
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}

// backfillLatest fills the latest_* tables once, for a database from before them.
func (s *Store) backfillLatest(ctx context.Context) error {
	var n int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM latest_checks`).Scan(&n)
	if n > 0 {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO latest_checks
		SELECT c.target, c.location, c.at, c.status, c.latency_ms, c.http_status, c.code, c.reason
		FROM checks c JOIN (SELECT target, location, MAX(at) AS at FROM checks GROUP BY target, location) m
		ON c.target=m.target AND c.location=m.location AND c.at=m.at`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO latest_verdicts
		SELECT v.target, v.at, v.status, v.reason, v.voters
		FROM verdicts v JOIN (SELECT target, MAX(at) AS at FROM verdicts GROUP BY target) m
		ON v.target=m.target AND v.at=m.at`)
	return err
}

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// ms stores a time as unix milliseconds; the zero time (an incident still open) is 0.
// (time.Time{}.UnixMilli() is -62135596800000, which no "ended=0" query matched: open
// incidents vanished from the list, the feed and the restore after a restart. Found by
// the game day, deploy/gameday.)
func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}

func (s *Store) SaveCheck(ctx context.Context, r probe.Result) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO checks(target, location, at, status, latency_ms, http_status, code, reason, dns_ms, connect_ms, tls_ms, wait_ms, reused)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.TargetID, r.Location, ms(r.At), string(r.Status), r.LatencyMS, r.HTTPStatus, r.Code, r.Reason,
		r.Phases.DNSMs, r.Phases.ConnectMs, r.Phases.TLSMs, r.Phases.WaitMs, r.Phases.Reused); err != nil {
		return err
	}
	// (a remote probe's result can arrive after a newer one: keep the newest)
	if _, err := tx.ExecContext(ctx, `INSERT INTO latest_checks(target, location, at, status, latency_ms, http_status, code, reason)
		VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(target, location) DO UPDATE SET at=excluded.at, status=excluded.status,
		latency_ms=excluded.latency_ms, http_status=excluded.http_status, code=excluded.code, reason=excluded.reason
		WHERE excluded.at >= latest_checks.at`,
		r.TargetID, r.Location, ms(r.At), string(r.Status), r.LatencyMS, r.HTTPStatus, r.Code, r.Reason); err != nil {
		return err
	}
	c, h, d, o := counts(r.Status)
	if err := rollCheck(ctx, tx, r.TargetID, r.Location, r.At, c, h, d, o, r.LatencyMS); err != nil {
		return err
	}
	return tx.Commit()
}

// counts: a status as (counted, healthy, degraded, outage) increments.
func counts(st probe.Status) (c, h, d, o int) {
	if st.Counts() {
		c = 1
	}
	if st.Healthy() {
		h = 1
	}
	switch st.Severity() {
	case 1:
		d = 1
	case 2:
		o = 1
	}
	return
}

// Verdict row (see the verdicts table).
type Verdict struct {
	TargetID string       `json:"target"`
	At       time.Time    `json:"at"`
	Status   probe.Status `json:"status"`
	Reason   string       `json:"reason,omitempty"`
	Voters   int          `json:"voters"`
}

func (s *Store) SaveVerdict(ctx context.Context, v Verdict) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO verdicts(target, at, status, reason, voters) VALUES(?,?,?,?,?)`,
		v.TargetID, ms(v.At), string(v.Status), v.Reason, v.Voters); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO latest_verdicts(target, at, status, reason, voters) VALUES(?,?,?,?,?)
		ON CONFLICT(target) DO UPDATE SET at=excluded.at, status=excluded.status, reason=excluded.reason, voters=excluded.voters
		WHERE excluded.at >= latest_verdicts.at`, v.TargetID, ms(v.At), string(v.Status), v.Reason, v.Voters); err != nil {
		return err
	}
	c, h, d, o := counts(v.Status)
	if err := rollVerdict(ctx, tx, v.TargetID, v.At, c, h, d, o); err != nil {
		return err
	}
	return tx.Commit()
}

// LatestVerdicts: the newest verdict per target within the last day (a target with none
// is "unknown"). Read from latest_verdicts, kept on write: a MAX/GROUP BY over the verdicts
// table cost ~30 ms per dashboard request on a month of data (and ~140 ms unbounded).
func (s *Store) LatestVerdicts(ctx context.Context) (map[string]Verdict, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT target, at, status, reason, voters FROM latest_verdicts WHERE at>=?`,
		ms(time.Now().Add(-24*time.Hour)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Verdict{}
	for rows.Next() {
		var v Verdict
		var at int64
		var status string
		if err := rows.Scan(&v.TargetID, &at, &status, &v.Reason, &v.Voters); err != nil {
			return nil, err
		}
		v.At, v.Status = fromMS(at), probe.Status(status)
		out[v.TargetID] = v
	}
	return out, rows.Err()
}

// SaveIncident inserts a new incident (ID 0) or updates an existing one.
func (s *Store) SaveIncident(ctx context.Context, in *incident.Incident) error {
	if in.ID == 0 {
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO incidents(target, started, ended, severity, status, reason, last_reason, flaps) VALUES(?,?,?,?,?,?,?,?)`,
			in.TargetID, ms(in.Started), ms(in.Ended), in.Severity, string(in.Status), in.Reason, in.LastReason, in.Flaps)
		if err != nil {
			return err
		}
		in.ID, err = res.LastInsertId()
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE incidents SET ended=?, severity=?, status=?, last_reason=?, flaps=? WHERE id=?`,
		ms(in.Ended), in.Severity, string(in.Status), in.LastReason, in.Flaps, in.ID)
	return err
}

func scanIncidents(rows *sql.Rows) ([]*incident.Incident, error) {
	defer rows.Close()
	var out []*incident.Incident
	for rows.Next() {
		var in incident.Incident
		var started, ended int64
		var status string
		if err := rows.Scan(&in.ID, &in.TargetID, &started, &ended, &in.Severity, &status, &in.Reason, &in.LastReason, &in.Flaps); err != nil {
			return nil, err
		}
		in.Started, in.Ended, in.Status = fromMS(started), fromMS(ended), probe.Status(status)
		out = append(out, &in)
	}
	return out, rows.Err()
}

const incidentCols = `id, target, started, ended, severity, status, reason, last_reason, flaps`

func (s *Store) OpenIncidents(ctx context.Context) ([]*incident.Incident, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+incidentCols+` FROM incidents WHERE ended=0 ORDER BY started`)
	if err != nil {
		return nil, err
	}
	return scanIncidents(rows)
}

// Incidents since a time, newest first; target "" for all.
func (s *Store) Incidents(ctx context.Context, target string, since time.Time, limit int) ([]*incident.Incident, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+incidentCols+` FROM incidents WHERE (?='' OR target=?) AND (ended=0 OR ended>=?) ORDER BY started DESC LIMIT ?`,
		target, target, ms(since), limit)
	if err != nil {
		return nil, err
	}
	return scanIncidents(rows)
}

// LatestByLocation: the newest raw check of each target from each location since a time.
// From latest_checks, kept on write (the MAX/GROUP BY over checks was ~90 ms a request).
func (s *Store) LatestByLocation(ctx context.Context, since time.Time) (map[string][]probe.Result, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT target, location, at, status, latency_ms, http_status, code, reason FROM latest_checks
		WHERE at>=? ORDER BY target, location`, ms(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]probe.Result{}
	for rows.Next() {
		var r probe.Result
		var at int64
		var status string
		if err := rows.Scan(&r.TargetID, &r.Location, &at, &status, &r.LatencyMS, &r.HTTPStatus, &r.Code, &r.Reason); err != nil {
			return nil, err
		}
		r.At, r.Status = fromMS(at), probe.Status(status)
		out[r.TargetID] = append(out[r.TargetID], r)
	}
	return out, rows.Err()
}

// Day is one target's checks in one KST day.
type Day struct {
	Date     string `json:"date"` // YYYY-MM-DD, KST
	Counted  int    `json:"counted"`
	Healthy  int    `json:"healthy"`
	Degraded int    `json:"degraded"`
	Outage   int    `json:"outage"`
}

// Uptime of a day, or -1 when nothing counted.
func (d Day) Uptime() float64 {
	if d.Counted == 0 {
		return -1
	}
	return float64(d.Healthy) / float64(d.Counted)
}

// Days returns per-target, per-KST-day aggregates of verdicts since a time.
func (s *Store) Days(ctx context.Context, since time.Time) (map[string][]Day, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT target, date(at/1000, 'unixepoch', '+9 hours') AS d,
		       SUM(status NOT IN ('client','maint')),
		       SUM(status IN ('ok','slow','stale')),
		       SUM(status IN ('slow','stale')),
		       SUM(status IN ('error','down'))
		FROM verdicts WHERE at>=? GROUP BY target, d ORDER BY target, d`, ms(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Day{}
	for rows.Next() {
		var target string
		var d Day
		if err := rows.Scan(&target, &d.Date, &d.Counted, &d.Healthy, &d.Degraded, &d.Outage); err != nil {
			return nil, err
		}
		out[target] = append(out[target], d)
	}
	return out, rows.Err()
}

// Latencies of healthy checks per target since a time (for percentiles), oldest first;
// location "" for all.
func (s *Store) Latencies(ctx context.Context, since time.Time, location ...string) (map[string][]int64, error) {
	loc := ""
	if len(location) > 0 {
		loc = location[0]
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT target, latency_ms FROM checks WHERE at>=? AND status IN ('ok','slow','stale') AND (?='' OR location=?) ORDER BY target, at`,
		ms(since), loc, loc)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]int64{}
	for rows.Next() {
		var t string
		var l int64
		if err := rows.Scan(&t, &l); err != nil {
			return nil, err
		}
		out[t] = append(out[t], l)
	}
	return out, rows.Err()
}

// LatencySample is one answered check's response time and whether it was judged slow.
type LatencySample struct {
	Ms   int64
	Slow bool
}

// LatencySamples of one location's answered checks per target since a time, oldest
// first: what the adaptive slow limit learned before a restart.
func (s *Store) LatencySamples(ctx context.Context, since time.Time, location string) (map[string][]LatencySample, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT target, latency_ms, status FROM checks WHERE at>=? AND status IN ('ok','slow','stale') AND location=? ORDER BY target, at`,
		ms(since), location)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]LatencySample{}
	for rows.Next() {
		var t, st string
		var l int64
		if err := rows.Scan(&t, &l, &st); err != nil {
			return nil, err
		}
		out[t] = append(out[t], LatencySample{l, st == "slow"})
	}
	return out, rows.Err()
}

// Counts are a target's verdicts in a period.
type Counts struct {
	Counted  int
	Healthy  int
	Degraded int
	Outage   int
}

// VerdictCounts per target in [from, to).
func (s *Store) VerdictCounts(ctx context.Context, from, to time.Time) (map[string]Counts, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT target, SUM(status NOT IN ('client','maint')), SUM(status IN ('ok','slow','stale')),
		       SUM(status IN ('slow','stale')), SUM(status IN ('error','down'))
		FROM verdicts WHERE at>=? AND at<? GROUP BY target`, ms(from), ms(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Counts{}
	for rows.Next() {
		var t string
		var c Counts
		if err := rows.Scan(&t, &c.Counted, &c.Healthy, &c.Degraded, &c.Outage); err != nil {
			return nil, err
		}
		out[t] = c
	}
	return out, rows.Err()
}

// ClientCounts exposes results excluded from provider uptime without changing its math
// or the hourly schema. The existing verdicts_at index bounds this read to the range.
// ponytail: long ranges still scan raw verdicts; add hourly client counters if cold
// overview latency exceeds its budget (the existing response cache covers repeat reads).
func (s *Store) ClientCounts(ctx context.Context, from, to time.Time) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT target, COUNT(*) FROM verdicts
		WHERE at>=? AND at<? AND status='client' GROUP BY target`, ms(from), ms(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var target string
		var count int
		if err := rows.Scan(&target, &count); err != nil {
			return nil, err
		}
		out[target] = count
	}
	return out, rows.Err()
}

// IncidentsOverlapping returns incidents that were open at some point in [from, to).
func (s *Store) IncidentsOverlapping(ctx context.Context, from, to time.Time) ([]*incident.Incident, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+incidentCols+` FROM incidents WHERE started<? AND (ended=0 OR ended>=?) ORDER BY started`,
		ms(to), ms(from))
	if err != nil {
		return nil, err
	}
	return scanIncidents(rows)
}

// LatenciesBetween: healthy response times per target in [from, to).
func (s *Store) LatenciesBetween(ctx context.Context, from, to time.Time) (map[string][]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT target, latency_ms FROM checks WHERE at>=? AND at<? AND status IN ('ok','slow','stale')`, ms(from), ms(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]int64{}
	for rows.Next() {
		var t string
		var l int64
		if err := rows.Scan(&t, &l); err != nil {
			return nil, err
		}
		out[t] = append(out[t], l)
	}
	return out, rows.Err()
}

// Bucket is one target's verdicts in one time bucket.
type Bucket struct {
	Start    int64 // unix ms
	Counted  int
	Healthy  int
	Degraded int
	Outage   int
}

// VerdictBuckets groups verdicts in [from, to) into buckets of step, per target.
func (s *Store) VerdictBuckets(ctx context.Context, from, to time.Time, step time.Duration) (map[string][]Bucket, error) {
	return s.verdictBuckets(ctx, "", from, to, step)
}

// TargetBuckets is VerdictBuckets for one target, read through its (target, at) index:
// a one-target chart used to bucket every target and keep one (89 times per export).
func (s *Store) TargetBuckets(ctx context.Context, target string, from, to time.Time, step time.Duration) ([]Bucket, error) {
	out, err := s.verdictBuckets(ctx, target, from, to, step)
	return out[target], err
}

func (s *Store) verdictBuckets(ctx context.Context, target string, from, to time.Time, step time.Duration) (map[string][]Bucket, error) {
	st := step.Milliseconds()
	where, args := `at>=? AND at<?`, []any{st, st, ms(from), ms(to)}
	if target != "" {
		where, args = `target=? AND `+where, []any{st, st, target, ms(from), ms(to)}
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT target, (at/?)*? AS b, SUM(status NOT IN ('client','maint')), SUM(status IN ('ok','slow','stale')),
		       SUM(status IN ('slow','stale')), SUM(status IN ('error','down'))
		FROM verdicts WHERE `+where+` GROUP BY target, b ORDER BY target, b`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Bucket{}
	for rows.Next() {
		var t string
		var b Bucket
		if err := rows.Scan(&t, &b.Start, &b.Counted, &b.Healthy, &b.Degraded, &b.Outage); err != nil {
			return nil, err
		}
		out[t] = append(out[t], b)
	}
	return out, rows.Err()
}

// Sample is one raw check, for latency charts.
type Sample struct {
	Target    string
	Location  string
	At        int64 // unix ms
	LatencyMS int64
	Status    probe.Status
}

// Samples returns raw checks in [from, to), oldest first; target "" for all.
func (s *Store) Samples(ctx context.Context, target string, from, to time.Time) ([]Sample, error) {
	// Two shapes, not (?='' OR target=?): the OR kept SQLite off the (target, at) index,
	// so one target's chart scanned every target's rows in the range.
	q, args := `SELECT target, location, at, latency_ms, status FROM checks WHERE at>=? AND at<? ORDER BY at`, []any{ms(from), ms(to)}
	if target != "" {
		q, args = `SELECT target, location, at, latency_ms, status FROM checks WHERE target=? AND at>=? AND at<? ORDER BY at`, []any{target, ms(from), ms(to)}
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		var x Sample
		var status string
		if err := rows.Scan(&x.Target, &x.Location, &x.At, &x.LatencyMS, &status); err != nil {
			return nil, err
		}
		x.Status = probe.Status(status)
		out = append(out, x)
	}
	return out, rows.Err()
}

// Incident by id; nil when there is none.
func (s *Store) Incident(ctx context.Context, id int64) (*incident.Incident, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+incidentCols+` FROM incidents WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	list, err := scanIncidents(rows)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return list[0], nil
}

// ChecksBetween: a target's raw checks in [from, to], oldest first.
func (s *Store) ChecksBetween(ctx context.Context, target string, from, to time.Time, limit int) ([]probe.Result, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT target, location, at, status, latency_ms, http_status, code, reason, dns_ms, connect_ms, tls_ms, wait_ms, reused FROM checks
		WHERE target=? AND at>=? AND at<=? ORDER BY at LIMIT ?`, target, ms(from), ms(to), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []probe.Result
	for rows.Next() {
		var r probe.Result
		var at int64
		var status string
		ph := &r.Phases
		if err := rows.Scan(&r.TargetID, &r.Location, &at, &status, &r.LatencyMS, &r.HTTPStatus, &r.Code, &r.Reason,
			&ph.DNSMs, &ph.ConnectMs, &ph.TLSMs, &ph.WaitMs, &ph.Reused); err != nil {
			return nil, err
		}
		r.At, r.Status = fromMS(at), probe.Status(status)
		out = append(out, r)
	}
	return out, rows.Err()
}

// VerdictsBetween: a target's verdicts in [from, to], oldest first.
func (s *Store) VerdictsBetween(ctx context.Context, target string, from, to time.Time, limit int) ([]Verdict, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT target, at, status, reason, voters FROM verdicts
		WHERE target=? AND at>=? AND at<=? ORDER BY at LIMIT ?`, target, ms(from), ms(to), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Verdict{}
	for rows.Next() {
		var v Verdict
		var at int64
		var status string
		if err := rows.Scan(&v.TargetID, &at, &status, &v.Reason, &v.Voters); err != nil {
			return nil, err
		}
		v.At, v.Status = fromMS(at), probe.Status(status)
		out = append(out, v)
	}
	return out, rows.Err()
}

// RecentChecks: a target's newest raw checks, newest first.
func (s *Store) RecentChecks(ctx context.Context, target string, limit int) ([]probe.Result, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT target, location, at, status, latency_ms, http_status, code, reason, dns_ms, connect_ms, tls_ms, wait_ms, reused FROM checks
		WHERE target=? ORDER BY at DESC LIMIT ?`, target, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []probe.Result
	for rows.Next() {
		var r probe.Result
		var at int64
		var status string
		ph := &r.Phases
		if err := rows.Scan(&r.TargetID, &r.Location, &at, &status, &r.LatencyMS, &r.HTTPStatus, &r.Code, &r.Reason,
			&ph.DNSMs, &ph.ConnectMs, &ph.TLSMs, &ph.WaitMs, &ph.Reused); err != nil {
			return nil, err
		}
		r.At, r.Status = fromMS(at), probe.Status(status)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ObservePaths records the field paths of one healthy answer. It returns the paths never
// seen before for that target, and whether the target already had a learned shape: one
// learned at least warmup ago (the first day of answers is the baseline, not change).
func (s *Store) ObservePaths(ctx context.Context, target string, paths []string, at time.Time, warmup time.Duration) (added []string, hadShape bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var n int
	// a shape counts once it has been learned for a full day: many APIs answer with
	// different fields by time of day (a forecast's day-4 fields only in the 06:00 issue,
	// bus arrivals only while buses run), and those are not changes
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_paths WHERE target=? AND first_seen<=?`, target, ms(at.Add(-warmup))).Scan(&n); err != nil {
		return nil, false, err
	}
	for _, p := range paths {
		res, err := tx.ExecContext(ctx, `INSERT INTO schema_paths(target, path, first_seen, last_seen) VALUES(?,?,?,?)
			ON CONFLICT(target, path) DO UPDATE SET last_seen=excluded.last_seen`, target, p, ms(at), ms(at))
		if err != nil {
			return nil, false, err
		}
		// a new row was inserted when first_seen equals now
		var first int64
		if err := tx.QueryRowContext(ctx, `SELECT first_seen FROM schema_paths WHERE target=? AND path=?`, target, p).Scan(&first); err != nil {
			return nil, false, err
		}
		if first == ms(at) && res != nil {
			added = append(added, p)
		}
	}
	return added, n > 0, tx.Commit()
}

// StalePaths removes and returns, per target, the paths not seen for `age` although the
// target answered with a shape since `active` (an API that is down or empty loses nothing).
func (s *Store) StalePaths(ctx context.Context, now time.Time, age, active time.Duration) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.target, p.path FROM schema_paths p
		WHERE p.last_seen < ? AND EXISTS (SELECT 1 FROM schema_paths q WHERE q.target=p.target AND q.last_seen >= ?)
		ORDER BY p.target, p.path`, ms(now.Add(-age)), ms(now.Add(-active)))
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for rows.Next() {
		var t, p string
		if err := rows.Scan(&t, &p); err != nil {
			rows.Close()
			return nil, err
		}
		out[t] = append(out[t], p)
	}
	rows.Close()
	for t, ps := range out {
		for _, p := range ps {
			if _, err := s.db.ExecContext(ctx, `DELETE FROM schema_paths WHERE target=? AND path=?`, t, p); err != nil {
				return nil, err
			}
		}
	}
	return out, rows.Err()
}

// SchemaEvent is a detected change in an API's response fields.
type SchemaEvent struct {
	ID     int64     `json:"id"`
	Target string    `json:"target"`
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"` // added | removed | type_changed
	Paths  []string  `json:"paths"`
}

// SchemaLabel names an event kind: added, removed, type_changed.
func SchemaLabel(kind string) string {
	switch kind {
	case "removed":
		return "필드 사라짐"
	case "type_changed":
		return "값 형식 바뀜"
	}
	return "필드 추가"
}

func (s *Store) SaveSchemaEvent(ctx context.Context, e *SchemaEvent) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO schema_events(target, at, kind, paths) VALUES(?,?,?,?)`,
		e.Target, ms(e.At), e.Kind, strings.Join(e.Paths, "\n"))
	if err != nil {
		return err
	}
	e.ID, err = res.LastInsertId()
	return err
}

// SchemaEvents since a time, newest first; target "" for all.
func (s *Store) SchemaEvents(ctx context.Context, target string, since time.Time, limit int) ([]SchemaEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, target, at, kind, paths FROM schema_events
		WHERE (?='' OR target=?) AND at>=? ORDER BY at DESC LIMIT ?`, target, target, ms(since), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SchemaEvent{}
	for rows.Next() {
		var e SchemaEvent
		var at int64
		var paths string
		if err := rows.Scan(&e.ID, &e.Target, &at, &e.Kind, &paths); err != nil {
			return nil, err
		}
		e.At, e.Paths = fromMS(at), strings.Split(paths, "\n")
		out = append(out, e)
	}
	return out, rows.Err()
}

// Prune deletes checks and verdicts older than the retention; incidents are kept.
func (s *Store) Prune(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	// raw rows go after the retention; the hourly rollups (a few KB per API per day) are
	// kept 4x longer, so months-long trends stay
	if _, err := s.db.ExecContext(ctx, `DELETE FROM hourly WHERE hour<?`, ms(time.Now().Add(-4*time.Since(before)))); err != nil {
		return 0, err
	}
	for _, q := range []string{`DELETE FROM checks WHERE at<?`, `DELETE FROM verdicts WHERE at<?`, `DELETE FROM followups WHERE at<?`} {
		res, err := s.db.ExecContext(ctx, q, ms(before))
		if err != nil {
			return n, err
		}
		k, _ := res.RowsAffected()
		n += k
	}
	return n, nil
}

// SaveFollowUp records one timeout follow-up.
func (s *Store) SaveFollowUp(ctx context.Context, target string, f probe.FollowUp) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO followups(target, at, answered, after_ms, http, wait_ms, timeout_ms, stalled) VALUES(?,?,?,?,?,?,?,?)`,
		target, ms(f.At), f.Answered, f.AfterMs, f.HTTP, f.WaitMs, f.Timeout, f.Stalled)
	return err
}

// FollowUps returns a target's follow-ups since a time, newest first.
func (s *Store) FollowUps(ctx context.Context, target string, since time.Time) ([]probe.FollowUp, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT at, answered, after_ms, http, wait_ms, timeout_ms, stalled FROM followups
		WHERE target=? AND at>=? ORDER BY at DESC LIMIT 500`, target, ms(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []probe.FollowUp
	for rows.Next() {
		var f probe.FollowUp
		var at int64
		if err := rows.Scan(&at, &f.Answered, &f.AfterMs, &f.HTTP, &f.WaitMs, &f.Timeout, &f.Stalled); err != nil {
			return nil, err
		}
		f.At = fromMS(at)
		out = append(out, f)
	}
	return out, rows.Err()
}

// FollowUpsBetween returns all targets' follow-ups in [from, to).
func (s *Store) FollowUpsBetween(ctx context.Context, from, to time.Time) (map[string][]probe.FollowUp, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT target, at, answered, after_ms, http, wait_ms, timeout_ms, stalled FROM followups
		WHERE at>=? AND at<? ORDER BY target, at`, ms(from), ms(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]probe.FollowUp{}
	for rows.Next() {
		var t string
		var f probe.FollowUp
		var at int64
		if err := rows.Scan(&t, &at, &f.Answered, &f.AfterMs, &f.HTTP, &f.WaitMs, &f.Timeout, &f.Stalled); err != nil {
			return nil, err
		}
		f.At = fromMS(at)
		out[t] = append(out[t], f)
	}
	return out, rows.Err()
}

// Note is one operator note on an incident.
type Note struct {
	ID       int64     `json:"id"`
	Incident int64     `json:"incident"`
	At       time.Time `json:"at"`
	Author   string    `json:"author"`
	Text     string    `json:"text"`
	Kind     string    `json:"kind"` // note | ack
}

// AddNote stores a note and sets its ID.
func (s *Store) AddNote(ctx context.Context, n *Note) error {
	if n.Kind == "" {
		n.Kind = "note"
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO incident_notes(incident, at, author, text, kind) VALUES(?,?,?,?,?)`,
		n.Incident, ms(n.At), n.Author, n.Text, n.Kind)
	if err != nil {
		return err
	}
	n.ID, err = res.LastInsertId()
	return err
}

// Notes returns an incident's notes, oldest first.
func (s *Store) Notes(ctx context.Context, incident int64) ([]Note, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, incident, at, author, text, kind FROM incident_notes WHERE incident=? ORDER BY at, id`, incident)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Note{}
	for rows.Next() {
		var n Note
		var at int64
		if err := rows.Scan(&n.ID, &n.Incident, &at, &n.Author, &n.Text, &n.Kind); err != nil {
			return nil, err
		}
		n.At = fromMS(at)
		out = append(out, n)
	}
	return out, rows.Err()
}

// NoteSummary is what an incident list needs: how many notes, and who took it last.
type NoteSummary struct {
	Notes int
	AckBy string
	AckAt time.Time
}

// NoteSummaries returns, per incident with notes, the note count and the latest ack.
func (s *Store) NoteSummaries(ctx context.Context) (map[int64]NoteSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT incident, at, author, kind FROM incident_notes ORDER BY at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]NoteSummary{}
	for rows.Next() {
		var id, at int64
		var author, kind string
		if err := rows.Scan(&id, &at, &author, &kind); err != nil {
			return nil, err
		}
		x := out[id]
		if kind == "ack" {
			x.AckBy, x.AckAt = author, fromMS(at)
		} else {
			x.Notes++
		}
		out[id] = x
	}
	return out, rows.Err()
}

// Silence mutes one API's pages until a time.
type Silence struct {
	ID      int64     `json:"id"`
	Target  string    `json:"target"`
	Created time.Time `json:"created"`
	Until   time.Time `json:"until"`
	Author  string    `json:"author"`
	Reason  string    `json:"reason"`
}

func (s *Store) AddSilence(ctx context.Context, x *Silence) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO silences(target, created, until, author, reason) VALUES(?,?,?,?,?)`,
		x.Target, ms(x.Created), ms(x.Until), x.Author, x.Reason)
	if err != nil {
		return err
	}
	x.ID, err = res.LastInsertId()
	return err
}

// EndSilence ends a silence now (it stays in the history).
func (s *Store) EndSilence(ctx context.Context, id int64, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE silences SET until=? WHERE id=? AND until>?`, ms(now), id, ms(now))
	return err
}

// ActiveSilences returns the silences in force at a time, latest end first per target.
func (s *Store) ActiveSilences(ctx context.Context, now time.Time) ([]Silence, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, target, created, until, author, reason FROM silences
		WHERE created<=? AND until>? ORDER BY until DESC`, ms(now), ms(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Silence{}
	for rows.Next() {
		var x Silence
		var c, u int64
		if err := rows.Scan(&x.ID, &x.Target, &c, &u, &x.Author, &x.Reason); err != nil {
			return nil, err
		}
		x.Created, x.Until = fromMS(c), fromMS(u)
		out = append(out, x)
	}
	return out, rows.Err()
}

// Silenced reports whether a target's pages are muted at a time.
func (s *Store) Silenced(ctx context.Context, target string, now time.Time) bool {
	var n int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM silences WHERE target=? AND created<=? AND until>?`, target, ms(now), ms(now)).Scan(&n)
	return n > 0
}

// Package store keeps check results and incidents in SQLite (pure Go driver, no cgo).
package store

import (
	"context"
	"database/sql"
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
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; SQLite serialises anyway
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}

func (s *Store) SaveCheck(ctx context.Context, r probe.Result) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO checks(target, location, at, status, latency_ms, http_status, code, reason) VALUES(?,?,?,?,?,?,?,?)`,
		r.TargetID, r.Location, ms(r.At), string(r.Status), r.LatencyMS, r.HTTPStatus, r.Code, r.Reason)
	return err
}

// Verdict row (see the verdicts table).
type Verdict struct {
	TargetID string
	At       time.Time
	Status   probe.Status
	Reason   string
	Voters   int
}

func (s *Store) SaveVerdict(ctx context.Context, v Verdict) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO verdicts(target, at, status, reason, voters) VALUES(?,?,?,?,?)`,
		v.TargetID, ms(v.At), string(v.Status), v.Reason, v.Voters)
	return err
}

// LatestVerdicts: the newest verdict per target.
func (s *Store) LatestVerdicts(ctx context.Context) (map[string]Verdict, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.target, v.at, v.status, v.reason, v.voters
		FROM verdicts v JOIN (SELECT target, MAX(at) AS at FROM verdicts GROUP BY target) m
		ON v.target=m.target AND v.at=m.at`)
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
			`INSERT INTO incidents(target, started, ended, severity, status, reason, last_reason) VALUES(?,?,?,?,?,?,?)`,
			in.TargetID, ms(in.Started), ms(in.Ended), in.Severity, string(in.Status), in.Reason, in.LastReason)
		if err != nil {
			return err
		}
		in.ID, err = res.LastInsertId()
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE incidents SET ended=?, severity=?, status=?, last_reason=? WHERE id=?`,
		ms(in.Ended), in.Severity, string(in.Status), in.LastReason, in.ID)
	return err
}

func scanIncidents(rows *sql.Rows) ([]*incident.Incident, error) {
	defer rows.Close()
	var out []*incident.Incident
	for rows.Next() {
		var in incident.Incident
		var started, ended int64
		var status string
		if err := rows.Scan(&in.ID, &in.TargetID, &started, &ended, &in.Severity, &status, &in.Reason, &in.LastReason); err != nil {
			return nil, err
		}
		in.Started, in.Ended, in.Status = fromMS(started), fromMS(ended), probe.Status(status)
		out = append(out, &in)
	}
	return out, rows.Err()
}

const incidentCols = `id, target, started, ended, severity, status, reason, last_reason`

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

// LatestByLocation: the newest raw check of each target from each location.
func (s *Store) LatestByLocation(ctx context.Context, since time.Time) (map[string][]probe.Result, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.target, c.location, c.at, c.status, c.latency_ms, c.http_status, c.code, c.reason
		FROM checks c JOIN (SELECT target, location, MAX(at) AS at FROM checks WHERE at>=? GROUP BY target, location) m
		ON c.target=m.target AND c.location=m.location AND c.at=m.at
		ORDER BY c.target, c.location`, ms(since))
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
		       SUM(status!='client'),
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
		SELECT target, SUM(status!='client'), SUM(status IN ('ok','slow','stale')),
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

// Prune deletes checks and verdicts older than the retention; incidents are kept.
func (s *Store) Prune(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	for _, q := range []string{`DELETE FROM checks WHERE at<?`, `DELETE FROM verdicts WHERE at<?`} {
		res, err := s.db.ExecContext(ctx, q, ms(before))
		if err != nil {
			return n, err
		}
		k, _ := res.RowsAffected()
		n += k
	}
	return n, nil
}

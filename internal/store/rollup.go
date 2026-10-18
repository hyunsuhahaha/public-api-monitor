package store

import (
	"context"
	"database/sql"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Hourly rollups. Long ranges (7 and 30 days) were answered from raw checks: 780k rows
// scanned and sorted per request, 2 s for the 30-day overview (bench_test.go). Each write
// now also updates one row per target/location/hour: verdict counts (location "*") and a
// latency histogram of healthy answers, so a month is ~32k small rows. Percentiles from
// the histogram are estimates (linear within a bucket), as Prometheus histograms are.

// HistBounds are the upper bounds (ms) of the latency buckets; one more bucket is +Inf.
var HistBounds = []int64{10, 25, 50, 75, 100, 150, 200, 300, 400, 500, 750, 1000, 1500, 2000, 3000, 5000, 8000, 13000}

// Hist is a latency histogram: Counts[i] answers <= HistBounds[i], the last is the rest.
type Hist []int64

func NewHist() Hist { return make(Hist, len(HistBounds)+1) }

func (h Hist) add(ms int64) {
	i := sort.Search(len(HistBounds), func(i int) bool { return HistBounds[i] >= ms })
	h[i]++
}

// Merge adds o into h.
func (h Hist) Merge(o Hist) {
	for i := range h {
		if i < len(o) {
			h[i] += o[i]
		}
	}
}

// Count is the number of answers.
func (h Hist) Count() int64 {
	var n int64
	for _, c := range h {
		n += c
	}
	return n
}

// Quantile estimates the q-quantile (0..1) in ms, interpolating linearly inside the
// bucket; 0 for an empty histogram.
func (h Hist) Quantile(q float64) int64 {
	n := h.Count()
	if n == 0 {
		return 0
	}
	rank := q * float64(n)
	var seen float64
	for i, c := range h {
		if c == 0 {
			continue
		}
		if seen+float64(c) >= rank {
			lo := int64(0)
			if i > 0 {
				lo = HistBounds[i-1]
			}
			hi := lo * 2
			if i < len(HistBounds) {
				hi = HistBounds[i]
			}
			frac := (rank - seen) / float64(c)
			return lo + int64(frac*float64(hi-lo)+0.5)
		}
		seen += float64(c)
	}
	return HistBounds[len(HistBounds)-1]
}

func (h Hist) encode() string {
	parts := make([]string, len(h))
	for i, c := range h {
		parts[i] = strconv.FormatInt(c, 10)
	}
	return strings.Join(parts, ",")
}

func decodeHist(s string) Hist {
	h := NewHist()
	for i, p := range strings.Split(s, ",") {
		if i < len(h) {
			h[i], _ = strconv.ParseInt(p, 10, 64)
		}
	}
	return h
}

const rollupSchema = `
CREATE TABLE IF NOT EXISTS hourly (
	target   TEXT    NOT NULL,
	location TEXT    NOT NULL, -- "*" for verdict counts
	hour     INTEGER NOT NULL, -- unix ms at the start of the hour
	counted  INTEGER NOT NULL DEFAULT 0,
	healthy  INTEGER NOT NULL DEFAULT 0,
	degraded INTEGER NOT NULL DEFAULT 0,
	outage   INTEGER NOT NULL DEFAULT 0,
	hist     TEXT    NOT NULL DEFAULT '',
	PRIMARY KEY (target, location, hour)
);
CREATE INDEX IF NOT EXISTS hourly_hour ON hourly(hour);
`

func hourOf(t time.Time) int64 { return t.Truncate(time.Hour).UnixMilli() }

// rollCheck counts one raw check in its location's hour (status counts as for verdicts,
// client excluded) and adds a healthy answer's latency to the histogram.
func rollCheck(ctx context.Context, x execer, target, location string, at time.Time, counted, healthy, degraded, outage int, latencyMS int64) error {
	var cur string
	err := x.QueryRowContext(ctx, `SELECT hist FROM hourly WHERE target=? AND location=? AND hour=?`, target, location, hourOf(at)).Scan(&cur)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	h := NewHist()
	if cur != "" {
		h = decodeHist(cur)
	}
	if healthy == 1 {
		h.add(latencyMS)
	}
	_, err = x.ExecContext(ctx, `INSERT INTO hourly(target, location, hour, counted, healthy, degraded, outage, hist) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(target, location, hour) DO UPDATE SET counted=counted+excluded.counted, healthy=healthy+excluded.healthy,
		degraded=degraded+excluded.degraded, outage=outage+excluded.outage, hist=excluded.hist`,
		target, location, hourOf(at), counted, healthy, degraded, outage, h.encode())
	if err != nil || location == AllLocations {
		return err
	}
	// and the all-locations row the overview reads (one row per API and hour, not one per
	// location)
	return rollCheck(ctx, x, target, AllLocations, at, counted, healthy, degraded, outage, latencyMS)
}

// AllLocations is the hourly row merging every location's checks.
const AllLocations = "ALL"

// rollVerdict counts one verdict in its hour.
func rollVerdict(ctx context.Context, x execer, target string, at time.Time, counted, healthy, degraded, outage int) error {
	_, err := x.ExecContext(ctx, `INSERT INTO hourly(target, location, hour, counted, healthy, degraded, outage) VALUES(?,'*',?,?,?,?,?)
		ON CONFLICT(target, location, hour) DO UPDATE SET counted=counted+excluded.counted, healthy=healthy+excluded.healthy,
		degraded=degraded+excluded.degraded, outage=outage+excluded.outage`, target, hourOf(at), counted, healthy, degraded, outage)
	return err
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// backfillRollups builds the hourly table from raw rows once (a database from before it).
func (s *Store) backfillRollups(ctx context.Context) error {
	var have, raw int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM hourly`).Scan(&have)
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM verdicts`).Scan(&raw)
	if have > 0 || raw == 0 {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO hourly(target, location, hour, counted, healthy, degraded, outage)
		SELECT target, '*', (at/3600000)*3600000, SUM(status!='client'), SUM(status IN ('ok','slow','stale')),
		       SUM(status IN ('slow','stale')), SUM(status IN ('error','down'))
		FROM verdicts GROUP BY target, (at/3600000)*3600000`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO hourly(target, location, hour, counted, healthy, degraded, outage)
		SELECT target, location, (at/3600000)*3600000, SUM(status!='client'), SUM(status IN ('ok','slow','stale')),
		       SUM(status IN ('slow','stale')), SUM(status IN ('error','down'))
		FROM checks GROUP BY target, location, (at/3600000)*3600000`); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT target, location, (at/3600000)*3600000, latency_ms FROM checks
		WHERE status IN ('ok','slow','stale') ORDER BY target, location, at`)
	if err != nil {
		return err
	}
	type key struct {
		t, l string
		h    int64
	}
	hists := map[key]Hist{}
	for rows.Next() {
		var k key
		var ms int64
		if err := rows.Scan(&k.t, &k.l, &k.h, &ms); err != nil {
			rows.Close()
			return err
		}
		if hists[k] == nil {
			hists[k] = NewHist()
		}
		hists[k].add(ms)
	}
	rows.Close()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	all := map[key]Hist{}
	for k, h := range hists {
		ak := key{k.t, AllLocations, k.h}
		if all[ak] == nil {
			all[ak] = NewHist()
		}
		all[ak].Merge(h)
	}
	for k, h := range all {
		hists[k] = h
	}
	// (on tx: the store has one connection, and the transaction holds it. This ran on s.db
	// and waited for itself forever on the first start of an existing database.)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hourly(target, location, hour, counted, healthy, degraded, outage)
		SELECT target, 'ALL', (at/3600000)*3600000, SUM(status!='client'), SUM(status IN ('ok','slow','stale')),
		       SUM(status IN ('slow','stale')), SUM(status IN ('error','down'))
		FROM checks GROUP BY target, (at/3600000)*3600000`); err != nil {
		return err
	}
	for k, h := range hists {
		if _, err := tx.ExecContext(ctx, `INSERT INTO hourly(target, location, hour, hist) VALUES(?,?,?,?)
			ON CONFLICT(target, location, hour) DO UPDATE SET hist=excluded.hist`, k.t, k.l, k.h, h.encode()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// HourRow is one target/location/hour: verdict counts (Location "*") or the latency
// histogram of one location's healthy answers.
type HourRow struct {
	Target   string
	Location string
	Hour     int64 // unix ms
	Bucket
	Hist Hist
}

// Hourly returns the rollup rows in [from, to); target "" for all. merged: only the
// verdict ("*") and all-locations ("ALL") rows, not the per-location ones.
func (s *Store) Hourly(ctx context.Context, target string, from, to time.Time, merged bool) ([]HourRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT target, location, hour, counted, healthy, degraded, outage, hist FROM hourly
		WHERE hour>=? AND hour<? AND (?='' OR target=?) AND (? OR location IN ('*','ALL'))`,
		hourOf(from), ms(to), target, target, !merged)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HourRow
	for rows.Next() {
		var r HourRow
		var hist string
		if err := rows.Scan(&r.Target, &r.Location, &r.Hour, &r.Counted, &r.Healthy, &r.Degraded, &r.Outage, &hist); err != nil {
			return nil, err
		}
		r.Start = r.Hour
		if hist != "" {
			r.Hist = decodeHist(hist)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

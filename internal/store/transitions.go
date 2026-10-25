package store

import (
	"context"
	"time"
)

// Transitions: how often each verdict status was followed by each other at the next
// check, per target and UTC day, counted on write (a window query over a week of
// verdicts took 1.5 s, too much for every export). It is what tells whether "wait" and
// "retry" were good advice: after a failing check, did the next one fail too?
const transitionsSchema = `
CREATE TABLE IF NOT EXISTS transitions (
	target TEXT    NOT NULL,
	day    INTEGER NOT NULL, -- unix ms at 00:00 UTC
	status TEXT    NOT NULL,
	next   TEXT    NOT NULL,
	n      INTEGER NOT NULL,
	PRIMARY KEY (target, day, status, next)
);
`

// TransitionGap: two verdicts further apart than this are not consecutive checks (the
// monitor was not running).
const TransitionGap = 15 * time.Minute

const dayMS = 24 * 3600 * 1000


// countTransition records prev -> v when prev is the target's previous verdict.
func countTransition(ctx context.Context, x execer, target string, prevAt int64, prevStatus string, at int64, status string) error {
	if gap := at - prevAt; gap <= 0 || gap > TransitionGap.Milliseconds() {
		return nil
	}
	_, err := x.ExecContext(ctx, `INSERT INTO transitions(target, day, status, next, n) VALUES(?,?,?,?,1)
		ON CONFLICT(target, day, status, next) DO UPDATE SET n=n+1`, target, at/dayMS*dayMS, prevStatus, status)
	return err
}

// backfillTransitions counts the stored verdicts once, for a database from before the table.
func (s *Store) backfillTransitions(ctx context.Context) error {
	var have, raw int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM transitions`).Scan(&have)
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM verdicts`).Scan(&raw)
	if have > 0 || raw == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO transitions(target, day, status, next, n)
		SELECT target, (next_at/?)*?, status, next, COUNT(*) FROM (
			SELECT target, at, status,
				LEAD(status) OVER (PARTITION BY target ORDER BY at) AS next,
				LEAD(at) OVER (PARTITION BY target ORDER BY at) AS next_at
			FROM verdicts)
		WHERE next IS NOT NULL AND next_at - at > 0 AND next_at - at <= ?
		GROUP BY target, (next_at/?)*?, status, next`, dayMS, dayMS, TransitionGap.Milliseconds(), dayMS, dayMS)
	return err
}

// Transition counts Next[status][next] for one target.
type Transition map[string]map[string]int

// Transitions sums the counts per target since a time (by whole UTC days).
func (s *Store) Transitions(ctx context.Context, since time.Time) (map[string]Transition, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT target, status, next, SUM(n) FROM transitions WHERE day >= ?
		GROUP BY target, status, next`, ms(since)/dayMS*dayMS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Transition{}
	for rows.Next() {
		var target, st, next string
		var n int
		if err := rows.Scan(&target, &st, &next, &n); err != nil {
			return nil, err
		}
		tr := out[target]
		if tr == nil {
			tr = Transition{}
			out[target] = tr
		}
		if tr[st] == nil {
			tr[st] = map[string]int{}
		}
		tr[st][next] += n
	}
	return out, rows.Err()
}

package store

import (
	"context"
	"time"
)

// officialSchema: what providers announced on their own status pages, as runs of one
// indicator. A run is [since, until]: since is the first fetch that saw the indicator,
// until the last. A new run starts when the indicator changes; a gap between runs (the
// page did not answer, or pamon did not run) stays visible, so the lead-time numbers
// can leave out the periods nobody was watching.
const officialSchema = `
CREATE TABLE IF NOT EXISTS official_status (
	id          INTEGER PRIMARY KEY,
	provider    TEXT    NOT NULL,
	since       INTEGER NOT NULL, -- unix ms, first fetch with this indicator
	until       INTEGER NOT NULL, -- unix ms, last fetch with it
	indicator   TEXT    NOT NULL, -- none, minor, major, critical, maintenance
	description TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS official_status_provider ON official_status(provider, since);
`

// OfficialRun is one stretch of a provider announcing the same indicator.
type OfficialRun struct {
	Provider    string    `json:"provider"`
	Since       time.Time `json:"since"`
	Until       time.Time `json:"until"`
	Indicator   string    `json:"indicator"`
	Description string    `json:"description"`
}

// Operational: the provider announced no problem.
func (r OfficialRun) Operational() bool { return r.Indicator == "none" }

// RecordOfficial stores one fetch of a provider's indicator: it extends the provider's
// last run when the indicator is the same and that run was seen within maxGap, else it
// starts a new run.
func (s *Store) RecordOfficial(ctx context.Context, provider, indicator, description string, at time.Time, maxGap time.Duration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id, until int64
	var last string
	err = tx.QueryRowContext(ctx, `SELECT id, until, indicator FROM official_status WHERE provider=? ORDER BY since DESC LIMIT 1`, provider).Scan(&id, &until, &last)
	if err == nil && last == indicator && ms(at)-until <= maxGap.Milliseconds() && ms(at) >= until {
		_, err = tx.ExecContext(ctx, `UPDATE official_status SET until=?, description=? WHERE id=?`, ms(at), description, id)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO official_status(provider, since, until, indicator, description) VALUES(?,?,?,?,?)`,
			provider, ms(at), ms(at), indicator, description)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// OfficialRuns returns the runs that end at or after since, oldest first; provider "" for all.
func (s *Store) OfficialRuns(ctx context.Context, provider string, since time.Time) ([]OfficialRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider, since, until, indicator, description FROM official_status
		WHERE (?='' OR provider=?) AND until>=? ORDER BY provider, since`, provider, provider, ms(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OfficialRun
	for rows.Next() {
		var r OfficialRun
		var a, b int64
		if err := rows.Scan(&r.Provider, &a, &b, &r.Indicator, &r.Description); err != nil {
			return nil, err
		}
		r.Since, r.Until = fromMS(a), fromMS(b)
		out = append(out, r)
	}
	return out, rows.Err()
}

// OfficialFirstSeen is when pamon first read each provider's page.
func (s *Store) OfficialFirstSeen(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider, MIN(since) FROM official_status GROUP BY provider`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var p string
		var at int64
		if err := rows.Scan(&p, &at); err != nil {
			return nil, err
		}
		out[p] = fromMS(at)
	}
	return out, rows.Err()
}

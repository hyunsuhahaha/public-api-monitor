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

// officialIncidentsSchema: incidents providers posted on their status pages, and how far
// back each provider's stored list is complete.
const officialIncidentsSchema = `
CREATE TABLE IF NOT EXISTS official_incidents (
	provider TEXT    NOT NULL,
	id       TEXT    NOT NULL,
	name     TEXT    NOT NULL,
	impact   TEXT    NOT NULL,
	created  INTEGER NOT NULL, -- unix ms, when it was first posted
	resolved INTEGER NOT NULL, -- 0 while open
	url      TEXT    NOT NULL,
	PRIMARY KEY (provider, id)
);
CREATE INDEX IF NOT EXISTS official_incidents_created ON official_incidents(provider, created);
CREATE TABLE IF NOT EXISTS official_history (
	provider TEXT    PRIMARY KEY,
	since    INTEGER NOT NULL, -- every incident posted since then is stored
	fetched  INTEGER NOT NULL  -- ... up to the last read
);
`

// OfficialIncident is one incident a provider posted.
type OfficialIncident struct {
	Provider string    `json:"provider"`
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Impact   string    `json:"impact"`
	Created  time.Time `json:"created"`
	Resolved time.Time `json:"resolved"` // zero while open
	URL      string    `json:"url,omitempty"`
}

// OfficialHistory is how far back a provider's stored incident list is complete:
// every incident posted in [Since, Fetched] is stored.
type OfficialHistory struct {
	Since, Fetched time.Time
}

// Covers reports whether every incident posted in [from, to] is known.
func (h OfficialHistory) Covers(from, to time.Time) bool {
	return !h.Since.After(from) && !h.Fetched.Before(to)
}

// SaveOfficialIncidents stores one read of a provider's incident list, complete since
// `since`. The complete stretch only grows: an earlier read that went further back still
// counts as long as the reads join up (a read whose list reaches back past the last read).
func (s *Store) SaveOfficialIncidents(ctx context.Context, provider string, list []OfficialIncident, since, fetched time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, in := range list {
		if _, err := tx.ExecContext(ctx, `INSERT INTO official_incidents(provider, id, name, impact, created, resolved, url) VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(provider, id) DO UPDATE SET name=excluded.name, impact=excluded.impact, created=excluded.created,
			resolved=excluded.resolved, url=excluded.url`, provider, in.ID, in.Name, in.Impact, ms(in.Created), ms(in.Resolved), in.URL); err != nil {
			return err
		}
	}
	var old OfficialHistory
	var a, b int64
	err = tx.QueryRowContext(ctx, `SELECT since, fetched FROM official_history WHERE provider=?`, provider).Scan(&a, &b)
	if err == nil {
		old = OfficialHistory{fromMS(a), fromMS(b)}
		if !since.After(old.Fetched) && old.Since.Before(since) {
			since = old.Since // this read joins the stored stretch, which went further back
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO official_history(provider, since, fetched) VALUES(?,?,?)
		ON CONFLICT(provider) DO UPDATE SET since=excluded.since, fetched=excluded.fetched`, provider, ms(since), ms(fetched)); err != nil {
		return err
	}
	return tx.Commit()
}

// OfficialIncidents returns the incidents posted or still open at or after since, oldest
// first, and each provider's complete stretch.
func (s *Store) OfficialIncidents(ctx context.Context, since time.Time) ([]OfficialIncident, map[string]OfficialHistory, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider, id, name, impact, created, resolved, url FROM official_incidents
		WHERE created>=? OR resolved=0 OR resolved>=? ORDER BY provider, created`, ms(since), ms(since))
	if err != nil {
		return nil, nil, err
	}
	var out []OfficialIncident
	for rows.Next() {
		var in OfficialIncident
		var c, r int64
		if err := rows.Scan(&in.Provider, &in.ID, &in.Name, &in.Impact, &c, &r, &in.URL); err != nil {
			rows.Close()
			return nil, nil, err
		}
		in.Created, in.Resolved = fromMS(c), fromMS(r)
		out = append(out, in)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	hist := map[string]OfficialHistory{}
	hr, err := s.db.QueryContext(ctx, `SELECT provider, since, fetched FROM official_history`)
	if err != nil {
		return nil, nil, err
	}
	defer hr.Close()
	for hr.Next() {
		var p string
		var a, b int64
		if err := hr.Scan(&p, &a, &b); err != nil {
			return nil, nil, err
		}
		hist[p] = OfficialHistory{fromMS(a), fromMS(b)}
	}
	return out, hist, hr.Err()
}

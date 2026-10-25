package store

import (
	"context"
	"sort"
	"strings"
	"time"
)

// typesSchema: the JSON value kinds each field path has answered with (probe.ShapeTypes),
// one row per target, path and kind.
const typesSchema = `
CREATE TABLE IF NOT EXISTS schema_types (
	target     TEXT    NOT NULL,
	path       TEXT    NOT NULL,
	kind       TEXT    NOT NULL,
	first_seen INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL,
	PRIMARY KEY (target, path, kind)
);
`

// TypeChange is a field answering with a kind it never had.
type TypeChange struct {
	Path string
	Was  []string // the kinds learned before, sorted
	Now  string
}

// ObserveTypes records the value kinds of one healthy answer. It returns the paths that
// came with a new kind, when the path itself was learned at least warmup ago (the first
// day of answers is the baseline: optional fields are null at some hours).
func (s *Store) ObserveTypes(ctx context.Context, target string, types map[string]string, at time.Time, warmup time.Duration) ([]TypeChange, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	paths := make([]string, 0, len(types))
	for p := range types {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var out []TypeChange
	for _, p := range paths {
		kind := types[p]
		res, err := tx.ExecContext(ctx, `UPDATE schema_types SET last_seen=? WHERE target=? AND path=? AND kind=?`, ms(at), target, p, kind)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			continue
		}
		rows, err := tx.QueryContext(ctx, `SELECT kind, first_seen FROM schema_types WHERE target=? AND path=?`, target, p)
		if err != nil {
			return nil, err
		}
		var was []string
		learned := false
		for rows.Next() {
			var k string
			var first int64
			if err := rows.Scan(&k, &first); err != nil {
				rows.Close()
				return nil, err
			}
			was = append(was, k)
			learned = learned || first <= ms(at.Add(-warmup))
		}
		rows.Close()
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_types(target, path, kind, first_seen, last_seen) VALUES(?,?,?,?,?)`, target, p, kind, ms(at), ms(at)); err != nil {
			return nil, err
		}
		if learned {
			sort.Strings(was)
			out = append(out, TypeChange{Path: p, Was: was, Now: kind})
		}
	}
	return out, tx.Commit()
}

// String: "a.b: number → string".
func (c TypeChange) String(name func(string) string) string {
	was := make([]string, len(c.Was))
	for i, k := range c.Was {
		was[i] = name(k)
	}
	return c.Path + ": " + strings.Join(was, "/") + " → " + name(c.Now)
}

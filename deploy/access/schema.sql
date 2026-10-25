CREATE TABLE IF NOT EXISTS access_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at TEXT NOT NULL,
  method TEXT NOT NULL,
  path TEXT NOT NULL,
  status INTEGER NOT NULL,
  duration_ms INTEGER NOT NULL,
  kind TEXT NOT NULL,
  agent TEXT NOT NULL,
  user_agent TEXT NOT NULL,
  country TEXT NOT NULL,
  referrer TEXT NOT NULL,
  resource TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS access_logs_at ON access_logs(at);
CREATE INDEX IF NOT EXISTS access_logs_kind_id ON access_logs(kind, id);
-- "나도 안 돼요" reports. voter is a hash of the address and browser with a salt that
-- changes daily: it can tell repeats apart for a day, not who anyone is.
CREATE TABLE IF NOT EXISTS reports (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  target TEXT NOT NULL,
  at TEXT NOT NULL,
  voter TEXT NOT NULL,
  country TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS reports_target_at ON reports(target, at);
CREATE INDEX IF NOT EXISTS reports_voter_at ON reports(voter, at);

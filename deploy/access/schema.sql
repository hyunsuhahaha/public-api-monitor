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

-- Keypoint Notify schema.
--
-- Invariants worth knowing before editing:
--   * Timestamps are INTEGER unix milliseconds, never TEXT, so range scans and
--     `since` filters stay index-friendly.
--   * Enumerated columns (status, kind, priority) are plain TEXT validated in
--     Go, not CHECK constraints: adding a status should be a code change, not
--     a migration.
--   * `segments.side_id` is '' (not NULL) for task-level segments. The UNIQUE
--     index relies on that, since SQLite treats NULLs as distinct.

CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS roles (
  key           TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  description   TEXT NOT NULL DEFAULT '',
  capabilities  TEXT NOT NULL DEFAULT '[]',
  default_sides TEXT NOT NULL DEFAULT '[]',
  builtin       INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS identities (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  kind        TEXT NOT NULL DEFAULT 'agent',
  roles       TEXT NOT NULL DEFAULT '[]',
  active_role TEXT NOT NULL DEFAULT '',
  key_hash    TEXT NOT NULL UNIQUE,
  key_prefix  TEXT NOT NULL DEFAULT '',
  disabled    INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS tasks (
  id             TEXT PRIMARY KEY,
  seq            INTEGER NOT NULL,
  code           TEXT NOT NULL UNIQUE,
  title          TEXT NOT NULL,
  kind           TEXT NOT NULL DEFAULT 'feature',
  priority       TEXT NOT NULL DEFAULT 'P2',
  status         TEXT NOT NULL DEFAULT 'inbox',
  summary        TEXT NOT NULL DEFAULT '',
  owner_identity TEXT NOT NULL DEFAULT '',
  owner_role     TEXT NOT NULL DEFAULT '',
  labels         TEXT NOT NULL DEFAULT '[]',
  links          TEXT NOT NULL DEFAULT '[]',
  created_by     TEXT NOT NULL DEFAULT '',
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tasks_status  ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_role    ON tasks(owner_role);
CREATE INDEX IF NOT EXISTS idx_tasks_updated ON tasks(updated_at DESC);

CREATE TABLE IF NOT EXISTS sides (
  id                TEXT PRIMARY KEY,
  task_id           TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  key               TEXT NOT NULL,
  title             TEXT NOT NULL DEFAULT '',
  assignee_role     TEXT NOT NULL DEFAULT '',
  assignee_identity TEXT NOT NULL DEFAULT '',
  status            TEXT NOT NULL DEFAULT 'todo',
  deps              TEXT NOT NULL DEFAULT '[]',
  repo              TEXT NOT NULL DEFAULT '',
  branch            TEXT NOT NULL DEFAULT '',
  ord               INTEGER NOT NULL DEFAULT 0,
  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL,
  UNIQUE(task_id, key)
);
CREATE INDEX IF NOT EXISTS idx_sides_assignee ON sides(assignee_role, status);
CREATE INDEX IF NOT EXISTS idx_sides_task     ON sides(task_id);

CREATE TABLE IF NOT EXISTS segments (
  id         TEXT PRIMARY KEY,
  task_id    TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  side_id    TEXT NOT NULL DEFAULT '',
  key        TEXT NOT NULL,
  title      TEXT NOT NULL DEFAULT '',
  body       TEXT NOT NULL DEFAULT '',
  kind       TEXT NOT NULL DEFAULT 'free',
  format     TEXT NOT NULL DEFAULT 'md',
  ord        INTEGER NOT NULL DEFAULT 0,
  updated_by TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(task_id, side_id, key)
);
CREATE INDEX IF NOT EXISTS idx_segments_task ON segments(task_id, side_id, ord);

CREATE TABLE IF NOT EXISTS reports (
  id          TEXT PRIMARY KEY,
  task_id     TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  side_id     TEXT NOT NULL DEFAULT '',
  identity_id TEXT NOT NULL DEFAULT '',
  role        TEXT NOT NULL DEFAULT '',
  type        TEXT NOT NULL DEFAULT 'progress',
  priority    TEXT NOT NULL DEFAULT '',
  body        TEXT NOT NULL DEFAULT '',
  segments    TEXT NOT NULL DEFAULT '[]',
  mentions    TEXT NOT NULL DEFAULT '[]',
  created_at  INTEGER NOT NULL,
  edited_at   INTEGER
);
CREATE INDEX IF NOT EXISTS idx_reports_task ON reports(task_id, created_at DESC);

CREATE TABLE IF NOT EXISTS files (
  id         TEXT PRIMARY KEY,
  sha256     TEXT NOT NULL,
  name       TEXT NOT NULL,
  mime       TEXT NOT NULL DEFAULT 'application/octet-stream',
  size       INTEGER NOT NULL DEFAULT 0,
  rel_path   TEXT NOT NULL,
  uploader   TEXT NOT NULL DEFAULT '',
  task_id    TEXT NOT NULL DEFAULT '',
  side_id    TEXT NOT NULL DEFAULT '',
  scope      TEXT NOT NULL DEFAULT 'task',
  ref_id     TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_files_task  ON files(task_id);
CREATE INDEX IF NOT EXISTS idx_files_scope ON files(scope, ref_id);

CREATE TABLE IF NOT EXISTS events (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  type       TEXT NOT NULL,
  actor_id   TEXT NOT NULL DEFAULT '',
  actor_name TEXT NOT NULL DEFAULT '',
  task_id    TEXT NOT NULL DEFAULT '',
  task_code  TEXT NOT NULL DEFAULT '',
  side_id    TEXT NOT NULL DEFAULT '',
  payload    TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_task ON events(task_id, id DESC);

CREATE TABLE IF NOT EXISTS notifications (
  id          TEXT PRIMARY KEY,
  identity_id TEXT NOT NULL,
  event_id    INTEGER NOT NULL,
  task_id     TEXT NOT NULL DEFAULT '',
  task_code   TEXT NOT NULL DEFAULT '',
  kind        TEXT NOT NULL DEFAULT '',
  title       TEXT NOT NULL DEFAULT '',
  url         TEXT NOT NULL DEFAULT '',
  read_at     INTEGER,
  created_at  INTEGER NOT NULL,
  UNIQUE(identity_id, event_id, kind)
);
CREATE INDEX IF NOT EXISTS idx_notif_inbox ON notifications(identity_id, read_at, id DESC);

CREATE TABLE IF NOT EXISTS watchers (
  task_id     TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  identity_id TEXT NOT NULL,
  created_at  INTEGER NOT NULL,
  PRIMARY KEY (task_id, identity_id)
);

CREATE TABLE IF NOT EXISTS webhooks (
  id          TEXT PRIMARY KEY,
  url         TEXT NOT NULL,
  secret      TEXT NOT NULL DEFAULT '',
  events      TEXT NOT NULL DEFAULT '[]',
  enabled     INTEGER NOT NULL DEFAULT 1,
  last_status INTEGER NOT NULL DEFAULT 0,
  last_error  TEXT NOT NULL DEFAULT '',
  last_at     INTEGER,
  created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS counters (
  name TEXT PRIMARY KEY,
  next INTEGER NOT NULL
);

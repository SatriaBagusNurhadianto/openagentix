#!/bin/sh
# Memory expiry for kagent's long-term memory (ADR 0006, round 3). Run by the CronJob `memory-expiry`; the chart mounts this
# file. It needs psql only, and these variables:
#   KAGENT_DB_URL        connection string of kagent's own read/write database user (from an existing Secret, never printed)
#   MEMORY_SCHEMA        schema of kagent's tables (default kagent)
#   MEMORY_DB_ROLE       the role the login assumes after connecting (default kagent_readwrite): kagent's logins are
#                        NOINHERIT and kagent itself runs set_config('role', ...) after every connect (go/core/pkg/postgres/pool.go)
#   SECRET_SCAN          1: also count memory rows that match well-known secret patterns (default 0)
#   SECRET_SCAN_DELETE   1: and delete them (needs SECRET_SCAN=1; default 0 = report only)
#
# The two prune statements are kagent's own, copied from PruneExpiredMemories in
# go/core/internal/database/memory.go lines 125-139 (kagent v1.0.1, commit 558e3d7). Nothing in kagent calls that function and
# the search does not look at expires_at (memory.go lines 53-66), so without this job a memory never expires. Memories read
# at least ten times since they expired are extended by fifteen days and their access count is reset; the other expired ones
# are deleted. Memories without an expiry are left alone.
#
# The log has COUNTS only: never a memory text, a user id or an agent name. The secret patterns have fixed names.
set -eu
: "${KAGENT_DB_URL:?KAGENT_DB_URL is required}"
SCHEMA="${MEMORY_SCHEMA:-kagent}"
ROLE="${MEMORY_DB_ROLE:-kagent_readwrite}"
case "${SECRET_SCAN:-0}" in 1|true) SCAN=on ;; *) SCAN=off ;; esac
case "${SECRET_SCAN_DELETE:-0}" in 1|true) SCANDEL=on ;; *) SCANDEL=off ;; esac
if [ "$SCANDEL" = on ] && [ "$SCAN" = off ]; then
  echo "memory-expiry: SECRET_SCAN_DELETE needs SECRET_SCAN" >&2
  exit 2
fi
export PGCONNECT_TIMEOUT=10
export PGOPTIONS="-c statement_timeout=120000 -c lock_timeout=15000 -c idle_in_transaction_session_timeout=60000"

# psql prints nothing but our own \echo line: results are taken with \gset, command tags are off with -q.
psql "$KAGENT_DB_URL" -X -q -v ON_ERROR_STOP=1 -v schema="$SCHEMA" -v role="$ROLE" -v scan="$SCAN" -v scandel="$SCANDEL" <<'SQL'
SET ROLE :"role";
SET search_path TO :"schema", public;
\set scan_rows 0
\set scan_deleted 0
\set scan_counts ''
BEGIN;
SELECT count(*) AS total_before FROM memory \gset
SELECT count(*) AS expired FROM memory WHERE expires_at < NOW() \gset
WITH e AS (
  UPDATE memory
  SET expires_at = NOW() + INTERVAL '15 days', access_count = 0
  WHERE expires_at < NOW() AND access_count >= 10
  RETURNING 1
) SELECT count(*) AS extended FROM e \gset
WITH d AS (
  DELETE FROM memory
  WHERE expires_at < NOW() AND access_count < 10
  RETURNING 1
) SELECT count(*) AS deleted FROM d \gset
\if :scan
-- Secret scan: a short, conservative list of well-known shapes. Names are fixed words; only counts leave the database.
CREATE TEMP TABLE secret_patterns (name text PRIMARY KEY, re text NOT NULL) ON COMMIT DROP;
INSERT INTO secret_patterns (name, re) VALUES
  ('aws_access_key_id',  '\m(AKIA|ASIA)[0-9A-Z]{16}\M'),
  ('bearer_token',       '[Bb]earer[ ]+[A-Za-z0-9._~+/=-]{24,}'),
  ('github_token',       '\m(gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{50,})'),
  ('google_api_key',     '\mAIza[0-9A-Za-z_-]{35}'),
  ('jwt',                '\meyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}'),
  ('private_key_header', '-----BEGIN [A-Z ]*PRIVATE KEY-----'),
  ('provider_secret_key', '\msk-[A-Za-z0-9_-]{32,}'),
  ('slack_token',        '\mxox[abeprs]-[A-Za-z0-9-]{10,}'),
  ('stripe_secret_key',  '\m(sk|rk)_(live|test)_[0-9A-Za-z]{16,}'),
  ('telegram_bot_token', '\m[0-9]{8,10}:[A-Za-z0-9_-]{35}');
SELECT coalesce(string_agg(name || '=' || n, ' ' ORDER BY name), '') AS scan_counts
  FROM (SELECT p.name, (SELECT count(*) FROM memory m WHERE m.content ~ p.re) AS n FROM secret_patterns p) s \gset
SELECT count(*) AS scan_rows FROM memory m WHERE EXISTS (SELECT 1 FROM secret_patterns p WHERE m.content ~ p.re) \gset
\if :scandel
WITH d AS (
  DELETE FROM memory m WHERE EXISTS (SELECT 1 FROM secret_patterns p WHERE m.content ~ p.re) RETURNING 1
) SELECT count(*) AS scan_deleted FROM d \gset
\endif
\endif
COMMIT;
SELECT count(*) AS total_after FROM memory \gset
\echo memory-expiry: total_before=:total_before expired=:expired extended=:extended deleted=:deleted total_after=:total_after
\if :scan
\echo memory-expiry: secret_scan=:scan delete=:scandel rows_matching=:scan_rows rows_deleted=:scan_deleted patterns=:scan_counts
\else
\echo memory-expiry: secret_scan=off
\endif
SQL

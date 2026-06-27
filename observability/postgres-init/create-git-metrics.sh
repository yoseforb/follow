#!/bin/sh
set -e

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<SQL
CREATE SCHEMA IF NOT EXISTS metrics;

CREATE TABLE IF NOT EXISTS metrics.git_daily (
  collected_at DATE NOT NULL,
  repo TEXT NOT NULL,
  commits_total INTEGER NOT NULL DEFAULT 0,
  files_total INTEGER NOT NULL DEFAULT 0,
  contributors_total INTEGER NOT NULL DEFAULT 0,
  lines_total BIGINT NOT NULL DEFAULT 0,
  commits_7d INTEGER NOT NULL DEFAULT 0,
  commits_30d INTEGER NOT NULL DEFAULT 0,
  commits_90d INTEGER NOT NULL DEFAULT 0,
  lines_added_7d BIGINT NOT NULL DEFAULT 0,
  lines_removed_7d BIGINT NOT NULL DEFAULT 0,
  lines_added_30d BIGINT NOT NULL DEFAULT 0,
  lines_removed_30d BIGINT NOT NULL DEFAULT 0,
  last_commit_epoch BIGINT NOT NULL DEFAULT 0,
  first_commit_epoch BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (collected_at, repo)
);

CREATE TABLE IF NOT EXISTS metrics.git_commit_types (
  collected_at DATE NOT NULL,
  repo TEXT NOT NULL,
  commit_type TEXT NOT NULL,
  count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (collected_at, repo, commit_type)
);

CREATE TABLE IF NOT EXISTS metrics.git_languages (
  collected_at DATE NOT NULL,
  repo TEXT NOT NULL,
  language TEXT NOT NULL,
  lines BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (collected_at, repo, language)
);

GRANT USAGE ON SCHEMA metrics TO postgres_exporter;
GRANT SELECT ON ALL TABLES IN SCHEMA metrics TO postgres_exporter;
SQL

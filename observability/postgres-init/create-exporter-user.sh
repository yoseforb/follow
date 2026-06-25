#!/bin/sh
set -e

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<SQL
DO \$\$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'postgres_exporter') THEN
    CREATE USER postgres_exporter WITH PASSWORD '${POSTGRES_EXPORTER_PASSWORD}';
    GRANT pg_monitor TO postgres_exporter;
  END IF;
END
\$\$;

-- Schema access for custom business metrics queries
GRANT USAGE ON SCHEMA "user" TO postgres_exporter;
GRANT USAGE ON SCHEMA route TO postgres_exporter;
GRANT USAGE ON SCHEMA images TO postgres_exporter;
GRANT SELECT ON "user".users TO postgres_exporter;
GRANT SELECT ON route.routes TO postgres_exporter;
GRANT SELECT ON images.images TO postgres_exporter;
SQL

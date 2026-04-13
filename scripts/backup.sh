#!/bin/sh
# backup.sh — PostgreSQL dump + MinIO mirror + encrypted .env → R2
# Commands: install-cron | run-now

# shellcheck disable=SC3040
set -euo pipefail

BACKUP_SCHEDULE="${BACKUP_SCHEDULE:-0 3 * * *}"

install_cron() {
  # Dump required env vars into the crontab — cron runs in a
  # clean shell with no access to the container's environment.
  {
    env | grep -E '^(R2_|POSTGRES_|PGPASSWORD|MINIO_|AGE_)' \
      | sort
    echo "${BACKUP_SCHEDULE} /usr/local/bin/backup.sh run-now 2>&1"
  } | crontab -
  echo "backup: cron installed (schedule: ${BACKUP_SCHEDULE})"
}

run_now() {
  ts=$(date -u +%Y-%m-%d-%H%M%S)
  echo "backup: starting (${ts})"

  # ── Configure mc aliases ──────────────────────────────────────
  mc alias set r2 "${R2_ENDPOINT}" "${R2_ACCESS_KEY}" "${R2_SECRET_KEY}" --api S3v4
  mc alias set local http://minio:9000 "${MINIO_ACCESS_KEY_ID}" "${MINIO_SECRET_ACCESS_KEY}"

  # ── Preflight checks ──────────────────────────────────────────
  if ! mc ls "r2/${R2_BACKUP_BUCKET}/" >/dev/null 2>&1; then
    echo "backup: FATAL — R2 bucket '${R2_BACKUP_BUCKET}' does not exist or is unreachable" >&2
    echo "backup: create it in the Cloudflare dashboard before running backups" >&2
    exit 1
  fi

  if ! mc ls "local/${MINIO_BUCKET_NAME}/" >/dev/null 2>&1; then
    echo "backup: FATAL — MinIO bucket '${MINIO_BUCKET_NAME}' does not exist or is unreachable" >&2
    exit 1
  fi

  # ── 1. PostgreSQL dump ────────────────────────────────────────
  # -Fc (custom format) compresses internally — no extra gzip needed.
  echo "backup: dumping postgres..."
  pg_dump -Fc -h postgres -U "${POSTGRES_USER}" "${POSTGRES_DB}" \
    | mc pipe "r2/${R2_BACKUP_BUCKET}/postgres/${ts}.dump"
  echo "backup: postgres dump uploaded"

  # ── 2. MinIO mirror (additive — never --remove) ──────────────
  echo "backup: mirroring minio..."
  mc mirror --overwrite "local/${MINIO_BUCKET_NAME}" "r2/${R2_BACKUP_BUCKET}/minio/${MINIO_BUCKET_NAME}/"
  echo "backup: minio mirror complete"

  # ── 3. Encrypted .env backup ─────────────────────────────────
  # age reads AGE_PASSPHRASE from the environment natively —
  # no stdin piping needed (age reads passphrase from tty, not stdin).
  echo "backup: encrypting .env..."
  age --passphrase -o /tmp/env-backup.age /backup-src/.env
  mc cp /tmp/env-backup.age "r2/${R2_BACKUP_BUCKET}/env/${ts}.env.age"
  shred -u /tmp/env-backup.age
  echo "backup: .env backup uploaded"

  # ── 4. Last-success marker (only on full success) ────────────
  printf '%s\n' "${ts}" | mc pipe "r2/${R2_BACKUP_BUCKET}/_last_success.txt"
  echo "backup: complete (${ts})"
}

case "${1:-}" in
  install-cron) install_cron ;;
  run-now)      run_now ;;
  *)
    echo "usage: backup.sh install-cron | run-now" >&2
    exit 1
    ;;
esac

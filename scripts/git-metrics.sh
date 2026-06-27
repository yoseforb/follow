#!/bin/sh
# git-metrics.sh — collect git development metrics into PostgreSQL
# Run via cron every hour: 0 * * * * /path/to/git-metrics.sh

set -eu

SCRIPT_DIR=$(cd -- "$(dirname -- "$0")" && pwd)
BASE_DIR=$(cd -- "${SCRIPT_DIR}/.." && pwd)

REPOS="
follow:${BASE_DIR}
follow-api:${BASE_DIR}/follow-api
follow-image-gateway:${BASE_DIR}/follow-image-gateway
follow-app:${BASE_DIR}/follow-app
follow-pkg:${BASE_DIR}/follow-pkg
follow-business:${BASE_DIR}/follow-business
"

BINARY_EXTS="jpg|jpeg|png|gif|webp|ico|woff|woff2|ttf|eot|svg|pdf|zip|tar|gz|bin|exe|so|dylib|onnx|pb"
EMPTY_TREE="4b825dc642cb6eb9a060e54bf899d15f3b7d7a3a"
COMMIT_TYPES="feat|fix|refactor|test|docs|style|improve|chore"
TODAY=$(date +%Y-%m-%d)

db_exec() {
  docker exec -i follow-postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -qtAX' 2>/dev/null
}

for entry in ${REPOS}; do
  repo_name="${entry%%:*}"
  repo_path="${entry#*:}"

  [ -d "${repo_path}/.git" ] || continue

  commits=$(git -C "${repo_path}" rev-list --count HEAD 2>/dev/null || echo 0)
  files=$(git -C "${repo_path}" ls-files 2>/dev/null | wc -l)
  contributors=$(git -C "${repo_path}" shortlog -sn --all 2>/dev/null | wc -l)
  last_epoch=$(git -C "${repo_path}" log -1 --format=%ct 2>/dev/null || echo 0)
  first_epoch=$(git -C "${repo_path}" log --reverse --format=%ct 2>/dev/null | head -1)

  total_lines=$(git -C "${repo_path}" diff --numstat "${EMPTY_TREE}" HEAD 2>/dev/null \
    | grep -vE "\.(${BINARY_EXTS})$" \
    | awk '{ if ($1 != "-") sum += $1 } END { print sum+0 }')

  commits_7d=$(git -C "${repo_path}" rev-list --count --since="7 days ago" HEAD 2>/dev/null || echo 0)
  commits_30d=$(git -C "${repo_path}" rev-list --count --since="30 days ago" HEAD 2>/dev/null || echo 0)
  commits_90d=$(git -C "${repo_path}" rev-list --count --since="90 days ago" HEAD 2>/dev/null || echo 0)

  churn_7d=$(git -C "${repo_path}" log --since="7 days ago" --pretty=tformat: --numstat 2>/dev/null \
    | awk '{ if ($1 != "-") { a += $1; r += $2 } } END { printf "%d %d", a+0, r+0 }')
  added_7d=$(echo "${churn_7d}" | cut -d' ' -f1)
  removed_7d=$(echo "${churn_7d}" | cut -d' ' -f2)

  churn_30d=$(git -C "${repo_path}" log --since="30 days ago" --pretty=tformat: --numstat 2>/dev/null \
    | awk '{ if ($1 != "-") { a += $1; r += $2 } } END { printf "%d %d", a+0, r+0 }')
  added_30d=$(echo "${churn_30d}" | cut -d' ' -f1)
  removed_30d=$(echo "${churn_30d}" | cut -d' ' -f2)

  echo "INSERT INTO metrics.git_daily VALUES
    ('${TODAY}','${repo_name}',${commits},${files},${contributors},${total_lines},
     ${commits_7d},${commits_30d},${commits_90d},
     ${added_7d},${removed_7d},${added_30d},${removed_30d},
     ${last_epoch},${first_epoch:-0})
    ON CONFLICT (collected_at, repo) DO UPDATE SET
      commits_total=EXCLUDED.commits_total, files_total=EXCLUDED.files_total,
      contributors_total=EXCLUDED.contributors_total, lines_total=EXCLUDED.lines_total,
      commits_7d=EXCLUDED.commits_7d, commits_30d=EXCLUDED.commits_30d, commits_90d=EXCLUDED.commits_90d,
      lines_added_7d=EXCLUDED.lines_added_7d, lines_removed_7d=EXCLUDED.lines_removed_7d,
      lines_added_30d=EXCLUDED.lines_added_30d, lines_removed_30d=EXCLUDED.lines_removed_30d,
      last_commit_epoch=EXCLUDED.last_commit_epoch, first_commit_epoch=EXCLUDED.first_commit_epoch;" \
    | db_exec

  git -C "${repo_path}" log --format="%s" 2>/dev/null \
    | sed -n 's/^\([a-z]*\)(.*/\1/p' \
    | grep -E "^(${COMMIT_TYPES})$" \
    | sort | uniq -c | while read -r count ctype; do
      echo "INSERT INTO metrics.git_commit_types VALUES ('${TODAY}','${repo_name}','${ctype}',${count})
        ON CONFLICT (collected_at, repo, commit_type) DO UPDATE SET count=EXCLUDED.count;" \
        | db_exec
    done

  git -C "${repo_path}" diff --numstat "${EMPTY_TREE}" HEAD 2>/dev/null \
    | grep -vE "\.(${BINARY_EXTS})$" \
    | sed -n 's/.*\.\([a-zA-Z]*\)$/\1/p' \
    | sort | uniq | while read -r ext; do
      lang_lines=$(git -C "${repo_path}" diff --numstat "${EMPTY_TREE}" HEAD 2>/dev/null \
        | grep "\.${ext}$" \
        | awk '{ if ($1 != "-") sum += $1 } END { print sum+0 }')
      if [ "${lang_lines:-0}" -gt 0 ]; then
        echo "INSERT INTO metrics.git_languages VALUES ('${TODAY}','${repo_name}','${ext}',${lang_lines})
          ON CONFLICT (collected_at, repo, language) DO UPDATE SET lines=EXCLUDED.lines;" \
          | db_exec
      fi
    done

  echo "collected: ${repo_name} (${commits} commits)"
done

#!/bin/sh
# git-metrics-backfill.sh — populate historical git metrics into PostgreSQL
# Walks git history day-by-day from each repo's first commit to end date.
#
# Usage: ./scripts/git-metrics-backfill.sh [end-date]
# Default end date: yesterday

set -eu

SCRIPT_DIR=$(cd -- "$(dirname -- "$0")" && pwd)
BASE_DIR=$(cd -- "${SCRIPT_DIR}/.." && pwd)

END_DATE="${1:-$(date -d "yesterday" +%Y-%m-%d 2>/dev/null || date -j -v-1d +%Y-%m-%d)}"

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

next_day() {
  date -d "$1 + 1 day" +%Y-%m-%d 2>/dev/null || date -j -v+1d -f "%Y-%m-%d" "$1" +%Y-%m-%d 2>/dev/null
}

db_exec() {
  docker exec -i follow-postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -qtAX' 2>/dev/null
}

echo "Backfill end date: ${END_DATE}"

for entry in ${REPOS}; do
  repo_name="${entry%%:*}"
  repo_path="${entry#*:}"

  [ -d "${repo_path}/.git" ] || continue

  first_date=$(git -C "${repo_path}" log --reverse --format=%aI | head -1 | cut -dT -f1)
  [ -z "${first_date}" ] && continue

  first_epoch=$(git -C "${repo_path}" log --reverse --format=%ct | head -1)
  current_date="${first_date}"

  echo "${repo_name}: ${first_date} to ${END_DATE}"

  while [ "${current_date}" \< "${END_DATE}" ] || [ "${current_date}" = "${END_DATE}" ]; do
    hash=$(git -C "${repo_path}" log --before="${current_date} 23:59:59" -1 --format=%H 2>/dev/null)
    [ -z "${hash}" ] && { current_date=$(next_day "${current_date}"); continue; }

    commits=$(git -C "${repo_path}" rev-list --count "${hash}" 2>/dev/null || echo 0)
    files=$(git -C "${repo_path}" ls-tree -r --name-only "${hash}" 2>/dev/null | wc -l)
    contributors=$(git -C "${repo_path}" shortlog -sn "${hash}" 2>/dev/null | wc -l)
    last_epoch=$(git -C "${repo_path}" log "${hash}" -1 --format=%ct 2>/dev/null || echo 0)

    total_lines=$(git -C "${repo_path}" diff --numstat "${EMPTY_TREE}" "${hash}" 2>/dev/null \
      | grep -vE "\.(${BINARY_EXTS})$" \
      | awk '{ if ($1 != "-") sum += $1 } END { print sum+0 }')

    for window in 7 30 90; do
      since_date=$(date -d "${current_date} - ${window} days" +%Y-%m-%d 2>/dev/null \
        || date -j -v-${window}d -f "%Y-%m-%d" "${current_date}" +%Y-%m-%d 2>/dev/null)
      eval "commits_${window}d=\$(git -C \"${repo_path}\" rev-list --count --since=\"${since_date}\" \"${hash}\" 2>/dev/null || echo 0)"
    done

    for window in 7 30; do
      since_date=$(date -d "${current_date} - ${window} days" +%Y-%m-%d 2>/dev/null \
        || date -j -v-${window}d -f "%Y-%m-%d" "${current_date}" +%Y-%m-%d 2>/dev/null)
      churn=$(git -C "${repo_path}" log --since="${since_date}" --before="${current_date} 23:59:59" \
        "${hash}" --pretty=tformat: --numstat 2>/dev/null \
        | awk '{ if ($1 != "-") { a += $1; r += $2 } } END { printf "%d %d", a+0, r+0 }')
      eval "added_${window}d=\$(echo \"${churn}\" | cut -d' ' -f1)"
      eval "removed_${window}d=\$(echo \"${churn}\" | cut -d' ' -f2)"
    done

    echo "INSERT INTO metrics.git_daily VALUES
      ('${current_date}','${repo_name}',${commits},${files},${contributors},${total_lines},
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

    git -C "${repo_path}" log "${hash}" --format="%s" 2>/dev/null \
      | sed -n 's/^\([a-z]*\)(.*/\1/p' \
      | grep -E "^(${COMMIT_TYPES})$" \
      | sort | uniq -c | while read -r count ctype; do
        echo "INSERT INTO metrics.git_commit_types VALUES ('${current_date}','${repo_name}','${ctype}',${count})
          ON CONFLICT (collected_at, repo, commit_type) DO UPDATE SET count=EXCLUDED.count;" \
          | db_exec
      done

    git -C "${repo_path}" diff --numstat "${EMPTY_TREE}" "${hash}" 2>/dev/null \
      | grep -vE "\.(${BINARY_EXTS})$" \
      | sed -n 's/.*\.\([a-zA-Z]*\)$/\1/p' \
      | sort | uniq | while read -r ext; do
        lang_lines=$(git -C "${repo_path}" diff --numstat "${EMPTY_TREE}" "${hash}" 2>/dev/null \
          | grep "\.${ext}$" \
          | awk '{ if ($1 != "-") sum += $1 } END { print sum+0 }')
        if [ "${lang_lines:-0}" -gt 0 ]; then
          echo "INSERT INTO metrics.git_languages VALUES ('${current_date}','${repo_name}','${ext}',${lang_lines})
            ON CONFLICT (collected_at, repo, language) DO UPDATE SET lines=EXCLUDED.lines;" \
            | db_exec
        fi
      done

    echo "  ${current_date}: ${repo_name} (${commits} commits)"
    current_date=$(next_day "${current_date}")
  done
done

echo "Backfill complete."

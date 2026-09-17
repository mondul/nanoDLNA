#!/usr/bin/env bash
#
# Checks commit messages against the Conventional Commits specification.
#
#   commit-lint.sh --all                  every commit reachable from HEAD
#   commit-lint.sh <range>                the commits in a range, e.g. v1.0.0..HEAD
#   commit-lint.sh --message-file <path>  a single message file, as handed to a
#                                         commit-msg hook
#
# Merge commits are skipped because they carry no authored message of their own,
# and so are the "Revert ..." subjects git writes by itself.
#
# Exits non-zero, naming every offending commit, when a message does not
# conform. Used both by the commit-msg hook and by the CI check, so the two can
# never disagree about what "conventional" means.
set -euo pipefail

TYPES='feat|fix|build|chore|ci|docs|style|refactor|perf|test|revert'
HEADER_RE="^(${TYPES})(\([a-z0-9][a-z0-9._/-]*\))?!?: .+$"
MAX_HEADER=100

failures=0
checked=0

usage() {
  printf 'usage: %s --all | <range> | --message-file <path>\n' "$(basename "$0")" >&2
  exit 2
}

explain() {
  cat >&2 <<'EOF'
A message must follow Conventional Commits:

    <type>[optional scope][!]: <description>

    feat(player): add gapless playback
    fix: stop the scan from following symlinks
    feat(api)!: drop the v1 endpoints

Allowed types: feat, fix, build, chore, ci, docs, style, refactor, perf,
test, revert. Mark a breaking change with ! before the colon, or with a
BREAKING CHANGE: footer.
EOF
}

check_subject() {
  local label="$1" subject="$2"
  local problems=''

  if [[ "$subject" != Revert\ \"* ]]; then
    if ! [[ "$subject" =~ $HEADER_RE ]]; then
      problems="${problems}  - the subject does not match <type>(<scope>): <description>"$'\n'
    fi
    if [ "${#subject}" -gt "$MAX_HEADER" ]; then
      problems="${problems}  - the subject is ${#subject} characters, the limit is ${MAX_HEADER}"$'\n'
    fi
  fi

  if [ -n "$problems" ]; then
    printf 'commit-lint: %s\n  %s\n%s\n' "$label" "$subject" "$problems" >&2
    failures=$((failures + 1))
  fi
}

lint_range() {
  local range="$1" sha
  while read -r sha; do
    [ -n "$sha" ] || continue
    checked=$((checked + 1))
    check_subject "${sha:0:8}" "$(git log -1 --format=%s "$sha")"
  done < <(git rev-list --no-merges "$range")
}

lint_message_file() {
  local path="$1" subject
  # A hook receives the message as git wrote it, comments and scissors line
  # included, so take the first line that is neither blank nor a comment.
  subject=$(awk '
    /^# ------------------------ >8 ------------------------$/ { exit }
    /^#/ { next }
    NF { print; exit }
  ' "$path")
  checked=$((checked + 1))
  check_subject "(the message being committed)" "$subject"
}

case "${1:-}" in
  --all)
    lint_range HEAD
    ;;
  --message-file)
    [ "$#" -eq 2 ] || usage
    lint_message_file "$2"
    ;;
  --help | -h)
    usage
    ;;
  '')
    usage
    ;;
  *)
    lint_range "$1"
    ;;
esac

if [ "$failures" -gt 0 ]; then
  printf 'commit-lint: %d of %d message(s) do not follow Conventional Commits.\n\n' \
    "$failures" "$checked" >&2
  explain
  exit 1
fi

printf 'commit-lint: all %d message(s) follow Conventional Commits.\n' "$checked"

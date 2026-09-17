#!/usr/bin/env bash
#
# Release helper, driven entirely by the Conventional Commits in the history.
#
#   release.sh next-version     prints version=, release= and reason= lines,
#                               ready to be appended to $GITHUB_OUTPUT
#   release.sh notes <version>  prints the release notes as markdown
#
# A breaking change bumps the major version, a feature the minor and a fix the
# patch. A range containing none of those produces no release at all, so a run
# of documentation or chore commits does not cut a version.
set -euo pipefail

TAG_PATTERN='v[0-9]*'
SECTION_ORDER='breaking feature fix perf refactor docs test build other'

last_tag() {
  git describe --tags --abbrev=0 --match "$TAG_PATTERN" 2>/dev/null || true
}

commits_since() {
  local tag="$1"
  if [ -n "$tag" ]; then
    git rev-list --no-merges --reverse "${tag}..HEAD"
  else
    git rev-list --no-merges --reverse HEAD
  fi
}

repo_url() {
  if [ -n "${GITHUB_REPOSITORY:-}" ]; then
    printf 'https://github.com/%s' "$GITHUB_REPOSITORY"
    return
  fi
  local url
  url=$(git remote get-url origin 2>/dev/null || true)
  case "$url" in
    git@github.com:*) printf 'https://github.com/%s' "$(printf '%s' "${url#git@github.com:}" | sed 's/\.git$//')" ;;
    https://github.com/*) printf '%s' "${url%.git}" ;;
    *) printf '' ;;
  esac
}

subject_type() {
  if [[ "$1" =~ ^([a-zA-Z]+)(\([^\)]*\))?(!)?: ]]; then
    printf '%s' "${BASH_REMATCH[1]}" | tr '[:upper:]' '[:lower:]'
  else
    printf 'other'
  fi
}

subject_scope() {
  if [[ "$1" =~ ^[a-zA-Z]+\(([^\)]*)\) ]]; then
    printf '%s' "${BASH_REMATCH[1]}"
  fi
}

subject_description() {
  printf '%s' "$1" | sed -E 's/^[a-zA-Z]+(\([^)]*\))?!?: //'
}

is_breaking() {
  local subject="$1" body="$2"
  if [[ "$subject" =~ ^[a-zA-Z]+(\([^\)]*\))?!: ]]; then
    return 0
  fi
  case "$body" in
    *'BREAKING CHANGE:'* | *'BREAKING-CHANGE:'*) return 0 ;;
  esac
  return 1
}

category_for_type() {
  case "$1" in
    feat) printf 'feature' ;;
    fix | revert) printf 'fix' ;;
    perf) printf 'perf' ;;
    refactor) printf 'refactor' ;;
    docs) printf 'docs' ;;
    test) printf 'test' ;;
    build | ci) printf 'build' ;;
    *) printf 'other' ;;
  esac
}

section_title() {
  case "$1" in
    breaking) printf 'Breaking changes' ;;
    feature) printf 'Features' ;;
    fix) printf 'Bug fixes' ;;
    perf) printf 'Performance' ;;
    refactor) printf 'Refactoring' ;;
    docs) printf 'Documentation' ;;
    test) printf 'Tests' ;;
    build) printf 'Build and continuous integration' ;;
    *) printf 'Other changes' ;;
  esac
}

cmd_next_version() {
  local tag
  tag=$(last_tag)

  local count=0 major=0 minor=0 patch=0
  local sha subject body type
  while read -r sha; do
    [ -n "$sha" ] || continue
    count=$((count + 1))
    subject=$(git log -1 --format=%s "$sha")
    body=$(git log -1 --format=%b "$sha")
    type=$(subject_type "$subject")

    if is_breaking "$subject" "$body"; then
      major=1
    elif [ "$type" = 'feat' ]; then
      minor=1
    else
      case "$type" in
        fix | perf | revert) patch=1 ;;
      esac
    fi
  done < <(commits_since "$tag")

  local where
  if [ -n "$tag" ]; then where="since $tag"; else where='in the whole history'; fi

  if [ "$major" -eq 0 ] && [ "$minor" -eq 0 ] && [ "$patch" -eq 0 ]; then
    printf 'release=false\n'
    printf 'version=\n'
    printf 'reason=no feature, fix or breaking change %s (%d commit(s) examined)\n' "$where" "$count"
    return
  fi

  local next
  if [ -z "$tag" ]; then
    # Nothing has been released yet, so the first release claims 1.0.0 rather
    # than deriving a number from a history that has no baseline.
    next='1.0.0'
  else
    local current="${tag#v}" cur_major cur_minor cur_patch
    cur_major=$(printf '%s' "$current" | cut -d. -f1)
    cur_minor=$(printf '%s' "$current" | cut -d. -f2)
    cur_patch=$(printf '%s' "$current" | cut -d. -f3)
    if [ "$major" -eq 1 ]; then
      next="$((cur_major + 1)).0.0"
    elif [ "$minor" -eq 1 ]; then
      next="${cur_major}.$((cur_minor + 1)).0"
    else
      next="${cur_major}.${cur_minor}.$((cur_patch + 1))"
    fi
  fi

  local kind
  if [ "$major" -eq 1 ]; then kind='major'; elif [ "$minor" -eq 1 ]; then kind='minor'; else kind='patch'; fi

  printf 'version=%s\n' "$next"
  printf 'release=true\n'
  printf 'reason=%s bump over %d commit(s) %s\n' "$kind" "$count" "$where"
}

cmd_notes() {
  local version="$1"
  local tag url
  tag=$(last_tag)
  url=$(repo_url)

  local tmp
  tmp=$(mktemp -d)
  # The path is expanded now rather than when the trap fires, because tmp is
  # local and would be out of scope by then.
  trap "rm -rf '$tmp'" EXIT

  local section
  for section in $SECTION_ORDER; do
    : >"$tmp/$section"
  done

  local count=0 sha subject body type category
  while read -r sha; do
    [ -n "$sha" ] || continue
    count=$((count + 1))
    subject=$(git log -1 --format=%s "$sha")
    body=$(git log -1 --format=%b "$sha")
    type=$(subject_type "$subject")

    if is_breaking "$subject" "$body"; then
      category='breaking'
    else
      category=$(category_for_type "$type")
    fi

    render_entry "$sha" "$subject" "$body" "$type" "$url" >>"$tmp/$category"
  done < <(commits_since "$tag")

  printf '## What changed\n\n'
  if [ "$count" -eq 1 ]; then
    printf 'One commit'
  else
    printf '%d commits' "$count"
  fi
  if [ -n "$tag" ]; then printf ' since `%s`' "$tag"; fi
  printf '.\n'

  for section in $SECTION_ORDER; do
    if [ -s "$tmp/$section" ]; then
      printf '\n### %s\n\n' "$(section_title "$section")"
      cat "$tmp/$section"
    fi
  done

  if [ -n "$url" ]; then
    printf '\n'
    if [ -n "$tag" ]; then
      printf '**Full changelog**: %s/compare/%s...v%s\n' "$url" "$tag" "$version"
    else
      printf '**Full changelog**: %s/commits/v%s\n' "$url" "$version"
    fi
  fi
}

render_entry() {
  local sha="$1" subject="$2" body="$3" type="$4" url="$5"
  local description scope origin link

  description=$(subject_description "$subject")
  scope=$(subject_scope "$subject")

  origin="$type"
  if [ -n "$scope" ]; then origin="$type($scope)"; fi

  if [ -n "$url" ]; then
    link="[\`${sha:0:8}\`]($url/commit/$sha)"
  else
    link="\`${sha:0:8}\`"
  fi

  printf -- '- **%s** (%s, %s)\n' "$description" "$origin" "$link"

  # The full body is included, because the release is meant to say what
  # actually changed rather than only naming the commits.
  local trimmed
  trimmed=$(printf '%s' "$body" | sed -e 's/[[:space:]]*$//')
  if [ -n "$(printf '%s' "$trimmed" | tr -d '[:space:]')" ]; then
    printf '\n'
    printf '%s\n' "$trimmed" | sed -e 's/^/  /'
    printf '\n'
  fi
}

case "${1:-}" in
  next-version)
    cmd_next_version
    ;;
  notes)
    [ "$#" -eq 2 ] || {
      printf 'usage: %s notes <version>\n' "$(basename "$0")" >&2
      exit 2
    }
    cmd_notes "$2"
    ;;
  *)
    printf 'usage: %s next-version | notes <version>\n' "$(basename "$0")" >&2
    exit 2
    ;;
esac

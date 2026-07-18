#!/bin/sh
set -eu

if [ "$#" -ne 3 ]; then
  echo "usage: rolling-latest.sh CANDIDATE CURRENT_MAIN CURRENT_LATEST_OR_DASH" >&2
  exit 2
fi

candidate=$1
current_main=$2
current_latest=$3

valid_revision() {
  case "$1" in
    ''|*[!0-9a-f]*) return 1 ;;
  esac
  length=${#1}
  [ "$length" -eq 40 ] || [ "$length" -eq 64 ]
}

require_commit() {
  valid_revision "$1" && git cat-file -e "$1^{commit}" 2>/dev/null || {
    echo "rolling-latest.sh: invalid or unavailable revision: $1" >&2
    exit 1
  }
}

require_commit "$candidate"
require_commit "$current_main"
if [ "$current_latest" != '-' ]; then
  require_commit "$current_latest"
fi

if ! git merge-base --is-ancestor "$candidate" "$current_main"; then
  echo "rolling-latest.sh: candidate is not an ancestor of current main" >&2
  exit 1
fi

if [ "$current_latest" != '-' ] &&
   ! git merge-base --is-ancestor "$current_latest" "$candidate" &&
   ! git merge-base --is-ancestor "$candidate" "$current_latest"; then
  echo "rolling-latest.sh: current latest and candidate have diverged" >&2
  exit 1
fi

if [ "$current_latest" = '-' ]; then
  echo advance
elif [ "$current_latest" = "$candidate" ]; then
  echo current
elif git merge-base --is-ancestor "$current_latest" "$candidate"; then
  echo advance
else
  # The current latest is a descendant of this candidate. Never roll the
  # pointer back even if main was reset to the older revision.
  echo noop
fi

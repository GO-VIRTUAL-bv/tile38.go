#!/usr/bin/env bash
# Stop guard: don't hand the turn back with the tree in a state CI would reject.
# Runs at end-of-turn rather than per-edit so a multi-file refactor isn't blocked
# on its own intermediate states. Exit 2 blocks the stop and returns stderr.
set -uo pipefail
cd "${CLAUDE_PROJECT_DIR:-.}" || exit 0

payload=$(cat)
# Already re-entered once from a blocked stop — let it through rather than loop.
if [ "$(jq -r '.stop_hook_active // false' <<<"$payload")" = "true" ]; then
	exit 0
fi

# Nothing to check if no Go file is dirty. --porcelain covers modified, staged,
# and untracked, so a brand-new file is caught too.
git status --porcelain -- '*.go' | grep -q . || exit 0

unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
	echo "gofmt would rewrite these files (CI fails on this before it runs anything else):" >&2
	echo "$unformatted" >&2
	exit 2
fi

# `make vet` runs both tag sets. A plain `go vet ./...` never compiles
# integration_test.go, so a broken integration test only surfaces in CI.
if ! out=$(make vet 2>&1); then
	echo "$out" >&2
	exit 2
fi

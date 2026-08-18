#!/usr/bin/env bash
# PostToolUse guard: format edited Go files, and refuse new runtime dependencies.
# Reads the hook payload on stdin. Exit 2 hands stderr back to Claude to fix.
set -uo pipefail
cd "${CLAUDE_PROJECT_DIR:-.}" || exit 0

f=$(jq -r '.tool_input.file_path // empty')

case "$f" in
*.go)
	# `golangci-lint fmt` applies the formatters block of .golangci.yml — gofmt
	# plus goimports with local-prefixes — so it agrees with `make lint` instead
	# of approximating it. CI hard-fails on `gofmt -l` before anything else runs.
	golangci-lint fmt "$f" >&2 || exit 2
	;;
*go.mod)
	# No runtime dependencies is the point of this repository, not an accident.
	# testcontainers is test-only and lives behind the `integration` build tag.
	new=$(go mod edit -json |
		jq -r '.Require[] | select(.Indirect != true) | .Path' |
		grep -v '^github.com/testcontainers/testcontainers-go$') || true
	if [ -n "$new" ]; then
		echo "New direct dependency: ${new//$'\n'/, }" >&2
		echo "This client is deliberately dependency-free (CLAUDE.md). Drop it unless the user explicitly asked for it." >&2
		exit 2
	fi
	;;
esac

#!/usr/bin/env bash
# PostToolUse guard: format edited Go files, and refuse new runtime dependencies.
# Reads the hook payload on stdin. Exit 2 hands stderr back to Claude to fix.
set -uo pipefail
cd "${CLAUDE_PROJECT_DIR:-.}" || exit 0

f=$(jq -r '.tool_input.file_path // empty')

case "$f" in
*.go)
	# MPL-2.0 is file-level copyleft, so the Exhibit A header is what puts a file
	# inside the licence at all — an unheadered file arguably falls outside the
	# protection this repo picked deliberately. Prepend rather than block, since
	# there is only one correct answer. The trailing blank line is load-bearing:
	# without it the header merges into a package doc comment (and shows up on
	# pkg.go.dev), or detaches a //go:build constraint from its package clause.
	if [ -f "$f" ] && ! head -1 "$f" | grep -q "Mozilla Public"; then
		{
			echo "// This Source Code Form is subject to the terms of the Mozilla Public"
			echo "// License, v. 2.0. If a copy of the MPL was not distributed with this"
			echo "// file, You can obtain one at https://mozilla.org/MPL/2.0/."
			echo
			cat "$f"
		} >"$f.mpl.tmp" && mv "$f.mpl.tmp" "$f"
		echo "Added the MPL-2.0 Exhibit A header to $f — every .go file needs one." >&2
	fi
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

#!/bin/sh
# Compile every ```go fence in the shipped documentation against the real
# package.
#
# The skill under .claude/skills/tile38 was hand-written and shipped three `Set`
# geometry terminals — Radius, Tile, A5 — that have never existed in any commit.
# Nothing caught it, because prose is not compiled. This target compiles it.
#
# Each fence becomes one function body in a throwaway module that `replace`s the
# repo, so a method named on the wrong receiver is a build error the way it would
# be in a caller's code. Fences are extracted verbatim: no rewriting beyond a
# wrapper, a shared preamble for identifiers an example leaves to the reader
# (ctx, c, use, ...), and a blank assignment per top-level `:=` so an example
# that shows a result without consuming it still compiles.
#
# The consequence for whoever edits the docs: a Go fence has to be real Go.
# Elide with a comment rather than `…`, and declare what you use.
set -eu

repo=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

cat > "$work/go.mod" <<EOF
module doccheck

go $(sed -n 's/^go \(.*\)/\1/p' "$repo/go.mod")

require github.com/GO-VIRTUAL-bv/tile38.go v0.0.0

replace github.com/GO-VIRTUAL-bv/tile38.go => $repo
EOF

{
	cat <<'EOF'
package doccheck

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	tile38 "github.com/GO-VIRTUAL-bv/tile38.go"
	"github.com/GO-VIRTUAL-bv/tile38.go/endpoint"
)

// Identifiers the examples leave to the reader.
var (
	_       = errors.Is
	_       = fmt.Println
	_       = log.Println
	_       = time.Second
	_       = endpoint.Local
	ctx     context.Context
	c       *tile38.Client
	geojson string
	pts     tile38.Points
	trucks  []struct {
		ID              string
		Speed, Lat, Lon float64
	}
)

func use(...any)                {}
func handle(*tile38.FenceEvent) {}
EOF
	awk '
		/^```go$/ { infence = 1; nline = 0; nname = 0; next }
		/^```$/   { if (infence) emit(); infence = 0; next }
		infence   { line[nline++] = $0
			# Collect the names a top-level `x, y := …` declares, so the
			# fence still compiles when it only shows a result.
			if ($0 ~ /^[a-zA-Z_][a-zA-Z0-9_, ]*:=/ && $1 != "for" && $1 != "if" && $1 != "switch") {
				split($0, halves, ":=")
				n = split(halves[1], names, ",")
				for (i = 1; i <= n; i++) {
					gsub(/[ \t]/, "", names[i])
					if (names[i] != "" && names[i] != "_") decl[nname++] = names[i]
				}
			}
			next
		}
		function emit(   i) {
			# An import fence is a snippet, not a body.
			for (i = 0; i < nline; i++) if (line[i] ~ /^import /) return
			printf "\nfunc _fence%d() error {\n", ++nfence
			for (i = 0; i < nline; i++) print "\t" line[i]
			for (i = 0; i < nname; i++) printf "\tuse(%s)\n", decl[i]
			print "\treturn nil\n}"
			for (i = 0; i < nname; i++) delete decl[i]
		}
	' "$@"
} > "$work/doccheck.go"

gofmt -w "$work/doccheck.go" 2>/dev/null || true

if ! out=$(cd "$work" && go build ./... 2>&1); then
	echo "A Go example in the documentation does not compile against the package:"
	echo "$out" | sed "s|^\./doccheck\.go|  generated|"
	echo
	echo "Each _fenceN is one \`\`\`go block, in file order across:"
	for f in "$@"; do echo "  $f"; done
	exit 1
fi

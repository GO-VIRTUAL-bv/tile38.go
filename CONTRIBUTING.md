# Contributing

## The rule that is not obvious

Every command this client speaks is one stock Tile38 accepts. `tile38/tile38:edge`,
pinned by digest in `.version`, is the acceptance test: if edge rejects it, it
does not go in — it lands in Tile38 itself first. A rejection is a finding, not
a quirk to work around.

Check per command, not per feature. A5 is the case that proves why: `WITHIN`,
`INTERSECTS` and `GET` all take an A5 cell, while `SETHOOK`/`SETCHAN` reject the
same token — so A5 is exposed as a search area and deliberately not as a fence
area.

## No runtime dependencies

That is the point of the repository, not an accident. `go.mod` has one direct
requirement, testcontainers, and it stays behind the `integration` build tag, so
`go get` of this library never pulls it into your build. A repo hook refuses a
new direct dependency on edit.

## Setup

`make lint` needs [golangci-lint](https://golangci-lint.run) v2 at the version
pinned in `.golangci-version`. CI reads the same file, so a newer local binary
means a clean run here stops predicting CI:

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(cat .golangci-version)
```

Integration tests need Docker; nothing else does.

Once golangci-lint is in place, `make hooks` points `core.hooksPath` at
`.githooks`, whose `pre-push` runs `make lint test` before every push. Git will not
pick a checked-in hooks directory up on its own, so that one command is
per-clone. Bypass it on a work-in-progress branch with `git push --no-verify`.

`make lint` also fails on a Go file that is not gofmt-formatted, or that is
missing the MPL-2.0 Exhibit A header — the header is what puts a file inside the
licence at all. CI runs the same two make targets, so the hook predicts it.

## Before opening a PR

```bash
make lint              # both tag sets — plain `go vet ./...` skips the integration file entirely
make test              # scripted server, no Docker
make test-integration  # a real Tile38 via testcontainers
```

If you changed how a command is assembled or a reply is decoded, verify it
against a running server rather than against the Tile38 source. Several reply
shapes are counterintuitive, and a wrong parser returns plausible-looking wrong
data instead of an error. `Client.Do(ctx, args...)` sends a raw command and
returns the decoded reply; the quirks already confirmed this way are listed in
[CLAUDE.md](CLAUDE.md), which is worth reading before touching `parse.go` or a
builder.

Say which Tile38 version you checked against in the PR.

## Commits

Conventional Commits — `type(scope): subject`, with `!` for a breaking change.
Breaking changes are permitted pre-1.0 and expected to be deliberate: prefer
deleting a name over deprecating it, so a caller gets a compile error rather
than a check that silently stops firing.

PRs are squash-merged, so the PR title becomes the commit and appears in the
release notes.

## Claude Code

`.claude/settings.json` is checked in, so [Claude Code](https://claude.com/claude-code)
picks up the two repo hooks automatically. The format hook shells out to
`golangci-lint` so it agrees with `make lint` rather than approximating it —
which means it fails on every edit if the binary is not on your `PATH`. Install
it first, or drop `.claude/settings.json` from your working copy.

---
name: add-command
description: Add a new Tile38 command, option token, or builder method to this client. Use when wiring up a command the client does not speak yet, adding an option to an existing builder, or exposing a new reply shape. Covers where each piece goes, the assemble-don't-append rules, and what to test.
---

# Adding a command to tile38.go

Three files, in this order. Skipping the third is how a command ships that emits
correct bytes and then decodes the reply wrong.

1. **`commands.go`** — the entry point on `*Client`. One function, one doc line.
2. **The builder** — next to its siblings: search builders and shared machinery in
   `builders.go`, `INTERSECTS` in `intersects.go`, `SETCHAN` in `channel.go`,
   `JSET`/`JGET`/`JDEL` in `json.go`, `DROP`/`RENAME`/`TTL`/`KEYS`/`BOUNDS` and
   friends in `management.go`.
3. **`parse.go`** — a parse helper, but only if the reply shape is genuinely new.
   Reuse an existing helper when the shape matches.

Everything public lives in the root package. `internal/resp` depends on nothing,
`internal/conn` depends on `resp`, the root depends on both — `parse.go` stays in
the root precisely because it decodes into public result types, and moving it down
would create an import cycle.

## Naming

**Methods are named after Tile38 keywords, in Go casing.** `Point` not `At`, `EX`
not `TTL`, `FSet`/`FGet` not `FSET`/`FGET`. Someone reading a chain should be able
to guess the command it emits and grep the Tile38 docs for it.

Invent a friendlier name only where no keyword exists — `Radius` for the trailing
metres of `POINT lat lon meters`, `DBSize` for `SERVER`.`num_objects` — and say so
in the doc comment.

Every command here is one stock Tile38 accepts. Verify against
`tile38/tile38:edge` before adding it: if edge rejects the command, it does not
go in — it needs to land in Tile38 itself first.

## The entry point

```go
// Nearby starts building a Tile38 NEARBY query for the given collection.
func (c *Client) Nearby(collection string) *NearbyCmd {
	return &NearbyCmd{c: c, args: []any{"NEARBY", collection}}
}
```

Verb and key go into `args` at construction. Nothing else does.

## Assemble, never append

This is the rule that breaks things quietly if you get it wrong.

**Search commands.** The grammar is `VERB key [options…] [fence clause] [output
format] <geometry>` and **geometry must come last**. So search builders keep the
parts in *separate slices* — `args` (verb, key, options), `fence`, `geom`, plus
`radius` on `NearbyCmd` — and `buildSearch` concatenates them in protocol order at
exec time.

That is what makes chain order irrelevant: `.Point(…).Radius(…).Limit(10)` and
`.Limit(10).Point(…).Radius(…)` emit identical bytes.

> A geometry method must **assign** `cmd.geom`. Never append to `cmd.args` —
> that puts the token in the options section and malforms the command. An earlier
> design tracked a `geomLen` offset and spliced around it, which silently
> corrupted any chain that added an option after the geometry.

`HookCmd` and `SetChanCmd` work the same way — their `Do` calls `buildSearch` with
`fenceTokens(...)`, because `SETHOOK` and `SETCHAN` take a full search command
after the name, with the same geometry-last rule.

**Positional arguments, including on hooks.** `SETHOOK name <endpoints> [META k v]…
[EX n] <trigger> …` takes the endpoint positionally, so `HookCmd` keeps `name`,
`endpoints`, `meta`, `ex`, and `trigger` in separate fields and assembles them in
`Do`. Appending in call order made `.Within(…).Endpoint(…)` emit a malformed
command. A trailing modifier on the fence area needs its own field for the same
reason: the old `LIVE` modifier appended to `geom`, and a later `Get` silently
discarded it.

## Adding an option

First decide whether it repeats. **Grep `internal/server/token.go` for an
`errDuplicateArgument` guard on the token.**

- **Guarded (single-use)** → store it in a field, render it once at exec time.
  `LIMIT`, `CURSOR`, `SPARSE`, `NOFIELDS`, and `CLIP` live in the `searchOpts`
  struct and render via `searchOpts.tokens()`. `DETECT` and `COMMANDS` live on the
  builder and render via `fenceTokens`. Calling `Limit` twice must overwrite, not
  emit a duplicate the server rejects.
- **Accumulating** → append to `args`. `WHERE`, `WHEREIN`, and `MATCH` genuinely
  accumulate in Tile38.

Two constraints that are easy to miss: `searchOpts.fenceOpts()` drops `CURSOR` on
the fence path because Tile38 rejects "CURSOR … FENCE", and `ScanCmd` has no
`Sparse` because the server rejects `SPARSE` for `SCAN`.

`Detect`/`Commands` render only from `Fence`, never from `IDs`/`Points`/`Count`/
`Objects` — otherwise a stray `.Detect(…)` turns a plain query into a live one on
a pooled connection and the caller tries to parse `+OK` as results.

## Protocol tokens are named types

`DetectState` and `Command` are `~string` types with exported
constants, so a typo is a compile error rather than a server error. A new
closed set of tokens should follow suit; `joinTokens[T ~string]` renders them in
the comma-separated form Tile38 expects.

`argString` switches on concrete types, so **convert a named string type to
`string` at the boundary** before it enters `args`.

## Parsing the reply

`resp.ReadReply` produces exactly four things: `string`, `int64`, `[]any`, `nil`.
Every helper in `parse.go` is written against those. RESP2 has no float type, so
coordinates arrive as bulk strings — that is why `toFloat64` accepts strings.

For search replies, element 0 is a **cursor, not a count**: a resume offset when
the scan hit the limit, `0` when it ran to completion. Every output except `COUNT`
is capped at 100 items with no explicit `LIMIT`, so a non-zero cursor is the
truncation signal — return it and let the terminal turn it into `ErrTruncated`
via `truncation()`. `COUNT` is exempt and replies with a **bare integer**.

If you are unsure of a reply's shape, get it instead of guessing:
`Client.Do(ctx, args...)` sends a raw command and returns the decoded reply
against `docker run --rm -p 9851:9851 tile38/tile38:edge`. Delegating to the
`protocol-verifier` agent is the thorough version of this.

## Tests

Add to `tile38_test.go`, against the scripted `net.Listener` — no Docker. Use the
existing `wire(...)`, `fakeServer`, and `fakeServerN` helpers; the
`resp-fixture-writer` agent handles the byte-counting if the fixture is awkward.

Extend the invariant tests that already exist rather than writing parallel ones:

| Test | Covers |
|---|---|
| `TestChainOrderIsIrrelevant` | geometry-last assembly |
| `TestHookAssembly` | positional hook args |
| `TestRepeatedOptions` | single-use vs accumulating options |
| `TestDetectIgnoredWithoutFence` | fence-only token rendering |

Add a `tile38_integration_test.go` case (build tag `integration`) when the command's
behaviour depends on real server state. Each test takes its collection key from
`t.Name()` and drops it on cleanup.

## Before you finish

```bash
make fmt
make vet     # both tag sets — a plain `go vet ./...` never compiles tile38_integration_test.go
make lint
make test
```

Then update `.claude/skills/tile38/reference.md` — it is the published command
catalog for this client and drifts silently when a command is added without it.

## No new dependencies

This client has no runtime dependencies and that is the point of the repository,
not an accident. Speaking RESP over `net.Conn` directly is what makes live
geofences possible. Do not add one without being asked.

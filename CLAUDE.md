# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Tile38 client that speaks RESP over `net.Conn` directly. It has **no runtime
dependencies** — that is the point of the repository, not an accident. Do not
add one without being asked; testcontainers is a test-only dependency and stays
behind the `integration` build tag.

Speaking the protocol directly is what makes live geofences possible: a Redis
client cannot hold a connection open while the server streams events into it.

## Commands

```bash
make help                                       # list targets
make lint                                       # golangci-lint, both tag sets
make test                                       # unit tests, no Docker needed
make test-integration                           # against tile38/tile38:edge via testcontainers
make test-all

go test -run TestFenceStream ./...              # a single unit test
go test -tags=integration -run TestLiveFence ./...
```

The supported server is pinned in `.version`, which `TestMain` reads and
`TILE38_IMAGE` overrides. It is an `edge` digest rather than a release tag
because A5 is merged into upstream master but ships in no tag yet — `1.38.0` has
no `internal/server/a5.go`. Pinning the digest keeps a push to upstream master
from silently changing what CI tests against; the `Tile38 Upstream Check`
workflow opens an issue when a release lands or the digest moves, so the pin
gets bumped deliberately rather than drifting.

Integration tests are behind the `integration` build tag, so a plain
`go vet ./...` or `golangci-lint run ./...` skips that file entirely — always
run both tag sets, which is what `make lint` and `make vet` do.

`.golangci.yml` follows the sibling `common` repo. Its exclusions are
deliberate and explained inline: complexity metrics are waived for `resp.go`
(flat protocol switches) and `dupl` for the parallel search builders. Prefer
fixing a finding over widening those.

## Layout

A Go package is a directory, so the whole public API is `package tile38` in the
repo root — it cannot be split across folders without changing the import path.
The root therefore holds **only public API**, and the wire plumbing lives under
`internal/`:

```
tile38.go        Client, New + Option funcs, Close/Ping/Do — transport façade
commands.go      command entry points (c.Set, c.Nearby, …)
types.go         result types
builders.go      write, read, search, hook builders + protocol token types
intersects.go    INTERSECTS builder
channel.go       SETCHAN / channel builders
json.go          JSET / JGET / JDEL builders
management.go    DROP, RENAME, TTL, KEYS, BOUNDS, … builders
stream.go        Stream (live fences and subscriptions)
fence_event.go   FenceEvent
parse.go         reply → result-type decoding
internal/resp/   RESP codec: AppendCommand, ReadReply, Error
internal/conn/   Conn and Pool — dialling, pooling, deadlines, pipelining
```

`internal/resp` depends on nothing; `internal/conn` depends on `resp`; the root
depends on both. Keep it that way — `parse.go` stays in the root precisely
because it decodes into public result types, and moving it down would create an
import cycle.

`ServerError` is a type alias for `resp.Error` so the internal codec can return
it while callers still `errors.As` against the public name.

## Architecture

**Builders** (`builders.go`, `intersects.go`, `channel.go`, `json.go`,
`management.go`). Every command is a struct holding `args []any` plus a
`*Client`. Chained methods append tokens; a terminal method (`Do`, `IDs`,
`Points`, `Count`, `Objects`, `Fence`) sends them. Adding a command means:
entry point in `commands.go`, builder type next to its siblings, parse helper
in `parse.go` if the reply shape is new.

**Methods are named after Tile38 keywords**, in Go casing — `Point` not `At`,
`EX` not `TTL`, `FSet`/`FGet` not `FSET`/`FGET`. Someone reading a chain should
be able to guess the command it emits and grep the Tile38 docs for it. Invent a
friendlier name only where no keyword exists (`Radius` for the trailing metres
of `POINT lat lon meters`, `DBSize` for `SERVER`.`num_objects`) and say so in
the doc comment.

`GlobalBounds()` returns four values rather than a struct so Go's `f(g())`
binding hands it straight to any `Bounds(swLat, swLon, neLat, neLon)` method.
Keep that shape — a struct would force every `Bounds` call site to name fields.

**Search commands are assembled, not appended.** Tile38 search grammar is
`VERB key [options…] [fence clause] [output format] <geometry>`, and the
geometry must come last. Search builders therefore keep those parts in
*separate slices* — `args` (verb, key, options), `fence`, `geom`, and for
`NearbyCmd` a `radius` — and `buildSearch` concatenates them in protocol order
at exec time.

This is what makes chain order irrelevant: `.Point(…).Radius(…).Limit(10)` and
`.Limit(10).Point(…).Radius(…)` emit identical bytes, and `TestChainOrderIsIrrelevant`
locks that down. A geometry method must **assign** `cmd.geom`, never append to
`cmd.args` — appending puts the token in the options section and malforms the
command. (An earlier design tracked a `geomLen` offset and spliced around it,
which silently corrupted any chain that added an option after the geometry.)

`HookCmd` and `SetChanCmd` are assembled the same way — their `Do` calls
`buildSearch` with `fenceTokens(...)` — because SETHOOK and SETCHAN take a full
search command after the name, with the same geometry-last rule.
`TestHookAssembly` covers it.

**Single-use options are fields, not appends.** Tile38 errors on a repeated
`LIMIT`, `CURSOR`, `SPARSE`, `NOFIELDS`, `CLIP`, `DETECT`, `COMMANDS`, or
`FENCE`. The search ones live in a `searchOpts` struct on each builder
(`limit *int`, `cursor *uint64`, `sparse *int`, `nofields`, `clip`) and are
rendered once by `searchOpts.tokens()` at exec time, so calling `Limit` twice
overwrites instead of emitting a duplicate the server would reject. `DETECT` and
`COMMANDS` are stored on the builder and rendered by `fenceTokens`.

`WHERE`, `WHEREIN`, and `MATCH` genuinely accumulate in Tile38, so they stay
appended to `args`. Adding a new option means checking
`internal/server/token.go` for an `errDuplicateArgument` guard and picking the
matching storage. `TestRepeatedOptions` covers both sides.

`searchOpts.fenceOpts()` drops `CURSOR` on the fence path, because Tile38
rejects "CURSOR ... FENCE". `ScanCmd` has no `Sparse`: the server rejects
`SPARSE` for `SCAN`.

**Positional arguments are assembled, not appended — including on hooks.**
`SETHOOK name <endpoints> [META k v]… [EX n] <trigger> …` takes the endpoint
positionally, so `HookCmd` keeps `name`, `endpoints`, `meta`, `ex`, and
`trigger` in separate fields and assembles them in `Do`. Appending them in call
order made `.Within(…).Endpoint(…)` emit a malformed command. `TestHookAssembly`
pins it. A trailing modifier on the fence area needs its own field for the same
reason: appended to `geom`, a later `Get` silently overwrites it away.

`Detect`/`Commands` are rendered only by `Fence`, never by `IDs`/`Points`/
`Count`/`Objects` — otherwise a stray `.Detect(…)` would turn a plain query
into a live one on a pooled connection, and the caller would try to parse the
`+OK` as results. `TestDetectIgnoredWithoutFence` pins that.

**Transport** (`internal/conn`). `Pool.Do` takes a connection from an idle
pool, writes one RESP array, reads one reply. The distinction that governs
connection reuse: a `resp.Error` (a `-ERR` reply) means the command was
rejected but the connection is fine, so it returns to the pool; any other error
means the stream is in an unknown state and the connection is dropped — that is
what `isServerError` gates. Context cancellation is handled with
`context.AfterFunc` closing the connection to unblock the read.

`MaxIdle` caps *idle* connections; `MaxActive` caps in-flight ones through a
semaphore taken in `Do`/`DoPipeline`. Streams take their connection from `Dial`
and are deliberately outside that cap — they hold it for hours, so counting them
would starve everything else. `Timeout` defaults to `DefaultTimeout` because an
unset timeout plus a deadline-free context meant no deadline at all; a negative
value opts back out.

**Streams** (`stream.go`). `Fence`, `Subscribe`, and `PSubscribe` take a
connection from `Pool.Dial`, which never returns to the pool, and carry no read
deadline — a quiet fence sends nothing for hours. Both live fences (bulk string
of JSON) and pub/sub pushes (`[message, channel, payload]` arrays) decode to
`*FenceEvent`, so `Stream.Next` handles both shapes and skips subscribe acks.

**Protocol tokens are named types.** `DetectState` and `Command` are `~string`
types with exported constants, so a typo is a compile error rather than a server
error. `joinTokens[T ~string]` renders them
in the comma-separated form Tile38 expects. Note that `argString` switches on
concrete types, so a named string type must be converted before it enters
`args` — the builders do this at the boundary.

**Decoding.** `resp.ReadReply` produces `string`, `int64`, `[]any`, or `nil`.
Every helper in `parse.go` is written against those four types. RESP2 has no
float type, so coordinates arrive as bulk strings — `toFloat64` accepts strings.

## Verified protocol quirks

These are counterintuitive and were confirmed against a running server. The
previous implementation of this client got all of them wrong, mostly silently.
Do not "correct" them back without re-testing.

- Every search output except `COUNT` is capped at **100 results** when the
  command carries no `LIMIT` (`limitItems`, `internal/server/scanner.go`). The
  reply's element 0 is a **cursor**, not a count: it is a resume offset when the
  scan hit the limit and `0` when it ran to completion. That makes a non-zero
  cursor the truncation signal, which `parse.go` returns and the search
  terminals turn into `ErrTruncated`. Discarding it is how a client silently
  returns partial results once a collection grows past 100.
- `COUNT` is exempt from that cap — the server sets `limit = MaxUint64` for it —
  and replies with a **bare integer**, not `[count, …]`. `parseCount`
  deliberately refuses the array form rather than reading the cursor as a count.
- `ROAM` is accepted **only** on a live `NEARBY` fence (`search.go`: the type
  check is bypassed when `fence && cmd == "nearby"`), which is what makes
  `NODWELL` reachable there. Hooks and channels accept it via their own trigger.
- `NODWELL` is opt-in everywhere, via `NoDwell()` on `NearbyCmd`, `HookCmd`, and
  `SetChanCmd`. `HookCmd.Roam`/`SetChanCmd.Roam` used to set it implicitly, which
  meant a hook could never report an object that stayed in range — dwelling is
  Tile38's own default and the caller's choice. It only affects `ROAM` fences
  (`fence.go` reads it in the roam matching path alone).
- `DISTANCE` is an **option** token, so it precedes the output format:
  `NEARBY key DISTANCE POINTS POINT …`, not `POINTS DISTANCE`.
- Every output format except `IDS` and `COUNT` appends the object's fields as a
  flat `[name, value, …]` array, present only when the object has non-zero
  fields: `OBJECTS` gives `[id, geojson, [fields…]?]`, `POINTS` gives
  `[id, [lat, lon], [fields…]?]`. `parseFields` decodes it into the public
  `Fields` type; a parse path that stops at the geometry silently drops every
  field the caller asked the server for.
- A coordinate array is `[lat, lon]` or `[lat, lon, z]`: Tile38 appends the third
  ordinate only when it is non-zero (`extractZCoordinate`), on `POINTS` output
  and on `GET key id POINT` alike, so both lengths turn up in one reply and a
  zero z is indistinguishable from a two-dimensional point. `parseCoords` is the
  one place that decodes it.
- With `DISTANCE POINTS`, an item is `[id, [lat, lon], [fields…]?, dist]`. The
  fields array appears only when the object has non-zero fields, so distance is
  read from the **end** of the item, not a fixed index — and the fields are only
  there when the item is longer than three elements.
- A geofence notification carries `"fields"` as a JSON **object** (`sw.fullFields`
  in `fence.go`), where the RESP search replies send the same values as flat
  text: a string field arrives quoted. `Fields.UnmarshalJSON` unquotes it so a
  field reads identically whichever path it arrived on. Hook and channel events
  also carry `"meta"`.
- Coordinate order differs by command: `GET key id BOUNDS` returns
  `[[swlat, swlon], [nelat, nelon]]`, while `BOUNDS key` returns the collection
  extent x-first as `[[minlon, minlat], [maxlon, maxlat]]`.
- `HOOKS` and `CHANS` return descriptors
  `[name, key, [endpoint…], [command token…], [meta…]]` — not flat strings, and
  element 1 is the collection key, not the endpoint.
- Tile38 has **no `DBSIZE` command**; `DBSize` reads `num_objects` out of `SERVER`.
- Tile38 accepts every coordinate a `float64` can hold, including out-of-range
  values and `NaN`. Only non-numeric text is rejected, which is why the
  pipeline error path can only be tested against a scripted reply.
- Live geofence framing: the server answers the fence command with `+OK`, then
  writes each event as a RESP bulk string containing JSON, on the same connection.

When a reply shape is in doubt, check it against the server rather than
guessing — `Client.Do(ctx, args...)` sends a raw command and returns the
decoded reply. If the Tile38 source is checked out at `../../../tile38`, the
authoritative encoders are `internal/server/scanner.go` (search output),
`internal/server/live.go` (fence streaming), and `internal/server/token.go`
(which option tokens are order-independent).

## Upstream Tile38 only

**Every command here is one stock Tile38 accepts.** `tile38/tile38:edge` is the
acceptance test: if edge rejects it, it does not go in — it needs to land in
Tile38 itself first. A rejection is a finding, not a quirk to document around.

Check per command, not per feature. A5 is the case that proves why: `WITHIN`,
`INTERSECTS`, and `GET` all accept an A5 cell, but `SETHOOK`/`SETCHAN` reject the
same token with `invalid argument` — while those same hooks take `FENCE BOUNDS …`
without complaint. So A5 is exposed as a search area and deliberately not as a
fence area.

A5 also needs a server built from upstream master: it is merged
(`internal/server/a5.go`) but carried by no release tag — `1.38.0` has no
`a5.go`. That is why `.version` pins an edge digest rather than a tag.

## Tests

`internal/resp/resp_test.go` covers the codec directly — encoding, every reply
type, malformed input, and the length bounds. `internal/conn/conn_test.go`
covers the transport against a local listener: pool defaults, the AUTH
handshake, which errors return a connection to the pool, `MaxIdle` eviction,
pipelining, and cancellation. `tile38_test.go` drives a scripted `net.Listener`
that returns hand-written RESP bytes, covering the commands the builders emit
and the stream lifecycle, without Docker. `integration_test.go` (build tag
`integration`) shares one container across the package via `TestMain`; each
test takes its own collection key from `t.Name()` and drops it on cleanup.

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

The linter version is pinned in `.golangci-version`, which the CI workflow reads
and `make lint` checks the local binary against before running. It was `latest`
in CI once, so a new golangci-lint release silently changed what CI enforced and
a clean local run stopped predicting it — which is how a `goconst` finding got
past review. Bump the file to adopt a new version deliberately, the same way
`.version` handles the server.

`.golangci.yml` follows the sibling `common` repo. Its exclusions are
deliberate and explained inline: complexity metrics are waived for `resp.go`
(flat protocol switches) and `dupl` for the parallel search builders. Prefer
fixing a finding over widening those.

## Layout

A Go package is a directory, so the client API is all `package tile38` in the
repo root — it cannot be split across folders without changing the import path.
The root therefore holds **only public API**, the wire plumbing lives under
`internal/`, and the one thing deliberately given its own import path is
`endpoint/`:

```
tile38.go        Client, New + Option funcs, Close/Ping/Do — transport façade
commands.go      command entry points (c.Set, c.Nearby, …)
types.go         result types
builders.go      write, read, search, hook builders + protocol token types
intersects.go    INTERSECTS builder
geometry.go      Area values + the TEST builder
server.go        STATS, CONFIG, GC, HEALTHZ, READONLY, FOLLOW, TIMEOUT
channel.go       SETCHAN / channel builders (chain methods live on fenceBase)
json.go          JSET / JGET / JDEL builders
management.go    DROP, RENAME, TTL, KEYS, BOUNDS, … builders
stream.go        Stream (live fences and subscriptions)
fence_event.go   FenceEvent
parse.go         reply → result-type decoding
endpoint/        SETHOOK endpoint URL builders (public, imported by callers)
internal/resp/   RESP codec: AppendCommand, ReadReply, Error
internal/conn/   Conn and Pool — dialling, pooling, deadlines, pipelining
```

`internal/resp` depends on nothing; `internal/conn` depends on `resp`; the root
depends on both. Keep it that way — `parse.go` stays in the root precisely
because it decodes into public result types, and moving it down would create an
import cycle.

`endpoint` is the one other public package. It depends on nothing, not even the
root: its constructors return plain strings, which is what lets them nest inside
`EndpointURL` — see **Endpoint URLs** below.

`ServerError` is a type alias for `resp.Error` so the internal codec can return
it while callers still `errors.As` against the public name.

**The not-found sentinels are `ServerError` constants, not `errors.New` values.**
`resp.Error` is a comparable string type holding the wire text verbatim
(`Error(body)` in `ReadReply`), so `const ErrIDNotFound ServerError = "ERR id
not found"` is matched by `errors.Is` through plain `==` while it unwraps the
`fmt.Errorf("tile38: GET: %w", …)` every command already applies. That is the
whole implementation — no `Is` method, no text normalising, no call-site change.

The `"ERR "` prefix is not decoration: upstream adds it in `server.go` unless the
message's first word is all-uppercase, so a lowercase-first error like `key not
found` always arrives prefixed. Check the exact string in
`internal/server/token.go` before adding a sentinel; one whose text is even
slightly off silently never matches.

**There are two sentinels, not four, and that is a finding rather than an
oversight.** `errPathNotFound` and `errIDAlreadyExists` exist upstream but are
unreachable over RESP — see the quirk below. A sentinel that can never match is
worse than none, because it reads as a supported branch.

Both are also produced from replies that are not errors at all: `GetCmd.exec`
turns a null into `ErrIDNotFound`, `parseBoundsResult` turns one into
`ErrKeyNotFound`, and `TTL` turns `-2` into `ErrIDNotFound`, so a caller writes
one check whichever way the server spelled the miss. The cost is that
`errors.As` on those paths yields a `ServerError` the server never literally
sent — a deliberate trade for one spelling of "missing". The guard lives in
`GetCmd.exec` rather than in each terminal so every GET output format answers a
miss identically.

`ErrUnexpectedReply` is the opposite case and is a plain `errors.New`: the
command was accepted and the connection is fine, but the reply did not decode.
Every "unexpected …" site in `parse.go`, `stream.go`, `builders.go`, `json.go`,
`geometry.go` and `server.go` wraps it, keeping its own `%T`/`%v` detail after
the `%w`. A new decode failure wraps it too — that is what lets a caller tell a
rejection apart from a shape mismatch, which retrying will not fix.

## Architecture

**Builders** (`builders.go`, `intersects.go`, `channel.go`, `json.go`,
`management.go`). Every command is a struct holding `args []any` plus a
`*Client`. Chained methods append tokens; a terminal method (`Do`, `Count`,
`Fence`) sends them. Adding a command means: entry point in `commands.go`,
builder type next to its siblings, parse helper in `parse.go` if the reply
shape is new.

**Search builders are generic over their element type.** `NearbyCmd[E]`,
`WithinCmd[E]`, `IntersectsCmd[E]`, `ScanCmd[E]` and `SearchCmd[E]` all hold a
shared `*searchState` plus a `format[E]`, and one `Do(ctx) ([]E, error)` serves
every output format. An output-format method — `Points`, `Objects`, `Rects`,
`Hashes`, `A5Cells`, `PointsWithDistance`, `Strings`, `IDs` — returns the *same
builder at a different `E`*; a fresh command is `[string]`, which emits `IDS`.

The parameter is the **element**, not the page, and that is load-bearing: it is
what lets `Iter` be a method. A method cannot declare type parameters of its own
(`method must have no type parameters`; golang/go#49085), so the only names a
method signature can use are the receiver's. With the page as the parameter, an
element-at-a-time `iter.Seq2[E, error]` is unspellable — `undefined: E` — and
with `T any` a method cannot even range its own page (`cannot range over page:
no specific type`). Neither is a syntax problem you can wrap your way out of: a
method delegating to a generic function fails the same way, because the
constraint has to be visible at the *declaration* to be visible inside the
methods.

**The format carries the decoder.** `format[E]` is tokens, keyword, and
`decode func(prefix string, reply any) ([]E, uint64, error)` — pointed straight
at the matching `parse*` function in `parse.go`. Nothing hangs a method off `E`,
which is what makes `E = string` workable for `IDS`.

That also means there is nothing to discover at run time. An earlier design put
`decodeReply` on named slice result types and had `searchDo` find it by
asserting an unexported `results[T]`, with a `default` arm for a `T` that could
not decode. Holding the decoder in the format deletes the interface, the type
switch, the unreachable arm, and the test that pinned it: a builder whose
element type disagrees with its format cannot be constructed.

**The result types in `types.go` are aliases, not defined types.** `Points`,
`Objects`, `IDs`, … are `= []NearbyResult`, `= []SearchObject`, `= []string`.
They name what `Do` returns for each format without being distinct types, so
`Points` and `[]NearbyResult` are *identical* rather than merely assignable.
They were defined types while they carried `decodeReply`; with the decoder on
the format they no longer need methods, and aliasing keeps the public names
while letting `Do` return `[]E`.

**`Iter` pages; `Do` does not.** `Do` is one round trip and one page.
`Iter(ctx) iter.Seq2[E, error]` follows the cursor instead, yielding one item at
a time so the caller never sees a page boundary.

**Truncation is not an error.** `Do` used to return `ErrTruncated` alongside a
capped page, and it no longer does: `NextCursor` is the signal, and `Iter` is
the way to take everything. The error was added when `Iter` did not exist and it
was the only thing standing between a caller and silent partial results; once
`Iter` landed the error was a second, louder rendering of a bit the caller could
already read. It also fought the language — a non-nil error beside *valid*
results means the reflex `if err != nil { return err }` propagates a correct
100-result page as a failure — and it was inconsistent, since an explicit
`Limit(100)` that truncated stayed silent while an implicit one did not.

The footgun it guarded is real and did not go away: a query correct at 50
objects returns a prefix at 150. That is now handled by documentation on every
search verb's `Do`, which is a deliberate trade, not an oversight. Do not
reintroduce the error without re-litigating it.

`searchIter` drives `opts.cursor` on the shared `searchState` and reads
`cursorOut` between pages, restoring the caller's own cursor when the range ends
— otherwise a second `Iter` on one builder would resume mid-scan instead of
starting over. An explicit `Limit` or `Cursor` bounds it to a single page.
`TestIter` pins all of it, including that an early `break` issues no further
command.

Putting `error` in the range signature rather than offering `iter.Seq[E]` plus a
trailing `Err()` is deliberate: the second form reads better but lets a caller
forget the check.

**`Count` and `Fence` are terminals, not formats.** `Count(ctx) (int, error)`
replies with a bare integer: no element type for a builder to carry and no
cursor to record, and the hundred-result cap does not apply because the server
exempts COUNT from it. `Fence` returns a `*Stream`. Both sit outside the `Do`
path, and `searchCount` assembles COUNT in protocol order the same way `Do`
does — `TestFormatSwitch` pins that the token still lands ahead of the geometry
whichever order the options were chained in.

Without any of this, the alternative is one terminal per format per builder —
35 today, and 62 once paging is added.

The five search builders cannot share a base: a format switch would have to
return "the same `Self` at a different `E`", and Go has no higher-kinded types —
`Self[NearbyResult]` fails to typecheck. The `dupl` waiver in `.golangci.yml`
records this.

**`HookCmd` and `SetChanCmd` do share a base**, because they have no output type
parameter in the way. `fenceBase[Self]` holds a `fenceState` plus `self Self`, so
each of the twenty shared chain methods returns the *concrete* builder and
`HookCmd.Endpoint` stays reachable mid-chain. `Do` stays per-builder — SETHOOK
assembles endpoints positionally and SETCHAN does not, which is the only real
difference between them. `TestHookAndChanShareTheFenceGrammar` pins that one
chain emits one grammar on both. The cost is cosmetic: godoc renders a promoted
method's result as the unsubstituted `Self`, which is why both type docs say
outright what a chain method returns.

A format switch **clones** `searchState` rather than sharing the pointer, so two
typed handles on one chain cannot mutate each other. `TestFormatSwitch` pins
that, along with the default output, order-independence across the switch, and
last-switch-wins.

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

`HookCmd` and `SetChanCmd` are assembled the same way — both `Do`s hand their
head to `fenceBase.buildFence`, which calls `buildSearch` with
`fenceTokens(...)` — because SETHOOK and SETCHAN take a full search command
after the name, with the same geometry-last rule. `TestHookAssembly` covers it.

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
positionally, so `HookCmd` keeps `endpoints` of its own and `fenceState` keeps
`name`, `meta`, `ex` and `trigger` in separate fields, assembled in `Do`. Appending them in call
order made `.Within(…).Endpoint(…)` emit a malformed command. `TestHookAssembly`
pins it. A trailing modifier on the fence area needs its own field for the same
reason: appended to `geom`, a later `Get` silently overwrites it away.

`Detect`/`Commands` are rendered only by `Fence`, never by `Do` or `Count` —
otherwise a stray `.Detect(…)` would turn a plain query into a live one on a
pooled connection, and the caller would try to parse the `+OK` as results. `TestDetectIgnoredWithoutFence` pins that.

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

**A dropped stream is reported, not repaired.** Every read failure in
`Stream.Next` wraps `ErrDisconnected`, which is the one label a caller needs:
`io.EOF` means Close and `ctx.Err()` means cancellation, so anything else is the
connection going away. Reopening is the caller's outer loop around `Fence`, and
`ExampleStream_reconnect` in `example_test.go` is that loop — it is a compiled,
vet-checked part of the package, so keep it working.

The cause goes in with `%v` rather than a second `%w`, which is the one place
that matters: a dropped connection is usually a read that ended at EOF, and
wrapping it would make a drop satisfy `errors.Is(err, io.EOF)` — the answer
`Next` promises for `Close` alone. `TestStreamDropIsDisconnected` asserts both
halves.

**Automatic reconnect was built and then deleted; do not rebuild it without
re-litigating.** A `Stream` that re-dialled inside `Next` needed a `spent` error,
an attempt counter, a capped backoff, a `done` channel, an `atomic.Pointer` for
the swapped connection, and a second sentinel — `ErrReconnect` — whose only job
was to terminate a loop the package itself had created. That is the tell: a
feature that needs another feature to bound it. Retry policy is *application*
policy (backoff shape, attempt cap, metrics, failing over to another address),
so the library version could not stay knob-free, and the knobs were exactly the
`Reconnect(WithBackoff, WithMaxRetries)` API the design had deleted. Ninety
lines of hidden state machine replaced by one sentinel and one example.

The classification the machinery encoded is still worth knowing and is written
into the example instead: a `ServerError` from the *reopened* command is
terminal, because a malformed fence is rejected identically every time; a dial
failure or a mid-stream EOF is worth retrying.

**Reopen is not resume.** Tile38 keeps no offset for a live fence: re-sending
re-registers it and everything during the gap is gone. A caller who cannot lose
events wants `SETHOOK` with a durable endpoint. `ErrDisconnected`, `Next`, and
the example all say so, and they have to keep saying so.

`openStream` bounds the acknowledgement with `Pool.DialTimeout` and clears the
deadline only once it arrives. The ack is one ordinary command — the same thing
`Dial` already bounds AUTH with — so a server that completes the TCP handshake
and then says nothing fails instead of hanging forever. `TestStreamAckDeadline`
pins it.

**Endpoint URLs** (`endpoint/`). Tile38 parses thirteen endpoint schemes in
`internal/endpoint/endpoint.go`, each with its own path shape and its own query
parameter set, and `Manager.Validate` is parse-only — so a wrong URL surfaces as
`invalid argument` at `SETHOOK` time, or as a hook that registers and never
delivers.

The package is a sibling rather than part of the root because it is ~50 names,
and every constructor returns a plain `string` so it nests inside
`EndpointURL(urls ...string)` with no change to `HookCmd`. Nothing returns
`(string, error)`: that signature cannot nest, and the server's parser is the
only authority worth asking anyway. What the constructors do instead is make the
documented failures unreachable — required parts are positional parameters,
optional ones are option funcs (`func(url.Values)`, one type per scheme so a
Kafka option cannot reach a NATS URL), and every path segment and option value
goes through `url.QueryEscape`, which is exactly what Tile38 reverses.

Three rules are workarounds for the server rather than escaping, and
`TestEndpointQuirksStillHold` pins that each is still needed:

- **NATS demands an explicit port.** Its host switch accepts only `len(hp) == 2`,
  so `nats://host/subject` is rejected — with the message `invalid SQS url`, an
  upstream copy/paste. `NATS` appends `:4222` rather than pass that on.
- **`retained` is parsed as an integer, alone among the flags.** MQTT rejects
  `retained=true` with `invalid MQTT retained value`, so `MQTTRetained` sends
  `1`. Every other flag in every other scheme reads `true` fine — Kafka through
  `strconv.ParseBool`, NATS and AMQP through `queryBool`.
- **Only the first path segment is read** (`sp := strings.Split(sqp[0], "/")`,
  then only `sp[1]`), so an unescaped `fleet/eu/events` silently becomes
  `fleet`. MQTT is the exception and joins `sp[1:]`, but escaping the topic whole
  round-trips either way, so there is one rule.

Other shapes worth knowing: `sqs://<region>:<queueID>/<name>` and
`pubsub://<project>:<topic>` put a colon-joined *pair* in the host position, not
a host:port; `cf-queue` requires its `token` parameter, so it is positional;
EventHub is not a URL at all but four ordered semicolon parts behind an
`Endpoint=` prefix. `AMQPDurable` takes a bool because it is the one flag Tile38
defaults to true. `http`/`https` get no constructor — Tile38 takes them verbatim
— except that any `https://sqs.*.amazonaws.com` URL is read as SQS instead.

A comma-joined Kafka broker list survives `SETHOOK`'s own endpoint separator:
`splitEndpointURLs` splits on commas but rejoins any part carrying no scheme.

`TestEndpointURLs` runs every constructor through a real `SETHOOK` and reads it
back with `HOOKS`, so upstream's parser is the acceptance test; `endpoint_test.go`
covers the byte-level escaping decisions without Docker.

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
  cursor the truncation signal, which `parse.go` returns and `searchDo` records
  as `cursorOut` for `NextCursor` to report. Discarding it is how a client
  silently returns partial results once a collection grows past 100.
- **A miss is a null reply over RESP, not an error.** `cmdJget`
  (`internal/server/json.go:189`) and `cmdSET`'s `nada` (`crud.go:852`) both read
  `if msg.OutputType == JSON { return …NotFound } return resp.NullValue()`, so
  the `key not found` / `id not found` / `path not found` / `id already exists`
  strings in `token.go` are the **JSON/HTTP output mode's** and never reach this
  client. Verified against `tile38:edge`: `GET`, `JGET` and `BOUNDS` null out;
  only `FGET`, `FSET` and `RENAME` answer `-ERR`; `TTL` answers `-2`; and
  `SET … NX` over an existing object is a **silent no-op**. That is why
  `ErrPathNotFound` and `ErrIDExists` do not exist here — nothing in a RESP
  client can produce them. Read a `-ERR` string out of the server source and you
  will write a check that never fires; probe it with `Client.Do` first.
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
- `BUFFER` **panics** the server on `NEARBY` — `cmdNearby` type-asserts the area
  to `*geojson.Circle` and the buffer turns it into a `GeometryCollection`
  (`search.go:569`). That is why `Buffer` exists on `WithinCmd` and
  `IntersectsCmd` and deliberately not on `NearbyCmd`.
- `ASC`/`DESC` are accepted only by `SCAN` and `SEARCH`; the spatial verbs answer
  "DESC is not allowed for NEARBY". They are mutually exclusive and each is
  duplicate-guarded, so they share one single-use `order` slot.
- `SECTOR`, `HASH` and `QUADKEY` are areas `WITHIN`, `INTERSECTS` and the hooks
  take and `NEARBY` rejects. `TEST` takes them too — but not `A5`, which upstream
  added after the pinned digest.
- `GET … WITHFIELDS` wraps the reply in `[value, [name, val …]]`, and drops to a
  **single-element** `[value]` when the object has no non-zero fields.
- `TEST … CLIP …` answers `[result, geojson]` rather than the bare integer plain
  `TEST` returns, which is why `Clip` is its own terminal.
- `STATS` answers with one element per key asked for and a **null** element for a
  collection that does not exist, rather than an error.
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
and the stream lifecycle, without Docker. `tile38_integration_test.go` (build tag
`integration`) shares one container across the package via `TestMain`; each
test takes its own collection key from `t.Name()` and drops it on cleanup.

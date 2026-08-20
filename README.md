# tile38.go

[![Go Reference](https://pkg.go.dev/badge/github.com/GO-VIRTUAL-bv/tile38.go.svg)](https://pkg.go.dev/github.com/GO-VIRTUAL-bv/tile38.go)
[![Lint and Test](https://github.com/GO-VIRTUAL-bv/tile38.go/actions/workflows/lint-and-test.yml/badge.svg)](https://github.com/GO-VIRTUAL-bv/tile38.go/actions/workflows/lint-and-test.yml)
[![Release](https://img.shields.io/github/v/release/GO-VIRTUAL-bv/tile38.go?label=release)](https://github.com/GO-VIRTUAL-bv/tile38.go/releases)
[![License](https://img.shields.io/badge/license-MPL--2.0-blue.svg)](LICENSE)

A [Tile38](https://tile38.com) client for Go with a fluent query builder and
**live geofence streaming**. No dependencies outside the standard library.

```go
st, _ := c.Nearby("fleet").Point(33.5, -115.5).Radius(5000).
    Detect(tile38.Enter, tile38.Exit).Fence(ctx)
defer st.Close()

for {
    ev, err := st.Next()
    if err != nil {
        return err
    }
    log.Printf("%s %s", ev.Detect, ev.ID) // enter truck1
}
```

## Why

Most Go clients drive Tile38 through a Redis library, because Tile38 speaks
RESP. That works for request/response commands and stops at the interesting
part: a **live geofence** turns the connection into a one-way event stream, and
a connection-pooling Redis client cannot hold one open.

This client speaks RESP over `net.Conn` directly, so live geofences and channel
subscriptions are first-class — the same thing `tile38-cli` does when you add
`FENCE` to a query. Talking to the wire directly also means the whole library is
the standard library: no `go-redis`, no transitive tree.

## Install

```bash
go get github.com/GO-VIRTUAL-bv/tile38.go
```

The import path ends in `.go`; the package is named `tile38`:

```go
import "github.com/GO-VIRTUAL-bv/tile38.go"
```

Requires Go 1.25+ and any recent Tile38.

## Getting started

```go
c := tile38.New("localhost:9851")
defer c.Close()

if err := c.Ping(ctx); err != nil {
    return err
}

// Write a point with a field and a 60s TTL.
err := c.Set("fleet", "truck1").EX(60).Field("speed", 42).Point(33.5, -115.5).Do(ctx)

lat, lon, err := c.Get("fleet", "truck1").Point(ctx)
```

Commands are built by chaining and executed by the terminal call, which is the
one that takes a `context.Context`. Chain the parts in whatever order reads
best — they are assembled into protocol order when the command runs.

A search ends in `Do`, and an output-format method decides what `Do` gives back:
`Points()` makes it a `Points` (`[]NearbyResult`), `Objects()` an `Objects`
(`[]SearchObject`), and so on. Leave the format out and you get `IDs`
(`[]string`). Those names are aliases for the slices themselves, so a result is
an ordinary slice — range it, index it, pass it where its element type is
wanted:

```go
pts, err := c.Nearby("fleet").Limit(10).Point(33.5, -115.5).Radius(5000).Points().Do(ctx)
ids, err := c.Nearby("fleet").Where("speed > 40").Point(33.5, -115.5).Radius(5000).Do(ctx)
n, err := c.Within("fleet").Bounds(33, -116, 34, -115).Count(ctx)
objs, err := c.Intersects("fleet").Circle(33.5, -115.5, 5000).Objects().Do(ctx)
near, err := c.Nearby("fleet").Point(33.5, -115.5).Radius(5000).PointsWithDistance().Do(ctx)
trucks, err := c.Scan("fleet").Match("truck:*").Do(ctx)
```

Every search verb offers the output formats `IDs`, `Points`, `Objects`,
`Rects` (BOUNDS), `Hashes`, and `A5Cells` — plus `PointsWithDistance` on
`Nearby` and `Strings` on `Search` — and every search area
Tile38 supports: `Bounds`, `Circle`, `Sector`, `Object` (GeoJSON), `Get` (an
object already stored), `Hash`, `QuadKey`, `Tile`, and `A5`. `Nearby` takes
`Point` + `Radius` instead of an area, and `Scan` and `Search` take none.

Filters are `Where`, `WhereIn`, `WhereEval`, `WhereEvalSha`, and `Match`, which
accumulate; `Limit`, `Cursor`, `Sparse`, `NoFields`, `Clip`, and `Asc`/`Desc`
are single-use and overwrite. They chain either side of the output format:
`.Limit(10).Points()` and `.Points().Limit(10)` emit the same bytes.

`Count` and `Fence` are terminals rather than output formats, so they end a
chain in place of `Do`: `c.Within("fleet").Bounds(…).Count(ctx)` returns an
`int`, and `Fence(ctx)` returns a live `*Stream`. `Count` is exempt from the
result cap below, so it always answers for the whole collection.

`Points` and `Objects` results carry the object's `Fields` beside its geometry,
so reading a collection's state is one round trip rather than an `FGet` per
field per object. Geofence notifications carry them too:

```go
for _, p := range pts {
    log.Printf("%s at %v,%v doing %s", p.ID, p.Lat, p.Lon, p.Fields["speed"])
}
```

A point may carry a third ordinate, written with `PointZ` and read back as
`NearbyResult.Z` or through `Get(...).PointZ(ctx)`. Tile38 omits it from a reply
when it is zero, so a zero `Z` and a two-dimensional point are the same thing.

Values are Tile38's own text encoding — the decimal form of a number, the
verbatim JSON text of a JSON field — and are absent for an object whose fields
are all zero or when the query used `NoFields`. `FieldOf` decodes one without a
`strconv` call at every call site, and `MustFieldOf` panics instead of reporting
a miss, for a field you control and know is set:

```go
speed, ok := tile38.FieldOf[float64](p.Fields, "speed")
if !ok {
    continue // absent, or not a float64 — FieldOf does not distinguish
}
id := tile38.MustFieldOf[int64](p.Fields, "vehicle_id")
```

`T` is any basic numeric type, plus `bool` and `string` — every float, signed and
unsigned integer width, with `byte` and `rune` along for free. Decoding is exact,
so a value the type cannot hold is a miss rather than a truncation:
`FieldOf[int64]` rejects `"42.5"`, `FieldOf[int8]` rejects `"300"`, and the
unsigned types reject a negative.

It is `FieldOf` rather than `Field` because `Field` already builds a key/value
pair for writes. One object's fields come back
with its geometry through `Get(...).WithFields()`:

```go
g := c.Get("fleet", "truck1").WithFields()
lat, lon, err := g.Point(ctx)
speed := g.Fields()["speed"]
```

### Other commands

`Search` matches the string values `Set(...).String(...)` stores, rather than
geometry. `Test` compares two areas without touching stored objects:

```go
ok, err := c.Test(tile38.AreaGet("fleet", "truck1")).
    Within(tile38.AreaBounds(tile38.GlobalBounds())).Do(ctx)
```

Field, collection and server commands: `FGet`, `FSet`, `FExists`, `JGet`/`JSet`/
`JDel`, `Keys`, `Bounds`, `Stats`, `DBSize`, `Drop`, `PDel`, `Rename`, `Expire`,
`Persist`, `TTL`, `Exists`, `FlushDB`, `ConfigGet`/`ConfigSet`/`ConfigRewrite`,
`GC`, `Healthz`, `AOFShrink`, `ReadOnly`, `Follow`/`FollowNone`, and `Timeout`.

### Result limits

**Tile38 caps every search except `Count` at 100 results when the command
carries no `LIMIT`** ([`limitItems`][limititems]). It reports that it stopped
early by returning a non-zero cursor, which `NextCursor` hands back:

```go
scan := c.Scan("fleet")
ids, err := scan.Do(ctx)
if scan.NextCursor() != 0 {
    // ids holds the first 100; more objects match.
}
```

`Do` is one round trip and one page. A capped page is a normal reply, not an
error — so a query that is complete against a small collection quietly returns a
prefix once that collection grows past 100.

To take everything, range `Iter` instead of calling `Do`. It follows the cursor
itself and yields one result at a time, so you never see a page boundary:

```go
for id, err := range c.Scan("fleet").Iter(ctx) {
    if err != nil {
        return err
    }
    use(id)
}
```

`Iter` is on every search verb and follows the output format, so the range
variable is whatever that format yields — a `SearchObject` here:

```go
for obj, err := range c.Nearby("fleet").Point(33.5, -112.2).Radius(5000).Objects().Iter(ctx) {
    …
}
```

Breaking out of the range just stops asking for pages; each one is an ordinary
pooled round trip, so nothing is left open.

Otherwise, set an explicit `Limit` to say the cap is intended. An explicit
`Limit` or `Cursor` bounds `Iter` too: one page, not the whole collection.
`Cursor` and `NextCursor` are there for driving the paging by hand.

[limititems]: https://github.com/tidwall/tile38/blob/master/internal/server/scanner.go

### Errors

A `-ERR` reply from Tile38 comes back as a `ServerError`, which means the
command was rejected but the connection is fine. The misses worth branching on
have sentinels, so a lookup that finds nothing does not need string matching:

```go
lat, lon, err := c.Get("fleet", "truck1").Point(ctx)
switch {
case errors.Is(err, tile38.ErrIDNotFound):
    // no such object; normal control flow, not a failure
case err != nil:
    return err
}
```

`ErrKeyNotFound` and `ErrIDNotFound` carry the server's own wire text, so
`errors.Is` matches them through every wrap.

Tile38 spells a miss three different ways over RESP, and the client normalises
all three onto those two values so one check covers them:

| Spelling | Commands |
| --- | --- |
| `-ERR key not found` / `-ERR id not found` | `Rename`, `FGet`, `FSet` |
| a null reply | `Get`, `JGet`, `Bounds` |
| a magic integer (`-2`) | `TTL` |

The null case is the one to know about: the `-ERR` strings you will find in
Tile38's source for `GET` and `JGET` belong to its **JSON/HTTP output mode**, and
never reach a RESP client. A null cannot say *which* of the two went missing, so
`Get` and `JGet` report `ErrIDNotFound` for a missing collection as well.

For the same reason `Set(...).NX()` over an existing object is a silent no-op
over RESP rather than an error — check with `Exists` if you need to know.

`ErrUnexpectedReply` is the other side: the command was accepted, but the reply
did not decode into the shape the command should produce. It is worth telling
apart from a `ServerError`, because retrying will not help. `ErrClosed` reports
a command issued on a closed `Client`.

## Live geofences

Adding `Fence` to a search opens a stream instead of returning results. The
`Stream` owns its own connection and has no read timeout, so a quiet fence can
sit idle for hours.

```go
st, err := c.Within("zones").Bounds(-90, -180, 90, 180).
    Detect(tile38.Enter, tile38.Exit).
    Commands(tile38.CommandSet).
    Fence(ctx)
if err != nil {
    return err
}
defer st.Close()

for {
    ev, err := st.Next() // io.EOF after Close, ctx.Err() on cancel
    if err != nil {
        return err
    }
    fmt.Println(ev.Detect, ev.ID, string(ev.Object))
}
```

`Detect` takes `Inside`, `Outside`, `Enter`, `Exit`, `Cross`; `Commands`
filters by what caused the event (`CommandSet`, `CommandDel`, `CommandDrop`,
`CommandFSet`, …). Both are named string types, so a typo is a compile error
rather than a server error. `Fence` is available on `Nearby`, `Within`, and
`Intersects`.

A `Nearby` fence can also roam — firing as objects come within range of another
collection — which Tile38 allows only on a live fence:

```go
st, err := c.Nearby("fleet").Roam("targets", 250).NoDwell().Fence(ctx)
```

### Surviving a dropped connection

A read failure wraps `ErrDisconnected` — `io.EOF` means `Close` and `ctx.Err()`
means cancellation, so everything else is the connection going away. The package
does not reopen the stream for you: backoff, an attempt cap, and whether to fail
over elsewhere are your policy, not the library's. The loop is short, and the
builder is reusable, so every attempt sends identical bytes:

```go
fence := c.Within("fleet").Circle(51.05, 3.72, 500).Detect(tile38.Enter, tile38.Exit)

for delay := time.Duration(0); ; {
    if delay > 0 {
        select {
        case <-time.After(delay):
        case <-ctx.Done():
            return ctx.Err()
        }
    }

    st, err := fence.Fence(ctx)
    if err != nil {
        var rejected tile38.ServerError
        if errors.As(err, &rejected) {
            return err // a malformed fence is rejected identically every time
        }
        delay = min(max(2*delay, 100*time.Millisecond), 30*time.Second)
        continue
    }
    delay = 0

    for {
        ev, err := st.Next()
        if err != nil {
            _ = st.Close()
            if !errors.Is(err, tile38.ErrDisconnected) {
                return err // Close, or the context ended
            }
            break // reopen
        }
        handle(ev)
    }
}
```

`ExampleStream_reconnect` in the package docs is the same loop, compiled.

**Reopening is not resuming.** Tile38 keeps no offset for a live fence, so every
event that happened while the connection was down is gone. If you cannot lose
events, register the fence with `SETHOOK` against a durable endpoint (Kafka,
AMQP, SQS) instead.

## Channels and hooks

A geofence can also be registered on the server and delivered to subscribers
(`SETCHAN`) or pushed to an endpoint (`SETHOOK`).

```go
// Server-side fence, delivered to subscribers.
err := c.SetChan("zone1").Within("fleet").
    Detect(tile38.Inside).
    Bounds(tile38.GlobalBounds()).
    Do(ctx)

sub, err := c.Subscribe(ctx, "zone1") // or c.PSubscribe(ctx, "zone*")
defer sub.Close()

ev, err := sub.Next() // same *FenceEvent as a live fence
```

```go
// Server-side fence, pushed to an endpoint.
err := c.SetHook("alerts").Endpoint("http://example.com", "events").
    Within("fleet").
    Detect(tile38.Enter, tile38.Exit).
    Meta("team", "ops").
    Circle(33.5, -115.5, 5000).
    Do(ctx)

hooks, err := c.Hooks("*").Do(ctx)
```

`Endpoint` joins a base URL and a subject with `/`. For schemes that do not fit
that shape, or to register several endpoints on one hook, use `EndpointURL`:

```go
c.SetHook("alerts").EndpointURL("kafka://k:9092/events", "http://x/y?token=1")
```

Tile38 parses thirteen endpoint schemes, each with its own path shape and query
parameters, and reports a mistake only as an `invalid argument` at `SETHOOK`
time. The `endpoint` package builds those URLs instead — required parts are
positional, options are named after the parameter they set, and values are
escaped:

```go
import "github.com/GO-VIRTUAL-bv/tile38.go/endpoint"

c.SetHook("alerts").
    EndpointURL(
        endpoint.NATS("10.0.0.1:4222", "fleet.events", endpoint.NATSJetstream()),
        endpoint.Kafka([]string{"k1:9092", "k2:9092"}, "fleet-events", endpoint.KafkaSSL()),
    ).
    Within("fleet").Circle(51.05, 3.72, 500).Do(ctx)
```

Each constructor returns a plain `string`, so it nests straight into
`EndpointURL`. `http://` and `https://` need no helper — Tile38 takes those
verbatim.

Hooks and channels trigger on `Nearby`, `Within`, or `Intersects`, take the same
fence areas as a search — `Bounds`, `Circle`, `Object`, `Get`, `A5` — plus
`Roam`, and accept `Meta` and `EX`. `GlobalBounds()` returns the whole-world box
as four values, which Go binds straight onto `Bounds`' parameters.

A roaming fence reports objects that stay in range on every update. Chain
`NoDwell` to suppress those — on hooks, channels, and live `Nearby` fences
alike:

```go
err := c.SetChan("proximity").Nearby("fleet").NoDwell().Roam("targets", 250).Do(ctx)
```

`Hooks` and `Chans` both return `[]HookInfo` — name, watched collection,
endpoints, and the fence command the hook was created with.

## Pipelining

Batch writes into a single round trip:

```go
p := c.Pipeline()
for _, t := range trucks {
    p.Set("fleet", t.ID).EX(300).Field("speed", t.Speed).Point(t.Lat, t.Lon).Queue()
}
err := p.Flush(ctx)
```

## Configuration

The address is required; everything else is an option.

```go
c := tile38.New("localhost:9851",
    tile38.WithPassword("secret"),            // AUTH on each new connection
    tile38.WithMaxIdle(16),                   // idle connections kept for reuse
    tile38.WithMaxActive(64),                 // commands in flight at once
    tile38.WithDialTimeout(5*time.Second),
    tile38.WithTimeout(2*time.Second),        // per-command deadline
)
```

Commands take a connection from an idle pool. A rejected command returns a
`ServerError` and the connection stays in the pool; a transport failure drops
it. Streams always get their own connection and are not counted against
`WithMaxActive`, since they hold it for as long as they run.

`WithMaxIdle` bounds only the connections kept for reuse, so without
`WithMaxActive` a burst of concurrent commands opens a socket per goroutine.
`WithTimeout` defaults to `DefaultTimeout` (30s) so a command against a wedged
server cannot hang forever on a context with no deadline; pass a negative
duration to rely on the context alone.

For anything this library does not model:

```go
v, err := c.Do(ctx, "SERVER") // string, int64, []any, or nil
```

## Testing

```bash
make test              # unit tests against a scripted server, no Docker
make test-integration  # against a real Tile38 in Docker via testcontainers
make lint
```

Integration tests are behind the `integration` build tag, so `go get` of this
library never pulls testcontainers into your build.

## Contributing

`make lint` needs [`golangci-lint`](https://golangci-lint.run) v2:

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
```

`.claude/settings.json` is checked in, so [Claude Code](https://claude.com/claude-code)
picks up two repo hooks automatically: Go files are formatted on edit, and a new
direct dependency in `go.mod` is refused — this client is deliberately
dependency-free. The format hook shells out to `golangci-lint` so it agrees with
`make lint` rather than approximating it, which means it fails on every edit if
the binary is not on your `PATH`. Install it first, or drop `.claude/settings.json`
from your working copy.

## Notes

This client targets upstream Tile38 only. A command upstream does not accept is
not exposed here — it goes upstream first, and lands here once accepted.

One nuance: `Within(…).A5`, `Intersects(…).A5`, and `Get(…).A5` are merged into
upstream Tile38 but have shipped in no release tag as of `1.38.0`, so they need a
server built from upstream master. That is why `.version` pins a
`tile38/tile38:edge` digest rather than a release tag.

## Claude Code skill

This repo ships a [Claude Code](https://claude.com/claude-code) skill that teaches
the agent to use this client. Install it as a plugin:

```bash
/plugin marketplace add GO-VIRTUAL-bv/tile38.go
/plugin install tile38@go-virtual
```

Or with the [skills](https://skills.sh) CLI:

```bash
npx skills add GO-VIRTUAL-bv/tile38.go@tile38
```

The skill lives at [.claude/skills/tile38](.claude/skills/tile38) (`SKILL.md` plus
a `reference.md` command catalog); you can also copy that folder into any
`~/.claude/skills/` directly.

## License

Mozilla Public License 2.0 — see [LICENSE](LICENSE).

MPL-2.0 is file-level copyleft: you can import this client into proprietary
software freely. If you modify one of its files and distribute the result, you
have to make that file's source available to whoever you distributed it to.
Files you add alongside it are yours.

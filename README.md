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
best — they are assembled into protocol order when the command runs:

```go
pts, err := c.Nearby("fleet").Limit(10).Point(33.5, -115.5).Radius(5000).Points(ctx)
ids, err := c.Nearby("fleet").Where("speed > 40").Point(33.5, -115.5).Radius(5000).IDs(ctx)
n, err := c.Within("fleet").Bounds(33, -116, 34, -115).Count(ctx)
objs, err := c.Intersects("fleet").Circle(33.5, -115.5, 5000).Objects(ctx)
near, err := c.Nearby("fleet").Point(33.5, -115.5).Radius(5000).PointsWithDistance(ctx)
trucks, err := c.Scan("fleet").Match("truck:*").IDs(ctx)
```

Every search verb offers the four output formats — `IDs`, `Points`, `Count`,
`Objects` — and every search area Tile38 supports: `Bounds`, `Circle`,
`Object` (GeoJSON), `Get` (an object already stored), `Tile`, and `A5`.

Filters are `Where`, `WhereIn`, and `Match`, which accumulate; `Limit`,
`Cursor`, `Sparse`, `NoFields`, and `Clip` are single-use and overwrite.

`Points` and `Objects` results carry the object's `Fields` beside its geometry,
so reading a collection's state is one round trip rather than an `FGet` per
field per object. Geofence notifications carry them too:

```go
for _, p := range pts {
    log.Printf("%s at %v,%v doing %s", p.ID, p.Lat, p.Lon, p.Fields["speed"])
}
```

Values are Tile38's own text encoding — the decimal form of a number, the
verbatim JSON text of a JSON field — and are absent for an object whose fields
are all zero or when the query used `NoFields`.

### Result limits

**Tile38 caps every search except `Count` at 100 results when the command
carries no `LIMIT`.** It reports that it stopped early by returning a non-zero
cursor, which this client surfaces as `ErrTruncated`:

```go
ids, err := c.Scan("fleet").IDs(ctx)
if errors.Is(err, ErrTruncated) {
    // ids holds the first 100; more objects match.
}
```

The results returned alongside the error are valid, just incomplete. Either page
through the rest, or set an explicit `Limit` to say the cap is intended — an
explicit `Limit` or `Cursor` silences the error, since then the bound is yours.

```go
cmd := c.Scan("fleet")
for {
    ids, err := cmd.IDs(ctx)
    if err != nil && !errors.Is(err, tile38.ErrTruncated) {
        return err
    }
    use(ids)
    if !errors.Is(err, tile38.ErrTruncated) {
        break
    }
    cmd = c.Scan("fleet").Cursor(cmd.NextCursor())
}
```

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

---
name: tile38
description: Use the tile38.go Go client (github.com/GO-VIRTUAL-bv/tile38.go) to talk to a Tile38 geospatial database — connect, SET/GET objects, run NEARBY/WITHIN/INTERSECTS/SCAN searches, and stream live geofences, channels, and hooks. Trigger when writing Go that stores or queries geospatial points, needs geofence/proximity events, or when the user mentions Tile38, geofence, NEARBY/WITHIN/INTERSECTS, or this client.
---

# tile38.go

A dependency-free Go client for [Tile38](https://tile38.com). It speaks RESP over
`net.Conn` directly, so **live geofences** (a connection that streams events) are
first-class — something a Redis-library client cannot do.

**Full command catalog, options, channels/hooks, pipelining, and every gotcha:
[reference.md](reference.md).** This file is the fast path.

## Install

```bash
go get github.com/GO-VIRTUAL-bv/tile38.go
```

Import path ends in `.go`; package is named `tile38`. Requires Go 1.25+.

```go
import "github.com/GO-VIRTUAL-bv/tile38.go"
```

## Connect

```go
c := tile38.New("localhost:9851", tile38.WithTimeout(2*time.Second))
defer c.Close()
if err := c.Ping(ctx); err != nil { return err }
```

Options: `WithPassword`, `WithMaxIdle`, `WithMaxActive`, `WithDialTimeout`,
`WithTimeout`. `Client` is safe for concurrent use; nothing connects until the
first command. See [reference.md](reference.md#configuration-options) for defaults.

## Core rule: chain, then call the terminal

Every command is a builder. Chain the parts **in any order** — they assemble into
protocol order at exec time. The terminal method is the one taking a `context`.

```go
// Write a point with a field and a 60s TTL.
err := c.Set("fleet", "truck1").EX(60).Field("speed", 42).Point(33.5, -115.5).Do(ctx)

// Read it back.
lat, lon, err := c.Get("fleet", "truck1").Point(ctx)
```

Method names mirror Tile38 keywords in Go casing (`Point`, `EX`, `Where`).

## Searches

Verbs `Nearby`, `Within`, `Intersects`, `Scan`. A search ends in `Do(ctx)`, and
an output format — `IDs`, `Points`, `Objects`, `Rects`, `Hashes`, `A5Cells` —
decides what `Do` returns; omit it for IDs. `Count` and `Fence` are separate
terminals. Areas `Bounds`, `Circle`, `Object`, `Get`, `Tile`, `A5` (`Nearby`
uses `Point().Radius()`). Filters `Where`/`WhereIn`/`Match` accumulate;
`Limit`/`Cursor`/`Sparse`/`NoFields`/`Clip` overwrite.

```go
ids, err := c.Nearby("fleet").Where("speed > 40").Point(33.5, -115.5).Radius(5000).Do(ctx)
pts, err := c.Nearby("fleet").Point(33.5, -115.5).Radius(5000).Points().Do(ctx)
n, err   := c.Within("fleet").Bounds(33, -116, 34, -115).Count(ctx)
```

### ErrTruncated — the #1 gotcha

**Tile38 caps every search except `Count` at 100 results when no `Limit` is set.**
The client surfaces this as `tile38.ErrTruncated`; returned results are valid but
partial. Set an explicit `Limit` (or `Cursor`) to silence it, or page with
`Cursor`/`NextCursor`.

```go
ids, err := c.Scan("fleet").Do(ctx)
if errors.Is(err, tile38.ErrTruncated) {
    // ids holds the first 100; more match.
}
```

## Live geofences

Adding `Fence` opens a `*Stream` instead of returning results. It owns its own
connection, has no read timeout, and must be `Close`d.

```go
st, err := c.Within("zones").Bounds(-90, -180, 90, 180).
    Detect(tile38.Enter, tile38.Exit).Fence(ctx)
if err != nil { return err }
defer st.Close()

for {
    ev, err := st.Next() // io.EOF after Close, ctx.Err() on cancel
    if err != nil { return err }
    fmt.Println(ev.Detect, ev.ID, string(ev.Object))
}
```

`Detect`: `Inside`, `Outside`, `Enter`, `Exit`, `Cross`. A `Nearby` fence can
`Roam("targets", 250)`. Server-side fences (`SetChan`/`SetHook`), pipelining, and
management commands are all in [reference.md](reference.md).

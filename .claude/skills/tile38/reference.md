# tile38.go — full command reference

Companion to [SKILL.md](SKILL.md). The essentials (install, connect, chaining
rule, `ErrTruncated`, a live-fence example) live there; this file is the complete
command catalog and every option.

## Configuration options

Passed to `tile38.New(addr, ...opts)`:

| Option | Effect | Default |
| --- | --- | --- |
| `WithPassword(s)` | AUTH on each new connection | none |
| `WithMaxIdle(n)` | idle connections kept for reuse | 8 |
| `WithMaxActive(n)` | commands in flight at once (0 = uncapped) | 0 |
| `WithDialTimeout(d)` | cap on opening a connection | 5s |
| `WithTimeout(d)` | per-command deadline; negative opts out, relying on ctx | 30s |

`New` opens nothing until the first command. `Client` is safe for concurrent use.
`WithMaxIdle` bounds only reused connections — without `WithMaxActive`, a burst of
concurrent commands opens a socket per goroutine. `WithTimeout` and `WithMaxActive`
never apply to streams.

## Writing objects

```go
c.Set("fleet", "truck1").EX(60).Field("speed", 42).Point(33.5, -115.5).Do(ctx)
c.Set("fleet", "truck1").Object(geojson).Do(ctx)   // GeoJSON geometry
c.Set("fleet", "truck1").Bounds(33, -116, 34, -115).Do(ctx)
```

`Set` geometry terminals: `Point(lat, lon)`, `Point(...).Radius(m)` for a circle
point, `Bounds`, `Object` (GeoJSON), `Tile`, `A5`, `Hash`, `String(s)` (a
string-valued object). `EX(seconds)` sets a TTL; `Field(k, v)` attaches numeric
fields; `NX`/`XX` gate on existence.

## Search verbs & terminals

Verbs: `Nearby`, `Within`, `Intersects`, `Scan`.

A search ends in `Do(ctx)`. An output-format method decides what `Do` returns —
it is chained, not a terminal, and may be called either side of the options:

| Format | `Do` returns | underlying |
| --- | --- | --- |
| *(none)* | `IDs` | `[]string` — IDS is the default |
| `IDs()` | `IDs` | `[]string` |
| `Points()` | `Points` | `[]NearbyResult` (lat/lon + fields) |
| `PointsWithDistance()` | `PointsWithDistance` | `[]NearbyResultWithDistance` (Nearby only) |
| `Objects()` | `Objects` | `[]SearchObject` (raw GeoJSON) |
| `Rects()` | `Rects` | `[]RectResult` (BOUNDS output) |
| `Hashes(precision)` | `Hashes` | `[]HashResult` |
| `A5Cells(level)` | `A5Cells` | `[]A5Result` |
| `Strings()` | `Strings` | `[]StringObject` (Search only) |
| `Count()` | `int` | bare integer; exempt from the 100 cap, never `ErrTruncated` |

Each result type is a named slice, so it behaves as its underlying slice —
range, index, `len`. Note `reflect.DeepEqual` compares types, so a test wanting
`Points{…}` will not match `[]NearbyResult{…}`.

One terminal sits outside that path, because it returns no value at all:

| Terminal | Returns |
| --- | --- |
| `Fence(ctx)` | opens a `*Stream` (live geofence) |

### Search areas

`Bounds(swLat, swLon, neLat, neLon)`, `Circle(lat, lon, meters)`,
`Object(geojson)`, `Get(collection, id)` (reuse a stored object as the area),
`Tile(x, y, z)`, `A5(cell)`. `Nearby` uses `Point(lat, lon).Radius(meters)`.

### Filters (accumulate — call repeatedly)

- `Where("speed > 40")` — field predicate
- `WhereIn("type", "a", "b")` — membership
- `Match("truck:*")` — glob on ID

### Single-use options (calling twice overwrites)

- `Limit(n)` — also silences `ErrTruncated`
- `Cursor(n)` — resume offset; pair with `NextCursor()`
- `Sparse(n)` — spread results (not on `Scan`; server rejects it)
- `NoFields()` — omit field data
- `Clip()` — clip objects to the search area

```go
pts, err  := c.Nearby("fleet").Limit(10).Point(33.5, -115.5).Radius(5000).Points().Do(ctx)
ids, err  := c.Nearby("fleet").Where("speed > 40").Point(33.5, -115.5).Radius(5000).Do(ctx)
n, err    := c.Within("fleet").Bounds(33, -116, 34, -115).Count().Do(ctx)
objs, err := c.Intersects("fleet").Circle(33.5, -115.5, 5000).Objects().Do(ctx)
near, err := c.Nearby("fleet").Point(33.5, -115.5).Radius(5000).PointsWithDistance().Do(ctx)
trucks, err := c.Scan("fleet").Match("truck:*").Do(ctx)
```

Chain order is irrelevant — parts assemble into protocol order at exec time,
and that includes the output format: `.Limit(10).Points()` and
`.Points().Limit(10)` emit the same bytes.

## Live geofences

`Fence` opens a `*Stream` instead of returning results. It owns its own
connection, ignores `WithTimeout`/`WithMaxActive`, and has no read deadline — a
quiet fence idles for hours. Always `Close`.

```go
st, err := c.Within("zones").Bounds(-90, -180, 90, 180).
    Detect(tile38.Enter, tile38.Exit).
    Commands(tile38.CommandSet).
    Fence(ctx)
if err != nil { return err }
defer st.Close()

for {
    ev, err := st.Next() // io.EOF after Close, ctx.Err() on cancel
    if err != nil { return err }
    fmt.Println(ev.Detect, ev.ID, string(ev.Object))
}
```

`Detect` values: `Inside`, `Outside`, `Enter`, `Exit`, `Cross`.
`Commands` values: `CommandSet`, `CommandDel`, `CommandDrop`, `CommandFSet`, …
Both are named string types (typo = compile error). `Fence` is on `Nearby`,
`Within`, `Intersects`.

### Roaming fence (Nearby only)

Fires as objects move near another collection. `NoDwell()` suppresses repeated
reports for objects that stay in range (Tile38 reports dwelling by default).

```go
st, err := c.Nearby("fleet").Roam("targets", 250).NoDwell().Fence(ctx)
```

## Channels & hooks

Register a fence on the server: delivered to subscribers (`SETCHAN`) or pushed to
an endpoint (`SETHOOK`). Both trigger on `Nearby`/`Within`/`Intersects`, take the
same areas plus `Roam`, and accept `Meta(k, v)` and `EX(seconds)`.

```go
// Subscriber channel.
err := c.SetChan("zone1").Within("fleet").Detect(tile38.Inside).
    Bounds(tile38.GlobalBounds()).Do(ctx)

sub, err := c.Subscribe(ctx, "zone1")   // or c.PSubscribe(ctx, "zone*")
defer sub.Close()
ev, err := sub.Next()                    // same *FenceEvent as a live fence

// Webhook.
err = c.SetHook("alerts").Endpoint("http://example.com", "events").
    Within("fleet").Detect(tile38.Enter, tile38.Exit).
    Meta("team", "ops").Circle(33.5, -115.5, 5000).Do(ctx)
```

- `GlobalBounds()` returns the whole-world box as four values that bind straight
  onto `Bounds(swLat, swLon, neLat, neLon)`.
- `Endpoint(base, subject)` joins with `/`. For non-URL schemes or multiple
  endpoints on one hook, use `EndpointURL(url1, url2, ...)`.
- List with `c.Hooks("*").Do(ctx)` / `c.Chans("*").Do(ctx)` → `[]HookInfo`
  (name, watched collection, endpoints, fence command).
- Remove with `DelHook`/`PDelHook`, `DelChan`/`PDelChan`.

## Pipelining

Batch writes into one round trip — `Queue` in place of a terminal, then `Flush`.

```go
p := c.Pipeline()
for _, t := range trucks {
    p.Set("fleet", t.ID).EX(300).Field("speed", t.Speed).Point(t.Lat, t.Lon).Queue()
}
err := p.Flush(ctx)
```

## Reads, management & JSON

- Read: `Get(coll, id).Point(ctx)` / `.Bounds(ctx)` / `.Object(ctx)`,
  `FGet(coll, id, field)`, `Exists`, `TTL`.
- Lifecycle: `Del`, `Drop`, `PDel(coll, pattern)`, `Rename(a, b)` (`.NX()` →
  RENAMENX), `Expire`/`Persist`, `FlushDB`.
- Introspection: `Keys("*")`, `Bounds(coll)`, `DBSize()` (reads `num_objects`
  from SERVER — Tile38 has no DBSIZE command).
- JSON fields: `JSet(coll, id, path, value)`, `JGet(coll, id, path)`,
  `JDel(coll, id, path)`.
- Fields: `FSet(coll, id).Field(k, v)`.

## Escape hatch

For unmodeled commands. Replies decode to `string`, `int64`, `[]any`, or `nil`.

```go
v, err := c.Do(ctx, "SERVER")
```

## Errors

- `tile38.ServerError` — a `-ERR` reply; command rejected, connection healthy.
- `tile38.ErrClosed` — issued after `Close`.
- `tile38.ErrTruncated` — search returned partial results (see SKILL.md).

Use `errors.As` / `errors.Is`.

## Gotchas

- **Truncation is silent without the check** — handle `ErrTruncated` on searches
  or set an explicit `Limit`. Tile38 caps every search except `Count` at 100 with
  no `Limit`.
- **`Count` returns a bare integer** and is exempt from the cap.
- **Streams aren't pooled** and ignore `WithMaxActive`/`WithTimeout`; they hold a
  dedicated connection until `Close`.
- **`NoDwell` is opt-in** — default is to report dwelling objects every roam update.
- **`Sparse` is unavailable on `Scan`**; **`Cursor` is dropped on the fence path**
  — Tile38 rejects both combinations.
- **Upstream Tile38 only** — every command here is one stock Tile38 accepts.
- **A5 is a search area only** (`Within`/`Intersects`/`Get`), not a hook or
  channel fence area — Tile38 rejects it there. It also needs a server built from
  upstream master; A5 is in no release tag as of `1.38.0`.

// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package tile38

import (
	"context"
	"iter"
)

// IntersectsCmd builds a Tile38 INTERSECTS query.
// INTERSECTS finds objects whose geometry intersects the search area,
// whereas WITHIN requires full containment.
// Methods may be chained in any order; the parts are assembled into protocol
// order when the command runs.
//
// The type parameter is the element type Do returns a slice of; see NearbyCmd.
type IntersectsCmd[E any] struct {
	*searchState
	out format[E]
}

// Limit caps the number of results. Zero means no limit.
func (cmd *IntersectsCmd[E]) Limit(n int) *IntersectsCmd[E] {
	cmd.opts.limit = &n
	return cmd
}

// Cursor resumes a search from where a previous one stopped, matching Tile38's
// CURSOR keyword. Pass the value NextCursor reported. Tile38 rejects CURSOR on a
// fence, so Fence ignores it.
func (cmd *IntersectsCmd[E]) Cursor(n uint64) *IntersectsCmd[E] {
	cmd.opts.cursor = &n
	return cmd
}

// NextCursor reports where to resume after the last executed terminal. It is
// non-zero only when Tile38 stopped at the limit with more objects matching.
func (cmd *IntersectsCmd[E]) NextCursor() uint64 { return cmd.cursorOut }

// Where sets an optional Tile38 field expression filter.
func (cmd *IntersectsCmd[E]) Where(expr string) *IntersectsCmd[E] {
	cmd.args = append(cmd.args, "WHERE", expr)
	return cmd
}

// Match filters results by ID pattern (glob-style, e.g. "truck:*").
func (cmd *IntersectsCmd[E]) Match(pattern string) *IntersectsCmd[E] {
	cmd.args = append(cmd.args, "MATCH", pattern)
	return cmd
}

// WhereIn keeps results whose field holds one of the given values, matching
// Tile38's WHEREIN keyword. It accumulates: each call adds another filter.
func (cmd *IntersectsCmd[E]) WhereIn(field string, values ...any) *IntersectsCmd[E] {
	cmd.args = append(cmd.args, whereInTokens(field, values)...)
	return cmd
}

// NoFields drops field values from the reply, matching Tile38's NOFIELDS keyword.
func (cmd *IntersectsCmd[E]) NoFields() *IntersectsCmd[E] {
	cmd.opts.nofields = true
	return cmd
}

// Clip trims returned objects to the search area rather than returning them
// whole, matching Tile38's CLIP keyword.
func (cmd *IntersectsCmd[E]) Clip() *IntersectsCmd[E] {
	cmd.opts.clip = true
	return cmd
}

// Sparse spreads results evenly over the search area at the given depth (1-8),
// matching Tile38's SPARSE keyword. Tile38 rejects SPARSE combined with Limit.
func (cmd *IntersectsCmd[E]) Sparse(depth int) *IntersectsCmd[E] {
	cmd.opts.sparse = &depth
	return cmd
}

// Detect restricts a live fence to the given transitions. Only meaningful with Fence.
func (cmd *IntersectsCmd[E]) Detect(states ...DetectState) *IntersectsCmd[E] {
	cmd.detect = states
	return cmd
}

// Commands restricts a live fence to events caused by the given commands.
// Only meaningful with Fence.
func (cmd *IntersectsCmd[E]) Commands(commands ...Command) *IntersectsCmd[E] {
	cmd.commands = commands
	return cmd
}

// Distance adds each object's distance from the fence centre to every event the
// fence produces, matching Tile38's DISTANCE keyword. It arrives on FenceEvent
// as Distance, and applies to the live fence only — a plain query reads the same
// value through PointsWithDistance.
func (cmd *IntersectsCmd[E]) Distance() *IntersectsCmd[E] {
	cmd.distance = true
	return cmd
}

// WhereEval keeps results for which the given Lua script returns true, matching
// Tile38's WHEREEVAL keyword. The script sees the object's fields as FIELDS and
// the extra arguments as ARGV. It accumulates: each call adds another filter.
func (cmd *IntersectsCmd[E]) WhereEval(script string, args ...any) *IntersectsCmd[E] {
	cmd.args = append(cmd.args, countedTokens("WHEREEVAL", script, args)...)
	return cmd
}

// WhereEvalSha is WhereEval against a script already loaded on the server,
// matching Tile38's WHEREEVALSHA keyword.
func (cmd *IntersectsCmd[E]) WhereEvalSha(sha string, args ...any) *IntersectsCmd[E] {
	cmd.args = append(cmd.args, countedTokens("WHEREEVALSHA", sha, args)...)
	return cmd
}

// Buffer grows the search area by the given number of metres before matching,
// matching Tile38's BUFFER keyword. Tile38 can only buffer point-like areas — it
// answers "cannot buffer Polygon type" for a Bounds or polygon Object area, and
// it panics rather than answering on NEARBY, which is why NearbyCmd has no
// Buffer.
//
// It is appended rather than stored: Tile38 has no duplicate guard for BUFFER,
// so a repeat is legal and the last one wins.
func (cmd *IntersectsCmd[E]) Buffer(metres int) *IntersectsCmd[E] {
	cmd.args = append(cmd.args, "BUFFER", metres)
	return cmd
}

// Get sets the search area to an object already stored in Tile38 (GET keyword).
func (cmd *IntersectsCmd[E]) Get(collection, id string) *IntersectsCmd[E] {
	cmd.geom = []any{"GET", collection, id}
	return cmd
}

// Object sets the search area to an inline GeoJSON string (OBJECT keyword).
func (cmd *IntersectsCmd[E]) Object(geojson string) *IntersectsCmd[E] {
	cmd.geom = []any{"OBJECT", geojson}
	return cmd
}

// Bounds sets the search area to a lat/lon bounding box (BOUNDS keyword).
func (cmd *IntersectsCmd[E]) Bounds(swLat, swLon, neLat, neLon float64) *IntersectsCmd[E] {
	cmd.geom = []any{"BOUNDS", swLat, swLon, neLat, neLon}
	return cmd
}

// Circle sets the search area to a circle with centre + radius in metres (CIRCLE keyword).
func (cmd *IntersectsCmd[E]) Circle(lat, lon float64, radius int) *IntersectsCmd[E] {
	cmd.geom = []any{"CIRCLE", lat, lon, radius}
	return cmd
}

// A5 sets the search area to a single A5 cell's pentagon, identified by its hex
// cell id (A5 keyword). Requires a server built from upstream master: A5 is
// merged upstream but has shipped in no release tag as of 1.38.0. Tile38 accepts
// A5 as a search area only, not as a hook or channel fence area.
func (cmd *IntersectsCmd[E]) A5(cellID string) *IntersectsCmd[E] {
	cmd.geom = []any{"A5", cellID}
	return cmd
}

// Sector sets the search area to a circular sector: a circle of radius metres
// centred on lat/lon, clipped to the arc between two compass bearings in
// degrees. Matches Tile38's SECTOR keyword, which NEARBY does not accept.
func (cmd *IntersectsCmd[E]) Sector(lat, lon float64, metres int, bearing1, bearing2 float64) *IntersectsCmd[E] {
	cmd.geom = []any{"SECTOR", lat, lon, metres, bearing1, bearing2}
	return cmd
}

// Hash sets the search area to the box a geohash covers, matching Tile38's HASH
// keyword. The shorter the hash, the larger the box.
func (cmd *IntersectsCmd[E]) Hash(geohash string) *IntersectsCmd[E] {
	cmd.geom = []any{"HASH", geohash}
	return cmd
}

// QuadKey sets the search area to the tile a Bing Maps quadkey names, matching
// Tile38's QUADKEY keyword. Tile is the same area expressed as x/y/z.
func (cmd *IntersectsCmd[E]) QuadKey(quadkey string) *IntersectsCmd[E] {
	cmd.geom = []any{"QUADKEY", quadkey}
	return cmd
}

// Tile sets the search area to a single XYZ map tile (TILE keyword).
func (cmd *IntersectsCmd[E]) Tile(x, y, z int) *IntersectsCmd[E] {
	cmd.geom = []any{"TILE", x, y, z}
	return cmd
}

// IDs selects the IDS output format: INTERSECTS collection [opts] IDS <area>.
// It is what a fresh command already emits, and is here to switch back.
func (cmd *IntersectsCmd[E]) IDs() *IntersectsCmd[string] {
	return &IntersectsCmd[string]{cmd.clone(), formatIDs}
}

// Points selects the POINTS output format: INTERSECTS collection [opts] POINTS <area>.
func (cmd *IntersectsCmd[E]) Points() *IntersectsCmd[NearbyResult] {
	return &IntersectsCmd[NearbyResult]{cmd.clone(), formatPoints}
}

// Objects selects the OBJECTS output format: INTERSECTS collection [opts] OBJECTS <area>.
func (cmd *IntersectsCmd[E]) Objects() *IntersectsCmd[SearchObject] {
	return &IntersectsCmd[SearchObject]{cmd.clone(), formatObjects}
}

// Rects selects the BOUNDS output format: INTERSECTS collection [opts] BOUNDS <area>.
// Each result is the bounding box of a matching object, lat first.
func (cmd *IntersectsCmd[E]) Rects() *IntersectsCmd[RectResult] {
	return &IntersectsCmd[RectResult]{cmd.clone(), formatRects}
}

// Hashes selects the HASHES output format: INTERSECTS collection [opts] HASHES precision <area>.
// Each result is the geohash of a matching object's centre.
func (cmd *IntersectsCmd[E]) Hashes(precision int) *IntersectsCmd[HashResult] {
	return &IntersectsCmd[HashResult]{cmd.clone(), formatHashes(precision)}
}

// A5Cells selects the A5 output format: INTERSECTS collection [opts] A5 level <area>.
// Requires a server built from upstream master.
func (cmd *IntersectsCmd[E]) A5Cells(level int) *IntersectsCmd[A5Result] {
	return &IntersectsCmd[A5Result]{cmd.clone(), formatA5Cells(level)}
}

// Do executes the command in whichever output format was selected, defaulting
// to IDS. It is one round trip and returns one page.
//
// Tile38 caps every output except COUNT at 100 results when the command carries
// no LIMIT (limitItems, internal/server/scanner.go), so a query that is complete
// against a small collection quietly returns a prefix once that collection
// grows. Truncation is not an error: NextCursor is non-zero when the server
// stopped early, and Iter pages past the cap instead.
func (cmd *IntersectsCmd[E]) Do(ctx context.Context) ([]E, error) {
	return searchDo(ctx, cmd.searchState, cmd.out)
}

// Iter pages the search to completion, yielding one result at a time in whichever
// output format was selected, following the cursor itself so the hundred-result
// cap never truncates what the caller sees.
//
//	for obj, err := range cmd.Objects().Iter(ctx) {
//		if err != nil {
//			return err
//		}
//		use(obj)
//	}
//
// An explicit Limit or Cursor is the caller's own bound, so Iter yields that one
// page rather than paging past it. Breaking out of the range just stops asking
// for pages; nothing is left open.
func (cmd *IntersectsCmd[E]) Iter(ctx context.Context) iter.Seq2[E, error] {
	return searchIter(ctx, cmd.searchState, cmd.out)
}

// Count runs the COUNT form: INTERSECTS collection [opts] COUNT <area>.
// It returns the number of matching objects.
//
// COUNT is a terminal rather than an output format: its reply is a bare
// integer, so there is no element type for a builder to carry, and the
// hundred-result cap does not apply: the server exempts COUNT from it.
func (cmd *IntersectsCmd[E]) Count(ctx context.Context) (int, error) {
	return searchCount(ctx, cmd.searchState)
}

// Fence opens a live geofence: INTERSECTS collection [opts] FENCE [DETECT …] <area>.
// The returned Stream holds a dedicated connection and delivers events until it
// is closed or ctx is cancelled.
func (cmd *IntersectsCmd[E]) Fence(ctx context.Context) (*Stream, error) {
	return cmd.c.fenceStream(ctx, cmd.fenceArgs())
}

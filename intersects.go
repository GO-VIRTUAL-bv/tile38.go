// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package tile38

import (
	"context"
	"fmt"
	"strconv"
)

// IntersectsCmd builds a Tile38 INTERSECTS query.
// INTERSECTS finds objects whose geometry intersects the search area,
// whereas WITHIN requires full containment.
// Methods may be chained in any order; the parts are assembled into protocol
// order when the command runs.
type IntersectsCmd struct {
	c         *Client
	args      []any // verb, key, and repeatable options
	opts      searchOpts
	cursorOut uint64 // cursor from the last executed terminal
	detect    []DetectState
	commands  []Command
	distance  bool
	geom      []any // search area
}

// Limit caps the number of results. Zero means no limit.
func (cmd *IntersectsCmd) Limit(n int) *IntersectsCmd {
	cmd.opts.limit = &n
	return cmd
}

// Cursor resumes a search from where a previous one stopped, matching Tile38's
// CURSOR keyword. Pass the value NextCursor reported. Setting it also means the
// caller is paging deliberately, so a truncated result no longer reports
// ErrTruncated. Tile38 rejects CURSOR on a fence, so Fence ignores it.
func (cmd *IntersectsCmd) Cursor(n uint64) *IntersectsCmd {
	cmd.opts.cursor = &n
	return cmd
}

// NextCursor reports where to resume after the last executed terminal. It is
// non-zero only when Tile38 stopped at the limit with more objects matching.
func (cmd *IntersectsCmd) NextCursor() uint64 { return cmd.cursorOut }

// Where sets an optional Tile38 field expression filter.
func (cmd *IntersectsCmd) Where(expr string) *IntersectsCmd {
	cmd.args = append(cmd.args, "WHERE", expr)
	return cmd
}

// Match filters results by ID pattern (glob-style, e.g. "truck:*").
func (cmd *IntersectsCmd) Match(pattern string) *IntersectsCmd {
	cmd.args = append(cmd.args, "MATCH", pattern)
	return cmd
}

// WhereIn keeps results whose field holds one of the given values, matching
// Tile38's WHEREIN keyword. It accumulates: each call adds another filter.
func (cmd *IntersectsCmd) WhereIn(field string, values ...any) *IntersectsCmd {
	cmd.args = append(cmd.args, whereInTokens(field, values)...)
	return cmd
}

// NoFields drops field values from the reply, matching Tile38's NOFIELDS keyword.
func (cmd *IntersectsCmd) NoFields() *IntersectsCmd {
	cmd.opts.nofields = true
	return cmd
}

// Clip trims returned objects to the search area rather than returning them
// whole, matching Tile38's CLIP keyword.
func (cmd *IntersectsCmd) Clip() *IntersectsCmd {
	cmd.opts.clip = true
	return cmd
}

// Sparse spreads results evenly over the search area at the given depth (1-8),
// matching Tile38's SPARSE keyword. Tile38 rejects SPARSE combined with Limit.
func (cmd *IntersectsCmd) Sparse(depth int) *IntersectsCmd {
	cmd.opts.sparse = &depth
	return cmd
}

// Detect restricts a live fence to the given transitions. Only meaningful with Fence.
func (cmd *IntersectsCmd) Detect(states ...DetectState) *IntersectsCmd {
	cmd.detect = states
	return cmd
}

// Commands restricts a live fence to events caused by the given commands.
// Only meaningful with Fence.
func (cmd *IntersectsCmd) Commands(commands ...Command) *IntersectsCmd {
	cmd.commands = commands
	return cmd
}

// Distance adds each object's distance from the fence centre to every event the
// fence produces, matching Tile38's DISTANCE keyword. It arrives on FenceEvent
// as Distance, and applies to the live fence only — a plain query reads the same
// value through PointsWithDistance.
func (cmd *IntersectsCmd) Distance() *IntersectsCmd {
	cmd.distance = true
	return cmd
}

// WhereEval keeps results for which the given Lua script returns true, matching
// Tile38's WHEREEVAL keyword. The script sees the object's fields as FIELDS and
// the extra arguments as ARGV. It accumulates: each call adds another filter.
func (cmd *IntersectsCmd) WhereEval(script string, args ...any) *IntersectsCmd {
	cmd.args = append(cmd.args, countedTokens("WHEREEVAL", script, args)...)
	return cmd
}

// WhereEvalSha is WhereEval against a script already loaded on the server,
// matching Tile38's WHEREEVALSHA keyword.
func (cmd *IntersectsCmd) WhereEvalSha(sha string, args ...any) *IntersectsCmd {
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
func (cmd *IntersectsCmd) Buffer(metres int) *IntersectsCmd {
	cmd.args = append(cmd.args, "BUFFER", metres)
	return cmd
}

// Get sets the search area to an object already stored in Tile38 (GET keyword).
func (cmd *IntersectsCmd) Get(collection, id string) *IntersectsCmd {
	cmd.geom = []any{"GET", collection, id}
	return cmd
}

// Object sets the search area to an inline GeoJSON string (OBJECT keyword).
func (cmd *IntersectsCmd) Object(geojson string) *IntersectsCmd {
	cmd.geom = []any{"OBJECT", geojson}
	return cmd
}

// Bounds sets the search area to a lat/lon bounding box (BOUNDS keyword).
func (cmd *IntersectsCmd) Bounds(swLat, swLon, neLat, neLon float64) *IntersectsCmd {
	cmd.geom = []any{"BOUNDS", swLat, swLon, neLat, neLon}
	return cmd
}

// Circle sets the search area to a circle with centre + radius in metres (CIRCLE keyword).
func (cmd *IntersectsCmd) Circle(lat, lon float64, radius int) *IntersectsCmd {
	cmd.geom = []any{"CIRCLE", lat, lon, radius}
	return cmd
}

// A5 sets the search area to a single A5 cell's pentagon, identified by its hex
// cell id (A5 keyword). Requires a server built from upstream master: A5 is
// merged upstream but has shipped in no release tag as of 1.38.0. Tile38 accepts
// A5 as a search area only, not as a hook or channel fence area.
func (cmd *IntersectsCmd) A5(cellID string) *IntersectsCmd {
	cmd.geom = []any{"A5", cellID}
	return cmd
}

// Sector sets the search area to a circular sector: a circle of radius metres
// centred on lat/lon, clipped to the arc between two compass bearings in
// degrees. Matches Tile38's SECTOR keyword, which NEARBY does not accept.
func (cmd *IntersectsCmd) Sector(lat, lon float64, metres int, bearing1, bearing2 float64) *IntersectsCmd {
	cmd.geom = []any{"SECTOR", lat, lon, metres, bearing1, bearing2}
	return cmd
}

// Hash sets the search area to the box a geohash covers, matching Tile38's HASH
// keyword. The shorter the hash, the larger the box.
func (cmd *IntersectsCmd) Hash(geohash string) *IntersectsCmd {
	cmd.geom = []any{"HASH", geohash}
	return cmd
}

// QuadKey sets the search area to the tile a Bing Maps quadkey names, matching
// Tile38's QUADKEY keyword. Tile is the same area expressed as x/y/z.
func (cmd *IntersectsCmd) QuadKey(quadkey string) *IntersectsCmd {
	cmd.geom = []any{"QUADKEY", quadkey}
	return cmd
}

// Tile sets the search area to a single XYZ map tile (TILE keyword).
func (cmd *IntersectsCmd) Tile(x, y, z int) *IntersectsCmd {
	cmd.geom = []any{"TILE", x, y, z}
	return cmd
}

func (cmd *IntersectsCmd) execArgs(format ...string) []any {
	// No fence clause: Detect and Commands only apply to Fence.
	return buildSearch(cmd.args, cmd.opts, nil, format, cmd.geom)
}

// IDs executes: INTERSECTS collection [opts] IDS area
func (cmd *IntersectsCmd) IDs(ctx context.Context) ([]string, error) {
	val, err := cmd.c.do(ctx, cmd.execArgs("IDS")...)
	if err != nil {
		return nil, fmt.Errorf("tile38: INTERSECTS IDs: %w", err)
	}
	res, cursor, err := parseScanIDs(val)
	if err != nil {
		return nil, err
	}
	cmd.cursorOut = cursor
	return res, truncation(cmd.opts, cursor)
}

// Points executes: INTERSECTS collection [opts] POINTS area
func (cmd *IntersectsCmd) Points(ctx context.Context) ([]NearbyResult, error) {
	val, err := cmd.c.do(ctx, cmd.execArgs("POINTS")...)
	if err != nil {
		return nil, fmt.Errorf("tile38: INTERSECTS POINTS: %w", err)
	}
	res, cursor, err := parseNearbyPoints(val)
	if err != nil {
		return nil, err
	}
	cmd.cursorOut = cursor
	return res, truncation(cmd.opts, cursor)
}

// Count executes: INTERSECTS collection [opts] COUNT area
func (cmd *IntersectsCmd) Count(ctx context.Context) (int, error) {
	val, err := cmd.c.do(ctx, cmd.execArgs("COUNT")...)
	if err != nil {
		return 0, fmt.Errorf("tile38: INTERSECTS COUNT: %w", err)
	}
	return parseCount("INTERSECTS", val)
}

// Objects executes: INTERSECTS collection [opts] OBJECTS area
func (cmd *IntersectsCmd) Objects(ctx context.Context) ([]SearchObject, error) {
	val, err := cmd.c.do(ctx, cmd.execArgs("OBJECTS")...)
	if err != nil {
		return nil, fmt.Errorf("tile38: INTERSECTS OBJECTS: %w", err)
	}
	res, cursor, err := parseObjects("INTERSECTS", val)
	if err != nil {
		return nil, err
	}
	cmd.cursorOut = cursor
	return res, truncation(cmd.opts, cursor)
}

// Rects executes: INTERSECTS collection [opts] BOUNDS <area>
// Each result is the bounding box of a matching object, lat first.
func (cmd *IntersectsCmd) Rects(ctx context.Context) ([]RectResult, error) {
	return searchRects(ctx, cmd.c, "INTERSECTS", cmd.opts, &cmd.cursorOut, cmd.execArgs("BOUNDS"))
}

// Hashes executes: INTERSECTS collection [opts] HASHES precision <area>
// Each result is the geohash of a matching object's centre.
func (cmd *IntersectsCmd) Hashes(ctx context.Context, precision int) ([]HashResult, error) {
	return searchHashes(ctx, cmd.c, "INTERSECTS", cmd.opts, &cmd.cursorOut, cmd.execArgs("HASHES", strconv.Itoa(precision)))
}

// A5Cells executes: INTERSECTS collection [opts] A5 level <area>
// Each result is the A5 cell a matching object's centre falls in. Named for the
// output rather than the keyword because A5 is already the search-area method on
// the builders that take one. Requires a server built from upstream master.
func (cmd *IntersectsCmd) A5Cells(ctx context.Context, level int) ([]A5Result, error) {
	return searchA5Cells(ctx, cmd.c, "INTERSECTS", cmd.opts, &cmd.cursorOut, cmd.execArgs("A5", strconv.Itoa(level)))
}

// Fence opens a live geofence: INTERSECTS collection [opts] FENCE [DETECT …] area.
// The returned Stream holds a dedicated connection and delivers events until it
// is closed or ctx is cancelled.
func (cmd *IntersectsCmd) Fence(ctx context.Context) (*Stream, error) {
	args := buildSearch(cmd.args, cmd.opts.fenceOpts(),
		fenceTokens(cmd.distance, cmd.detect, cmd.commands, false), nil, cmd.geom)
	return cmd.c.fenceStream(ctx, args)
}

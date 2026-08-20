// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package tile38

import (
	"context"
	"fmt"
)

// Area is one side of a TEST comparison. TEST takes two areas positionally
// rather than as chained options, so they are built as values and handed to
// Client.Test and the verb method:
//
//	ok, err := c.Test(tile38.AreaGet("fleet", "truck1")).
//		Within(tile38.AreaBounds(tile38.GlobalBounds())).Do(ctx)
//
// The same geometries a search takes are available here, except A5: WITHIN and
// INTERSECTS accept an A5 cell but TEST rejects it on the supported server.
type Area struct {
	tokens []any
}

// AreaPoint is a single coordinate (POINT keyword).
func AreaPoint(lat, lon float64) Area {
	return Area{tokens: []any{"POINT", lat, lon}}
}

// AreaBounds is a lat/lon bounding box (BOUNDS keyword). Pass GlobalBounds() for
// the whole world.
func AreaBounds(swLat, swLon, neLat, neLon float64) Area {
	return Area{tokens: []any{"BOUNDS", swLat, swLon, neLat, neLon}}
}

// AreaCircle is a circle with centre and radius in metres (CIRCLE keyword).
func AreaCircle(lat, lon float64, metres int) Area {
	return Area{tokens: []any{"CIRCLE", lat, lon, metres}}
}

// AreaSector is a circle clipped to the arc between two compass bearings in
// degrees (SECTOR keyword).
func AreaSector(lat, lon float64, metres int, bearing1, bearing2 float64) Area {
	return Area{tokens: []any{"SECTOR", lat, lon, metres, bearing1, bearing2}}
}

// AreaObject is an inline GeoJSON geometry (OBJECT keyword).
func AreaObject(geojson string) Area {
	return Area{tokens: []any{"OBJECT", geojson}}
}

// AreaGet is an object already stored in Tile38 (GET keyword).
func AreaGet(collection, id string) Area {
	return Area{tokens: []any{"GET", collection, id}}
}

// AreaHash is the box a geohash covers (HASH keyword).
func AreaHash(geohash string) Area {
	return Area{tokens: []any{"HASH", geohash}}
}

// AreaQuadKey is the tile a Bing Maps quadkey names (QUADKEY keyword).
func AreaQuadKey(quadkey string) Area {
	return Area{tokens: []any{"QUADKEY", quadkey}}
}

// AreaTile is a single XYZ map tile (TILE keyword).
func AreaTile(x, y, z int) Area {
	return Area{tokens: []any{"TILE", x, y, z}}
}

// TestCmd builds a Tile38 TEST command: a spatial comparison of two areas that
// touches no stored object and needs no collection.
type TestCmd struct {
	c     *Client
	area1 Area
	verb  string
	area2 Area
}

// Within compares with the WITHIN relation: true when the first area lies
// entirely inside the second.
func (cmd *TestCmd) Within(area Area) *TestCmd {
	cmd.verb, cmd.area2 = "WITHIN", area
	return cmd
}

// Intersects compares with the INTERSECTS relation: true when the two areas
// overlap at all.
func (cmd *TestCmd) Intersects(area Area) *TestCmd {
	cmd.verb, cmd.area2 = "INTERSECTS", area
	return cmd
}

// args assembles TEST <area1> <verb> [CLIP] <area2>, which is positional
// throughout: the areas cannot be appended in call order.
func (cmd *TestCmd) args(clip bool) []any {
	out := make([]any, 0, len(cmd.area1.tokens)+len(cmd.area2.tokens)+2)
	out = append(out, "TEST")
	out = append(out, cmd.area1.tokens...)
	out = append(out, cmd.verb)
	if clip {
		out = append(out, "CLIP")
	}
	return append(out, cmd.area2.tokens...)
}

// Do executes the comparison and reports whether it holds.
func (cmd *TestCmd) Do(ctx context.Context) (bool, error) {
	val, err := cmd.c.do(ctx, cmd.args(false)...)
	if err != nil {
		return false, fmt.Errorf("tile38: TEST: %w", err)
	}
	n, err := toInt64("TEST", val)
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// Clip executes the comparison with Tile38's CLIP keyword, which also returns
// the first area clipped to the second as GeoJSON. It is a separate terminal
// because CLIP changes the reply from a bare integer to [result, geojson].
func (cmd *TestCmd) Clip(ctx context.Context) (bool, string, error) {
	val, err := cmd.c.do(ctx, cmd.args(true)...)
	if err != nil {
		return false, "", fmt.Errorf("tile38: TEST CLIP: %w", err)
	}
	outer, ok := val.([]any)
	if !ok || len(outer) < 2 {
		return false, "", fmt.Errorf("tile38: TEST CLIP: %w: response shape %T", ErrUnexpectedReply, val)
	}
	n, err := toInt64("TEST CLIP", outer[0])
	if err != nil {
		return false, "", err
	}
	geojson, err := toString("TEST CLIP", outer[1])
	if err != nil {
		return false, "", err
	}
	return n == 1, geojson, nil
}

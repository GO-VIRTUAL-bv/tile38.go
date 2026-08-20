// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package tile38

import (
	"fmt"
	"strconv"
)

// searchReply splits a search reply into its cursor and its item list.
//
// Element 0 is the cursor, not a count. Tile38 sets it to a resume offset when
// the scan stopped at the limit and to 0 when it ran to completion (scanner.go,
// writeFoot: "cursor := sw.numberIters; if !sw.hitLimit { cursor = 0 }"), which
// makes a non-zero value the signal that results were truncated.
func searchReply(prefix string, val any) (uint64, []any, error) {
	outer, ok := val.([]any)
	if !ok || len(outer) < 2 {
		return 0, nil, fmt.Errorf("tile38: %s: %w: response shape %T", prefix, ErrUnexpectedReply, val)
	}
	cursor, err := toInt64(prefix, outer[0])
	if err != nil || cursor < 0 {
		return 0, nil, fmt.Errorf("tile38: %s: bad cursor %v", prefix, outer[0])
	}
	inner, ok := outer[1].([]any)
	if !ok {
		return 0, nil, fmt.Errorf("tile38: %s: %w: inner shape %T", prefix, ErrUnexpectedReply, outer[1])
	}
	return uint64(cursor), inner, nil
}

// parseCoords decodes a [lat, lon, z?] coordinate array. Tile38 appends the
// third ordinate only when it is non-zero (scanner.go, extractZCoordinate), so a
// two-element array and a zero z are the same thing on the wire.
func parseCoords(prefix string, v any) (lat, lon, z float64, err error) {
	coords, ok := v.([]any)
	if !ok || len(coords) < 2 {
		return 0, 0, 0, fmt.Errorf("tile38: %s: %w: coords shape %T", prefix, ErrUnexpectedReply, v)
	}
	if lat, err = toFloat64(coords[0]); err != nil {
		return 0, 0, 0, fmt.Errorf("tile38: %s: lat: %w", prefix, err)
	}
	if lon, err = toFloat64(coords[1]); err != nil {
		return 0, 0, 0, fmt.Errorf("tile38: %s: lon: %w", prefix, err)
	}
	if len(coords) > 2 {
		if z, err = toFloat64(coords[2]); err != nil {
			return 0, 0, 0, fmt.Errorf("tile38: %s: z: %w", prefix, err)
		}
	}
	return lat, lon, z, nil
}

// parsePoints is the shared implementation for both NEARBY and SCAN POINTS
// responses. Both return [cursor, [[id, [lat, lon, z?], [field, val, …]?], ...]]
// — the fields array is present only when the object has non-zero fields,
// exactly as it is on the OBJECTS output.
func parsePoints(prefix string, val any) ([]NearbyResult, uint64, error) {
	cursor, inner, err := searchReply(prefix, val)
	if err != nil {
		return nil, 0, err
	}

	results := make([]NearbyResult, 0, len(inner))
	for _, item := range inner {
		pair, ok := item.([]any)
		if !ok || len(pair) < 2 {
			return nil, 0, fmt.Errorf("tile38: %s: %w: item shape %T", prefix, ErrUnexpectedReply, item)
		}
		id, ok := pair[0].(string)
		if !ok {
			return nil, 0, fmt.Errorf("tile38: %s: %w: id type %T", prefix, ErrUnexpectedReply, pair[0])
		}
		lat, lon, z, err := parseCoords(prefix, pair[1])
		if err != nil {
			return nil, 0, err
		}
		res := NearbyResult{ID: id, Lat: lat, Lon: lon, Z: z}
		if len(pair) > 2 {
			res.Fields = parseFields(pair[2])
		}
		results = append(results, res)
	}
	return results, cursor, nil
}

// parsePointsWithDistance parses the NEARBY ... DISTANCE POINTS response.
// Tile38 returns: [cursor, [[id, [lat, lon, z?], [field, val, …]?, dist], ...]] —
// the fields array is present only when the object has non-zero fields, so the
// distance is read from the end of the item rather than a fixed index.
func parsePointsWithDistance(prefix string, val any) ([]NearbyResultWithDistance, uint64, error) {
	cursor, inner, err := searchReply(prefix+" PointsWithDistance", val)
	if err != nil {
		return nil, 0, err
	}
	results := make([]NearbyResultWithDistance, 0, len(inner))
	for _, item := range inner {
		pair, ok := item.([]any)
		if !ok || len(pair) < 3 {
			return nil, 0, fmt.Errorf("tile38: %s PointsWithDistance: %w: item shape %T", prefix, ErrUnexpectedReply, item)
		}
		id, ok := pair[0].(string)
		if !ok {
			return nil, 0, fmt.Errorf("tile38: %s PointsWithDistance: %w: id type %T", prefix, ErrUnexpectedReply, pair[0])
		}
		lat, lon, z, err := parseCoords(prefix+" PointsWithDistance", pair[1])
		if err != nil {
			return nil, 0, err
		}
		dist, err := toFloat64(pair[len(pair)-1])
		if err != nil {
			return nil, 0, fmt.Errorf("tile38: %s PointsWithDistance: dist: %w", prefix, err)
		}
		res := NearbyResultWithDistance{
			NearbyResult: NearbyResult{ID: id, Lat: lat, Lon: lon, Z: z},
			Distance:     dist,
		}
		if len(pair) > 3 {
			res.Fields = parseFields(pair[2])
		}
		results = append(results, res)
	}
	return results, cursor, nil
}

// parseScanIDs parses the RESP response from SCAN ... IDS.
// Tile38 returns: [cursor, [id1, id2, ...]] — a flat list of strings.
func parseScanIDs(prefix string, val any) ([]string, uint64, error) {
	cursor, inner, err := searchReply(prefix+" IDs", val)
	if err != nil {
		return nil, 0, err
	}
	ids := make([]string, 0, len(inner))
	for _, item := range inner {
		id, ok := item.(string)
		if !ok {
			return nil, 0, fmt.Errorf("tile38: %s IDs: %w: id type %T", prefix, ErrUnexpectedReply, item)
		}
		ids = append(ids, id)
	}
	return ids, cursor, nil
}

// parseCount parses a Tile38 COUNT output response, which is a bare integer
// (scanner.go writes respOut = IntegerValue(count) for outputCount).
//
// It deliberately does not unwrap an array reply: element 0 of a search reply is
// the cursor, not a count, so tolerating that shape would silently return the
// wrong number rather than reporting that something is wrong.
func parseCount(prefix string, val any) (int, error) {
	switch v := val.(type) {
	case int64:
		return int(v), nil
	case string:
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("tile38: %s Count: %w", prefix, err)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("tile38: %s Count: %w: count type %T", prefix, ErrUnexpectedReply, val)
	}
}

// parseStringItems walks a search reply whose items are
// [id, <bulk string>, [field, val, …]?] — the shape OBJECTS, HASHES, A5 and
// SEARCH's default output all share, differing only in what the string means.
// build turns one item into the caller's result type.
func parseStringItems[T any](prefix string, val any, build func(id, value string, fields Fields) T) ([]T, uint64, error) {
	cursor, inner, err := searchReply(prefix, val)
	if err != nil {
		return nil, 0, err
	}
	results := make([]T, 0, len(inner))
	for _, item := range inner {
		pair, ok := item.([]any)
		if !ok || len(pair) < 2 {
			return nil, 0, fmt.Errorf("tile38: %s: %w: item shape %T", prefix, ErrUnexpectedReply, item)
		}
		id, ok := pair[0].(string)
		if !ok {
			return nil, 0, fmt.Errorf("tile38: %s: %w: id type %T", prefix, ErrUnexpectedReply, pair[0])
		}
		value, ok := pair[1].(string)
		if !ok {
			return nil, 0, fmt.Errorf("tile38: %s: %w: value type %T", prefix, ErrUnexpectedReply, pair[1])
		}
		var fields Fields
		if len(pair) > 2 {
			fields = parseFields(pair[2])
		}
		results = append(results, build(id, value, fields))
	}
	return results, cursor, nil
}

// parseObjects parses a Tile38 OBJECTS output response.
// Tile38 returns: [cursor, [[id, geojson, [field, val, …]?], ...]]
func parseObjects(prefix string, val any) ([]SearchObject, uint64, error) {
	return parseStringItems(prefix+" Objects", val,
		func(id, geojson string, fields Fields) SearchObject {
			return SearchObject{ID: id, GeoJSON: geojson, Fields: fields}
		})
}

// parseStrings parses a Tile38 SEARCH response in its default output format,
// where element 1 is the object's string value rather than its geometry.
// Tile38 returns: [cursor, [[id, value, [field, val, …]?], ...]]
func parseStrings(prefix string, val any) ([]StringObject, uint64, error) {
	return parseStringItems(prefix+" Strings", val,
		func(id, value string, fields Fields) StringObject {
			return StringObject{ID: id, Value: value, Fields: fields}
		})
}

// parseHashes parses a Tile38 HASHES output response.
// Tile38 returns: [cursor, [[id, geohash, [field, val, …]?], ...]]
func parseHashes(prefix string, val any) ([]HashResult, uint64, error) {
	return parseStringItems(prefix+" Hashes", val,
		func(id, hash string, fields Fields) HashResult {
			return HashResult{ID: id, Hash: hash, Fields: fields}
		})
}

// parseA5Cells parses a Tile38 A5 output response.
// Tile38 returns: [cursor, [[id, cell], ...]] — A5 is not one of the outputs
// scanner.go attaches fields to, so an item never carries a third element.
func parseA5Cells(prefix string, val any) ([]A5Result, uint64, error) {
	return parseStringItems(prefix+" A5", val,
		func(id, cell string, _ Fields) A5Result {
			return A5Result{ID: id, Cell: cell}
		})
}

// parseRects parses a Tile38 BOUNDS output response. Tile38 returns
// [cursor, [[id, [[swlat, swlon], [nelat, nelon]], [field, val, …]?], ...]] —
// lat first, as GET key id BOUNDS does, not the x-first order BOUNDS key uses.
func parseRects(prefix string, val any) ([]RectResult, uint64, error) {
	cursor, inner, err := searchReply(prefix+" Bounds", val)
	if err != nil {
		return nil, 0, err
	}
	results := make([]RectResult, 0, len(inner))
	for _, item := range inner {
		pair, ok := item.([]any)
		if !ok || len(pair) < 2 {
			return nil, 0, fmt.Errorf("tile38: %s Bounds: %w: item shape %T", prefix, ErrUnexpectedReply, item)
		}
		id, ok := pair[0].(string)
		if !ok {
			return nil, 0, fmt.Errorf("tile38: %s Bounds: %w: id type %T", prefix, ErrUnexpectedReply, pair[0])
		}
		box, err := parseBoundsResult(prefix, pair[1], false)
		if err != nil {
			return nil, 0, err
		}
		rect := RectResult{ID: id, Bounds: box}
		if len(pair) > 2 {
			rect.Fields = parseFields(pair[2])
		}
		results = append(results, rect)
	}
	return results, cursor, nil
}

// Results decoding
//
// Each parse function above is a format's decoder: format[E] holds one directly,
// which is what lets the builders be parameterised by the element type. Nothing
// has to hang a method off E, so IDS decodes to a plain string.
//
// The reply reaching a decoder has already been decoded from RESP by
// internal/resp, so it arrives as a string, int64, []any or nil rather than as
// wire bytes; prefix is the search verb and format keyword, for error text.

// parseFields decodes the optional FIELDS element of a search result item, which
// Tile38 emits as a flat [name, value, name, value, …] array and omits entirely
// for an object whose fields are all zero.
//
// A shape it does not recognise yields no fields rather than an error. Fields
// decorate a result; they do not define it, and failing the whole search over an
// unexpected field encoding would lose the geometry too.
func parseFields(v any) Fields {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 || len(arr)%2 != 0 {
		return nil
	}
	out := make(Fields, len(arr)/2)
	for i := 0; i+1 < len(arr); i += 2 {
		name, ok := arr[i].(string)
		if !ok {
			return nil // not name/value pairs after all: nothing to key these by
		}
		out[name] = fieldString(arr[i+1])
	}
	return out
}

// fieldString renders one field value as text. Tile38 sends JSON and string
// fields as bulk strings, but a plain number may arrive already typed.
func fieldString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return ""
	}
}

// parseBoundsResult parses a Tile38 BOUNDS response, which is a pair of corner
// pairs. The two commands disagree on coordinate order: GET key id BOUNDS emits
// [[swlat, swlon], [nelat, nelon]], while BOUNDS key emits the collection extent
// x-first as [[minlon, minlat], [maxlon, maxlat]]. lonFirst selects the latter.
func parseBoundsResult(prefix string, val any, lonFirst bool) (BoundsResult, error) {
	// GET screens its own null in GetCmd.exec, so a null reaching here is
	// BOUNDS key on a collection that does not exist.
	if val == nil {
		return BoundsResult{}, fmt.Errorf("tile38: %s Bounds: %w", prefix, ErrKeyNotFound)
	}
	outer, ok := val.([]any)
	if !ok || len(outer) < 2 {
		return BoundsResult{}, fmt.Errorf("tile38: %s Bounds: %w: response shape %T", prefix, ErrUnexpectedReply, val)
	}
	corner := func(name string, v any) ([2]float64, error) {
		pair, ok := v.([]any)
		if !ok || len(pair) < 2 {
			return [2]float64{}, fmt.Errorf("tile38: %s Bounds: %w: %s shape %T", prefix, ErrUnexpectedReply, name, v)
		}
		first, err := toFloat64(pair[0])
		if err != nil {
			return [2]float64{}, fmt.Errorf("tile38: %s Bounds: %s: %w", prefix, name, err)
		}
		second, err := toFloat64(pair[1])
		if err != nil {
			return [2]float64{}, fmt.Errorf("tile38: %s Bounds: %s: %w", prefix, name, err)
		}
		if lonFirst {
			return [2]float64{second, first}, nil
		}
		return [2]float64{first, second}, nil
	}
	sw, err := corner("SW", outer[0])
	if err != nil {
		return BoundsResult{}, err
	}
	ne, err := corner("NE", outer[1])
	if err != nil {
		return BoundsResult{}, err
	}
	return BoundsResult{SW: sw, NE: ne}, nil
}

// parseHooks parses a HOOKS or CHANS response into []HookInfo. Each descriptor
// is [name, key, [endpoint …], [command token …], [meta name, value …]];
// channels report a single "local://<name>" endpoint.
func parseHooks(prefix string, val any) ([]HookInfo, error) {
	outer, ok := val.([]any)
	if !ok {
		return nil, fmt.Errorf("tile38: %s: %w: response shape %T", prefix, ErrUnexpectedReply, val)
	}
	hooks := make([]HookInfo, 0, len(outer))
	for i, item := range outer {
		desc, ok := item.([]any)
		if !ok || len(desc) < 2 {
			return nil, fmt.Errorf("tile38: %s: %w: item %d shape %T", prefix, ErrUnexpectedReply, i, item)
		}
		name, ok := desc[0].(string)
		if !ok {
			return nil, fmt.Errorf("tile38: %s: item %d name type: %T", prefix, i, desc[0])
		}
		hook := HookInfo{Name: name}
		hook.Key, _ = desc[1].(string)
		if len(desc) > 2 {
			hook.Endpoints, _ = parseStringSlice(prefix, desc[2])
		}
		if len(desc) > 3 {
			hook.Command, _ = parseStringSlice(prefix, desc[3])
		}
		if len(desc) > 4 {
			// Meta arrives in the same flat [name, value, …] shape as fields.
			hook.Meta = parseFields(desc[4])
		}
		hooks = append(hooks, hook)
	}
	return hooks, nil
}

// parseStringSlice parses a flat RESP string array.
func parseStringSlice(prefix string, val any) ([]string, error) {
	items, ok := val.([]any)
	if !ok {
		return nil, fmt.Errorf("tile38: %s: %w: response shape %T", prefix, ErrUnexpectedReply, val)
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("tile38: %s: %w: item type %T", prefix, ErrUnexpectedReply, item)
		}
		result = append(result, s)
	}
	return result, nil
}

// lookupKV finds a value in a flat [key, value, key, value, …] RESP reply.
func lookupKV(prefix string, val any, key string) (any, error) {
	items, ok := val.([]any)
	if !ok {
		return nil, fmt.Errorf("tile38: %s: %w: response shape %T", prefix, ErrUnexpectedReply, val)
	}
	for i := 0; i+1 < len(items); i += 2 {
		if name, ok := items[i].(string); ok && name == key {
			return items[i+1], nil
		}
	}
	return nil, fmt.Errorf("tile38: %s: %q not in response", prefix, key)
}

// toFloat64 converts a RESP value to float64. RESP2 has no float type, so
// Tile38 sends coordinates as bulk strings and counters as integers.
func toFloat64(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case int64:
		return float64(x), nil
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return 0, fmt.Errorf("parse float %q: %w", x, err)
		}
		return f, nil
	default:
		return 0, fmt.Errorf("%w: type %T", ErrUnexpectedReply, v)
	}
}

// toInt64 converts a RESP integer reply, accepting the bulk-string form too.
func toInt64(prefix string, v any) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case string:
		n, err := strconv.ParseInt(x, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("tile38: %s: parse int %q: %w", prefix, x, err)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("tile38: %s: %w: integer type %T", prefix, ErrUnexpectedReply, v)
	}
}

// toString converts a RESP reply to a string. A null is an error rather than an
// empty string: the commands that use this always reply with a bulk string, so a
// null means something unexpected happened and flattening it would hide that.
func toString(prefix string, v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	default:
		return "", fmt.Errorf("tile38: %s: %w: response type %T", prefix, ErrUnexpectedReply, v)
	}
}

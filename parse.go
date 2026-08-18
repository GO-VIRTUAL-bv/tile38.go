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
		return 0, nil, fmt.Errorf("tile38: %s: unexpected response shape: %T", prefix, val)
	}
	cursor, err := toInt64(prefix, outer[0])
	if err != nil || cursor < 0 {
		return 0, nil, fmt.Errorf("tile38: %s: bad cursor %v", prefix, outer[0])
	}
	inner, ok := outer[1].([]any)
	if !ok {
		return 0, nil, fmt.Errorf("tile38: %s: unexpected inner shape: %T", prefix, outer[1])
	}
	return uint64(cursor), inner, nil
}

// parseNearbyPoints parses the RESP response from NEARBY ... POINTS.
// Tile38 returns: [cursor, [[id, [lat, lon], [field, val, …]?], ...]]
func parseNearbyPoints(val any) ([]NearbyResult, uint64, error) {
	return parsePoints("NearbyPoints", val)
}

// parseScanPoints parses the RESP response from SCAN ... POINTS.
// Tile38 returns: [cursor, [[id, [lat, lon], [field, val, …]?], ...]]
func parseScanPoints(val any) ([]NearbyResult, uint64, error) {
	return parsePoints("ScanPoints", val)
}

// parsePoints is the shared implementation for both NEARBY and SCAN POINTS
// responses. Both return [cursor, [[id, [lat, lon], [field, val, …]?], ...]] —
// the fields array is present only when the object has non-zero fields, exactly
// as it is on the OBJECTS output.
func parsePoints(prefix string, val any) ([]NearbyResult, uint64, error) {
	cursor, inner, err := searchReply(prefix, val)
	if err != nil {
		return nil, 0, err
	}

	results := make([]NearbyResult, 0, len(inner))
	for _, item := range inner {
		pair, ok := item.([]any)
		if !ok || len(pair) < 2 {
			return nil, 0, fmt.Errorf("tile38: %s: unexpected item shape: %T", prefix, item)
		}
		id, ok := pair[0].(string)
		if !ok {
			return nil, 0, fmt.Errorf("tile38: %s: unexpected id type: %T", prefix, pair[0])
		}
		coords, ok := pair[1].([]any)
		if !ok || len(coords) < 2 {
			return nil, 0, fmt.Errorf("tile38: %s: unexpected coords shape: %T", prefix, pair[1])
		}
		lat, err := toFloat64(coords[0])
		if err != nil {
			return nil, 0, fmt.Errorf("tile38: %s: lat: %w", prefix, err)
		}
		lon, err := toFloat64(coords[1])
		if err != nil {
			return nil, 0, fmt.Errorf("tile38: %s: lon: %w", prefix, err)
		}
		res := NearbyResult{ID: id, Lat: lat, Lon: lon}
		if len(pair) > 2 {
			res.Fields = parseFields(pair[2])
		}
		results = append(results, res)
	}
	return results, cursor, nil
}

// parsePointsWithDistance parses the NEARBY ... DISTANCE POINTS response.
// Tile38 returns: [cursor, [[id, [lat, lon], [field, val, …]?, dist], ...]] —
// the fields array is present only when the object has non-zero fields, so the
// distance is read from the end of the item rather than a fixed index.
func parsePointsWithDistance(val any) ([]NearbyResultWithDistance, uint64, error) {
	cursor, inner, err := searchReply("PointsWithDistance", val)
	if err != nil {
		return nil, 0, err
	}
	results := make([]NearbyResultWithDistance, 0, len(inner))
	for _, item := range inner {
		pair, ok := item.([]any)
		if !ok || len(pair) < 3 {
			return nil, 0, fmt.Errorf("tile38: PointsWithDistance: unexpected item shape: %T", item)
		}
		id, ok := pair[0].(string)
		if !ok {
			return nil, 0, fmt.Errorf("tile38: PointsWithDistance: unexpected id type: %T", pair[0])
		}
		coords, ok := pair[1].([]any)
		if !ok || len(coords) < 2 {
			return nil, 0, fmt.Errorf("tile38: PointsWithDistance: unexpected coords shape: %T", pair[1])
		}
		lat, err := toFloat64(coords[0])
		if err != nil {
			return nil, 0, fmt.Errorf("tile38: PointsWithDistance: lat: %w", err)
		}
		lon, err := toFloat64(coords[1])
		if err != nil {
			return nil, 0, fmt.Errorf("tile38: PointsWithDistance: lon: %w", err)
		}
		dist, err := toFloat64(pair[len(pair)-1])
		if err != nil {
			return nil, 0, fmt.Errorf("tile38: PointsWithDistance: dist: %w", err)
		}
		res := NearbyResultWithDistance{
			NearbyResult: NearbyResult{ID: id, Lat: lat, Lon: lon},
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
func parseScanIDs(val any) ([]string, uint64, error) {
	cursor, inner, err := searchReply("ScanIDs", val)
	if err != nil {
		return nil, 0, err
	}
	ids := make([]string, 0, len(inner))
	for _, item := range inner {
		id, ok := item.(string)
		if !ok {
			return nil, 0, fmt.Errorf("tile38: ScanIDs: unexpected id type: %T", item)
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
		return 0, fmt.Errorf("tile38: %s Count: unexpected count type: %T", prefix, val)
	}
}

// parseObjects parses a Tile38 OBJECTS output response.
// Tile38 returns: [cursor, [[id, geojson, [field, val, …]?], ...]]
func parseObjects(prefix string, val any) ([]SearchObject, uint64, error) {
	cursor, inner, err := searchReply(prefix+" Objects", val)
	if err != nil {
		return nil, 0, err
	}
	results := make([]SearchObject, 0, len(inner))
	for _, item := range inner {
		pair, ok := item.([]any)
		if !ok || len(pair) < 2 {
			return nil, 0, fmt.Errorf("tile38: %s Objects: unexpected item shape: %T", prefix, item)
		}
		id, ok := pair[0].(string)
		if !ok {
			return nil, 0, fmt.Errorf("tile38: %s Objects: unexpected id type: %T", prefix, pair[0])
		}
		geojson, ok := pair[1].(string)
		if !ok {
			return nil, 0, fmt.Errorf("tile38: %s Objects: unexpected geojson type: %T", prefix, pair[1])
		}
		obj := SearchObject{ID: id, GeoJSON: geojson}
		if len(pair) > 2 {
			obj.Fields = parseFields(pair[2])
		}
		results = append(results, obj)
	}
	return results, cursor, nil
}

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
	if val == nil {
		return BoundsResult{}, fmt.Errorf("tile38: %s Bounds: not found", prefix)
	}
	outer, ok := val.([]any)
	if !ok || len(outer) < 2 {
		return BoundsResult{}, fmt.Errorf("tile38: %s Bounds: unexpected response shape: %T", prefix, val)
	}
	corner := func(name string, v any) ([2]float64, error) {
		pair, ok := v.([]any)
		if !ok || len(pair) < 2 {
			return [2]float64{}, fmt.Errorf("tile38: %s Bounds: unexpected %s shape: %T", prefix, name, v)
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
// is [name, key, [endpoint …], [command token …], [meta …]]; channels report a
// single "local://<name>" endpoint.
func parseHooks(prefix string, val any) ([]HookInfo, error) {
	outer, ok := val.([]any)
	if !ok {
		return nil, fmt.Errorf("tile38: %s: unexpected response shape: %T", prefix, val)
	}
	hooks := make([]HookInfo, 0, len(outer))
	for i, item := range outer {
		desc, ok := item.([]any)
		if !ok || len(desc) < 2 {
			return nil, fmt.Errorf("tile38: %s: item %d unexpected shape: %T", prefix, i, item)
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
		hooks = append(hooks, hook)
	}
	return hooks, nil
}

// parseStringSlice parses a flat RESP string array.
func parseStringSlice(prefix string, val any) ([]string, error) {
	items, ok := val.([]any)
	if !ok {
		return nil, fmt.Errorf("tile38: %s: unexpected response shape: %T", prefix, val)
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("tile38: %s: unexpected item type: %T", prefix, item)
		}
		result = append(result, s)
	}
	return result, nil
}

// lookupKV finds a value in a flat [key, value, key, value, …] RESP reply.
func lookupKV(prefix string, val any, key string) (any, error) {
	items, ok := val.([]any)
	if !ok {
		return nil, fmt.Errorf("tile38: %s: unexpected response shape: %T", prefix, val)
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
		return 0, fmt.Errorf("unexpected type %T", v)
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
		return 0, fmt.Errorf("tile38: %s: unexpected integer type %T", prefix, v)
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
		return "", fmt.Errorf("tile38: %s: unexpected response type %T", prefix, v)
	}
}

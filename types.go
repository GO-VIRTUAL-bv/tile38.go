// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package tile38

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// Result types returned by the command builders. Coordinates are always
// {lat, lon}, matching the order Tile38 takes them in, with the optional third
// z ordinate alongside them.

// Fields are an object's Tile38 FIELDS, name → Tile38's own text encoding of the
// value: the decimal form of a number, the verbatim JSON text of a JSON field.
// Nil when the object has no non-zero fields, or when the query used NoFields.
//
// Reading them off a result is what makes a whole-collection query one round
// trip rather than an FGet per field per object.
type Fields map[string]string

// UnmarshalJSON decodes the "fields" object of a geofence notification. Tile38
// writes those values as JSON — a string field arrives quoted — where the RESP
// search replies carry the same values as flat text. Unquoting here means a
// field reads the same whichever path it arrived on.
func (f *Fields) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := make(Fields, len(raw))
	for name, v := range raw {
		var s string
		if json.Unmarshal(v, &s) == nil {
			out[name] = s
		} else {
			out[name] = string(v)
		}
	}
	*f = out
	return nil
}

// FieldOf decodes the named field into T, which is what a caller wants from
// Fields most of the time: the map holds Tile38's text encoding, so reading a
// number off a result otherwise means a strconv call at every call site.
//
// ok is false when the field is absent *or* does not decode as a T. Those are
// different bugs and this deliberately collapses them — when the difference
// matters, index Fields directly and convert.
//
// A nil Fields is an ordinary miss, which is what an object with no non-zero
// fields and a NoFields query both give back.
//
// Named FieldOf rather than Field because Field already builds a key/value pair
// for the write builders.
//
// The constraint has no ~ terms on purpose. Under ~float64 a named type would
// compile and then fall through the switch, because any(&value) is *knots
// rather than *float64, and the caller would get a zero it never decoded.
// Without the tilde that is a compile error instead.
func FieldOf[T float64 | int64 | bool | string](f Fields, name string) (value T, ok bool) {
	raw, present := f[name]
	if !present {
		return value, false
	}
	switch p := any(&value).(type) {
	case *string:
		*p = raw
	case *float64:
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return value, false
		}
		*p = v
	case *int64:
		// ParseInt, so "42.5" is a miss rather than a silent truncation to 42.
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return value, false
		}
		*p = v
	case *bool:
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return value, false
		}
		*p = v
	default:
		// Unreachable while the constraint has no ~ terms. Kept so that widening
		// it later degrades to a miss rather than to a silent zero.
		return value, false
	}
	return value, true
}

// MustFieldOf is FieldOf for a field the caller knows is set. It panics when
// FieldOf would report false, naming both possibilities because it cannot tell
// them apart.
func MustFieldOf[T float64 | int64 | bool | string](f Fields, name string) T {
	value, ok := FieldOf[T](f, name)
	if !ok {
		panic(fmt.Sprintf("tile38: field %q is missing or not a %T", name, value))
	}
	return value
}

// NearbyResult holds a single result from a Nearby or Scan query.
type NearbyResult struct {
	ID  string
	Lat float64
	Lon float64
	// Z is the point's third ordinate — Tile38 stores whatever a caller put
	// there, most often an altitude. It is zero both for a two-dimensional point
	// and for a z of zero: Tile38 omits the ordinate entirely when it is zero,
	// so the two are indistinguishable on the wire.
	Z      float64
	Fields Fields
}

// NearbyResultWithDistance extends NearbyResult with the distance from the query centre.
type NearbyResultWithDistance struct {
	NearbyResult
	Distance float64 // metres
}

// SearchObject holds a single result from a search query using the OBJECTS output format.
type SearchObject struct {
	ID      string
	GeoJSON string
	Fields  Fields
}

// StringObject holds a single result from a Search query, which matches on the
// string values "SET … STRING" stores rather than on geometry.
type StringObject struct {
	ID     string
	Value  string
	Fields Fields
}

// RectResult holds a single result from a search using the BOUNDS output format.
type RectResult struct {
	ID     string
	Bounds BoundsResult
	Fields Fields
}

// HashResult holds a single result from a search using the HASHES output format.
type HashResult struct {
	ID     string
	Hash   string // geohash at the precision the query asked for
	Fields Fields
}

// A5Result holds a single result from a search using the A5 output format. A5 is
// the one output format Tile38 does not attach fields to (scanner.go,
// hasFieldsOutput).
type A5Result struct {
	ID   string
	Cell string // A5 cell id at the level the query asked for
}

// BoundsResult holds the SW and NE corners of a bounding box.
type BoundsResult struct {
	SW [2]float64 // {lat, lon}
	NE [2]float64 // {lat, lon}
}

// The output formats a search can return. Each names the slice Do hands back
// for that format, as an alias rather than a defined type: the builders are
// parameterised by the element type, so Points and []NearbyResult are the same
// type rather than merely assignable, and the decoding lives on the format
// value rather than on these.

// IDs is the IDS output: the matching object ids, and the format a search
// returns when no other is chosen.
type IDs = []string

// Points is the POINTS output: each object's centre, with its fields.
type Points = []NearbyResult

// PointsWithDistance is the DISTANCE POINTS output, which adds each object's
// distance from the search centre. Nearby only.
type PointsWithDistance = []NearbyResultWithDistance

// Objects is the OBJECTS output: each object's GeoJSON, with its fields.
type Objects = []SearchObject

// Rects is the BOUNDS output: each object's bounding box, lat first.
type Rects = []RectResult

// Hashes is the HASHES output: each object's centre as a geohash.
type Hashes = []HashResult

// A5Cells is the A5 output: the A5 cell each object's centre falls in.
type A5Cells = []A5Result

// Strings is SEARCH's own default output, where each item carries the object's
// string value rather than its geometry.
type Strings = []StringObject

// CollectionStats holds the STATS counters for one collection. Exists is false
// when the collection is not there: Tile38 answers with a null element for a
// missing key rather than an error.
type CollectionStats struct {
	Key          string
	Exists       bool
	InMemorySize int64
	NumObjects   int64
	NumPoints    int64
	NumStrings   int64
}

// HookInfo describes a registered hook (HOOKS) or pub/sub channel (CHANS).
type HookInfo struct {
	Name      string            // hook or channel name
	Key       string            // collection the fence watches
	Endpoints []string          // delivery targets; "local://<name>" for channels
	Command   []string          // the fence command tokens the hook was created with
	Meta      map[string]string // the META pairs the hook was created with
}

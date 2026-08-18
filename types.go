// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package tile38

import "encoding/json"

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

// HookInfo describes a registered hook (HOOKS) or pub/sub channel (CHANS).
type HookInfo struct {
	Name      string            // hook or channel name
	Key       string            // collection the fence watches
	Endpoints []string          // delivery targets; "local://<name>" for channels
	Command   []string          // the fence command tokens the hook was created with
	Meta      map[string]string // the META pairs the hook was created with
}

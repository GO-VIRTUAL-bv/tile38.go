// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package tile38

// Result types returned by the command builders. Coordinates are always
// {lat, lon}, matching the order Tile38 takes them in.

// NearbyResult holds a single result from a Nearby or Scan query.
type NearbyResult struct {
	ID  string
	Lat float64
	Lon float64
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
	// Fields are the object's FIELDS, name → Tile38's own text encoding of the
	// value: the decimal form of a number, the verbatim JSON text of a JSON field.
	// Nil when the object has no non-zero fields, or when the query used NoFields.
	//
	// Reading them here is what makes a whole-collection query one round trip
	// rather than an FGet per field per object.
	Fields map[string]string
}

// BoundsResult holds the SW and NE corners of a bounding box.
type BoundsResult struct {
	SW [2]float64 // {lat, lon}
	NE [2]float64 // {lat, lon}
}

// HookInfo describes a registered hook (HOOKS) or pub/sub channel (CHANS).
type HookInfo struct {
	Name      string   // hook or channel name
	Key       string   // collection the fence watches
	Endpoints []string // delivery targets; "local://<name>" for channels
	Command   []string // the fence command tokens the hook was created with
}

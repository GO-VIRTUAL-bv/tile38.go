// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package tile38

import (
	"encoding/json"
	"fmt"
)

// FenceEvent is a decoded Tile38 geofence notification — the JSON payload Tile38
// pushes to a webhook endpoint (SETHOOK), broadcasts to a pub/sub channel
// (SETCHAN), or streams down a live fence connection (Fence) when an object
// enters, dwells in, or exits a fenced area.
type FenceEvent struct {
	Command  string            `json:"command"`            // set | del | drop | fset
	Group    string            `json:"group,omitempty"`    // correlation group id
	Detect   string            `json:"detect,omitempty"`   // enter|exit|inside|outside|cross|roam
	Hook     string            `json:"hook,omitempty"`     // hook/channel name
	Meta     map[string]string `json:"meta,omitempty"`     // hook META key/values
	Key      string            `json:"key,omitempty"`      // collection key
	Time     string            `json:"time,omitempty"`     // RFC3339 timestamp
	ID       string            `json:"id,omitempty"`       // object id
	Object   json.RawMessage   `json:"object,omitempty"`   // GeoJSON of the object
	Fields   Fields            `json:"fields,omitempty"`   // the object's FIELDS at the time of the event
	Distance float64           `json:"distance,omitempty"` // metres from the fence centre; only on a fence that asked for DISTANCE
	Nearby   json.RawMessage   `json:"nearby,omitempty"`   // roam companion object
	Faraway  json.RawMessage   `json:"faraway,omitempty"`  // roam companion object
}

// DecodeFenceEvent unmarshals a Tile38 geofence notification payload.
func DecodeFenceEvent(data []byte) (*FenceEvent, error) {
	var ev FenceEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return nil, fmt.Errorf("tile38: decode fence event: %w", err)
	}
	return &ev, nil
}

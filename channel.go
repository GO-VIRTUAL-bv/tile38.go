// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package tile38

import (
	"context"
	"fmt"
)

// SetChanCmd builds a Tile38 SETCHAN command for a pub/sub geofence channel.
// SETCHAN is identical to SETHOOK but broadcasts events to Subscribe clients
// instead of pushing to an endpoint URL: a spatial trigger (Nearby/Within),
// optional Detect/Commands filters, and one fence area.
//
// Methods may be chained in any order; the parts are assembled into protocol
// order when the command runs.
type SetChanCmd struct {
	c        *Client
	name     string
	meta     [][2]string
	ex       *int
	trigger  []any // NEARBY|WITHIN|INTERSECTS collection
	args     []any // repeatable options that follow the trigger
	detect   []DetectState
	commands []Command
	nodwell  bool
	geom     []any // fence area
	radius   *int  // trailing metres of a POINT area
}

// Nearby selects the NEARBY spatial trigger. Use with Point and Radius, or with
// Roam.
func (cmd *SetChanCmd) Nearby(collection string) *SetChanCmd {
	cmd.trigger = []any{"NEARBY", collection}
	return cmd
}

// Within selects the WITHIN spatial trigger. Use with any fence area.
func (cmd *SetChanCmd) Within(collection string) *SetChanCmd {
	cmd.trigger = []any{"WITHIN", collection}
	return cmd
}

// Intersects selects the INTERSECTS spatial trigger, which fires on any overlap
// with the fence area rather than requiring full containment.
func (cmd *SetChanCmd) Intersects(collection string) *SetChanCmd {
	cmd.trigger = []any{"INTERSECTS", collection}
	return cmd
}

// Meta attaches a key/value pair to the channel, echoed back on every event it
// produces. It accumulates: each call adds another pair.
func (cmd *SetChanCmd) Meta(key, value string) *SetChanCmd {
	cmd.meta = append(cmd.meta, [2]string{key, value})
	return cmd
}

// EX sets how long the channel lives before Tile38 removes it, in seconds.
func (cmd *SetChanCmd) EX(secs int) *SetChanCmd {
	cmd.ex = &secs
	return cmd
}

// Where sets an optional Tile38 field expression filter.
func (cmd *SetChanCmd) Where(expr string) *SetChanCmd {
	cmd.args = append(cmd.args, "WHERE", expr)
	return cmd
}

// Detect restricts the channel to the given transitions. When omitted, Tile38's
// default detect set applies.
func (cmd *SetChanCmd) Detect(states ...DetectState) *SetChanCmd {
	cmd.detect = states
	return cmd
}

// Commands restricts the channel to events caused by the given commands.
func (cmd *SetChanCmd) Commands(commands ...Command) *SetChanCmd {
	cmd.commands = commands
	return cmd
}

// Roam fires when objects in the trigger collection come within radiusM metres
// of an object in collection. Use with Nearby.
//
// Objects that stay in range keep reporting on each update; chain NoDwell to
// suppress those.
func (cmd *SetChanCmd) Roam(collection string, radiusM int) *SetChanCmd {
	cmd.geom = []any{"ROAM", collection, "*", radiusM}
	return cmd
}

// NoDwell stops a roaming fence from re-reporting objects that stay within range
// between updates, matching Tile38's NODWELL keyword. It only affects Roam
// fences, and it is opt-in: dwelling is Tile38's own default.
func (cmd *SetChanCmd) NoDwell() *SetChanCmd {
	cmd.nodwell = true
	return cmd
}

// Bounds sets the fence area to a lat/lon bounding box. Pass GlobalBounds() to
// fence the whole world.
func (cmd *SetChanCmd) Bounds(swLat, swLon, neLat, neLon float64) *SetChanCmd {
	cmd.geom = []any{"BOUNDS", swLat, swLon, neLat, neLon}
	return cmd
}

// Circle sets the fence area to a circle with centre + radius in metres.
func (cmd *SetChanCmd) Circle(lat, lon float64, radius int) *SetChanCmd {
	cmd.geom = []any{"CIRCLE", lat, lon, radius}
	return cmd
}

// Point sets the fence area to a point, and is the area a Nearby trigger takes:
// NEARBY reads "POINT lat lon meters" and rejects CIRCLE, so a channel fencing
// on NEARBY needs this rather than Circle. Pair it with Radius.
func (cmd *SetChanCmd) Point(lat, lon float64) *SetChanCmd {
	cmd.geom = []any{"POINT", lat, lon}
	return cmd
}

// Radius sets the trailing metres of a Point area. Named for the value it
// carries: Tile38 has no keyword for it, it is the last argument of
// "POINT lat lon meters".
func (cmd *SetChanCmd) Radius(metres int) *SetChanCmd {
	cmd.radius = &metres
	return cmd
}

// Object sets the fence area to an inline GeoJSON string.
func (cmd *SetChanCmd) Object(geojson string) *SetChanCmd {
	cmd.geom = []any{"OBJECT", geojson}
	return cmd
}

// Get sets the fence area to an object already stored in Tile38.
func (cmd *SetChanCmd) Get(collection, id string) *SetChanCmd {
	cmd.geom = []any{"GET", collection, id}
	return cmd
}

// Do executes the SETCHAN command.
func (cmd *SetChanCmd) Do(ctx context.Context) error {
	head := hookHead([]any{"SETCHAN", cmd.name}, cmd.meta, cmd.ex)
	head = append(head, cmd.trigger...)
	args := buildSearch(append(head, cmd.args...), searchOpts{},
		fenceTokens(cmd.detect, cmd.commands, cmd.nodwell), nil,
		pointGeometry(cmd.geom, cmd.radius))
	if _, err := cmd.c.do(ctx, args...); err != nil {
		return fmt.Errorf("tile38: SETCHAN: %w", err)
	}
	return nil
}

// DelChanCmd builds a Tile38 DELCHAN command.
type DelChanCmd struct {
	c    *Client
	args []any
}

// Do executes: DELCHAN channelName
func (cmd *DelChanCmd) Do(ctx context.Context) error {
	if _, err := cmd.c.do(ctx, cmd.args...); err != nil {
		return fmt.Errorf("tile38: DELCHAN: %w", err)
	}
	return nil
}

// PDelChanCmd builds a Tile38 PDELCHAN command (pattern-based channel deletion).
type PDelChanCmd struct {
	c    *Client
	args []any
}

// Do executes: PDELCHAN pattern
func (cmd *PDelChanCmd) Do(ctx context.Context) error {
	if _, err := cmd.c.do(ctx, cmd.args...); err != nil {
		return fmt.Errorf("tile38: PDELCHAN: %w", err)
	}
	return nil
}

// ChansCmd builds a Tile38 CHANS command to list registered pub/sub channels.
type ChansCmd struct {
	c    *Client
	args []any
}

// Do executes: CHANS [pattern] — returns the channels matching the glob pattern.
func (cmd *ChansCmd) Do(ctx context.Context) ([]HookInfo, error) {
	val, err := cmd.c.do(ctx, cmd.args...)
	if err != nil {
		return nil, fmt.Errorf("tile38: CHANS: %w", err)
	}
	return parseHooks("CHANS", val)
}

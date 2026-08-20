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
// optional Detect/Commands filters, and one fence area. It shares its chain
// methods with HookCmd through fenceBase, so every one of them returns
// *SetChanCmd however godoc renders the promoted signature.
//
// Methods may be chained in any order; the parts are assembled into protocol
// order when the command runs.
type SetChanCmd struct {
	fenceBase[*SetChanCmd]
}

// Do executes the SETCHAN command.
func (cmd *SetChanCmd) Do(ctx context.Context) error {
	head := hookHead([]any{"SETCHAN", cmd.name}, cmd.meta, cmd.ex)
	if _, err := cmd.c.do(ctx, cmd.buildFence(head)...); err != nil {
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

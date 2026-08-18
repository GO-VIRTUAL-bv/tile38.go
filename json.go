// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package tile38

import (
	"context"
	"fmt"
)

// JSetCmd builds a Tile38 JSET command to set a value at a JSON path.
type JSetCmd struct {
	c    *Client
	args []any
}

// Do executes: JSET collection id path value
func (cmd *JSetCmd) Do(ctx context.Context) error {
	if _, err := cmd.c.do(ctx, cmd.args...); err != nil {
		return fmt.Errorf("tile38: JSET: %w", err)
	}
	return nil
}

// JGetCmd builds a Tile38 JGET command to read a value at a JSON path.
type JGetCmd struct {
	c    *Client
	args []any
}

// Do executes: JGET collection id path — returns the JSON value at the path.
func (cmd *JGetCmd) Do(ctx context.Context) (string, error) {
	val, err := cmd.c.do(ctx, cmd.args...)
	if err != nil {
		return "", fmt.Errorf("tile38: JGET: %w", err)
	}
	s, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("tile38: JGET: unexpected response type %T", val)
	}
	return s, nil
}

// JDelCmd builds a Tile38 JDEL command to delete a value at a JSON path.
type JDelCmd struct {
	c    *Client
	args []any
}

// Do executes: JDEL collection id path
func (cmd *JDelCmd) Do(ctx context.Context) error {
	if _, err := cmd.c.do(ctx, cmd.args...); err != nil {
		return fmt.Errorf("tile38: JDEL: %w", err)
	}
	return nil
}

// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package tile38

import (
	"context"
	"fmt"
	"time"
)

// DropCmd builds a Tile38 DROP command.
type DropCmd struct {
	c    *Client
	args []any
}

// Do executes: DROP collection
func (cmd *DropCmd) Do(ctx context.Context) error {
	if _, err := cmd.c.do(ctx, cmd.args...); err != nil {
		return fmt.Errorf("tile38: DROP: %w", err)
	}
	return nil
}

// PDelCmd builds a Tile38 PDEL command to delete objects matching a glob pattern.
type PDelCmd struct {
	c    *Client
	args []any
}

// Do executes: PDEL collection pattern
func (cmd *PDelCmd) Do(ctx context.Context) error {
	if _, err := cmd.c.do(ctx, cmd.args...); err != nil {
		return fmt.Errorf("tile38: PDEL: %w", err)
	}
	return nil
}

// RenameCmd builds a Tile38 RENAME command.
type RenameCmd struct {
	c    *Client
	args []any
}

// NX switches the command to RENAMENX, which only renames if the destination does not exist.
func (cmd *RenameCmd) NX() *RenameCmd {
	cmd.args[0] = "RENAMENX"
	return cmd
}

// Do executes: RENAME collection newCollection (or RENAMENX after calling NX)
func (cmd *RenameCmd) Do(ctx context.Context) error {
	if _, err := cmd.c.do(ctx, cmd.args...); err != nil {
		return fmt.Errorf("tile38: RENAME: %w", err)
	}
	return nil
}

// ExpireCmd builds a Tile38 EXPIRE command.
type ExpireCmd struct {
	c    *Client
	args []any
}

// Do executes: EXPIRE collection id seconds
func (cmd *ExpireCmd) Do(ctx context.Context) error {
	if _, err := cmd.c.do(ctx, cmd.args...); err != nil {
		return fmt.Errorf("tile38: EXPIRE: %w", err)
	}
	return nil
}

// PersistCmd builds a Tile38 PERSIST command to remove the TTL from an object.
type PersistCmd struct {
	c    *Client
	args []any
}

// Do executes: PERSIST collection id
func (cmd *PersistCmd) Do(ctx context.Context) error {
	if _, err := cmd.c.do(ctx, cmd.args...); err != nil {
		return fmt.Errorf("tile38: PERSIST: %w", err)
	}
	return nil
}

// TTLCmd builds a Tile38 TTL command.
type TTLCmd struct {
	c    *Client
	args []any
}

// Do executes: TTL collection id
// Returns the remaining TTL as a Duration, or -1 if the object has no expiry.
// Returns an error if the object does not exist.
func (cmd *TTLCmd) Do(ctx context.Context) (time.Duration, error) {
	val, err := cmd.c.do(ctx, cmd.args...)
	if err != nil {
		return 0, fmt.Errorf("tile38: TTL: %w", err)
	}
	secs, err := toInt64("TTL", val)
	if err != nil {
		return 0, err
	}
	if secs == -2 {
		return 0, fmt.Errorf("tile38: TTL: object not found")
	}
	if secs == -1 {
		return -1, nil // sentinel: object exists but has no expiry
	}
	return time.Duration(secs) * time.Second, nil
}

// ExistsCmd builds a Tile38 EXISTS command.
type ExistsCmd struct {
	c    *Client
	args []any
}

// Do executes: EXISTS collection id
func (cmd *ExistsCmd) Do(ctx context.Context) (bool, error) {
	val, err := cmd.c.do(ctx, cmd.args...)
	if err != nil {
		return false, fmt.Errorf("tile38: EXISTS: %w", err)
	}
	n, err := toInt64("EXISTS", val)
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// KeysCmd builds a Tile38 KEYS command to list collection names.
type KeysCmd struct {
	c    *Client
	args []any
}

// Do executes: KEYS pattern — returns collection names matching the glob pattern.
func (cmd *KeysCmd) Do(ctx context.Context) ([]string, error) {
	val, err := cmd.c.do(ctx, cmd.args...)
	if err != nil {
		return nil, fmt.Errorf("tile38: KEYS: %w", err)
	}
	return parseStringSlice("KEYS", val)
}

// BoundsCmd builds a Tile38 BOUNDS command to get the bounding box of a collection.
type BoundsCmd struct {
	c    *Client
	args []any
}

// Do executes: BOUNDS collection — returns the SW and NE corners of the collection's extent.
func (cmd *BoundsCmd) Do(ctx context.Context) (BoundsResult, error) {
	val, err := cmd.c.do(ctx, cmd.args...)
	if err != nil {
		return BoundsResult{}, fmt.Errorf("tile38: BOUNDS: %w", err)
	}
	return parseBoundsResult("BOUNDS", val, true)
}

// DBSizeCmd reads the total object count from SERVER.
type DBSizeCmd struct {
	c    *Client
	args []any
}

// Do executes: SERVER — returns num_objects, the total across all collections.
func (cmd *DBSizeCmd) Do(ctx context.Context) (int64, error) {
	val, err := cmd.c.do(ctx, cmd.args...)
	if err != nil {
		return 0, fmt.Errorf("tile38: SERVER: %w", err)
	}
	field, err := lookupKV("SERVER", val, "num_objects")
	if err != nil {
		return 0, err
	}
	return toInt64("SERVER num_objects", field)
}

// FlushDBCmd builds a Tile38 FLUSHDB command.
type FlushDBCmd struct {
	c    *Client
	args []any
}

// Do executes: FLUSHDB — deletes all objects and collections.
func (cmd *FlushDBCmd) Do(ctx context.Context) error {
	if _, err := cmd.c.do(ctx, cmd.args...); err != nil {
		return fmt.Errorf("tile38: FLUSHDB: %w", err)
	}
	return nil
}

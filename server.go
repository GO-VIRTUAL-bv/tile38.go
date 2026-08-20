// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package tile38

import (
	"context"
	"fmt"
)

// Tile38 spells READONLY's off switch and the detach form of FOLLOW — "FOLLOW no
// one" — with the same lowercase token. It is named here because it is the one
// argument keyword this package repeats; the uppercase command keywords stay
// spelled out at each call site, where they show the command being emitted.
const (
	tokenYes = "yes"
	tokenNo  = "no"
)

// StatusCmd is a command that takes no chained options and answers with a
// status: its entry point fixes every argument. Commands that do take options
// get a type of their own, so that a builder's methods always describe what that
// one command accepts.
type StatusCmd struct {
	c    *Client
	name string
	args []any
}

// Do executes the command.
func (cmd *StatusCmd) Do(ctx context.Context) error {
	if _, err := cmd.c.do(ctx, cmd.args...); err != nil {
		return fmt.Errorf("tile38: %s: %w", cmd.name, err)
	}
	return nil
}

// GC starts building a Tile38 GC command, which forces a garbage collection.
func (c *Client) GC() *StatusCmd {
	return &StatusCmd{c: c, name: "GC", args: []any{"GC"}}
}

// Healthz starts building a Tile38 HEALTHZ command: a liveness check that takes
// no read lock, so it answers even while the server is busy. It is the one
// command besides AUTH and OUTPUT that needs no authentication.
func (c *Client) Healthz() *StatusCmd {
	return &StatusCmd{c: c, name: "HEALTHZ", args: []any{"HEALTHZ"}}
}

// AOFShrink starts building a Tile38 AOFSHRINK command, which rewrites the
// append-only file in the background.
func (c *Client) AOFShrink() *StatusCmd {
	return &StatusCmd{c: c, name: "AOFSHRINK", args: []any{"AOFSHRINK"}}
}

// ReadOnly starts building a Tile38 READONLY command, turning read-only mode on
// or off. In read-only mode the server rejects every write.
func (c *Client) ReadOnly(on bool) *StatusCmd {
	mode := tokenNo
	if on {
		mode = tokenYes
	}
	return &StatusCmd{c: c, name: "READONLY", args: []any{"READONLY", mode}}
}

// Follow starts building a Tile38 FOLLOW command, making this server a replica
// of the one at host:port. Use FollowNone to stop.
func (c *Client) Follow(host string, port int) *StatusCmd {
	return &StatusCmd{c: c, name: "FOLLOW", args: []any{"FOLLOW", host, port}}
}

// FollowNone starts building "FOLLOW no one", which promotes a replica back to a
// leader. Tile38 spells it as a host of "no" and a port of "one".
func (c *Client) FollowNone() *StatusCmd {
	return &StatusCmd{c: c, name: "FOLLOW", args: []any{"FOLLOW", tokenNo, "one"}}
}

// ConfigSet starts building a Tile38 CONFIG SET command. The change applies
// immediately but is lost on restart unless ConfigRewrite persists it.
func (c *Client) ConfigSet(parameter, value string) *StatusCmd {
	return &StatusCmd{c: c, name: "CONFIG SET", args: []any{"CONFIG", "SET", parameter, value}}
}

// ConfigRewrite starts building a Tile38 CONFIG REWRITE command, which writes the
// running configuration back to the config file.
func (c *Client) ConfigRewrite() *StatusCmd {
	return &StatusCmd{c: c, name: "CONFIG REWRITE", args: []any{"CONFIG", "REWRITE"}}
}

// ConfigGetCmd builds a Tile38 CONFIG GET command.
type ConfigGetCmd struct {
	c    *Client
	args []any
}

// ConfigGet starts building a Tile38 CONFIG GET command for one parameter.
func (c *Client) ConfigGet(parameter string) *ConfigGetCmd {
	return &ConfigGetCmd{c: c, args: []any{"CONFIG", "GET", parameter}}
}

// Do executes: CONFIG GET parameter — returns the value as text. An unset
// parameter reads as the empty string rather than an error, which is how Tile38
// reports it.
func (cmd *ConfigGetCmd) Do(ctx context.Context) (string, error) {
	val, err := cmd.c.do(ctx, cmd.args...)
	if err != nil {
		return "", fmt.Errorf("tile38: CONFIG GET: %w", err)
	}
	name, _ := cmd.args[2].(string)
	value, err := lookupKV("CONFIG GET", val, name)
	if err != nil {
		return "", err
	}
	return toString("CONFIG GET", value)
}

// StatsCmd builds a Tile38 STATS command.
type StatsCmd struct {
	c    *Client
	keys []string
}

// Stats starts building a Tile38 STATS command for one or more collections.
func (c *Client) Stats(collections ...string) *StatsCmd {
	return &StatsCmd{c: c, keys: collections}
}

// Do executes: STATS collection… — one CollectionStats per collection asked for,
// in the same order. A collection that does not exist comes back as a null
// element, which reads as Exists false rather than as an error.
func (cmd *StatsCmd) Do(ctx context.Context) ([]CollectionStats, error) {
	args := make([]any, 0, len(cmd.keys)+1)
	args = append(args, "STATS")
	for _, k := range cmd.keys {
		args = append(args, k)
	}
	val, err := cmd.c.do(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("tile38: STATS: %w", err)
	}
	outer, ok := val.([]any)
	if !ok {
		return nil, fmt.Errorf("tile38: STATS: %w: response shape %T", ErrUnexpectedReply, val)
	}
	out := make([]CollectionStats, 0, len(outer))
	for i, item := range outer {
		stats := CollectionStats{}
		if i < len(cmd.keys) {
			stats.Key = cmd.keys[i]
		}
		if item == nil {
			out = append(out, stats)
			continue
		}
		stats.Exists = true
		for _, f := range []struct {
			name string
			dst  *int64
		}{
			{"in_memory_size", &stats.InMemorySize},
			{"num_objects", &stats.NumObjects},
			{"num_points", &stats.NumPoints},
			{"num_strings", &stats.NumStrings},
		} {
			value, err := lookupKV("STATS", item, f.name)
			if err != nil {
				return nil, err
			}
			if *f.dst, err = toInt64("STATS "+f.name, value); err != nil {
				return nil, err
			}
		}
		out = append(out, stats)
	}
	return out, nil
}

// Timeout runs one raw command with a server-side time limit, matching Tile38's
// TIMEOUT keyword: the server abandons the command after seconds and answers
// with an error. That is a different guarantee from a context deadline, which
// only stops this client waiting.
//
// It returns the decoded reply of the wrapped command, as Do does.
func (c *Client) Timeout(ctx context.Context, seconds float64, args ...any) (any, error) {
	val, err := c.do(ctx, append([]any{"TIMEOUT", seconds}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("tile38: TIMEOUT: %w", err)
	}
	return val, nil
}

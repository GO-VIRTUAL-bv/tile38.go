// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package tile38 is a client for the Tile38 geospatial database.
//
// It speaks RESP over TCP directly rather than through a Redis library, which
// is what makes live geofences possible: adding Fence to a search turns the
// connection into a stream of events instead of a single reply.
//
// The package has no dependencies outside the standard library.
package tile38

import (
	"context"
	"errors"
	"time"

	"github.com/GO-VIRTUAL-bv/tile38.go/internal/conn"
	"github.com/GO-VIRTUAL-bv/tile38.go/internal/resp"
)

// ServerError is an error reply from Tile38 ("-ERR ..."). It means the command
// was rejected, not that the connection broke.
type ServerError = resp.Error

// ErrClosed is returned when a command is issued on a closed Client.
var ErrClosed = conn.ErrClosed

// DefaultTimeout bounds a single command when WithTimeout is not given.
const DefaultTimeout = conn.DefaultTimeout

// ErrTruncated reports that a search returned only part of what matched: Tile38
// stopped at the limit and more objects remain.
//
// This is easy to hit unknowingly, because Tile38 caps every search output
// except COUNT at 100 results when the command carries no LIMIT (limitItems in
// internal/server/scanner.go). A query that is correct against a small
// collection therefore starts silently dropping results as that collection
// grows, which is what this error exists to prevent.
//
// The results returned alongside it are valid, just incomplete. To take
// everything, range Iter instead of calling Do — it follows the cursor itself
// and never reports this error:
//
//	for id, err := range c.Scan("fleet").Iter(ctx) {
//		if err != nil {
//			return err
//		}
//		use(id)
//	}
//
// Cursor and NextCursor are still there for driving the paging by hand. Or set
// an explicit Limit to say the cap is intended — an explicit Limit or Cursor
// silences this error, since then the bound is the caller's own.
var ErrTruncated = errors.New("tile38: result truncated, more objects match")

// Option configures a Client. Pass any number of them to New.
type Option func(*options)

type options struct {
	password    string
	maxIdle     int
	maxActive   int
	dialTimeout time.Duration
	timeout     time.Duration
}

// WithPassword sends AUTH on each new connection.
func WithPassword(password string) Option {
	return func(o *options) { o.password = password }
}

// WithMaxIdle caps the connections kept for reuse. It bounds idle connections,
// not in-flight ones — use WithMaxActive to bound those. Defaults to 8.
func WithMaxIdle(n int) Option {
	return func(o *options) { o.maxIdle = n }
}

// WithMaxActive caps how many commands may be in flight at once. Beyond that,
// callers wait for a slot rather than opening another connection, which is what
// keeps a burst of concurrent commands from opening a socket per goroutine.
// Zero, the default, leaves it uncapped. Streams are not counted: they hold a
// dedicated connection for as long as they run.
func WithMaxActive(n int) Option {
	return func(o *options) { o.maxActive = n }
}

// WithDialTimeout caps how long opening a connection may take. Defaults to 5s.
func WithDialTimeout(d time.Duration) Option {
	return func(o *options) { o.dialTimeout = d }
}

// WithTimeout sets a deadline for every command. Whichever of this and the
// call's context expires first wins. Unset, it defaults to DefaultTimeout, so a
// command against a wedged server fails instead of hanging forever when the
// context carries no deadline of its own. Pass a negative duration to opt out
// and rely on the context alone.
//
// It does not apply to streams, which have no read deadline: a quiet fence may
// legitimately send nothing for hours.
func WithTimeout(d time.Duration) Option {
	return func(o *options) { o.timeout = d }
}

// Client is a Tile38 client. It is safe for concurrent use.
//
// Commands take a connection from a small idle pool. Streaming calls (Fence,
// Subscribe, PSubscribe) get a dedicated connection that never returns to the
// pool, because the server keeps writing to it.
//
// The zero value is not usable; build one with New.
type Client struct {
	pool *conn.Pool
}

// New creates a Client for addr, the "host:port" of a Tile38 server. No
// connection is made until the first command.
func New(addr string, opts ...Option) *Client {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return &Client{pool: conn.NewPool(conn.Config{
		Addr:        addr,
		Password:    o.password,
		MaxIdle:     o.maxIdle,
		MaxActive:   o.maxActive,
		DialTimeout: o.dialTimeout,
		Timeout:     o.timeout,
	})}
}

// Ping verifies connectivity. Call at startup.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.do(ctx, "PING")
	return err
}

// Close closes all pooled connections. Streams opened from this Client are not
// affected; close those with their own Close.
func (c *Client) Close() error { return c.pool.Close() }

// Do runs a raw Tile38 command and returns the decoded reply — an escape hatch
// for commands this package does not model. Replies decode to string, int64,
// []any, or nil.
func (c *Client) Do(ctx context.Context, args ...any) (any, error) {
	return c.do(ctx, args...)
}

func (c *Client) do(ctx context.Context, args ...any) (any, error) {
	return c.pool.Do(ctx, args...)
}

func (c *Client) doPipeline(ctx context.Context, cmds [][]any) error {
	return c.pool.DoPipeline(ctx, cmds)
}

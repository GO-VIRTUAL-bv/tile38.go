// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package tile38

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/GO-VIRTUAL-bv/tile38.go/internal/conn"
)

// Stream is a long-lived connection that receives geofence notifications as
// they happen: either a live geofence opened with Fence on a search command, or
// a pub/sub subscription to channels registered with SETCHAN.
//
// A Stream owns a dedicated connection and no read deadline, because a quiet
// fence may send nothing for hours. Stop it with Close or by cancelling the
// context it was created with.
//
// A Stream is not safe for concurrent use; call Next from one goroutine.
type Stream struct {
	ctx    context.Context
	cn     *conn.Conn
	stop   func() bool
	once   sync.Once
	closed atomic.Bool
}

func newStream(ctx context.Context, cn *conn.Conn) *Stream {
	s := &Stream{ctx: ctx, cn: cn}
	s.stop = context.AfterFunc(ctx, func() { _ = cn.Close() })
	return s
}

// Next blocks until the next event arrives. It returns io.EOF after Close, and
// the context error if the stream's context is cancelled. Any other error wraps
// ErrDisconnected: the connection failed. The Stream is spent either way.
//
// Reopening a dropped fence is the caller's job — an outer loop around Fence,
// with whatever backoff and attempt cap the application wants. See
// ExampleStream_reconnect.
func (s *Stream) Next() (*FenceEvent, error) {
	for {
		v, err := s.cn.ReadReply()
		if err != nil {
			if s.closed.Load() {
				return nil, io.EOF
			}
			if ctxErr := s.ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			// %v, not %w: a read that ends at EOF would otherwise make a drop
			// satisfy errors.Is(err, io.EOF), which Next promises means Close.
			return nil, fmt.Errorf("tile38: stream: %w: %v", ErrDisconnected, err)
		}
		switch m := v.(type) {
		case string:
			// Live geofence: each event is a bulk string holding the JSON.
			return DecodeFenceEvent([]byte(m))
		case []any:
			// Pub/sub: skip subscribe/unsubscribe acks and PONGs.
			payload, ok := pubsubPayload(m)
			if !ok {
				continue
			}
			return DecodeFenceEvent([]byte(payload))
		default:
			return nil, fmt.Errorf("tile38: stream: %w: message type %T", ErrUnexpectedReply, v)
		}
	}
}

// Close stops the stream and releases its connection. It is safe to call more
// than once, and unblocks a concurrent Next.
func (s *Stream) Close() error {
	var err error
	s.once.Do(func() {
		s.closed.Store(true)
		s.stop()
		err = s.cn.Close()
	})
	return err
}

// pubsubPayload extracts the message body from a pub/sub push, reporting false
// for anything that is not a message (acks, PONG).
func pubsubPayload(m []any) (string, bool) {
	if len(m) < 3 {
		return "", false
	}
	kind, _ := m[0].(string)
	switch kind {
	case "message": // [message, channel, payload]
		s, ok := m[2].(string)
		return s, ok
	case "pmessage": // [pmessage, pattern, channel, payload]
		if len(m) < 4 {
			return "", false
		}
		s, ok := m[3].(string)
		return s, ok
	}
	return "", false
}

// openStream dials a dedicated connection, sends args, and returns the first
// reply along with the live connection.
func (c *Client) openStream(ctx context.Context, args []any) (*conn.Conn, any, error) {
	if c.pool.Closed() {
		return nil, nil, ErrClosed
	}
	cn, err := c.pool.Dial(ctx)
	if err != nil {
		return nil, nil, err
	}
	// The acknowledgement is one ordinary command, so it gets the handshake
	// deadline Dial gives AUTH: a server that completes the TCP handshake and
	// then never answers would otherwise hang here forever. Everything after the
	// ack is a stream, which wants no deadline at all, so it comes off again.
	cn.SetDeadline(ctx, c.pool.DialTimeout())
	v, err := cn.Do(args)
	if err != nil {
		_ = cn.Close()
		return nil, nil, err
	}
	cn.SetDeadline(context.Background(), 0)
	return cn, v, nil
}

// fenceStream opens a live geofence. Tile38 answers the fence command with +OK
// and then streams events on the same connection.
func (c *Client) fenceStream(ctx context.Context, args []any) (*Stream, error) {
	cn, v, err := c.openStream(ctx, args)
	if err != nil {
		return nil, fmt.Errorf("tile38: FENCE: %w", err)
	}
	if s, ok := v.(string); !ok || s != "OK" {
		_ = cn.Close()
		return nil, fmt.Errorf("tile38: FENCE: %w: %v", ErrUnexpectedReply, v)
	}
	return newStream(ctx, cn), nil
}

// Subscribe opens a stream of events from the given SETCHAN channels.
func (c *Client) Subscribe(ctx context.Context, channels ...string) (*Stream, error) {
	return c.subscribe(ctx, "SUBSCRIBE", channels)
}

// PSubscribe opens a stream of events from every SETCHAN channel matching the
// given glob patterns.
func (c *Client) PSubscribe(ctx context.Context, patterns ...string) (*Stream, error) {
	return c.subscribe(ctx, "PSUBSCRIBE", patterns)
}

func (c *Client) subscribe(ctx context.Context, command string, names []string) (*Stream, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("tile38: %s: no channels given", command)
	}
	args := make([]any, 0, len(names)+1)
	args = append(args, command)
	for _, n := range names {
		args = append(args, n)
	}
	// Only the first ack is read here; Next skips the rest.
	cn, v, err := c.openStream(ctx, args)
	if err != nil {
		return nil, fmt.Errorf("tile38: %s: %w", command, err)
	}
	if _, ok := v.([]any); !ok {
		_ = cn.Close()
		return nil, fmt.Errorf("tile38: %s: %w: %v", command, ErrUnexpectedReply, v)
	}
	return newStream(ctx, cn), nil
}

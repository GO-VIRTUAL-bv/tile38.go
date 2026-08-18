// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package conn holds the Tile38 transport: a single connection, and a pool of
// idle ones for request/response commands.
//
// Streaming commands (live geofences, subscriptions) take a connection from
// Dial and never give it back, because the server keeps writing to it.
package conn

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/GO-VIRTUAL-bv/tile38.go/internal/resp"
)

// ErrClosed is returned when a command is issued on a closed pool.
var ErrClosed = errors.New("tile38: client is closed")

// DefaultTimeout bounds a single command when none was configured.
const DefaultTimeout = 30 * time.Second

// Config is the transport half of the parent package's Options.
type Config struct {
	Addr        string
	Password    string
	MaxIdle     int
	MaxActive   int
	DialTimeout time.Duration
	Timeout     time.Duration
}

// Conn is a single Tile38 connection: one write buffer, one buffered reader.
type Conn struct {
	nc   net.Conn
	r    *bufio.Reader
	wbuf []byte
}

// maxRetainedBuf caps the write buffer a connection keeps between commands. A
// single oversized SET would otherwise pin its buffer for the life of a pooled
// connection, so anything larger is dropped and reallocated next time.
const maxRetainedBuf = 64 << 10

// Do writes one command and reads one reply.
func (cn *Conn) Do(args []any) (any, error) {
	buf, err := resp.AppendCommand(cn.wbuf[:0], args)
	if err != nil {
		return nil, err
	}
	if cap(buf) <= maxRetainedBuf {
		cn.wbuf = buf
	} else {
		cn.wbuf = nil
	}
	if _, err := cn.nc.Write(buf); err != nil {
		return nil, err
	}
	return resp.ReadReply(cn.r)
}

// ReadReply reads one reply without writing anything, which is how a stream
// consumes the events the server pushes after the initial command.
func (cn *Conn) ReadReply() (any, error) { return resp.ReadReply(cn.r) }

// Close closes the underlying connection.
func (cn *Conn) Close() error { return cn.nc.Close() }

// SetDeadline applies whichever of the context deadline and timeout expires
// first. Zero timeout plus a deadline-free context means no deadline at all,
// which is what a long-lived stream wants.
func (cn *Conn) SetDeadline(ctx context.Context, timeout time.Duration) {
	dl, ok := ctx.Deadline()
	if timeout > 0 {
		if t := time.Now().Add(timeout); !ok || t.Before(dl) {
			dl, ok = t, true
		}
	}
	if !ok {
		dl = time.Time{}
	}
	_ = cn.nc.SetDeadline(dl)
}

// Pool hands out connections for request/response commands and keeps a bounded
// number of idle ones for reuse. MaxIdle caps idle connections, not in-flight
// ones, so concurrent callers never block on each other.
type Pool struct {
	cfg    Config
	dialer net.Dialer
	sem    chan struct{} // in-flight slots; nil when MaxActive is unset

	mu     sync.Mutex
	idle   []*Conn
	closed bool
}

// NewPool creates a pool. No connection is made until the first command.
func NewPool(cfg Config) *Pool {
	if cfg.MaxIdle <= 0 {
		cfg.MaxIdle = 8
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 5 * time.Second
	}
	// An unset timeout plus a context without a deadline means no deadline at
	// all, so a wedged server hangs the caller forever. Default to a ceiling far
	// above any healthy command; a negative value opts back out explicitly.
	switch {
	case cfg.Timeout == 0:
		cfg.Timeout = DefaultTimeout
	case cfg.Timeout < 0:
		cfg.Timeout = 0
	}
	p := &Pool{cfg: cfg, dialer: net.Dialer{Timeout: cfg.DialTimeout}}
	if cfg.MaxActive > 0 {
		p.sem = make(chan struct{}, cfg.MaxActive)
	}
	return p
}

// acquire takes an in-flight slot, blocking until one frees up or ctx is done.
// It governs the pooled command path only: streams take their connection from
// Dial and hold it for hours, so counting them would starve everything else.
func (p *Pool) acquire(ctx context.Context) error {
	if p.sem == nil {
		return nil
	}
	select {
	case p.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Pool) release() {
	if p.sem != nil {
		<-p.sem
	}
}

// Close closes every idle connection. Connections handed out by Dial are not
// affected.
func (p *Pool) Close() error {
	p.mu.Lock()
	idle := p.idle
	p.idle, p.closed = nil, true
	p.mu.Unlock()
	for _, cn := range idle {
		_ = cn.Close()
	}
	return nil
}

// Closed reports whether Close has been called.
func (p *Pool) Closed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// Dial opens a connection that does not belong to the pool. Callers own it and
// must close it — this is how streams get a connection of their own.
func (p *Pool) Dial(ctx context.Context) (*Conn, error) {
	nc, err := p.dialer.DialContext(ctx, "tcp", p.cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("tile38: dial %s: %w", p.cfg.Addr, err)
	}
	cn := &Conn{nc: nc, r: bufio.NewReader(nc)}
	if p.cfg.Password != "" {
		cn.SetDeadline(ctx, p.cfg.DialTimeout)
		if _, err := cn.Do([]any{"AUTH", p.cfg.Password}); err != nil {
			_ = cn.Close()
			return nil, fmt.Errorf("tile38: auth: %w", err)
		}
	}
	return cn, nil
}

// ponytail: idle conns are handed out without a liveness check. Tile38 does not
// reap idle clients and Go enables TCP keepalives, so a stale conn surfaces as
// a normal I/O error. Add a retry-once-on-reused-conn path if that shows up.
func (p *Pool) get(ctx context.Context) (*Conn, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrClosed
	}
	if n := len(p.idle); n > 0 {
		cn := p.idle[n-1]
		p.idle = p.idle[:n-1]
		p.mu.Unlock()
		return cn, nil
	}
	p.mu.Unlock()
	return p.Dial(ctx)
}

func (p *Pool) put(cn *Conn) {
	p.mu.Lock()
	if !p.closed && len(p.idle) < p.cfg.MaxIdle {
		p.idle = append(p.idle, cn)
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	_ = cn.Close()
}

// Do runs one command on a pooled connection and returns the decoded reply.
func (p *Pool) Do(ctx context.Context, args ...any) (any, error) {
	if err := p.acquire(ctx); err != nil {
		return nil, err
	}
	defer p.release()
	cn, err := p.get(ctx)
	if err != nil {
		return nil, err
	}
	v, err := p.roundtrip(ctx, cn, args)
	if err != nil && !isServerError(err) {
		// Network or protocol failure: the connection is no longer in a known
		// state, so drop it instead of poisoning the pool.
		_ = cn.Close()
		return nil, err
	}
	p.put(cn)
	return v, err
}

// roundtrip writes the command and reads the reply, closing the connection if
// ctx is cancelled mid-flight so the blocked read returns.
func (p *Pool) roundtrip(ctx context.Context, cn *Conn, args []any) (any, error) {
	cn.SetDeadline(ctx, p.cfg.Timeout)
	stop := context.AfterFunc(ctx, func() { _ = cn.Close() })
	v, err := cn.Do(args)
	if !stop() {
		return nil, ctx.Err()
	}
	return v, err
}

// DoPipeline writes every command in one batch, then drains the replies. It
// returns the first command error; a transport failure aborts the batch.
func (p *Pool) DoPipeline(ctx context.Context, cmds [][]any) error {
	buf := []byte{}
	for _, args := range cmds {
		var err error
		if buf, err = resp.AppendCommand(buf, args); err != nil {
			return err
		}
	}
	if err := p.acquire(ctx); err != nil {
		return err
	}
	defer p.release()
	cn, err := p.get(ctx)
	if err != nil {
		return err
	}
	cn.SetDeadline(ctx, p.cfg.Timeout)
	stop := context.AfterFunc(ctx, func() { _ = cn.Close() })

	var cmdErr error
	if _, err := cn.nc.Write(buf); err != nil {
		cmdErr = err
	} else {
		for range cmds {
			_, err := cn.ReadReply()
			if err == nil {
				continue
			}
			if !isServerError(err) {
				cmdErr = err
				break
			}
			if cmdErr == nil {
				cmdErr = err
			}
		}
	}

	if !stop() {
		_ = cn.Close()
		return ctx.Err()
	}
	if cmdErr != nil && !isServerError(cmdErr) {
		_ = cn.Close()
		return cmdErr
	}
	p.put(cn)
	return cmdErr
}

// isServerError reports whether err is a rejected command rather than a broken
// connection — the distinction that decides if a connection may be reused.
func isServerError(err error) bool {
	var se resp.Error
	return errors.As(err, &se)
}

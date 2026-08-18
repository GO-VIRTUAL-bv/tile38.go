// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package conn

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/GO-VIRTUAL-bv/tile38.go/internal/resp"
)

// serve accepts every connection until the listener closes and hands each to
// handle. It returns the address to dial.
func serve(t *testing.T, handle func(r *bufio.Reader, w net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				handle(bufio.NewReader(c), c)
			}()
		}
	}()
	return ln.Addr().String()
}

// readArgs decodes one command into its string arguments.
func readArgs(r *bufio.Reader) ([]string, error) {
	v, err := resp.ReadReply(r)
	if err != nil {
		return nil, err
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, errors.New("command is not an array")
	}
	args := make([]string, len(arr))
	for i, a := range arr {
		args[i], _ = a.(string)
	}
	return args, nil
}

func TestNewPoolDefaults(t *testing.T) {
	tests := map[string]struct {
		in   Config
		want Config
	}{
		"zero config gets every default": {
			in:   Config{},
			want: Config{MaxIdle: 8, DialTimeout: 5 * time.Second, Timeout: DefaultTimeout},
		},
		"explicit values are kept": {
			in:   Config{MaxIdle: 2, DialTimeout: time.Second, Timeout: time.Minute},
			want: Config{MaxIdle: 2, DialTimeout: time.Second, Timeout: time.Minute},
		},
		// A caller who genuinely wants to rely on the context alone says so with
		// a negative duration, which maps back to "no deadline".
		"a negative timeout opts out": {
			in:   Config{Timeout: -1},
			want: Config{MaxIdle: 8, DialTimeout: 5 * time.Second, Timeout: 0},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := NewPool(tc.in).cfg
			if got.MaxIdle != tc.want.MaxIdle {
				t.Errorf("MaxIdle = %d, want %d", got.MaxIdle, tc.want.MaxIdle)
			}
			if got.DialTimeout != tc.want.DialTimeout {
				t.Errorf("DialTimeout = %v, want %v", got.DialTimeout, tc.want.DialTimeout)
			}
			if got.Timeout != tc.want.Timeout {
				t.Errorf("Timeout = %v, want %v", got.Timeout, tc.want.Timeout)
			}
		})
	}
}

// A password makes every new connection authenticate before anything else goes
// out, including the connections streams dial for themselves.
func TestDialSendsAuth(t *testing.T) {
	got := make(chan []string, 2)
	addr := serve(t, func(r *bufio.Reader, w net.Conn) {
		for {
			args, err := readArgs(r)
			if err != nil {
				return
			}
			got <- args
			_, _ = io.WriteString(w, "+OK\r\n")
		}
	})

	p := NewPool(Config{Addr: addr, Password: "hunter2"})
	defer p.Close()

	if _, err := p.Do(t.Context(), "PING"); err != nil {
		t.Fatal(err)
	}
	if args := <-got; len(args) != 2 || args[0] != "AUTH" || args[1] != "hunter2" {
		t.Errorf("first command = %q, want AUTH hunter2", args)
	}
	if args := <-got; len(args) != 1 || args[0] != "PING" {
		t.Errorf("second command = %q, want PING", args)
	}
}

// The distinction that governs reuse: a -ERR reply is a rejected command on a
// healthy connection, anything else leaves the stream in an unknown state.
func TestConnectionReuseAfterError(t *testing.T) {
	tests := map[string]struct {
		reply       string
		wantErr     bool
		wantIdle    int
		wantSrvErr  bool
		description string
	}{
		"server error returns the connection": {
			reply: "-ERR invalid argument\r\n", wantErr: true, wantSrvErr: true, wantIdle: 1,
		},
		"protocol garbage drops the connection": {
			reply: "!bogus\r\n", wantErr: true, wantIdle: 0,
		},
		"success returns the connection": {
			reply: "+PONG\r\n", wantIdle: 1,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			addr := serve(t, func(r *bufio.Reader, w net.Conn) {
				for {
					if _, err := readArgs(r); err != nil {
						return
					}
					_, _ = io.WriteString(w, tc.reply)
				}
			})
			p := NewPool(Config{Addr: addr})
			defer p.Close()

			_, err := p.Do(t.Context(), "PING")
			if (err != nil) != tc.wantErr {
				t.Fatalf("Do = %v, wantErr %v", err, tc.wantErr)
			}
			var se resp.Error
			if tc.wantSrvErr && !errors.As(err, &se) {
				t.Errorf("Do = %v, want a resp.Error", err)
			}
			p.mu.Lock()
			idle := len(p.idle)
			p.mu.Unlock()
			if idle != tc.wantIdle {
				t.Errorf("idle = %d, want %d", idle, tc.wantIdle)
			}
		})
	}
}

// MaxIdle bounds what the pool keeps; connections beyond it are closed rather
// than accumulating.
func TestMaxIdleBoundsThePool(t *testing.T) {
	addr := serve(t, func(r *bufio.Reader, w net.Conn) {
		for {
			if _, err := readArgs(r); err != nil {
				return
			}
			_, _ = io.WriteString(w, "+PONG\r\n")
		}
	})
	p := NewPool(Config{Addr: addr, MaxIdle: 1})
	defer p.Close()

	// Hold two connections at once, then hand both back.
	a, err := p.get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	p.put(a)
	p.put(b)

	p.mu.Lock()
	idle := len(p.idle)
	p.mu.Unlock()
	if idle != 1 {
		t.Errorf("idle = %d, want 1 with MaxIdle(1)", idle)
	}
}

// A cancelled context must unblock a command waiting on a server that never
// answers, and must not leave the dead connection in the pool.
func TestContextCancelUnblocksCommand(t *testing.T) {
	addr := serve(t, func(_ *bufio.Reader, _ net.Conn) {
		<-time.After(5 * time.Second)
	})
	p := NewPool(Config{Addr: addr})
	defer p.Close()

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-time.After(50 * time.Millisecond)
		cancel()
	}()
	if _, err := p.Do(ctx, "PING"); !errors.Is(err, context.Canceled) {
		t.Errorf("Do = %v, want context.Canceled", err)
	}
	p.mu.Lock()
	idle := len(p.idle)
	p.mu.Unlock()
	if idle != 0 {
		t.Errorf("idle = %d, want the cancelled connection dropped", idle)
	}
}

// A batch writes every command before reading any reply, and reports the first
// command error while still draining the rest so the connection stays in sync.
func TestDoPipeline(t *testing.T) {
	got := make(chan []string, 3)
	addr := serve(t, func(r *bufio.Reader, w net.Conn) {
		for range 3 {
			args, err := readArgs(r)
			if err != nil {
				return
			}
			got <- args
		}
		_, _ = io.WriteString(w, "+OK\r\n-ERR bad\r\n+OK\r\n")
		// The connection must still be usable for a follow-up command.
		for {
			if _, err := readArgs(r); err != nil {
				return
			}
			_, _ = io.WriteString(w, "+PONG\r\n")
		}
	})
	p := NewPool(Config{Addr: addr})
	defer p.Close()

	err := p.DoPipeline(t.Context(), [][]any{
		{"SET", "k", "a"}, {"SET", "k", "b"}, {"SET", "k", "c"},
	})
	var se resp.Error
	if !errors.As(err, &se) {
		t.Fatalf("DoPipeline = %v, want a resp.Error", err)
	}
	for _, want := range []string{"a", "b", "c"} {
		if args := <-got; len(args) != 3 || args[2] != want {
			t.Errorf("command = %q, want id %q", args, want)
		}
	}
	// Every reply was drained, so the connection went back to the pool usable.
	if _, err := p.Do(t.Context(), "PING"); err != nil {
		t.Errorf("Do after pipeline = %v, want the connection reusable", err)
	}
}

// Commands issued after Close must fail rather than dialling again.
func TestClosedPoolRejectsCommands(t *testing.T) {
	p := NewPool(Config{Addr: "127.0.0.1:1"})
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if !p.Closed() {
		t.Error("Closed() = false after Close")
	}
	if _, err := p.Do(t.Context(), "PING"); !errors.Is(err, ErrClosed) {
		t.Errorf("Do = %v, want ErrClosed", err)
	}
}

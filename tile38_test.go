// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package tile38

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GO-VIRTUAL-bv/tile38.go/internal/resp"
)

// wire joins lines with CRLF, so wire fixtures stay readable.
func wire(lines ...string) string { return strings.Join(lines, "\r\n") + "\r\n" }

// fakeServer accepts one connection and hands it to handle. It returns the
// address to dial.
func fakeServer(t *testing.T, handle func(r *bufio.Reader, w net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		handle(bufio.NewReader(conn), conn)
	}()
	return ln.Addr().String()
}

// fakeServerN accepts every connection until the listener closes, serving each
// with handle, and reports how many it has accepted.
func fakeServerN(t *testing.T, handle func(r *bufio.Reader, w net.Conn)) (string, func() int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var n atomic.Int64
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			go func() {
				defer conn.Close()
				handle(bufio.NewReader(conn), conn)
			}()
		}
	}()
	return ln.Addr().String(), func() int { return int(n.Load()) }
}

// serveCommands answers every command with reply, holding each one for hold so
// concurrent callers actually overlap.
func serveCommands(r *bufio.Reader, w net.Conn, reply string, hold time.Duration) {
	for {
		if _, err := resp.ReadReply(r); err != nil {
			return
		}
		<-time.After(hold)
		if _, err := io.WriteString(w, reply); err != nil {
			return
		}
	}
}

// readCommand decodes one client command into its string arguments.
func readCommand(t *testing.T, r *bufio.Reader) []string {
	t.Helper()
	v, err := resp.ReadReply(r)
	if err != nil {
		t.Errorf("read command: %v", err)
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		t.Errorf("command is %T, want array", v)
		return nil
	}
	args := make([]string, len(arr))
	for i, a := range arr {
		args[i], _ = a.(string)
	}
	return args
}

// Covers argument encoding, output-format splicing, and array reply decoding.
func TestNearbyPoints(t *testing.T) {
	got := make(chan []string, 1)
	addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
		got <- readCommand(t, r)
		_, _ = io.WriteString(w, wire(
			"*2", ":1",
			"*1", "*2", "$6", "truck1",
			"*2", "$4", "33.5", "$6", "-115.5",
		))
	})

	c := New(addr)
	defer c.Close()

	res, err := c.Nearby("fleet").Limit(10).Point(33.5, -115.5).Radius(5000).Points(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"NEARBY", "fleet", "LIMIT", "10", "POINTS", "POINT", "33.5", "-115.5", "5000"}
	if cmd := <-got; !reflect.DeepEqual(cmd, want) {
		t.Errorf("command = %q, want %q", cmd, want)
	}
	wantRes := []NearbyResult{{ID: "truck1", Lat: 33.5, Lon: -115.5}}
	if !reflect.DeepEqual(res, wantRes) {
		t.Errorf("result = %+v, want %+v", res, wantRes)
	}
}

// Field values are handed to the RESP encoder untouched, so every type it knows
// how to render must survive the trip — bools as true/false, raw JSON verbatim.
func TestFieldValueEncoding(t *testing.T) {
	got := make(chan []string, 1)
	addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
		got <- readCommand(t, r)
		_, _ = io.WriteString(w, "+OK\r\n")
	})
	c := New(addr)
	defer c.Close()

	err := c.Set("fleet", "truck1").Fields(
		Field("active", true),
		Field("idle", false),
		Field("speed", 12.5),
		Field("meta", json.RawMessage(`{"a":1}`)),
	).Point(33.5, -115.5).Do(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"SET", "fleet", "truck1",
		"FIELD", "active", "true", "FIELD", "idle", "false",
		"FIELD", "speed", "12.5", "FIELD", "meta", `{"a":1}`,
		"POINT", "33.5", "-115.5"}
	if cmd := <-got; !reflect.DeepEqual(cmd, want) {
		t.Errorf("command = %q,\n           want %q", cmd, want)
	}
}

// Options chained after the geometry used to corrupt the command, because the
// output format was spliced in at a token offset. Every chain order must now
// produce the same bytes.
func TestChainOrderIsIrrelevant(t *testing.T) {
	// Options render in a canonical order regardless of chain order: repeatable
	// options in the order given, then LIMIT, then the format, then the geometry.
	want := []string{"NEARBY", "fleet", "WHERE", "speed > 5", "LIMIT", "10", "POINTS", "POINT", "33.5", "-115.5", "5000"}

	orders := map[string]func(*Client) *NearbyCmd{
		"options first": func(c *Client) *NearbyCmd {
			return c.Nearby("fleet").Limit(10).Where("speed > 5").Point(33.5, -115.5).Radius(5000)
		},
		"options last": func(c *Client) *NearbyCmd {
			return c.Nearby("fleet").Point(33.5, -115.5).Radius(5000).Limit(10).Where("speed > 5")
		},
		"interleaved": func(c *Client) *NearbyCmd {
			return c.Nearby("fleet").Point(33.5, -115.5).Limit(10).Radius(5000).Where("speed > 5")
		},
		"radius before at": func(c *Client) *NearbyCmd {
			return c.Nearby("fleet").Radius(5000).Limit(10).Point(33.5, -115.5).Where("speed > 5")
		},
	}

	for name, build := range orders {
		t.Run(name, func(t *testing.T) {
			got := make(chan []string, 1)
			addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
				got <- readCommand(t, r)
				_, _ = io.WriteString(w, wire("*2", ":0", "*0"))
			})
			c := New(addr)
			defer c.Close()

			if _, err := build(c).Points(t.Context()); err != nil {
				t.Fatal(err)
			}
			if cmd := <-got; !reflect.DeepEqual(cmd, want) {
				t.Errorf("command = %q,\n           want %q", cmd, want)
			}
		})
	}
}

// Tile38 rejects a repeated LIMIT/DETECT/COMMANDS, so calling those twice must
// overwrite rather than emit a duplicate. WHERE and MATCH legitimately
// accumulate and must keep doing so.
func TestRepeatedOptions(t *testing.T) {
	tests := map[string]struct {
		build func(*Client) error
		want  []string
	}{
		"limit twice keeps the last": {
			build: func(c *Client) error {
				_, err := c.Nearby("fleet").Limit(1).Limit(5).
					Point(33.5, -115.5).Radius(500).IDs(t.Context())
				return err
			},
			want: []string{"NEARBY", "fleet", "LIMIT", "5", "IDS", "POINT", "33.5", "-115.5", "500"},
		},
		"detect and commands twice keep the last": {
			build: func(c *Client) error {
				return c.SetChan("z").Within("fleet").
					Detect(Enter).Detect(Inside, Exit).
					Commands(CommandDel).Commands(CommandSet).
					Bounds(GlobalBounds()).Do(t.Context())
			},
			want: []string{"SETCHAN", "z", "WITHIN", "fleet", "FENCE",
				"DETECT", "inside,exit", "COMMANDS", "set",
				"BOUNDS", "-90", "-180", "90", "180"},
		},
		"scan renders its limit": {
			build: func(c *Client) error {
				_, err := c.Scan("fleet").Limit(50).IDs(t.Context())
				return err
			},
			want: []string{"SCAN", "fleet", "LIMIT", "50", "IDS"},
		},
		"single-use flags render once": {
			build: func(c *Client) error {
				_, err := c.Within("fleet").NoFields().NoFields().Clip().
					Sparse(2).Sparse(4).Bounds(GlobalBounds()).Objects(t.Context())
				return err
			},
			want: []string{"WITHIN", "fleet", "SPARSE", "4", "NOFIELDS", "CLIP",
				"OBJECTS", "BOUNDS", "-90", "-180", "90", "180"},
		},
		"wherein is length-prefixed and accumulates": {
			build: func(c *Client) error {
				_, err := c.Scan("fleet").
					WhereIn("kind", "truck", "van").
					WhereIn("zone", 1, 2, 3).IDs(t.Context())
				return err
			},
			want: []string{"SCAN", "fleet",
				"WHEREIN", "kind", "2", "truck", "van",
				"WHEREIN", "zone", "3", "1", "2", "3", "IDS"},
		},
		"where and match accumulate": {
			build: func(c *Client) error {
				_, err := c.Scan("fleet").
					Where("speed > 5").Where("fuel < 50").
					Match("truck:*").Match("van:*").IDs(t.Context())
				return err
			},
			want: []string{"SCAN", "fleet", "WHERE", "speed > 5", "WHERE", "fuel < 50",
				"MATCH", "truck:*", "MATCH", "van:*", "IDS"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := make(chan []string, 1)
			addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
				got <- readCommand(t, r)
				_, _ = io.WriteString(w, wire("*2", ":0", "*0"))
			})
			c := New(addr)
			defer c.Close()

			if err := tc.build(c); err != nil {
				t.Fatal(err)
			}
			if cmd := <-got; !reflect.DeepEqual(cmd, tc.want) {
				t.Errorf("command = %q,\n           want %q", cmd, tc.want)
			}
		})
	}
}

// Detect and Commands describe a fence. On a plain query they must be dropped
// rather than turning it into a live one the caller is not reading as a stream.
func TestDetectIgnoredWithoutFence(t *testing.T) {
	got := make(chan []string, 1)
	addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
		got <- readCommand(t, r)
		_, _ = io.WriteString(w, ":0\r\n") // COUNT replies with a bare integer
	})
	c := New(addr)
	defer c.Close()

	if _, err := c.Within("fleet").Detect(Enter).Commands(CommandSet).
		Bounds(GlobalBounds()).Count(t.Context()); err != nil {
		t.Fatal(err)
	}

	want := []string{"WITHIN", "fleet", "COUNT", "BOUNDS", "-90", "-180", "90", "180"}
	if cmd := <-got; !reflect.DeepEqual(cmd, want) {
		t.Errorf("command = %q, want %q", cmd, want)
	}
}

// Hook and channel builders assemble the same way as search builders, and
// GlobalBounds() must bind straight onto Bounds' four parameters.
func TestHookAssembly(t *testing.T) {
	tests := map[string]struct {
		build func(*Client) error
		want  []string
	}{
		"global bounds in any order": {
			build: func(c *Client) error {
				return c.SetChan("zone").Bounds(GlobalBounds()).Within("fleet").
					Commands(CommandSet).Detect(Inside).Do(t.Context())
			},
			want: []string{"SETCHAN", "zone", "WITHIN", "fleet", "FENCE",
				"DETECT", "inside", "COMMANDS", "set", "BOUNDS", "-90", "-180", "90", "180"},
		},
		"detect then area": {
			build: func(c *Client) error {
				return c.SetHook("h").Endpoint("http://x", "y").Within("fleet").
					Detect(Enter, Exit).Get("zones", "z1").Do(t.Context())
			},
			want: []string{"SETHOOK", "h", "http://x/y", "WITHIN", "fleet", "FENCE",
				"DETECT", "enter,exit", "GET", "zones", "z1"},
		},
		// SETHOOK takes the endpoint positionally, right after the name, so it
		// cannot be appended in call order the way an option can.
		"endpoint after trigger": {
			build: func(c *Client) error {
				return c.SetHook("h").Within("fleet").Detect(Enter, Exit).
					Endpoint("http://x", "y").Get("zones", "z1").Do(t.Context())
			},
			want: []string{"SETHOOK", "h", "http://x/y", "WITHIN", "fleet", "FENCE",
				"DETECT", "enter,exit", "GET", "zones", "z1"},
		},
		// Tile38 splits one endpoint token on commas, so several endpoints
		// register on the same hook.
		"multiple endpoints": {
			build: func(c *Client) error {
				return c.SetHook("h").
					EndpointURL("kafka://k:9092/events", "http://x/y?token=1").
					Within("fleet").Circle(1, 2, 3).Do(t.Context())
			},
			want: []string{"SETHOOK", "h", "kafka://k:9092/events,http://x/y?token=1",
				"WITHIN", "fleet", "FENCE", "CIRCLE", "1", "2", "3"},
		},
		// META and EX sit between the name/endpoint and the trigger.
		"meta and ex before the trigger": {
			build: func(c *Client) error {
				return c.SetChan("zone").Intersects("fleet").
					Meta("team", "ops").Meta("tier", "1").EX(3600).
					Circle(1, 2, 3).Do(t.Context())
			},
			want: []string{"SETCHAN", "zone", "META", "team", "ops", "META", "tier", "1",
				"EX", "3600", "INTERSECTS", "fleet", "FENCE", "CIRCLE", "1", "2", "3"},
		},
		"bare fence needs no detect": {
			build: func(c *Client) error {
				return c.SetChan("zone").Within("fleet").Circle(33.5, -115.5, 5000).Do(t.Context())
			},
			want: []string{"SETCHAN", "zone", "WITHIN", "fleet", "FENCE",
				"CIRCLE", "33.5", "-115.5", "5000"},
		},
		// NODWELL is opt-in: Roam used to force it, so a hook could never
		// report objects that stayed in range.
		"roam dwells by default": {
			build: func(c *Client) error {
				return c.SetChan("zone").Nearby("fleet").Roam("targets", 500).Do(t.Context())
			},
			want: []string{"SETCHAN", "zone", "NEARBY", "fleet", "FENCE",
				"ROAM", "targets", "*", "500"},
		},
		"roam with nodwell": {
			build: func(c *Client) error {
				return c.SetHook("h").Endpoint("http://x", "y").Nearby("fleet").
					NoDwell().Roam("targets", 500).Do(t.Context())
			},
			want: []string{"SETHOOK", "h", "http://x/y", "NEARBY", "fleet", "FENCE",
				"NODWELL", "ROAM", "targets", "*", "500"},
		},
		// NEARBY reads "POINT lat lon meters" and rejects CIRCLE, so a hook or
		// channel fencing on NEARBY needs its own POINT area — without one the
		// trigger cannot emit a command the server accepts at all.
		"nearby point area": {
			build: func(c *Client) error {
				return c.SetChan("zone").Nearby("fleet").Point(33.5, -115.5).Radius(5000).Do(t.Context())
			},
			want: []string{"SETCHAN", "zone", "NEARBY", "fleet", "FENCE",
				"POINT", "33.5", "-115.5", "5000"},
		},
		// The radius is the trailing argument of the area, so it has to attach at
		// exec time rather than in call order.
		"nearby point area, radius chained first": {
			build: func(c *Client) error {
				return c.SetHook("h").Radius(5000).Endpoint("http://x", "y").
					Detect(Enter).Nearby("fleet").Point(33.5, -115.5).Do(t.Context())
			},
			want: []string{"SETHOOK", "h", "http://x/y", "NEARBY", "fleet", "FENCE",
				"DETECT", "enter", "POINT", "33.5", "-115.5", "5000"},
		},
		// A ROAM area carries its own radius, so a stray Radius must not attach.
		"radius never attaches to roam": {
			build: func(c *Client) error {
				return c.SetChan("zone").Nearby("fleet").Radius(5000).Roam("targets", 500).Do(t.Context())
			},
			want: []string{"SETCHAN", "zone", "NEARBY", "fleet", "FENCE",
				"ROAM", "targets", "*", "500"},
		},
		// MATCH is an option on the trigger collection: it renders after WITHIN
		// and before the FENCE clause, whichever order it is chained in.
		"match filters the trigger before the fence": {
			build: func(c *Client) error {
				return c.SetHook("geofence:acme:g1").Endpoint("nats://n:4222", "app.sys.geofence.hook.detect").
					Meta("org", "acme").Meta("geofence_id", "g1").
					Within("devices").Match("acme:*").Detect(Enter, Exit).
					Object(`{"type":"Point"}`).Do(t.Context())
			},
			want: []string{"SETHOOK", "geofence:acme:g1", "nats://n:4222/app.sys.geofence.hook.detect",
				"META", "org", "acme", "META", "geofence_id", "g1",
				"WITHIN", "devices", "MATCH", "acme:*", "FENCE", "DETECT", "enter,exit",
				"OBJECT", `{"type":"Point"}`},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := make(chan []string, 1)
			addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
				got <- readCommand(t, r)
				_, _ = io.WriteString(w, "+OK\r\n")
			})
			c := New(addr)
			defer c.Close()

			if err := tc.build(c); err != nil {
				t.Fatal(err)
			}
			if cmd := <-got; !reflect.DeepEqual(cmd, tc.want) {
				t.Errorf("command = %q,\n           want %q", cmd, tc.want)
			}
		})
	}
}

// Covers the FENCE clause splice, live-message framing, and Close semantics.
func TestFenceStream(t *testing.T) {
	got := make(chan []string, 1)
	addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
		got <- readCommand(t, r)
		_, _ = io.WriteString(w, "+OK\r\n")
		for _, ev := range []string{
			`{"command":"set","detect":"enter","id":"truck1","hook":"h"}`,
			`{"command":"set","detect":"exit","id":"truck1","hook":"h"}`,
		} {
			_, _ = io.WriteString(w, wire("$"+strconv.Itoa(len(ev)), ev))
		}
		<-time.After(time.Second) // hold the connection open until Close
	})

	c := New(addr)
	defer c.Close()

	st, err := c.Nearby("fleet").Point(33.5, -115.5).Radius(5000).Detect(Enter, Exit).Fence(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"NEARBY", "fleet", "FENCE", "DETECT", "enter,exit", "POINT", "33.5", "-115.5", "5000"}
	if cmd := <-got; !reflect.DeepEqual(cmd, want) {
		t.Errorf("command = %q, want %q", cmd, want)
	}

	for _, wantDetect := range []string{"enter", "exit"} {
		ev, err := st.Next()
		if err != nil {
			t.Fatal(err)
		}
		if ev.Detect != wantDetect || ev.ID != "truck1" {
			t.Errorf("event = %+v, want detect %q", ev, wantDetect)
		}
	}

	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("Next after Close = %v, want io.EOF", err)
	}
}

// Tile38 caps every search at 100 results when no LIMIT is given and signals it
// by returning a non-zero cursor. Dropping that cursor is how a query silently
// starts returning partial results as a collection grows.
func TestSearchTruncation(t *testing.T) {
	// Two ids and a cursor of 2: the scan stopped early.
	truncated := wire("*2", ":2", "*2", "$2", "t1", "$2", "t2")
	complete := wire("*2", ":0", "*2", "$2", "t1", "$2", "t2")

	tests := map[string]struct {
		reply      string
		build      func(*Client) *ScanCmd
		wantErr    bool
		wantCursor uint64
		wantCmd    []string
	}{
		"non-zero cursor without a limit reports truncation": {
			reply:      truncated,
			build:      func(c *Client) *ScanCmd { return c.Scan("fleet") },
			wantErr:    true,
			wantCursor: 2,
			wantCmd:    []string{"SCAN", "fleet", "IDS"},
		},
		"zero cursor is a complete result": {
			reply:   complete,
			build:   func(c *Client) *ScanCmd { return c.Scan("fleet") },
			wantCmd: []string{"SCAN", "fleet", "IDS"},
		},
		"an explicit limit means the cap was asked for": {
			reply:      truncated,
			build:      func(c *Client) *ScanCmd { return c.Scan("fleet").Limit(2) },
			wantCursor: 2,
			wantCmd:    []string{"SCAN", "fleet", "LIMIT", "2", "IDS"},
		},
		"paging with a cursor is deliberate": {
			reply:      truncated,
			build:      func(c *Client) *ScanCmd { return c.Scan("fleet").Cursor(100) },
			wantCursor: 2,
			wantCmd:    []string{"SCAN", "fleet", "CURSOR", "100", "IDS"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := make(chan []string, 1)
			addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
				got <- readCommand(t, r)
				_, _ = io.WriteString(w, tc.reply)
			})
			c := New(addr)
			defer c.Close()

			cmd := tc.build(c)
			ids, err := cmd.IDs(t.Context())
			if tc.wantErr != errors.Is(err, ErrTruncated) {
				t.Errorf("err = %v, want ErrTruncated: %v", err, tc.wantErr)
			}
			// The partial results are valid and must come back either way.
			if want := []string{"t1", "t2"}; !reflect.DeepEqual(ids, want) {
				t.Errorf("ids = %q, want %q", ids, want)
			}
			if cmd.NextCursor() != tc.wantCursor {
				t.Errorf("NextCursor = %d, want %d", cmd.NextCursor(), tc.wantCursor)
			}
			if c := <-got; !reflect.DeepEqual(c, tc.wantCmd) {
				t.Errorf("command = %q, want %q", c, tc.wantCmd)
			}
		})
	}
}

// Tile38 rejects "CURSOR ... FENCE", so a cursor left on the builder must not
// reach the fence command.
func TestFenceDropsCursor(t *testing.T) {
	got := make(chan []string, 1)
	addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
		got <- readCommand(t, r)
		_, _ = io.WriteString(w, "+OK\r\n")
		<-time.After(time.Second)
	})
	c := New(addr)
	defer c.Close()

	st, err := c.Within("fleet").Cursor(50).Bounds(GlobalBounds()).Fence(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	want := []string{"WITHIN", "fleet", "FENCE", "BOUNDS", "-90", "-180", "90", "180"}
	if cmd := <-got; !reflect.DeepEqual(cmd, want) {
		t.Errorf("command = %q,\n           want %q", cmd, want)
	}
}

// COUNT replies with a bare integer. An array reply leads with the cursor, so
// accepting that shape would report the cursor as the count.
func TestCountRejectsArrayReply(t *testing.T) {
	for name, reply := range map[string]string{
		"bare integer": ":7\r\n",
		"cursor array": wire("*2", ":3", "*0"),
	} {
		t.Run(name, func(t *testing.T) {
			addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
				readCommand(t, r)
				_, _ = io.WriteString(w, reply)
			})
			c := New(addr)
			defer c.Close()

			n, err := c.Within("fleet").Bounds(GlobalBounds()).Count(t.Context())
			if name == "bare integer" {
				if err != nil || n != 7 {
					t.Errorf("Count = %d, %v; want 7, nil", n, err)
				}
				return
			}
			if err == nil {
				t.Errorf("Count = %d, nil; want an error for a cursor-shaped reply", n)
			}
		})
	}
}

// ROAM is only legal on a live NEARBY fence, and it carries its own radius —
// a Radius chained alongside it must not be appended a second time.
func TestRoamFence(t *testing.T) {
	got := make(chan []string, 1)
	addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
		got <- readCommand(t, r)
		_, _ = io.WriteString(w, "+OK\r\n")
		<-time.After(time.Second)
	})
	c := New(addr)
	defer c.Close()

	st, err := c.Nearby("fleet").Radius(500).NoDwell().
		Roam("targets", 250).Fence(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	want := []string{"NEARBY", "fleet", "FENCE", "NODWELL", "ROAM", "targets", "*", "250"}
	if cmd := <-got; !reflect.DeepEqual(cmd, want) {
		t.Errorf("command = %q,\n           want %q", cmd, want)
	}
}

// MaxIdle bounds only idle connections, so without MaxActive a burst of
// concurrent commands opens a socket per goroutine.
func TestMaxActiveBoundsConnections(t *testing.T) {
	tests := map[string]struct {
		opts   []Option
		verify func(t *testing.T, accepted int)
	}{
		"capped at one": {
			opts: []Option{WithMaxActive(1)},
			verify: func(t *testing.T, accepted int) {
				if accepted != 1 {
					t.Errorf("accepted %d connections, want 1", accepted)
				}
			},
		},
		"uncapped by default": {
			verify: func(t *testing.T, accepted int) {
				if accepted < 2 {
					t.Errorf("accepted %d connections, want the burst to fan out", accepted)
				}
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			addr, accepted := fakeServerN(t, func(r *bufio.Reader, w net.Conn) {
				serveCommands(r, w, "+PONG\r\n", 20*time.Millisecond)
			})
			c := New(addr, tc.opts...)
			defer c.Close()

			var wg sync.WaitGroup
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := c.Ping(t.Context()); err != nil {
						t.Errorf("Ping: %v", err)
					}
				}()
			}
			wg.Wait()
			tc.verify(t, accepted())
		})
	}
}

// A context without a deadline must not leave a command waiting forever on a
// server that never answers.
func TestCommandTimeoutWithoutContextDeadline(t *testing.T) {
	addr := fakeServer(t, func(_ *bufio.Reader, _ net.Conn) {
		<-time.After(5 * time.Second) // never replies
	})
	c := New(addr, WithTimeout(50*time.Millisecond))
	defer c.Close()

	done := make(chan error, 1)
	go func() { done <- c.Ping(context.Background()) }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Ping = nil, want a timeout error")
		}
	case <-time.After(2 * time.Second):
		t.Error("Ping did not return; the timeout was not applied")
	}
}

// Covers batching every queued command into one write and surfacing the first
// command error while keeping the connection usable.
func TestPipelineFlush(t *testing.T) {
	got := make(chan []string, 2)
	addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
		got <- readCommand(t, r)
		got <- readCommand(t, r)
		_, _ = io.WriteString(w, "+OK\r\n-ERR invalid argument\r\n")
	})

	c := New(addr)
	defer c.Close()

	p := c.Pipeline()
	p.Set("fleet", "truck1").Point(33.5, -115.5).Queue()
	p.Set("fleet", "truck2").EX(60).Field("speed", 12).Point(33.6, -115.6).Queue()
	if p.Len() != 2 {
		t.Fatalf("Len = %d, want 2", p.Len())
	}

	err := p.Flush(t.Context())
	var se ServerError
	if !errors.As(err, &se) {
		t.Fatalf("Flush = %v, want a ServerError", err)
	}

	want := [][]string{
		{"SET", "fleet", "truck1", "POINT", "33.5", "-115.5"},
		{"SET", "fleet", "truck2", "EX", "60", "FIELD", "speed", "12", "POINT", "33.6", "-115.6"},
	}
	for _, w := range want {
		if cmd := <-got; !reflect.DeepEqual(cmd, w) {
			t.Errorf("command = %q, want %q", cmd, w)
		}
	}
	if p.Len() != 0 {
		t.Errorf("Len after Flush = %d, want 0", p.Len())
	}
}

// Covers the pub/sub message shape: acks are skipped, payloads decoded.
func TestSubscribeSkipsAcks(t *testing.T) {
	addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
		readCommand(t, r)
		ev := `{"command":"set","detect":"inside","id":"truck1"}`
		_, _ = io.WriteString(w, wire(
			"*3", "$9", "subscribe", "$5", "zones", ":1",
			"*3", "$9", "subscribe", "$5", "other", ":2",
			"*3", "$7", "message", "$5", "zones", "$"+strconv.Itoa(len(ev)), ev,
		))
		<-time.After(time.Second)
	})

	c := New(addr)
	defer c.Close()

	st, err := c.Subscribe(t.Context(), "zones", "other")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ev, err := st.Next()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Detect != "inside" || ev.ID != "truck1" {
		t.Errorf("event = %+v", ev)
	}
}

// Cancelling the context must unblock a stream stuck waiting for events.
func TestStreamContextCancel(t *testing.T) {
	addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
		readCommand(t, r)
		_, _ = io.WriteString(w, "+OK\r\n")
		<-time.After(5 * time.Second)
	})

	c := New(addr)
	defer c.Close()

	ctx, cancel := context.WithCancel(t.Context())
	st, err := c.Within("fleet").Bounds(-90, -180, 90, 180).Fence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	go func() {
		<-time.After(50 * time.Millisecond)
		cancel()
	}()
	if _, err := st.Next(); !errors.Is(err, context.Canceled) {
		t.Errorf("Next = %v, want context.Canceled", err)
	}
}

// Search results carry the object's FIELDS as a flat name/value array that Tile38
// omits entirely when every field is zero, so both item shapes have to decode in
// the same reply. The fixtures below are the bytes a 1.38 server actually sends.
func TestObjectsDecodeFields(t *testing.T) {
	const point = `{"type":"Point","coordinates":[4.4,51.2]}`

	tests := map[string]struct {
		item string
		want Fields
	}{
		"no fields element": {
			item: wire("*2", "$1", "a", "$41", point),
			want: nil,
		},
		"numeric field keeps Tile38's own text": {
			item: wire("*3", "$1", "b", "$41", point, "*2", "$5", "speed", "$4", "12.5"),
			want: Fields{"speed": "12.5"},
		},
		"json field survives verbatim": {
			item: wire("*3", "$1", "c", "$41", point,
				"*4", "$8", "hardware", "$29", `{"battery":{"percentage":80}}`, "$5", "speed", "$1", "3"),
			want: Fields{"hardware": `{"battery":{"percentage":80}}`, "speed": "3"},
		},
		// Rather than fail the search and lose the geometry with it.
		"unpaired fields array is dropped": {
			item: wire("*3", "$1", "d", "$41", point, "*1", "$5", "speed"),
			want: nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
				_ = readCommand(t, r)
				_, _ = io.WriteString(w, wire("*2", ":0", "*1")+tc.item)
			})
			c := New(addr)
			defer c.Close()

			objs, err := c.Scan("fleet").Objects(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(objs) != 1 {
				t.Fatalf("objects = %+v, want 1", objs)
			}
			if objs[0].GeoJSON != point {
				t.Errorf("geojson = %q, want %q", objs[0].GeoJSON, point)
			}
			if !reflect.DeepEqual(objs[0].Fields, tc.want) {
				t.Errorf("fields = %v, want %v", objs[0].Fields, tc.want)
			}
		})
	}
}

// POINTS output carries the same trailing FIELDS array as OBJECTS, and with
// DISTANCE it sits between the coordinates and the distance rather than at the
// end. Both were read as geometry-only, so every field on a Points query was
// silently dropped.
func TestPointsDecodeFields(t *testing.T) {
	const coords = "*2\r\n$4\r\n51.2\r\n$3\r\n4.4"
	const fields = "*4\r\n$5\r\nspeed\r\n$4\r\n12.5\r\n$6\r\ndriver\r\n$3\r\nbob"

	t.Run("points", func(t *testing.T) {
		addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
			_ = readCommand(t, r)
			_, _ = io.WriteString(w, "*2\r\n:0\r\n*1\r\n*3\r\n$1\r\na\r\n"+coords+"\r\n"+fields+"\r\n")
		})
		c := New(addr)
		defer c.Close()

		res, err := c.Nearby("fleet").Point(51.2, 4.4).Radius(100).Points(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		want := []NearbyResult{{ID: "a", Lat: 51.2, Lon: 4.4, Fields: Fields{"speed": "12.5", "driver": "bob"}}}
		if !reflect.DeepEqual(res, want) {
			t.Errorf("points = %+v, want %+v", res, want)
		}
	})

	t.Run("points with distance", func(t *testing.T) {
		addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
			_ = readCommand(t, r)
			_, _ = io.WriteString(w, "*2\r\n:0\r\n*1\r\n*4\r\n$1\r\na\r\n"+coords+"\r\n"+fields+"\r\n$3\r\n120\r\n")
		})
		c := New(addr)
		defer c.Close()

		res, err := c.Nearby("fleet").Point(51.2, 4.4).Radius(100).PointsWithDistance(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		want := []NearbyResultWithDistance{{
			NearbyResult: NearbyResult{ID: "a", Lat: 51.2, Lon: 4.4, Fields: Fields{"speed": "12.5", "driver": "bob"}},
			Distance:     120,
		}}
		if !reflect.DeepEqual(res, want) {
			t.Errorf("points with distance = %+v, want %+v", res, want)
		}
	})

	// No fields array: the third element of a DISTANCE item is the distance, and
	// reading it as fields would be worse than dropping it.
	t.Run("distance only", func(t *testing.T) {
		addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
			_ = readCommand(t, r)
			_, _ = io.WriteString(w, "*2\r\n:0\r\n*1\r\n*3\r\n$1\r\na\r\n"+coords+"\r\n$3\r\n120\r\n")
		})
		c := New(addr)
		defer c.Close()

		res, err := c.Nearby("fleet").Point(51.2, 4.4).Radius(100).PointsWithDistance(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != 1 || res[0].Fields != nil || res[0].Distance != 120 {
			t.Errorf("points with distance = %+v, want one item, no fields, dist 120", res)
		}
	})
}

// A fence notification writes fields as JSON — a string field arrives quoted,
// everything else as its JSON text — where a search reply sends the same values
// as flat strings. Both have to land in the same Fields shape.
func TestFenceEventDecodesFieldsAndMeta(t *testing.T) {
	const payload = `{"command":"set","detect":"enter","hook":"h1","meta":{"owner":"ops"},` +
		`"key":"fleet","id":"t1","object":{"type":"Point","coordinates":[4.4,51.2]},` +
		`"fields":{"speed":42,"driver":"bob","hardware":{"battery":80}}}`

	ev, err := DecodeFenceEvent([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	want := Fields{"speed": "42", "driver": "bob", "hardware": `{"battery":80}`}
	if !reflect.DeepEqual(ev.Fields, want) {
		t.Errorf("fields = %v, want %v", ev.Fields, want)
	}
	if !reflect.DeepEqual(ev.Meta, map[string]string{"owner": "ops"}) {
		t.Errorf("meta = %v, want owner=ops", ev.Meta)
	}
}

// Tile38 appends a third ordinate to a coordinate array only when it is
// non-zero, so both lengths arrive in the same reply and a parse that stops at
// lon drops whatever the caller stored there.
func TestPointsDecodeZ(t *testing.T) {
	const flat = "*2\r\n$4\r\n51.2\r\n$3\r\n4.4"
	const withZ = "*3\r\n$4\r\n51.2\r\n$3\r\n4.4\r\n$5\r\n100.5"

	t.Run("points", func(t *testing.T) {
		addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
			_ = readCommand(t, r)
			_, _ = io.WriteString(w, "*2\r\n:0\r\n*2\r\n"+
				"*2\r\n$1\r\na\r\n"+withZ+"\r\n"+
				"*2\r\n$1\r\nb\r\n"+flat+"\r\n")
		})
		c := New(addr)
		defer c.Close()

		res, err := c.Scan("fleet").Points(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		want := []NearbyResult{
			{ID: "a", Lat: 51.2, Lon: 4.4, Z: 100.5},
			{ID: "b", Lat: 51.2, Lon: 4.4},
		}
		if !reflect.DeepEqual(res, want) {
			t.Errorf("points = %+v, want %+v", res, want)
		}
	})

	// With DISTANCE the z sits inside the coordinate array, so it must not be
	// confused with the trailing distance.
	t.Run("points with distance and fields", func(t *testing.T) {
		addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
			_ = readCommand(t, r)
			_, _ = io.WriteString(w, "*2\r\n:0\r\n*1\r\n*4\r\n$1\r\na\r\n"+withZ+
				"\r\n*2\r\n$5\r\nspeed\r\n$2\r\n42\r\n$3\r\n120\r\n")
		})
		c := New(addr)
		defer c.Close()

		res, err := c.Nearby("fleet").Point(1, 2).Radius(3).PointsWithDistance(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		want := []NearbyResultWithDistance{{
			NearbyResult: NearbyResult{ID: "a", Lat: 51.2, Lon: 4.4, Z: 100.5, Fields: Fields{"speed": "42"}},
			Distance:     120,
		}}
		if !reflect.DeepEqual(res, want) {
			t.Errorf("points with distance = %+v, want %+v", res, want)
		}
	})

	// GET POINT carries the same optional ordinate; Point drops it by contract,
	// PointZ is the terminal that keeps it.
	t.Run("get point", func(t *testing.T) {
		addr, _ := fakeServerN(t, func(r *bufio.Reader, w net.Conn) {
			for {
				if _, err := resp.ReadReply(r); err != nil {
					return
				}
				_, _ = io.WriteString(w, withZ+"\r\n")
			}
		})
		c := New(addr)
		defer c.Close()

		lat, lon, z, err := c.Get("fleet", "truck1").PointZ(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if lat != 51.2 || lon != 4.4 || z != 100.5 {
			t.Errorf("point z = %v,%v,%v; want 51.2,4.4,100.5", lat, lon, z)
		}
		if lat, lon, err = c.Get("fleet", "truck1").Point(t.Context()); err != nil || lat != 51.2 || lon != 4.4 {
			t.Errorf("point = %v,%v,%v; want 51.2,4.4", lat, lon, err)
		}
	})
}

// WITHFIELDS wraps the reply in an envelope whose second element is the fields,
// and which Tile38 emits with a single element when the object has no non-zero
// fields. Every output format has to unwrap both shapes.
func TestGetWithFields(t *testing.T) {
	const point = "*2\r\n$4\r\n51.2\r\n$3\r\n4.4"

	tests := map[string]struct {
		reply      string
		wantFields Fields
	}{
		"fields present": {
			reply:      "*2\r\n" + point + "\r\n*2\r\n$5\r\nspeed\r\n$2\r\n42\r\n",
			wantFields: Fields{"speed": "42"},
		},
		// Tile38 drops the fields element entirely for an all-zero object.
		"no fields element": {
			reply:      "*1\r\n" + point + "\r\n",
			wantFields: nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := make(chan []string, 1)
			addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
				got <- readCommand(t, r)
				_, _ = io.WriteString(w, tc.reply)
			})
			c := New(addr)
			defer c.Close()

			g := c.Get("fleet", "truck1").WithFields()
			lat, lon, err := g.Point(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"GET", "fleet", "truck1", "WITHFIELDS", "POINT"}
			if cmd := <-got; !reflect.DeepEqual(cmd, want) {
				t.Errorf("command = %q, want %q", cmd, want)
			}
			if lat != 51.2 || lon != 4.4 {
				t.Errorf("point = %v,%v, want 51.2,4.4", lat, lon)
			}
			if !reflect.DeepEqual(g.Fields(), tc.wantFields) {
				t.Errorf("fields = %v, want %v", g.Fields(), tc.wantFields)
			}
		})
	}

	// Without WithFields the reply is unwrapped, so no envelope is expected.
	t.Run("not requested", func(t *testing.T) {
		got := make(chan []string, 1)
		addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
			got <- readCommand(t, r)
			_, _ = io.WriteString(w, point+"\r\n")
		})
		c := New(addr)
		defer c.Close()

		g := c.Get("fleet", "truck1")
		if _, _, err := g.Point(t.Context()); err != nil {
			t.Fatal(err)
		}
		want := []string{"GET", "fleet", "truck1", "POINT"}
		if cmd := <-got; !reflect.DeepEqual(cmd, want) {
			t.Errorf("command = %q, want %q", cmd, want)
		}
		if g.Fields() != nil {
			t.Errorf("fields = %v, want nil", g.Fields())
		}
	})
}

// BOUNDS, HASHES and A5 are output formats, so they sit between the options and
// the search area — a chain that appended them would put the geometry in the
// wrong slot and malform the command.
func TestOutputFormatTokenOrder(t *testing.T) {
	tests := map[string]struct {
		run   func(*Client) error
		want  []string
		reply string
	}{
		"bounds": {
			run: func(c *Client) error {
				_, err := c.Nearby("fleet").Limit(5).Point(33.5, -115.5).Radius(100).Rects(t.Context())
				return err
			},
			want: []string{"NEARBY", "fleet", "LIMIT", "5", "BOUNDS", "POINT", "33.5", "-115.5", "100"},
		},
		"hashes": {
			run: func(c *Client) error {
				_, err := c.Within("fleet").Bounds(GlobalBounds()).Hashes(t.Context(), 6)
				return err
			},
			want: []string{"WITHIN", "fleet", "HASHES", "6", "BOUNDS", "-90", "-180", "90", "180"},
		},
		"a5": {
			run: func(c *Client) error {
				_, err := c.Intersects("fleet").Circle(1, 2, 3).A5Cells(t.Context(), 8)
				return err
			},
			want: []string{"INTERSECTS", "fleet", "A5", "8", "CIRCLE", "1", "2", "3"},
		},
		"scan bounds": {
			run: func(c *Client) error {
				_, err := c.Scan("fleet").Match("truck:*").Rects(t.Context())
				return err
			},
			want: []string{"SCAN", "fleet", "MATCH", "truck:*", "BOUNDS"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := make(chan []string, 1)
			addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
				got <- readCommand(t, r)
				_, _ = io.WriteString(w, wire("*2", ":0", "*0"))
			})
			c := New(addr)
			defer c.Close()

			if err := tc.run(c); err != nil {
				t.Fatal(err)
			}
			if cmd := <-got; !reflect.DeepEqual(cmd, tc.want) {
				t.Errorf("command = %q,\n           want %q", cmd, tc.want)
			}
		})
	}
}

// BOUNDS items nest a corner pair inside the item and carry fields after it;
// HASHES and A5 are flat. A5 is the one output Tile38 attaches no fields to.
func TestOutputFormatDecoding(t *testing.T) {
	t.Run("rects", func(t *testing.T) {
		reply := wire("*2", ":0", "*1",
			"*3", "$1", "a",
			"*2", "*2", "$4", "51.1", "$3", "4.1", "*2", "$4", "51.3", "$3", "4.3",
			"*2", "$5", "speed", "$2", "42")
		addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
			_ = readCommand(t, r)
			_, _ = io.WriteString(w, reply)
		})
		c := New(addr)
		defer c.Close()

		res, err := c.Scan("fleet").Rects(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		want := []RectResult{{
			ID:     "a",
			Bounds: BoundsResult{SW: [2]float64{51.1, 4.1}, NE: [2]float64{51.3, 4.3}},
			Fields: Fields{"speed": "42"},
		}}
		if !reflect.DeepEqual(res, want) {
			t.Errorf("rects = %+v, want %+v", res, want)
		}
	})

	t.Run("hashes", func(t *testing.T) {
		reply := wire("*2", ":0", "*1", "*3", "$1", "a", "$6", "9mvyed", "*2", "$5", "speed", "$2", "42")
		addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
			_ = readCommand(t, r)
			_, _ = io.WriteString(w, reply)
		})
		c := New(addr)
		defer c.Close()

		res, err := c.Scan("fleet").Hashes(t.Context(), 6)
		if err != nil {
			t.Fatal(err)
		}
		want := []HashResult{{ID: "a", Hash: "9mvyed", Fields: Fields{"speed": "42"}}}
		if !reflect.DeepEqual(res, want) {
			t.Errorf("hashes = %+v, want %+v", res, want)
		}
	})

	t.Run("a5 cells", func(t *testing.T) {
		reply := wire("*2", ":0", "*1", "*2", "$1", "a", "$16", "1970980000000000")
		addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
			_ = readCommand(t, r)
			_, _ = io.WriteString(w, reply)
		})
		c := New(addr)
		defer c.Close()

		res, err := c.Scan("fleet").A5Cells(t.Context(), 8)
		if err != nil {
			t.Fatal(err)
		}
		want := []A5Result{{ID: "a", Cell: "1970980000000000"}}
		if !reflect.DeepEqual(res, want) {
			t.Errorf("a5 cells = %+v, want %+v", res, want)
		}
	})
}

// The option and area tokens added alongside the output formats. Each is checked
// for the slot it lands in, since a token in the wrong section malforms the
// command rather than erroring in an obvious way.
func TestOptionAndAreaTokens(t *testing.T) {
	tests := map[string]struct {
		run  func(*Client) error
		want []string
	}{
		// ASC and DESC overwrite: Tile38 rejects a command carrying both.
		"scan order overwrites": {
			run: func(c *Client) error {
				_, err := c.Scan("fleet").Desc().Asc().Desc().IDs(t.Context())
				return err
			},
			want: []string{"SCAN", "fleet", "DESC", "IDS"},
		},
		"whereeval is length prefixed": {
			run: func(c *Client) error {
				_, err := c.Scan("fleet").WhereEval("return FIELDS.speed > tonumber(ARGV[1])", 15).IDs(t.Context())
				return err
			},
			want: []string{"SCAN", "fleet", "WHEREEVAL",
				"return FIELDS.speed > tonumber(ARGV[1])", "1", "15", "IDS"},
		},
		"whereevalsha accumulates": {
			run: func(c *Client) error {
				_, err := c.Within("fleet").WhereEvalSha("abc", 1).WhereEvalSha("def").
					Bounds(GlobalBounds()).IDs(t.Context())
				return err
			},
			want: []string{"WITHIN", "fleet", "WHEREEVALSHA", "abc", "1", "1",
				"WHEREEVALSHA", "def", "0", "IDS", "BOUNDS", "-90", "-180", "90", "180"},
		},
		"buffer precedes the output format": {
			run: func(c *Client) error {
				_, err := c.Intersects("fleet").Buffer(500).Circle(1, 2, 3).IDs(t.Context())
				return err
			},
			want: []string{"INTERSECTS", "fleet", "BUFFER", "500", "IDS", "CIRCLE", "1", "2", "3"},
		},
		"sector area": {
			run: func(c *Client) error {
				_, err := c.Within("fleet").Sector(33.5, -115.5, 5000, 0, 90).IDs(t.Context())
				return err
			},
			want: []string{"WITHIN", "fleet", "IDS", "SECTOR", "33.5", "-115.5", "5000", "0", "90"},
		},
		"hash area": {
			run: func(c *Client) error {
				_, err := c.Intersects("fleet").Hash("9mv").Count(t.Context())
				return err
			},
			want: []string{"INTERSECTS", "fleet", "COUNT", "HASH", "9mv"},
		},
		"quadkey area": {
			run: func(c *Client) error {
				_, err := c.Within("fleet").QuadKey("0231").Count(t.Context())
				return err
			},
			want: []string{"WITHIN", "fleet", "COUNT", "QUADKEY", "0231"},
		},
		// An area replaces whatever came before it rather than appending.
		"last area wins": {
			run: func(c *Client) error {
				_, err := c.Within("fleet").Hash("9mv").QuadKey("0231").Count(t.Context())
				return err
			},
			want: []string{"WITHIN", "fleet", "COUNT", "QUADKEY", "0231"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := make(chan []string, 1)
			addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
				got <- readCommand(t, r)
				_, _ = io.WriteString(w, wire("*2", ":0", "*0"))
			})
			c := New(addr)
			defer c.Close()

			_ = tc.run(c)
			if cmd := <-got; !reflect.DeepEqual(cmd, tc.want) {
				t.Errorf("command = %q,\n           want %q", cmd, tc.want)
			}
		})
	}
}

// DISTANCE belongs to the fence clause for the same reason DETECT does: on a
// plain query it would add a trailing element to every item and shift the shape
// the parse helpers expect.
func TestDistanceOnlyOnFence(t *testing.T) {
	tests := map[string]struct {
		run  func(*Client) error
		want []string
	}{
		"ignored without fence": {
			run: func(c *Client) error {
				_, err := c.Nearby("fleet").Distance().Point(1, 2).Radius(3).Points(t.Context())
				return err
			},
			want: []string{"NEARBY", "fleet", "POINTS", "POINT", "1", "2", "3"},
		},
		"rendered on the fence": {
			run: func(c *Client) error {
				st, err := c.Nearby("fleet").Distance().Detect(Enter).Point(1, 2).Radius(3).Fence(t.Context())
				if st != nil {
					_ = st.Close()
				}
				return err
			},
			want: []string{"NEARBY", "fleet", "DISTANCE", "FENCE", "DETECT", "enter",
				"POINT", "1", "2", "3"},
		},
		"rendered on a hook": {
			run: func(c *Client) error {
				return c.SetChan("zone").Distance().Nearby("fleet").
					Point(1, 2).Radius(3).Do(t.Context())
			},
			want: []string{"SETCHAN", "zone", "NEARBY", "fleet", "DISTANCE", "FENCE",
				"POINT", "1", "2", "3"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := make(chan []string, 1)
			addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
				got <- readCommand(t, r)
				_, _ = io.WriteString(w, "+OK\r\n")
			})
			c := New(addr)
			defer c.Close()

			_ = tc.run(c)
			if cmd := <-got; !reflect.DeepEqual(cmd, tc.want) {
				t.Errorf("command = %q,\n           want %q", cmd, tc.want)
			}
		})
	}
}

// TEST is positional throughout — two areas around a verb — so nothing about it
// can be appended in call order.
func TestTestCommandAssembly(t *testing.T) {
	tests := map[string]struct {
		run   func(*Client) error
		want  []string
		reply string
	}{
		"within": {
			run: func(c *Client) error {
				_, err := c.Test(AreaGet("fleet", "truck1")).
					Within(AreaBounds(GlobalBounds())).Do(t.Context())
				return err
			},
			want: []string{"TEST", "GET", "fleet", "truck1", "WITHIN",
				"BOUNDS", "-90", "-180", "90", "180"},
			reply: ":1\r\n",
		},
		"intersects a sector": {
			run: func(c *Client) error {
				_, err := c.Test(AreaPoint(33.5, -115.5)).
					Intersects(AreaSector(33.5, -115.5, 5000, 0, 90)).Do(t.Context())
				return err
			},
			want: []string{"TEST", "POINT", "33.5", "-115.5", "INTERSECTS",
				"SECTOR", "33.5", "-115.5", "5000", "0", "90"},
			reply: ":0\r\n",
		},
		// CLIP sits between the verb and the second area, and turns the bare
		// integer reply into [result, geojson].
		"clip": {
			run: func(c *Client) error {
				_, _, err := c.Test(AreaBounds(33, -116, 34, -115)).
					Intersects(AreaQuadKey("023")).Clip(t.Context())
				return err
			},
			want: []string{"TEST", "BOUNDS", "33", "-116", "34", "-115",
				"INTERSECTS", "CLIP", "QUADKEY", "023"},
			reply: wire("*2", ":1", "$7", "{\"a\":1}"),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := make(chan []string, 1)
			addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
				got <- readCommand(t, r)
				_, _ = io.WriteString(w, tc.reply)
			})
			c := New(addr)
			defer c.Close()

			if err := tc.run(c); err != nil {
				t.Fatal(err)
			}
			if cmd := <-got; !reflect.DeepEqual(cmd, tc.want) {
				t.Errorf("command = %q,\n           want %q", cmd, tc.want)
			}
		})
	}
}

// SEARCH's default output pairs each id with the string value that matched,
// where a geometry search would carry GeoJSON.
func TestSearchStrings(t *testing.T) {
	addr := fakeServer(t, func(r *bufio.Reader, w net.Conn) {
		_ = readCommand(t, r)
		_, _ = io.WriteString(w, wire("*2", ":0", "*1",
			"*3", "$5", "note1", "$11", "hello world", "*2", "$4", "prio", "$1", "3"))
	})
	c := New(addr)
	defer c.Close()

	res, err := c.Search("notes").Match("*hello*").Asc().Strings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []StringObject{{ID: "note1", Value: "hello world", Fields: Fields{"prio": "3"}}}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("strings = %+v, want %+v", res, want)
	}
}

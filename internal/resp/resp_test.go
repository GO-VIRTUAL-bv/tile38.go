// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package resp

import (
	"bufio"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func wire(lines ...string) string { return strings.Join(lines, "\r\n") + "\r\n" }

func TestAppendCommand(t *testing.T) {
	buf, err := AppendCommand(nil, []any{"SET", "fleet", "truck1", "POINT", 33.5, -115.5, 60, true})
	if err != nil {
		t.Fatal(err)
	}
	want := wire(
		"*8",
		"$3", "SET",
		"$5", "fleet",
		"$6", "truck1",
		"$5", "POINT",
		"$4", "33.5",
		"$6", "-115.5",
		"$2", "60",
		"$4", "true",
	)
	if string(buf) != want {
		t.Errorf("command =\n%q\nwant\n%q", buf, want)
	}
}

// Floats must never reach Tile38 in exponent form.
func TestAppendCommandFloatFormat(t *testing.T) {
	buf, err := AppendCommand(nil, []any{0.000001, 1e21})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(string(buf), "eE") {
		t.Errorf("float rendered in exponent form: %q", buf)
	}
}

func TestAppendCommandRejectsUnsupported(t *testing.T) {
	if _, err := AppendCommand(nil, []any{struct{}{}}); err == nil {
		t.Error("AppendCommand(struct{}{}) = nil error, want error")
	}
}

func TestReadReply(t *testing.T) {
	tests := map[string]struct {
		in   string
		want any
	}{
		"simple string": {wire("+OK"), "OK"},
		"integer":       {wire(":42"), int64(42)},
		"bulk string":   {wire("$5", "hello"), "hello"},
		"null bulk":     {wire("$-1"), nil},
		"null array":    {wire("*-1"), nil},
		"empty array":   {wire("*0"), []any{}},
		"array":         {wire("*2", ":1", "*1", "$2", "id"), []any{int64(1), []any{"id"}}},
		// A bulk payload may contain CRLF, so it must be read by length.
		"bulk with crlf": {wire("$4", "a\r\nb"), "a\r\nb"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := ReadReply(bufio.NewReader(strings.NewReader(tc.in)))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ReadReply = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// A -ERR reply is a rejected command, not a broken connection, so it must come
// back as an Error the caller can distinguish.
func TestReadReplyServerError(t *testing.T) {
	_, err := ReadReply(bufio.NewReader(strings.NewReader(wire("-ERR invalid argument"))))
	var se Error
	if !errors.As(err, &se) {
		t.Fatalf("ReadReply = %v, want an Error", err)
	}
	if se.Error() != "ERR invalid argument" {
		t.Errorf("message = %q", se.Error())
	}
}

// Header lines are read in buffer-sized fragments, so an error reply that echoes
// a large argument back must still reassemble rather than fail at 4KB.
func TestReadReplyLongErrorLine(t *testing.T) {
	msg := "ERR invalid argument '" + strings.Repeat("x", 8<<10) + "'"
	_, err := ReadReply(bufio.NewReader(strings.NewReader(wire("-" + msg))))
	var se Error
	if !errors.As(err, &se) {
		t.Fatalf("ReadReply = %v, want an Error", err)
	}
	if se.Error() != msg {
		t.Errorf("message truncated: got %d bytes, want %d", len(se.Error()), len(msg))
	}
}

// A desynced stream must not be read without limit.
func TestReadReplyLineTooLong(t *testing.T) {
	_, err := ReadReply(bufio.NewReader(strings.NewReader("+" + strings.Repeat("x", (64<<10)+1) + "\r\n")))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("ReadReply = %v, want a line-length error", err)
	}
}

// A corrupt length prefix must be rejected before it allocates.
func TestReadReplyBulkTooLong(t *testing.T) {
	_, err := ReadReply(bufio.NewReader(strings.NewReader("$999999999999\r\n")))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("ReadReply = %v, want a bulk-length error", err)
	}
}

func TestReadReplyMalformed(t *testing.T) {
	for name, in := range map[string]string{
		"unknown type": wire("!nope"),
		"bad integer":  wire(":abc"),
		"no crlf":      "+OK\n",
		"short bulk":   "$10\r\nshort\r\n",
		// The length disagrees with the payload, so the bytes where the CRLF
		// should be are payload. Catching it here stops a silent desync.
		"bulk length too short": "$2\r\nhello\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadReply(bufio.NewReader(strings.NewReader(in))); err == nil {
				t.Error("ReadReply = nil error, want error")
			}
		})
	}
}

// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package resp implements the subset of the Redis serialization protocol that
// Tile38 speaks: commands out as arrays of bulk strings, replies in as strings,
// integers, arrays, and nulls.
//
// RESP2 only. Tile38 does not implement HELLO, so there are no RESP3 push or
// double types to handle, and numbers other than integers arrive as bulk
// strings.
package resp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// Error is an error reply from Tile38 ("-ERR ..."). It means the command was
// rejected, not that the connection broke, so the connection stays usable.
type Error string

func (e Error) Error() string { return string(e) }

// AppendCommand encodes args onto buf as a RESP array of bulk strings.
func AppendCommand(buf []byte, args []any) ([]byte, error) {
	buf = append(buf, '*')
	buf = strconv.AppendInt(buf, int64(len(args)), 10)
	buf = append(buf, '\r', '\n')
	for _, arg := range args {
		s, err := argString(arg)
		if err != nil {
			return nil, err
		}
		buf = appendBulk(buf, s)
	}
	return buf, nil
}

func appendBulk(buf []byte, s string) []byte {
	buf = append(buf, '$')
	buf = strconv.AppendInt(buf, int64(len(s)), 10)
	buf = append(buf, '\r', '\n')
	buf = append(buf, s...)
	return append(buf, '\r', '\n')
}

// argString renders a command argument as the text Tile38 expects. Floats use
// 'f' rather than 'g' so coordinates never reach the server in exponent form.
func argString(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case []byte:
		return string(x), nil
	case json.RawMessage:
		return string(x), nil
	case bool:
		if x {
			return "true", nil
		}
		return "false", nil
	case int:
		return strconv.Itoa(x), nil
	case int8:
		return strconv.FormatInt(int64(x), 10), nil
	case int16:
		return strconv.FormatInt(int64(x), 10), nil
	case int32:
		return strconv.FormatInt(int64(x), 10), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case uint:
		return strconv.FormatUint(uint64(x), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(x), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(x), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(x), 10), nil
	case uint64:
		return strconv.FormatUint(x, 10), nil
	case float32:
		return strconv.FormatFloat(float64(x), 'f', -1, 32), nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case nil:
		return "", nil
	default:
		return "", fmt.Errorf("tile38: cannot encode argument of type %T", v)
	}
}

// ReadReply decodes one RESP value. Bulk and simple strings decode to string,
// integers to int64, arrays to []any, and nulls to nil — the four types every
// parse helper in the parent package is written against.
func ReadReply(r *bufio.Reader) (any, error) {
	line, err := readLine(r)
	if err != nil {
		return nil, err
	}
	if line == "" {
		return nil, fmt.Errorf("tile38: empty reply")
	}
	body := line[1:]
	switch line[0] {
	case '+':
		return body, nil
	case '-':
		return nil, Error(body)
	case ':':
		n, err := strconv.ParseInt(body, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("tile38: bad integer reply %q", body)
		}
		return n, nil
	case '$':
		n, err := strconv.Atoi(body)
		if err != nil {
			return nil, fmt.Errorf("tile38: bad bulk length %q", body)
		}
		if n < 0 {
			return nil, nil
		}
		if n > maxBulkLen {
			return nil, fmt.Errorf("tile38: bulk reply length %d exceeds %d bytes", n, maxBulkLen)
		}
		b := make([]byte, n+2) // payload + trailing CRLF
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, err
		}
		// Without this check a length prefix that disagrees with the payload
		// desyncs the stream silently; every later reply is then garbage.
		if b[n] != '\r' || b[n+1] != '\n' {
			return nil, fmt.Errorf("tile38: bulk reply of %d bytes is not CRLF-terminated", n)
		}
		return string(b[:n]), nil
	case '*':
		n, err := strconv.Atoi(body)
		if err != nil {
			return nil, fmt.Errorf("tile38: bad array length %q", body)
		}
		if n < 0 {
			return nil, nil
		}
		arr := make([]any, n)
		for i := range arr {
			v, err := ReadReply(r)
			if err != nil {
				// An error nested in an array leaves the stream mid-value, so
				// it is fatal to the connection rather than an Error.
				return nil, fmt.Errorf("tile38: error inside array reply: %w", err)
			}
			arr[i] = v
		}
		return arr, nil
	default:
		return nil, fmt.Errorf("tile38: unknown reply type %q", line[0])
	}
}

// maxLineLen bounds one protocol header line. Length prefixes and status replies
// are a handful of bytes; the only long line Tile38 sends is an error echoing a
// large argument back, so the ceiling is generous. Without it, a stream that has
// desynced and contains no newline is read until memory runs out.
const maxLineLen = 64 << 10

// maxBulkLen bounds a bulk payload. It is far above any real Tile38 object and
// exists so a corrupt length prefix cannot allocate an arbitrary buffer.
const maxBulkLen = 512 << 20

// readLine reads one CRLF-terminated protocol line, accumulating across the
// reader's buffer but refusing to grow past maxLineLen.
func readLine(r *bufio.Reader) (string, error) {
	var line []byte
	for {
		frag, err := r.ReadSlice('\n')
		line = append(line, frag...) // ReadSlice aliases the buffer, so copy now
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return "", err
		}
		if len(line) > maxLineLen {
			return "", fmt.Errorf("tile38: reply line exceeds %d bytes", maxLineLen)
		}
		if err == nil {
			break
		}
	}
	if len(line) < 2 || line[len(line)-2] != '\r' {
		return "", fmt.Errorf("tile38: malformed reply line %q", line)
	}
	return string(line[:len(line)-2]), nil
}

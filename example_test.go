// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package tile38_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	tile38 "github.com/GO-VIRTUAL-bv/tile38.go"
)

// A live fence is expected to run for days, so surviving a server restart, a
// rolling deploy, or an idle NAT timeout means reopening it. Next reports a
// dropped connection as ErrDisconnected; io.EOF after Close and the context's
// own error are the two reasons not to come back.
//
// The outer loop is deliberately the caller's rather than the package's:
// backoff, an attempt cap, metrics, and whether to fail over to another address
// are application policy, and a retry hidden inside Next would be the surprise
// you debug at 3am.
//
// Reopening is not resuming. Tile38 keeps no offset for a live fence, so every
// event during the gap is gone — use SETHOOK with a durable endpoint (Kafka,
// AMQP, SQS) if you cannot lose them.
func ExampleStream_reconnect() {
	c := tile38.New("localhost:9851")
	defer c.Close()

	ctx := context.Background()

	// The builder is reusable: every Fence call assembles its own argument
	// slice, so each attempt sends identical bytes.
	fence := c.Within("fleet").
		Circle(51.05, 3.72, 500).
		Detect(tile38.Enter, tile38.Exit)

	for delay := time.Duration(0); ; {
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return
			}
		}

		st, err := fence.Fence(ctx)
		if err != nil {
			// A command the server rejects will be rejected identically every
			// time, so retrying it just re-dials forever.
			var rejected tile38.ServerError
			if errors.As(err, &rejected) {
				log.Printf("fence rejected: %v", err)
				return
			}
			delay = min(max(2*delay, 100*time.Millisecond), 30*time.Second)
			log.Printf("cannot open fence, retrying in %s: %v", delay, err)
			continue
		}
		delay = 0

		for {
			ev, err := st.Next()
			if err != nil {
				_ = st.Close()
				if !errors.Is(err, tile38.ErrDisconnected) {
					return // Close, or the context ended
				}
				log.Printf("fence dropped, reopening: %v", err)
				break
			}
			fmt.Println(ev.Detect, ev.ID)
		}
	}
}

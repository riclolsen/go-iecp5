package cs104

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
)

// Send does not block.
//
// A session's send buffer is finite, and when it is full Send refuses the
// ASDU and returns ErrBufferFulled rather than making a protocol goroutine
// wait. That is the right default — an outstation must not stall its own
// APCI state machine because one master is slow — but it puts the burden on
// the caller, and the burden is easy to miss: writing
//
//	_ = asdu.MeasuredValueFloat(c, false, cause, ca, values...)
//
// in an interrogation handler works perfectly on a small database and
// quietly loses ASDUs on a large one. The outstation believes it answered in
// full; the master has holes it has no way to detect.
//
// Waiting removes the trap for the case where waiting is what you want.

// waitingConn is a Connect whose Send waits for buffer room.
type waitingConn struct {
	asdu.Connect
	ctx   context.Context
	retry time.Duration
}

// Send retries while the send buffer is full, and gives up when ctx is done.
func (w waitingConn) Send(a *asdu.ASDU) error {
	for {
		err := w.Connect.Send(a)
		if err == nil || !errors.Is(err, ErrBufferFulled) {
			// Anything other than a full buffer — a closed connection, a
			// malformed ASDU — will not be fixed by waiting.
			return err
		}
		select {
		case <-w.ctx.Done():
			return fmt.Errorf("send buffer full: %w", w.ctx.Err())
		case <-time.After(w.retry):
		}
	}
}

// Waiting wraps a Connect so that Send waits for send-buffer room instead of
// failing with ErrBufferFulled.
//
// Use it for bulk replies — an interrogation over a large database — where
// the data must go out and arriving late is better than not arriving:
//
//	func (h *handler) InterrogationHandler(c asdu.Connect, a *asdu.ASDU, qoi asdu.QualifierOfInterrogation) error {
//		_ = a.SendReplyMirror(c, asdu.ActivationCon)
//
//		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
//		defer cancel()
//		w := cs104.Waiting(ctx, c) // every send below now waits rather than dropping
//
//		for _, batch := range h.database() {
//			if err := asdu.MeasuredValueFloat(w, false, cause, ca, batch...); err != nil {
//				return err
//			}
//		}
//		return a.SendReplyMirror(c, asdu.ActivationTerm)
//	}
//
// Waiting blocks the calling goroutine, never the session's own protocol
// goroutine, so the APCI state machine keeps running: the buffer drains as
// the master acknowledges, and the wait ends. Always bound it with a ctx
// deadline so a master that has stopped acknowledging cannot block a handler
// for ever.
//
// Do not wrap a *Server with this: retrying a broadcast re-sends to the
// sessions that already accepted the ASDU. Use Server.SendWait instead,
// which retries only the sessions that refused it.
func Waiting(ctx context.Context, c asdu.Connect) asdu.Connect {
	if srv, ok := c.(*Server); ok {
		// Retrying a broadcast would re-send to the sessions that already
		// accepted the ASDU, so a Server gets the per-session retry instead
		// of the naive one. Doing this here rather than refusing keeps the
		// easy thing correct.
		return srv.WaitingConn(ctx)
	}
	return waitingConn{Connect: c, ctx: ctx, retry: 2 * time.Millisecond}
}

// broadcastConn is a Connect whose Send is Server.SendWait.
type broadcastConn struct {
	*Server
	ctx context.Context
}

func (b broadcastConn) Send(a *asdu.ASDU) error { return b.Server.SendWait(b.ctx, a) }

// WaitingConn returns the server as a Connect whose Send waits for room on
// each session rather than losing the ASDU for a master that is behind.
//
// Use it to push spontaneous data through the asdu helpers without the
// duplicate hazard of retrying a broadcast:
//
//	w := srv.WaitingConn(ctx)
//	if err := asdu.Single(w, false, spont, ca, points...); err != nil {
//		log.Printf("send: %v", err)
//	}
func (sf *Server) WaitingConn(ctx context.Context) asdu.Connect {
	return broadcastConn{Server: sf, ctx: ctx}
}

package cs104

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
)

// fullConn is a Connect whose buffer holds n ASDUs and is drained by hand,
// which is what a session's buffer does while the master acknowledges.
type fullConn struct {
	mu     sync.Mutex
	queued []*asdu.ASDU
	limit  int
	params asdu.Params
}

func (f *fullConn) Params() *asdu.Params     { return &f.params }
func (f *fullConn) UnderlyingConn() net.Conn { return nil }

func (f *fullConn) Send(a *asdu.ASDU) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queued) >= f.limit {
		return ErrBufferFulled
	}
	f.queued = append(f.queued, a)
	return nil
}

// drain empties the buffer the way an acknowledging master does, and reports
// how many it took.
func (f *fullConn) drain() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.queued)
	f.queued = f.queued[:0]
	return n
}

func (f *fullConn) depth() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queued)
}

func newFullConn(limit int) *fullConn {
	return &fullConn{limit: limit, params: *asdu.ParamsWide}
}

// TestSendRefusesWhenBufferIsFull is the behaviour Waiting exists to work
// around: Send does not block, so a caller that discards the error loses the
// ASDU without any indication.
func TestSendRefusesWhenBufferIsFull(t *testing.T) {
	c := newFullConn(2)
	cause := asdu.CauseOfTransmission{Cause: asdu.Spontaneous}

	var lost int
	for i := 0; i < 5; i++ {
		err := asdu.Single(c, false, cause, 1,
			asdu.SinglePointInfo{Ioa: asdu.InfoObjAddr(100 + i), Value: true})
		if err != nil {
			if !errors.Is(err, ErrBufferFulled) {
				t.Fatalf("unexpected error: %v", err)
			}
			lost++
		}
	}
	if c.depth() != 2 || lost != 3 {
		t.Fatalf("queued %d and refused %d, want 2 and 3", c.depth(), lost)
	}
}

// TestWaitingSendsEverythingOnceTheBufferDrains checks that a bulk reply
// through Waiting loses nothing: it waits for room instead of failing.
func TestWaitingSendsEverythingOnceTheBufferDrains(t *testing.T) {
	c := newFullConn(4)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := Waiting(ctx, c)

	// Drain the buffer the way an acknowledging master does.
	done := make(chan struct{})
	drained := 0
	go func() {
		defer close(done)
		for drained < 50 {
			time.Sleep(time.Millisecond)
			drained += c.drain()
		}
	}()

	cause := asdu.CauseOfTransmission{Cause: asdu.InterrogatedByStation}
	for i := 0; i < 50; i++ {
		if err := asdu.Single(w, false, cause, 1,
			asdu.SinglePointInfo{Ioa: asdu.InfoObjAddr(100 + i), Value: true}); err != nil {
			t.Fatalf("object %d was lost: %v", i, err)
		}
	}
	<-done
}

// TestWaitingGivesUpWhenTheContextEnds: a master that has stopped
// acknowledging must not block a handler for ever.
func TestWaitingGivesUpWhenTheContextEnds(t *testing.T) {
	c := newFullConn(1)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	w := Waiting(ctx, c)

	cause := asdu.CauseOfTransmission{Cause: asdu.Spontaneous}
	_ = asdu.Single(w, false, cause, 1, asdu.SinglePointInfo{Ioa: 100, Value: true})

	start := time.Now()
	err := asdu.Single(w, false, cause, 1, asdu.SinglePointInfo{Ioa: 101, Value: true})
	if err == nil {
		t.Fatal("a send into a buffer that never drains must fail, not hang")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error must say why it gave up, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("gave up after %v, want about the context deadline", elapsed)
	}
}

// TestWaitingOnAServerUsesPerSessionRetry: wrapping a Server must not give
// the naive retry, which would re-send to sessions that already accepted the
// ASDU.
func TestWaitingOnAServerUsesPerSessionRetry(t *testing.T) {
	srv := NewServer(nil)
	c := Waiting(context.Background(), srv)
	if _, naive := c.(waitingConn); naive {
		t.Fatal("a Server must not get the naive broadcast retry")
	}
	if _, ok := c.(broadcastConn); !ok {
		t.Fatalf("want the per-session retry, got %T", c)
	}
	// With no sessions connected there is nothing to refuse, so this is a
	// no-op rather than an error.
	if err := c.Send(nil); err != nil {
		t.Fatalf("broadcast to no sessions: %v", err)
	}
}

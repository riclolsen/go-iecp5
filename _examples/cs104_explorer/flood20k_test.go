package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestEventFlood20k pushes 20000 single-object event ASDUs — the traffic
// shape that overwhelms a master — and checks every one lands.
func TestEventFlood20k(t *testing.T) {
	const events = 20000
	f := startFlood(t, 0)

	conn := newConnection(link{Host: f.addr, CommonAddr: 1,
		Timeout: 10 * time.Second, Reconnect: 2 * time.Second}, t.TempDir())
	if err := conn.start(); err != nil {
		t.Fatal(err)
	}
	defer conn.stop()
	m := NewModel(conn)
	m.width, m.height = 140, 40
	m.now = time.Now()
	m.screen = ScreenPoints

	if !pump(t, m, func() bool { return m.active }, 20*time.Second) {
		t.Fatal("not active")
	}

	done := make(chan struct{})
	go func() { f.floodSpontaneous(events); close(done) }()

	start := time.Now()
	batches := 0
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) && len(m.points) < events {
		if msg, ok := nextMsg(m.conn, 200*time.Millisecond); ok {
			if _, isU := msg.(updateMsg); isU {
				batches++
			}
			m.now = time.Now()
			m.Update(msg)
			_ = m.View()
		}
	}
	<-done

	t.Logf("sent=%d points=%d asdus=%d dropped=%d batches=%d elapsed=%v",
		atomic.LoadUint64(&f.spont), len(m.points), m.rxASDU, conn.dropCount(),
		batches, time.Since(start))
	t.Logf("status=%q active=%v", m.status, m.active)

	if len(m.points) != events {
		t.Errorf("%d of %d events arrived (%d missing)", len(m.points), events, events-len(m.points))
	}
	if conn.dropCount() != 0 {
		t.Errorf("%d messages dropped", conn.dropCount())
	}
}

package main

import (
	"io"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestRealProgramLargeDatabase drives the actual Bubble Tea program — its own
// message loop, View called for every frame — against a large outstation.
// Every other test here bypasses that loop, and the loop is the one thing a
// hand-written drain cannot stand in for.
//
// The event loop owns the model, so nothing is read out of it until the
// program has stopped: p.Run returns the final model, and that is what is
// inspected.
func TestRealProgramLargeDatabase(t *testing.T) {
	const objects = 15000
	f := startFlood(t, objects)

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

	p := tea.NewProgram(m,
		tea.WithOutput(io.Discard), // View still runs, so its cost is paid
		tea.WithInput(nil),
	)
	type result struct {
		model tea.Model
		err   error
	}
	res := make(chan result, 1)
	go func() {
		model, err := p.Run()
		res <- result{model, err}
	}()

	// The outstation only answers once data transfer is active, and the
	// interrogation is what asks it to. Neither fact can be read off the
	// model from here, so drive it and watch the outstation instead.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		p.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
		time.Sleep(300 * time.Millisecond)
		if atomic.LoadUint64(&f.sent) > 0 {
			break
		}
	}
	if atomic.LoadUint64(&f.sent) == 0 {
		t.Fatal("the outstation never answered an interrogation")
	}

	start := time.Now()
	deadline = time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadUint64(&f.sent) >= objects && conn.batchPending() == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond) // let the last batch be applied
	elapsed := time.Since(start)

	p.Quit()
	var final *Model
	select {
	case r := <-res:
		if r.err != nil {
			t.Fatalf("program: %v", r.err)
		}
		var ok bool
		final, ok = r.model.(*Model)
		if !ok {
			t.Fatalf("final model is %T", r.model)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("program did not stop")
	}

	t.Logf("sent=%d points=%d asdus=%d items=%d events=%d dropped=%d elapsed=%v",
		atomic.LoadUint64(&f.sent), len(final.points), final.rxASDU, final.rxItems,
		len(final.events), conn.dropCount(), elapsed)

	if len(final.points) != objects {
		t.Errorf("%d of %d objects reached the table (%d missing)",
			len(final.points), objects, objects-len(final.points))
	}
	if conn.dropCount() != 0 {
		t.Errorf("%d messages dropped", conn.dropCount())
	}
	if final.eventsDropped != 0 {
		t.Errorf("%d arrivals discarded from the event window", final.eventsDropped)
	}
}

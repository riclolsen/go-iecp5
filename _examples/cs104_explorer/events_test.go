package main

import (
	"strings"
	"testing"
	"time"
)

// The point table keeps every information object a device reports. The event
// list is a window on the order they arrived in, and a window has to end
// somewhere — but it must end well past one interrogation of a large
// database, and when it does trim it must say so.
//
// It used to keep 5000 arrivals and say nothing: a 15000 object
// interrogation silently discarded two thirds of what the operator had just
// asked for, and the header read "5000 events" as though that were all the
// device had reported.

// interrogateN brings a model up against an n object outstation and
// interrogates it.
func interrogateN(t *testing.T, n int, history int) (*Model, func()) {
	t.Helper()
	f := startFlood(t, n)
	conn := newConnection(link{Host: f.addr, CommonAddr: 1,
		Timeout: 10 * time.Second, Reconnect: 2 * time.Second}, t.TempDir())
	if err := conn.start(); err != nil {
		t.Fatal(err)
	}
	m := NewModel(conn)
	m.width, m.height = 140, 40
	m.now = time.Now()
	m.history = history

	if !pump(t, m, func() bool { return m.active }, 20*time.Second) {
		conn.stop()
		t.Fatalf("data transfer never became active: %s", m.status)
	}
	press(t, m, "i")
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && len(m.points) < n {
		if msg, ok := nextMsg(m.conn, 200*time.Millisecond); ok {
			m.now = time.Now()
			m.Update(msg)
		}
	}
	if len(m.points) != n {
		conn.stop()
		t.Fatalf("%d of %d objects arrived", len(m.points), n)
	}
	return m, conn.stop
}

// TestEventHistoryHoldsALargeInterrogation: the default window must not trim
// one interrogation of a large device.
func TestEventHistoryHoldsALargeInterrogation(t *testing.T) {
	const objects = 15000
	m, stop := interrogateN(t, objects, defaultHistory)
	defer stop()

	if len(m.events) != objects {
		t.Errorf("event list kept %d of %d arrivals", len(m.events), objects)
	}
	if m.eventsDropped != 0 {
		t.Errorf("%d arrivals were discarded from a window of %d", m.eventsDropped, defaultHistory)
	}
}

// TestTrimmedEventHistorySaysSo: a window that does trim must not pretend
// the device reported only what is left.
func TestTrimmedEventHistorySaysSo(t *testing.T) {
	const objects, window = 6000, 1000
	m, stop := interrogateN(t, objects, window)
	defer stop()

	if len(m.events) != window {
		t.Fatalf("event list holds %d, want the window size %d", len(m.events), window)
	}
	if want := uint64(objects - window); m.eventsDropped != want {
		t.Fatalf("discarded count is %d, want %d", m.eventsDropped, want)
	}

	// The Events screen must show it.
	m.screen = ScreenEvents
	view := m.View()
	if !strings.Contains(view, "older discarded") {
		t.Errorf("the Events screen does not disclose the discarded arrivals:\n%s",
			firstLines(view, 3))
	}

	// And so must the Overview, which is where you look for what the
	// session has been doing.
	m.screen = ScreenOverview
	if got := m.historyText(); !strings.Contains(got, "discarded") {
		t.Errorf("Overview history reads %q, which does not mention the loss", got)
	}
}

func firstLines(s string, n int) string {
	parts := strings.SplitN(s, "\n", n+1)
	if len(parts) > n {
		parts = parts[:n]
	}
	return strings.Join(parts, "\n")
}

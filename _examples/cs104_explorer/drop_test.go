package main

import (
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/cs104"
)

// A large outstation database is the case where a master either keeps up or
// quietly loses measurements. These tests stand up an outstation with several
// thousand information objects, in this process, and check that every one of
// them reaches the table.

// floodOutstation answers a general interrogation with nObjects information
// objects and then reports spontaneously as fast as it is allowed to.
type floodOutstation struct {
	nObjects int
	addr     string
	srv      *cs104.Server
	stop0    func()
	// sent counts information objects the outstation believes it sent, so a
	// test compares what arrived against what left.
	sent uint64
	// spont counts the single-object event ASDUs it pushed out.
	spont uint64
}

func (f *floodOutstation) InterrogationHandler(c asdu.Connect, a *asdu.ASDU, qoi asdu.QualifierOfInterrogation) error {
	_ = a.SendReplyMirror(c, asdu.ActivationCon)
	cause := asdu.CauseOfTransmission{Cause: asdu.InterrogatedByStation}
	// 30 short floats is the most that fits one ASDU inside the 249 octet
	// limit, so a big database is a lot of ASDUs however it is packed.
	const per = 30
	for base := 0; base < f.nObjects; base += per {
		n := per
		if base+n > f.nObjects {
			n = f.nObjects - base
		}
		infos := make([]asdu.MeasuredValueFloatInfo, n)
		for i := range infos {
			infos[i] = asdu.MeasuredValueFloatInfo{
				Ioa:   asdu.InfoObjAddr(1000 + base + i),
				Value: float32(base + i),
				Qds:   asdu.QDSGood,
			}
		}
		// The send buffer is not infinite and Send does not block: an error
		// here means the ASDU was never queued, so it is retried rather
		// than counted as sent.
		for {
			if err := asdu.MeasuredValueFloat(c, false, cause, 1, infos...); err == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
		atomic.AddUint64(&f.sent, uint64(n))
	}
	return a.SendReplyMirror(c, asdu.ActivationTerm)
}

// floodSpontaneous reports n objects the way a device reports events: one
// information object per ASDU, as fast as the link will carry them.
//
// This is the shape of traffic that overwhelms a master. A packed
// interrogation reply of 5000 objects is only ~167 ASDUs; 5000 events are
// 5000 ASDUs, and an interface that spends one update cycle on each of them
// falls behind immediately.
func (f *floodOutstation) floodSpontaneous(n int) {
	cause := asdu.CauseOfTransmission{Cause: asdu.Spontaneous}
	now := time.Now()
	for i := 0; i < n; i++ {
		info := asdu.MeasuredValueFloatInfo{
			Ioa:   asdu.InfoObjAddr(50000 + i),
			Value: float32(i),
			Qds:   asdu.QDSGood,
			Time:  now,
		}
		for {
			if err := asdu.MeasuredValueFloatCP56Time2a(f.srv, cause, 1, info); err == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
		atomic.AddUint64(&f.spont, 1)
	}
}

func (f *floodOutstation) CounterInterrogationHandler(asdu.Connect, *asdu.ASDU, asdu.QualifierCountCall) error {
	return nil
}
func (f *floodOutstation) ReadHandler(asdu.Connect, *asdu.ASDU, asdu.InfoObjAddr) error { return nil }
func (f *floodOutstation) ClockSyncHandler(asdu.Connect, *asdu.ASDU, time.Time) error   { return nil }
func (f *floodOutstation) ResetProcessHandler(asdu.Connect, *asdu.ASDU, asdu.QualifierOfResetProcessCmd) error {
	return nil
}
func (f *floodOutstation) DelayAcquisitionHandler(asdu.Connect, *asdu.ASDU, uint16) error { return nil }
func (f *floodOutstation) ASDUHandlerAll(asdu.Connect, *asdu.ASDU, int) error             { return nil }
func (f *floodOutstation) ASDUHandler(asdu.Connect, *asdu.ASDU) error                     { return nil }

func startFlood(t *testing.T, nObjects int) *floodOutstation {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	f := &floodOutstation{nObjects: nObjects}
	f.srv = cs104.NewServer(f)
	f.srv.LogMode(false)
	go func() { _ = f.srv.ListenAndServer(addr) }()
	f.stop0 = func() { _ = f.srv.Close() }
	t.Cleanup(f.stop0)
	time.Sleep(150 * time.Millisecond)
	f.addr = addr
	return f
}

// TestLargeDatabaseLosesNothing is the regression test for a master that
// decided how much of a device's database it was willing to look at.
//
// The interface processes one message per update cycle with a render in
// between. One message per ASDU into a bounded channel meant a burst of a
// few thousand objects lost about 40% of itself, silently. Arrivals are
// batched instead, so the cost to the interface is one render per burst
// rather than one per ASDU.
func TestLargeDatabaseLosesNothing(t *testing.T) {
	const objects = 5000
	f := startFlood(t, objects)

	conn := newConnection(link{Host: f.addr, CommonAddr: 1,
		Timeout: 5 * time.Second, Reconnect: time.Second}, t.TempDir())
	if err := conn.start(); err != nil {
		t.Fatal(err)
	}
	defer conn.stop()

	m := NewModel(conn)
	m.width, m.height = 140, 40
	m.now = time.Now()
	m.screen = ScreenPoints

	if !pump(t, m, func() bool { return m.active }, 15*time.Second) {
		t.Fatalf("data transfer never became active: %s", m.status)
	}
	press(t, m, "i")

	// Render on every batch, the way the event loop does: the cost of
	// drawing is exactly what used to make the interface fall behind.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && len(m.points) < objects {
		if msg, ok := nextMsg(m.conn, 100*time.Millisecond); ok {
			m.now = time.Now()
			m.Update(msg)
			_ = m.View()
		}
	}

	sent := atomic.LoadUint64(&f.sent)
	t.Logf("interrogation: outstation sent %d objects; table holds %d; %d messages dropped",
		sent, len(m.points), conn.dropCount())
	if conn.dropCount() != 0 {
		t.Errorf("%d messages dropped during interrogation", conn.dropCount())
	}
	if len(m.points) != objects {
		t.Errorf("table holds %d of %d interrogated objects", len(m.points), objects)
	}

	// Phase two: the same volume as events, one information object per
	// ASDU. This is what used to lose about 40% of itself.
	const events = 4000
	done := make(chan struct{})
	go func() { f.floodSpontaneous(events); close(done) }()

	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if msg, ok := nextMsg(m.conn, 100*time.Millisecond); ok {
			m.now = time.Now()
			m.Update(msg)
			_ = m.View()
		}
		if len(m.points) >= objects+events {
			select {
			case <-done:
				break
			default:
			}
		}
		if len(m.points) >= objects+events {
			break
		}
	}

	dropped := conn.dropCount()
	t.Logf("events: outstation sent %d single-object ASDUs; table holds %d objects; %d messages dropped",
		atomic.LoadUint64(&f.spont), len(m.points), dropped)
	if dropped != 0 {
		t.Errorf("%d messages were dropped: data the device sent never reached the table", dropped)
	}
	if got, want := len(m.points), objects+events; got != want {
		t.Errorf("table holds %d objects, want %d — %d never arrived", got, want, want-got)
	}
	// Every value must be the one that was sent, not a torn or stale batch.
	for i := 0; i < objects; i += 137 {
		key := pointKey{CA: 1, IOA: uint(1000 + i)}
		p, ok := m.points[key]
		if !ok {
			t.Fatalf("object %d missing", 1000+i)
		}
		if want := fmt.Sprint(i); p.Value != want && p.Value != want+".0" {
			t.Errorf("object %d = %q, want %s", 1000+i, p.Value, want)
		}
	}
}

// newServerOn starts a server for an already-chosen address.
func newServerOn(t *testing.T, h cs104.ServerHandlerInterface, addr string) *cs104.Server {
	t.Helper()
	srv := cs104.NewServer(h)
	srv.LogMode(false)
	go func() { _ = srv.ListenAndServer(addr) }()
	t.Cleanup(func() { _ = srv.Close() })
	time.Sleep(150 * time.Millisecond)
	return srv
}

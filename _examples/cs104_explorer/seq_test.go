package main

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
)

// seqOutstation answers an interrogation with SEQUENCE ASDUs (SQ=1): one
// information object address followed by consecutive values. A large device
// packs its database this way, because it is the only way to fit a lot of
// objects into a 249 octet ASDU.
type seqOutstation struct {
	floodOutstation
}

func (f *seqOutstation) InterrogationHandler(c asdu.Connect, a *asdu.ASDU, qoi asdu.QualifierOfInterrogation) error {
	_ = a.SendReplyMirror(c, asdu.ActivationCon)
	cause := asdu.CauseOfTransmission{Cause: asdu.InterrogatedByStation}
	const per = 40
	for base := 0; base < f.nObjects; base += per {
		n := per
		if base+n > f.nObjects {
			n = f.nObjects - base
		}
		infos := make([]asdu.SinglePointInfo, n)
		for i := range infos {
			infos[i] = asdu.SinglePointInfo{
				Ioa:   asdu.InfoObjAddr(1000 + base + i),
				Value: (base+i)%2 == 0,
				Qds:   asdu.QDSGood,
			}
		}
		// true = sequence of information elements.
		for {
			if err := asdu.Single(c, true, cause, 1, infos...); err == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
		atomic.AddUint64(&f.sent, uint64(n))
	}
	return a.SendReplyMirror(c, asdu.ActivationTerm)
}

// TestSequenceInterrogation checks that a sequence-packed reply produces one
// point per information element, not one per ASDU.
func TestSequenceInterrogation(t *testing.T) {
	const objects = 15000
	base := startFlood(t, objects)
	base.srv.Close()
	time.Sleep(100 * time.Millisecond)

	f := &seqOutstation{floodOutstation{nObjects: objects}}
	f.addr = base.addr
	f.srv = newServerOn(t, f, f.addr)

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
	press(t, m, "i")

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && len(m.points) < objects {
		if msg, ok := nextMsg(m.conn, 200*time.Millisecond); ok {
			m.now = time.Now()
			m.Update(msg)
			_ = m.View()
		}
	}

	t.Logf("outstation sent %d objects in sequence ASDUs; table holds %d; asdus=%d dropped=%d",
		atomic.LoadUint64(&f.sent), len(m.points), m.rxASDU, conn.dropCount())
	if len(m.points) != objects {
		t.Errorf("%d of %d sequence-packed objects arrived (%d missing)",
			len(m.points), objects, objects-len(m.points))
	}
}

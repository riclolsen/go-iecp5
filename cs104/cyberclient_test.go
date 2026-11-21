package cs104

import (
	"net"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/secevent"
)

// stationConn is the outstation side of a raw connection to a Client.
type stationConn struct {
	t    *testing.T
	conn net.Conn
	sn   uint16
}

func (s *stationConn) send(data []byte) {
	f, _ := newIFrame(s.sn, 0, data)
	s.sn++
	_, _ = s.conn.Write(f)
}

// readI returns the next ASDU the client sent, skipping S and U frames.
func (s *stationConn) readI(d time.Duration) *asdu.ASDU {
	w := &wire{t: s.t, conn: s.conn}
	return w.read(d)
}

func (s *stationConn) expectStartDT() {
	buf := make([]byte, 6)
	_, _ = ioReadFull(s.conn, buf)
	_, _ = s.conn.Write(frStartCon)
}

func activeClient(t *testing.T, onActive func(*Client), script func(*stationConn)) (*Client, *evCollector) {
	t.Helper()
	addr := fakeOutstation(t, func(c net.Conn) {
		s := &stationConn{t: t, conn: c}
		s.expectStartDT()
		script(s)
	})
	cl, c := clientTo(t, addr, nil)
	cl.SetOnConnectHandler(func(c *Client) { c.SendStartDt() })
	if onActive != nil {
		cl.SetOnActivatedHandler(onActive)
	}
	_ = cl.Start()
	return cl, c
}

func TestClientUnsolicitedResponsesAreReported(t *testing.T) {
	_, c := activeClient(t, nil, func(s *stationConn) {
		time.Sleep(100 * time.Millisecond)
		// A confirmation of a command nobody sent.
		s.send(rawASDU(asdu.C_SC_NA_1, 1, asdu.ActivationCon, 1, obj(5, 0x01)...))
		// Interrogation data with no interrogation.
		s.send(rawASDU(asdu.M_SP_NA_1, 1, asdu.InterrogatedByStation, 1, obj(1, 0x01)...))
		// The station says it was reset remotely.
		s.send(rawASDU(asdu.M_EI_NA_1, 1, asdu.Initialized, 1, obj(0, 2)...))
		// It does not know what we asked.
		s.send(rawASDU(asdu.C_RD_NA_1, 1, asdu.UnknownIOA, 1, ioa3(77)...))
		// A command pushed at the master.
		s.send(rawASDU(asdu.C_SC_NA_1, 1, asdu.Activation, 1, obj(5, 0x01)...))
		time.Sleep(time.Second)
	})
	if e := c.wait(t, secevent.KindUnsolActResponse); e.TypeID != asdu.C_SC_NA_1 || e.IOA != 5 {
		t.Errorf("%+v", e)
	}
	if e := c.wait(t, secevent.KindUnsolInterrogated); e.Severity != secevent.SevNotice {
		t.Errorf("%+v", e)
	}
	if e := c.wait(t, secevent.KindCtrlEndOfInit); e.Severity != secevent.SevWarning {
		t.Errorf("end of init after a remote reset should be a warning: %+v", e)
	}
	c.wait(t, secevent.KindRemoteRejected)
	c.wait(t, secevent.KindProtoWrongDirection)
}

func TestClientSolicitedAnswersAreNotUnsolicited(t *testing.T) {
	_, c := activeClient(t, func(c *Client) {
		_ = c.InterrogationCmd(asdu.CauseOfTransmission{Cause: asdu.Activation}, 1, asdu.QOIStation)
	}, func(s *stationConn) {
		if s.readI(3*time.Second) == nil {
			return
		}
		s.send(rawASDU(asdu.C_IC_NA_1, 1, asdu.ActivationCon, 1, obj(0, 20)...))
		s.send(rawASDU(asdu.M_SP_NA_1, 1, asdu.InterrogatedByStation, 1, obj(1, 0x01)...))
		s.send(rawASDU(asdu.C_IC_NA_1, 1, asdu.ActivationTerm, 1, obj(0, 20)...))
		time.Sleep(time.Second)
	})
	time.Sleep(1500 * time.Millisecond)
	for _, k := range c.kinds() {
		switch k {
		case secevent.KindUnsolActResponse, secevent.KindUnsolInterrogated, secevent.KindRemoteRejected:
			t.Errorf("a solicited answer raised %s", k)
		}
	}
}

// A request to the global address is answered by each station with its own
// common address; those answers are solicited.
func TestRequestTrackerBroadcast(t *testing.T) {
	var rt requestTracker
	now := time.Now()
	rt.add(reqKey{asdu.C_IC_NA_1, asdu.GlobalCommonAddr, 0}, now)
	for _, ca := range []asdu.CommonAddr{1, 7} {
		k := reqKey{asdu.C_IC_NA_1, ca, 0}
		if !rt.live(k, now) {
			t.Fatalf("answer from station %d to a broadcast taken as unsolicited", ca)
		}
		rt.done(k, now)
	}
	// One station terminating must not end the broadcast for the others.
	if !rt.live(reqKey{asdu.C_IC_NA_1, 9, 0}, now.Add(requestTrackGrace+time.Second)) {
		t.Error("broadcast ended by the first station's termination")
	}
	if rt.live(reqKey{asdu.C_CS_NA_1, 1, 0}, now) {
		t.Error("an answer of another type matched the broadcast")
	}
	if rt.live(reqKey{asdu.C_IC_NA_1, 1, 0}, now.Add(requestTrackTTL+time.Second)) {
		t.Error("expired broadcast still live")
	}
}

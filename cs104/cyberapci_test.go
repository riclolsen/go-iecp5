package cs104

import (
	"net"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/secevent"
)

var (
	frStartAct  = []byte{0x68, 4, 0x07, 0, 0, 0}
	frStopAct   = []byte{0x68, 4, 0x13, 0, 0, 0}
	frStartCon  = []byte{0x68, 4, 0x0B, 0, 0, 0}
	frTestCon   = []byte{0x68, 4, 0x83, 0, 0, 0}
	frBadUFrame = []byte{0x68, 4, 0x07, 1, 0, 0} // reserved octet set
)

func validASDU(t *testing.T) []byte {
	t.Helper()
	a := asdu.NewASDU(asdu.ParamsWide, asdu.Identifier{Type: asdu.C_SC_NA_1,
		Variable: asdu.VariableStruct{Number: 1}, Coa: asdu.CauseOfTransmission{Cause: asdu.Activation}, CommonAddr: 1})
	a.InfoObj = []byte{1, 0, 0, 1}
	d, err := a.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func started(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn := dialRetry(t, addr)
	_, _ = conn.Write(frStartAct)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 6)); err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestFramingEvents(t *testing.T) {
	_, c, addr := startCyberServer(t, &nullHandler{}, DefaultConfig())
	conn := dialRetry(t, addr)
	defer conn.Close()
	_, _ = conn.Write([]byte{0xDE, 0xAD, 0xBE, 0xEF}) // noise
	_, _ = conn.Write([]byte{0x68, 0x02, 0x00, 0x00}) // impossible length
	_, _ = conn.Write(frBadUFrame)                    // well framed, malformed APCI
	_, _ = conn.Write(frTestCon)                      // TESTFR con nobody asked for
	_, _ = conn.Write(frStartCon)                     // a monitor-direction frame
	_, _ = conn.Write(frStartAct)

	if e := c.wait(t, secevent.KindProtoGarbage); e.Action != secevent.ActIgnored || len(e.Raw) == 0 {
		t.Errorf("garbage: %+v", e)
	}
	c.wait(t, secevent.KindProtoBadLength)
	if e := c.wait(t, secevent.KindProtoBadAPCI); e.Err == nil || len(e.Raw) != 6 {
		t.Errorf("bad apci: %+v", e)
	}
	c.wait(t, secevent.KindUnsolUConfirm)
	c.wait(t, secevent.KindProtoUWrongDir)
	c.wait(t, secevent.KindCtrlStartDT)
}

func TestStopDTAndIWhileStopped(t *testing.T) {
	_, c, addr := startCyberServer(t, &nullHandler{}, DefaultConfig())
	conn := dialRetry(t, addr)
	defer conn.Close()
	iframe, _ := newIFrame(0, 0, validASDU(t))
	_, _ = conn.Write(iframe) // never started
	e := c.wait(t, secevent.KindProtoIWhileStopped)
	if e.Severity != secevent.SevWarning || e.Action != secevent.ActIgnored {
		t.Errorf("%+v", e)
	}
	_, _ = conn.Write(frStopAct)
	c.wait(t, secevent.KindCtrlStopDT)
}

func TestSequenceViolationsAreClassified(t *testing.T) {
	data := validASDU(t)
	cases := []struct {
		name         string
		sendSN, rcv  uint16
		first        bool // send a good frame 0 first
		want         secevent.Kind
		expectedWant uint16
	}{
		{"replay of the previous frame", 0, 0, true, secevent.KindSeqNsDuplicate, 1},
		{"gap", 5, 0, true, secevent.KindSeqNsGap, 1},
		{"old frame", 20000, 0, false, secevent.KindSeqNsBehind, 0},
		{"acknowledges frames never sent", 0, 5, false, secevent.KindSeqNrAhead, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, c, addr := startCyberServer(t, &nullHandler{}, DefaultConfig())
			conn := started(t, addr)
			defer conn.Close()
			if tc.first {
				f, _ := newIFrame(0, 0, data)
				_, _ = conn.Write(f)
			}
			f, _ := newIFrame(tc.sendSN, tc.rcv, data)
			_, _ = conn.Write(f)
			e := c.wait(t, tc.want)
			if e.Action != secevent.ActDisconnected || e.Severity != secevent.SevAlert ||
				e.Fields&secevent.FieldSeq == 0 || len(e.Raw) == 0 {
				t.Errorf("%+v", e)
			}
			if tc.want != secevent.KindSeqNrAhead && (e.SendSN != tc.sendSN || e.ExpectedSN != tc.expectedWant) {
				t.Errorf("send=%d expected=%d, want %d/%d", e.SendSN, e.ExpectedSN, tc.sendSN, tc.expectedWant)
			}
			expectClosed(t, conn)
		})
	}
}

func TestSeqClassifiers(t *testing.T) {
	if ackKind(10, 20, 25) != secevent.KindSeqNrAhead || ackKind(10, 20, 5) != secevent.KindSeqNrRegressed {
		t.Error("ackKind")
	}
	if ackKind(32760, 4, 6) != secevent.KindSeqNrAhead {
		t.Error("ackKind across the wrap")
	}
	if sendSNKind(32767, 0) != secevent.KindSeqNsDuplicate || sendSNKind(3, 2) != secevent.KindSeqNsGap ||
		sendSNKind(2, 3) != secevent.KindSeqNsDuplicate || sendSNKind(0, 100) != secevent.KindSeqNsBehind {
		t.Error("sendSNKind")
	}
}

// fakeOutstation accepts one connection, runs script on it, and returns.
func fakeOutstation(t *testing.T, script func(net.Conn)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		script(conn)
	}()
	return l.Addr().String()
}

func clientTo(t *testing.T, addr string, p *secevent.Policy) (*Client, *evCollector) {
	t.Helper()
	opt := NewOption()
	_ = opt.AddRemoteServer(addr)
	opt.SetAutoReconnect(false)
	if p != nil {
		opt.SetSecurityPolicy(*p)
	}
	c := &evCollector{}
	cl := NewClient(nopClientHandler{}, opt)
	cl.LogMode(false)
	cl.SetCyberEventSink(c)
	t.Cleanup(func() { _ = cl.Close(); cl.SetCyberEventSink(nil) })
	return cl, c
}

func TestClientUnsolicitedAndWrongDirection(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		addr := fakeOutstation(t, func(c net.Conn) {
			_, _ = c.Write(frStartCon) // we never sent STARTDT act
			_, _ = c.Write(frStartAct) // a master's frame, from an outstation
			time.Sleep(500 * time.Millisecond)
		})
		cl, c := clientTo(t, addr, &secevent.Policy{IgnoreUnsolicitedU: ignore})
		_ = cl.Start()
		c.wait(t, secevent.KindUnsolUConfirm)
		c.wait(t, secevent.KindProtoUWrongDir)
		time.Sleep(100 * time.Millisecond)
		if cl.IsActive() == ignore {
			t.Errorf("IgnoreUnsolicitedU=%v but IsActive=%v", ignore, cl.IsActive())
		}
	}
}

func TestClientSequenceError(t *testing.T) {
	data := validASDU(t)
	addr := fakeOutstation(t, func(c net.Conn) {
		buf := make([]byte, 6)
		_, _ = c.Read(buf) // STARTDT act
		_, _ = c.Write(frStartCon)
		good, _ := newIFrame(0, 0, data)
		_, _ = c.Write(good)
		replay, _ := newIFrame(0, 0, data) // the same frame again
		_, _ = c.Write(replay)
		time.Sleep(time.Second)
	})
	cl, c := clientTo(t, addr, nil)
	cl.SetOnConnectHandler(func(c *Client) { c.SendStartDt() })
	_ = cl.Start()
	e := c.wait(t, secevent.KindSeqNsDuplicate)
	if e.Role != "client" || e.Action != secevent.ActDisconnected {
		t.Errorf("%+v", e)
	}
}

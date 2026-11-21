package cs101

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/secevent"
)

type collector struct {
	mu sync.Mutex
	ev []secevent.Event
}

func (c *collector) Handle(e secevent.Event) { c.mu.Lock(); c.ev = append(c.ev, e); c.mu.Unlock() }

func (c *collector) wait(t *testing.T, k secevent.Kind) secevent.Event {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		c.mu.Lock()
		for _, e := range c.ev {
			if e.Kind == k {
				c.mu.Unlock()
				return e
			}
		}
		c.mu.Unlock()
	}
	t.Fatalf("no %s event", k)
	return secevent.Event{}
}

func startCyberServer(t *testing.T, c *collector) (*Server, *duplex, func()) {
	t.Helper()
	portA, portB := pipeLine()
	sh := &srvHandler{gotCmd: make(chan asdu.QualifierOfInterrogation, 4), any: make(chan *asdu.ASDU, 4)}
	srv := NewServer(sh)
	sh.srv = srv
	srv.SetLogMode(false)
	srv.SetCyberEventSink(c)
	srv.config = testConfig(ModeUnbalanced)
	srv.port = portB
	srv.setConnectStatus(statusConnected)
	srv.fcbExpected = true
	srv.beginCyber()
	srv.connCtx, srv.connCancel = context.WithCancel(context.Background())
	srv.wg.Add(3)
	go srv.frameRecvLoop()
	go srv.frameSendLoop()
	go srv.handlerLoop()
	return srv, portA, func() {
		srv.connCancel()
		_ = portB.Close()
		srv.wg.Wait()
		srv.cyber.em.Close()
	}
}

func TestServerLinkEvents(t *testing.T) {
	c := &collector{}
	_, port, stop := startCyberServer(t, c)
	defer stop()

	// A frame for another station.
	writeFixed(t, port, PrimFcReqStatus, 0x07)
	if e := c.wait(t, secevent.KindLinkForeignAddress); e.LinkAddr != 7 {
		t.Errorf("link address %d", e.LinkAddr)
	}

	// A broadcast that asks for a reply.
	writeFixed(t, port, PrimFcReqStatus, 0xFF)
	c.wait(t, secevent.KindLinkBroadcastReply)

	// A bad checksum.
	ctrl := CtrlPRM | PrimFcReqStatus
	if _, err := port.Write([]byte{StartFixed, ctrl, 0x01, 0x00, EndChar}); err != nil {
		t.Fatal(err)
	}
	c.wait(t, secevent.KindProtoCS101Frame)

	// Reset link, then a repeated FCB.
	writeFixed(t, port, PrimFcResetLink, 0x01)
	c.wait(t, secevent.KindCtrlLinkReset)
}

func TestServerInspectsASDUs(t *testing.T) {
	c := &collector{}
	_, port, stop := startCyberServer(t, c)
	defer stop()

	// A command in the monitor direction is a wrong-direction ASDU.
	a := asdu.NewASDU(asdu.ParamsStandard101, asdu.Identifier{
		Type:       asdu.M_SP_NA_1,
		Variable:   asdu.VariableStruct{Number: 1},
		Coa:        asdu.CauseOfTransmission{Cause: asdu.Spontaneous},
		CommonAddr: 1,
	})
	a.InfoObj = []byte{0x01, 0x00, 0x01}
	raw, err := a.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	out, _ := NewDataFrame(CtrlPRM|PrimFcUserDataNoConf, []byte{0x01}, raw).MarshalBinary(1)
	if _, err := port.Write(out); err != nil {
		t.Fatal(err)
	}
	c.wait(t, secevent.KindProtoWrongDirection)
}

func TestClientLinkEvents(t *testing.T) {
	portA, portB := pipeLine()
	c := &collector{}
	cli, stop := startClient(t, &cliHandler{}, testConfig(ModeUnbalanced), portA)
	defer stop()
	cli.SetCyberEventSink(c)
	defer cli.cyber.em.Close()
	// Drain whatever the client transmits so its writes never block.
	go func() {
		buf := make([]byte, 256)
		for {
			if _, err := portB.Read(buf); err != nil {
				return
			}
		}
	}()

	// A status answer from a station that is not polled.
	ctrl := byte(SecFcRespStatus)
	if _, err := portB.Write([]byte{StartFixed, ctrl, 0x09, ctrl + 0x09, EndChar}); err != nil {
		t.Fatal(err)
	}
	if e := c.wait(t, secevent.KindLinkUnexpectedAddr); e.LinkAddr != 9 {
		t.Errorf("link address %d", e.LinkAddr)
	}

	// A bad checksum.
	if _, err := portB.Write([]byte{StartFixed, ctrl, 0x01, 0x00, EndChar}); err != nil {
		t.Fatal(err)
	}
	c.wait(t, secevent.KindProtoCS101Frame)
}

func TestBusyConnectionIsReported(t *testing.T) {
	c := &collector{}
	srv := NewServer(&srvHandler{})
	srv.SetLogMode(false)
	srv.SetCyberEventSink(c)
	defer srv.cyber.em.Close()
	srv.reportBusy(&net.TCPAddr{IP: net.IPv4(192, 0, 2, 9), Port: 4000})
	e := c.wait(t, secevent.KindConnRejectedBusy)
	if e.Peer.RemoteAddr.String() != "192.0.2.9:4000" {
		t.Errorf("peer %v", e.Peer.RemoteAddr)
	}
}

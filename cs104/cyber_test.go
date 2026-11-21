package cs104

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/secevent"
)

type evCollector struct {
	mu sync.Mutex
	ev []secevent.Event
}

func (c *evCollector) Handle(e secevent.Event) { c.mu.Lock(); c.ev = append(c.ev, e); c.mu.Unlock() }

func (c *evCollector) find(k secevent.Kind) (secevent.Event, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.ev {
		if e.Kind == k {
			return e, true
		}
	}
	return secevent.Event{}, false
}

func (c *evCollector) wait(t *testing.T, k secevent.Kind) secevent.Event {
	t.Helper()
	for end := time.Now().Add(4 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if e, ok := c.find(k); ok {
			return e
		}
	}
	t.Fatalf("no %s event; have %v", k, c.kinds())
	return secevent.Event{}
}

func (c *evCollector) kinds() []secevent.Kind {
	c.mu.Lock()
	defer c.mu.Unlock()
	var k []secevent.Kind
	for _, e := range c.ev {
		k = append(k, e.Kind)
	}
	return k
}

type peerHandler struct {
	nullHandler
	got chan secevent.PeerInfo
}

func (h *peerHandler) ASDUHandler(c asdu.Connect, a *asdu.ASDU) error {
	if p, ok := secevent.PeerOf(c); ok {
		select {
		case h.got <- p:
		default:
		}
	}
	return nil
}

func startCyberServer(t *testing.T, h ServerHandlerInterface, cfg Config) (*Server, *evCollector, string) {
	t.Helper()
	addr := freeAddr(t)
	c := &evCollector{}
	srv := NewServer(h)
	srv.LogMode(testing.Verbose())
	srv.SetConfig(cfg)
	srv.SetCyberEventSink(c)
	go func() { _ = srv.ListenAndServer(addr) }()
	t.Cleanup(func() { _ = srv.Close(); srv.SetCyberEventSink(nil) })
	return srv, c, addr
}

func TestConnAcceptedAndNoData(t *testing.T) {
	_, c, addr := startCyberServer(t, &nullHandler{}, DefaultConfig())
	conn := dialRetry(t, addr)
	a := c.wait(t, secevent.KindConnAccepted)
	if a.Protocol != "iec104" || a.Role != "server" || a.SessionID == 0 ||
		!a.Peer.RemoteAddr.IsValid() || a.Peer.RemoteAddr.Addr().String() != "127.0.0.1" ||
		a.Peer.Network != "tcp" || a.Severity != secevent.SevInfo {
		t.Errorf("accepted event: %+v", a)
	}
	_ = conn.Close() // a scan: connect and leave
	if e := c.wait(t, secevent.KindConnNoData); e.SessionID != a.SessionID {
		t.Errorf("no_data session %d, accepted session %d", e.SessionID, a.SessionID)
	}
	if e := c.wait(t, secevent.KindConnClosed); e.Detail == "" {
		t.Error("closed event has no detail")
	}
}

func TestSessionWithTrafficIsNotNoData(t *testing.T) {
	_, c, addr := startCyberServer(t, &nullHandler{}, DefaultConfig())
	conn := dialRetry(t, addr)
	_, _ = conn.Write([]byte{0x68, 4, 0x07, 0, 0, 0}) // STARTDT act
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 6)); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	c.wait(t, secevent.KindConnClosed)
	if _, ok := c.find(secevent.KindConnNoData); ok {
		t.Error("no_data raised for a session that exchanged frames")
	}
}

func TestT1TimeoutEvent(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SendUnAckTimeout1, cfg.RecvUnAckTimeout2, cfg.IdleTimeout3 = 2*time.Second, time.Second, time.Second
	_, c, addr := startCyberServer(t, &nullHandler{}, cfg)
	conn := dialRetry(t, addr) // never answers the TESTFR act
	defer conn.Close()
	e := c.wait(t, secevent.KindConnT1Timeout)
	if e.Action != secevent.ActDisconnected {
		t.Errorf("action %v", e.Action)
	}
}

func TestPeerInfoReachableFromHandler(t *testing.T) {
	h := &peerHandler{got: make(chan secevent.PeerInfo, 1)}
	_, _, addr := startCyberServer(t, h, DefaultConfig())
	conn := dialRetry(t, addr)
	defer conn.Close()
	// STARTDT act, then an I-frame carrying C_SC_NA_1 so ASDUHandler runs.
	_, _ = conn.Write([]byte{0x68, 4, 0x07, 0, 0, 0})
	time.Sleep(100 * time.Millisecond)
	a := asdu.NewASDU(asdu.ParamsWide, asdu.Identifier{Type: asdu.C_SC_NA_1,
		Variable: asdu.VariableStruct{Number: 1}, Coa: asdu.CauseOfTransmission{Cause: asdu.Activation}, CommonAddr: 1})
	a.InfoObj = []byte{1, 0, 0, 1}
	data, err := a.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	frame, _ := newIFrame(0, 0, data)
	_, _ = conn.Write(frame)
	select {
	case p := <-h.got:
		if p.RemoteAddr != netipAddrPort(conn.LocalAddr()) || p.TLS != nil {
			t.Errorf("peer %+v, want remote %v", p, conn.LocalAddr())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not see the peer")
	}
}

func TestAcceptErrorEvent(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := &flakyListener{Listener: inner}
	l.fails.Store(1)
	c := &evCollector{}
	srv := NewServer(&nullHandler{})
	srv.LogMode(false)
	srv.SetCyberEventSink(c)
	go func() { _ = srv.serve(l) }()
	defer func() { _ = srv.Close(); srv.SetCyberEventSink(nil) }()
	e := c.wait(t, secevent.KindConnAcceptError)
	if e.Severity != secevent.SevCritical || e.Err == nil {
		t.Errorf("accept_error: %+v", e)
	}
}

func TestDialFailedEvent(t *testing.T) {
	addr := freeAddr(t) // nothing listening
	c := &evCollector{}
	opt := NewOption()
	_ = opt.AddRemoteServer(addr)
	opt.SetAutoReconnect(false)
	cl := NewClient(nopClientHandler{}, opt)
	cl.LogMode(false)
	cl.SetCyberEventSink(c)
	defer cl.SetCyberEventSink(nil)
	if err := cl.Start(); err != nil {
		t.Fatal(err)
	}
	e := c.wait(t, secevent.KindConnDialFailed)
	if e.Role != "client" || e.Err == nil {
		t.Errorf("dial_failed: %+v", e)
	}
}

func TestNoSinkIsInert(t *testing.T) {
	srv := NewServer(&nullHandler{})
	if srv.cyber.Enabled() {
		t.Fatal("enabled by default")
	}
	srv.ReportCyberEvent(secevent.Event{Kind: secevent.KindConnClosed}) // must not panic
	if _, ok := SecurityStats(srv); !ok {
		t.Error("Server has no stats")
	}
	spec := NewServerSpecial(&nullHandler{}, NewOption())
	c := &evCollector{}
	if !SetCyberEventSink(spec, c) {
		t.Fatal("ServerSpecial cannot take a sink")
	}
	defer SetCyberEventSink(spec, nil)
	if SetCyberEventSink(42, c) {
		t.Error("accepted a value that raises no events")
	}
}

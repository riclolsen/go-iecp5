package cs104

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/netpeer"
	"github.com/riclolsen/go-iecp5/secevent"
)

func mustMAC(s string) net.HardwareAddr { m, _ := net.ParseMAC(s); return m }

// swappable resolver: the test decides what MAC loopback "has".
type fakeMAC struct {
	mu  sync.Mutex
	mac net.HardwareAddr
}

func (f *fakeMAC) set(s string) { f.mu.Lock(); f.mac = mustMAC(s); f.mu.Unlock() }
func (f *fakeMAC) Lookup(context.Context, netip.Addr) (netpeer.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mac == nil {
		return netpeer.Result{OnLink: true}, netpeer.ErrNotFound
	}
	return netpeer.Result{MAC: f.mac, Source: netpeer.SourceStatic, OnLink: true}, nil
}

func policyServer(t *testing.T, p secevent.Policy) (*Server, *evCollector, string) {
	t.Helper()
	addr := freeAddr(t)
	c := &evCollector{}
	p.Sink = c
	if p.MACCache == nil {
		p.MACCache = netpeer.NewCache(netpeer.CacheOptions{})
	}
	srv := NewServer(&nullHandler{})
	srv.LogMode(false)
	srv.SetSecurityPolicy(p)
	go func() { _ = srv.ListenAndServer(addr) }()
	t.Cleanup(func() { _ = srv.Close(); srv.SetCyberEventSink(nil) })
	return srv, c, addr
}

// expectClosed asserts the server closed conn without our sending anything.
func expectClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 512)
	for {
		// Anything the server had already queued may arrive first.
		if _, err := conn.Read(buf); err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				t.Fatal("connection was not closed")
			}
			return
		}
	}
}

func TestAllowlistRejectsAndReports(t *testing.T) {
	_, c, addr := policyServer(t, secevent.Policy{
		AllowedPeers: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, RejectUnlisted: true})
	conn := dialRetry(t, addr)
	defer conn.Close()
	expectClosed(t, conn)
	e := c.wait(t, secevent.KindConnUnexpected)
	if e.Action != secevent.ActRejected || e.Severity != secevent.SevWarning {
		t.Errorf("%+v", e)
	}
	if _, ok := c.find(secevent.KindConnAccepted); ok {
		t.Error("a rejected peer was reported as accepted")
	}
}

func TestAllowlistObserveOnly(t *testing.T) {
	_, c, addr := policyServer(t, secevent.Policy{AllowedPeers: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}})
	conn := dialRetry(t, addr)
	defer conn.Close()
	if e := c.wait(t, secevent.KindConnUnexpected); e.Action != secevent.ActObserved {
		t.Errorf("action %v", e.Action)
	}
	c.wait(t, secevent.KindConnAccepted) // still served
}

func TestSessionLimitPerIP(t *testing.T) {
	srv, c, addr := policyServer(t, secevent.Policy{MaxSessionsPerIP: 2})
	a, b := dialRetry(t, addr), dialRetry(t, addr)
	defer a.Close()
	defer b.Close()
	for i := 0; srv.GetSessionsLen() < 2 && i < 100; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	third := dialRetry(t, addr)
	defer third.Close()
	expectClosed(t, third)
	if e := c.wait(t, secevent.KindConnRejectedLimit); e.Action != secevent.ActRejected {
		t.Errorf("%+v", e)
	}
	// A slot freed is a slot usable again.
	_ = a.Close()
	for i := 0; srv.GetSessionsLen() > 1 && i < 100; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	d := dialRetry(t, addr)
	defer d.Close()
	_ = d.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := d.Read(make([]byte, 1)); err == nil {
		t.Error("connection after a slot was freed was closed")
	}
}

func TestAcceptRateReportsAndCanThrottle(t *testing.T) {
	for _, act := range []secevent.Action{secevent.ActObserved, secevent.ActThrottled} {
		_, c, addr := policyServer(t, secevent.Policy{
			AcceptRate: secevent.Rate{N: 3, Per: time.Minute}, FloodAction: act})
		var conns []net.Conn
		for i := 0; i < 8; i++ {
			conns = append(conns, dialRetry(t, addr))
		}
		e := c.wait(t, secevent.KindConnRate)
		if act == secevent.ActThrottled {
			if e.Action != secevent.ActRejected {
				t.Errorf("throttled: action %v", e.Action)
			}
			expectClosed(t, conns[7])
		} else if e.Action != secevent.ActObserved {
			t.Errorf("observed: action %v", e.Action)
		}
		for _, cn := range conns {
			cn.Close()
		}
	}
}

func TestPeerMACInPeerInfoAndEvents(t *testing.T) {
	f := &fakeMAC{}
	f.set("aa:bb:cc:00:00:01")
	srv, c, addr := policyServer(t, secevent.Policy{Resolver: f})
	conn := dialRetry(t, addr)
	defer conn.Close()
	e := c.wait(t, secevent.KindConnAccepted)
	if e.Peer.MAC.String() != "aa:bb:cc:00:00:01" || e.Peer.MACSource != secevent.MACStatic || !e.Peer.OnLink {
		t.Errorf("accepted peer %+v", e.Peer)
	}
	// The same through the session API.
	srv.mux.Lock()
	var got secevent.PeerInfo
	for s := range srv.sessions {
		got = s.PeerInfo()
	}
	srv.mux.Unlock()
	if got.MAC.String() != "aa:bb:cc:00:00:01" {
		t.Errorf("PeerInfo().MAC = %v", got.MAC)
	}
}

func TestMACChangeForSameIP(t *testing.T) {
	f := &fakeMAC{}
	f.set("aa:bb:cc:00:00:01")
	_, c, addr := policyServer(t, secevent.Policy{Resolver: f})
	first := dialRetry(t, addr)
	c.wait(t, secevent.KindConnAccepted)
	_ = first.Close()
	f.set("aa:bb:cc:00:00:99") // same IP, different box
	second := dialRetry(t, addr)
	defer second.Close()
	e := c.wait(t, secevent.KindPeerMACChanged)
	if e.Severity != secevent.SevAlert || e.Peer.MAC.String() != "aa:bb:cc:00:00:99" {
		t.Errorf("%+v", e)
	}
}

func TestMACPinning(t *testing.T) {
	f := &fakeMAC{}
	f.set("aa:bb:cc:00:00:01")
	loop := netip.MustParseAddr("127.0.0.1")
	_, c, addr := policyServer(t, secevent.Policy{Resolver: f, RejectBindingMismatch: true,
		PeerBindings: map[netip.Addr]net.HardwareAddr{loop: mustMAC("aa:bb:cc:00:00:02")}})
	conn := dialRetry(t, addr)
	defer conn.Close()
	expectClosed(t, conn)
	if e := c.wait(t, secevent.KindPeerMACPinning); e.Severity != secevent.SevCritical {
		t.Errorf("%+v", e)
	}
	c.wait(t, secevent.KindConnUnexpectedMAC)

	f.set("aa:bb:cc:00:00:02") // the right box
	ok := dialRetry(t, addr)
	defer ok.Close()
	c.wait(t, secevent.KindConnAccepted)
}

func TestMACChangeMidSessionDisconnects(t *testing.T) {
	f := &fakeMAC{}
	f.set("aa:bb:cc:00:00:01")
	_, c, addr := policyServer(t, secevent.Policy{Resolver: f, MACRecheckInterval: 100 * time.Millisecond,
		DisconnectOnMACChange: true})
	conn := dialRetry(t, addr)
	defer conn.Close()
	c.wait(t, secevent.KindConnAccepted)
	f.set("aa:bb:cc:00:00:77")
	e := c.wait(t, secevent.KindPeerMACChangedLive)
	if e.Severity != secevent.SevCritical || e.Action != secevent.ActDisconnected {
		t.Errorf("%+v", e)
	}
	expectClosed(t, conn)
}

func TestNeverStartedSessionIsClosed(t *testing.T) {
	_, c, addr := policyServer(t, secevent.Policy{InactiveTimeout: 500 * time.Millisecond})
	conn := dialRetry(t, addr)
	defer conn.Close()
	c.wait(t, secevent.KindConnNeverStarted)
	expectClosed(t, conn)
}

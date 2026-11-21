package cs104

import (
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
)

func shortT1() Config {
	c := DefaultConfig()
	c.SendUnAckTimeout1 = 2 * time.Second
	c.RecvUnAckTimeout2 = time.Second
	return c
}

// flakyListener fails Accept a number of times before behaving normally.
type flakyListener struct {
	net.Listener
	fails atomic.Int32
}

func (l *flakyListener) Accept() (net.Conn, error) {
	if l.fails.Add(-1) >= 0 {
		return nil, errors.New("accept tcp: too many open files")
	}
	return l.Listener.Accept()
}

// A failing Accept, as when descriptors run out, used to end ListenAndServer
// and with it the outstation.
func TestAcceptErrorDoesNotStopTheServer(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := &flakyListener{Listener: inner}
	l.fails.Store(3)

	srv := NewServer(&nullHandler{})
	srv.LogMode(false)
	done := make(chan error, 1)
	go func() { done <- srv.serve(l) }()

	var conn net.Conn
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err = net.Dial("tcp", inner.Addr().String()); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// STARTDT act must be answered, proving the session is being served.
	if _, err := conn.Write([]byte{0x68, 4, 0x07, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 6)
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("no STARTDT con after accept errors: %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("server stopped: %v", err)
	default:
	}
	_ = srv.Close()
}

// A frame that starts and never finishes must not hold the connection.
func TestStalledFrameClosesConnection(t *testing.T) {
	addr := freeAddr(t)
	srv := NewServer(&nullHandler{})
	srv.LogMode(false)
	srv.SetConfig(shortT1())
	go func() { _ = srv.ListenAndServer(addr) }()
	defer srv.Close()

	conn := dialRetry(t, addr)
	defer conn.Close()
	// Start of an I-frame announcing 20 octets, then silence.
	if _, err := conn.Write([]byte{0x68, 20, 0, 0}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("expected the connection to be closed")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("server did not close the stalled connection: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("closed after %v, want about t₁ (2s)", d)
	}
}

// A peer that stops reading must not freeze the session: the write deadline
// ends it.
func TestWriteStallEndsSession(t *testing.T) {
	cfg := shortT1()
	srvConn, peer := net.Pipe() // unbuffered: a write blocks until the peer reads
	defer peer.Close()
	lost := make(chan struct{})
	sess := &SrvSession{
		config: &cfg, params: asdu.ParamsWide, handler: &nullHandler{}, conn: srvConn,
		rcvASDU: make(chan []byte, 16), sendASDU: make(chan []byte, 16),
		rcvRaw: make(chan []byte, 16), sendRaw: make(chan []byte, 16),
		connectionLost: func(asdu.Connect) { close(lost) },
	}
	sess.LogMode(false)
	go sess.run(t.Context())

	// STARTDT act: the con cannot be written because peer never reads.
	if _, err := peer.Write([]byte{0x68, 4, 0x07, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lost:
	case <-time.After(6 * time.Second):
		t.Fatal("session survived a peer that does not read")
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := l.Addr().String()
	_ = l.Close()
	return a
}

func dialRetry(t *testing.T, addr string) net.Conn {
	t.Helper()
	var err error
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		var c net.Conn
		if c, err = net.Dial("tcp", addr); err == nil {
			return c
		}
	}
	t.Fatal(err)
	return nil
}

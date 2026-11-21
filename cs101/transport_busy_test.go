package cs101

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// While one connection is being served a second must be turned away at once,
// not left in the kernel backlog.
func TestTCPServerRejectsSecondConnectionWhileBusy(t *testing.T) {
	tr := NewTransporter(TransportTCPServer, SerialConfig{}, TCPConfig{Address: "127.0.0.1:0"})
	var rejected atomic.Int32
	tr.SetBusyHandler(func(net.Addr) { rejected.Add(1) })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	type res struct {
		c   io.ReadWriteCloser
		err error
	}
	opened := make(chan res, 1)
	go func() { c, _, err := tr.Open(ctx); opened <- res{c, err} }()

	var addr net.Addr
	for end := time.Now().Add(2 * time.Second); addr == nil && time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		addr = tr.ListenAddr()
	}
	if addr == nil {
		t.Fatal("not listening")
	}
	first, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	r := <-opened
	if r.err != nil {
		t.Fatal(r.err)
	}

	second, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	_ = second.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := second.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("second connection not closed by the server: %v", err)
	}
	if rejected.Load() != 1 {
		t.Fatalf("busy handler called %d times, want 1", rejected.Load())
	}

	// Once the first is closed, the next connection is served.
	_ = r.c.Close()
	go func() { c, _, err := tr.Open(ctx); opened <- res{c, err} }()
	third, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	select {
	case r2 := <-opened:
		if r2.err != nil {
			t.Fatal(r2.err)
		}
		_ = r2.c.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("third connection not served after the first closed")
	}
}

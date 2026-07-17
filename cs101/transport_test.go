package cs101

import (
	"net"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
)

// freeTCPAddr reserves an ephemeral local port and returns its address.
func freeTCPAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// TestTCPEncapsulatedExchange runs the unbalanced 101 exchange with the
// FT1.2 frames encapsulated in TCP: the secondary station listens
// (TransportTCPServer), the primary dials out (TransportTCPClient), both
// through their real Start() connection managers.
func TestTCPEncapsulatedExchange(t *testing.T) {
	addr := freeTCPAddr(t)

	baseCfg := testConfig(ModeUnbalanced)
	baseCfg.Serial = SerialConfig{} // TCP transport must not require serial settings
	baseCfg.TCP.Address = addr

	srvCfg := baseCfg
	srvCfg.Transport = TransportTCPServer
	sh := &srvHandler{gotCmd: make(chan asdu.QualifierOfInterrogation, 4), any: make(chan *asdu.ASDU, 4)}
	srv := NewServer(sh)
	sh.srv = srv
	srv.SetConfig(srvCfg) // re-creates the logger, so silence it afterwards
	srv.SetLogMode(false)
	srv.SetReconnectInterval(50 * time.Millisecond)
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	defer srv.Close()

	cliCfg := baseCfg
	cliCfg.Transport = TransportTCPClient
	ch := &cliHandler{inro: make(chan *asdu.ASDU, 4), any: make(chan *asdu.ASDU, 4)}
	opt := NewOption()
	if err := opt.SetConfig(cliCfg); err != nil {
		t.Fatalf("client config: %v", err)
	}
	opt.SetAutoReconnect(true).SetReconnectInterval(50 * time.Millisecond)
	cli := NewClient(ch, opt)
	if cli == nil {
		t.Fatal("NewClient returned nil")
	}
	cli.SetLogMode(false)
	if err := cli.Start(); err != nil {
		t.Fatalf("client start: %v", err)
	}
	defer cli.Close()

	// Link must come up through TCP.
	deadline := time.After(10 * time.Second)
	for !cli.IsLinkActive() {
		select {
		case <-deadline:
			t.Fatal("link never became active over TCP")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Command reaches the secondary, response comes back via polling.
	if err := cli.InterrogationCmd(asdu.CauseOfTransmission{Cause: asdu.Activation}, 1, asdu.QOIStation); err != nil {
		t.Fatalf("InterrogationCmd: %v", err)
	}
	select {
	case qoi := <-sh.gotCmd:
		if qoi != asdu.QOIStation {
			t.Fatalf("server got QOI %d, want %d", qoi, asdu.QOIStation)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("secondary never received the interrogation command")
	}
	select {
	case a := <-ch.inro:
		if a.Identifier.Type != asdu.M_SP_NA_1 {
			t.Fatalf("got response type %v, want M_SP_NA_1", a.Identifier.Type)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("primary never received the buffered interrogation response")
	}

	// Close must unblock everything, including a pending Accept.
	_ = cli.Close()
	_ = srv.Close()
	deadline = time.After(5 * time.Second)
	for srv.connectStatus() != statusInitial {
		select {
		case <-deadline:
			t.Fatal("server did not shut down (Accept not unblocked?)")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

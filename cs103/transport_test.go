package cs103

import (
	"net"
	"testing"
	"time"
)

// TestTCPEncapsulatedMaster runs the 103 master against the fake protection
// device with the FT1.2 frames encapsulated in TCP: the test listens as the
// terminal server, the client dials out (TransportTCPClient) through its
// real Start() connection manager.
func TestTCPEncapsulatedMaster(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	devReady := make(chan *fakeDevice, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		dev := newFakeDevice(t, conn, 3)
		devReady <- dev
		dev.run()
	}()

	cfg := testConfig()
	cfg.Serial = SerialConfig{} // TCP transport must not require serial settings
	cfg.Transport = TransportTCPClient
	cfg.TCP.Address = l.Addr().String()

	h := newRecHandler()
	opt := NewOption()
	if err := opt.SetConfig(cfg); err != nil {
		t.Fatalf("config: %v", err)
	}
	opt.SetAutoReconnect(true).SetReconnectInterval(50 * time.Millisecond)
	cli := NewClient(h, opt)
	if cli == nil {
		t.Fatal("NewClient returned nil")
	}
	cli.SetLogMode(false)
	if err := cli.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer cli.Close()

	var dev *fakeDevice
	select {
	case dev = <-devReady:
	case <-time.After(5 * time.Second):
		t.Fatal("client never connected to the TCP endpoint")
	}
	defer dev.stop()

	// Link init + identification collection must work over TCP.
	select {
	case id := <-h.ident:
		if id.ASCII != "FAKEDEV1" {
			t.Fatalf("identification = %+v", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("identification message never arrived over TCP")
	}

	// Command round trip with RII-matched acknowledgement.
	if err := cli.GeneralCommand(3, FunOvercurrentProtection, InfAutoRecloserActive, DCOOn, 7); err != nil {
		t.Fatalf("GeneralCommand: %v", err)
	}
	select {
	case cmd := <-dev.gotCommand:
		if cmd.Dco != DCOOn || cmd.Rii != 7 {
			t.Fatalf("device got command %+v", cmd)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("device never received the general command")
	}
	select {
	case ack := <-h.cmdAcks:
		if ack.Sin != 7 {
			t.Fatalf("command ack = %+v, want RII 7", ack)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command acknowledgement never arrived")
	}
}

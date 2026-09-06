package cs101

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
)

// A multi-drop line is one shared wire. Two rules follow from that, and both
// were being broken:
//
//   - A secondary that reports DFC=1 has a full receive buffer. Sending it
//     more user data loses the data.
//   - A frame addressed to the broadcast address reaches every station. If
//     they answer, they all transmit at once and the replies collide.

// --- broadcast ---

// writeFixed sends a fixed-length primary frame straight onto the line.
func writeFixed(t *testing.T, port *duplex, fun, addr byte) {
	t.Helper()
	ctrl := CtrlPRM | fun
	if _, err := port.Write([]byte{StartFixed, ctrl, addr, byte(ctrl + addr), EndChar}); err != nil {
		t.Fatal(err)
	}
}

// readWithin returns whatever the station put on the line, or nil.
func readWithin(port *duplex, d time.Duration) []byte {
	got := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 64)
		if n, err := port.Read(buf); err == nil {
			got <- buf[:n]
		}
	}()
	select {
	case b := <-got:
		return b
	case <-time.After(d):
		return nil
	}
}

func TestSecondaryNeverAnswersBroadcast(t *testing.T) {
	for _, tt := range []struct {
		name      string
		addr, fun byte
		wantReply bool
	}{
		{"unicast reset link", 0x01, PrimFcResetLink, true},
		{"unicast request status", 0x01, PrimFcReqStatus, true},
		{"broadcast reset link", 0xFF, PrimFcResetLink, false},
		{"broadcast test link", 0xFF, PrimFcTestLink, false},
		{"broadcast request status", 0xFF, PrimFcReqStatus, false},
		{"broadcast request class 1", 0xFF, PrimFcReqData1, false},
		{"broadcast request class 2", 0xFF, PrimFcReqData2, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			portA, portB := pipeLine()
			sh := &srvHandler{gotCmd: make(chan asdu.QualifierOfInterrogation, 4),
				any: make(chan *asdu.ASDU, 4)}
			srv, stop := startServer(t, sh, testConfig(ModeUnbalanced), portB)
			sh.srv = srv
			defer stop()

			writeFixed(t, portA, tt.fun, tt.addr)
			reply := readWithin(portA, 800*time.Millisecond)

			switch {
			case tt.wantReply && reply == nil:
				t.Error("no reply to a frame addressed to this station")
			case !tt.wantReply && reply != nil:
				t.Errorf("answered a broadcast with % x: every station on the line would transmit together", reply)
			}
		})
	}
}

// Broadcast data must still be delivered — silently, but delivered.
func TestSecondaryTakesBroadcastUserData(t *testing.T) {
	portA, portB := pipeLine()
	sh := &srvHandler{gotCmd: make(chan asdu.QualifierOfInterrogation, 4),
		any: make(chan *asdu.ASDU, 4)}
	srv, stop := startServer(t, sh, testConfig(ModeUnbalanced), portB)
	sh.srv = srv
	defer stop()

	a := asdu.NewASDU(asdu.ParamsStandard101, asdu.Identifier{
		Type:       asdu.C_SC_NA_1,
		Variable:   asdu.VariableStruct{Number: 1},
		Coa:        asdu.CauseOfTransmission{Cause: asdu.Activation},
		CommonAddr: 1,
	})
	a.InfoObj = []byte{0x64, 0x00, 0x01}
	raw, err := a.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	// SEND/NO REPLY to the broadcast address.
	f := NewDataFrame(CtrlPRM|PrimFcUserDataNoConf, []byte{0xFF}, raw)
	out, err := f.MarshalBinary(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := portA.Write(out); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-sh.any:
		if got.Type != asdu.C_SC_NA_1 {
			t.Errorf("delivered %s", got.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast user data was not delivered to the application")
	}
	if reply := readWithin(portA, 500*time.Millisecond); reply != nil {
		t.Errorf("answered broadcast user data with % x", reply)
	}
}

// --- data flow control ---

// dfcSecondary plays a secondary by hand so a test can control DFC exactly.
// It acknowledges everything, with DFC set while `full` is true, and counts
// the user-data frames the primary sends.
type dfcSecondary struct {
	port *duplex
	mu   sync.Mutex
	full bool

	userData int32
	stop     chan struct{}
}

func (d *dfcSecondary) setFull(v bool) {
	d.mu.Lock()
	d.full = v
	d.mu.Unlock()
}

func (d *dfcSecondary) isFull() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.full
}

func (d *dfcSecondary) run() {
	ctx := context.Background()
	for {
		select {
		case <-d.stop:
			return
		default:
		}
		f, err := ParseFrame(d.port, 1, &ctx)
		if err != nil {
			return
		}
		if f.Start == StartVariable && f.GetControlField().Fun == PrimFcUserDataConf {
			atomic.AddInt32(&d.userData, 1)
		}
		ack := ControlField{PRM: false, Fun: SecFcConfACK, DFC: d.isFull()}
		resp := &Frame{Start: StartFixed, Control: ack.Value(), LinkAddr: []byte{0x01}}
		raw, err := resp.MarshalBinary(1)
		if err != nil {
			return
		}
		if _, err := d.port.Write(raw); err != nil {
			return
		}
	}
}

func (d *dfcSecondary) count() int32 { return atomic.LoadInt32(&d.userData) }

// queueOne puts one command in the primary's send queue.
func queueOne(t *testing.T, cli *Client, addr uint16) {
	t.Helper()
	a := asdu.NewASDU(cli.Params(), asdu.Identifier{
		Type:       asdu.C_SC_NA_1,
		Variable:   asdu.VariableStruct{Number: 1},
		Coa:        asdu.CauseOfTransmission{Cause: asdu.Activation},
		CommonAddr: 1,
	})
	a.InfoObj = []byte{0x64, 0x00, 0x01}
	if err := cli.SendTo(a, addr); err != nil {
		t.Fatalf("SendTo: %v", err)
	}
}

// TestDFCHoldsOffUserDataAndReleasesIt: while a station reports DFC=1 no user
// data may go to it, and when it clears the data must flow again — holding
// back for ever would be its own bug.
func TestDFCHoldsOffUserDataAndReleasesIt(t *testing.T) {
	portA, portB := pipeLine()
	sec := &dfcSecondary{port: portB, full: true, stop: make(chan struct{})}
	go sec.run()

	cli, stopCli := startClient(t, &cliHandler{}, testConfig(ModeUnbalanced), portA)
	defer func() { close(sec.stop); stopCli() }()

	// The link initialises against a station whose buffer is full.
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		queueOne(t, cli, 1)
		time.Sleep(150 * time.Millisecond)
		if sec.count() > 0 {
			break
		}
	}
	if n := sec.count(); n != 0 {
		t.Fatalf("%d user-data frame(s) went to a station reporting DFC=1", n)
	}

	// The buffer drains; the next poll carries DFC=0 and the queue must move.
	sec.setFull(false)
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && sec.count() == 0 {
		time.Sleep(100 * time.Millisecond)
	}
	if sec.count() == 0 {
		t.Error("user data never resumed after DFC cleared: the queue is stuck")
	}
}

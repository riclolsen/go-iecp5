package cs101

import (
	"bytes"
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
)

// duplex is one end of an in-memory serial line.
type duplex struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (d *duplex) Read(p []byte) (int, error)  { return d.r.Read(p) }
func (d *duplex) Write(p []byte) (int, error) { return d.w.Write(p) }
func (d *duplex) Close() error {
	_ = d.r.Close()
	_ = d.w.Close()
	return nil
}

// pipeLine returns two connected duplex "serial ports".
func pipeLine() (*duplex, *duplex) {
	ar, bw := io.Pipe()
	br, aw := io.Pipe()
	return &duplex{r: ar, w: aw}, &duplex{r: br, w: bw}
}

func testConfig(mode TransmissionMode) Config {
	cfg := DefaultConfig()
	cfg.Serial = SerialConfig{Address: "test", BaudRate: 9600, DataBits: 8}
	cfg.Mode = mode
	cfg.LinkAddress = 1
	cfg.LinkAddrSize = 1
	cfg.TimeoutResponseT1 = 2 * time.Second
	cfg.TimeoutRepeatT2 = 1 * time.Second
	cfg.TimeoutSendLinkMsg = 5 * time.Millisecond
	return cfg
}

// --- frame parser robustness ---

func TestParseFrameMalformedLength(t *testing.T) {
	ctx := context.Background()
	// Variable frame with L == linkAddrSize (1): used to panic with a slice
	// bounds error; must return an error instead.
	raw := []byte{StartVariable, 0x01, 0x01, StartVariable, 0x53, 0x01, 0x54, EndChar}
	_, err := ParseFrame(bytes.NewReader(raw), 1, &ctx)
	if err == nil {
		t.Fatal("expected error for malformed length, got nil")
	}

	// L == 1+linkAddrSize (control+address, empty ASDU) must not panic either.
	control, addr := byte(0x53), byte(0x01)
	cs := control + addr
	raw = []byte{StartVariable, 0x02, 0x02, StartVariable, control, addr, cs, EndChar}
	f, err := ParseFrame(bytes.NewReader(raw), 1, &ctx)
	if err != nil {
		t.Fatalf("unexpected error for empty-ASDU frame: %v", err)
	}
	if len(f.ASDU) != 0 {
		t.Fatalf("expected empty ASDU, got %d bytes", len(f.ASDU))
	}
}

// --- handler stubs ---

type cliHandler struct {
	inro chan *asdu.ASDU
	any  chan *asdu.ASDU
}

func (h *cliHandler) InterrogationHandler(a *asdu.ASDU) error {
	select {
	case h.inro <- a:
	default:
	}
	return nil
}
func (h *cliHandler) CounterInterrogationHandler(*asdu.ASDU) error { return nil }
func (h *cliHandler) ReadHandler(*asdu.ASDU) error                 { return nil }
func (h *cliHandler) TestCommandHandler(*asdu.ASDU) error          { return nil }
func (h *cliHandler) ClockSyncHandler(*asdu.ASDU) error            { return nil }
func (h *cliHandler) ResetProcessHandler(*asdu.ASDU) error         { return nil }
func (h *cliHandler) DelayAcquisitionHandler(*asdu.ASDU) error     { return nil }
func (h *cliHandler) ASDUHandler(a *asdu.ASDU, _ int) error {
	select {
	case h.any <- a:
	default:
	}
	return nil
}
func (h *cliHandler) ASDUHandlerAll(*asdu.ASDU, int) error { return nil }

type srvHandler struct {
	srv    *Server
	inroN  int32 // number of interrogation commands seen
	gotCmd chan asdu.QualifierOfInterrogation
	any    chan *asdu.ASDU
}

func (h *srvHandler) InterrogationHandler(a *asdu.ASDU, qoi asdu.QualifierOfInterrogation) error {
	atomic.AddInt32(&h.inroN, 1)
	select {
	case h.gotCmd <- qoi:
	default:
	}
	// Buffer an interrogation response for the primary to collect.
	return asdu.Single(h.srv, false,
		asdu.CauseOfTransmission{Cause: asdu.InterrogatedByStation}, a.CommonAddr,
		asdu.SinglePointInfo{Ioa: 100, Value: true})
}
func (h *srvHandler) CounterInterrogationHandler(*asdu.ASDU, asdu.QualifierCountCall) error {
	return nil
}
func (h *srvHandler) ReadHandler(*asdu.ASDU, asdu.InfoObjAddr) error                     { return nil }
func (h *srvHandler) ClockSyncHandler(*asdu.ASDU, time.Time) error                       { return nil }
func (h *srvHandler) ResetProcessHandler(*asdu.ASDU, asdu.QualifierOfResetProcessCmd) error { return nil }
func (h *srvHandler) DelayAcquisitionHandler(*asdu.ASDU, uint16) error                   { return nil }
func (h *srvHandler) ASDUHandler(a *asdu.ASDU) error {
	select {
	case h.any <- a:
	default:
	}
	return nil
}
func (h *srvHandler) ASDUHandlerAll(*asdu.ASDU, int) error { return nil }

// startServer wires a Server to an in-memory port and starts its loops.
func startServer(t *testing.T, h ServerHandlerInterface, cfg Config, port io.ReadWriteCloser) (*Server, context.CancelFunc) {
	t.Helper()
	srv := NewServer(h)
	srv.SetLogMode(false)
	srv.config = cfg
	srv.port = port
	srv.setConnectStatus(statusConnected)
	srv.fcbExpected = true
	srv.primNextInit = time.Now()
	srv.connCtx, srv.connCancel = context.WithCancel(context.Background())
	srv.wg.Add(3)
	go srv.frameRecvLoop()
	go srv.frameSendLoop()
	go srv.handlerLoop()
	return srv, func() {
		srv.connCancel()
		_ = port.Close()
		srv.wg.Wait()
	}
}

// startClient wires a Client to an in-memory port and starts runProtocol.
func startClient(t *testing.T, h ClientHandlerInterface, cfg Config, port io.ReadWriteCloser) (*Client, context.CancelFunc) {
	t.Helper()
	opt := NewOption()
	if err := opt.SetConfig(cfg); err != nil {
		t.Fatalf("invalid test config: %v", err)
	}
	cli := NewClient(h, opt)
	if cli == nil {
		t.Fatal("NewClient returned nil")
	}
	cli.SetLogMode(false)
	cli.port = port
	cli.setConnectStatus(statusConnected)
	cli.ctx, cli.cancel = context.WithCancel(context.Background())
	cli.connCtx, cli.connCancel = context.WithCancel(cli.ctx)
	done := make(chan struct{})
	go func() {
		_ = cli.runProtocol()
		close(done)
	}()
	return cli, func() {
		cli.connCancel()
		_ = port.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("client runProtocol did not stop")
		}
	}
}

// TestUnbalancedExchange verifies the full unbalanced cycle against this
// library's own secondary: link init (status/reset), FCB synchronization for
// the first command after reset (the historic silent-drop bug), class 1/2
// buffering with ACD, and delivery of the poll responses back to the primary.
func TestUnbalancedExchange(t *testing.T) {
	portA, portB := pipeLine()
	cfg := testConfig(ModeUnbalanced)

	sh := &srvHandler{gotCmd: make(chan asdu.QualifierOfInterrogation, 4), any: make(chan *asdu.ASDU, 4)}
	srv, stopSrv := startServer(t, sh, cfg, portB)
	sh.srv = srv
	defer stopSrv()

	ch := &cliHandler{inro: make(chan *asdu.ASDU, 4), any: make(chan *asdu.ASDU, 4)}
	cli, stopCli := startClient(t, ch, cfg, portA)
	defer stopCli()

	// Wait for the link to come up.
	deadline := time.After(5 * time.Second)
	for !cli.IsLinkActive() {
		select {
		case <-deadline:
			t.Fatal("link never became active")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// First command after link reset: must arrive at the secondary intact.
	err := cli.InterrogationCmd(asdu.CauseOfTransmission{Cause: asdu.Activation}, 1, asdu.QOIStation)
	if err != nil {
		t.Fatalf("InterrogationCmd: %v", err)
	}
	select {
	case qoi := <-sh.gotCmd:
		if qoi != asdu.QOIStation {
			t.Fatalf("server got QOI %d, want %d", qoi, asdu.QOIStation)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("secondary never received the interrogation command (FCB desync?)")
	}

	// The secondary buffered a class 1 response; polling must deliver it.
	select {
	case a := <-ch.inro:
		if a.Identifier.Type != asdu.M_SP_NA_1 {
			t.Fatalf("got response type %v, want M_SP_NA_1", a.Identifier.Type)
		}
		pts := a.GetSinglePoint()
		if len(pts) != 1 || pts[0].Ioa != 100 || !pts[0].Value {
			t.Fatalf("unexpected single point payload: %+v", pts)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("primary never received the buffered interrogation response")
	}

	// A second command exercises continued FCB alternation.
	err = cli.InterrogationCmd(asdu.CauseOfTransmission{Cause: asdu.Activation}, 1, asdu.QOIGroup1)
	if err != nil {
		t.Fatalf("second InterrogationCmd: %v", err)
	}
	select {
	case qoi := <-sh.gotCmd:
		if qoi != asdu.QOIGroup1 {
			t.Fatalf("server got QOI %d, want %d", qoi, asdu.QOIGroup1)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("secondary never received the second interrogation command")
	}
	if n := atomic.LoadInt32(&sh.inroN); n != 2 {
		t.Fatalf("secondary processed %d interrogations, want 2 (duplicate suppression broken?)", n)
	}
}

// TestBalancedExchange verifies balanced mode: both stations initialize their
// transmit direction and exchange spontaneous confirmed user data.
func TestBalancedExchange(t *testing.T) {
	portA, portB := pipeLine()
	cfg := testConfig(ModeBalanced)

	sh := &srvHandler{gotCmd: make(chan asdu.QualifierOfInterrogation, 4), any: make(chan *asdu.ASDU, 4)}
	srv, stopSrv := startServer(t, sh, cfg, portB)
	sh.srv = srv
	defer stopSrv()

	ch := &cliHandler{inro: make(chan *asdu.ASDU, 4), any: make(chan *asdu.ASDU, 4)}
	cli, stopCli := startClient(t, ch, cfg, portA)
	defer stopCli()

	// Wait for the client's transmit direction to come up.
	deadline := time.After(5 * time.Second)
	for !cli.IsLinkActive() {
		select {
		case <-deadline:
			t.Fatal("client link never became active")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Client -> server spontaneous command.
	err := cli.InterrogationCmd(asdu.CauseOfTransmission{Cause: asdu.Activation}, 1, asdu.QOIStation)
	if err != nil {
		t.Fatalf("InterrogationCmd: %v", err)
	}
	select {
	case <-sh.gotCmd:
	case <-time.After(5 * time.Second):
		t.Fatal("server never received the client's command")
	}

	// Server -> client spontaneous data (its primary role must reset its
	// own transmit direction and then push the frame).
	err = asdu.Single(srv, false,
		asdu.CauseOfTransmission{Cause: asdu.Spontaneous}, 1,
		asdu.SinglePointInfo{Ioa: 200, Value: true})
	if err != nil {
		t.Fatalf("server Send: %v", err)
	}
	select {
	case a := <-ch.any:
		if a.Identifier.Type != asdu.M_SP_NA_1 {
			t.Fatalf("client got type %v, want M_SP_NA_1", a.Identifier.Type)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client never received the server's spontaneous data")
	}
}

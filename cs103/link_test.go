package cs103

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/cs101"
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

func pipeLine() (*duplex, *duplex) {
	ar, bw := io.Pipe()
	br, aw := io.Pipe()
	return &duplex{r: ar, w: aw}, &duplex{r: br, w: bw}
}

// fakeDevice is a minimal IEC 60870-5-103 protection device (secondary
// station) operating at the FT1.2 frame level.
type fakeDevice struct {
	t    *testing.T
	port io.ReadWriteCloser
	addr byte

	fcbExpected bool
	lastResp    *cs101.Frame

	mu     sync.Mutex
	class1 []*ASDU // events

	gotCommand chan GeneralCommandInfo

	ctx    context.Context
	cancel context.CancelFunc
}

func newFakeDevice(t *testing.T, port io.ReadWriteCloser, addr byte) *fakeDevice {
	ctx, cancel := context.WithCancel(context.Background())
	return &fakeDevice{
		t: t, port: port, addr: addr,
		fcbExpected: true,
		gotCommand:  make(chan GeneralCommandInfo, 4),
		ctx:         ctx, cancel: cancel,
	}
}

func (d *fakeDevice) stop() {
	d.cancel()
	_ = d.port.Close()
}

func (d *fakeDevice) queueClass1(a *ASDU) {
	d.mu.Lock()
	d.class1 = append(d.class1, a)
	d.mu.Unlock()
}

func (d *fakeDevice) popClass1() (*ASDU, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.class1) == 0 {
		return nil, false
	}
	a := d.class1[0]
	d.class1 = d.class1[1:]
	return a, len(d.class1) > 0
}

func (d *fakeDevice) class1Pending() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.class1) > 0
}

func (d *fakeDevice) respond(f *cs101.Frame) {
	d.lastResp = f
	raw, err := f.MarshalBinary(1)
	if err != nil {
		d.t.Errorf("fake device marshal: %v", err)
		return
	}
	if _, err := d.port.Write(raw); err != nil && d.ctx.Err() == nil {
		d.t.Logf("fake device write: %v", err)
	}
}

func (d *fakeDevice) respondFixed(fun byte, acd bool) {
	control := fun & cs101.CtrlFuncMask
	if acd {
		control |= cs101.CtrlACD
	}
	d.respond(cs101.NewFixedFrame(control, []byte{d.addr}))
}

func (d *fakeDevice) respondData(a *ASDU, acd bool) {
	raw, err := a.MarshalBinary()
	if err != nil {
		d.t.Errorf("fake device ASDU marshal: %v", err)
		return
	}
	control := cs101.SecFcUserDataConf & cs101.CtrlFuncMask
	if acd {
		control |= cs101.CtrlACD
	}
	d.respond(cs101.NewDataFrame(control, []byte{d.addr}, raw))
}

func (d *fakeDevice) identASDU(cause Cause) *ASDU {
	a := NewASDU(TypIdentification, asdu.VariableStruct{Number: 1, IsSequence: true}, cause, d.addr)
	a.AppendBytes(FunGlobal, InfStartRestart, 2) // COL 2
	a.AppendBytes([]byte("FAKEDEV1")...)
	return a
}

func (d *fakeDevice) run() {
	for {
		frame, err := cs101.ParseFrame(d.port, 1, &d.ctx)
		if err != nil {
			if d.ctx.Err() != nil {
				return
			}
			continue
		}
		if frame.Start == cs101.SingleCharAck {
			continue
		}
		if byte(frame.GetLinkAddress()) != d.addr {
			continue
		}
		ctrl := frame.GetControlField()
		if !ctrl.PRM {
			continue
		}

		if ctrl.FCV {
			if ctrl.FCB != d.fcbExpected {
				// repeated frame: retransmit the previous response
				if d.lastResp != nil {
					d.respond(d.lastResp)
				} else {
					d.respondFixed(cs101.SecFcConfACK, d.class1Pending())
				}
				continue
			}
			d.fcbExpected = !d.fcbExpected
		}

		switch ctrl.Fun {
		case cs101.PrimFcReqStatus: // request status of link
			d.respondFixed(cs101.SecFcRespStatus, d.class1Pending())

		case cs101.PrimFcResetLink, PrimFcResetFCB: // reset CU / reset FCB
			d.fcbExpected = true
			d.queueClass1(d.identASDU(CauseResetCU))
			d.respondFixed(cs101.SecFcConfACK, true)

		case cs101.PrimFcUserDataConf: // command from the master
			a := new(ASDU)
			if err := a.UnmarshalBinary(frame.ASDU); err != nil {
				d.t.Errorf("fake device ASDU decode: %v", err)
				d.respondFixed(cs101.SecFcConfACK, d.class1Pending())
				continue
			}
			switch a.Type {
			case TypTimeSync:
				// confirm with a monitor-direction ASDU 6
				conf := NewASDU(TypTimeSync, asdu.VariableStruct{Number: 1, IsSequence: true}, CauseTimeSync, d.addr)
				conf.AppendBytes(FunGlobal, 0)
				conf.AppendBytes(asdu.CP56Time2a(time.Now(), time.UTC)...)
				d.queueClass1(conf)
			case TypGeneralInterrogation:
				scn, _ := a.GetGeneralInterrogation()
				// one GI reply followed by the termination
				ev := NewASDU(TypTimeTagged, asdu.VariableStruct{Number: 1, IsSequence: true}, CauseGI, d.addr)
				ev.AppendBytes(FunOvercurrentProtection, InfProtectionActive, byte(DPIOn))
				ev.AppendBytes(CP32Time2a(time.Now(), time.UTC)...)
				ev.AppendBytes(0) // SIN
				d.queueClass1(ev)
				term := NewASDU(TypGITermination, asdu.VariableStruct{Number: 1, IsSequence: true}, CauseGITermination, d.addr)
				term.AppendBytes(FunGlobal, 0, scn)
				d.queueClass1(term)
			case TypGeneralCommand:
				cmd, cerr := a.GetGeneralCommand()
				if cerr != nil {
					d.t.Errorf("fake device command decode: %v", cerr)
					break
				}
				d.gotCommand <- cmd
				// positive acknowledgement: ASDU 1, cause 20, RII in SIN
				ack := NewASDU(TypTimeTagged, asdu.VariableStruct{Number: 1, IsSequence: true}, CauseCommandAckPos, d.addr)
				dpi := DPIOff
				if cmd.Dco == DCOOn {
					dpi = DPIOn
				}
				ack.AppendBytes(cmd.Fun, cmd.Inf, byte(dpi))
				ack.AppendBytes(CP32Time2a(time.Now(), time.UTC)...)
				ack.AppendBytes(cmd.Rii)
				d.queueClass1(ack)
			}
			d.respondFixed(cs101.SecFcConfACK, d.class1Pending())

		case cs101.PrimFcReqData1: // class 1 poll: events
			if a, more := d.popClass1(); a != nil {
				d.respondData(a, more)
			} else {
				d.respondFixed(cs101.SecFcUserDataNoRep, false)
			}

		case cs101.PrimFcReqData2: // class 2 poll: cyclic measurands
			m := NewASDU(TypMeasurandsI, asdu.VariableStruct{Number: 2, IsSequence: true}, CauseCyclic, d.addr)
			m.AppendBytes(FunOvercurrentProtection, InfMeasurandIV)
			v1 := Measurand{Val: 2048}.Value()  // 0.5 of full scale
			v2 := Measurand{Val: -1024}.Value() // -0.25 of full scale
			m.AppendBytes(byte(v1), byte(v1>>8), byte(v2), byte(v2>>8))
			d.respondData(m, d.class1Pending())

		default:
			d.respondFixed(cs101.SecFcRespLinkNI, false)
		}
	}
}

// --- client handler recording what arrives ---

type recHandler struct {
	ident   chan IdentificationInfo
	events  chan TimeTaggedInfo
	meas    chan MeasurandsInfo
	giTerm  chan byte
	cmdAcks chan TimeTaggedInfo
}

func newRecHandler() *recHandler {
	return &recHandler{
		ident:   make(chan IdentificationInfo, 4),
		events:  make(chan TimeTaggedInfo, 16),
		meas:    make(chan MeasurandsInfo, 16),
		giTerm:  make(chan byte, 4),
		cmdAcks: make(chan TimeTaggedInfo, 4),
	}
}

func (h *recHandler) TimeTaggedHandler(a *ASDU, info TimeTaggedInfo) error {
	if a.Coa == CauseCommandAckPos || a.Coa == CauseCommandAckNeg {
		h.cmdAcks <- info
	} else {
		h.events <- info
	}
	return nil
}
func (h *recHandler) MeasurandsHandler(_ *ASDU, info MeasurandsInfo) error {
	select {
	case h.meas <- info:
	default:
	}
	return nil
}
func (h *recHandler) IdentificationHandler(_ *ASDU, info IdentificationInfo) error {
	h.ident <- info
	return nil
}
func (h *recHandler) GITerminationHandler(_ *ASDU, scn byte) error {
	h.giTerm <- scn
	return nil
}
func (h *recHandler) ASDUHandler(*ASDU) error    { return nil }
func (h *recHandler) ASDUHandlerAll(*ASDU) error { return nil }

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

func testConfig() Config {
	cfg := DefaultConfig()
	cfg.Serial = SerialConfig{Address: "test", BaudRate: 9600, DataBits: 8}
	cfg.LinkAddress = 3
	cfg.TimeoutResponseT1 = 2 * time.Second
	cfg.TimeoutRepeatT2 = 1 * time.Second
	cfg.TimeoutSendLinkMsg = 5 * time.Millisecond
	return cfg
}

// TestMasterExchange runs the complete 103 master cycle against a fake
// protection device: link init (status/reset CU), collection of the
// identification message, automatic time sync + general interrogation,
// cyclic measurand polling, and a general command with its positive
// acknowledgement.
func TestMasterExchange(t *testing.T) {
	portA, portB := pipeLine()
	dev := newFakeDevice(t, portB, 3)
	go dev.run()
	defer dev.stop()

	h := newRecHandler()
	cli, stopCli := startClient(t, h, testConfig(), portA)
	defer stopCli()

	// Link must come up.
	deadline := time.After(5 * time.Second)
	for !cli.IsLinkActive() {
		select {
		case <-deadline:
			t.Fatal("link never became active")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// The identification message queued on reset must arrive via class 1.
	select {
	case id := <-h.ident:
		if id.ASCII != "FAKEDEV1" || id.Col != 2 {
			t.Fatalf("identification = %+v, want FAKEDEV1 / COL 2", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("identification message never arrived")
	}

	// Auto-init GI: one event with cause 9, then the termination.
	select {
	case ev := <-h.events:
		if ev.Fun != FunOvercurrentProtection || ev.Inf != InfProtectionActive || ev.Dpi != DPIOn {
			t.Fatalf("GI event = %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GI event never arrived")
	}
	select {
	case scn := <-h.giTerm:
		if scn != 0 {
			t.Fatalf("GI termination SCN = %d, want 0", scn)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GI termination never arrived")
	}

	// Cyclic measurands must flow from class 2 polling.
	select {
	case m := <-h.meas:
		if len(m.Values) != 2 || m.Values[0].Val != 2048 || m.Values[1].Val != -1024 {
			t.Fatalf("measurands = %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("measurands never arrived")
	}

	// General command round trip.
	if err := cli.GeneralCommand(3, FunOvercurrentProtection, InfAutoRecloserActive, DCOOn, 42); err != nil {
		t.Fatalf("GeneralCommand: %v", err)
	}
	select {
	case cmd := <-dev.gotCommand:
		if cmd.Fun != FunOvercurrentProtection || cmd.Inf != InfAutoRecloserActive || cmd.Dco != DCOOn || cmd.Rii != 42 {
			t.Fatalf("device got command %+v", cmd)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("device never received the general command")
	}
	select {
	case ack := <-h.cmdAcks:
		if ack.Sin != 42 || ack.Dpi != DPIOn {
			t.Fatalf("command ack = %+v, want RII 42 / DPI On", ack)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command acknowledgement never arrived")
	}
}

// TestCP32Time2aRoundTrip checks the 4-octet time codec.
func TestCP32Time2aRoundTrip(t *testing.T) {
	now := time.Now().In(time.UTC).Truncate(time.Millisecond)
	b := CP32Time2a(now, time.UTC)
	got := ParseCP32Time2a(b, time.UTC)
	if got.Hour() != now.Hour() || got.Minute() != now.Minute() ||
		got.Second() != now.Second() {
		t.Fatalf("CP32 roundtrip: got %v, want %v", got, now)
	}
	// invalid flag decodes to zero time
	b[2] |= 0x80
	if !ParseCP32Time2a(b, time.UTC).IsZero() {
		t.Fatal("IV-flagged CP32 time must decode to zero time")
	}
}

// TestMeasurandCodec checks the 13-bit measurand encoding.
func TestMeasurandCodec(t *testing.T) {
	for _, v := range []int16{0, 1, -1, 2048, -2048, 4095, -4096} {
		m := Measurand{Val: v, Overflow: v == 4095, Invalid: v == -4096}
		got := ParseMeasurand(m.Value())
		if got != m {
			t.Fatalf("measurand roundtrip: got %+v, want %+v", got, m)
		}
	}
}

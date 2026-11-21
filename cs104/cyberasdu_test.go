package cs104

import (
	"encoding/binary"
	"math"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/secevent"
)

// secHandler records what reached it and can refuse commands.
type secHandler struct {
	nullHandler
	mu     sync.Mutex
	called []asdu.TypeID
	reject bool
}

func (h *secHandler) note(t asdu.TypeID) { h.mu.Lock(); h.called = append(h.called, t); h.mu.Unlock() }
func (h *secHandler) count() int         { h.mu.Lock(); defer h.mu.Unlock(); return len(h.called) }

func (h *secHandler) ASDUHandler(c asdu.Connect, a *asdu.ASDU) error {
	h.note(a.Type)
	if h.reject {
		a.Coa.IsNegative = true
		return a.SendReplyMirror(c, asdu.ActivationCon)
	}
	return nil
}
func (h *secHandler) ReadHandler(c asdu.Connect, a *asdu.ASDU, ioa asdu.InfoObjAddr) error {
	h.note(a.Type)
	return a.SendReplyMirror(c, asdu.UnknownIOA) // no such point
}

// rawASDU hand-assembles an ASDU with the 104 wire parameters.
func rawASDU(typ asdu.TypeID, num byte, cause asdu.Cause, ca uint16, body ...byte) []byte {
	return append([]byte{byte(typ), num, byte(cause), 0, byte(ca), byte(ca >> 8)}, body...)
}

func ioa3(n uint32) []byte { return []byte{byte(n), byte(n >> 8), byte(n >> 16)} }

func obj(n uint32, rest ...byte) []byte { return append(ioa3(n), rest...) }

func cp56now(d time.Duration) []byte {
	t := time.Now().UTC().Add(d)
	b := make([]byte, 7)
	binary.LittleEndian.PutUint16(b, uint16(t.Second()*1000+t.Nanosecond()/1e6))
	b[2], b[3], b[4], b[5], b[6] = byte(t.Minute()), byte(t.Hour()), byte(t.Day()), byte(t.Month()), byte(t.Year()-2000)
	return b
}

type wire struct {
	t    *testing.T
	conn net.Conn
	sn   uint16
}

func openWire(t *testing.T, addr string) *wire {
	return &wire{t: t, conn: started(t, addr)}
}

func (w *wire) send(data []byte) {
	f, err := newIFrame(w.sn, 0, data)
	if err != nil {
		w.t.Fatal(err)
	}
	w.sn++
	if _, err := w.conn.Write(f); err != nil {
		w.t.Fatal(err)
	}
}

// read returns the next ASDU the server sent, or nil after the timeout.
func (w *wire) read(d time.Duration) *asdu.ASDU {
	_ = w.conn.SetReadDeadline(time.Now().Add(d))
	for {
		hdr := make([]byte, 2)
		if _, err := ioReadFull(w.conn, hdr); err != nil {
			return nil
		}
		body := make([]byte, hdr[1])
		if _, err := ioReadFull(w.conn, body); err != nil {
			return nil
		}
		if body[0]&1 == 0 { // I-frame
			a := asdu.NewEmptyASDU(asdu.ParamsWide)
			if a.UnmarshalBinary(body[4:]) != nil {
				return nil
			}
			return a
		}
	}
}

func ioReadFull(c net.Conn, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := c.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func policyWithHandler(t *testing.T, h ServerHandlerInterface, p secevent.Policy) (*evCollector, string) {
	t.Helper()
	addr := freeAddr(t)
	c := &evCollector{}
	p.Sink = c
	srv := NewServer(h)
	srv.LogMode(false)
	srv.SetSecurityPolicy(p)
	go func() { _ = srv.ListenAndServer(addr) }()
	t.Cleanup(func() { _ = srv.Close(); srv.SetCyberEventSink(nil) })
	return c, addr
}

func TestInvalidRequestsAreReported(t *testing.T) {
	h := &secHandler{}
	c, addr := policyWithHandler(t, h, secevent.Policy{})
	w := openWire(t, addr)
	defer w.conn.Close()

	// A command claiming two objects.
	w.send(rawASDU(asdu.C_SC_NA_1, 2, asdu.Activation, 1, append(obj(1, 0x01), obj(2, 0x01)...)...))
	if e := c.wait(t, secevent.KindAccessOversized); e.TypeID != asdu.C_SC_NA_1 || e.Err == nil || len(e.Raw) == 0 {
		t.Errorf("oversized: %+v", e)
	}
	// A process command to the global address.
	w.send(rawASDU(asdu.C_SC_NA_1, 1, asdu.Activation, 0xFFFF, obj(1, 0x01)...))
	c.wait(t, secevent.KindAccessBroadcastMisuse)
	// A double command in the "not permitted" state, and a NaN set-point.
	w.send(rawASDU(asdu.C_DC_NA_1, 1, asdu.Activation, 1, obj(1, 0x00)...))
	w.send(rawASDU(asdu.C_SE_NC_1, 1, asdu.Activation, 1, obj(1, append(f32le(float32(math.NaN())), 0)...)...))
	for i := 0; i < 50 && func() bool {
		n := 0
		for _, k := range c.kinds() {
			if k == secevent.KindProtoFieldInvalid {
				n++
			}
		}
		return n < 1
	}(); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	c.wait(t, secevent.KindProtoFieldInvalid)
	// Types a 104 peer does not send, in the wrong direction.
	w.send(rawASDU(asdu.C_TS_NA_1, 1, asdu.Activation, 1, obj(0, 0xAA, 0x55)...))
	c.wait(t, secevent.KindProtoNotInProfile)
	w.send(rawASDU(asdu.M_SP_NA_1, 1, asdu.Spontaneous, 1, obj(1, 0x01)...))
	c.wait(t, secevent.KindProtoWrongDirection)
	w.send(rawASDU(asdu.C_SC_NA_1, 1, asdu.ActivationCon, 1, obj(1, 0x01)...))
	// A security ASDU the library does not speak: S_CH_NA_1 needs a body, the decoder may refuse it.
	// A file pushed at the station.
	w.send(rawASDU(asdu.F_FR_NA_1, 1, asdu.FileTransfer, 1, obj(0, 1, 0, 10, 0, 0, 0)...))
	c.wait(t, secevent.KindFileDownloadAttempt)
}

func f32le(f float32) []byte {
	b := math.Float32bits(f)
	return []byte{byte(b), byte(b >> 8), byte(b >> 16), byte(b >> 24)}
}

func TestAuditTrailOfControlFunctions(t *testing.T) {
	h := &secHandler{}
	c, addr := policyWithHandler(t, h, secevent.Policy{})
	w := openWire(t, addr)
	defer w.conn.Close()

	w.send(rawASDU(asdu.C_SC_NA_1, 1, asdu.Activation, 1, obj(1000, 0x01|0x80)...)) // select, ON
	e := c.wait(t, secevent.KindCmdReceived)
	if e.IOA != 1000 || e.Value == "" || e.TypeID != asdu.C_SC_NA_1 || e.Severity != secevent.SevNotice {
		t.Errorf("cmd.received: %+v", e)
	}
	w.send(rawASDU(asdu.C_RP_NA_1, 1, asdu.Activation, 1, obj(0, 1)...))
	if e := c.wait(t, secevent.KindCtrlResetProcess); e.Severity != secevent.SevAlert || e.Value != "QRP=1" {
		t.Errorf("reset: %+v", e)
	}
	w.send(rawASDU(asdu.C_IC_NA_1, 1, asdu.Activation, 1, obj(0, 20)...))
	c.wait(t, secevent.KindCtrlInterrogation)
	// Clock an hour off.
	w.send(rawASDU(asdu.C_CS_NA_1, 1, asdu.Activation, 1, obj(0, cp56now(-time.Hour)...)...))
	c.wait(t, secevent.KindCtrlClockSync)
	if e := c.wait(t, secevent.KindCtrlClockStep); e.Severity != secevent.SevAlert {
		t.Errorf("clock step: %+v", e)
	}
	w.send(rawASDU(asdu.P_ME_NC_1, 1, asdu.Activation, 1, obj(2000, append(f32le(5), 0)...)...))
	c.wait(t, secevent.KindCtrlParamChange)
}

func TestAuditCanBeSilenced(t *testing.T) {
	c, addr := policyWithHandler(t, &secHandler{}, secevent.Policy{DisableAudit: true})
	w := openWire(t, addr)
	defer w.conn.Close()
	w.send(rawASDU(asdu.C_RP_NA_1, 1, asdu.Activation, 1, obj(0, 1)...))
	time.Sleep(300 * time.Millisecond)
	if _, ok := c.find(secevent.KindCtrlResetProcess); ok {
		t.Error("audit event raised though DisableAudit is set")
	}
}

func TestRejectedRepliesAreObserved(t *testing.T) {
	h := &secHandler{reject: true}
	c, addr := policyWithHandler(t, h, secevent.Policy{})
	w := openWire(t, addr)
	defer w.conn.Close()

	w.send(rawASDU(asdu.C_RD_NA_1, 1, asdu.Request, 1, ioa3(4242)...)) // handler says: no such point
	e := c.wait(t, secevent.KindAccessUnknownIOA)
	if e.IOA != 4242 || e.Severity != secevent.SevWarning || e.Action != secevent.ActRejected {
		t.Errorf("unknown ioa: %+v", e)
	}
	w.send(rawASDU(asdu.C_SC_NA_1, 1, asdu.Activation, 1, obj(7, 0x01)...)) // handler refuses
	if e := c.wait(t, secevent.KindCmdRejected); e.IOA != 7 || e.Value == "" {
		t.Errorf("cmd.rejected: %+v", e)
	}
	// The library's own refusal: a system command with an unexpected cause.
	w.send(rawASDU(asdu.C_IC_NA_1, 1, asdu.Spontaneous, 1, obj(0, 20)...))
	c.wait(t, secevent.KindProtoUnknownCOT)
	// A command to a CA 0.
	w.send(rawASDU(asdu.C_IC_NA_1, 1, asdu.Activation, 0, obj(0, 20)...))
}

func TestScanIsRecognised(t *testing.T) {
	h := &secHandler{}
	c, addr := policyWithHandler(t, h, secevent.Policy{ScanThreshold: 10, ScanWindow: time.Minute})
	w := openWire(t, addr)
	defer w.conn.Close()
	for i := 0; i < 15; i++ {
		w.send(rawASDU(asdu.C_RD_NA_1, 1, asdu.Request, 1, ioa3(uint32(100+i))...))
	}
	e := c.wait(t, secevent.KindAccessScan)
	if e.Severity != secevent.SevAlert {
		t.Errorf("%+v", e)
	}
}

type space struct{}

func (space) KnownCA(ca asdu.CommonAddr) bool { return ca == 1 }
func (space) KnownIOA(_ asdu.CommonAddr, _ asdu.TypeID, ioa asdu.InfoObjAddr) bool {
	return ioa >= 100 && ioa < 200
}

func TestAddressSpaceAndAutoReject(t *testing.T) {
	h := &secHandler{}
	c, addr := policyWithHandler(t, h, secevent.Policy{AddressSpace: space{}, AutoRejectUnknown: true})
	w := openWire(t, addr)
	defer w.conn.Close()

	w.send(rawASDU(asdu.C_SC_NA_1, 1, asdu.Activation, 9, obj(150, 0x01)...)) // no such station
	if e := c.wait(t, secevent.KindAccessUnknownCA); e.Action != secevent.ActRejected || e.CommonAddr != 9 {
		t.Errorf("%+v", e)
	}
	if r := w.read(2 * time.Second); r == nil || r.Coa.Cause != asdu.UnknownCA {
		t.Errorf("reply %+v, want UnknownCA", r)
	}
	w.send(rawASDU(asdu.C_SC_NA_1, 1, asdu.Activation, 1, obj(999, 0x01)...)) // no such point
	c.wait(t, secevent.KindAccessUnknownIOA)
	if r := w.read(2 * time.Second); r == nil || r.Coa.Cause != asdu.UnknownIOA {
		t.Errorf("reply %+v, want UnknownIOA", r)
	}
	w.send(rawASDU(asdu.C_SC_NA_1, 1, asdu.Activation, 1, obj(150, 0x01)...)) // fine
	for i := 0; h.count() == 0 && i < 100; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if h.count() != 1 {
		t.Errorf("handler called %d times, want only for the valid object", h.count())
	}
	// Each refusal was reported once, not twice (inbound check and its reply).
	n := 0
	for _, k := range c.kinds() {
		if k == secevent.KindAccessUnknownIOA {
			n++
		}
	}
	if n > 1 {
		t.Errorf("unknown_ioa reported %d times for one request", n)
	}
}

func TestFunctionPolicyReadOnly(t *testing.T) {
	h := &secHandler{}
	c, addr := policyWithHandler(t, h, secevent.Policy{Functions: secevent.FunctionPolicy{ReadOnly: true, DenyResetProcess: true}})
	w := openWire(t, addr)
	defer w.conn.Close()
	w.send(rawASDU(asdu.C_SC_NA_1, 1, asdu.Activation, 1, obj(1, 0x01)...))
	if e := c.wait(t, secevent.KindAccessTypeBlocked); e.Action != secevent.ActRejected {
		t.Errorf("%+v", e)
	}
	if r := w.read(2 * time.Second); r == nil || !r.Coa.IsNegative || r.Coa.Cause != asdu.ActivationCon {
		t.Errorf("reply %+v, want a negative confirmation", r)
	}
	w.send(rawASDU(asdu.C_RP_NA_1, 1, asdu.Activation, 1, obj(0, 1)...))
	time.Sleep(200 * time.Millisecond)
	if h.count() != 0 {
		t.Errorf("handler reached %d times through a denial", h.count())
	}
}

func TestStaleTimeTagRejected(t *testing.T) {
	h := &secHandler{}
	c, addr := policyWithHandler(t, h, secevent.Policy{MaxCommandAge: 10 * time.Second, RejectStaleCommands: true})
	w := openWire(t, addr)
	defer w.conn.Close()
	w.send(rawASDU(asdu.C_SC_TA_1, 1, asdu.Activation, 1, obj(1, append([]byte{0x01}, cp56now(-5*time.Minute)...)...)...))
	if e := c.wait(t, secevent.KindCmdStaleTimeTag); e.Severity != secevent.SevAlert || e.Action != secevent.ActRejected {
		t.Errorf("%+v", e)
	}
	if r := w.read(2 * time.Second); r == nil || !r.Coa.IsNegative {
		t.Errorf("reply %+v", r)
	}
	// A fresh one goes through.
	w.send(rawASDU(asdu.C_SC_TA_1, 1, asdu.Activation, 1, obj(1, append([]byte{0x01}, cp56now(0)...)...)...))
	for i := 0; h.count() == 0 && i < 100; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if h.count() != 1 {
		t.Errorf("fresh command: handler called %d times", h.count())
	}
}

func TestReplayedCommandOnAnotherConnection(t *testing.T) {
	c, addr := policyWithHandler(t, &secHandler{}, secevent.Policy{DuplicateWindow: time.Minute})
	cmd := rawASDU(asdu.C_SC_NA_1, 1, asdu.Activation, 1, obj(1, 0x01)...)
	first := openWire(t, addr)
	first.send(cmd)
	c.wait(t, secevent.KindCmdReceived)
	_ = first.conn.Close()
	second := openWire(t, addr) // fresh TCP connection, fresh sequence numbers
	defer second.conn.Close()
	second.send(cmd)
	if e := c.wait(t, secevent.KindCmdDuplicate); e.Severity != secevent.SevWarning {
		t.Errorf("%+v", e)
	}
}

func TestCommandFloodThrottled(t *testing.T) {
	h := &secHandler{}
	c, addr := policyWithHandler(t, h, secevent.Policy{
		Limits: secevent.Limits{Commands: secevent.Rate{N: 5, Per: time.Minute}}, FloodAction: secevent.ActThrottled})
	w := openWire(t, addr)
	defer w.conn.Close()
	for i := 0; i < 30; i++ {
		w.send(rawASDU(asdu.C_SC_NA_1, 1, asdu.Activation, 1, obj(uint32(i+1), 0x01)...))
	}
	if e := c.wait(t, secevent.KindFloodCommands); e.Action != secevent.ActThrottled {
		t.Errorf("%+v", e)
	}
	time.Sleep(500 * time.Millisecond)
	if n := h.count(); n != 5 {
		t.Errorf("handler saw %d of 30 commands, want the 5 within the rate", n)
	}
}

func TestKeepAliveStormAndFrameFlood(t *testing.T) {
	c, addr := policyWithHandler(t, &secHandler{}, secevent.Policy{
		Limits:      secevent.Limits{TestFR: secevent.Rate{N: 5, Per: time.Minute}, Frames: secevent.Rate{N: 100, Per: time.Minute}},
		FloodAction: secevent.ActDisconnected})
	conn := dialRetry(t, addr)
	defer conn.Close()
	for i := 0; i < 20; i++ {
		_, _ = conn.Write([]byte{0x68, 4, 0x43, 0, 0, 0}) // TESTFR act
	}
	e := c.wait(t, secevent.KindFloodTestFR)
	if e.Action != secevent.ActDisconnected {
		t.Errorf("%+v", e)
	}
	expectClosed(t, conn)
}

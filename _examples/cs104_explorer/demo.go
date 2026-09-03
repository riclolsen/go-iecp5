package main

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/cs104"
	"github.com/riclolsen/go-iecp5/filetransfer"
)

// Demo mode runs a full outstation inside this process, reached over a
// loopback socket: the real APCI state machine, the real ASDU codecs and the
// real file transfer, with no device. It is the fastest way to see what the
// tool does, and it is what the tests drive.

const demoCA = asdu.CommonAddr(1)

// Demo point addresses, grouped the way a small RTU usually is.
const (
	demoSingleBase = 100 // single points 100..103
	demoDoubleBase = 200 // double points 200..201
	demoFloatBase  = 400 // measured values, short float 400..403
	demoScaledBase = 500 // measured values, scaled 500..501
	demoStepBase   = 600 // step position 600
	demoCounter    = 700 // integrated total 700
	demoBitstring  = 800 // bitstring 800
	demoCmdSingle  = 6000
	demoCmdDouble  = 6100
	demoCmdSetpt   = 6200

	demoFileRecord = asdu.InfoObjAddr(100)
	demoFileEvents = asdu.InfoObjAddr(101)
)

type demoServer struct {
	addr string
	srv  *cs104.Server

	mu      sync.Mutex
	single  [4]bool
	double  [2]asdu.DoublePoint
	floats  [4]float32
	scaled  [2]int16
	step    int
	counter int32
	bits    uint32

	stop0 func()
}

// startDemoServer binds a loopback port and starts simulating.
func startDemoServer() (*demoServer, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	addr := l.Addr().String()
	_ = l.Close()

	d := &demoServer{addr: addr}
	d.double[0], d.double[1] = asdu.DPIDeterminedOn, asdu.DPIDeterminedOff
	d.floats = [4]float32{11019.9, 230.4, 49.98, 0.42}
	d.scaled = [2]int16{1250, -320}
	d.counter = 10422
	d.bits = 0x0000A5A5

	store := filetransfer.NewMemStore()
	record := []byte("COMTRADE-LIKE SAMPLE RECORD\n")
	for i := 0; i < 400; i++ {
		record = append(record, []byte(fmt.Sprintf("%04d,%8.3f,%8.3f\n",
			i, 10*float64(i%50), 230+float64(i%7)))...)
	}
	_ = store.Write(demoFileRecord, asdu.FileDisturbanceData, record)
	_ = store.Write(demoFileEvents, asdu.FileSequencesOfEvents,
		[]byte("2026-01-01T00:00:00Z trip L1\n2026-01-01T00:00:01Z reclose\n"))

	h := &demoHandler{d: d, sender: filetransfer.NewSender(store).SetSectionSize(4096)}
	d.srv = cs104.NewServer(h)
	d.srv.LogMode(false)

	go func() { _ = d.srv.ListenAndServer(addr) }()

	done := make(chan struct{})
	d.stop0 = func() { close(done) }
	go d.simulate(done)

	// Give the listener a moment to come up before the client dials it.
	time.Sleep(150 * time.Millisecond)
	return d, nil
}

func (d *demoServer) stop() {
	if d.stop0 != nil {
		d.stop0()
		d.stop0 = nil
	}
	if d.srv != nil {
		_ = d.srv.Close()
	}
}

// simulate moves the process on, and reports what changed the way a real
// device does: spontaneously, as it happens.
func (d *demoServer) simulate(done <-chan struct{}) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	phase := 0.0

	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		phase += 0.15

		d.mu.Lock()
		// Analogue values drift; a couple of digitals flip now and then.
		d.floats[0] = float32(11000 + 400*math.Sin(phase))
		d.floats[1] = float32(230 + 4*math.Sin(phase*1.7))
		d.floats[2] = float32(50 + 0.05*math.Sin(phase*0.6))
		d.scaled[0] = int16(1250 + 60*math.Sin(phase*1.1))
		d.counter += int32(rnd.Intn(7))
		flip := rnd.Intn(12) == 0
		if flip {
			i := rnd.Intn(len(d.single))
			d.single[i] = !d.single[i]
		}
		floats := d.floats
		scaled := d.scaled
		counter := d.counter
		single := d.single
		d.mu.Unlock()

		if d.srv.GetSessionsLen() == 0 {
			continue
		}

		spont := asdu.CauseOfTransmission{Cause: asdu.Spontaneous}
		periodic := asdu.CauseOfTransmission{Cause: asdu.Periodic}
		now := time.Now()

		_ = asdu.MeasuredValueFloatCP56Time2a(d.srv, spont, demoCA,
			asdu.MeasuredValueFloatInfo{Ioa: demoFloatBase, Value: floats[0], Time: now},
		)
		_ = asdu.MeasuredValueFloat(d.srv, false, periodic, demoCA,
			asdu.MeasuredValueFloatInfo{Ioa: demoFloatBase + 1, Value: floats[1]},
			asdu.MeasuredValueFloatInfo{Ioa: demoFloatBase + 2, Value: floats[2]},
			asdu.MeasuredValueFloatInfo{Ioa: demoFloatBase + 3, Value: floats[3],
				Qds: asdu.QDSSubstituted},
		)
		_ = asdu.MeasuredValueScaled(d.srv, false, periodic, demoCA,
			asdu.MeasuredValueScaledInfo{Ioa: demoScaledBase, Value: scaled[0]},
		)
		_ = asdu.IntegratedTotals(d.srv, false,
			asdu.CauseOfTransmission{Cause: asdu.Spontaneous}, demoCA,
			asdu.BinaryCounterReadingInfo{Ioa: demoCounter,
				Value: asdu.BinaryCounterReading{CounterReading: counter, SeqNumber: 1}},
		)
		if flip {
			for i, v := range single {
				_ = asdu.SingleCP56Time2a(d.srv, spont, demoCA,
					asdu.SinglePointInfo{Ioa: asdu.InfoObjAddr(demoSingleBase + i),
						Value: v, Time: now})
			}
		}
	}
}

// snapshot is everything the device knows, as an interrogation reply.
func (d *demoServer) snapshot(c asdu.Connect, cause asdu.CauseOfTransmission) {
	// cs104's Send does not block: a full send buffer refuses the ASDU
	// rather than queueing it. This database is small enough never to fill
	// it, but an interrogation reply is exactly where a real one would, so
	// the reply waits for room instead of discarding points.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c = cs104.Waiting(ctx, c)

	d.mu.Lock()
	single, double := d.single, d.double
	floats, scaled := d.floats, d.scaled
	// Counters are not part of a general interrogation; counter
	// interrogation returns them.
	step, bits := d.step, d.bits
	d.mu.Unlock()

	sp := make([]asdu.SinglePointInfo, 0, len(single))
	for i, v := range single {
		q := asdu.QDSGood
		if i == 3 {
			q = asdu.QDSNotTopical // one point with a flag, so quality is visible
		}
		sp = append(sp, asdu.SinglePointInfo{
			Ioa: asdu.InfoObjAddr(demoSingleBase + i), Value: v, Qds: q})
	}
	_ = asdu.Single(c, false, cause, demoCA, sp...)

	dp := make([]asdu.DoublePointInfo, 0, len(double))
	for i, v := range double {
		dp = append(dp, asdu.DoublePointInfo{
			Ioa: asdu.InfoObjAddr(demoDoubleBase + i), Value: v})
	}
	_ = asdu.Double(c, false, cause, demoCA, dp...)

	mv := make([]asdu.MeasuredValueFloatInfo, 0, len(floats))
	for i, v := range floats {
		q := asdu.QDSGood
		if i == 3 {
			q = asdu.QDSSubstituted
		}
		mv = append(mv, asdu.MeasuredValueFloatInfo{
			Ioa: asdu.InfoObjAddr(demoFloatBase + i), Value: v, Qds: q})
	}
	_ = asdu.MeasuredValueFloat(c, false, cause, demoCA, mv...)

	sv := make([]asdu.MeasuredValueScaledInfo, 0, len(scaled))
	for i, v := range scaled {
		sv = append(sv, asdu.MeasuredValueScaledInfo{
			Ioa: asdu.InfoObjAddr(demoScaledBase + i), Value: v})
	}
	_ = asdu.MeasuredValueScaled(c, false, cause, demoCA, sv...)

	_ = asdu.Step(c, false, cause, demoCA, asdu.StepPositionInfo{
		Ioa: demoStepBase, Value: asdu.StepPosition{Val: step}})
	_ = asdu.BitString32(c, false, cause, demoCA, asdu.BitString32Info{
		Ioa: demoBitstring, Value: bits})

	// The command points report their own state, so a command has something
	// visible to change.
	_ = asdu.Single(c, false, cause, demoCA,
		asdu.SinglePointInfo{Ioa: demoCmdSingle, Value: single[0]})
	_ = asdu.Double(c, false, cause, demoCA,
		asdu.DoublePointInfo{Ioa: demoCmdDouble, Value: double[0]})
	_ = asdu.MeasuredValueFloat(c, false, cause, demoCA,
		asdu.MeasuredValueFloatInfo{Ioa: demoCmdSetpt, Value: floats[3]})
}

// demoHandler is the outstation's application layer.
type demoHandler struct {
	d      *demoServer
	sender *filetransfer.Sender
}

func (h *demoHandler) InterrogationHandler(c asdu.Connect, a *asdu.ASDU, _ asdu.QualifierOfInterrogation) error {
	_ = a.SendReplyMirror(c, asdu.ActivationCon)
	h.d.snapshot(c, asdu.CauseOfTransmission{Cause: asdu.InterrogatedByStation})
	return a.SendReplyMirror(c, asdu.ActivationTerm)
}

func (h *demoHandler) CounterInterrogationHandler(c asdu.Connect, a *asdu.ASDU, _ asdu.QualifierCountCall) error {
	_ = a.SendReplyMirror(c, asdu.ActivationCon)
	h.d.mu.Lock()
	counter := h.d.counter
	h.d.mu.Unlock()
	_ = asdu.IntegratedTotals(c, false,
		asdu.CauseOfTransmission{Cause: asdu.RequestByGeneralCounter}, demoCA,
		asdu.BinaryCounterReadingInfo{Ioa: demoCounter,
			Value: asdu.BinaryCounterReading{CounterReading: counter, SeqNumber: 2}})
	return a.SendReplyMirror(c, asdu.ActivationTerm)
}

func (h *demoHandler) ReadHandler(c asdu.Connect, a *asdu.ASDU, ioa asdu.InfoObjAddr) error {
	h.d.mu.Lock()
	floats := h.d.floats
	h.d.mu.Unlock()
	if ioa >= demoFloatBase && ioa < demoFloatBase+asdu.InfoObjAddr(len(floats)) {
		return asdu.MeasuredValueFloat(c, false,
			asdu.CauseOfTransmission{Cause: asdu.Request}, demoCA,
			asdu.MeasuredValueFloatInfo{Ioa: ioa, Value: floats[ioa-demoFloatBase]})
	}
	a.Coa.IsNegative = true
	return a.SendReplyMirror(c, asdu.UnknownIOA)
}

func (h *demoHandler) ClockSyncHandler(c asdu.Connect, a *asdu.ASDU, _ time.Time) error {
	return a.SendReplyMirror(c, asdu.ActivationCon)
}

func (h *demoHandler) ResetProcessHandler(c asdu.Connect, a *asdu.ASDU, _ asdu.QualifierOfResetProcessCmd) error {
	return a.SendReplyMirror(c, asdu.ActivationCon)
}

func (h *demoHandler) DelayAcquisitionHandler(c asdu.Connect, a *asdu.ASDU, _ uint16) error {
	return a.SendReplyMirror(c, asdu.ActivationCon)
}

func (h *demoHandler) ASDUHandlerAll(asdu.Connect, *asdu.ASDU, int) error { return nil }

// ASDUHandler takes the commands and the file transfer.
func (h *demoHandler) ASDUHandler(c asdu.Connect, a *asdu.ASDU) error {
	if handled, _ := h.sender.Handle(c, a); handled {
		return nil
	}

	switch a.Type {
	case asdu.C_SC_NA_1, asdu.C_SC_TA_1:
		cmd := a.GetSingleCmd()
		_ = a.SendReplyMirror(c, asdu.ActivationCon)
		if cmd.Qoc.InSelect {
			// A select is the outstation's chance to refuse before plant
			// moves; this one accepts and waits for the execute.
			return nil
		}
		h.d.mu.Lock()
		h.d.single[0] = cmd.Value
		h.d.mu.Unlock()
		_ = asdu.Single(c, false,
			asdu.CauseOfTransmission{Cause: asdu.ReturnInfoRemote}, demoCA,
			asdu.SinglePointInfo{Ioa: cmd.Ioa, Value: cmd.Value})
		return a.SendReplyMirror(c, asdu.ActivationTerm)

	case asdu.C_DC_NA_1, asdu.C_DC_TA_1:
		cmd := a.GetDoubleCmd()
		_ = a.SendReplyMirror(c, asdu.ActivationCon)
		if cmd.Qoc.InSelect {
			return nil
		}
		v := asdu.DPIDeterminedOff
		if cmd.Value == asdu.DCOOn {
			v = asdu.DPIDeterminedOn
		}
		h.d.mu.Lock()
		h.d.double[0] = v
		h.d.mu.Unlock()
		_ = asdu.Double(c, false,
			asdu.CauseOfTransmission{Cause: asdu.ReturnInfoRemote}, demoCA,
			asdu.DoublePointInfo{Ioa: cmd.Ioa, Value: v})
		return a.SendReplyMirror(c, asdu.ActivationTerm)

	case asdu.C_RC_NA_1, asdu.C_RC_TA_1:
		cmd := a.GetStepCmd()
		_ = a.SendReplyMirror(c, asdu.ActivationCon)
		if cmd.Qoc.InSelect {
			return nil
		}
		h.d.mu.Lock()
		if cmd.Value == asdu.SCOStepUP {
			h.d.step++
		} else {
			h.d.step--
		}
		step := h.d.step
		h.d.mu.Unlock()
		_ = asdu.Step(c, false,
			asdu.CauseOfTransmission{Cause: asdu.ReturnInfoRemote}, demoCA,
			asdu.StepPositionInfo{Ioa: cmd.Ioa, Value: asdu.StepPosition{Val: step}})
		return a.SendReplyMirror(c, asdu.ActivationTerm)

	case asdu.C_SE_NC_1, asdu.C_SE_TC_1:
		cmd := a.GetSetpointFloatCmd()
		_ = a.SendReplyMirror(c, asdu.ActivationCon)
		if cmd.Qos.InSelect {
			return nil
		}
		h.d.mu.Lock()
		h.d.floats[3] = cmd.Value
		h.d.mu.Unlock()
		_ = asdu.MeasuredValueFloat(c, false,
			asdu.CauseOfTransmission{Cause: asdu.ReturnInfoRemote}, demoCA,
			asdu.MeasuredValueFloatInfo{Ioa: cmd.Ioa, Value: cmd.Value})
		return a.SendReplyMirror(c, asdu.ActivationTerm)

	case asdu.C_SE_NA_1, asdu.C_SE_NB_1, asdu.C_BO_NA_1:
		_ = a.SendReplyMirror(c, asdu.ActivationCon)
		return a.SendReplyMirror(c, asdu.ActivationTerm)
	}
	return nil
}

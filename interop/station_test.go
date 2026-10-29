package interop

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/cs104"
	"github.com/riclolsen/go-iecp5/filetransfer"
)

// station is a controlled station built on go-iecp5 that behaves as the
// fixture says (docs/FIXTURES.md in iec104-interop): the fixture is read from
// the image under test, so the model cannot drift from it.
type station struct {
	fx *fixture
	ca asdu.CommonAddr

	mu       sync.Mutex
	points   []*point          // in fixture order, which is ascending address order
	byIoa    map[uint32]*point // point address -> point
	selected map[uint32]bool   // command address -> selected

	senders map[uint32]*filetransfer.Sender // file address -> transfer service
	files   map[uint32]uint16               // file address -> name of file
}

type point struct {
	ioa   uint32
	typ   string
	value float64
	qds   asdu.QualityDescriptor
	seq   byte
	trans bool
}

func newStation(fx *fixture) *station {
	s := &station{
		fx: fx, ca: asdu.CommonAddr(fx.Station.CommonAddress),
		byIoa: map[uint32]*point{}, selected: map[uint32]bool{},
		senders: map[uint32]*filetransfer.Sender{}, files: map[uint32]uint16{},
	}
	for _, p := range fx.Points {
		pt := &point{ioa: p.Ioa, typ: p.Type, value: float64(p.Value), qds: asdu.QualityDescriptor(qdsOf(p.Quality)), seq: p.Sequence, trans: p.Transient}
		s.points = append(s.points, pt)
		s.byIoa[p.Ioa] = pt
	}
	for _, f := range fx.Files {
		store := filetransfer.NewMemStore()
		_ = store.Write(asdu.InfoObjAddr(f.Ioa), asdu.NameOfFile(f.Name), fileContent(f.Ioa, f.Size))
		s.senders[f.Ioa] = filetransfer.NewSender(store).SetSectionSize(f.SectionSize)
		s.files[f.Ioa] = f.Name
	}
	return s
}

// startStation serves the station on a free port of all interfaces (the
// reference clients run in containers) and returns the port.
func startStation(t *testing.T, s *station) int {
	t.Helper()
	l, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	srv := cs104.NewServer(s)
	srv.LogMode(false)
	go func() { _ = srv.ListenAndServer(fmt.Sprintf("0.0.0.0:%d", port)) }()
	t.Cleanup(func() { _ = srv.Close() })
	for deadline := time.Now().Add(5 * time.Second); ; {
		c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			_ = c.Close()
			return port
		}
		if time.Now().After(deadline) {
			t.Fatalf("station never listened: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *station) forMe(a *asdu.ASDU) bool {
	return a.CommonAddr == s.ca || a.CommonAddr == asdu.GlobalCommonAddr
}

// mirror answers a request with the same type and objects and another cause.
// The P/N bit is set when negative (asdu's SendReplyMirror cannot set it).
func mirror(c asdu.Connect, a *asdu.ASDU, cause asdu.Cause, negative bool) error {
	r := asdu.NewASDU(a.Params, a.Identifier)
	r.CommonAddr = a.CommonAddr
	r.Coa.Cause = cause
	r.Coa.IsNegative = negative
	r.InfoObj = append(r.InfoObj, a.InfoObj...)
	return c.Send(r)
}

func (s *station) reply(c asdu.Connect, a *asdu.ASDU, cause asdu.Cause, negative bool) error {
	r := asdu.NewASDU(a.Params, a.Identifier)
	r.CommonAddr = s.ca
	r.Coa.Cause = cause
	r.Coa.IsNegative = negative
	r.InfoObj = append(r.InfoObj, a.InfoObj...)
	return c.Send(r)
}

// sendPoints sends the points in order; consecutive points of one type share
// an ASDU (SQ = 0). With timed the CP56Time2a type is used.
func (s *station) sendPoints(c asdu.Connect, cause asdu.Cause, timed bool, pts []*point) error {
	coa := asdu.CauseOfTransmission{Cause: cause}
	now := time.Now().UTC()
	for i := 0; i < len(pts); {
		j := i
		for j < len(pts) && pts[j].typ == pts[i].typ {
			j++
		}
		group := pts[i:j]
		i = j
		var err error
		switch group[0].typ {
		case "M_SP_NA_1":
			var v []asdu.SinglePointInfo
			for _, p := range group {
				v = append(v, asdu.SinglePointInfo{Ioa: asdu.InfoObjAddr(p.ioa), Value: p.value != 0, Qds: p.qds, Time: now})
			}
			err = pick(timed, func() error { return asdu.SingleCP56Time2a(c, coa, s.ca, v...) }, func() error { return asdu.Single(c, false, coa, s.ca, v...) })
		case "M_DP_NA_1":
			var v []asdu.DoublePointInfo
			for _, p := range group {
				v = append(v, asdu.DoublePointInfo{Ioa: asdu.InfoObjAddr(p.ioa), Value: asdu.DoublePoint(p.value), Qds: p.qds, Time: now})
			}
			err = pick(timed, func() error { return asdu.DoubleCP56Time2a(c, coa, s.ca, v...) }, func() error { return asdu.Double(c, false, coa, s.ca, v...) })
		case "M_ST_NA_1":
			var v []asdu.StepPositionInfo
			for _, p := range group {
				v = append(v, asdu.StepPositionInfo{Ioa: asdu.InfoObjAddr(p.ioa), Value: asdu.StepPosition{Val: int(p.value), HasTransient: p.trans}, Qds: p.qds, Time: now})
			}
			err = pick(timed, func() error { return asdu.StepCP56Time2a(c, coa, s.ca, v...) }, func() error { return asdu.Step(c, false, coa, s.ca, v...) })
		case "M_BO_NA_1":
			var v []asdu.BitString32Info
			for _, p := range group {
				v = append(v, asdu.BitString32Info{Ioa: asdu.InfoObjAddr(p.ioa), Value: uint32(p.value), Qds: p.qds, Time: now})
			}
			err = pick(timed, func() error { return asdu.BitString32CP56Time2a(c, coa, s.ca, v...) }, func() error { return asdu.BitString32(c, false, coa, s.ca, v...) })
		case "M_ME_NA_1":
			var v []asdu.MeasuredValueNormalInfo
			for _, p := range group {
				v = append(v, asdu.MeasuredValueNormalInfo{Ioa: asdu.InfoObjAddr(p.ioa), Value: asdu.Normalize(p.value), Qds: p.qds, Time: now})
			}
			err = pick(timed, func() error { return asdu.MeasuredValueNormalCP56Time2a(c, coa, s.ca, v...) }, func() error { return asdu.MeasuredValueNormal(c, false, coa, s.ca, v...) })
		case "M_ME_NB_1":
			var v []asdu.MeasuredValueScaledInfo
			for _, p := range group {
				v = append(v, asdu.MeasuredValueScaledInfo{Ioa: asdu.InfoObjAddr(p.ioa), Value: int16(p.value), Qds: p.qds, Time: now})
			}
			err = pick(timed, func() error { return asdu.MeasuredValueScaledCP56Time2a(c, coa, s.ca, v...) }, func() error { return asdu.MeasuredValueScaled(c, false, coa, s.ca, v...) })
		case "M_ME_NC_1":
			var v []asdu.MeasuredValueFloatInfo
			for _, p := range group {
				v = append(v, asdu.MeasuredValueFloatInfo{Ioa: asdu.InfoObjAddr(p.ioa), Value: float32(p.value), Qds: p.qds, Time: now})
			}
			err = pick(timed, func() error { return asdu.MeasuredValueFloatCP56Time2a(c, coa, s.ca, v...) }, func() error { return asdu.MeasuredValueFloat(c, false, coa, s.ca, v...) })
		case "M_IT_NA_1":
			var v []asdu.BinaryCounterReadingInfo
			for _, p := range group {
				v = append(v, asdu.BinaryCounterReadingInfo{Ioa: asdu.InfoObjAddr(p.ioa),
					Value: asdu.BinaryCounterReading{CounterReading: int32(p.value), SeqNumber: p.seq}, Time: now})
			}
			err = pick(timed, func() error { return asdu.IntegratedTotalsCP56Time2a(c, coa, s.ca, v...) }, func() error { return asdu.IntegratedTotals(c, false, coa, s.ca, v...) })
		default:
			err = fmt.Errorf("point type %s not modelled", group[0].typ)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func pick(timed bool, withTime, plain func() error) error {
	if timed {
		return withTime()
	}
	return plain()
}

func (s *station) snapshot(filter func(*point) bool) []*point {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*point
	for _, p := range s.points {
		if filter(p) {
			cp := *p
			out = append(out, &cp)
		}
	}
	return out
}

// InterrogationHandler: QOI 20 only.
func (s *station) InterrogationHandler(c asdu.Connect, a *asdu.ASDU, qoi asdu.QualifierOfInterrogation) error {
	if !s.forMe(a) {
		return mirror(c, a, asdu.UnknownCA, true)
	}
	if qoi != 20 {
		return s.reply(c, a, asdu.ActivationCon, true)
	}
	if err := s.reply(c, a, asdu.ActivationCon, false); err != nil {
		return err
	}
	pts := s.snapshot(func(p *point) bool { return p.typ != "M_IT_NA_1" })
	if err := s.sendPoints(c, asdu.InterrogatedByStation, false, pts); err != nil {
		return err
	}
	return s.reply(c, a, asdu.ActivationTerm, false)
}

// CounterInterrogationHandler: QCC 5 (general request, no freeze) only.
func (s *station) CounterInterrogationHandler(c asdu.Connect, a *asdu.ASDU, qcc asdu.QualifierCountCall) error {
	if !s.forMe(a) {
		return mirror(c, a, asdu.UnknownCA, true)
	}
	if qcc != asdu.ParseQualifierCountCall(5) {
		return s.reply(c, a, asdu.ActivationCon, true)
	}
	if err := s.reply(c, a, asdu.ActivationCon, false); err != nil {
		return err
	}
	pts := s.snapshot(func(p *point) bool { return p.typ == "M_IT_NA_1" })
	if err := s.sendPoints(c, asdu.RequestByGeneralCounter, false, pts); err != nil {
		return err
	}
	return s.reply(c, a, asdu.ActivationTerm, false)
}

func (s *station) ReadHandler(c asdu.Connect, a *asdu.ASDU, ioa asdu.InfoObjAddr) error {
	if !s.forMe(a) {
		return mirror(c, a, asdu.UnknownCA, true)
	}
	pts := s.snapshot(func(p *point) bool { return p.ioa == uint32(ioa) })
	if len(pts) == 0 {
		return mirror(c, a, asdu.UnknownIOA, true)
	}
	return s.sendPoints(c, asdu.Request, false, pts)
}

func (s *station) ClockSyncHandler(c asdu.Connect, a *asdu.ASDU, _ time.Time) error {
	if !s.forMe(a) {
		return mirror(c, a, asdu.UnknownCA, true)
	}
	return s.reply(c, a, asdu.ActivationCon, false)
}

func (s *station) ResetProcessHandler(c asdu.Connect, a *asdu.ASDU, _ asdu.QualifierOfResetProcessCmd) error {
	return mirror(c, a, asdu.UnknownTypeID, true)
}

func (s *station) DelayAcquisitionHandler(c asdu.Connect, a *asdu.ASDU, _ uint16) error {
	return mirror(c, a, asdu.UnknownTypeID, true)
}

func (s *station) ASDUHandlerAll(asdu.Connect, *asdu.ASDU, int) error { return nil }

// ASDUHandler receives everything the cs104 server does not dispatch itself:
// the process commands, the test command with time tag and file transfer.
func (s *station) ASDUHandler(c asdu.Connect, a *asdu.ASDU) error {
	switch a.Type {
	case asdu.C_SC_NA_1, asdu.C_SC_TA_1, asdu.C_DC_NA_1, asdu.C_DC_TA_1, asdu.C_RC_NA_1, asdu.C_RC_TA_1,
		asdu.C_SE_NA_1, asdu.C_SE_TA_1, asdu.C_SE_NB_1, asdu.C_SE_TB_1, asdu.C_SE_NC_1, asdu.C_SE_TC_1:
		return s.command(c, a)
	case asdu.C_TS_TA_1:
		if !s.forMe(a) {
			return mirror(c, a, asdu.UnknownCA, true)
		}
		return s.reply(c, a, asdu.ActivationCon, false)
	case asdu.F_FR_NA_1, asdu.F_SR_NA_1, asdu.F_SC_NA_1, asdu.F_LS_NA_1, asdu.F_AF_NA_1, asdu.F_SG_NA_1:
		return s.file(c, a)
	}
	return mirror(c, a, asdu.UnknownTypeID, true)
}

// command implements the process command table of FIXTURES.md.
func (s *station) command(c asdu.Connect, a *asdu.ASDU) error {
	if !s.forMe(a) {
		return mirror(c, a, asdu.UnknownCA, true)
	}
	if cause := a.Coa.Cause; cause != asdu.Activation && cause != asdu.Deactivation {
		return mirror(c, a, asdu.UnknownCOT, true)
	}

	// base type (without time tag), address, select bit, step/state value
	var base string
	var ioa uint32
	var sel bool
	var state byte // C_DC / C_RC state
	var value float64
	switch a.Type {
	case asdu.C_SC_NA_1, asdu.C_SC_TA_1:
		i := a.GetSingleCmd()
		base, ioa, sel = "C_SC_NA_1", uint32(i.Ioa), i.Qoc.InSelect
		if i.Value {
			value = 1
		}
	case asdu.C_DC_NA_1, asdu.C_DC_TA_1:
		i := a.GetDoubleCmd()
		base, ioa, sel, state = "C_DC_NA_1", uint32(i.Ioa), i.Qoc.InSelect, byte(i.Value)
		value = float64(i.Value)
	case asdu.C_RC_NA_1, asdu.C_RC_TA_1:
		i := a.GetStepCmd()
		base, ioa, sel, state = "C_RC_NA_1", uint32(i.Ioa), i.Qoc.InSelect, byte(i.Value)
	case asdu.C_SE_NA_1, asdu.C_SE_TA_1:
		i := a.GetSetpointNormalCmd()
		base, ioa, sel, value = "C_SE_NA_1", uint32(i.Ioa), i.Qos.InSelect, float64(i.Value)
	case asdu.C_SE_NB_1, asdu.C_SE_TB_1:
		i := a.GetSetpointCmdScaled()
		base, ioa, sel, value = "C_SE_NB_1", uint32(i.Ioa), i.Qos.InSelect, float64(i.Value)
	default:
		i := a.GetSetpointFloatCmd()
		base, ioa, sel, value = "C_SE_NC_1", uint32(i.Ioa), i.Qos.InSelect, float64(i.Value)
	}

	var target *point
	var reportCause asdu.Cause
	var selectRequired, found bool
	for _, cmd := range s.fx.Commands {
		if cmd.Ioa == ioa && cmd.Type == base {
			found, selectRequired = true, cmd.SelectRequired
			reportCause = asdu.Cause(cmd.ReportCause)
			if reportCause == 0 {
				reportCause = asdu.ReturnInfoRemote
			}
			s.mu.Lock()
			target = s.byIoa[cmd.Target]
			s.mu.Unlock()
		}
	}
	if !found {
		return mirror(c, a, asdu.UnknownIOA, true)
	}

	s.mu.Lock()
	if a.Coa.Cause == asdu.Deactivation {
		delete(s.selected, ioa)
		s.mu.Unlock()
		return mirror(c, a, asdu.DeactivationCon, false)
	}
	if (base == "C_DC_NA_1" || base == "C_RC_NA_1") && (state == 0 || state == 3) {
		s.mu.Unlock()
		return mirror(c, a, asdu.ActivationCon, true)
	}
	if sel {
		s.selected[ioa] = true
		s.mu.Unlock()
		return mirror(c, a, asdu.ActivationCon, false)
	}
	if selectRequired && !s.selected[ioa] {
		s.mu.Unlock()
		return mirror(c, a, asdu.ActivationCon, true)
	}
	if base == "C_RC_NA_1" {
		next := target.value - 1
		if state == 2 {
			next = target.value + 1
		}
		if next < -64 || next > 63 {
			delete(s.selected, ioa)
			s.mu.Unlock()
			return mirror(c, a, asdu.ActivationCon, true)
		}
		value = next
	}
	target.value = value
	delete(s.selected, ioa)
	report := *target
	s.mu.Unlock()

	if err := mirror(c, a, asdu.ActivationCon, false); err != nil {
		return err
	}
	if err := s.sendPoints(c, reportCause, true, []*point{&report}); err != nil {
		return err
	}
	return mirror(c, a, asdu.ActivationTerm, false)
}

// file serves the file transfer procedure with one filetransfer.Sender per
// file, since a Sender has a single section size and the fixture sets one per
// file. A selection of an unknown file or name is refused as the fixture says.
func (s *station) file(c asdu.Connect, a *asdu.ASDU) error {
	if len(a.InfoObj) < 3 {
		return nil
	}
	ioa := uint32(a.InfoObj[0]) | uint32(a.InfoObj[1])<<8 | uint32(a.InfoObj[2])<<16
	if a.Type == asdu.F_SC_NA_1 {
		info := a.GetCallOrSelectFile()
		name, known := s.files[ioa]
		if info.Scq.Action == asdu.SCQSelectFile && (!known || uint16(info.Nof) != name) {
			return mirror(c, a, asdu.UnknownIOA, true)
		}
	}
	sender, ok := s.senders[ioa]
	if !ok {
		return nil
	}
	_, err := sender.Handle(c, a)
	return err
}

var _ cs104.ServerHandlerInterface = (*station)(nil)

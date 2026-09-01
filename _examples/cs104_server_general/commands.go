package main

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
)

// Commands live in their own address space.
//
// Nothing in IEC 60870-5-104 says the command that operates the single point
// at 1001 is itself at 1001, and this simulator deliberately does not put it
// there: the command block starts at 9001, and each command names the
// monitored object it moves. A master that assumes the two addresses are the
// same will find nothing here, which is the point.
const (
	ioaCmdSingle   = 9001 // C_SC_NA_1 / C_SC_TA_1 -> single points 1001..1004
	ioaCmdDouble   = 9101 // C_DC_NA_1 / C_DC_TA_1 -> double points 2001..2002
	ioaCmdStep     = 9201 // C_RC_NA_1 / C_RC_TA_1 -> step position 3001
	ioaCmdNormal   = 9301 // C_SE_NA_1 / C_SE_TA_1 -> normalized 4001
	ioaCmdScaled   = 9401 // C_SE_NB_1 / C_SE_TB_1 -> scaled 4501
	ioaCmdFloat    = 9501 // C_SE_NC_1 / C_SE_TC_1 -> short float 5001
	ioaCmdBits     = 9601 // C_BO_NA_1 / C_BO_TA_1 -> bit string 3501
	ioaCmdRefusing = 9004 // always answers with a negative confirmation

	nCmdSingle = 4
	nCmdDouble = 2
)

// selection remembers an outstanding select, so that an execute can be
// checked against it. A select for one address must never license an execute
// on another.
type selection struct {
	mu     sync.Mutex
	typ    asdu.TypeID
	ioa    asdu.InfoObjAddr
	at     time.Time
	active bool
}

// selectTimeout is how long a select stays valid. A select that is never
// executed must expire, or a stale one authorises an operation minutes later.
const selectTimeout = 30 * time.Second

func (s *selection) set(typ asdu.TypeID, ioa asdu.InfoObjAddr) {
	s.mu.Lock()
	s.typ, s.ioa, s.at, s.active = typ, ioa, time.Now(), true
	s.mu.Unlock()
}

// take consumes a select for the given address, reporting whether one was
// outstanding and still valid.
func (s *selection) take(typ asdu.TypeID, ioa asdu.InfoObjAddr) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ok := s.active && s.typ == typ && s.ioa == ioa &&
		time.Since(s.at) < selectTimeout
	s.active = false
	return ok
}

func (s *selection) clear() {
	s.mu.Lock()
	s.active = false
	s.mu.Unlock()
}

// handleCommand answers one command ASDU. It returns true when the ASDU was a
// command this simulator recognises.
func (h *handler) handleCommand(c asdu.Connect, a *asdu.ASDU) bool {
	switch a.Type {
	case asdu.C_SC_NA_1, asdu.C_SC_TA_1:
		cmd := a.GetSingleCmd()
		h.operate(c, a, cmd.Ioa, cmd.Qoc.InSelect, func() (asdu.InfoObjAddr, string) {
			i := int(cmd.Ioa - ioaCmdSingle)
			h.sim.setSingle(i, cmd.Value)
			ioa := asdu.InfoObjAddr(ioaSingle + i)
			h.returnSingle(c, ioa, cmd.Value)
			return ioa, fmt.Sprintf("single point %d := %v", ioa, cmd.Value)
		}, func(ioa asdu.InfoObjAddr) bool {
			return ioa >= ioaCmdSingle && ioa < ioaCmdSingle+nCmdSingle
		})
		return true

	case asdu.C_DC_NA_1, asdu.C_DC_TA_1:
		cmd := a.GetDoubleCmd()
		h.operate(c, a, cmd.Ioa, cmd.Qoc.InSelect, func() (asdu.InfoObjAddr, string) {
			i := int(cmd.Ioa - ioaCmdDouble)
			v := asdu.DPIDeterminedOff
			if cmd.Value == asdu.DCOOn {
				v = asdu.DPIDeterminedOn
			}
			h.sim.setDouble(i, v)
			ioa := asdu.InfoObjAddr(ioaDouble + i)
			_ = asdu.Double(c, false,
				asdu.CauseOfTransmission{Cause: asdu.ReturnInfoRemote}, simCA,
				asdu.DoublePointInfo{Ioa: ioa, Value: v})
			return ioa, fmt.Sprintf("double point %d := %v", ioa, v)
		}, func(ioa asdu.InfoObjAddr) bool {
			return ioa >= ioaCmdDouble && ioa < ioaCmdDouble+nCmdDouble
		})
		return true

	case asdu.C_RC_NA_1, asdu.C_RC_TA_1:
		cmd := a.GetStepCmd()
		h.operate(c, a, cmd.Ioa, cmd.Qoc.InSelect, func() (asdu.InfoObjAddr, string) {
			step := h.sim.stepBy(0, cmd.Value == asdu.SCOStepUP)
			ioa := asdu.InfoObjAddr(ioaStep)
			_ = asdu.Step(c, false,
				asdu.CauseOfTransmission{Cause: asdu.ReturnInfoRemote}, simCA,
				asdu.StepPositionInfo{Ioa: ioa, Value: step})
			return ioa, fmt.Sprintf("step position %d := %d", ioa, step.Val)
		}, func(ioa asdu.InfoObjAddr) bool { return ioa == ioaCmdStep })
		return true

	case asdu.C_SE_NA_1, asdu.C_SE_TA_1:
		cmd := a.GetSetpointNormalCmd()
		h.operate(c, a, cmd.Ioa, cmd.Qos.InSelect, func() (asdu.InfoObjAddr, string) {
			h.sim.setNormal(0, cmd.Value)
			ioa := asdu.InfoObjAddr(ioaNormal)
			_ = asdu.MeasuredValueNormal(c, false,
				asdu.CauseOfTransmission{Cause: asdu.ReturnInfoRemote}, simCA,
				asdu.MeasuredValueNormalInfo{Ioa: ioa, Value: cmd.Value})
			return ioa, fmt.Sprintf("normalized %d := %.5f", ioa, cmd.Value.Float64())
		}, func(ioa asdu.InfoObjAddr) bool { return ioa == ioaCmdNormal })
		return true

	case asdu.C_SE_NB_1, asdu.C_SE_TB_1:
		cmd := a.GetSetpointCmdScaled()
		h.operate(c, a, cmd.Ioa, cmd.Qos.InSelect, func() (asdu.InfoObjAddr, string) {
			h.sim.setScaled(0, cmd.Value)
			ioa := asdu.InfoObjAddr(ioaScaled)
			_ = asdu.MeasuredValueScaled(c, false,
				asdu.CauseOfTransmission{Cause: asdu.ReturnInfoRemote}, simCA,
				asdu.MeasuredValueScaledInfo{Ioa: ioa, Value: cmd.Value})
			return ioa, fmt.Sprintf("scaled %d := %d", ioa, cmd.Value)
		}, func(ioa asdu.InfoObjAddr) bool { return ioa == ioaCmdScaled })
		return true

	case asdu.C_SE_NC_1, asdu.C_SE_TC_1:
		cmd := a.GetSetpointFloatCmd()
		h.operate(c, a, cmd.Ioa, cmd.Qos.InSelect, func() (asdu.InfoObjAddr, string) {
			h.sim.setFloat(0, cmd.Value)
			ioa := asdu.InfoObjAddr(ioaFloat)
			_ = asdu.MeasuredValueFloat(c, false,
				asdu.CauseOfTransmission{Cause: asdu.ReturnInfoRemote}, simCA,
				asdu.MeasuredValueFloatInfo{Ioa: ioa, Value: cmd.Value})
			return ioa, fmt.Sprintf("short float %d := %g", ioa, cmd.Value)
		}, func(ioa asdu.InfoObjAddr) bool { return ioa == ioaCmdFloat })
		return true

	case asdu.C_BO_NA_1, asdu.C_BO_TA_1:
		cmd := a.GetBitsString32Cmd()
		// A bit string command carries no select/execute bit.
		h.operate(c, a, cmd.Ioa, false, func() (asdu.InfoObjAddr, string) {
			h.sim.setBits(0, cmd.Value)
			ioa := asdu.InfoObjAddr(ioaBits)
			_ = asdu.BitString32(c, false,
				asdu.CauseOfTransmission{Cause: asdu.ReturnInfoRemote}, simCA,
				asdu.BitString32Info{Ioa: ioa, Value: cmd.Value})
			return ioa, fmt.Sprintf("bit string %d := 0x%08X", ioa, cmd.Value)
		}, func(ioa asdu.InfoObjAddr) bool { return ioa == ioaCmdBits })
		return true
	}
	return false
}

// operate is the confirmation sequence every command follows.
//
// Unknown address, refusal, select and execute all answer differently, and
// each answer is what a master needs to distinguish "the device said no" from
// "the device never replied".
func (h *handler) operate(c asdu.Connect, a *asdu.ASDU, ioa asdu.InfoObjAddr,
	isSelect bool, apply func() (asdu.InfoObjAddr, string), known func(asdu.InfoObjAddr) bool) {

	if !known(ioa) {
		log.Printf("command %s: no command at ioa %d", typeName(a.Type), ioa)
		_ = a.SendReplyMirror(c, asdu.UnknownIOA)
		return
	}

	// One address always refuses, so a master has something to show a
	// negative activation confirmation with.
	if ioa == ioaCmdRefusing {
		log.Printf("command %s ioa %d: refusing (this address always does)",
			typeName(a.Type), ioa)
		a.Coa.IsNegative = true
		_ = a.SendReplyMirror(c, asdu.ActivationCon)
		return
	}

	if isSelect {
		h.sel.set(a.Type, ioa)
		log.Printf("command %s ioa %d: selected", typeName(a.Type), ioa)
		return_ := *a
		_ = return_.SendReplyMirror(c, asdu.ActivationCon)
		return
	}

	// An execute after a select must match it. A direct execute — one that
	// was never selected — is accepted, because plenty of devices allow it
	// and refusing would make this simulator useless for testing that path.
	h.sel.take(a.Type, ioa)

	_ = a.SendReplyMirror(c, asdu.ActivationCon)
	_, what := apply()
	log.Printf("command %s ioa %d: %s", typeName(a.Type), ioa, what)
	_ = a.SendReplyMirror(c, asdu.ActivationTerm)
}

// returnSingle reports a single point back after a command moved it.
func (h *handler) returnSingle(c asdu.Connect, ioa asdu.InfoObjAddr, v bool) {
	_ = asdu.Single(c, false,
		asdu.CauseOfTransmission{Cause: asdu.ReturnInfoRemote}, simCA,
		asdu.SinglePointInfo{Ioa: ioa, Value: v})
}

// ---------- the simulation's setters ----------

func (s *sim) setSingle(i int, v bool) {
	s.mu.Lock()
	if i >= 0 && i < len(s.single) {
		s.single[i] = v
	}
	s.mu.Unlock()
}

func (s *sim) setDouble(i int, v asdu.DoublePoint) {
	s.mu.Lock()
	if i >= 0 && i < len(s.double) {
		s.double[i] = v
	}
	s.mu.Unlock()
}

func (s *sim) stepBy(i int, up bool) asdu.StepPosition {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.step) {
		return asdu.StepPosition{}
	}
	v := s.step[i].Val
	if up {
		v = min(v+1, 63)
	} else {
		v = max(v-1, -64)
	}
	s.step[i] = asdu.StepPosition{Val: v}
	return s.step[i]
}

func (s *sim) setNormal(i int, v asdu.Normalize) {
	s.mu.Lock()
	if i >= 0 && i < len(s.normal) {
		s.normal[i] = v
	}
	s.mu.Unlock()
}

func (s *sim) setScaled(i int, v int16) {
	s.mu.Lock()
	if i >= 0 && i < len(s.scaled) {
		s.scaled[i] = v
	}
	s.mu.Unlock()
}

func (s *sim) setFloat(i int, v float32) {
	s.mu.Lock()
	if i >= 0 && i < len(s.floats) {
		s.floats[i] = v
	}
	s.mu.Unlock()
}

func (s *sim) setBits(i int, v uint32) {
	s.mu.Lock()
	if i >= 0 && i < len(s.bits) {
		s.bits[i] = v
	}
	s.mu.Unlock()
}

// typeName renders a type identification without the TID<> wrapper, for the
// log lines above.
func typeName(t asdu.TypeID) string {
	s := t.String()
	s = trimPrefix(s, "TID<")
	return trimSuffix(s, ">")
}

func trimPrefix(s, p string) string {
	if len(s) >= len(p) && s[:len(p)] == p {
		return s[len(p):]
	}
	return s
}

func trimSuffix(s, x string) string {
	if len(s) >= len(x) && s[len(s)-len(x):] == x {
		return s[:len(s)-len(x)]
	}
	return s
}

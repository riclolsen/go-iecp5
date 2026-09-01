package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/cs104"
)

// In IEC 60870-5-104 a command is not attached to a monitored object: the
// information object address of a command lives in its own address space, and
// nothing in the protocol says that the single point at IOA 100 is operated
// by a command at IOA 100. So a command is never derived from whatever row
// the cursor happens to be on — every parameter is entered deliberately, and
// this file is the single place that turns those parameters into an ASDU.

// valueKind is how a command's value is entered.
type valueKind int

const (
	valOnOff  valueKind = iota // single and double commands
	valStep                    // regulating step: lower / higher
	valNormal                  // normalised, -1..1
	valScaled                  // scaled, int16
	valFloat                   // short float
	valBits                    // 32 bit string
)

// qualKind is which qualifier a command carries.
type qualKind int

const (
	qualQOC  qualKind = iota // qualifier of command: pulse duration + S/E
	qualQOS                  // qualifier of set-point command: 0..127 + S/E
	qualNone                 // bit strings carry neither
)

// cmdSpec describes one command type identification.
type cmdSpec struct {
	id      asdu.TypeID
	label   string
	value   valueKind
	qual    qualKind
	timeTag bool // the CP56Time2a variant
}

// cmdSpecs is every command this tool can send, in the order the dialog
// cycles through them.
var cmdSpecs = []cmdSpec{
	{asdu.C_SC_NA_1, "C_SC_NA_1  single command", valOnOff, qualQOC, false},
	{asdu.C_SC_TA_1, "C_SC_TA_1  single command, time tag", valOnOff, qualQOC, true},
	{asdu.C_DC_NA_1, "C_DC_NA_1  double command", valOnOff, qualQOC, false},
	{asdu.C_DC_TA_1, "C_DC_TA_1  double command, time tag", valOnOff, qualQOC, true},
	{asdu.C_RC_NA_1, "C_RC_NA_1  regulating step", valStep, qualQOC, false},
	{asdu.C_RC_TA_1, "C_RC_TA_1  regulating step, time tag", valStep, qualQOC, true},
	{asdu.C_SE_NA_1, "C_SE_NA_1  set-point, normalised", valNormal, qualQOS, false},
	{asdu.C_SE_TA_1, "C_SE_TA_1  set-point, normalised, time tag", valNormal, qualQOS, true},
	{asdu.C_SE_NB_1, "C_SE_NB_1  set-point, scaled", valScaled, qualQOS, false},
	{asdu.C_SE_TB_1, "C_SE_TB_1  set-point, scaled, time tag", valScaled, qualQOS, true},
	{asdu.C_SE_NC_1, "C_SE_NC_1  set-point, short float", valFloat, qualQOS, false},
	{asdu.C_SE_TC_1, "C_SE_TC_1  set-point, short float, time tag", valFloat, qualQOS, true},
	{asdu.C_BO_NA_1, "C_BO_NA_1  bit string of 32 bits", valBits, qualNone, false},
	{asdu.C_BO_TA_1, "C_BO_TA_1  bit string, time tag", valBits, qualNone, true},
}

// qocNames are the pulse durations a qualifier of command can carry.
var qocNames = []string{"no additional definition", "short pulse", "long pulse", "persistent output"}

var qocValues = []asdu.QOCQual{
	asdu.QOCNoAdditionalDefinition,
	asdu.QOCShortPulseDuration,
	asdu.QOCLongPulseDuration,
	asdu.QOCPersistentOutput,
}

// commandOp is one command, fully described before anything is sent.
type commandOp struct {
	Spec cmdSpec
	CA   uint16
	IOA  uint

	// OnOff carries single, double and step commands; Norm, Scaled, Float
	// and Bits the others.
	OnOff  bool
	Norm   asdu.Normalize
	Scaled int16
	Float  float32
	Bits   uint32

	Select bool // this transmission is the select half of select-before-execute
	Qoc    asdu.QOCQual
	Qos    asdu.QOSQual
}

// valueText names the value the way the dialog and the log show it.
func (op commandOp) valueText() string {
	switch op.Spec.value {
	case valOnOff:
		return onOffText(op.OnOff)
	case valStep:
		if op.OnOff {
			return "HIGHER"
		}
		return "LOWER"
	case valNormal:
		return fmt.Sprintf("%.5f", op.Norm.Float64())
	case valScaled:
		return strconv.Itoa(int(op.Scaled))
	case valFloat:
		return trimFloat(float64(op.Float))
	case valBits:
		return fmt.Sprintf("0x%08X", op.Bits)
	}
	return ""
}

// qualText names the qualifier in force.
func (op commandOp) qualText() string {
	switch op.Spec.qual {
	case qualQOC:
		return qocName(op.Qoc)
	case qualQOS:
		return fmt.Sprintf("QOS %d", uint(op.Qos))
	}
	return "none"
}

// phaseText names which half of a select-before-execute sequence this is.
func (op commandOp) phaseText() string {
	if op.Select {
		return "select"
	}
	return "execute"
}

// describe is the one line the confirmation, the feedback and the log all
// show. Nothing is sent that this line does not name.
func (op commandOp) describe() string {
	return fmt.Sprintf("%s ca=%d ioa=%d %s (%s, %s)",
		typeName(op.Spec.id), op.CA, op.IOA, op.valueText(),
		op.phaseText(), op.qualText())
}

// send puts the command on the wire.
func (op commandOp) send(c *cs104.Client) error {
	coa := asdu.CauseOfTransmission{Cause: asdu.Activation}
	ca := asdu.CommonAddr(op.CA)
	ioa := asdu.InfoObjAddr(op.IOA)
	qoc := asdu.QualifierOfCommand{Qual: op.Qoc, InSelect: op.Select}
	qos := asdu.QualifierOfSetpointCmd{Qual: op.Qos, InSelect: op.Select}
	now := time.Now()

	switch op.Spec.value {
	case valOnOff:
		switch op.Spec.id {
		case asdu.C_SC_NA_1, asdu.C_SC_TA_1:
			return asdu.SingleCmd(c, op.Spec.id, coa, ca, asdu.SingleCommandInfo{
				Ioa: ioa, Value: op.OnOff, Qoc: qoc, Time: now})
		default:
			v := asdu.DCOOff
			if op.OnOff {
				v = asdu.DCOOn
			}
			return asdu.DoubleCmd(c, op.Spec.id, coa, ca, asdu.DoubleCommandInfo{
				Ioa: ioa, Value: v, Qoc: qoc, Time: now})
		}

	case valStep:
		v := asdu.SCOStepDown
		if op.OnOff {
			v = asdu.SCOStepUP
		}
		return asdu.StepCmd(c, op.Spec.id, coa, ca, asdu.StepCommandInfo{
			Ioa: ioa, Value: v, Qoc: qoc, Time: now})

	case valNormal:
		return asdu.SetpointCmdNormal(c, op.Spec.id, coa, ca,
			asdu.SetpointCommandNormalInfo{Ioa: ioa, Value: op.Norm, Qos: qos, Time: now})

	case valScaled:
		return asdu.SetpointCmdScaled(c, op.Spec.id, coa, ca,
			asdu.SetpointCommandScaledInfo{Ioa: ioa, Value: op.Scaled, Qos: qos, Time: now})

	case valFloat:
		return asdu.SetpointCmdFloat(c, op.Spec.id, coa, ca,
			asdu.SetpointCommandFloatInfo{Ioa: ioa, Value: op.Float, Qos: qos, Time: now})

	case valBits:
		return asdu.BitsString32Cmd(c, op.Spec.id, coa, ca,
			asdu.BitsString32CommandInfo{Ioa: ioa, Value: op.Bits, Time: now})
	}
	return fmt.Errorf("no command defined")
}

// isCommandType reports whether a type identification is one this tool sends,
// so a mirrored reply can be matched to the command that caused it.
func isCommandType(t asdu.TypeID) bool {
	for _, s := range cmdSpecs {
		if s.id == t {
			return true
		}
	}
	switch t {
	case asdu.C_IC_NA_1, asdu.C_CI_NA_1, asdu.C_RD_NA_1, asdu.C_CS_NA_1,
		asdu.C_TS_NA_1, asdu.C_TS_TA_1, asdu.C_RP_NA_1, asdu.C_CD_NA_1:
		return true
	}
	return false
}

// parseValue reads a typed value out of what the operator entered.
func parseValue(kind valueKind, in string) (commandOp, error) {
	var op commandOp
	s := strings.TrimSpace(in)
	if s == "" {
		return op, fmt.Errorf("a value is required")
	}
	switch kind {
	case valNormal:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return op, fmt.Errorf("%q is not a number", s)
		}
		if f < -1 || f > 1 {
			return op, fmt.Errorf("a normalised value is in [-1, 1]")
		}
		op.Norm = asdu.Normalize(f * 32767)
	case valScaled:
		n, err := strconv.ParseInt(s, 0, 16)
		if err != nil {
			return op, fmt.Errorf("%q is not a 16 bit integer", s)
		}
		op.Scaled = int16(n)
	case valFloat:
		f, err := strconv.ParseFloat(s, 32)
		if err != nil {
			return op, fmt.Errorf("%q is not a number", s)
		}
		op.Float = float32(f)
	case valBits:
		v, err := strconv.ParseUint(s, 0, 32)
		if err != nil {
			return op, fmt.Errorf("%q is not a 32 bit value (try 0x00FF)", s)
		}
		op.Bits = uint32(v)
	}
	return op, nil
}

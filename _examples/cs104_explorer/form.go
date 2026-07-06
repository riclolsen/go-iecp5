package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/riclolsen/go-iecp5/asdu"
)

// cmdKind is one selectable entry in the command builder.
type cmdKind struct {
	name   string
	typeID asdu.TypeID
}

var cmdKinds = []cmdKind{
	{"Single command (C_SC_NA_1)", asdu.C_SC_NA_1},
	{"Double command (C_DC_NA_1)", asdu.C_DC_NA_1},
	{"Step command (C_RC_NA_1)", asdu.C_RC_NA_1},
	{"Setpoint float (C_SE_NC_1)", asdu.C_SE_NC_1},
	{"Setpoint scaled (C_SE_NB_1)", asdu.C_SE_NB_1},
	{"Setpoint normalized (C_SE_NA_1)", asdu.C_SE_NA_1},
	{"Read command (C_RD_NA_1)", asdu.C_RD_NA_1},
}

func parseBoolCmd(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "on", "true", "t", "yes", "y", "close":
		return true
	}
	return false
}

func parseDoubleCmd(s string) asdu.DoubleCommand {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "2", "on", "true", "close":
		return asdu.DCOOn
	default:
		return asdu.DCOOff
	}
}

func parseStepCmd(s string) asdu.StepCommand {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "2", "up", "higher", "+":
		return asdu.SCOStepUP
	default:
		return asdu.SCOStepDown
	}
}

func clampNorm(f float64) float64 {
	if f > 1 {
		return 1
	}
	if f < -1 {
		return -1
	}
	return f
}

// sendForm builds and queues the command described by the current form state.
func (m *model) sendForm() {
	if m.client == nil {
		m.appendLog("[app] not connected — press 'c' to connect first")
		return
	}
	kind := cmdKinds[m.formKind]

	ioaU, err := strconv.ParseUint(strings.TrimSpace(m.inIOA.Value()), 0, 32)
	if err != nil {
		m.appendLog("[app] invalid IOA: " + err.Error())
		return
	}
	ioa := asdu.InfoObjAddr(ioaU)
	ca := asdu.CommonAddr(m.ca)
	coa := asdu.CauseOfTransmission{Cause: asdu.Activation}
	valStr := m.inVal.Value()
	qualU, _ := strconv.ParseUint(strings.TrimSpace(m.inQual.Value()), 10, 8)
	qoc := asdu.QualifierOfCommand{Qual: asdu.QOCQual(qualU), InSelect: m.formSelect}
	qos := asdu.QualifierOfSetpointCmd{Qual: asdu.QOSQual(qualU), InSelect: m.formSelect}

	var sendErr error
	switch kind.typeID {
	case asdu.C_RD_NA_1:
		sendErr = m.client.ReadCmd(asdu.CauseOfTransmission{Cause: asdu.Request}, ca, ioa)
	case asdu.C_SC_NA_1:
		sendErr = asdu.SingleCmd(m.client, asdu.C_SC_NA_1, coa, ca,
			asdu.SingleCommandInfo{Ioa: ioa, Value: parseBoolCmd(valStr), Qoc: qoc})
	case asdu.C_DC_NA_1:
		sendErr = asdu.DoubleCmd(m.client, asdu.C_DC_NA_1, coa, ca,
			asdu.DoubleCommandInfo{Ioa: ioa, Value: parseDoubleCmd(valStr), Qoc: qoc})
	case asdu.C_RC_NA_1:
		sendErr = asdu.StepCmd(m.client, asdu.C_RC_NA_1, coa, ca,
			asdu.StepCommandInfo{Ioa: ioa, Value: parseStepCmd(valStr), Qoc: qoc})
	case asdu.C_SE_NC_1:
		f, perr := strconv.ParseFloat(strings.TrimSpace(valStr), 32)
		if perr != nil {
			m.appendLog("[app] invalid float value: " + perr.Error())
			return
		}
		sendErr = asdu.SetpointCmdFloat(m.client, asdu.C_SE_NC_1, coa, ca,
			asdu.SetpointCommandFloatInfo{Ioa: ioa, Value: float32(f), Qos: qos})
	case asdu.C_SE_NB_1:
		n, perr := strconv.ParseInt(strings.TrimSpace(valStr), 10, 16)
		if perr != nil {
			m.appendLog("[app] invalid scaled value: " + perr.Error())
			return
		}
		sendErr = asdu.SetpointCmdScaled(m.client, asdu.C_SE_NB_1, coa, ca,
			asdu.SetpointCommandScaledInfo{Ioa: ioa, Value: int16(n), Qos: qos})
	case asdu.C_SE_NA_1:
		f, perr := strconv.ParseFloat(strings.TrimSpace(valStr), 64)
		if perr != nil {
			m.appendLog("[app] invalid normalized value (expected -1..1): " + perr.Error())
			return
		}
		sendErr = asdu.SetpointCmdNormal(m.client, asdu.C_SE_NA_1, coa, ca,
			asdu.SetpointCommandNormalInfo{Ioa: ioa, Value: asdu.Normalize(int16(clampNorm(f) * 32767)), Qos: qos})
	}

	if sendErr != nil {
		m.appendLog("[tx] " + kind.name + " failed: " + sendErr.Error())
		return
	}
	m.appendLog(fmt.Sprintf("[tx] %s IOA=%d value=%q mode=%s -> queued",
		kind.name, ioaU, valStr, map[bool]string{true: "select", false: "execute"}[m.formSelect]))
}

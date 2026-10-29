package asdu

import (
	"net"
	"testing"
	"time"
)

type sinkConn struct{ sent []*ASDU }

func (c *sinkConn) Params() *Params          { return ParamsWide }
func (c *sinkConn) Send(a *ASDU) error       { c.sent = append(c.sent, a); return nil }
func (c *sinkConn) UnderlyingConn() net.Conn { return nil }

// The commands with time tag CP56Time2a (types 58..64) must survive a
// marshal/unmarshal round trip: found by the interoperability tests, where
// the confirmation of such a command was dropped on reception.
func TestTimeTaggedCommandsRoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 9, 15, 35, 12, 0, time.UTC)
	coa := CauseOfTransmission{Cause: Activation}
	c := &sinkConn{}
	cmds := []func() error{
		func() error {
			return SingleCmd(c, C_SC_TA_1, coa, 1, SingleCommandInfo{Ioa: 500, Value: true, Time: now})
		},
		func() error {
			return DoubleCmd(c, C_DC_TA_1, coa, 1, DoubleCommandInfo{Ioa: 501, Value: DCOOn, Time: now})
		},
		func() error {
			return StepCmd(c, C_RC_TA_1, coa, 1, StepCommandInfo{Ioa: 505, Value: SCOStepUP, Time: now})
		},
		func() error {
			return SetpointCmdNormal(c, C_SE_TA_1, coa, 1, SetpointCommandNormalInfo{Ioa: 503, Value: 5, Time: now})
		},
		func() error {
			return SetpointCmdScaled(c, C_SE_TB_1, coa, 1, SetpointCommandScaledInfo{Ioa: 504, Value: -7, Time: now})
		},
		func() error {
			return SetpointCmdFloat(c, C_SE_TC_1, coa, 1, SetpointCommandFloatInfo{Ioa: 502, Value: 49.5, Time: now})
		},
	}
	for _, send := range cmds {
		if err := send(); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range c.sent {
		raw, err := a.MarshalBinary()
		if err != nil {
			t.Fatalf("%v: %v", a.Type, err)
		}
		back := NewEmptyASDU(ParamsWide)
		if err := back.UnmarshalBinary(raw); err != nil {
			t.Errorf("%v does not decode: %v", a.Type, err)
		}
	}
}

// A station reports the result of a command with cause 11 (return information
// caused by a remote command), and a counter can be read with cause 5.
func TestMonitorCausesAccepted(t *testing.T) {
	c := &sinkConn{}
	now := time.Now()
	for _, cause := range []Cause{ReturnInfoRemote, ReturnInfoLocal} {
		coa := CauseOfTransmission{Cause: cause}
		for name, err := range map[string]error{
			"normal": MeasuredValueNormalCP56Time2a(c, coa, 1, MeasuredValueNormalInfo{Ioa: 1, Time: now}),
			"scaled": MeasuredValueScaledCP56Time2a(c, coa, 1, MeasuredValueScaledInfo{Ioa: 1, Time: now}),
			"float":  MeasuredValueFloatCP56Time2a(c, coa, 1, MeasuredValueFloatInfo{Ioa: 1, Time: now}),
		} {
			if err != nil {
				t.Errorf("M_ME %s CP56 with cause %v: %v", name, cause, err)
			}
		}
	}
	req := CauseOfTransmission{Cause: Request}
	if err := IntegratedTotals(c, false, req, 1, BinaryCounterReadingInfo{Ioa: 1}); err != nil {
		t.Errorf("M_IT_NA_1 with cause Request: %v", err)
	}
	if err := IntegratedTotalsCP56Time2a(c, req, 1, BinaryCounterReadingInfo{Ioa: 1, Time: now}); err != nil {
		t.Errorf("M_IT_TB_1 with cause Request: %v", err)
	}
}

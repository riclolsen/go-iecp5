package asdu

import (
	"errors"
	"math"
	"testing"
	"time"
)

func req(t TypeID, n byte, seq bool, ca CommonAddr, body ...byte) *ASDU {
	a := NewASDU(ParamsWide, Identifier{Type: t, Variable: VariableStruct{Number: n, IsSequence: seq},
		Coa: CauseOfTransmission{Cause: Activation}, CommonAddr: ca})
	a.InfoObj = append([]byte{1, 0, 0}, body...) // IOA 1
	return a
}

func f32(f float32) []byte {
	b := math.Float32bits(f)
	return []byte{byte(b), byte(b >> 8), byte(b >> 16), byte(b >> 24)}
}

func cp56(minute, hour, day, month byte, iv bool) []byte {
	m := minute
	if iv {
		m |= 0x80
	}
	return []byte{0, 0, m, hour, day, month, 26}
}

func TestValidateRequest(t *testing.T) {
	good56 := cp56(30, 12, 5, 6, false)
	cases := []struct {
		name string
		a    *ASDU
		want error
	}{
		{"plain single command", req(C_SC_NA_1, 1, false, 1, 0x01), nil},
		{"two objects in a command", req(C_SC_NA_1, 2, false, 1, 0x01, 0x01), ErrSingleObjectExpected},
		{"a sequence of commands", req(C_SE_NA_1, 1, true, 1, 0, 0, 0), ErrSingleObjectExpected},
		{"broadcast process command", req(C_SC_NA_1, 1, false, GlobalCommonAddr, 0x01), ErrBroadcastNotAllowed},
		{"broadcast setpoint", req(C_SE_NC_1, 1, false, GlobalCommonAddr, append(f32(1), 0)...), ErrBroadcastNotAllowed},
		{"broadcast interrogation is allowed", req(C_IC_NA_1, 1, false, GlobalCommonAddr, 20), nil},
		{"broadcast read is not", req(C_RD_NA_1, 1, false, GlobalCommonAddr), ErrBroadcastNotAllowed},
		{"station interrogation", req(C_IC_NA_1, 1, false, 1, 20), nil},
		{"reserved interrogation qualifier", req(C_IC_NA_1, 1, false, 1, 10), ErrQualifierReserved},
		{"private interrogation qualifier", req(C_IC_NA_1, 1, false, 1, 100), nil},
		{"counter request 5", req(C_CI_NA_1, 1, false, 1, 5), nil},
		{"counter request 9", req(C_CI_NA_1, 1, false, 1, 9), ErrQualifierReserved},
		{"reset process general", req(C_RP_NA_1, 1, false, 1, 1), nil},
		{"reset process reserved", req(C_RP_NA_1, 1, false, 1, 7), ErrQualifierReserved},
		{"double command on", req(C_DC_NA_1, 1, false, 1, 0x02), nil},
		{"double command state 0", req(C_DC_NA_1, 1, false, 1, 0x00), ErrCommandStateNotPermitted},
		{"double command state 3", req(C_DC_NA_1, 1, false, 1, 0x03), ErrCommandStateNotPermitted},
		{"step command state 3", req(C_RC_NA_1, 1, false, 1, 0x03), ErrCommandStateNotPermitted},
		{"reserved command qualifier", req(C_SC_NA_1, 1, false, 1, 0x01|(5<<2)), ErrQualifierReserved},
		{"float setpoint", req(C_SE_NC_1, 1, false, 1, append(f32(22.5), 0)...), nil},
		{"NaN setpoint", req(C_SE_NC_1, 1, false, 1, append(f32(float32(math.NaN())), 0)...), ErrFloatNotFinite},
		{"infinite parameter", req(P_ME_NC_1, 1, false, 1, append(f32(float32(math.Inf(1))), 0)...), ErrFloatNotFinite},
		{"time tagged command", req(C_SC_TA_1, 1, false, 1, append([]byte{0x01}, good56...)...), nil},
		{"time tag with IV", req(C_SC_TA_1, 1, false, 1, append([]byte{0x01}, cp56(30, 12, 5, 6, true)...)...), ErrTimeTagInvalid},
		{"time tag month 13", req(C_SC_TA_1, 1, false, 1, append([]byte{0x01}, cp56(30, 12, 5, 13, false)...)...), ErrTimeTagInvalid},
		{"clock sync bad hour", req(C_CS_NA_1, 1, false, 1, cp56(0, 31, 5, 6, false)...), ErrTimeTagInvalid},
		{"monitor type is not a request", req(M_SP_NA_1, 5, false, 1), nil},
	}
	for _, tc := range cases {
		err := ValidateRequest(tc.a)
		if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
		}
		if err != nil && !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: %v does not wrap ErrInvalidRequest", tc.name, err)
		}
	}
}

func TestClassification(t *testing.T) {
	for _, tc := range []struct {
		t                    TypeID
		mon, ctl, proc, prof bool
	}{
		{M_SP_NA_1, true, false, false, true},
		{M_SP_TA_1, true, false, false, false}, // CP24 tag: not in the 104 selection
		{M_SP_TB_1, true, false, false, true},
		{C_SC_NA_1, false, true, true, true},
		{C_TS_NA_1, false, true, false, false},
		{C_CD_NA_1, false, true, false, false},
		{C_TS_TA_1, false, true, false, true},
		{F_FR_NA_1, true, false, false, true},
		{F_SC_NA_1, false, true, false, true},
		{TypeID(200), false, false, false, false},
	} {
		if IsMonitorDirection(tc.t) != tc.mon || IsControlDirection(tc.t) != tc.ctl ||
			IsProcessCommand(tc.t) != tc.proc || InProfile104(tc.t) != tc.prof {
			t.Errorf("%v: mon=%v ctl=%v proc=%v prof=%v", tc.t, IsMonitorDirection(tc.t), IsControlDirection(tc.t),
				IsProcessCommand(tc.t), InProfile104(tc.t))
		}
	}
	if !IsPrivate(200) || IsPrivate(100) || !IsSecurity(S_CH_NA_1) || IsSecurity(C_IC_NA_1) {
		t.Error("IsPrivate/IsSecurity")
	}
}

func TestCommandTimeTagAndDescribe(t *testing.T) {
	a := req(C_SC_TA_1, 1, false, 1, append([]byte{0x81}, cp56(30, 12, 5, 6, false)...)...)
	tm, ok := CommandTimeTag(a)
	if !ok || tm.Minute() != 30 || tm.Hour() != 12 || tm.Year() != 2026 {
		t.Errorf("%v %v", tm, ok)
	}
	if _, ok := CommandTimeTag(req(C_SC_NA_1, 1, false, 1, 0x01)); ok {
		t.Error("untagged command has a time tag")
	}
	if got := DescribeCommand(a); got != "Select:QOCQual(0) value=true" && got == "" {
		t.Errorf("describe %q", got)
	}
	if ioa, ok := FirstIOA(a); !ok || ioa != 1 {
		t.Errorf("ioa %v %v", ioa, ok)
	}
	_ = time.Now
}

// Whatever arrives on the wire, validation must not panic.
func FuzzValidateRequest(f *testing.F) {
	f.Add(byte(C_SC_NA_1), byte(1), false, []byte{1, 0, 0, 1})
	f.Add(byte(C_SC_TA_1), byte(1), false, append([]byte{1, 0, 0, 1}, 0, 0, 30, 12, 5, 6, 26))
	f.Add(byte(C_SE_NC_1), byte(2), true, []byte{1, 0})
	f.Fuzz(func(t *testing.T, typ, n byte, seq bool, body []byte) {
		a := NewASDU(ParamsWide, Identifier{Type: TypeID(typ), Variable: VariableStruct{Number: n, IsSequence: seq},
			CommonAddr: 1})
		a.InfoObj = append(a.InfoObj, body...)
		_ = ValidateRequest(a)
		_, _ = CommandTimeTag(a)
		_ = DescribeCommand(a)
		_, _ = FirstIOA(a)
	})
}

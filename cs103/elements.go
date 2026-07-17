// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs103

import (
	"encoding/binary"
	"io"
	"math"
	"strings"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
)

// Standardized function types (FUN).
// See IEC 60870-5-103, subclass 7.2.5.1.
const (
	FunDistanceProtection    byte = 128
	FunOvercurrentProtection byte = 160
	FunTransformerDiff       byte = 176
	FunLineDiff              byte = 192
	FunGeneric               byte = 254
	FunGlobal                byte = 255
)

// A selection of standardized information numbers (INF) in monitor
// direction. See IEC 60870-5-103, subclass 7.2.5.2. The full semantics are
// device-specific; these constants cover the compatible range basics.
const (
	// System functions
	InfResetFCB     byte = 2
	InfResetCU      byte = 3
	InfStartRestart byte = 4
	InfPowerOn      byte = 5

	// Status indications
	InfAutoRecloserActive   byte = 16
	InfTeleprotectionActive byte = 17
	InfProtectionActive     byte = 18
	InfLEDReset             byte = 19
	InfMonitorBlocked       byte = 20
	InfTestMode             byte = 21
	InfLocalParameterSet    byte = 22
	InfCharacteristic1      byte = 23
	InfCharacteristic2      byte = 24
	InfCharacteristic3      byte = 25
	InfCharacteristic4      byte = 26
	InfAuxInput1            byte = 27
	InfAuxInput2            byte = 28
	InfAuxInput3            byte = 29
	InfAuxInput4            byte = 30

	// Supervision indications
	InfMeasurandSupervisionI   byte = 32
	InfMeasurandSupervisionV   byte = 33
	InfPhaseSeqSupervision     byte = 35
	InfTripCircuitSupervision  byte = 36
	InfBackupOperation         byte = 37
	InfVTFuseFailure           byte = 38
	InfTeleprotectionDisturbed byte = 39
	InfGroupWarning            byte = 46
	InfGroupAlarm              byte = 47

	// Earth fault indications
	InfEarthFaultL1      byte = 48
	InfEarthFaultL2      byte = 49
	InfEarthFaultL3      byte = 50
	InfEarthFaultForward byte = 51
	InfEarthFaultReverse byte = 52

	// Fault indications
	InfStartL1          byte = 64
	InfStartL2          byte = 65
	InfStartL3          byte = 66
	InfStartN           byte = 67
	InfGeneralTrip      byte = 68
	InfTripL1           byte = 69
	InfTripL2           byte = 70
	InfTripL3           byte = 71
	InfTripBackup       byte = 72
	InfFaultLocation    byte = 73
	InfFaultForward     byte = 74
	InfFaultReverse     byte = 75
	InfTeleprotSent     byte = 76
	InfTeleprotReceived byte = 77
	InfZone1            byte = 78
	InfZone2            byte = 79
	InfZone3            byte = 80
	InfZone4            byte = 81
	InfZone5            byte = 82
	InfZone6            byte = 83
	InfGeneralStart     byte = 84
	InfBreakerFailure   byte = 85

	// Auto-reclosure indications
	InfCBOnByAR     byte = 128
	InfCBOnByLongAR byte = 129
	InfARBlocked    byte = 130

	// Measurands
	InfMeasurandI          byte = 144 // I
	InfMeasurandIV         byte = 145 // I, V
	InfMeasurandIVPQ       byte = 146 // I, V, P, Q
	InfMeasurandINVEN      byte = 147 // IN, VEN
	InfMeasurandIL123VL123 byte = 148 // IL1..3, VL1..3, P, Q, f
)

// DPI is the 103 double-point information (2 bits).
type DPI byte

// DPI values.
const (
	DPITransient DPI = 0 // transient / not used
	DPIOff       DPI = 1
	DPIOn        DPI = 2
	DPIUnknown   DPI = 3
)

func (d DPI) String() string {
	switch d {
	case DPIOff:
		return "Off"
	case DPIOn:
		return "On"
	case DPITransient:
		return "Transient"
	default:
		return "Unknown"
	}
}

// DCO is the double command of the general command ASDU (type 20).
type DCO byte

// DCO values.
const (
	DCOOff DCO = 1
	DCOOn  DCO = 2
)

func (d DCO) String() string {
	switch d {
	case DCOOff:
		return "Off"
	case DCOOn:
		return "On"
	default:
		return "Invalid"
	}
}

// Measurand is one 103 measurand: a signed 13-bit value with overflow and
// error flags. See IEC 60870-5-103, subclass 7.2.6.8.
//
//	bit 0: OV overflow, bit 1: ER error/invalid, bit 2: reserved,
//	bits 3..15: MVAL, signed, -4096..4095
type Measurand struct {
	Val      int16 // -4096..4095
	Overflow bool
	Invalid  bool // ER bit set
}

// ParseMeasurand decodes a little-endian measurand octet pair.
func ParseMeasurand(u uint16) Measurand {
	return Measurand{
		Val:      int16(u) >> 3, // arithmetic shift keeps the sign
		Overflow: u&0x01 != 0,
		Invalid:  u&0x02 != 0,
	}
}

// Value encodes the measurand to its octet pair value.
func (m Measurand) Value() uint16 {
	u := uint16(m.Val) << 3
	if m.Overflow {
		u |= 0x01
	}
	if m.Invalid {
		u |= 0x02
	}
	return u
}

// Float64 returns the measurand as a fraction of full scale in [-1, 1):
// the rated value corresponds to 1/1.2 or 1/2.4 of full scale depending on
// the device parameterization.
func (m Measurand) Float64() float64 {
	return float64(m.Val) / 4096.0
}

// CP32Time2a encodes a 4-octet binary time (milliseconds, minutes, hours
// with summer-time flag). See IEC 60870-5-4.
func CP32Time2a(t time.Time, loc *time.Location) []byte {
	if loc == nil {
		loc = time.UTC
	}
	ts := t.In(loc)
	msec := ts.Nanosecond()/int(time.Millisecond) + ts.Second()*1000
	return []byte{byte(msec), byte(msec >> 8), byte(ts.Minute()), byte(ts.Hour())}
}

// ParseCP32Time2a decodes a 4-octet binary time. The date is taken from the
// local clock; a time-of-day more than five minutes ahead of the current
// time is interpreted as belonging to the previous day. An invalid (IV) tag
// or short input decodes to the zero time.
func ParseCP32Time2a(b []byte, loc *time.Location) time.Time {
	if len(b) < 4 || b[2]&0x80 != 0 { // IV bit
		return time.Time{}
	}
	x := int(binary.LittleEndian.Uint16(b))
	msec := x % 1000
	sec := x / 1000
	min := int(b[2] & 0x3f)
	hour := int(b[3] & 0x1f)

	if loc == nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	year, month, day := now.Date()
	val := time.Date(year, month, day, hour, min, sec, msec*int(time.Millisecond), loc)
	if val.After(now.Add(5 * time.Minute)) {
		val = val.AddDate(0, 0, -1)
	}
	return val
}

// --- decoded information object structs and getters ---

// TimeTaggedInfo is the content of ASDU 1 (time-tagged message) and ASDU 2
// (time-tagged message with relative time).
type TimeTaggedInfo struct {
	Fun byte
	Inf byte
	Dpi DPI
	// RelativeTime (RET) and FaultNumber (FAN): ASDU 2 only.
	RelativeTime uint16
	FaultNumber  uint16
	Time         time.Time
	// Sin is the supplementary information; for command acknowledgements
	// (cause 20/21) it carries the RII of the original command.
	Sin byte
}

// GetTimeTagged decodes ASDU 1 or ASDU 2.
func (a *ASDU) GetTimeTagged() (TimeTaggedInfo, error) {
	var info TimeTaggedInfo
	need := 8 // FUN INF DPI CP32(4) SIN
	rel := a.Type == TypTimeTaggedRel
	if rel {
		need = 12 // + RET(2) FAN(2)
	} else if a.Type != TypTimeTagged {
		return info, ErrTypeIDNotMatch
	}
	if len(a.InfoObj) < need {
		return info, io.ErrUnexpectedEOF
	}
	b := a.InfoObj
	info.Fun, info.Inf = b[0], b[1]
	info.Dpi = DPI(b[2] & 0x03)
	b = b[3:]
	if rel {
		info.RelativeTime = binary.LittleEndian.Uint16(b)
		info.FaultNumber = binary.LittleEndian.Uint16(b[2:])
		b = b[4:]
	}
	info.Time = ParseCP32Time2a(b, time.UTC)
	info.Sin = b[4]
	return info, nil
}

// MeasurandsInfo is the content of ASDU 3 (measurands I) and ASDU 9
// (measurands II): one information object with 1..n measurands.
type MeasurandsInfo struct {
	Fun    byte
	Inf    byte
	Values []Measurand
}

// GetMeasurands decodes ASDU 3 or ASDU 9.
func (a *ASDU) GetMeasurands() (MeasurandsInfo, error) {
	var info MeasurandsInfo
	if a.Type != TypMeasurandsI && a.Type != TypMeasurandsII {
		return info, ErrTypeIDNotMatch
	}
	n := int(a.Variable.Number)
	if len(a.InfoObj) < 2+2*n {
		return info, io.ErrUnexpectedEOF
	}
	info.Fun, info.Inf = a.InfoObj[0], a.InfoObj[1]
	for i := 0; i < n; i++ {
		u := binary.LittleEndian.Uint16(a.InfoObj[2+2*i:])
		info.Values = append(info.Values, ParseMeasurand(u))
	}
	return info, nil
}

// TimeTaggedMeasurandsInfo is the content of ASDU 4 (time-tagged measurands
// with relative time): the short-circuit location as a short float.
type TimeTaggedMeasurandsInfo struct {
	Fun          byte
	Inf          byte
	Value        float32 // SCL, short-circuit location
	RelativeTime uint16
	FaultNumber  uint16
	Time         time.Time
}

// GetTimeTaggedMeasurands decodes ASDU 4.
func (a *ASDU) GetTimeTaggedMeasurands() (TimeTaggedMeasurandsInfo, error) {
	var info TimeTaggedMeasurandsInfo
	if a.Type != TypTimeTaggedMeasurands {
		return info, ErrTypeIDNotMatch
	}
	if len(a.InfoObj) < 14 { // FUN INF SCL(4) RET(2) FAN(2) CP32(4)
		return info, io.ErrUnexpectedEOF
	}
	b := a.InfoObj
	info.Fun, info.Inf = b[0], b[1]
	info.Value = math.Float32frombits(binary.LittleEndian.Uint32(b[2:]))
	info.RelativeTime = binary.LittleEndian.Uint16(b[6:])
	info.FaultNumber = binary.LittleEndian.Uint16(b[8:])
	info.Time = ParseCP32Time2a(b[10:], time.UTC)
	return info, nil
}

// IdentificationInfo is the content of ASDU 5 (identification message).
type IdentificationInfo struct {
	Fun   byte
	Inf   byte
	Col   byte   // compatibility level
	ASCII string // 8 ASCII characters: manufacturer and product designation
	Extra []byte // remaining free-assignable identification octets
}

// GetIdentification decodes ASDU 5.
func (a *ASDU) GetIdentification() (IdentificationInfo, error) {
	var info IdentificationInfo
	if a.Type != TypIdentification {
		return info, ErrTypeIDNotMatch
	}
	if len(a.InfoObj) < 3 {
		return info, io.ErrUnexpectedEOF
	}
	info.Fun, info.Inf, info.Col = a.InfoObj[0], a.InfoObj[1], a.InfoObj[2]
	rest := a.InfoObj[3:]
	n := len(rest)
	if n > 8 {
		n = 8
	}
	info.ASCII = strings.TrimRight(string(rest[:n]), "\x00 ")
	if len(rest) > 8 {
		info.Extra = append([]byte(nil), rest[8:]...)
	}
	return info, nil
}

// GetTimeSync decodes ASDU 6 (time synchronization, either direction).
func (a *ASDU) GetTimeSync() (time.Time, error) {
	if a.Type != TypTimeSync {
		return time.Time{}, ErrTypeIDNotMatch
	}
	if len(a.InfoObj) < 9 { // FUN INF CP56(7)
		return time.Time{}, io.ErrUnexpectedEOF
	}
	return asdu.ParseCP56Time2a(a.InfoObj[2:], time.UTC), nil
}

// GetGITermination decodes ASDU 8 and returns the scan number (SCN).
func (a *ASDU) GetGITermination() (byte, error) {
	if a.Type != TypGITermination {
		return 0, ErrTypeIDNotMatch
	}
	if len(a.InfoObj) < 3 { // FUN INF SCN
		return 0, io.ErrUnexpectedEOF
	}
	return a.InfoObj[2], nil
}

// GetGeneralInterrogation decodes a control-direction ASDU 7 and returns the
// scan number (SCN).
func (a *ASDU) GetGeneralInterrogation() (byte, error) {
	if a.Type != TypGeneralInterrogation {
		return 0, ErrTypeIDNotMatch
	}
	if len(a.InfoObj) < 3 { // FUN INF SCN
		return 0, io.ErrUnexpectedEOF
	}
	return a.InfoObj[2], nil
}

// GeneralCommandInfo is the content of ASDU 20 (general command).
type GeneralCommandInfo struct {
	Fun byte
	Inf byte
	Dco DCO
	Rii byte // return information identifier
}

// GetGeneralCommand decodes ASDU 20.
func (a *ASDU) GetGeneralCommand() (GeneralCommandInfo, error) {
	var info GeneralCommandInfo
	if a.Type != TypGeneralCommand {
		return info, ErrTypeIDNotMatch
	}
	if len(a.InfoObj) < 4 { // FUN INF DCO RII
		return info, io.ErrUnexpectedEOF
	}
	info.Fun, info.Inf = a.InfoObj[0], a.InfoObj[1]
	info.Dco = DCO(a.InfoObj[2] & 0x03)
	info.Rii = a.InfoObj[3]
	return info, nil
}

// --- control-direction ASDU builders ---

func vsqOne() asdu.VariableStruct { return asdu.VariableStruct{Number: 1, IsSequence: true} }

// NewTimeSyncASDU builds a control-direction ASDU 6 (time synchronization).
func NewTimeSyncASDU(commonAddr byte, t time.Time) *ASDU {
	a := NewASDU(TypTimeSync, vsqOne(), CauseTimeSync, commonAddr)
	a.AppendBytes(FunGlobal, 0)
	a.AppendBytes(asdu.CP56Time2a(t, time.UTC)...)
	return a
}

// NewGeneralInterrogationASDU builds a control-direction ASDU 7
// (initiation of general interrogation) with the given scan number.
func NewGeneralInterrogationASDU(commonAddr, scn byte) *ASDU {
	a := NewASDU(TypGeneralInterrogation, vsqOne(), CauseGI, commonAddr)
	a.AppendBytes(FunGlobal, 0, scn)
	return a
}

// NewGeneralCommandASDU builds a control-direction ASDU 20 (general command).
func NewGeneralCommandASDU(commonAddr, fun, inf byte, dco DCO, rii byte) *ASDU {
	a := NewASDU(TypGeneralCommand, vsqOne(), CauseGeneralCommand, commonAddr)
	a.AppendBytes(fun, inf, byte(dco), rii)
	return a
}

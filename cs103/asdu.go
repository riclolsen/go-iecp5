// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

// Package cs103 implements the primary station (master) side of
// IEC 60870-5-103, the companion standard for the informative interface of
// protection equipment. The link layer is FT1.2 unbalanced (shared with
// IEC 60870-5-101); the application layer uses the 103-specific ASDU layout:
//
//	| type ID | VSQ | cause | common address | FUN | INF | elements... |
//	|    1    |  1  |   1   |       1        |  1  |  1  |     n       |
//
// Information objects are addressed by FUNCTION TYPE (FUN) and INFORMATION
// NUMBER (INF) instead of the 101/104 information object address.
package cs103

import (
	"fmt"
	"io"

	"github.com/riclolsen/go-iecp5/asdu"
)

// ASDUSizeMax is the maximum ASDU length carried in one FT1.2 frame
// (frame length field max 255 minus control and link address octet).
const ASDUSizeMax = 253

// identifierSize is the fixed ASDU identifier size of IEC 60870-5-103:
// type ID + VSQ + cause of transmission + common address, one octet each.
const identifierSize = 4

// TypeID is the ASDU type identification of IEC 60870-5-103.
// See IEC 60870-5-103, subclass 7.2.1.
type TypeID uint8

// Compatible-range type identifications.
const (
	// Monitor direction
	TypTimeTagged            TypeID = 1 // time-tagged message
	TypTimeTaggedRel         TypeID = 2 // time-tagged message with relative time
	TypMeasurandsI           TypeID = 3 // measurands I (up to 4 values)
	TypTimeTaggedMeasurands  TypeID = 4 // time-tagged measurands with relative time
	TypIdentification        TypeID = 5 // identification message
	TypTimeSync              TypeID = 6 // time synchronization (both directions)
	TypGeneralInterrogation  TypeID = 7 // general interrogation (control direction)
	TypGITermination         TypeID = 8 // termination of general interrogation
	TypMeasurandsII          TypeID = 9 // measurands II (up to 9 values)
	TypGenericData           TypeID = 10
	TypGenericIdentification TypeID = 11

	// Control direction
	TypGeneralCommand TypeID = 20 // general command
	TypGenericCommand TypeID = 21 // generic command

	// Disturbance data transfer (not implemented by this package)
	TypListOfRecordedDisturbances    TypeID = 23
	TypOrderDisturbanceTransmission  TypeID = 24
	TypAckDisturbanceTransmission    TypeID = 25
	TypReadyDisturbanceData          TypeID = 26
	TypReadyDisturbanceChannel       TypeID = 27
	TypReadyDisturbanceTags          TypeID = 28
	TypTransmissionDisturbanceTags   TypeID = 29
	TypTransmissionDisturbanceValues TypeID = 30
	TypEndDisturbanceTransmission    TypeID = 31
)

func (t TypeID) String() string {
	switch t {
	case TypTimeTagged:
		return "TimeTagged<1>"
	case TypTimeTaggedRel:
		return "TimeTaggedRel<2>"
	case TypMeasurandsI:
		return "MeasurandsI<3>"
	case TypTimeTaggedMeasurands:
		return "TimeTaggedMeasurands<4>"
	case TypIdentification:
		return "Identification<5>"
	case TypTimeSync:
		return "TimeSync<6>"
	case TypGeneralInterrogation:
		return "GeneralInterrogation<7>"
	case TypGITermination:
		return "GITermination<8>"
	case TypMeasurandsII:
		return "MeasurandsII<9>"
	case TypGenericData:
		return "GenericData<10>"
	case TypGenericIdentification:
		return "GenericIdentification<11>"
	case TypGeneralCommand:
		return "GeneralCommand<20>"
	case TypGenericCommand:
		return "GenericCommand<21>"
	default:
		return fmt.Sprintf("TypeID<%d>", byte(t))
	}
}

// Cause is the IEC 60870-5-103 cause of transmission (one octet).
// See IEC 60870-5-103, subclass 7.2.3.
type Cause byte

// Cause of transmission values. Values 8, 9, 20, 31, 40 and 42 exist in
// both directions with related meanings.
const (
	// Monitor direction
	CauseSpontaneous     Cause = 1
	CauseCyclic          Cause = 2
	CauseResetFCB        Cause = 3 // reset frame count bit
	CauseResetCU         Cause = 4 // reset communication unit
	CauseStartRestart    Cause = 5
	CausePowerOn         Cause = 6
	CauseTestMode        Cause = 7
	CauseTimeSync        Cause = 8 // both directions
	CauseGI              Cause = 9 // control: initiation; monitor: GI reply
	CauseGITermination   Cause = 10
	CauseLocalOperation  Cause = 11
	CauseRemoteOperation Cause = 12
	CauseCommandAckPos   Cause = 20 // monitor; control direction: general command
	CauseCommandAckNeg   Cause = 21
	CauseDisturbanceData Cause = 31 // both directions

	CauseGenericWriteAckPos Cause = 40 // control direction: generic write
	CauseGenericWriteAckNeg Cause = 41
	CauseGenericReadValid   Cause = 42 // control direction: generic read
	CauseGenericReadInvalid Cause = 43
	CauseGenericWriteConf   Cause = 44

	// Control direction aliases
	CauseGeneralCommand Cause = 20
	CauseGenericWrite   Cause = 40
	CauseGenericRead    Cause = 42
)

func (c Cause) String() string {
	switch c {
	case CauseSpontaneous:
		return "Spontaneous"
	case CauseCyclic:
		return "Cyclic"
	case CauseResetFCB:
		return "ResetFCB"
	case CauseResetCU:
		return "ResetCU"
	case CauseStartRestart:
		return "StartRestart"
	case CausePowerOn:
		return "PowerOn"
	case CauseTestMode:
		return "TestMode"
	case CauseTimeSync:
		return "TimeSync"
	case CauseGI:
		return "GeneralInterrogation"
	case CauseGITermination:
		return "GITermination"
	case CauseLocalOperation:
		return "LocalOperation"
	case CauseRemoteOperation:
		return "RemoteOperation"
	case CauseCommandAckPos:
		return "CommandAckPos/GeneralCommand"
	case CauseCommandAckNeg:
		return "CommandAckNeg"
	case CauseDisturbanceData:
		return "DisturbanceData"
	case CauseGenericWriteAckPos:
		return "GenericWriteAckPos/GenericWrite"
	case CauseGenericWriteAckNeg:
		return "GenericWriteAckNeg"
	case CauseGenericReadValid:
		return "GenericReadValid/GenericRead"
	case CauseGenericReadInvalid:
		return "GenericReadInvalid"
	case CauseGenericWriteConf:
		return "GenericWriteConf"
	default:
		return fmt.Sprintf("Cause<%d>", byte(c))
	}
}

// GlobalCommonAddr is the broadcast common address / link address of
// IEC 60870-5-103 (one octet addressing).
const GlobalCommonAddr byte = 255

// ASDU is one IEC 60870-5-103 application message.
type ASDU struct {
	Type       TypeID
	Variable   asdu.VariableStruct // SQ bit + number of elements
	Coa        Cause
	CommonAddr byte   // common address of ASDU (conventionally = link address)
	InfoObj    []byte // information object bytes, starting at FUN
}

// NewASDU builds an ASDU with the given identifier and an empty payload.
func NewASDU(typ TypeID, vsq asdu.VariableStruct, coa Cause, commonAddr byte) *ASDU {
	return &ASDU{Type: typ, Variable: vsq, Coa: coa, CommonAddr: commonAddr}
}

// AppendBytes appends raw information object bytes.
func (a *ASDU) AppendBytes(b ...byte) *ASDU {
	a.InfoObj = append(a.InfoObj, b...)
	return a
}

// Fun returns the function type octet of the (first) information object.
func (a *ASDU) Fun() byte {
	if len(a.InfoObj) > 0 {
		return a.InfoObj[0]
	}
	return 0
}

// Inf returns the information number octet of the (first) information object.
func (a *ASDU) Inf() byte {
	if len(a.InfoObj) > 1 {
		return a.InfoObj[1]
	}
	return 0
}

// MarshalBinary honors the encoding.BinaryMarshaler interface.
func (a *ASDU) MarshalBinary() ([]byte, error) {
	if a.Type == 0 {
		return nil, ErrTypeIDZero
	}
	if a.Coa == 0 {
		return nil, ErrCauseZero
	}
	if identifierSize+len(a.InfoObj) > ASDUSizeMax {
		return nil, ErrLengthOutOfRange
	}
	raw := make([]byte, 0, identifierSize+len(a.InfoObj))
	raw = append(raw, byte(a.Type), a.Variable.Value(), byte(a.Coa), a.CommonAddr)
	raw = append(raw, a.InfoObj...)
	return raw, nil
}

// UnmarshalBinary honors the encoding.BinaryUnmarshaler interface.
func (a *ASDU) UnmarshalBinary(raw []byte) error {
	if len(raw) < identifierSize {
		return io.EOF
	}
	a.Type = TypeID(raw[0])
	a.Variable = asdu.ParseVariableStruct(raw[1])
	a.Coa = Cause(raw[2])
	a.CommonAddr = raw[3]
	a.InfoObj = append([]byte(nil), raw[identifierSize:]...)
	return nil
}

// String returns a compact human-readable description.
func (a *ASDU) String() string {
	if a == nil {
		return "<nil>"
	}
	s := fmt.Sprintf("%s %s @%d VSQ<%d>", a.Type, a.Coa, a.CommonAddr, a.Variable.Number)
	if len(a.InfoObj) >= 2 {
		s += fmt.Sprintf(" FUN=%d INF=%d", a.InfoObj[0], a.InfoObj[1])
	}
	switch a.Type {
	case TypTimeTagged, TypTimeTaggedRel:
		if info, err := a.GetTimeTagged(); err == nil {
			s += fmt.Sprintf(" DPI=%s", info.Dpi)
			if !info.Time.IsZero() {
				s += " @" + info.Time.Format("15:04:05.000")
			}
		}
	case TypMeasurandsI, TypMeasurandsII:
		if info, err := a.GetMeasurands(); err == nil {
			s += " ["
			for i, m := range info.Values {
				if i > 0 {
					s += ", "
				}
				s += fmt.Sprintf("%.4f", m.Float64())
			}
			s += "]"
		}
	case TypIdentification:
		if id, err := a.GetIdentification(); err == nil {
			s += fmt.Sprintf(" COL=%d %q", id.Col, id.ASCII)
		}
	case TypGITermination:
		if scn, err := a.GetGITermination(); err == nil {
			s += fmt.Sprintf(" SCN=%d", scn)
		}
	case TypGeneralCommand:
		if cmd, err := a.GetGeneralCommand(); err == nil {
			s += fmt.Sprintf(" DCO=%s RII=%d", cmd.Dco, cmd.Rii)
		}
	default:
		s += fmt.Sprintf(" payload=%dB", len(a.InfoObj))
	}
	return s
}

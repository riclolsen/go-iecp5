// Copyright 2020 thinkgos (thinkgo@aliyun.com).  All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package asdu

import (
	"time"
)

// Application service data unit for process information in the monitoring direction

// checkValid check common parameter of request is valid
func checkValid(c Connect, typeID TypeID, isSequence bool, infosLen int) error {
	if infosLen == 0 {
		return ErrNotAnyObjInfo
	}
	objSize, err := GetInfoObjSize(typeID)
	if err != nil {
		return err
	}
	param := c.Params()
	if err := param.Valid(); err != nil {
		return err
	}

	var asduLen int
	if isSequence {
		asduLen = param.IdentifierSize() + infosLen*objSize + param.InfoObjAddrSize
	} else {
		asduLen = param.IdentifierSize() + infosLen*(objSize+param.InfoObjAddrSize)
	}

	if asduLen > ASDUSizeMax {
		return ErrLengthOutOfRange
	}
	return nil
}

// SinglePointInfo the measured value attributes.
type SinglePointInfo struct {
	Ioa InfoObjAddr
	// value of single point
	Value bool
	// Quality descriptor asdu.OK means no remarks.
	Qds QualityDescriptor
	// the type does not include timing will ignore
	Time time.Time
}

// single sends a type identification [M_SP_NA_1], [M_SP_TA_1] or [M_SP_TB_1].单点信息
// [M_SP_NA_1] See companion standard 101,subclass 7.3.1.1
// [M_SP_TA_1] See companion standard 101,subclass 7.3.1.2
// [M_SP_TB_1] See companion standard 101,subclass 7.3.1.22
func single(c Connect, typeID TypeID, isSequence bool, coa CauseOfTransmission, ca CommonAddr, infos ...SinglePointInfo) error {
	if err := checkValid(c, typeID, isSequence, len(infos)); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		typeID,
		VariableStruct{IsSequence: isSequence},
		coa,
		0,
		ca,
	})
	if err := u.SetVariableNumber(len(infos)); err != nil {
		return err
	}
	once := false
	for _, v := range infos {
		if !isSequence || !once {
			once = true
			if err := u.AppendInfoObjAddr(v.Ioa); err != nil {
				return err
			}
		}

		value := byte(0)
		if v.Value {
			value = 0x01
		}
		u.AppendBytes(value | byte(v.Qds&0xf0))
		switch typeID {
		case M_SP_NA_1:
		case M_SP_TA_1:
			u.AppendBytes(CP24Time2a(v.Time, u.InfoObjTimeZone)...)
		case M_SP_TB_1:
			u.AppendBytes(CP56Time2a(v.Time, u.InfoObjTimeZone)...)
		default:
			return ErrTypeIDNotMatch
		}
	}
	return c.Send(u)
}

// Single sends a type identification [M_SP_NA_1].Single point information without time stamp
// [M_SP_NA_1] See companion standard 101,subclass 7.3.1.1
// The reason for delivery (coa) is used for
// Monitoring direction:
// <2> := background scan
// <3> := burst (spontaneous)
// <5> := was requested
// <11> := Return information caused by remote commands
// <12> := Return messages caused by local commands
// <20> := answer station call
// <21> := Respond to Group 1 call
// to
// <36> := Respond to Group 16 Call
func Single(c Connect, isSequence bool, coa CauseOfTransmission, ca CommonAddr, infos ...SinglePointInfo) error {
	if !(coa.Cause == Background || coa.Cause == Spontaneous || coa.Cause == Request ||
		coa.Cause == ReturnInfoRemote || coa.Cause == ReturnInfoLocal ||
		(coa.Cause >= InterrogatedByStation && coa.Cause <= InterrogatedByGroup16)) {
		return ErrCmdCause
	}
	return single(c, M_SP_NA_1, isSequence, coa, ca, infos...)
}

// SingleCP24Time2a sends a type identification [M_SP_TA_1],Single point information with time stamp CP24Time2a, only (SQ = 0) a single set of information elements
// [M_SP_TA_1] See companion standard 101,subclass 7.3.1.2
// The reason for delivery (coa) is used for
// Monitoring direction:
// <3> := burst (spontaneous)
// <5> := was requested
// <11> := Return information caused by remote commands
// <12> := Return messages caused by local commands
func SingleCP24Time2a(c Connect, coa CauseOfTransmission, ca CommonAddr, infos ...SinglePointInfo) error {
	if !(coa.Cause == Spontaneous || coa.Cause == Request ||
		coa.Cause == ReturnInfoRemote || coa.Cause == ReturnInfoLocal) {
		return ErrCmdCause
	}
	return single(c, M_SP_TA_1, false, coa, ca, infos...)
}

// SingleCP56Time2a sends a type identification [M_SP_TB_1]. Single point information with time stamp CP56Time2a, only (SQ = 0) a single information element set
// [M_SP_TB_1] See companion standard 101, subclass 7.3.1.22
// send reason (coa) for
// monitor direction:
// <3> := burst (spontaneous)
// <5> := requested
// <11> := Return information caused by remote command
// <12> := Return information caused by local commands
func SingleCP56Time2a(c Connect, coa CauseOfTransmission, ca CommonAddr, infos ...SinglePointInfo) error {
	if !(coa.Cause == Spontaneous || coa.Cause == Request ||
		coa.Cause == ReturnInfoRemote || coa.Cause == ReturnInfoLocal) {
		return ErrCmdCause
	}
	return single(c, M_SP_TB_1, false, coa, ca, infos...)
}

// DoublePointInfo the measured value attributes.
type DoublePointInfo struct {
	Ioa   InfoObjAddr
	Value DoublePoint
	// Quality descriptor asdu.QDSGood means no remarks.
	Qds QualityDescriptor
	// the type does not include timing will ignore
	Time time.Time
}

// double sends a type identification [M_DP_NA_1], [M_DP_TA_1] or [M_DP_TB_1]. double point information
// [M_DP_NA_1] See companion standard 101,subclass 7.3.1.3
// [M_DP_TA_1] See companion standard 101,subclass 7.3.1.4
// [M_DP_TB_1] See companion standard 101,subclass 7.3.1.23
func double(c Connect, typeID TypeID, isSequence bool, coa CauseOfTransmission, ca CommonAddr, infos ...DoublePointInfo) error {
	if err := checkValid(c, typeID, isSequence, len(infos)); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		typeID,
		VariableStruct{IsSequence: isSequence},
		coa,
		0,
		ca,
	})
	if err := u.SetVariableNumber(len(infos)); err != nil {
		return err
	}
	once := false
	for _, v := range infos {
		if !isSequence || !once {
			once = true
			if err := u.AppendInfoObjAddr(v.Ioa); err != nil {
				return err
			}
		}

		u.AppendBytes(byte(v.Value&0x03) | byte(v.Qds&0xf0))
		switch typeID {
		case M_DP_NA_1:
		case M_DP_TA_1:
			u.AppendBytes(CP24Time2a(v.Time, u.InfoObjTimeZone)...)
		case M_DP_TB_1:
			u.AppendBytes(CP56Time2a(v.Time, u.InfoObjTimeZone)...)
		default:
			return ErrTypeIDNotMatch
		}
	}
	return c.Send(u)
}

// Double sends a type identification [M_DP_NA_1]. Double point information
// [M_DP_NA_1] See companion standard 101, subclass 7.3.1.3
// send reason (coa) for
// monitor direction:
// <2> := background scan
// <3> := burst (spontaneous)
// <5> := requested
// <11> := Return information caused by remote command
// <12> := Return information caused by local commands
// <20> := Respond to station calls
// <21> := Respond to the first group call
// to
// <36> := Respond to the 16th group call
func Double(c Connect, isSequence bool, coa CauseOfTransmission, ca CommonAddr, infos ...DoublePointInfo) error {
	if !(coa.Cause == Background || coa.Cause == Spontaneous || coa.Cause == Request ||
		coa.Cause == ReturnInfoRemote || coa.Cause == ReturnInfoLocal ||
		(coa.Cause >= InterrogatedByStation && coa.Cause <= InterrogatedByGroup16)) {
		return ErrCmdCause
	}
	return double(c, M_DP_NA_1, isSequence, coa, ca, infos...)
}

// DoubleCP24Time2a sends a type identification [M_DP_TA_1]. With CP24Time2a double point information, only (SQ = 0) a single information element set
// [M_DP_TA_1] See companion standard 101, subclass 7.3.1.4
// send reason (coa) for
// monitor direction:
// <3> := burst (spontaneous)
// <5> := requested
// <11> := Return information caused by remote command
// <12> := Return information caused by local commands
func DoubleCP24Time2a(c Connect, coa CauseOfTransmission, ca CommonAddr, infos ...DoublePointInfo) error {
	if !(coa.Cause == Spontaneous || coa.Cause == Request ||
		coa.Cause == ReturnInfoRemote || coa.Cause == ReturnInfoLocal) {
		return ErrCmdCause
	}
	return double(c, M_DP_TA_1, false, coa, ca, infos...)
}

// DoubleCP56Time2a sends a type identification [M_DP_TB_1]. Double point information with CP56Time2a, only (SQ = 0) a single information element set
// [M_DP_TB_1] See companion standard 101, subclass 7.3.1.23
// send reason (coa) for
// monitor direction:
// <3> := burst (spontaneous)
// <5> := requested
// <11> := Return information caused by remote command
// <12> := Return information caused by local commands
func DoubleCP56Time2a(c Connect, coa CauseOfTransmission, ca CommonAddr, infos ...DoublePointInfo) error {
	if !(coa.Cause == Spontaneous || coa.Cause == Request ||
		coa.Cause == ReturnInfoRemote || coa.Cause == ReturnInfoLocal) {
		return ErrCmdCause
	}
	return double(c, M_DP_TB_1, false, coa, ca, infos...)
}

// StepPositionInfo the measured value attributes.
type StepPositionInfo struct {
	Ioa   InfoObjAddr
	Value StepPosition
	// Quality descriptor asdu.GOOD means no remarks.
	Qds QualityDescriptor
	// the type does not include timing will ignore
	Time time.Time
}

// step sends a type identification [M_ST_NA_1], [M_ST_TA_1] or [M_ST_TB_1]. step location information
// [M_ST_NA_1] See companion standard 101, subclass 7.3.1.5
// [M_ST_TA_1] See companion standard 101, subclass 7.3.1.6
// [M_ST_TB_1] See companion standard 101, subclass 7.3.1.24
func step(c Connect, typeID TypeID, isSequence bool, coa CauseOfTransmission, ca CommonAddr, infos ...StepPositionInfo) error {
	if err := checkValid(c, typeID, isSequence, len(infos)); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		typeID,
		VariableStruct{IsSequence: isSequence},
		coa,
		0,
		ca,
	})
	if err := u.SetVariableNumber(len(infos)); err != nil {
		return err
	}
	once := false
	for _, v := range infos {
		if !isSequence || !once {
			once = true
			if err := u.AppendInfoObjAddr(v.Ioa); err != nil {
				return err
			}
		}

		u.AppendBytes(v.Value.Value(), byte(v.Qds))
		switch typeID {
		case M_ST_NA_1:
		case M_ST_TA_1:
			u.AppendBytes(CP24Time2a(v.Time, u.InfoObjTimeZone)...)
		case M_ST_TB_1:
			u.AppendBytes(CP56Time2a(v.Time, u.InfoObjTimeZone)...)
		default:
			return ErrTypeIDNotMatch
		}
	}
	return c.Send(u)
}

// Step sends a type identification [M_ST_NA_1]. Step location information
// [M_ST_NA_1] See companion standard 101, subclass 7.3.1.5
// send reason (coa) for
// monitor direction:
// <2> := background scan
// <3> := burst (spontaneous)
// <5> := requested
// <11> := Return information caused by remote command
// <12> := Return information caused by local commands
// <20> := Respond to station calls
// <21> := Respond to the first group call
// to
// <36> := Respond to the 16th group call
func Step(c Connect, isSequence bool, coa CauseOfTransmission, ca CommonAddr, infos ...StepPositionInfo) error {
	if !(coa.Cause == Background || coa.Cause == Spontaneous || coa.Cause == Request ||
		coa.Cause == ReturnInfoRemote || coa.Cause == ReturnInfoLocal ||
		(coa.Cause >= InterrogatedByStation && coa.Cause <= InterrogatedByGroup16)) {
		return ErrCmdCause
	}
	return step(c, M_ST_NA_1, isSequence, coa, ca, infos...)
}

// StepCP24Time2a sends a type identification [M_ST_TA_1]. Double point information with time stamp CP24Time2a, only (SQ = 0) a single information element set
// [M_ST_TA_1] See companion standard 101, subclass 7.3.1.5
// send reason (coa) for
// monitor direction:
// <3> := burst (spontaneous)
// <5> := requested
// <11> := Return information caused by remote command
// <12> := Return information caused by local commands
func StepCP24Time2a(c Connect, coa CauseOfTransmission, ca CommonAddr, infos ...StepPositionInfo) error {
	if !(coa.Cause == Spontaneous || coa.Cause == Request ||
		coa.Cause == ReturnInfoRemote || coa.Cause == ReturnInfoLocal) {
		return ErrCmdCause
	}
	return step(c, M_ST_TA_1, false, coa, ca, infos...)
}

// StepCP56Time2a sends a type identification [M_ST_TB_1]. Double point information with time stamp CP56Time2a, only (SQ = 0) a single information element set
// [M_ST_TB_1] See companion standard 101, subclass 7.3.1.24
// send reason (coa) for
// monitor direction:
// <3> := burst (spontaneous)
// <5> := requested
// <11> := Return information caused by remote command
// <12> := Return information caused by local commands
func StepCP56Time2a(c Connect, coa CauseOfTransmission, ca CommonAddr, infos ...StepPositionInfo) error {
	if !(coa.Cause == Spontaneous || coa.Cause == Request ||
		coa.Cause == ReturnInfoRemote || coa.Cause == ReturnInfoLocal) {
		return ErrCmdCause
	}
	return step(c, M_ST_TB_1, false, coa, ca, infos...)
}

// BitString32Info the measured value attributes.
type BitString32Info struct {
	Ioa   InfoObjAddr
	Value uint32
	// Quality descriptor asdu.GOOD means no remarks.
	Qds QualityDescriptor
	// the type does not include timing will ignore
	Time time.Time
}

// bitString32 sends a type identification [M_BO_NA_1], [M_BO_TA_1] or [M_BO_TB_1].
func bitString32(c Connect, typeID TypeID, isSequence bool, coa CauseOfTransmission, ca CommonAddr, infos ...BitString32Info) error {
	if err := checkValid(c, typeID, isSequence, len(infos)); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		typeID,
		VariableStruct{IsSequence: isSequence},
		coa,
		0,
		ca,
	})
	if err := u.SetVariableNumber(len(infos)); err != nil {
		return err
	}
	once := false
	for _, v := range infos {
		if !isSequence || !once {
			once = true
			if err := u.AppendInfoObjAddr(v.Ioa); err != nil {
				return err
			}
		}
		u.AppendBitsString32(v.Value).AppendBytes(byte(v.Qds))

		switch typeID {
		case M_BO_NA_1:
		case M_BO_TA_1:
			u.AppendBytes(CP24Time2a(v.Time, u.InfoObjTimeZone)...)
		case M_BO_TB_1:
			u.AppendBytes(CP56Time2a(v.Time, u.InfoObjTimeZone)...)
		default:
			return ErrTypeIDNotMatch
		}
	}
	return c.Send(u)
}

// BitString32 sends a type identification [M_BO_NA_1]. Bit string
// [M_BO_NA_1] See companion standard 101, subclass 7.3.1.7
// send reason (coa) for
// monitor direction:
// <2> := background scan
// <3> := burst (spontaneous)
// <5> := requested
// <20> := Respond to station calls
// <21> := Respond to the first group call
// to
// <36> := Respond to the 16th group call
func BitString32(c Connect, isSequence bool, coa CauseOfTransmission, ca CommonAddr, infos ...BitString32Info) error {
	if !(coa.Cause == Background || coa.Cause == Spontaneous || coa.Cause == Request ||
		(coa.Cause >= InterrogatedByStation && coa.Cause <= InterrogatedByGroup16)) {
		return ErrCmdCause
	}
	return bitString32(c, M_BO_NA_1, isSequence, coa, ca, infos...)
}

// BitString32CP24Time2a sends a type identification [M_BO_TA_1]. CP24Time2a bit string with time stamp, only (SQ = 0) a single information element set
// [M_BO_TA_1] See companion standard 101, subclass 7.3.1.8
// send reason (coa) for
// monitor direction:
// <3> := burst (spontaneous)
// <5> := requested
func BitString32CP24Time2a(c Connect, coa CauseOfTransmission, ca CommonAddr, infos ...BitString32Info) error {
	if !(coa.Cause == Spontaneous || coa.Cause == Request) {
		return ErrCmdCause
	}
	return bitString32(c, M_BO_TA_1, false, coa, ca, infos...)
}

// BitString32CP56Time2a sends a type identification [M_BO_TB_1]. CP56Time2a bit string with time stamp, only (SQ = 0) a single information element set
// [M_BO_TB_1] See companion standard 101, subclass 7.3.1.25
// send reason (coa) for
// monitor direction:
// <3> := burst (spontaneous)
// <5> := requested
func BitString32CP56Time2a(c Connect, coa CauseOfTransmission, ca CommonAddr, infos ...BitString32Info) error {
	if !(coa.Cause == Spontaneous || coa.Cause == Request) {
		return ErrCmdCause
	}
	return bitString32(c, M_BO_TB_1, false, coa, ca, infos...)
}

// MeasuredValueNormalInfo the measured value attributes.
type MeasuredValueNormalInfo struct {
	Ioa   InfoObjAddr
	Value Normalize
	// Quality descriptor asdu.GOOD means no remarks.
	Qds QualityDescriptor
	// the type does not include timing will ignore
	Time time.Time
}

// measuredValueNormal sends a type identification [M_ME_NA_1], [M_ME_TA_1],[ M_ME_TD_1] or [M_ME_ND_1].测量值,规一化值
// [M_ME_NA_1] See companion standard 101, subclass 7.3.1.9
// [M_ME_TA_1] See companion standard 101, subclass 7.3.1.10
// [M_ME_TD_1] See companion standard 101, subclass 7.3.1.26
// [M_ME_ND_1] See companion standard 101, subclass 7.3.1.21， The quality descriptor must default to asdu.GOOD
func measuredValueNormal(c Connect, typeID TypeID, isSequence bool, coa CauseOfTransmission, ca CommonAddr, attrs ...MeasuredValueNormalInfo) error {
	if err := checkValid(c, typeID, isSequence, len(attrs)); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		typeID,
		VariableStruct{IsSequence: isSequence},
		coa,
		0,
		ca,
	})
	if err := u.SetVariableNumber(len(attrs)); err != nil {
		return err
	}
	once := false
	for _, v := range attrs {
		if !isSequence || !once {
			once = true
			if err := u.AppendInfoObjAddr(v.Ioa); err != nil {
				return err
			}
		}
		u.AppendNormalize(v.Value)
		switch typeID {
		case M_ME_NA_1:
			u.AppendBytes(byte(v.Qds))
		case M_ME_TA_1:
			u.AppendBytes(byte(v.Qds)).AppendBytes(CP24Time2a(v.Time, u.InfoObjTimeZone)...)
		case M_ME_TD_1:
			u.AppendBytes(byte(v.Qds)).AppendBytes(CP56Time2a(v.Time, u.InfoObjTimeZone)...)
		case M_ME_ND_1: // without quality
		default:
			return ErrTypeIDNotMatch
		}
	}
	return c.Send(u)
}

// MeasuredValueNormal sends a type identification [M_ME_NA_1]. Measured value, normalized value
// [M_ME_NA_1] See companion standard 101, subclass 7.3.1.9
// send reason (coa) for
// monitor direction:
// <1> := period/cycle
// <2> := background scan
// <3> := burst (spontaneous)
// <5> := requested
// <20> := Respond to station calls
// <21> := Respond to the first group call
// to
// <36> := Respond to the 16th group call
func MeasuredValueNormal(c Connect, isSequence bool, coa CauseOfTransmission, ca CommonAddr, infos ...MeasuredValueNormalInfo) error {
	if !(coa.Cause == Periodic || coa.Cause == Background ||
		coa.Cause == Spontaneous || coa.Cause == Request ||
		(coa.Cause >= InterrogatedByStation && coa.Cause <= InterrogatedByGroup16)) {
		return ErrCmdCause
	}
	return measuredValueNormal(c, M_ME_NA_1, isSequence, coa, ca, infos...)
}

// MeasuredValueNormalCP24Time2a sends a type identification [M_ME_TA_1]. Measured value with time stamp CP24Time2a, normalized value, only (SQ = 0) a single set of information elements
// [M_ME_TA_1] See companion standard 101, subclass 7.3.1.10
// send reason (coa) for
// monitor direction:
// <3> := burst (spontaneous)
// <5> := requested
func MeasuredValueNormalCP24Time2a(c Connect, coa CauseOfTransmission,
	ca CommonAddr, infos ...MeasuredValueNormalInfo) error {
	if !(coa.Cause == Spontaneous || coa.Cause == Request) {
		return ErrCmdCause
	}
	return measuredValueNormal(c, M_ME_TA_1, false, coa, ca, infos...)
}

// MeasuredValueNormalCP56Time2a sends a type identification [M_ME_TD_1] Measured value with time stamp CP57Time2a, normalized value, only (SQ = 0) a single information element set
// [M_ME_TD_1] See companion standard 101, subclass 7.3.1.26
// send reason (coa) for
// monitor direction:
// <3> := burst (spontaneous)
// <5> := requested
func MeasuredValueNormalCP56Time2a(c Connect, coa CauseOfTransmission, ca CommonAddr, infos ...MeasuredValueNormalInfo) error {
	if !(coa.Cause == Spontaneous || coa.Cause == Request) {
		return ErrCmdCause
	}
	return measuredValueNormal(c, M_ME_TD_1, false, coa, ca, infos...)
}

// MeasuredValueNormalNoQuality sends a type identification [M_ME_ND_1]. Measured value without quality, normalized value
// [M_ME_ND_1] See companion standard 101, subclass 7.3.1.21,
// The quality descriptor must default to asdu.GOOD
// send reason (coa) for
// monitor direction:
// <1> := period/cycle
// <2> := background scan
// <3> := burst (spontaneous)
// <5> := requested
// <20> := Respond to station calls
// <21> := Respond to the first group call
// to
// <36> := Respond to the 16th group call
func MeasuredValueNormalNoQuality(c Connect, isSequence bool, coa CauseOfTransmission, ca CommonAddr, infos ...MeasuredValueNormalInfo) error {
	if !(coa.Cause == Periodic || coa.Cause == Background ||
		coa.Cause == Spontaneous || coa.Cause == Request ||
		(coa.Cause >= InterrogatedByStation && coa.Cause <= InterrogatedByGroup16)) {
		return ErrCmdCause
	}
	return measuredValueNormal(c, M_ME_ND_1, isSequence, coa, ca, infos...)
}

// MeasuredValueScaledInfo the measured value attributes.
type MeasuredValueScaledInfo struct {
	Ioa   InfoObjAddr
	Value int16
	// Quality descriptor asdu.GOOD means no remarks.
	Qds QualityDescriptor
	// the type does not include timing will ignore
	Time time.Time
}

// measuredValueScaled sends a type identification [M_ME_NB_1], [M_ME_TB_1] or [M_ME_TE_1]. measured value, scaled value
// [M_ME_NB_1] See companion standard 101, subclass 7.3.1.11
// [M_ME_TB_1] See companion standard 101, subclass 7.3.1.12
// [M_ME_TE_1] See companion standard 101, subclass 7.3.1.27
func measuredValueScaled(c Connect, typeID TypeID, isSequence bool, coa CauseOfTransmission, ca CommonAddr, infos ...MeasuredValueScaledInfo) error {
	if err := checkValid(c, typeID, isSequence, len(infos)); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		typeID,
		VariableStruct{IsSequence: isSequence},
		coa,
		0,
		ca,
	})
	if err := u.SetVariableNumber(len(infos)); err != nil {
		return err
	}
	once := false
	for _, v := range infos {
		if !isSequence || !once {
			once = true
			if err := u.AppendInfoObjAddr(v.Ioa); err != nil {
				return err
			}
		}
		u.AppendScaled(v.Value).AppendBytes(byte(v.Qds))
		switch typeID {
		case M_ME_NB_1:
		case M_ME_TB_1:
			u.AppendBytes(CP24Time2a(v.Time, u.InfoObjTimeZone)...)
		case M_ME_TE_1:
			u.AppendBytes(CP56Time2a(v.Time, u.InfoObjTimeZone)...)
		default:
			return ErrTypeIDNotMatch
		}
	}
	return c.Send(u)
}

// MeasuredValueScaled sends a type identification [M_ME_NB_1]. measured value, scaled value
// [M_ME_NB_1] See companion standard 101, subclass 7.3.1.11
// send reason (coa) for
// monitor direction:
// <1> := period/cycle
// <2> := background scan
// <3> := burst (spontaneous)
// <5> := requested
// <20> := Respond to station calls
// <21> := Respond to the first group call
// to
// <36> := Respond to the 16th group call
func MeasuredValueScaled(c Connect, isSequence bool, coa CauseOfTransmission, ca CommonAddr, infos ...MeasuredValueScaledInfo) error {
	if !(coa.Cause == Periodic || coa.Cause == Background ||
		coa.Cause == Spontaneous || coa.Cause == Request ||
		(coa.Cause >= InterrogatedByStation && coa.Cause <= InterrogatedByGroup16)) {
		return ErrCmdCause
	}
	return measuredValueScaled(c, M_ME_NB_1, isSequence, coa, ca, infos...)
}

// MeasuredValueScaledCP24Time2a sends a type identification [M_ME_TB_1]. Measured value with time scale CP24Time2a, scaled value, only (SQ = 0) a single set of information elements
// [M_ME_TB_1] See companion standard 101, subclass 7.3.1.12
// send reason (coa) for
// monitor direction:
// <3> := burst (spontaneous)
// <5> := requested
func MeasuredValueScaledCP24Time2a(c Connect, coa CauseOfTransmission, ca CommonAddr, infos ...MeasuredValueScaledInfo) error {
	if !(coa.Cause == Spontaneous || coa.Cause == Request) {
		return ErrCmdCause
	}
	return measuredValueScaled(c, M_ME_TB_1, false, coa, ca, infos...)
}

// MeasuredValueScaledCP56Time2a sends a type identification [M_ME_TE_1]. Measured value with time scale CP56Time2a, scaled value, only (SQ = 0) a single set of information elements
// [M_ME_TE_1] See companion standard 101, subclass 7.3.1.27
// send reason (coa) for
// monitor direction:
// <3> := burst (spontaneous)
// <5> := requested
func MeasuredValueScaledCP56Time2a(c Connect, coa CauseOfTransmission, ca CommonAddr, infos ...MeasuredValueScaledInfo) error {
	if !(coa.Cause == Spontaneous || coa.Cause == Request) {
		return ErrCmdCause
	}
	return measuredValueScaled(c, M_ME_TE_1, false, coa, ca, infos...)
}

// MeasuredValueFloatInfo the measured value attributes.
type MeasuredValueFloatInfo struct {
	Ioa   InfoObjAddr
	Value float32
	// Quality descriptor asdu.GOOD means no remarks.
	Qds QualityDescriptor
	// the type does not include timing will ignore
	Time time.Time
}

// measuredValueFloat sends a type identification [M_ME_NC_1], [M_ME_TC_1] or [M_ME_TF_1]. measured value, short float
// [M_ME_NC_1] See companion standard 101, subclass 7.3.1.13
// [M_ME_TC_1] See companion standard 101, subclass 7.3.1.14
// [M_ME_TF_1] See companion standard 101, subclass 7.3.1.28
func measuredValueFloat(c Connect, typeID TypeID, isSequence bool, coa CauseOfTransmission, ca CommonAddr, infos ...MeasuredValueFloatInfo) error {
	if err := checkValid(c, typeID, isSequence, len(infos)); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		typeID,
		VariableStruct{IsSequence: isSequence},
		coa,
		0,
		ca,
	})
	if err := u.SetVariableNumber(len(infos)); err != nil {
		return err
	}
	once := false
	for _, v := range infos {
		if !isSequence || !once {
			once = true
			if err := u.AppendInfoObjAddr(v.Ioa); err != nil {
				return err
			}
		}

		u.AppendFloat32(v.Value).AppendBytes(byte(v.Qds & 0xf1))
		switch typeID {
		case M_ME_NC_1:
		case M_ME_TC_1:
			u.AppendBytes(CP24Time2a(v.Time, u.InfoObjTimeZone)...)
		case M_ME_TF_1:
			u.AppendBytes(CP56Time2a(v.Time, u.InfoObjTimeZone)...)
		default:
			return ErrTypeIDNotMatch
		}
	}
	return c.Send(u)
}

// MeasuredValueFloat sends a type identification [M_ME_TF_1]. Measured value, short floating point number
// [M_ME_NC_1] See companion standard 101, subclass 7.3.1.13
// send reason (coa) for
// monitor direction:
// <1> := period/cycle
// <2> := background scan
// <3> := burst (spontaneous)
// <5> := requested
// <20> := Respond to station calls
// <21> := Respond to the first group call
// to
// <36> := Respond to the 16th group call
func MeasuredValueFloat(c Connect, isSequence bool, coa CauseOfTransmission, ca CommonAddr, infos ...MeasuredValueFloatInfo) error {
	if !(coa.Cause == Periodic || coa.Cause == Background ||
		coa.Cause == Spontaneous || coa.Cause == Request ||
		(coa.Cause >= InterrogatedByStation && coa.Cause <= InterrogatedByGroup16)) {
		return ErrCmdCause
	}
	return measuredValueFloat(c, M_ME_NC_1, isSequence, coa, ca, infos...)
}

// MeasuredValueFloatCP24Time2a sends a type identification [M_ME_TC_1]. Measured value with time stamp CP24Time2a, short floating point number, only (SQ = 0) a single set of information elements
// [M_ME_TC_1] See companion standard 101, subclass 7.3.1.14
// send reason (coa) for
// monitor direction:
// <3> := burst (spontaneous)
// <5> := requested
func MeasuredValueFloatCP24Time2a(c Connect, coa CauseOfTransmission, ca CommonAddr, infos ...MeasuredValueFloatInfo) error {
	if !(coa.Cause == Spontaneous || coa.Cause == Request) {
		return ErrCmdCause
	}
	return measuredValueFloat(c, M_ME_TC_1, false, coa, ca, infos...)
}

// MeasuredValueFloatCP56Time2a sends a type identification [M_ME_TF_1]. Measured value with time stamp CP56Time2a, short floating point number, only (SQ = 0) a single set of information elements
// [M_ME_TF_1] See companion standard 101, subclass 7.3.1.28
// send reason (coa) for
// monitor direction:
// <3> := burst (spontaneous)
// <5> := requested
func MeasuredValueFloatCP56Time2a(c Connect, coa CauseOfTransmission, ca CommonAddr, infos ...MeasuredValueFloatInfo) error {
	if !(coa.Cause == Spontaneous || coa.Cause == Request) {
		return ErrCmdCause
	}
	return measuredValueFloat(c, M_ME_TF_1, false, coa, ca, infos...)
}

// BinaryCounterReadingInfo the counter reading attributes. Binary count readout
type BinaryCounterReadingInfo struct {
	Ioa   InfoObjAddr
	Value BinaryCounterReading
	// the type does not include timing will ignore
	Time time.Time
}

// integratedTotals sends a type identification [M_IT_NA_1], [M_IT_TA_1] or [M_IT_TB_1]. Cumulative amount
// [M_IT_NA_1] See companion standard 101, subclass 7.3.1.15
// [M_IT_TA_1] See companion standard 101, subclass 7.3.1.16
// [M_IT_TB_1] See companion standard 101, subclass 7.3.1.29
func integratedTotals(c Connect, typeID TypeID, isSequence bool, coa CauseOfTransmission, ca CommonAddr, infos ...BinaryCounterReadingInfo) error {
	if err := checkValid(c, typeID, isSequence, len(infos)); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		typeID,
		VariableStruct{IsSequence: isSequence},
		coa,
		0,
		ca,
	})
	if err := u.SetVariableNumber(len(infos)); err != nil {
		return err
	}
	once := false
	for _, v := range infos {
		if !isSequence || !once {
			once = true
			if err := u.AppendInfoObjAddr(v.Ioa); err != nil {
				return err
			}
		}
		u.AppendBinaryCounterReading(v.Value)
		switch typeID {
		case M_IT_NA_1:
		case M_IT_TA_1:
			u.AppendBytes(CP24Time2a(v.Time, u.InfoObjTimeZone)...)
		case M_IT_TB_1:
			u.AppendBytes(CP56Time2a(v.Time, u.InfoObjTimeZone)...)
		default:
			return ErrTypeIDNotMatch
		}
	}
	return c.Send(u)
}

// IntegratedTotals sends a type identification [M_IT_NA_1]. Cumulative amount
// [M_IT_NA_1] See companion standard 101, subclass 7.3.1.15
// send reason (coa) for
// monitor direction:
// <3> := burst (spontaneous)
// <37> := Respond to the total quantity call
// <38> := Respond to the call of the first group count
// <39> := Respond to the call of the second group count
// <40> := Respond to the 3rd group count call
// <41> := Respond to the 4th group count call
func IntegratedTotals(c Connect, isSequence bool, coa CauseOfTransmission, ca CommonAddr, infos ...BinaryCounterReadingInfo) error {
	if !(coa.Cause == Spontaneous || (coa.Cause >= RequestByGeneralCounter && coa.Cause <= RequestByGroup4Counter)) {
		return ErrCmdCause
	}
	return integratedTotals(c, M_IT_NA_1, isSequence, coa, ca, infos...)
}

// IntegratedTotalsCP24Time2a sends a type identification [M_IT_TA_1]. The cumulative amount of CP24Time2a with time stamp, only (SQ = 0) a single set of information elements
// [M_IT_TA_1] See companion standard 101, subclass 7.3.1.16
// send reason (coa) for
// monitor direction:
// <3> := burst (spontaneous)
// <37> := Respond to the total quantity call
// <38> := Respond to the call of the first group count
// <39> := Respond to the call of the second group count
// <40> := Respond to the 3rd group count call
// <41> := Respond to the 4th group count call
func IntegratedTotalsCP24Time2a(c Connect, coa CauseOfTransmission, ca CommonAddr, infos ...BinaryCounterReadingInfo) error {
	if !(coa.Cause == Spontaneous || (coa.Cause >= RequestByGeneralCounter && coa.Cause <= RequestByGroup4Counter)) {
		return ErrCmdCause
	}
	return integratedTotals(c, M_IT_TA_1, false, coa, ca, infos...)
}

// IntegratedTotalsCP56Time2a sends a type identification [M_IT_TB_1]. The cumulative amount of CP56Time2a with time stamp, only (SQ = 0) a single set of information elements
// [M_IT_TB_1] See companion standard 101, subclass 7.3.1.29
// send reason (coa) for
// monitor direction:
// <3> := burst (spontaneous)
// <37> := Respond to the total quantity call
// <38> := Respond to the call of the first group count
// <39> := Respond to the call of the second group count
// <40> := Respond to the 3rd group count call
// <41> := Respond to the 4th group count call
func IntegratedTotalsCP56Time2a(c Connect, coa CauseOfTransmission, ca CommonAddr, infos ...BinaryCounterReadingInfo) error {
	if !(coa.Cause == Spontaneous || (coa.Cause >= RequestByGeneralCounter && coa.Cause <= RequestByGroup4Counter)) {
		return ErrCmdCause
	}
	return integratedTotals(c, M_IT_TB_1, false, coa, ca, infos...)
}

// EventOfProtectionEquipmentInfo the counter reading attributes. Binary count readout
type EventOfProtectionEquipmentInfo struct {
	Ioa   InfoObjAddr
	Event SingleEvent
	Qdp   QualityDescriptorProtection
	Msec  uint16
	// the type does not include timing will ignore
	Time time.Time
}

// eventOfProtectionEquipment sends a type identification [M_EP_TA_1], [M_EP_TD_1]. Relay Protection Device Events
// [M_EP_TA_1] See companion standard 101, subclass 7.3.1.17
// [M_EP_TD_1] See companion standard 101, subclass 7.3.1.30
func eventOfProtectionEquipment(c Connect, typeID TypeID, coa CauseOfTransmission, ca CommonAddr, infos ...EventOfProtectionEquipmentInfo) error {
	if coa.Cause != Spontaneous {
		return ErrCmdCause
	}
	if err := checkValid(c, typeID, false, len(infos)); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		typeID,
		VariableStruct{IsSequence: false},
		coa,
		0,
		ca,
	})
	if err := u.SetVariableNumber(len(infos)); err != nil {
		return err
	}
	for _, v := range infos {
		if err := u.AppendInfoObjAddr(v.Ioa); err != nil {
			return err
		}
		u.AppendBytes(byte(v.Event&0x03) | byte(v.Qdp&0xf8))
		u.AppendCP16Time2a(v.Msec)
		switch typeID {
		case M_EP_TA_1:
			u.AppendCP24Time2a(v.Time, u.InfoObjTimeZone)
		case M_EP_TD_1:
			u.AppendCP56Time2a(v.Time, u.InfoObjTimeZone)
		default:
			return ErrTypeIDNotMatch
		}
	}
	return c.Send(u)
}

// EventOfProtectionEquipmentCP24Time2a sends a type identification [M_EP_TA_1]. CP24Time2a relay protection equipment event with time stamp
// [M_EP_TA_1] See companion standard 101, subclass 7.3.1.17
// send reason (coa) for
// monitor direction:
// <3> := burst (spontaneous)
func EventOfProtectionEquipmentCP24Time2a(c Connect, coa CauseOfTransmission, ca CommonAddr, infos ...EventOfProtectionEquipmentInfo) error {
	return eventOfProtectionEquipment(c, M_EP_TA_1, coa, ca, infos...)
}

// EventOfProtectionEquipmentCP56Time2a sends a type identification [M_EP_TD_1]. CP24Time2a relay protection equipment event with time stamp
// [M_EP_TD_1] See companion standard 101, subclass 7.3.1.30
// send reason (coa) for
// monitor direction:
// <3> := burst (spontaneous)
func EventOfProtectionEquipmentCP56Time2a(c Connect, coa CauseOfTransmission, ca CommonAddr, infos ...EventOfProtectionEquipmentInfo) error {
	return eventOfProtectionEquipment(c, M_EP_TD_1, coa, ca, infos...)
}

// PackedStartEventsOfProtectionEquipmentInfo Group start event of relay protection equipment
type PackedStartEventsOfProtectionEquipmentInfo struct {
	Ioa   InfoObjAddr
	Event StartEvent
	Qdp   QualityDescriptorProtection
	Msec  uint16
	// the type does not include timing will ignore
	Time time.Time
}

// packedStartEventsOfProtectionEquipment sends a type identification [M_EP_TB_1], [M_EP_TE_1]. Relay Protection Device Events
// [M_EP_TB_1] See companion standard 101, subclass 7.3.1.18
// [M_EP_TE_1] See companion standard 101, subclass 7.3.1.31
func packedStartEventsOfProtectionEquipment(c Connect, typeID TypeID, coa CauseOfTransmission, ca CommonAddr, info PackedStartEventsOfProtectionEquipmentInfo) error {
	if coa.Cause != Spontaneous {
		return ErrCmdCause
	}
	if err := checkValid(c, typeID, false, 1); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		typeID,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})

	if err := u.AppendInfoObjAddr(info.Ioa); err != nil {
		return err
	}
	u.AppendBytes(byte(info.Event), byte(info.Qdp)&0xf1)
	u.AppendCP16Time2a(info.Msec)
	switch typeID {
	case M_EP_TB_1:
		u.AppendCP24Time2a(info.Time, u.InfoObjTimeZone)
	case M_EP_TE_1:
		u.AppendCP56Time2a(info.Time, u.InfoObjTimeZone)
	default:
		return ErrTypeIDNotMatch
	}

	return c.Send(u)
}

// PackedStartEventsOfProtectionEquipmentCP24Time2a sends a type identification [M_EP_TB_1]. Relay protection equipment event
// [M_EP_TB_1] See companion standard 101, subclass 7.3.1.18
// send reason (coa) for
// monitor direction:
// <3> := burst (spontaneous)
func PackedStartEventsOfProtectionEquipmentCP24Time2a(c Connect, coa CauseOfTransmission, ca CommonAddr, info PackedStartEventsOfProtectionEquipmentInfo) error {
	return packedStartEventsOfProtectionEquipment(c, M_EP_TB_1, coa, ca, info)
}

// PackedStartEventsOfProtectionEquipmentCP56Time2a sends a type identification [M_EP_TB_1]. Relay protection equipment event
// [M_EP_TE_1] See companion standard 101, subclass 7.3.1.31
// send reason (coa) for
// monitor direction:
// <3> := burst (spontaneous)
func PackedStartEventsOfProtectionEquipmentCP56Time2a(c Connect, coa CauseOfTransmission, ca CommonAddr, info PackedStartEventsOfProtectionEquipmentInfo) error {
	return packedStartEventsOfProtectionEquipment(c, M_EP_TE_1, coa, ca, info)
}

// PackedOutputCircuitInfoInfo Relay protection equipment outputs circuit information in groups
type PackedOutputCircuitInfoInfo struct {
	Ioa  InfoObjAddr
	Oci  OutputCircuitInfo
	Qdp  QualityDescriptorProtection
	Msec uint16
	// the type does not include timing will ignore
	Time time.Time
}

// packedOutputCircuitInfo sends a type identification [M_EP_TC_1], [M_EP_TF_1]. Relay protection equipment outputs circuit information in groups
// [M_EP_TC_1] See companion standard 101, subclass 7.3.1.19
// [M_EP_TF_1] See companion standard 101, subclass 7.3.1.32
func packedOutputCircuitInfo(c Connect, typeID TypeID, coa CauseOfTransmission, ca CommonAddr, info PackedOutputCircuitInfoInfo) error {
	if coa.Cause != Spontaneous {
		return ErrCmdCause
	}
	if err := checkValid(c, typeID, false, 1); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		typeID,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})

	if err := u.AppendInfoObjAddr(info.Ioa); err != nil {
		return err
	}
	u.AppendBytes(byte(info.Oci), byte(info.Qdp)&0xf1)
	u.AppendCP16Time2a(info.Msec)
	switch typeID {
	case M_EP_TC_1:
		u.AppendCP24Time2a(info.Time, u.InfoObjTimeZone)
	case M_EP_TF_1:
		u.AppendCP56Time2a(info.Time, u.InfoObjTimeZone)
	default:
		return ErrTypeIDNotMatch
	}

	return c.Send(u)
}

// PackedOutputCircuitInfoCP24Time2a sends a type identification [M_EP_TC_1]. Output circuit information in groups with CP24Time2a relay protection equipment
// [M_EP_TC_1] See companion standard 101, subclass 7.3.1.19
// send reason (coa) for
// monitor direction:
// <3> := burst (spontaneous)
func PackedOutputCircuitInfoCP24Time2a(c Connect, coa CauseOfTransmission, ca CommonAddr, info PackedOutputCircuitInfoInfo) error {
	return packedOutputCircuitInfo(c, M_EP_TC_1, coa, ca, info)
}

// PackedOutputCircuitInfoCP56Time2a sends a type identification [M_EP_TF_1]. Packed output circuit information with CP56Time2a relay protection equipment
// [M_EP_TF_1] See companion standard 101, subclass 7.3.1.32
// send reason (coa) for
// monitor direction:
// <3> := burst (spontaneous)
func PackedOutputCircuitInfoCP56Time2a(c Connect, coa CauseOfTransmission, ca CommonAddr, info PackedOutputCircuitInfoInfo) error {
	return packedOutputCircuitInfo(c, M_EP_TF_1, coa, ca, info)
}

// PackedSinglePointWithSCDInfo Grouped single point information with displacement detection
type PackedSinglePointWithSCDInfo struct {
	Ioa InfoObjAddr
	Scd StatusAndStatusChangeDetection
	Qds QualityDescriptor
}

// PackedSinglePointWithSCD sends a type identification [M_PS_NA_1]. Packed single point information with displacement detection
// [M_PS_NA_1] See companion standard 101, subclass 7.3.1.20
// send reason (coa) for
// monitor direction:
// <2> := background scan
// <3> := burst (spontaneous)
// <5> := requested
// <11> := The return information that will be generated by the remote command
// <12> := The return information generated by the local command
// <20> := Respond to station calls
// <21> := Respond to the first group call
// to
// <36> := Respond to the 16th group call
func PackedSinglePointWithSCD(c Connect, isSequence bool, coa CauseOfTransmission, ca CommonAddr, infos ...PackedSinglePointWithSCDInfo) error {
	if !(coa.Cause == Background || coa.Cause == Spontaneous || coa.Cause == Request ||
		coa.Cause == ReturnInfoRemote || coa.Cause == ReturnInfoLocal ||
		(coa.Cause >= InterrogatedByStation && coa.Cause <= InterrogatedByGroup16)) {
		return ErrCmdCause
	}
	if err := checkValid(c, M_PS_NA_1, isSequence, len(infos)); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		M_PS_NA_1,
		VariableStruct{IsSequence: isSequence},
		coa,
		0,
		ca,
	})
	if err := u.SetVariableNumber(len(infos)); err != nil {
		return err
	}
	once := false
	for _, v := range infos {
		if !isSequence || !once {
			once = true
			if err := u.AppendInfoObjAddr(v.Ioa); err != nil {
				return err
			}
		}
		u.AppendStatusAndStatusChangeDetection(v.Scd)
		u.AppendBytes(byte(v.Qds))
	}
	return c.Send(u)
}

// GetSinglePoint [M_SP_NA_1], [M_SP_TA_1] or [M_SP_TB_1] Obtain a collection of single-point information information bodies
func (sf *ASDU) GetSinglePoint() []SinglePointInfo {
	info := make([]SinglePointInfo, 0, sf.Variable.Number)
	infoObjAddr := InfoObjAddr(0)
	for i, once := 0, false; i < int(sf.Variable.Number); i++ {
		if !sf.Variable.IsSequence || !once {
			once = true
			infoObjAddr = sf.DecodeInfoObjAddr()
		} else {
			infoObjAddr++
		}
		value := sf.DecodeByte()

		var t time.Time
		switch sf.Type {
		case M_SP_NA_1:
		case M_SP_TA_1:
			t = sf.DecodeCP24Time2a()
		case M_SP_TB_1:
			t = sf.DecodeCP56Time2a()
		default:
			panic(ErrTypeIDNotMatch)
		}

		info = append(info, SinglePointInfo{
			Ioa:   infoObjAddr,
			Value: value&0x01 == 0x01,
			Qds:   QualityDescriptor(value & 0xf0),
			Time:  t})
	}
	return info
}

// GetDoublePoint [M_DP_NA_1], [M_DP_TA_1] or [M_DP_TB_1] Obtain the set of two-point information bodies
func (sf *ASDU) GetDoublePoint() []DoublePointInfo {
	info := make([]DoublePointInfo, 0, sf.Variable.Number)
	infoObjAddr := InfoObjAddr(0)
	for i, once := 0, false; i < int(sf.Variable.Number); i++ {
		if !sf.Variable.IsSequence || !once {
			once = true
			infoObjAddr = sf.DecodeInfoObjAddr()
		} else {
			infoObjAddr++
		}
		value := sf.DecodeByte()

		var t time.Time
		switch sf.Type {
		case M_DP_NA_1:
		case M_DP_TA_1:
			t = sf.DecodeCP24Time2a()
		case M_DP_TB_1:
			t = sf.DecodeCP56Time2a()
		default:
			panic(ErrTypeIDNotMatch)
		}

		info = append(info, DoublePointInfo{
			Ioa:   infoObjAddr,
			Value: DoublePoint(value & 0x03),
			Qds:   QualityDescriptor(value & 0xf0),
			Time:  t})
	}
	return info
}

// GetStepPosition [M_ST_NA_1], [M_ST_TA_1] or [M_ST_TB_1] Obtain the set of step position information
func (sf *ASDU) GetStepPosition() []StepPositionInfo {
	info := make([]StepPositionInfo, 0, sf.Variable.Number)
	infoObjAddr := InfoObjAddr(0)
	for i, once := 0, false; i < int(sf.Variable.Number); i++ {
		if !sf.Variable.IsSequence || !once {
			once = true
			infoObjAddr = sf.DecodeInfoObjAddr()
		} else {
			infoObjAddr++
		}
		value := ParseStepPosition(sf.DecodeByte())
		qds := QualityDescriptor(sf.DecodeByte())

		var t time.Time
		switch sf.Type {
		case M_ST_NA_1:
		case M_ST_TA_1:
			t = sf.DecodeCP24Time2a()
		case M_ST_TB_1:
			t = sf.DecodeCP56Time2a()
		default:
			panic(ErrTypeIDNotMatch)
		}

		info = append(info, StepPositionInfo{
			Ioa:   infoObjAddr,
			Value: value,
			Qds:   qds,
			Time:  t})
	}
	return info
}

// GetBitString32 [M_BO_NA_1], [M_BO_TA_1] or [M_BO_TB_1] Obtain the set of bit string information bodies
func (sf *ASDU) GetBitString32() []BitString32Info {
	info := make([]BitString32Info, 0, sf.Variable.Number)
	infoObjAddr := InfoObjAddr(0)
	for i, once := 0, false; i < int(sf.Variable.Number); i++ {
		if !sf.Variable.IsSequence || !once {
			once = true
			infoObjAddr = sf.DecodeInfoObjAddr()
		} else {
			infoObjAddr++
		}

		value := sf.DecodeBitsString32()
		qds := QualityDescriptor(sf.DecodeByte())

		var t time.Time
		switch sf.Type {
		case M_BO_NA_1:
		case M_BO_TA_1:
			t = sf.DecodeCP24Time2a()
		case M_BO_TB_1:
			t = sf.DecodeCP56Time2a()
		default:
			panic(ErrTypeIDNotMatch)
		}

		info = append(info, BitString32Info{
			Ioa:   infoObjAddr,
			Value: value,
			Qds:   qds,
			Time:  t})
	}
	return info
}

// GetMeasuredValueNormal [M_ME_NA_1], [M_ME_TA_1],[ M_ME_TD_1] or [M_ME_ND_1] Obtain the measured value and normalize the value information body collection
func (sf *ASDU) GetMeasuredValueNormal() []MeasuredValueNormalInfo {
	info := make([]MeasuredValueNormalInfo, 0, sf.Variable.Number)
	infoObjAddr := InfoObjAddr(0)
	for i, once := 0, false; i < int(sf.Variable.Number); i++ {
		if !sf.Variable.IsSequence || !once {
			once = true
			infoObjAddr = sf.DecodeInfoObjAddr()
		} else {
			infoObjAddr++
		}

		value := sf.DecodeNormalize()

		var t time.Time
		var qds QualityDescriptor
		switch sf.Type {
		case M_ME_NA_1:
			qds = QualityDescriptor(sf.DecodeByte())
		case M_ME_TA_1:
			qds = QualityDescriptor(sf.DecodeByte())
			t = sf.DecodeCP24Time2a()
		case M_ME_TD_1:
			qds = QualityDescriptor(sf.DecodeByte())
			t = sf.DecodeCP56Time2a()
		case M_ME_ND_1: // 不带品质
		default:
			panic(ErrTypeIDNotMatch)
		}

		info = append(info, MeasuredValueNormalInfo{
			Ioa:   infoObjAddr,
			Value: value,
			Qds:   qds,
			Time:  t})
	}
	return info
}

// GetMeasuredValueScaled [M_ME_NB_1], [M_ME_TB_1] or [M_ME_TE_1] Obtain the measured value, the set of scaled value information body
func (sf *ASDU) GetMeasuredValueScaled() []MeasuredValueScaledInfo {
	info := make([]MeasuredValueScaledInfo, 0, sf.Variable.Number)
	infoObjAddr := InfoObjAddr(0)
	for i, once := 0, false; i < int(sf.Variable.Number); i++ {
		if !sf.Variable.IsSequence || !once {
			once = true
			infoObjAddr = sf.DecodeInfoObjAddr()
		} else {
			infoObjAddr++
		}

		value := sf.DecodeScaled()
		qds := QualityDescriptor(sf.DecodeByte())

		var t time.Time
		switch sf.Type {
		case M_ME_NB_1:
		case M_ME_TB_1:
			t = sf.DecodeCP24Time2a()
		case M_ME_TE_1:
			t = sf.DecodeCP56Time2a()
		default:
			panic(ErrTypeIDNotMatch)
		}

		info = append(info, MeasuredValueScaledInfo{
			Ioa:   infoObjAddr,
			Value: value,
			Qds:   qds,
			Time:  t})
	}
	return info
}

// GetMeasuredValueFloat [M_ME_NC_1], [M_ME_TC_1] or [M_ME_TF_1]. Obtain the measurement value, a collection of short floating-point number information bodies
func (sf *ASDU) GetMeasuredValueFloat() []MeasuredValueFloatInfo {
	info := make([]MeasuredValueFloatInfo, 0, sf.Variable.Number)
	infoObjAddr := InfoObjAddr(0)
	for i, once := 0, false; i < int(sf.Variable.Number); i++ {
		if !sf.Variable.IsSequence || !once {
			once = true
			infoObjAddr = sf.DecodeInfoObjAddr()
		} else {
			infoObjAddr++
		}

		value := sf.DecodeFloat32()
		qua := sf.DecodeByte() & 0xf1

		var t time.Time
		switch sf.Type {
		case M_ME_NC_1:
		case M_ME_TC_1:
			t = sf.DecodeCP24Time2a()
		case M_ME_TF_1:
			t = sf.DecodeCP56Time2a()
		default:
			panic(ErrTypeIDNotMatch)
		}
		info = append(info, MeasuredValueFloatInfo{
			Ioa:   infoObjAddr,
			Value: value,
			Qds:   QualityDescriptor(qua),
			Time:  t})
	}
	return info
}

// GetIntegratedTotals [M_IT_NA_1], [M_IT_TA_1] or [M_IT_TB_1]. Get cumulative information body set
func (sf *ASDU) GetIntegratedTotals() []BinaryCounterReadingInfo {
	info := make([]BinaryCounterReadingInfo, 0, sf.Variable.Number)
	infoObjAddr := InfoObjAddr(0)
	for i, once := 0, false; i < int(sf.Variable.Number); i++ {
		if !sf.Variable.IsSequence || !once {
			once = true
			infoObjAddr = sf.DecodeInfoObjAddr()
		} else {
			infoObjAddr++
		}

		value := sf.DecodeBinaryCounterReading()

		var t time.Time
		switch sf.Type {
		case M_IT_NA_1:
		case M_IT_TA_1:
			t = sf.DecodeCP24Time2a()
		case M_IT_TB_1:
			t = sf.DecodeCP56Time2a()
		default:
			panic(ErrTypeIDNotMatch)
		}
		info = append(info, BinaryCounterReadingInfo{
			Ioa:   infoObjAddr,
			Value: value,
			Time:  t})
	}
	return info
}

// GetEventOfProtectionEquipment [M_EP_TA_1] [M_EP_TD_1] Obtain the event information body of relay protection equipment
func (sf *ASDU) GetEventOfProtectionEquipment() []EventOfProtectionEquipmentInfo {
	info := make([]EventOfProtectionEquipmentInfo, 0, sf.Variable.Number)
	infoObjAddr := InfoObjAddr(0)
	for i, once := 0, false; i < int(sf.Variable.Number); i++ {
		if !sf.Variable.IsSequence || !once {
			once = true
			infoObjAddr = sf.DecodeInfoObjAddr()
		} else {
			infoObjAddr++
		}

		value := sf.DecodeByte()
		msec := sf.DecodeCP16Time2a()
		var t time.Time
		switch sf.Type {
		case M_EP_TA_1:
			t = sf.DecodeCP24Time2a()
		case M_EP_TD_1:
			t = sf.DecodeCP56Time2a()
		default:
			panic(ErrTypeIDNotMatch)
		}
		info = append(info, EventOfProtectionEquipmentInfo{
			Ioa:   infoObjAddr,
			Event: SingleEvent(value & 0x03),
			Qdp:   QualityDescriptorProtection(value & 0xf1),
			Msec:  msec,
			Time:  t})
	}
	return info
}

// GetPackedStartEventsOfProtectionEquipment [M_EP_TB_1] [M_EP_TE_1] Obtain the event information body of relay protection equipment
func (sf *ASDU) GetPackedStartEventsOfProtectionEquipment() PackedStartEventsOfProtectionEquipmentInfo {
	info := PackedStartEventsOfProtectionEquipmentInfo{}

	if sf.Variable.IsSequence || sf.Variable.Number != 1 {
		return info
	}

	info.Ioa = sf.DecodeInfoObjAddr()
	info.Event = StartEvent(sf.DecodeByte())
	info.Qdp = QualityDescriptorProtection(sf.DecodeByte() & 0xf1)
	info.Msec = sf.DecodeCP16Time2a()
	switch sf.Type {
	case M_EP_TB_1:
		info.Time = sf.DecodeCP24Time2a()
	case M_EP_TE_1:
		info.Time = sf.DecodeCP56Time2a()
	default:
		panic(ErrTypeIDNotMatch)
	}
	return info
}

// GetPackedOutputCircuitInfo [M_EP_TC_1] [M_EP_TF_1] Obtain the group output circuit information body of relay protection equipment
func (sf *ASDU) GetPackedOutputCircuitInfo() PackedOutputCircuitInfoInfo {
	info := PackedOutputCircuitInfoInfo{}

	if sf.Variable.IsSequence || sf.Variable.Number != 1 {
		return info
	}

	info.Ioa = sf.DecodeInfoObjAddr()
	info.Oci = OutputCircuitInfo(sf.DecodeByte())
	info.Qdp = QualityDescriptorProtection(sf.DecodeByte() & 0xf1)
	info.Msec = sf.DecodeCP16Time2a()
	switch sf.Type {
	case M_EP_TC_1:
		info.Time = sf.DecodeCP24Time2a()
	case M_EP_TF_1:
		info.Time = sf.DecodeCP56Time2a()
	default:
		panic(ErrTypeIDNotMatch)
	}
	return info
}

// GetPackedSinglePointWithSCD [M_PS_NA_1]. Obtain grouped single point information with displacement detection
func (sf *ASDU) GetPackedSinglePointWithSCD() []PackedSinglePointWithSCDInfo {
	info := make([]PackedSinglePointWithSCDInfo, 0, sf.Variable.Number)
	infoObjAddr := InfoObjAddr(0)
	for i, once := 0, false; i < int(sf.Variable.Number); i++ {
		if !sf.Variable.IsSequence || !once {
			once = true
			infoObjAddr = sf.DecodeInfoObjAddr()
		} else {
			infoObjAddr++
		}
		scd := sf.DecodeStatusAndStatusChangeDetection()
		qds := QualityDescriptor(sf.DecodeByte())
		info = append(info, PackedSinglePointWithSCDInfo{
			Ioa: infoObjAddr,
			Scd: scd,
			Qds: qds})
	}
	return info
}

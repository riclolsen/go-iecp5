// Copyright 2020 thinkgos (thinkgo@aliyun.com).  All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package asdu

import (
	"time"
)

// Application service data unit for process information in the control direction

// SingleCommandInfo Single command message body
type SingleCommandInfo struct {
	Ioa   InfoObjAddr
	Value bool
	Qoc   QualifierOfCommand
	Time  time.Time
}

// SingleCmd sends a type identification [C_SC_NA_1] or [C_SC_TA_1]. Single command, only a single info object(SQ = 0)
// [C_SC_NA_1] See companion standard 101, subclass 7.3.2.1
// [C_SC_TA_1] See companion standard 101,
// The reason for delivery (coa) is used for
// control direction:
// <6> := activation
// <8> := deactivate
// Monitoring direction:
// <7> := activation confirmation
// <9> := deactivation confirmation
// <10> := activation termination
// <44> := unknown type identifier
// <45> := unknown delivery reason
// <46> := Unknown ASDU public address
// <47> := Unknown message object address
func SingleCmd(c Connect, typeID TypeID, coa CauseOfTransmission, ca CommonAddr, cmd SingleCommandInfo) error {
	if !(coa.Cause == Activation || coa.Cause == Deactivation) {
		return ErrCmdCause
	}
	if err := c.Params().Valid(); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		typeID,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})

	if err := u.AppendInfoObjAddr(cmd.Ioa); err != nil {
		return err
	}
	value := cmd.Qoc.Value()
	if cmd.Value {
		value |= 0x01
	}
	u.AppendBytes(value)
	switch typeID {
	case C_SC_NA_1:
	case C_SC_TA_1:
		u.AppendBytes(CP56Time2a(cmd.Time, u.InfoObjTimeZone)...)
	default:
		return ErrTypeIDNotMatch
	}
	return c.Send(u)
}

// DoubleCommandInfo Single command message body
type DoubleCommandInfo struct {
	Ioa   InfoObjAddr
	Value DoubleCommand
	Qoc   QualifierOfCommand
	Time  time.Time
}

// DoubleCmd sends a type identification [C_DC_NA_1] or [C_DC_TA_1]. double command, only single info object(SQ = 0)
// [C_DC_NA_1] See companion standard 101, subclass 7.3.2.2
// [C_DC_TA_1] See companion standard 101,
// The reason for delivery (coa) is used for
// control direction:
// <6> := activation
// <8> := deactivate
// Monitoring direction:
// <7> := activation confirmation
// <9> := deactivation confirmation
// <10> := activation termination
// <44> := unknown type identifier
// <45> := unknown delivery reason
// <46> := Unknown ASDU public address
// <47> := Unknown message object address
func DoubleCmd(c Connect, typeID TypeID, coa CauseOfTransmission, ca CommonAddr,
	cmd DoubleCommandInfo) error {
	if !(coa.Cause == Activation || coa.Cause == Deactivation) {
		return ErrCmdCause
	}
	if err := c.Params().Valid(); err != nil {
		return err
	}
	u := NewASDU(c.Params(), Identifier{
		typeID,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})

	if err := u.AppendInfoObjAddr(cmd.Ioa); err != nil {
		return err
	}

	u.AppendBytes(cmd.Qoc.Value() | byte(cmd.Value&0x03))
	switch typeID {
	case C_DC_NA_1:
	case C_DC_TA_1:
		u.AppendBytes(CP56Time2a(cmd.Time, u.InfoObjTimeZone)...)
	default:
		return ErrTypeIDNotMatch
	}
	return c.Send(u)
}

// StepCommandInfo step-by-step regulation information body
type StepCommandInfo struct {
	Ioa   InfoObjAddr
	Value StepCommand
	Qoc   QualifierOfCommand
	Time  time.Time
}

// StepCmd sends a type [C_RC_NA_1] or [C_RC_TA_1]. step tuning command, only a single info object(SQ = 0)
// [C_RC_NA_1] See companion standard 101, subclass 7.3.2.3
// [C_RC_TA_1] See companion standard 101,
// The reason for delivery (coa) is used for
// control direction:
// <6> := activation
// <8> := deactivate
// Monitoring direction:
// <7> := activation confirmation
// <9> := deactivation confirmation
// <10> := activation termination
// <44> := unknown type identifier
// <45> := unknown delivery reason
// <46> := Unknown ASDU public address
// <47> := Unknown message object address
func StepCmd(c Connect, typeID TypeID, coa CauseOfTransmission, ca CommonAddr, cmd StepCommandInfo) error {
	if !(coa.Cause == Activation || coa.Cause == Deactivation) {
		return ErrCmdCause
	}
	if err := c.Params().Valid(); err != nil {
		return err
	}
	u := NewASDU(c.Params(), Identifier{
		typeID,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})

	if err := u.AppendInfoObjAddr(cmd.Ioa); err != nil {
		return err
	}

	u.AppendBytes(cmd.Qoc.Value() | byte(cmd.Value&0x03))
	switch typeID {
	case C_RC_NA_1:
	case C_RC_TA_1:
		u.AppendBytes(CP56Time2a(cmd.Time, u.InfoObjTimeZone)...)
	default:
		return ErrTypeIDNotMatch
	}
	return c.Send(u)
}

// SetpointCommandNormalInfo set command, normalize value message body
type SetpointCommandNormalInfo struct {
	Ioa   InfoObjAddr
	Value Normalize
	Qos   QualifierOfSetpointCmd
	Time  time.Time
}

// SetpointCmdNormal sends a type [C_SE_NA_1] or [C_SE_TA_1]. set command, normalized value, only single info object(SQ = 0)
// [C_SE_NA_1] See companion standard 101, subclass 7.3.2.4
// [C_SE_TA_1] See companion standard 101,
// The reason for delivery (coa) is used for
// control direction:
// <6> := activation
// <8> := deactivate
// Monitoring direction:
// <7> := activation confirmation
// <9> := deactivation confirmation
// <10> := activation termination
// <44> := unknown type identifier
// <45> := unknown delivery reason
// <46> := Unknown ASDU public address
// <47> := Unknown message object address
func SetpointCmdNormal(c Connect, typeID TypeID, coa CauseOfTransmission, ca CommonAddr, cmd SetpointCommandNormalInfo) error {
	if !(coa.Cause == Activation || coa.Cause == Deactivation) {
		return ErrCmdCause
	}
	if err := c.Params().Valid(); err != nil {
		return err
	}
	u := NewASDU(c.Params(), Identifier{
		typeID,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})

	if err := u.AppendInfoObjAddr(cmd.Ioa); err != nil {
		return err
	}
	u.AppendNormalize(cmd.Value).AppendBytes(cmd.Qos.Value())
	switch typeID {
	case C_SE_NA_1:
	case C_SE_TA_1:
		u.AppendBytes(CP56Time2a(cmd.Time, u.InfoObjTimeZone)...)
	default:
		return ErrTypeIDNotMatch
	}
	return c.Send(u)
}

// SetpointCommandScaledInfo set command, scaled value message body
type SetpointCommandScaledInfo struct {
	Ioa   InfoObjAddr
	Value int16
	Qos   QualifierOfSetpointCmd
	Time  time.Time
}

// SetpointCmdScaled sends a type [C_SE_NB_1] or [C_SE_TB_1]. set command, scaled value, only single info object(SQ = 0)
// [C_SE_NB_1] See companion standard 101, subclass 7.3.2.5
// [C_SE_TB_1] See companion standard 101,
// The reason for delivery (coa) is used for
// control direction:
// <6> := activation
// <8> := deactivate
// Monitoring direction:
// <7> := activation confirmation
// <9> := deactivation confirmation
// <10> := activation termination
// <44> := unknown type identifier
// <45> := unknown delivery reason
// <46> := Unknown ASDU public address
// <47> := Unknown message object address
func SetpointCmdScaled(c Connect, typeID TypeID, coa CauseOfTransmission, ca CommonAddr, cmd SetpointCommandScaledInfo) error {
	if !(coa.Cause == Activation || coa.Cause == Deactivation) {
		return ErrCmdCause
	}
	if err := c.Params().Valid(); err != nil {
		return err
	}
	u := NewASDU(c.Params(), Identifier{
		typeID,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})

	if err := u.AppendInfoObjAddr(cmd.Ioa); err != nil {
		return err
	}
	u.AppendScaled(cmd.Value).AppendBytes(cmd.Qos.Value())
	switch typeID {
	case C_SE_NB_1:
	case C_SE_TB_1:
		u.AppendBytes(CP56Time2a(cmd.Time, u.InfoObjTimeZone)...)
	default:
		return ErrTypeIDNotMatch
	}
	return c.Send(u)
}

// SetpointCommandFloatInfo set command, short float message body
type SetpointCommandFloatInfo struct {
	Ioa   InfoObjAddr
	Value float32
	Qos   QualifierOfSetpointCmd
	Time  time.Time
}

// SetpointCmdFloat sends a type [C_SE_NC_1] or [C_SE_TC_1].Set command, short floating point number, only a single information object(SQ = 0)
// [C_SE_NC_1] See companion standard 101, subclass 7.3.2.6
// [C_SE_TC_1] See companion standard 101,
// The reason for delivery (coa) is used for
// control direction:
// <6> := activation
// <8> := deactivate
// Monitoring direction:
// <7> := activation confirmation
// <9> := deactivation confirmation
// <10> := activation termination
// <44> := unknown type identifier
// <45> := unknown delivery reason
// <46> := Unknown ASDU public address
// <47> := Unknown message object address
func SetpointCmdFloat(c Connect, typeID TypeID, coa CauseOfTransmission, ca CommonAddr, cmd SetpointCommandFloatInfo) error {
	if !(coa.Cause == Activation || coa.Cause == Deactivation) {
		return ErrCmdCause
	}
	if err := c.Params().Valid(); err != nil {
		return err
	}
	u := NewASDU(c.Params(), Identifier{
		typeID,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})
	if err := u.AppendInfoObjAddr(cmd.Ioa); err != nil {
		return err
	}

	u.AppendFloat32(cmd.Value).AppendBytes(cmd.Qos.Value())

	switch typeID {
	case C_SE_NC_1:
	case C_SE_TC_1:
		u.AppendBytes(CP56Time2a(cmd.Time, u.InfoObjTimeZone)...)
	default:
		return ErrTypeIDNotMatch
	}

	return c.Send(u)
}

// BitsString32CommandInfo bit string command message body
type BitsString32CommandInfo struct {
	Ioa   InfoObjAddr
	Value uint32
	Time  time.Time
}

// BitsString32Cmd sends a type [C_BO_NA_1] or [C_BO_TA_1]. bitstring command, only a single info object(SQ = 0)
// [C_BO_NA_1] See companion standard 101, subclass 7.3.2.7
// [C_BO_TA_1] See companion standard 101,
// The reason for delivery (coa) is used for
// control direction:
// <6> := activation
// <8> := deactivate
// Monitoring direction:
// <7> := activation confirmation
// <9> := deactivation confirmation
// <10> := activation termination
// <44> := unknown type identifier
// <45> := unknown delivery reason
// <46> := Unknown ASDU public address
// <47> := Unknown message object address
func BitsString32Cmd(c Connect, typeID TypeID, coa CauseOfTransmission, commonAddr CommonAddr,
	cmd BitsString32CommandInfo) error {
	if !(coa.Cause == Activation || coa.Cause == Deactivation) {
		return ErrCmdCause
	}
	if err := c.Params().Valid(); err != nil {
		return err
	}
	u := NewASDU(c.Params(), Identifier{
		typeID,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		commonAddr,
	})
	if err := u.AppendInfoObjAddr(cmd.Ioa); err != nil {
		return err
	}

	u.AppendBitsString32(cmd.Value)

	switch typeID {
	case C_BO_NA_1:
	case C_BO_TA_1:
		u.AppendBytes(CP56Time2a(cmd.Time, u.InfoObjTimeZone)...)
	default:
		return ErrTypeIDNotMatch
	}

	return c.Send(u)
}

// GetSingleCmd [C_SC_NA_1] or [C_SC_TA_1] Get the message body of a single command
func (sf *ASDU) GetSingleCmd() SingleCommandInfo {
	defer sf.restoreInfoObj(sf.InfoObj)
	var s SingleCommandInfo

	s.Ioa = sf.DecodeInfoObjAddr()
	value := sf.DecodeByte()
	s.Value = value&0x01 == 0x01
	s.Qoc = ParseQualifierOfCommand(value & 0xfe)

	switch sf.Type {
	case C_SC_NA_1:
	case C_SC_TA_1:
		s.Time = sf.DecodeCP56Time2a()
	default:
		panic(ErrTypeIDNotMatch)
	}

	return s
}

// GetDoubleCmd [C_DC_NA_1] or [C_DC_TA_1] Get the dual command information body
func (sf *ASDU) GetDoubleCmd() DoubleCommandInfo {
	defer sf.restoreInfoObj(sf.InfoObj)
	var cmd DoubleCommandInfo

	cmd.Ioa = sf.DecodeInfoObjAddr()
	value := sf.DecodeByte()
	cmd.Value = DoubleCommand(value & 0x03)
	cmd.Qoc = ParseQualifierOfCommand(value & 0xfc)

	switch sf.Type {
	case C_DC_NA_1:
	case C_DC_TA_1:
		cmd.Time = sf.DecodeCP56Time2a()
	default:
		panic(ErrTypeIDNotMatch)
	}

	return cmd
}

// GetStepCmd [C_RC_NA_1] or [C_RC_TA_1] Get step adjustment command information body
func (sf *ASDU) GetStepCmd() StepCommandInfo {
	defer sf.restoreInfoObj(sf.InfoObj)
	var cmd StepCommandInfo

	cmd.Ioa = sf.DecodeInfoObjAddr()
	value := sf.DecodeByte()
	cmd.Value = StepCommand(value & 0x03)
	cmd.Qoc = ParseQualifierOfCommand(value & 0xfc)

	switch sf.Type {
	case C_RC_NA_1:
	case C_RC_TA_1:
		cmd.Time = sf.DecodeCP56Time2a()
	default:
		panic(ErrTypeIDNotMatch)
	}

	return cmd
}

// GetSetpointNormalCmd [C_SE_NA_1] or [C_SE_TA_1] Get setting command, normalize value information body
func (sf *ASDU) GetSetpointNormalCmd() SetpointCommandNormalInfo {
	defer sf.restoreInfoObj(sf.InfoObj)
	var cmd SetpointCommandNormalInfo

	cmd.Ioa = sf.DecodeInfoObjAddr()
	cmd.Value = sf.DecodeNormalize()
	cmd.Qos = ParseQualifierOfSetpointCmd(sf.DecodeByte())

	switch sf.Type {
	case C_SE_NA_1:
	case C_SE_TA_1:
		cmd.Time = sf.DecodeCP56Time2a()
	default:
		panic(ErrTypeIDNotMatch)
	}

	return cmd
}

// GetSetpointCmdScaled [C_SE_NB_1] or [C_SE_TB_1] Get setting command, scaled value information body
func (sf *ASDU) GetSetpointCmdScaled() SetpointCommandScaledInfo {
	defer sf.restoreInfoObj(sf.InfoObj)
	var cmd SetpointCommandScaledInfo

	cmd.Ioa = sf.DecodeInfoObjAddr()
	cmd.Value = sf.DecodeScaled()
	cmd.Qos = ParseQualifierOfSetpointCmd(sf.DecodeByte())

	switch sf.Type {
	case C_SE_NB_1:
	case C_SE_TB_1:
		cmd.Time = sf.DecodeCP56Time2a()
	default:
		panic(ErrTypeIDNotMatch)
	}

	return cmd
}

// GetSetpointFloatCmd [C_SE_NC_1] or [C_SE_TC_1] Get setting command, short floating point number information body
func (sf *ASDU) GetSetpointFloatCmd() SetpointCommandFloatInfo {
	defer sf.restoreInfoObj(sf.InfoObj)
	var cmd SetpointCommandFloatInfo

	cmd.Ioa = sf.DecodeInfoObjAddr()
	cmd.Value = sf.DecodeFloat32()
	cmd.Qos = ParseQualifierOfSetpointCmd(sf.DecodeByte())

	switch sf.Type {
	case C_SE_NC_1:
	case C_SE_TC_1:
		cmd.Time = sf.DecodeCP56Time2a()
	default:
		panic(ErrTypeIDNotMatch)
	}

	return cmd
}

// GetBitsString32Cmd [C_BO_NA_1] or [C_BO_TA_1] Get bit string command message body
func (sf *ASDU) GetBitsString32Cmd() BitsString32CommandInfo {
	defer sf.restoreInfoObj(sf.InfoObj)
	var cmd BitsString32CommandInfo

	cmd.Ioa = sf.DecodeInfoObjAddr()
	cmd.Value = sf.DecodeBitsString32()
	switch sf.Type {
	case C_BO_NA_1:
	case C_BO_TA_1:
		cmd.Time = sf.DecodeCP56Time2a()
	default:
		panic(ErrTypeIDNotMatch)
	}

	return cmd
}

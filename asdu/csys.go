// Copyright 2020 thinkgos (thinkgo@aliyun.com).  All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package asdu

import (
	"time"
)

// Application service data unit for system information in the control direction

// InterrogationCmd send a new interrogation command [C_IC_NA_1]. Total summon command, only a single message object(SQ = 0)
// [C_IC_NA_1] See companion standard 101, subclass 7.3.4.1
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
func InterrogationCmd(c Connect, coa CauseOfTransmission, ca CommonAddr, qoi QualifierOfInterrogation) error {
	if !(coa.Cause == Activation || coa.Cause == Deactivation) {
		return ErrCmdCause
	}
	if err := c.Params().Valid(); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		C_IC_NA_1,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})
	if err := u.AppendInfoObjAddr(InfoObjAddrIrrelevant); err != nil {
		return err
	}
	u.AppendBytes(byte(qoi))
	return c.Send(u)
}

// CounterInterrogationCmd send Counter Interrogation command [C_CI_NA_1]，Counted call command, only a single info object(SQ = 0)
// [C_CI_NA_1] See companion standard 101, subclass 7.3.4.2
// The reason for delivery (coa) is used for
// control direction:
// <6> := activation
// Monitoring direction:
// <7> := activation confirmation
// <10> := activation termination
// <44> := unknown type identifier
// <45> := unknown delivery reason
// <46> := Unknown ASDU public address
// <47> := Unknown message object address
func CounterInterrogationCmd(c Connect, coa CauseOfTransmission, ca CommonAddr, qcc QualifierCountCall) error {
	if err := c.Params().Valid(); err != nil {
		return err
	}
	coa.Cause = Activation
	u := NewASDU(c.Params(), Identifier{
		C_CI_NA_1,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})
	if err := u.AppendInfoObjAddr(InfoObjAddrIrrelevant); err != nil {
		return err
	}
	u.AppendBytes(qcc.Value())
	return c.Send(u)
}

// ReadCmd send read command [C_RD_NA_1], read command, only a single info object(SQ = 0)
// [C_RD_NA_1] See companion standard 101, subclass 7.3.4.3
// The reason for delivery (coa) is used for
// control direction:
// <5> := ask
// Monitoring direction:
// <44> := unknown type identifier
// <45> := unknown delivery reason
// <46> := Unknown ASDU public address
// <47> := Unknown message object address
func ReadCmd(c Connect, coa CauseOfTransmission, ca CommonAddr, ioa InfoObjAddr) error {
	if err := c.Params().Valid(); err != nil {
		return err
	}
	coa.Cause = Request
	u := NewASDU(c.Params(), Identifier{
		C_RD_NA_1,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})
	if err := u.AppendInfoObjAddr(ioa); err != nil {
		return err
	}
	return c.Send(u)
}

// ClockSynchronizationCmd send clock sync command [C_CS_NA_1],Clock synchronization commands, only a single info object(SQ = 0)
// [C_CS_NA_1] See companion standard 101, subclass 7.3.4.4
// The reason for delivery (coa) is used for
// control direction:
// <6> := activation
// Monitoring direction:
// <7> := activation confirmation
// <10> := activation termination
// <44> := unknown type identifier
// <45> := unknown delivery reason
// <46> := Unknown ASDU public address
// <47> := Unknown message object address
func ClockSynchronizationCmd(c Connect, coa CauseOfTransmission, ca CommonAddr, t time.Time) error {
	if err := c.Params().Valid(); err != nil {
		return err
	}
	coa.Cause = Activation
	u := NewASDU(c.Params(), Identifier{
		C_CS_NA_1,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})
	if err := u.AppendInfoObjAddr(InfoObjAddrIrrelevant); err != nil {
		return err
	}
	u.AppendBytes(CP56Time2a(t, u.InfoObjTimeZone)...)
	return c.Send(u)
}

// TestCommand send test command [C_TS_NA_1]，Test command, only a single info object(SQ = 0)
// [C_TS_NA_1] See companion standard 101, subclass 7.3.4.5
// The reason for delivery (coa) is used for
// control direction:
// <6> := activation
// 监视方向：
// <7> := activation confirmation
// <44> := unknown type identifier
// <45> := unknown delivery reason
// <46> := Unknown ASDU public address
// <47> := Unknown message object address
func TestCommand(c Connect, coa CauseOfTransmission, ca CommonAddr) error {
	if err := c.Params().Valid(); err != nil {
		return err
	}
	coa.Cause = Activation
	u := NewASDU(c.Params(), Identifier{
		C_TS_NA_1,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})
	if err := u.AppendInfoObjAddr(InfoObjAddrIrrelevant); err != nil {
		return err
	}
	u.AppendBytes(byte(FBPTestWord&0xff), byte(FBPTestWord>>8))
	return c.Send(u)
}

// ResetProcessCmd send reset process command [C_RP_NA_1], Reset process command, only single info object(SQ = 0)
// [C_RP_NA_1] See companion standard 101, subclass 7.3.4.6
// The reason for delivery (coa) is used for
// control direction:
// <6> := activation
// Monitoring direction:
// <7> := activation confirmation
// <44> := unknown type identifier
// <45> := unknown delivery reason
// <46> := Unknown ASDU public address
// <47> := Unknown message object address
func ResetProcessCmd(c Connect, coa CauseOfTransmission, ca CommonAddr, qrp QualifierOfResetProcessCmd) error {
	if err := c.Params().Valid(); err != nil {
		return err
	}
	coa.Cause = Activation
	u := NewASDU(c.Params(), Identifier{
		C_RP_NA_1,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})
	if err := u.AppendInfoObjAddr(InfoObjAddrIrrelevant); err != nil {
		return err
	}
	u.AppendBytes(byte(qrp))
	return c.Send(u)
}

// DelayAcquireCommand send delay acquire command [C_CD_NA_1], Delayed get command, only a single info object(SQ = 0)
// [C_CD_NA_1] See companion standard 101, subclass 7.3.4.7
// The reason for delivery (coa) is used for
// Control direction:
// <3> := sudden
// <6> := activation
// Monitoring direction:
// <7> := activation confirmation
// <44> := unknown type identifier
// <45> := unknown delivery reason
// <46> := Unknown ASDU public address
// <47> := Unknown message object address
func DelayAcquireCommand(c Connect, coa CauseOfTransmission, ca CommonAddr, msec uint16) error {
	if !(coa.Cause == Spontaneous || coa.Cause == Activation) {
		return ErrCmdCause
	}
	if err := c.Params().Valid(); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		C_CD_NA_1,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})
	if err := u.AppendInfoObjAddr(InfoObjAddrIrrelevant); err != nil {
		return err
	}
	u.AppendCP16Time2a(msec)
	return c.Send(u)
}

// TestCommandCP56Time2a send test command [C_TS_TA_1]，Test command, only a single info object(SQ = 0)
// The reason for delivery (coa) is used for
// control direction:
// <6> := activation
// Monitoring direction:
// <7> := activation confirmation
// <44> := unknown type identifier
// <45> := unknown delivery reason
// <46> := Unknown ASDU public address
// <47> := Unknown message object address
func TestCommandCP56Time2a(c Connect, coa CauseOfTransmission, ca CommonAddr, t time.Time) error {
	if err := c.Params().Valid(); err != nil {
		return err
	}
	u := NewASDU(c.Params(), Identifier{
		C_TS_TA_1,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})
	if err := u.AppendInfoObjAddr(InfoObjAddrIrrelevant); err != nil {
		return err
	}
	u.AppendUint16(FBPTestWord)
	u.AppendCP56Time2a(t, u.InfoObjTimeZone)
	return c.Send(u)
}

// GetInterrogationCmd [C_IC_NA_1] Get the total call information body (information object address, call qualifier)
func (sf *ASDU) GetInterrogationCmd() (InfoObjAddr, QualifierOfInterrogation) {
	return sf.DecodeInfoObjAddr(), QualifierOfInterrogation(sf.InfoObj[0])
}

// GetCounterInterrogationCmd [C_CI_NA_1] Obtain the metered call information body (information object address, metered call qualifier)
func (sf *ASDU) GetCounterInterrogationCmd() (InfoObjAddr, QualifierCountCall) {
	return sf.DecodeInfoObjAddr(), ParseQualifierCountCall(sf.InfoObj[0])
}

// GetReadCmd [C_RD_NA_1] Get the address of the read command information
func (sf *ASDU) GetReadCmd() InfoObjAddr {
	return sf.DecodeInfoObjAddr()
}

// GetClockSynchronizationCmd [C_CS_NA_1] Obtain clock synchronization command information body (information object address, time)
func (sf *ASDU) GetClockSynchronizationCmd() (InfoObjAddr, time.Time) {

	return sf.DecodeInfoObjAddr(), sf.DecodeCP56Time2a()
}

// GetTestCommand [C_TS_NA_1], get the test command information body (information object address, whether it is a test word)
func (sf *ASDU) GetTestCommand() (InfoObjAddr, bool) {
	return sf.DecodeInfoObjAddr(), sf.DecodeUint16() == FBPTestWord
}

// GetResetProcessCmd [C_RP_NA_1] Obtain the reset process command information body (information object address, reset process command qualifier)
func (sf *ASDU) GetResetProcessCmd() (InfoObjAddr, QualifierOfResetProcessCmd) {
	return sf.DecodeInfoObjAddr(), QualifierOfResetProcessCmd(sf.InfoObj[0])
}

// GetDelayAcquireCommand [C_CD_NA_1] Get delay Get command information body (information object address, delay in milliseconds)
func (sf *ASDU) GetDelayAcquireCommand() (InfoObjAddr, uint16) {
	return sf.DecodeInfoObjAddr(), sf.DecodeUint16()
}

// GetTestCommandCP56Time2a [C_TS_TA_1]，Obtain the test command information body (information object address, whether it is a test word)
func (sf *ASDU) GetTestCommandCP56Time2a() (InfoObjAddr, bool, time.Time) {
	return sf.DecodeInfoObjAddr(), sf.DecodeUint16() == FBPTestWord, sf.DecodeCP56Time2a()
}

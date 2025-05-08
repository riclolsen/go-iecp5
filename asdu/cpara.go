// Copyright 2020 thinkgos (thinkgo@aliyun.com).  All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package asdu

// in the ASDU of the control direction parameter

// ParameterNormalInfo Measured value parameter, normalized value Information body
type ParameterNormalInfo struct {
	Ioa   InfoObjAddr
	Value Normalize
	Qpm   QualifierOfParameterMV
}

// ParameterNormal Measured value parameter, normalized value, only single info object(SQ = 0)
// [P_ME_NA_1], See companion standard 101, subclass 7.3.5.1
// The reason for delivery (coa) is used for
// control direction:
// <6> := activation
// Monitoring direction:
// <7> := activation confirmation
// <20> := answer station call
// <21> := Answer Group 1 Call
// <22> := Answer Group 2 Call
// to
// <36> := Answer Group 16 Call
// <44> := unknown type identifier
// <45> := unknown delivery reason
// <46> := Unknown ASDU public address
// <47> := Unknown message object address
func ParameterNormal(c Connect, coa CauseOfTransmission, ca CommonAddr, p ParameterNormalInfo) error {
	if coa.Cause != Activation {
		return ErrCmdCause
	}
	if err := c.Params().Valid(); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		P_ME_NA_1,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})
	if err := u.AppendInfoObjAddr(p.Ioa); err != nil {
		return err
	}
	u.AppendNormalize(p.Value)
	u.AppendBytes(p.Qpm.Value())
	return c.Send(u)
}

// ParameterScaledInfo Measured value parameter, scaled value Information body
type ParameterScaledInfo struct {
	Ioa   InfoObjAddr
	Value int16
	Qpm   QualifierOfParameterMV
}

// ParameterScaled Measured value parameter, scaled value, only single info object(SQ = 0)
// [P_ME_NB_1], See companion standard 101, subclass 7.3.5.2
// The reason for delivery (coa) is used for
// control direction:
// <6> := activation
// Monitoring direction:
// <7> := activation confirmation
// <20> := answer station call
// <21> := Answer Group 1 Call
// <22> := Answer Group 2 Call
// to
// <36> := Answer Group 16 Call
// <44> := unknown type identifier
// <45> := unknown delivery reason
// <46> := Unknown ASDU public address
// <47> := Unknown message object address
func ParameterScaled(c Connect, coa CauseOfTransmission, ca CommonAddr, p ParameterScaledInfo) error {
	if coa.Cause != Activation {
		return ErrCmdCause
	}
	if err := c.Params().Valid(); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		P_ME_NB_1,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})
	if err := u.AppendInfoObjAddr(p.Ioa); err != nil {
		return err
	}
	u.AppendScaled(p.Value).AppendBytes(p.Qpm.Value())
	return c.Send(u)
}

// ParameterFloatInfo Measurement parameters, short floats Message body
type ParameterFloatInfo struct {
	Ioa   InfoObjAddr
	Value float32
	Qpm   QualifierOfParameterMV
}

// ParameterFloat Measured value parameter, short float, only a single info object(SQ = 0)
// [P_ME_NC_1], See companion standard 101, subclass 7.3.5.3
// The reason for delivery (coa) is used for
// control direction:
// <6> := activation
// Monitoring direction:
// <7> := activation confirmation
// <20> := answer station call
// <21> := Answer Group 1 Call
// <22> := Answer Group 2 Call
// to
// <36> := Answer Group 16 Call
// <44> := unknown type identifier
// <45> := unknown delivery reason
// <46> := Unknown ASDU public address
// <47> := Unknown message object address
func ParameterFloat(c Connect, coa CauseOfTransmission, ca CommonAddr, p ParameterFloatInfo) error {
	if coa.Cause != Activation {
		return ErrCmdCause
	}
	if err := c.Params().Valid(); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		P_ME_NC_1,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})
	if err := u.AppendInfoObjAddr(p.Ioa); err != nil {
		return err
	}
	u.AppendFloat32(p.Value).AppendBytes(p.Qpm.Value())
	return c.Send(u)
}

// ParameterActivationInfo parameter activation message body
type ParameterActivationInfo struct {
	Ioa InfoObjAddr
	Qpa QualifierOfParameterAct
}

// ParameterActivation Parameter activation, only a single info object(SQ = 0)
// [P_AC_NA_1], See companion standard 101, subclass 7.3.5.4
// The reason for delivery (coa) is used for
// control direction:
// <6> := activation
// <8> := deactivate
// Monitoring direction:
// <7> := activation confirmation
// <9> := deactivation confirmation
// <44> := unknown type identifier
// <45> := unknown delivery reason
// <46> := Unknown ASDU public address
// <47> := Unknown message object address
func ParameterActivation(c Connect, coa CauseOfTransmission, ca CommonAddr, p ParameterActivationInfo) error {
	if !(coa.Cause == Activation || coa.Cause == Deactivation) {
		return ErrCmdCause
	}
	if err := c.Params().Valid(); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		P_AC_NA_1,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})
	if err := u.AppendInfoObjAddr(p.Ioa); err != nil {
		return err
	}
	u.AppendBytes(byte(p.Qpa))
	return c.Send(u)
}

// GetParameterNormal [P_ME_NA_1]，Get measured value parameters, scaled value information body
func (sf *ASDU) GetParameterNormal() ParameterNormalInfo {
	return ParameterNormalInfo{
		sf.DecodeInfoObjAddr(),
		sf.DecodeNormalize(),
		ParseQualifierOfParamMV(sf.InfoObj[0]),
	}
}

// GetParameterScaled [P_ME_NB_1]，Get measured value parameters, normalized value information body
func (sf *ASDU) GetParameterScaled() ParameterScaledInfo {
	return ParameterScaledInfo{
		sf.DecodeInfoObjAddr(),
		sf.DecodeScaled(),
		ParseQualifierOfParamMV(sf.InfoObj[0]),
	}
}

// GetParameterFloat [P_ME_NC_1]，Get measured value parameter, short floating point number information body
func (sf *ASDU) GetParameterFloat() ParameterFloatInfo {
	return ParameterFloatInfo{
		sf.DecodeInfoObjAddr(),
		sf.DecodeFloat32(),
		ParseQualifierOfParamMV(sf.InfoObj[0]),
	}
}

// GetParameterActivation [P_AC_NA_1]，Get parameter activation message body
func (sf *ASDU) GetParameterActivation() ParameterActivationInfo {
	return ParameterActivationInfo{
		sf.DecodeInfoObjAddr(),
		QualifierOfParameterAct(sf.InfoObj[0]),
	}
}

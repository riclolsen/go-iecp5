// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package asdu

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"
)

// Errors returned by ValidateRequest. Each wraps ErrInvalidRequest so that a
// caller can ask "is this request unacceptable at all?" with one errors.Is.
var (
	// ErrInvalidRequest is the parent of every error ValidateRequest returns.
	ErrInvalidRequest = errors.New("asdu: invalid request")
	// ErrSingleObjectExpected: a command carrying more than one information
	// object, or a sequence. Commands act on one object; a count above one is
	// at best a malformed sender and at worst an attempt to hit many objects
	// with one request.
	ErrSingleObjectExpected = fmt.Errorf("%w: a command must carry exactly one information object", ErrInvalidRequest)
	// ErrBroadcastNotAllowed: a command sent to the global common address
	// that is not one of the few that may be.
	ErrBroadcastNotAllowed = fmt.Errorf("%w: this command may not be broadcast", ErrInvalidRequest)
	// ErrQualifierReserved: a qualifier value the standard reserves.
	ErrQualifierReserved = fmt.Errorf("%w: reserved qualifier value", ErrInvalidRequest)
	// ErrCommandStateNotPermitted: a double or regulating-step command whose
	// state is 0 or 3, "not permitted".
	ErrCommandStateNotPermitted = fmt.Errorf("%w: command state 0 or 3 is not permitted", ErrInvalidRequest)
	// ErrFloatNotFinite: a floating point value that is NaN or infinite, as a
	// set-point or parameter.
	ErrFloatNotFinite = fmt.Errorf("%w: floating point value is not finite", ErrInvalidRequest)
	// ErrTimeTagInvalid: a time tag with the invalid bit set or fields out of range.
	ErrTimeTagInvalid = fmt.Errorf("%w: invalid time tag", ErrInvalidRequest)
)

// ValidateRequest checks a control-direction ASDU for what no conforming
// sender produces, and that decoding alone does not catch. It looks at the
// shape and the qualifiers; whether the addressed object exists, or whether
// the command is allowed, is for the application.
//
// It returns nil for types it has no rule for.
func ValidateRequest(a *ASDU) error {
	t := a.Type
	if !IsControlDirection(t) {
		return nil
	}
	if (IsProcessCommand(t) || IsSystemCommand(t)) && (a.Variable.Number != 1 || a.Variable.IsSequence) {
		return fmt.Errorf("%w: type %s with %d object(s), sequence=%v",
			ErrSingleObjectExpected, t, a.Variable.Number, a.Variable.IsSequence)
	}
	if a.CommonAddr == GlobalCommonAddr && (IsProcessCommand(t) || IsParameter(t) || (IsSystemCommand(t) && !IsBroadcastAllowed(t))) {
		return fmt.Errorf("%w: type %s", ErrBroadcastNotAllowed, t)
	}

	ioa := a.InfoObjAddrSize
	body := a.InfoObj
	if len(body) < ioa {
		return nil // size problems are reported by the decoder
	}
	body = body[ioa:]
	at := func(i int) (byte, bool) {
		if i < len(body) {
			return body[i], true
		}
		return 0, false
	}

	switch t {
	case C_IC_NA_1:
		if q, ok := at(0); ok && !(q == 0 || (q >= 20 && q <= 36)) && !(q >= 64) {
			return fmt.Errorf("%w: interrogation qualifier %d", ErrQualifierReserved, q)
		}
	case C_CI_NA_1:
		if q, ok := at(0); ok && q&0x3f > 5 {
			return fmt.Errorf("%w: counter interrogation request %d", ErrQualifierReserved, q&0x3f)
		}
	case C_RP_NA_1:
		if q, ok := at(0); ok && (q == 0 || (q >= 3 && q < 128)) {
			return fmt.Errorf("%w: reset process qualifier %d", ErrQualifierReserved, q)
		}
	case C_DC_NA_1, C_DC_TA_1, C_RC_NA_1, C_RC_TA_1:
		if b, ok := at(0); ok {
			if s := b & 0x03; s == 0 || s == 3 {
				return fmt.Errorf("%w: type %s state %d", ErrCommandStateNotPermitted, t, s)
			}
		}
		fallthrough
	case C_SC_NA_1, C_SC_TA_1:
		if b, ok := at(0); ok {
			if qu := (b >> 2) & 0x1f; qu >= 4 && qu <= 15 {
				return fmt.Errorf("%w: command qualifier %d", ErrQualifierReserved, qu)
			}
		}
	case C_SE_NC_1, C_SE_TC_1, P_ME_NC_1:
		if len(body) >= 4 {
			f := math.Float32frombits(binary.LittleEndian.Uint32(body[:4]))
			if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
				return fmt.Errorf("%w: type %s", ErrFloatNotFinite, t)
			}
		}
	}

	if IsTimeTaggedCommand(t) && len(body) >= 7 {
		if err := checkCP56(body[len(body)-7:]); err != nil {
			return fmt.Errorf("%w: %v", ErrTimeTagInvalid, err)
		}
	}
	if t == C_CS_NA_1 && len(body) >= 7 {
		if err := checkCP56(body[:7]); err != nil {
			return fmt.Errorf("%w: %v", ErrTimeTagInvalid, err)
		}
	}
	return nil
}

// checkCP56 reports what is wrong with a CP56Time2a, if anything.
func checkCP56(b []byte) error {
	if len(b) < 7 {
		return errors.New("short")
	}
	switch {
	case b[2]&0x80 != 0:
		return errors.New("invalid bit set")
	case binary.LittleEndian.Uint16(b[0:2]) > 59999:
		return errors.New("milliseconds out of range")
	case b[2]&0x3f > 59:
		return errors.New("minute out of range")
	case b[3]&0x1f > 23:
		return errors.New("hour out of range")
	case b[4]&0x1f < 1 || b[4]&0x1f > 31:
		return errors.New("day out of range")
	case b[5]&0x0f < 1 || b[5]&0x0f > 12:
		return errors.New("month out of range")
	}
	return nil
}

// CommandTimeTag returns the CP56Time2a of a time-tagged command, which is
// always the last seven octets of its single information object.
func CommandTimeTag(a *ASDU) (time.Time, bool) {
	if !IsTimeTaggedCommand(a.Type) || len(a.InfoObj) < a.InfoObjAddrSize+7 {
		return time.Time{}, false
	}
	b := a.InfoObj[len(a.InfoObj)-7:]
	if checkCP56(b) != nil {
		return time.Time{}, false
	}
	return ParseCP56Time2a(b, a.InfoObjTimeZone), true
}

// FirstIOA returns the address of the first information object, when the
// ASDU has one.
func FirstIOA(a *ASDU) (InfoObjAddr, bool) {
	n := a.InfoObjAddrSize
	if n == 0 || len(a.InfoObj) < n {
		return 0, false
	}
	var v InfoObjAddr
	for i := n - 1; i >= 0; i-- {
		v = v<<8 | InfoObjAddr(a.InfoObj[i])
	}
	return v, true
}

// DescribeCommand renders what a command asks for, in a few words, for an
// audit trail: "Execute:ON", "Select:UP", "Execute:22.5".
func DescribeCommand(a *ASDU) (s string) {
	defer func() {
		if recover() != nil {
			s = ""
		}
	}()
	switch a.Type {
	case C_SC_NA_1, C_SC_TA_1:
		c := a.GetSingleCmd()
		return fmt.Sprintf("%s value=%v", c.Qoc, c.Value)
	case C_DC_NA_1, C_DC_TA_1:
		c := a.GetDoubleCmd()
		return fmt.Sprintf("%s value=%d", c.Qoc, c.Value)
	case C_RC_NA_1, C_RC_TA_1:
		c := a.GetStepCmd()
		return fmt.Sprintf("%s value=%d", c.Qoc, c.Value)
	case C_SE_NA_1, C_SE_TA_1:
		c := a.GetSetpointNormalCmd()
		return fmt.Sprintf("%s value=%d", c.Qos, c.Value)
	case C_SE_NB_1, C_SE_TB_1:
		c := a.GetSetpointCmdScaled()
		return fmt.Sprintf("%s value=%d", c.Qos, c.Value)
	case C_SE_NC_1, C_SE_TC_1:
		c := a.GetSetpointFloatCmd()
		return fmt.Sprintf("%s value=%g", c.Qos, c.Value)
	case C_BO_NA_1, C_BO_TA_1:
		c := a.GetBitsString32Cmd()
		return fmt.Sprintf("value=0x%08x", c.Value)
	}
	return ""
}

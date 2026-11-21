// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package asdu

// Classification of type identifications, for code that has to decide
// whether an ASDU has any business arriving where it did. They are tables,
// not ad-hoc switches, so every caller agrees.

// IsMonitorDirection reports whether t is sent from the controlled station
// (outstation) to the controlling station: process information, end of
// initialisation, and the monitor-direction half of file transfer.
func IsMonitorDirection(t TypeID) bool {
	switch {
	case t >= M_SP_NA_1 && t <= M_ME_ND_1:
		return true
	case t >= M_SP_TB_1 && t <= S_IT_TC_1:
		return true
	case t == M_EI_NA_1:
		return true
	case t == F_FR_NA_1, t == F_SR_NA_1, t == F_LS_NA_1, t == F_SG_NA_1, t == F_DR_TA_1:
		return true
	}
	return false
}

// IsControlDirection reports whether t is sent from the controlling station
// to the controlled station: commands, system commands, parameters and the
// control-direction half of file transfer.
func IsControlDirection(t TypeID) bool {
	switch {
	case t >= C_SC_NA_1 && t <= C_BO_NA_1:
		return true
	case t >= C_SC_TA_1 && t <= C_BO_TA_1:
		return true
	case t >= C_IC_NA_1 && t <= C_TS_TA_1:
		return true
	case t >= P_ME_NA_1 && t <= P_AC_NA_1:
		return true
	case t == F_SC_NA_1, t == F_AF_NA_1, t == F_SC_NB_1:
		return true
	}
	return false
}

// IsProcessCommand reports whether t is a command that operates the process:
// single, double, regulating step, set-point or bit string, with or without
// a time tag.
func IsProcessCommand(t TypeID) bool {
	return (t >= C_SC_NA_1 && t <= C_BO_NA_1) || (t >= C_SC_TA_1 && t <= C_BO_TA_1)
}

// IsSystemCommand reports whether t is a command in the system range
// (interrogation, read, clock, test, reset, delay).
func IsSystemCommand(t TypeID) bool { return t >= C_IC_NA_1 && t <= C_TS_TA_1 }

// IsParameter reports whether t loads or activates a parameter.
func IsParameter(t TypeID) bool { return t >= P_ME_NA_1 && t <= P_AC_NA_1 }

// IsFile reports whether t belongs to file transfer.
func IsFile(t TypeID) bool { return t >= F_FR_NA_1 && t <= F_SC_NB_1 }

// IsSecurity reports whether t belongs to the IEC 62351-5 authentication set.
func IsSecurity(t TypeID) bool {
	return (t >= S_CH_NA_1 && t <= S_ER_NA_1) || (t >= S_US_NA_1 && t <= S_UC_NA_1)
}

// IsTimeTaggedCommand reports whether t is a command carrying CP56Time2a.
func IsTimeTaggedCommand(t TypeID) bool {
	return (t >= C_SC_TA_1 && t <= C_BO_TA_1) || t == C_TS_TA_1
}

// IsBroadcastAllowed reports whether a command of type t may be sent to the
// global common address. Only the system commands that every station can
// act on without answering qualify; operating the process through a
// broadcast is never legitimate.
func IsBroadcastAllowed(t TypeID) bool {
	switch t {
	case C_IC_NA_1, C_CI_NA_1, C_CS_NA_1, C_RP_NA_1:
		return true
	}
	return false
}

// IsPrivate reports whether t is in the range reserved for special use.
func IsPrivate(t TypeID) bool { return t >= 128 }

// InProfile104 reports whether t is among the standard ASDUs IEC 60870-5-104
// selects from the 101 set. Types outside it are not malformed; a conforming
// 104 peer simply does not send them. Types with CP24Time2a time tags, and
// the test and delay commands that 104 replaces with C_TS_TA_1, are outside.
func InProfile104(t TypeID) bool {
	switch {
	case t == M_SP_NA_1, t == M_DP_NA_1, t == M_ST_NA_1, t == M_BO_NA_1,
		t == M_ME_NA_1, t == M_ME_NB_1, t == M_ME_NC_1, t == M_IT_NA_1,
		t == M_PS_NA_1, t == M_ME_ND_1:
		return true
	case t >= M_SP_TB_1 && t <= M_EP_TF_1:
		return true
	case t >= C_SC_NA_1 && t <= C_BO_NA_1, t >= C_SC_TA_1 && t <= C_BO_TA_1:
		return true
	case t == M_EI_NA_1:
		return true
	case t == C_IC_NA_1, t == C_CI_NA_1, t == C_RD_NA_1, t == C_CS_NA_1, t == C_RP_NA_1, t == C_TS_TA_1:
		return true
	case IsParameter(t), IsFile(t):
		return true
	case IsSecurity(t):
		return true
	}
	return false
}

// HasConfirmationCause reports whether c is a cause only a controlled
// station uses in answer to a command.
func HasConfirmationCause(c Cause) bool {
	return c == ActivationCon || c == DeactivationCon || c == ActivationTerm
}

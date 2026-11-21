// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs104

import (
	"errors"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/internal/secmon"
	"github.com/riclolsen/go-iecp5/secevent"
)

// buckets are a session's rate limiters, one per kind of traffic that can be
// turned against the outstation. They belong to the goroutine that uses
// them: the run loop for frames, the handler loop for requests.
type buckets struct {
	commands, interrogation, counter, read, clock, reset *secmon.Bucket // handler loop
	testfr, utoggle, sframes, frames                     *secmon.Bucket // run loop
}

func newBuckets(l secevent.Limits) *buckets {
	b := func(r secevent.Rate) *secmon.Bucket { return secmon.NewBucket(r.N, r.Per, 0) }
	return &buckets{
		commands: b(l.Commands), interrogation: b(l.Interrogation), counter: b(l.CounterInterrogation),
		read: b(l.Read), clock: b(l.ClockSync), reset: b(l.ResetProcess),
		testfr: b(l.TestFR), utoggle: b(l.UToggle), sframes: b(l.SFrames), frames: b(l.Frames),
	}
}

// asduEvent starts an event describing a, with the fields every one of them has.
func asduEvent(kind secevent.Kind, a *asdu.ASDU, act secevent.Action, detail string) secevent.Event {
	e := secevent.Event{
		Kind: kind, Action: act, Detail: detail,
		Fields: secevent.FieldType | secevent.FieldCause | secevent.FieldCommonAddr | secevent.FieldOrigAddr,
		TypeID: a.Type, Cause: a.Coa.Cause, Negative: a.Coa.IsNegative, Test: a.Coa.IsTest,
		OrigAddr: a.OrigAddr, CommonAddr: a.CommonAddr,
	}
	if ioa, ok := asdu.FirstIOA(a); ok {
		e.Fields |= secevent.FieldIOA
		e.IOA = ioa
	}
	return e
}

func unknownKey(a *asdu.ASDU) uint64 {
	h := fnv.New64a()
	ioa, _ := asdu.FirstIOA(a)
	_, _ = fmt.Fprintf(h, "%d/%d/%d", a.Type, a.CommonAddr, ioa)
	return h.Sum64()
}

// noteUnknown feeds the scan detector: one session asking for many things
// that do not exist is enumerating the station.
func (sf *SrvSession) noteUnknown(a *asdu.ASDU) {
	if sf.scan == nil {
		return
	}
	if sf.scan.Note(unknownKey(a), time.Now()) {
		sf.emit(asduEvent(secevent.KindAccessScan, a, secevent.ActObserved,
			fmt.Sprintf("%d or more distinct unknown objects requested within %v: the station is being enumerated",
				sf.pol.ScanThreshold, sf.pol.ScanWindow)))
	}
}

// negativeConfirm refuses a command the way the standard says: the same
// ASDU back, with the negative bit set and a confirmation cause.
func (sf *SrvSession) negativeConfirm(a *asdu.ASDU) {
	r := asdu.NewASDU(sf.params, a.Identifier)
	r.Coa.Cause = asdu.ActivationCon
	if a.Coa.Cause == asdu.Deactivation {
		r.Coa.Cause = asdu.DeactivationCon
	}
	r.Coa.IsNegative = true
	r.InfoObj = append(r.InfoObj, a.InfoObj...)
	_ = sf.send(r, false)
}

func (sf *SrvSession) replyCause(a *asdu.ASDU, c asdu.Cause) {
	r := asdu.NewASDU(sf.params, a.Identifier)
	r.Coa.Cause = c
	r.InfoObj = append(r.InfoObj, a.InfoObj...)
	_ = sf.send(r, false)
}

// refuse answers a request that policy keeps from the handler.
func (sf *SrvSession) refuse(a *asdu.ASDU, unknown asdu.Cause) {
	if unknown != 0 {
		sf.replyCause(a, unknown)
		return
	}
	if asdu.IsControlDirection(a.Type) && a.Coa.Cause != asdu.Request {
		sf.negativeConfirm(a)
		return
	}
	sf.replyCause(a, asdu.UnknownTypeID)
}

// flood checks one bucket and reports the excess. It returns true when the
// request must not reach the handler.
func (sf *SrvSession) flood(b *secmon.Bucket, kind secevent.Kind, a *asdu.ASDU, what string) (drop bool) {
	if b.Allow(time.Now()) {
		return false
	}
	act := secevent.ActObserved
	switch sf.pol.FloodAction {
	case secevent.ActThrottled:
		act = secevent.ActThrottled
	case secevent.ActDisconnected:
		act = secevent.ActDisconnected
	}
	sf.emit(asduEvent(kind, a, act, what+" exceeds the rate limit"))
	switch act {
	case secevent.ActThrottled:
		sf.refuse(a, 0)
		return true
	case secevent.ActDisconnected:
		sf.noteEnd("flood: " + string(kind))
		sf.cancel()
		return true
	}
	return false
}

// inspect examines a decoded request before the handler sees it. It raises
// the security events and returns true when policy says the handler must not
// be called (a refusal has then already been sent).
func (sf *SrvSession) inspect(a *asdu.ASDU, raw []byte) (block bool) {
	if !sf.cyberOn() {
		return false
	}
	t := a.Type
	ev := func(kind secevent.Kind, act secevent.Action, detail string, err error) {
		e := asduEvent(kind, a, act, detail)
		e.Err, e.Raw = err, raw
		sf.emit(e)
	}

	// Is it something a 104 peer sends, and in the direction it should go?
	switch {
	case asdu.IsSecurity(t):
		ev(secevent.KindProtoSecureAuth, secevent.ActObserved, "IEC 62351-5 authentication ASDU: this endpoint does not implement it", nil)
	case !asdu.InProfile104(t):
		if !(asdu.IsPrivate(t) && sf.pol.AllowPrivateTypes) {
			ev(secevent.KindProtoNotInProfile, secevent.ActObserved,
				fmt.Sprintf("type %s is not among the ASDUs IEC 60870-5-104 selects", t), nil)
		}
	}
	if asdu.IsFile(t) && asdu.IsMonitorDirection(t) {
		ev(secevent.KindFileDownloadAttempt, secevent.ActObserved,
			"the master is pushing a file (control-direction file transfer): a download to this station", nil)
	} else if asdu.IsMonitorDirection(t) {
		ev(secevent.KindProtoWrongDirection, secevent.ActObserved,
			fmt.Sprintf("monitor-direction type %s received by the controlled station", t), nil)
	} else if asdu.IsControlDirection(t) && asdu.HasConfirmationCause(a.Coa.Cause) {
		ev(secevent.KindProtoWrongDirection, secevent.ActObserved,
			fmt.Sprintf("confirmation cause %d on a command received by the controlled station", a.Coa.Cause), nil)
	}

	// Is it well formed beyond what decoding checks?
	if err := asdu.ValidateRequest(a); err != nil {
		switch {
		case errors.Is(err, asdu.ErrSingleObjectExpected):
			ev(secevent.KindAccessOversized, secevent.ActObserved, err.Error(), err)
		case errors.Is(err, asdu.ErrBroadcastNotAllowed):
			ev(secevent.KindAccessBroadcastMisuse, secevent.ActObserved, err.Error(), err)
		default:
			ev(secevent.KindProtoFieldInvalid, secevent.ActObserved, err.Error(), err)
		}
	}

	if len(sf.pol.AllowedOriginators) > 0 && asdu.IsControlDirection(t) {
		ok := false
		for _, o := range sf.pol.AllowedOriginators {
			ok = ok || o == a.OrigAddr
		}
		if !ok {
			ev(secevent.KindAccessOriginator, secevent.ActObserved, fmt.Sprintf("originator address %d is not expected", a.OrigAddr), nil)
		}
	}

	// The audit trail: what operators need on record.
	if !sf.pol.DisableAudit {
		sf.audit(a, raw)
	}

	// Does the station or object exist?
	if sp := sf.pol.AddressSpace; sp != nil && asdu.IsControlDirection(t) && a.CommonAddr != asdu.GlobalCommonAddr {
		switch {
		case !sp.KnownCA(a.CommonAddr):
			act := secevent.ActObserved
			if sf.pol.AutoRejectUnknown {
				act = secevent.ActRejected
			}
			ev(secevent.KindAccessUnknownCA, act, fmt.Sprintf("common address %d does not exist", a.CommonAddr), nil)
			sf.noteUnknown(a)
			if sf.pol.AutoRejectUnknown {
				sf.replyCause(a, asdu.UnknownCA)
				return true
			}
		case !asdu.IsSystemCommand(t) || t == asdu.C_RD_NA_1:
			if ioa, ok := asdu.FirstIOA(a); ok && !sp.KnownIOA(a.CommonAddr, t, ioa) {
				act := secevent.ActObserved
				if sf.pol.AutoRejectUnknown {
					act = secevent.ActRejected
				}
				ev(secevent.KindAccessUnknownIOA, act, fmt.Sprintf("object %d does not exist under common address %d", ioa, a.CommonAddr), nil)
				sf.noteUnknown(a)
				if sf.pol.AutoRejectUnknown {
					sf.replyCause(a, asdu.UnknownIOA)
					return true
				}
			}
		}
	}

	// Is the function allowed here at all?
	if why, denied := sf.pol.Functions.Denied(t, a.CommonAddr); denied {
		ev(secevent.KindAccessTypeBlocked, secevent.ActRejected, why, nil)
		if sf.pol.Functions.DenyWithUnknownType {
			sf.replyCause(a, asdu.UnknownTypeID)
		} else {
			sf.refuse(a, 0)
		}
		return true
	}

	// Replay and age of commands.
	if asdu.IsTimeTaggedCommand(t) && (sf.pol.MaxCommandAge > 0 || sf.pol.MaxCommandFuture > 0) {
		if tm, ok := asdu.CommandTimeTag(a); ok {
			now := time.Now()
			var why string
			switch {
			case sf.pol.MaxCommandAge > 0 && now.Sub(tm) > sf.pol.MaxCommandAge:
				why = fmt.Sprintf("command time tag %s is %v old, over the limit %v", tm.Format(time.RFC3339), now.Sub(tm).Round(time.Second), sf.pol.MaxCommandAge)
			case sf.pol.MaxCommandFuture > 0 && tm.Sub(now) > sf.pol.MaxCommandFuture:
				why = fmt.Sprintf("command time tag %s is %v in the future, over the limit %v", tm.Format(time.RFC3339), tm.Sub(now).Round(time.Second), sf.pol.MaxCommandFuture)
			}
			if why != "" {
				act := secevent.ActObserved
				if sf.pol.RejectStaleCommands {
					act = secevent.ActRejected
				}
				ev(secevent.KindCmdStaleTimeTag, act, why+": a replayed command, or clocks out of step", nil)
				if sf.pol.RejectStaleCommands {
					sf.negativeConfirm(a)
					return true
				}
			}
		}
	}
	if (asdu.IsProcessCommand(t) || t == asdu.C_RP_NA_1 || asdu.IsParameter(t)) && sf.guard != nil && sf.pol.DuplicateWindow > 0 {
		key := fmt.Sprintf("%d/%d/%d/%x", t, a.CommonAddr, a.Coa.Cause, a.InfoObj)
		if sf.guard.SeenCommand(key, time.Now()) {
			ev(secevent.KindCmdDuplicate, secevent.ActObserved,
				fmt.Sprintf("an identical command was seen within %v: a replay, here or on another connection", sf.pol.DuplicateWindow), nil)
		}
	}

	// Rates.
	if sf.bk != nil {
		switch {
		case asdu.IsProcessCommand(t):
			if sf.flood(sf.bk.commands, secevent.KindFloodCommands, a, "process command rate") {
				return true
			}
		case t == asdu.C_IC_NA_1:
			if sf.flood(sf.bk.interrogation, secevent.KindFloodInterrogation, a, "interrogation rate") {
				return true
			}
		case t == asdu.C_CI_NA_1:
			if sf.flood(sf.bk.counter, secevent.KindFloodCounterInterr, a, "counter interrogation rate") {
				return true
			}
		case t == asdu.C_RD_NA_1:
			if sf.flood(sf.bk.read, secevent.KindFloodRead, a, "read rate") {
				return true
			}
		case t == asdu.C_CS_NA_1:
			if sf.flood(sf.bk.clock, secevent.KindFloodClockSync, a, "clock synchronisation rate") {
				return true
			}
		case t == asdu.C_RP_NA_1:
			if sf.flood(sf.bk.reset, secevent.KindFloodResetProcess, a, "reset process rate") {
				return true
			}
		}
	}
	return false
}

// audit raises the events that record control functions.
func (sf *SrvSession) audit(a *asdu.ASDU, raw []byte) {
	t := a.Type
	emit := func(kind secevent.Kind, value, detail string) {
		e := asduEvent(kind, a, secevent.ActObserved, detail)
		e.Value, e.Raw = value, raw
		sf.emit(e)
	}
	switch {
	case asdu.IsProcessCommand(t):
		if a.Coa.Cause == asdu.Activation || a.Coa.Cause == asdu.Deactivation {
			emit(secevent.KindCmdReceived, asdu.DescribeCommand(a), "process command received")
		}
	case t == asdu.C_IC_NA_1:
		_, q := a.GetInterrogationCmd()
		emit(secevent.KindCtrlInterrogation, fmt.Sprintf("QOI=%d", q), "interrogation command")
	case t == asdu.C_CI_NA_1:
		_, q := a.GetCounterInterrogationCmd()
		emit(secevent.KindCtrlInterrogation, fmt.Sprintf("QCC=%v", q), "counter interrogation command")
	case t == asdu.C_RP_NA_1:
		_, q := a.GetResetProcessCmd()
		emit(secevent.KindCtrlResetProcess, fmt.Sprintf("QRP=%d", q), "RESET PROCESS command: a restart of the station's process")
	case t == asdu.C_CS_NA_1:
		_, tm := a.GetClockSynchronizationCmd()
		delta := time.Until(tm)
		emit(secevent.KindCtrlClockSync, tm.Format(time.RFC3339Nano), fmt.Sprintf("clock synchronisation, %v from the local clock", delta.Round(time.Millisecond)))
		if th := sf.pol.ClockStepThreshold; th > 0 && (delta > th || -delta > th) {
			emit(secevent.KindCtrlClockStep, delta.Round(time.Millisecond).String(),
				fmt.Sprintf("the clock would move by %v, over %v: time manipulation defeats time-tag based defences", delta.Round(time.Second), th))
		}
	case t == asdu.C_TS_NA_1 || t == asdu.C_TS_TA_1:
		emit(secevent.KindCtrlTestCommand, "", "test command")
	case t == asdu.C_CD_NA_1:
		emit(secevent.KindCtrlDelayAcq, "", "delay acquisition command")
	case t == asdu.P_AC_NA_1:
		emit(secevent.KindCtrlParamActivate, "", "parameter activation")
	case asdu.IsParameter(t):
		if a.Coa.Cause == asdu.Activation {
			emit(secevent.KindCtrlParamChange, "", "parameter of a measured value is being changed")
		}
	}
}

// observeOutbound turns the replies a handler sends into security events:
// a refusal says something about what the peer asked for.
func (sf *SrvSession) observeOutbound(u *asdu.ASDU) {
	if !sf.cyberOn() {
		return
	}
	switch u.Coa.Cause {
	case asdu.UnknownTypeID:
		sf.emit(asduEvent(secevent.KindAccessUnknownType, u, secevent.ActRejected, "request for an unknown type or function refused"))
		sf.noteUnknown(u)
	case asdu.UnknownCOT:
		sf.emit(asduEvent(secevent.KindProtoUnknownCOT, u, secevent.ActRejected, "request with an unexpected cause of transmission refused"))
	case asdu.UnknownCA:
		sf.emit(asduEvent(secevent.KindAccessUnknownCA, u, secevent.ActRejected, "request for an unknown common address refused"))
		sf.noteUnknown(u)
	case asdu.UnknownIOA:
		sf.emit(asduEvent(secevent.KindAccessUnknownIOA, u, secevent.ActRejected, "request for an unknown information object refused"))
		sf.noteUnknown(u)
	case asdu.ActivationCon, asdu.DeactivationCon:
		if u.Coa.IsNegative && asdu.IsControlDirection(u.Type) {
			e := asduEvent(secevent.KindCmdRejected, u, secevent.ActRejected, "command refused (negative confirmation sent)")
			if asdu.IsProcessCommand(u.Type) {
				e.Value = asdu.DescribeCommand(u)
			}
			sf.emit(e)
			sf.noteRejected(u)
		}
	}
}

// noteRejected escalates a run of refused commands: someone is trying
// values until one is accepted.
func (sf *SrvSession) noteRejected(u *asdu.ASDU) {
	if sf.rejects == nil {
		sf.rejects = secmon.NewBucket(10, time.Minute, 0)
	}
	if !sf.rejects.Allow(time.Now()) {
		sf.emit(asduEvent(secevent.KindCmdBruteForce, u, secevent.ActObserved,
			"more than 10 commands refused within a minute: probing for an accepted command"))
	}
}

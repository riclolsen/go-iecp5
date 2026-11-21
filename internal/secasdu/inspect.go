// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

// Package secasdu is the application-layer security inspection shared by the
// protocol endpoints: it examines ASDUs as they arrive and as they are
// answered, raises the security events, and applies the request policy.
package secasdu

import (
	"errors"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/internal/secmon"
	"github.com/riclolsen/go-iecp5/secevent"
)

// Host is what the inspector needs from the endpoint it serves.
type Host interface {
	// Emit raises a security event, completed with the peer and session.
	Emit(secevent.Event)
	// Enabled reports whether events are being collected.
	Enabled() bool
	// NegativeConfirm refuses a command with a negative confirmation.
	NegativeConfirm(a *asdu.ASDU)
	// ReplyCause answers a request with a cause such as UnknownIOA.
	ReplyCause(a *asdu.ASDU, c asdu.Cause)
	// Disconnect ends the session for a reason.
	Disconnect(reason string)
}

type buckets struct {
	commands, interrogation, counter, read, clock, reset *secmon.Bucket
}

// FrameLimits are the rate limiters of the frame layer, owned by the
// endpoint's protocol goroutine.
type FrameLimits struct {
	TestFR, UToggle, SFrames, Frames *secmon.Bucket
}

// NewFrameLimits builds the frame-layer limiters of a policy.
func NewFrameLimits(l secevent.Limits) *FrameLimits {
	b := func(r secevent.Rate) *secmon.Bucket { return secmon.NewBucket(r.N, r.Per, 0) }
	return &FrameLimits{TestFR: b(l.TestFR), UToggle: b(l.UToggle), SFrames: b(l.SFrames), Frames: b(l.Frames)}
}

// Inspector examines the ASDUs of one session. It belongs to the goroutine
// that handles that session's ASDUs.
type Inspector struct {
	host    Host
	pol     secevent.Policy
	guard   *secevent.Guard
	bk      *buckets
	scan    *secmon.Scan
	rejects *secmon.Bucket
}

// New creates the inspector of a session. guard may be nil.
func New(h Host, g *secevent.Guard, pol secevent.Policy) *Inspector {
	b := func(r secevent.Rate) *secmon.Bucket { return secmon.NewBucket(r.N, r.Per, 0) }
	l := pol.Limits
	return &Inspector{
		host: h, pol: pol, guard: g,
		bk: &buckets{commands: b(l.Commands), interrogation: b(l.Interrogation), counter: b(l.CounterInterrogation),
			read: b(l.Read), clock: b(l.ClockSync), reset: b(l.ResetProcess)},
		scan: secmon.NewScan(pol.ScanThreshold, pol.ScanWindow),
	}
}

// Policy is the policy the inspector applies.
func (in *Inspector) Policy() secevent.Policy { return in.pol }

// asduEvent starts an event describing a, with the fields every one of them has.
func Event(kind secevent.Kind, a *asdu.ASDU, act secevent.Action, detail string) secevent.Event {
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
func (in *Inspector) noteUnknown(a *asdu.ASDU) {
	if in.scan == nil {
		return
	}
	if in.scan.Note(unknownKey(a), time.Now()) {
		in.host.Emit(Event(secevent.KindAccessScan, a, secevent.ActObserved,
			fmt.Sprintf("%d or more distinct unknown objects requested within %v: the station is being enumerated",
				in.pol.ScanThreshold, in.pol.ScanWindow)))
	}
}

// refuse answers a request that policy keeps from the handler.
func (in *Inspector) refuse(a *asdu.ASDU, unknown asdu.Cause) {
	if unknown != 0 {
		in.host.ReplyCause(a, unknown)
		return
	}
	if asdu.IsControlDirection(a.Type) && a.Coa.Cause != asdu.Request {
		in.host.NegativeConfirm(a)
		return
	}
	in.host.ReplyCause(a, asdu.UnknownTypeID)
}

// flood checks one bucket and reports the excess. It returns true when the
// request must not reach the handler.
func (in *Inspector) flood(b *secmon.Bucket, kind secevent.Kind, a *asdu.ASDU, what string) (drop bool) {
	if b.Allow(time.Now()) {
		return false
	}
	act := secevent.ActObserved
	switch in.pol.FloodAction {
	case secevent.ActThrottled:
		act = secevent.ActThrottled
	case secevent.ActDisconnected:
		act = secevent.ActDisconnected
	}
	in.host.Emit(Event(kind, a, act, what+" exceeds the rate limit"))
	switch act {
	case secevent.ActThrottled:
		in.refuse(a, 0)
		return true
	case secevent.ActDisconnected:
		in.host.Disconnect("flood: " + string(kind))
		return true
	}
	return false
}

// inspect examines a decoded request before the handler sees it. It raises
// the security events and returns true when policy says the handler must not
// be called (a refusal has then already been sent).
func (in *Inspector) Inspect(a *asdu.ASDU, raw []byte) (block bool) {
	if !in.host.Enabled() {
		return false
	}
	t := a.Type
	ev := func(kind secevent.Kind, act secevent.Action, detail string, err error) {
		e := Event(kind, a, act, detail)
		e.Err, e.Raw = err, raw
		in.host.Emit(e)
	}

	// Is it something a 104 peer sends, and in the direction it should go?
	switch {
	case asdu.IsSecurity(t):
		ev(secevent.KindProtoSecureAuth, secevent.ActObserved, "IEC 62351-5 authentication ASDU: this endpoint does not implement it", nil)
	case !asdu.InProfile104(t):
		if !(asdu.IsPrivate(t) && in.pol.AllowPrivateTypes) {
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

	if len(in.pol.AllowedOriginators) > 0 && asdu.IsControlDirection(t) {
		ok := false
		for _, o := range in.pol.AllowedOriginators {
			ok = ok || o == a.OrigAddr
		}
		if !ok {
			ev(secevent.KindAccessOriginator, secevent.ActObserved, fmt.Sprintf("originator address %d is not expected", a.OrigAddr), nil)
		}
	}

	// The audit trail: what operators need on record.
	if !in.pol.DisableAudit {
		in.audit(a, raw)
	}

	// Does the station or object exist?
	if sp := in.pol.AddressSpace; sp != nil && asdu.IsControlDirection(t) && a.CommonAddr != asdu.GlobalCommonAddr {
		switch {
		case !sp.KnownCA(a.CommonAddr):
			act := secevent.ActObserved
			if in.pol.AutoRejectUnknown {
				act = secevent.ActRejected
			}
			ev(secevent.KindAccessUnknownCA, act, fmt.Sprintf("common address %d does not exist", a.CommonAddr), nil)
			in.noteUnknown(a)
			if in.pol.AutoRejectUnknown {
				in.host.ReplyCause(a, asdu.UnknownCA)
				return true
			}
		case !asdu.IsSystemCommand(t) || t == asdu.C_RD_NA_1:
			if ioa, ok := asdu.FirstIOA(a); ok && !sp.KnownIOA(a.CommonAddr, t, ioa) {
				act := secevent.ActObserved
				if in.pol.AutoRejectUnknown {
					act = secevent.ActRejected
				}
				ev(secevent.KindAccessUnknownIOA, act, fmt.Sprintf("object %d does not exist under common address %d", ioa, a.CommonAddr), nil)
				in.noteUnknown(a)
				if in.pol.AutoRejectUnknown {
					in.host.ReplyCause(a, asdu.UnknownIOA)
					return true
				}
			}
		}
	}

	// Is the function allowed here at all?
	if why, denied := in.pol.Functions.Denied(t, a.CommonAddr); denied {
		ev(secevent.KindAccessTypeBlocked, secevent.ActRejected, why, nil)
		if in.pol.Functions.DenyWithUnknownType {
			in.host.ReplyCause(a, asdu.UnknownTypeID)
		} else {
			in.refuse(a, 0)
		}
		return true
	}

	// Replay and age of commands.
	if asdu.IsTimeTaggedCommand(t) && (in.pol.MaxCommandAge > 0 || in.pol.MaxCommandFuture > 0) {
		if tm, ok := asdu.CommandTimeTag(a); ok {
			now := time.Now()
			var why string
			switch {
			case in.pol.MaxCommandAge > 0 && now.Sub(tm) > in.pol.MaxCommandAge:
				why = fmt.Sprintf("command time tag %s is %v old, over the limit %v", tm.Format(time.RFC3339), now.Sub(tm).Round(time.Second), in.pol.MaxCommandAge)
			case in.pol.MaxCommandFuture > 0 && tm.Sub(now) > in.pol.MaxCommandFuture:
				why = fmt.Sprintf("command time tag %s is %v in the future, over the limit %v", tm.Format(time.RFC3339), tm.Sub(now).Round(time.Second), in.pol.MaxCommandFuture)
			}
			if why != "" {
				act := secevent.ActObserved
				if in.pol.RejectStaleCommands {
					act = secevent.ActRejected
				}
				ev(secevent.KindCmdStaleTimeTag, act, why+": a replayed command, or clocks out of step", nil)
				if in.pol.RejectStaleCommands {
					in.host.NegativeConfirm(a)
					return true
				}
			}
		}
	}
	if (asdu.IsProcessCommand(t) || t == asdu.C_RP_NA_1 || asdu.IsParameter(t)) && in.guard != nil && in.pol.DuplicateWindow > 0 {
		key := fmt.Sprintf("%d/%d/%d/%x", t, a.CommonAddr, a.Coa.Cause, a.InfoObj)
		if in.guard.SeenCommand(key, time.Now()) {
			ev(secevent.KindCmdDuplicate, secevent.ActObserved,
				fmt.Sprintf("an identical command was seen within %v: a replay, here or on another connection", in.pol.DuplicateWindow), nil)
		}
	}

	// Rates.
	if in.bk != nil {
		switch {
		case asdu.IsProcessCommand(t):
			if in.flood(in.bk.commands, secevent.KindFloodCommands, a, "process command rate") {
				return true
			}
		case t == asdu.C_IC_NA_1:
			if in.flood(in.bk.interrogation, secevent.KindFloodInterrogation, a, "interrogation rate") {
				return true
			}
		case t == asdu.C_CI_NA_1:
			if in.flood(in.bk.counter, secevent.KindFloodCounterInterr, a, "counter interrogation rate") {
				return true
			}
		case t == asdu.C_RD_NA_1:
			if in.flood(in.bk.read, secevent.KindFloodRead, a, "read rate") {
				return true
			}
		case t == asdu.C_CS_NA_1:
			if in.flood(in.bk.clock, secevent.KindFloodClockSync, a, "clock synchronisation rate") {
				return true
			}
		case t == asdu.C_RP_NA_1:
			if in.flood(in.bk.reset, secevent.KindFloodResetProcess, a, "reset process rate") {
				return true
			}
		}
	}
	return false
}

// audit raises the events that record control functions.
func (in *Inspector) audit(a *asdu.ASDU, raw []byte) {
	t := a.Type
	emit := func(kind secevent.Kind, value, detail string) {
		e := Event(kind, a, secevent.ActObserved, detail)
		e.Value, e.Raw = value, raw
		in.host.Emit(e)
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
		if th := in.pol.ClockStepThreshold; th > 0 && (delta > th || -delta > th) {
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
func (in *Inspector) ObserveOutbound(u *asdu.ASDU) {
	if !in.host.Enabled() {
		return
	}
	switch u.Coa.Cause {
	case asdu.UnknownTypeID:
		in.host.Emit(Event(secevent.KindAccessUnknownType, u, secevent.ActRejected, "request for an unknown type or function refused"))
		in.noteUnknown(u)
	case asdu.UnknownCOT:
		in.host.Emit(Event(secevent.KindProtoUnknownCOT, u, secevent.ActRejected, "request with an unexpected cause of transmission refused"))
	case asdu.UnknownCA:
		in.host.Emit(Event(secevent.KindAccessUnknownCA, u, secevent.ActRejected, "request for an unknown common address refused"))
		in.noteUnknown(u)
	case asdu.UnknownIOA:
		in.host.Emit(Event(secevent.KindAccessUnknownIOA, u, secevent.ActRejected, "request for an unknown information object refused"))
		in.noteUnknown(u)
	case asdu.ActivationCon, asdu.DeactivationCon:
		if u.Coa.IsNegative && asdu.IsControlDirection(u.Type) {
			e := Event(secevent.KindCmdRejected, u, secevent.ActRejected, "command refused (negative confirmation sent)")
			if asdu.IsProcessCommand(u.Type) {
				e.Value = asdu.DescribeCommand(u)
			}
			in.host.Emit(e)
			in.noteRejected(u)
		}
	}
}

// noteRejected escalates a run of refused commands: someone is trying
// values until one is accepted.
func (in *Inspector) noteRejected(u *asdu.ASDU) {
	if in.rejects == nil {
		in.rejects = secmon.NewBucket(10, time.Minute, 0)
	}
	if !in.rejects.Allow(time.Now()) {
		in.host.Emit(Event(secevent.KindCmdBruteForce, u, secevent.ActObserved,
			"more than 10 commands refused within a minute: probing for an accepted command"))
	}
}

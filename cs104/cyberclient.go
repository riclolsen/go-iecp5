// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs104

import (
	"fmt"
	"sync"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/secevent"
)

const (
	// requestTrackTTL is how long a request stays outstanding with no answer.
	requestTrackTTL = 2 * time.Minute
	// requestTrackGrace is how long data may still follow a termination, for
	// the frames already in flight when ActTerm was sent.
	requestTrackGrace = 5 * time.Second
	requestTrackMax   = 1024
)

type reqKey struct {
	typ asdu.TypeID
	ca  asdu.CommonAddr
	ioa asdu.InfoObjAddr
}

type reqState struct {
	sent   time.Time
	doneAt time.Time // set once terminated or refused
}

// requestTracker remembers what a client has asked, so that an answer nobody
// asked for can be told from one that was asked for. Bounded: a peer cannot
// grow it, and abandoned requests expire.
type requestTracker struct {
	mu sync.Mutex
	m  map[reqKey]*reqState
}

func (rt *requestTracker) add(k reqKey, now time.Time) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.m == nil {
		rt.m = map[reqKey]*reqState{}
	}
	if len(rt.m) >= requestTrackMax {
		for kk, s := range rt.m {
			if now.Sub(s.sent) > requestTrackTTL {
				delete(rt.m, kk)
			}
		}
		if len(rt.m) >= requestTrackMax {
			rt.m = map[reqKey]*reqState{}
		}
	}
	rt.m[k] = &reqState{sent: now}
}

// lookup finds the request an answer for k belongs to: the request to that
// station, or one sent to the global address, which every station answers
// with its own common address. The caller holds the lock.
func (rt *requestTracker) lookup(k reqKey) (reqKey, *reqState) {
	if s := rt.m[k]; s != nil {
		return k, s
	}
	g := k
	g.ca = asdu.GlobalCommonAddr
	return g, rt.m[g]
}

// live reports whether k is outstanding, or finished so recently that its
// last frames may still be arriving.
func (rt *requestTracker) live(k reqKey, now time.Time) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	kk, s := rt.lookup(k)
	if s == nil {
		return false
	}
	if now.Sub(s.sent) > requestTrackTTL || (!s.doneAt.IsZero() && now.Sub(s.doneAt) > requestTrackGrace) {
		delete(rt.m, kk)
		return false
	}
	return true
}

// done marks k answered. A broadcast request stays live until it expires:
// one station terminating it says nothing about when the others will.
func (rt *requestTracker) done(k reqKey, now time.Time) {
	rt.mu.Lock()
	if kk, s := rt.lookup(k); s != nil && kk.ca != asdu.GlobalCommonAddr {
		s.doneAt = now
	}
	rt.mu.Unlock()
}

// forget drops k, for a request that was never sent.
func (rt *requestTracker) forget(k reqKey) {
	rt.mu.Lock()
	delete(rt.m, k)
	rt.mu.Unlock()
}

// anyForCA reports whether some request of type typ to ca is live.
func (rt *requestTracker) anyForCA(typ asdu.TypeID, ca asdu.CommonAddr, now time.Time) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for k, s := range rt.m {
		if k.typ != typ || (k.ca != ca && k.ca != asdu.GlobalCommonAddr) {
			continue
		}
		if now.Sub(s.sent) > requestTrackTTL || (!s.doneAt.IsZero() && now.Sub(s.doneAt) > requestTrackGrace) {
			delete(rt.m, k)
			continue
		}
		return true
	}
	return false
}

// trackRequest records an outgoing request. It must run before the request
// is queued: the answer can arrive before Send returns.
func (sf *Client) trackRequest(a *asdu.ASDU) (reqKey, bool) {
	if !asdu.IsControlDirection(a.Type) {
		return reqKey{}, false
	}
	ioa, _ := asdu.FirstIOA(a)
	k := reqKey{a.Type, a.CommonAddr, ioa}
	sf.reqs.add(k, time.Now())
	return k, true
}

// inspect examines an ASDU received from the server before the handler sees
// it. It only reports: whatever the server says, the application decides.
func (sf *Client) inspect(a *asdu.ASDU, raw []byte) {
	if !sf.cyberOn() {
		return
	}
	t, cause := a.Type, a.Coa.Cause
	ev := func(kind secevent.Kind, act secevent.Action, detail string) {
		e := asduEvent(kind, a, act, detail)
		e.Raw = raw
		sf.emit(e)
	}
	now := time.Now()
	ioa, _ := asdu.FirstIOA(a)
	key := reqKey{t, a.CommonAddr, ioa}

	switch {
	case asdu.IsSecurity(t):
		ev(secevent.KindProtoSecureAuth, secevent.ActObserved, "IEC 62351-5 authentication ASDU: this endpoint does not implement it")
	case !asdu.InProfile104(t) && !(asdu.IsPrivate(t) && sf.pol.AllowPrivateTypes):
		ev(secevent.KindProtoNotInProfile, secevent.ActObserved, fmt.Sprintf("type %s is not among the ASDUs IEC 60870-5-104 selects", t))
	}

	// Commands travel from master to outstation only.
	if asdu.IsControlDirection(t) && !asdu.HasConfirmationCause(cause) && cause < asdu.UnknownTypeID {
		ev(secevent.KindProtoWrongDirection, secevent.ActObserved,
			fmt.Sprintf("control-direction type %s with cause %d received by the controlling station", t, cause))
	}

	// The server refusing what we asked is worth knowing, mildly.
	switch cause {
	case asdu.UnknownTypeID, asdu.UnknownCOT, asdu.UnknownCA, asdu.UnknownIOA:
		ev(secevent.KindRemoteRejected, secevent.ActObserved, fmt.Sprintf("the outstation answered with cause %d: it does not know what was asked", cause))
		sf.reqs.done(key, now)
	case asdu.ActivationCon, asdu.DeactivationCon:
		if a.Coa.IsNegative {
			ev(secevent.KindRemoteRejected, secevent.ActObserved, "the outstation refused the command (negative confirmation)")
		}
	}

	// An answer must answer something.
	if asdu.IsControlDirection(t) && asdu.HasConfirmationCause(cause) {
		if !sf.reqs.live(key, now) {
			ev(secevent.KindUnsolActResponse, secevent.ActObserved,
				fmt.Sprintf("%s with cause %d arrived and no such request is outstanding", t, cause))
		} else if cause == asdu.ActivationTerm || a.Coa.IsNegative {
			sf.reqs.done(key, now)
		}
	}

	// Data attributed to a request must have a request behind it.
	switch {
	case cause >= asdu.InterrogatedByStation && cause <= asdu.InterrogatedByGroup16 && asdu.IsMonitorDirection(t):
		if !sf.reqs.anyForCA(asdu.C_IC_NA_1, a.CommonAddr, now) {
			ev(secevent.KindUnsolInterrogated, secevent.ActObserved,
				fmt.Sprintf("data with interrogation cause %d and no interrogation outstanding for common address %d", cause, a.CommonAddr))
		}
	case cause >= asdu.RequestByGeneralCounter && cause <= asdu.RequestByGroup4Counter && asdu.IsMonitorDirection(t):
		if !sf.reqs.anyForCA(asdu.C_CI_NA_1, a.CommonAddr, now) {
			ev(secevent.KindUnsolInterrogated, secevent.ActObserved,
				fmt.Sprintf("counter data with cause %d and no counter interrogation outstanding for common address %d", cause, a.CommonAddr))
		}
	case cause == asdu.Request && asdu.IsMonitorDirection(t):
		if !sf.reqs.anyForCA(asdu.C_RD_NA_1, a.CommonAddr, now) {
			ev(secevent.KindUnsolInterrogated, secevent.ActObserved,
				fmt.Sprintf("data with cause 'requested' and no read outstanding for common address %d", a.CommonAddr))
		}
	}

	// The station says it restarted.
	if t == asdu.M_EI_NA_1 && !sf.pol.DisableAudit {
		_, coi := a.GetEndOfInitialization()
		e := asduEvent(secevent.KindCtrlEndOfInit, a, secevent.ActObserved, fmt.Sprintf("the outstation reports end of initialisation (%v)", coi))
		e.Raw = raw
		if len(a.InfoObj) > a.InfoObjAddrSize && a.InfoObj[a.InfoObjAddrSize]&0x7f == 2 {
			e.Severity = secevent.SevWarning
			e.Detail += ": it was reset remotely"
		}
		sf.emit(e)
	}
}

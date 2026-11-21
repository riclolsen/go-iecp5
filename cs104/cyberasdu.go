// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs104

import (
	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/internal/secasdu"
	"github.com/riclolsen/go-iecp5/secevent"
)

// srvHost lets the shared inspector act on a server session.
type srvHost struct{ sf *SrvSession }

func (h srvHost) Emit(e secevent.Event) { h.sf.emit(e) }
func (h srvHost) Enabled() bool         { return h.sf.cyberOn() }
func (h srvHost) Disconnect(reason string) {
	h.sf.noteEnd(reason)
	h.sf.cancel()
}

// NegativeConfirm refuses a command the way the standard says: the same
// ASDU back, with the negative bit set and a confirmation cause.
func (h srvHost) NegativeConfirm(a *asdu.ASDU) {
	r := asdu.NewASDU(h.sf.params, a.Identifier)
	r.Coa.Cause = asdu.ActivationCon
	if a.Coa.Cause == asdu.Deactivation {
		r.Coa.Cause = asdu.DeactivationCon
	}
	r.Coa.IsNegative = true
	r.InfoObj = append(r.InfoObj, a.InfoObj...)
	_ = h.sf.send(r, false)
}

func (h srvHost) ReplyCause(a *asdu.ASDU, c asdu.Cause) {
	r := asdu.NewASDU(h.sf.params, a.Identifier)
	r.Coa.Cause = c
	r.InfoObj = append(r.InfoObj, a.InfoObj...)
	_ = h.sf.send(r, false)
}

// asduEvent is kept for the client's own events.
func asduEvent(kind secevent.Kind, a *asdu.ASDU, act secevent.Action, detail string) secevent.Event {
	return secasdu.Event(kind, a, act, detail)
}

// inspect examines a decoded request; true means the handler must not see it.
func (sf *SrvSession) inspect(a *asdu.ASDU, raw []byte) bool {
	if sf.insp == nil {
		return false
	}
	return sf.insp.Inspect(a, raw)
}

func (sf *SrvSession) observeOutbound(u *asdu.ASDU) {
	if sf.insp != nil {
		sf.insp.ObserveOutbound(u)
	}
}

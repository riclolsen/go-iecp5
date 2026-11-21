// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs101

import (
	"fmt"
	"net"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/internal/secasdu"
	"github.com/riclolsen/go-iecp5/secevent"
)

// serverCyber is the security state of a Server.
type serverCyber struct {
	em        secevent.Emitter
	policy    atomic.Pointer[secevent.Policy]
	insp      *secasdu.Inspector // owned by the handler goroutine
	sessionID atomic.Uint64
	started   time.Time
}

func (c *serverCyber) init(proto, role string) {
	c.em.Protocol, c.em.Role = proto, role
}

// SetCyberEventSink sets where the server delivers security events; nil turns
// them off. Events cover the link layer (frames for another station, bad
// checksums, repeated frames, unsupported functions) and, through the shared
// inspector, the ASDUs that arrive.
func (sf *Server) SetCyberEventSink(s secevent.Sink) *Server {
	sf.cyber.em.SetSink(s)
	return sf
}

// SetCyberDispatcher shares one secevent.Dispatcher between endpoints.
func (sf *Server) SetCyberDispatcher(d *secevent.Dispatcher) *Server {
	sf.cyber.em.SetDispatcher(d)
	return sf
}

// SetSecurityPolicy sets the policy applied to requests. Call before Start.
func (sf *Server) SetSecurityPolicy(p secevent.Policy) *Server {
	sf.cyber.policy.Store(&p)
	return sf
}

// SecurityStats returns the security event counters.
func (sf *Server) SecurityStats() secevent.Stats { return sf.cyber.em.Stats() }

// PeerInfo describes what is known of the other end: the TCP peer when the
// transport is TCP, otherwise only that the link is serial.
func (sf *Server) PeerInfo() secevent.PeerInfo {
	p := secevent.PeerInfo{Network: "serial"}
	if c := sf.UnderlyingConn(); c != nil {
		p.Network = "tcp"
		if a, err := netip.ParseAddrPort(addrOf(c.RemoteAddr())); err == nil {
			p.RemoteAddr = a
		}
		if a, err := netip.ParseAddrPort(addrOf(c.LocalAddr())); err == nil {
			p.LocalAddr = a
		}
	}
	return p
}

func addrOf(a net.Addr) string {
	if a == nil {
		return ""
	}
	return a.String()
}

// ReportCyberEvent raises a security event for this server. Handlers use it
// for what only they can tell.
func (sf *Server) ReportCyberEvent(e secevent.Event) { sf.emit(e) }

func (sf *Server) cyberOn() bool { return sf.cyber.em.Enabled() }

func (sf *Server) emit(e secevent.Event) {
	if !sf.cyber.em.Enabled() {
		return
	}
	if e.Peer.Network == "" {
		e.Peer = sf.PeerInfo()
	}
	if e.SessionID == 0 {
		e.SessionID = sf.cyber.sessionID.Load()
	}
	if !e.Peer.LocalAddr.IsValid() {
		e.Local = fmt.Sprintf("link %d", sf.config.LinkAddress)
	}
	sf.cyber.em.Emit(e)
}

// linkEvent builds a link-layer event.
func (sf *Server) linkEvent(kind secevent.Kind, act secevent.Action, detail string, f *Frame) secevent.Event {
	e := secevent.Event{Kind: kind, Action: act, Detail: detail, Fields: secevent.FieldLinkAddr}
	if f != nil {
		e.LinkAddr = uint16(f.GetLinkAddress())
	}
	return e
}

// beginCyber starts the bookkeeping of a connection.
func (sf *Server) beginCyber() {
	sf.cyber.sessionID.Store(secevent.NextSessionID())
	sf.cyber.started = time.Now()
	pol := secevent.DefaultPolicy()
	if p := sf.cyber.policy.Load(); p != nil {
		pol = *p
	}
	sf.cyber.insp = secasdu.New(csHost{sf}, nil, pol)
}

// csHost lets the shared inspector act on the server.
type csHost struct{ sf *Server }

func (h csHost) Emit(e secevent.Event) { h.sf.emit(e) }
func (h csHost) Enabled() bool         { return h.sf.cyberOn() }
func (h csHost) Disconnect(reason string) {
	h.sf.emit(secevent.Event{Kind: secevent.KindConnClosed, Action: secevent.ActDisconnected, Detail: reason})
	if h.sf.connCancel != nil {
		h.sf.connCancel()
	}
}

func (h csHost) NegativeConfirm(a *asdu.ASDU) {
	r := asdu.NewASDU(&h.sf.params, a.Identifier)
	r.Coa.Cause = asdu.ActivationCon
	if a.Coa.Cause == asdu.Deactivation {
		r.Coa.Cause = asdu.DeactivationCon
	}
	r.Coa.IsNegative = true
	r.InfoObj = append(r.InfoObj, a.InfoObj...)
	_ = h.sf.Send(r)
}

func (h csHost) ReplyCause(a *asdu.ASDU, c asdu.Cause) {
	r := asdu.NewASDU(&h.sf.params, a.Identifier)
	r.Coa.Cause = c
	r.InfoObj = append(r.InfoObj, a.InfoObj...)
	_ = h.sf.Send(r)
}

// --- Client ---

// clientCyber is the security state of a Client.
type clientCyber struct {
	em secevent.Emitter
}

// SetCyberEventSink sets where the client delivers security events; nil turns
// them off. The client reports what a primary station can see go wrong on the
// line: frames from stations it does not poll, secondaries answering as
// primaries, bad frames, repeated frames and undecodable ASDUs.
func (sf *Client) SetCyberEventSink(s secevent.Sink) *Client {
	sf.cyber.em.SetSink(s)
	return sf
}

// SetCyberDispatcher shares one secevent.Dispatcher between endpoints.
func (sf *Client) SetCyberDispatcher(d *secevent.Dispatcher) *Client {
	sf.cyber.em.SetDispatcher(d)
	return sf
}

// SecurityStats returns the security event counters.
func (sf *Client) SecurityStats() secevent.Stats { return sf.cyber.em.Stats() }

// ReportCyberEvent raises a security event for this client.
func (sf *Client) ReportCyberEvent(e secevent.Event) { sf.emit(e) }

func (sf *Client) cyberOn() bool { return sf.cyber.em.Enabled() }

func (sf *Client) emit(e secevent.Event) {
	if !sf.cyber.em.Enabled() {
		return
	}
	if e.Peer.Network == "" {
		e.Peer = secevent.PeerInfo{Network: "serial"}
		if c := sf.UnderlyingConn(); c != nil {
			e.Peer.Network = "tcp"
			if a, err := netip.ParseAddrPort(addrOf(c.RemoteAddr())); err == nil {
				e.Peer.RemoteAddr = a
			}
			if a, err := netip.ParseAddrPort(addrOf(c.LocalAddr())); err == nil {
				e.Peer.LocalAddr = a
			}
		}
	}
	sf.cyber.em.Emit(e)
}

func (sf *Client) linkEvent(kind secevent.Kind, act secevent.Action, detail string, f *Frame) secevent.Event {
	e := secevent.Event{Kind: kind, Action: act, Detail: detail, Fields: secevent.FieldLinkAddr}
	if f != nil {
		e.LinkAddr = uint16(f.GetLinkAddress())
	}
	return e
}

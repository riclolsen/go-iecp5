// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs103

import (
	"github.com/riclolsen/go-iecp5/cs101"
	"github.com/riclolsen/go-iecp5/secevent"
)

// clientCyber is the security state of a Client.
type clientCyber struct {
	em secevent.Emitter
}

// SetCyberEventSink sets where the client delivers security events; nil turns
// them off. The client reports what a primary station can see go wrong on the
// line: answers from stations it does not poll, secondaries that transmit as
// primaries, bad frames and undecodable ASDUs.
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
	}
	sf.cyber.em.Emit(e)
}

func (sf *Client) linkEvent(kind secevent.Kind, act secevent.Action, detail string, f *cs101.Frame) secevent.Event {
	e := secevent.Event{Kind: kind, Action: act, Detail: detail}
	if f != nil {
		e.Fields = secevent.FieldLinkAddr
		e.LinkAddr = uint16(f.GetLinkAddress())
	}
	return e
}

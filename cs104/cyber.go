// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs104

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/riclolsen/go-iecp5/secevent"
)

// peerInfoFromConn describes the other end of conn: its address and, when the
// connection is TLS and the handshake is done, the certificate it presented.
func peerInfoFromConn(conn net.Conn) secevent.PeerInfo {
	p := secevent.PeerInfo{Network: "tcp", ConnectedAt: time.Now()}
	if conn == nil {
		return p
	}
	if a, err := netip.ParseAddrPort(addrString(conn.RemoteAddr())); err == nil {
		p.RemoteAddr = netip.AddrPortFrom(a.Addr().Unmap(), a.Port())
	}
	if a, err := netip.ParseAddrPort(addrString(conn.LocalAddr())); err == nil {
		p.LocalAddr = netip.AddrPortFrom(a.Addr().Unmap(), a.Port())
	}
	if tc, ok := conn.(*tls.Conn); ok {
		p.TLS = tlsPeerInfo(tc.ConnectionState())
	}
	return p
}

func addrString(a net.Addr) string {
	if a == nil {
		return ""
	}
	return a.String()
}

func tlsPeerInfo(cs tls.ConnectionState) *secevent.TLSPeer {
	t := &secevent.TLSPeer{
		Version: cs.Version, CipherSuite: cs.CipherSuite, ServerName: cs.ServerName,
		Verified: len(cs.VerifiedChains) > 0,
	}
	if len(cs.PeerCertificates) > 0 {
		c := cs.PeerCertificates[0]
		sum := sha256.Sum256(c.Raw)
		t.Subject, t.Issuer = c.Subject.String(), c.Issuer.String()
		t.SerialHex = c.SerialNumber.Text(16)
		t.NotAfter = c.NotAfter
		t.FingerprintSHA256 = hex.EncodeToString(sum[:])
	}
	return t
}

// SetCyberEventSink sets where an endpoint delivers security events; nil
// turns them off and flushes what is queued. It exists for ServerSpecial,
// whose interface is fixed; a Server or Client has its own chainable method.
// It reports false for a value that raises no events.
func SetCyberEventSink(endpoint any, s secevent.Sink) bool {
	switch e := endpoint.(type) {
	case *Server:
		e.SetCyberEventSink(s)
	case *Client:
		e.SetCyberEventSink(s)
	case *serverSpec:
		e.SetCyberEventSink(s)
	default:
		return false
	}
	return true
}

// SecurityStats returns the event counters of an endpoint, or false for a
// value that raises no events.
func SecurityStats(endpoint any) (secevent.Stats, bool) {
	switch e := endpoint.(type) {
	case *Server:
		return e.SecurityStats(), true
	case *Client:
		return e.SecurityStats(), true
	case *serverSpec:
		return e.SecurityStats(), true
	}
	return secevent.Stats{}, false
}

// --- SrvSession ---

// PeerInfo describes the master on the other end of this session.
func (sf *SrvSession) PeerInfo() secevent.PeerInfo {
	sf.peerMu.RLock()
	defer sf.peerMu.RUnlock()
	return sf.peer
}

// ReportCyberEvent raises a security event for this session. Handlers use it,
// through secevent.Report, for what only they can tell, such as a read of a
// point that does not exist.
func (sf *SrvSession) ReportCyberEvent(e secevent.Event) { sf.emit(e) }

// emit completes e with the session's identity and queues it. It is cheap and
// does nothing when no sink is set.
func (sf *SrvSession) emit(e secevent.Event) {
	if sf.cyber == nil || !sf.cyber.Enabled() {
		return
	}
	if !e.Peer.RemoteAddr.IsValid() && e.Peer.Network == "" {
		e.Peer = sf.PeerInfo()
	}
	if e.SessionID == 0 {
		e.SessionID = sf.sessionID.Load()
	}
	if e.Local == "" && e.Peer.LocalAddr.IsValid() {
		e.Local = e.Peer.LocalAddr.String()
	}
	sf.cyber.Emit(e)
}

func (sf *SrvSession) cyberOn() bool { return sf.cyber != nil && sf.cyber.Enabled() }

// noteEnd records why the session ended; the first reason wins, because the
// later ones are consequences of it.
func (sf *SrvSession) noteEnd(reason string) {
	sf.endMu.Lock()
	if sf.endReason == "" {
		sf.endReason = reason
	}
	sf.endMu.Unlock()
}

func (sf *SrvSession) endedBecause() string {
	sf.endMu.Lock()
	defer sf.endMu.Unlock()
	if sf.endReason == "" {
		return "connection ended"
	}
	return sf.endReason
}

// --- Server ---

// SetCyberEventSink sets where security events are delivered; nil turns them
// off. Events carry the peer, with its MAC address when it can be learned,
// and are raised for connections, protocol violations, rejected requests,
// control functions and floods. See the secevent package.
func (sf *Server) SetCyberEventSink(s secevent.Sink) *Server {
	sf.cyber.SetSink(s)
	return sf
}

// SetCyberDispatcher shares one secevent.Dispatcher between endpoints.
func (sf *Server) SetCyberDispatcher(d *secevent.Dispatcher) *Server {
	sf.cyber.SetDispatcher(d)
	return sf
}

// SecurityStats returns the security event counters.
func (sf *Server) SecurityStats() secevent.Stats { return sf.cyber.Stats() }

// ReportCyberEvent raises a security event not tied to a session.
func (sf *Server) ReportCyberEvent(e secevent.Event) { sf.cyber.Emit(e) }

// --- serverSpec ---

func (sf *serverSpec) SetCyberEventSink(s secevent.Sink) { sf.cyber.SetSink(s) }

func (sf *serverSpec) SecurityStats() secevent.Stats { return sf.cyber.Stats() }

// --- Client ---

// PeerInfo describes the server this client is connected to.
func (sf *Client) PeerInfo() secevent.PeerInfo {
	sf.peerMu.RLock()
	defer sf.peerMu.RUnlock()
	return sf.peer
}

// SetCyberEventSink sets where security events are delivered; nil turns them
// off. See the secevent package.
func (sf *Client) SetCyberEventSink(s secevent.Sink) *Client {
	sf.cyber.SetSink(s)
	return sf
}

// SetCyberDispatcher shares one secevent.Dispatcher between endpoints.
func (sf *Client) SetCyberDispatcher(d *secevent.Dispatcher) *Client {
	sf.cyber.SetDispatcher(d)
	return sf
}

// SecurityStats returns the security event counters.
func (sf *Client) SecurityStats() secevent.Stats { return sf.cyber.Stats() }

// ReportCyberEvent raises a security event for this client.
func (sf *Client) ReportCyberEvent(e secevent.Event) { sf.emit(e) }

func (sf *Client) emit(e secevent.Event) {
	if !sf.cyber.Enabled() {
		return
	}
	if !e.Peer.RemoteAddr.IsValid() && e.Peer.Network == "" {
		e.Peer = sf.PeerInfo()
	}
	if e.SessionID == 0 {
		e.SessionID = sf.sessionID.Load()
	}
	sf.cyber.Emit(e)
}

func (sf *Client) cyberOn() bool { return sf.cyber.Enabled() }

func (sf *Client) noteEnd(reason string) {
	sf.endMu.Lock()
	if sf.endReason == "" {
		sf.endReason = reason
	}
	sf.endMu.Unlock()
}

func (sf *Client) endedBecause() string {
	sf.endMu.Lock()
	defer sf.endMu.Unlock()
	if sf.endReason == "" {
		return "connection ended"
	}
	return sf.endReason
}

func closedDetail(reason string, dur time.Duration, in, out uint64) string {
	return fmt.Sprintf("%s; lasted %s, %d frame(s) in, %d out", reason, dur.Round(time.Millisecond), in, out)
}

// beginCyber starts the security bookkeeping of a connection.
func (sf *SrvSession) beginCyber() {
	sf.sessionID.Store(secevent.NextSessionID())
	sf.started = time.Now()
	sf.framesIn.Store(0)
	sf.framesOut.Store(0)
	sf.endMu.Lock()
	sf.endReason = ""
	sf.endMu.Unlock()
	p := peerInfoFromConn(sf.conn)
	sf.peerMu.Lock()
	sf.peer = p
	sf.peerMu.Unlock()
	if sf.inbound && sf.cyberOn() {
		sf.emit(secevent.Event{Kind: secevent.KindConnAccepted, Peer: p,
			Detail: "connection accepted on the listener"})
	}
}

// endCyber reports the end of a connection.
func (sf *SrvSession) endCyber() {
	if !sf.cyberOn() {
		return
	}
	in, out := sf.framesIn.Load(), sf.framesOut.Load()
	if in == 0 && sf.inbound {
		sf.emit(secevent.Event{Kind: secevent.KindConnNoData,
			Detail: "the peer closed or was closed without sending one valid frame"})
	}
	sf.emit(secevent.Event{Kind: secevent.KindConnClosed,
		Detail: closedDetail(sf.endedBecause(), time.Since(sf.started), in, out)})
}

func (sf *Client) beginCyber() {
	sf.sessionID.Store(secevent.NextSessionID())
	sf.started = time.Now()
	sf.framesIn.Store(0)
	sf.framesOut.Store(0)
	sf.endMu.Lock()
	sf.endReason = ""
	sf.endMu.Unlock()
	p := peerInfoFromConn(sf.conn)
	sf.peerMu.Lock()
	sf.peer = p
	sf.peerMu.Unlock()
}

func (sf *Client) endCyber() {
	if !sf.cyberOn() {
		return
	}
	sf.emit(secevent.Event{Kind: secevent.KindConnClosed,
		Detail: closedDetail(sf.endedBecause(), time.Since(sf.started), sf.framesIn.Load(), sf.framesOut.Load())})
}

func netipAddrPort(a net.Addr) netip.AddrPort {
	ap, _ := netip.ParseAddrPort(addrString(a))
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}

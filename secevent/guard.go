// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package secevent

import (
	"bytes"
	"context"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/riclolsen/go-iecp5/internal/secmon"
	"github.com/riclolsen/go-iecp5/netpeer"
)

// Guard applies the connection-level parts of a Policy: who may connect, how
// many and how fast, and who the peer is. Endpoints that accept connections
// call Admit before reading a byte; endpoints that dial call Identify.
type Guard struct {
	p  Policy
	em *Emitter

	mu       sync.Mutex
	total    int
	perIP    map[netip.Addr]int
	rate     map[netip.Addr]*secmon.Bucket
	global   *secmon.Bucket
	lastSwep time.Time

	dupMu sync.Mutex
	dup   map[string]time.Time
}

// NewGuard creates a Guard that raises its events through em; a nil em
// raises none.
func NewGuard(p Policy, em *Emitter) *Guard {
	p.defaults()
	if em == nil {
		em = &Emitter{}
	}
	return &Guard{
		p: p, em: em,
		dup:    map[string]time.Time{},
		perIP:  map[netip.Addr]int{},
		rate:   map[netip.Addr]*secmon.Bucket{},
		global: secmon.NewBucket(p.GlobalAcceptRate.N, p.GlobalAcceptRate.Per, 0),
	}
}

// Policy returns the policy in force (with defaults filled in).
func (g *Guard) Policy() Policy { return g.p }

func (g *Guard) emit(kind Kind, peer PeerInfo, id uint64, act Action, detail string, mod func(*Event)) {
	if !g.em.Enabled() {
		return
	}
	e := Event{Kind: kind, Peer: peer, SessionID: id, Action: act, Detail: detail}
	if mod != nil {
		mod(&e)
	}
	g.em.Emit(e)
}

// Admit decides whether a new connection is let in. It returns the peer,
// completed with its MAC address when one can be learned. When it returns
// false the caller closes the connection without reading from it, and must
// not call Release. When true, the caller calls Release when the connection
// ends.
func (g *Guard) Admit(peer PeerInfo, id uint64) (PeerInfo, bool) {
	ip := peer.RemoteAddr.Addr().Unmap()
	now := time.Now()

	// 1. Is this a peer anyone expected?
	if len(g.p.AllowedPeers) > 0 {
		listed := false
		for _, pf := range g.p.AllowedPeers {
			if pf.Contains(ip) {
				listed = true
				break
			}
		}
		if !listed {
			act := ActObserved
			if g.p.RejectUnlisted {
				act = ActRejected
			}
			g.emit(KindConnUnexpected, peer, id, act, "connection from a network that is not in AllowedPeers", nil)
			if g.p.RejectUnlisted {
				return peer, false
			}
		}
	}

	// 2. Rates and limits, before anything costly.
	g.mu.Lock()
	over := ""
	if !g.global.Allow(now) {
		over = "overall accept rate exceeded"
	}
	b := g.rate[ip]
	if b == nil && g.p.AcceptRate.N > 0 {
		g.sweep(now)
		b = secmon.NewBucket(g.p.AcceptRate.N, g.p.AcceptRate.Per, 0)
		g.rate[ip] = b
	}
	if !b.Allow(now) {
		over = "accept rate exceeded for this source address"
	}
	limit := ""
	switch {
	case g.p.MaxSessions > 0 && g.total >= g.p.MaxSessions:
		limit = fmt.Sprintf("MaxSessions (%d) reached", g.p.MaxSessions)
	case g.p.MaxSessionsPerIP > 0 && g.perIP[ip] >= g.p.MaxSessionsPerIP:
		limit = fmt.Sprintf("MaxSessionsPerIP (%d) reached for this source address", g.p.MaxSessionsPerIP)
	}
	// Take the slot now, in the same critical section as the check: the
	// identity lookup below can take a while, and every connection racing
	// through it would otherwise see the same free count.
	if limit == "" {
		g.total++
		g.perIP[ip]++
	}
	g.mu.Unlock()
	orig := peer
	reject := func() (PeerInfo, bool) {
		g.Release(orig)
		return peer, false
	}

	if over != "" {
		drop := g.p.FloodAction == ActThrottled || g.p.FloodAction == ActDisconnected
		act := ActObserved
		if drop {
			act = ActRejected
		}
		g.emit(KindConnRate, peer, id, act, over, nil)
		if drop {
			if limit == "" {
				return reject()
			}
			return peer, false
		}
	}
	if limit != "" {
		g.emit(KindConnRejectedLimit, peer, id, ActRejected, limit, nil)
		return peer, false
	}

	// 3. Who is it, link-layer wise?
	peer = g.Identify(peer, id)
	if want, pinned := g.p.PeerBindings[ip]; pinned {
		var why string
		switch {
		case peer.MAC == nil:
			why = fmt.Sprintf("a MAC binding exists (%s) but the peer's MAC cannot be learned", want)
		case !bytes.Equal(peer.MAC, want):
			why = fmt.Sprintf("peer MAC %s does not match the pinned %s", peer.MAC, want)
		}
		if why != "" {
			act := ActObserved
			if g.p.RejectBindingMismatch {
				act = ActRejected
			}
			g.emit(KindConnUnexpectedMAC, peer, id, act, why, nil)
			g.emit(KindPeerMACPinning, peer, id, act, why, nil)
			if g.p.RejectBindingMismatch {
				return reject()
			}
		}
	}

	return peer, true
}

// Release gives back the slot of an admitted connection.
func (g *Guard) Release(peer PeerInfo) {
	ip := peer.RemoteAddr.Addr().Unmap()
	g.mu.Lock()
	if g.total > 0 {
		g.total--
	}
	if n := g.perIP[ip]; n <= 1 {
		delete(g.perIP, ip)
	} else {
		g.perIP[ip] = n - 1
	}
	g.mu.Unlock()
}

// Sessions is the number of admitted connections still open.
func (g *Guard) Sessions() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.total
}

// sweep drops rate buckets idle for a while, bounding memory under a scan
// from many addresses. Callers hold g.mu.
func (g *Guard) sweep(now time.Time) {
	if len(g.rate) < 1024 || now.Sub(g.lastSwep) < time.Second {
		return
	}
	g.lastSwep = now
	for ip, b := range g.rate {
		if b.Idle(now, g.p.AcceptRate.Per*2) {
			delete(g.rate, ip)
		}
	}
}

// Identify completes peer with the MAC address behind its IP and raises the
// peer.* events the cache reveals: a MAC that changed for this IP, an IP that
// changed for this MAC, a MAC shared by several addresses.
func (g *Guard) Identify(peer PeerInfo, id uint64) PeerInfo {
	ip := peer.RemoteAddr.Addr().Unmap()
	if !ip.IsValid() {
		return peer
	}
	res, err := netpeer.LookupTimeout(g.p.Resolver, ip, g.p.MACLookupTimeout)
	peer.OnLink, peer.Interface = res.OnLink, res.Interface
	if err != nil || res.MAC == nil {
		if res.OnLink && !res.Loopback && g.p.MACCache.FirstMissing(ip) {
			detail := "no MAC address could be learned for an on-link peer"
			if err != nil {
				detail = fmt.Sprintf("%s: %v", detail, err)
			}
			g.emit(KindPeerMACUnavailable, peer, id, ActObserved, detail, nil)
		}
		return peer
	}
	peer.MAC = res.MAC
	switch res.Source {
	case netpeer.SourceStatic:
		peer.MACSource = MACStatic
	case netpeer.SourceCustom:
		peer.MACSource = MACCustom
	default:
		peer.MACSource = MACNeighbor
	}
	for _, c := range g.p.MACCache.Observe(ip, res.MAC) {
		switch c.Kind {
		case netpeer.MACChanged:
			g.emit(KindPeerMACChanged, peer, id, ActObserved,
				fmt.Sprintf("%s was at MAC %s, now %s: ARP spoofing, a man in the middle or a replaced device", ip, c.PrevMAC, c.MAC), nil)
		case netpeer.IPChanged:
			g.emit(KindPeerIPChanged, peer, id, ActObserved,
				fmt.Sprintf("MAC %s was at %s, now %s", c.MAC, c.OtherIP, ip), nil)
		case netpeer.MACShared:
			g.emit(KindPeerMACShared, peer, id, ActObserved,
				fmt.Sprintf("MAC %s is in use with %s and %s at the same time", c.MAC, c.OtherIP, ip), nil)
		}
	}
	return peer
}

// ObserveTLS raises the events a completed TLS handshake reveals: a
// certificate that changed for this IP, one about to expire, weak parameters.
func (g *Guard) ObserveTLS(peer PeerInfo, id uint64) {
	t := peer.TLS
	if t == nil {
		return
	}
	ip := peer.RemoteAddr.Addr().Unmap()
	if ch, ok := g.p.MACCache.ObserveCert(ip, t.FingerprintSHA256); ok {
		g.emit(KindTLSCertChanged, peer, id, ActObserved,
			fmt.Sprintf("%s presented certificate %s, previously %s", ip, ch.FP, ch.PrevFP), nil)
	}
	if !t.NotAfter.IsZero() {
		if left := time.Until(t.NotAfter); left < g.p.CertExpiryWarning {
			g.emit(KindTLSCertExpiring, peer, id, ActObserved,
				fmt.Sprintf("peer certificate expires in %s", left.Round(time.Hour)), nil)
		}
	}
	if t.Version != 0 && t.Version < 0x0303 { // below TLS 1.2
		g.emit(KindTLSWeakParams, peer, id, ActObserved, fmt.Sprintf("negotiated TLS version 0x%04x is below 1.2", t.Version), nil)
	}
}

// WatchMAC re-resolves the peer's MAC every MACRecheckInterval until ctx is
// done. When it changes under a live session, get/set keep the session's
// PeerInfo current and disconnect, if non-nil and DisconnectOnMACChange is
// set, is called. It returns at once when rechecking is off.
func (g *Guard) WatchMAC(ctx context.Context, id uint64, get func() PeerInfo, set func(PeerInfo), disconnect func()) {
	if g.p.MACRecheckInterval <= 0 {
		return
	}
	go func() {
		t := time.NewTicker(g.p.MACRecheckInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			cur := get()
			ip := cur.RemoteAddr.Addr().Unmap()
			res, err := netpeer.LookupTimeout(g.p.Resolver, ip, g.p.MACLookupTimeout)
			if err != nil || res.MAC == nil || bytes.Equal(res.MAC, cur.MAC) {
				continue
			}
			if cur.MAC == nil {
				// No MAC was known at admission: this is the baseline that
				// later lookups are compared with, not a change.
				cur.MAC = res.MAC
				set(cur)
				g.p.MACCache.Observe(ip, res.MAC)
				continue
			}
			old := cur.MAC
			cur.MAC = res.MAC
			set(cur)
			act := ActObserved
			if g.p.DisconnectOnMACChange && disconnect != nil {
				act = ActDisconnected
			}
			g.p.MACCache.Observe(ip, res.MAC)
			g.emit(KindPeerMACChangedLive, cur, id, act,
				fmt.Sprintf("the MAC behind %s changed from %s to %s during the session", ip, old, res.MAC), nil)
			if act == ActDisconnected {
				disconnect()
				return
			}
		}
	}()
}

// SeenCommand reports whether an identical command was seen within
// DuplicateWindow, and records this one. The key identifies the command
// byte for byte; the window is shared by all sessions of the endpoint.
func (g *Guard) SeenCommand(key string, now time.Time) bool {
	w := g.p.DuplicateWindow
	if w <= 0 {
		return false
	}
	g.dupMu.Lock()
	defer g.dupMu.Unlock()
	if len(g.dup) >= 4096 {
		for k, t := range g.dup { // bounded: forget what has expired
			if now.Sub(t) > w {
				delete(g.dup, k)
			}
		}
		if len(g.dup) >= 4096 {
			g.dup = map[string]time.Time{}
		}
	}
	prev, ok := g.dup[key]
	g.dup[key] = now
	return ok && now.Sub(prev) < w
}

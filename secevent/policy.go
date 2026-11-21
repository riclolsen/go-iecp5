// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package secevent

import (
	"net"
	"net/netip"
	"time"

	"github.com/riclolsen/go-iecp5/netpeer"
)

// Rate is "N events per Per". The zero Rate is no limit.
type Rate struct {
	N   int
	Per time.Duration
}

// Policy configures how an endpoint watches and, optionally, protects
// itself. Everything defaults to observing and reporting: a limit or a
// refusal only happens when it is asked for here.
type Policy struct {
	// Sink receives the events. Equivalent to the endpoint's
	// SetCyberEventSink.
	Sink Sink
	// AggregateWindow merges repeats of an event (default 1s, negative off).
	AggregateWindow time.Duration
	// MaxRaw truncates the frame copy in an event (default 64 octets).
	MaxRaw int

	// --- who may connect ---

	// AllowedPeers lists the networks peers are expected from; a connection
	// from elsewhere raises conn.unexpected. Empty means any.
	AllowedPeers []netip.Prefix
	// RejectUnlisted closes connections from outside AllowedPeers before any
	// byte is read, instead of only reporting them.
	RejectUnlisted bool
	// PeerBindings pins an IP address to the MAC it must come from. A pinned
	// peer whose MAC differs, or cannot be learned, raises
	// conn.unexpected_mac.
	PeerBindings map[netip.Addr]net.HardwareAddr
	// RejectBindingMismatch closes connections that violate a binding.
	RejectBindingMismatch bool

	// --- how many, how fast ---

	// MaxSessions and MaxSessionsPerIP bound concurrent connections (0 =
	// unlimited). A connection over the limit is closed.
	MaxSessions, MaxSessionsPerIP int
	// AcceptRate and GlobalAcceptRate bound new connections per source IP and
	// overall. Exceeding them raises conn.rate; the connection is also closed
	// when FloodAction is ActThrottled or ActDisconnected.
	AcceptRate, GlobalAcceptRate Rate
	// FloodAction is what is done to traffic over a rate: ActObserved (the
	// default) only reports.
	FloodAction Action

	// --- timeouts ---

	// InactiveTimeout closes a session that never starts data transfer (0 = never).
	InactiveTimeout time.Duration

	// --- peer identity ---

	// Resolver finds MAC addresses (default: the system neighbour table).
	Resolver netpeer.Resolver
	// MACCache remembers MAC/IP history (default: the process-wide cache).
	MACCache *netpeer.Cache
	// MACLookupTimeout bounds a lookup (default 200ms).
	MACLookupTimeout time.Duration
	// MACRecheckInterval, when positive, re-resolves the MAC of a live
	// session this often; a change raises peer.mac_changed_midsession.
	MACRecheckInterval time.Duration
	// DisconnectOnMACChange closes a session whose MAC changed mid-session.
	DisconnectOnMACChange bool
	// CertExpiryWarning raises tls.cert_expiring when a peer certificate
	// expires within this time (default 30 days).
	CertExpiryWarning time.Duration
}

// DefaultPolicy is the report-only policy with the library's thresholds.
func DefaultPolicy() Policy {
	return Policy{
		AggregateWindow:   time.Second,
		MaxRaw:            64,
		MACLookupTimeout:  200 * time.Millisecond,
		CertExpiryWarning: 30 * 24 * time.Hour,
		AcceptRate:        Rate{10, 10 * time.Second},
		GlobalAcceptRate:  Rate{100, 10 * time.Second},
	}
}

func (p *Policy) defaults() {
	if p.MACLookupTimeout <= 0 {
		p.MACLookupTimeout = 200 * time.Millisecond
	}
	if p.CertExpiryWarning <= 0 {
		p.CertExpiryWarning = 30 * 24 * time.Hour
	}
	if p.Resolver == nil {
		p.Resolver = netpeer.Default()
	}
	if p.MACCache == nil {
		p.MACCache = netpeer.DefaultCache()
	}
	if p.FloodAction == "" {
		p.FloodAction = ActObserved
	}
}

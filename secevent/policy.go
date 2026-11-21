// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package secevent

import (
	"net"
	"net/netip"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
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

	// --- protocol ---

	// IgnoreUnsolicitedU makes a client disregard a STARTDT con or STOPDT con
	// it never asked for, instead of activating or deactivating on it. The
	// event is raised either way.
	IgnoreUnsolicitedU bool

	// --- application layer ---

	// DisableAudit silences the audit-trail events for control functions
	// (ctrl.*, cmd.received, file.*): by default they are raised, since an
	// operator wants a record of every command, reset and parameter change.
	DisableAudit bool
	// AddressSpace, when set, lets the library tell requests for objects that
	// do not exist from those that do, before the handler is called.
	AddressSpace AddressSpace
	// AutoRejectUnknown answers a request for an unknown station or object
	// with the standard negative reply (unknown CA / IOA) and does not call
	// the handler.
	AutoRejectUnknown bool
	// Functions allows or denies request types.
	Functions FunctionPolicy
	// AllowPrivateTypes stops proto.type_not_in_profile being raised for the
	// type identifications reserved for special use (128..255).
	AllowPrivateTypes bool
	// AllowedOriginators, when non-empty, lists the originator addresses a
	// master may use; others raise access.originator_unexpected.
	AllowedOriginators []asdu.OriginAddr
	// MaxCommandAge and MaxCommandFuture bound how far a time-tagged
	// command's time may be from the local clock (0 = unchecked). Both
	// stations need synchronised clocks for this to be meaningful.
	MaxCommandAge, MaxCommandFuture time.Duration
	// RejectStaleCommands answers a stale time-tagged command with a
	// negative confirmation instead of only reporting it.
	RejectStaleCommands bool
	// DuplicateWindow raises cmd.duplicate when a byte-identical command is
	// seen again within the window, on any session (0 = off). A replay over a
	// new connection has fresh APCI sequence numbers and is caught only here.
	DuplicateWindow time.Duration
	// ClockStepThreshold raises ctrl.clock_step when a clock synchronisation
	// moves the clock by more than this (default 60s; negative = off).
	ClockStepThreshold time.Duration
	// ScanThreshold and ScanWindow raise access.scan when one session
	// addresses this many distinct unknown objects within the window
	// (default 20 in 60s).
	ScanThreshold int
	ScanWindow    time.Duration

	// --- flooding ---

	// Limits are per-session rates; exceeding one raises a flood.* event.
	// Zero fields take the defaults, a negative N disables that limit.
	Limits Limits

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

// Limits are per-session rates for the kinds of traffic that can be turned
// against an outstation.
type Limits struct {
	Commands             Rate // process commands
	Interrogation        Rate
	CounterInterrogation Rate
	Read                 Rate
	ClockSync            Rate
	ResetProcess         Rate
	TestFR               Rate // TESTFR act received
	UToggle              Rate // STARTDT/STOPDT act received
	SFrames              Rate // S-frames that acknowledge nothing new
	Frames               Rate // every frame
}

// AddressSpace tells the library which stations and objects exist.
type AddressSpace interface {
	// KnownCA reports whether the common address (station) exists.
	KnownCA(ca asdu.CommonAddr) bool
	// KnownIOA reports whether the information object exists for requests of
	// that type under that station.
	KnownIOA(ca asdu.CommonAddr, t asdu.TypeID, ioa asdu.InfoObjAddr) bool
}

// FunctionPolicy denies request types. The zero value allows everything.
type FunctionPolicy struct {
	// DenyTypes lists type identifications that are refused outright.
	DenyTypes []asdu.TypeID
	// ReadOnly refuses every process command and parameter.
	ReadOnly bool
	// DenyResetProcess refuses C_RP_NA_1 (restart and reset).
	DenyResetProcess bool
	// DenyClockSync refuses C_CS_NA_1.
	DenyClockSync bool
	// DenyParameters refuses P_ME_* and P_AC_NA_1.
	DenyParameters bool
	// DenyFileTransfer refuses the file transfer ASDUs.
	DenyFileTransfer bool
	// DenyBroadcastCommands refuses any command to the global address.
	DenyBroadcastCommands bool
	// DenyWithUnknownType answers a refused request with "unknown type"
	// (cause 44) instead of the negative confirmation used by default.
	DenyWithUnknownType bool
}

// Denied reports whether a request is refused, and why.
func (f FunctionPolicy) Denied(t asdu.TypeID, ca asdu.CommonAddr) (string, bool) {
	for _, d := range f.DenyTypes {
		if d == t {
			return "type " + t.String() + " is denied by policy", true
		}
	}
	switch {
	case f.ReadOnly && (asdu.IsProcessCommand(t) || asdu.IsParameter(t)):
		return "the station is read-only", true
	case f.DenyResetProcess && t == asdu.C_RP_NA_1:
		return "reset process is denied by policy", true
	case f.DenyClockSync && t == asdu.C_CS_NA_1:
		return "clock synchronisation is denied by policy", true
	case f.DenyParameters && asdu.IsParameter(t):
		return "parameter changes are denied by policy", true
	case f.DenyFileTransfer && asdu.IsFile(t):
		return "file transfer is denied by policy", true
	case f.DenyBroadcastCommands && ca == asdu.GlobalCommonAddr && asdu.IsControlDirection(t):
		return "commands to the global address are denied by policy", true
	}
	return "", false
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

// WithDefaults returns p with every unset threshold, limit and helper filled in
// with the library's default. A Guard applies it itself; code that inspects
// traffic without a Guard must apply it before use.
func (p Policy) WithDefaults() Policy {
	p.defaults()
	return p
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
	if p.ClockStepThreshold == 0 {
		p.ClockStepThreshold = time.Minute
	}
	if p.ScanThreshold <= 0 {
		p.ScanThreshold = 20
	}
	if p.ScanWindow <= 0 {
		p.ScanWindow = time.Minute
	}
	def := func(r *Rate, n int, per time.Duration) {
		if r.N == 0 {
			*r = Rate{n, per}
		}
	}
	l := &p.Limits
	def(&l.Commands, 20, 10*time.Second)
	def(&l.Interrogation, 6, time.Minute)
	def(&l.CounterInterrogation, 6, time.Minute)
	def(&l.Read, 500, 10*time.Second)
	def(&l.ClockSync, 6, time.Minute)
	def(&l.ResetProcess, 2, 10*time.Minute)
	def(&l.TestFR, 20, 10*time.Second)
	def(&l.UToggle, 5, 10*time.Second)
	def(&l.SFrames, 500, 10*time.Second)
	def(&l.Frames, 20000, 10*time.Second)
}

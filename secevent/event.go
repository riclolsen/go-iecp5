// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

// Package secevent carries security-relevant events from the protocol
// endpoints (cs104, cs101, cs103, filetransfer) to the application.
//
// An endpoint reports what it observes: connections it did not expect,
// certificates it rejected, frames and ASDUs no conforming peer sends,
// sequence errors, control functions worth an audit trail, floods. The
// application receives them through a Sink and decides what they mean.
//
// Delivery never blocks a protocol goroutine and never grows without bound:
// events pass through a bounded queue, repeats are aggregated, and whatever
// is dropped is counted and reported. With no sink set an endpoint pays one
// atomic load per check.
package secevent

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
)

// Category groups kinds for filtering.
type Category string

// Categories.
const (
	CatConnection   Category = "connection"
	CatPeer         Category = "peer"
	CatTLS          Category = "tls"
	CatProtocol     Category = "protocol"
	CatSequence     Category = "sequence"
	CatAccess       Category = "access"
	CatControl      Category = "control"
	CatUnsolicited  Category = "unsolicited"
	CatFlood        Category = "flood"
	CatResource     Category = "resource"
	CatFileTransfer Category = "file"
	CatMeta         Category = "meta"
)

// Severity follows RFC 5424 loosely, from least to most severe.
type Severity uint8

// Severities.
const (
	SevInfo Severity = iota
	SevNotice
	SevWarning
	SevAlert
	SevCritical
)

func (s Severity) String() string {
	switch s {
	case SevInfo:
		return "info"
	case SevNotice:
		return "notice"
	case SevWarning:
		return "warning"
	case SevAlert:
		return "alert"
	case SevCritical:
		return "critical"
	}
	return fmt.Sprintf("severity(%d)", uint8(s))
}

// MarshalText implements encoding.TextMarshaler.
func (s Severity) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// Action is what the endpoint did about the event.
type Action string

// Actions.
const (
	ActObserved     Action = "observed"     // noted, nothing done
	ActIgnored      Action = "ignored"      // the offending item was discarded
	ActRejected     Action = "rejected"     // the request was refused
	ActThrottled    Action = "throttled"    // excess traffic was dropped
	ActDisconnected Action = "disconnected" // the connection was closed
)

// FieldMask says which protocol context fields of an Event are meaningful,
// since zero is a valid value for most of them (IOA 0, for one).
type FieldMask uint16

// Field flags.
const (
	FieldLinkAddr FieldMask = 1 << iota
	FieldType
	FieldCause
	FieldCommonAddr
	FieldIOA
	FieldOrigAddr
	FieldSeq
)

// MACSource says where a MAC address came from.
type MACSource string

// MAC sources.
const (
	MACNone     MACSource = ""         // unknown
	MACNeighbor MACSource = "neighbor" // the peer's own address, from the neighbour table
	MACGateway  MACSource = "gateway"  // the peer is routed: this is the next hop's address
	MACStatic   MACSource = "static"   // supplied by configuration
	MACCustom   MACSource = "custom"   // supplied by a user Resolver
)

// TLSPeer describes the TLS state of a connection.
type TLSPeer struct {
	Version           uint16    `json:"version"`
	CipherSuite       uint16    `json:"cipher_suite"`
	ServerName        string    `json:"server_name,omitempty"`
	Subject           string    `json:"subject,omitempty"`
	Issuer            string    `json:"issuer,omitempty"`
	SerialHex         string    `json:"serial,omitempty"`
	NotAfter          time.Time `json:"not_after,omitempty"`
	FingerprintSHA256 string    `json:"fingerprint_sha256,omitempty"`
	Verified          bool      `json:"verified"`
}

// PeerInfo identifies the other end of a connection.
type PeerInfo struct {
	Network     string           // "tcp" or "serial"
	RemoteAddr  netip.AddrPort   // zero for serial
	LocalAddr   netip.AddrPort   // zero for serial
	MAC         net.HardwareAddr // nil when unknown
	MACSource   MACSource
	OnLink      bool   // the peer's IP is on a directly attached subnet
	Interface   string // network interface the peer was reached through
	TLS         *TLSPeer
	ConnectedAt time.Time
	SerialPort  string
}

// String is a compact one-line form.
func (p PeerInfo) String() string {
	var b strings.Builder
	switch {
	case p.RemoteAddr.IsValid():
		b.WriteString(p.RemoteAddr.String())
	case p.SerialPort != "":
		b.WriteString(p.SerialPort)
	default:
		b.WriteString("unknown")
	}
	if p.MAC != nil {
		fmt.Fprintf(&b, " mac=%s", p.MAC)
		if p.MACSource == MACGateway {
			b.WriteString("(gateway)")
		}
	}
	if p.TLS != nil {
		b.WriteString(" tls")
	}
	return b.String()
}

// Event is one security-relevant observation.
//
// Kind, Category, Severity and Technique come from the table in kinds.go;
// an endpoint only supplies what varies. Fields are copies: the Event may be
// kept, and Raw never aliases a protocol buffer.
type Event struct {
	Time     time.Time
	Kind     Kind
	Category Category
	Severity Severity
	Action   Action

	Protocol  string // "iec104", "iec101", "iec103"
	Role      string // "server", "client", "server-special", "primary", "secondary"
	Endpoint  int    // the endpoint's server or client number
	SessionID uint64 // groups the events of one connection
	Peer      PeerInfo
	Local     string

	Fields     FieldMask
	LinkAddr   uint16
	TypeID     asdu.TypeID
	Cause      asdu.Cause
	Negative   bool
	Test       bool
	OrigAddr   asdu.OriginAddr
	CommonAddr asdu.CommonAddr
	IOA        asdu.InfoObjAddr
	SendSN     uint16
	RecvSN     uint16
	ExpectedSN uint16
	Value      string // command value or qualifier, bounded

	Count     int       // greater than 1 when repeats were aggregated
	FirstSeen time.Time // start of the aggregation window
	Detail    string    // one line, no secrets
	Err       error
	Raw       []byte // the offending frame, truncated
	Technique string // MITRE ATT&CK for ICS hint
}

// String is a one-line form for logs.
func (e Event) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s %s", e.Severity, e.Kind, e.Peer)
	if e.Count > 1 {
		fmt.Fprintf(&b, " x%d", e.Count)
	}
	if e.Fields&FieldCommonAddr != 0 {
		fmt.Fprintf(&b, " ca=%d", e.CommonAddr)
	}
	if e.Fields&FieldIOA != 0 {
		fmt.Fprintf(&b, " ioa=%d", e.IOA)
	}
	if e.Fields&FieldType != 0 {
		fmt.Fprintf(&b, " type=%s", e.TypeID)
	}
	if e.Fields&FieldLinkAddr != 0 {
		fmt.Fprintf(&b, " link=%d", e.LinkAddr)
	}
	if e.Detail != "" {
		b.WriteString(": " + e.Detail)
	}
	if e.Err != nil {
		b.WriteString(" (" + e.Err.Error() + ")")
	}
	return b.String()
}

// MarshalJSON gives the stable JSON form: field names never change.
func (e Event) MarshalJSON() ([]byte, error) {
	type peer struct {
		Network    string   `json:"network,omitempty"`
		Remote     string   `json:"remote,omitempty"`
		Local      string   `json:"local,omitempty"`
		MAC        string   `json:"mac,omitempty"`
		MACSource  string   `json:"mac_source,omitempty"`
		OnLink     bool     `json:"on_link,omitempty"`
		Interface  string   `json:"interface,omitempty"`
		TLS        *TLSPeer `json:"tls,omitempty"`
		Connected  string   `json:"connected_at,omitempty"`
		SerialPort string   `json:"serial_port,omitempty"`
	}
	type out struct {
		Time       string  `json:"time"`
		Kind       Kind    `json:"kind"`
		Category   string  `json:"category"`
		Severity   string  `json:"severity"`
		Action     Action  `json:"action,omitempty"`
		Protocol   string  `json:"protocol,omitempty"`
		Role       string  `json:"role,omitempty"`
		Endpoint   int     `json:"endpoint,omitempty"`
		SessionID  uint64  `json:"session,omitempty"`
		Peer       peer    `json:"peer"`
		LinkAddr   *uint16 `json:"link_addr,omitempty"`
		TypeID     *uint8  `json:"type_id,omitempty"`
		TypeName   string  `json:"type,omitempty"`
		Cause      *uint8  `json:"cause,omitempty"`
		Negative   bool    `json:"negative,omitempty"`
		Test       bool    `json:"test,omitempty"`
		OrigAddr   *uint8  `json:"originator,omitempty"`
		CommonAddr *uint16 `json:"common_addr,omitempty"`
		IOA        *uint32 `json:"ioa,omitempty"`
		SendSN     *uint16 `json:"send_sn,omitempty"`
		RecvSN     *uint16 `json:"recv_sn,omitempty"`
		ExpectedSN *uint16 `json:"expected_sn,omitempty"`
		Value      string  `json:"value,omitempty"`
		Count      int     `json:"count,omitempty"`
		FirstSeen  string  `json:"first_seen,omitempty"`
		Detail     string  `json:"detail,omitempty"`
		Error      string  `json:"error,omitempty"`
		Raw        string  `json:"raw,omitempty"`
		Technique  string  `json:"technique,omitempty"`
	}
	o := out{
		Time: e.Time.UTC().Format(time.RFC3339Nano), Kind: e.Kind,
		Category: string(e.Category), Severity: e.Severity.String(), Action: e.Action,
		Protocol: e.Protocol, Role: e.Role, Endpoint: e.Endpoint, SessionID: e.SessionID,
		Negative: e.Negative, Test: e.Test, Value: e.Value, Count: e.Count,
		Detail: e.Detail, Technique: e.Technique, Raw: hex.EncodeToString(e.Raw),
	}
	if e.Err != nil {
		o.Error = e.Err.Error()
	}
	if !e.FirstSeen.IsZero() {
		o.FirstSeen = e.FirstSeen.UTC().Format(time.RFC3339Nano)
	}
	p := peer{Network: e.Peer.Network, MACSource: string(e.Peer.MACSource), OnLink: e.Peer.OnLink,
		Interface: e.Peer.Interface, TLS: e.Peer.TLS, SerialPort: e.Peer.SerialPort}
	if e.Peer.RemoteAddr.IsValid() {
		p.Remote = e.Peer.RemoteAddr.String()
	}
	if e.Peer.LocalAddr.IsValid() {
		p.Local = e.Peer.LocalAddr.String()
	}
	if e.Peer.MAC != nil {
		p.MAC = e.Peer.MAC.String()
	}
	if !e.Peer.ConnectedAt.IsZero() {
		p.Connected = e.Peer.ConnectedAt.UTC().Format(time.RFC3339Nano)
	}
	o.Peer = p
	if e.Fields&FieldLinkAddr != 0 {
		v := e.LinkAddr
		o.LinkAddr = &v
	}
	if e.Fields&FieldType != 0 {
		v := uint8(e.TypeID)
		o.TypeID, o.TypeName = &v, e.TypeID.String()
	}
	if e.Fields&FieldCause != 0 {
		v := uint8(e.Cause)
		o.Cause = &v
	}
	if e.Fields&FieldOrigAddr != 0 {
		v := uint8(e.OrigAddr)
		o.OrigAddr = &v
	}
	if e.Fields&FieldCommonAddr != 0 {
		v := uint16(e.CommonAddr)
		o.CommonAddr = &v
	}
	if e.Fields&FieldIOA != 0 {
		v := uint32(e.IOA)
		o.IOA = &v
	}
	if e.Fields&FieldSeq != 0 {
		s, r, x := e.SendSN, e.RecvSN, e.ExpectedSN
		o.SendSN, o.RecvSN, o.ExpectedSN = &s, &r, &x
	}
	return json.Marshal(o)
}

var sessionCounter atomic.Uint64

// NextSessionID returns a process-unique session identifier.
func NextSessionID() uint64 { return sessionCounter.Add(1) }

// Sink receives events. Handle is called from a single dedicated goroutine
// per Dispatcher, so it need not be safe for concurrent use, and it may take
// its time: a slow sink makes events queue, then drop, but never slows the
// protocol.
type Sink interface{ Handle(Event) }

// SinkFunc adapts a function to a Sink.
type SinkFunc func(Event)

// Handle implements Sink.
func (f SinkFunc) Handle(e Event) { f(e) }

// Reporter is implemented by every endpoint and session. Code that holds
// only an asdu.Connect, such as a handler or the filetransfer package, reports
// through Report.
type Reporter interface{ ReportCyberEvent(Event) }

// Peerer is implemented by connections that know who is on the other end.
type Peerer interface{ PeerInfo() PeerInfo }

// Report sends e through c's endpoint, which fills in the peer, session,
// protocol and role. It returns false when c cannot report events or no sink
// is set.
func Report(c asdu.Connect, e Event) bool {
	r, ok := c.(Reporter)
	if !ok {
		return false
	}
	r.ReportCyberEvent(e)
	return true
}

// PeerOf returns what is known about the peer behind c.
func PeerOf(c asdu.Connect) (PeerInfo, bool) {
	p, ok := c.(Peerer)
	if !ok {
		return PeerInfo{}, false
	}
	return p.PeerInfo(), true
}

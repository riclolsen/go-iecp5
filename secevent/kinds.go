// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package secevent

// Kind names an event type. The strings are part of the public API: they are
// what a SIEM rule matches, so they are never renamed. New kinds are added,
// and obsolete ones deprecated, not changed.
type Kind string

// Connection.
const (
	KindConnAccepted       Kind = "conn.accepted"
	KindConnUnexpected     Kind = "conn.unexpected"
	KindConnUnexpectedMAC  Kind = "conn.unexpected_mac"
	KindConnRejectedLimit  Kind = "conn.rejected_limit"
	KindConnRejectedBusy   Kind = "conn.rejected_busy"
	KindConnRate           Kind = "conn.rate"
	KindConnNoData         Kind = "conn.no_data"
	KindConnNeverStarted   Kind = "conn.never_started"
	KindConnClosed         Kind = "conn.closed"
	KindConnT1Timeout      Kind = "conn.t1_timeout"
	KindConnDialFailed     Kind = "conn.dial_failed"
	KindConnAcceptError    Kind = "conn.accept_error"
	KindConnSecondActive   Kind = "conn.redundancy_second_active"
	KindTLSCertRejected    Kind = "tls.cert_rejected"
	KindTLSNoClientCert    Kind = "tls.no_client_cert"
	KindTLSLocalCertReject Kind = "tls.local_cert_rejected"
	KindTLSPlaintext       Kind = "tls.plaintext_on_tls_port"
	KindTLSNonTLSProbe     Kind = "tls.non_tls_probe"
	KindTLSHandshakeFailed Kind = "tls.handshake_failed"
	KindTLSHandshakeTO     Kind = "tls.handshake_timeout"
	KindTLSWeakParams      Kind = "tls.weak_params"
	KindTLSCertExpiring    Kind = "tls.cert_expiring"
	KindTLSCertChanged     Kind = "tls.cert_changed"
)

// Peer identity.
const (
	KindPeerMACChanged     Kind = "peer.mac_changed"
	KindPeerMACChangedLive Kind = "peer.mac_changed_midsession"
	KindPeerMACPinning     Kind = "peer.mac_pinning_violation"
	KindPeerIPChanged      Kind = "peer.ip_changed"
	KindPeerMACShared      Kind = "peer.mac_shared"
	KindPeerMACUnavailable Kind = "peer.mac_unavailable"
)

// Addressing and link layer.
const (
	KindAccessUnknownCA       Kind = "access.unknown_ca"
	KindAccessUnknownIOA      Kind = "access.unknown_ioa"
	KindAccessUnknownType     Kind = "access.unknown_type"
	KindAccessBroadcastMisuse Kind = "access.broadcast_misuse"
	KindAccessScan            Kind = "access.scan"
	KindAccessTypeBlocked     Kind = "access.type_blocked"
	KindAccessOriginator      Kind = "access.originator_unexpected"
	KindAccessOversized       Kind = "access.oversized_request"
	KindLinkForeignAddress    Kind = "link.foreign_address"
	KindLinkUnexpectedAddr    Kind = "link.unexpected_address"
	KindLinkBroadcastReply    Kind = "link.broadcast_reply_requested"
	KindLinkWrongPRM          Kind = "link.wrong_prm"
)

// Protocol validity.
const (
	KindProtoGarbage        Kind = "proto.garbage"
	KindProtoBadLength      Kind = "proto.bad_length"
	KindProtoBadAPCI        Kind = "proto.bad_apci"
	KindProtoIWhileStopped  Kind = "proto.i_while_stopped"
	KindProtoASDUInvalid    Kind = "proto.asdu_invalid"
	KindProtoFieldInvalid   Kind = "proto.field_invalid"
	KindProtoUnknownCOT     Kind = "proto.unknown_cot"
	KindProtoWrongDirection Kind = "proto.wrong_direction"
	KindProtoUWrongDir      Kind = "proto.u_wrong_direction"
	KindProtoNotInProfile   Kind = "proto.type_not_in_profile"
	KindProtoSecureAuth     Kind = "proto.secure_auth_type"
	KindProtoCS101Frame     Kind = "proto.cs101_frame"
	KindProtoCS101Function  Kind = "proto.cs101_function"
	KindProtoCS101FCV       Kind = "proto.cs101_fcv"
	KindProtoCS103Unknown   Kind = "proto.cs103_unknown"
)

// Control functions and commands: the audit trail.
const (
	KindCtrlStartDT       Kind = "ctrl.startdt"
	KindCtrlStopDT        Kind = "ctrl.stopdt"
	KindCtrlResetProcess  Kind = "ctrl.reset_process"
	KindCtrlClockSync     Kind = "ctrl.clock_sync"
	KindCtrlClockStep     Kind = "ctrl.clock_step"
	KindCtrlTestCommand   Kind = "ctrl.test_command"
	KindCtrlDelayAcq      Kind = "ctrl.delay_acquisition"
	KindCtrlParamChange   Kind = "ctrl.parameter_change"
	KindCtrlParamActivate Kind = "ctrl.parameter_activation"
	KindCtrlInterrogation Kind = "ctrl.interrogation"
	KindCtrlEndOfInit     Kind = "ctrl.end_of_init"
	KindCtrlLinkReset     Kind = "ctrl.link_reset"
	KindCtrlCS103Setting  Kind = "ctrl.cs103_setting_change"
	KindCmdReceived       Kind = "cmd.received"
	KindCmdSelectNoExec   Kind = "cmd.select_without_execute"
	KindCmdExecNoSelect   Kind = "cmd.execute_without_select"
	KindCmdRejected       Kind = "cmd.rejected"
	KindCmdBruteForce     Kind = "cmd.brute_force"
	KindCmdStaleTimeTag   Kind = "cmd.stale_timetag"
	KindCmdDuplicate      Kind = "cmd.duplicate"
	KindRemoteRejected    Kind = "remote.rejected"
)

// Sequence and replay.
const (
	KindSeqNsDuplicate Kind = "seq.ns_duplicate"
	KindSeqNsGap       Kind = "seq.ns_gap"
	KindSeqNsBehind    Kind = "seq.ns_behind"
	KindSeqNrAhead     Kind = "seq.nr_ahead"
	KindSeqNrRegressed Kind = "seq.nr_regressed"
	KindSeqFCBRepeat   Kind = "seq.fcb_repeat"
)

// Unsolicited.
const (
	KindUnsolUConfirm     Kind = "unsol.u_confirm"
	KindUnsolActResponse  Kind = "unsol.act_response"
	KindUnsolInterrogated Kind = "unsol.interrogated"
	KindUnsolLinkConfirm  Kind = "unsol.link_confirm"
	KindUnsolFile         Kind = "unsol.file"
)

// File transfer.
const (
	KindFileDirectoryCall   Kind = "file.directory_call"
	KindFileUpload          Kind = "file.upload"
	KindFileDownloadAttempt Kind = "file.download_attempt"
	KindFileRejected        Kind = "file.rejected"
	KindFileChecksum        Kind = "file.checksum"
	KindFileOversized       Kind = "file.oversized"
	KindFileUnsupported     Kind = "file.unsupported"
)

// Flooding and resources.
const (
	KindFloodCommands      Kind = "flood.commands"
	KindFloodInterrogation Kind = "flood.interrogation"
	KindFloodCounterInterr Kind = "flood.counter_interrogation"
	KindFloodRead          Kind = "flood.read"
	KindFloodClockSync     Kind = "flood.clock_sync"
	KindFloodResetProcess  Kind = "flood.reset_process"
	KindFloodTestFR        Kind = "flood.testfr"
	KindFloodUToggle       Kind = "flood.u_toggle"
	KindFloodSFrames       Kind = "flood.s_frames"
	KindFloodFrames        Kind = "flood.frames"
	KindFloodCS101Requests Kind = "flood.cs101_requests"
	KindDoSRxBacklog       Kind = "dos.rx_backlog"
	KindDoSTxBacklog       Kind = "dos.tx_backlog"
	KindDoSWindowStall     Kind = "dos.window_stall"
	KindDoSWriteStall      Kind = "dos.write_stall"
	KindDoSSlowFrame       Kind = "dos.slow_frame"
)

// Meta: events about the event system itself.
const (
	KindMetaEventsDropped Kind = "meta.events_dropped"
	KindMetaSinkPanic     Kind = "meta.sink_panic"
)

// KindInfo is the fixed description of a kind.
type KindInfo struct {
	Category  Category
	Severity  Severity
	Technique string // MITRE ATT&CK for ICS technique hint
	// Always marks kinds that bypass aggregation: each occurrence matters.
	Always bool
}

// ATT&CK for ICS technique hints. They steer SIEM routing; they are not an
// attribution.
const (
	tDoS       = "T0814"
	tUnauthCmd = "T0855"
	tRemoteDis = "T0846"
	tTagID     = "T0861"
	tMITM      = "T0830"
	tModParam  = "T0836"
	tRestart   = "T0816"
	tDownload  = "T0843"
	tUpload    = "T0845"
	tBruteIO   = "T0806"
	tMasq      = "T0849"
	tSpoofRpt  = "T0856"
	tExploit   = "T0866"
)

var kinds = map[Kind]KindInfo{
	KindConnAccepted:       {CatConnection, SevInfo, "", false},
	KindConnUnexpected:     {CatConnection, SevWarning, tRemoteDis, true},
	KindConnUnexpectedMAC:  {CatConnection, SevAlert, tMasq, true},
	KindConnRejectedLimit:  {CatConnection, SevWarning, tDoS, false},
	KindConnRejectedBusy:   {CatConnection, SevNotice, "", false},
	KindConnRate:           {CatConnection, SevAlert, tDoS, false},
	KindConnNoData:         {CatConnection, SevNotice, tRemoteDis, false},
	KindConnNeverStarted:   {CatConnection, SevWarning, tDoS, false},
	KindConnClosed:         {CatConnection, SevInfo, "", false},
	KindConnT1Timeout:      {CatConnection, SevNotice, "", false},
	KindConnDialFailed:     {CatConnection, SevNotice, "", false},
	KindConnAcceptError:    {CatResource, SevCritical, tDoS, true},
	KindConnSecondActive:   {CatConnection, SevNotice, "", false},
	KindTLSCertRejected:    {CatTLS, SevAlert, tMasq, true},
	KindTLSNoClientCert:    {CatTLS, SevAlert, tMasq, true},
	KindTLSLocalCertReject: {CatTLS, SevWarning, tMITM, true},
	KindTLSPlaintext:       {CatTLS, SevWarning, "", false},
	KindTLSNonTLSProbe:     {CatTLS, SevNotice, tRemoteDis, false},
	KindTLSHandshakeFailed: {CatTLS, SevWarning, "", false},
	KindTLSHandshakeTO:     {CatTLS, SevWarning, tDoS, false},
	KindTLSWeakParams:      {CatTLS, SevNotice, "", false},
	KindTLSCertExpiring:    {CatTLS, SevNotice, "", false},
	KindTLSCertChanged:     {CatTLS, SevNotice, tMITM, true},

	KindPeerMACChanged:     {CatPeer, SevAlert, tMITM, true},
	KindPeerMACChangedLive: {CatPeer, SevCritical, tMITM, true},
	KindPeerMACPinning:     {CatPeer, SevCritical, tMasq, true},
	KindPeerIPChanged:      {CatPeer, SevInfo, "", false},
	KindPeerMACShared:      {CatPeer, SevNotice, tMITM, false},
	KindPeerMACUnavailable: {CatPeer, SevInfo, "", false},

	KindAccessUnknownCA:       {CatAccess, SevWarning, tTagID, false},
	KindAccessUnknownIOA:      {CatAccess, SevWarning, tTagID, false},
	KindAccessUnknownType:     {CatAccess, SevWarning, tTagID, false},
	KindAccessBroadcastMisuse: {CatAccess, SevAlert, tUnauthCmd, false},
	KindAccessScan:            {CatAccess, SevAlert, tTagID, false},
	KindAccessTypeBlocked:     {CatAccess, SevWarning, tUnauthCmd, false},
	KindAccessOriginator:      {CatAccess, SevNotice, "", false},
	KindAccessOversized:       {CatAccess, SevWarning, tDoS, false},
	KindLinkForeignAddress:    {CatAccess, SevNotice, "", false},
	KindLinkUnexpectedAddr:    {CatAccess, SevWarning, tMasq, false},
	KindLinkBroadcastReply:    {CatAccess, SevWarning, "", false},
	KindLinkWrongPRM:          {CatAccess, SevWarning, "", false},

	KindProtoGarbage:        {CatProtocol, SevNotice, "", false},
	KindProtoBadLength:      {CatProtocol, SevWarning, tExploit, false},
	KindProtoBadAPCI:        {CatProtocol, SevWarning, tExploit, false},
	KindProtoIWhileStopped:  {CatProtocol, SevWarning, tUnauthCmd, false},
	KindProtoASDUInvalid:    {CatProtocol, SevWarning, tExploit, false},
	KindProtoFieldInvalid:   {CatProtocol, SevWarning, tExploit, false},
	KindProtoUnknownCOT:     {CatProtocol, SevWarning, "", false},
	KindProtoWrongDirection: {CatProtocol, SevWarning, tSpoofRpt, false},
	KindProtoUWrongDir:      {CatProtocol, SevWarning, "", false},
	KindProtoNotInProfile:   {CatProtocol, SevNotice, "", false},
	KindProtoSecureAuth:     {CatProtocol, SevNotice, "", false},
	KindProtoCS101Frame:     {CatProtocol, SevWarning, "", false},
	KindProtoCS101Function:  {CatProtocol, SevWarning, "", false},
	KindProtoCS101FCV:       {CatProtocol, SevNotice, "", false},
	KindProtoCS103Unknown:   {CatProtocol, SevNotice, "", false},

	KindCtrlStartDT:       {CatControl, SevInfo, "", false},
	KindCtrlStopDT:        {CatControl, SevInfo, "", false},
	KindCtrlResetProcess:  {CatControl, SevAlert, tRestart, true},
	KindCtrlClockSync:     {CatControl, SevNotice, "", false},
	KindCtrlClockStep:     {CatControl, SevAlert, tUnauthCmd, true},
	KindCtrlTestCommand:   {CatControl, SevInfo, "", false},
	KindCtrlDelayAcq:      {CatControl, SevInfo, "", false},
	KindCtrlParamChange:   {CatControl, SevAlert, tModParam, true},
	KindCtrlParamActivate: {CatControl, SevAlert, tModParam, true},
	KindCtrlInterrogation: {CatControl, SevInfo, "", false},
	KindCtrlEndOfInit:     {CatControl, SevNotice, tRestart, true},
	KindCtrlLinkReset:     {CatControl, SevNotice, "", false},
	KindCtrlCS103Setting:  {CatControl, SevAlert, tModParam, true},
	KindCmdReceived:       {CatControl, SevNotice, tUnauthCmd, true},
	KindCmdSelectNoExec:   {CatControl, SevWarning, tUnauthCmd, false},
	KindCmdExecNoSelect:   {CatControl, SevWarning, tUnauthCmd, false},
	KindCmdRejected:       {CatControl, SevNotice, tUnauthCmd, false},
	KindCmdBruteForce:     {CatControl, SevAlert, tBruteIO, false},
	KindCmdStaleTimeTag:   {CatControl, SevAlert, tUnauthCmd, true},
	KindCmdDuplicate:      {CatControl, SevWarning, tUnauthCmd, true},
	KindRemoteRejected:    {CatControl, SevNotice, "", false},

	KindSeqNsDuplicate: {CatSequence, SevAlert, tMITM, true},
	KindSeqNsGap:       {CatSequence, SevAlert, tMITM, true},
	KindSeqNsBehind:    {CatSequence, SevAlert, tMITM, true},
	KindSeqNrAhead:     {CatSequence, SevAlert, tMITM, true},
	KindSeqNrRegressed: {CatSequence, SevWarning, tMITM, true},
	KindSeqFCBRepeat:   {CatSequence, SevNotice, "", false},

	KindUnsolUConfirm:     {CatUnsolicited, SevWarning, tSpoofRpt, false},
	KindUnsolActResponse:  {CatUnsolicited, SevWarning, tSpoofRpt, false},
	KindUnsolInterrogated: {CatUnsolicited, SevNotice, tSpoofRpt, false},
	KindUnsolLinkConfirm:  {CatUnsolicited, SevNotice, "", false},
	KindUnsolFile:         {CatUnsolicited, SevWarning, "", false},

	KindFileDirectoryCall:   {CatFileTransfer, SevInfo, tRemoteDis, false},
	KindFileUpload:          {CatFileTransfer, SevNotice, tUpload, true},
	KindFileDownloadAttempt: {CatFileTransfer, SevAlert, tDownload, true},
	KindFileRejected:        {CatFileTransfer, SevWarning, "", false},
	KindFileChecksum:        {CatFileTransfer, SevWarning, "", false},
	KindFileOversized:       {CatFileTransfer, SevWarning, tDoS, false},
	KindFileUnsupported:     {CatFileTransfer, SevNotice, "", false},

	KindFloodCommands:      {CatFlood, SevAlert, tBruteIO, false},
	KindFloodInterrogation: {CatFlood, SevWarning, tDoS, false},
	KindFloodCounterInterr: {CatFlood, SevWarning, tDoS, false},
	KindFloodRead:          {CatFlood, SevWarning, tDoS, false},
	KindFloodClockSync:     {CatFlood, SevWarning, tDoS, false},
	KindFloodResetProcess:  {CatFlood, SevAlert, tRestart, false},
	KindFloodTestFR:        {CatFlood, SevWarning, tDoS, false},
	KindFloodUToggle:       {CatFlood, SevWarning, tDoS, false},
	KindFloodSFrames:       {CatFlood, SevWarning, tDoS, false},
	KindFloodFrames:        {CatFlood, SevAlert, tDoS, false},
	KindFloodCS101Requests: {CatFlood, SevWarning, tDoS, false},
	KindDoSRxBacklog:       {CatResource, SevWarning, tDoS, false},
	KindDoSTxBacklog:       {CatResource, SevWarning, tDoS, false},
	KindDoSWindowStall:     {CatResource, SevWarning, tDoS, false},
	KindDoSWriteStall:      {CatResource, SevAlert, tDoS, true},
	KindDoSSlowFrame:       {CatResource, SevAlert, tDoS, true},

	KindMetaEventsDropped: {CatMeta, SevWarning, "", true},
	KindMetaSinkPanic:     {CatMeta, SevAlert, "", true},
}

// Info describes a kind. Unknown kinds report ok false.
func Info(k Kind) (KindInfo, bool) { i, ok := kinds[k]; return i, ok }

// Kinds lists every defined kind, for documentation and tests.
func Kinds() []Kind {
	out := make([]Kind, 0, len(kinds))
	for k := range kinds {
		out = append(out, k)
	}
	return out
}

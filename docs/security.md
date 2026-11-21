# Security events

The library reports what looks wrong on the wire as **security events**
instead of burying it in debug logs. It is report-only by default: nothing is
rejected, throttled or disconnected until you set a policy that says so.

Events are raised by `cs104` (server, client, special server), `cs101`
(server, client), `cs103` (client) and `filetransfer`. They share one model,
in package `secevent`.

## Receiving events

```go
srv := cs104.NewServer(handler)
srv.SetCyberEventSink(secevent.JSONLinesSink(os.Stdout))
```

A sink is anything with `Handle(secevent.Event)`. Helpers: `JSONLinesSink`,
`ClogSink`, `ChannelSink`, `MultiSink` and `FilterSink(min, sink, categories...)`.

Delivery never slows the protocol. Events go through a bounded queue to one
goroutine; when the queue overflows events are dropped and counted
(`SecurityStats()`), and a `meta.events_dropped` event says how many. Repeats of
the same thing from one session are aggregated into one event with `Count`.
With no sink set, events cost one atomic load. The request policy (read-only,
denied functions, unknown objects, command age, rate limits) is enforced
whether or not a sink is set; only the events are skipped.

Several endpoints can share one dispatcher: `SetCyberDispatcher(d)`.

## What an event carries

`Kind` is a stable dotted string (`access.unknown_ioa`, `flood.testfr`,
`seq.fcb_repeat`, ...), with a `Category`, a `Severity` (Info to Critical), an
`Action` (observed, ignored, rejected, throttled, disconnected) and, where one
applies, a MITRE ATT&CK for ICS hint. Protocol context fields (link address,
type, cause, common address, IOA, sequence numbers) are valid only when the
matching bit is set in `Fields`. `Peer` holds the remote address, the **MAC
address** when known, and the TLS certificate summary.

## Peer MAC address

`session.PeerInfo()` returns the peer, MAC included, for any `cs104`
`SrvSession` or `Client` (and `secevent.PeerOf(conn)` for a bare
`asdu.Connect`). The MAC comes from the kernel neighbour table through package
`netpeer` and is only meaningful for a peer on the same link: for a routed peer
no MAC is reported, because the table would hold the router's. Backends:
Linux (netlink, `/proc/net/arp` fallback), Windows (`SendARP`, IPv4), and
Darwin/BSD (`arp`, `ndp`). Supply your own with `netpeer.SetDefault`.

A MAC is spoofable by anyone on the link. Use it for detection and correlation,
not as authentication.

## Policy

```go
p := secevent.DefaultPolicy()
p.AllowedPeers = []netip.Prefix{netip.MustParsePrefix("10.1.0.0/24")}
p.RejectUnlisted = true                // default: report only
p.MaxSessionsPerIP = 4
p.Functions = secevent.FunctionPolicy{ReadOnly: true, DenyFileTransfer: true}
p.AddressSpace = myStationModel        // KnownCA / KnownIOA
p.AutoRejectUnknown = true             // answer unknown objects with the proper cause
p.FloodAction = secevent.ActThrottled  // or ActDisconnected
srv.SetSecurityPolicy(p)
```

Everything defaults to observe. Kinds worth knowing:

| Area | Kinds |
|---|---|
| Connections | `conn.accepted`, `conn.unexpected`, `conn.rejected_limit`, `conn.rate`, `conn.accept_error`, `conn.closed`, `tls.*` |
| Identity | `peer.mac_changed`, `peer.mac_changed_midsession`, `peer.mac_pinning_violation`, `peer.ip_changed` |
| Link layer (101/103) | `link.foreign_address`, `link.unexpected_address`, `link.broadcast_reply_requested`, `link.wrong_prm`, `proto.cs101_frame`, `seq.fcb_repeat` |
| APCI / framing (104) | `proto.garbage`, `proto.bad_length`, `proto.bad_apci`, `seq.*` |
| ASDU | `proto.asdu_invalid`, `proto.wrong_direction`, `proto.type_not_in_profile`, `access.unknown_ca/ioa/type`, `access.scan`, `access.type_blocked` |
| Controls | commands, `ctrl.*` (restart, clock, parameters), `file.*` |
| Unsolicited (client) | `unsol.act_response`, `unsol.interrogated`, `ctrl.end_of_init` |
| Floods | `flood.commands`, `flood.testfr`, `flood.u_toggle`, `flood.frames`, `flood.s_frames` |

`secevent` exposes the full table.

## Reporting from your own handlers

Only your handler knows whether a point exists:

```go
func (h *H) ReadHandler(c asdu.Connect, a *asdu.ASDU, ioa asdu.InfoObjAddr) error {
	if !h.known(ioa) {
		secevent.Report(c, secevent.Event{Kind: secevent.KindAccessUnknownIOA, ...})
	}
	...
}
```

`cs101` and `cs103` handlers do not receive the connection; call
`server.ReportCyberEvent(e)` instead.

## Behaviour changes worth knowing

- A TESTFR is no longer queued while one is outstanding, so t1 can expire when
  t3 is shorter than t1.
- Idle links are policed by t3 only; a frame that has begun must complete
  within the assemble timeout.
- Accept errors back off (5 ms doubling to 1 s) instead of spinning.
- Some profile checks (a type outside the 104 profile) are flagged at Notice
  and never reject.

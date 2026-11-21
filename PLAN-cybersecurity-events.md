# Plan — cyber-security awareness for go-iecp5

- **Date:** 2026-11-21
- **Base:** `fbe6719` (master)
- **Scope:** `cs104`, `cs101`, `cs103`, `asdu`, `filetransfer`, `clog`, `docs`, `_examples`
- **Status:** plan only. No code written yet.

## 0. Goals and non-goals

### Goals

1. **Peer identity in the API.** Every TCP-based endpoint can report its peer's
   MAC address together with IP, port, TLS identity and connect time. The
   library notices when the MAC behind a known IP changes.
2. **A cyber-event channel.** One structured callback (a "sink") receives
   typed security events from every protocol layer: transport, TLS, APCI or
   link layer, ASDU, application and file transfer. It works the same way in
   cs104, cs101 and cs103.
3. **Detection of the requested event classes.** Each one maps to concrete
   code locations (catalogue in §4):
   unexpected connections · rejected certificates · invalid addresses, tags and
   objects · oversized requests · non-existent commands · wrong link addresses
   · rejected commands · invalid PDUs, fields, headers and functions · sequence
   errors and replay · restart, stop, halt, parameter, file and other unusual
   functions · MAC change for an IP · unsolicited responses · command flooding
   · keep-alive and test-frame storms · DoS and connection exhaustion.
4. **Optional hardening hooks.** Allowlists, limits and function policies the
   application can turn on. Every enforcement is opt-in. The default stays
   "observe and report".
5. **Zero cost and zero behaviour change when unused.** With no sink and no
   policy, wire behaviour and performance stay as they are today. The only
   exceptions are the robustness bug fixes in §7, which are called out
   explicitly.

### Non-goals

- Implementing IEC 62351-5 secure authentication (S_* types 81–95). We only
  *detect* them (§4.6).
- Kernel-level DoS mitigation (SYN floods, half-open TCP). User space cannot
  see these. Document the limitation and recommend SYN cookies and a firewall.
- Being an IDS. The library reports what it observes as a protocol endpoint.
  Correlation across devices belongs to the consumer (SIEM, IEC 62351-7 NSM
  agent).

---

## 1. Findings from the current code that shape the design

| # | Location | Observation | Consequence for the plan |
|---|----------|-------------|--------------------------|
| F1 | `cs104/server.go:118-121` | `listen.Accept()` error **returns from `ListenAndServer`**. A single `EMFILE`/`ENFILE` (fd exhaustion) or a transient error stops the outstation for good. | Real DoS hole. Fix: retry with backoff on temporary errors and emit `conn.accept_error` (§7.1). |
| F2 | `cs104/server.go:125-148` | No limit on concurrent sessions, per-IP sessions or accept rate. Each session costs 4 goroutines and 4 buffered channels sized k·16/k·32/w·16/w·32. | Connection-exhaustion detection and optional limits (§4.1, §6). |
| F3 | `cs104/server.go:87-91` | `tls.Listen` hands out a lazy `tls.Conn`. The handshake runs inside the first `Read` in `recvLoop`, so a failed handshake only shows up as `"receive failed, …"`. The certificate is never inspected. | Do the handshake explicitly, with a deadline, in the per-connection goroutine, and classify the error (§4.2). |
| F4 | `cs104/server_session.go:68-72`, `cs104/client.go:208-213` | `io.ReadFull` has **no read deadline**. A peer that sends `0x68` and stalls holds a partial frame for ever (slowloris). It is only caught indirectly, if TESTFR goes unanswered. | Frame-assembly timeout plus event (§4.10). |
| F5 | `server_session.go:148`, `client.go:272` | `conn.Write` has **no write deadline**. A peer advertising a zero TCP window blocks `sendLoop`. `sendRaw` fills up, `sf.sendRaw <-` at lines 200/204/235 blocks, and **the whole APCI state machine stalls** with no t₁ firing. | Write deadline plus `dos.write_stall` event (§7.2). |
| F6 | `server_session.go:104-116` | Resync silently throws away non-0x68 octets and bad length octets. | Count them and report as `proto.garbage` / `proto.bad_length`, aggregated (§4.5). |
| F7 | `server_session.go:301-310` | `parse()` errors are logged and ignored. That is good behaviour, but there is no structured signal. | `proto.bad_apci` event. The error type already exists (`ErrInvalidAPCI`). |
| F8 | `server_session.go:313-334` | N(R)/N(S) violations close the connection with one generic message. Duplicate, gap and out-of-window are not told apart. | Classify the violation and emit `seq.*` (§4.7). |
| F9 | `server_session.go:398-400` | The server receiving STARTDT con / STOPDT con (monitor-direction U-frames) and the client receiving STARTDT act / STOPDT act (`client.go:468`) end in the "illegal U-Frame" default. TESTFR con without an outstanding act is silently accepted (`server_session.go:396`, `client.go:465`). | Wrong-direction and unsolicited U-frame events (§4.8). |
| F10 | `server_session.go:493-598` | The server dispatcher already validates COT, CA and IOA for system commands and replies `UnknownCOT/CA/IOA/TypeID`. User handlers reply the same way (`_examples/cs104_server_general/commands.go:193,202`). | **Observe outbound replies**: any ASDU the session sends with COT 44–47, or with P/N=1 on a confirmation, becomes an event automatically, with no handler changes (§3.6). |
| F11 | `server_session.go:421` | ASDU decode errors (`asdu/error.go`: `ErrTypeIDZero`, `ErrTrailingOctets`, `ErrInfoObjSizeMismatch`, …) are logged and dropped. | `proto.asdu_invalid` with a sub-reason taken from the sentinel error (§4.5). |
| F12 | `cs104/client.go:452-466` | STARTDT con / STOPDT con are accepted even when no act was sent. `onActivated`/`onDeactivated` fire on an unsolicited con. | Unsolicited event. Optionally ignore the con (§4.8). |
| F13 | `cs104/client.go` (whole) | The client has no record of commands it sent, so it cannot recognise an unsolicited ActCon or ActTerm. | Outstanding-command tracker (§5.4). |
| F14 | `cs101/server.go:378-386` | Frames for other link addresses are "silently ignored". On a shared RS-485 bus this is normal. On point-to-point or TCP-encapsulated links it is anomalous. | Link-topology setting decides whether it is reported (§4.4). |
| F15 | `cs101/server.go:427-433`, `cs101/client.go:706` | An FCB mismatch triggers retransmission of the last response. Line noise causes this. So do replays and duplicated frames on encapsulated links. | Rate-based `seq.fcb_repeat` event (§4.7). |
| F16 | `cs101/transport.go:112` | The TCP-server transport accepts one connection per `Open()` call. Extra connections sit unanswered in the kernel backlog until the active one drops. | Accept-and-reject extra connections with `conn.rejected_busy` (§4.1). |
| F17 | `cs101/client.go:556-560,613`, `cs103/client.go:534-538,603` | "Confirm but none outstanding", "confirm from station X while waiting for Y" and "unexpected link address" are logged only. | Unsolicited and wrong-address events (§4.4, §4.8). |
| F18 | `filetransfer/sender.go:129-199`, `receiver.go:198-323` | Negative acks, unknown file or section, checksum failures and unsupported services come back as errors that callers mostly drop (`docs/filetransfer.md` shows `_ = err`). | Report through the Connect (§3.5, §4.9). |
| F19 | `clog/clog.go` | Logging is printf-style, off by default (`has=0`), and has no structure. | The event channel is separate from Clog. A Clog adapter sink is provided (§3.4). |
| F20 | `cs104/server_session.go:555,580` | `C_TS_NA_1` and `C_CD_NA_1` are dispatched in cs104. The 104 companion standard's ASDU selection excludes types with CP24Time2a and some 101-only system types. **Verify the exact list against IEC 60870-5-104 §8 during implementation.** | `proto.type_not_in_profile` event, report only (§4.5). |

---

## 2. Architecture overview

```
           ┌────────────────────────────────────────────────────────┐
           │                    application                         │
           │   sink := secevent.SinkFunc(func(e secevent.Event){…}) │
           └──────────────▲─────────────────────────────▲───────────┘
                          │ async, bounded, never blocks │ PeerInfo(), Stats()
                 ┌────────┴─────────┐          ┌────────┴─────────┐
                 │ secevent.Dispatcher│        │ netpeer (MAC)    │
                 │ (queue, aggregate,│         │ resolver + cache │
                 │  drop accounting) │         └────────▲─────────┘
                 └────────▲─────────┘                   │
        ┌─────────────────┼────────────────────┬────────┴───────┐
   internal/secmon detectors (token buckets, scan window, cmd tracker, conn tracker)
        │                 │                    │                │
   cs104.Server/Session   cs104.Client    cs101 Server/Client  cs103 Client
        │                                                       │
   asdu (validation helpers)            filetransfer (reports via Connect)
```

### New packages

| Package | Visibility | Contents |
|---------|-----------|----------|
| `secevent` | public | `Event`, `Kind`, `Category`, `Severity`, `Action`, `PeerInfo`, `Sink`/`SinkFunc`, `Dispatcher`, ready-made sinks, the `Reporter` interface and the `Report(c, e)` helper, `Policy`/`Limits` structs shared by all protocols, `Stats` counters. |
| `netpeer` | public | `LookupMAC(ip)`, `Resolver` interface, OS backends (Linux netlink + `/proc/net/arp`, Windows `GetIpNetTable2`, Darwin/BSD route sysctl), `Cache` (IP↔MAC history, change detection), `OnLink(ip)`. Usable on its own with any `net.Conn`. |
| `internal/secmon` | internal | Token bucket, sliding-window distinct counter (scan detector), connection tracker (per-IP counts and rates), outstanding-command tracker, aggregator and deduplicator, time-tag checker. Shared by cs101, cs103 and cs104. |

Names are still to be decided (§12, Q1). The alternatives are `cyber`,
`secmon` (public) and `iecsec`.

---

## 3. Event model and delivery

### 3.1 `secevent.Event`

```
Event {
    Time        time.Time
    Kind        Kind        // stable dotted code, e.g. "seq.ns_mismatch" (string type, constants)
    Category    Category    // Connection | Peer | TLS | Protocol | Sequence | Access |
                            //   Control | Unsolicited | Flood | Resource | FileTransfer | Meta
    Severity    Severity    // Info | Notice | Warning | Alert | Critical   (RFC 5424 aligned)
    Action      Action      // Observed | Ignored | Rejected | Throttled | Disconnected | Blocked
    Protocol    string      // "iec104" | "iec101" | "iec103"
    Role        string      // "server" | "client" | "server-special" | "primary" | "secondary"
    Endpoint    int         // existing serverNumber / clientNumber
    SessionID   uint64      // monotonically increasing per process; correlates events of one connection
    Peer        PeerInfo    // see 3.2
    Local       string      // local address or serial port

    // Protocol context — zero when not applicable; Has* bitmask says which are set
    Fields      FieldMask
    LinkAddr    uint16      // cs101/103
    TypeID      asdu.TypeID
    Cause       asdu.Cause
    Negative    bool
    Test        bool
    OrigAddr    asdu.OriginAddr
    CommonAddr  asdu.CommonAddr
    IOA         asdu.InfoObjAddr
    SendSN, RecvSN, ExpectedSN uint16   // cs104 sequence context
    Value       string      // human-readable command value / qualifier (bounded length)

    Count       int         // >1 when aggregated (3.3)
    FirstSeen   time.Time   // aggregation window start
    Detail      string      // one-line English explanation, no secrets
    Err         error       // underlying error (errors.Is-able sentinel where one exists)
    Raw         []byte      // copy of the offending frame, truncated to MaxRaw (default 64 B)
    Technique   string      // optional MITRE ATT&CK for ICS hint, e.g. "T0855" (§3.7)
}
```

Rules:

- `Kind` values are part of the public API and **must never be renamed**. Add
  new ones and deprecate old ones only. Each `Kind` has a fixed default
  `Category`, `Severity` and `Technique`, set in one table (`kinds.go`), so
  call sites only pass the variable fields.
- `Raw` is always a copy. The caller's buffer is reused.
- `Detail` never contains key material, passwords or full certificates (only
  the fingerprint and subject).
- `Event.String()` gives a one-line text form. `MarshalJSON` gives a stable
  JSON form (field names fixed, `omitempty`).

### 3.2 `secevent.PeerInfo`

```
PeerInfo {
    Network     string          // "tcp", "serial"
    RemoteAddr  netip.AddrPort  // zero for serial
    LocalAddr   netip.AddrPort
    MAC         net.HardwareAddr // nil when unknown
    MACSource   MACSource        // None | Neighbor | Gateway | Static | Custom
    OnLink      bool             // peer IP is inside a directly attached subnet
    Interface   string           // e.g. "eth0"
    TLS         *TLSPeer         // nil when plaintext
    ConnectedAt time.Time
    SerialPort  string           // cs101/103 serial
}
TLSPeer { Version uint16; CipherSuite uint16; ServerName string;
          Subject, Issuer string; SerialHex string; NotAfter time.Time;
          FingerprintSHA256 string; Verified bool }
```

Accessors:

- `(*cs104.SrvSession).PeerInfo() secevent.PeerInfo`, `(*cs104.Client).PeerInfo()`,
  `ServerSpecial.PeerInfo()`, `(*cs101.Server|Client).PeerInfo()`,
  `(*cs103.Client).PeerInfo()`.
- `secevent.PeerOf(c asdu.Connect) (PeerInfo, bool)`. Handlers only receive
  `asdu.Connect`, so this type-asserts an optional
  `interface{ PeerInfo() secevent.PeerInfo }`. **`asdu.Connect` itself is not
  changed**, so external implementers are not broken.
- `netpeer.LookupMAC(addr net.Addr)` for users with their own `net.Conn`
  (`UnderlyingConn()` already exists).
- `PeerInfo` is computed once at connect time and cached in the session. MAC
  re-checks (§5.2) update it under a mutex. The accessor returns a copy.

### 3.3 Delivery: `secevent.Dispatcher`

- **Never block a protocol goroutine.** Call sites do `emitter.emit(e)`, which
  checks one `atomic.Pointer[Dispatcher]`. Nil means return immediately: no
  allocation, no time read. This is the zero-cost path from goal 5.
- The dispatcher owns a bounded channel (default 1024) and one goroutine that
  calls the user sink. When the channel is full, the event is dropped and
  `dropped` is incremented. The next delivered event is preceded by a synthetic
  `meta.events_dropped{Count: n}`. The library never drops silently.
- **Aggregation / dedup** (this matters because the event path must not become
  a DoS amplifier). Per `(SessionID, Kind, key)` with
  key = CA/IOA/TypeID/LinkAddr where relevant: the first event is delivered at
  once. Repeats inside `AggregateWindow` (default 1 s) are counted and flushed
  as one event with `Count` and `FirstSeen`. Kinds marked *always-deliver*
  (e.g. `peer.mac_changed`, `tls.cert_rejected`, `ctrl.reset_process`) bypass
  aggregation. The aggregator memory is bounded (LRU, default 4096 keys).
- `Dispatcher.Close()` flushes. Endpoints that create their own dispatcher
  close it in `Close()`. A dispatcher can be shared between endpoints, e.g. one
  per process feeding a SIEM.
- A panicking user sink is recovered, counted and reported once as
  `meta.sink_panic`.
- `secevent.SyncSink(s)` wraps a sink for synchronous delivery. It exists for
  tests only and is documented as such.

### 3.4 Ready-made sinks

| Sink | Purpose |
|------|---------|
| `SinkFunc(func(Event))` | adapter |
| `ChannelSink(chan<- Event)` | non-blocking send, counts drops |
| `ClogSink(clog.LogProvider)` | maps Severity to `Critical/Error/Warn/Debug`, prefix `[CYBER]` |
| `JSONLinesSink(io.Writer)` | one JSON object per line (file, stdout, socket) |
| `SyslogFormatter` | RFC 5424 structured-data or CEF line formatting (no network client; the user supplies the writer) |
| `MultiSink(...Sink)` | fan-out |
| `FilterSink(min Severity, cats ...Category, s Sink)` | filtering |

### 3.5 Reporting from outside the library: `secevent.Reporter`

```
type Reporter interface { ReportCyberEvent(Event) }
func Report(c asdu.Connect, e Event) bool   // type-asserts, fills Peer/Session/Protocol/Role/Time, returns false if unsupported
```

All sessions and clients implement `Reporter`. Two kinds of code use it:

- **User handlers**, which know the database. For example, a `ReadHandler` that
  finds no point does
  `secevent.Report(c, secevent.Event{Kind: secevent.KindUnknownIOA, IOA: ioa})`.
  Constructor helpers keep this short:
  `secevent.UnknownIOA(a *asdu.ASDU, ioa)`, `secevent.CommandRejected(a, reason)`, …
- **`filetransfer`**, which reports through the same Connect, so its events
  inherit peer and session identity without a new setter.

### 3.6 Automatic detection from outbound replies

In `SrvSession.Send` (`cs104/server_session.go:612`) and the cs101 server's
outbound path, inspect the ASDU *before* marshalling. This only happens when a
dispatcher is attached.

| Outbound ASDU | Event |
|---------------|-------|
| COT 44 `UnknownTypeID` | `access.unknown_type` |
| COT 45 `UnknownCOT` | `proto.unknown_cot` |
| COT 46 `UnknownCA` | `access.unknown_ca` |
| COT 47 `UnknownIOA` | `access.unknown_ioa` |
| P/N=1 with COT 7 `ActivationCon` or 9 `DeactivationCon` on a `C_*` type | `cmd.rejected` |
| P/N=1 on C_IC / C_CI / C_RD / C_CS / C_RP | `cmd.rejected` (system command) |
| `F_AF_NA_1`/`F_SC_NA_1` negative / `FileError≠0` | `file.rejected` |

Every existing handler that already answers correctly produces events with no
changes, and the library's own replies at `server_session.go:505-596` are
covered. Correlation: the session keeps the last inbound ASDU identifier per
(TypeID, CA, IOA) for a short window, so the event carries the *request's*
OrigAddr and Raw.

Client side, mirrored: an *inbound* ASDU carrying COT 44–47 or P/N=1 means the
outstation rejected one of our requests. That is a weaker cyber signal (most
likely misconfiguration). Report it as `Notice` `remote.rejected`, so an
operator can spot a master that is being steered.

### 3.7 MITRE ATT&CK for ICS hints

`Technique` is a hint for SIEM routing, not an attribution. Starting mapping
(**check IDs against the current ATT&CK for ICS matrix when implementing**):
T0814 Denial of Service · T0855 Unauthorized Command Message · T0846 Remote
System Discovery · T0888 Remote System Information Discovery · T0861 Point & Tag
Identification · T0830 Adversary-in-the-Middle · T0836 Modify Parameter ·
T0816 Device Restart/Shutdown · T0843 Program Download · T0845 Program
Upload · T0806 Brute Force I/O · T0849 Masquerading · T0856 Spoof Reporting
Message · T0858 Change Operating Mode · T0815 Denial of View · T0866
Exploitation of Remote Services.

---

## 4. Event catalogue

Columns: **Kind** · default **Sev** · trigger · where · protocols (4=cs104, 1=cs101, 3=cs103; S=server/outstation, C=client/master).

### 4.1 Connection (`conn.*`)

| Kind | Sev | Trigger | Where | P |
|------|-----|---------|-------|---|
| `conn.accepted` | Info | Every accepted TCP connection, with full PeerInfo incl. MAC. This is the "log every connection" baseline. | `cs104/server.go:118` after Accept; `cs101/transport.go:112` | 4S 1S 3S(tcp) |
| `conn.unexpected` | Warning | Peer IP not in `Policy.AllowedPeers` (CIDR list). Action `Rejected` (closed before any byte is read, before the TLS handshake) or `Observed` when `RejectUnlisted=false`. | same, before session creation | 4S 1S |
| `conn.unexpected_mac` | Alert | IP allowed but MAC not in the pinned binding `Policy.PeerBindings[ip]` | same | 4S 1S |
| `conn.rejected_limit` | Warning | `MaxSessions` or `MaxSessionsPerIP` reached | same | 4S |
| `conn.rejected_busy` | Notice | cs101/103 TCP server already serving its single connection (F16). Accept and close at once instead of leaving the peer in the backlog. | new accept-drain goroutine in `Transporter` | 1S 3S |
| `conn.rate` | Alert | New connections from one IP exceed `AcceptRate` (token bucket, default 10 / 10 s). Also fires for distinct-IP bursts (`GlobalAcceptRate`). Connection storm or scan. | conn tracker | 4S 1S |
| `conn.no_data` | Notice | Connection closed by the peer without sending one valid frame. A TCP-connect scan or banner grab. | session end | 4S 1S |
| `conn.never_started` | Warning | cs104 session never sent STARTDT within `InactiveTimeout` (opt-in; 0 = off). It holds a slot without transferring data. Action `Disconnected`. | run loop ticker | 4S |
| `conn.closed` | Info | Session end, with `Detail` = reason (peer close, t₁, sequence error, policy), duration, frames, octets in and out | run defer | all |
| `conn.t1_timeout` | Notice | The peer stopped acknowledging or answering TESTFR (`server_session.go:273,281`). Blackholing or link loss. | ticker | 4 |
| `conn.dial_failed` | Notice | Client or ServerSpecial dial failure. Escalates to Warning after N consecutive failures. | `client.go` running(), `server_special.go` | 4C 1C 3C |
| `conn.accept_error` | Critical | Listener Accept error (EMFILE, ENFILE, ENOBUFS…). The resource-exhaustion indicator. See §7.1. | `server.go:118` | 4S 1S |
| `conn.redundancy_second_active` | Notice | A second session sends STARTDT while another session of the same Server is active (IEC 104 redundancy groups allow one active connection). Optional `MaxActiveSessions`. | uStartDtActive branch | 4S |

### 4.2 TLS (`tls.*`)

Handshake done explicitly: `tlsConn.HandshakeContext(ctx)` with deadline
`Policy.HandshakeTimeout` (default = t₀) in the per-connection goroutine, so a
slow handshake never blocks Accept (F3). Errors are classified with
`errors.As`.

| Kind | Sev | Trigger / classification |
|------|-----|--------------------------|
| `tls.cert_rejected` | Alert | Peer certificate failed verification: `*tls.CertificateVerificationError` → reason from `x509.UnknownAuthorityError`, `x509.CertificateInvalidError{Expired, NotAuthorizedToSign, …}`, `x509.HostnameError`. Event carries the unverified leaf's subject, issuer, serial, NotAfter and SHA-256 fingerprint. Server side (client certs) and client side (server cert). |
| `tls.no_client_cert` | Alert | `RequireAnyClientCert`/`RequireAndVerifyClientCert` and the client sent none |
| `tls.local_cert_rejected` | Warning | Peer sent alert `bad_certificate`/`unknown_ca`/`certificate_expired`: **it** does not trust **us**. Misconfiguration, or a MITM presenting a different chain. |
| `tls.plaintext_on_tls_port` | Warning | `tls.RecordHeaderError` whose first octet is `0x68`: an IEC 104 client speaking plaintext to the TLS port |
| `tls.non_tls_probe` | Notice | `RecordHeaderError` with any other leading octets (e.g. `GET `, `SSH-`, null probe): a scanner |
| `tls.handshake_failed` | Warning | Other handshake failures (version, no shared cipher, protocol). `Detail` = Go error text |
| `tls.handshake_timeout` | Warning | Deadline exceeded. Slow-handshake DoS. |
| `tls.weak_params` | Notice | Negotiated version < TLS 1.2, or a suite in `tls.InsecureCipherSuites()` (when the user's config allows them) |
| `tls.cert_expiring` | Notice | Peer leaf `NotAfter` within `CertExpiryWarning` (default 30 days) |
| `tls.cert_changed` | Notice | Peer IP presents a different leaf fingerprint than last time (trust-on-first-use history in the same cache as MAC, §5.2) |

Also provide `secevent.WrapTLSConfig(cfg, emitter)`. It chains a
`VerifyConnection` callback, so even users who do their own handshakes get
`tls.cert_*` events. The user's own callback is preserved.

### 4.3 Peer identity (`peer.*`)

| Kind | Sev | Trigger |
|------|-----|---------|
| `peer.mac_changed` | Alert | Connection from IP X with MAC M2, while the cache last saw X with M1 (within `MACHistoryTTL`, default 30 days). Possible ARP spoofing or MITM, or a legitimate device swap. Always delivered. |
| `peer.mac_changed_midsession` | Critical | Periodic re-check (`MACRecheckInterval`, opt-in, e.g. 60 s) finds the MAC behind a *live* session's IP changed. A strong MITM indicator. Optional action `Disconnected`. |
| `peer.mac_pinning_violation` | Critical | Pinned binding mismatch (also `conn.unexpected_mac` when rejected at accept) |
| `peer.ip_changed` | Info | A known MAC appears with a new IP (DHCP, re-addressing, or IP spoofing from the same host) |
| `peer.mac_shared` | Notice | One MAC behind several *on-link* IPs at the same time (proxy ARP or spoofing). Suppressed when `MACSource=Gateway`. |
| `peer.mac_unavailable` | Info | On-link peer but no neighbour entry. Delivered once per IP. |

### 4.4 Addressing and link layer (`access.*`, `link.*`)

| Kind | Sev | Trigger | Where |
|------|-----|---------|-------|
| `access.unknown_ca` | Warning | Outbound COT 46 (§3.6), or CA not in `Policy.AddressSpace` | session send / dispatcher |
| `access.unknown_ioa` | Warning | Outbound COT 47, or AddressSpace miss | idem |
| `access.unknown_type` | Warning | Outbound COT 44 (`server_session.go:596`): non-existent command or function | idem |
| `access.broadcast_misuse` | Alert | CA = global (0xFF / 0xFFFF) on a type where broadcast is not allowed. Allowed: C_IC, C_CI, C_CS, C_RP. Process commands C_SC…C_BO*, setpoints and P_* to broadcast are not legitimate. | server dispatcher, before handler |
| `access.scan` | Alert | One session hits ≥ `ScanThreshold` distinct unknown CA/IOA/TypeID within `ScanWindow` (default 20 in 60 s). Point and tag discovery. Same for distinct link addresses on point-to-point cs101. | `internal/secmon` sliding window |
| `access.type_blocked` | Warning | TypeID disabled by `Policy.Functions` (§6.3). The library replies UnknownTypeID (or negative con, configurable) and the handler is not called. | dispatcher |
| `access.originator_unexpected` | Notice | `Policy.AllowedOriginators` set and the ASDU's OA is not in it | dispatcher |
| `access.oversized_request` | Warning | Request ASDUs carrying more objects than meaningful: C_RD with VSQ>1, any single-object command (C_SC…C_BO, C_IC, C_CS, C_RP, …) with VSQ.Number≠1 or SQ=1, C_IC with QOI outside 20–36, file section call for a section beyond the file length, segment length > allowed | dispatcher / filetransfer |
| `link.foreign_address` | Notice | cs101/103 frame for another link address when `LinkTopology=PointToPoint` (TCP-encapsulated default). Silent on `SharedBus`. | `cs101/server.go:385` |
| `link.unexpected_address` | Warning | Master receives a response from a link address not in its station list (`cs101/client.go:613`, `cs103/client.go:603`) | |
| `link.broadcast_reply_requested` | Warning | Broadcast frame asking for a reply (`cs101/server.go:401`) | |
| `link.wrong_prm` | Warning | PRM=1 frame at an unbalanced master, PRM=0 at an unbalanced slave (`cs101/server.go:418`, `client.go:623`, `cs103/client.go:609`) | |

The library does not know which points exist. Three layers cover that:
(a) outbound-reply observation (§3.6), which needs no work from the user;
(b) an optional `secevent.AddressSpace` interface
`{ KnownCA(ca) bool; KnownIOA(ca, typ, ioa) bool }` that the library consults
before calling the handler, with optional auto-reply (`AutoRejectUnknown`,
default false so behaviour is unchanged); (c) `secevent.Report` from handlers.

### 4.5 Protocol validity (`proto.*`)

| Kind | Sev | Trigger | Where |
|------|-----|---------|-------|
| `proto.garbage` | Notice | Octets thrown away while resyncing to `0x68` (aggregated count) | `server_session.go:96-116`, `client.go:222-241` |
| `proto.bad_length` | Warning | APDU length octet < 4 or > 253 | same |
| `proto.bad_apci` | Warning | `parse()` returns `ErrInvalidAPCI`: I-frame without ASDU, reserved bits set, more than one U function, S/U with wrong length. Sub-reason from a new typed error (`apciError{Reason}`) so the `Detail` is stable. | `server_session.go:301`, `client.go:403` |
| `proto.i_while_stopped` | Warning | I-frame received in the STOPDT state (`server_session.go:322`, `client.go:422`). Injection attempt or a broken peer. | |
| `proto.asdu_invalid` | Warning | `UnmarshalBinary` failure. Sub-reason via `errors.Is`: `ErrTypeIDZero`, `ErrTypeIdentifier`, `ErrCauseZero`, `ErrCommonAddrZero`, `ErrInfoObjCountZero`, `ErrTrailingOctets`, `ErrInfoObjSizeMismatch`, `ErrLengthOutOfRange`, `ErrParam` | `server_session.go:421`, `client.go:490`, `cs101/server.go:501`, cs103 |
| `proto.field_invalid` | Warning | Semantically invalid field in a well-formed ASDU: `ErrInvalidTimeTag` (CP56 IV bit, out-of-range minute/hour/day), double command DCS 0 or 3 ("not permitted"), RCS 0 or 3, reserved QU values, QOI/QCC/QRP/QPM/QPA/COI reserved values, NaN/±Inf short floats in setpoints or parameters. New `asdu` validation helpers (§8), called before the handler. | dispatcher |
| `proto.unknown_cot` | Warning | COT not allowed for the type (outbound COT 45, plus checks for types the library dispatches) | |
| `proto.wrong_direction` | Warning | Server receives monitor-direction types (`M_*`, `F_FR_NA_1`, `F_SR_NA_1`, `F_SG_NA_1` when acting as file *receiver* is not configured), or a confirmation cause (7, 9, 10) on a command; client receives control-direction `C_*` with COT 6 or 8, or `P_*` activation | dispatcher |
| `proto.u_wrong_direction` | Warning | Server: STARTDT con or STOPDT con. Client: STARTDT act or STOPDT act. | `server_session.go:398`, `client.go:468` |
| `proto.type_not_in_profile` | Notice | Types outside the 104 companion selection (CP24 time-tag types 2, 4, 6, …, 19; C_TS_NA_1; C_CD_NA_1, **to be verified**, F20); reserved type ranges; private range 128–255 when `AllowPrivateTypes=false` | |
| `proto.secure_auth_type` | Notice | S_* 81–95 (IEC 62351-5) received while unsupported. A capability probe or a misconfigured peer. | |
| `proto.cs101_frame` | Warning | `ErrChecksumMismatch`, `ErrInvalidStartChar`, `ErrLengthMismatch`, `ErrInvalidEndChar`, `ErrFrameLenExceeded`, `ErrFrameTooShort` (`cs101/frame.go:216-222`). Aggregated: noise on serial is normal, so the default severity is Notice on serial and Warning on TCP. | `cs101/server.go:276`, `client.go:784`, cs103 |
| `proto.cs101_function` | Warning | Unknown or unhandled link function code (`cs101/server.go:489`, `client.go:665,684,747`, cs103) | |
| `proto.cs101_fcv` | Notice | Confirmed user data with FCV=0 (`cs101/server.go:460`) | |
| `proto.cs103_unknown` | Notice | cs103 FUN/INF or ASDU type unknown or unhandled | `cs103/client.go` |

### 4.6 Control and unusual functions, as an audit trail (`ctrl.*`, `cmd.*`)

These are not errors. They are security-relevant operations an operator wants
on record (IEC 62351-7 style). All are opt-out with `Policy.AuditControl=false`.

| Kind | Sev | Trigger |
|------|-----|---------|
| `ctrl.startdt` / `ctrl.stopdt` | Info | STARTDT/STOPDT act received (server) or confirmed (client) |
| `ctrl.reset_process` | Alert | C_RP_NA_1, with QRP (1 = general reset of process, 2 = reset of pending time-tagged events). **Restart/halt.** Always delivered. |
| `ctrl.clock_sync` | Notice | C_CS_NA_1, with `Detail` = received time and local-clock delta |
| `ctrl.clock_step` | Alert | \|delta\| > `ClockStepThreshold` (default 60 s): time manipulation that breaks time-tag-based defences |
| `ctrl.test_command` | Info | C_TS_NA_1 / C_TS_TA_1 |
| `ctrl.delay_acquisition` | Info | C_CD_NA_1 |
| `ctrl.parameter_change` | Alert | P_ME_NA_1/NB_1/NC_1 with COT 6 (thresholds, smoothing, limits: can hide alarms, T0878) |
| `ctrl.parameter_activation` | Alert | P_AC_NA_1 (QPA: act/deact of parameters, cyclic transmission, …) |
| `ctrl.interrogation` | Info | C_IC_NA_1 (with QOI) / C_CI_NA_1 (QCC freeze/reset!). `QCC FRZ=reset` → Notice. |
| `ctrl.end_of_init` | Notice | Client receives M_EI_NA_1. Outstation restarted; COI says local power-on, local manual reset or *remote* reset. Remote reset → Warning. |
| `ctrl.link_reset` | Notice | cs101 Reset Remote Link / Reset User Process received (`cs101/server.go:438,450`); cs103 Reset CU/FCB, identification ASDU 5 with COT reset or start-up |
| `ctrl.cs103_setting_change` | Alert | cs103 general command for setting-group activation, protection/auto-recloser/teleprotection on/off, or generic write (ASDU 10 write) seen on the link |
| `cmd.received` | Notice | Each process command C_SC/DC/RC/SE/BO (NA and TA): CA, IOA, value, S/E, QU, OA. The command audit trail. |
| `cmd.select_without_execute` / `cmd.execute_without_select` | Warning | Optional SBO tracker (§5.5): execute with no matching select, select timeout, execute on another IOA or value than selected |
| `cmd.rejected` | Notice | Negative ActCon/DeactCon sent (§3.6). Repeated rejections from one session within a window → escalates to `cmd.brute_force` (Alert, T0806). |
| `cmd.stale_timetag` | Alert | Time-tagged command (C_*_TA_1, C_TS_TA_1) whose CP56 tag is older than `MaxCommandAge` (opt-in, e.g. 10 s) or more than `MaxCommandFuture` in the future. **Replay of captured commands.** Action `Rejected` when `RejectStaleCommands=true`: the library sends a negative ActCon and skips the handler. The companion standard leaves the permitted delay as a system parameter; check the exact clause during implementation. |
| `cmd.duplicate` | Warning | Byte-identical command ASDU (incl. time tag) seen again within `DuplicateWindow` on any session of the same Server. A replay indicator independent of APCI sequence numbers, because a replay over a *new* TCP connection has fresh N(S). |
| `file.*` | see §4.9 | |

### 4.7 Sequence and replay (`seq.*`)

cs104 classification replacing the generic "fatal incoming acknowledge" at
`server_session.go:315,332` and `client.go:417,428`:

| Kind | Sev | Trigger |
|------|-----|---------|
| `seq.ns_duplicate` | Alert | I-frame N(S) == expected−1 (mod 32768): the previous frame again. **Replay or duplication.** |
| `seq.ns_gap` | Alert | N(S) ahead of expected: frames lost, or injected out of order |
| `seq.ns_behind` | Alert | N(S) further behind (older replay) |
| `seq.nr_ahead` | Alert | N(R) acknowledges I-frames we never sent: an injection or hijack indicator |
| `seq.nr_regressed` | Warning | N(R) earlier than the previous ack |
| `seq.peer_k_exceeded` | Warning | Peer has more than `PeerK` (default = our k) unacknowledged I-frames toward us: window violation, flood |
| `seq.fcb_repeat` | Notice→Alert | cs101/103 FCB mismatch (repeated frame, F15). Notice per event; Alert when the rate exceeds `FCBRepeatRate`. On TCP-encapsulated links one repeat is already Warning. |

Every `seq.*` event carries `SendSN`, `RecvSN`, `ExpectedSN` and `Raw`. The
connection-close behaviour stays as it is today (the standard requires closing
on sequence errors), so `Action=Disconnected`.

### 4.8 Unsolicited (`unsol.*`)

| Kind | Sev | Trigger |
|------|-----|---------|
| `unsol.u_confirm` | Warning | TESTFR con without an outstanding TESTFR act (server and client); STARTDT con / STOPDT con without an act sent (client, F12). New opt-in `IgnoreUnsolicitedU` makes the client ignore them instead of firing onActivated/onDeactivated. |
| `unsol.act_response` | Warning | Client gets ActCon/DeactCon/ActTerm (COT 7/9/10) for a (TypeID, CA, IOA) with no outstanding command (tracker §5.4) or after it already terminated |
| `unsol.interrogated` | Notice | COT 20–36 data with no interrogation outstanding for that CA/group; COT 37–41 with no counter interrogation; COT 5 (requested) with no read outstanding. Spoofed reporting (T0856). |
| `unsol.link_confirm` | Notice | cs101/103 confirm with nothing outstanding, or from a station other than the one polled (`cs101/client.go:556,560`, `cs103/client.go:534,538`, `cs101/server.go:546`) |
| `unsol.file` | Warning | File-transfer ASDU outside any transfer (`filetransfer` `ErrNoTransfer`) |

### 4.9 File transfer (`file.*`)

| Kind | Sev | Trigger |
|------|-----|---------|
| `file.directory_call` | Info | F_SC_NA_1 with SCQ call directory (reconnaissance on a device's records) |
| `file.upload` | Notice | Monitor-direction transfer started/finished (sender selected/called file; receiver completed file), with IOA, NOF, size. **Upload of files.** |
| `file.download_attempt` | Alert | Outstation receives control-direction file offer (F_FR_NA_1 / F_SR_NA_1 / F_SG_NA_1 from the master). The library does not implement it, so this is unexpected and a firmware or config download attempt (T0843). |
| `file.rejected` | Warning | Negative file/section ack sent (unknown file `FileErrUnexpectedNameOfFile`, unknown section) |
| `file.checksum` | Warning | Section checksum mismatch (`receiver.go:292,320`) |
| `file.oversized` | Warning | Requested section beyond file, segment longer than the negotiated max, or announced file length > `MaxFileSize` (receiver protection against memory exhaustion) |
| `file.unsupported` | Notice | `ErrUnsupported` service |

Implementation: `Sender.Handle` / `Receiver.Handle` call
`secevent.Report(c, …)` at each of these points. No new setter is needed (§3.5).

### 4.10 Flooding and resource (`flood.*`, `dos.*`)

Token buckets per session per class, with defaults chosen so that normal SCADA
traffic never trips them (tune in §9):

| Kind | Class / default threshold | Notes |
|------|---------------------------|-------|
| `flood.commands` | process commands > 10/s burst 20 | T0806 |
| `flood.interrogation` | C_IC > 6/min burst 3 | GI is heavy for the outstation (amplification: one request, thousands of objects) |
| `flood.counter_interrogation` | C_CI > 6/min | |
| `flood.read` | C_RD > 50/s | |
| `flood.clock_sync` | C_CS > 6/min | |
| `flood.reset_process` | C_RP > 2/10 min | |
| `flood.testfr` | TESTFR act received > 3 per t₃ **or** > 5/s | the keep-alive storm. A peer normally sends ≤ 1 per its t₃. |
| `flood.u_toggle` | STARTDT/STOPDT > 5/10 s | session flapping |
| `flood.s_frames` | S-frames without a new N(R) > 50/s | redundant-ack storm |
| `flood.frames` | total inbound frames > `MaxFrameRate` (default 2000/s) or octets > `MaxByteRate` | generic |
| `flood.cs101_requests` | link requests per s on TCP-encapsulated 101 | |
| `dos.rx_backlog` | `rcvASDU`/`rcvRaw` channel ≥ 90 % full (`server_session.go:337`): the handler cannot keep up and the peer outpaces it. Hysteresis: reported once until it falls below 50 %. | |
| `dos.tx_backlog` | `ErrBufferFulled` returned more than N times in a window, *and* the peer's ack latency is high: a slow-reader attack | |
| `dos.window_stall` | k window full and the peer withholds acks until just before t₁, repeatedly | |
| `dos.write_stall` | `conn.Write` deadline hit (F5) | Action `Disconnected` |
| `dos.slow_frame` | partial frame not completed within `FrameAssemblyTimeout` (default t₁) (F4) | slowloris; Action `Disconnected` |

Action per class, from `Policy.FloodAction`:

- `Report` (default).
- `Throttle`. Excess requests are dropped. For commands the library replies with
  a negative ActCon, so the master is not left hanging, and the handler is not
  called.
- `Disconnect`.

Never throttle S-frames, TESTFR con or STOPDT. Dropping them would break the
protocol. They are report-only.

---

## 5. Key mechanisms in detail

### 5.1 MAC lookup (`netpeer`)

- `Resolver` interface: `Lookup(ctx, netip.Addr) (Neighbor, error)`, where
  `Neighbor{MAC, Interface, State, Source}`. The default is chosen per GOOS
  through build tags. `netpeer.SetDefaultResolver` and the per-endpoint
  `Policy.Resolver` allow injection (tests, or a MAC from a switch via SNMP).
- **On-link decision first.** Enumerate `net.Interfaces()` and their prefixes
  (cached, refreshed every 60 s or on miss). If the peer is not on-link, the
  neighbour table holds the *gateway's* MAC. Report it as `MACSource=Gateway`
  (with the gateway IP when obtainable) or `None` (configurable). That keeps a
  router change from looking like a spoofed peer.
- Loopback peers: `MACSource=None`, `OnLink=true`, `Interface="lo"`. This is
  what all current tests use.
- **Linux:** netlink `RTM_GETNEIGH` dump via `golang.org/x/sys/unix` (already
  an indirect dependency). Parse `NDA_DST`/`NDA_LLADDR`, accept states
  REACHABLE/STALE/DELAY/PROBE/PERMANENT/NOARP, skip INCOMPLETE/FAILED. Works
  for IPv4 and IPv6, no root needed. Fallback: `/proc/net/arp` for IPv4 when
  netlink is unavailable (e.g. seccomp).
- **Windows:** `iphlpapi!GetIpNetTable2` / `ResolveIpNetEntry2`
  (`ResolveIpNetEntry2` only on cache miss, because it sends an ARP probe) via
  `windows.NewLazySystemDLL`. IPv4 and IPv6.
- **Darwin/FreeBSD:** `sysctl(NET_RT_FLAGS, RTF_LLINFO)` parsed with
  `golang.org/x/net/route` (new dependency, Go team module) or a hand parser
  (see Q4).
- **Others:** `ErrUnsupported`, and `MACSource=None`.
- **Timing:** after a completed TCP handshake from an on-link peer, the kernel
  has resolved the neighbour (it had to send the SYN-ACK), so the entry exists.
  Lookup runs synchronously in the per-connection goroutine (not the accept
  loop), bounded by `MACLookupTimeout` (default 200 ms). A failure never
  blocks or rejects a connection unless pinning is configured.
- For the **client** side, look up after dial. The result is the server's MAC
  if on-link, else the gateway's.

### 5.2 MAC/IP history cache (`netpeer.Cache`)

- Process-wide default instance, injectable per endpoint. Maps
  IP→{MAC, firstSeen, lastSeen, certFingerprint} and MAC→set of IPs. Bounded
  (default 4096 entries, LRU), with entries expiring after `MACHistoryTTL`.
- Optional persistence: `Cache.Save(io.Writer)` / `Load(io.Reader)` (JSON), so
  a restart does not reset the baseline and miss a swap that happened while the
  process was down. Off by default.
- Produces the `peer.*` events of §4.3. Periodic mid-session re-check is driven
  by the session's existing 100 ms ticker (every `MACRecheckInterval`). No new
  goroutine.

### 5.3 Connection tracker (server level)

`internal/secmon.ConnTracker` sits in the accept path *before* the session is
built and before the TLS handshake, so rejected peers cost almost nothing:

1. Allowlist check (CIDR) → `conn.unexpected`.
2. Per-IP and global accept-rate buckets → `conn.rate`.
3. Concurrent counts (total, per IP) → `conn.rejected_limit`.
4. Then spawn the goroutine: TLS handshake (§4.2) → MAC lookup and pinning
   (§5.1) → session.

Counts are released in the session's defer. `GetSessionsLen` stays as it is.

### 5.4 Outstanding-request tracker (clients)

In `cs104.Client.Send` (`client.go:621`) and the cs101/103 client send paths:
record outgoing control-direction requests keyed by (TypeID, CA, IOA, Cause),
with timestamp and state (sent → con → term). Inbound COT 7/9/10/20–41/5 is
matched against it. Entries expire after `RequestTrackTTL` (default 2·t₁ or
60 s for GI). Bounded size. Feeds `unsol.act_response` and
`unsol.interrogated`. Same tracker records U-frame acts (`SendStartDt`,
`SendStopDt`, TESTFR) for `unsol.u_confirm`.

Server side, a small tracker records TESTFR act sent (`server_session.go:290`),
so TESTFR con can be validated (F9).

### 5.5 Optional SBO tracker (servers)

`secevent` (or `asdu`) helper `SelectTracker` with `Select(sess, typ, ioa,
value)` and `Execute(...) error`, plus a timeout. Designed for handlers like
`_examples/cs104_server_general/commands.go:48-70` (which re-implements this),
and the example is migrated to it. Emits `cmd.execute_without_select` and the
related kinds. Opt-in: the library cannot enforce SBO on its own because
handlers own command semantics.

### 5.6 Time-tag checks

`internal/secmon.CheckCommandAge(tag time.Time, now, maxAge, maxFuture)`.
Called from the server dispatcher for the C_*_TA_1 types *before* the
handler, using `params.InfoObjTimeZone`. Clock-sync deltas use the same helper.
Document that both stations need synchronised clocks for this to work, and that
a successful `ctrl.clock_step` attack defeats it, which is why `ctrl.clock_step`
is Alert.

---

## 6. Policy / configuration surface

### 6.1 Shared struct

```
secevent.Policy {
    // Delivery
    Sink                Sink
    AggregateWindow     time.Duration
    MaxRaw              int
    AuditControl        bool           // §4.6 audit events, default true when a sink is set

    // Connection
    AllowedPeers        []netip.Prefix // nil = any
    RejectUnlisted      bool
    PeerBindings        map[netip.Addr]net.HardwareAddr
    RejectBindingMismatch bool
    MaxSessions, MaxSessionsPerIP, MaxActiveSessions int
    AcceptRate, GlobalAcceptRate Rate   // {N, Per}
    InactiveTimeout     time.Duration  // never-STARTDT sessions
    HandshakeTimeout    time.Duration
    FrameAssemblyTimeout time.Duration
    WriteTimeout        time.Duration

    // Identity
    Resolver            netpeer.Resolver
    MACCache            *netpeer.Cache
    MACLookupTimeout, MACRecheckInterval, MACHistoryTTL time.Duration
    CertExpiryWarning   time.Duration

    // Application
    AddressSpace        AddressSpace
    AutoRejectUnknown   bool
    Functions           FunctionPolicy
    AllowedOriginators  []asdu.OriginAddr
    AllowPrivateTypes   bool
    MaxCommandAge, MaxCommandFuture time.Duration
    RejectStaleCommands bool
    DuplicateWindow     time.Duration
    ClockStepThreshold  time.Duration
    MaxFileSize         int

    // Flooding
    Limits              Limits         // per-class Rate, §4.10
    FloodAction         Action         // Report | Throttle | Disconnect
    ScanThreshold       int; ScanWindow time.Duration

    // cs101/103
    LinkTopology        LinkTopology   // SharedBus | PointToPoint (default PointToPoint on TCP, SharedBus on serial)
    FCBRepeatRate       Rate
}
```

- `secevent.DefaultPolicy()` is report-only, with the thresholds from §4.10.
- `Policy.Valid()` follows the repo's Valid-then-defaults convention
  (`cs104/config.go:84`).

### 6.2 Setters (additive, chainable, matching each package's style)

- `cs104.Server.SetSecurityPolicy(p secevent.Policy) *Server`,
  `Server.SetCyberEventSink(s secevent.Sink) *Server` (shortcut),
  `Server.SecurityStats() secevent.Stats`.
- `cs104.ClientOption.SetSecurityPolicy(p) *ClientOption`,
  `Client.SetCyberEventSink(s) *Client`, `Client.PeerInfo()`.
- `ServerSpecial`: adding methods to the exported interface breaks external
  implementers. It is very unlikely anyone implements it, but to be safe add
  them only to the concrete type and expose `cs104.SecurityOf(ServerSpecial)`
  via a type assertion (Q6).
- `cs101.Server/Client`, `cs103.Client`: the same setters. `cs101.Transporter`
  gets `SetPolicy` internally and returns `PeerInfo` from `Open` (the internal
  signature changes. `Open` is exported, so add `OpenPeer` and leave `Open` as
  a wrapper).

### 6.3 `FunctionPolicy` (hardening)

An allow/deny set of TypeIDs plus a few qualifiers:
`DenyTypes []asdu.TypeID`, `DenyResetProcess`, `DenyClockSync`,
`DenyParameters`, `DenyFileTransfer`, `DenyBroadcastCommands`,
`ReadOnly` (deny every C_* process command and P_*). A denied request gets the
standard reply (UnknownTypeID, or a negative ActCon via `DenyReply`) and
`access.type_blocked`. The handler is never called.

### 6.4 Stats (`secevent.Stats`)

Monotonic counters per Kind, per Category, plus dropped and aggregated counts,
current sessions, rejected connections. Snapshot by value. Exists for pull-based
monitoring (IEC 62351-7 NSM, Prometheus exporters built by the user). The
library does not depend on Prometheus.

---

## 7. Robustness fixes found during analysis (behaviour changes, called out)

1. **Accept loop resilience (F1).** Back off on errors that are not "listener
   closed" (5 ms doubling to 1 s, like `net/http.Server`) and continue. Emit
   `conn.accept_error` (Critical). Only `net.ErrClosed` ends the loop.
   Regression test: a fake listener that fails N times.
2. **Write deadline (F5).** `sendLoop` sets
   `conn.SetWriteDeadline(now + WriteTimeout)` (default t₁) per frame. On
   timeout: `dos.write_stall`, then close. The run loop's
   `sf.sendRaw <- …` sites (`server_session.go:200,204,235`,
   `client.go:319,338,535`) become selects with `ctx.Done()`, so a stalled
   writer cannot freeze the state machine.
3. **Frame-assembly deadline (F4).** Once the first octet of a frame is read,
   set `SetReadDeadline(now + FrameAssemblyTimeout)`, and clear it after the
   frame completes. While idle between frames no deadline applies (t₃/TESTFR
   still governs idleness). A deadline error mid-frame → `dos.slow_frame`, close.
4. **Explicit TLS handshake with deadline (F3).**
5. **cs101 TCP-server busy rejection (F16).**

Each is its own commit with a test, landed *before* the event work so the
events can rely on it.

---

## 8. `asdu` package additions

- `asdu.ValidateRequest(a *ASDU, dir Direction) error`: the semantic checks of
  §4.5 `proto.field_invalid` and §4.4 `access.oversized_request`. It returns
  typed errors (`ErrQualifierReserved`, `ErrDCSNotPermitted`, `ErrFloatNaN`,
  `ErrSingleObjectExpected`, `ErrBroadcastNotAllowed`, …) so the dispatcher can
  map them to Kinds. It is pure, needs no I/O, and is fuzzed.
- `asdu.IsControlDirection(t)`, `IsMonitorDirection(t)`,
  `IsSystemCommand(t)`, `IsProcessCommand(t)`, `IsParameter(t)`, `IsFile(t)`,
  `IsSecurity(t)`, `InProfile104(t)`, `IsBroadcastAllowed(t)`. These are
  tables, not ad-hoc switches.
- `asdu.CommandTimeTag(a) (time.Time, bool)` for C_*_TA_1.
- Existing errors stay, and gain no new wrapping, so `errors.Is` keeps working.

---

## 9. Performance and safety requirements

- No sink → per-frame overhead is one atomic pointer load. Detectors are not
  even updated. Benchmarks: `BenchmarkServerThroughput` with nil and with a
  `SinkFunc` no-op. The target is < 2 % throughput loss with a sink, and 0
  allocations per frame on the happy path (events allocate only when emitted).
- Token buckets are lock-free per session (owned by the run goroutine). Shared
  structures (conn tracker, MAC cache, duplicate-command window) use a single
  mutex each with bounded size.
- Every detector's memory is bounded, and hostile input cannot grow it without
  limit: LRU caps on aggregation keys, scan windows, request trackers and the
  MAC cache.
- No event Detail is built with `fmt.Sprintf` unless the event will actually
  be emitted (lazy).
- Threshold defaults are validated against the existing load tests
  (`_examples/cs104_explorer/flood20k_test.go`, `cs104_server_general` at
  copies=N) so they produce **zero** events under legitimate heavy load.

---

## 10. Tests

| Layer | Tests |
|-------|-------|
| `secevent` | dispatcher non-blocking under a slow sink; drop accounting and `meta.events_dropped`; aggregation window and counts; always-deliver kinds; sink panic recovery; JSON stability (golden file); Kind table completeness (every Kind has Category/Severity) |
| `netpeer` | netlink message parser on canned bytes; `/proc/net/arp` parser on canned text; on-link logic with synthetic interfaces; Windows/Darwin parsers on canned buffers (build-tagged); cache change detection, TTL, LRU, persistence round-trip; fake Resolver |
| `internal/secmon` | token bucket math; sliding distinct counter; tracker expiry; time-tag checks incl. timezone |
| `asdu` | table tests and **fuzz** (`go test -fuzz`) for `ValidateRequest`; profile/direction tables against the type list |
| cs104 socket tests (style of `cs104/stopdt_test.go`) | one test per Kind that can be triggered from the wire: connect without data → `conn.no_data`; allowlist reject; limit reject; accept-rate; never-started timeout; garbage/bad length/bad APCI; I while stopped; each `seq.*` subtype via hand-crafted frames; U wrong direction; unsolicited TESTFR con; outbound-reply observation for 44–47 and negative con; broadcast misuse; stale time tag (reject on/off); duplicate command across two connections; each flood class; slowloris; zero-window write stall (client that never reads); TLS: unknown CA, expired, no client cert, plaintext-on-TLS, HTTP probe, handshake timeout (certs generated in-test with `crypto/x509`) |
| cs104 client | unsolicited STARTDT con, ActCon without command, GI data without GI, M_EI_NA_1, server cert rejected |
| cs101/cs103 | via existing `link_test.go` / `multidrop_test.go` harnesses: foreign address on PointToPoint vs SharedBus, FCB repeat rate, checksum errors, wrong PRM, busy rejection on TCP transport |
| filetransfer | each `file.*` Kind through `Report` with a fake Connect implementing Reporter |
| MAC end-to-end | Linux-only, skipped unless `IECP5_NETNS=1` and root: veth pair in a netns, connect, assert MAC; change the veth MAC, reconnect → `peer.mac_changed` |
| Regression | full `go test -race ./...`; `IEC104_INTEROP=1` suite (§ interop/README) must stay green with **policy unset and with DefaultPolicy+sink**: proves no wire-behaviour change |
| Fuzz | existing parsers (`cs104.parse`, `asdu.UnmarshalBinary`, `cs101.ParseFrame`) fuzzed with a sink attached: every malformed input yields an event and never a panic or blocked goroutine |

---

## 11. Phased delivery (each phase = one PR, green CI)

| Phase | Content | Depends |
|-------|---------|---------|
| **P0** | Agree on open questions (§12); freeze Kind names (`kinds.go` + doc table) | — |
| **P1** | §7 robustness fixes (accept loop, write deadline, frame deadline, TLS explicit handshake, cs101 busy). Behaviour change, called out. | — |
| **P2** | `secevent` core: Event, Kinds table, Dispatcher, sinks, Reporter/Report, Stats, Policy skeleton; emitter embedded in cs104 Server/Session/Client; `conn.accepted/closed/no_data/t1_timeout/accept_error` | P1 |
| **P3** | `netpeer` Linux + cache; `PeerInfo` accessors; `peer.*` events; pinning; mid-session re-check | P2 |
| **P4** | cs104 connection policy: allowlist, limits, rates, never-started; TLS classification and `tls.*`; `WrapTLSConfig` | P2 |
| **P5** | cs104 APCI: `proto.garbage/bad_length/bad_apci/i_while_stopped/u_wrong_direction`, `seq.*` classification, `unsol.u_confirm` | P2 |
| **P6** | `asdu` validation helpers; cs104 ASDU layer: `proto.asdu_invalid/field_invalid/wrong_direction/type_not_in_profile/secure_auth_type`, outbound-reply observation, `access.*`, `ctrl.*`, `cmd.*` incl. time-tag and duplicate checks, AddressSpace, FunctionPolicy | P5 |
| **P7** | Flood detectors and actions, `dos.*` backlog events | P5 |
| **P8** | cs104 client: request tracker, `unsol.*`, `ctrl.end_of_init`, `remote.rejected` | P6 |
| **P9** | cs101 + cs103 (link topology, `link.*`, `proto.cs101_*`, FCB, TCP transport events, audit) | P2–P7 |
| **P10** | `filetransfer` events | P2 |
| **P11** | `netpeer` Windows and Darwin/BSD backends | P3 |
| **P12** | Docs: new `docs/security.md` (event catalogue = §4 tables, policy guide, MAC caveats, deployment guidance: IEC 62351-3 TLS, firewalling, SYN cookies, what the library cannot see); updates to `docs/cs104.md`, `cs101.md`, `cs103.md`, `filetransfer.md`, `SKILL.md`, `README.md`. Examples: `cs104_server_general` with JSON sink + policy flags; `cs104_explorer` gets an "events" pane (optional). | all |

Rough size: P2–P7 are the bulk (~3–4 k lines including tests). Each later phase
is ≤ 1 k.

---

## 12. Open questions (decisions needed before P2)

1. **Package names:** `secevent` + `netpeer` + `internal/secmon`, or one
   public `cyber` package? Answer: Split packages.
2. **Defaults when a sink is set:** report-only everywhere (proposed), or
   enable the safe enforcements by default (`AutoRejectUnknown`, stale-command
   rejection)? Answer: report only.
3. **`conn.accepted` for every connection:** Info (proposed) or Notice? Some
   SIEMs alert on Notice. Answer: info.
4. **Darwin/BSD MAC backend:** take `golang.org/x/net/route` as a new
   dependency, or hand-parse the sysctl buffer to keep `go.mod` minimal? Answer: new dep.
5. **Output formats:** are JSON lines + RFC 5424/CEF formatters enough, or is a
   built-in syslog network client wanted? Answer: JSON.
6. **`ServerSpecial` interface:** add methods (technically breaking) or use the
   type-assertion helper (proposed)? Answer: proposed.
7. **Profile checks (F20):** flag `C_TS_NA_1` / `C_CD_NA_1` / CP24 types on
   104 as Notice (proposed), or reject them? Answer: proposed.
8. **Event sink delivery:** one global dispatcher per process by default, or one
   per endpoint (proposed: per endpoint unless the user shares one)? Answer: proposed.

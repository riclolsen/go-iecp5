# `filetransfer` — IEC 60870-5-101/104 file transfer

The `filetransfer` package implements the file transfer procedures of
IEC 60870-5-101 subclause 7.4.11 (used unchanged by IEC 60870-5-104) in the
**monitor direction** — a file travelling from the controlled station
(outstation) to the controlling station (master). That is the direction used
in practice, for example to fetch disturbance records.

Two components drive a transfer. Both act on an `asdu.Connect` and consume
the ASDUs handed to them by your ASDU handler, so they are transport
agnostic:

| Type | Role | Station |
|------|------|---------|
| `Sender` | offers files, answers directory calls, serves sections/segments | controlled (outstation) |
| `Receiver` | calls the directory, requests files, verifies and assembles them | controlling (master) |

The ASDU codecs themselves live in the `asdu` package
(`asdu.FileReady`, `asdu.FileSegment`, `asdu.GetFileDirectory`, …) and can
be used directly if you want to drive the procedure yourself.

## Wiring it into cs104

File transfer ASDUs are not special-cased by the cs104 dispatcher, so they
arrive at the generic `ASDUHandler`. Delegate them:

```go
sender := filetransfer.NewSender(store) // outstation side

func (h *myHandler) ASDUHandler(c asdu.Connect, a *asdu.ASDU) error {
	if handled, err := sender.Handle(c, a); handled {
		return nil // consumed by the transfer service
		_ = err   // inspect/log if you want to see protocol faults
	}
	switch a.Type { // your own types
	// ...
	}
	return nil
}
```

`Handle` returns `handled == false` for every ASDU that is not part of the
file transfer set, so your own dispatch continues untouched.

On the master side the client handler takes the extra `*cs104.Server, int`
parameters but is otherwise identical:

```go
receiver := filetransfer.NewReceiver(store)

func (h *myHandler) ASDUHandler(c asdu.Connect, a *asdu.ASDU, _ *cs104.Server, _ int) error {
	if handled, _ := receiver.Handle(c, a); handled {
		return nil
	}
	// ... your data handling
	return nil
}
```

## Fetching a file (master)

```go
receiver := filetransfer.NewReceiver(store)
receiver.SetFileHandler(func(e filetransfer.Entry, data []byte) {
	log.Printf("file %d/%d complete: %d octets", e.Ioa, e.Nof, len(data))
})
receiver.SetDirectoryHandler(func(ca asdu.CommonAddr, d []asdu.DirectoryInfo) {
	for _, f := range d {
		log.Printf("station %d has file ioa=%d nof=%d size=%d", ca, f.Ioa, f.Nof, f.LengthOfFile)
	}
})

_ = receiver.RequestDirectory(client, 1)                              // list files
_ = receiver.RequestFile(client, 1, 100, asdu.FileDisturbanceData)    // fetch one
```

After `RequestFile` the transfer runs on its own: the receiver requests each
section, accumulates its segments, verifies the section checksum,
acknowledges it, and finally acknowledges the file and delivers it to the
file handler (and to the `Store`, when one was given).

When the outstation announces a file on its own (`Sender.Offer` →
`F_FR_NA_1`), the receiver selects it automatically. Turn that off with
`SetAutoAccept(false)` to decide per file.

## Serving files (outstation)

```go
store := filetransfer.NewMemStore()
_ = store.Write(100, asdu.FileDisturbanceData, record) // whatever you captured

sender := filetransfer.NewSender(store).SetSectionSize(4096)

// optionally tell the master a new record is waiting:
_ = sender.Offer(conn, 1, 100, asdu.FileDisturbanceData)
```

The sender answers directory calls from the store's `List`, splits a
selected file into sections of at most `SetSectionSize` octets (default
`DefaultSectionSize` = 4096), and each section into segments that fit one
ASDU (`asdu.Params.MaxSegmentSize()`, 236 octets with the 104 standard
parameters). A section rejected by the master — a checksum mismatch — is
served again.

## The Store

```go
type Store interface {
	List() ([]Entry, error)
	Read(ioa asdu.InfoObjAddr, nof asdu.NameOfFile) ([]byte, error)
	Write(ioa asdu.InfoObjAddr, nof asdu.NameOfFile, data []byte) error
	Delete(ioa asdu.InfoObjAddr, nof asdu.NameOfFile) error
}
```

Files are addressed by the pair (information object address, name of file).
`NewMemStore()` is an in-memory implementation; implement the interface
yourself to back transfers with disk, a database, or a live record
generator. Implementations must be safe for concurrent use.
`Read` returns `ErrFileNotFound` for an unknown file, which the sender turns
into a negative acknowledge carrying `asdu.FileErrUnexpectedNameOfFile`.

A `Receiver` may be constructed with a `nil` store when the completion
callback is all you need.

## The procedure on the wire

```
outstation                              master
     |  F_FR_NA_1  file ready  ------------>|   (optional, Sender.Offer)
     |<---------- F_SC_NA_1  select file    |   SCQ = 1
     |  F_SR_NA_1  section ready ---------->|
     |<---------- F_SC_NA_1  request section|   SCQ = 6
     |  F_SG_NA_1  segment ...  ----------->|   (repeated)
     |  F_LS_NA_1  last segment + CHS ----->|   LSQ = 3
     |<---------- F_AF_NA_1  ack section    |   AFQ = 3 (or 4 on checksum failure)
     |            ... next section ...      |
     |  F_LS_NA_1  last section ----------->|   LSQ = 1
     |<---------- F_AF_NA_1  ack file       |   AFQ = 1
```

The directory call is a single exchange: `F_SC_NA_1` with SCQ = 0 answered
by `F_DR_TA_1`. All transfer ASDUs use cause of transmission 13
(`asdu.FileTransfer`); the directory uses 5 (`Request`) or 3 (`Spontaneous`).

Each section carries a checksum (`CHS`, the arithmetic sum of its segment
octets modulo 256) which the receiver verifies before acknowledging — this
is the integrity check of the procedure and it is enforced.

## Throughput

A transfer is round-trip heavy: roughly four master↔outstation turnarounds
per section, plus the segments themselves. Over cs104 each turnaround
currently costs up to one 100 ms scheduler tick (see the note in
[cs104.md](cs104.md#throughput-of-requestresponse-patterns)), so large files
are latency bound rather than bandwidth bound. Larger sections mean fewer
turnarounds, at the cost of retransmitting more data when a checksum fails.

## Limits and non-goals

- **Monitor direction only.** Files travelling master → outstation
  (the control direction of the same ASDUs) are not implemented.
- **One transfer at a time** per `Sender`/`Receiver`, which is what the
  standard's procedure provides for. `RequestFile` returns `ErrBusy` while
  another transfer is running.
- `F_SC_NB_1` <127> (QueryLog / request archive file, an edition 2
  extension) is **not** implemented.
- The announced length of file (LOF) is carried through to the directory and
  the file-ready ASDU but is not enforced against the assembled length; the
  per-section checksum is the integrity check.
- The IEC 60870-5-103 disturbance data transfer (ASDU 23–31) is a different
  service and is not implemented.

## Verification status

The transfer is covered by an in-process loopback (every ASDU round-tripped
through the wire codecs, including multi-section files, empty files, a
corrupted segment forcing a section retry, and directory calls) and by an
end-to-end transfer over a real TCP cs104 connection. The ASDU layouts are
pinned by byte-level tests in the `asdu` package.

It has **not** been verified against a third-party implementation or a real
device. Before relying on it in the field, test against your counterpart
equipment or a reference stack such as lib60870.

## Working examples

- [`_examples/cs104_server_general`](../_examples/cs104_server_general) — an
  outstation that seeds two sample files, serves them, and announces the
  disturbance record shortly after a master connects.
- [`_examples/cs104_explorer`](../_examples/cs104_explorer) — the terminal
  master: its **Files** tab calls the directory (`d`), fetches the selected
  file (`enter`), auto-accepts announced files, and writes completed
  transfers to `./iec104-files/`.

Run them against each other: `go run .` in the server directory, then
`go run . 127.0.0.1:2404` in the explorer directory.

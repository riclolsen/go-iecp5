# go-iecp5

IEC 60870-5-104 (TCP/IP) and IEC 60870-5-101 (serial FT1.2) protocol library in pure Go.

Forked from [thinkgos/go-iecp5](https://github.com/thinkgos/go-iecp5) (original project archived and unmaintained).

```
go get github.com/riclolsen/go-iecp5
```

Requires Go 1.25+. Serial support uses [go.bug.st/serial](https://github.com/bugst/go-serial).

## Packages

| Package | Purpose |
|---------|---------|
| [`asdu`](docs/asdu.md) | Application layer for 101/104: ASDU encode/decode for the standard type identifications, cause of transmission, quality descriptors, time tags |
| [`cs104`](docs/cs104.md) | IEC 60870-5-104 client (master) and server (slave) over TCP/IP, with optional TLS |
| [`cs101`](docs/cs101.md) | IEC 60870-5-101 primary station (master) and secondary station (slave) over serial, unbalanced (multi-drop) and balanced modes |
| [`cs103`](docs/cs103.md) | IEC 60870-5-103 primary station (master) for protection equipment over serial, with its own 103 application layer (FUN/INF addressing, measurands, CP32 time) |
| `clog` | Pluggable logging used by the transport packages |

Full documentation:

- [Application layer (`asdu`) reference](docs/asdu.md)
- [IEC 60870-5-104 guide (`cs104`)](docs/cs104.md)
- [IEC 60870-5-101 guide (`cs101`)](docs/cs101.md)
- [IEC 60870-5-103 guide (`cs103`)](docs/cs103.md)
- [SKILL.md](SKILL.md) — condensed build guide for AI coding agents

## Quick start: IEC 104 server (slave / controlled station)

```go
package main

import (
	"log"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/cs104"
)

type handler struct{}

// General interrogation: confirm, send the current process image, terminate.
func (handler) InterrogationHandler(c asdu.Connect, pack *asdu.ASDU, qoi asdu.QualifierOfInterrogation) error {
	if qoi != asdu.QOIStation {
		pack.Coa.IsNegative = true
		return pack.SendReplyMirror(c, asdu.ActivationCon)
	}
	_ = pack.SendReplyMirror(c, asdu.ActivationCon)
	_ = asdu.Single(c, false,
		asdu.CauseOfTransmission{Cause: asdu.InterrogatedByStation}, pack.CommonAddr,
		asdu.SinglePointInfo{Ioa: 100, Value: true, Qds: asdu.QDSGood})
	_ = asdu.MeasuredValueFloat(c, false,
		asdu.CauseOfTransmission{Cause: asdu.InterrogatedByStation}, pack.CommonAddr,
		asdu.MeasuredValueFloatInfo{Ioa: 400, Value: 22.5, Qds: asdu.QDSGood})
	return pack.SendReplyMirror(c, asdu.ActivationTerm)
}

func (handler) CounterInterrogationHandler(asdu.Connect, *asdu.ASDU, asdu.QualifierCountCall) error { return nil }
func (handler) ReadHandler(asdu.Connect, *asdu.ASDU, asdu.InfoObjAddr) error                       { return nil }
func (handler) ClockSyncHandler(asdu.Connect, *asdu.ASDU, time.Time) error                          { return nil }
func (handler) ResetProcessHandler(asdu.Connect, *asdu.ASDU, asdu.QualifierOfResetProcessCmd) error { return nil }
func (handler) DelayAcquisitionHandler(asdu.Connect, *asdu.ASDU, uint16) error                      { return nil }
func (handler) ASDUHandlerAll(asdu.Connect, *asdu.ASDU, int) error                                  { return nil }

// Commands (C_SC/C_DC/C_SE/...) arrive here; parse, act, and confirm.
func (handler) ASDUHandler(c asdu.Connect, pack *asdu.ASDU) error {
	switch pack.Type {
	case asdu.C_SC_NA_1:
		cmd := pack.GetSingleCmd()
		log.Printf("single command IOA=%d value=%v", cmd.Ioa, cmd.Value)
		return pack.SendReplyMirror(c, asdu.ActivationCon)
	}
	return nil
}

func main() {
	srv := cs104.NewServer(&handler{})
	srv.LogMode(true)
	if err := srv.ListenAndServer(":2404"); err != nil {
		log.Fatal(err)
	}
}
```

Push spontaneous data to all connected masters at any time:

```go
_ = asdu.Single(srv, false,
	asdu.CauseOfTransmission{Cause: asdu.Spontaneous}, 1,
	asdu.SinglePointInfo{Ioa: 100, Value: false, Time: time.Now()})
```

## Quick start: IEC 104 client (master / controlling station)

```go
option := cs104.NewOption()
_ = option.AddRemoteServer("127.0.0.1:2404")

client := cs104.NewClient(&clientHandler{}, option) // implements cs104.ClientHandlerInterface
client.LogMode(true)
client.SetOnConnectHandler(func(c *cs104.Client) { c.SendStartDt() }) // activate data transfer
client.SetOnActivatedHandler(func(c *cs104.Client) {
	_ = c.InterrogationCmd(asdu.CauseOfTransmission{Cause: asdu.Activation}, 1, asdu.QOIStation)
})
_ = client.Start()
```

Monitor-direction data (M_SP, M_ME, ...) is delivered to the handler's
`ASDUHandler`; decode it with the typed getters (`GetSinglePoint`,
`GetMeasuredValueFloat`, ...). See the [cs104 guide](docs/cs104.md).

## Quick start: IEC 101

See the [cs101 guide](docs/cs101.md) for serial configuration, unbalanced
polling (including multi-drop with several secondary stations on one line)
and balanced point-to-point mode.

## Examples

Runnable programs live under [`_examples`](_examples) (each buildable with
`go run .` from its directory):

- [`cs104_explorer`](_examples/cs104_explorer) — an interactive terminal IEC 104
  master (Bubble Tea TUI): connect to servers, issue interrogation/clock/test/
  reset requests, send control commands, and watch received points and a live
  protocol log. It is a self-contained module so its UI dependencies stay out
  of the library.
- `cs104_server_general`, `cs104_client_general`, `cs104_server_special` — minimal
  104 server, client, and reverse-connection server.
- `cs101_client_general` — minimal 101 serial master.

## Implemented

- All process information types in monitor direction, with and without
  CP24/CP56 time tags (single/double point, step, bitstring, normalized /
  scaled / float measured values, integrated totals, protection events,
  packed single points with SCD).
- Process information in control direction (single/double/step commands,
  setpoints, bitstring commands) including the CP56Time2a variants.
- System information: end of init, (counter) interrogation, read, clock
  sync, test, reset process, delay acquisition; parameter commands.
- cs104: APCI state machine with I/S/U frames, k/w flow control, t₀–t₃
  timeouts, StartDT/StopDT/TestFR, TLS, auto-reconnect, reverse-connection
  server (`NewServerSpecial`).
- cs101: FT1.2 framing with checksum validation, unbalanced mode with
  correct FCB tracking, class 1/2 data buffering, ACD/DFC signalling and
  multi-drop polling; balanced mode with both stations transmitting
  spontaneously (DIR bit).
- cs103 (master): automatic link initialization (status/reset CU), device
  identification collection, automatic time sync + general interrogation,
  cyclic measurand polling (class 2) with event fetch on ACD (class 1),
  general commands with RII-matched acknowledgements, multi-drop.
- TCP-encapsulated 101/103: the FT1.2 frames of both serial protocols can
  be carried over a TCP stream (terminal server / serial-device server)
  instead of a local port — `Config.Transport` selects serial (default),
  TCP dial-out or TCP listen, with optional TLS.

## Not implemented

- File transfer ASDUs (`F_FR_NA_1` … `F_DR_TA_1`) — type identifiers are
  defined but there is no file transfer service.
- IEC 62351-5 security/authentication ASDUs (`S_*`) — enumerated only.
- Select-before-execute command supervision is left to the application:
  command ASDUs are delivered to the generic `ASDUHandler`, and the
  application decides how to confirm/execute them (the S/E bit is available
  via `QualifierOfCommand.InSelect`).
- cs103: generic services (structured GIN/GDD/GID codecs), disturbance data
  transfer (ASDU 23–31) and the secondary (device) side.

## License

GPL v3, see [LICENSE](LICENSE).

---
name: go-iecp5
description: Build IEC 60870-5-104 (TCP) and IEC 60870-5-101 (serial) SCADA/telecontrol masters and outstations in Go with the go-iecp5 library. Use when implementing an IEC 104 client/server, an IEC 101 primary/secondary station, a protocol gateway, an RTU simulator, or when parsing/sending ASDUs (single points, measured values, commands, interrogation).
---

# Building IEC 60870-5-104/101 apps with go-iecp5

Module: `github.com/riclolsen/go-iecp5` (Go ≥ 1.25, GPL v3).
Packages: `asdu` (application layer), `cs104` (TCP), `cs101` (serial), `clog` (logging).
Detailed docs: `docs/asdu.md`, `docs/cs104.md`, `docs/cs101.md`.

## Pick the right endpoint

| You are building | Use |
|---|---|
| Master polling devices over TCP | `cs104.Client` |
| Outstation/RTU serving masters over TCP | `cs104.Server` (listens) |
| Outstation that dials out to the master (NAT) | `cs104.NewServerSpecial` |
| Master on a serial line (RS-232/485) | `cs101.Client` |
| Outstation on a serial line | `cs101.Server` |

Vocabulary: master = controlling station = client; outstation = RTU = slave =
controlled station = server. Monitor direction = data flowing to the master
(`M_*` types). Control direction = commands to the outstation (`C_*` types).

## Core concepts (5 minutes)

- **ASDU**: one application message. `Identifier` = TypeID + variable
  structure (object count) + cause of transmission (COT) + common address
  (CA, the station). Each information object has an information object
  address (IOA, the point number).
- **`asdu.Params`** sets the byte widths of COT/CA/IOA. It must be identical
  on both peers. Use `asdu.ParamsWide` for 104 (its standard; cs104
  default), `asdu.ParamsStandard101` for 101 (cs101 default). Only override
  when the device profile says so.
- **`asdu.Connect`** is the interface every endpoint implements
  (`Params()`, `Send(*ASDU)`, `UnderlyingConn()`). All `asdu.*` send
  helpers take it as the first argument, so the same data-sending code works
  on cs104 and cs101 endpoints.
- **COT matters.** Helpers validate it and return `asdu.ErrCmdCause` when
  wrong. Rules of thumb: commands are sent with `Activation` (6); the
  outstation confirms with `ActivationCon` (7) and finishes with
  `ActivationTerm` (10); spontaneous data uses `Spontaneous` (3);
  interrogation responses use `InterrogatedByStation` (20); cyclic data uses
  `Periodic` (1).
- **Sends are queued, not blocking.** `ErrBufferFulled`/`ErrSendQueueFull`
  mean back off and retry.

## Recipe: IEC 104 outstation (server)

```go
package main

import (
	"log"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/cs104"
)

type h struct{}

func (h) InterrogationHandler(c asdu.Connect, pack *asdu.ASDU, qoi asdu.QualifierOfInterrogation) error {
	if qoi != asdu.QOIStation { // only station interrogation supported here
		pack.Coa.IsNegative = true
		return pack.SendReplyMirror(c, asdu.ActivationCon)
	}
	_ = pack.SendReplyMirror(c, asdu.ActivationCon)
	coa := asdu.CauseOfTransmission{Cause: asdu.InterrogatedByStation}
	_ = asdu.Single(c, false, coa, pack.CommonAddr,
		asdu.SinglePointInfo{Ioa: 100, Value: true, Qds: asdu.QDSGood})
	_ = asdu.MeasuredValueFloat(c, false, coa, pack.CommonAddr,
		asdu.MeasuredValueFloatInfo{Ioa: 400, Value: 22.5, Qds: asdu.QDSGood})
	return pack.SendReplyMirror(c, asdu.ActivationTerm)
}

// Control commands arrive at ASDUHandler — parse, act, confirm.
func (h) ASDUHandler(c asdu.Connect, pack *asdu.ASDU) error {
	switch pack.Type {
	case asdu.C_SC_NA_1, asdu.C_SC_TA_1: // single command
		cmd := pack.GetSingleCmd()
		log.Printf("command IOA=%d value=%v select=%v", cmd.Ioa, cmd.Value, cmd.Qoc.InSelect)
		_ = pack.SendReplyMirror(c, asdu.ActivationCon)
		return pack.SendReplyMirror(c, asdu.ActivationTerm)
	case asdu.C_SE_NC_1: // float setpoint
		sp := pack.GetSetpointFloatCmd()
		_ = sp
		return pack.SendReplyMirror(c, asdu.ActivationCon)
	}
	return nil
}

func (h) CounterInterrogationHandler(asdu.Connect, *asdu.ASDU, asdu.QualifierCountCall) error  { return nil }
func (h) ReadHandler(asdu.Connect, *asdu.ASDU, asdu.InfoObjAddr) error                        { return nil }
func (h) ClockSyncHandler(asdu.Connect, *asdu.ASDU, time.Time) error                           { return nil }
func (h) ResetProcessHandler(asdu.Connect, *asdu.ASDU, asdu.QualifierOfResetProcessCmd) error  { return nil }
func (h) DelayAcquisitionHandler(asdu.Connect, *asdu.ASDU, uint16) error                       { return nil }
func (h) ASDUHandlerAll(asdu.Connect, *asdu.ASDU, int) error                                   { return nil }

func main() {
	srv := cs104.NewServer(h{})
	srv.LogMode(true)

	go func() { // spontaneous event push: srv.Send broadcasts to all masters
		for range time.Tick(10 * time.Second) {
			_ = asdu.Single(srv, false,
				asdu.CauseOfTransmission{Cause: asdu.Spontaneous}, 1,
				asdu.SinglePointInfo{Ioa: 100, Value: true, Time: time.Now()})
		}
	}()

	if err := srv.ListenAndServer(":2404"); err != nil { // blocks
		log.Fatal(err)
	}
}
```

## Recipe: IEC 104 master (client)

```go
package main

import (
	"log"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/cs104"
)

type h struct{}

// Process data lands here (interrogation responses AND spontaneous data).
func (h) ASDUHandler(c asdu.Connect, pack *asdu.ASDU, _ *cs104.Server, _ int) error {
	switch pack.Type {
	case asdu.M_SP_NA_1, asdu.M_SP_TA_1, asdu.M_SP_TB_1:
		for _, p := range pack.GetSinglePoint() {
			log.Printf("SP ioa=%d v=%v q=%v t=%v", p.Ioa, p.Value, p.Qds, p.Time)
		}
	case asdu.M_ME_NC_1, asdu.M_ME_TC_1, asdu.M_ME_TF_1:
		for _, m := range pack.GetMeasuredValueFloat() {
			log.Printf("ME ioa=%d v=%g q=%v", m.Ioa, m.Value, m.Qds)
		}
	}
	return nil
}

// These receive the mirrored command confirmations (ActCon/ActTerm), not data.
func (h) InterrogationHandler(_ asdu.Connect, pack *asdu.ASDU) error {
	log.Printf("interrogation %s", pack.Coa)
	return nil
}
func (h) CounterInterrogationHandler(asdu.Connect, *asdu.ASDU) error       { return nil }
func (h) ReadHandler(asdu.Connect, *asdu.ASDU) error                       { return nil }
func (h) TestCommandHandler(asdu.Connect, *asdu.ASDU) error                { return nil }
func (h) ClockSyncHandler(asdu.Connect, *asdu.ASDU) error                  { return nil }
func (h) ResetProcessHandler(asdu.Connect, *asdu.ASDU) error               { return nil }
func (h) DelayAcquisitionHandler(asdu.Connect, *asdu.ASDU) error           { return nil }
func (h) ASDUHandlerAll(asdu.Connect, *asdu.ASDU, *cs104.Server, int) error { return nil }

func main() {
	opt := cs104.NewOption()
	if err := opt.AddRemoteServer("127.0.0.1:2404"); err != nil {
		log.Fatal(err)
	}

	cli := cs104.NewClient(h{}, opt)
	cli.LogMode(true)
	cli.SetOnConnectHandler(func(c *cs104.Client) { c.SendStartDt() }) // REQUIRED
	cli.SetOnActivatedHandler(func(c *cs104.Client) { // StartDT confirmed → interrogate
		_ = c.InterrogationCmd(asdu.CauseOfTransmission{Cause: asdu.Activation}, 1, asdu.QOIStation)
	})
	if err := cli.Start(); err != nil {
		log.Fatal(err)
	}

	// send a control command later (client must be active):
	time.Sleep(3 * time.Second)
	_ = asdu.SingleCmd(cli, asdu.C_SC_NA_1,
		asdu.CauseOfTransmission{Cause: asdu.Activation}, 1,
		asdu.SingleCommandInfo{Ioa: 6000, Value: true})

	select {}
}
```

## Recipe: IEC 101 (serial)

Master and outstation mirror the 104 recipes with these differences:

```go
// shared serial + link config (both ends must match LinkAddrSize/Mode/params)
cfg := cs101.DefaultConfig()
cfg.Serial = cs101.SerialConfig{Address: "COM3", BaudRate: 9600, DataBits: 8,
	StopBits: serial.OneStopBit, Parity: serial.EvenParity} // 8E1 standard
cfg.LinkAddress = 1

// master
opt := cs101.NewOption()
_ = opt.SetConfig(cfg)
// multi-drop: opt.AddSecondaryAddress(1); opt.AddSecondaryAddress(2); ...
cli := cs101.NewClient(handler, opt) // returns nil if serial config missing!
_ = cli.Start()                      // link init + class polling is automatic

// outstation
srv := cs101.NewServer(handler)
srv.SetConfig(cfg)
_ = srv.Start()
```

- cs101 handler interfaces have **different signatures** than cs104
  (no `asdu.Connect` parameter — capture the client/server in your handler
  struct to reply). See `docs/cs101.md` for the exact interfaces.
- cs101 client routes by COT: interrogation-caused data →
  `InterrogationHandler`; everything else → `ASDUHandler(pack, int)`.
- cs101 server handlers receive the full ASDU plus the decoded
  qualifier/time as dedicated arguments. Reply via the captured
  `*cs101.Server` (e.g. `pack.SendReplyMirror(srv, asdu.ActivationCon)`).
- On the outstation, `Send` buffers into class 1/2 queues; a standard master
  collects them by polling (class 2 = `Periodic`/`Background` causes,
  class 1 = everything else, announced via the ACD bit). No unsolicited
  transmission in unbalanced mode.
- Balanced point-to-point: `cfg.Mode = cs101.ModeBalanced` on both ends,
  same `LinkAddress`; then both sides transmit spontaneously.
- Multi-drop targeting: `cli.SendTo(pack, linkAddr)`; `cli.Send` targets the
  first configured secondary.

## Cause-of-transmission cheat sheet

| Situation | COT to use |
|---|---|
| Master sends any command | `asdu.Activation` |
| Outstation confirms a command | `SendReplyMirror(c, asdu.ActivationCon)` |
| Outstation finishes a command/interrogation | `SendReplyMirror(c, asdu.ActivationTerm)` |
| Outstation rejects | set `pack.Coa.IsNegative = true` then mirror `ActivationCon` |
| Interrogation response data | `asdu.InterrogatedByStation` (or `InterrogatedByGroupN`) |
| Event/change data | `asdu.Spontaneous` |
| Cyclic measurements | `asdu.Periodic` (untagged types only) |
| Counter responses | `asdu.RequestByGeneralCounter` … `RequestByGroup4Counter` |
| Protocol errors | mirror `asdu.UnknownTypeID` / `UnknownCOT` / `UnknownCA` / `UnknownIOA` |

## Choosing a type ID

- Boolean status → `M_SP_*`; three-state switchgear → `M_DP_*`.
- Analog: float `M_ME_NC_1` (easiest), scaled int16 `M_ME_NB_1`,
  normalized −1..1 `M_ME_NA_1` (`asdu.Normalize`, `Float64()` to convert).
- Counters → `M_IT_*`. 32 status bits → `M_BO_*`.
- Suffix picks the time tag: `_NA`=none, `_TA/_TB`(1–16)=CP24,
  `_TB/_TD/_TE/_TF`(30+)=CP56. Prefer CP56 (`...CP56Time2a` helpers) for
  events; untagged for interrogation/cyclic data.
- Commands: `C_SC`(bool), `C_DC`(double: `DCOOn`/`DCOOff`),
  `C_RC`(step up/down), `C_SE_NA/NB/NC`(setpoints), `C_BO`(bitstring).

## Pitfalls (read before debugging)

1. **cs104 client: call `SendStartDt()`** in `SetOnConnectHandler`, or the
   link stays in STOPDT and nothing flows; `Send` returns `ErrNotActive`.
2. The `Get*` getters, `String()` and `json.Marshal` are non-destructive and
   can be called in any order; only the low-level `Decode*` methods consume
   the ASDU buffer — avoid them unless you are hand-parsing.
3. **`asdu.Params` mismatch** = garbage decoding (wrong types/COTs). Both
   ends identical. 104 ⇒ `ParamsWide`. 101 ⇒ `ParamsStandard101` unless the
   device profile differs.
4. `asdu.ErrCmdCause` means your COT is not allowed for that type — see the
   cheat sheet.
5. On the cs104 **client**, interrogation *data* arrives in `ASDUHandler`,
   not `InterrogationHandler` (which gets the C_IC confirmations).
6. Broadcast: use `asdu.GlobalCommonAddr` (65535) — never 255, even with
   1-byte CA. Legal only for C_IC/C_CI/C_CS/C_RP.
7. IOA 0 is "irrelevant" and reserved for system commands; real points start
   at 1. Max IOA depends on `InfoObjAddrSize`.
8. Max ~249 bytes per ASDU (`ErrLengthOutOfRange`): batch large point sets
   into several helper calls, or use `isSequence=true` for contiguous IOAs.
9. `cs101.NewClient` returns **nil** when `Serial.Address` is empty — check.
10. Handlers run on the connection goroutine: don't block; hand work to your
    own goroutines and use the endpoint's `Send` later (it is thread-safe).
11. Select-before-execute is application-level: check
    `cmd.Qoc.InSelect` / `Qos.InSelect` and confirm without operating when
    it's a select.
12. File transfer (F_*) and IEC 62351-5 (S_*) types are not implemented.

## Verification without hardware

- Loop a cs104 client against a cs104 server on `127.0.0.1:2404`.
- For cs101 logic, see `cs101/link_test.go` — it wires client and server
  through an in-memory pipe (`io.Pipe`) and is the reference for driving the
  stack in tests.
- Third-party interop: `lib60870` (C), OpenMUC j60870, QTester104, mosaik,
  or any IEC 104 test set with defaults k=12, w=8, t1=15s, t2=10s, t3=20s.

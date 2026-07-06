# `asdu` — Application layer reference

The `asdu` package implements the IEC 60870-5-101/104 application layer: the
Application Service Data Unit (ASDU) with its data unit identifier, the
standard information object types, quality descriptors and time tags. Both
transport packages (`cs101`, `cs104`) exchange `*asdu.ASDU` values with your
application through the `asdu.Connect` interface.

```
ASDU = data unit identifier + information objects
       | type ID | variable structure | cause of transmission | common address | information objects... |
bytes |    1    |         1          |        1..2           |      1..2      |          n             |
```

## System parameters (`Params`)

`Params` fixes the on-wire width of the identifier fields. **Both peers must
use identical parameters** or every ASDU will be mis-parsed.

| Field | Meaning | Allowed |
|-------|---------|---------|
| `CauseSize` | Cause of transmission octets; 2 adds the originator address | 1, 2 |
| `CommonAddrSize` | Common (station) address octets | 1, 2 |
| `InfoObjAddrSize` | Information object address octets | 1, 2, 3 |
| `InfoObjTimeZone` | Time zone used to encode/decode CP24/CP56 time tags | any `*time.Location` |

Predefined:

- `asdu.ParamsWide` = `ParamsStandard104` — COT 2 (with originator address), CA 2, IOA 3. **This is the standard for IEC 104** and the default of `cs104`.
- `asdu.ParamsStandard101` — COT 1, CA 1, IOA 2. Default of `cs101`.
- `asdu.ParamsNarrow` — COT 1, CA 1, IOA 1.

## Identifier

Every ASDU carries an `Identifier`:

```go
type Identifier struct {
	Type       TypeID               // e.g. asdu.M_SP_NA_1
	Variable   VariableStruct       // object count + SQ (sequence) bit
	Coa        CauseOfTransmission  // cause + test/negative flags
	OrigAddr   OriginAddr           // originator address (only on wire when CauseSize == 2)
	CommonAddr CommonAddr           // station address; 0 is invalid, 0xFFFF broadcast
}
```

`CauseOfTransmission`:

```go
asdu.CauseOfTransmission{
	Cause:      asdu.Spontaneous, // see cause table below
	IsTest:     false,           // T bit
	IsNegative: false,           // P/N bit (negative confirmation)
}
```

Frequently used causes: `Periodic` (1), `Background` (2), `Spontaneous` (3),
`Initialized` (4), `Request` (5), `Activation` (6), `ActivationCon` (7),
`Deactivation` (8), `DeactivationCon` (9), `ActivationTerm` (10),
`ReturnInfoRemote` (11), `ReturnInfoLocal` (12),
`InterrogatedByStation` (20), `InterrogatedByGroup1..16` (21–36),
`RequestByGeneralCounter` (37), `RequestByGroup1..4Counter` (38–41),
`UnknownTypeID` (44), `UnknownCOT` (45), `UnknownCA` (46), `UnknownIOA` (47).

`CommonAddr` special values: `asdu.InvalidCommonAddr` (0, never valid) and
`asdu.GlobalCommonAddr` (65535, broadcast — use it even with 1-octet
addressing; it is mapped to 255 on the wire). Broadcast is only legal for
C_IC_NA_1, C_CI_NA_1, C_CS_NA_1 and C_RP_NA_1.

## Supported type identifications

Monitor direction (device → master):

| TypeID | Value | Content | Time tag |
|--------|-------|---------|----------|
| `M_SP_NA_1` / `M_SP_TA_1` / `M_SP_TB_1` | 1/2/30 | single point | — / CP24 / CP56 |
| `M_DP_NA_1` / `M_DP_TA_1` / `M_DP_TB_1` | 3/4/31 | double point | — / CP24 / CP56 |
| `M_ST_NA_1` / `M_ST_TA_1` / `M_ST_TB_1` | 5/6/32 | step position | — / CP24 / CP56 |
| `M_BO_NA_1` / `M_BO_TA_1` / `M_BO_TB_1` | 7/8/33 | 32-bit bitstring | — / CP24 / CP56 |
| `M_ME_NA_1` / `M_ME_TA_1` / `M_ME_TD_1` | 9/10/34 | normalized value | — / CP24 / CP56 |
| `M_ME_NB_1` / `M_ME_TB_1` / `M_ME_TE_1` | 11/12/35 | scaled value | — / CP24 / CP56 |
| `M_ME_NC_1` / `M_ME_TC_1` / `M_ME_TF_1` | 13/14/36 | short float | — / CP24 / CP56 |
| `M_IT_NA_1` / `M_IT_TA_1` / `M_IT_TB_1` | 15/16/37 | integrated totals | — / CP24 / CP56 |
| `M_EP_TA_1` / `M_EP_TD_1` | 17/38 | protection event | CP24 / CP56 |
| `M_EP_TB_1` / `M_EP_TE_1` | 18/39 | packed protection start events | CP24 / CP56 |
| `M_EP_TC_1` / `M_EP_TF_1` | 19/40 | packed output circuit info | CP24 / CP56 |
| `M_PS_NA_1` | 20 | packed single points with SCD | — |
| `M_ME_ND_1` | 21 | normalized value without quality | — |
| `M_EI_NA_1` | 70 | end of initialization | — |

Control direction (master → device):

| TypeID | Value | Content |
|--------|-------|---------|
| `C_SC_NA_1` / `C_SC_TA_1` | 45/58 | single command (/ CP56) |
| `C_DC_NA_1` / `C_DC_TA_1` | 46/59 | double command (/ CP56) |
| `C_RC_NA_1` / `C_RC_TA_1` | 47/60 | regulating step command (/ CP56) |
| `C_SE_NA_1` / `C_SE_TA_1` | 48/61 | set-point, normalized (/ CP56) |
| `C_SE_NB_1` / `C_SE_TB_1` | 49/62 | set-point, scaled (/ CP56) |
| `C_SE_NC_1` / `C_SE_TC_1` | 50/63 | set-point, short float (/ CP56) |
| `C_BO_NA_1` / `C_BO_TA_1` | 51/64 | 32-bit bitstring (/ CP56) |
| `C_IC_NA_1` | 100 | interrogation command |
| `C_CI_NA_1` | 101 | counter interrogation command |
| `C_RD_NA_1` | 102 | read command |
| `C_CS_NA_1` | 103 | clock synchronization command |
| `C_TS_NA_1` / `C_TS_TA_1` | 104/107 | test command (/ CP56) |
| `C_RP_NA_1` | 105 | reset process command |
| `C_CD_NA_1` | 106 | delay acquisition command (101 only) |
| `P_ME_NA_1` / `P_ME_NB_1` / `P_ME_NC_1` | 110/111/112 | parameter of measured value |
| `P_AC_NA_1` | 113 | parameter activation |

File transfer types (120–127) and IEC 62351-5 security types are enumerated
but not implemented.

## Sending data (monitor direction helpers)

Each helper builds the ASDU, validates the cause of transmission against the
standard, and calls `c.Send(...)` on the given `asdu.Connect` (a cs104
client/server/session or a cs101 client/server).

```go
// without time tag; isSequence packs consecutive IOAs (SQ=1)
asdu.Single(c, isSequence, coa, commonAddr, infos ...asdu.SinglePointInfo) error
asdu.Double(c, isSequence, coa, ca, infos ...asdu.DoublePointInfo) error
asdu.Step(c, isSequence, coa, ca, infos ...asdu.StepPositionInfo) error
asdu.BitString32(c, isSequence, coa, ca, infos ...asdu.BitString32Info) error
asdu.MeasuredValueNormal(c, isSequence, coa, ca, infos ...asdu.MeasuredValueNormalInfo) error
asdu.MeasuredValueNormalNoQuality(c, isSequence, coa, ca, infos...) error // M_ME_ND_1
asdu.MeasuredValueScaled(c, isSequence, coa, ca, infos ...asdu.MeasuredValueScaledInfo) error
asdu.MeasuredValueFloat(c, isSequence, coa, ca, infos ...asdu.MeasuredValueFloatInfo) error
asdu.IntegratedTotals(c, isSequence, coa, ca, infos ...asdu.BinaryCounterReadingInfo) error

// with time tag: CP24Time2a (…CP24Time2a) or CP56Time2a (…CP56Time2a); never sequences
asdu.SingleCP56Time2a(c, coa, ca, infos...) error   // and Double/Step/BitString32/
                                                    // MeasuredValueNormal/Scaled/Float,
                                                    // IntegratedTotals variants

// protection equipment, packed points, end of init
asdu.EventOfProtectionEquipmentCP56Time2a(c, coa, ca, infos...) error
asdu.PackedStartEventsOfProtectionEquipmentCP56Time2a(c, coa, ca, info) error
asdu.PackedOutputCircuitInfoCP56Time2a(c, coa, ca, info) error
asdu.PackedSinglePointWithSCD(c, isSequence, coa, ca, infos...) error
asdu.EndOfInitialization(c, coa, ca, ioa, coi) error
```

Info structs carry IOA, value, quality and an optional `Time` (used only by
the time-tagged variants):

```go
asdu.SinglePointInfo{Ioa: 100, Value: true, Qds: asdu.QDSGood, Time: time.Now()}
asdu.MeasuredValueFloatInfo{Ioa: 400, Value: 21.5, Qds: asdu.QDSInvalid}
```

**Cause validation:** the helpers return `asdu.ErrCmdCause` when the cause is
not permitted for the type. E.g. `Single` accepts `Background`,
`Spontaneous`, `Request`, `ReturnInfoRemote`, `ReturnInfoLocal` and the
interrogation causes; the time-tagged variants do not accept `Background`
or `Periodic`; `IntegratedTotals` accepts `Spontaneous` and the counter
request causes only.

## Sending commands (control direction helpers)

```go
asdu.SingleCmd(c, asdu.C_SC_NA_1, coa, ca, asdu.SingleCommandInfo{
	Ioa:   6000,
	Value: true,
	Qoc:   asdu.QualifierOfCommand{Qual: asdu.QOCShortPulseDuration, InSelect: false},
})
asdu.DoubleCmd(c, asdu.C_DC_NA_1, coa, ca, asdu.DoubleCommandInfo{...})   // DCOOn / DCOOff
asdu.StepCmd(c, asdu.C_RC_TA_1, coa, ca, asdu.StepCommandInfo{...})       // SCOStepUP / SCOStepDown
asdu.SetpointCmdNormal(c, asdu.C_SE_NA_1, coa, ca, asdu.SetpointCommandNormalInfo{...})
asdu.SetpointCmdScaled(c, asdu.C_SE_NB_1, coa, ca, asdu.SetpointCommandScaledInfo{...})
asdu.SetpointCmdFloat(c, asdu.C_SE_NC_1, coa, ca, asdu.SetpointCommandFloatInfo{...})
asdu.BitsString32Cmd(c, asdu.C_BO_NA_1, coa, ca, asdu.BitsString32CommandInfo{...})
```

Pass the `_TA_1` type constant to get the CP56Time2a variant (the `Time`
field of the info struct is then encoded). Command causes must be
`Activation` or `Deactivation`, otherwise `ErrCmdCause`.

System commands:

```go
asdu.InterrogationCmd(c, coa, ca, asdu.QOIStation)        // QOIStation or QOIGroup1..16
asdu.CounterInterrogationCmd(c, coa, ca, asdu.QualifierCountCall{Request: asdu.QCCTotal, Freeze: asdu.QCCFrzRead})
asdu.ReadCmd(c, coa, ca, ioa)                             // cause forced to Request
asdu.ClockSynchronizationCmd(c, coa, ca, time.Now())      // cause forced to Activation
asdu.TestCommand(c, coa, ca)                              // cause forced to Activation
asdu.TestCommandCP56Time2a(c, coa, ca, time.Now())
asdu.ResetProcessCmd(c, coa, ca, asdu.QPRGeneralRest)
asdu.DelayAcquireCommand(c, coa, ca, msec)                // IEC 101 only
asdu.ParameterNormal(c, coa, ca, asdu.ParameterNormalInfo{...}) // and Scaled/Float/Activation
```

## Decoding received ASDUs (getters)

On the receiving side, switch on `pack.Type` and call the matching getter.
Getters are non-destructive — they leave the ASDU intact, so they can be
called repeatedly and combined freely with `SendReplyMirror` (only the
low-level `Decode*` building blocks consume the buffer):

```go
switch pack.Type {
case asdu.M_SP_NA_1, asdu.M_SP_TA_1, asdu.M_SP_TB_1:
	for _, p := range pack.GetSinglePoint() {
		// p.Ioa, p.Value (bool), p.Qds, p.Time (zero when untagged)
	}
case asdu.M_ME_NC_1, asdu.M_ME_TC_1, asdu.M_ME_TF_1:
	for _, m := range pack.GetMeasuredValueFloat() { ... }
case asdu.C_SC_NA_1, asdu.C_SC_TA_1:
	cmd := pack.GetSingleCmd() // cmd.Value, cmd.Qoc.InSelect, cmd.Qoc.Qual
}
```

Available getters: `GetSinglePoint`, `GetDoublePoint`, `GetStepPosition`,
`GetBitString32`, `GetMeasuredValueNormal`, `GetMeasuredValueScaled`,
`GetMeasuredValueFloat`, `GetIntegratedTotals`,
`GetEventOfProtectionEquipment`, `GetPackedStartEventsOfProtectionEquipment`,
`GetPackedOutputCircuitInfo`, `GetPackedSinglePointWithSCD`,
`GetEndOfInitialization`, `GetSingleCmd`, `GetDoubleCmd`, `GetStepCmd`,
`GetSetpointNormalCmd`, `GetSetpointCmdScaled`, `GetSetpointFloatCmd`,
`GetBitsString32Cmd`, `GetInterrogationCmd`, `GetCounterInterrogationCmd`,
`GetReadCmd`, `GetClockSynchronizationCmd`, `GetTestCommand`,
`GetTestCommandCP56Time2a`, `GetResetProcessCmd`, `GetDelayAcquireCommand`,
`GetParameterNormal`, `GetParameterScaled`, `GetParameterFloat`,
`GetParameterActivation`.

## Replying (server/slave side)

`SendReplyMirror` echoes the received ASDU back with a different cause —
the standard confirm pattern:

```go
_ = pack.SendReplyMirror(c, asdu.ActivationCon)  // positive confirmation
// ... send the requested data ...
_ = pack.SendReplyMirror(c, asdu.ActivationTerm) // activation termination

// negative confirmation:
pack.Coa.IsNegative = true
_ = pack.SendReplyMirror(c, asdu.ActivationCon)

// protocol-error mirrors:
_ = pack.SendReplyMirror(c, asdu.UnknownTypeID) // also UnknownCOT / UnknownCA / UnknownIOA
```

## Quality descriptors

`QualityDescriptor` bit flags: `QDSGood` (0), `QDSOverflow`, `QDSBlocked`,
`QDSSubstituted`, `QDSNotTopical`, `QDSInvalid`.
Protection equipment uses `QualityDescriptorProtection`: `QDPGood`,
`QDPElapsedTimeInvalid`, `QDPBlocked`, `QDPSubstituted`, `QDPNotTopical`,
`QDPInvalid`.

## Time tags

- `CP56Time2a` — 7 octets, full date/time with milliseconds. Encoded/decoded
  in `Params.InfoObjTimeZone` (default UTC — recommended).
- `CP24Time2a` — 3 octets, minutes+milliseconds only. On decode, date and
  hour come from the local clock; tags more than 5 minutes "ahead" are
  interpreted as belonging to the previous hour.
- An invalid (IV bit) or truncated time decodes to the zero `time.Time`
  (check with `t.IsZero()`).

## Diagnostics

- `pack.String()` — compact human-readable dump (non-destructive).
- `json.Marshal(pack)` — structured JSON with a `value` field typed per
  TypeID (non-destructive). See the TypeScript-style schema in the
  `MarshalJSON` doc comment.

## Errors

| Error | Meaning |
|-------|---------|
| `ErrCmdCause` | Cause of transmission not allowed for this type |
| `ErrTypeIDNotMatch` | Type constant doesn't match the helper used |
| `ErrParam` | Params out of range / peers mis-configured |
| `ErrCommonAddrZero` | Common address 0 is not usable |
| `ErrInfoObjAddrFit` | IOA does not fit `InfoObjAddrSize` |
| `ErrLengthOutOfRange` | Too many objects for one ASDU (max 249 bytes) |
| `ErrNotAnyObjInfo` | No information objects passed to a send helper |

# cs104_server_general

An IEC 60870-5-104 outstation that simulates a small substation: **114
information objects covering every monitored type this library supports**,
with values that move and quality descriptors that cover every flag, plus
commands and file transfer.

It exists to be pointed at. A master is only as tested as the device it was
tested against, and a device that reports a dozen good analogues is not a test
of anything.

```console
$ go run .
IEC 60870-5-104 outstation on :2404
common address 1 · 114 information objects · 2 files
commands at 9001..9601 — see commands.go for what each one moves
```

```
-listen ADDR     address to listen on            (default :2404)
-period DUR      how often the process advances  (default 1s)
-quiet           do not log the protocol
-offer-file      announce the disturbance record after a master connects (default true)
```

Try it with the explorer in this repository:

```console
$ go run .                                   # this directory
$ cd ../cs104_explorer && go run . 127.0.0.1:2404
```

## What it reports

Common address **1**. Addresses are grouped one block per type, so a master's
point table sorts into something readable.

| Addresses | Type | Count | Sent as |
| --- | --- | --- | --- |
| 0 | `M_EI_NA_1` end of initialization | 1 | once per connection |
| 1001–1012 | `M_SP_NA_1` single point | 12 | interrogation |
| 1101–1104 | `M_SP_TA_1` single point, CP24Time2a | 4 | spontaneous |
| 1201–1208 | `M_SP_TB_1` single point, CP56Time2a | 8 | spontaneous |
| 2001–2008 | `M_DP_NA_1` double point | 8 | interrogation |
| 2201–2204 | `M_DP_TB_1` double point, CP56Time2a | 4 | spontaneous |
| 3001–3004 | `M_ST_NA_1` step position | 4 | interrogation |
| 3201–3202 | `M_ST_TB_1` step position, CP56Time2a | 2 | spontaneous |
| 3501–3504 | `M_BO_NA_1` bit string of 32 bits | 4 | interrogation |
| 3601–3602 | `M_BO_TB_1` bit string, CP56Time2a | 2 | spontaneous |
| 4001–4008 | `M_ME_NA_1` measured value, normalized | 8 | interrogation |
| 4101–4104 | `M_ME_ND_1` normalized, **no quality descriptor** | 4 | interrogation |
| 4201–4204 | `M_ME_TD_1` normalized, CP56Time2a | 4 | spontaneous |
| 4501–4508 | `M_ME_NB_1` measured value, scaled | 8 | interrogation |
| 4601–4604 | `M_ME_TE_1` scaled, CP56Time2a | 4 | spontaneous |
| 5001–5012 | `M_ME_NC_1` measured value, short float | 12 | interrogation |
| 5101–5106 | `M_ME_TF_1` short float, CP56Time2a | 6 | spontaneous |
| 6001–6006 | `M_IT_NA_1` integrated totals | 6 | **counter** interrogation |
| 6101–6102 | `M_IT_TB_1` integrated totals, CP56Time2a | 2 | **counter** interrogation |
| 6501–6502 | `M_PS_NA_1` packed single points with status change detection | 2 | interrogation |
| 7001–7004 | `M_EP_TD_1` protection event, CP56Time2a | 4 | spontaneous |
| 7101 | `M_EP_TE_1` packed start events, CP56Time2a | 1 | spontaneous |
| 7201 | `M_EP_TF_1` packed output circuit info, CP56Time2a | 1 | spontaneous |
| 7301 | `M_EP_TA_1` protection event, CP24Time2a | 1 | spontaneous |
| 7401 | `M_EP_TB_1` packed start events, CP24Time2a | 1 | spontaneous |
| 7501 | `M_EP_TC_1` packed output circuit info, CP24Time2a | 1 | spontaneous |

**Time-tagged types are never part of an interrogation reply.** The standard
answers a general interrogation with the untagged variants and uses the tagged
ones for spontaneous reporting, and so does this. Counters answer counter
interrogation, not general interrogation.

## What it reports badly, on purpose

A simulator where everything reads GOOD cannot tell you whether the master you
are testing renders quality at all. Every point block spreads the quality
descriptors across its addresses, so all of them are on screen at once:

`GOOD`, `OV` overflow, `BL` blocked, `SB` substituted, `NT` not topical,
`IV` invalid, and the combination `NT|IV`. Protection equipment carries its
own descriptor, so `EI` elapsed time invalid appears there too. The
`M_ME_ND_1` block carries **no** quality descriptor at all — a master that
shows those as GOOD is inventing it.

One short-float point is forced invalid at a time, moving through the block
every ten cycles, so quality is seen *changing* rather than being a static
pattern. Two scaled points sit at the ends of the int16 range, where a master
that mishandles the sign or the width shows it. Double points cycle through
all four states, including the two that mean "the device cannot tell".

Values move on their own periods: analogues are sine waves at substation
magnitudes (line voltage, current, frequency, power factor, a negative flow),
counters only ever increase, step positions walk their range, and bit strings
rotate so every bit is set at some point.

## Commands

**Commands live in their own address space.** Nothing in IEC 60870-5-104 says
the command that operates the single point at 1001 is itself at 1001, and this
simulator deliberately does not put it there.

| Address | Types accepted | Moves |
| --- | --- | --- |
| 9001–9004 | `C_SC_NA_1`, `C_SC_TA_1` | single points 1001–1004 |
| 9101–9102 | `C_DC_NA_1`, `C_DC_TA_1` | double points 2001–2002 |
| 9201 | `C_RC_NA_1`, `C_RC_TA_1` | step position 3001 |
| 9301 | `C_SE_NA_1`, `C_SE_TA_1` | normalized 4001 |
| 9401 | `C_SE_NB_1`, `C_SE_TB_1` | scaled 4501 |
| 9501 | `C_SE_NC_1`, `C_SE_TC_1` | short float 5001 |
| 9601 | `C_BO_NA_1`, `C_BO_TA_1` | bit string 3501 |

Every command is answered the way the standard specifies, so a master can tell
the outcomes apart:

- **select** (S/E = 1) is confirmed and remembered for 30 seconds; an execute
  is checked against it, and a select for one address never licenses an
  execute on another.
- **execute** is confirmed, applied, reported back as return information
  (cause 11) on the monitored point it moved, and then terminated.
- **9004 always refuses** — it answers with a negative activation
  confirmation, so a master has something to render that path with.
- an **unknown command address** is answered `UnknownIOA` (cause 47).

A read command (`C_RD_NA_1`) for any monitored address answers with that one
object; anything else gets `UnknownIOA`. Clock synchronisation, test command,
reset process and delay acquisition are all confirmed.

## Files

Two files are offered over file transfer: a ~9 kB COMTRADE-like disturbance
record at IOA 100, and a short event list at IOA 101. The disturbance record
is announced with `F_FR_NA_1` shortly after a master connects, so a master
that accepts announcements pulls it across on its own. `-offer-file=false`
turns that off.

## Reading it as example code

| File | Shows |
| --- | --- |
| `srvGeneral.go` | server setup and the handler interface: interrogation, counters, read, clock, reset |
| `points.go` | the simulated database, and which types answer which request |
| `commands.go` | the command confirmation sequence, including select-before-execute |

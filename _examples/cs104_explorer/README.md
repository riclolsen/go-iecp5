# cs104-explorer

A full-screen terminal browser for one IEC 60870-5-104 outstation, driven by
the keyboard or the mouse.

It answers the question you actually have when pointing at an unfamiliar
device: *what is this thing reporting, and does it respond?*

```console
$ cs104-explorer -demo
cs104-explorer  demo (in-process outstation)  ● active            up 0:31  1.5/s  18:25:43
 1 Overview  2 Points  3 Events  4 Log  5 Files  6 Help                         18 points
──────────────────────────────────────────────────────────────────────────────────────────
 POINT ▲    TYPE               VALUE TREND        QUALITY     CAUSE              AGE TIME
 1:100      M_SP_NA              OFF ▁▁▁▁████████ GOOD        InterrogatedBySta…  0s —
 1:103      M_SP_NA              OFF ▄▄▄▄▄▄▄▄▄▄▄▄ NT          InterrogatedBySta…  0s —
 1:200      M_DP_NA               ON ▄▄▄▄▄▄▄▄▄▄▄▄ GOOD        InterrogatedBySta…  0s —
 1:400      M_ME_TF        11059.775 ▁▂▃▄▄▅▆▆▇███ GOOD        Spontaneous         0s 18:25:43.170
 1:403      M_ME_NC             0.42 ▄▄▄▄▄▄▄▄▄▄▄▄ SB          Periodic            0s —
 1:700      M_IT_NA            10427 ▁▂▃▄▄▅▆▆▇███ GOOD        Spontaneous         0s —
──────────────────────────────────────────────────────────────────────────────────────────
 [i GI] [s Read] [/ Filter] [d Inspect] [c Off] [o On] [b Setpoint] [E Select+Execute]
 ↑↓ move · enter command · d inspect · / filter · < > r sort · b setpoint · e export
```

```console
$ go run . -demo                 # from this directory
$ go run . 10.0.0.5:2404
```

**`-demo` needs no hardware.** It runs a full outstation inside the same
process over a loopback socket — the real APCI state machine, the real ASDU
codecs and the real file transfer, with no device. It is the fastest way to
see what the tool does, and it is what the tests drive.

This is a **separate Go module** (its own `go.mod` with a `replace` pointing
at the repository root), so the UI dependencies stay out of the `go-iecp5`
library.

## Usage

```
cs104-explorer -host HOST:PORT [flags]
cs104-explorer -demo
```

**Connection** — all of it editable while running, with `C`

| Flag | Default | Meaning |
| --- | --- | --- |
| `-host ADDR` | — | outstation address (`host:port`, port defaults to 2404) |
| `-ca N` | 1 | ASDU common address of the outstation |
| `-orig N` | 0 | originator address (0 when unused) |
| `-timeout DUR` | 30s | connect timeout (t₀) |
| `-reconnect DUR` | 10s | reconnect interval |
| `-demo` | off | run a simulated outstation in-process |

**Interface**

| Flag | Default | Meaning |
| --- | --- | --- |
| `-mouse` | true | enable the mouse |
| `-inline` | off | draw inline instead of taking the whole terminal |
| `-stale DUR` | 30s | fade points not updated for this long; `0` disables |
| `-file-dir PATH` | `iec104-files` | where completed file transfers are written |
| `-v` | off | start with protocol logging on |

**Commands**

| Flag | Default | Meaning |
| --- | --- | --- |
| `-direct` | off | direct execute instead of select before execute |
| `-no-confirm` | off | issue commands without asking first |
| `-pulse WHICH` | short | qualifier of command: `none`, `short`, `long`, `persistent` |

## The six screens

**1 Overview** — the session and the device's health: connection and STARTDT
state, uptime, ASDU rate and trend, the addressing and parameters in force,
how many points of each kind have arrived, how many carry quality flags, and
the recent activity.

**2 Points** — the point table. Every information object the device has
reported, with its value, a sparkline trend, the named quality flags, the
cause it last arrived under, how long since it updated and the device's own
time tag.

**3 Events** — every arrival in order, newest last. What changed and when, as
distinct from what the value is now.

**4 Log** — the activity log: what was sent, what came back, what failed.
`v` adds the library's own protocol log — raw frames, APCI and ASDU decode.

**5 Files** — file transfer over `F_FR_NA_1`…`F_DR_TA_1`. `l` calls the
outstation's file directory, `enter` fetches the selected file, and files the
outstation announces on its own are pulled automatically. Completed transfers
are written to `./iec104-files/`.

**6 Help** — the full key and mouse reference.

Points can be filtered on anything in the row and sorted by any column —
including by quality, **worst first**, which is how you find the broken points
in a device with a thousand good ones. `d` opens the inspector for one point,
with the flags named, the trend drawn, and the type and cause each value
arrived under.

## Keys

| Key | Does |
| --- | --- |
| `1`–`6`, `tab` | switch screens |
| `↑` `↓`, `j` `k` | move the cursor; `pgup`/`pgdn` by a page |
| `enter` | act on the selected row — the command dialog, or fetch a file |
| `a` / `A` | STARTDT / STOPDT — start and stop data transfer |
| `i` / `p` | general interrogation / counter interrogation |
| `t` / `T` | clock synchronisation / test command |
| `R` | reset process |
| `s` | read command for one information object address |
| `c` / `o` | off / on — single or double command on the selected point |
| `b` | setpoint on the selected point |
| `E` | switch between select-before-execute and direct execute |
| `C` | edit the connection and reconnect in place |
| `/`, `esc` | filter the list; clear the filter |
| `<` `>`, `r` | change and reverse the sort column |
| `d` | the point inspector |
| `f` | follow the newest row |
| `l` | Files: call the directory |
| `x` | clear the current list |
| `e` | export the current list as CSV |
| `v` | protocol (debug) logging |
| `?` | the full reference |
| `q` | quit |

The setpoint prompt reads the variation from the value: `12.5f` is a short
float, `300s` a scaled value, `0.5n` a normalised one and `0xffb` a bitstring.
One field says both what to send and how.

## Mouse

Click a tab, a row, a column heading or a footer button; click a selected row
again to act on it, right-click a point for the inspector, scroll with the
wheel, and drag the scrollbar. `-mouse=false` turns it off.

Every click resolves to the key the keyboard would have pressed, so the two
can never drift apart.

## Commands are deliberate

Commands are the reason to be careful, so the tool is.

`enter` on a point opens a dialog naming exactly what will be sent,
**select-before-execute by default**, with a confirmation before anything
moves. The select is the outstation's chance to refuse before plant moves.

`-direct` and `-no-confirm` turn that off for the devices and situations that
need it. While `-no-confirm` is in effect **the toolbar says so** — it is the
one mode where no dialog appears to say it for itself.

`E` toggles between select-before-execute and direct execute at runtime.

## Editing the connection while it runs

`C` opens an editor for the address, the common address, the originator
address, the connect timeout and the reconnect interval. Applying it tears the
session down and brings a new one up in place.

That exists because a common address read off a drawing is a guess until
something answers, and restarting the tool to try 2 instead of 1 is how ten
minutes of commissioning becomes an afternoon.

Pointing somewhere new drops the point table with it. Those measurements came
from a different device.

## Export

`e` writes what is on screen — **after the filter and the sort**, not before —
to a timestamped CSV in the working directory.

| Screen | File | Columns |
| --- | --- | --- |
| Points, Overview | `iec104-points-<stamp>.csv` | `common_address,ioa,type,value,quality,cause,timestamp,received,updates` |
| Events | `iec104-events-<stamp>.csv` | `received,common_address,ioa,type,value,quality,cause,timestamp` |
| Log | `iec104-log-<stamp>.csv` | `time,level,message` |
| Files | `iec104-files-<stamp>.csv` | `ioa,name_of_file,size,modified,status,local` |

## Reading it as example code

| File | Shows |
| --- | --- |
| `conn.go` | the session lifecycle, the cs104 client in its own goroutine, ASDU decoding into table rows |
| `demo.go` | a complete in-process outstation: interrogation, spontaneous data, commands, file transfer |
| `model.go` | the Bubble Tea model: point state, event ring, sorting and filtering, key handling |
| `view.go`, `layout.go`, `theme.go` | rendering, and a layout computed from the terminal size |
| `mouse.go` | resolving a pointer position to the key the keyboard would have pressed |
| `command.go` | one type that describes a command, names it, and puts it on the wire |
| `files.go` | the file transfer screen over the `filetransfer` package |
| `export.go` | CSV export of the current view |

The architecture worth copying: **the cs104 session runs in its own goroutines
and never touches the model.** Everything the device says arrives on one
channel that the model drains, and every action is a `tea.Cmd` that returns a
result message.

Key handling is a plain `HandleKey(string)`, and the pointer resolves against
a layout computed from the terminal size. That is what lets the whole
interface — clicks included — be driven from tests without a terminal, which
is how `explorer_test.go` works.

## Troubleshooting

**Connects but the point table stays empty.** Data transfer has to be started:
the header says `connected (STOPDT)` until it is. Press `a`, then `i` for an
interrogation. If it is already active, check `-ca` — the common address must
match the outstation's.

**Points fade after 30 seconds.** That is `-stale` marking them as not
recently updated. Raise it, or set `-stale 0`, for a slowly polled device.

**A command is confirmed but nothing moves.** With select-before-execute the
select is only half of it. The log shows the outstation's `ActivationCon`; a
negative one (`,neg`) means it refused.

**Time tags look shifted.** Time tags carry no zone. The library encodes and
decodes them in `Params.InfoObjTimeZone`, UTC by default, and the tool shows
them in local time. A device whose clock is set to local time while its tags
claim UTC will read as offset by exactly that difference.

**The terminal is left in a strange state after a crash.** Run `reset`. Use
`-inline` to avoid the alternate screen entirely.

## See also

- [`cs104_server_general`](../cs104_server_general) — an outstation to point it at
- [File transfer guide](../../docs/filetransfer.md) · [cs104 guide](../../docs/cs104.md)

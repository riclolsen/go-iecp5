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
 [i GI] [s Read] [/ Filter] [d Inspect] [o Command] [O Feedback] [E Select+Execute]
 ↑↓ move · o command · O feedback · d inspect · / filter · < > r sort · e export
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
| `-history N` | 100000 | arrivals the event list keeps; `0` keeps everything |
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

The point table keeps **every** information object a device reports; the event
list is a window, because the order of arrivals is unbounded while a device
keeps talking. The window defaults to 100000, which holds one general
interrogation of a very large device with room to spare — one interrogation of
an *N* object outstation produces *N* arrivals, so a 15000 point device fills
15000 of it at a stroke. When the window does trim, the tab bar and the
Overview say how many arrivals were discarded; raise `-history`, or set
`-history 0` to keep everything.

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
| `o` / `O` | the command dialog / the feedback for the last command |
| `a` / `A` | STARTDT / STOPDT — start and stop data transfer |
| `i` / `p` | general interrogation / counter interrogation |
| `t` / `T` | clock synchronisation / test command |
| `R` | reset process |
| `s` | read command for one information object address |
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

In the command dialog `←` `→` change the type identification, the value of a
single, double or step command, the pulse qualifier and the command mode;
everything else is typed.

## Mouse

Click a tab, a row, a column heading or a footer button; click a selected row
again to act on it, right-click a point for the inspector, scroll with the
wheel, and drag the scrollbar. `-mouse=false` turns it off.

Every click resolves to the key the keyboard would have pressed, so the two
can never drift apart.

## Commands: every parameter, entered

**In IEC 60870-5-104 a command is not attached to a monitored object.** Its
information object address lives in its own address space, and nothing says
the command that operates the single point at IOA 1001 is itself at 1001 —
on the sample outstation in this repository it is at 9001. A tool that
operates "the selected row" is guessing which plant item moves.

So `o` opens a dialog and every parameter is entered there:

```
 ╭─ Send command ─────────────────────────────────────────────╮
 │ ▸ Type identification                                      │
 │   ‹ C_SC_NA_1  single command ›                            │
 │   Common address (ASDU)                                    │
 │   1                              the station, 1..65534     │
 │   Information object address                               │
 │   9001                           the command's own address │
 │   Value                                                    │
 │   ‹ ON ›                         ← → to change             │
 │   Qualifier of command                                     │
 │   ‹ short pulse ›                pulse duration            │
 │   Command mode                                             │
 │   ‹ select, then execute ›       the S/E bit               │
 ╰────────────────────────────────────────────────────────────╯
```

The dialog keeps what was entered last, so adjusting or repeating a command
does not mean typing it again.

**Feedback is shown, not assumed.** Sending opens a timeline that fills in as
the outstation answers, and `O` reopens it afterwards:

```
 ╭─ Command ──────────────────────────────────────────────────╮
 │ C_SC_NA_1 ca=1 ioa=9001 ON (execute, short pulse)          │
 │ state  complete                                            │
 │ 18:41:02.114  execute sent: C_SC_NA_1 ca=1 ioa=9001 ON     │
 │ 18:41:02.220  activation confirmed                         │
 │ 18:41:02.221  return information: 1:1001 = ON              │
 │ 18:41:02.223  activation terminated — the command is …     │
 ╰────────────────────────────────────────────────────────────╯
```

A command that is sent and never spoken of again is the failure this exists
to make visible: the state line says what is still outstanding, a negative
activation confirmation is reported as a refusal, and `UnknownIOA` says the
outstation has no command at that address.

**Select-before-execute is two transmissions**, as the standard intends. The
dialog sends the select; the execute goes only once the outstation confirms
it and the operator presses `enter` again. A failed select is never followed
by an execute.

`-direct` and `-no-confirm` turn the confirmation off for the devices and
situations that need it. While `-no-confirm` is in effect **the toolbar says
so** — it is the one mode where no dialog appears to say it for itself.
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
| `conn.go` | the session lifecycle, the cs104 client in its own goroutine, ASDU decoding into table rows, and the batch queue that keeps a burst from being dropped |
| `demo.go` | a complete in-process outstation: interrogation, spontaneous data, commands, file transfer |
| `model.go` | the Bubble Tea model: point state, event ring, sorting and filtering, key handling |
| `view.go`, `layout.go`, `theme.go` | rendering, and a layout computed from the terminal size |
| `mouse.go` | resolving a pointer position to the key the keyboard would have pressed |
| `command.go` | one type that describes a command, names it, and puts it on the wire |
| `cmddialog.go` | the command dialog and the lifecycle it tracks afterwards |
| `files.go` | the file transfer screen over the `filetransfer` package |
| `export.go` | CSV export of the current view |

The architecture worth copying: **the cs104 session runs in its own goroutines
and never touches the model.** Everything the device says is accumulated into
a batch that the model takes whole, and every action is a `tea.Cmd` that
returns a result message.

**Arrivals are batched, and that is not an optimisation.** Bubble Tea
processes one message per update cycle with a render in between. One message
per ASDU into a bounded channel means the interface decides how much of the
device's database it is willing to look at and discards the rest: measured
against a 4500-object outstation reporting events, that cost **about 40% of
the stream, silently**. Batching makes the cost one render per burst instead
of one per ASDU, and a batch that outgrows its limit makes the protocol
goroutine wait — so back-pressure reaches TCP and the outstation holds the
data at the source rather than the master losing it.

The Overview screen shows a **Dropped** count for the messages that *are*
droppable (log and status lines). A tool that loses anything quietly is not
one you can trust about what a device sent.

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
select is only half of it — press `enter` again on the feedback dialog to
send the execute. If the state line says the select was confirmed and nothing
followed, the execute was never sent.

**A command comes back `UnknownIOA`.** The outstation has no command at that
address. Command addresses are not monitored-point addresses; look them up in
the device's point list rather than reading one off the Points table.

**The Events list shows fewer arrivals than the device sent.** Check the tab
bar: it says `N older discarded` when the event window has trimmed. The point
table is unaffected — it keeps every object — but the arrival history is a
window, and one interrogation of a 15000 point device produces 15000
arrivals. Raise `-history`.

**Points are missing after interrogating a large device.** The explorer no
longer drops them, so suspect the outstation: `cs104`'s `Send` does not
block, and an outstation that writes `_ = asdu.MeasuredValueFloat(c, ...)`
loses ASDUs as soon as its send buffer fills — it believes it answered in
full. Check `Objects in` on the Overview against the device's point count,
and see [the cs104 guide](../../docs/cs104.md#sending-flow-control-and-back-pressure).

**Time tags look shifted.** Time tags carry no zone. The library encodes and
decodes them in `Params.InfoObjTimeZone`, UTC by default, and the tool shows
them in local time. A device whose clock is set to local time while its tags
claim UTC will read as offset by exactly that difference.

**The terminal is left in a strange state after a crash.** Run `reset`. Use
`-inline` to avoid the alternate screen entirely.

## See also

- [`cs104_server_general`](../cs104_server_general) — an outstation to point it at
- [File transfer guide](../../docs/filetransfer.md) · [cs104 guide](../../docs/cs104.md)

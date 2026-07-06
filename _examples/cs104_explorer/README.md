# cs104_explorer

An interactive terminal IEC 60870-5-104 **master** (controlling station) built
with [Bubble Tea](https://github.com/charmbracelet/bubbletea). It connects to
IEC 104 servers, issues requests, sends control commands, and shows the
monitored points and a live protocol log.

This is a **separate Go module** (its own `go.mod` with a `replace` pointing at
the repository root), so the Bubble Tea UI dependencies stay out of the
`go-iecp5` library.

## Run

```bash
cd _examples/cs104_explorer
go run .                 # then set the target and connect inside the UI
go run . 10.0.0.5:2404   # optional: preset the server address
```

To try it without hardware, run one of the sample servers in another terminal
(e.g. `cd ../cs104_server_general && go run .`) and point the explorer at
`127.0.0.1:2404`.

## Features

- Connect / disconnect to an IEC 104 server, with editable target address and
  common address.
- Automatic STARTDT on connect; manual STARTDT/STOPDT control.
- One-key requests: general interrogation, counter interrogation, clock
  synchronization, test command, reset process.
- Command builder for control commands: single, double, step, set-point
  (float / scaled / normalized) and read command, with select/execute and
  a command qualifier.
- **Points** tab: every received monitored value (single/double point, step,
  bitstring, normalized/scaled/float measured value, integrated totals) shown
  by IOA with value, quality, cause of transmission, time tag and update count.
- **Log** tab: application events plus the full protocol log (raw RX/TX
  frames, APCI, ASDU decode) — toggle protocol verbosity with `v`.

## Keys

| Key | Action |
|-----|--------|
| `1` `2` `3` / `tab` | switch Points / Log / Send Command tabs |
| `e` | edit target (server address + common address) |
| `c` / `x` | connect / disconnect |
| `s` / `S` | send STARTDT / STOPDT |
| `g` | general interrogation |
| `C` | counter interrogation |
| `y` | clock synchronization |
| `t` | test command |
| `z` | reset process |
| `v` | toggle protocol (debug) logging |
| `ctrl+l` / `ctrl+r` | clear log / clear points |
| `i` | (Send tab) edit and send a command |
| `↑`/`↓`, `tab` | (in forms) move between fields |
| `←`/`→` | (in forms) change the command kind or select/execute |
| `enter` | apply (connection editor) / send (command builder) |
| `esc` | leave the editor/form |
| `q` / `ctrl+c` | quit |

In the command builder, the **Value** field is interpreted per command:
`on`/`off` for single/double, `up`/`down` for step, a number for set-points;
the read command ignores it.

## How it works

- `handler.go` implements `cs104.ClientHandlerInterface`. `ASDUHandlerAll`
  logs every ASDU and decodes monitor-direction payloads into table rows.
  A `clog.LogProvider` routes the library's protocol log into the UI instead
  of stdout (which would corrupt the alternate screen).
- Background client goroutines deliver updates to the UI through a `bridge`
  that wraps `*tea.Program.Send`.
- `model.go` / `view.go` hold the Bubble Tea model, update loop and rendering;
  `form.go` builds the outgoing command ASDUs.

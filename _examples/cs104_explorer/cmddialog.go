package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/riclolsen/go-iecp5/asdu"
)

// The command dialog is where every parameter of a command is entered.
//
// It is a dialog rather than a pair of keys on the point table because in
// IEC 60870-5-104 a command has nothing to do with the monitored object the
// cursor is on: its type identification, common address and information
// object address are all independent of anything being reported. Guessing any
// of them from the selected row would be guessing which plant item moves.

// cmdFieldID names one row of the dialog.
type cmdFieldID int

const (
	cfType cmdFieldID = iota
	cfCA
	cfIOA
	cfValue
	cfQual
	cfMode
)

// cmdForm is the state of the open command dialog.
type cmdForm struct {
	active  bool
	typeIdx int
	ca      string
	ioa     string
	onoff   int // 0 = OFF / LOWER, 1 = ON / HIGHER
	value   string
	qocIdx  int
	qos     string
	sbo     bool

	cursor int
	offset int
	err    string

	// everOpened keeps the last entry so that adjusting or repeating a
	// command does not mean typing every field again.
	everOpened bool
}

func (c *cmdForm) spec() cmdSpec { return cmdSpecs[c.typeIdx] }

// visible is the field list for the selected command type: a bit string
// command carries neither a qualifier nor a select/execute bit, so neither
// row is offered.
func (c *cmdForm) visible() []cmdFieldID {
	out := []cmdFieldID{cfType, cfCA, cfIOA, cfValue}
	if c.spec().qual != qualNone {
		out = append(out, cfQual, cfMode)
	}
	return out
}

// fields renders the dialog's rows, which the layout sizes and the view draws.
func (c *cmdForm) fields() []formField {
	sp := c.spec()
	out := make([]formField, 0, 6)
	for _, id := range c.visible() {
		switch id {
		case cfType:
			out = append(out, formField{
				label: "Type identification", value: "‹ " + sp.label + " ›",
				hint: "← → to change"})
		case cfCA:
			out = append(out, formField{
				label: "Common address (ASDU)", value: c.ca,
				hint: "the station, 1..65534"})
		case cfIOA:
			out = append(out, formField{
				label: "Information object address", value: c.ioa,
				hint: "the command's own address, not a monitored one"})
		case cfValue:
			out = append(out, formField{
				label: "Value", value: c.valueField(), hint: c.valueHint()})
		case cfQual:
			if sp.qual == qualQOC {
				out = append(out, formField{
					label: "Qualifier of command", value: "‹ " + qocNames[c.qocIdx] + " ›",
					hint: "pulse duration, ← → to change"})
			} else {
				out = append(out, formField{
					label: "Qualifier of set-point", value: c.qos, hint: "QOS, 0..127"})
			}
		case cfMode:
			mode := "direct execute"
			if c.sbo {
				mode = "select, then execute"
			}
			out = append(out, formField{
				label: "Command mode", value: "‹ " + mode + " ›",
				hint: "the S/E bit, ← → to change"})
		}
	}
	return out
}

func (c *cmdForm) valueField() string {
	switch c.spec().value {
	case valOnOff:
		if c.onoff == 1 {
			return "‹ ON ›"
		}
		return "‹ OFF ›"
	case valStep:
		if c.onoff == 1 {
			return "‹ HIGHER ›"
		}
		return "‹ LOWER ›"
	}
	return c.value
}

func (c *cmdForm) valueHint() string {
	switch c.spec().value {
	case valOnOff, valStep:
		return "← → to change"
	case valNormal:
		return "normalised, -1 .. 1"
	case valScaled:
		return "scaled, -32768 .. 32767"
	case valFloat:
		return "short float, e.g. 42.5"
	case valBits:
		return "32 bits, e.g. 0x0000FFFF"
	}
	return ""
}

// isChoice reports whether a field is cycled rather than typed.
func (c *cmdForm) isChoice(id cmdFieldID) bool {
	switch id {
	case cfType, cfMode:
		return true
	case cfValue:
		k := c.spec().value
		return k == valOnOff || k == valStep
	case cfQual:
		return c.spec().qual == qualQOC
	}
	return false
}

// openCommandDialog opens the dialog, keeping whatever was entered last so
// that repeating or adjusting a command is quick.
func (m *Model) openCommandDialog() {
	if !m.cmd.everOpened {
		m.cmd = cmdForm{
			ca:    strconv.Itoa(int(m.conn.lk.CommonAddr)),
			ioa:   "",
			value: "0",
			qos:   "0",
			qocIdx: func() int {
				for i, q := range qocValues {
					if q == m.qoc {
						return i
					}
				}
				return 0
			}(),
			sbo: m.sbo,
		}
		m.cmd.everOpened = true
	}
	m.cmd.active = true
	m.cmd.err = ""
	m.cmd.cursor = 0
	m.modal = modalState{}
}

func (m *Model) handleCommandFormKey(key string) (tea.Model, tea.Cmd) {
	c := &m.cmd
	vis := c.visible()
	if c.cursor >= len(vis) {
		c.cursor = len(vis) - 1
	}
	id := vis[c.cursor]

	switch key {
	case "esc":
		c.active = false
		return m, nil

	case "enter":
		return m.submitCommandForm()

	case "tab", "down":
		c.cursor = (c.cursor + 1) % len(vis)
		return m, nil
	case "shift+tab", "up":
		c.cursor = (c.cursor + len(vis) - 1) % len(vis)
		return m, nil

	case "left", "right":
		delta := 1
		if key == "left" {
			delta = -1
		}
		switch id {
		case cfType:
			c.typeIdx = (c.typeIdx + delta + len(cmdSpecs)) % len(cmdSpecs)
			c.err = ""
		case cfValue:
			if c.isChoice(cfValue) {
				c.onoff ^= 1
			}
		case cfQual:
			if c.spec().qual == qualQOC {
				c.qocIdx = (c.qocIdx + delta + len(qocNames)) % len(qocNames)
			}
		case cfMode:
			c.sbo = !c.sbo
		}
		return m, nil

	case "backspace":
		if !c.isChoice(id) {
			c.editText(id, func(s string) string {
				r := []rune(s)
				if len(r) == 0 {
					return s
				}
				return string(r[:len(r)-1])
			})
		}
		return m, nil

	case "ctrl+u":
		if !c.isChoice(id) {
			c.editText(id, func(string) string { return "" })
		}
		return m, nil
	}

	if len([]rune(key)) == 1 && !c.isChoice(id) {
		c.editText(id, func(s string) string { return s + key })
	}
	return m, nil
}

func (c *cmdForm) editText(id cmdFieldID, f func(string) string) {
	switch id {
	case cfCA:
		c.ca = f(c.ca)
	case cfIOA:
		c.ioa = f(c.ioa)
	case cfValue:
		c.value = f(c.value)
	case cfQual:
		c.qos = f(c.qos)
	}
}

// submitCommandForm validates every field before anything is sent: a
// half-validated command is one nobody can describe afterwards.
func (m *Model) submitCommandForm() (tea.Model, tea.Cmd) {
	c := &m.cmd
	sp := c.spec()

	focus := func(id cmdFieldID, msg string) (tea.Model, tea.Cmd) {
		for i, v := range c.visible() {
			if v == id {
				c.cursor = i
			}
		}
		c.err = msg
		return m, nil
	}

	ca, err := parseUint(c.ca, 16)
	if err != nil {
		return focus(cfCA, "common address: "+err.Error())
	}
	if ca == 0 {
		return focus(cfCA, "common address: 0 is not used")
	}
	ioa, err := parseUint(c.ioa, 24)
	if err != nil {
		return focus(cfIOA, "information object address: "+err.Error())
	}
	if ioa == 0 {
		return focus(cfIOA, "information object address: 0 addresses no object")
	}

	op := commandOp{Spec: sp, CA: uint16(ca), IOA: uint(ioa), Select: c.sbo}

	switch sp.value {
	case valOnOff, valStep:
		op.OnOff = c.onoff == 1
	default:
		parsed, perr := parseValue(sp.value, c.value)
		if perr != nil {
			return focus(cfValue, "value: "+perr.Error())
		}
		op.Norm, op.Scaled, op.Float, op.Bits = parsed.Norm, parsed.Scaled, parsed.Float, parsed.Bits
	}

	switch sp.qual {
	case qualQOC:
		op.Qoc = qocValues[c.qocIdx]
	case qualQOS:
		q, qerr := parseUint(c.qos, 8)
		if qerr != nil || q > 127 {
			return focus(cfQual, "qualifier of set-point: 0..127")
		}
		op.Qos = asdu.QOSQual(q)
	case qualNone:
		op.Select = false // a bit string command has no S/E bit
	}

	c.active = false
	c.err = ""

	if m.confirm {
		m.openCommandConfirm(op)
		return m, nil
	}
	return m.issueCommand(op)
}

// openCommandConfirm asks before anything moves, naming exactly what will be
// sent.
func (m *Model) openCommandConfirm(op commandOp) {
	lines := []string{
		op.describe(),
		"",
		"type    " + typeName(op.Spec.id),
		fmt.Sprintf("address ca=%d  ioa=%d", op.CA, op.IOA),
		"value   " + op.valueText(),
		"mode    " + op.phaseText() + ", " + op.qualText(),
	}
	if op.Select {
		lines = append(lines, "",
			"This sends the SELECT only. The execute follows",
			"once the outstation confirms it.")
	}
	m.modal = modalState{
		kind:    modalConfirm,
		title:   "Send command",
		lines:   lines,
		choices: []modalChoice{{key: "enter", label: "Send", danger: true}, {key: "esc", label: "Cancel"}},
		op:      op,
	}
}

// ---------- lifecycle tracking ----------

type cmdPhase int

const (
	cmdPhaseNone cmdPhase = iota
	cmdPhaseSelectSent
	cmdPhaseSelectConfirmed
	cmdPhaseExecuteSent
	cmdPhaseDone
	cmdPhaseFailed
)

func (p cmdPhase) String() string {
	switch p {
	case cmdPhaseSelectSent:
		return "select sent, waiting for confirmation"
	case cmdPhaseSelectConfirmed:
		return "select confirmed — press enter to execute"
	case cmdPhaseExecuteSent:
		return "execute sent, waiting for confirmation"
	case cmdPhaseDone:
		return "complete"
	case cmdPhaseFailed:
		return "failed"
	}
	return "idle"
}

type cmdEvent struct {
	at    time.Time
	level string
	text  string
}

// cmdTracker follows one command through its confirmations. A command that
// is sent and never spoken of again is the failure mode this exists to make
// visible.
type cmdTracker struct {
	active bool
	op     commandOp
	phase  cmdPhase
	events []cmdEvent
}

func (t *cmdTracker) note(at time.Time, level, text string) {
	t.events = append(t.events, cmdEvent{at: at, level: level, text: text})
	if len(t.events) > 24 {
		t.events = t.events[len(t.events)-24:]
	}
}

// begin starts tracking a freshly sent command, keeping the history when this
// is the execute half of a select-before-execute sequence.
func (m *Model) beginCommand(op commandOp, now time.Time) {
	sameSequence := m.track.active && m.track.op.Spec.id == op.Spec.id &&
		m.track.op.CA == op.CA && m.track.op.IOA == op.IOA && !op.Select

	if !sameSequence {
		m.track = cmdTracker{}
	}
	m.track.active = true
	m.track.op = op
	if op.Select {
		m.track.phase = cmdPhaseSelectSent
	} else {
		m.track.phase = cmdPhaseExecuteSent
	}
	m.track.note(now, "tx", op.phaseText()+" sent: "+op.describe())
}

// applyCommandFeedback matches a mirrored control-direction ASDU to the
// command in flight.
func (m *Model) applyCommandFeedback(fb cmdFeedback, at time.Time) {
	if !m.track.active {
		return
	}
	op := m.track.op
	if fb.Type != op.Spec.id || fb.CA != op.CA || fb.IOA != op.IOA {
		return // a reply to something else, or to a system command
	}

	switch fb.Cause {
	case asdu.ActivationCon:
		if fb.Negative {
			m.track.phase = cmdPhaseFailed
			m.track.note(at, "error", "activation confirmation NEGATIVE — the outstation refused")
			m.toast.show("error", "command refused by the outstation", at)
			return
		}
		switch m.track.phase {
		case cmdPhaseSelectSent:
			m.track.phase = cmdPhaseSelectConfirmed
			m.track.note(at, "ok", "select confirmed — press enter to execute")
			m.toast.show("ok", "select confirmed — enter to execute", at)
		default:
			m.track.note(at, "ok", "activation confirmed")
		}

	case asdu.ActivationTerm:
		m.track.phase = cmdPhaseDone
		m.track.note(at, "ok", "activation terminated — the command is complete")
		m.toast.show("ok", "command complete", at)

	case asdu.DeactivationCon:
		m.track.phase = cmdPhaseFailed
		m.track.note(at, "warn", "deactivation confirmed — the command was cancelled")

	case asdu.UnknownTypeID:
		m.failCommand(at, "the outstation does not know this type identification")
	case asdu.UnknownCOT:
		m.failCommand(at, "the outstation rejected the cause of transmission")
	case asdu.UnknownCA:
		m.failCommand(at, fmt.Sprintf("the outstation does not answer to common address %d", op.CA))
	case asdu.UnknownIOA:
		m.failCommand(at, fmt.Sprintf("the outstation has no command at address %d", op.IOA))
	}
}

func (m *Model) failCommand(at time.Time, why string) {
	m.track.phase = cmdPhaseFailed
	m.track.note(at, "error", why)
	m.toast.show("error", why, at)
}

// noteReturnInfo records the monitored value a command caused to change,
// which is the only proof that plant actually moved.
func (m *Model) noteReturnInfo(key pointKey, value string, at time.Time) {
	if !m.track.active {
		return
	}
	m.track.note(at, "ok", fmt.Sprintf("return information: %s = %s", pointLabel(key), value))
}

// modalContent is what the open dialog holds. The feedback dialog is built
// fresh each frame because it updates while it is on screen, so the layout
// and the renderer must ask the same function rather than a stored copy.
func (m *Model) modalContent() (string, []string, []modalChoice) {
	if m.modal.kind == modalCmdFeedback {
		return "Command", m.feedbackLines(), m.feedbackChoices()
	}
	return m.modal.title, m.modal.lines, m.modal.choices
}

// openCommandFeedback shows the timeline of the command in flight.
func (m *Model) openCommandFeedback() {
	m.modal = modalState{kind: modalCmdFeedback, title: "Command"}
}

// feedbackChoices are the actions the current phase offers.
func (m *Model) feedbackChoices() []modalChoice {
	switch m.track.phase {
	case cmdPhaseSelectConfirmed:
		return []modalChoice{
			{key: "enter", label: "Execute", danger: true},
			{key: "esc", label: "Cancel"},
		}
	case cmdPhaseDone, cmdPhaseFailed:
		return []modalChoice{
			{key: "r", label: "Send another"},
			{key: "esc", label: "Close"},
		}
	}
	return []modalChoice{{key: "esc", label: "Close"}}
}

// handleFeedbackKey drives the feedback dialog.
func (m *Model) handleFeedbackKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "q":
		m.modal = modalState{}
		return m, nil
	case "enter":
		if m.track.phase == cmdPhaseSelectConfirmed {
			op := m.track.op
			op.Select = false // the execute half
			m.modal = modalState{}
			return m.issueCommand(op)
		}
		m.modal = modalState{}
		return m, nil
	case "r", "o":
		// "o" is the key that opens the dialog everywhere else, so it opens
		// it from here too rather than being swallowed by the modal.
		m.modal = modalState{}
		m.openCommandDialog()
		return m, nil
	}
	return m, nil
}

// feedbackLines renders the timeline for the dialog.
func (m *Model) feedbackLines() []string {
	t := m.track
	if !t.active {
		return []string{stMuted.Render("no command has been sent yet")}
	}
	out := []string{
		stBold.Render(t.op.describe()),
		"",
		stMuted.Render("state  ") + phaseStyle(t.phase).Render(t.phase.String()),
		"",
	}
	for _, e := range t.events {
		out = append(out, stMuted.Render(e.at.Format("15:04:05.000"))+" "+
			levelStyle(e.level).Render(e.text))
	}
	return out
}

func phaseStyle(p cmdPhase) lipgloss.Style {
	switch p {
	case cmdPhaseDone, cmdPhaseSelectConfirmed:
		return stGood
	case cmdPhaseFailed:
		return stBad
	}
	return stWarn
}

// lastCommandLines is the Overview panel: what the last command was and how
// it ended, so the answer survives the dialog being closed.
func (m *Model) lastCommandLines() []string {
	if !m.track.active {
		return []string{stMuted.Render("none sent")}
	}
	out := []string{
		field("Command", truncate(m.track.op.describe(), 44)),
		field("State", phaseStyle(m.track.phase).Render(m.track.phase.String())),
	}
	if n := len(m.track.events); n > 0 {
		last := m.track.events[n-1]
		out = append(out, field("Last", stMuted.Render(last.at.Format("15:04:05"))+" "+
			levelStyle(last.level).Render(truncate(last.text, 36))))
	}
	return out
}

// commandSummary is the one-line status the hint bar shows while a command is
// in flight.
func (m *Model) commandSummary() string {
	if !m.track.active {
		return ""
	}
	return strings.TrimSpace(typeName(m.track.op.Spec.id) + " ioa=" +
		strconv.Itoa(int(m.track.op.IOA)) + ": " + m.track.phase.String())
}

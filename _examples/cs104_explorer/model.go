package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/cs104"
)

// tabs
const (
	tabPoints = iota
	tabLog
	tabSend
)

// focus targets
const (
	focusMain = iota
	focusAddr // editing connection (address + common address)
	focusForm // editing the command builder
)

const maxLogLines = 2000

type model struct {
	bridge *bridge
	client *cs104.Client

	addr                       string
	ca                         uint16
	connected, active, verbose bool

	width, height int
	tab           int
	focus         int

	points  map[uint]point
	table   table.Model
	logs    []string
	logView viewport.Model

	// command builder
	formKind             int
	formField            int // 0 kind, 1 IOA, 2 value, 3 mode, 4 qualifier
	formSelect           bool
	inIOA, inVal, inQual textinput.Model

	// connection editor
	connField        int // 0 address, 1 common address
	connAddr, connCA textinput.Model
}

func initialModel(b *bridge) model {
	cols := []table.Column{
		{Title: "IOA", Width: 8},
		{Title: "Type", Width: 12},
		{Title: "Value", Width: 16},
		{Title: "Quality", Width: 14},
		{Title: "Cause", Width: 18},
		{Title: "Time", Width: 12},
		{Title: "Cnt", Width: 5},
	}
	t := table.New(table.WithColumns(cols), table.WithFocused(true), table.WithHeight(10))
	st := table.DefaultStyles()
	st.Header = st.Header.Bold(true).Foreground(lipgloss.Color("205")).BorderBottom(true)
	st.Selected = st.Selected.Foreground(lipgloss.Color("0")).Background(lipgloss.Color("205"))
	t.SetStyles(st)

	mkInput := func(placeholder, val string, limit, width int) textinput.Model {
		in := textinput.New()
		in.Placeholder = placeholder
		if val != "" {
			in.SetValue(val)
		}
		in.CharLimit = limit
		in.Width = width
		return in
	}

	return model{
		bridge:   b,
		addr:     "127.0.0.1:2404",
		ca:       1,
		tab:      tabPoints,
		focus:    focusMain,
		points:   map[uint]point{},
		table:    t,
		logView:  viewport.New(80, 10),
		inIOA:    mkInput("e.g. 6000", "", 10, 20),
		inVal:    mkInput("on / off / number", "", 24, 24),
		inQual:   mkInput("", "0", 3, 6),
		connAddr: mkInput("host:port", "127.0.0.1:2404", 64, 32),
		connCA:   mkInput("", "1", 5, 8),
	}
}

func (m model) Init() tea.Cmd { return textinput.Blink }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		(&m).layout()
		return m, nil
	case logMsg:
		(&m).appendLog(msg.line)
		return m, nil
	case pointsMsg:
		(&m).applyPoints(msg.pts)
		return m, nil
	case statusMsg:
		m.connected, m.active = msg.connected, msg.active
		if msg.note != "" {
			(&m).appendLog("[app] " + msg.note)
		}
		return m, nil
	case errMsg:
		(&m).appendLog("[err] " + msg.err.Error())
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		switch m.focus {
		case focusAddr:
			return m.keyAddr(msg)
		case focusForm:
			return m.keyForm(msg)
		default:
			return m.keyMain(msg)
		}
	}
	return m, nil
}

// --- key handling ---------------------------------------------------------

func (m model) keyMain(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "q":
		return m, tea.Quit
	case "1":
		m.tab = tabPoints
		return m, nil
	case "2":
		m.tab = tabLog
		return m, nil
	case "3":
		m.tab = tabSend
		return m, nil
	case "tab":
		m.tab = (m.tab + 1) % 3
		return m, nil
	case "shift+tab":
		m.tab = (m.tab + 2) % 3
		return m, nil
	case "e":
		m.connAddr.SetValue(m.addr)
		m.connCA.SetValue(strconv.FormatUint(uint64(m.ca), 10))
		m.connField = 0
		m.focus = focusAddr
		(&m).connSyncFocus()
		return m, textinput.Blink
	case "c":
		(&m).connect()
		return m, nil
	case "x":
		(&m).disconnect()
		return m, nil
	case "v":
		m.verbose = !m.verbose
		if m.client != nil {
			m.client.LogMode(m.verbose)
		}
		(&m).appendLog(fmt.Sprintf("[app] protocol logging %v", m.verbose))
		return m, nil
	case "s":
		if m.client != nil {
			m.client.SendStartDt()
			(&m).appendLog("[tx] STARTDT act -> sent")
		} else {
			(&m).appendLog("[app] not connected")
		}
		return m, nil
	case "S":
		if m.client != nil {
			m.client.SendStopDt()
			(&m).appendLog("[tx] STOPDT act -> sent")
		} else {
			(&m).appendLog("[app] not connected")
		}
		return m, nil
	case "g":
		(&m).actGeneralInterrogation()
		return m, nil
	case "C":
		(&m).actCounterInterrogation()
		return m, nil
	case "y":
		(&m).actClockSync()
		return m, nil
	case "t":
		(&m).actTest()
		return m, nil
	case "z":
		(&m).actReset()
		return m, nil
	case "ctrl+l":
		m.logs = nil
		m.logView.SetContent("")
		return m, nil
	case "ctrl+r":
		m.points = map[uint]point{}
		(&m).refreshPointRows()
		return m, nil
	case "i":
		if m.tab == tabSend {
			m.focus = focusForm
			m.formField = 0
			(&m).formSyncFocus()
			return m, textinput.Blink
		}
	}

	// delegate navigation to the focused component
	var cmd tea.Cmd
	switch m.tab {
	case tabPoints:
		m.table, cmd = m.table.Update(k)
	case tabLog:
		m.logView, cmd = m.logView.Update(k)
	}
	return m, cmd
}

func (m model) keyAddr(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.focus = focusMain
		m.connAddr.Blur()
		m.connCA.Blur()
		return m, nil
	case "enter":
		m.addr = strings.TrimSpace(m.connAddr.Value())
		if v, err := strconv.ParseUint(strings.TrimSpace(m.connCA.Value()), 10, 16); err == nil {
			m.ca = uint16(v)
		}
		m.focus = focusMain
		m.connAddr.Blur()
		m.connCA.Blur()
		(&m).appendLog(fmt.Sprintf("[app] target set: %s common addr %d", m.addr, m.ca))
		return m, nil
	case "tab", "down":
		m.connField = (m.connField + 1) % 2
		(&m).connSyncFocus()
		return m, textinput.Blink
	case "shift+tab", "up":
		m.connField = (m.connField + 1) % 2
		(&m).connSyncFocus()
		return m, textinput.Blink
	}
	var cmd tea.Cmd
	if m.connField == 0 {
		m.connAddr, cmd = m.connAddr.Update(k)
	} else {
		m.connCA, cmd = m.connCA.Update(k)
	}
	return m, cmd
}

func (m model) keyForm(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.focus = focusMain
		(&m).blurForm()
		return m, nil
	case "enter":
		(&m).sendForm()
		return m, nil
	case "tab", "down":
		m.formField = (m.formField + 1) % 5
		(&m).formSyncFocus()
		return m, textinput.Blink
	case "shift+tab", "up":
		m.formField = (m.formField + 4) % 5
		(&m).formSyncFocus()
		return m, textinput.Blink
	case "left":
		if m.formField == 0 {
			m.formKind = (m.formKind + len(cmdKinds) - 1) % len(cmdKinds)
			return m, nil
		}
		if m.formField == 3 {
			m.formSelect = !m.formSelect
			return m, nil
		}
	case "right":
		if m.formField == 0 {
			m.formKind = (m.formKind + 1) % len(cmdKinds)
			return m, nil
		}
		if m.formField == 3 {
			m.formSelect = !m.formSelect
			return m, nil
		}
	}
	var cmd tea.Cmd
	switch m.formField {
	case 1:
		m.inIOA, cmd = m.inIOA.Update(k)
	case 2:
		m.inVal, cmd = m.inVal.Update(k)
	case 4:
		m.inQual, cmd = m.inQual.Update(k)
	}
	return m, cmd
}

// --- client lifecycle ------------------------------------------------------

func (m *model) connect() {
	if m.client != nil {
		m.disconnect()
	}
	opt := cs104.NewOption()
	if err := opt.AddRemoteServer(m.addr); err != nil {
		m.appendLog("[err] bad server address: " + err.Error())
		return
	}
	opt.SetAutoReconnect(false)

	cli := cs104.NewClient(&tuiHandler{b: m.bridge}, opt)
	cli.SetLogProvider(&logProvider{b: m.bridge})
	cli.LogMode(m.verbose)

	b := m.bridge
	cli.SetOnConnectHandler(func(c *cs104.Client) {
		b.send(statusMsg{connected: true, note: "TCP connected, sending STARTDT"})
		c.SendStartDt()
	})
	cli.SetOnActivatedHandler(func(*cs104.Client) {
		b.send(statusMsg{connected: true, active: true, note: "data transfer active (STARTDT confirmed)"})
	})
	cli.SetOnDeactivatedHandler(func(*cs104.Client) {
		b.send(statusMsg{connected: true, active: false, note: "data transfer stopped (STOPDT confirmed)"})
	})
	cli.SetConnectionLostHandler(func(*cs104.Client) {
		b.send(statusMsg{connected: false, active: false, note: "connection lost"})
	})
	cli.SetConnectTimeoutHandler(func(*cs104.Client) {
		b.send(logMsg{"[app] connect attempt timed out"})
	})

	m.client = cli
	m.appendLog("[app] connecting to " + m.addr)
	if err := cli.Start(); err != nil {
		m.appendLog("[err] start failed: " + err.Error())
		m.client = nil
	}
}

func (m *model) disconnect() {
	if m.client == nil {
		return
	}
	_ = m.client.Close()
	m.client = nil
	m.connected, m.active = false, false
	m.appendLog("[app] disconnected")
}

// --- request actions -------------------------------------------------------

func (m *model) requireActive() bool {
	if m.client == nil || !m.active {
		m.appendLog("[app] not connected/active — press 'c' to connect first")
		return false
	}
	return true
}

func (m *model) logAct(name string, err error) {
	if err != nil {
		m.appendLog("[tx] " + name + " failed: " + err.Error())
	} else {
		m.appendLog("[tx] " + name + " -> sent")
	}
}

func (m *model) actGeneralInterrogation() {
	if !m.requireActive() {
		return
	}
	err := m.client.InterrogationCmd(asdu.CauseOfTransmission{Cause: asdu.Activation}, asdu.CommonAddr(m.ca), asdu.QOIStation)
	m.logAct("general interrogation", err)
}

func (m *model) actCounterInterrogation() {
	if !m.requireActive() {
		return
	}
	err := m.client.CounterInterrogationCmd(asdu.CauseOfTransmission{Cause: asdu.Activation}, asdu.CommonAddr(m.ca),
		asdu.QualifierCountCall{Request: asdu.QCCTotal, Freeze: asdu.QCCFrzRead})
	m.logAct("counter interrogation", err)
}

func (m *model) actClockSync() {
	if !m.requireActive() {
		return
	}
	err := m.client.ClockSynchronizationCmd(asdu.CauseOfTransmission{Cause: asdu.Activation}, asdu.CommonAddr(m.ca), time.Now())
	m.logAct("clock synchronization", err)
}

func (m *model) actTest() {
	if !m.requireActive() {
		return
	}
	err := m.client.TestCommand(asdu.CauseOfTransmission{Cause: asdu.Activation}, asdu.CommonAddr(m.ca))
	m.logAct("test command", err)
}

func (m *model) actReset() {
	if !m.requireActive() {
		return
	}
	err := m.client.ResetProcessCmd(asdu.CauseOfTransmission{Cause: asdu.Activation}, asdu.CommonAddr(m.ca), asdu.QPRGeneralRest)
	m.logAct("reset process", err)
}

// --- state helpers ---------------------------------------------------------

func (m *model) appendLog(line string) {
	m.logs = append(m.logs, time.Now().Format("15:04:05.000")+" "+line)
	if len(m.logs) > maxLogLines {
		m.logs = m.logs[len(m.logs)-maxLogLines:]
	}
	m.logView.SetContent(strings.Join(m.logs, "\n"))
	m.logView.GotoBottom()
}

func (m *model) applyPoints(pts []point) {
	for _, p := range pts {
		if old, ok := m.points[p.IOA]; ok {
			p.Count = old.Count + 1
		} else {
			p.Count = 1
		}
		m.points[p.IOA] = p
	}
	m.refreshPointRows()
}

func (m *model) refreshPointRows() {
	keys := make([]uint, 0, len(m.points))
	for k := range m.points {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	rows := make([]table.Row, 0, len(keys))
	for _, k := range keys {
		p := m.points[k]
		rows = append(rows, table.Row{
			strconv.FormatUint(uint64(p.IOA), 10), p.Type, p.Value, p.Quality, p.Cause, p.Time,
			strconv.Itoa(p.Count),
		})
	}
	m.table.SetRows(rows)
}

func (m *model) connSyncFocus() {
	m.connAddr.Blur()
	m.connCA.Blur()
	if m.connField == 0 {
		m.connAddr.Focus()
	} else {
		m.connCA.Focus()
	}
}

func (m *model) formSyncFocus() {
	m.blurForm()
	switch m.formField {
	case 1:
		m.inIOA.Focus()
	case 2:
		m.inVal.Focus()
	case 4:
		m.inQual.Focus()
	}
}

func (m *model) blurForm() {
	m.inIOA.Blur()
	m.inVal.Blur()
	m.inQual.Blur()
}

func (m *model) layout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	inner := m.width - 2
	if inner < 20 {
		inner = 20
	}
	bodyH := m.height - 6
	if bodyH < 3 {
		bodyH = 3
	}
	m.table.SetWidth(inner)
	m.table.SetHeight(bodyH - 1)
	m.logView.Width = inner
	m.logView.Height = bodyH
	m.logView.SetContent(strings.Join(m.logs, "\n"))
	m.logView.GotoBottom()
}

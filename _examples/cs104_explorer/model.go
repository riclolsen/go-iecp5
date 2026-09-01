package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/riclolsen/go-iecp5/asdu"
)

// Screen is one of the tabs.
type Screen int

const (
	ScreenOverview Screen = iota
	ScreenPoints
	ScreenEvents
	ScreenLog
	ScreenFiles
	ScreenHelp
	numScreens
)

var screenNames = [numScreens]string{"Overview", "Points", "Events", "Log", "Files", "Help"}

func (s Screen) isTable() bool {
	return s == ScreenPoints || s == ScreenEvents || s == ScreenLog || s == ScreenFiles
}

// follows marks the screens where new rows arrive at the bottom and the
// operator usually wants to watch the end of the list.
func (s Screen) follows() bool { return s == ScreenEvents || s == ScreenLog }

func (s Screen) scrolls() bool { return s.isTable() || s == ScreenHelp }

func (s Screen) String() string {
	if s < 0 || s >= numScreens {
		return "?"
	}
	return screenNames[s]
}

// pointKey identifies one information object: the station it came from and
// its information object address.
type pointKey struct {
	CA  uint16
	IOA uint
}

const histCap = 120

// pointState is the current value of one information object plus enough
// history to draw a trend.
type pointState struct {
	Key    pointKey
	Type   asdu.TypeID
	Cause  asdu.Cause
	Value  string
	Num    float64
	HasNum bool
	Qds    asdu.QualityDescriptor
	HasQds bool
	IsQdp  bool
	// Stamp is the device's own time tag, zero when the type carries none.
	Stamp   time.Time
	Updated time.Time
	Updates int
	Hist    []float64
}

func (p *pointState) stale(now time.Time, limit time.Duration) bool {
	return limit > 0 && now.Sub(p.Updated) > limit
}

// eventRow is one arrival, kept in order. What changed and when, as distinct
// from what the value is now.
type eventRow struct {
	At    time.Time
	Key   pointKey
	Type  asdu.TypeID
	Cause asdu.Cause
	Value string
	Qds   asdu.QualityDescriptor
	HasQ  bool
	Stamp time.Time
}

type logRow struct {
	At    time.Time
	Level string
	Text  string
}

const (
	maxEvents = 5000
	maxLogs   = 5000
)

// Model is the whole interface state.
type Model struct {
	width, height int
	screen        Screen

	conn *connection

	status    string
	lastErr   string
	connected bool
	active    bool // STARTDT confirmed: data transfer running
	startedAt time.Time
	linkSince time.Time
	now       time.Time

	// points is keyed for update and pointsOrder keeps a stable arrival
	// order, so the table does not reshuffle under the cursor every time a
	// value arrives. Display order is derived from it by visiblePoints.
	points      map[pointKey]*pointState
	pointsOrder []pointKey

	events []eventRow
	logs   []logRow
	files  filesState

	// One cursor and scroll offset per screen, so moving between tabs does
	// not lose the operator's place in a list they were reading.
	cursor [numScreens]int
	offset [numScreens]int

	follow bool
	detail bool
	filter string

	sortBy   sortKey
	sortDesc bool

	prompt promptState
	modal  modalState
	form   formState
	cmd    cmdForm
	track  cmdTracker
	toast  toastState

	// sbo selects between select-before-execute and direct execute, and
	// confirm decides whether a command asks first. Both are on screen — sbo
	// as a toolbar button, confirm as a standing warning when it is off —
	// because an operator must never have to remember which mode a command
	// tool is in.
	sbo      bool
	confirm  bool
	qoc      asdu.QOCQual
	mouse    bool
	altmode  bool
	staleAge time.Duration

	hover    zone
	dragging bool

	// Counters behind the Overview screen.
	rxASDU  uint64
	txASDU  uint64
	rxItems uint64
	cmdSent uint64
	cmdOK   uint64
	cmdFail uint64

	// rate counts ASDUs in 500ms buckets over the last ten seconds, and
	// rateHist keeps the resulting number for a minute: a device that has
	// gone quiet looks exactly like a healthy idle one until you can see
	// that it used to be busy.
	rate     [20]int
	rateIdx  int
	rateHist []float64

	quitting bool
}

// NewModel builds the initial model.
func NewModel(conn *connection) *Model {
	return &Model{
		screen:    ScreenOverview,
		conn:      conn,
		files:     newFilesState(),
		points:    map[pointKey]*pointState{},
		follow:    true,
		status:    "connecting",
		sortBy:    sortPoint,
		confirm:   true,
		sbo:       true,
		qoc:       asdu.QOCShortPulseDuration,
		mouse:     true,
		altmode:   true,
		staleAge:  30 * time.Second,
		startedAt: time.Now(),
		now:       time.Now(),
	}
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.conn.wait(), tick())
}

// tickMsg drives the age column, the clock and the rate meter without needing
// a repaint on every protocol event.
type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) clock() time.Time { return m.now }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.HandleKey(msg.String())

	case tea.MouseMsg:
		return m.HandleMouse(fromTeaMouse(msg))

	case tickMsg:
		m.now = time.Time(msg)
		m.toast.expire(m.now)
		// Roll the rate window forward one bucket.
		m.rateIdx = (m.rateIdx + 1) % len(m.rate)
		m.rate[m.rateIdx] = 0
		m.rateHist = append(m.rateHist, m.eventRate())
		if len(m.rateHist) > histCap {
			m.rateHist = m.rateHist[len(m.rateHist)-histCap:]
		}
		return m, tick()

	case statusMsg:
		m.applyStatus(msg)
		return m, m.conn.wait()

	case updateMsg:
		m.applyUpdate(msg)
		return m, m.conn.wait()

	case logLineMsg:
		m.addLog(msg.level, msg.text)
		return m, m.conn.wait()

	case directoryMsg:
		m.files.applyDirectory(msg.entries)
		m.addLog("ok", fmt.Sprintf("file directory: %d file(s)", len(msg.entries)))
		return m, m.conn.wait()

	case fileDoneMsg:
		m.files.applyDone(msg)
		if msg.err != nil {
			m.addLog("error", "file transfer: "+msg.err.Error())
			m.toast.show("error", "file transfer failed", m.now)
		} else {
			m.addLog("ok", fmt.Sprintf("file ioa=%d %s: %d octets saved to %s",
				msg.ioa, nofName(msg.nof), msg.size, msg.path))
			m.toast.show("ok", "saved "+msg.path, m.now)
		}
		return m, m.conn.wait()

	case commandResultMsg:
		level := "error"
		if msg.ok {
			level = "ok"
			m.cmdOK++
		} else {
			m.cmdFail++
			m.lastErr = msg.text
		}
		m.addLog(level, msg.text)
		m.toast.show(level, msg.text, m.now)
		return m, nil
	}
	return m, nil
}

func (m *Model) applyStatus(msg statusMsg) {
	switch {
	case msg.connected && !m.connected:
		m.linkSince = time.Now()
		m.addLog("ok", "connected to "+m.conn.target())
	case !msg.connected && m.connected:
		m.linkSince = time.Time{}
		m.addLog("warn", "connection lost")
	}
	m.connected, m.active = msg.connected, msg.active
	switch {
	case !msg.connected:
		m.status = "disconnected"
	case msg.active:
		m.status = "active"
	default:
		m.status = "connected (STOPDT)"
	}
	if msg.note != "" {
		m.addLog("info", msg.note)
	}
}

// HandleKey is the single implementation of every action. The mouse resolves
// clicks to key names and calls this, so the two input methods cannot drift.
func (m *Model) HandleKey(key string) (tea.Model, tea.Cmd) {
	// A prompt or a dialog owns the keyboard while it is open. Commands are
	// issued from this interface, so a keystroke must never fall through to a
	// breaker while the operator believes they are typing.
	if m.prompt.active {
		return m.handlePromptKey(key)
	}
	if m.form.active {
		return m.handleFormKey(key)
	}
	if m.cmd.active {
		return m.handleCommandFormKey(key)
	}
	if m.modal.kind != modalNone {
		return m.handleModalKey(key)
	}

	// The Files screen claims a few keys before the global bindings see them.
	if m.screen == ScreenFiles {
		if model, cmd, handled := m.handleFilesKey(key); handled {
			return model, cmd
		}
	}

	switch key {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit

	case "esc":
		switch {
		case m.filter != "":
			m.filter = ""
			m.toast.show("info", "filter cleared", m.now)
		case m.detail:
			m.detail = false
		}

	// ---- navigation ----
	case "tab", "right":
		return m, m.setScreen((m.screen + 1) % numScreens)
	case "shift+tab", "left":
		return m, m.setScreen((m.screen + numScreens - 1) % numScreens)
	case "1", "2", "3", "4", "5", "6":
		return m, m.setScreen(Screen(key[0] - '1'))
	case "?":
		return m, m.setScreen(ScreenHelp)

	case "up", "k":
		m.moveCursor(-1)
	case "down", "j":
		m.moveCursor(1)
	case "pgup", "ctrl+b":
		m.moveCursor(-m.pageSize())
	case "pgdown", "ctrl+f":
		m.moveCursor(m.pageSize())
	case "home", "g":
		m.jumpTo(0)
	case "end", "G":
		m.jumpTo(m.rowCount() - 1)

	// ---- view ----
	case "/":
		m.prompt = promptState{active: true, kind: promptFilter,
			label: "filter", input: m.filter}
		if m.screen == ScreenOverview || m.screen == ScreenHelp {
			m.setScreen(ScreenPoints)
		}
	case "f":
		m.follow = !m.follow
		m.toast.show("info", "follow "+onOff(m.follow), m.now)
	case "d", "enter", " ":
		return m.contextAction(key)
	case "r":
		m.sortDesc = !m.sortDesc
	case "<":
		m.cycleSort(-1)
	case ">":
		m.cycleSort(1)
	case "x":
		m.clearList()
	case "e":
		return m, m.export()
	case "v":
		m.conn.setVerbose(!m.conn.verbose())
		m.toast.show("info", "protocol log "+onOff(m.conn.verbose()), m.now)

	// ---- link ----
	case "a":
		m.addLog("tx", "STARTDT act")
		return m, m.conn.startDT()
	case "A":
		m.addLog("tx", "STOPDT act")
		return m, m.conn.stopDT()
	case "C":
		m.openConnectionForm()

	// ---- interrogation and system commands ----
	case "i":
		m.txASDU++
		m.addLog("tx", "general interrogation (C_IC_NA_1)")
		return m, m.conn.interrogation(m.qoiGroup())
	case "p":
		m.txASDU++
		m.addLog("tx", "counter interrogation (C_CI_NA_1)")
		return m, m.conn.counterInterrogation()
	case "t":
		m.txASDU++
		m.addLog("tx", "clock synchronisation (C_CS_NA_1)")
		return m, m.conn.clockSync()
	case "T":
		m.txASDU++
		m.addLog("tx", "test command (C_TS_NA_1)")
		return m, m.conn.testCommand()
	case "R":
		m.openResetDialog()
	case "s":
		m.prompt = promptState{active: true, kind: promptRead,
			label: "read command  information object address", input: ""}

	// ---- process commands ----
	case "o":
		m.openCommandDialog()
	case "O":
		// The feedback for whatever was last sent, whether or not the
		// dialog that sent it is still open.
		m.openCommandFeedback()
	case "E":
		m.sbo = !m.sbo
		m.toast.show("info", "commands: "+commandMode(m.sbo), m.now)
	}
	return m, nil
}

// setScreen switches tabs and returns whatever the new screen needs on
// arrival. The Files screen is the only one with anything to fetch.
func (m *Model) setScreen(s Screen) tea.Cmd {
	if s < 0 || s >= numScreens {
		return nil
	}
	m.screen = s
	if s == ScreenFiles && !m.files.listed && m.active {
		return m.conn.fileDirectory()
	}
	return nil
}

func (m *Model) pageSize() int {
	return max(m.height-chromeTop-chromeBottom-2, 1)
}

func (m *Model) moveCursor(delta int) {
	if m.follow && m.screen.follows() && delta != 0 {
		m.follow = false
	}
	m.cursor[m.screen] += delta
	m.clampScroll(m.rowCount(), m.visibleRows())
}

func (m *Model) jumpTo(row int) {
	if m.follow && m.screen.follows() {
		m.follow = false
	}
	m.cursor[m.screen] = row
	m.clampScroll(m.rowCount(), m.visibleRows())
}

func (m *Model) scroll(delta int) {
	if m.follow && m.screen.follows() {
		m.follow = false
	}
	m.offset[m.screen] += delta
	total, vis := m.rowCount(), m.visibleRows()
	m.offset[m.screen] = min(max(m.offset[m.screen], 0), max(total-vis, 0))
	cur := m.cursor[m.screen]
	m.cursor[m.screen] = min(max(cur, m.offset[m.screen]), m.offset[m.screen]+max(vis-1, 0))
	m.clampScroll(total, vis)
}

// visibleRows is how many data rows the body can draw, without running the
// full layout (which would recurse through clampScroll).
func (m *Model) visibleRows() int {
	h := m.height - chromeTop - chromeBottom
	if m.screen.isTable() {
		h-- // the column header
	}
	return max(h, 0)
}

var sortOrder = []sortKey{sortPoint, sortType, sortValue, sortQuality, sortAge, sortTime}

func (m *Model) cycleSort(dir int) {
	at := 0
	for i, k := range sortOrder {
		if k == m.sortBy {
			at = i
			break
		}
	}
	m.sortBy = sortOrder[(at+dir+len(sortOrder))%len(sortOrder)]
	m.cursor[m.screen], m.offset[m.screen] = 0, 0
	m.toast.show("info", "sort by "+sortName(m.sortBy), m.now)
}

func (m *Model) clearList() {
	switch m.screen {
	case ScreenPoints, ScreenOverview:
		m.points = map[pointKey]*pointState{}
		m.pointsOrder = nil
		m.toast.show("info", "point table cleared", m.now)
	case ScreenEvents:
		m.events = nil
		m.toast.show("info", "events cleared", m.now)
	case ScreenLog:
		m.logs = nil
		m.toast.show("info", "log cleared", m.now)
	case ScreenFiles:
		m.files.clear()
		m.toast.show("info", "file list cleared", m.now)
	}
	m.cursor[m.screen], m.offset[m.screen] = 0, 0
}

// contextAction is what enter, space and d do on the current row.
func (m *Model) contextAction(key string) (tea.Model, tea.Cmd) {
	if key == "d" {
		if m.screen == ScreenPoints {
			m.detail = !m.detail
		}
		return m, nil
	}
	switch m.screen {
	case ScreenPoints:
		// A command is not derived from the selected row: in 104 its address
		// space is its own, so every parameter is entered in the dialog.
		m.openCommandDialog()
	case ScreenFiles:
		return m.fetchSelectedFile()
	}
	return m, nil
}

func (m *Model) issueCommand(op commandOp) (tea.Model, tea.Cmd) {
	m.cmdSent++
	m.txASDU++
	m.addLog("tx", op.describe())
	m.beginCommand(op, m.now)
	m.openCommandFeedback()
	return m, m.conn.sendCommand(op)
}

func (m *Model) selectedPoint() (*pointState, bool) {
	rows := m.visiblePoints()
	i := m.cursor[ScreenPoints]
	if i < 0 || i >= len(rows) {
		return nil, false
	}
	return rows[i], true
}

// ---------- data in ----------

func (m *Model) applyUpdate(u updateMsg) {
	m.rxASDU++
	m.rate[m.rateIdx]++
	m.rxItems += uint64(len(u.rows))

	if u.summary != "" {
		m.addLog("rx", u.summary)
	}
	if u.feedback != nil {
		m.applyCommandFeedback(*u.feedback, u.at)
	}

	for _, r := range u.rows {
		p, ok := m.points[r.Key]
		if !ok {
			p = &pointState{Key: r.Key}
			m.points[r.Key] = p
			m.pointsOrder = append(m.pointsOrder, r.Key)
		}
		p.Type, p.Cause = r.Type, r.Cause
		p.Value = r.Value
		p.Qds, p.HasQds, p.IsQdp = r.Qds, r.HasQds, r.IsQdp
		p.Stamp = r.Stamp
		p.Updated = u.at
		p.Updates++
		if r.HasNum {
			p.Num, p.HasNum = r.Num, true
			p.Hist = append(p.Hist, r.Num)
			if len(p.Hist) > histCap {
				p.Hist = p.Hist[len(p.Hist)-histCap:]
			}
		}

		if r.Cause == asdu.ReturnInfoRemote || r.Cause == asdu.ReturnInfoLocal {
			m.noteReturnInfo(r.Key, r.Value, u.at)
		}

		m.events = append(m.events, eventRow{
			At: u.at, Key: r.Key, Type: r.Type, Cause: r.Cause,
			Value: r.Value, Qds: r.Qds, HasQ: r.HasQds, Stamp: r.Stamp,
		})
	}
	if len(m.events) > maxEvents {
		m.events = m.events[len(m.events)-maxEvents:]
	}
}

func (m *Model) addLog(level, text string) {
	at := m.now
	if at.IsZero() {
		at = time.Now()
	}
	m.logs = append(m.logs, logRow{At: at, Level: level, Text: text})
	if len(m.logs) > maxLogs {
		m.logs = m.logs[len(m.logs)-maxLogs:]
	}
}

// eventRate is ASDUs per second over the last ten seconds.
func (m *Model) eventRate() float64 {
	total := 0
	for _, n := range m.rate {
		total += n
	}
	return float64(total) / (float64(len(m.rate)) * 0.5)
}

// ---------- filtering and sorting ----------

func matchesFilter(filter string, fields ...string) bool {
	if filter == "" {
		return true
	}
	needle := strings.ToLower(filter)
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), needle) {
			return true
		}
	}
	return false
}

func (m *Model) visiblePoints() []*pointState {
	out := make([]*pointState, 0, len(m.pointsOrder))
	for _, k := range m.pointsOrder {
		p := m.points[k]
		if p == nil {
			continue
		}
		if !matchesFilter(m.filter, pointLabel(p.Key), typeName(p.Type),
			p.Value, qualityTextKind(p.Qds, p.HasQds, p.IsQdp), causeName(p.Cause)) {
			continue
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if m.sortDesc {
			return pointLess(out[j], out[i], m.sortBy)
		}
		return pointLess(out[i], out[j], m.sortBy)
	})
	return out
}

func pointLess(a, b *pointState, key sortKey) bool {
	switch key {
	case sortType:
		if a.Type != b.Type {
			return a.Type < b.Type
		}
	case sortValue:
		switch {
		case a.HasNum && b.HasNum && a.Num != b.Num:
			return a.Num < b.Num
		case a.Value != b.Value:
			return a.Value < b.Value
		}
	case sortQuality:
		// Worst first: this is how you find the broken points in a device
		// with a thousand good ones.
		if qa, qb := qualityRank(a), qualityRank(b); qa != qb {
			return qa > qb
		}
	case sortAge:
		if !a.Updated.Equal(b.Updated) {
			return a.Updated.After(b.Updated)
		}
	case sortTime:
		if !a.Stamp.Equal(b.Stamp) {
			return a.Stamp.After(b.Stamp)
		}
	}
	if a.Key.CA != b.Key.CA {
		return a.Key.CA < b.Key.CA
	}
	return a.Key.IOA < b.Key.IOA
}

// qualityRank scores a point's quality so the worst sorts first.
func qualityRank(p *pointState) int {
	if !p.HasQds {
		return 0
	}
	n := 0
	q := p.Qds
	if q&asdu.QDSInvalid != 0 {
		n += 16
	}
	if q&asdu.QDSNotTopical != 0 {
		n += 8
	}
	if q&asdu.QDSSubstituted != 0 {
		n += 4
	}
	if q&asdu.QDSBlocked != 0 {
		n += 2
	}
	if q&asdu.QDSOverflow != 0 {
		n++
	}
	if p.IsQdp && q&asdu.QualityDescriptor(asdu.QDPElapsedTimeInvalid) != 0 {
		n++
	}
	return n
}

func (m *Model) visibleEvents() []eventRow {
	if m.filter == "" {
		return m.events
	}
	out := make([]eventRow, 0, len(m.events))
	for _, e := range m.events {
		if matchesFilter(m.filter, pointLabel(e.Key), typeName(e.Type), e.Value,
			causeName(e.Cause), qualityText(e.Qds, e.HasQ)) {
			out = append(out, e)
		}
	}
	return out
}

func (m *Model) visibleLogs() []logRow {
	if m.filter == "" {
		return m.logs
	}
	out := make([]logRow, 0, len(m.logs))
	for _, l := range m.logs {
		if matchesFilter(m.filter, l.Level, l.Text) {
			out = append(out, l)
		}
	}
	return out
}

func (m *Model) rowCount() int {
	switch m.screen {
	case ScreenPoints:
		return len(m.visiblePoints())
	case ScreenEvents:
		return len(m.visibleEvents())
	case ScreenLog:
		return len(m.visibleLogs())
	case ScreenFiles:
		return m.files.rowCount()
	case ScreenHelp:
		return len(helpLines())
	}
	return 0
}

func (m *Model) rowNoun() string {
	switch m.screen {
	case ScreenPoints:
		return "point"
	case ScreenEvents:
		return "event"
	case ScreenLog:
		return "line"
	case ScreenFiles:
		return "file"
	}
	return "row"
}

// ---------- prompt ----------

type promptKind int

const (
	promptFilter promptKind = iota
	promptRead
)

type promptState struct {
	active bool
	kind   promptKind
	label  string
	input  string
}

func (m *Model) closePrompt() { m.prompt = promptState{} }

func (m *Model) handlePromptKey(key string) (tea.Model, tea.Cmd) {
	p := m.prompt
	switch key {
	case "esc":
		m.closePrompt()
		if p.kind == promptFilter {
			m.filter = ""
		}
		return m, nil
	case "enter":
		m.closePrompt()
		return m.submitPrompt(p)
	case "backspace":
		if len(p.input) > 0 {
			r := []rune(p.input)
			m.prompt.input = string(r[:len(r)-1])
		}
	case "ctrl+u":
		m.prompt.input = ""
	case " ", "space":
		m.prompt.input += " "
	default:
		if len([]rune(key)) == 1 {
			m.prompt.input += key
		}
	}
	// A filter applies as it is typed: the list is the feedback.
	if m.prompt.kind == promptFilter {
		m.filter = m.prompt.input
		m.cursor[m.screen], m.offset[m.screen] = 0, 0
	}
	return m, nil
}

func (m *Model) submitPrompt(p promptState) (tea.Model, tea.Cmd) {
	switch p.kind {
	case promptFilter:
		m.filter = strings.TrimSpace(p.input)
		m.cursor[m.screen], m.offset[m.screen] = 0, 0

	case promptRead:
		ioa, err := parseUint(p.input, 24)
		if err != nil {
			m.toast.show("error", "read: "+err.Error(), m.now)
			return m, nil
		}
		m.txASDU++
		m.addLog("tx", fmt.Sprintf("read command (C_RD_NA_1) ioa=%d", ioa))
		return m, m.conn.readCommand(asdu.InfoObjAddr(ioa))

	}
	return m, nil
}

// ---------- modal ----------

type modalKind int

const (
	modalNone modalKind = iota
	modalConfirm
	modalReset
	modalCmdFeedback
)

type modalChoice struct {
	key   string
	label string
	// danger marks the choice that actually does something.
	danger bool
}

type modalState struct {
	kind    modalKind
	title   string
	lines   []string
	choices []modalChoice
	op      commandOp
}

func (m *Model) openResetDialog() {
	m.modal = modalState{
		kind:  modalReset,
		title: "Reset process",
		lines: []string{
			"C_RP_NA_1 resets the outstation's process.",
			"It is not a communications reset.",
		},
		choices: []modalChoice{
			{key: "enter", label: "Send reset", danger: true},
			{key: "esc", label: "Cancel"},
		},
	}
}

func (m *Model) handleModalKey(key string) (tea.Model, tea.Cmd) {
	d := m.modal
	if d.kind == modalCmdFeedback {
		return m.handleFeedbackKey(key)
	}
	switch key {
	case "esc", "q":
		m.modal = modalState{}
		return m, nil
	}

	switch d.kind {
	case modalReset:
		if key == "enter" {
			m.modal = modalState{}
			m.txASDU++
			m.addLog("tx", "reset process (C_RP_NA_1)")
			return m, m.conn.resetProcess()
		}
	case modalConfirm:
		if key == "enter" {
			m.modal = modalState{}
			return m.issueCommand(d.op)
		}
	}
	return m, nil
}

// ---------- toast ----------

type toastState struct {
	level string
	text  string
	until time.Time
}

func (t *toastState) show(level, text string, now time.Time) {
	t.level, t.text = level, text
	t.until = now.Add(4 * time.Second)
}

func (t *toastState) expire(now time.Time) {
	if !t.until.IsZero() && now.After(t.until) {
		t.text = ""
	}
}

func (t toastState) active() bool { return t.text != "" }

// ---------- naming ----------

func pointLabel(k pointKey) string {
	return fmt.Sprintf("%d:%d", k.CA, k.IOA)
}

// typeName renders an ASDU type identification without the TID<> wrapper.
func typeName(t asdu.TypeID) string {
	s := t.String()
	s = strings.TrimPrefix(s, "TID<")
	return strings.TrimSuffix(s, ">")
}

func causeName(c asdu.Cause) string {
	s := asdu.CauseOfTransmission{Cause: c}.String()
	s = strings.TrimPrefix(s, "COT<")
	return strings.TrimSuffix(s, ">")
}

// qualityText names the set quality bits, or "GOOD" when none are.
//
// A protection equipment descriptor is a different set of flags in the same
// octet, so it is named as one rather than being read as a measured value's.
func qualityText(q asdu.QualityDescriptor, has bool) string {
	return qualityTextKind(q, has, false)
}

func qualityTextKind(q asdu.QualityDescriptor, has, isQdp bool) string {
	if !has {
		return "—"
	}
	if q == 0 {
		return "GOOD"
	}
	if isQdp {
		var parts []string
		if q&asdu.QualityDescriptor(asdu.QDPElapsedTimeInvalid) != 0 {
			parts = append(parts, "EI")
		}
		if q&asdu.QualityDescriptor(asdu.QDPBlocked) != 0 {
			parts = append(parts, "BL")
		}
		if q&asdu.QualityDescriptor(asdu.QDPSubstituted) != 0 {
			parts = append(parts, "SB")
		}
		if q&asdu.QualityDescriptor(asdu.QDPNotTopical) != 0 {
			parts = append(parts, "NT")
		}
		if q&asdu.QualityDescriptor(asdu.QDPInvalid) != 0 {
			parts = append(parts, "IV")
		}
		if len(parts) == 0 {
			return fmt.Sprintf("0x%02x", byte(q))
		}
		return strings.Join(parts, "|")
	}
	var parts []string
	if q&asdu.QDSOverflow != 0 {
		parts = append(parts, "OV")
	}
	if q&asdu.QDSBlocked != 0 {
		parts = append(parts, "BL")
	}
	if q&asdu.QDSSubstituted != 0 {
		parts = append(parts, "SB")
	}
	if q&asdu.QDSNotTopical != 0 {
		parts = append(parts, "NT")
	}
	if q&asdu.QDSInvalid != 0 {
		parts = append(parts, "IV")
	}
	if len(parts) == 0 {
		return fmt.Sprintf("0x%02x", byte(q))
	}
	return strings.Join(parts, "|")
}

func sortName(k sortKey) string {
	switch k {
	case sortPoint:
		return "address"
	case sortType:
		return "type"
	case sortValue:
		return "value"
	case sortQuality:
		return "quality (worst first)"
	case sortAge:
		return "age"
	case sortTime:
		return "timestamp"
	}
	return "none"
}

func commandMode(sbo bool) string {
	if sbo {
		return "select before execute"
	}
	return "direct execute"
}

func qocName(q asdu.QOCQual) string {
	switch q {
	case asdu.QOCNoAdditionalDefinition:
		return "no pulse definition"
	case asdu.QOCShortPulseDuration:
		return "short pulse"
	case asdu.QOCLongPulseDuration:
		return "long pulse"
	case asdu.QOCPersistentOutput:
		return "persistent"
	}
	return fmt.Sprintf("qoc %d", byte(q))
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// qoiGroup is the interrogation qualifier the toolbar sends: station
// interrogation, which is what an operator means by "read everything".
func (m *Model) qoiGroup() asdu.QualifierOfInterrogation { return asdu.QOIStation }

func fmtDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	h := int(d.Hours())
	mnt := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, mnt, s)
	}
	return fmt.Sprintf("%d:%02d", mnt, s)
}

func fmtAge(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

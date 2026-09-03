package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/riclolsen/go-iecp5/asdu"
)

func (m *Model) View() string {
	if m.quitting {
		return ""
	}
	if m.width == 0 {
		return "starting…"
	}

	l := m.layout()
	if !l.ok {
		// A terminal too small to lay out honestly gets told so, rather than
		// a mangled table that looks like corrupted data.
		return fmt.Sprintf("terminal is %dx%d; this needs at least %dx%d",
			m.width, m.height, minWidth, minHeight)
	}

	lines := make([]string, 0, m.height)
	lines = append(lines, m.viewHeader(l))
	lines = append(lines, m.viewTabs(l))
	lines = append(lines, stMuted.Render(repeat("─", m.width)))
	lines = append(lines, m.viewBody(l)...)
	lines = append(lines, stMuted.Render(repeat("─", m.width)))
	lines = append(lines, m.viewToolbar(l))
	lines = append(lines, m.viewHint(l))

	return strings.Join(lines, "\n")
}

// ---------- frame ----------

func (m *Model) viewHeader(l layout) string {
	var state string
	switch {
	case m.active:
		state = stGood.Render("● active")
	case m.connected:
		state = stWarn.Render("◐ connected (STOPDT)")
	default:
		state = stBad.Render("○ disconnected")
	}

	left := stTitle.Render("cs104-explorer") + "  " +
		stMuted.Render(m.conn.target()) + "  " + state

	// The right-hand side gives up its least important part first, so a
	// narrow terminal loses the uptime rather than the clock.
	clock := m.clock().Format("15:04:05")
	candidates := []string{clock, m.rateText() + "  " + clock}
	if !m.linkSince.IsZero() {
		candidates = append(candidates,
			"up "+fmtDuration(m.clock().Sub(m.linkSince))+"  "+m.rateText()+"  "+clock)
	}
	for i := len(candidates) - 1; i >= 0; i-- {
		gap := m.width - lipgloss.Width(left) - lipgloss.Width(candidates[i]) - 1
		if gap >= 1 {
			return left + repeat(" ", gap+1) + stMuted.Render(candidates[i])
		}
	}
	return fit(left, m.width)
}

func tabLabel(i int, name string) string {
	return fmt.Sprintf(" %d %s ", i+1, name)
}

func (m *Model) viewTabs(l layout) string {
	var b strings.Builder
	for i, name := range screenNames {
		label := tabLabel(i, name)
		switch {
		case Screen(i) == m.screen:
			b.WriteString(stTabOn.Render(label))
		case m.hover.kind == zoneTab && m.hover.n == i:
			b.WriteString(stKey.Render(label))
		default:
			b.WriteString(stTabOff.Render(label))
		}
	}

	// The right of the tab bar is where the view's own state lives: what is
	// being filtered out, and how much of the list is on screen. Without it a
	// filtered table is indistinguishable from a device that stopped talking.
	var status []string
	if m.filter != "" {
		status = append(status, "filter "+strconv.Quote(m.filter))
	}
	if m.screen.isTable() {
		status = append(status, plural(l.total, m.rowNoun()))
		// A window on a longer stream must say so. Otherwise "5000 events"
		// after a 15000 object interrogation reads as the device having
		// reported 5000 things.
		if n := m.screenDiscarded(); n > 0 {
			status = append(status, stBad.Render(fmt.Sprintf("%d older discarded", n)))
		}
	}
	if m.screen.follows() && m.follow {
		status = append(status, "following")
	}
	if len(status) == 0 {
		return fit(b.String(), m.width)
	}

	right := stMuted.Render(strings.Join(status, " · ") + " ")
	gap := m.width - lipgloss.Width(b.String()) - lipgloss.Width(right)
	if gap < 1 {
		return fit(b.String(), m.width)
	}
	return b.String() + repeat(" ", gap) + right
}

func (m *Model) viewBody(l layout) []string {
	if m.cmd.active {
		return clip(m.viewCmdForm(l), l.body.h, l.body.w)
	}
	if m.form.active {
		return clip(m.viewForm(l), l.body.h, l.body.w)
	}
	if m.modal.kind != modalNone {
		return clip(m.viewModal(l), l.body.h, l.body.w)
	}

	var body []string
	switch m.screen {
	case ScreenOverview:
		body = m.viewOverview(l.body)
	case ScreenPoints:
		body = m.viewPoints(l)
	case ScreenEvents:
		body = m.viewEvents(l)
	case ScreenLog:
		body = m.viewLog(l)
	case ScreenFiles:
		body = m.viewFiles(l)
	case ScreenHelp:
		body = m.viewHelp(l)
	}

	if l.detail.empty() {
		return clip(body, l.body.h, l.body.w)
	}
	// The inspector is a second column of the body, joined row by row so
	// neither side can push the other out of the frame.
	return joinColumns([][]string{
		clip(body, l.body.h, l.table.w),
		box("Inspector", l.detail.w, l.detail.h, m.viewDetail(l.detail.w-4)),
	}, l.body.h)
}

// viewToolbar draws the clickable actions, and the standing warning that
// commands are not being confirmed. That warning has no key and cannot be
// dismissed: the one moment an operator needs to be told is the moment they
// have stopped expecting a dialog to appear.
func (m *Model) viewToolbar(l layout) string {
	var b strings.Builder
	b.WriteByte(' ')
	for i, btn := range l.buttons {
		if i > 0 {
			b.WriteByte(' ')
		}
		hovered := m.hover.kind == zoneButton && m.hover.n == i
		b.WriteString(renderButton(btn, hovered))
	}
	if m.confirm {
		return fit(b.String(), m.width)
	}
	warn := stWarn.Render(directWarning)
	gap := m.width - lipgloss.Width(b.String()) - lipgloss.Width(warn)
	if gap < 1 {
		return fit(b.String(), m.width)
	}
	return b.String() + repeat(" ", gap) + warn
}

func buttonLabel(b button) string { return "[" + b.key + " " + b.label + "]" }

func renderButton(b button, hovered bool) string {
	key, label := stKey.Render(b.key), b.label
	switch {
	case hovered:
		return stSel.Render(buttonLabel(b))
	case b.on:
		// An engaged mode is drawn as engaged, so the toolbar reports state
		// rather than only offering actions.
		return stMuted.Render("[") + key + " " + stGood.Render(label) + stMuted.Render("]")
	}
	return stMuted.Render("[") + key + " " + label + stMuted.Render("]")
}

// footerButtons is the action set for the current screen.
func (m *Model) footerButtons() []button {
	if m.cmd.active {
		return []button{{label: "Send", key: "enter"}, {label: "Cancel", key: "esc"}}
	}
	if m.form.active {
		return []button{{label: "Apply", key: "enter"}, {label: "Cancel", key: "esc"}}
	}
	if m.modal.kind == modalCmdFeedback {
		out := make([]button, 0, 2)
		for _, c := range m.feedbackChoices() {
			out = append(out, button{label: c.label, key: c.key})
		}
		return out
	}
	if m.modal.kind != modalNone {
		out := make([]button, 0, len(m.modal.choices))
		for _, c := range m.modal.choices {
			out = append(out, button{label: c.label, key: c.key})
		}
		return out
	}

	link := button{label: "STARTDT", key: "a"}
	if m.active {
		link = button{label: "STOPDT", key: "A"}
	}

	switch m.screen {
	case ScreenOverview:
		return []button{
			{label: "GI", key: "i"}, {label: "Counters", key: "p"},
			{label: "Clock", key: "t"}, {label: "Test", key: "T"},
			{label: "Command", key: "o"}, link, {label: "Connection", key: "C"},
		}
	case ScreenPoints:
		return []button{
			{label: "GI", key: "i"}, {label: "Read", key: "s"},
			{label: "Filter", key: "/"}, {label: "Inspect", key: "d", on: m.detail},
			{label: "Command", key: "o"}, {label: "Feedback", key: "O"},
			{label: sboLabel(m.sbo), key: "E", on: m.sbo},
			{label: "Export", key: "e"},
		}
	case ScreenEvents:
		return []button{
			{label: "Filter", key: "/"}, {label: "Follow", key: "f", on: m.follow},
			{label: "Clear", key: "x"}, {label: "Export", key: "e"},
			{label: "GI", key: "i"},
		}
	case ScreenLog:
		return []button{
			{label: "Filter", key: "/"}, {label: "Follow", key: "f", on: m.follow},
			{label: "Clear", key: "x"}, {label: "Protocol log", key: "v", on: m.conn.verbose()},
			{label: "Export", key: "e"},
		}
	case ScreenFiles:
		return []button{
			{label: "Directory", key: "l"}, {label: "Fetch", key: "enter"},
			{label: "Clear", key: "x"}, link,
		}
	}
	return []button{{label: "Quit", key: "q"}}
}

func sboLabel(sbo bool) string {
	if sbo {
		return "Select+Execute"
	}
	return "Direct"
}

// viewHint is the last line: what the keys do here, or whatever the prompt,
// the toast or the last error has to say.
func (m *Model) viewHint(l layout) string {
	switch {
	case m.prompt.active:
		return fit(" "+stKey.Render(m.prompt.label+": ")+m.prompt.input+"▏"+
			stMuted.Render("   enter apply · esc cancel"), m.width)
	case m.toast.active():
		return fit(" "+levelStyle(m.toast.level).Render(m.toast.text), m.width)
	}

	var hint string
	switch m.screen {
	case ScreenOverview:
		hint = "i GI · p counters · t clock · o command · a/A startdt/stopdt · C connection · ? help"
	case ScreenPoints:
		hint = "↑↓ move · o command · O feedback · d inspect · / filter · < > r sort · e export"
	case ScreenEvents:
		hint = "↑↓ move · f follow · / filter · x clear · e export"
	case ScreenLog:
		hint = "↑↓ move · f follow · v protocol log · / filter · x clear · e export"
	case ScreenFiles:
		hint = "l directory · enter fetch · ↑↓ move · saved to ./" + m.files.dir + "/"
	case ScreenHelp:
		hint = "↑↓ scroll · 1-6 or tab to leave"
	}
	if m.lastErr != "" && m.screen == ScreenOverview {
		hint = m.lastErr
		return fit(" "+stBad.Render(hint), m.width)
	}
	return fit(" "+stMuted.Render(hint), m.width)
}

// ---------- overview ----------

type panelSpec struct {
	title string
	lines []string
}

func (m *Model) viewOverview(b rect) []string {
	panels := []panelSpec{
		{"Session", m.overviewSession()},
		{"Traffic", m.overviewTraffic()},
		{"Database", m.overviewDatabase()},
		{"Activity", m.overviewActivity(b.h)},
	}
	return stackPanels(panels, b.w, b.h)
}

// stackPanels lays panels out in two columns when there is room for two, and
// one when there is not.
func stackPanels(panels []panelSpec, w, h int) []string {
	if w < 76 {
		var out []string
		for _, p := range panels {
			ph := min(len(p.lines)+2, max(h-len(out), 0))
			if ph < 3 {
				break
			}
			out = append(out, box(p.title, w, ph, p.lines)...)
		}
		return out
	}

	colW := (w - 1) / 2
	left, right := panels[:2], panels[2:]

	build := func(ps []panelSpec, width int) []string {
		var out []string
		for _, p := range ps {
			ph := min(len(p.lines)+2, max(h-len(out), 0))
			if ph < 3 {
				break
			}
			out = append(out, box(p.title, width, ph, p.lines)...)
		}
		return clip(out, h, width)
	}
	return joinColumns([][]string{build(left, colW), build(right, w-colW-1)}, h)
}

func field(name, value string) string {
	return stMuted.Render(cell(name, 15, false)) + value
}

func (m *Model) overviewSession() []string {
	p := m.conn.params()
	up := "—"
	if !m.linkSince.IsZero() {
		up = fmtDuration(m.clock().Sub(m.linkSince))
	}
	state := m.status
	switch {
	case m.active:
		state = stGood.Render("active — data transfer running")
	case m.connected:
		state = stWarn.Render("connected, STOPDT — press a to activate")
	default:
		state = stBad.Render(state)
	}
	return []string{
		field("Outstation", m.conn.target()),
		field("State", state),
		field("Uptime", up),
		field("Common addr", fmt.Sprint(m.conn.lk.CommonAddr)),
		field("Originator", fmt.Sprint(m.conn.lk.Originator)),
		field("Parameters", fmt.Sprintf("COT %d · CA %d · IOA %d",
			p.CauseSize, p.CommonAddrSize, p.InfoObjAddrSize)),
		field("Commands", commandMode(m.sbo)+", "+qocName(m.qoc)),
	}
}

// screenDiscarded is how many rows fell off the end of the window behind the
// current screen.
func (m *Model) screenDiscarded() uint64 {
	switch m.screen {
	case ScreenEvents:
		return m.eventsDropped
	case ScreenLog:
		return m.logsDropped
	}
	return 0
}

func (m *Model) overviewTraffic() []string {
	return []string{
		field("ASDUs in", fmt.Sprint(m.rxASDU)),
		field("Objects in", fmt.Sprint(m.rxItems)),
		field("ASDUs out", fmt.Sprint(m.txASDU)),
		field("Rate", m.rateText()),
		field("Trend", sparkline(m.rateHist, 24)),
		field("Commands", fmt.Sprintf("%d sent · %s · %s",
			m.cmdSent, okText(m.cmdOK), failedText(m.cmdFail))),
		field("Dropped", droppedText(m.conn.dropCount())),
		field("History", m.historyText()),
	}
}

// historyText says how much of the arrival order is still on hand. The point
// table keeps every object a device reports; this is the event window.
func (m *Model) historyText() string {
	kept := len(m.events)
	if m.eventsDropped == 0 {
		return fmt.Sprintf("%d events kept, none discarded", kept)
	}
	return fmt.Sprintf("%d events kept, %s",
		kept, stBad.Render(fmt.Sprintf("%d discarded — raise -history", m.eventsDropped)))
}

// droppedText reports messages the interface could not take.
//
// Process data is never dropped — arrivals are batched, and a batch that
// outgrows its limit makes the protocol goroutine wait so back-pressure
// reaches the outstation instead. This counts what is deliberately
// droppable: log and status messages. It is shown because a tool that loses
// anything quietly is not one you can trust about what a device sent.
func droppedText(n uint64) string {
	if n == 0 {
		return "none"
	}
	return stBad.Render(fmt.Sprintf("%d log/status messages", n))
}

func okText(n uint64) string {
	if n == 0 {
		return "0 ok"
	}
	return stGood.Render(fmt.Sprintf("%d ok", n))
}

func failedText(n uint64) string {
	if n == 0 {
		return "0 failed"
	}
	return stBad.Render(fmt.Sprintf("%d failed", n))
}

// overviewDatabase counts what has arrived, by kind and by quality: the
// question is whether the device is reporting sensibly, not just at all.
func (m *Model) overviewDatabase() []string {
	var single, double, measured, counter, other, bad, stale int
	now := m.clock()
	for _, p := range m.points {
		switch p.Type {
		case asdu.M_SP_NA_1, asdu.M_SP_TA_1, asdu.M_SP_TB_1:
			single++
		case asdu.M_DP_NA_1, asdu.M_DP_TA_1, asdu.M_DP_TB_1:
			double++
		case asdu.M_ME_NA_1, asdu.M_ME_TA_1, asdu.M_ME_TD_1, asdu.M_ME_ND_1,
			asdu.M_ME_NB_1, asdu.M_ME_TB_1, asdu.M_ME_TE_1,
			asdu.M_ME_NC_1, asdu.M_ME_TC_1, asdu.M_ME_TF_1:
			measured++
		case asdu.M_IT_NA_1, asdu.M_IT_TA_1, asdu.M_IT_TB_1:
			counter++
		default:
			other++
		}
		if p.HasQds && p.Qds != 0 {
			bad++
		}
		if p.stale(now, m.staleAge) {
			stale++
		}
	}
	qualityLine := stGood.Render("all good")
	if bad > 0 {
		qualityLine = stWarn.Render(fmt.Sprintf("%d with quality flags", bad))
	}
	staleLine := "—"
	if m.staleAge > 0 {
		staleLine = fmt.Sprintf("%d older than %s", stale, m.staleAge)
	}
	return []string{
		field("Points", fmt.Sprint(len(m.points))),
		field("Single/double", fmt.Sprintf("%d / %d", single, double)),
		field("Measured", fmt.Sprint(measured)),
		field("Counters", fmt.Sprint(counter)),
		field("Other", fmt.Sprint(other)),
		field("Quality", qualityLine),
		field("Stale", staleLine),
		field("Events", func() string {
			if m.eventsDropped == 0 {
				return fmt.Sprint(len(m.events))
			}
			return fmt.Sprintf("%d %s", len(m.events),
				stBad.Render(fmt.Sprintf("(+%d discarded)", m.eventsDropped)))
		}()),
	}
}

func (m *Model) overviewActivity(n int) []string {
	rows := m.logs
	keep := max(min(n/2-2, 10), 3)
	if len(rows) > keep {
		rows = rows[len(rows)-keep:]
	}
	out := make([]string, 0, len(rows))
	for _, l := range rows {
		out = append(out, stMuted.Render(l.At.Format("15:04:05"))+" "+
			levelStyle(l.Level).Render(truncate(l.Text, 60)))
	}
	if len(out) == 0 {
		out = append(out, stMuted.Render("nothing yet"))
	}
	return out
}

func (m *Model) rateText() string {
	r := m.eventRate()
	if r == 0 {
		return "0/s"
	}
	return fmt.Sprintf("%.1f/s", r)
}

// ---------- tables ----------

type tableRow struct {
	cells     map[colID]string
	cellStyle map[colID]lipgloss.Style
	line      lipgloss.Style
	lineSet   bool
}

func newRow() tableRow {
	return tableRow{cells: map[colID]string{}, cellStyle: map[colID]lipgloss.Style{}}
}

func (m *Model) renderTable(l layout, empty string, row func(i int) tableRow) []string {
	out := make([]string, 0, l.table.h)
	out = append(out, m.renderColumnHeader(l))

	if l.total == 0 {
		out = append(out, "")
		out = append(out, "  "+stMuted.Render(empty))
		return out
	}

	cursor := m.cursor[m.screen]
	for i := 0; i < l.rows.h; i++ {
		idx := l.offset + i
		var line string
		if idx < l.total {
			line = renderRow(l.cols, row(idx), idx == cursor)
		}
		if !l.scroll.empty() {
			bar := scrollbarRune(i, l.rows.h, l.offset, l.total)
			line = fit(" "+line, l.table.w-1) + stMuted.Render(bar)
		} else {
			line = " " + line
		}
		out = append(out, line)
	}
	return out
}

func (m *Model) renderColumnHeader(l layout) string {
	var b strings.Builder
	b.WriteByte(' ')
	for i, c := range l.cols {
		if i > 0 {
			b.WriteByte(' ')
		}
		title := c.title
		if c.key != sortNone && c.key == m.sortBy && m.screen == ScreenPoints {
			if m.sortDesc {
				title += " ▼"
			} else {
				title += " ▲"
			}
		}
		text := cell(title, c.width, c.right)
		if m.hover.kind == zoneColumn && m.hover.n == i && c.key != sortNone {
			b.WriteString(stSel.Render(text))
		} else {
			b.WriteString(stColHead.Render(text))
		}
	}
	return fit(b.String(), l.table.w)
}

func renderRow(cols []column, r tableRow, selected bool) string {
	var b strings.Builder
	for i, c := range cols {
		if i > 0 {
			b.WriteByte(' ')
		}
		text := cell(r.cells[c.id], c.width, c.right)
		if !selected && !r.lineSet {
			if st, ok := r.cellStyle[c.id]; ok {
				text = st.Render(text)
			}
		}
		b.WriteString(text)
	}
	switch {
	case selected:
		return stSel.Render(b.String())
	case r.lineSet:
		return r.line.Render(b.String())
	}
	return b.String()
}

// columnsFor is the column set for a screen, before widths are resolved.
func columnsFor(s Screen) []column {
	switch s {
	case ScreenPoints:
		return []column{
			{id: colPoint, title: "POINT", key: sortPoint, width: 10},
			{id: colType, title: "TYPE", key: sortType, width: 9, prio: 4},
			{id: colValue, title: "VALUE", key: sortValue, width: 14, right: true},
			{id: colTrend, title: "TREND", width: 12, prio: 3},
			{id: colQuality, title: "QUALITY", key: sortQuality, width: 11},
			{id: colCause, title: "CAUSE", min: 12, flex: true},
			{id: colAge, title: "AGE", key: sortAge, width: 5, right: true, prio: 2},
			{id: colStamp, title: "TIMESTAMP", key: sortTime, width: 12, prio: 1},
		}
	case ScreenEvents:
		return []column{
			{id: colReceived, title: "RECEIVED", width: 12},
			{id: colPoint, title: "POINT", width: 10},
			{id: colType, title: "TYPE", width: 9, prio: 4},
			{id: colValue, title: "VALUE", width: 14, right: true},
			{id: colQuality, title: "QUALITY", width: 11},
			{id: colCause, title: "CAUSE", min: 12, flex: true},
			{id: colStamp, title: "TIMESTAMP", width: 12, prio: 1},
		}
	case ScreenFiles:
		return []column{
			{id: colPoint, title: "IOA", width: 8},
			{id: colFileName, title: "FILE", width: 12},
			{id: colFileSize, title: "SIZE", width: 10, right: true},
			{id: colFileTime, title: "MODIFIED", width: 16, prio: 1},
			{id: colFileStatus, title: "STATUS", min: 16, flex: true},
		}
	default:
		return []column{
			{id: colReceived, title: "TIME", width: 12},
			{id: colLevel, title: "LEVEL", width: 5},
			{id: colMessage, title: "MESSAGE", min: 20, flex: true},
		}
	}
}

func (m *Model) viewPoints(l layout) []string {
	rows := m.visiblePoints()
	empty := "nothing reported yet — press i for a general interrogation"
	if m.filter != "" {
		empty = fmt.Sprintf("no points match %q — press esc to clear the filter", m.filter)
	}
	now := m.clock()

	return m.renderTable(l, empty, func(i int) tableRow {
		p := rows[i]
		r := newRow()
		r.cells[colPoint] = pointLabel(p.Key)
		r.cells[colType] = shortType(p.Type)
		r.cells[colValue] = p.Value
		r.cells[colTrend] = sparkline(p.Hist, 12)
		r.cells[colQuality] = qualityTextKind(p.Qds, p.HasQds, p.IsQdp)
		r.cells[colCause] = causeName(p.Cause)
		r.cells[colAge] = fmtAge(now.Sub(p.Updated))
		if !p.Stamp.IsZero() {
			r.cells[colStamp] = p.Stamp.Local().Format("15:04:05.000")
		} else {
			r.cells[colStamp] = "—"
		}

		switch {
		case p.HasQds && p.Qds&asdu.QDSInvalid != 0:
			r.cellStyle[colQuality] = stBad
		case p.HasQds && p.Qds != 0:
			r.cellStyle[colQuality] = stWarn
		case p.HasQds:
			r.cellStyle[colQuality] = stGood
		}
		if p.stale(now, m.staleAge) {
			r.line, r.lineSet = stStale, true
		}
		return r
	})
}

func (m *Model) viewEvents(l layout) []string {
	rows := m.visibleEvents()
	empty := "no events yet"
	if m.filter != "" {
		empty = fmt.Sprintf("no events match %q", m.filter)
	}
	return m.renderTable(l, empty, func(i int) tableRow {
		e := rows[i]
		r := newRow()
		r.cells[colReceived] = e.At.Format("15:04:05.000")
		r.cells[colPoint] = pointLabel(e.Key)
		r.cells[colType] = shortType(e.Type)
		r.cells[colValue] = e.Value
		r.cells[colQuality] = qualityText(e.Qds, e.HasQ)
		r.cells[colCause] = causeName(e.Cause)
		if !e.Stamp.IsZero() {
			r.cells[colStamp] = e.Stamp.Local().Format("15:04:05.000")
		} else {
			r.cells[colStamp] = "—"
		}
		if e.HasQ && e.Qds&asdu.QDSInvalid != 0 {
			r.cellStyle[colQuality] = stBad
		}
		r.cellStyle[colCause] = stEvent
		return r
	})
}

func (m *Model) viewLog(l layout) []string {
	rows := m.visibleLogs()
	empty := "nothing logged yet"
	if m.filter != "" {
		empty = fmt.Sprintf("no lines match %q", m.filter)
	}
	return m.renderTable(l, empty, func(i int) tableRow {
		x := rows[i]
		r := newRow()
		r.cells[colReceived] = x.At.Format("15:04:05.000")
		r.cells[colLevel] = x.Level
		r.cells[colMessage] = x.Text
		r.cellStyle[colLevel] = levelStyle(x.Level)
		r.cellStyle[colMessage] = levelStyle(x.Level)
		return r
	})
}

func (m *Model) viewFiles(l layout) []string {
	empty := "no directory yet — press l to call the outstation's file directory"
	if m.files.listed {
		empty = "the outstation reported no files"
	}
	return m.renderTable(l, empty, func(i int) tableRow {
		f := m.files.rows[i]
		r := newRow()
		r.cells[colPoint] = strconv.FormatUint(uint64(f.Ioa), 10)
		r.cells[colFileName] = nofName(f.Nof)
		r.cells[colFileSize] = fmtBytes(f.Size)
		if !f.Time.IsZero() {
			r.cells[colFileTime] = f.Time.Format("2006-01-02 15:04")
		} else {
			r.cells[colFileTime] = "—"
		}
		r.cells[colFileStatus] = f.Status
		switch {
		case f.Local != "":
			r.cellStyle[colFileStatus] = stGood
		case strings.HasPrefix(f.Status, "failed"):
			r.cellStyle[colFileStatus] = stBad
		case strings.HasPrefix(f.Status, "transferring"):
			r.cellStyle[colFileStatus] = stWarn
		}
		return r
	})
}

// ---------- inspector ----------

func (m *Model) viewDetail(w int) []string {
	p, ok := m.selectedPoint()
	if !ok {
		return []string{stMuted.Render("no point selected")}
	}
	out := []string{
		detailField("Address", pointLabel(p.Key)),
		detailField("Type", typeName(p.Type)),
		detailField("Value", p.Value),
		detailField("Cause", causeName(p.Cause)),
		detailField("Updates", fmt.Sprint(p.Updates)),
		detailField("Age", fmtAge(m.clock().Sub(p.Updated))),
		"",
		stColHead.Render("Quality"),
	}
	out = append(out, qualityLines(p)...)
	out = append(out, "", stColHead.Render("Timestamp"))
	if p.Stamp.IsZero() {
		out = append(out, stMuted.Render("  the type carries no time tag"))
	} else {
		out = append(out, "  "+p.Stamp.Local().Format("2006-01-02 15:04:05.000"))
	}
	if len(p.Hist) > 1 {
		out = append(out, "", stColHead.Render("Trend"), "  "+sparkline(p.Hist, w-2))
	}
	// A command is not addressed by this point: in 104 the command address
	// space is its own, so the dialog asks for the address rather than
	// offering to operate whatever the cursor is on.
	out = append(out, "", stColHead.Render("Commands"),
		stMuted.Render("  o  command dialog"),
		stMuted.Render("  commands carry their own"),
		stMuted.Render("  address, not this one"))
	return out
}

func detailField(name, value string) string {
	return stMuted.Render(cell(name, 10, false)) + value
}

func qualityLines(p *pointState) []string {
	if !p.HasQds {
		return []string{stMuted.Render("  the type carries no descriptor")}
	}
	if p.Qds == asdu.QDSGood {
		return []string{"  " + stGood.Render("GOOD")}
	}
	flags := []struct {
		bit  asdu.QualityDescriptor
		name string
	}{
		{asdu.QDSInvalid, "IV  invalid"},
		{asdu.QDSNotTopical, "NT  not topical"},
		{asdu.QDSSubstituted, "SB  substituted"},
		{asdu.QDSBlocked, "BL  blocked"},
		{asdu.QDSOverflow, "OV  overflow"},
	}
	var out []string
	for _, f := range flags {
		if p.Qds&f.bit != 0 {
			out = append(out, "  "+stWarn.Render(f.name))
		}
	}
	return out
}

// ---------- dialogs ----------

func (m *Model) viewModal(l layout) []string {
	title, lines, choices := m.modalContent()
	out := make([]string, l.body.h)
	for i := range out {
		out[i] = repeat(" ", l.body.w)
	}

	inner := make([]string, 0, l.modal.h)
	inner = append(inner, lines...)
	inner = append(inner, "")
	for i, c := range choices {
		label := "  " + c.label
		style := lipgloss.NewStyle()
		if c.danger {
			style = stWarn
		}
		if m.hover.kind == zoneChoice && m.hover.n == i {
			style = stSel
		}
		inner = append(inner, style.Render(cell(label, l.modal.w-4, false))+
			stMuted.Render(" "+c.key))
	}

	frame := box(title, l.modal.w, l.modal.h, inner)
	for i, line := range frame {
		y := l.modal.y - l.body.y + i
		if y < 0 || y >= len(out) {
			continue
		}
		out[y] = pad(repeat(" ", l.modal.x)+line, l.body.w)
	}
	return out
}

// viewCmdForm draws the command dialog: every parameter of the command, and
// nothing taken from the row behind it.
func (m *Model) viewCmdForm(l layout) []string {
	fields := m.cmd.fields()
	out := make([]string, l.body.h)
	for i := range out {
		out[i] = repeat(" ", l.body.w)
	}

	inner := make([]string, 0, l.form.h)
	last := min(l.formFirst+l.formRows, len(fields))
	for i := l.formFirst; i < last; i++ {
		fld := fields[i]
		name := stMuted.Render(fld.label)
		if i == m.cmd.cursor {
			name = stKey.Render("▸ " + fld.label)
		}
		inner = append(inner, name)

		value := fld.value
		vis := m.cmd.visible()
		if i == m.cmd.cursor && i < len(vis) && !m.cmd.isChoice(vis[i]) {
			value += "▏"
		}
		boxed := "  " + cell(value, max(l.form.w-8, 4), false)
		if i == m.cmd.cursor {
			inner = append(inner, stSel.Render(boxed))
		} else {
			inner = append(inner, boxed+stMuted.Render("  "+fld.hint))
		}
	}
	if m.cmd.err != "" {
		inner = append(inner, stBad.Render(m.cmd.err))
	} else {
		inner = append(inner, stMuted.Render("a command carries its own address, unrelated to any monitored point"))
	}
	inner = append(inner, stMuted.Render("tab next · ← → change · enter send · esc cancel"))

	frame := box("Send command", l.form.w, l.form.h, inner)
	for i, line := range frame {
		y := l.form.y - l.body.y + i
		if y < 0 || y >= len(out) {
			continue
		}
		out[y] = pad(repeat(" ", l.form.x)+line, l.body.w)
	}
	return out
}

func (m *Model) viewForm(l layout) []string {
	f := m.form
	out := make([]string, l.body.h)
	for i := range out {
		out[i] = repeat(" ", l.body.w)
	}

	inner := make([]string, 0, l.form.h)
	last := min(l.formFirst+l.formRows, len(f.fields))
	for i := l.formFirst; i < last; i++ {
		fld := f.fields[i]
		name := stMuted.Render(fld.label)
		if i == f.cursor {
			name = stKey.Render("▸ " + fld.label)
		}
		inner = append(inner, name)

		value := fld.value
		if i == f.cursor {
			value += "▏"
		}
		boxed := "  " + cell(value, max(l.form.w-8, 4), false)
		if i == f.cursor {
			inner = append(inner, stSel.Render(boxed))
		} else {
			inner = append(inner, boxed+stMuted.Render("  "+fld.hint))
		}
	}
	if f.err != "" {
		inner = append(inner, stBad.Render(f.err))
	} else {
		inner = append(inner, stMuted.Render("applying reconnects in place"))
	}
	inner = append(inner, stMuted.Render("tab next · enter apply · esc cancel"))

	frame := box(f.title, l.form.w, l.form.h, inner)
	for i, line := range frame {
		y := l.form.y - l.body.y + i
		if y < 0 || y >= len(out) {
			continue
		}
		out[y] = pad(repeat(" ", l.form.x)+line, l.body.w)
	}
	return out
}

// ---------- help ----------

func (m *Model) viewHelp(l layout) []string {
	lines := helpLines()
	off := m.offset[ScreenHelp]
	out := make([]string, 0, l.body.h)
	for i := 0; i < l.body.h; i++ {
		idx := off + i
		if idx >= len(lines) {
			break
		}
		out = append(out, " "+lines[idx])
	}
	return out
}

func helpLines() []string {
	section := func(s string) string { return stColHead.Render(s) }
	key := func(k, what string) string {
		return "  " + stKey.Render(cell(k, 14, false)) + what
	}
	return []string{
		section("Screens"),
		"  The Points table keeps every information object the device reports.",
		"  The Events list is a window on the order they arrived in: one",
		"  interrogation of an N object device produces N arrivals. When the",
		"  window trims, the tab bar says how many were discarded — raise",
		"  -history, or -history 0 to keep everything.",
		"",
		key("1-6, tab", "switch screens"),
		key("?", "this reference"),
		key("q, ctrl+c", "quit"),
		"",
		section("Moving"),
		key("↑ ↓, j k", "move the cursor"),
		key("pgup pgdn", "move by a page"),
		key("home end", "first and last row"),
		key("f", "follow the newest row"),
		key("/ , esc", "filter the list; clear the filter"),
		key("< >, r", "change and reverse the sort column"),
		key("x", "clear the current list"),
		key("e", "export the current list as CSV"),
		"",
		section("Link"),
		key("a / A", "STARTDT / STOPDT — start and stop data transfer"),
		key("C", "edit the connection and reconnect in place"),
		key("v", "protocol (debug) logging on the Log screen"),
		"",
		section("Interrogation and system commands"),
		key("i", "general interrogation (C_IC_NA_1)"),
		key("p", "counter interrogation (C_CI_NA_1)"),
		key("t", "clock synchronisation (C_CS_NA_1)"),
		key("T", "test command (C_TS_NA_1)"),
		key("R", "reset process (C_RP_NA_1)"),
		key("s", "read command for one address (C_RD_NA_1)"),
		"",
		section("Process commands"),
		"  In IEC 60870-5-104 a command has its own information object",
		"  address: nothing says the command for the point at IOA 100 is",
		"  also at 100. So every parameter is entered in the dialog.",
		"",
		key("o", "the command dialog — type, addresses, value, qualifier"),
		key("O", "the feedback for the last command sent"),
		key("E", "select-before-execute or direct execute"),
		key("d", "the point inspector"),
		"",
		"  With select-before-execute the dialog sends the select, and the",
		"  execute follows only once the outstation confirms it.",
		"",
		section("Files"),
		key("l", "call the outstation's file directory"),
		key("enter", "fetch the selected file"),
		key("w", "show where the fetched file was saved"),
		"",
		section("Mouse"),
		"  Click a tab, a row, a column heading or a footer button. Click a",
		"  selected row again to act on it, right-click a point for the",
		"  inspector, scroll with the wheel, and drag the scrollbar.",
		"",
		section("Commands are deliberate"),
		"  Commands default to select-before-execute with a confirmation",
		"  dialog naming exactly what will be sent. -direct and -no-confirm",
		"  turn that off; while -no-confirm is in effect the toolbar says so.",
	}
}

// shortType abbreviates a type identification for a narrow column.
func shortType(t asdu.TypeID) string {
	s := typeName(t)
	return strings.TrimSuffix(s, "_1")
}

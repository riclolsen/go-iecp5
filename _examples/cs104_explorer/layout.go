package main

import "github.com/charmbracelet/lipgloss"

// Geometry is computed once, in one place, and used by both the renderer and
// the mouse handler. A pointer-driven interface has to answer "what is under
// the cursor" with exactly the same arithmetic that put the thing there; here
// the view and the hit test read the same struct, so a region can never be
// clickable somewhere it is not drawn.

type rect struct{ x, y, w, h int }

func (r rect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

func (r rect) empty() bool { return r.w <= 0 || r.h <= 0 }

// Screen furniture is a fixed number of rows and the table gets what is left:
// above the body the title bar, the tab bar and a rule; below it a rule, the
// toolbar and the hint line. Nothing reflows as data arrives.
const (
	rowTabs      = 1
	chromeTop    = 3
	chromeBottom = 3

	// The smallest terminal this can lay out honestly. Below it the tool says
	// so rather than drawing a mangled table that reads like corrupt data.
	minWidth  = 60
	minHeight = 12

	// detailWidth is the point inspector. It only appears when the terminal
	// can spare the columns without squeezing the table.
	detailWidth  = 34
	detailMinCol = 96
)

// sortKey names the orders the tables can be put in.
type sortKey int

const (
	sortNone sortKey = iota
	sortPoint
	sortType
	sortValue
	sortQuality
	sortAge
	sortTime
)

// colID names a column's contents. Rows are built against these rather than
// against positions, because a narrow terminal drops columns: with positional
// cells, dropping TREND would slide every point's quality one column left.
type colID int

const (
	colPoint colID = iota
	colType
	colValue
	colTrend
	colQuality
	colCause
	colAge
	colStamp
	colReceived
	colLevel
	colMessage
	colFileName
	colFileSize
	colFileTime
	colFileStatus
)

// column is one table column, resolved to a concrete width by layoutColumns.
type column struct {
	id    colID
	title string
	key   sortKey
	width int  // fixed width, ignored when flex
	min   int  // smallest acceptable width when flex
	flex  bool // absorbs the leftover columns
	right bool // right aligned
	// prio orders the columns for dropping when the terminal is narrow: the
	// highest number goes first, and zero never goes.
	prio int
}

// layoutColumns resolves widths for the space available, dropping the least
// important columns rather than squeezing every column into illegibility.
func layoutColumns(cols []column, width int) []column {
	out := append([]column(nil), cols...)

	fits := func(cs []column) (int, bool) {
		total := max(len(cs)-1, 0) // one space between columns
		for _, c := range cs {
			if c.flex {
				total += c.min
			} else {
				total += c.width
			}
		}
		return total, total <= width
	}

	for {
		if _, ok := fits(out); ok {
			break
		}
		worst, at := 0, -1
		for i, c := range out {
			if c.prio > worst {
				worst, at = c.prio, i
			}
		}
		if at < 0 {
			break // everything left is essential; flex absorbs the overflow
		}
		out = append(out[:at:at], out[at+1:]...)
	}

	used, _ := fits(out)
	slack := width - used
	for i := range out {
		if out[i].flex {
			out[i].width = max(out[i].min+slack, 1)
			slack = 0
		}
	}
	return out
}

// zoneKind classifies a clickable region.
type zoneKind int

const (
	zoneNone   zoneKind = iota
	zoneTab             // n is the screen
	zoneRows            // the table body; the row is derived from y
	zoneColumn          // n indexes layout.cols
	zoneButton          // n indexes layout.buttons
	zoneScroll          // the scrollbar track
	zoneDetail          // the point inspector
	zoneChoice          // n indexes the open dialog's choices
	zoneField           // n indexes the open form's fields
)

type zone struct {
	rect rect
	kind zoneKind
	n    int
}

// directWarning is the standing notice that commands are not being confirmed.
// It lives here because the toolbar has to reserve its width before the
// buttons are laid out: a warning dropped whenever the toolbar happens to be
// full is a warning that disappears exactly when the screen is busiest.
const directWarning = "! commands send immediately "

// button is a footer action. Clicking one presses its key, so the pointer and
// the keyboard can never drift apart: there is one implementation of every
// action and the mouse is just another way to reach it.
type button struct {
	label string
	key   string
	on    bool // an engaged mode, drawn as engaged
}

// layout is the resolved geometry of one frame.
type layout struct {
	w, h int
	ok   bool // false when the terminal is too small to draw

	body   rect // between the rules, the whole content area
	table  rect // the table within the body, excluding any detail panel
	rows   rect // just the data rows of the table
	detail rect // the point inspector, empty when closed
	scroll rect // the scrollbar track, empty when everything fits
	modal  rect // the open dialog, empty when none
	form   rect // the open editor, empty when none

	// formRows is how many fields the editor has room to draw and formFirst
	// the first of them, so a short terminal scrolls the list rather than
	// hiding the field the cursor is on.
	formRows, formFirst int

	cols    []column
	buttons []button
	choices []modalChoice

	zones []zone

	// total is how many rows the current screen holds and offset the first
	// one drawn, both after clamping, so the view and the hit test agree on
	// what is on screen.
	total, offset int
}

// modalRect centres a dialog in the body, sized to its content. The choice
// list is drawn flush to the bottom of the box and the hit test finds it by
// counting up from there, so the two must agree on the size.
func modalRect(d modalState, body rect) rect {
	w := lipgloss.Width(d.title) + 6
	for _, l := range d.lines {
		w = max(w, lipgloss.Width(l)+6)
	}
	for _, c := range d.choices {
		w = max(w, lipgloss.Width(c.label)+14)
	}
	w = min(max(w, 36), max(body.w-4, 12))

	// Two rows of frame, the message, a blank row, then one row per choice.
	h := min(len(d.lines)+len(d.choices)+3, body.h)
	return rect{
		x: body.x + max((body.w-w)/2, 0),
		y: body.y + max((body.h-h)/2, 0),
		w: w, h: h,
	}
}

// formRect centres the editor in the body and works out how much of the field
// list fits, scrolling that list to keep the focused field on screen.
func formRect(f *formState, body rect) (rect, int, int) {
	w := 44
	for _, fld := range f.fields {
		w = max(w, lipgloss.Width(fld.label)+6)
		w = max(w, lipgloss.Width(fld.value)+8)
		w = max(w, lipgloss.Width(fld.hint)+8)
	}
	if f.err != "" {
		w = max(w, lipgloss.Width(f.err)+6)
	}
	w = min(w, max(body.w-4, 12))

	// Two rows of frame, two of footer, and two rows for every field: its
	// name and the box holding its value.
	h := min(len(f.fields)*2+4, body.h)

	rows := min(max((h-4)/2, 1), len(f.fields))
	if f.cursor < f.offset {
		f.offset = f.cursor
	}
	if f.cursor >= f.offset+rows {
		f.offset = f.cursor - rows + 1
	}
	f.offset = min(max(f.offset, 0), max(len(f.fields)-rows, 0))

	return rect{
		x: body.x + max((body.w-w)/2, 0),
		y: body.y + max((body.h-h)/2, 0),
		w: w, h: h,
	}, rows, f.offset
}

// zoneAt returns the region under a point, innermost first.
func (l layout) zoneAt(x, y int) (zone, bool) {
	for _, z := range l.zones {
		if z.rect.contains(x, y) {
			return z, true
		}
	}
	return zone{}, false
}

// rowAt maps a screen row to an index in the current list.
func (l layout) rowAt(y int) (int, bool) {
	if !l.rows.contains(l.rows.x, y) {
		return 0, false
	}
	i := l.offset + (y - l.rows.y)
	if i < 0 || i >= l.total {
		return 0, false
	}
	return i, true
}

// layout computes this frame's geometry and clamps the scroll position to it.
// It is called by the view before drawing and by the mouse handler before hit
// testing; both get the same answer because both run this code.
func (m *Model) layout() layout {
	l := layout{w: m.width, h: m.height}
	if m.width < minWidth || m.height < minHeight {
		return l
	}
	l.ok = true

	bodyH := m.height - chromeTop - chromeBottom
	l.body = rect{x: 0, y: chromeTop, w: m.width, h: bodyH}

	x := 0
	for i, name := range screenNames {
		w := lipgloss.Width(tabLabel(i, name))
		if x+w > m.width {
			break
		}
		l.zones = append(l.zones, zone{rect: rect{x: x, y: rowTabs, w: w, h: 1},
			kind: zoneTab, n: i})
		x += w
	}
	l.total = m.rowCount()
	l.offset = m.offset[m.screen]

	// The command dialog owns the body: every parameter of a command is
	// entered there, and a click reaching the table behind it would act on
	// the device while the operator is composing something to send it.
	if m.cmd.active {
		f := formState{fields: m.cmd.fields(), cursor: m.cmd.cursor,
			offset: m.cmd.offset, err: m.cmd.err, title: "Command"}
		l.form, l.formRows, l.formFirst = formRect(&f, l.body)
		m.cmd.offset = f.offset
		for i := 0; i < l.formRows; i++ {
			l.zones = append(l.zones, zone{
				rect: rect{x: l.form.x + 2, y: l.form.y + 1 + i*2, w: l.form.w - 4, h: 2},
				kind: zoneField, n: l.formFirst + i,
			})
		}
		l.buttons = m.footerButtons()
		l.layoutButtons(m.toolbarWidth(), m.height)
		return l
	}

	// The editor owns the body the same way a dialog does, and for the same
	// reason: it is a set of values being changed together, and a click that
	// reached the table behind it would act on a device the operator is in
	// the middle of navigating away from.
	if m.form.active {
		l.form, l.formRows, l.formFirst = formRect(&m.form, l.body)
		for i := 0; i < l.formRows; i++ {
			l.zones = append(l.zones, zone{
				rect: rect{x: l.form.x + 2, y: l.form.y + 1 + i*2, w: l.form.w - 4, h: 2},
				kind: zoneField, n: l.formFirst + i,
			})
		}
		l.buttons = m.footerButtons()
		l.layoutButtons(m.toolbarWidth(), m.height)
		return l
	}

	// A dialog owns the body while it is open: nothing behind it is
	// clickable, which is the point of a modal.
	if m.modal.kind != modalNone {
		title, lines, choices := m.modalContent()
		l.choices = choices
		l.modal = modalRect(modalState{title: title, lines: lines, choices: choices}, l.body)
		for i := range l.choices {
			l.zones = append(l.zones, zone{
				rect: rect{
					x: l.modal.x + 2,
					y: l.modal.y + l.modal.h - 1 - len(l.choices) + i,
					w: l.modal.w - 4, h: 1,
				},
				kind: zoneChoice, n: i,
			})
		}
		l.buttons = m.footerButtons()
		l.layoutButtons(m.toolbarWidth(), m.height)
		return l
	}

	l.table = l.body
	if m.screen == ScreenPoints && m.detail && m.width >= detailMinCol {
		l.table.w = m.width - detailWidth - 1
		l.detail = rect{x: l.table.w + 1, y: l.body.y, w: detailWidth, h: bodyH}
		l.zones = append(l.zones, zone{rect: l.detail, kind: zoneDetail})
	}

	if m.screen.isTable() {
		// One row of the table is its column header; the rest are data.
		l.rows = rect{x: l.table.x, y: l.table.y + 1, w: l.table.w, h: max(l.table.h-1, 0)}

		tableW := l.table.w - 2 // one column of margin either side
		if l.total > l.rows.h {
			tableW-- // make room for the scrollbar
			l.scroll = rect{x: l.table.x + l.table.w - 1, y: l.rows.y, w: 1, h: l.rows.h}
			l.zones = append(l.zones, zone{rect: l.scroll, kind: zoneScroll})
		}

		l.cols = layoutColumns(columnsFor(m.screen), tableW)

		cx := l.table.x + 1
		for i, c := range l.cols {
			if c.key != sortNone {
				l.zones = append(l.zones, zone{
					rect: rect{x: cx, y: l.table.y, w: c.width, h: 1},
					kind: zoneColumn, n: i,
				})
			}
			cx += c.width + 1
		}
		l.zones = append(l.zones, zone{rect: l.rows, kind: zoneRows})
	}

	if m.screen == ScreenHelp {
		// The reference is not a list, but on a short terminal it is longer
		// than the body, and a reference you cannot reach the end of is not a
		// reference.
		l.rows = l.body
	}

	m.clampScroll(l.total, l.rows.h)
	l.offset = m.offset[m.screen]

	l.buttons = m.footerButtons()
	l.layoutButtons(m.toolbarWidth(), m.height)
	return l
}

// toolbarWidth is how much of the footer the buttons may use, which is all of
// it unless the unconfirmed-commands warning claims the right-hand end.
func (m *Model) toolbarWidth() int {
	if m.confirm {
		return m.width
	}
	return m.width - lipgloss.Width(directWarning) - 1
}

// layoutButtons places the footer toolbar and registers its click targets.
func (l *layout) layoutButtons(w, h int) {
	x := 1
	for i, b := range l.buttons {
		bw := lipgloss.Width(buttonLabel(b))
		if x+bw > w {
			l.buttons = l.buttons[:i]
			break
		}
		l.zones = append(l.zones, zone{
			rect: rect{x: x, y: h - 2, w: bw, h: 1}, kind: zoneButton, n: i,
		})
		x += bw + 1
	}
}

// clampScroll keeps the cursor inside the list and the window around the
// cursor, after anything that could have moved either.
func (m *Model) clampScroll(total, visible int) {
	cur, off := m.cursor[m.screen], m.offset[m.screen]

	cur = min(max(cur, 0), max(total-1, 0))
	if m.follow && m.screen.follows() {
		cur = max(total-1, 0)
	}

	switch {
	case visible <= 0 || total <= visible:
		off = 0
	default:
		off = min(max(off, 0), total-visible)
		if cur < off {
			off = cur
		}
		if cur >= off+visible {
			off = cur - visible + 1
		}
	}
	m.cursor[m.screen], m.offset[m.screen] = cur, off
}

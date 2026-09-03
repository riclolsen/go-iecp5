package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/riclolsen/go-iecp5/asdu"
)

// The whole interface is driven through HandleKey and HandleMouse against a
// layout computed from a terminal size, so every test here runs without a
// terminal — which is the point of resolving clicks to keys.

// newTestModel returns a model wired to a demo outstation, sized for a
// terminal, without starting Bubble Tea.
func newTestModel(t *testing.T) (*Model, func()) {
	t.Helper()
	conn := newConnection(link{Demo: true, CommonAddr: 1,
		Timeout: 5 * time.Second, Reconnect: time.Second}, t.TempDir())
	if err := conn.start(); err != nil {
		t.Fatalf("start demo session: %v", err)
	}
	m := NewModel(conn)
	m.width, m.height = 120, 30
	m.now = time.Now()
	return m, conn.stop
}

// nextMsg takes the next thing the interface would react to, with a deadline.
// It prefers a pending batch exactly as connection.wait does, so tests drive
// the same path the event loop does.
func nextMsg(c *connection, d time.Duration) (tea.Msg, bool) {
	if msg, ok := c.takeBatch(); ok {
		return msg, true
	}
	select {
	case <-c.batchWake:
		if msg, ok := c.takeBatch(); ok {
			return msg, true
		}
	case msg := <-c.out:
		return msg, true
	case <-time.After(d):
	}
	if msg, ok := c.takeBatch(); ok {
		return msg, true
	}
	return nil, false
}

// pump drains everything the session has produced, with a deadline, so a test
// can wait for the device to answer without sleeping blindly.
func pump(t *testing.T, m *Model, until func() bool, deadline time.Duration) bool {
	t.Helper()
	stop := time.Now().Add(deadline)
	for time.Now().Before(stop) {
		if until() {
			return true
		}
		if msg, ok := nextMsg(m.conn, 20*time.Millisecond); ok {
			m.now = time.Now()
			m.Update(msg)
		}
	}
	return until()
}

func TestDemoSessionActivatesAndReportsPoints(t *testing.T) {
	m, stop := newTestModel(t)
	defer stop()

	if !pump(t, m, func() bool { return m.active }, 10*time.Second) {
		t.Fatalf("data transfer never became active; status %q", m.status)
	}

	press(t, m, "i") // general interrogation
	if !pump(t, m, func() bool { return len(m.points) >= 10 }, 10*time.Second) {
		t.Fatalf("interrogation returned %d points, want at least 10", len(m.points))
	}

	// The demo reports every kind the table can draw.
	var single, measured, counted bool
	for _, p := range m.points {
		switch p.Type {
		case asdu.M_SP_NA_1, asdu.M_SP_TB_1:
			single = true
		case asdu.M_ME_NC_1, asdu.M_ME_TF_1:
			measured = true
		case asdu.M_IT_NA_1:
			counted = true
		}
	}
	if !single || !measured {
		t.Fatalf("missing kinds: single=%v measured=%v", single, measured)
	}

	// Counter interrogation, not general interrogation, is what returns
	// integrated totals.
	press(t, m, "p")
	if !pump(t, m, func() bool {
		for _, p := range m.points {
			if p.Type == asdu.M_IT_NA_1 {
				return true
			}
		}
		return counted
	}, 10*time.Second) {
		t.Fatal("counter interrogation returned no integrated totals")
	}

	// Everything must render at several sizes without panicking.
	for _, size := range [][2]int{{120, 30}, {80, 24}, {200, 60}, {61, 13}, {40, 10}} {
		m.width, m.height = size[0], size[1]
		for s := Screen(0); s < numScreens; s++ {
			m.screen = s
			if got := m.View(); got == "" {
				t.Fatalf("empty render at %dx%d on %s", size[0], size[1], s)
			}
		}
	}
	m.width, m.height = 120, 30
}

func TestCommandDialogRoundTrip(t *testing.T) {
	m, stop := newTestModel(t)
	defer stop()

	if !pump(t, m, func() bool { return m.active }, 10*time.Second) {
		t.Fatal("never became active")
	}
	press(t, m, "i")
	if !pump(t, m, func() bool { return hasPoint(m, demoCmdSingle) }, 10*time.Second) {
		t.Fatal("the interrogation reply never included the command point")
	}

	// The dialog opens from anywhere and takes nothing from the cursor: the
	// row selected here is deliberately not the one the command addresses.
	m.screen = ScreenPoints
	m.cursor[ScreenPoints] = 0
	m.HandleKey("o")
	if !m.cmd.active {
		t.Fatal("o must open the command dialog")
	}
	if m.cmd.ioa != "" {
		t.Fatalf("the dialog must not guess an address from the table, got %q", m.cmd.ioa)
	}

	// Type the command: single command, ON, direct execute.
	typeInto(m, cfIOA, fmt.Sprint(demoCmdSingle))
	focusField(m, cfValue)
	m.HandleKey("right") // OFF -> ON
	focusField(m, cfMode)
	if m.cmd.sbo {
		m.HandleKey("right") // select+execute -> direct execute
	}
	if m.cmd.sbo {
		t.Fatal("the mode field must toggle to direct execute")
	}

	m.HandleKey("enter")
	if m.cmd.active {
		t.Fatalf("a valid command must leave the dialog: %q", m.cmd.err)
	}
	if m.modal.kind != modalConfirm {
		t.Fatal("a command must be confirmed before it is sent")
	}
	joined := strings.Join(m.modal.lines, " ")
	for _, want := range []string{"C_SC_NA_1", fmt.Sprint(demoCmdSingle), "ON", "execute"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("the dialog must name %q: %v", want, m.modal.lines)
		}
	}

	_, cmd := m.HandleKey("enter")
	runCmd(t, m, cmd)

	// Sending opens the feedback dialog, and the outstation's confirmations
	// arrive in it.
	if m.modal.kind != modalCmdFeedback {
		t.Fatal("sending must show the command feedback")
	}
	if !pump(t, m, func() bool { return m.track.phase == cmdPhaseDone }, 10*time.Second) {
		t.Fatalf("the command never completed: phase %v, events %+v",
			m.track.phase, m.track.events)
	}
	var steps []string
	for _, e := range m.track.events {
		steps = append(steps, e.text)
	}
	for _, want := range []string{"execute sent", "activation confirmed", "terminated"} {
		if !strings.Contains(strings.Join(steps, " | "), want) {
			t.Fatalf("the feedback must record %q: %s", want, strings.Join(steps, " | "))
		}
	}

	// And the outstation reports the point it moved.
	if !pump(t, m, func() bool {
		p, ok := m.points[pointKey{CA: 1, IOA: demoCmdSingle}]
		return ok && p.Value == "ON" && p.Cause == asdu.ReturnInfoRemote
	}, 10*time.Second) {
		t.Fatalf("command was not reflected back: %+v", m.points[pointKey{CA: 1, IOA: demoCmdSingle}])
	}
	if m.cmdSent == 0 {
		t.Fatal("the command counter did not move")
	}
}

func TestSelectBeforeExecute(t *testing.T) {
	m, stop := newTestModel(t)
	defer stop()
	if !pump(t, m, func() bool { return m.active }, 10*time.Second) {
		t.Fatal("never became active")
	}

	m.HandleKey("o")
	typeInto(m, cfIOA, fmt.Sprint(demoCmdSingle))
	focusField(m, cfMode)
	if !m.cmd.sbo {
		m.HandleKey("right")
	}
	m.HandleKey("enter") // validate -> confirm dialog

	joined := strings.Join(m.modal.lines, " ")
	if !strings.Contains(joined, "SELECT only") {
		t.Fatalf("the confirmation must say this is only the select: %v", m.modal.lines)
	}
	_, cmd := m.HandleKey("enter")
	runCmd(t, m, cmd)

	// The select is confirmed and the execute waits for the operator.
	if !pump(t, m, func() bool { return m.track.phase == cmdPhaseSelectConfirmed },
		10*time.Second) {
		t.Fatalf("the select was never confirmed: phase %v", m.track.phase)
	}
	sentAfterSelect := m.cmdSent

	_, cmd = m.HandleKey("enter") // execute
	runCmd(t, m, cmd)
	if m.cmdSent != sentAfterSelect+1 {
		t.Fatal("the execute must be a second transmission")
	}
	if !pump(t, m, func() bool { return m.track.phase == cmdPhaseDone }, 10*time.Second) {
		t.Fatalf("the execute never completed: phase %v", m.track.phase)
	}
}

func TestCommandDialogValidates(t *testing.T) {
	m, stop := newTestModel(t)
	defer stop()

	m.HandleKey("o")
	if !m.cmd.active {
		t.Fatal("o must open the command dialog")
	}

	// An empty address is refused, and the cursor lands on the field.
	m.HandleKey("enter")
	if !m.cmd.active || m.cmd.err == "" {
		t.Fatal("an empty information object address must be refused")
	}
	if m.cmd.visible()[m.cmd.cursor] != cfIOA {
		t.Fatal("the cursor must move to the field that failed")
	}

	// A set-point takes a typed value, and a bad one is refused.
	focusField(m, cfType)
	for m.cmd.spec().id != asdu.C_SE_NC_1 {
		m.HandleKey("right")
	}
	typeInto(m, cfIOA, "6200")
	typeInto(m, cfValue, "not-a-number")
	m.HandleKey("enter")
	if !m.cmd.active || !strings.Contains(m.cmd.err, "value") {
		t.Fatalf("a bad set-point value must be refused, got %q", m.cmd.err)
	}

	// Cancelling sends nothing.
	before := m.cmdSent
	m.HandleKey("esc")
	if m.cmd.active {
		t.Fatal("esc must close the dialog")
	}
	if m.cmdSent != before {
		t.Fatal("cancelling must not send a command")
	}
}

func TestFilterSortAndExport(t *testing.T) {
	m, stop := newTestModel(t)
	defer stop()
	if !pump(t, m, func() bool { return m.active }, 10*time.Second) {
		t.Fatal("never became active")
	}
	press(t, m, "i")
	if !pump(t, m, func() bool { return len(m.points) >= 10 }, 10*time.Second) {
		t.Fatal("no points")
	}
	m.screen = ScreenPoints
	all := len(m.visiblePoints())

	// Filtering narrows the list and the tab bar says so.
	m.HandleKey("/")
	for _, k := range []string{"M", "_", "M", "E"} {
		m.HandleKey(k)
	}
	m.HandleKey("enter")
	filtered := len(m.visiblePoints())
	if filtered == 0 || filtered >= all {
		t.Fatalf("filter matched %d of %d points", filtered, all)
	}
	for _, p := range m.visiblePoints() {
		if !strings.Contains(typeName(p.Type), "M_ME") {
			t.Fatalf("point %s does not match the filter", pointLabel(p.Key))
		}
	}
	m.HandleKey("esc")
	if len(m.visiblePoints()) != all {
		t.Fatal("esc must clear the filter")
	}

	// Sorting by quality puts the flagged points first.
	m.sortBy, m.sortDesc = sortQuality, false
	rows := m.visiblePoints()
	if len(rows) > 1 && qualityRank(rows[0]) < qualityRank(rows[len(rows)-1]) {
		t.Fatal("sorting by quality must put the worst first")
	}

	// Export writes the view that is on screen.
	dir := t.TempDir()
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(wd)

	_, cmd := m.HandleKey("e")
	if cmd == nil {
		t.Fatal("export must produce a command")
	}
	msg, ok := cmd().(commandResultMsg)
	if !ok || !msg.ok {
		t.Fatalf("export failed: %+v", msg)
	}
	entries, _ := os.ReadDir(dir)
	found := ""
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "iec104-points-") {
			found = e.Name()
		}
	}
	if found == "" {
		t.Fatalf("no CSV written: %v", entries)
	}
	body, err := os.ReadFile(found)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != all+1 {
		t.Fatalf("export has %d rows, want %d plus a header", len(lines)-1, all)
	}
	if !strings.HasPrefix(lines[0], "common_address,ioa,type,value,quality") {
		t.Fatalf("unexpected header: %q", lines[0])
	}
}

func TestFileTransferScreen(t *testing.T) {
	m, stop := newTestModel(t)
	defer stop()
	if !pump(t, m, func() bool { return m.active }, 10*time.Second) {
		t.Fatal("never became active")
	}

	m.HandleKey("5") // Files
	if m.screen != ScreenFiles {
		t.Fatalf("screen = %v, want Files", m.screen)
	}

	_, cmd := m.HandleKey("l")
	if cmd == nil {
		t.Fatal("l must request the directory")
	}
	if msg := cmd(); msg != nil {
		m.Update(msg)
	}
	if !pump(t, m, func() bool { return m.files.rowCount() >= 2 }, 15*time.Second) {
		t.Fatalf("directory listed %d files, want 2", m.files.rowCount())
	}

	_, cmd = m.HandleKey("enter") // fetch the selected file
	if cmd == nil {
		t.Fatal("enter must start a transfer")
	}
	if msg := cmd(); msg != nil {
		m.Update(msg)
	}
	if !pump(t, m, func() bool { return m.files.rows[0].Local != "" }, 30*time.Second) {
		t.Fatalf("file never arrived: %+v", m.files.rows)
	}
	if body, err := os.ReadFile(m.files.rows[0].Local); err != nil || len(body) == 0 {
		t.Fatalf("saved file unreadable: %v", err)
	}
}

func TestMouseResolvesToKeys(t *testing.T) {
	m, stop := newTestModel(t)
	defer stop()
	if !pump(t, m, func() bool { return m.active }, 10*time.Second) {
		t.Fatal("never became active")
	}
	press(t, m, "i")
	if !pump(t, m, func() bool { return len(m.points) >= 10 }, 10*time.Second) {
		t.Fatal("no points")
	}

	// Clicking a tab switches screen.
	l := m.layout()
	tab, ok := findZone(l, zoneTab, int(ScreenPoints))
	if !ok {
		t.Fatal("no tab zone for Points")
	}
	m.HandleMouse(mouseEvent{x: tab.rect.x, y: tab.rect.y, button: tea.MouseButtonLeft})
	if m.screen != ScreenPoints {
		t.Fatalf("clicking the tab left the screen at %v", m.screen)
	}

	// Clicking a row selects it; clicking the selected row acts on it.
	l = m.layout()
	m.HandleMouse(mouseEvent{x: 2, y: l.rows.y + 2, button: tea.MouseButtonLeft})
	if m.cursor[ScreenPoints] != l.offset+2 {
		t.Fatalf("cursor = %d, want %d", m.cursor[ScreenPoints], l.offset+2)
	}
	m.HandleMouse(mouseEvent{x: 2, y: l.rows.y + 2, button: tea.MouseButtonLeft})
	if !m.cmd.active {
		t.Fatal("clicking a selected row must open the command dialog")
	}
	// The dialog takes nothing from the row that was clicked: a command in
	// 104 has its own address.
	if m.cmd.ioa != "" {
		t.Fatalf("the dialog must not prefill an address from the table, got %q", m.cmd.ioa)
	}
	m.HandleKey("esc")

	// Right-clicking a point opens the inspector.
	m.HandleMouse(mouseEvent{x: 2, y: l.rows.y + 1, button: tea.MouseButtonRight})
	if !m.detail {
		t.Fatal("right click must open the inspector")
	}

	// A column heading sorts, and clicking it again reverses.
	l = m.layout()
	col, ok := findZoneFunc(l, func(z zone) bool {
		return z.kind == zoneColumn && l.cols[z.n].key == sortValue
	})
	if !ok {
		t.Fatal("no clickable VALUE column")
	}
	m.HandleMouse(mouseEvent{x: col.rect.x, y: col.rect.y, button: tea.MouseButtonLeft})
	if m.sortBy != sortValue || m.sortDesc {
		t.Fatalf("sort = %v desc=%v, want value ascending", m.sortBy, m.sortDesc)
	}
	m.HandleMouse(mouseEvent{x: col.rect.x, y: col.rect.y, button: tea.MouseButtonLeft})
	if !m.sortDesc {
		t.Fatal("clicking the sort column again must reverse it")
	}

	// A footer button presses its key: the Inspect button toggles the panel.
	l = m.layout()
	btn, ok := findButton(l, "d")
	if !ok {
		t.Fatal("no Inspect button")
	}
	before := m.detail
	m.HandleMouse(mouseEvent{x: btn.rect.x, y: btn.rect.y, button: tea.MouseButtonLeft})
	if m.detail == before {
		t.Fatal("clicking the Inspect button must toggle the inspector")
	}

	// The wheel scrolls the list and the tab bar walks the tabs.
	m.screen, m.detail = ScreenPoints, false
	m.cursor[ScreenPoints], m.offset[ScreenPoints] = 0, 0
	m.height = 14 // short enough that the point list overflows
	l = m.layout()
	if l.total <= l.rows.h {
		t.Fatalf("need a list longer than %d rows to test scrolling", l.rows.h)
	}
	m.HandleMouse(mouseEvent{x: 2, y: l.rows.y + 1,
		button: tea.MouseButtonWheelDown, kind: mouseWheel})
	if m.offset[ScreenPoints] == 0 {
		t.Fatal("the wheel must scroll the table")
	}
	m.height = 30
	m.HandleMouse(mouseEvent{x: 2, y: rowTabs,
		button: tea.MouseButtonWheelDown, kind: mouseWheel})
	if m.screen != ScreenEvents {
		t.Fatalf("the wheel over the tabs must walk them; screen = %v", m.screen)
	}
}

func TestConnectionEditorValidates(t *testing.T) {
	m, stop := newTestModel(t)
	defer stop()

	m.HandleKey("C")
	if !m.form.active {
		t.Fatal("C must open the connection editor")
	}
	if got := m.form.fields[fieldAddress].value; got != "demo" {
		t.Fatalf("address field = %q, want demo", got)
	}

	// A bad common address is refused, with the cursor left on the field.
	m.form.cursor = fieldCommonAddr
	m.form.fields[fieldCommonAddr].value = "0"
	m.HandleKey("enter")
	if !m.form.active || m.form.err == "" {
		t.Fatal("common address 0 must be refused")
	}
	if m.form.cursor != fieldCommonAddr {
		t.Fatal("the cursor must stay on the field that failed")
	}

	// A bad duration is refused too.
	m.form.fields[fieldCommonAddr].value = "1"
	m.form.fields[fieldTimeout].value = "soon"
	m.HandleKey("enter")
	if !m.form.active || !strings.Contains(m.form.err, "timeout") {
		t.Fatalf("a bad duration must be refused: %q", m.form.err)
	}

	// Escape leaves the session untouched.
	m.form.fields[fieldTimeout].value = "30s"
	m.HandleKey("esc")
	if m.form.active {
		t.Fatal("esc must close the editor")
	}
}

func TestPointsClearedWhenAddressingAnotherDevice(t *testing.T) {
	m, stop := newTestModel(t)
	defer stop()
	if !pump(t, m, func() bool { return m.active }, 10*time.Second) {
		t.Fatal("never became active")
	}
	press(t, m, "i")
	if !pump(t, m, func() bool { return len(m.points) >= 10 }, 10*time.Second) {
		t.Fatal("no points")
	}

	m.HandleKey("C")
	m.form.fields[fieldCommonAddr].value = "7" // a different station
	m.HandleKey("enter")

	if len(m.points) != 0 {
		t.Fatalf("%d points survived a change of device", len(m.points))
	}
}

func TestNarrowTerminalSaysSo(t *testing.T) {
	m, stop := newTestModel(t)
	defer stop()
	m.width, m.height = 40, 8
	out := m.View()
	if !strings.Contains(out, "needs at least") {
		t.Fatalf("a tiny terminal must say so, got %q", out)
	}
}

// ---------- helpers ----------

// press sends a key and runs whatever command it produced, which is what
// Bubble Tea does for real. Without this the action never leaves the model.
func press(t *testing.T, m *Model, key string) {
	t.Helper()
	_, cmd := m.HandleKey(key)
	runCmd(t, m, cmd)
}

func runCmd(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	if msg := cmd(); msg != nil {
		m.Update(msg)
	}
}

// focusField moves the dialog cursor to a named field.
func focusField(m *Model, id cmdFieldID) {
	for i, v := range m.cmd.visible() {
		if v == id {
			m.cmd.cursor = i
			return
		}
	}
}

// typeInto clears a text field and types a value into it.
func typeInto(m *Model, id cmdFieldID, text string) {
	focusField(m, id)
	m.HandleKey("ctrl+u")
	for _, r := range text {
		m.HandleKey(string(r))
	}
}

func hasPoint(m *Model, ioa uint) bool {
	_, ok := m.points[pointKey{CA: 1, IOA: ioa}]
	return ok
}

func selectPoint(m *Model, ioa uint) bool {
	for i, p := range m.visiblePoints() {
		if p.Key.IOA == ioa {
			m.cursor[ScreenPoints] = i
			return true
		}
	}
	return false
}

func findZone(l layout, kind zoneKind, n int) (zone, bool) {
	return findZoneFunc(l, func(z zone) bool { return z.kind == kind && z.n == n })
}

func findZoneFunc(l layout, ok func(zone) bool) (zone, bool) {
	for _, z := range l.zones {
		if ok(z) {
			return z, true
		}
	}
	return zone{}, false
}

func findButton(l layout, key string) (zone, bool) {
	for i, b := range l.buttons {
		if b.key == key {
			if z, ok := findZone(l, zoneButton, i); ok {
				return z, true
			}
		}
	}
	return zone{}, false
}

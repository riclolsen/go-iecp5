package main

import (
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

// pump drains everything the session has produced, with a deadline, so a test
// can wait for the device to answer without sleeping blindly.
func pump(t *testing.T, m *Model, until func() bool, deadline time.Duration) bool {
	t.Helper()
	stop := time.Now().Add(deadline)
	for time.Now().Before(stop) {
		if until() {
			return true
		}
		select {
		case msg := <-m.conn.out:
			m.now = time.Now()
			m.Update(msg)
		case <-time.After(20 * time.Millisecond):
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

func TestCommandRoundTrip(t *testing.T) {
	m, stop := newTestModel(t)
	defer stop()

	if !pump(t, m, func() bool { return m.active }, 10*time.Second) {
		t.Fatal("never became active")
	}
	press(t, m, "i")
	if !pump(t, m, func() bool { return hasPoint(m, demoCmdSingle) }, 10*time.Second) {
		t.Fatal("the interrogation reply never included the command point")
	}

	// Select the single-point command point and turn it on, with the
	// confirmation dialog in the way.
	m.screen = ScreenPoints
	if !selectPoint(m, demoCmdSingle) {
		t.Fatalf("command point %d not in the table", demoCmdSingle)
	}
	m.sbo = false // direct execute, so one command completes the round trip

	m.HandleKey("o")
	if m.modal.kind == modalNone {
		t.Fatal("a command must open a confirmation dialog")
	}
	if !strings.Contains(strings.Join(m.modal.lines, " "), "single command ON") {
		t.Fatalf("the dialog must name what will be sent: %v", m.modal.lines)
	}

	_, cmd := m.HandleKey("enter")
	if cmd == nil {
		t.Fatal("confirming must produce a command")
	}
	if msg := cmd(); msg != nil {
		m.Update(msg)
	}

	// The outstation echoes the new state as return information.
	if !pump(t, m, func() bool {
		p, ok := m.points[pointKey{CA: 1, IOA: demoCmdSingle}]
		return ok && p.Value == "ON" && p.Cause == asdu.ReturnInfoRemote
	}, 10*time.Second) {
		p := m.points[pointKey{CA: 1, IOA: demoCmdSingle}]
		t.Fatalf("command was not reflected back: %+v", p)
	}
	if m.cmdSent == 0 {
		t.Fatal("the command counter did not move")
	}
}

func TestSetpointPromptAndCancel(t *testing.T) {
	m, stop := newTestModel(t)
	defer stop()
	if !pump(t, m, func() bool { return m.active }, 10*time.Second) {
		t.Fatal("never became active")
	}
	press(t, m, "i")
	if !pump(t, m, func() bool { return hasPoint(m, demoCmdSetpt) }, 10*time.Second) {
		t.Fatal("the interrogation reply never included the setpoint point")
	}

	m.screen = ScreenPoints
	if !selectPoint(m, demoCmdSetpt) {
		t.Fatalf("setpoint point %d not in the table", demoCmdSetpt)
	}
	m.sbo = false

	m.HandleKey("b")
	if !m.prompt.active {
		t.Fatal("b must open the setpoint prompt")
	}
	for _, k := range []string{"4", "2", ".", "5", "f"} {
		m.HandleKey(k)
	}
	if m.prompt.input != "42.5f" {
		t.Fatalf("prompt input = %q", m.prompt.input)
	}
	m.HandleKey("enter")
	if m.modal.kind == modalNone {
		t.Fatal("a setpoint must be confirmed before it is sent")
	}
	if !strings.Contains(strings.Join(m.modal.lines, " "), "42.5") {
		t.Fatalf("the dialog must name the value: %v", m.modal.lines)
	}

	// Cancelling must send nothing.
	before := m.cmdSent
	m.HandleKey("esc")
	if m.modal.kind != modalNone {
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
	if m.modal.kind == modalNone && !m.prompt.active {
		t.Fatal("clicking a selected point must open its command dialog")
	}
	m.HandleKey("esc")
	m.closePrompt()

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

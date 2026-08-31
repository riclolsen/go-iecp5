package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Export exists because the answer to "what is this device reporting" usually
// has to leave the terminal: it goes into a commissioning report, an email to
// the vendor, or a diff against yesterday. Writing what is on screen — after
// the filter and the sort, not before — means the operator exports the view
// they were looking at rather than something they have to reconstruct.

func (m *Model) export() tea.Cmd {
	var name string
	var rows [][]string
	stamp := time.Now().Format("20060102-150405")

	switch m.screen {
	case ScreenPoints, ScreenOverview:
		name = "iec104-points-" + stamp + ".csv"
		rows = append(rows, []string{"common_address", "ioa", "type", "value",
			"quality", "cause", "timestamp", "received", "updates"})
		for _, p := range m.visiblePoints() {
			rows = append(rows, []string{
				fmt.Sprint(p.Key.CA), fmt.Sprint(p.Key.IOA), typeName(p.Type),
				p.Value, qualityText(p.Qds, p.HasQds), causeName(p.Cause),
				stampCSV(p.Stamp), p.Updated.Format(time.RFC3339Nano),
				fmt.Sprint(p.Updates),
			})
		}

	case ScreenEvents:
		name = "iec104-events-" + stamp + ".csv"
		rows = append(rows, []string{"received", "common_address", "ioa", "type",
			"value", "quality", "cause", "timestamp"})
		for _, e := range m.visibleEvents() {
			rows = append(rows, []string{
				e.At.Format(time.RFC3339Nano), fmt.Sprint(e.Key.CA), fmt.Sprint(e.Key.IOA),
				typeName(e.Type), e.Value, qualityText(e.Qds, e.HasQ),
				causeName(e.Cause), stampCSV(e.Stamp),
			})
		}

	case ScreenLog:
		name = "iec104-log-" + stamp + ".csv"
		rows = append(rows, []string{"time", "level", "message"})
		for _, l := range m.visibleLogs() {
			rows = append(rows, []string{l.At.Format(time.RFC3339Nano), l.Level, l.Text})
		}

	case ScreenFiles:
		name = "iec104-files-" + stamp + ".csv"
		rows = append(rows, []string{"ioa", "name_of_file", "size", "modified", "status", "local"})
		for _, f := range m.files.rows {
			rows = append(rows, []string{
				fmt.Sprint(f.Ioa), nofName(f.Nof), fmt.Sprint(f.Size),
				stampCSV(f.Time), f.Status, f.Local,
			})
		}

	default:
		return func() tea.Msg {
			return commandResultMsg{text: "nothing to export from this screen"}
		}
	}

	if len(rows) == 1 {
		return func() tea.Msg {
			return commandResultMsg{text: "nothing to export — the list is empty"}
		}
	}

	return func() tea.Msg {
		var b strings.Builder
		w := csv.NewWriter(&b)
		if err := w.WriteAll(rows); err != nil {
			return commandResultMsg{text: "export failed: " + err.Error()}
		}
		if err := os.WriteFile(name, []byte(b.String()), 0o644); err != nil {
			return commandResultMsg{text: "export failed: " + err.Error()}
		}
		return commandResultMsg{
			text: fmt.Sprintf("wrote %d rows to %s", len(rows)-1, name),
			ok:   true,
		}
	}
}

func stampCSV(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

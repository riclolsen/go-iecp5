package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/filetransfer"
)

// The Files screen is the IEC 60870-5-101/104 file transfer service: call the
// directory, pick a file, and pull it across. A transfer takes as long as it
// takes and holds the session while it runs, so it is one deliberate keypress
// rather than something that happens on arrival at the screen.

// defaultDownloadDir is where completed transfers are written.
const defaultDownloadDir = "iec104-files"

// fileRow is one entry of the device's directory.
type fileRow struct {
	Ioa    uint
	Nof    uint16
	Size   uint32
	Time   time.Time
	Status string
	Local  string
}

type filesState struct {
	rows   []fileRow
	listed bool
	dir    string
}

func newFilesState() filesState {
	return filesState{dir: defaultDownloadDir}
}

func (f *filesState) rowCount() int { return len(f.rows) }

func (f *filesState) clear() {
	f.rows = nil
	f.listed = false
}

// applyDirectory replaces the listing, keeping the status of files already
// fetched in this session.
func (f *filesState) applyDirectory(entries []asdu.DirectoryInfo) {
	prev := make(map[[2]uint]fileRow, len(f.rows))
	for _, r := range f.rows {
		prev[[2]uint{r.Ioa, uint(r.Nof)}] = r
	}

	f.rows = f.rows[:0]
	f.listed = true
	for _, e := range entries {
		row := fileRow{
			Ioa: uint(e.Ioa), Nof: uint16(e.Nof),
			Size: e.LengthOfFile, Time: e.Time, Status: "on device",
		}
		if e.Sof.IsDirectory {
			row.Status = "subdirectory"
		}
		if old, ok := prev[[2]uint{row.Ioa, uint(row.Nof)}]; ok && old.Local != "" {
			row.Status, row.Local = old.Status, old.Local
		}
		f.rows = append(f.rows, row)
	}
}

// applyDone records a completed transfer, adding a row for a file the device
// announced without a directory call.
func (f *filesState) applyDone(msg fileDoneMsg) {
	status, local := "saved "+msg.path, msg.path
	if msg.err != nil {
		status, local = "failed: "+msg.err.Error(), ""
	}
	for i := range f.rows {
		if f.rows[i].Ioa == msg.ioa && f.rows[i].Nof == msg.nof {
			f.rows[i].Status, f.rows[i].Local = status, local
			if f.rows[i].Size == 0 {
				f.rows[i].Size = uint32(msg.size)
			}
			return
		}
	}
	f.rows = append(f.rows, fileRow{
		Ioa: msg.ioa, Nof: msg.nof, Size: uint32(msg.size),
		Time: time.Now(), Status: status, Local: local,
	})
}

func (f *filesState) selected(cursor int) (fileRow, bool) {
	if cursor < 0 || cursor >= len(f.rows) {
		return fileRow{}, false
	}
	return f.rows[cursor], true
}

// handleFilesKey claims the keys the Files screen needs before the global
// bindings see them.
func (m *Model) handleFilesKey(key string) (tea.Model, tea.Cmd, bool) {
	switch key {
	case "l":
		if !m.requireActive() {
			return m, nil, true
		}
		m.addLog("tx", "call file directory (F_SC_NA_1)")
		return m, m.conn.fileDirectory(), true

	case "w":
		row, ok := m.files.selected(m.cursor[ScreenFiles])
		if !ok || row.Local == "" {
			m.toast.show("warn", "nothing saved for that row yet", m.now)
			return m, nil, true
		}
		m.toast.show("info", "saved at "+row.Local, m.now)
		return m, nil, true
	}
	return m, nil, false
}

// fetchSelectedFile starts a transfer of the highlighted directory entry.
func (m *Model) fetchSelectedFile() (tea.Model, tea.Cmd) {
	if !m.requireActive() {
		return m, nil
	}
	row, ok := m.files.selected(m.cursor[ScreenFiles])
	if !ok {
		m.toast.show("warn", "no file selected — press l to call the directory", m.now)
		return m, nil
	}
	for i := range m.files.rows {
		if m.files.rows[i].Ioa == row.Ioa && m.files.rows[i].Nof == row.Nof {
			m.files.rows[i].Status = "transferring…"
		}
	}
	m.addLog("tx", fmt.Sprintf("select file (F_SC_NA_1) ioa=%d %s", row.Ioa, nofName(row.Nof)))
	return m, m.conn.fetchFile(row.Ioa, row.Nof)
}

func (m *Model) requireActive() bool {
	if !m.active {
		m.toast.show("warn", "data transfer is not active — press a for STARTDT", m.now)
		return false
	}
	return true
}

// saveFile writes a completed transfer to the download directory.
func saveFile(dir string, e filetransfer.Entry, data []byte) (string, error) {
	if dir == "" {
		dir = defaultDownloadDir
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("ioa%d-%s-%s.bin", e.Ioa, nofName(uint16(e.Nof)),
		time.Now().Format("20060102-150405"))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// nofName renders the name of file, naming the values the standard predefines.
func nofName(nof uint16) string {
	switch asdu.NameOfFile(nof) {
	case asdu.FileDefault:
		return "default"
	case asdu.FileTransparent:
		return "transparent"
	case asdu.FileDisturbanceData:
		return "disturbance"
	case asdu.FileSequencesOfEvents:
		return "events"
	case asdu.FileSequencesOfAnalogues:
		return "analogues"
	default:
		return strconv.FormatUint(uint64(nof), 10)
	}
}

// fmtBytes renders a file size the way a person reads one.
func fmtBytes(n uint32) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
	}
}

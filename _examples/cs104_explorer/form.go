package main

import (
	"fmt"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"
)

// The connection editor exists because a common address read off a drawing is
// a guess until something answers, and restarting the tool to try 2 instead
// of 1 is how ten minutes of commissioning becomes an afternoon.

type formField struct {
	label string
	value string
	hint  string
}

type formState struct {
	active bool
	title  string
	fields []formField
	cursor int
	offset int
	err    string
}

const (
	fieldAddress = iota
	fieldCommonAddr
	fieldOriginator
	fieldTimeout
	fieldReconnect
	numFields
)

func (m *Model) openConnectionForm() {
	lk := m.conn.lk
	m.form = formState{
		active: true,
		title:  "Connection",
		fields: []formField{
			{label: "Address", value: lk.address(), hint: "host:port, or demo"},
			{label: "Common address (ASDU)", value: strconv.Itoa(int(lk.CommonAddr)), hint: "1..65534, 65535 broadcast"},
			{label: "Originator address", value: strconv.Itoa(int(lk.Originator)), hint: "0..255, 0 when unused"},
			{label: "Connect timeout", value: lk.Timeout.String(), hint: "t0, e.g. 30s"},
			{label: "Reconnect interval", value: lk.Reconnect.String(), hint: "e.g. 10s"},
		},
	}
}

func (m *Model) handleFormKey(key string) (tea.Model, tea.Cmd) {
	f := &m.form
	switch key {
	case "esc":
		m.form = formState{}
		return m, nil

	case "enter":
		return m.applyConnectionForm()

	case "tab", "down":
		f.cursor = (f.cursor + 1) % len(f.fields)
	case "shift+tab", "up":
		f.cursor = (f.cursor + len(f.fields) - 1) % len(f.fields)

	case "backspace":
		v := []rune(f.fields[f.cursor].value)
		if len(v) > 0 {
			f.fields[f.cursor].value = string(v[:len(v)-1])
		}
	case "ctrl+u":
		f.fields[f.cursor].value = ""
	case " ", "space":
		f.fields[f.cursor].value += " "

	default:
		if len([]rune(key)) == 1 {
			f.fields[f.cursor].value += key
		}
	}
	return m, nil
}

// applyConnectionForm validates every field before touching the session: a
// half-applied connection is worse than none.
func (m *Model) applyConnectionForm() (tea.Model, tea.Cmd) {
	f := &m.form
	next := m.conn.lk

	if err := parseAddress(f.fields[fieldAddress].value, &next); err != nil {
		f.err = err.Error()
		f.cursor = fieldAddress
		return m, nil
	}
	ca, err := parseUint(f.fields[fieldCommonAddr].value, 16)
	if err != nil {
		f.err = "common address: " + err.Error()
		f.cursor = fieldCommonAddr
		return m, nil
	}
	if ca == 0 {
		f.err = "common address: 0 is not used"
		f.cursor = fieldCommonAddr
		return m, nil
	}
	oa, err := parseUint(f.fields[fieldOriginator].value, 8)
	if err != nil {
		f.err = "originator address: " + err.Error()
		f.cursor = fieldOriginator
		return m, nil
	}
	to, err := parseDurationField(f.fields[fieldTimeout].value, "connect timeout")
	if err != nil {
		f.err = err.Error()
		f.cursor = fieldTimeout
		return m, nil
	}
	rc, err := parseDurationField(f.fields[fieldReconnect].value, "reconnect interval")
	if err != nil {
		f.err = err.Error()
		f.cursor = fieldReconnect
		return m, nil
	}

	next.CommonAddr = uint16(ca)
	next.Originator = byte(oa)
	next.Timeout, next.Reconnect = to, rc

	// Pointing somewhere new drops the point table with it: those
	// measurements came from a different device.
	if !m.conn.lk.sameDevice(next) {
		m.points = map[pointKey]*pointState{}
		m.pointsOrder = nil
		m.events = nil
		m.files.clear()
		m.addLog("info", "point table cleared: now addressing "+next.target())
	}

	m.form = formState{}
	m.connected, m.active = false, false
	m.status = "connecting"
	m.addLog("info", fmt.Sprintf("reconnecting to %s, common address %d",
		next.target(), next.CommonAddr))
	return m, m.conn.reconnect(next)
}

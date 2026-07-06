package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/cs104"
)

// bridge lets background client goroutines push messages into the Bubble Tea
// program. The program pointer is set once, after the program is created.
type bridge struct {
	mu   sync.Mutex
	prog *tea.Program
}

func (b *bridge) setProgram(p *tea.Program) {
	b.mu.Lock()
	b.prog = p
	b.mu.Unlock()
}

func (b *bridge) send(msg tea.Msg) {
	b.mu.Lock()
	p := b.prog
	b.mu.Unlock()
	if p != nil {
		p.Send(msg)
	}
}

// logProvider routes the cs104/clog protocol logs into the TUI log panel.
// Registered with Client.SetLogProvider so nothing is written to stdout
// (which would corrupt the alternate-screen UI).
type logProvider struct{ b *bridge }

func (l *logProvider) Critical(f string, v ...interface{}) {
	l.b.send(logMsg{"[C] " + fmt.Sprintf(f, v...)})
}
func (l *logProvider) Error(f string, v ...interface{}) {
	l.b.send(logMsg{"[E] " + fmt.Sprintf(f, v...)})
}
func (l *logProvider) Warn(f string, v ...interface{}) {
	l.b.send(logMsg{"[W] " + fmt.Sprintf(f, v...)})
}
func (l *logProvider) Debug(f string, v ...interface{}) {
	l.b.send(logMsg{"[D] " + fmt.Sprintf(f, v...)})
}

// tuiHandler implements cs104.ClientHandlerInterface. Every received ASDU is
// summarized to the log and, when it carries monitor data, decoded into
// points that are pushed to the UI.
type tuiHandler struct{ b *bridge }

func (h *tuiHandler) ASDUHandlerAll(_ asdu.Connect, a *asdu.ASDU, _ *cs104.Server, _ int) error {
	h.b.send(logMsg{"[rx] " + a.String()})
	if pts := extractPoints(a); len(pts) > 0 {
		h.b.send(pointsMsg{pts})
	}
	return nil
}

func (h *tuiHandler) ASDUHandler(asdu.Connect, *asdu.ASDU, *cs104.Server, int) error { return nil }
func (h *tuiHandler) InterrogationHandler(asdu.Connect, *asdu.ASDU) error            { return nil }
func (h *tuiHandler) CounterInterrogationHandler(asdu.Connect, *asdu.ASDU) error     { return nil }
func (h *tuiHandler) ReadHandler(asdu.Connect, *asdu.ASDU) error                     { return nil }
func (h *tuiHandler) TestCommandHandler(asdu.Connect, *asdu.ASDU) error              { return nil }
func (h *tuiHandler) ClockSyncHandler(asdu.Connect, *asdu.ASDU) error                { return nil }
func (h *tuiHandler) ResetProcessHandler(asdu.Connect, *asdu.ASDU) error             { return nil }
func (h *tuiHandler) DelayAcquisitionHandler(asdu.Connect, *asdu.ASDU) error         { return nil }

// point is one row in the monitored-points table.
type point struct {
	IOA     uint
	Type    string
	Value   string
	Quality string
	Cause   string
	Time    string
	Count   int
}

func typeName(t asdu.TypeID) string {
	s := t.String()
	s = strings.TrimPrefix(s, "TID<")
	return strings.TrimSuffix(s, ">")
}

func causeName(c asdu.CauseOfTransmission) string {
	s := c.String()
	s = strings.TrimPrefix(s, "COT<")
	return strings.TrimSuffix(s, ">")
}

func boolStr(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func tsStr(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("15:04:05.000")
}

// extractPoints decodes the monitor-direction information objects of an ASDU
// into displayable points. Control/system ASDUs yield no points.
func extractPoints(a *asdu.ASDU) []point {
	tid := typeName(a.Type)
	cause := causeName(a.Coa)
	mk := func(ioa asdu.InfoObjAddr, val, qual string, t time.Time) point {
		return point{IOA: uint(ioa), Type: tid, Value: val, Quality: qual, Cause: cause, Time: tsStr(t)}
	}

	var out []point
	switch a.Type {
	case asdu.M_SP_NA_1, asdu.M_SP_TA_1, asdu.M_SP_TB_1:
		for _, p := range a.GetSinglePoint() {
			out = append(out, mk(p.Ioa, boolStr(p.Value), p.Qds.String(), p.Time))
		}
	case asdu.M_DP_NA_1, asdu.M_DP_TA_1, asdu.M_DP_TB_1:
		for _, p := range a.GetDoublePoint() {
			out = append(out, mk(p.Ioa, p.Value.String(), p.Qds.String(), p.Time))
		}
	case asdu.M_ST_NA_1, asdu.M_ST_TA_1, asdu.M_ST_TB_1:
		for _, p := range a.GetStepPosition() {
			v := fmt.Sprintf("%d", p.Value.Val)
			if p.Value.HasTransient {
				v += " (transient)"
			}
			out = append(out, mk(p.Ioa, v, p.Qds.String(), p.Time))
		}
	case asdu.M_BO_NA_1, asdu.M_BO_TA_1, asdu.M_BO_TB_1:
		for _, p := range a.GetBitString32() {
			out = append(out, mk(p.Ioa, fmt.Sprintf("0x%08X", p.Value), p.Qds.String(), p.Time))
		}
	case asdu.M_ME_NA_1, asdu.M_ME_TA_1, asdu.M_ME_TD_1, asdu.M_ME_ND_1:
		for _, p := range a.GetMeasuredValueNormal() {
			out = append(out, mk(p.Ioa, fmt.Sprintf("%.5f", p.Value.Float64()), p.Qds.String(), p.Time))
		}
	case asdu.M_ME_NB_1, asdu.M_ME_TB_1, asdu.M_ME_TE_1:
		for _, p := range a.GetMeasuredValueScaled() {
			out = append(out, mk(p.Ioa, fmt.Sprintf("%d", p.Value), p.Qds.String(), p.Time))
		}
	case asdu.M_ME_NC_1, asdu.M_ME_TC_1, asdu.M_ME_TF_1:
		for _, p := range a.GetMeasuredValueFloat() {
			out = append(out, mk(p.Ioa, fmt.Sprintf("%g", p.Value), p.Qds.String(), p.Time))
		}
	case asdu.M_IT_NA_1, asdu.M_IT_TA_1, asdu.M_IT_TB_1:
		for _, p := range a.GetIntegratedTotals() {
			v := fmt.Sprintf("%d (seq %d)", p.Value.CounterReading, p.Value.SeqNumber)
			q := make([]string, 0, 3)
			if p.Value.IsInvalid {
				q = append(q, "IV")
			}
			if p.Value.HasCarry {
				q = append(q, "CY")
			}
			if p.Value.IsAdjusted {
				q = append(q, "CA")
			}
			out = append(out, mk(p.Ioa, v, strings.Join(q, ","), p.Time))
		}
	}
	return out
}

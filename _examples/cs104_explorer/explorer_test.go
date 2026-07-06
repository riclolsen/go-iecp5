package main

import (
	"io"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/cs104"
)

// testSrv is a minimal IEC 104 outstation used by the end-to-end test.
type testSrv struct{}

func (testSrv) InterrogationHandler(c asdu.Connect, pack *asdu.ASDU, _ asdu.QualifierOfInterrogation) error {
	_ = pack.SendReplyMirror(c, asdu.ActivationCon)
	coa := asdu.CauseOfTransmission{Cause: asdu.InterrogatedByStation}
	_ = asdu.Single(c, false, coa, pack.CommonAddr,
		asdu.SinglePointInfo{Ioa: 100, Value: true, Qds: asdu.QDSGood})
	_ = asdu.MeasuredValueFloat(c, false, coa, pack.CommonAddr,
		asdu.MeasuredValueFloatInfo{Ioa: 400, Value: 21.5, Qds: asdu.QDSGood})
	return pack.SendReplyMirror(c, asdu.ActivationTerm)
}
func (testSrv) CounterInterrogationHandler(asdu.Connect, *asdu.ASDU, asdu.QualifierCountCall) error {
	return nil
}
func (testSrv) ReadHandler(asdu.Connect, *asdu.ASDU, asdu.InfoObjAddr) error { return nil }
func (testSrv) ClockSyncHandler(asdu.Connect, *asdu.ASDU, time.Time) error   { return nil }
func (testSrv) ResetProcessHandler(asdu.Connect, *asdu.ASDU, asdu.QualifierOfResetProcessCmd) error {
	return nil
}
func (testSrv) DelayAcquisitionHandler(asdu.Connect, *asdu.ASDU, uint16) error { return nil }
func (testSrv) ASDUHandler(asdu.Connect, *asdu.ASDU) error                     { return nil }
func (testSrv) ASDUHandlerAll(asdu.Connect, *asdu.ASDU, int) error             { return nil }

// TestExplorerEndToEnd drives the TUI headlessly: it feeds the connect and
// general-interrogation keystrokes and checks that the interrogation response
// is decoded into the points table.
func TestExplorerEndToEnd(t *testing.T) {
	const addr = "127.0.0.1:23404"

	srv := cs104.NewServer(testSrv{})
	go func() { _ = srv.ListenAndServer(addr) }()
	defer srv.Close()
	time.Sleep(250 * time.Millisecond)

	pr, pw := io.Pipe()
	b := &bridge{}
	m := initialModel(b)
	m.addr = addr

	p := tea.NewProgram(m, tea.WithInput(pr), tea.WithoutRenderer())
	b.setProgram(p)

	done := make(chan tea.Model, 1)
	go func() {
		fm, err := p.Run()
		if err != nil {
			t.Errorf("program error: %v", err)
		}
		done <- fm
	}()

	time.Sleep(200 * time.Millisecond)
	_, _ = pw.Write([]byte("c")) // connect (auto STARTDT)
	time.Sleep(900 * time.Millisecond)
	_, _ = pw.Write([]byte("g")) // general interrogation
	time.Sleep(900 * time.Millisecond)
	_, _ = pw.Write([]byte("q")) // quit

	var fm tea.Model
	select {
	case fm = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("program did not exit")
	}

	mm, ok := fm.(model)
	if !ok {
		t.Fatalf("unexpected final model type %T", fm)
	}
	if mm.client != nil {
		_ = mm.client.Close()
	}
	if len(mm.points) == 0 {
		t.Fatalf("expected decoded points after interrogation, got none.\nlogs:\n%s", joinLogs(mm.logs))
	}
	if p := mm.points[100]; p.Type != "M_SP_NA_1" || p.Value != "on" {
		t.Fatalf("point 100 = %+v, want M_SP_NA_1 / on", p)
	}
	if p := mm.points[400]; p.Type != "M_ME_NC_1" {
		t.Fatalf("point 400 type = %q, want M_ME_NC_1", p.Type)
	}
}

func joinLogs(logs []string) string {
	out := ""
	for _, l := range logs {
		out += l + "\n"
	}
	return out
}

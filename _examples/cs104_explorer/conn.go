package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/cs104"
	"github.com/riclolsen/go-iecp5/filetransfer"
)

// The session runs in its own goroutine and never touches the model. Every
// action is a tea.Cmd that returns a result message, and everything the
// device says arrives on one channel that the model drains with wait().

type statusMsg struct {
	connected bool
	active    bool
	note      string
}

type updateMsg struct {
	at      time.Time
	rows    []pointUpdate
	summary string
	// feedback is set when the ASDU is a mirrored control-direction reply,
	// which is how a command reports what became of it.
	feedback *cmdFeedback
}

// cmdFeedback is one mirrored reply to a command: which command, and what the
// outstation said about it.
type cmdFeedback struct {
	Type     asdu.TypeID
	CA       uint16
	IOA      uint
	Cause    asdu.Cause
	Negative bool
}

type logLineMsg struct{ level, text string }

type commandResultMsg struct {
	text string
	ok   bool
}

type directoryMsg struct{ entries []asdu.DirectoryInfo }

type fileDoneMsg struct {
	ioa  uint
	nof  uint16
	size int
	path string
	err  error
}

// pointUpdate is one information object decoded into what the table shows.
type pointUpdate struct {
	Key    pointKey
	Type   asdu.TypeID
	Cause  asdu.Cause
	Value  string
	Num    float64
	HasNum bool
	Qds    asdu.QualityDescriptor
	HasQds bool
	// IsQdp marks a protection equipment descriptor (QDP), whose flags are
	// named differently from a measured value's.
	IsQdp bool
	Stamp time.Time
}

// connection owns the cs104 client and the file transfer receiver.
type connection struct {
	mu   sync.Mutex
	lk   link
	cli  *cs104.Client
	recv *filetransfer.Receiver
	demo *demoServer

	out         chan tea.Msg
	verboseFlag bool
	downloadDir string
}

func newConnection(lk link, downloadDir string) *connection {
	return &connection{
		lk:          lk,
		out:         make(chan tea.Msg, 512),
		downloadDir: downloadDir,
	}
}

func (c *connection) target() string { return c.lk.target() }

func (c *connection) params() *asdu.Params {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cli != nil {
		return c.cli.Params()
	}
	return asdu.ParamsWide
}

func (c *connection) verbose() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.verboseFlag
}

func (c *connection) setVerbose(v bool) {
	c.mu.Lock()
	c.verboseFlag = v
	if c.cli != nil {
		c.cli.LogMode(v)
	}
	c.mu.Unlock()
}

// push delivers a message to the UI, dropping it rather than blocking a
// protocol goroutine when the interface is behind.
func (c *connection) push(msg tea.Msg) {
	select {
	case c.out <- msg:
	default:
	}
}

// wait is the command that pulls the next message from the session.
func (c *connection) wait() tea.Cmd {
	return func() tea.Msg { return <-c.out }
}

// start brings the session up, including the in-process outstation in demo
// mode. It returns an error only for a setup problem; a device that is simply
// not answering is reported through the status messages.
func (c *connection) start() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.startLocked()
}

func (c *connection) startLocked() error {
	host := c.lk.Host
	if c.lk.Demo {
		d, err := startDemoServer()
		if err != nil {
			return fmt.Errorf("demo outstation: %w", err)
		}
		c.demo = d
		host = d.addr
	}

	opt := cs104.NewOption()
	if err := opt.AddRemoteServer(host); err != nil {
		return fmt.Errorf("server address: %w", err)
	}
	opt.SetAutoReconnect(true).SetReconnectInterval(c.lk.Reconnect)
	cfg := cs104.DefaultConfig()
	cfg.ConnectTimeout0 = c.lk.Timeout
	_ = opt.SetConfig(cfg)

	recv := filetransfer.NewReceiver(nil)
	recv.SetAutoAccept(true)
	dir := c.downloadDir
	recv.SetDirectoryHandler(func(_ asdu.CommonAddr, d []asdu.DirectoryInfo) {
		c.push(directoryMsg{entries: d})
	})
	recv.SetFileHandler(func(e filetransfer.Entry, data []byte) {
		path, err := saveFile(dir, e, data)
		c.push(fileDoneMsg{ioa: uint(e.Ioa), nof: uint16(e.Nof),
			size: len(data), path: path, err: err})
	})
	c.recv = recv

	cli := cs104.NewClient(&sessionHandler{c: c}, opt)
	cli.SetLogProvider(&sessionLog{c: c})
	cli.LogMode(c.verboseFlag)

	cli.SetOnConnectHandler(func(k *cs104.Client) {
		c.push(statusMsg{connected: true, note: "TCP connected, sending STARTDT"})
		k.SendStartDt()
	})
	cli.SetOnActivatedHandler(func(*cs104.Client) {
		c.push(statusMsg{connected: true, active: true, note: "data transfer active"})
	})
	cli.SetOnDeactivatedHandler(func(*cs104.Client) {
		c.push(statusMsg{connected: true, active: false, note: "data transfer stopped"})
	})
	cli.SetConnectionLostHandler(func(*cs104.Client) {
		c.push(statusMsg{})
	})
	cli.SetConnectTimeoutHandler(func(*cs104.Client) {
		c.push(logLineMsg{level: "warn", text: "connect attempt timed out"})
	})

	c.cli = cli
	return cli.Start()
}

// stop tears the session down, including the demo outstation.
func (c *connection) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopLocked()
}

func (c *connection) stopLocked() {
	if c.cli != nil {
		_ = c.cli.Close()
		c.cli = nil
	}
	if c.demo != nil {
		c.demo.stop()
		c.demo = nil
	}
	if c.recv != nil {
		c.recv.Abort()
	}
}

// reconnect applies a new link setup by taking the session down and bringing
// another one up in its place.
func (c *connection) reconnect(lk link) tea.Cmd {
	return func() tea.Msg {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.stopLocked()
		c.lk = lk
		if err := c.startLocked(); err != nil {
			return commandResultMsg{text: "reconnect failed: " + err.Error()}
		}
		return commandResultMsg{text: "connecting to " + lk.target(), ok: true}
	}
}

// client returns the live client, or an error command when there is none.
func (c *connection) client() (*cs104.Client, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cli, c.cli != nil
}

func notConnected() tea.Msg {
	return commandResultMsg{text: "not connected"}
}

// act runs f against the live client and reports the outcome.
func (c *connection) act(what string, f func(*cs104.Client) error) tea.Cmd {
	return func() tea.Msg {
		cli, ok := c.client()
		if !ok {
			return notConnected()
		}
		if err := f(cli); err != nil {
			return commandResultMsg{text: what + " failed: " + err.Error()}
		}
		return commandResultMsg{text: what + " sent", ok: true}
	}
}

func (c *connection) startDT() tea.Cmd {
	return c.act("STARTDT", func(k *cs104.Client) error { k.SendStartDt(); return nil })
}

func (c *connection) stopDT() tea.Cmd {
	return c.act("STOPDT", func(k *cs104.Client) error { k.SendStopDt(); return nil })
}

func (c *connection) interrogation(qoi asdu.QualifierOfInterrogation) tea.Cmd {
	return c.act("general interrogation", func(k *cs104.Client) error {
		return k.InterrogationCmd(activation(), c.commonAddr(), qoi)
	})
}

func (c *connection) counterInterrogation() tea.Cmd {
	return c.act("counter interrogation", func(k *cs104.Client) error {
		return k.CounterInterrogationCmd(activation(), c.commonAddr(),
			asdu.QualifierCountCall{Request: asdu.QCCTotal, Freeze: asdu.QCCFrzRead})
	})
}

func (c *connection) clockSync() tea.Cmd {
	return c.act("clock synchronisation", func(k *cs104.Client) error {
		return k.ClockSynchronizationCmd(activation(), c.commonAddr(), time.Now())
	})
}

func (c *connection) testCommand() tea.Cmd {
	return c.act("test command", func(k *cs104.Client) error {
		return k.TestCommand(activation(), c.commonAddr())
	})
}

func (c *connection) resetProcess() tea.Cmd {
	return c.act("reset process", func(k *cs104.Client) error {
		return k.ResetProcessCmd(activation(), c.commonAddr(), asdu.QPRGeneralRest)
	})
}

func (c *connection) readCommand(ioa asdu.InfoObjAddr) tea.Cmd {
	return c.act(fmt.Sprintf("read command ioa=%d", ioa), func(k *cs104.Client) error {
		return k.ReadCmd(asdu.CauseOfTransmission{Cause: asdu.Request}, c.commonAddr(), ioa)
	})
}

func (c *connection) fileDirectory() tea.Cmd {
	return func() tea.Msg {
		cli, ok := c.client()
		if !ok {
			return notConnected()
		}
		if err := c.recv.RequestDirectory(cli, c.commonAddr()); err != nil {
			return commandResultMsg{text: "file directory failed: " + err.Error()}
		}
		return commandResultMsg{text: "file directory requested", ok: true}
	}
}

func (c *connection) fetchFile(ioa uint, nof uint16) tea.Cmd {
	return func() tea.Msg {
		cli, ok := c.client()
		if !ok {
			return notConnected()
		}
		err := c.recv.RequestFile(cli, c.commonAddr(),
			asdu.InfoObjAddr(ioa), asdu.NameOfFile(nof))
		if err != nil {
			return commandResultMsg{text: "file request failed: " + err.Error()}
		}
		return commandResultMsg{
			text: fmt.Sprintf("requested file ioa=%d %s", ioa, nofName(nof)), ok: true,
		}
	}
}

// sendCommand issues one process command in the control direction.
func (c *connection) sendCommand(op commandOp) tea.Cmd {
	return func() tea.Msg {
		cli, ok := c.client()
		if !ok {
			return notConnected()
		}
		if err := op.send(cli); err != nil {
			return commandResultMsg{text: op.describe() + " failed: " + err.Error()}
		}
		return commandResultMsg{text: op.describe() + " sent", ok: true}
	}
}

func (c *connection) commonAddr() asdu.CommonAddr {
	c.mu.Lock()
	defer c.mu.Unlock()
	return asdu.CommonAddr(c.lk.CommonAddr)
}

func activation() asdu.CauseOfTransmission {
	return asdu.CauseOfTransmission{Cause: asdu.Activation}
}

// ---------- handler ----------

// sessionLog routes the library's protocol log into the UI. Writing it to
// stdout would corrupt the alternate screen.
type sessionLog struct{ c *connection }

func (l *sessionLog) Critical(f string, v ...interface{}) {
	l.c.push(logLineMsg{"error", fmt.Sprintf(f, v...)})
}
func (l *sessionLog) Error(f string, v ...interface{}) {
	l.c.push(logLineMsg{"error", fmt.Sprintf(f, v...)})
}
func (l *sessionLog) Warn(f string, v ...interface{}) {
	l.c.push(logLineMsg{"warn", fmt.Sprintf(f, v...)})
}
func (l *sessionLog) Debug(f string, v ...interface{}) {
	l.c.push(logLineMsg{"debug", fmt.Sprintf(f, v...)})
}

// sessionHandler implements cs104.ClientHandlerInterface.
type sessionHandler struct{ c *connection }

func (h *sessionHandler) ASDUHandlerAll(_ asdu.Connect, a *asdu.ASDU, _ *cs104.Server, _ int) error {
	rows, summary := decodeASDU(a)
	h.c.push(updateMsg{at: time.Now(), rows: rows, summary: summary,
		feedback: commandFeedback(a)})
	return nil
}

// commandFeedback recognises a mirrored control-direction ASDU. The
// outstation answers a command by echoing its identifier with a new cause of
// transmission, so the reply is matched by type, common address and
// information object address.
func commandFeedback(a *asdu.ASDU) *cmdFeedback {
	if !isCommandType(a.Type) {
		return nil
	}
	switch a.Coa.Cause {
	case asdu.ActivationCon, asdu.ActivationTerm, asdu.DeactivationCon,
		asdu.UnknownTypeID, asdu.UnknownCOT, asdu.UnknownCA, asdu.UnknownIOA:
	default:
		return nil
	}
	return &cmdFeedback{
		Type:     a.Type,
		CA:       uint16(a.CommonAddr),
		IOA:      firstIOA(a),
		Cause:    a.Coa.Cause,
		Negative: a.Coa.IsNegative,
	}
}

// firstIOA reads the information object address of the first object without
// consuming the ASDU, which the typed getters would.
func firstIOA(a *asdu.ASDU) uint {
	n := a.InfoObjAddrSize
	if n <= 0 || len(a.InfoObj) < n {
		return 0
	}
	v := uint(0)
	for i := n - 1; i >= 0; i-- {
		v = v<<8 | uint(a.InfoObj[i])
	}
	return v
}

func (h *sessionHandler) ASDUHandler(c asdu.Connect, a *asdu.ASDU, _ *cs104.Server, _ int) error {
	if h.c.recv != nil {
		if handled, err := h.c.recv.Handle(c, a); handled && err != nil {
			h.c.push(logLineMsg{"warn", "file transfer: " + err.Error()})
		}
	}
	return nil
}

func (h *sessionHandler) InterrogationHandler(asdu.Connect, *asdu.ASDU) error        { return nil }
func (h *sessionHandler) CounterInterrogationHandler(asdu.Connect, *asdu.ASDU) error { return nil }
func (h *sessionHandler) ReadHandler(asdu.Connect, *asdu.ASDU) error                 { return nil }
func (h *sessionHandler) TestCommandHandler(asdu.Connect, *asdu.ASDU) error          { return nil }
func (h *sessionHandler) ClockSyncHandler(asdu.Connect, *asdu.ASDU) error            { return nil }
func (h *sessionHandler) ResetProcessHandler(asdu.Connect, *asdu.ASDU) error         { return nil }
func (h *sessionHandler) DelayAcquisitionHandler(asdu.Connect, *asdu.ASDU) error     { return nil }

// ---------- decoding ----------

// decodeASDU turns one received ASDU into table rows and a log summary.
func decodeASDU(a *asdu.ASDU) ([]pointUpdate, string) {
	ca := uint16(a.CommonAddr)
	cause := a.Coa.Cause
	mk := func(ioa asdu.InfoObjAddr, val string, num float64, hasNum bool,
		q asdu.QualityDescriptor, hasQ bool, t time.Time) pointUpdate {
		return pointUpdate{
			Key: pointKey{CA: ca, IOA: uint(ioa)}, Type: a.Type, Cause: cause,
			Value: val, Num: num, HasNum: hasNum, Qds: q, HasQds: hasQ, Stamp: t,
		}
	}

	var rows []pointUpdate
	switch a.Type {
	case asdu.M_SP_NA_1, asdu.M_SP_TA_1, asdu.M_SP_TB_1:
		for _, p := range a.GetSinglePoint() {
			rows = append(rows, mk(p.Ioa, onOffText(p.Value), boolNum(p.Value), true, p.Qds, true, p.Time))
		}
	case asdu.M_DP_NA_1, asdu.M_DP_TA_1, asdu.M_DP_TB_1:
		for _, p := range a.GetDoublePoint() {
			rows = append(rows, mk(p.Ioa, dpText(p.Value), float64(p.Value), true, p.Qds, true, p.Time))
		}
	case asdu.M_ST_NA_1, asdu.M_ST_TA_1, asdu.M_ST_TB_1:
		for _, p := range a.GetStepPosition() {
			v := fmt.Sprintf("%d", p.Value.Val)
			if p.Value.HasTransient {
				v += "T"
			}
			rows = append(rows, mk(p.Ioa, v, float64(p.Value.Val), true, p.Qds, true, p.Time))
		}
	case asdu.M_BO_NA_1, asdu.M_BO_TA_1, asdu.M_BO_TB_1:
		for _, p := range a.GetBitString32() {
			rows = append(rows, mk(p.Ioa, fmt.Sprintf("0x%08X", p.Value),
				float64(p.Value), true, p.Qds, true, p.Time))
		}
	case asdu.M_ME_NA_1, asdu.M_ME_TA_1, asdu.M_ME_TD_1, asdu.M_ME_ND_1:
		hasQ := a.Type != asdu.M_ME_ND_1
		for _, p := range a.GetMeasuredValueNormal() {
			f := p.Value.Float64()
			rows = append(rows, mk(p.Ioa, fmt.Sprintf("%.5f", f), f, true, p.Qds, hasQ, p.Time))
		}
	case asdu.M_ME_NB_1, asdu.M_ME_TB_1, asdu.M_ME_TE_1:
		for _, p := range a.GetMeasuredValueScaled() {
			rows = append(rows, mk(p.Ioa, fmt.Sprintf("%d", p.Value),
				float64(p.Value), true, p.Qds, true, p.Time))
		}
	case asdu.M_ME_NC_1, asdu.M_ME_TC_1, asdu.M_ME_TF_1:
		for _, p := range a.GetMeasuredValueFloat() {
			rows = append(rows, mk(p.Ioa, trimFloat(float64(p.Value)),
				float64(p.Value), true, p.Qds, true, p.Time))
		}
	case asdu.M_IT_NA_1, asdu.M_IT_TA_1, asdu.M_IT_TB_1:
		for _, p := range a.GetIntegratedTotals() {
			v := fmt.Sprintf("%d", p.Value.CounterReading)
			q := asdu.QDSGood
			if p.Value.IsInvalid {
				q |= asdu.QDSInvalid
			}
			rows = append(rows, mk(p.Ioa, v, float64(p.Value.CounterReading), true, q, true, p.Time))
		}
	case asdu.M_PS_NA_1:
		for _, p := range a.GetPackedSinglePointWithSCD() {
			rows = append(rows, mk(p.Ioa, fmt.Sprintf("0x%08X", uint32(p.Scd)),
				float64(uint32(p.Scd)), true, p.Qds, true, time.Time{}))
		}

	case asdu.M_EP_TA_1, asdu.M_EP_TD_1:
		for _, p := range a.GetEventOfProtectionEquipment() {
			r := mk(p.Ioa, singleEventText(p.Event)+fmt.Sprintf(" %dms", p.Msec),
				float64(p.Event), true, asdu.QualityDescriptor(p.Qdp), true, p.Time)
			r.IsQdp = true
			rows = append(rows, r)
		}

	case asdu.M_EP_TB_1, asdu.M_EP_TE_1:
		p := a.GetPackedStartEventsOfProtectionEquipment()
		r := mk(p.Ioa, startEventText(p.Event)+fmt.Sprintf(" %dms", p.Msec),
			float64(p.Event), true, asdu.QualityDescriptor(p.Qdp), true, p.Time)
		r.IsQdp = true
		rows = append(rows, r)

	case asdu.M_EP_TC_1, asdu.M_EP_TF_1:
		p := a.GetPackedOutputCircuitInfo()
		r := mk(p.Ioa, outputCircuitText(p.Oci)+fmt.Sprintf(" %dms", p.Msec),
			float64(p.Oci), true, asdu.QualityDescriptor(p.Qdp), true, p.Time)
		r.IsQdp = true
		rows = append(rows, r)

	case asdu.M_EI_NA_1:
		ioa, coi := a.GetEndOfInitialization()
		rows = append(rows, mk(ioa, fmt.Sprintf("init cause %d", byte(coi.Cause)),
			0, false, asdu.QDSGood, false, time.Time{}))
	}

	return rows, summarize(a, len(rows))
}

// summarize is the one-line description of an ASDU for the log.
func summarize(a *asdu.ASDU, n int) string {
	var b strings.Builder
	b.WriteString(typeName(a.Type))
	b.WriteString("  ")
	b.WriteString(causeName(a.Coa.Cause))
	if a.Coa.IsNegative {
		b.WriteString(",neg")
	}
	if a.Coa.IsTest {
		b.WriteString(",test")
	}
	fmt.Fprintf(&b, "  ca=%d", a.CommonAddr)
	if n > 0 {
		fmt.Fprintf(&b, "  %s", plural(n, "object"))
	}
	return b.String()
}

// singleEventText names the state a protection event reports.
func singleEventText(e asdu.SingleEvent) string {
	switch e {
	case asdu.SEDeterminedOn:
		return "ON"
	case asdu.SEDeterminedOff:
		return "OFF"
	case asdu.SEIndeterminateOrIntermediate:
		return "INTERMED"
	default:
		return "INDETERM"
	}
}

// startEventText names which phases a protection start reports. The flags are
// a set, not an enumeration, so all of them are shown.
func startEventText(e asdu.StartEvent) string {
	var f []string
	for _, b := range []struct {
		bit  asdu.StartEvent
		name string
	}{
		{asdu.SEPGeneralStart, "GS"},
		{asdu.SEPStartL1, "L1"},
		{asdu.SEPStartL2, "L2"},
		{asdu.SEPStartL3, "L3"},
		{asdu.SEPStartEarthCurrent, "IE"},
		{asdu.SEPStartReverseDirection, "REV"},
	} {
		if e&b.bit != 0 {
			f = append(f, b.name)
		}
	}
	if len(f) == 0 {
		return "none"
	}
	return strings.Join(f, "|")
}

// outputCircuitText names which output circuits a protection relay drove.
func outputCircuitText(o asdu.OutputCircuitInfo) string {
	var f []string
	for _, b := range []struct {
		bit  asdu.OutputCircuitInfo
		name string
	}{
		{asdu.OCIGeneralCommand, "GC"},
		{asdu.OCICommandL1, "L1"},
		{asdu.OCICommandL2, "L2"},
		{asdu.OCICommandL3, "L3"},
	} {
		if o&b.bit != 0 {
			f = append(f, b.name)
		}
	}
	if len(f) == 0 {
		return "none"
	}
	return strings.Join(f, "|")
}

// dpText abbreviates a double point for a table cell.
func dpText(v asdu.DoublePoint) string {
	switch v {
	case asdu.DPIDeterminedOn:
		return "ON"
	case asdu.DPIDeterminedOff:
		return "OFF"
	case asdu.DPIIndeterminateOrIntermediate:
		return "INTERMED"
	default:
		return "INDETERM"
	}
}

func onOffText(v bool) string {
	if v {
		return "ON"
	}
	return "OFF"
}

func boolNum(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

// trimFloat prints a float without a wall of trailing zeros.
func trimFloat(f float64) string {
	s := fmt.Sprintf("%.3f", f)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	return s
}

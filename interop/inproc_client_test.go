package interop

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/cs104"
	"github.com/riclolsen/go-iecp5/filetransfer"
)

// selfCapabilities is what go-iecp5 declares for itself, in the vocabulary of
// print-capabilities, so the same `needs` column of the cases applies.
var selfCapabilities = capabilities{
	Features: map[string]bool{
		"generalInterrogation": true, "counterInterrogation": true, "read": true,
		"clockSync": true, "testCommand": true, "directCommands": true,
		"selectBeforeOperate": true, "commandDeactivation": true,
		"timeTaggedCommands": true, "spontaneousOnCommand": true,
		"apciParameters": true, "multipleConnections": true,
		"dataTransferEvents": true, "fileTransfer": true, "fileServer": true, "fileClient": true,
	},
}

const stepTimeout = 5 * time.Second

// masterHandler records every ASDU the client receives and feeds file
// transfer ASDUs to a Receiver.
type masterHandler struct {
	mu       sync.Mutex
	asdus    []*asdu.ASDU
	receiver *filetransfer.Receiver
	wake     chan struct{}
}

func newMasterHandler() *masterHandler {
	return &masterHandler{wake: make(chan struct{}, 1)}
}

func (h *masterHandler) ASDUHandlerAll(_ asdu.Connect, a *asdu.ASDU, _ *cs104.Server, _ int) error {
	h.mu.Lock()
	h.asdus = append(h.asdus, a)
	h.mu.Unlock()
	select {
	case h.wake <- struct{}{}:
	default:
	}
	return nil
}

func (h *masterHandler) ASDUHandler(c asdu.Connect, a *asdu.ASDU, _ *cs104.Server, _ int) error {
	if h.receiver != nil {
		_, _ = h.receiver.Handle(c, a)
	}
	return nil
}

func (h *masterHandler) InterrogationHandler(asdu.Connect, *asdu.ASDU) error        { return nil }
func (h *masterHandler) CounterInterrogationHandler(asdu.Connect, *asdu.ASDU) error { return nil }
func (h *masterHandler) ReadHandler(asdu.Connect, *asdu.ASDU) error                 { return nil }
func (h *masterHandler) TestCommandHandler(asdu.Connect, *asdu.ASDU) error          { return nil }
func (h *masterHandler) ClockSyncHandler(asdu.Connect, *asdu.ASDU) error            { return nil }
func (h *masterHandler) ResetProcessHandler(asdu.Connect, *asdu.ASDU) error         { return nil }
func (h *masterHandler) DelayAcquisitionHandler(asdu.Connect, *asdu.ASDU) error     { return nil }

func (h *masterHandler) snapshot() []*asdu.ASDU {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*asdu.ASDU(nil), h.asdus...)
}

// waitFor blocks until pred(received ASDUs) holds or the timeout passes.
func (h *masterHandler) waitFor(timeout time.Duration, pred func([]*asdu.ASDU) bool) bool {
	deadline := time.After(timeout)
	for {
		if pred(h.snapshot()) {
			return true
		}
		select {
		case <-h.wake:
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			return pred(h.snapshot())
		}
	}
}

// runInProcessClient performs one client operation of the container contract
// with go-iecp5's cs104.Client and describes the outcome in the contract's
// result document.
func runInProcessClient(t *testing.T, addr string, args []string) *result {
	t.Helper()
	res := runInProcessClientOp(t, addr, args)
	switch {
	case res.OK:
		res.exit = 0
	case res.Error != nil && res.Error.Code == "connect-failed":
		res.exit = 3
	default:
		res.exit = 1
	}
	return res
}

func runInProcessClientOp(t *testing.T, addr string, args []string) *result {
	t.Helper()
	op := args[0]
	args = args[1:]
	res := &result{Adapter: "go-iecp5", Operation: op}
	fail := func(code, msg string) *result {
		res.OK = false
		res.Error = &struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}{code, msg}
		return res
	}

	ca := asdu.CommonAddr(flagInt(args, "common-address", 1))
	orig := asdu.OriginAddr(flagInt(args, "originator-address", 0))

	h := newMasterHandler()
	opt := cs104.NewOption()
	if err := opt.AddRemoteServer(addr); err != nil {
		t.Fatal(err)
	}
	opt.SetAutoReconnect(false)
	cli := cs104.NewClient(h, opt)
	cli.LogMode(os.Getenv("IEC104_INTEROP_LOG") != "")
	connected := make(chan struct{})
	active := make(chan struct{})
	deactivated := make(chan struct{})
	lost := make(chan struct{})
	var once1, once2, once3, once4 sync.Once
	cli.SetOnConnectHandler(func(c *cs104.Client) {
		once1.Do(func() { close(connected) })
		c.SendStartDt()
	})
	cli.SetOnActivatedHandler(func(*cs104.Client) { once2.Do(func() { close(active) }) })
	cli.SetOnDeactivatedHandler(func(*cs104.Client) { once3.Do(func() { close(deactivated) }) })
	cli.SetConnectionLostHandler(func(*cs104.Client) { once4.Do(func() { close(lost) }) })
	if err := cli.Start(); err != nil {
		return fail("connect-failed", err.Error())
	}
	defer cli.Close()

	select {
	case <-connected:
		res.Connected = true
	case <-time.After(stepTimeout):
		return fail("connect-failed", "no TCP connection")
	}
	select {
	case <-active:
		res.StartdtConfirmed = true
	case <-time.After(stepTimeout):
		return fail("startdt-timeout", "no STARTDT con")
	}

	coa := func(c asdu.Cause) asdu.CauseOfTransmission { return asdu.CauseOfTransmission{Cause: c} }
	// All requests carry the originator address through the Connect's params
	// in other libraries; here it is part of the identifier, set after the
	// ASDU is built. The helpers of package asdu take no originator, so the
	// requests go through send().
	send := func(build func(c asdu.Connect) error) error {
		return build(&originatorConn{Client: cli, orig: orig})
	}

	// positive confirmation received (and, if wanted, termination).
	confirmed := func(reqType asdu.TypeID, n int, term bool) func([]*asdu.ASDU) bool {
		return func(as []*asdu.ASDU) bool {
			conf, trm := 0, false
			for _, a := range as {
				if a.Type != reqType {
					continue
				}
				switch {
				case a.Coa.IsNegative:
					return true
				case a.Coa.Cause == asdu.ActivationCon || a.Coa.Cause == asdu.DeactivationCon:
					conf++
				case a.Coa.Cause == asdu.ActivationTerm:
					trm = true
				case a.Coa.Cause >= asdu.UnknownTypeID && a.Coa.Cause <= asdu.UnknownIOA:
					return true
				}
			}
			return conf >= n && (!term || trm)
		}
	}
	succeed := func(reqType asdu.TypeID, ok bool) *result {
		got := h.snapshot()
		fillResult(res, got, reqType)
		if hasNegative(res) {
			return fail("negative-confirmation", "refused")
		}
		if !ok {
			return fail("timeout", "no answer in time")
		}
		res.OK = true
		return res
	}

	switch op {
	case "connect":
		cli.SendStopDt()
		select {
		case <-deactivated:
			res.StopdtConfirmed = true
			res.OK = true
			return res
		case <-time.After(stepTimeout):
			return fail("stopdt-timeout", "no STOPDT con")
		}

	case "interrogate":
		qoi := asdu.QualifierOfInterrogation(flagInt(args, "qoi", 20))
		if err := send(func(c asdu.Connect) error { return asdu.InterrogationCmd(c, coa(asdu.Activation), ca, qoi) }); err != nil {
			return fail("failed", err.Error())
		}
		return succeed(asdu.C_IC_NA_1, h.waitFor(stepTimeout, confirmed(asdu.C_IC_NA_1, 1, true)))

	case "counter-interrogate":
		qcc := asdu.ParseQualifierCountCall(byte(flagInt(args, "qcc", 5)))
		if err := send(func(c asdu.Connect) error { return asdu.CounterInterrogationCmd(c, coa(asdu.Activation), ca, qcc) }); err != nil {
			return fail("failed", err.Error())
		}
		return succeed(asdu.C_CI_NA_1, h.waitFor(stepTimeout, confirmed(asdu.C_CI_NA_1, 1, true)))

	case "read":
		ioa := asdu.InfoObjAddr(flagInt(args, "ioa", -1))
		if err := send(func(c asdu.Connect) error { return asdu.ReadCmd(c, coa(asdu.Request), ca, ioa) }); err != nil {
			return fail("failed", err.Error())
		}
		ok := h.waitFor(stepTimeout, func(as []*asdu.ASDU) bool {
			for _, a := range as {
				if a.Type == asdu.C_RD_NA_1 && (a.Coa.IsNegative || a.Coa.Cause >= asdu.UnknownTypeID) {
					return true
				}
				if a.Coa.Cause == asdu.Request && a.Type != asdu.C_RD_NA_1 {
					return true
				}
			}
			return false
		})
		return succeed(asdu.C_RD_NA_1, ok)

	case "clock-sync":
		tm := time.Now().UTC()
		if v := flag(args, "time", ""); v != "" {
			var err error
			if tm, err = time.Parse(time.RFC3339Nano, v); err != nil {
				return fail("failed", err.Error())
			}
		}
		if err := send(func(c asdu.Connect) error { return asdu.ClockSynchronizationCmd(c, coa(asdu.Activation), ca, tm) }); err != nil {
			return fail("failed", err.Error())
		}
		return succeed(asdu.C_CS_NA_1, h.waitFor(stepTimeout, confirmed(asdu.C_CS_NA_1, 1, false)))

	case "test-command":
		if err := send(func(c asdu.Connect) error {
			return asdu.TestCommandCP56Time2a(c, coa(asdu.Activation), ca, time.Now().UTC())
		}); err != nil {
			return fail("failed", err.Error())
		}
		return succeed(asdu.C_TS_TA_1, h.waitFor(stepTimeout, confirmed(asdu.C_TS_TA_1, 1, false)))

	case "monitor":
		time.Sleep(time.Duration(flagInt(args, "duration-ms", 1000)) * time.Millisecond)
		select {
		case <-lost:
			return fail("connection-lost", "closed")
		default:
		}
		fillResult(res, h.snapshot(), 0)
		res.OK = true
		return res

	case "command":
		return runInProcessCommand(res, h, cli, ca, orig, args, fail, succeed, confirmed)

	case "file-get":
		// STOPDT after the transfer: it flushes the final acknowledgement,
		// which a plain close could cut off, leaving the station mid-transfer.
		settle := func() {
			cli.SendStopDt()
			select {
			case <-deactivated:
			case <-time.After(stepTimeout):
			}
		}
		return runInProcessFileGet(res, h, cli, ca, args, fail, settle)
	}
	return fail("failed", "unsupported operation "+op)
}

func hasNegative(r *result) bool {
	for _, c := range r.Confirmations {
		if c.Negative {
			return true
		}
	}
	return false
}

// originatorConn stamps the originator address on ASDUs sent through it.
type originatorConn struct {
	*cs104.Client
	orig asdu.OriginAddr
}

func (o *originatorConn) Send(a *asdu.ASDU) error {
	a.OrigAddr = o.orig
	return o.Client.Send(a)
}

// fillResult derives asdus and confirmations from the received ASDUs. A
// confirmation is a mirror of the request type with cause 7, 9, 17 or 44..47.
func fillResult(r *result, got []*asdu.ASDU, reqType asdu.TypeID) {
	r.ASDUs = nil
	r.Confirmations = nil
	for _, a := range got {
		d := asduDoc{
			Type: typeName(a.Type), TypeID: int(a.Type), Cot: int(a.Coa.Cause),
			Negative: a.Coa.IsNegative, CommonAddress: int(a.CommonAddr), Objects: objectsOf(a),
		}
		r.ASDUs = append(r.ASDUs, d)
		c := a.Coa.Cause
		if reqType != 0 && a.Type == reqType &&
			(c == asdu.ActivationCon || c == asdu.DeactivationCon || c == 17 || (c >= 44 && c <= 47)) {
			r.Confirmations = append(r.Confirmations, struct {
				Cot      int  `json:"cot"`
				Negative bool `json:"negative"`
			}{int(c), a.Coa.IsNegative || c == 17 || c >= 44})
		}
		if reqType != 0 && a.Type == reqType && c == asdu.ActivationTerm {
			r.Terminated = true
		}
	}
}

// objectsOf decodes the monitor-direction types of the baseline fixture into
// the object documents of the container contract.
func objectsOf(a *asdu.ASDU) []map[string]any {
	var objs []map[string]any
	defer func() { _ = recover() }() // a type that does not decode has no objects
	switch a.Type {
	case asdu.M_SP_NA_1, asdu.M_SP_TB_1:
		for _, p := range a.GetSinglePoint() {
			objs = append(objs, map[string]any{"ioa": float64(p.Ioa), "value": p.Value, "quality": toAny(qualityFlags(byte(p.Qds)))})
		}
	case asdu.M_DP_NA_1, asdu.M_DP_TB_1:
		for _, p := range a.GetDoublePoint() {
			objs = append(objs, map[string]any{"ioa": float64(p.Ioa), "value": float64(p.Value), "quality": toAny(qualityFlags(byte(p.Qds)))})
		}
	case asdu.M_ST_NA_1, asdu.M_ST_TB_1:
		for _, p := range a.GetStepPosition() {
			objs = append(objs, map[string]any{"ioa": float64(p.Ioa), "value": float64(p.Value.Val), "transient": p.Value.HasTransient, "quality": toAny(qualityFlags(byte(p.Qds)))})
		}
	case asdu.M_BO_NA_1, asdu.M_BO_TB_1:
		for _, p := range a.GetBitString32() {
			objs = append(objs, map[string]any{"ioa": float64(p.Ioa), "value": float64(p.Value), "quality": toAny(qualityFlags(byte(p.Qds)))})
		}
	case asdu.M_ME_NA_1, asdu.M_ME_TD_1:
		for _, p := range a.GetMeasuredValueNormal() {
			objs = append(objs, map[string]any{"ioa": float64(p.Ioa), "value": float64(p.Value), "quality": toAny(qualityFlags(byte(p.Qds)))})
		}
	case asdu.M_ME_NB_1, asdu.M_ME_TE_1:
		for _, p := range a.GetMeasuredValueScaled() {
			objs = append(objs, map[string]any{"ioa": float64(p.Ioa), "value": float64(p.Value), "quality": toAny(qualityFlags(byte(p.Qds)))})
		}
	case asdu.M_ME_NC_1, asdu.M_ME_TF_1:
		for _, p := range a.GetMeasuredValueFloat() {
			objs = append(objs, map[string]any{"ioa": float64(p.Ioa), "value": float64(p.Value), "quality": toAny(qualityFlags(byte(p.Qds)))})
		}
	case asdu.M_IT_NA_1, asdu.M_IT_TB_1:
		for _, p := range a.GetIntegratedTotals() {
			objs = append(objs, map[string]any{"ioa": float64(p.Ioa), "value": float64(p.Value.CounterReading), "sequence": float64(p.Value.SeqNumber), "quality": toAny(nil)})
		}
	}
	return objs
}

func toAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// runInProcessCommand implements `client command` for go-iecp5.
func runInProcessCommand(res *result, h *masterHandler, cli *cs104.Client, ca asdu.CommonAddr, orig asdu.OriginAddr, args []string,
	fail func(code, msg string) *result,
	succeed func(reqType asdu.TypeID, ok bool) *result,
	confirmed func(reqType asdu.TypeID, n int, term bool) func([]*asdu.ASDU) bool) *result {

	typ := flag(args, "type", "")
	ioa := asdu.InfoObjAddr(flagInt(args, "ioa", -1))
	val := flag(args, "value", "")
	mode := flag(args, "mode", "direct")
	qual := flagInt(args, "qualifier", 0)
	timed := hasFlag(args, "with-time")
	conn := &originatorConn{Client: cli, orig: orig}

	typeID := map[string]struct{ plain, timed asdu.TypeID }{
		"C_SC_NA_1": {asdu.C_SC_NA_1, asdu.C_SC_TA_1}, "C_DC_NA_1": {asdu.C_DC_NA_1, asdu.C_DC_TA_1},
		"C_RC_NA_1": {asdu.C_RC_NA_1, asdu.C_RC_TA_1}, "C_SE_NA_1": {asdu.C_SE_NA_1, asdu.C_SE_TA_1},
		"C_SE_NB_1": {asdu.C_SE_NB_1, asdu.C_SE_TB_1}, "C_SE_NC_1": {asdu.C_SE_NC_1, asdu.C_SE_TC_1},
	}[typ]
	if typeID.plain == 0 {
		return fail("failed", "unsupported command type "+typ)
	}
	id := typeID.plain
	if timed {
		id = typeID.timed
	}

	// issue sends the command with the given S/E bit and cause.
	issue := func(sel bool, cause asdu.Cause) error {
		coa := asdu.CauseOfTransmission{Cause: cause}
		tm := time.Now().UTC()
		switch typ {
		case "C_SC_NA_1":
			v, err := parseBool(val)
			if err != nil {
				return err
			}
			return asdu.SingleCmd(conn, id, coa, ca, asdu.SingleCommandInfo{Ioa: ioa, Value: v,
				Qoc: asdu.QualifierOfCommand{Qual: asdu.QOCQual(qual), InSelect: sel}, Time: tm})
		case "C_DC_NA_1":
			n, err := strconv.Atoi(val)
			if err != nil {
				return err
			}
			return asdu.DoubleCmd(conn, id, coa, ca, asdu.DoubleCommandInfo{Ioa: ioa, Value: asdu.DoubleCommand(n),
				Qoc: asdu.QualifierOfCommand{Qual: asdu.QOCQual(qual), InSelect: sel}, Time: tm})
		case "C_RC_NA_1":
			n, err := strconv.Atoi(val)
			if err != nil {
				return err
			}
			return asdu.StepCmd(conn, id, coa, ca, asdu.StepCommandInfo{Ioa: ioa, Value: asdu.StepCommand(n),
				Qoc: asdu.QualifierOfCommand{Qual: asdu.QOCQual(qual), InSelect: sel}, Time: tm})
		case "C_SE_NA_1":
			n, err := strconv.Atoi(val)
			if err != nil {
				return err
			}
			return asdu.SetpointCmdNormal(conn, id, coa, ca, asdu.SetpointCommandNormalInfo{Ioa: ioa, Value: asdu.Normalize(n),
				Qos: asdu.QualifierOfSetpointCmd{Qual: asdu.QOSQual(qual), InSelect: sel}, Time: tm})
		case "C_SE_NB_1":
			n, err := strconv.Atoi(val)
			if err != nil {
				return err
			}
			return asdu.SetpointCmdScaled(conn, id, coa, ca, asdu.SetpointCommandScaledInfo{Ioa: ioa, Value: int16(n),
				Qos: asdu.QualifierOfSetpointCmd{Qual: asdu.QOSQual(qual), InSelect: sel}, Time: tm})
		default: // C_SE_NC_1
			f, err := strconv.ParseFloat(val, 32)
			if err != nil {
				return err
			}
			return asdu.SetpointCmdFloat(conn, id, coa, ca, asdu.SetpointCommandFloatInfo{Ioa: ioa, Value: float32(f),
				Qos: asdu.QualifierOfSetpointCmd{Qual: asdu.QOSQual(qual), InSelect: sel}, Time: tm})
		}
	}

	// The request type of the mirrors is the one on the wire.
	switch mode {
	case "direct":
		if err := issue(false, asdu.Activation); err != nil {
			return fail("failed", err.Error())
		}
		return succeed(id, h.waitFor(stepTimeout, confirmed(id, 1, true)))
	case "select":
		if err := issue(true, asdu.Activation); err != nil {
			return fail("failed", err.Error())
		}
		return succeed(id, h.waitFor(stepTimeout, confirmed(id, 1, false)))
	case "sbo":
		if err := issue(true, asdu.Activation); err != nil {
			return fail("failed", err.Error())
		}
		if !h.waitFor(stepTimeout, confirmed(id, 1, false)) {
			return succeed(id, false)
		}
		if err := issue(false, asdu.Activation); err != nil {
			return fail("failed", err.Error())
		}
		return succeed(id, h.waitFor(stepTimeout, confirmed(id, 2, true)))
	case "cancel":
		if err := issue(true, asdu.Activation); err != nil {
			return fail("failed", err.Error())
		}
		if !h.waitFor(stepTimeout, confirmed(id, 1, false)) {
			return succeed(id, false)
		}
		if err := issue(false, asdu.Deactivation); err != nil {
			return fail("failed", err.Error())
		}
		return succeed(id, h.waitFor(stepTimeout, confirmed(id, 2, false)))
	}
	return fail("failed", "unsupported mode "+mode)
}

func parseBool(s string) (bool, error) {
	switch s {
	case "true", "1", "on":
		return true, nil
	case "false", "0", "off":
		return false, nil
	}
	return false, fmt.Errorf("bad boolean %q", s)
}

// runInProcessFileGet implements `client file-get` with filetransfer.Receiver.
func runInProcessFileGet(res *result, h *masterHandler, cli *cs104.Client, ca asdu.CommonAddr, args []string,
	fail func(code, msg string) *result, settle func()) *result {

	ioa := asdu.InfoObjAddr(flagInt(args, "ioa", -1))
	nof := asdu.NameOfFile(flagInt(args, "name", 1))
	done := make(chan []byte, 1)
	h.receiver = filetransfer.NewReceiver(nil).SetFileHandler(func(_ filetransfer.Entry, data []byte) {
		done <- data
	})
	if err := h.receiver.RequestFile(cli, ca, ioa, nof); err != nil {
		return fail("failed", err.Error())
	}
	refused := func(as []*asdu.ASDU) bool {
		for _, a := range as {
			if a.Type == asdu.F_SC_NA_1 && a.Coa.IsNegative {
				return true
			}
		}
		return false
	}
	// The timeout bounds each wait for the station, not the whole transfer.
	last := len(h.snapshot())
	for {
		select {
		case data := <-done:
			settle()
			fillResult(res, h.snapshot(), 0)
			sum := sha256.Sum256(data)
			res.File = &fileDoc{Ioa: uint32(ioa), Name: int(nof), Received: len(data), Sha256: hex.EncodeToString(sum[:])}
			res.OK = true
			return res
		case <-time.After(stepTimeout):
			fillResult(res, h.snapshot(), 0)
			if refused(h.snapshot()) {
				return fail("negative-confirmation", "refused")
			}
			if n := len(h.snapshot()); n > last {
				last = n
				continue
			}
			return fail("timeout", "file never completed")
		}
	}
}

// typeName is the mnemonic ("C_IC_NA_1"); TypeID.String wraps it in TID<>.
func typeName(t asdu.TypeID) string {
	return strings.TrimSuffix(strings.TrimPrefix(t.String(), "TID<"), ">")
}

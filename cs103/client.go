// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs103

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/riclolsen/go-iecp5/clog"
	"github.com/riclolsen/go-iecp5/cs101"
)

// Connection states
const (
	statusInitial uint32 = iota
	statusConnecting
	statusConnected
	statusDisconnected
)

// Link layer states
const (
	linkStateReset uint32 = iota
	linkStateActive
)

// PrimFcResetFCB is the IEC 60870-5-103 specific primary function code 7,
// "reset frame count bit" (in addition to the FT1.2 codes shared with 101,
// see the cs101 package constants).
const PrimFcResetFCB byte = 7

// linkAddrSize is fixed at one octet by IEC 60870-5-103.
const linkAddrSize byte = 1

// secondary station link phase (primary point of view)
type secondaryPhase byte

const (
	phaseStatus secondaryPhase = iota // request status of link pending
	phaseReset                        // reset of communication unit pending
	phaseActive                       // link layer active
)

// secondaryState is the per-device link state kept by the primary. The FCB
// alternates per station across ALL frames sent with FCV=1, as required by
// IEC 60870-5-2.
type secondaryState struct {
	addr       byte
	phase      secondaryPhase
	fcb        bool // FCB of the next FCV frame sent to this station
	wantClass1 bool // ACD seen: a class 1 data request is due
}

// outgoingASDU is a queued application message with its target station.
type outgoingASDU struct {
	a    *ASDU
	addr byte
}

// pendingSend is the confirmed frame awaiting ACK/RESP from the device.
type pendingSend struct {
	frame *cs101.Frame
	ctrl  cs101.ControlField
	addr  byte
}

// Client is an IEC 60870-5-103 primary station (master).
type Client struct {
	option  ClientOption
	port    io.ReadWriteCloser
	handler ClientHandlerInterface

	// Channels for internal communication
	rcvFrame  chan *cs101.Frame
	sendFrame chan *cs101.Frame
	rcvASDU   chan *ASDU
	linkReq   chan linkRequest // manual link-layer requests executed by the run loop

	sendQueue      []outgoingASDU
	sendQueueMutex sync.Mutex

	// Link layer state — owned by the runProtocol goroutine
	secs       map[byte]*secondaryState
	secOrder   []byte
	pollIdx    int
	lastSent   *pendingSend
	retryCount int

	t1Timer      *time.Timer
	t3Timer      *time.Timer
	timerTrySend *time.Timer

	connStatus uint32
	linkStatus uint32
	rwMux      sync.RWMutex

	clog.Clog
	wg         sync.WaitGroup
	ctx        context.Context
	cancel     context.CancelFunc
	connCtx    context.Context
	connCancel context.CancelFunc

	onConnectCalled  bool
	onConnect        func(c *Client)
	onConnectionLost func(c *Client, err error)
	onConnectError   func(c *Client, err error)
	onDeviceActive   func(c *Client, addr byte)
}

type linkRequest struct {
	fun  byte
	addr byte
}

// NewClient creates a new IEC 60870-5-103 primary station client.
// It returns nil when the serial port address is not configured.
func NewClient(handler ClientHandlerInterface, o *ClientOption) *Client {
	opt := *o
	tempLogger := clog.NewLogger("")
	tempLogger.LogMode(true)

	if err := opt.config.Valid(); err != nil {
		tempLogger.Warn("Invalid CS103 config provided, using defaults. Error: %v", err)
		opt.config = DefaultConfig()
	}
	if opt.config.Transport == TransportSerial && opt.config.Serial.Address == "" {
		tempLogger.Error("Serial port address (e.g., COM3 or /dev/ttyS0) must be set in the CS103 config")
		return nil
	}
	if opt.config.Transport != TransportSerial && opt.config.TCP.Address == "" {
		tempLogger.Error("TCP address must be set in the CS103 config for the TCP transport")
		return nil
	}

	c := &Client{
		option:           opt,
		handler:          handler,
		rcvFrame:         make(chan *cs101.Frame, 20),
		sendFrame:        make(chan *cs101.Frame, 20),
		rcvASDU:          make(chan *ASDU, 50),
		linkReq:          make(chan linkRequest, 8),
		sendQueue:        make([]outgoingASDU, 0, opt.config.MaxSendQueueSize),
		Clog:             clog.NewLogger(fmt.Sprintf("cs103 client [%s] => ", opt.config.transportLabel())),
		onConnect:        func(*Client) {},
		onConnectionLost: func(*Client, error) {},
		onConnectError:   func(*Client, error) {},
		onDeviceActive:   func(*Client, byte) {},
	}
	c.Clog.LogMode(true)
	return c
}

// SetLogMode enables or disables logging output.
func (sf *Client) SetLogMode(enable bool) { sf.Clog.LogMode(enable) }

// SetOnConnectHandler sets the handler called when the line is alive
// (first frame received).
func (sf *Client) SetOnConnectHandler(f func(c *Client)) *Client {
	if f != nil {
		sf.onConnect = f
	}
	return sf
}

// SetConnectionLostHandler sets the handler called when the connection is lost.
func (sf *Client) SetConnectionLostHandler(f func(c *Client, err error)) *Client {
	if f != nil {
		sf.onConnectionLost = f
	}
	return sf
}

// SetConnectErrorHandler sets the handler called when opening the serial
// port fails.
func (sf *Client) SetConnectErrorHandler(f func(c *Client, err error)) *Client {
	if f != nil {
		sf.onConnectError = f
	}
	return sf
}

// SetOnDeviceActiveHandler sets the handler called when a device's link
// becomes active (after reset of the communication unit is confirmed).
func (sf *Client) SetOnDeviceActiveHandler(f func(c *Client, addr byte)) *Client {
	if f != nil {
		sf.onDeviceActive = f
	}
	return sf
}

// Start initiates the connection process in the background.
func (sf *Client) Start() error {
	sf.rwMux.Lock()
	if sf.connStatus != statusInitial {
		sf.rwMux.Unlock()
		return errors.New("client already started or starting")
	}
	sf.connStatus = statusConnecting
	sf.ctx, sf.cancel = context.WithCancel(context.Background())
	sf.rwMux.Unlock()

	go sf.connectionManager()
	return nil
}

// connectionManager handles the transport lifecycle and reconnection.
func (sf *Client) connectionManager() {
	sf.Debug("Connection manager started")
	transporter := cs101.NewTransporter(sf.option.config.Transport, sf.option.config.Serial, sf.option.config.TCP)
	defer func() {
		_ = transporter.Close()
		sf.setConnectStatus(statusInitial)
		sf.Debug("Connection manager stopped")
	}()

	for {
		select {
		case <-sf.ctx.Done():
			return
		default:
		}

		sf.setConnectStatus(statusConnecting)
		sf.Debug("Opening %s transport (%s)...", sf.option.config.Transport, sf.option.config.transportLabel())

		conn, desc, err := transporter.Open(sf.ctx)
		if err != nil {
			if sf.ctx.Err() != nil {
				return // Close() was called while opening
			}
			sf.Error("Failed to open transport: %v", err)
			sf.setConnectStatus(statusDisconnected)
			sf.onConnectError(sf, err)
			if !sf.option.autoReconnect {
				return
			}
			select {
			case <-time.After(sf.option.reconnectInterval):
				continue
			case <-sf.ctx.Done():
				return
			}
		}

		sf.Debug("%s connected successfully", desc)
		sf.port = conn
		sf.setConnectStatus(statusConnected)

		sf.connCtx, sf.connCancel = context.WithCancel(sf.ctx)
		connectionErr := sf.runProtocol()

		sf.setConnectStatus(statusDisconnected)
		_ = sf.port.Close()
		sf.port = nil
		sf.connCancel()

		if connectionErr != nil {
			sf.Warn("Protocol run ended with error: %v", connectionErr)
			sf.onConnectionLost(sf, connectionErr)
		} else {
			sf.Debug("Protocol run ended gracefully.")
			sf.onConnectionLost(sf, nil)
		}

		select {
		case <-sf.ctx.Done():
			return
		default:
			if !sf.option.autoReconnect {
				sf.Debug("Auto-reconnect disabled, stopping.")
				return
			}
			select {
			case <-time.After(sf.option.reconnectInterval):
			case <-sf.ctx.Done():
				return
			}
		}
	}
}

// stopTimer stops a timer and drains its channel if it already fired.
func stopTimer(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}

// initSecondaries builds the per-station link state table for this connection.
func (sf *Client) initSecondaries() {
	addrs := sf.option.secondaryAddrs
	if len(addrs) == 0 {
		addrs = []byte{sf.option.config.LinkAddress}
	}
	sf.secs = make(map[byte]*secondaryState, len(addrs))
	sf.secOrder = sf.secOrder[:0]
	for _, a := range addrs {
		if _, ok := sf.secs[a]; ok {
			continue
		}
		sf.secs[a] = &secondaryState{addr: a, phase: phaseStatus}
		sf.secOrder = append(sf.secOrder, a)
	}
	sf.pollIdx = 0
}

// defaultAddr returns the link address used by Send and the wrappers when
// no explicit address is given.
func (sf *Client) defaultAddr() byte {
	if len(sf.option.secondaryAddrs) > 0 {
		return sf.option.secondaryAddrs[0]
	}
	return sf.option.config.LinkAddress
}

// updateLinkActive refreshes the aggregated link status flag.
func (sf *Client) updateLinkActive() {
	for _, a := range sf.secOrder {
		if sf.secs[a].phase == phaseActive {
			atomic.StoreUint32(&sf.linkStatus, linkStateActive)
			return
		}
	}
	atomic.StoreUint32(&sf.linkStatus, linkStateReset)
}

// runProtocol manages the communication loops and state machine for an
// active connection.
func (sf *Client) runProtocol() error {
	sf.Debug("runProtocol started")
	atomic.StoreUint32(&sf.linkStatus, linkStateReset)
	sf.initSecondaries()
	sf.lastSent = nil
	sf.retryCount = 0

	sf.sendQueueMutex.Lock()
	if len(sf.sendQueue) > 0 {
		sf.Warn("Clearing %d unsent ASDUs from send queue due to new connection/protocol start.", len(sf.sendQueue))
		sf.sendQueue = sf.sendQueue[:0]
	}
	sf.sendQueueMutex.Unlock()

	// Drain channel leftovers from a previous connection
drain:
	for {
		select {
		case <-sf.rcvFrame:
		case <-sf.sendFrame:
		case <-sf.rcvASDU:
		case <-sf.linkReq:
		default:
			break drain
		}
	}

	sf.onConnectCalled = false
	sf.wg.Add(3)
	go sf.frameRecvLoop()
	go sf.frameSendLoop()
	go sf.handlerLoop()

	sf.t1Timer = time.NewTimer(sf.option.config.TimeoutResponseT1)
	stopTimer(sf.t1Timer)
	sf.t3Timer = time.NewTimer(sf.option.config.TimeoutTestT3)
	sf.timerTrySend = time.NewTimer(sf.option.config.TimeoutSendLinkMsg)

	defer sf.t1Timer.Stop()
	defer sf.t3Timer.Stop()
	defer sf.timerTrySend.Stop()

	for {
		select {
		case <-sf.connCtx.Done():
			sf.Debug("Connection context done, stopping protocol run.")
			sf.wg.Wait()
			return sf.connCtx.Err()

		case <-sf.timerTrySend.C:
			sf.tick()
			sf.timerTrySend.Reset(sf.option.config.TimeoutSendLinkMsg)

		case req := <-sf.linkReq:
			if sf.lastSent != nil {
				sf.Debug("Manual link request 0x%02X dropped: link busy", req.fun)
				break
			}
			sec := sf.secs[req.addr]
			if sec == nil {
				break
			}
			switch req.fun {
			case cs101.PrimFcResetLink:
				sf.sendResetCU(sec)
			case PrimFcResetFCB:
				sf.sendResetFCB(sec)
			case cs101.PrimFcReqStatus:
				sf.sendRequestLinkStatus(sec)
			case cs101.PrimFcReqData2:
				if sec.phase == phaseActive {
					sf.sendReqData(sec, cs101.PrimFcReqData2)
				}
			}

		case <-sf.t1Timer.C:
			if sf.lastSent == nil {
				sf.Warn("T1 fired but no outstanding frame found.")
				break
			}
			if sf.retryCount < 1 {
				sf.retryCount++
				sf.Warn("T1 timeout, retrying last confirmed frame (Retry %d)", sf.retryCount)
				if err := sf.sendFrameToChan(sf.lastSent.frame); err != nil {
					sf.Error("Failed to resend frame on T1 retry: %v", err)
					return err
				}
				sf.t1Timer.Reset(sf.option.config.TimeoutRepeatT2)
			} else {
				sec := sf.secs[sf.lastSent.addr]
				sf.Error("T1 timeout after retry for station %d. Marking link inactive.", sf.lastSent.addr)
				sf.lastSent = nil
				sf.retryCount = 0
				if sec != nil {
					sec.phase = phaseStatus
					sec.wantClass1 = false
				}
				sf.updateLinkActive()
				// Point-to-point: treat total loss of the only device as a
				// connection failure so the manager can reconnect. In
				// multi-drop keep serving the other stations.
				if len(sf.secOrder) == 1 {
					return ErrTimeoutT1
				}
			}

		case <-sf.t3Timer.C:
			sf.Debug("T3 idle timeout expired.")
			if sf.lastSent == nil {
				if sec := sf.firstActiveSecondary(); sec != nil {
					sf.sendReqData(sec, cs101.PrimFcReqData2)
				}
			} else {
				sf.Debug("Skipping keep-alive poll (link busy)")
			}
			sf.t3Timer.Reset(sf.option.config.TimeoutTestT3)

		case frame := <-sf.rcvFrame:
			if frame.Start == cs101.SingleCharAck {
				sf.Debug("Received Single Char ACK")
			} else {
				sf.Debug("Received Frame: Start=0x%02X, Ctrl=%s, LinkAddr=%X", frame.Start, frame.GetControlField(), frame.LinkAddr)
			}

			stopTimer(sf.t3Timer)
			sf.t3Timer.Reset(sf.option.config.TimeoutTestT3)

			if err := sf.handleIncomingFrame(frame); err != nil {
				sf.Error("Error handling incoming frame: %v", err)
			}
			if sf.lastSent == nil {
				stopTimer(sf.t1Timer)
			}

			if !sf.onConnectCalled {
				sf.Debug("First frame received, calling onConnect handler.")
				sf.onConnectCalled = true
				sf.onConnect(sf)
			}
		}
	}
}

// tick drives the link when it is free: first queued user data, then link
// initialization and class polling, round-robin over stations.
func (sf *Client) tick() {
	if sf.lastSent != nil {
		return
	}
	if sf.trySendNextFromQueue() {
		return
	}
	n := len(sf.secOrder)
	for i := 0; i < n; i++ {
		sec := sf.secs[sf.secOrder[sf.pollIdx]]
		sf.pollIdx = (sf.pollIdx + 1) % n
		switch sec.phase {
		case phaseStatus:
			sf.sendRequestLinkStatus(sec)
			return
		case phaseReset:
			sf.sendResetCU(sec)
			return
		case phaseActive:
			if sec.wantClass1 {
				sf.sendReqData(sec, cs101.PrimFcReqData1)
			} else {
				sf.sendReqData(sec, cs101.PrimFcReqData2)
			}
			return
		}
	}
}

// firstActiveSecondary returns any station with an active link, or nil.
func (sf *Client) firstActiveSecondary() *secondaryState {
	for _, a := range sf.secOrder {
		if sf.secs[a].phase == phaseActive {
			return sf.secs[a]
		}
	}
	return nil
}

// confirmPositive completes the outstanding transaction positively.
// addrValid is false for E5 single char acks, which carry no link address.
func (sf *Client) confirmPositive(addr byte, addrValid bool) {
	p := sf.lastSent
	if p == nil {
		sf.Warn("Received confirm but no confirmed frame outstanding.")
		return
	}
	if addrValid && p.addr != addr {
		sf.Warn("Received confirm from station %d while waiting for station %d; ignored.", addr, p.addr)
		return
	}
	sec := sf.secs[p.addr]
	if sec != nil {
		if p.ctrl.FCV {
			sec.fcb = !p.ctrl.FCB // toggle only after positive confirm
		}
		switch p.ctrl.Fun {
		case cs101.PrimFcResetLink, PrimFcResetFCB:
			sf.Debug("Reset confirmed by station %d. Link active.", p.addr)
			sec.phase = phaseActive
			sec.fcb = true // first FCV frame after reset carries FCB=1
			sf.updateLinkActive()
			sf.onDeviceActive(sf, p.addr)
			if sf.option.config.AutoInit {
				sf.queueAutoInit(p.addr)
			}
		case cs101.PrimFcReqStatus:
			if sec.phase == phaseStatus {
				sec.phase = phaseReset
			}
		case cs101.PrimFcReqData1:
			sec.wantClass1 = false
		}
	}
	sf.lastSent = nil
	sf.retryCount = 0
}

// queueAutoInit enqueues the standard 103 startup sequence for a device:
// time synchronization followed by a general interrogation.
func (sf *Client) queueAutoInit(addr byte) {
	sf.Debug("Auto-init for station %d: time sync + general interrogation", addr)
	sf.enqueue(NewTimeSyncASDU(addr, time.Now()), addr)
	sf.enqueue(NewGeneralInterrogationASDU(addr, 0), addr)
}

// failTransaction aborts the outstanding transaction without toggling FCB
// and downgrades the station link phase.
func (sf *Client) failTransaction(addr byte) {
	p := sf.lastSent
	if p == nil || p.addr != addr {
		return
	}
	if sec := sf.secs[p.addr]; sec != nil {
		sec.phase = phaseStatus
		sec.wantClass1 = false
	}
	sf.lastSent = nil
	sf.retryCount = 0
	sf.updateLinkActive()
}

// handleIncomingFrame processes a parsed frame from the receive loop.
func (sf *Client) handleIncomingFrame(frame *cs101.Frame) error {
	// Single character ACK carries no address or control field.
	if frame.Start == cs101.SingleCharAck {
		sf.confirmPositive(0, false)
		return nil
	}

	receivedAddr := byte(frame.GetLinkAddress())
	sec := sf.secs[receivedAddr]
	if sec == nil {
		sf.Warn("Ignoring frame with unexpected link address %d", receivedAddr)
		return nil
	}

	ctrl := frame.GetControlField()
	if ctrl.PRM {
		sf.Warn("Received unexpected frame with PRM=1 (103 is unbalanced): %s", ctrl)
		return nil
	}
	if ctrl.DFC {
		sf.Warn("Station %d indicates Data Flow Control active (buffer likely full).", receivedAddr)
	}

	switch frame.Start {
	case cs101.StartFixed:
		switch ctrl.Fun {
		case cs101.SecFcConfACK:
			sf.confirmPositive(receivedAddr, true)

		case cs101.SecFcConfNACK: // link busy — keep outstanding, T1/T2 will retry
			sf.Warn("Received NACK (link busy) from station %d.", receivedAddr)

		case cs101.SecFcUserDataNoRep: // requested data not available
			sf.Debug("Station %d: requested data not available.", receivedAddr)
			sf.confirmPositive(receivedAddr, true)

		case cs101.SecFcRespStatus:
			sf.Debug("Received Link Status from station %d (DFC=%v)", receivedAddr, ctrl.DFC)
			sf.confirmPositive(receivedAddr, true)

		case cs101.SecFcRespLinkNF, cs101.SecFcRespLinkNI:
			sf.Warn("Station %d link service not functioning/implemented (FC=%d).", receivedAddr, ctrl.Fun)
			sf.failTransaction(receivedAddr)

		default:
			sf.Warn("Received unhandled fixed-length frame from station %d: %s", receivedAddr, ctrl)
		}

	case cs101.StartVariable:
		switch ctrl.Fun {
		case cs101.SecFcUserDataConf, cs101.SecFcRespStatus:
			sf.Debug("Received user data from station %d", receivedAddr)
			sf.confirmPositive(receivedAddr, true)
			a := new(ASDU)
			if err := a.UnmarshalBinary(frame.ASDU); err != nil {
				sf.Warn("Failed to decode ASDU from variable frame: %v", err)
			} else {
				sf.dispatchASDU(a)
			}

		case cs101.SecFcUserDataNoRep:
			sf.Debug("Station %d: no user data available.", receivedAddr)
			sf.confirmPositive(receivedAddr, true)

		default:
			sf.Warn("Received unhandled variable-length frame from station %d: %s", receivedAddr, ctrl)
		}
	}

	// Access demand: the station has class 1 data (events) waiting.
	if ctrl.ACD {
		sec.wantClass1 = true
		if sf.lastSent == nil && sec.phase == phaseActive {
			sf.sendReqData(sec, cs101.PrimFcReqData1)
		}
	}

	return nil
}

// dispatchASDU hands a decoded ASDU to the handler loop.
func (sf *Client) dispatchASDU(a *ASDU) {
	select {
	case sf.rcvASDU <- a:
	case <-sf.connCtx.Done():
	}
}

// frameRecvLoop reads from the serial port and parses FT1.2 frames.
func (sf *Client) frameRecvLoop() {
	sf.Debug("frameRecvLoop started")
	defer func() {
		sf.connCancel()
		sf.wg.Done()
		sf.Debug("frameRecvLoop stopped")
	}()

	for {
		select {
		case <-sf.connCtx.Done():
			return
		default:
			frame, err := cs101.ParseFrame(sf.port, linkAddrSize, &sf.connCtx)
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
					sf.Debug("Serial port closed or EOF reached.")
					return
				}
				if errors.Is(err, cs101.ErrChecksumMismatch) || errors.Is(err, cs101.ErrInvalidStartChar) ||
					errors.Is(err, cs101.ErrLengthMismatch) || errors.Is(err, cs101.ErrInvalidEndChar) ||
					errors.Is(err, cs101.ErrFrameTooShort) || errors.Is(err, cs101.ErrFrameLenExceeded) {
					sf.Warn("Frame parsing error: %v. Attempting to recover.", err)
					continue
				}
				sf.Error("Error reading/parsing frame: %v", err)
				return
			}

			select {
			case sf.rcvFrame <- frame:
			case <-sf.connCtx.Done():
				return
			}
		}
	}
}

// frameSendLoop takes frames from the send channel and writes them to the
// serial port.
func (sf *Client) frameSendLoop() {
	sf.Debug("frameSendLoop started")
	defer func() {
		sf.connCancel()
		sf.wg.Done()
		sf.Debug("frameSendLoop stopped")
	}()

	for {
		select {
		case <-sf.connCtx.Done():
			return
		case frame := <-sf.sendFrame:
			sf.Debug("Sending Frame: Start=0x%02X, Ctrl=%s, LinkAddr=%X, ASDULen=%d",
				frame.Start, frame.GetControlField(), frame.LinkAddr, len(frame.ASDU))

			rawData, err := frame.MarshalBinary(linkAddrSize)
			if err != nil {
				sf.Error("Failed to marshal frame: %v", err)
				continue
			}

			sf.Debug("TX Raw Frame [% X]", rawData)
			if _, err = sf.port.Write(rawData); err != nil {
				sf.Error("Failed to write frame to serial port: %v", err)
				return
			}
		}
	}
}

// handlerLoop processes decoded ASDUs received from the main loop.
func (sf *Client) handlerLoop() {
	sf.Debug("handlerLoop started")
	defer func() {
		sf.wg.Done()
		sf.Debug("handlerLoop stopped")
	}()

	for {
		select {
		case <-sf.connCtx.Done():
			return
		case a := <-sf.rcvASDU:
			sf.Debug("Processing ASDU: %s", a)
			if err := sf.callHandler(a); err != nil {
				sf.Warn("Error in ASDU handler: %v (ASDU: %s)", err, a)
			}
		}
	}
}

// callHandler safely dispatches an ASDU to the user handler.
func (sf *Client) callHandler(a *ASDU) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic recovered in handler: %v", r)
			sf.Critical("%v", err)
		}
	}()

	if handlerErr := sf.handler.ASDUHandlerAll(a); handlerErr != nil {
		sf.Warn("Error in ASDUHandlerAll: %v", handlerErr)
	}

	switch a.Type {
	case TypTimeTagged, TypTimeTaggedRel:
		info, derr := a.GetTimeTagged()
		if derr != nil {
			return derr
		}
		return sf.handler.TimeTaggedHandler(a, info)
	case TypMeasurandsI, TypMeasurandsII:
		info, derr := a.GetMeasurands()
		if derr != nil {
			return derr
		}
		return sf.handler.MeasurandsHandler(a, info)
	case TypIdentification:
		info, derr := a.GetIdentification()
		if derr != nil {
			return derr
		}
		return sf.handler.IdentificationHandler(a, info)
	case TypGITermination:
		scn, derr := a.GetGITermination()
		if derr != nil {
			return derr
		}
		return sf.handler.GITerminationHandler(a, scn)
	default:
		return sf.handler.ASDUHandler(a)
	}
}

// setConnectStatus updates the connection status atomically.
func (sf *Client) setConnectStatus(status uint32) {
	atomic.StoreUint32(&sf.connStatus, status)
}

// IsConnected returns true if the serial port is open and running.
func (sf *Client) IsConnected() bool {
	return atomic.LoadUint32(&sf.connStatus) == statusConnected
}

// IsLinkActive returns true if at least one device link is active.
func (sf *Client) IsLinkActive() bool {
	return sf.IsConnected() && atomic.LoadUint32(&sf.linkStatus) == linkStateActive
}

// Close disconnects the client and stops all background goroutines.
func (sf *Client) Close() error {
	sf.rwMux.Lock()
	if sf.cancel == nil {
		sf.rwMux.Unlock()
		return errors.New("client not running")
	}
	sf.Debug("Close requested.")
	sf.cancel()
	sf.cancel = nil
	sf.rwMux.Unlock()
	return nil
}

// Port returns the underlying serial port (nil when disconnected).
func (sf *Client) Port() io.ReadWriteCloser { return sf.port }

// --- link layer actions ---

// primControl builds a primary control field byte.
func primControl(fun byte, fcv, fcb bool) byte {
	control := cs101.CtrlPRM | (fun & cs101.CtrlFuncMask)
	if fcv {
		control |= cs101.CtrlFCV
		if fcb {
			control |= cs101.CtrlFCB
		}
	}
	return control
}

// sendFrameToChan queues a frame on the send channel.
func (sf *Client) sendFrameToChan(frame *cs101.Frame) error {
	if !sf.IsConnected() {
		return ErrUseClosedConnection
	}
	select {
	case sf.sendFrame <- frame:
		return nil
	case <-sf.connCtx.Done():
		return sf.connCtx.Err()
	default:
		return ErrBufferFulled
	}
}

// sendPrimConfirmed sends a fixed-length primary frame that expects a
// confirmation and starts the T1 response timer.
func (sf *Client) sendPrimConfirmed(sec *secondaryState, fun byte, fcv bool) error {
	control := primControl(fun, fcv, sec.fcb)
	frame := cs101.NewFixedFrame(control, []byte{sec.addr})
	err := sf.sendFrameToChan(frame)
	if err == nil {
		sf.lastSent = &pendingSend{frame: frame, ctrl: cs101.ParseControlField(control), addr: sec.addr}
		sf.retryCount = 0
		stopTimer(sf.t1Timer)
		sf.t1Timer.Reset(sf.option.config.TimeoutResponseT1)
	}
	return err
}

// sendResetCU sends a Reset of the Communication Unit (FC 0, FCV=0).
func (sf *Client) sendResetCU(sec *secondaryState) {
	sf.Debug("Sending Reset CU command to station %d", sec.addr)
	sec.phase = phaseReset
	sf.updateLinkActive()
	if err := sf.sendPrimConfirmed(sec, cs101.PrimFcResetLink, false); err != nil {
		sf.Error("Failed to send Reset CU: %v", err)
	}
}

// sendResetFCB sends a Reset of the Frame Count Bit (FC 7, FCV=0),
// the 103-specific lightweight reset.
func (sf *Client) sendResetFCB(sec *secondaryState) {
	sf.Debug("Sending Reset FCB command to station %d", sec.addr)
	sec.phase = phaseReset
	sf.updateLinkActive()
	if err := sf.sendPrimConfirmed(sec, PrimFcResetFCB, false); err != nil {
		sf.Error("Failed to send Reset FCB: %v", err)
	}
}

// sendRequestLinkStatus sends a Request Status of Link (FC 9, FCV=0).
func (sf *Client) sendRequestLinkStatus(sec *secondaryState) {
	sf.Debug("Sending Request Link Status command to station %d", sec.addr)
	if err := sf.sendPrimConfirmed(sec, cs101.PrimFcReqStatus, false); err != nil {
		sf.Error("Failed to send Request Link Status: %v", err)
	}
}

// sendReqData sends a Request User Data Class 1 or Class 2 (FCV=1).
func (sf *Client) sendReqData(sec *secondaryState, fun byte) {
	sf.Debug("Sending Request User Data Class %d command to station %d", fun-cs101.PrimFcReqData1+1, sec.addr)
	if err := sf.sendPrimConfirmed(sec, fun, true); err != nil {
		sf.Error("Failed to send Request Data: %v", err)
	}
}

// queueLinkReq forwards a manual link-layer request to the run loop
// (safe to call from any goroutine).
func (sf *Client) queueLinkReq(fun, addr byte) error {
	if !sf.IsConnected() {
		return ErrUseClosedConnection
	}
	select {
	case sf.linkReq <- linkRequest{fun: fun, addr: addr}:
		return nil
	default:
		return ErrBufferFulled
	}
}

// SendResetCU requests a Reset of the Communication Unit of the given
// device. Executed by the protocol loop when the link is free.
func (sf *Client) SendResetCU(addr byte) error {
	return sf.queueLinkReq(cs101.PrimFcResetLink, addr)
}

// SendResetFCB requests a Reset of the Frame Count Bit of the given device.
// Executed by the protocol loop when the link is free.
func (sf *Client) SendResetFCB(addr byte) error {
	return sf.queueLinkReq(PrimFcResetFCB, addr)
}

// SendRequestLinkStatus requests a Request Status of Link of the given
// device. Executed by the protocol loop when the link is free.
func (sf *Client) SendRequestLinkStatus(addr byte) error {
	return sf.queueLinkReq(cs101.PrimFcReqStatus, addr)
}

// --- application layer actions ---

// enqueue adds an ASDU to the send queue without the connection check
// (internal use by the protocol loop itself).
func (sf *Client) enqueue(a *ASDU, addr byte) {
	sf.sendQueueMutex.Lock()
	defer sf.sendQueueMutex.Unlock()
	if len(sf.sendQueue) >= sf.option.config.MaxSendQueueSize {
		sf.Warn("Send queue full, discarding ASDU: %s", a)
		return
	}
	sf.sendQueue = append(sf.sendQueue, outgoingASDU{a: a, addr: addr})
}

// Send queues an ASDU for transmission to the default device.
func (sf *Client) Send(a *ASDU) error {
	return sf.SendTo(a, sf.defaultAddr())
}

// SendTo queues an ASDU for transmission to the device with the given link
// address. The protocol loop transmits it as confirmed user data when that
// station's link is free and active.
func (sf *Client) SendTo(a *ASDU, addr byte) error {
	if !sf.IsConnected() {
		return ErrUseClosedConnection
	}
	sf.sendQueueMutex.Lock()
	defer sf.sendQueueMutex.Unlock()
	if len(sf.sendQueue) >= sf.option.config.MaxSendQueueSize {
		sf.Warn("Send queue full, discarding ASDU: %s. Queue size: %d", a, len(sf.sendQueue))
		return ErrSendQueueFull
	}
	sf.sendQueue = append(sf.sendQueue, outgoingASDU{a: a, addr: addr})
	sf.Debug("ASDU %s enqueued for station %d. Queue size: %d", a, addr, len(sf.sendQueue))
	return nil
}

// TimeSync sends a time synchronization (ASDU 6) with the current time to
// the given device. The device confirms with a monitor-direction ASDU 6.
func (sf *Client) TimeSync(addr byte) error {
	return sf.SendTo(NewTimeSyncASDU(addr, time.Now()), addr)
}

// GeneralInterrogation initiates a general interrogation (ASDU 7) of the
// given device with the given scan number. The device answers with ASDU 1
// messages (cause 9) and terminates with ASDU 8 carrying the same SCN.
func (sf *Client) GeneralInterrogation(addr, scn byte) error {
	return sf.SendTo(NewGeneralInterrogationASDU(addr, scn), addr)
}

// GeneralCommand sends a general command (ASDU 20) to the given device.
// The acknowledgement returns as ASDU 1 with cause 20 (positive) or 21
// (negative) carrying the RII in the supplementary information.
func (sf *Client) GeneralCommand(addr, fun, inf byte, dco DCO, rii byte) error {
	return sf.SendTo(NewGeneralCommandASDU(addr, fun, inf, dco, rii), addr)
}

// _sendASDU sends one ASDU as confirmed user data (called by the run loop
// only, with the link free).
func (sf *Client) _sendASDU(out outgoingASDU) error {
	sec := sf.secs[out.addr]
	if sec == nil || sec.phase != phaseActive {
		return ErrNotActive
	}

	asduData, err := out.a.MarshalBinary()
	if err != nil {
		return fmt.Errorf("failed to marshal ASDU: %w", err)
	}

	control := primControl(cs101.PrimFcUserDataConf, true, sec.fcb)
	frame := cs101.NewDataFrame(control, []byte{sec.addr}, asduData)

	sf.Debug("Sending User Data to station %d (%s, FCB: %v)", sec.addr, out.a, sec.fcb)

	err = sf.sendFrameToChan(frame)
	if err == nil {
		sf.lastSent = &pendingSend{frame: frame, ctrl: cs101.ParseControlField(control), addr: sec.addr}
		sf.retryCount = 0
		stopTimer(sf.t1Timer)
		sf.t1Timer.Reset(sf.option.config.TimeoutResponseT1)
	}
	return err
}

// trySendNextFromQueue sends the first queued ASDU whose target station is
// active. Entries addressed to unknown stations are dropped. Returns true
// when a frame was handed to the send loop.
func (sf *Client) trySendNextFromQueue() bool {
	if sf.lastSent != nil {
		return false
	}
	sf.sendQueueMutex.Lock()
	var next outgoingASDU
	found := false
	for i := 0; i < len(sf.sendQueue); {
		e := sf.sendQueue[i]
		sec := sf.secs[e.addr]
		if sec == nil {
			sf.Error("Dropping queued ASDU %s: unknown station %d", e.a, e.addr)
			sf.sendQueue = append(sf.sendQueue[:i], sf.sendQueue[i+1:]...)
			continue
		}
		if sec.phase == phaseActive {
			next = e
			sf.sendQueue = append(sf.sendQueue[:i], sf.sendQueue[i+1:]...)
			found = true
			break
		}
		i++
	}
	sf.sendQueueMutex.Unlock()

	if !found {
		return false
	}
	if err := sf._sendASDU(next); err != nil {
		sf.Error("Failed to send ASDU from queue (%s): %v. ASDU lost.", next.a, err)
		return false
	}
	return true
}

// Copyright 2025 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs101

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/clog"
)

// Connection states
const (
	statusInitial uint32 = iota
	statusConnecting
	statusConnected
	statusDisconnected
)

// Link layer states (simplified)
const (
	linkStateReset uint32 = iota
	linkStateActive
)

// secondary station link phase (primary point of view)
type secondaryPhase byte

const (
	phaseStatus secondaryPhase = iota // request status of link pending
	phaseReset                        // reset of remote link pending
	phaseActive                       // link layer active
)

// secondaryState is the per-secondary-station link state kept by the primary.
// The FCB alternates per station across ALL frames sent with FCV=1
// (user data, class 1/2 requests, test link), as required by IEC 60870-5-2.
type secondaryState struct {
	addr       uint16
	phase      secondaryPhase
	fcb        bool // FCB of the next FCV frame sent to this station
	wantClass1 bool // ACD seen: a class 1 data request is due
}

// outgoingASDU is a queued application message with its target station.
type outgoingASDU struct {
	a    *asdu.ASDU
	addr uint16
}

// pendingSend is the confirmed frame awaiting ACK/RESP from the secondary.
type pendingSend struct {
	frame *Frame
	ctrl  ControlField
	addr  uint16
}

// Client is an IEC101 Primary Station (Master)
type Client struct {
	option       ClientOption
	port         io.ReadWriteCloser // Serial port connection
	handler      ClientHandlerInterface
	clientNumber int

	// Channels for internal communication
	rcvFrame  chan *Frame     // Parsed incoming frames
	sendFrame chan *Frame     // Frames to be sent
	rcvASDU   chan *asdu.ASDU // Decoded ASDUs from variable frames
	linkReq   chan byte       // manual link-layer requests executed by the run loop

	sendQueue      []outgoingASDU // Queue for outgoing ASDUs
	sendQueueMutex sync.Mutex     // Mutex to protect sendQueue

	// CS101 Link Layer State — owned by the runProtocol goroutine
	secs        map[uint16]*secondaryState
	secOrder    []uint16
	pollIdx     int
	lastSent    *pendingSend // outstanding confirmed frame (nil when link free)
	retryCount  int
	fcbExpected bool // balanced mode: expected FCB of the peer's next FCV frame

	t1Timer      *time.Timer // Response timeout timer (T1/T2)
	t3Timer      *time.Timer // Idle timeout timer (T3)
	timerTrySend *time.Timer // Timer pacing polls and queued sends

	// Connection Status
	connStatus uint32 // statusInitial, statusConnecting, ...
	linkStatus uint32 // linkStateReset / linkStateActive (any station active)
	rwMux      sync.RWMutex

	// Logging and Control
	clog.Clog
	wg         sync.WaitGroup
	ctx        context.Context    // Controls the main run loop
	cancel     context.CancelFunc // Cancels the main run loop
	connCtx    context.Context    // Controls connection-specific loops (send/recv)
	connCancel context.CancelFunc // Cancels connection-specific loops

	onConnectCalled bool // Flag to ensure onConnect is called only once per connection
	// Callbacks
	onConnect        func(c *Client)
	onConnectionLost func(c *Client, err error)
	onConnectError   func(c *Client, err error)
}

// NewClient creates a new IEC101 primary station client.
func NewClient(handler ClientHandlerInterface, o *ClientOption) *Client {
	// Ensure option is valid, provide defaults if not
	opt := *o                        // Copy option
	tempLogger := clog.NewLogger("") // Temporary logger for init warnings
	tempLogger.LogMode(true)         // Enable temporary logger

	if err := opt.config.Valid(); err != nil {
		tempLogger.Warn("Invalid CS101 config provided, using defaults. Error: %v", err)
		opt.config = DefaultConfig() // Re-apply defaults, user still needs to set SerialConfig
	}
	if err := opt.params.Valid(); err != nil {
		tempLogger.Warn("Invalid ASDU params provided, using CS101 standard. Error: %v", err)
		opt.params = *asdu.ParamsStandard101
	}

	// Basic check for transport endpoint presence
	if opt.config.Transport == TransportSerial && opt.config.Serial.Address == "" {
		tempLogger.Error("Serial port address (e.g., COM3 or /dev/ttyS0) must be set in the config")
		return nil
	}
	if opt.config.Transport != TransportSerial && opt.config.TCP.Address == "" {
		tempLogger.Error("TCP address must be set in the config for the TCP transport")
		return nil
	}

	client := &Client{
		option:           opt,
		handler:          handler,
		rcvFrame:         make(chan *Frame, 20),
		sendFrame:        make(chan *Frame, 20),
		rcvASDU:          make(chan *asdu.ASDU, 50),
		linkReq:          make(chan byte, 8),
		sendQueue:        make([]outgoingASDU, 0, opt.config.MaxSendQueueSize),
		Clog:             clog.NewLogger(fmt.Sprintf("cs101 client [%s] => ", opt.config.transportLabel())),
		onConnect:        func(*Client) {},
		onConnectionLost: func(*Client, error) {},
		onConnectError:   func(*Client, error) {},
	}
	client.Clog.LogMode(true) // Enable logging by default
	return client
}

// SetLogMode enables or disables logging output.
func (sf *Client) SetLogMode(enable bool) {
	sf.Clog.LogMode(enable)
}

// SetOnConnectHandler sets the handler called upon successful connection.
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

// SetConnectErrorHandler sets the handler called when a connection attempt fails.
func (sf *Client) SetConnectErrorHandler(f func(c *Client, err error)) *Client {
	if f != nil {
		sf.onConnectError = f
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
	sf.connStatus = statusConnecting // Mark as starting
	sf.ctx, sf.cancel = context.WithCancel(context.Background())
	sf.rwMux.Unlock()

	go sf.connectionManager()
	return nil
}

// connectionManager handles the connection lifecycle and reconnection.
func (sf *Client) connectionManager() {
	sf.Debug("Connection manager started")
	transporter := NewTransporter(sf.option.config.Transport, sf.option.config.Serial, sf.option.config.TCP)
	defer func() {
		_ = transporter.Close()
		sf.setConnectStatus(statusInitial)
		sf.Debug("Connection manager stopped")
	}()

	for {
		select {
		case <-sf.ctx.Done(): // Check if client Close() was called
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
				return // Stop if auto-reconnect is disabled
			}
			select {
			case <-time.After(sf.option.reconnectInterval):
				continue // Wait and retry
			case <-sf.ctx.Done():
				return // Exit if Close() was called during wait
			}
		}

		sf.Debug("%s connected successfully", desc)
		sf.port = conn
		sf.setConnectStatus(statusConnected)

		// Create a context for this specific connection attempt
		sf.connCtx, sf.connCancel = context.WithCancel(sf.ctx)

		// Run the main protocol logic for this connection
		connectionErr := sf.runProtocol()

		// Cleanup after runProtocol returns (due to error or context cancellation)
		sf.setConnectStatus(statusDisconnected)
		_ = sf.port.Close() // Ensure port is closed
		sf.port = nil
		sf.connCancel() // Cancel connection-specific context

		if connectionErr != nil {
			sf.Warn("Protocol run ended with error: %v", connectionErr)
			sf.onConnectionLost(sf, connectionErr)
		} else {
			sf.Debug("Protocol run ended gracefully.")
			sf.onConnectionLost(sf, nil) // Indicate graceful stop if no error
		}

		// Check if Close() was called or if we should reconnect
		select {
		case <-sf.ctx.Done():
			return // Exit if Close() was called
		default:
			if !sf.option.autoReconnect {
				sf.Debug("Auto-reconnect disabled, stopping.")
				return
			}
			sf.Debug("Waiting %.1fs before attempting reconnection...", sf.option.reconnectInterval.Seconds())
			select {
			case <-time.After(sf.option.reconnectInterval):
				// Continue loop to reconnect
			case <-sf.ctx.Done():
				return // Exit if Close() was called during wait
			}
		}
	}
}

// isBalanced reports whether the link operates in balanced mode.
func (sf *Client) isBalanced() bool {
	return sf.option.config.Mode == ModeBalanced
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
	if len(addrs) == 0 || sf.isBalanced() {
		// balanced mode is point-to-point: single peer at the configured address
		addrs = []uint16{sf.option.config.LinkAddress}
	}
	sf.secs = make(map[uint16]*secondaryState, len(addrs))
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

// defaultAddr returns the link address used by Send and the command wrappers.
func (sf *Client) defaultAddr() uint16 {
	if len(sf.option.secondaryAddrs) > 0 && !sf.isBalanced() {
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

// runProtocol manages the communication loops and state machine for an active connection.
// Returns an error if the connection fails or the protocol encounters a fatal error.
func (sf *Client) runProtocol() error {
	sf.Debug("runProtocol started")
	atomic.StoreUint32(&sf.linkStatus, linkStateReset)
	sf.initSecondaries()
	sf.lastSent = nil
	sf.retryCount = 0
	sf.fcbExpected = true

	// Clear any pending send queue from a previous connection
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

	sf.onConnectCalled = false // Reset flag for this connection attempt
	// Start communication loops
	sf.wg.Add(3)
	go sf.frameRecvLoop()
	go sf.frameSendLoop()
	go sf.handlerLoop()

	// Initialize Timers
	sf.t1Timer = time.NewTimer(sf.option.config.TimeoutResponseT1)
	stopTimer(sf.t1Timer) // started when a confirmed frame is sent
	sf.t3Timer = time.NewTimer(sf.option.config.TimeoutTestT3)
	sf.timerTrySend = time.NewTimer(sf.option.config.TimeoutSendLinkMsg)

	defer sf.t1Timer.Stop()
	defer sf.t3Timer.Stop()
	defer sf.timerTrySend.Stop()

	for {
		select {
		case <-sf.connCtx.Done(): // Connection context cancelled
			sf.Debug("Connection context done, stopping protocol run.")
			sf.wg.Wait() // Wait for loops to finish
			return sf.connCtx.Err()

		case <-sf.timerTrySend.C: // Link maintenance / poll / queued sends
			sf.tick()
			sf.timerTrySend.Reset(sf.option.config.TimeoutSendLinkMsg)

		case fun := <-sf.linkReq: // manual link-layer request from the user API
			if sf.lastSent != nil {
				sf.Debug("Manual link request 0x%02X dropped: link busy", fun)
				break
			}
			sec := sf.secs[sf.defaultAddr()]
			if sec == nil {
				break
			}
			switch fun {
			case PrimFcResetLink:
				sf.sendResetLink(sec)
			case PrimFcReqStatus:
				sf.sendRequestLinkStatus(sec)
			case PrimFcReqData2:
				if sec.phase == phaseActive && !sf.isBalanced() {
					sf.sendReqData(sec, PrimFcReqData2)
				}
			}

		case <-sf.t1Timer.C: // T1 Timeout: No response received for confirmed message
			if sf.lastSent == nil {
				sf.Warn("T1 fired but no outstanding frame found.")
				break
			}
			if sf.retryCount < 1 {
				sf.retryCount++
				sf.Warn("T1 timeout, retrying last confirmed frame (Retry %d)", sf.retryCount)
				// Resend the exact same frame (same FCB)
				if err := sf.sendFrameToChan(sf.lastSent.frame); err != nil {
					sf.Error("Failed to resend frame on T1 retry: %v", err)
					return err // Connection likely lost
				}
				sf.t1Timer.Reset(sf.option.config.TimeoutRepeatT2) // Use T2 for retry timeout
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
				// Point-to-point (single secondary or balanced): treat total
				// loss of the peer as a connection failure so the manager can
				// reconnect. In multi-drop keep serving the other stations.
				if len(sf.secOrder) == 1 {
					return ErrTimeoutT1
				}
			}

		case <-sf.t3Timer.C: // T3 Timeout: Idle period expired
			sf.Debug("T3 idle timeout expired.")
			if sf.lastSent == nil {
				if sec := sf.firstActiveSecondary(); sec != nil {
					sf.sendTestLink(sec)
				}
			} else {
				sf.Debug("Skipping Test Link send (link busy)")
			}
			sf.t3Timer.Reset(sf.option.config.TimeoutTestT3)

		case frame := <-sf.rcvFrame:
			if frame.Start == SingleCharAck {
				sf.Debug("Received Single Char ACK")
			} else {
				sf.Debug("Received Frame: Start=0x%02X, Ctrl=%s, LinkAddr=%X", frame.Start, frame.GetControlField(), frame.LinkAddr)
			}

			// Reset T3 timer as we received something
			stopTimer(sf.t3Timer)
			sf.t3Timer.Reset(sf.option.config.TimeoutTestT3)

			if err := sf.handleIncomingFrame(frame); err != nil {
				sf.Error("Error handling incoming frame: %v", err)
			}
			// The handler clears lastSent when the outstanding transaction
			// completed; stop the response timer in that case.
			if sf.lastSent == nil {
				stopTimer(sf.t1Timer)
			}

			// Call onConnect callback upon receiving the first frame for this connection
			if !sf.onConnectCalled {
				sf.Debug("First frame received, calling onConnect handler.")
				sf.onConnectCalled = true
				sf.onConnect(sf)
			}
		}
	}
}

// tick drives the link when it is free: first queued user data, then link
// initialization and class polling (unbalanced), round-robin over stations.
func (sf *Client) tick() {
	if sf.lastSent != nil { // busy, T1 governs
		return
	}
	// queued user data to any active station goes first
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
			sf.sendResetLink(sec)
			return
		case phaseActive:
			if sf.isBalanced() {
				// no polling in balanced mode; peer transmits spontaneously
				continue
			}
			if sec.wantClass1 {
				sf.sendReqData(sec, PrimFcReqData1)
			} else {
				sf.sendReqData(sec, PrimFcReqData2)
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

// confirmPositive completes the outstanding transaction positively:
// toggles the station FCB (if the sent frame had FCV) and advances the
// link initialization phase. addrValid is false for E5 single char acks,
// which carry no link address.
func (sf *Client) confirmPositive(addr uint16, addrValid bool) {
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
		case PrimFcResetLink:
			sf.Debug("Link Reset confirmed by station %d. Link active.", p.addr)
			sec.phase = phaseActive
			sec.fcb = true // first FCV frame after reset carries FCB=1
			sf.updateLinkActive()
		case PrimFcReqStatus:
			if sec.phase == phaseStatus {
				sec.phase = phaseReset
			}
		case PrimFcReqData1:
			sec.wantClass1 = false
		}
	}
	sf.lastSent = nil
	sf.retryCount = 0
}

// failTransaction aborts the outstanding transaction without toggling FCB
// and downgrades the station link phase.
func (sf *Client) failTransaction(addr uint16) {
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
func (sf *Client) handleIncomingFrame(frame *Frame) error {
	// Single character ACK carries no address or control field.
	if frame.Start == SingleCharAck {
		sf.confirmPositive(0, false)
		return nil
	}

	receivedAddr := frame.GetLinkAddress()
	sec := sf.secs[receivedAddr]
	if sf.option.config.LinkAddrSize > 0 && sec == nil {
		sf.Warn("Ignoring frame with unexpected link address %d", receivedAddr)
		return nil // Ignore frames from unknown stations
	}

	ctrl := frame.GetControlField()

	if ctrl.PRM {
		// A frame from a primary station: only meaningful in balanced mode,
		// where the peer also acts as a primary.
		if !sf.isBalanced() {
			sf.Warn("Received unexpected frame with PRM=1 in unbalanced mode: %s", ctrl)
			return nil
		}
		return sf.handlePeerPrimaryFrame(frame, ctrl, receivedAddr)
	}

	if ctrl.DFC {
		sf.Warn("Station %d indicates Data Flow Control active (buffer likely full).", receivedAddr)
	}

	switch frame.Start {
	case StartFixed:
		switch ctrl.Fun {
		case SecFcConfACK: // positive acknowledge
			sf.confirmPositive(receivedAddr, true)

		case SecFcConfNACK: // link busy — keep outstanding, T1/T2 will retry
			sf.Warn("Received NACK (link busy) from station %d.", receivedAddr)

		case SecFcUserDataNoRep: // NACK: requested data not available
			sf.Debug("Station %d: requested data not available.", receivedAddr)
			sf.confirmPositive(receivedAddr, true)

		case SecFcRespStatus: // status of link
			sf.Debug("Received Link Status from station %d (DFC=%v)", receivedAddr, ctrl.DFC)
			sf.confirmPositive(receivedAddr, true)

		case SecFcRespLinkNF, SecFcRespLinkNI:
			sf.Warn("Station %d link service not functioning/implemented (FC=%d).", receivedAddr, ctrl.Fun)
			sf.failTransaction(receivedAddr)

		default:
			sf.Warn("Received unhandled fixed-length frame from station %d: %s", receivedAddr, ctrl)
		}

	case StartVariable:
		switch ctrl.Fun {
		case SecFcUserDataConf, SecFcRespStatus: // responded user data
			sf.Debug("Received user data from station %d", receivedAddr)
			sf.confirmPositive(receivedAddr, true)
			if a := frame.GetASDU(&sf.option.params); a != nil {
				sf.dispatchASDU(a)
			} else {
				sf.Warn("Failed to decode ASDU from variable frame")
			}

		case SecFcUserDataNoRep:
			sf.Debug("Station %d: no user data available.", receivedAddr)
			sf.confirmPositive(receivedAddr, true)

		default:
			sf.Warn("Received unhandled variable-length frame from station %d: %s", receivedAddr, ctrl)
		}
	}

	// Access demand: the station has class 1 data waiting.
	if ctrl.ACD && sec != nil && !sf.isBalanced() {
		sec.wantClass1 = true
		if sf.lastSent == nil && sec.phase == phaseActive {
			sf.sendReqData(sec, PrimFcReqData1)
		}
	}

	return nil
}

// handlePeerPrimaryFrame services the secondary role of this station in
// balanced mode: the remote peer acts as a primary and sends us link
// commands and user data, which we must acknowledge.
func (sf *Client) handlePeerPrimaryFrame(frame *Frame, ctrl ControlField, addr uint16) error {
	// FCB duplicate detection for FCV frames from the peer.
	if ctrl.FCV {
		if ctrl.FCB != sf.fcbExpected {
			sf.Warn("Peer frame with unexpected FCB: repeated frame, re-sending ACK.")
			return sf.sendSecFixed(SecFcConfACK, addr)
		}
		sf.fcbExpected = !sf.fcbExpected
	}

	switch ctrl.Fun {
	case PrimFcResetLink:
		sf.Debug("Peer requested Reset Link.")
		sf.fcbExpected = true // first FCV frame after reset carries FCB=1
		return sf.sendSecFixed(SecFcConfACK, addr)

	case PrimFcResetUser:
		sf.Debug("Peer requested Reset User Process.")
		return sf.sendSecFixed(SecFcConfACK, addr)

	case PrimFcTestLink:
		sf.Debug("Peer sent Test Link.")
		return sf.sendSecFixed(SecFcConfACK, addr)

	case PrimFcUserDataConf:
		sf.Debug("Peer sent confirmed user data.")
		if a := frame.GetASDU(&sf.option.params); a != nil {
			sf.dispatchASDU(a)
		} else {
			sf.Warn("Failed to decode ASDU from peer user data frame")
		}
		return sf.sendSecFixed(SecFcConfACK, addr)

	case PrimFcUserDataNoConf:
		sf.Debug("Peer sent unconfirmed user data.")
		if a := frame.GetASDU(&sf.option.params); a != nil {
			sf.dispatchASDU(a)
		}
		return nil

	case PrimFcReqStatus:
		sf.Debug("Peer requested link status.")
		return sf.sendSecFixed(SecFcRespStatus, addr)

	default:
		sf.Warn("Peer sent unhandled primary frame: %s", ctrl)
		return sf.sendSecFixed(SecFcRespLinkNI, addr)
	}
}

// dispatchASDU hands a decoded ASDU to the handler loop.
func (sf *Client) dispatchASDU(a *asdu.ASDU) {
	select {
	case sf.rcvASDU <- a:
	case <-sf.connCtx.Done():
	}
}

// frameRecvLoop reads from the serial port and parses CS101 frames.
func (sf *Client) frameRecvLoop() {
	sf.Debug("frameRecvLoop started")
	defer func() {
		sf.connCancel() // Signal connection failure/closure to runProtocol
		sf.wg.Done()
		sf.Debug("frameRecvLoop stopped")
	}()

	for {
		select {
		case <-sf.connCtx.Done():
			return
		default:
			frame, err := ParseFrame(sf.port, sf.option.config.LinkAddrSize, &sf.connCtx)
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
					sf.Debug("Serial port closed or EOF reached.")
					return // Normal closure or disconnect
				}
				// Checksum/framing errors might be recoverable or indicate noise
				if errors.Is(err, ErrChecksumMismatch) || errors.Is(err, ErrInvalidStartChar) ||
					errors.Is(err, ErrLengthMismatch) || errors.Is(err, ErrInvalidEndChar) ||
					errors.Is(err, ErrFrameTooShort) || errors.Is(err, ErrFrameLenExceeded) {
					sf.Warn("Frame parsing error: %v. Attempting to recover.", err)
					continue
				}
				// Other errors might be more severe
				sf.Error("Error reading/parsing frame: %v", err)
				return // Treat other errors as fatal for this connection attempt
			}

			// Send valid frame to the main processing loop
			select {
			case sf.rcvFrame <- frame:
			case <-sf.connCtx.Done():
				return
			}
		}
	}
}

// frameSendLoop takes frames from the send channel and writes them to the serial port.
func (sf *Client) frameSendLoop() {
	sf.Debug("frameSendLoop started")
	defer func() {
		sf.connCancel() // Signal connection failure/closure to runProtocol
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

			rawData, err := frame.MarshalBinary(sf.option.config.LinkAddrSize)
			if err != nil {
				sf.Error("Failed to marshal frame: %v", err)
				continue // Skip this frame
			}

			sf.Debug("TX Raw Frame [% X]", rawData)
			_, err = sf.port.Write(rawData)
			if err != nil {
				sf.Error("Failed to write frame to serial port: %v", err)
				// Assume connection is lost on write error
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
		case <-sf.connCtx.Done(): // Use connection context here
			return
		case asduPack := <-sf.rcvASDU:
			sf.Debug("Processing ASDU: %s", asduPack.Identifier)
			if err := sf.callHandler(asduPack); err != nil {
				sf.Warn("Error in ASDU handler: %v (ASDU: %s)", err, asduPack.Identifier)
			}
		}
	}
}

// callHandler safely calls the appropriate user-defined handler function.
func (sf *Client) callHandler(asduPack *asdu.ASDU) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic recovered in handler: %v", r)
			sf.Critical("%v", err) // Log critical error on panic
		}
	}()

	// Call the generic handler first
	if handlerErr := sf.handler.ASDUHandlerAll(asduPack, sf.clientNumber); handlerErr != nil {
		sf.Warn("Error in ASDUHandlerAll: %v", handlerErr)
	}

	// Determine specific handler based on TypeID AND CauseOfTransmission
	cause := asduPack.Identifier.Coa.Cause
	typeID := asduPack.Identifier.Type

	switch {
	// Interrogation Responses
	case (cause == asdu.InterrogatedByStation || (cause >= asdu.InterrogatedByGroup1 && cause <= asdu.InterrogatedByGroup16)):
		if typeID >= asdu.M_SP_NA_1 && typeID <= asdu.M_EP_TF_1 {
			err = sf.handler.InterrogationHandler(asduPack)
		} else {
			sf.Warn("Received unexpected TypeID (%s) for Interrogation COT (%s)", typeID, cause)
			err = sf.handler.ASDUHandler(asduPack, sf.clientNumber) // Fallback to generic
		}

	// Counter Interrogation Responses
	case (cause == asdu.RequestByGeneralCounter || (cause >= asdu.RequestByGroup1Counter && cause <= asdu.RequestByGroup4Counter)):
		if typeID == asdu.M_IT_NA_1 || typeID == asdu.M_IT_TA_1 || typeID == asdu.M_IT_TB_1 {
			err = sf.handler.CounterInterrogationHandler(asduPack)
		} else {
			sf.Warn("Received unexpected TypeID (%s) for Counter Interrogation COT (%s)", typeID, cause)
			err = sf.handler.ASDUHandler(asduPack, sf.clientNumber) // Fallback to generic
		}

	// Activation Confirmation / Termination
	case cause == asdu.ActivationCon || cause == asdu.ActivationTerm:
		if typeID >= asdu.C_SC_NA_1 && typeID <= asdu.C_BO_NA_1 {
			sf.Debug("Received Activation Confirmation/Termination: %s", asduPack.Identifier)
			err = sf.handler.ASDUHandler(asduPack, sf.clientNumber)
		} else {
			sf.Warn("Received unexpected TypeID (%s) for ActivationCon/Term COT (%s)", typeID, cause)
			err = sf.handler.ASDUHandler(asduPack, sf.clientNumber) // Fallback to generic
		}

	// End of Initialization
	case typeID == asdu.M_EI_NA_1 && cause == asdu.Initialized:
		sf.Debug("Received End of Initialization from secondary station.")
		err = sf.handler.ASDUHandler(asduPack, sf.clientNumber)

	// Default to the generic handler for everything else
	default:
		err = sf.handler.ASDUHandler(asduPack, sf.clientNumber)
	}

	return err
}

// setConnectStatus updates the connection status atomically.
func (sf *Client) setConnectStatus(status uint32) {
	atomic.StoreUint32(&sf.connStatus, status)
}

// IsConnected returns true if the client is currently connected.
func (sf *Client) IsConnected() bool {
	return atomic.LoadUint32(&sf.connStatus) == statusConnected
}

// IsLinkActive returns true if at least one secondary station link is active.
func (sf *Client) IsLinkActive() bool {
	return sf.IsConnected() && atomic.LoadUint32(&sf.linkStatus) == linkStateActive
}

// Close disconnects the client and stops all background goroutines.
func (sf *Client) Close() error {
	sf.rwMux.Lock()
	if sf.cancel == nil { // Already closed or never started
		sf.rwMux.Unlock()
		return errors.New("client not running")
	}
	sf.Debug("Close requested.")
	sf.cancel()     // Signal connectionManager to stop
	sf.cancel = nil // Prevent double close
	sf.rwMux.Unlock()
	// connectionManager will handle closing the port and stopping loops;
	// it resets the status to initial when it exits.
	return nil
}

// --- CS101 Specific Actions ---

// encodeLinkAddr encodes a link address for the configured address size.
func (sf *Client) encodeLinkAddr(addr uint16) []byte {
	switch sf.option.config.LinkAddrSize {
	case 1:
		return []byte{byte(addr)}
	case 2:
		buf := make([]byte, 2)
		binary.LittleEndian.PutUint16(buf, addr)
		return buf
	default: // Size 0 or invalid
		return nil
	}
}

// primControl builds a primary control field byte. In balanced mode this
// station acts as station A and sets the DIR bit on everything it sends.
func (sf *Client) primControl(fun byte, fcv, fcb bool) byte {
	control := CtrlPRM | (fun & CtrlFuncMask)
	if sf.isBalanced() {
		control |= CtrlDIR
	}
	if fcv {
		control |= CtrlFCV
		if fcb {
			control |= CtrlFCB
		}
	}
	return control
}

// sendFrameToChan queues a frame on the send channel.
func (sf *Client) sendFrameToChan(frame *Frame) error {
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
	control := sf.primControl(fun, fcv, sec.fcb)
	frame := NewFixedFrame(control, sf.encodeLinkAddr(sec.addr))
	err := sf.sendFrameToChan(frame)
	if err == nil {
		sf.lastSent = &pendingSend{frame: frame, ctrl: ParseControlField(control), addr: sec.addr}
		sf.retryCount = 0
		stopTimer(sf.t1Timer)
		sf.t1Timer.Reset(sf.option.config.TimeoutResponseT1)
	}
	return err
}

// sendSecFixed sends a fixed-length secondary (PRM=0) response frame.
// Used by the balanced-mode secondary role of this station.
func (sf *Client) sendSecFixed(fun byte, addr uint16) error {
	control := fun & CtrlFuncMask
	if sf.isBalanced() {
		control |= CtrlDIR
	}
	return sf.sendFrameToChan(NewFixedFrame(control, sf.encodeLinkAddr(addr)))
}

// sendResetLink sends a Reset of Remote Link command (FCV=0).
func (sf *Client) sendResetLink(sec *secondaryState) {
	sf.Debug("Sending Reset Link command to station %d", sec.addr)
	sec.phase = phaseReset
	sf.updateLinkActive()
	if err := sf.sendPrimConfirmed(sec, PrimFcResetLink, false); err != nil {
		sf.Error("Failed to send Reset Link: %v", err)
	}
}

// sendRequestLinkStatus sends a Request Status of Link command (FCV=0).
func (sf *Client) sendRequestLinkStatus(sec *secondaryState) {
	sf.Debug("Sending Request Link Status command to station %d", sec.addr)
	if err := sf.sendPrimConfirmed(sec, PrimFcReqStatus, false); err != nil {
		sf.Error("Failed to send Request Link Status: %v", err)
	}
}

// sendTestLink sends a Test Function of Link command (FCV=1).
func (sf *Client) sendTestLink(sec *secondaryState) {
	sf.Debug("Sending Test Link command to station %d", sec.addr)
	if err := sf.sendPrimConfirmed(sec, PrimFcTestLink, true); err != nil {
		sf.Error("Failed to send Test Link: %v", err)
	}
}

// sendReqData sends a Request User Data Class 1 or Class 2 command (FCV=1).
func (sf *Client) sendReqData(sec *secondaryState, fun byte) {
	sf.Debug("Sending Request User Data Class %d command to station %d", fun-PrimFcReqData1+1, sec.addr)
	if err := sf.sendPrimConfirmed(sec, fun, true); err != nil {
		sf.Error("Failed to send Request Data: %v", err)
	}
}

// queueLinkReq forwards a manual link-layer request to the run loop, which
// owns the link state (safe to call from any goroutine).
func (sf *Client) queueLinkReq(fun byte) error {
	if !sf.IsConnected() {
		return ErrUseClosedConnection
	}
	select {
	case sf.linkReq <- fun:
		return nil
	default:
		return ErrBufferFulled
	}
}

// SendResetLink requests a Reset of Remote Link to the default secondary
// station. The command is executed by the protocol loop when the link is free.
func (sf *Client) SendResetLink() error {
	return sf.queueLinkReq(PrimFcResetLink)
}

// SendRequestLinkStatus requests a Request Status of Link to the default
// secondary station. Executed by the protocol loop when the link is free.
func (sf *Client) SendRequestLinkStatus() error {
	return sf.queueLinkReq(PrimFcReqStatus)
}

// SendReqDataClass2 requests a Request User Data Class 2 poll of the default
// secondary station. Executed by the protocol loop when the link is free.
// Note the client polls class 1/2 data automatically in unbalanced mode.
func (sf *Client) SendReqDataClass2() error {
	return sf.queueLinkReq(PrimFcReqData2)
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

	control := sf.primControl(PrimFcUserDataConf, true, sec.fcb)
	frame := NewDataFrame(control, sf.encodeLinkAddr(sec.addr), asduData)

	sf.Debug("Sending User Data to station %d (ASDU Type: %s, FCB: %v)", sec.addr, out.a.Identifier.Type, sec.fcb)

	err = sf.sendFrameToChan(frame)
	if err == nil {
		sf.lastSent = &pendingSend{frame: frame, ctrl: ParseControlField(control), addr: sec.addr}
		sf.retryCount = 0
		stopTimer(sf.t1Timer)
		sf.t1Timer.Reset(sf.option.config.TimeoutResponseT1)
		// FCB toggles only after the positive confirm arrives.
	}
	return err
}

// Send queues an ASDU for transmission to the default secondary station.
func (sf *Client) Send(a *asdu.ASDU) error {
	return sf.SendTo(a, sf.defaultAddr())
}

// SendTo queues an ASDU for transmission to the secondary station with the
// given link address (unbalanced multi-drop). The protocol loop transmits it
// as confirmed user data when that station's link is free and active.
func (sf *Client) SendTo(a *asdu.ASDU, linkAddr uint16) error {
	if !sf.IsConnected() { // Basic connection check
		return ErrUseClosedConnection
	}

	sf.sendQueueMutex.Lock()
	defer sf.sendQueueMutex.Unlock()
	if len(sf.sendQueue) >= sf.option.config.MaxSendQueueSize {
		sf.Warn("Send queue full, discarding ASDU: %s. Queue size: %d", a.Identifier, len(sf.sendQueue))
		return ErrSendQueueFull
	}
	sf.sendQueue = append(sf.sendQueue, outgoingASDU{a: a, addr: linkAddr})
	sf.Debug("ASDU %s enqueued for station %d. Queue size: %d", a.Identifier, linkAddr, len(sf.sendQueue))
	return nil
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
			sf.Error("Dropping queued ASDU %s: unknown station %d", e.a.Identifier, e.addr)
			sf.sendQueue = append(sf.sendQueue[:i], sf.sendQueue[i+1:]...)
			continue
		}
		if sec.phase == phaseActive {
			next = e
			sf.sendQueue = append(sf.sendQueue[:i], sf.sendQueue[i+1:]...)
			found = true
			break
		}
		i++ // target not active yet: keep queued, try later entries
	}
	sf.sendQueueMutex.Unlock()

	if !found {
		return false
	}
	if err := sf._sendASDU(next); err != nil {
		sf.Error("Failed to send ASDU from queue (%s): %v. ASDU lost.", next.a.Identifier, err)
		return false
	}
	return true
}

// --- ASDU Command Wrappers ---

// InterrogationCmd sends a C_IC_NA_1 ASDU.
func (sf *Client) InterrogationCmd(coa asdu.CauseOfTransmission, ca asdu.CommonAddr, qoi asdu.QualifierOfInterrogation) error {
	a := asdu.NewEmptyASDU(&sf.option.params) // Create empty ASDU first
	a.Identifier = asdu.Identifier{           // Set identifier
		Type:       asdu.C_IC_NA_1,
		Variable:   asdu.VariableStruct{Number: 1}, // Single object
		Coa:        coa,
		CommonAddr: ca,
	}

	// Manually construct the info object bytes: IOA + QOI
	ioaBytes := encodeIOA(0, &sf.option.params) // IOA=0 for station interrogation
	qoiByte := byte(qoi)
	infoBytes := append(ioaBytes, qoiByte)

	// Construct the full raw ASDU bytes
	rawASDU, err := buildRawASDU(a.Identifier, infoBytes, &sf.option.params)
	if err != nil {
		return fmt.Errorf("failed to build raw ASDU for InterrogationCmd: %w", err)
	}

	// Unmarshal into the ASDU struct to populate internal fields correctly
	if err := a.UnmarshalBinary(rawASDU); err != nil {
		return fmt.Errorf("failed to unmarshal locally built ASDU for InterrogationCmd: %w", err)
	}

	return sf.Send(a)
}

// CounterInterrogationCmd sends a C_CI_NA_1 ASDU.
func (sf *Client) CounterInterrogationCmd(coa asdu.CauseOfTransmission, ca asdu.CommonAddr, qcc asdu.QualifierCountCall) error {
	a := asdu.NewEmptyASDU(&sf.option.params) // Create empty ASDU first
	a.Identifier = asdu.Identifier{           // Set identifier
		Type:       asdu.C_CI_NA_1,
		Variable:   asdu.VariableStruct{Number: 1},
		Coa:        coa,
		CommonAddr: ca,
	}

	// Manually construct the info object bytes: IOA + QCC
	ioaBytes := encodeIOA(0, &sf.option.params) // IOA=0 for counter interrogation
	qccByte := qcc.Value()
	infoBytes := append(ioaBytes, qccByte)

	// Construct the full raw ASDU bytes
	rawASDU, err := buildRawASDU(a.Identifier, infoBytes, &sf.option.params)
	if err != nil {
		return fmt.Errorf("failed to build raw ASDU for CounterInterrogationCmd: %w", err)
	}
	if err := a.UnmarshalBinary(rawASDU); err != nil {
		return fmt.Errorf("failed to unmarshal locally built ASDU for CounterInterrogationCmd: %w", err)
	}

	return sf.Send(a)
}

// ReadCmd sends a C_RD_NA_1 ASDU.
func (sf *Client) ReadCmd(coa asdu.CauseOfTransmission, ca asdu.CommonAddr, ioa asdu.InfoObjAddr) error {
	a := asdu.NewEmptyASDU(&sf.option.params)
	a.Identifier = asdu.Identifier{
		Type:       asdu.C_RD_NA_1,
		Variable:   asdu.VariableStruct{Number: 1},
		Coa:        coa,
		CommonAddr: ca,
	}

	// Manually construct the info object bytes: IOA only
	ioaBytes := encodeIOA(ioa, &sf.option.params) // Specific IOA
	infoBytes := ioaBytes                         // No element data for ReadCmd

	// Construct the full raw ASDU bytes
	rawASDU, err := buildRawASDU(a.Identifier, infoBytes, &sf.option.params)
	if err != nil {
		return fmt.Errorf("failed to build raw ASDU for ReadCmd: %w", err)
	}
	if err := a.UnmarshalBinary(rawASDU); err != nil {
		return fmt.Errorf("failed to unmarshal locally built ASDU for ReadCmd: %w", err)
	}

	return sf.Send(a)
}

// ClockSynchronizationCmd sends a C_CS_NA_1 ASDU.
func (sf *Client) ClockSynchronizationCmd(coa asdu.CauseOfTransmission, ca asdu.CommonAddr, t time.Time) error {
	a := asdu.NewEmptyASDU(&sf.option.params)
	a.Identifier = asdu.Identifier{
		Type:       asdu.C_CS_NA_1,
		Variable:   asdu.VariableStruct{Number: 1},
		Coa:        coa,
		CommonAddr: ca,
	}

	// Manually construct the info object bytes: IOA + CP56Time2a
	ioaBytes := encodeIOA(0, &sf.option.params) // IOA=0 for clock sync
	timeBytes := asdu.CP56Time2a(t, sf.option.params.InfoObjTimeZone)
	infoBytes := append(ioaBytes, timeBytes...)

	// Construct the full raw ASDU bytes
	rawASDU, err := buildRawASDU(a.Identifier, infoBytes, &sf.option.params)
	if err != nil {
		return fmt.Errorf("failed to build raw ASDU for ClockSynchronizationCmd: %w", err)
	}
	if err := a.UnmarshalBinary(rawASDU); err != nil {
		return fmt.Errorf("failed to unmarshal locally built ASDU for ClockSynchronizationCmd: %w", err)
	}

	return sf.Send(a)
}

// ResetProcessCmd sends a C_RP_NA_1 ASDU.
func (sf *Client) ResetProcessCmd(coa asdu.CauseOfTransmission, ca asdu.CommonAddr, qrp asdu.QualifierOfResetProcessCmd) error {
	a := asdu.NewEmptyASDU(&sf.option.params)
	a.Identifier = asdu.Identifier{
		Type:       asdu.C_RP_NA_1,
		Variable:   asdu.VariableStruct{Number: 1},
		Coa:        coa,
		CommonAddr: ca,
	}

	// Manually construct the info object bytes: IOA + QRP
	ioaBytes := encodeIOA(0, &sf.option.params) // IOA=0 for reset process
	qrpByte := byte(qrp)
	infoBytes := append(ioaBytes, qrpByte)

	// Construct the full raw ASDU bytes
	rawASDU, err := buildRawASDU(a.Identifier, infoBytes, &sf.option.params)
	if err != nil {
		return fmt.Errorf("failed to build raw ASDU for ResetProcessCmd: %w", err)
	}
	if err := a.UnmarshalBinary(rawASDU); err != nil {
		return fmt.Errorf("failed to unmarshal locally built ASDU for ResetProcessCmd: %w", err)
	}

	return sf.Send(a)
}

// DelayAcquireCommand sends a C_CD_NA_1 ASDU.
func (sf *Client) DelayAcquireCommand(coa asdu.CauseOfTransmission, ca asdu.CommonAddr, msec uint16) error {
	a := asdu.NewEmptyASDU(&sf.option.params)
	a.Identifier = asdu.Identifier{
		Type:       asdu.C_CD_NA_1,
		Variable:   asdu.VariableStruct{Number: 1},
		Coa:        coa,
		CommonAddr: ca,
	}

	// Manually construct the info object bytes: IOA + BinaryTime2a (CP16Time2a)
	ioaBytes := encodeIOA(0, &sf.option.params) // IOA=0 for delay acquisition
	timeBytes := asdu.CP16Time2a(msec)
	infoBytes := append(ioaBytes, timeBytes...)

	// Construct the full raw ASDU bytes
	rawASDU, err := buildRawASDU(a.Identifier, infoBytes, &sf.option.params)
	if err != nil {
		return fmt.Errorf("failed to build raw ASDU for DelayAcquireCommand: %w", err)
	}
	if err := a.UnmarshalBinary(rawASDU); err != nil {
		return fmt.Errorf("failed to unmarshal locally built ASDU for DelayAcquireCommand: %w", err)
	}

	return sf.Send(a)
}

// TestCommand sends a C_TS_NA_1 ASDU.
func (sf *Client) TestCommand(coa asdu.CauseOfTransmission, ca asdu.CommonAddr) error {
	a := asdu.NewEmptyASDU(&sf.option.params)
	a.Identifier = asdu.Identifier{
		Type:       asdu.C_TS_NA_1,
		Variable:   asdu.VariableStruct{Number: 1},
		Coa:        coa,
		CommonAddr: ca,
	}

	// Manually construct the info object bytes: IOA + Test Pattern
	ioaBytes := encodeIOA(0, &sf.option.params) // IOA=0 for test command
	testPattern := []byte{0xAA, 0x55}           // Standard test pattern (FBP 0x55AA little endian)
	infoBytes := append(ioaBytes, testPattern...)

	// Construct the full raw ASDU bytes
	rawASDU, err := buildRawASDU(a.Identifier, infoBytes, &sf.option.params)
	if err != nil {
		return fmt.Errorf("failed to build raw ASDU for TestCommand: %w", err)
	}
	if err := a.UnmarshalBinary(rawASDU); err != nil {
		return fmt.Errorf("failed to unmarshal locally built ASDU for TestCommand: %w", err)
	}

	return sf.Send(a)
}

// --- Helper Functions ---

// encodeIOA encodes the Information Object Address based on configured size.
func encodeIOA(ioa asdu.InfoObjAddr, params *asdu.Params) []byte {
	buf := make([]byte, params.InfoObjAddrSize)
	switch params.InfoObjAddrSize {
	case 1:
		buf[0] = byte(ioa)
	case 2:
		binary.LittleEndian.PutUint16(buf, uint16(ioa))
	case 3:
		buf[0] = byte(ioa)
		buf[1] = byte(ioa >> 8)
		buf[2] = byte(ioa >> 16)
	}
	return buf
}

// buildRawASDU constructs the full byte slice for an ASDU.
func buildRawASDU(identifier asdu.Identifier, infoBytes []byte, params *asdu.Params) ([]byte, error) {
	headerSize := params.IdentifierSize()
	totalSize := headerSize + len(infoBytes)
	if totalSize > asdu.ASDUSizeMax {
		return nil, errors.New("ASDU size exceeds maximum")
	}

	raw := make([]byte, totalSize)

	// Manually encode the identifier based on asdu.MarshalBinary logic
	raw[0] = byte(identifier.Type)
	raw[1] = identifier.Variable.Value()
	raw[2] = identifier.Coa.Value()
	offset := 3
	if params.CauseSize == 2 {
		if offset >= headerSize {
			return nil, errors.New("header size mismatch calculation (cause)")
		}
		raw[offset] = byte(identifier.OrigAddr)
		offset++
	}
	if params.CommonAddrSize == 1 {
		if offset >= headerSize {
			return nil, errors.New("header size mismatch calculation (common addr 1)")
		}
		if identifier.CommonAddr == asdu.GlobalCommonAddr {
			raw[offset] = 255
		} else {
			raw[offset] = byte(identifier.CommonAddr)
		}
		offset++
	} else { // 2
		if offset+1 >= headerSize {
			return nil, errors.New("header size mismatch calculation (common addr 2)")
		}
		raw[offset] = byte(identifier.CommonAddr)
		offset++
		raw[offset] = byte(identifier.CommonAddr >> 8)
		offset++
	}

	// Check if manually calculated offset matches expected header size
	if offset != headerSize {
		return nil, fmt.Errorf("internal error: calculated header offset %d does not match expected size %d", offset, headerSize)
	}

	// Append the information object bytes
	copy(raw[headerSize:], infoBytes)

	return raw, nil
}

// --- Interface Implementations ---

// Params returns the ASDU parameters used by the client.
func (sf *Client) Params() *asdu.Params {
	return &sf.option.params
}

// UnderlyingConn is required by the asdu.Connect interface. A serial port is
// not a net.Conn, so this returns nil; use Port to access the serial port.
func (sf *Client) UnderlyingConn() net.Conn {
	return nil
}

// Port returns the underlying serial port (nil when disconnected).
func (sf *Client) Port() io.ReadWriteCloser {
	return sf.port
}

func (sf *Client) SetClientNumber(n int) {
	sf.clientNumber = n
}

func (sf *Client) ClientNumber() int {
	return sf.clientNumber
}

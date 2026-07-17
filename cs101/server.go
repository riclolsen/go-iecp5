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

// Server represents an IEC101 Secondary Station (Slave). In balanced mode it
// additionally runs a primary role so it can transmit spontaneously.
type Server struct {
	config  Config
	params  asdu.Params
	handler ServerHandlerInterface
	port    io.ReadWriteCloser // Serial port connection

	// Channels for internal communication
	rcvFrame       chan *Frame             // Parsed incoming frames
	sendFrame      chan *Frame             // Frames to be sent
	handlerPayload chan handlerASDUPayload // Identifier + Raw ASDU bytes for handler

	// CS101 Link Layer State (secondary role) — owned by the handlerLoop goroutine.
	// The FCB alternates across ALL frames received with FCV=1 (user data,
	// class 1/2 requests, test link), as required by IEC 60870-5-2.
	linkStatus  uint32 // linkStateReset, linkStateActive
	fcbExpected bool   // Expected FCB state of the next FCV frame from the primary
	lastResp    *Frame // Last response sent to a confirmed frame; retransmitted on FCB mismatch

	// Class 1/2 output buffers (unbalanced mode). The primary collects them
	// with Request User Data Class 1/2 polls; ACD signals pending class 1 data.
	dataMux sync.Mutex
	class1  []*asdu.ASDU
	class2  []*asdu.ASDU

	// Primary role state (balanced mode) — owned by the handlerLoop goroutine
	// except primQueue, which is guarded by dataMux.
	primQueue    []*asdu.ASDU
	primOut      *Frame       // outstanding confirmed frame sent by our primary role
	primOutCtrl  ControlField // control field of primOut
	primFCB      bool         // FCB of the next FCV frame we send as primary
	primActive   bool         // our transmit direction is initialized (ResetLink ACKed)
	primRetry    int
	primDeadline time.Time // response deadline for primOut
	primNextInit time.Time // throttle for ResetLink attempts

	// Connection Status & Control
	connStatus        uint32 // statusInitial, statusConnecting, etc.
	reconnectInterval time.Duration
	rwMux             sync.RWMutex
	clog.Clog
	wg         sync.WaitGroup
	ctx        context.Context    // Controls the main run loop
	cancel     context.CancelFunc // Cancels the main run loop
	connCtx    context.Context    // Controls connection-specific loops
	connCancel context.CancelFunc // Cancels connection-specific loops
}

// handlerASDUPayload holds data needed by the handler loop
type handlerASDUPayload struct {
	Identifier asdu.Identifier
	RawData    []byte
}

// NewServer creates a new IEC101 secondary station server.
func NewServer(handler ServerHandlerInterface) *Server {
	// Use default config and params initially. User should set them via methods.
	srv := &Server{
		config:            DefaultConfig(),
		params:            *asdu.ParamsStandard101, // Use CS101 standard params
		handler:           handler,
		rcvFrame:          make(chan *Frame, 20),
		sendFrame:         make(chan *Frame, 20),
		handlerPayload:    make(chan handlerASDUPayload, 50),
		reconnectInterval: DefaultReconnectInterval,
		Clog:              clog.NewLogger("cs101 server => "), // Placeholder address initially
	}
	srv.Clog.LogMode(true) // Enable logging by default
	return srv
}

// SetConfig sets the CS101 configuration. Must be called before Start.
func (sf *Server) SetConfig(cfg Config) *Server {
	if err := cfg.Valid(); err != nil {
		sf.Warn("Invalid CS101 config provided: %v. Using previous/default.", err)
	} else {
		sf.config = cfg
		// Update logger prefix if address changed
		sf.Clog = clog.NewLogger(fmt.Sprintf("cs101 server [%s] => ", cfg.transportLabel()))
		sf.Clog.LogMode(true) // Re-enable logging
	}
	return sf
}

// SetParams sets the ASDU parameters. Must be called before Start.
func (sf *Server) SetParams(p *asdu.Params) *Server {
	if err := p.Valid(); err != nil {
		sf.Warn("Invalid ASDU params provided: %v. Using CS101 standard.", err)
		sf.params = *asdu.ParamsStandard101
	} else {
		sf.params = *p
	}
	return sf
}

// SetLogMode enables or disables logging output.
func (sf *Server) SetLogMode(enable bool) {
	sf.Clog.LogMode(enable)
}

// isBalanced reports whether the link operates in balanced mode.
func (sf *Server) isBalanced() bool {
	return sf.config.Mode == ModeBalanced
}

// SetReconnectInterval sets the retry interval used by the TCP transports
// after a failed open (default 1 minute). Not used with the serial
// transport, which is opened once.
func (sf *Server) SetReconnectInterval(t time.Duration) *Server {
	if t > 0 {
		sf.reconnectInterval = t
	}
	return sf
}

// Start opens the configured transport and begins processing frames.
func (sf *Server) Start() error {
	sf.rwMux.Lock()
	if atomic.LoadUint32(&sf.connStatus) != statusInitial {
		sf.rwMux.Unlock()
		return errors.New("server already started or starting")
	}
	if sf.config.Transport == TransportSerial && sf.config.Serial.Address == "" {
		sf.rwMux.Unlock()
		return errors.New("serial port address not configured")
	}
	if sf.config.Transport != TransportSerial && sf.config.TCP.Address == "" {
		sf.rwMux.Unlock()
		return errors.New("TCP address not configured for the TCP transport")
	}
	sf.setConnectStatus(statusConnecting)
	sf.ctx, sf.cancel = context.WithCancel(context.Background())
	sf.rwMux.Unlock()

	go sf.run()
	return nil
}

// run manages the transport connection(s) and protocol loops. With the
// serial transport it serves the port until closed (historic behavior);
// with the TCP transports it keeps accepting/redialing until Close is
// called, serving one connection at a time.
func (sf *Server) run() {
	sf.Debug("Server run loop started")
	transporter := NewTransporter(sf.config.Transport, sf.config.Serial, sf.config.TCP)
	defer func() {
		_ = transporter.Close()
		sf.setConnectStatus(statusInitial)
		sf.Debug("Server run loop stopped")
	}()

	for {
		select {
		case <-sf.ctx.Done():
			return
		default:
		}

		sf.setConnectStatus(statusConnecting)
		sf.Debug("Opening %s transport (%s)...", sf.config.Transport, sf.config.transportLabel())
		conn, desc, err := transporter.Open(sf.ctx)
		if err != nil {
			if sf.ctx.Err() != nil {
				return
			}
			sf.Error("Failed to open transport: %v", err)
			sf.setConnectStatus(statusDisconnected)
			if sf.config.Transport == TransportSerial {
				return // historic behavior: a serial port is opened once
			}
			select {
			case <-time.After(sf.reconnectInterval):
				continue
			case <-sf.ctx.Done():
				return
			}
		}

		sf.Debug("%s connected", desc)
		sf.serveConn(conn)

		if sf.config.Transport == TransportSerial {
			return // serial session ended (Close or port failure)
		}
		// TCP: loop back to accept/redial the next connection
	}
}

// serveConn runs the protocol loops over one established connection until
// it ends (Close, peer disconnect or I/O error).
func (sf *Server) serveConn(conn io.ReadWriteCloser) {
	sf.port = conn
	sf.setConnectStatus(statusConnected)
	atomic.StoreUint32(&sf.linkStatus, linkStateReset)
	sf.fcbExpected = true
	sf.lastResp = nil
	sf.primOut = nil
	sf.primActive = false
	sf.primRetry = 0
	sf.primNextInit = time.Now()

	// Drain channel leftovers from a previous connection
drain:
	for {
		select {
		case <-sf.rcvFrame:
		case <-sf.sendFrame:
		case <-sf.handlerPayload:
		default:
			break drain
		}
	}

	// Create context for this connection
	sf.connCtx, sf.connCancel = context.WithCancel(sf.ctx)

	// Start communication and handler loops
	sf.wg.Add(3)
	go sf.frameRecvLoop()
	go sf.frameSendLoop()
	go sf.handlerLoop()

	// Wait for context cancellation (from Close() or internal error)
	<-sf.connCtx.Done()

	// Cleanup
	sf.Debug("Connection context done, cleaning up connection.")
	sf.setConnectStatus(statusDisconnected)
	_ = sf.port.Close()
	sf.port = nil
	sf.wg.Wait() // Wait for loops to finish
}

// frameRecvLoop reads from the serial port and parses CS101 frames.
func (sf *Server) frameRecvLoop() {
	sf.Debug("frameRecvLoop started")
	defer func() {
		sf.connCancel() // Signal connection closure
		sf.wg.Done()
		sf.Debug("frameRecvLoop stopped")
	}()

	for {
		select {
		case <-sf.connCtx.Done():
			return
		default:
			frame, err := ParseFrame(sf.port, sf.config.LinkAddrSize, &sf.connCtx)
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
					sf.Debug("Serial port closed or EOF.")
					return
				}
				if errors.Is(err, ErrChecksumMismatch) || errors.Is(err, ErrInvalidStartChar) ||
					errors.Is(err, ErrLengthMismatch) || errors.Is(err, ErrInvalidEndChar) ||
					errors.Is(err, ErrFrameTooShort) || errors.Is(err, ErrFrameLenExceeded) {
					sf.Warn("Frame parsing error: %v. Attempting recovery.", err)
					continue
				}
				sf.Error("Error reading/parsing frame: %v", err)
				return // Fatal error for this connection
			}

			// Send valid frame to the main processing loop (handlerLoop via rcvFrame)
			select {
			case sf.rcvFrame <- frame:
			case <-sf.connCtx.Done():
				return
			}
		}
	}
}

// frameSendLoop takes frames from the send channel and writes them to the serial port.
func (sf *Server) frameSendLoop() {
	sf.Debug("frameSendLoop started")
	defer func() {
		sf.connCancel() // Signal connection closure
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

			rawData, err := frame.MarshalBinary(sf.config.LinkAddrSize)
			if err != nil {
				sf.Error("Failed to marshal frame: %v", err)
				continue
			}

			sf.Debug("TX Raw Frame [% X]", rawData)
			_, err = sf.port.Write(rawData)
			if err != nil {
				sf.Error("Failed to write frame to serial port: %v", err)
				return // Assume connection lost
			}
		}
	}
}

// handlerLoop processes incoming frames and decoded ASDUs, and supervises
// the balanced-mode primary role.
func (sf *Server) handlerLoop() {
	sf.Debug("handlerLoop started")
	defer func() {
		sf.wg.Done()
		sf.Debug("handlerLoop stopped")
	}()

	ticker := time.NewTicker(sf.config.TimeoutSendLinkMsg)
	defer ticker.Stop()

	for {
		select {
		case <-sf.connCtx.Done():
			return
		case frame := <-sf.rcvFrame: // Process frames directly here
			sf.Debug("Processing Frame: Start=0x%02X, Ctrl=%s", frame.Start, frame.GetControlField())
			if err := sf.handleIncomingFrame(frame); err != nil {
				sf.Error("Error handling incoming frame: %v", err)
			}
		case payload := <-sf.handlerPayload: // Process payload from handleIncomingFrame
			sf.Debug("Processing ASDU: %s", payload.Identifier)
			if err := sf.callHandler(payload); err != nil {
				sf.Warn("Error in ASDU handler: %v (ASDU: %s)", err, payload.Identifier)
			}
		case <-ticker.C:
			if sf.isBalanced() {
				sf.primTick()
			}
		}
	}
}

// handleIncomingFrame processes a parsed frame, updates state, calls handlers, and queues responses.
func (sf *Server) handleIncomingFrame(frame *Frame) error {
	// Single character ACK carries no address or control field. It only
	// makes sense as a confirmation of our primary role (balanced mode).
	if frame.Start == SingleCharAck {
		if sf.isBalanced() {
			sf.primConfirm(true)
		} else {
			sf.Warn("Received single char ACK in unbalanced secondary mode; ignored.")
		}
		return nil
	}

	// Validate Link Address if configured and non-zero size
	if sf.config.LinkAddrSize > 0 {
		receivedAddr := frame.GetLinkAddress()
		expectedAddr := sf.config.LinkAddress
		// Check against configured address and broadcast address (if applicable)
		isBroadcast := (sf.config.LinkAddrSize == 1 && receivedAddr == 0xFF) ||
			(sf.config.LinkAddrSize == 2 && receivedAddr == 0xFFFF)

		if receivedAddr != expectedAddr && !isBroadcast {
			// Silently ignore frames not addressed to this station
			return nil
		}
	}

	ctrl := frame.GetControlField()

	if !ctrl.PRM {
		// A frame from a secondary station: only meaningful in balanced mode,
		// as a response to our own primary role.
		if sf.isBalanced() {
			sf.handlePeerSecondaryFrame(ctrl)
			return nil
		}
		sf.Warn("Ignoring frame with PRM=0 (from secondary station?)")
		return nil
	}

	// Duplicate detection: the FCB alternates for every frame received with
	// FCV=1 (regardless of function code). On mismatch the frame is a
	// repetition — retransmit the previous response without processing.
	// See IEC 60870-5-2, subclass 5.1.2.
	if ctrl.FCV {
		if ctrl.FCB != sf.fcbExpected {
			sf.Warn("Received FCV frame with unexpected FCB (Expected=%v): repeated frame, re-sending previous response.", sf.fcbExpected)
			if sf.lastResp != nil {
				return sf.sendFrameToChan(sf.lastResp)
			}
			return sf.sendLinkAck()
		}
		sf.fcbExpected = !sf.fcbExpected
	}

	// Handle based on primary function codes
	switch ctrl.Fun {
	case PrimFcResetLink:
		sf.Debug("Received Reset Link command")
		atomic.StoreUint32(&sf.linkStatus, linkStateReset)
		sf.fcbExpected = true // first FCV frame after reset carries FCB=1
		sf.lastResp = nil
		if err := sf.sendLinkAck(); err != nil {
			return err
		}
		atomic.StoreUint32(&sf.linkStatus, linkStateActive) // link active after ACK

	case PrimFcResetUser:
		sf.Debug("Received Reset User Process command")
		return sf.sendLinkAck()

	case PrimFcTestLink:
		sf.Debug("Received Test Link command")
		return sf.sendLinkAck()

	case PrimFcUserDataConf: // Confirmed User Data from Primary
		sf.Debug("Received Confirmed User Data from Primary (FCB=%v)", ctrl.FCB)
		if !ctrl.FCV {
			sf.Warn("Received Confirmed User Data frame with FCV=0. Ignoring.")
			return nil
		}
		sf.dispatchASDU(frame)
		return sf.sendLinkAck()

	case PrimFcUserDataNoConf: // Unconfirmed User Data from Primary
		sf.Debug("Received Unconfirmed User Data from Primary")
		sf.dispatchASDU(frame)
		// No link layer response for unconfirmed data

	case PrimFcReqAccess:
		sf.Debug("Received Request Access Demand")
		// Respond with status of link; ACD reflects pending class 1 data.
		return sf.sendLinkStatus()

	case PrimFcReqStatus:
		sf.Debug("Received Request Status of Link")
		return sf.sendLinkStatus()

	case PrimFcReqData1:
		sf.Debug("Received Request User Data Class 1")
		return sf.respondClassData(1)

	case PrimFcReqData2:
		sf.Debug("Received Request User Data Class 2")
		return sf.respondClassData(2)

	default:
		sf.Warn("Received unhandled frame function code from primary: %s", ctrl)
		// Respond: link service not implemented
		return sf.sendResp(NewFixedFrame(sf.secControl(SecFcRespLinkNI, false), sf.buildLinkAddress()))
	}
	return nil
}

// dispatchASDU parses the ASDU identifier of a user data frame and hands the
// raw bytes to the handler loop.
func (sf *Server) dispatchASDU(frame *Frame) {
	tempASDU := asdu.NewEmptyASDU(&sf.params)
	if err := tempASDU.UnmarshalBinary(frame.ASDU); err != nil {
		sf.Warn("Failed to decode ASDU from user data frame: %v", err)
		return
	}
	payload := handlerASDUPayload{
		Identifier: tempASDU.Identifier,
		RawData:    frame.ASDU, // Send the original raw ASDU bytes
	}
	select {
	case sf.handlerPayload <- payload:
	case <-sf.connCtx.Done():
	}
}

// --- Balanced mode: primary role ---

// primControl builds a primary control field byte for our primary role.
// The server acts as station B, so the DIR bit stays 0.
func (sf *Server) primControl(fun byte, fcv, fcb bool) byte {
	control := CtrlPRM | (fun & CtrlFuncMask)
	if fcv {
		control |= CtrlFCV
		if fcb {
			control |= CtrlFCB
		}
	}
	return control
}

// primSendConfirmed transmits a confirmed frame for our primary role and
// starts the response deadline.
func (sf *Server) primSendConfirmed(frame *Frame) {
	if err := sf.sendFrameToChan(frame); err != nil {
		sf.Error("Primary role failed to send frame: %v", err)
		return
	}
	sf.primOut = frame
	sf.primOutCtrl = frame.GetControlField()
	sf.primRetry = 0
	sf.primDeadline = time.Now().Add(sf.config.TimeoutResponseT1)
}

// primConfirm completes the outstanding primary-role transaction.
func (sf *Server) primConfirm(positive bool) {
	p := sf.primOut
	if p == nil {
		sf.Warn("Received confirm but no primary frame outstanding.")
		return
	}
	if positive {
		if sf.primOutCtrl.FCV {
			sf.primFCB = !sf.primOutCtrl.FCB // toggle only after positive confirm
		}
		if sf.primOutCtrl.Fun == PrimFcResetLink {
			sf.Debug("Primary role: Reset Link confirmed, transmit direction active.")
			sf.primActive = true
			sf.primFCB = true // first FCV frame after reset carries FCB=1
		}
	}
	sf.primOut = nil
	sf.primRetry = 0
}

// handlePeerSecondaryFrame services responses (PRM=0) from the peer to our
// primary role in balanced mode.
func (sf *Server) handlePeerSecondaryFrame(ctrl ControlField) {
	switch ctrl.Fun {
	case SecFcConfACK:
		sf.primConfirm(true)
	case SecFcConfNACK:
		sf.Warn("Peer NACK (link busy); primary frame will be retried.")
		// keep outstanding; the deadline in primTick retries
	case SecFcRespStatus:
		sf.primConfirm(true)
	case SecFcRespLinkNF, SecFcRespLinkNI:
		sf.Warn("Peer link service not functioning/implemented (FC=%d).", ctrl.Fun)
		sf.primOut = nil
		sf.primRetry = 0
		sf.primActive = false
		sf.primNextInit = time.Now().Add(sf.config.TimeoutResponseT1)
	default:
		sf.Warn("Received unhandled secondary frame from peer: %s", ctrl)
	}
}

// primTick supervises the balanced-mode primary role: link initialization,
// response timeouts with one retry, and transmission of queued user data.
func (sf *Server) primTick() {
	now := time.Now()

	if sf.primOut != nil {
		if now.After(sf.primDeadline) {
			if sf.primRetry < 1 {
				sf.primRetry++
				sf.Warn("Primary role response timeout, retrying frame (Retry %d)", sf.primRetry)
				if err := sf.sendFrameToChan(sf.primOut); err != nil {
					sf.Error("Primary role failed to resend frame: %v", err)
				}
				sf.primDeadline = now.Add(sf.config.TimeoutRepeatT2)
			} else {
				sf.Error("Primary role response timeout after retry; transmit direction inactive.")
				sf.primOut = nil
				sf.primRetry = 0
				sf.primActive = false
				sf.primNextInit = now.Add(sf.config.TimeoutResponseT1)
			}
		}
		return
	}

	if !sf.primActive {
		if now.After(sf.primNextInit) {
			sf.Debug("Primary role: sending Reset Link to initialize transmit direction.")
			sf.primNextInit = now.Add(2 * sf.config.TimeoutResponseT1)
			sf.primSendConfirmed(NewFixedFrame(sf.primControl(PrimFcResetLink, false, false), sf.buildLinkAddress()))
		}
		return
	}

	// Transmit direction active and idle: send next queued ASDU.
	sf.dataMux.Lock()
	var next *asdu.ASDU
	if len(sf.primQueue) > 0 {
		next = sf.primQueue[0]
		sf.primQueue = sf.primQueue[1:]
	}
	sf.dataMux.Unlock()
	if next == nil {
		return
	}

	asduData, err := next.MarshalBinary()
	if err != nil {
		sf.Error("Failed to marshal ASDU: %v", err)
		return
	}
	sf.Debug("Primary role: sending user data (ASDU Type: %s, FCB: %v)", next.Identifier.Type, sf.primFCB)
	sf.primSendConfirmed(NewDataFrame(sf.primControl(PrimFcUserDataConf, true, sf.primFCB), sf.buildLinkAddress(), asduData))
}

// callHandler safely calls the appropriate user-defined handler function.
// It now receives the identifier and raw ASDU bytes.
func (sf *Server) callHandler(payload handlerASDUPayload) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic recovered in handler: %v", r)
			sf.Critical("%v", err)
		}
	}()

	// Calculate offset to information objects within the raw data
	ioOffset := sf.params.IdentifierSize()
	if ioOffset > len(payload.RawData) {
		return fmt.Errorf("invalid ASDU raw data: length %d less than identifier size %d", len(payload.RawData), ioOffset)
	}
	infoObjectBytes := payload.RawData[ioOffset:]

	// Reconstruct the ASDU (identifier + information objects) for the
	// handlers, so the Get* decoders and SendReplyMirror work on it.
	tempASDU := asdu.NewASDU(&sf.params, payload.Identifier)
	tempASDU.InfoObj = append(tempASDU.InfoObj, infoObjectBytes...)

	// Call generic handler first
	if handlerErr := sf.handler.ASDUHandlerAll(tempASDU, 0); handlerErr != nil {
		sf.Warn("Error in ASDUHandlerAll: %v", handlerErr)
	}

	// Call specific handlers based on TypeID
	switch payload.Identifier.Type {
	case asdu.C_IC_NA_1: // Interrogation Command
		// Expect IOA + QOI
		ioaSize := sf.params.InfoObjAddrSize
		if len(infoObjectBytes) < ioaSize+1 {
			return fmt.Errorf("not enough data for IOA+QOI in C_IC_NA_1")
		}
		qoi := asdu.QualifierOfInterrogation(infoObjectBytes[ioaSize])
		err = sf.handler.InterrogationHandler(tempASDU, qoi)
	case asdu.C_CI_NA_1: // Counter Interrogation Command
		// Expect IOA + QCC
		ioaSize := sf.params.InfoObjAddrSize
		if len(infoObjectBytes) < ioaSize+1 {
			return fmt.Errorf("not enough data for IOA+QCC in C_CI_NA_1")
		}
		qcc := asdu.ParseQualifierCountCall(infoObjectBytes[ioaSize])
		err = sf.handler.CounterInterrogationHandler(tempASDU, qcc)
	case asdu.C_RD_NA_1: // Read Command
		// Expect IOA only
		ioaSize := sf.params.InfoObjAddrSize
		if len(infoObjectBytes) < ioaSize {
			return fmt.Errorf("not enough data for IOA in C_RD_NA_1")
		}
		ioa := parseIOA(infoObjectBytes, &sf.params)
		err = sf.handler.ReadHandler(tempASDU, ioa)
	case asdu.C_CS_NA_1: // Clock Synchronization Command
		// Expect IOA + CP56Time2a (7 bytes)
		ioaSize := sf.params.InfoObjAddrSize
		if len(infoObjectBytes) < ioaSize+7 {
			return fmt.Errorf("not enough data for IOA+Time in C_CS_NA_1")
		}
		t := asdu.ParseCP56Time2a(infoObjectBytes[ioaSize:], sf.params.InfoObjTimeZone)
		err = sf.handler.ClockSyncHandler(tempASDU, t)
	case asdu.C_RP_NA_1: // Reset Process Command
		// Expect IOA + QRP
		ioaSize := sf.params.InfoObjAddrSize
		if len(infoObjectBytes) < ioaSize+1 {
			return fmt.Errorf("not enough data for IOA+QRP in C_RP_NA_1")
		}
		qrp := asdu.QualifierOfResetProcessCmd(infoObjectBytes[ioaSize])
		err = sf.handler.ResetProcessHandler(tempASDU, qrp)
	case asdu.C_CD_NA_1: // Delay Acquisition Command
		// Expect IOA + CP16Time2a (2 bytes)
		ioaSize := sf.params.InfoObjAddrSize
		if len(infoObjectBytes) < ioaSize+2 {
			return fmt.Errorf("not enough data for IOA+Delay in C_CD_NA_1")
		}
		delay := asdu.ParseCP16Time2a(infoObjectBytes[ioaSize:])
		err = sf.handler.DelayAcquisitionHandler(tempASDU, delay)
	default:
		// Default to the generic handler for other types
		err = sf.handler.ASDUHandler(tempASDU)
	}
	return err
}

// --- Link Layer Response Helpers ---

// buildLinkAddress encodes the configured link address based on size.
func (sf *Server) buildLinkAddress() []byte {
	size := sf.config.LinkAddrSize
	addr := sf.config.LinkAddress
	switch size {
	case 1:
		return []byte{byte(addr)}
	case 2:
		buf := make([]byte, 2)
		binary.LittleEndian.PutUint16(buf, uint16(addr))
		return buf
	default: // Size 0 or invalid
		return nil
	}
}

// parseIOA decodes the Information Object Address based on configured size.
// Note: This is a helper function needed for callHandler.
func parseIOA(data []byte, params *asdu.Params) asdu.InfoObjAddr {
	if len(data) < params.InfoObjAddrSize {
		return asdu.InfoObjAddrIrrelevant // Or handle error
	}
	switch params.InfoObjAddrSize {
	case 1:
		return asdu.InfoObjAddr(data[0])
	case 2:
		return asdu.InfoObjAddr(binary.LittleEndian.Uint16(data))
	case 3:
		val := uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16
		return asdu.InfoObjAddr(val)
	default:
		return asdu.InfoObjAddrIrrelevant
	}
}

// secControl builds a secondary control field byte, with the ACD bit
// reflecting pending class 1 data when requested.
func (sf *Server) secControl(fun byte, acd bool) byte {
	control := fun & CtrlFuncMask
	if acd {
		control |= CtrlACD
	}
	return control
}

// class1Pending reports whether class 1 data is buffered.
func (sf *Server) class1Pending() bool {
	sf.dataMux.Lock()
	pending := len(sf.class1) > 0
	sf.dataMux.Unlock()
	return pending
}

// popClassData removes and returns the next buffered ASDU for a class 1 or
// class 2 request, falling back to the other class when empty (permitted by
// IEC 60870-5-101, subclass 6.6).
func (sf *Server) popClassData(class int) *asdu.ASDU {
	sf.dataMux.Lock()
	defer sf.dataMux.Unlock()
	first, second := &sf.class1, &sf.class2
	if class == 2 {
		first, second = &sf.class2, &sf.class1
	}
	for _, q := range []*[]*asdu.ASDU{first, second} {
		if len(*q) > 0 {
			a := (*q)[0]
			*q = (*q)[1:]
			return a
		}
	}
	return nil
}

// sendFrameToChan queues a frame for sending.
func (sf *Server) sendFrameToChan(frame *Frame) error {
	if sf.connectStatus() != statusConnected {
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

// sendResp queues a response to a primary request and records it for
// retransmission in case the request is repeated (FCB mismatch).
func (sf *Server) sendResp(frame *Frame) error {
	sf.lastResp = frame
	return sf.sendFrameToChan(frame)
}

// sendLinkAck sends a fixed-length ACK frame (SecFcConfACK).
func (sf *Server) sendLinkAck() error {
	ackFrame := NewFixedFrame(sf.secControl(SecFcConfACK, sf.class1Pending()), sf.buildLinkAddress())
	sf.Debug("Queueing Link ACK frame")
	return sf.sendResp(ackFrame)
}

// sendLinkNack sends a fixed-length NACK frame (SecFcConfNACK - Link Busy/Error).
func (sf *Server) sendLinkNack() error {
	nackFrame := NewFixedFrame(sf.secControl(SecFcConfNACK, false), sf.buildLinkAddress())
	sf.Debug("Queueing Link NACK frame")
	return sf.sendResp(nackFrame)
}

// sendLinkStatus sends a fixed-length Link Status frame (SecFcRespStatus).
func (sf *Server) sendLinkStatus() error {
	statusFrame := NewFixedFrame(sf.secControl(SecFcRespStatus, sf.class1Pending()), sf.buildLinkAddress())
	sf.Debug("Queueing Link Status frame")
	return sf.sendResp(statusFrame)
}

// respondClassData answers a Request User Data Class 1/2 poll: responds with
// buffered user data (RESP: user data, FC 8) or, when nothing is buffered,
// with "requested data not available" (FC 9). The ACD bit signals pending
// class 1 data in either case.
func (sf *Server) respondClassData(class int) error {
	a := sf.popClassData(class)
	if a == nil {
		sf.Debug("No class %d data available, responding NACK (FC 9)", class)
		return sf.sendResp(NewFixedFrame(sf.secControl(SecFcUserDataNoRep, false), sf.buildLinkAddress()))
	}

	asduData, err := a.MarshalBinary()
	if err != nil {
		sf.Error("Failed to marshal buffered ASDU: %v", err)
		return sf.sendResp(NewFixedFrame(sf.secControl(SecFcUserDataNoRep, sf.class1Pending()), sf.buildLinkAddress()))
	}
	sf.Debug("Responding class %d poll with user data (ASDU Type: %s)", class, a.Identifier.Type)
	frame := NewDataFrame(sf.secControl(SecFcUserDataConf, sf.class1Pending()), sf.buildLinkAddress(), asduData)
	return sf.sendResp(frame)
}

// --- asdu.Connect Interface Implementation ---

// Send queues an ASDU for delivery to the primary station.
//
// In unbalanced mode the data is placed in the class 1 or class 2 output
// buffer (periodic/background causes go to class 2, everything else to
// class 1) and handed out when the primary polls; pending class 1 data is
// signalled with the ACD bit.
//
// In balanced mode the ASDU is transmitted spontaneously as confirmed user
// data by this station's primary role.
func (sf *Server) Send(a *asdu.ASDU) error {
	if sf.connectStatus() != statusConnected {
		return ErrUseClosedConnection
	}

	sf.dataMux.Lock()
	defer sf.dataMux.Unlock()

	if sf.isBalanced() {
		if len(sf.primQueue) >= sf.config.MaxSendQueueSize {
			return ErrSendQueueFull
		}
		sf.primQueue = append(sf.primQueue, a)
		sf.Debug("ASDU %s enqueued for spontaneous transmission. Queue size: %d", a.Identifier, len(sf.primQueue))
		return nil
	}

	queue := &sf.class1
	if a.Coa.Cause == asdu.Periodic || a.Coa.Cause == asdu.Background {
		queue = &sf.class2
	}
	if len(*queue) >= sf.config.MaxSendQueueSize {
		return ErrSendQueueFull
	}
	*queue = append(*queue, a)
	sf.Debug("ASDU %s buffered for polling. Class1=%d Class2=%d", a.Identifier, len(sf.class1), len(sf.class2))
	return nil
}

// Params returns the ASDU parameters used by the server.
func (sf *Server) Params() *asdu.Params {
	return &sf.params
}

// UnderlyingConn is required by the asdu.Connect interface. A serial port is
// not a net.Conn, so this returns nil; use Port to access the serial port.
func (sf *Server) UnderlyingConn() net.Conn {
	return nil
}

// Port returns the underlying serial port (nil when disconnected).
func (sf *Server) Port() io.ReadWriteCloser {
	return sf.port
}

// Close stops the server and closes the serial port.
func (sf *Server) Close() error {
	sf.rwMux.Lock()
	if sf.cancel == nil { // Already closed or never started
		sf.rwMux.Unlock()
		return errors.New("server not running")
	}
	sf.Debug("Close requested.")
	sf.cancel()     // Signal run loop to stop
	sf.cancel = nil // Prevent double close
	sf.rwMux.Unlock()
	// The run loop handles closing the port and waiting for goroutines
	return nil
}

// setConnectStatus updates the connection status atomically.
func (sf *Server) setConnectStatus(status uint32) {
	atomic.StoreUint32(&sf.connStatus, status)
}

// connectStatus reads the connection status atomically.
func (sf *Server) connectStatus() uint32 {
	return atomic.LoadUint32(&sf.connStatus)
}

// IsConnected returns true when the serial port is open and running.
func (sf *Server) IsConnected() bool {
	return sf.connectStatus() == statusConnected
}

// IsLinkActive returns true when the link layer has been reset/activated by
// the primary station (unbalanced) or by our own primary role (balanced).
func (sf *Server) IsLinkActive() bool {
	if sf.isBalanced() {
		return sf.IsConnected()
	}
	return sf.IsConnected() && atomic.LoadUint32(&sf.linkStatus) == linkStateActive
}

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
	"sync"
	"sync/atomic"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/clog"
	"github.com/tarm/serial"
)

// Server represents an IEC101 Secondary Station (Slave)
type Server struct {
	config  Config
	params  asdu.Params
	handler ServerHandlerInterface
	port    io.ReadWriteCloser // Serial port connection

	// Channels for internal communication
	rcvFrame       chan *Frame             // Parsed incoming frames
	sendFrame      chan *Frame             // Frames to be sent
	handlerPayload chan handlerASDUPayload // Identifier + Raw ASDU bytes for handler

	// CS101 Link Layer State
	linkStatus  uint32 // linkStateReset, linkStateActive
	fcbExpected bool   // Expected FCB state from Primary for next confirmed message
	// TODO: Add state for DFC (Data Flow Control), ACD (Access Demand) if needed for secondary role

	// Connection Status & Control
	connStatus uint32 // statusInitial, statusConnecting, etc.
	rwMux      sync.RWMutex
	clog.Clog
	wg         sync.WaitGroup
	ctx        context.Context    // Controls the main run loop
	cancel     context.CancelFunc // Cancels the main run loop
	connCtx    context.Context    // Controls connection-specific loops
	connCancel context.CancelFunc // Cancels connection-specific loops

	// Callbacks (optional, might be less relevant for server)
	// onPortOpen func(s *Server)
	// onPortClose func(s *Server, err error)
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
		config:         DefaultConfig(),
		params:         *asdu.ParamsStandard101, // Use CS101 standard params
		handler:        handler,
		rcvFrame:       make(chan *Frame, 20),
		sendFrame:      make(chan *Frame, 20),
		handlerPayload: make(chan handlerASDUPayload, 50),  // Use new channel type
		Clog:           clog.NewLogger("cs101 server => "), // Placeholder address initially
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
		sf.Clog = clog.NewLogger(fmt.Sprintf("cs101 server [%s] => ", cfg.Serial.Address))
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

// Start opens the serial port and begins processing frames.
func (sf *Server) Start() error {
	sf.rwMux.Lock()
	if sf.connStatus != statusInitial {
		sf.rwMux.Unlock()
		return errors.New("server already started or starting")
	}
	if sf.config.Serial.Address == "" {
		sf.rwMux.Unlock()
		return errors.New("serial port address not configured")
	}
	sf.connStatus = statusConnecting
	sf.ctx, sf.cancel = context.WithCancel(context.Background())
	sf.rwMux.Unlock()

	go sf.run()
	return nil
}

// run manages the serial port connection and protocol loops.
func (sf *Server) run() {
	sf.Debug("Server run loop started")
	defer func() {
		sf.setConnectStatus(statusInitial)
		sf.Debug("Server run loop stopped")
	}()

	// --- Open Serial Port ---
	sf.Debug("Opening serial port %s...", sf.config.Serial.Address)
	serialCfg := &serial.Config{
		Name:        sf.config.Serial.Address,
		Baud:        sf.config.Serial.BaudRate,
		ReadTimeout: sf.config.Serial.Timeout,
		Size:        byte(sf.config.Serial.DataBits), // DataBits (Size expects byte)
		Parity:      sf.config.Serial.Parity,         // Parity
		StopBits:    sf.config.Serial.StopBits,       // StopBits
	}
	port, err := serial.OpenPort(serialCfg)
	if err != nil {
		sf.Error("Failed to open serial port %s: %v", sf.config.Serial.Address, err)
		sf.setConnectStatus(statusDisconnected)
		// Consider adding an error callback here if needed
		return // Cannot proceed without the port
	}
	sf.Debug("Serial port %s opened successfully", sf.config.Serial.Address)
	sf.port = port
	sf.setConnectStatus(statusConnected)
	sf.linkStatus = linkStateReset // Start in reset state

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
	sf.Debug("Connection context done, cleaning up server.")
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
			frame, err := ParseFrame(sf.port, sf.config.LinkAddrSize)
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || (err.Error() == "serial: port closed") {
					sf.Debug("Serial port closed or EOF.")
					return
				}
				if errors.Is(err, ErrChecksumMismatch) || errors.Is(err, ErrInvalidStartChar) || errors.Is(err, ErrLengthMismatch) {
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

// handlerLoop processes incoming frames and decoded ASDUs.
func (sf *Server) handlerLoop() {
	sf.Debug("handlerLoop started")
	defer func() {
		sf.wg.Done()
		sf.Debug("handlerLoop stopped")
	}()

	for {
		select {
		case <-sf.connCtx.Done():
			return
		case frame := <-sf.rcvFrame: // Process frames directly here
			sf.Debug("Processing Frame: Start=0x%02X, Ctrl=%s", frame.Start, frame.GetControlField())
			err := sf.handleIncomingFrame(frame)
			if err != nil {
				sf.Error("Error handling incoming frame: %v", err)
				// Decide if error is fatal and trigger connCancel if needed
			}
		case payload := <-sf.handlerPayload: // Process payload from handleIncomingFrame
			sf.Debug("Processing ASDU: %s", payload.Identifier)
			if err := sf.callHandler(payload); err != nil { // Pass payload
				sf.Warn("Error in ASDU handler: %v (ASDU: %s)", err, payload.Identifier)
			}
		}
	}
}

// handleIncomingFrame processes a parsed frame, updates state, calls handlers, and queues responses.
func (sf *Server) handleIncomingFrame(frame *Frame) error {
	// Validate Link Address if configured and non-zero size
	if sf.config.LinkAddrSize > 0 {
		receivedAddr := frame.GetLinkAddress()
		expectedAddr := sf.config.LinkAddress
		// Check against configured address and broadcast address (if applicable)
		// Assuming 0xFFFF is broadcast for 2-byte address, 0xFF for 1-byte
		isBroadcast := (sf.config.LinkAddrSize == 1 && receivedAddr == 0xFF) ||
			(sf.config.LinkAddrSize == 2 && receivedAddr == 0xFFFF)

		if receivedAddr != expectedAddr && !isBroadcast {
			// Silently ignore frames not addressed to this station
			// sf.Warn("Ignoring frame with unexpected link address %d (expected %d)", receivedAddr, expectedAddr)
			return nil
		}
	}

	ctrl := frame.GetControlField()

	if !ctrl.PRM { // Ignore frames not from the primary station
		sf.Warn("Ignoring frame with PRM=0 (from secondary station?)")
		return nil
	}

	// Handle based on primary function codes
	switch ctrl.Fun {
	case PrimFcResetLink:
		sf.Debug("Received Reset Link command")
		atomic.StoreUint32(&sf.linkStatus, linkStateReset)
		// Respond with ACK
		sf.sendLinkAck()
		atomic.StoreUint32(&sf.linkStatus, linkStateActive) // Assume link active after ACK
		sf.fcbExpected = false                              // Reset expected FCB on link reset
	case PrimFcResetUser:
		sf.Debug("Received Reset User Process command")
		// TODO: Implement user process reset logic if needed (Application specific)
		sf.sendLinkAck() // Acknowledge the command
	case PrimFcTestLink:
		sf.Debug("Received Test Link command")
		// Respond with ACK (or NACK if busy/error)
		sf.sendLinkAck()
	case PrimFcUserDataConf: // Confirmed User Data from Primary
		sf.Debug("Received Confirmed User Data from Primary (FCB=%v, Expected=%v)", ctrl.FCB, sf.fcbExpected)
		// Check FCB state for duplicate detection
		if ctrl.FCV && ctrl.FCB == sf.fcbExpected {
			// FCB matches expected state, process the data
			sf.fcbExpected = !sf.fcbExpected // Toggle expected state for the *next* message

			// Parse ASDU just to get identifier, pass raw data to handler
			tempASDU := asdu.NewEmptyASDU(&sf.params)
			err := tempASDU.UnmarshalBinary(frame.ASDU)
			if err == nil {
				payload := handlerASDUPayload{
					Identifier: tempASDU.Identifier,
					RawData:    frame.ASDU, // Send the original raw ASDU bytes
				}
				select {
				case sf.handlerPayload <- payload:
				case <-sf.connCtx.Done():
					return sf.connCtx.Err() // Check context
				}
			} else {
				sf.Warn("Failed to decode ASDU identifier from confirmed user data frame: %v", err)
			}
			// Respond with ACK
			sf.sendLinkAck()
		} else if ctrl.FCV && ctrl.FCB != sf.fcbExpected {
			// FCB does not match - likely a repeated frame due to lost ACK
			sf.Warn("Received Confirmed User Data with unexpected FCB (Expected=%v). Re-sending ACK.", sf.fcbExpected)
			// Re-send ACK, but do not process data or toggle fcbExpected
			sf.sendLinkAck()
		} else {
			// FCV is 0 - protocol error or unexpected frame type?
			sf.Warn("Received Confirmed User Data frame with FCV=0. Ignoring.")
			// Optionally send NACK? Standard might specify behavior here.
			// sf.sendLinkNack()
		}
		// Original processing logic moved inside the FCB check
		/*
			tempASDU := asdu.NewEmptyASDU(&sf.params)
		*/
	case PrimFcUserDataNoConf: // Unconfirmed User Data from Primary
		sf.Debug("Received Unconfirmed User Data from Primary")
		// Parse ASDU just to get identifier, pass raw data to handler
		tempASDU := asdu.NewEmptyASDU(&sf.params)
		err := tempASDU.UnmarshalBinary(frame.ASDU)
		if err == nil {
			payload := handlerASDUPayload{
				Identifier: tempASDU.Identifier,
				RawData:    frame.ASDU, // Send the original raw ASDU bytes
			}
			select {
			case sf.handlerPayload <- payload:
			case <-sf.connCtx.Done():
				return sf.connCtx.Err() // Check context
			}
		} else {
			sf.Warn("Failed to decode ASDU identifier from unconfirmed user data frame: %v", err)
		}
		// No link layer response for unconfirmed data
	case PrimFcReqAccess:
		sf.Debug("Received Request Access Demand")
		// Respond with NACK (Function Not Implemented or Busy) - Access Demand typically initiated by secondary
		sf.sendLinkNack()
	case PrimFcReqStatus:
		sf.Debug("Received Request Status of Link")
		// Respond with Link Status (Function Code 11)
		sf.sendLinkStatus()
	case PrimFcReqData1:
		sf.Debug("Received Request User Data Class 1")
		// TODO: Check DFC state. If DFC=0, respond with data (SecFcUserDataConf/SecFcRespStatus) or NACK(SecFcRespNACK).
		// TODO: If DFC=1, respond ACK(SecFcConfACK)/NACK(SecFcConfNACK).
		// Requires state management (knowing if class 1 data is available & DFC state).
		// For now, respond NACK (Data Not Available)
		sf.sendDataNack()
	case PrimFcReqData2:
		sf.Debug("Received Request User Data Class 2")
		// TODO: Check DFC state. If DFC=0, respond with data (SecFcUserDataConf/SecFcRespStatus) or NACK(SecFcRespNACK).
		// TODO: If DFC=1, respond ACK(SecFcConfACK)/NACK(SecFcConfNACK).
		// Requires state management (knowing if class 2 data is available & DFC state).
		// For now, respond NACK (Data Not Available)
		sf.sendDataNack()
	default:
		sf.Warn("Received unhandled frame function code from primary: %s", ctrl)
		// Respond NACK (Function Not Implemented)?
		sf.sendLinkNack()
	}
	return nil
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

	// Create a temporary ASDU struct for passing to handlers that expect it
	// Note: This ASDU will only have the Identifier populated correctly.
	// Handlers needing detailed info object data must parse the raw bytes.
	tempASDU := asdu.NewASDU(&sf.params, payload.Identifier)

	// Call generic handler first
	if handlerErr := sf.handler.ASDUHandlerAll(tempASDU, 0); handlerErr != nil {
		sf.Warn("Error in ASDUHandlerAll: %v", handlerErr)
	}

	// Calculate offset to information objects within the raw data
	ioOffset := sf.params.IdentifierSize()
	if ioOffset > len(payload.RawData) {
		return fmt.Errorf("invalid ASDU raw data: length %d less than identifier size %d", len(payload.RawData), ioOffset)
	}
	infoObjectBytes := payload.RawData[ioOffset:]

	// Call specific handlers based on TypeID
	switch payload.Identifier.Type {
	case asdu.C_IC_NA_1: // Interrogation Command
		// Expect IOA + QOI
		ioaSize := sf.params.InfoObjAddrSize
		if len(infoObjectBytes) < ioaSize+1 {
			return fmt.Errorf("not enough data for IOA+QOI in C_IC_NA_1")
		}
		// ioa := parseIOA(infoObjectBytes, &sf.params) // Assuming parseIOA helper exists or is added
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
		ioa := parseIOA(infoObjectBytes, &sf.params) // Need parseIOA helper
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
	// Add cases for other command types (Setpoints C_SE_*, Commands C_SC_*, C_DC_*, etc.)
	// case asdu.C_SC_NA_1: ... sf.handler.CommandHandler(...)
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

// sendFrameToChan queues a frame for sending.
func (sf *Server) sendFrameToChan(frame *Frame) error {
	if sf.connStatus != statusConnected {
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

// sendLinkAck sends a fixed-length ACK frame (SecFcConfACK).
func (sf *Server) sendLinkAck() error {
	control := byte(SecFcConfACK) // PRM=0, Func=0
	linkAddr := sf.buildLinkAddress()
	ackFrame := NewFixedFrame(control, linkAddr)
	sf.Debug("Queueing Link ACK frame")
	return sf.sendFrameToChan(ackFrame)
}

// sendLinkNack sends a fixed-length NACK frame (SecFcConfNACK - Link Busy/Error).
func (sf *Server) sendLinkNack() error {
	control := byte(SecFcConfNACK) // PRM=0, Func=1
	linkAddr := sf.buildLinkAddress()
	nackFrame := NewFixedFrame(control, linkAddr)
	sf.Debug("Queueing Link NACK frame")
	return sf.sendFrameToChan(nackFrame)
}

// sendDataNack sends a fixed-length NACK frame (SecFcRespNACK - Data Not Available).
func (sf *Server) sendDataNack() error {
	control := byte(SecFcRespLinkNF) // PRM=0, Func=14 (implies DFC=1)
	linkAddr := sf.buildLinkAddress()
	nackFrame := NewFixedFrame(control, linkAddr)
	sf.Debug("Queueing Data NACK frame")
	return sf.sendFrameToChan(nackFrame)
}

// sendLinkStatus sends a fixed-length Link Status frame (SecFcRespStatus).
func (sf *Server) sendLinkStatus() error {
	// TODO: Set DFC bit based on actual buffer status if implementing flow control.
	// Requires monitoring send buffer state. Assume DFC=0 (buffer available) for now.
	control := byte(SecFcRespStatus) // PRM=0, Func=11, DFC=0 implicitly
	linkAddr := sf.buildLinkAddress()
	statusFrame := NewFixedFrame(control, linkAddr)
	sf.Debug("Queueing Link Status frame")
	return sf.sendFrameToChan(statusFrame)
}

// --- asdu.Connect Interface Implementation ---

// Send queues an ASDU to be sent to the primary station.
func (sf *Server) Send(a *asdu.ASDU) error {
	if sf.connStatus != statusConnected || atomic.LoadUint32(&sf.linkStatus) != linkStateActive {
		return ErrNotActive
	}

	asduData, err := a.MarshalBinary()
	if err != nil {
		return fmt.Errorf("failed to marshal ASDU: %w", err)
	}

	// Determine Control Field for User Data (Confirmed response from Secondary)
	// PRM=0, Func=8 (SecFcUserDataConf, implies ACD=1)
	// TODO: Need to manage DFC bit based on primary station's requests/flow control state.
	// Assume DFC=0 (buffer available) for now. Full DFC implementation requires tracking primary requests.
	control := byte(SecFcUserDataConf) // ACD=1, DFC=0 implicitly

	linkAddr := sf.buildLinkAddress()
	frame := NewDataFrame(control, linkAddr, asduData)

	sf.Debug("Queueing User Data Response (ASDU Type: %s)", a.Identifier.Type)
	return sf.sendFrameToChan(frame)
}

// Params returns the ASDU parameters used by the server.
func (sf *Server) Params() *asdu.Params {
	return &sf.params
}

// UnderlyingConn returns the serial port connection.
func (sf *Server) UnderlyingConn() io.ReadWriteCloser {
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

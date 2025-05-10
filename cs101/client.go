// Copyright 2025 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs101

import (
	"context"
	"encoding/binary" // Re-add missing import
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/clog"

	// Import a serial library, e.g., tarm/serial (already in go.mod)
	"github.com/tarm/serial" // Using tarm/serial as an example
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

	sendQueue      []*asdu.ASDU // Queue for outgoing ASDUs when link is busy
	sendQueueMutex sync.Mutex   // Mutex to protect sendQueue

	// CS101 Link Layer State
	fcbState          bool   // Expected Frame Count Bit state for next confirmed send
	lastSentConfFrame *Frame // Last sent frame requiring confirmation (ACK/NACK)
	lastSentConfTime  time.Time
	lastRecvTime      time.Time
	retryCount        int
	t1Timer           *time.Timer // Response timeout timer (T1/T2)
	t3Timer           *time.Timer // Idle timeout timer (T3)
	timerTrySend      *time.Timer // Timer to attempt resending frames

	// Connection Status
	connStatus uint32 // Uses statusInitial, statusConnecting, etc.
	linkStatus uint32 // Uses linkStateReset, linkStateActive
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

	// Basic check for serial config presence
	if opt.config.Serial.Address == "" {
		tempLogger.Error("Serial port address (e.g., COM3 or /dev/ttyS0) must be set in ClientOption.Serial")
		// Return nil or handle error appropriately? Returning nil for now.
		return nil
	}

	client := &Client{
		option:           opt,
		handler:          handler,
		rcvFrame:         make(chan *Frame, 20), // Adjust buffer sizes as needed
		sendFrame:        make(chan *Frame, 20),
		rcvASDU:          make(chan *asdu.ASDU, 50),
		sendQueue:        make([]*asdu.ASDU, 0, opt.config.MaxSendQueueSize), // Initialize with capacity from config
		Clog:             clog.NewLogger(fmt.Sprintf("cs101 client [%s] => ", opt.config.Serial.Address)),
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
	sf.lastSentConfFrame = nil
	sf.connStatus = statusConnecting // Mark as starting
	sf.ctx, sf.cancel = context.WithCancel(context.Background())
	sf.rwMux.Unlock()

	go sf.connectionManager()
	return nil
}

// connectionManager handles the connection lifecycle and reconnection.
func (sf *Client) connectionManager() {
	sf.Debug("Connection manager started")
	defer func() {
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
		sf.Debug("Connecting to serial port %s...", sf.option.config.Serial.Address) // Use Debug

		// --- Attempt to open serial port ---
		// Example using tarm/serial
		serialCfg := &serial.Config{
			Name:        sf.option.config.Serial.Address,
			Baud:        sf.option.config.Serial.BaudRate,
			ReadTimeout: sf.option.config.Serial.Timeout,
			Size:        byte(sf.option.config.Serial.DataBits), // DataBits (Size expects byte)
			Parity:      sf.option.config.Serial.Parity,         // Parity
			StopBits:    sf.option.config.Serial.StopBits,       // StopBits
		}
		port, err := serial.OpenPort(serialCfg)
		// --- End serial port opening ---

		if err != nil {
			sf.Error("Failed to open serial port %s: %v", sf.option.config.Serial.Address, err)
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

		sf.Debug("Serial port %s connected successfully", sf.option.config.Serial.Address) // Use Debug
		sf.port = port
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
			sf.Debug("Protocol run ended gracefully.") // Use Debug
			sf.onConnectionLost(sf, nil)               // Indicate graceful stop if no error
		}

		// Check if Close() was called or if we should reconnect
		select {
		case <-sf.ctx.Done():
			return // Exit if Close() was called
		default:
			if !sf.option.autoReconnect {
				sf.Debug("Auto-reconnect disabled, stopping.") // Use Debug
				return
			}
			sf.Debug("Waiting %.1fs before attempting reconnection...", sf.option.reconnectInterval.Seconds()) // Use Debug
			select {
			case <-time.After(sf.option.reconnectInterval):
				// Continue loop to reconnect
			case <-sf.ctx.Done():
				return // Exit if Close() was called during wait
			}
		}
	}
}

// runProtocol manages the communication loops and state machine for an active connection.
// Returns an error if the connection fails or the protocol encounters a fatal error.
func (sf *Client) runProtocol() error {
	sf.Debug("runProtocol started")
	sf.linkStatus = linkStateReset // Start in reset state
	sf.fcbState = false            // Initial FCB state

	// Clear any pending send queue from a previous connection
	sf.sendQueueMutex.Lock()
	if len(sf.sendQueue) > 0 {
		sf.Warn("Clearing %d unsent ASDUs from send queue due to new connection/protocol start.", len(sf.sendQueue))
		sf.sendQueue = make([]*asdu.ASDU, 0, sf.option.config.MaxSendQueueSize)
	}
	sf.sendQueueMutex.Unlock()

	sf.onConnectCalled = false // Reset flag for this connection attempt
	// Start communication loops
	sf.wg.Add(3)
	go sf.frameRecvLoop()
	go sf.frameSendLoop()
	go sf.handlerLoop()

	// --- CS101 State Machine ---
	sf.lastRecvTime = time.Now() // Initialize last receive time

	// Initialize Timers
	sf.t1Timer = time.NewTimer(sf.option.config.TimeoutResponseT1)
	if !sf.t1Timer.Stop() { // Stop initially, start when needed
		<-sf.t1Timer.C
	}
	sf.t3Timer = time.NewTimer(sf.option.config.TimeoutTestT3) // Idle timer, starts running

	sf.timerTrySend = time.NewTimer(sf.option.config.TimeoutSendLinkMsg * time.Millisecond)

	defer sf.t1Timer.Stop()
	defer sf.t3Timer.Stop()
	defer sf.timerTrySend.Stop()

	// Send Request Link Status command
	if err := sf.SendRequestLinkStatus(); err != nil {
		sf.Error("Failed to send initial Request Link Status: %v", err)
		return err // Connection likely lost
	}

	for {
		// Reset T3 timer whenever we are active (sending or expecting response)
		// or after receiving something. Done implicitly by resetting below.

		select {
		case <-sf.connCtx.Done(): // Connection context cancelled
			sf.Debug("Connection context done, stopping protocol run.")
			sf.wg.Wait() // Wait for loops to finish
			return sf.connCtx.Err()

		case <-sf.timerTrySend.C: // Try to send a frame if there is one in the queue
			if !sf.IsLinkActive() {
				sf.timerTrySend.Reset(5 * sf.option.config.TimeoutSendLinkMsg)
				continue
			}
			if len(sf.sendQueue) == 0 { // if nothing to send, request data from secondary station.
				sf.SendReqDataClass2()
			}
			sf.trySendNextFromQueue()
			sf.timerTrySend.Reset(sf.option.config.TimeoutSendLinkMsg * time.Millisecond)

		case <-sf.t1Timer.C: // T1 Timeout: No response received for confirmed message
			sf.Warn("T1 timeout waiting for response.")
			// Timer already fired, no need to Stop/Drain
			if sf.lastSentConfFrame != nil {
				if sf.retryCount < 1 { // Allow one retry using T2 logic (T2 < T1)
					sf.retryCount++
					sf.Warn("Retrying last confirmed frame (Retry %d)", sf.retryCount)
					// Resend the exact same frame (same FCB)
					err := sf.sendFrameToChan(sf.lastSentConfFrame)
					if err != nil {
						sf.Error("Failed to resend frame on T1 retry: %v", err)
						return err // Connection likely lost
					}
					sf.lastSentConfTime = time.Now()
					sf.t1Timer.Reset(sf.option.config.TimeoutRepeatT2) // Use T2 for retry timeout
				} else {
					sf.Error("T1 timeout after retry. Assuming link failure.")
					return ErrTimeoutT1 // Return specific timeout error
				}
			} else {
				sf.Warn("T1 fired but no outstanding frame found.")
			}

		case <-sf.t3Timer.C: // T3 Timeout: Idle period expired
			sf.Debug("T3 idle timeout expired.")
			// Timer already fired, no need to Stop/Drain
			// Send Test Link Frame if link is active and not waiting for confirmation
			if sf.IsLinkActive() && sf.lastSentConfFrame == nil {
				if err := sf.sendTestLink(); err != nil {
					sf.Error("Failed to send Test Link frame on T3 timeout: %v", err)
					return err // Connection likely lost
				}
				// Timer T1 started within sendTestLink
			} else {
				sf.Debug("Skipping Test Link send (Link inactive or waiting for confirm)")
				// Reset T3 to try again later if still idle
				sf.t3Timer.Reset(sf.option.config.TimeoutTestT3)
			}

		case frame := <-sf.rcvFrame:
			sf.lastRecvTime = time.Now() // Update last receive time
			sf.Debug("Received Frame: Start=0x%02X, Ctrl=%s, LinkAddr=%X", frame.Start, frame.GetControlField(), frame.LinkAddr)

			// Stop T1 timer if we were waiting for a response and received one
			// (handleIncomingFrame will decide if it's the correct response)
			if sf.lastSentConfFrame != nil {
				if !sf.t1Timer.Stop() {
					// Drain channel if Stop returned false before Reset
					select {
					case <-sf.t1Timer.C:
					default:
					}
				}
			}
			// Reset T3 timer as we received something
			if !sf.t3Timer.Stop() {
				select {
				case <-sf.t3Timer.C:
				default:
				}
			}
			sf.timerTrySend.Reset(sf.option.config.TimeoutSendLinkMsg)
			if !sf.timerTrySend.Stop() {
				select {
				case <-sf.timerTrySend.C:
				default:
				}
			}
			sf.timerTrySend.Reset(sf.option.config.TimeoutSendLinkMsg)

			err := sf.handleIncomingFrame(frame)
			if err != nil {
				sf.Error("Error handling incoming frame: %v", err)
				// Decide if error is fatal
				// return err // Example: return on fatal error
			}

			// Call onConnect callback upon receiving the first frame for this connection
			if !sf.onConnectCalled {
				sf.Debug("First frame received, calling onConnect handler.")
				sf.onConnect(sf)
				sf.onConnectCalled = true
				// Send initial Reset Link command
				if err := sf.SendResetLink(); err != nil {
					sf.Error("Failed to send initial Reset Link: %v", err)
				}
			} else {
				ctrl := frame.GetControlField()
				if !ctrl.ACD && !ctrl.PRM { // Message from Secondary Station
					// sf.SendReqDataClass2() // Request class 2 data if ACD is not set
				}
			}
		}
	}
}

// handleIncomingFrame processes a parsed frame from the receive loop.
func (sf *Client) handleIncomingFrame(frame *Frame) error {
	// Validate Link Address if configured and non-zero size
	if sf.option.config.LinkAddrSize > 0 {
		receivedAddr := frame.GetLinkAddress()
		expectedAddr := sf.option.config.LinkAddress
		// Check against configured address and broadcast address (if applicable)
		// Assuming 0xFFFF is broadcast for 2-byte address, 0xFF for 1-byte
		isBroadcast := (sf.option.config.LinkAddrSize == 1 && receivedAddr == 0xFF) ||
			(sf.option.config.LinkAddrSize == 2 && receivedAddr == 0xFFFF)

		if receivedAddr != expectedAddr && !isBroadcast {
			sf.Warn("Ignoring frame with unexpected link address %d (expected %d)", receivedAddr, expectedAddr)
			return nil // Ignore frame intended for other stations
		}
	}

	ctrl := frame.GetControlField()

	if ctrl.DFC {
		sf.Warn("Received frame with Data Flow Control active (DFC=1).")
		// Application layer might need to pause sending based on DFC state.
	}

	// Handle based on frame type and control field
	switch frame.Start {
	case StartFixed:
		// Handle fixed-length frames (ACK, NACK, Link Status Resp)
		if !ctrl.PRM { // Message from Secondary Station
			switch ctrl.Fun {
			case SecFcConfACK:
				sf.Debug("Received ACK")
				if sf.lastSentConfFrame != nil {
					// Check if ACK corresponds to the outstanding frame type
					lastCtrl := sf.lastSentConfFrame.GetControlField()
					confirmed := false
					if lastCtrl.Fun == PrimFcResetLink {
						sf.Debug("Link Reset Confirmed by ACK. Link Active.")
						atomic.StoreUint32(&sf.linkStatus, linkStateActive)
						confirmed = true
					} else if lastCtrl.Fun == PrimFcUserDataConf {
						sf.Debug("Confirmed User Data ACKed (FCB=%v)", lastCtrl.FCB)
						sf.fcbState = !lastCtrl.FCB // Toggle FCB state *now*
						confirmed = true
					} else if lastCtrl.Fun == PrimFcReqStatus {
						sf.Debug("Test Link Confirmed by ACK.")
						confirmed = true
					}
					// Add checks for other confirmed requests if needed (e.g., ReqStatus)

					if confirmed {
						sf.lastSentConfFrame = nil // Clear outstanding frame
						sf.retryCount = 0
						// T1 timer was already stopped before calling handleIncomingFrame
					} else {
						sf.Warn("Received ACK but it didn't match outstanding frame (Fun=%d)", lastCtrl.Fun)
						// Keep outstanding frame? Or treat as error? For now, log warning.
						// T1 might restart implicitly if not stopped earlier, or timeout if stopped.
					}
					if lastCtrl.Fun == PrimFcResetLink && !ctrl.ACD {
						sf.SendReqDataClass2() // Send ReqDataClass2 after ResetLink ACK if ACD is not set
					}
				} else {
					// Unsolicited ACK?
					sf.Warn("Received ACK but no confirmed frame outstanding.")
				}

			case SecFcConfNACK:
				sf.Warn("Received NACK (Link Busy or Error)")
				if sf.lastSentConfFrame != nil {
					sf.Warn("NACK received for outstanding frame (Fun=%d). Retrying may occur on T1.", sf.lastSentConfFrame.GetControlField().Fun)
					// Don't clear outstanding frame here. Let T1 handle retry/failure.
					// Reset retry count as NACK is a valid (negative) response.
					sf.retryCount = 0
					// T1 timer was already stopped before calling handleIncomingFrame
				} else {
					sf.Warn("Received NACK but no confirmed frame outstanding.")
				}
			case SecFcRespStatus: // Response to Request Status or unsolicited status
				sf.Debug("Received Link Status Response (DFC=%v)", ctrl.DFC)
				// TODO: Process link status if needed (e.g., DFC bit indicates secondary buffer full)
				// Application layer might need to pause sending based on DFC state.
				// For now, just log the DFC state.
				if ctrl.DFC {
					sf.Warn("Secondary station indicates Data Flow Control active (buffer likely full).")
				}
				// If this was response to ReqStatus, clear outstanding frame
				if sf.lastSentConfFrame != nil && sf.lastSentConfFrame.GetControlField().Fun == PrimFcReqStatus {
					sf.Debug("Request Link Status confirmed by Response.")
					sf.lastSentConfFrame = nil
					sf.retryCount = 0
					// T1 timer was already stopped before calling handleIncomingFrame
				}
			case SecFcRespLinkNF:
				sf.Warn("Received NACK (Link not functioning)")
				if sf.lastSentConfFrame != nil {
					sf.lastSentConfFrame = nil
					sf.retryCount = 0
				}
			case SecFcRespLinkNI:
				sf.Error("Received Link Service Not Implemented")
				if sf.lastSentConfFrame != nil {
					sf.lastSentConfFrame = nil
					sf.retryCount = 0
				}
				// TODO: Handle link failure indication (e.g., trigger reconnect or notify application)
				// For now, log the error. The connection manager might handle reconnection.
			case SecFcUserDataNoRep:
				sf.Debug("NACK: requested data not available")
				sf.fcbState = !sf.lastSentConfFrame.GetControlField().FCB // Toggle FCB state *now*
				if sf.lastSentConfFrame != nil {
					sf.lastSentConfFrame = nil
					sf.retryCount = 0
				}
			default:
				sf.Warn("Received unhandled fixed-length frame from secondary: %s", ctrl)
			}
		} else {
			sf.Warn("Received unexpected fixed-length frame from primary: %s", ctrl)
		}

	case StartVariable:
		// Handle variable-length frames (User Data, Request Responses)
		if !ctrl.PRM { // Message from Secondary Station
			switch ctrl.Fun {
			case SecFcUserDataConf: // Confirmed User Data from Secondary
				sf.Debug("Received Confirmed User Data from Secondary")
				if sf.lastSentConfFrame != nil {
					sf.lastSentConfFrame = nil
					sf.retryCount = 0
				}
				// Decode ASDU
				asdu := frame.GetASDU(&sf.option.params)
				if asdu != nil {
					sf.rcvASDU <- asdu // Send to handler loop
				} else {
					sf.Warn("Failed to decode ASDU from variable frame")
				}
				// Send ACK back
				//sf.sendAck()
			case SecFcRespStatus: // Response containing requested data (Class 1/2) or Link Status
				sf.Debug("Received Response Status/Data from Secondary")
				// If this was response to ReqData1/2, clear outstanding frame
				if sf.lastSentConfFrame != nil &&
					(sf.lastSentConfFrame.GetControlField().Fun == PrimFcReqData1 ||
						sf.lastSentConfFrame.GetControlField().Fun == PrimFcReqData2) {
					sf.Debug("Request Data confirmed by Response.")
					sf.lastSentConfFrame = nil
					sf.retryCount = 0
					// T1 timer was already stopped before calling handleIncomingFrame
				}
				// Decode ASDU if present (could be data or just status)
				asdu := frame.GetASDU(&sf.option.params)
				if asdu != nil {
					sf.rcvASDU <- asdu
				} else {
					// This might be expected if it's just a status response without ASDU
					// sf.Warn("Failed to decode ASDU from variable frame")
				}
			case SecFcRespLinkNF: // NACK - Data Not Available (for Class 1/2 requests)
				sf.Warn("Received Data Not Available NACK")
				// If this was response to ReqData1/2, clear outstanding frame
				if sf.lastSentConfFrame != nil &&
					(sf.lastSentConfFrame.GetControlField().Fun == PrimFcReqData1 ||
						sf.lastSentConfFrame.GetControlField().Fun == PrimFcReqData2) {
					sf.lastSentConfFrame = nil
					sf.retryCount = 0
					// T1 timer was already stopped before calling handleIncomingFrame
				}
			default:
				sf.Warn("Received unhandled variable-length frame from secondary: %s", ctrl)
			}
		} else {
			sf.Warn("Received unexpected variable-length frame from primary: %s", ctrl)
		}
	}

	if ctrl.ACD {
		// request class1 data
		sf.sendReqDataClass1()
	}

	return nil
}

// frameRecvLoop reads from the serial port and parses CS101 frames.
func (sf *Client) frameRecvLoop() {
	sf.Debug("frameRecvLoop started")
	defer func() {
		sf.connCancel() // Signal connection failure/closure to runProtocol
		sf.wg.Done()
		sf.Debug("frameRecvLoop stopped")
	}()

	// Buffer for reading bytes
	// A more robust implementation would use a dedicated buffered reader
	// or state machine to handle partial reads and timeouts correctly.
	// This is simplified.
	for {
		select {
		case <-sf.connCtx.Done():
			return
		default:
			// Use ParseFrame which handles reading internally (simplified version)
			frame, err := ParseFrame(sf.port, sf.option.config.LinkAddrSize)
			if err != nil {
				// Handle different error types
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || (err.Error() == "serial: port closed") {
					sf.Debug("Serial port closed or EOF reached.") // Use Debug
					return                                         // Normal closure or disconnect
				}
				// Checksum/framing errors might be recoverable or indicate noise
				if errors.Is(err, ErrChecksumMismatch) || errors.Is(err, ErrInvalidStartChar) || errors.Is(err, ErrLengthMismatch) {
					sf.Warn("Frame parsing error: %v. Attempting to recover.", err)
					// TODO: Implement recovery logic (e.g., flush buffer, wait)
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
	// Pass 0 for the unused int parameter (originally clientNumber/server context)
	if handlerErr := sf.handler.ASDUHandlerAll(asduPack, sf.clientNumber); handlerErr != nil { // Removed sf argument
		sf.Warn("Error in ASDUHandlerAll: %v", handlerErr)
		// Decide if this error should prevent specific handlers from running
	}

	// Determine specific handler based on TypeID AND CauseOfTransmission
	cause := asduPack.Identifier.Coa.Cause
	typeID := asduPack.Identifier.Type

	switch {
	// Interrogation Responses
	case (cause == asdu.InterrogatedByStation || (cause >= asdu.InterrogatedByGroup1 && cause <= asdu.InterrogatedByGroup16)):
		// Check if typeID is an expected response type (M_ types)
		// This check can be more specific if needed
		if typeID >= asdu.M_SP_NA_1 && typeID <= asdu.M_EP_TF_1 {
			err = sf.handler.InterrogationHandler(asduPack)
		} else {
			sf.Warn("Received unexpected TypeID (%s) for Interrogation COT (%s)", typeID, cause)
			err = sf.handler.ASDUHandler(asduPack, sf.clientNumber) // Removed sf argument // Fallback to generic
		}

	// Counter Interrogation Responses
	case (cause == asdu.RequestByGeneralCounter || (cause >= asdu.RequestByGroup1Counter && cause <= asdu.RequestByGroup4Counter)):
		if typeID == asdu.M_IT_NA_1 || typeID == asdu.M_IT_TA_1 || typeID == asdu.M_IT_TB_1 {
			err = sf.handler.CounterInterrogationHandler(asduPack) // Removed sf argument
		} else {
			sf.Warn("Received unexpected TypeID (%s) for Counter Interrogation COT (%s)", typeID, cause)
			err = sf.handler.ASDUHandler(asduPack, sf.clientNumber) // Removed sf argument // Fallback to generic
		}

	// Activation Confirmation / Termination
	case cause == asdu.ActivationCon || cause == asdu.ActivationTerm:
		// Check if typeID is a command type (C_ types)
		if typeID >= asdu.C_SC_NA_1 && typeID <= asdu.C_BO_NA_1 {
			// Handle confirmation/termination, maybe via generic handler or specific logic
			sf.Debug("Received Activation Confirmation/Termination: %s", asduPack.Identifier)
			err = sf.handler.ASDUHandler(asduPack, sf.clientNumber) // Removed sf argument // Use generic for now
		} else {
			sf.Warn("Received unexpected TypeID (%s) for ActivationCon/Term COT (%s)", typeID, cause)
			err = sf.handler.ASDUHandler(asduPack, sf.clientNumber) // Removed sf argument // Fallback to generic
		}

	// End of Initialization
	case typeID == asdu.M_EI_NA_1 && cause == asdu.Initialized:
		sf.Debug("Received End of Initialization from secondary station.") // Use Debug
		// Potentially trigger interrogation or other actions
		err = sf.handler.ASDUHandler(asduPack, sf.clientNumber) // Removed sf argument // Use generic handler for now

	// --- Add handlers for other expected spontaneous data (M_ types with Spontaneous COT, etc.) ---
	// case cause == asdu.Spontaneous && (typeID >= asdu.M_SP_NA_1 && typeID <= asdu.M_EP_TF_1):
	//     err = sf.handler.ASDUHandler(asduPack, 0) // Removed sf argument

	// Default to the generic handler for everything else
	default:
		err = sf.handler.ASDUHandler(asduPack, sf.clientNumber) // Removed sf argument
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

// IsLinkActive returns true if the link layer is considered active.
func (sf *Client) IsLinkActive() bool {
	return sf.IsConnected() && atomic.LoadUint32(&sf.linkStatus) == linkStateActive
}

// Close disconnects the client and stops all background goroutines.
func (sf *Client) Close() error {
	sf.rwMux.Lock()
	sf.lastSentConfFrame = nil // Clear any pending confirmation
	if sf.cancel == nil {      // Already closed or never started
		sf.rwMux.Unlock()
		return errors.New("client not running")
	}
	sf.Debug("Close requested.") // Use Debug
	sf.cancel()                  // Signal connectionManager to stop
	sf.cancel = nil              // Prevent double close
	sf.rwMux.Unlock()
	// connectionManager will handle closing the port and stopping loops
	return nil
}

// --- CS101 Specific Actions ---

// buildLinkAddress encodes the configured link address based on size.
func (sf *Client) buildLinkAddress() []byte {
	size := sf.option.config.LinkAddrSize
	addr := sf.option.config.LinkAddress
	switch size {
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

// sendFrameToChan marshals and sends a frame via the send channel.
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
		// Buffer full? Or connection closing?
		// Consider adding a timeout or returning ErrBufferFulled
		return ErrBufferFulled // Or a more specific error
	}
}

// sendAck sends a fixed-length ACK frame.
func (sf *Client) sendAck() error {
	// Control field for ACK (PRM=1, Function=?) - Need to define ACK function code for primary
	// Let's assume a simple ACK function code (e.g., 0) for now. Needs verification.
	// Control field: PRM=1, FCV=0, FCB=0, Func=0 --> 0x40
	control := byte(CtrlPRM) // Example ACK control field
	linkAddr := sf.buildLinkAddress()
	ackFrame := NewFixedFrame(control, linkAddr)
	sf.Debug("Queueing ACK frame")
	return sf.sendFrameToChan(ackFrame)
}

// SendResetLink sends a Reset Link command and waits for ACK via T1.
func (sf *Client) SendResetLink() error {
	if sf.lastSentConfFrame != nil {
		return errors.New("cannot send Reset Link while waiting for confirmation")
	}
	control := byte(CtrlPRM | PrimFcResetLink) // PRM=1, FCV=0, FCB=0, Func=0
	linkAddr := sf.buildLinkAddress()
	frame := NewFixedFrame(control, linkAddr)
	sf.Debug("Sending Reset Link command")
	atomic.StoreUint32(&sf.linkStatus, linkStateReset) // Assume link goes to reset state

	err := sf.sendFrameToChan(frame)
	if err == nil {
		sf.lastSentConfFrame = frame // Store for confirmation/timeout
		sf.lastSentConfTime = time.Now()
		sf.retryCount = 0
		// Restart T1 timer (ensure it's stopped first)
		if !sf.t1Timer.Stop() {
			select {
			case <-sf.t1Timer.C:
			default:
			}
		}
		sf.t1Timer.Reset(sf.option.config.TimeoutResponseT1)
	}
	return err
}

// SendRequestLinkStatus sends a Request Link Status command and waits for response via T1.
func (sf *Client) SendRequestLinkStatus() error {
	if sf.lastSentConfFrame != nil {
		return errors.New("cannot send Request Status while waiting for confirmation")
	}
	control := byte(CtrlPRM | PrimFcReqStatus) // PRM=1, FCV=0, FCB=0, Func=9
	linkAddr := sf.buildLinkAddress()
	frame := NewFixedFrame(control, linkAddr)
	sf.Debug("Sending Request Link Status command")

	err := sf.sendFrameToChan(frame)
	if err == nil {
		sf.lastSentConfFrame = frame // Store for confirmation/timeout
		sf.lastSentConfTime = time.Now()
		sf.retryCount = 0
		if !sf.t1Timer.Stop() {
			select {
			case <-sf.t1Timer.C:
			default:
			}
		}
		sf.t1Timer.Reset(sf.option.config.TimeoutResponseT1)
	}
	return err
}

// sendTestLink sends a Test Link command and waits for ACK via T1.
func (sf *Client) sendTestLink() error {
	if sf.lastSentConfFrame != nil {
		// Avoid sending while waiting for another confirmation
		sf.Debug("Skipping Test Link send, waiting for confirmation of previous frame.")
		return nil // Not strictly an error, but can't send now
	}
	control := byte(CtrlPRM | PrimFcTestLink) // PRM=1, FCV=0, FCB=0, Func=9
	linkAddr := sf.buildLinkAddress()
	frame := NewFixedFrame(control, linkAddr)
	sf.Debug("Sending Test Link command")

	err := sf.sendFrameToChan(frame)
	if err == nil {
		sf.lastSentConfFrame = frame // Store for confirmation/timeout
		sf.lastSentConfTime = time.Now()
		sf.retryCount = 0
		// Restart T1 timer (ensure it's stopped first)
		if !sf.t1Timer.Stop() {
			select {
			case <-sf.t1Timer.C:
			default:
			}
		}
		sf.t1Timer.Reset(sf.option.config.TimeoutResponseT1)
	}
	return err
}

// sendReqDataClass1 sends a Request User Data Class 1 command and waits for response via T1.
func (sf *Client) sendReqDataClass1() error {
	if !sf.IsLinkActive() {
		sf.Warn("Cannot send Request Data Class 1: Link not active.")
		return ErrNotActive
	}
	if sf.lastSentConfFrame != nil {
		sf.Debug("Skipping Request Data Class 1 send, waiting for confirmation of previous frame.")
		return errors.New("cannot send Request Data Class 1 while waiting for confirmation")
	}
	control := byte(CtrlPRM | PrimFcReqData1) // PRM=1, Func=10
	control |= CtrlFCV
	if sf.fcbState {
		control |= CtrlFCB
	}

	linkAddr := sf.buildLinkAddress()
	frame := NewFixedFrame(control, linkAddr)
	sf.Debug("Sending Request User Data Class 1 command")

	err := sf.sendFrameToChan(frame)
	if err == nil {
		sf.lastSentConfFrame = frame // Store for confirmation/timeout
		sf.lastSentConfTime = time.Now()
		sf.retryCount = 0
		if !sf.t1Timer.Stop() {
			select {
			case <-sf.t1Timer.C:
			default:
			}
		}
		sf.t1Timer.Reset(sf.option.config.TimeoutResponseT1)
	}
	return err
}

// SendReqDataClass2 sends a Request User Data Class 2 command and waits for response via T1.
func (sf *Client) SendReqDataClass2() error {
	if !sf.IsLinkActive() {
		sf.Warn("Cannot send Request Data Class 2: Link not active.")
		return ErrNotActive
	}
	if sf.lastSentConfFrame != nil {
		sf.Debug("Skipping Request Data Class 2 send, waiting for confirmation of previous frame.")
		return errors.New("cannot send Request Data Class 2 while waiting for confirmation")
	}
	control := byte(CtrlPRM | PrimFcReqData2) // PRM=1, Func=11
	control |= CtrlFCV
	if sf.fcbState {
		control |= CtrlFCB
	}

	linkAddr := sf.buildLinkAddress()
	frame := NewFixedFrame(control, linkAddr)
	sf.Debug("Sending Request User Data Class 2 command")

	err := sf.sendFrameToChan(frame)
	if err == nil {
		sf.lastSentConfFrame = frame // Store for confirmation/timeout
		sf.lastSentConfTime = time.Now()
		sf.retryCount = 0
		if !sf.t1Timer.Stop() {
			select {
			case <-sf.t1Timer.C:
			default:
			}
		}
		sf.t1Timer.Reset(sf.option.config.TimeoutResponseT1)
	}
	return err
}

// Send sends an ASDU as user data.
// Handles creating the appropriate variable-length frame.
// This function handles sending Confirmed User Data (PrimFcUserDataConf).
func (sf *Client) _sendASDU(a *asdu.ASDU) error {

	// This function is called when it's confirmed that we can send a new frame.
	// Pre-condition: sf.lastSentConfFrame should be nil.
	if !sf.IsLinkActive() {
		return ErrNotActive
	}

	// Marshal the ASDU
	asduData, err := a.MarshalBinary()
	if err != nil {
		return fmt.Errorf("failed to marshal ASDU: %w", err)
	}

	// Determine Control Field for User Data (Confirmed)
	// PRM=1, FCV=1 (valid FCB), FCB=(current state), Func=3
	control := byte(CtrlPRM | CtrlFCV | PrimFcUserDataConf)
	if sf.fcbState {
		control |= CtrlFCB
	}

	linkAddr := sf.buildLinkAddress()
	frame := NewDataFrame(control, linkAddr, asduData)

	sf.Debug("Sending User Data (ASDU Type: %s, FCB: %v)", a.Identifier.Type, sf.fcbState)

	err = sf.sendFrameToChan(frame)
	if err == nil {
		sf.lastSentConfFrame = frame // Store for confirmation/timeout
		sf.lastSentConfTime = time.Now()
		sf.retryCount = 0
		if !sf.t1Timer.Stop() {
			select {
			case <-sf.t1Timer.C:
			default:
			}
		}
		sf.t1Timer.Reset(sf.option.config.TimeoutResponseT1)
		// DO NOT toggle fcbState here. Toggle only after receiving ACK.
	}
	return err
}

// Send queues an ASDU for transmission.
// If the link is free and the queue is empty, it sends immediately.
// Otherwise, it enqueues the ASDU.
func (sf *Client) Send(a *asdu.ASDU) error {
	if !sf.IsConnected() { // Basic connection check
		return ErrUseClosedConnection
	}
	if !sf.IsLinkActive() {
		// It might be okay to queue even if link is not fully active yet (e.g. during reset link)
		// but for sending, link active is a better check.
		// For now, let's allow queueing if connected, _sendASDU will check IsLinkActive.
		sf.Debug("Send called but link not active. ASDU will be queued: %s", a.Identifier)
	}

	sf.sendQueueMutex.Lock()

	//// If link is free (no outstanding confirmed frame) AND queue is empty, send immediately.
	// if sf.lastSentConfFrame == nil && len(sf.sendQueue) == 0 {
	// 	sf.sendQueueMutex.Unlock()
	// 	return sf._sendASDU(a)
	// }

	// Otherwise, enqueue the ASDU.
	if len(sf.sendQueue) >= sf.option.config.MaxSendQueueSize {
		sf.sendQueueMutex.Unlock()
		sf.Warn("Send queue full, discarding ASDU: %s. Queue size: %d", a.Identifier, len(sf.sendQueue))
		return ErrSendQueueFull
	}

	sf.sendQueue = append(sf.sendQueue, a)
	sf.Debug("ASDU %s enqueued. Queue size: %d", a.Identifier, len(sf.sendQueue))
	sf.sendQueueMutex.Unlock()
	return nil
}

// trySendNextFromQueue attempts to send the next ASDU from the queue if the link is free.
func (sf *Client) trySendNextFromQueue() {
	sf.sendQueueMutex.Lock()
	if sf.lastSentConfFrame == nil && len(sf.sendQueue) > 0 {
		nextASDU := sf.sendQueue[0]
		sf.sendQueue = sf.sendQueue[1:]
		sf.sendQueueMutex.Unlock() // Unlock before calling _sendASDU

		sf.Debug("Dequeued ASDU %s for sending. Remaining queue size: %d", nextASDU.Identifier, len(sf.sendQueue))
		if err := sf._sendASDU(nextASDU); err != nil {
			sf.Error("Failed to send ASDU from queue (%s): %v. ASDU lost.", nextASDU.Identifier, err)
			// Optionally, re-queue at the front or handle error more robustly.
			// For now, it's logged and lost from the queue.
		}
	} else {
		sf.sendQueueMutex.Unlock()
	}
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
	qoiByte := byte(qoi)                        // Correctly define qoiByte
	infoBytes := append(ioaBytes, qoiByte)

	// Construct the full raw ASDU bytes
	rawASDU, err := buildRawASDU(a.Identifier, infoBytes, &sf.option.params)
	if err != nil {
		return fmt.Errorf("failed to build raw ASDU for InterrogationCmd: %w", err)
	}

	// Unmarshal into the ASDU struct to populate internal fields correctly
	if err := a.UnmarshalBinary(rawASDU); err != nil {
		// This shouldn't fail if buildRawASDU is correct, but check anyway
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
	qccByte := qcc.Value()                      // Use Value() method if available
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
	testPattern := []byte{0xAA, 0x55}           // Standard test pattern
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
	// We need to temporarily assign the infoBytes to the internal field
	// This is a hack due to the unexported field. A better solution would be
	// to modify the asdu package to provide a way to set infoObj or marshal with it.
	// Since we can't modify asdu package directly here, we'll construct the header manually.

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

// UnderlyingConn is required by asdu.Connect interface, but less relevant for serial.
// Return nil or a placeholder.
func (sf *Client) UnderlyingConn() io.ReadWriteCloser {
	return sf.port
}

func (sf *Client) SetClientNumber(n int) {
	sf.clientNumber = n
}

func (sf *Client) ClientNumber() int {
	return sf.clientNumber
}

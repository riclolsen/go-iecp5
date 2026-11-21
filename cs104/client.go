// Copyright 2020 thinkgos (thinkgo@aliyun.com).  All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs104

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/clog"
	"github.com/riclolsen/go-iecp5/secevent"
)

const (
	inactive = iota
	active
)

// Client is an IEC104 master
type Client struct {
	option       ClientOption
	conn         net.Conn
	handler      ClientHandlerInterface
	pairedServer *Server
	clientNumber int

	// channel
	rcvASDU  chan []byte // for received asdu
	sendASDU chan []byte // for send asdu
	rcvRaw   chan []byte // for recvLoop raw cs104 frame
	sendRaw  chan []byte // for sendLoop raw cs104 frame

	// I frame send and receive sequence number
	seqNoSend uint16 // sequence number of next outbound I-frame
	ackNoSend uint16 // outbound sequence number yet to be confirmed
	seqNoRcv  uint16 // sequence number of next inbound I-frame
	ackNoRcv  uint16 // inbound sequence number yet to be confirmed

	// maps sendTime I-frames to their respective sequence number
	pending []seqPending

	startDtActiveSendSince atomic.Value // The timeout interval to wait for an acknowledgment reply when sending startDtActive
	stopDtActiveSendSince  atomic.Value // Timeout waiting for confirmation reply when stopDtActive is initiated

	// Connection Status
	status   uint32
	rwMux    sync.RWMutex
	isActive uint32

	// other
	clog.Clog

	wg          sync.WaitGroup
	ctx         context.Context
	cancel      context.CancelFunc
	closeCancel context.CancelFunc

	onConnect        func(c *Client)
	onConnectionLost func(c *Client)
	onConnectTimeout func(c *Client)
	onActivated      func(c *Client)
	onDeactivated    func(c *Client)

	// security events
	cyber     secevent.Emitter
	sessionID atomic.Uint64
	peerMu    sync.RWMutex
	peer      secevent.PeerInfo
	started   time.Time
	framesIn  atomic.Uint64
	framesOut atomic.Uint64
	endMu     sync.Mutex
	endReason string
}

// NewClient returns an IEC104 master,default config and default asdu.ParamsWide params
func NewClient(handler ClientHandlerInterface, o *ClientOption) *Client {
	c := &Client{
		option:           *o,
		handler:          handler,
		rcvASDU:          make(chan []byte, o.config.RecvUnAckLimitW<<4),
		sendASDU:         make(chan []byte, o.config.SendUnAckLimitK<<4),
		rcvRaw:           make(chan []byte, o.config.RecvUnAckLimitW<<5),
		sendRaw:          make(chan []byte, o.config.SendUnAckLimitK<<5), // may not block!
		Clog:             clog.NewLogger("cs104 client => "),
		onConnect:        func(*Client) {},
		onConnectionLost: func(*Client) {},
		onConnectTimeout: func(*Client) {},
		onActivated:      func(*Client) {},
		onDeactivated:    func(*Client) {},
	}
	c.cyber.Protocol, c.cyber.Role = "iec104", "client"
	return c
}

// SetOnConnectHandler set on connect handler
func (sf *Client) SetOnConnectHandler(f func(c *Client)) *Client {
	if f != nil {
		sf.onConnect = f
	}
	return sf
}

// SetConnectionLostHandler set connection lost handler
func (sf *Client) SetConnectionLostHandler(f func(c *Client)) *Client {
	if f != nil {
		sf.onConnectionLost = f
	}
	return sf
}

// SetConnectionLostHandler set connection lost handler
func (sf *Client) SetConnectTimeoutHandler(f func(c *Client)) *Client {
	if f != nil {
		sf.onConnectTimeout = f
	}
	return sf
}

// SetOnActivatedHandler sets a handler invoked when StartDT is confirmed by the server
func (sf *Client) SetOnActivatedHandler(f func(c *Client)) *Client {
	if f != nil {
		sf.onActivated = f
	}
	return sf
}

// SetOnDeactivatedHandler sets a handler invoked when StopDT is confirmed by the server
func (sf *Client) SetOnDeactivatedHandler(f func(c *Client)) *Client {
	if f != nil {
		sf.onDeactivated = f
	}
	return sf
}

// Start start the server,and return quickly,if it nil,the server will disconnected background,other failed
func (sf *Client) Start() error {
	if sf.option.server == nil {
		return errors.New("empty remote server")
	}
	if advice := sf.option.config.flowControlAdvice(); advice != "" {
		sf.Warn("%s", advice)
	}

	go sf.running()
	return nil
}

// Connect is
func (sf *Client) running() {
	var ctx context.Context

	sf.rwMux.Lock()
	if !atomic.CompareAndSwapUint32(&sf.status, initial, disconnected) {
		sf.rwMux.Unlock()
		return
	}
	ctx, sf.closeCancel = context.WithCancel(context.Background())
	sf.rwMux.Unlock()
	defer sf.setConnectStatus(initial)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		sf.Debug("connecting server %+v", sf.option.server)
		conn, err := openConnection(sf.option.server, sf.option.TLSConfig, sf.option.config.ConnectTimeout0)
		if err != nil {
			sf.Error("connect failed, %v", err)
			sf.emit(secevent.Event{Kind: secevent.KindConnDialFailed, Err: err,
				Peer:   secevent.PeerInfo{Network: "tcp"},
				Detail: fmt.Sprintf("could not connect to %v", sf.option.server)})
			sf.onConnectTimeout(sf)
			if !sf.option.autoReconnect {
				return
			}
			time.Sleep(sf.option.reconnectInterval)
			continue
		}
		sf.Debug("connect success")
		sf.conn = conn
		sf.run(ctx)

		sf.Debug("disconnected server %+v", sf.option.server)
		select {
		case <-ctx.Done():
			return
		default:
			// Random 500ms-1s retry to avoid fast retry causing many invalid connections to the server
			time.Sleep(time.Millisecond * time.Duration(500+rand.Intn(500)))
		}
	}
}

func (sf *Client) recvLoop() {
	sf.Debug("recvLoop started")
	defer func() {
		sf.cancel()
		sf.wg.Done()
		sf.Debug("recvLoop stopped")
	}()

	fr := newFrameReader(sf.conn, sf.option.config.SendUnAckTimeout1)
	for {
		apdu, err := fr.next()
		if err != nil {
			sf.Error("receive failed, %v", err)
			sf.noteEnd(fmt.Sprintf("receive failed: %v", err))
			return
		}
		sf.Debug("RX Raw[% x]", apdu)
		select {
		case sf.rcvRaw <- apdu:
		case <-sf.ctx.Done():
			return
		}
	}
}

func (sf *Client) sendLoop() {
	sf.Debug("sendLoop started")
	defer func() {
		sf.cancel()
		sf.wg.Done()
		sf.Debug("sendLoop stopped")
	}()
	for {
		select {
		case <-sf.ctx.Done():
			return
		case apdu := <-sf.sendRaw:
			sf.Debug("TX Raw[% x]", apdu)
			sf.framesOut.Add(1)
			for wrCnt := 0; len(apdu) > wrCnt; {
				// A peer that stops reading must not be able to hold this
				// goroutine, and with it the state machine, for ever.
				_ = sf.conn.SetWriteDeadline(time.Now().Add(sf.option.config.SendUnAckTimeout1))
				byteCount, err := sf.conn.Write(apdu[wrCnt:])
				if errors.Is(err, os.ErrDeadlineExceeded) {
					sf.Error("sendRaw stalled for longer than t₁, peer is not reading: %v", err)
					sf.noteEnd("peer stopped reading (write stalled for t₁)")
					sf.emit(secevent.Event{Kind: secevent.KindDoSWriteStall, Action: secevent.ActDisconnected,
						Detail: "peer is not reading: a write was blocked for longer than t₁", Err: err})
					return
				}
				if err != nil {
					// See: https://github.com/golang/go/issues/4373
					if err != io.EOF && err != io.ErrClosedPipe ||
						strings.Contains(err.Error(), "use of closed network connection") {
						sf.Error("sendRaw failed, %v", err)
						sf.noteEnd(fmt.Sprintf("send failed: %v", err))
						return
					}
					if e, ok := err.(net.Error); !ok || !e.Temporary() {
						sf.Error("sendRaw failed, %v", err)
						sf.noteEnd(fmt.Sprintf("send failed: %v", err))
						return
					}
					// temporary error may be recoverable
				}
				wrCnt += byteCount
			}
		}
	}
}

// run is the big fat state machine.
func (sf *Client) run(ctx context.Context) {
	sf.Debug("run started!")
	// before any thing make sure init
	sf.cleanUp()

	sf.ctx, sf.cancel = context.WithCancel(ctx)
	sf.setConnectStatus(connected)
	sf.beginCyber()
	sf.wg.Add(3)
	go sf.recvLoop()
	go sf.sendLoop()
	go sf.handlerLoop()

	var checkTicker = time.NewTicker(timeoutResolution)

	// transmission timestamps for timeout calculation
	var willNotTimeout = time.Now().Add(time.Hour * 24 * 365 * 100)

	var unAckRcvSince = willNotTimeout
	var idleTimeout3Sine = time.Now()         // idle interval initiated testFrAlive
	var testFrAliveSendSince = willNotTimeout // When testFrAlive is initiated, the timeout interval for waiting for a confirmation reply

	sf.startDtActiveSendSince.Store(willNotTimeout)
	sf.stopDtActiveSendSince.Store(willNotTimeout)

	sendSFrame := func(rcvSN uint16) {
		sf.Debug("TX sFrame %v", sAPCI{rcvSN})
		sf.queueRaw(newSFrame(rcvSN))
	}

	sendIFrame := func(asdu1 []byte) {
		seqNo := sf.seqNoSend

		iframe, err := newIFrame(seqNo, sf.seqNoRcv, asdu1)
		if err != nil {
			// MarshalBinary refuses an oversized ASDU, so this should be
			// unreachable. Saying so anyway is the point: an ASDU accepted
			// for sending and then vanishing here would leave the caller
			// believing its data went out.
			sf.Error("dropping an ASDU that will not fit an APDU: %v", err)
		}
		sf.ackNoRcv = sf.seqNoRcv
		sf.seqNoSend = (seqNo + 1) & 32767
		sf.pending = append(sf.pending, seqPending{seqNo & 32767, time.Now()})

		sf.Debug("TX iFrame %v", iAPCI{seqNo, sf.seqNoRcv})
		sf.queueRaw(iframe)
	}

	defer func() {
		// default: STOPDT, when connected establish and not enable "data transfer" yet
		atomic.StoreUint32(&sf.isActive, inactive)
		sf.setConnectStatus(disconnected)
		checkTicker.Stop()
		_ = sf.conn.Close() // chain trigger cancel
		sf.wg.Wait()
		sf.endCyber()
		sf.onConnectionLost(sf)
		sf.Debug("run stopped!")
	}()

	sf.onConnect(sf)
	for {
		// "k" is the maximum number of outstanding (unacknowledged) I-frames,
		// so stop handing out new ones once the count reaches k.
		if atomic.LoadUint32(&sf.isActive) == active && seqNoCount(sf.ackNoSend, sf.seqNoSend) < sf.option.config.SendUnAckLimitK {
			select {
			case o := <-sf.sendASDU:
				sendIFrame(o)
				idleTimeout3Sine = time.Now()
				continue
			case <-sf.ctx.Done():
				return
			default: // make no block
			}
		}
		select {
		case <-sf.ctx.Done():
			return
		case now := <-checkTicker.C:
			// check all timeouts
			if now.Sub(testFrAliveSendSince) >= sf.option.config.SendUnAckTimeout1 ||
				now.Sub(sf.startDtActiveSendSince.Load().(time.Time)) >= sf.option.config.SendUnAckTimeout1 ||
				now.Sub(sf.stopDtActiveSendSince.Load().(time.Time)) >= sf.option.config.SendUnAckTimeout1 {
				sf.Error("test frame alive confirm timeout t₁")
				sf.noteEnd("STARTDT, STOPDT or TESTFR not confirmed within t₁")
				sf.emit(secevent.Event{Kind: secevent.KindConnT1Timeout, Action: secevent.ActDisconnected,
					Detail: "STARTDT, STOPDT or TESTFR act was not confirmed within t₁"})
				return
			}
			// check oldest unacknowledged outbound
			if sf.ackNoSend != sf.seqNoSend &&
				//now.Sub(sf.peek()) >= sf.SendUnAckTimeout1 {
				now.Sub(sf.pending[0].sendTime) >= sf.option.config.SendUnAckTimeout1 {
				sf.ackNoSend++
				sf.Error("fatal transmission timeout t₁")
				sf.noteEnd("I-frame not acknowledged within t₁")
				sf.emit(secevent.Event{Kind: secevent.KindConnT1Timeout, Action: secevent.ActDisconnected,
					Detail: "an I-frame was not acknowledged within t₁"})
				return
			}

			// Determine whether the earliest i-Frame sent has timed out, and will reply sFrame when timed out
			if sf.ackNoRcv != sf.seqNoRcv &&
				(now.Sub(unAckRcvSince) >= sf.option.config.RecvUnAckTimeout2 ||
					now.Sub(idleTimeout3Sine) >= timeoutResolution) {
				sendSFrame(sf.seqNoRcv)
				sf.ackNoRcv = sf.seqNoRcv
			}

			// When the idle time is up, send a TestFrActive frame to keep alive. Only
			// one is outstanding at a time: sending another would restart the t₁
			// clock, and a peer that never answers would then never be dropped
			// when t₃ is shorter than t₁.
			if testFrAliveSendSince == willNotTimeout && now.Sub(idleTimeout3Sine) >= sf.option.config.IdleTimeout3 {
				sf.queueUFrame(uTestFrActive)
				testFrAliveSendSince = time.Now()
				idleTimeout3Sine = testFrAliveSendSince
			}

		case apdu := <-sf.rcvRaw:
			idleTimeout3Sine = time.Now() // Every time an i frame, S frame, U frame is received, the idle timer is reset, t3
			apci, asduVal, err := parse(apdu)
			if err != nil {
				// A malformed control field is not acted on. Ignoring it
				// rather than closing the link keeps a peer from being able
				// to drop the connection with a single bad frame; the log
				// line is what makes the sender's fault visible.
				sf.Error("%v, frame ignored [% x]", err, apdu)
				continue
			}
			sf.framesIn.Add(1)
			switch head := apci.(type) {
			case sAPCI:
				sf.Debug("RX sFrame %v", head)
				if !sf.updateAckNoOut(head.rcvSN) {
					sf.Error("fatal incoming acknowledge either earlier than previous or later than sendTime")
					return
				}

			case iAPCI:
				sf.Debug("RX iFrame %v", head)
				if atomic.LoadUint32(&sf.isActive) == inactive {
					sf.Warn("station not active")
					break // not active, discard apdu
				}
				if !sf.updateAckNoOut(head.rcvSN) || head.sendSN != sf.seqNoRcv {
					sf.Error("fatal incoming acknowledge either earlier than previous or later than sendTime")
					return
				}

				select {
				case sf.rcvASDU <- asduVal:
				case <-sf.ctx.Done():
					return
				}
				if sf.ackNoRcv == sf.seqNoRcv { // first unacked
					unAckRcvSince = time.Now()
				}

				sf.seqNoRcv = (sf.seqNoRcv + 1) & 32767
				if seqNoCount(sf.ackNoRcv, sf.seqNoRcv) >= sf.option.config.RecvUnAckLimitW {
					sendSFrame(sf.seqNoRcv)
					sf.ackNoRcv = sf.seqNoRcv
				}

			case uAPCI:
				sf.Debug("RX uFrame %v", head)
				switch head.function {
				//case uStartDtActive:
				//	sf.sendUFrame(uStartDtConfirm)
				//	atomic.StoreUint32(&sf.isActive, active)
				case uStartDtConfirm:
					atomic.StoreUint32(&sf.isActive, active)
					sf.startDtActiveSendSince.Store(willNotTimeout)
					sf.onActivated(sf)
				//case uStopDtActive:
				//	sf.sendUFrame(uStopDtConfirm)
				//	atomic.StoreUint32(&sf.isActive, inactive)
				case uStopDtConfirm:
					atomic.StoreUint32(&sf.isActive, inactive)
					sf.stopDtActiveSendSince.Store(willNotTimeout)
					sf.onDeactivated(sf)
				case uTestFrActive:
					sf.queueUFrame(uTestFrConfirm)
				case uTestFrConfirm:
					testFrAliveSendSince = willNotTimeout
				default:
					sf.Error("illegal U-Frame functions[0x%02x] ignored", head.function)
				}
			}
		}
	}
}

func (sf *Client) handlerLoop() {
	sf.Debug("handlerLoop started")
	defer func() {
		sf.wg.Done()
		sf.Debug("handlerLoop stopped")
	}()

	for {
		select {
		case <-sf.ctx.Done():
			return
		case rawAsdu := <-sf.rcvASDU:
			asduPack := asdu.NewEmptyASDU(&sf.option.params)
			if err := asduPack.UnmarshalBinary(rawAsdu); err != nil {
				sf.Warn("asdu UnmarshalBinary failed,%+v", err)
				continue
			}
			if err := sf.clientHandler(asduPack); err != nil {
				sf.Warn("Falied handling I frame, error: %v", err)
			}
		}
	}
}

func (sf *Client) setConnectStatus(status uint32) {
	sf.rwMux.Lock()
	atomic.StoreUint32(&sf.status, status)
	sf.rwMux.Unlock()
}

func (sf *Client) connectStatus() uint32 {
	sf.rwMux.RLock()
	status := atomic.LoadUint32(&sf.status)
	sf.rwMux.RUnlock()
	return status
}

func (sf *Client) cleanUp() {
	sf.ackNoRcv = 0
	sf.ackNoSend = 0
	sf.seqNoRcv = 0
	sf.seqNoSend = 0
	sf.pending = nil
	// clear sending chan buffer
loop:
	for {
		select {
		case <-sf.sendRaw:
		case <-sf.rcvRaw:
		case <-sf.rcvASDU:
		case <-sf.sendASDU:
		default:
			break loop
		}
	}
}

func (sf *Client) sendUFrame(which byte) {
	sf.Debug("TX uFrame %v", uAPCI{which})
	sf.sendRaw <- newUFrame(which)
}

// queueUFrame is sendUFrame for the protocol goroutine.
func (sf *Client) queueUFrame(which byte) {
	sf.Debug("TX uFrame %v", uAPCI{which})
	sf.queueRaw(newUFrame(which))
}

// queueRaw is sendUFrame for the protocol goroutine: it gives up when the
// session ends instead of blocking on a send loop that has stopped.
func (sf *Client) queueRaw(b []byte) {
	select {
	case sf.sendRaw <- b:
	case <-sf.ctx.Done():
	}
}

func (sf *Client) updateAckNoOut(ackNo uint16) (ok bool) {
	if ackNo == sf.ackNoSend {
		return true
	}
	// new acks validate, ack cannot be in front of req seq, error
	if seqNoCount(sf.ackNoSend, sf.seqNoSend) < seqNoCount(ackNo, sf.seqNoSend) {
		return false
	}

	// confirm reception; sequence numbers live in [0, 32767], so the
	// "previous" of 0 wraps to 32767 (not 65535 as plain uint16 math gives).
	for i, v := range sf.pending {
		if v.seq == (ackNo-1)&32767 {
			sf.pending = sf.pending[i+1:]
			break
		}
	}

	sf.ackNoSend = ackNo
	return true
}

// IsConnected get server session connected state
func (sf *Client) IsConnected() bool {
	return sf.connectStatus() == connected
}

// IsActive returns whether the data transfer is active (StartDT confirmed)
func (sf *Client) IsActive() bool {
	return atomic.LoadUint32(&sf.isActive) == active
}

// clientHandler hand response handler
func (sf *Client) clientHandler(asduPack *asdu.ASDU) error {
	defer func() {
		if err := recover(); err != nil {
			sf.Critical("client handler %+v", err)
		}
	}()

	sf.Debug("ASDU %+v", asduPack)
	sf.handler.ASDUHandlerAll(sf, asduPack, sf.pairedServer, sf.clientNumber)

	switch asduPack.Identifier.Type {
	case asdu.C_IC_NA_1: // InterrogationCmd
		return sf.handler.InterrogationHandler(sf, asduPack)

	case asdu.C_CI_NA_1: // CounterInterrogationCmd
		return sf.handler.CounterInterrogationHandler(sf, asduPack)

	case asdu.C_RD_NA_1: // ReadCmd
		return sf.handler.ReadHandler(sf, asduPack)

	case asdu.C_CS_NA_1: // ClockSynchronizationCmd
		return sf.handler.ClockSyncHandler(sf, asduPack)

	case asdu.C_TS_NA_1: // TestCommand
		return sf.handler.TestCommandHandler(sf, asduPack)

	case asdu.C_RP_NA_1: // ResetProcessCmd
		return sf.handler.ResetProcessHandler(sf, asduPack)

	case asdu.C_CD_NA_1: // DelayAcquireCommand
		return sf.handler.DelayAcquisitionHandler(sf, asduPack)
	}

	return sf.handler.ASDUHandler(sf, asduPack, sf.pairedServer, sf.clientNumber)
}

// Params returns params of client
func (sf *Client) Params() *asdu.Params {
	return &sf.option.params
}

// Send send asdu
func (sf *Client) Send(a *asdu.ASDU) error {
	if !sf.IsConnected() {
		return ErrUseClosedConnection
	}
	if atomic.LoadUint32(&sf.isActive) == inactive {
		return ErrNotActive
	}
	data, err := a.MarshalBinary()
	if err != nil {
		return err
	}
	select {
	case sf.sendASDU <- data:
	default:
		return ErrBufferFulled
	}
	return nil
}

// UnderlyingConn returns underlying conn of client
func (sf *Client) UnderlyingConn() net.Conn {
	return sf.conn
}

// Close close all
func (sf *Client) Close() error {
	sf.rwMux.Lock()
	if sf.closeCancel != nil {
		sf.closeCancel()
	}
	sf.rwMux.Unlock()
	return nil
}

// SendStartDt start data transmission on this connection
func (sf *Client) SendStartDt() {
	sf.startDtActiveSendSince.Store(time.Now())
	sf.sendUFrame(uStartDtActive)
}

// SendStopDt stop data transmission on this connection
func (sf *Client) SendStopDt() {
	sf.stopDtActiveSendSince.Store(time.Now())
	sf.sendUFrame(uStopDtActive)
}

// InterrogationCmd wrap asdu.InterrogationCmd
func (sf *Client) InterrogationCmd(coa asdu.CauseOfTransmission, ca asdu.CommonAddr, qoi asdu.QualifierOfInterrogation) error {
	return asdu.InterrogationCmd(sf, coa, ca, qoi)
}

// CounterInterrogationCmd wrap asdu.CounterInterrogationCmd
func (sf *Client) CounterInterrogationCmd(coa asdu.CauseOfTransmission, ca asdu.CommonAddr, qcc asdu.QualifierCountCall) error {
	return asdu.CounterInterrogationCmd(sf, coa, ca, qcc)
}

// ReadCmd wrap asdu.ReadCmd
func (sf *Client) ReadCmd(coa asdu.CauseOfTransmission, ca asdu.CommonAddr, ioa asdu.InfoObjAddr) error {
	return asdu.ReadCmd(sf, coa, ca, ioa)
}

// ClockSynchronizationCmd wrap asdu.ClockSynchronizationCmd
func (sf *Client) ClockSynchronizationCmd(coa asdu.CauseOfTransmission, ca asdu.CommonAddr, t time.Time) error {
	return asdu.ClockSynchronizationCmd(sf, coa, ca, t)
}

// ResetProcessCmd wrap asdu.ResetProcessCmd
func (sf *Client) ResetProcessCmd(coa asdu.CauseOfTransmission, ca asdu.CommonAddr, qrp asdu.QualifierOfResetProcessCmd) error {
	return asdu.ResetProcessCmd(sf, coa, ca, qrp)
}

// DelayAcquireCommand wrap asdu.DelayAcquireCommand
func (sf *Client) DelayAcquireCommand(coa asdu.CauseOfTransmission, ca asdu.CommonAddr, msec uint16) error {
	return asdu.DelayAcquireCommand(sf, coa, ca, msec)
}

// TestCommand  wrap asdu.TestCommand
func (sf *Client) TestCommand(coa asdu.CauseOfTransmission, ca asdu.CommonAddr) error {
	return asdu.TestCommand(sf, coa, ca)
}

// Allow to specify a paired server (e.g. for AL data forwarding)
func (sf *Client) SetPairedServer(s *Server) {
	sf.pairedServer = s
}

func (sf *Client) SetClientNumber(n int) {
	sf.clientNumber = n
}

func (sf *Client) ClientNumber() int {
	return sf.clientNumber
}

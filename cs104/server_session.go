// Copyright 2020 thinkgos (thinkgo@aliyun.com).  All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs104

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/clog"
	"github.com/riclolsen/go-iecp5/internal/secasdu"
	"github.com/riclolsen/go-iecp5/secevent"
)

const (
	initial uint32 = iota
	disconnected
	connected
)

// SrvSession the cs104 server session
type SrvSession struct {
	config  *Config
	params  *asdu.Params
	conn    net.Conn
	handler ServerHandlerInterface

	rcvASDU  chan []byte // for received asdu
	sendASDU chan []byte // for send asdu
	rcvRaw   chan []byte // for recvLoop raw cs104 frame
	sendRaw  chan []byte // for sendLoop raw cs104 frame

	// see subclass 5.1 — Protection against loss and duplication of messages
	seqNoSend uint16 // sequence number of next outbound I-frame
	ackNoSend uint16 // outbound sequence number yet to be confirmed
	seqNoRcv  uint16 // sequence number of next inbound I-frame
	ackNoRcv  uint16 // inbound sequence number yet to be confirmed
	// maps sendTime I-frames to their respective sequence number
	pending []seqPending
	//seqManage

	status uint32
	rwMux  sync.RWMutex

	clog.Clog

	onConnection   func(asdu.Connect)
	connectionLost func(asdu.Connect)

	wg           sync.WaitGroup
	cancel       context.CancelFunc
	ctx          context.Context
	serverNumber int

	// security events
	cyber     *secevent.Emitter
	sessionID atomic.Uint64
	inbound   bool // accepted by a Server, as opposed to dialled out
	peerMu    sync.RWMutex
	peer      secevent.PeerInfo
	started   time.Time
	framesIn  atomic.Uint64 // frames that parsed
	framesOut atomic.Uint64
	endMu     sync.Mutex
	endReason string
	guard     *secevent.Guard
	pol       secevent.Policy
	fl        *secasdu.FrameLimits
	insp      *secasdu.Inspector
	// admission result handed over by the Server, consumed by beginCyber
	presetID   uint64
	presetPeer *secevent.PeerInfo
}

// RecvLoop feeds t.rcvRaw.
func (sf *SrvSession) recvLoop() {
	sf.Debug("recvLoop started!")
	defer func() {
		sf.cancel()
		sf.wg.Done()
		sf.Debug("recvLoop stopped!")
	}()

	fr := newFrameReader(sf.conn, sf.config.SendUnAckTimeout1)
	if sf.cyberOn() {
		fr.hook = func(k secevent.Kind, detail string, raw []byte) {
			sf.emit(secevent.Event{Kind: k, Action: secevent.ActIgnored, Detail: detail, Raw: raw})
		}
	}
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

// sendLoop drains t.sendTime.
func (sf *SrvSession) sendLoop() {
	sf.Debug("sendLoop started!")
	defer func() {
		sf.cancel()
		sf.wg.Done()
		sf.Debug("sendLoop stopped!")
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
				_ = sf.conn.SetWriteDeadline(time.Now().Add(sf.config.SendUnAckTimeout1))
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
func (sf *SrvSession) run(ctx context.Context) {
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

	// default: STOPDT, when connected establish and not enable "data transfer" yet
	var isActive = false
	// pendingStopDt records a STOPDT activation whose confirmation is waiting
	// for the I-frames already sent to be acknowledged.
	var pendingStopDt = false
	var checkTicker = time.NewTicker(timeoutResolution)
	var everActive = false
	var inactiveLimit time.Duration
	if sf.guard != nil {
		inactiveLimit = sf.guard.Policy().InactiveTimeout
	}
	var connectedAt = time.Now()

	// transmission timestamps for timeout calculation
	var willNotTimeout = time.Now().Add(time.Hour * 24 * 365 * 100)

	var unAckRcvSince = willNotTimeout
	var idleTimeout3Sine = time.Now()         // Initiate testFrAlive in idle interval
	var testFrAliveSendSince = willNotTimeout // When testFrAlive is initiated, the timeout interval for waiting for a confirmation reply
	// For the server side, there is no need for a corresponding U-Frame, no need to judge
	// var startDtActiveSendSince = willNotTimeout
	// var stopDtActiveSendSince = willNotTimeout

	sendSFrame := func(rcvSN uint16) {
		sf.Debug("TX sFrame %v", sAPCI{rcvSN})
		sf.queueRaw(newSFrame(rcvSN))
	}
	sendUFrame := func(which byte) {
		sf.Debug("TX uFrame %v", uAPCI{which})
		sf.queueRaw(newUFrame(which))
	}

	// confirmStopDtIfDone answers a STOPDT activation once the I-frames that
	// were already on the wire have been acknowledged. Until then the
	// controlling station is still owed data, and saying transfer has stopped
	// would be untrue.
	confirmStopDtIfDone := func() {
		if pendingStopDt && sf.ackNoSend == sf.seqNoSend {
			sf.Debug("outstanding data acknowledged, confirming STOPDT")
			sendUFrame(uStopDtConfirm)
			pendingStopDt = false
		}
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
	if sf.onConnection != nil {
		sf.onConnection(sf)
	}
	defer func() {
		sf.setConnectStatus(disconnected)
		checkTicker.Stop()
		_ = sf.conn.Close() // chain trigger cancel
		sf.wg.Wait()
		sf.endCyber()
		if sf.connectionLost != nil {
			sf.connectionLost(sf)
		}
		sf.Debug("run stopped!")
	}()

	for {
		// "k" is the maximum number of outstanding (unacknowledged) I-frames,
		// so stop handing out new ones once the count reaches k.
		if isActive && seqNoCount(sf.ackNoSend, sf.seqNoSend) < sf.config.SendUnAckLimitK {
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
			if now.Sub(testFrAliveSendSince) >= sf.config.SendUnAckTimeout1 {
				// now.Sub(startDtActiveSendSince) >= t.SendUnAckTimeout1 ||
				// now.Sub(stopDtActiveSendSince) >= t.SendUnAckTimeout1 ||
				sf.Error("test frame alive confirm timeout t₁")
				sf.noteEnd("TESTFR not confirmed within t₁")
				sf.emit(secevent.Event{Kind: secevent.KindConnT1Timeout, Action: secevent.ActDisconnected,
					Detail: "TESTFR act was not confirmed within t₁"})
				return
			}
			if inactiveLimit > 0 && !everActive && now.Sub(connectedAt) >= inactiveLimit {
				sf.Error("session never started data transfer, closing")
				sf.noteEnd("STARTDT never received")
				sf.emit(secevent.Event{Kind: secevent.KindConnNeverStarted, Action: secevent.ActDisconnected,
					Detail: fmt.Sprintf("no STARTDT within %v of connecting", inactiveLimit)})
				return
			}
			// check oldest unacknowledged outbound
			if sf.ackNoSend != sf.seqNoSend &&
				//now.Sub(sf.peek()) >= sf.SendUnAckTimeout1 {
				now.Sub(sf.pending[0].sendTime) >= sf.config.SendUnAckTimeout1 {
				sf.ackNoSend++
				sf.Error("fatal transmission timeout t₁")
				sf.noteEnd("I-frame not acknowledged within t₁")
				sf.emit(secevent.Event{Kind: secevent.KindConnT1Timeout, Action: secevent.ActDisconnected,
					Detail: "an I-frame was not acknowledged within t₁"})
				return
			}

			// Determine whether the earliest i-Frame sent has timed out, and will reply sFrame when timed out
			if sf.ackNoRcv != sf.seqNoRcv &&
				(now.Sub(unAckRcvSince) >= sf.config.RecvUnAckTimeout2 ||
					now.Sub(idleTimeout3Sine) >= timeoutResolution) {
				sendSFrame(sf.seqNoRcv)
				sf.ackNoRcv = sf.seqNoRcv
			}

			// When the idle time is up, send a TestFrActive frame to keep alive. Only
			// one is outstanding at a time: sending another would restart the t₁
			// clock, and a peer that never answers would then never be dropped
			// when t₃ is shorter than t₁.
			if testFrAliveSendSince == willNotTimeout && now.Sub(idleTimeout3Sine) >= sf.config.IdleTimeout3 {
				sendUFrame(uTestFrActive)
				testFrAliveSendSince = time.Now()
				idleTimeout3Sine = testFrAliveSendSince
			}

		case apdu := <-sf.rcvRaw:
			idleTimeout3Sine = time.Now() // Every time an i frame, S frame, U frame is received, the idle timer is reset, t3
			if sf.fl != nil && sf.frameFlood(sf.fl.Frames, secevent.KindFloodFrames, "inbound frame rate") {
				return
			}
			apci, asduVal, err := parse(apdu)
			if err != nil {
				// A malformed control field is not acted on. Ignoring it
				// rather than closing the link keeps a peer from being able
				// to drop the connection with a single bad frame; the log
				// line is what makes the sender's fault visible.
				sf.Error("%v, frame ignored [% x]", err, apdu)
				sf.emit(secevent.Event{Kind: secevent.KindProtoBadAPCI, Action: secevent.ActIgnored,
					Detail: "malformed APCI: frame not acted on", Err: err, Raw: apdu})
				continue
			}
			sf.framesIn.Add(1)
			switch head := apci.(type) {
			case sAPCI:
				sf.Debug("RX sFrame %v", head)
				if sf.fl != nil && head.rcvSN == sf.ackNoSend &&
					sf.frameFlood(sf.fl.SFrames, secevent.KindFloodSFrames, "rate of S-frames that acknowledge nothing new") {
					return
				}
				if !sf.updateAckNoOut(head.rcvSN) {
					sf.Error("fatal incoming acknowledge either earlier than previous or later than sendTime")
					sf.seqViolation(ackKind(sf.ackNoSend, sf.seqNoSend, head.rcvSN), 0, head.rcvSN, apdu)
					return
				}
				confirmStopDtIfDone()

			case iAPCI:
				sf.Debug("RX iFrame %v", head)
				if !isActive {
					// Its acknowledgement still counts, which is what lets a
					// pending STOPDT complete.
					if sf.updateAckNoOut(head.rcvSN) {
						confirmStopDtIfDone()
					}
					sf.Warn("station not active")
					sf.emit(secevent.Event{Kind: secevent.KindProtoIWhileStopped, Action: secevent.ActIgnored,
						Detail: "I-frame received while data transfer is stopped: discarded", Raw: apdu})
					break // not active, discard apdu
				}
				if !sf.updateAckNoOut(head.rcvSN) {
					sf.Error("fatal incoming acknowledge either earlier than previous or later than sendTime")
					sf.seqViolation(ackKind(sf.ackNoSend, sf.seqNoSend, head.rcvSN), head.sendSN, head.rcvSN, apdu)
					return
				}
				if head.sendSN != sf.seqNoRcv {
					sf.Error("fatal incoming acknowledge either earlier than previous or later than sendTime")
					sf.seqViolation(sendSNKind(head.sendSN, sf.seqNoRcv), head.sendSN, head.rcvSN, apdu)
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
				if seqNoCount(sf.ackNoRcv, sf.seqNoRcv) > sf.config.SendUnAckLimitK {
					sf.emit(secevent.Event{Kind: secevent.KindSeqPeerK, Action: secevent.ActObserved,
						Detail: fmt.Sprintf("the peer has sent %d I-frames without an acknowledgement, more than k=%d",
							seqNoCount(sf.ackNoRcv, sf.seqNoRcv), sf.config.SendUnAckLimitK)})
				}
				if seqNoCount(sf.ackNoRcv, sf.seqNoRcv) >= sf.config.RecvUnAckLimitW {
					sendSFrame(sf.seqNoRcv)
					sf.ackNoRcv = sf.seqNoRcv
				}

			case uAPCI:
				sf.Debug("RX uFrame %v", head)
				switch head.function {
				case uStartDtActive:
					if sf.fl != nil && sf.frameFlood(sf.fl.UToggle, secevent.KindFloodUToggle, "STARTDT/STOPDT rate") {
						return
					}
					sf.emit(secevent.Event{Kind: secevent.KindCtrlStartDT, Detail: "STARTDT act received"})
					sendUFrame(uStartDtConfirm)
					isActive = true
					everActive = true
					// A new STARTDT supersedes a STOPDT still waiting for its
					// acknowledgements. Left set, the next acknowledgement
					// would emit a STOPDT con on a session the controlling
					// station has just started, and it would deactivate.
					pendingStopDt = false
				// case uStartDtConfirm:
				// 	isActive = true
				// 	startDtActiveSendSince = willNotTimeout
				case uStopDtActive:
					if sf.fl != nil && sf.frameFlood(sf.fl.UToggle, secevent.KindFloodUToggle, "STARTDT/STOPDT rate") {
						return
					}
					sf.emit(secevent.Event{Kind: secevent.KindCtrlStopDT, Detail: "STOPDT act received"})
					// Stop handing out new I-frames at once, but do not
					// confirm yet: the confirmation says data transfer has
					// finished, and I-frames already sent may still be
					// unacknowledged. Confirming early tells the controlling
					// station the link is idle while data it never
					// acknowledged is still in flight — which is exactly the
					// moment it would switch to a redundant connection or
					// close this one.
					// This is the procedure of IEC 60870-5-104 subclause 5.3
					// and what the IEC 60870-5-604 conformance tests expect.
					isActive = false
					// Acknowledge what was received before stopping, so the
					// controlling station is not left waiting on us either.
					if sf.ackNoRcv != sf.seqNoRcv {
						sendSFrame(sf.seqNoRcv)
						sf.ackNoRcv = sf.seqNoRcv
					}
					pendingStopDt = true
					if sf.ackNoSend == sf.seqNoSend {
						sendUFrame(uStopDtConfirm)
						pendingStopDt = false
					} else {
						sf.Debug("STOPDT requested with %d unacknowledged I-frame(s): confirming once they are acknowledged",
							seqNoCount(sf.ackNoSend, sf.seqNoSend))
					}
				// case uStopDtConfirm:
				// 	isActive = false
				// 	stopDtActiveSendSince = willNotTimeout
				case uTestFrActive:
					if sf.fl != nil && sf.frameFlood(sf.fl.TestFR, secevent.KindFloodTestFR, "TESTFR act rate (keep-alive storm)") {
						return
					}
					sendUFrame(uTestFrConfirm)
				case uTestFrConfirm:
					if testFrAliveSendSince == willNotTimeout {
						sf.emit(secevent.Event{Kind: secevent.KindUnsolUConfirm, Action: secevent.ActIgnored,
							Detail: "TESTFR con received with no TESTFR act outstanding"})
					}
					testFrAliveSendSince = willNotTimeout
				case uStartDtConfirm, uStopDtConfirm:
					// Monitor-direction frames: only an outstation sends them.
					sf.emit(secevent.Event{Kind: secevent.KindProtoUWrongDir, Action: secevent.ActIgnored,
						Detail: fmt.Sprintf("%v received by the controlled station: wrong direction", head)})
				default:
					sf.Error("illegal U-Frame functions[0x%02x] ignored", head.function)
				}
			}
		}
	}
}

// handlerLoop handler iFrame asdu
func (sf *SrvSession) handlerLoop() {
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
			asduPack := asdu.NewEmptyASDU(sf.params)
			if err := asduPack.UnmarshalBinary(rawAsdu); err != nil {
				sf.Error("asdu UnmarshalBinary failed,%+v", err)
				sf.emit(secevent.Event{Kind: secevent.KindProtoASDUInvalid, Action: secevent.ActIgnored,
					Detail: "ASDU rejected by the decoder: " + err.Error(), Err: err, Raw: rawAsdu})
				continue
			}
			if sf.inspect(asduPack, rawAsdu) {
				continue
			}
			if err := sf.serverHandler(asduPack); err != nil {
				sf.Error("serverHandler falied,%+v", err)
			}
		}
	}
}

func (sf *SrvSession) setConnectStatus(status uint32) {
	sf.rwMux.Lock()
	atomic.StoreUint32(&sf.status, status)
	sf.rwMux.Unlock()
}

func (sf *SrvSession) connectStatus() uint32 {
	sf.rwMux.RLock()
	status := atomic.LoadUint32(&sf.status)
	sf.rwMux.RUnlock()
	return status
}

func (sf *SrvSession) cleanUp() {
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

// rewind mechanism
func seqNoCount(nextAckNo, nextSeqNo uint16) uint16 {
	if nextAckNo > nextSeqNo {
		nextSeqNo += 32768
	}
	return nextSeqNo - nextAckNo
}

func (sf *SrvSession) updateAckNoOut(ackNo uint16) (ok bool) {
	if ackNo == sf.ackNoSend {
		return true
	}
	// new acks validate， ack not in req seq front, error
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

func (sf *SrvSession) serverHandler(asduPack *asdu.ASDU) error {
	defer func() {
		if err := recover(); err != nil {
			sf.Critical("server handler %+v", err)
		}
	}()

	sf.Debug("ASDU %+v", asduPack)
	sf.handler.ASDUHandlerAll(sf, asduPack, sf.serverNumber)

	switch asduPack.Identifier.Type {
	case asdu.C_IC_NA_1: // InterrogationCmd
		if !(asduPack.Identifier.Coa.Cause == asdu.Activation ||
			asduPack.Identifier.Coa.Cause == asdu.Deactivation) {
			return asduPack.SendReplyMirror(sf, asdu.UnknownCOT)
		}
		if asduPack.CommonAddr == asdu.InvalidCommonAddr {
			return asduPack.SendReplyMirror(sf, asdu.UnknownCA)
		}
		ioa, qoi := asduPack.GetInterrogationCmd()
		if ioa != asdu.InfoObjAddrIrrelevant {
			return asduPack.SendReplyMirror(sf, asdu.UnknownIOA)
		}
		return sf.handler.InterrogationHandler(sf, asduPack, qoi)

	case asdu.C_CI_NA_1: // CounterInterrogationCmd
		if asduPack.Identifier.Coa.Cause != asdu.Activation {
			return asduPack.SendReplyMirror(sf, asdu.UnknownCOT)
		}
		if asduPack.CommonAddr == asdu.InvalidCommonAddr {
			return asduPack.SendReplyMirror(sf, asdu.UnknownCA)
		}
		ioa, qcc := asduPack.GetCounterInterrogationCmd()
		if ioa != asdu.InfoObjAddrIrrelevant {
			return asduPack.SendReplyMirror(sf, asdu.UnknownIOA)
		}
		return sf.handler.CounterInterrogationHandler(sf, asduPack, qcc)

	case asdu.C_RD_NA_1: // ReadCmd
		if asduPack.Identifier.Coa.Cause != asdu.Request {
			return asduPack.SendReplyMirror(sf, asdu.UnknownCOT)
		}
		if asduPack.CommonAddr == asdu.InvalidCommonAddr {
			return asduPack.SendReplyMirror(sf, asdu.UnknownCA)
		}
		return sf.handler.ReadHandler(sf, asduPack, asduPack.GetReadCmd())

	case asdu.C_CS_NA_1: // ClockSynchronizationCmd
		if asduPack.Identifier.Coa.Cause != asdu.Activation {
			return asduPack.SendReplyMirror(sf, asdu.UnknownCOT)
		}
		if asduPack.CommonAddr == asdu.InvalidCommonAddr {
			return asduPack.SendReplyMirror(sf, asdu.UnknownCA)
		}

		ioa, tm := asduPack.GetClockSynchronizationCmd()
		if ioa != asdu.InfoObjAddrIrrelevant {
			return asduPack.SendReplyMirror(sf, asdu.UnknownIOA)
		}
		return sf.handler.ClockSyncHandler(sf, asduPack, tm)

	case asdu.C_TS_NA_1: // TestCommand
		if asduPack.Identifier.Coa.Cause != asdu.Activation {
			return asduPack.SendReplyMirror(sf, asdu.UnknownCOT)
		}
		if asduPack.CommonAddr == asdu.InvalidCommonAddr {
			return asduPack.SendReplyMirror(sf, asdu.UnknownCA)
		}
		ioa, _ := asduPack.GetTestCommand()
		if ioa != asdu.InfoObjAddrIrrelevant {
			return asduPack.SendReplyMirror(sf, asdu.UnknownIOA)
		}
		return asduPack.SendReplyMirror(sf, asdu.ActivationCon)

	case asdu.C_RP_NA_1: // ResetProcessCmd
		if asduPack.Identifier.Coa.Cause != asdu.Activation {
			return asduPack.SendReplyMirror(sf, asdu.UnknownCOT)
		}
		if asduPack.CommonAddr == asdu.InvalidCommonAddr {
			return asduPack.SendReplyMirror(sf, asdu.UnknownCA)
		}
		ioa, qrp := asduPack.GetResetProcessCmd()
		if ioa != asdu.InfoObjAddrIrrelevant {
			return asduPack.SendReplyMirror(sf, asdu.UnknownIOA)
		}
		return sf.handler.ResetProcessHandler(sf, asduPack, qrp)
	case asdu.C_CD_NA_1: // DelayAcquireCommand
		if !(asduPack.Identifier.Coa.Cause == asdu.Activation ||
			asduPack.Identifier.Coa.Cause == asdu.Spontaneous) {
			return asduPack.SendReplyMirror(sf, asdu.UnknownCOT)
		}
		if asduPack.CommonAddr == asdu.InvalidCommonAddr {
			return asduPack.SendReplyMirror(sf, asdu.UnknownCA)
		}
		ioa, msec := asduPack.GetDelayAcquireCommand()
		if ioa != asdu.InfoObjAddrIrrelevant {
			return asduPack.SendReplyMirror(sf, asdu.UnknownIOA)
		}
		return sf.handler.DelayAcquisitionHandler(sf, asduPack, msec)
	}

	if err := sf.handler.ASDUHandler(sf, asduPack); err != nil {
		return asduPack.SendReplyMirror(sf, asdu.UnknownTypeID)
	}
	return nil
}

// IsConnected get server session connected state
func (sf *SrvSession) IsConnected() bool {
	return sf.connectStatus() == connected
}

// Params get params
func (sf *SrvSession) Params() *asdu.Params {
	return sf.params
}

// Send asdu frame
func (sf *SrvSession) Send(u *asdu.ASDU) error { return sf.send(u, true) }

// send queues an ASDU. observe says whether a refusal in it is reported as a
// security event; the library's own refusals were reported when decided.
func (sf *SrvSession) send(u *asdu.ASDU, observe bool) error {
	if observe {
		sf.observeOutbound(u)
	}
	if !sf.IsConnected() {
		return ErrUseClosedConnection
	}
	data, err := u.MarshalBinary()
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

// UnderlyingConn got under net.conn
func (sf *SrvSession) UnderlyingConn() net.Conn {
	return sf.conn
}

// queueRaw hands a frame to the send loop. It gives up when the session ends:
// the send loop is the only reader of sendRaw, so once it has stopped, for
// instance because the peer stopped reading, a bare channel send here would
// block the state machine for ever instead of letting it tear the session down.
func (sf *SrvSession) queueRaw(b []byte) {
	select {
	case sf.sendRaw <- b:
	case <-sf.ctx.Done():
	}
}

// seqViolation reports the sequence error that is about to close the session.
func (sf *SrvSession) seqViolation(kind secevent.Kind, send, recv uint16, raw []byte) {
	sf.noteEnd("sequence error: " + string(kind))
	sf.emit(secevent.Event{Kind: kind, Action: secevent.ActDisconnected, Fields: secevent.FieldSeq,
		SendSN: send, RecvSN: recv, ExpectedSN: sf.seqNoRcv, Raw: raw,
		Detail: seqDetail(kind, send, recv, sf.seqNoRcv, sf.ackNoSend, sf.seqNoSend)})
}

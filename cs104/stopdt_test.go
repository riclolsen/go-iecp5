package cs104

import (
	"bytes"
	"encoding/hex"
	"io"
	"net"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
)

// STOPDT confirms that data transfer has finished. Sending the confirmation
// while I-frames are still unacknowledged says the link is idle when it is
// not — and STOPDT is used at exactly the moment a controlling station is
// about to close this connection or switch to a redundant one, so what was
// unacknowledged is what gets lost.

// pushHandler answers an interrogation with several ASDUs, so the outstation
// has I-frames outstanding.
type pushHandler struct{ nullHandler }

func (pushHandler) InterrogationHandler(c asdu.Connect, a *asdu.ASDU, _ asdu.QualifierOfInterrogation) error {
	_ = a.SendReplyMirror(c, asdu.ActivationCon)
	cause := asdu.CauseOfTransmission{Cause: asdu.InterrogatedByStation}
	for i := 0; i < 5; i++ {
		_ = asdu.Single(c, false, cause, 1,
			asdu.SinglePointInfo{Ioa: asdu.InfoObjAddr(100 + i), Value: true})
	}
	return a.SendReplyMirror(c, asdu.ActivationTerm)
}

// readFrame reads one APDU.
func readFrame(conn net.Conn, d time.Duration) []byte {
	_ = conn.SetReadDeadline(time.Now().Add(d))
	head := make([]byte, 2)
	if _, err := conn.Read(head); err != nil {
		return nil
	}
	body := make([]byte, head[1])
	for n := 0; n < len(body); {
		m, err := conn.Read(body[n:])
		if err != nil {
			return nil
		}
		n += m
	}
	return append(head, body...)
}

var (
	stopDtAct  = []byte{startFrame, 4, uStopDtActive | 0x03, 0, 0, 0}
	stopDtCon  = []byte{startFrame, 4, uStopDtConfirm | 0x03, 0, 0, 0}
	startDtAct = []byte{startFrame, 4, uStartDtActive | 0x03, 0, 0, 0}
	startDtCon = []byte{startFrame, 4, uStartDtConfirm | 0x03, 0, 0, 0}
)

// dialStarted brings a session up to the point where data transfer is active.
// cfg, when not nil, configures the server.
func dialStarted(t *testing.T, h ServerHandlerInterface, cfg *Config) net.Conn {
	t.Helper()
	addr := startServer(t, h, cfg)

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	write(t, conn, startDtAct)
	if got := readFrame(conn, 2*time.Second); !bytes.Equal(got, startDtCon) {
		t.Fatalf("no STARTDT con: got %s", hex.EncodeToString(got))
	}
	return conn
}

func write(t *testing.T, conn net.Conn, frame []byte) {
	t.Helper()
	if _, err := conn.Write(frame); err != nil {
		t.Fatal(err)
	}
}

// interrogation is an I-frame carrying a station interrogation.
func interrogation(sendSN, rcvSN uint16) []byte {
	asduBytes := []byte{byte(asdu.C_IC_NA_1), 0x01, 0x06, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x14}
	iframe, _ := newIFrame(sendSN, rcvSN, asduBytes)
	return iframe
}

// readUnacked reads frames until the outstation falls silent for quiet, and
// returns how many I-frames it sent and the receive sequence number that
// acknowledges all of them.
func readUnacked(t *testing.T, conn net.Conn, quiet time.Duration) (n int, ackTo uint16) {
	t.Helper()
	for {
		f := readFrame(conn, quiet)
		if len(f) == 0 {
			break
		}
		if f[2]&0x01 == 0 { // I-frame
			ackTo = (uint16(f[2])>>1 + uint16(f[3])<<7 + 1) & 32767
			n++
		}
	}
	if n == 0 {
		t.Fatal("the outstation sent no I-frames; the test proves nothing")
	}
	return n, ackTo
}

// pendingStopDt leaves the session with I-frames unacknowledged and a STOPDT
// act waiting on them, and returns the acknowledgement that would release it.
func pendingStopDt(t *testing.T, conn net.Conn, quiet time.Duration) uint16 {
	t.Helper()
	write(t, conn, interrogation(0, 0))
	unacked, ackTo := readUnacked(t, conn, quiet)

	write(t, conn, stopDtAct)
	if got := readFrame(conn, quiet); got != nil {
		t.Fatalf("with %d I-frames unacknowledged, STOPDT act must be answered by nothing yet: got %s",
			unacked, hex.EncodeToString(got))
	}
	return ackTo
}

func TestStopDtWaitsForOutstandingData(t *testing.T) {
	conn := dialStarted(t, pushHandler{}, nil)
	ackTo := pendingStopDt(t, conn, time.Second)

	// Acknowledge everything; the confirmation must follow promptly.
	write(t, conn, newSFrame(ackTo))
	if got := readFrame(conn, 3*time.Second); !bytes.Equal(got, stopDtCon) {
		t.Fatalf("after acknowledging, STOPDT was not confirmed: got %s", hex.EncodeToString(got))
	}
}

// The acknowledgement carried by an I-frame counts as well as an S-frame's,
// even though the I-frame itself is discarded while data transfer is stopped.
func TestStopDtCompletedByIFrameAck(t *testing.T) {
	conn := dialStarted(t, pushHandler{}, nil)
	ackTo := pendingStopDt(t, conn, time.Second)

	write(t, conn, interrogation(1, ackTo))
	if got := readFrame(conn, 3*time.Second); !bytes.Equal(got, stopDtCon) {
		t.Fatalf("an I-frame's acknowledgement did not complete STOPDT: got %s", hex.EncodeToString(got))
	}
	// And the interrogation it carried must not start data flowing again.
	if got := readFrame(conn, time.Second); got != nil {
		t.Fatalf("frame sent after STOPDT con: %s", hex.EncodeToString(got))
	}
}

// A STARTDT that arrives while a STOPDT is still waiting supersedes it. The
// late acknowledgement must not then produce a STOPDT con, which would
// deactivate a session the controlling station has just started.
func TestStartDtCancelsPendingStopDt(t *testing.T) {
	conn := dialStarted(t, pushHandler{}, nil)
	ackTo := pendingStopDt(t, conn, time.Second)

	write(t, conn, startDtAct)
	if got := readFrame(conn, 2*time.Second); !bytes.Equal(got, startDtCon) {
		t.Fatalf("no STARTDT con: got %s", hex.EncodeToString(got))
	}
	write(t, conn, newSFrame(ackTo))
	if got := readFrame(conn, 1500*time.Millisecond); got != nil {
		t.Fatalf("frame sent after the acknowledgement of a started session: %s", hex.EncodeToString(got))
	}
}

// A repeated STOPDT act while one is pending gets one confirmation, not two.
func TestStopDtRepeatedWhilePending(t *testing.T) {
	conn := dialStarted(t, pushHandler{}, nil)
	ackTo := pendingStopDt(t, conn, time.Second)

	write(t, conn, stopDtAct)
	if got := readFrame(conn, time.Second); got != nil {
		t.Fatalf("repeated STOPDT act answered while data is outstanding: %s", hex.EncodeToString(got))
	}
	write(t, conn, newSFrame(ackTo))
	if got := readFrame(conn, 3*time.Second); !bytes.Equal(got, stopDtCon) {
		t.Fatalf("after acknowledging, STOPDT was not confirmed: got %s", hex.EncodeToString(got))
	}
	if got := readFrame(conn, time.Second); got != nil {
		t.Fatalf("second frame after STOPDT con: %s", hex.EncodeToString(got))
	}
}

// A controlling station that never acknowledges does not leave the session
// hanging: t₁ runs out and the connection closes.
func TestStopDtWithoutAcknowledgementTimesOut(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SendUnAckTimeout1 = 2 * time.Second
	cfg.RecvUnAckTimeout2 = 1 * time.Second
	conn := dialStarted(t, pushHandler{}, &cfg)
	sent := time.Now()
	_ = pendingStopDt(t, conn, 300*time.Millisecond)

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != io.EOF {
		t.Fatalf("expected the outstation to close the connection, got %d bytes (% x), err %v", n, buf[:n], err)
	}
	if elapsed := time.Since(sent); elapsed < cfg.SendUnAckTimeout1 {
		t.Fatalf("connection closed after %v, before t₁ = %v", elapsed, cfg.SendUnAckTimeout1)
	}
}

// With nothing outstanding there is nothing to wait for.
func TestStopDtConfirmsImmediatelyWhenIdle(t *testing.T) {
	conn := dialStarted(t, nullHandler{}, nil)

	write(t, conn, stopDtAct)
	if got := readFrame(conn, 2*time.Second); !bytes.Equal(got, stopDtCon) {
		t.Fatalf("STOPDT was not confirmed on an idle session: got %s", hex.EncodeToString(got))
	}
}

// Data transfer must stop at once, even though the confirmation waits.
func TestStopDtStopsNewDataImmediately(t *testing.T) {
	conn := dialStarted(t, pushHandler{}, nil)

	write(t, conn, stopDtAct)
	if got := readFrame(conn, 2*time.Second); !bytes.Equal(got, stopDtCon) {
		t.Fatalf("idle STOPDT not confirmed: %s", hex.EncodeToString(got))
	}

	// An interrogation after STOPDT must produce no I-frames.
	write(t, conn, interrogation(0, 0))
	for {
		f := readFrame(conn, 1200*time.Millisecond)
		if len(f) == 0 {
			break
		}
		if f[2]&0x01 == 0 {
			t.Fatalf("an I-frame was sent after STOPDT: %s", hex.EncodeToString(f))
		}
	}
}

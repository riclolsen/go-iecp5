package cs104

import (
	"bytes"
	"encoding/hex"
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

var stopDtCon = []byte{startFrame, 4, uStopDtConfirm | 0x03, 0, 0, 0}

// dialStarted brings a session up to the point where data transfer is active.
func dialStarted(t *testing.T, h ServerHandlerInterface) net.Conn {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	srv := NewServer(h)
	srv.LogMode(false)
	go func() { _ = srv.ListenAndServer(addr) }()
	t.Cleanup(func() { _ = srv.Close() })
	time.Sleep(200 * time.Millisecond)

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if _, err := conn.Write([]byte{startFrame, 4, uStartDtActive | 0x03, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if got := readFrame(conn, 2*time.Second); len(got) == 0 {
		t.Fatal("no STARTDT con")
	}
	return conn
}

func TestStopDtWaitsForOutstandingData(t *testing.T) {
	conn := dialStarted(t, pushHandler{})

	// Interrogate, then read the replies without acknowledging them.
	asduBytes := []byte{byte(asdu.C_IC_NA_1), 0x01, 0x06, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x14}
	iframe := append([]byte{startFrame, byte(4 + len(asduBytes)), 0x00, 0x00, 0x00, 0x00}, asduBytes...)
	if _, err := conn.Write(iframe); err != nil {
		t.Fatal(err)
	}

	unacked := 0
	var lastSendSN uint16
	for {
		f := readFrame(conn, 1500*time.Millisecond)
		if len(f) == 0 {
			break
		}
		if f[2]&0x01 == 0 { // I-frame: remember its send sequence number
			lastSendSN = uint16(f[2])>>1 + uint16(f[3])<<7
			unacked++
		}
	}
	if unacked == 0 {
		t.Fatal("the outstation sent no I-frames; the test proves nothing")
	}

	// Ask it to stop. Nothing may be confirmed yet.
	if _, err := conn.Write([]byte{startFrame, 4, uStopDtActive | 0x03, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if got := readFrame(conn, 1500*time.Millisecond); bytes.Equal(got, stopDtCon) {
		t.Fatalf("STOPDT confirmed with %d I-frames unacknowledged", unacked)
	}

	// Acknowledge everything; the confirmation must follow promptly.
	ackTo := lastSendSN + 1
	if _, err := conn.Write(newSFrame(ackTo)); err != nil {
		t.Fatal(err)
	}
	got := readFrame(conn, 3*time.Second)
	if !bytes.Equal(got, stopDtCon) {
		t.Fatalf("after acknowledging, STOPDT was not confirmed: got %s", hex.EncodeToString(got))
	}
}

// With nothing outstanding there is nothing to wait for.
func TestStopDtConfirmsImmediatelyWhenIdle(t *testing.T) {
	conn := dialStarted(t, nullHandler{})

	if _, err := conn.Write([]byte{startFrame, 4, uStopDtActive | 0x03, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	got := readFrame(conn, 2*time.Second)
	if !bytes.Equal(got, stopDtCon) {
		t.Fatalf("STOPDT was not confirmed on an idle session: got %s", hex.EncodeToString(got))
	}
}

// Data transfer must stop at once, even though the confirmation waits.
func TestStopDtStopsNewDataImmediately(t *testing.T) {
	conn := dialStarted(t, pushHandler{})

	if _, err := conn.Write([]byte{startFrame, 4, uStopDtActive | 0x03, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if got := readFrame(conn, 2*time.Second); !bytes.Equal(got, stopDtCon) {
		t.Fatalf("idle STOPDT not confirmed: %s", hex.EncodeToString(got))
	}

	// An interrogation after STOPDT must produce no I-frames.
	asduBytes := []byte{byte(asdu.C_IC_NA_1), 0x01, 0x06, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x14}
	iframe := append([]byte{startFrame, byte(4 + len(asduBytes)), 0x00, 0x00, 0x00, 0x00}, asduBytes...)
	if _, err := conn.Write(iframe); err != nil {
		t.Fatal(err)
	}
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

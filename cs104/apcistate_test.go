package cs104

import (
	"bytes"
	"encoding/hex"
	"net"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
)

// A malformed control field must not change the state of the link.
//
// The S and U formats carry no payload to be validated later: whatever their
// control field says is acted on directly — activating or deactivating data
// transfer, or moving the acknowledged sequence number. A U frame whose
// reserved octets were not clear, or whose APDU length was not 4, used to be
// honoured as a STARTDT activation.
//
// A raw socket is the honest way to ask this: it is exactly what a peer on
// the wire can send, with no help from the library's own encoder.

type nullHandler struct{}

func (nullHandler) InterrogationHandler(asdu.Connect, *asdu.ASDU, asdu.QualifierOfInterrogation) error {
	return nil
}
func (nullHandler) CounterInterrogationHandler(asdu.Connect, *asdu.ASDU, asdu.QualifierCountCall) error {
	return nil
}
func (nullHandler) ReadHandler(asdu.Connect, *asdu.ASDU, asdu.InfoObjAddr) error { return nil }
func (nullHandler) ClockSyncHandler(asdu.Connect, *asdu.ASDU, time.Time) error   { return nil }
func (nullHandler) ResetProcessHandler(asdu.Connect, *asdu.ASDU, asdu.QualifierOfResetProcessCmd) error {
	return nil
}
func (nullHandler) DelayAcquisitionHandler(asdu.Connect, *asdu.ASDU, uint16) error { return nil }
func (nullHandler) ASDUHandler(asdu.Connect, *asdu.ASDU) error                     { return nil }
func (nullHandler) ASDUHandlerAll(asdu.Connect, *asdu.ASDU, int) error             { return nil }

func startTestServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	srv := NewServer(nullHandler{})
	srv.LogMode(false)
	go func() { _ = srv.ListenAndServer(addr) }()
	t.Cleanup(func() { _ = srv.Close() })
	time.Sleep(200 * time.Millisecond)
	return addr
}

// sendAndRead writes one frame on a fresh connection and returns whatever
// comes back within the window, or nil.
func sendAndRead(t *testing.T, addr string, frame []byte) []byte {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		return nil
	}
	return buf[:n]
}

func TestMalformedUFrameDoesNotActivateDataTransfer(t *testing.T) {
	addr := startTestServer(t)
	startDtCon := []byte{startFrame, 4, uStartDtConfirm | 0x03, 0, 0, 0}

	// The control: a conforming STARTDT activation must still work, or the
	// rest of this test proves nothing.
	got := sendAndRead(t, addr, []byte{startFrame, 4, uStartDtActive | 0x03, 0x00, 0x00, 0x00})
	if !bytes.Equal(got, startDtCon) {
		t.Fatalf("a well formed STARTDT act was not confirmed: got %s", hex.EncodeToString(got))
	}

	for _, tc := range []struct {
		name  string
		frame []byte
	}{
		{"reserved control octets set",
			[]byte{startFrame, 4, uStartDtActive | 0x03, 0xFF, 0xFF, 0xFF}},
		{"one reserved octet set",
			[]byte{startFrame, 4, uStartDtActive | 0x03, 0x00, 0x01, 0x00}},
		{"APDU length 10 instead of 4",
			append([]byte{startFrame, 10, uStartDtActive | 0x03, 0x00, 0x00, 0x00},
				bytes.Repeat([]byte{0xAA}, 6)...)},
		{"length 10 and reserved octets set",
			append([]byte{startFrame, 10, uStartDtActive | 0x03, 0xDE, 0xAD, 0xBE},
				bytes.Repeat([]byte{0xAA}, 6)...)},
		{"two function bits set",
			[]byte{startFrame, 4, 0x0F, 0x00, 0x00, 0x00}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sendAndRead(t, addr, tc.frame)
			if bytes.Equal(got, startDtCon) {
				t.Errorf("data transfer was activated by a malformed frame [% x]", tc.frame)
			}
			if len(got) != 0 {
				t.Errorf("answered a malformed frame with %s", hex.EncodeToString(got))
			}
		})
	}

	// And the link is still usable afterwards: a malformed frame is ignored,
	// not treated as grounds to drop the connection, so a peer cannot kill a
	// session with one bad packet.
	got = sendAndRead(t, addr, []byte{startFrame, 4, uStartDtActive | 0x03, 0x00, 0x00, 0x00})
	if !bytes.Equal(got, startDtCon) {
		t.Errorf("the server stopped answering well formed frames: got %s", hex.EncodeToString(got))
	}
}

// TestMalformedFrameOnAnOpenSession: the same frames must be refused on a
// session that is already up, where they would otherwise deactivate it.
func TestMalformedFrameOnAnOpenSession(t *testing.T) {
	addr := startTestServer(t)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	read := func() []byte {
		_ = conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
		buf := make([]byte, 64)
		n, err := conn.Read(buf)
		if err != nil {
			return nil
		}
		return buf[:n]
	}

	if _, err := conn.Write([]byte{startFrame, 4, uStartDtActive | 0x03, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if got := read(); !bytes.Equal(got, []byte{startFrame, 4, uStartDtConfirm | 0x03, 0, 0, 0}) {
		t.Fatalf("STARTDT was not confirmed: %s", hex.EncodeToString(got))
	}

	// A malformed STOPDT must not deactivate the session, and a malformed
	// TESTFR must not be answered.
	for _, frame := range [][]byte{
		{startFrame, 4, uStopDtActive | 0x03, 0x01, 0x00, 0x00},
		{startFrame, 8, uStopDtActive | 0x03, 0x00, 0x00, 0x00, 0xAA, 0xAA, 0xAA, 0xAA},
		{startFrame, 4, uTestFrActive | 0x03, 0xFF, 0x00, 0x00},
	} {
		if _, err := conn.Write(frame); err != nil {
			t.Fatal(err)
		}
		if got := read(); len(got) != 0 {
			t.Errorf("malformed frame [% x] was answered with %s", frame, hex.EncodeToString(got))
		}
	}

	// The session is still up: a conforming TESTFR is still confirmed.
	if _, err := conn.Write([]byte{startFrame, 4, uTestFrActive | 0x03, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if got := read(); !bytes.Equal(got, []byte{startFrame, 4, uTestFrConfirm | 0x03, 0, 0, 0}) {
		t.Errorf("the session did not survive the malformed frames: %s", hex.EncodeToString(got))
	}
}

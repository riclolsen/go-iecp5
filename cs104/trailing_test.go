package cs104

import (
	"net"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
)

// recordingHandler reports every command ASDU that reaches the application.
type recordingHandler struct {
	nullHandler
	got chan asdu.SingleCommandInfo
}

func (h *recordingHandler) ASDUHandler(c asdu.Connect, a *asdu.ASDU) error {
	if a.Type == asdu.C_SC_NA_1 {
		select {
		case h.got <- a.GetSingleCmd():
		default:
		}
	}
	return nil
}

func startRecordingServer(t *testing.T) (string, chan asdu.SingleCommandInfo) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	h := &recordingHandler{got: make(chan asdu.SingleCommandInfo, 4)}
	srv := NewServer(h)
	srv.LogMode(false)
	go func() { _ = srv.ListenAndServer(addr) }()
	t.Cleanup(func() { _ = srv.Close() })
	time.Sleep(200 * time.Millisecond)
	return addr, h.got
}

// TestPaddedCommandIsNotExecuted injects an I-frame whose ASDU is one valid
// single command followed by garbage. Such an ASDU used to reach the handler
// and be executed, the surplus discarded unseen.
func TestPaddedCommandIsNotExecuted(t *testing.T) {
	for _, trailing := range [][]byte{nil, {0xAA}, {0xDE, 0xAD, 0xBE, 0xEF}} {
		addr, got := startRecordingServer(t)
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}

		// Activate data transfer, or the I-frame is discarded.
		if _, err := conn.Write([]byte{startFrame, 4, uStartDtActive | 0x03, 0, 0, 0}); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		ack := make([]byte, 6)
		if _, err := conn.Read(ack); err != nil {
			t.Fatalf("no STARTDT con: %v", err)
		}

		// C_SC_NA_1, one object, activation, CA 1, IOA 100, ON + execute.
		asduBytes := []byte{
			byte(asdu.C_SC_NA_1), 0x01, 0x06, 0x00, 0x01, 0x00,
			0x64, 0x00, 0x00, 0x01,
		}
		asduBytes = append(asduBytes, trailing...)

		iframe := []byte{startFrame, byte(4 + len(asduBytes)), 0x00, 0x00, 0x00, 0x00}
		iframe = append(iframe, asduBytes...)
		if _, err := conn.Write(iframe); err != nil {
			t.Fatal(err)
		}

		select {
		case cmd := <-got:
			if len(trailing) == 0 {
				t.Logf("no trailing octets: executed, IOA=%d value=%v (the control)", cmd.Ioa, cmd.Value)
			} else {
				t.Errorf("%d trailing octets [% x]: EXECUTED as valid — IOA=%d value=%v",
					len(trailing), trailing, cmd.Ioa, cmd.Value)
			}
			_ = cmd
		case <-time.After(2 * time.Second):
			if len(trailing) == 0 {
				t.Error("the well formed command was not executed; the test proves nothing")
			} else {
				t.Logf("%d trailing octets: not executed", len(trailing))
			}
		}
		conn.Close()
	}
}

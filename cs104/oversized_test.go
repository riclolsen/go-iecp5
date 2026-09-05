package cs104

import (
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
)

// An ASDU too large for an APDU used to be accepted by Send, queued, and then
// discarded by the frame builder — with Send having already reported success,
// so the caller had no way to learn its data never left. MarshalBinary now
// refuses it, which is early enough to tell the caller.

type countingHandler struct {
	nullHandler
	mu   sync.Mutex
	seen []asdu.TypeID
}

func (h *countingHandler) ASDUHandler(_ asdu.Connect, a *asdu.ASDU) error {
	h.mu.Lock()
	h.seen = append(h.seen, a.Type)
	h.mu.Unlock()
	return nil
}

func (h *countingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.seen)
}

type nopClientHandler struct{}

func (nopClientHandler) InterrogationHandler(asdu.Connect, *asdu.ASDU) error         { return nil }
func (nopClientHandler) CounterInterrogationHandler(asdu.Connect, *asdu.ASDU) error  { return nil }
func (nopClientHandler) ReadHandler(asdu.Connect, *asdu.ASDU) error                  { return nil }
func (nopClientHandler) TestCommandHandler(asdu.Connect, *asdu.ASDU) error           { return nil }
func (nopClientHandler) ClockSyncHandler(asdu.Connect, *asdu.ASDU) error             { return nil }
func (nopClientHandler) ResetProcessHandler(asdu.Connect, *asdu.ASDU) error          { return nil }
func (nopClientHandler) DelayAcquisitionHandler(asdu.Connect, *asdu.ASDU) error      { return nil }
func (nopClientHandler) ASDUHandler(asdu.Connect, *asdu.ASDU, *Server, int) error    { return nil }
func (nopClientHandler) ASDUHandlerAll(asdu.Connect, *asdu.ASDU, *Server, int) error { return nil }

func TestSendRefusesAnASDUThatCannotFitAnAPDU(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	h := &countingHandler{}
	srv := NewServer(h)
	srv.LogMode(false)
	go func() { _ = srv.ListenAndServer(addr) }()
	defer srv.Close()
	time.Sleep(200 * time.Millisecond)

	opt := NewOption()
	if err := opt.AddRemoteServer(addr); err != nil {
		t.Fatal(err)
	}
	cli := NewClient(nopClientHandler{}, opt)
	cli.LogMode(false)
	activated := make(chan struct{}, 1)
	cli.SetOnConnectHandler(func(c *Client) { c.SendStartDt() })
	cli.SetOnActivatedHandler(func(c *Client) {
		select {
		case activated <- struct{}{}:
		default:
		}
	})
	if err := cli.Start(); err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	select {
	case <-activated:
	case <-time.After(10 * time.Second):
		t.Fatal("data transfer never became active")
	}

	// M_SP_NA_1, not a sequence: every object is IOA(3) + SIQ(1). The payload
	// always matches the object count, so the only thing under test is size.
	build := func(objects int) *asdu.ASDU {
		a := asdu.NewASDU(cli.Params(), asdu.Identifier{
			Type:       asdu.M_SP_NA_1,
			Variable:   asdu.VariableStruct{Number: byte(objects)},
			Coa:        asdu.CauseOfTransmission{Cause: asdu.Spontaneous},
			CommonAddr: 1,
		})
		for i := 0; i < objects; i++ {
			a.InfoObj = append(a.InfoObj, byte(i+1), 0x00, 0x00, 0x01)
		}
		return a
	}

	// 60 objects: identifier(6) + 240 = 246 octets, inside the maximum.
	before := h.count()
	if err := cli.Send(build(60)); err != nil {
		t.Fatalf("a well formed 246 octet ASDU was refused: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && h.count() == before {
		time.Sleep(50 * time.Millisecond)
	}
	if h.count() == before {
		t.Fatal("the well formed ASDU never arrived; the test proves nothing")
	}

	// 127 objects, the most the qualifier can express: 6 + 508 = 514 octets.
	// Structurally consistent, simply too large to be carried.
	before = h.count()
	err = cli.Send(build(127))
	if err == nil {
		t.Error("Send accepted an ASDU that cannot fit an APDU")
	} else if !errors.Is(err, asdu.ErrLengthOutOfRange) {
		t.Errorf("Send error = %v, want ErrLengthOutOfRange", err)
	}

	time.Sleep(700 * time.Millisecond)
	if h.count() != before {
		t.Error("the oversized ASDU arrived after all")
	}
}

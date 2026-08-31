package filetransfer

import (
	"bytes"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/cs104"
)

// The tests below run a real transfer over a TCP cs104 connection, so the
// state machines are driven by the asynchronous, flow-controlled transport
// and run on separate goroutines — unlike the synchronous harness in
// filetransfer_test.go.

// outstationHandler implements cs104.ServerHandlerInterface and delegates
// file transfer ASDUs to a Sender.
type outstationHandler struct {
	sender *Sender
	mu     sync.Mutex
	errs   []error
}

func (h *outstationHandler) ASDUHandler(c asdu.Connect, a *asdu.ASDU) error {
	if handled, err := h.sender.Handle(c, a); handled {
		if err != nil {
			h.mu.Lock()
			h.errs = append(h.errs, err)
			h.mu.Unlock()
		}
		return nil // handled; never mirror UnknownTypeID back
	}
	return nil
}

func (h *outstationHandler) InterrogationHandler(asdu.Connect, *asdu.ASDU, asdu.QualifierOfInterrogation) error {
	return nil
}
func (h *outstationHandler) CounterInterrogationHandler(asdu.Connect, *asdu.ASDU, asdu.QualifierCountCall) error {
	return nil
}
func (h *outstationHandler) ReadHandler(asdu.Connect, *asdu.ASDU, asdu.InfoObjAddr) error { return nil }
func (h *outstationHandler) ClockSyncHandler(asdu.Connect, *asdu.ASDU, time.Time) error   { return nil }
func (h *outstationHandler) ResetProcessHandler(asdu.Connect, *asdu.ASDU, asdu.QualifierOfResetProcessCmd) error {
	return nil
}
func (h *outstationHandler) DelayAcquisitionHandler(asdu.Connect, *asdu.ASDU, uint16) error {
	return nil
}
func (h *outstationHandler) ASDUHandlerAll(asdu.Connect, *asdu.ASDU, int) error { return nil }

// masterHandler implements cs104.ClientHandlerInterface and delegates file
// transfer ASDUs to a Receiver.
type masterHandler struct {
	receiver *Receiver
	mu       sync.Mutex
	errs     []error
}

func (h *masterHandler) ASDUHandler(c asdu.Connect, a *asdu.ASDU, _ *cs104.Server, _ int) error {
	if handled, err := h.receiver.Handle(c, a); handled && err != nil {
		h.mu.Lock()
		h.errs = append(h.errs, err)
		h.mu.Unlock()
	}
	return nil
}

func (h *masterHandler) InterrogationHandler(asdu.Connect, *asdu.ASDU) error        { return nil }
func (h *masterHandler) CounterInterrogationHandler(asdu.Connect, *asdu.ASDU) error { return nil }
func (h *masterHandler) ReadHandler(asdu.Connect, *asdu.ASDU) error                 { return nil }
func (h *masterHandler) TestCommandHandler(asdu.Connect, *asdu.ASDU) error          { return nil }
func (h *masterHandler) ClockSyncHandler(asdu.Connect, *asdu.ASDU) error            { return nil }
func (h *masterHandler) ResetProcessHandler(asdu.Connect, *asdu.ASDU) error         { return nil }
func (h *masterHandler) DelayAcquisitionHandler(asdu.Connect, *asdu.ASDU) error     { return nil }
func (h *masterHandler) ASDUHandlerAll(asdu.Connect, *asdu.ASDU, *cs104.Server, int) error {
	return nil
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// startPair brings up a cs104 outstation serving store and an activated
// cs104 master, both wired to the file transfer components.
func startPair(t *testing.T, sender *Sender, receiver *Receiver) (*cs104.Client, func()) {
	t.Helper()
	addr := freeAddr(t)

	srvH := &outstationHandler{sender: sender}
	srv := cs104.NewServer(srvH)
	srv.LogMode(false)
	go func() { _ = srv.ListenAndServer(addr) }()
	time.Sleep(200 * time.Millisecond)

	cliH := &masterHandler{receiver: receiver}
	opt := cs104.NewOption()
	if err := opt.AddRemoteServer(addr); err != nil {
		t.Fatalf("AddRemoteServer: %v", err)
	}
	opt.SetAutoReconnect(false)
	cli := cs104.NewClient(cliH, opt)
	cli.LogMode(false)

	active := make(chan struct{})
	var once sync.Once
	cli.SetOnConnectHandler(func(c *cs104.Client) { c.SendStartDt() })
	cli.SetOnActivatedHandler(func(*cs104.Client) { once.Do(func() { close(active) }) })
	if err := cli.Start(); err != nil {
		t.Fatalf("client start: %v", err)
	}

	select {
	case <-active:
	case <-time.After(10 * time.Second):
		t.Fatal("cs104 data transfer never became active")
	}

	return cli, func() {
		_ = cli.Close()
		_ = srv.Close()
	}
}

// TestTransferOverCS104 fetches a multi-section file across a real TCP
// cs104 connection.
func TestTransferOverCS104(t *testing.T) {
	const ca, ioa = asdu.CommonAddr(1), asdu.InfoObjAddr(100)
	want := testData(5000)

	srvStore := NewMemStore()
	if err := srvStore.Write(ioa, asdu.FileDisturbanceData, want); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	sender := NewSender(srvStore).SetSectionSize(1024) // 5 sections

	done := make(chan []byte, 1)
	receiver := NewReceiver(nil)
	receiver.SetFileHandler(func(_ Entry, b []byte) { done <- b })

	cli, stop := startPair(t, sender, receiver)
	defer stop()

	if err := receiver.RequestFile(cli, ca, ioa, asdu.FileDisturbanceData); err != nil {
		t.Fatalf("RequestFile: %v", err)
	}

	select {
	case got := <-done:
		if !bytes.Equal(got, want) {
			t.Fatalf("received %d octets, want %d (content mismatch)", len(got), len(want))
		}
	case <-time.After(20 * time.Second):
		t.Fatal("file never completed over cs104")
	}

	// Both machines must be idle afterwards.
	deadline := time.After(3 * time.Second)
	for sender.InProgress() || receiver.InProgress() {
		select {
		case <-deadline:
			t.Fatalf("transfer still in progress (sender=%v receiver=%v)",
				sender.InProgress(), receiver.InProgress())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestDirectoryOverCS104 calls the outstation directory and then fetches
// the announced file over a real cs104 connection.
func TestDirectoryOverCS104(t *testing.T) {
	const ca, ioa = asdu.CommonAddr(1), asdu.InfoObjAddr(7)
	want := testData(300)

	srvStore := NewMemStore()
	_ = srvStore.Write(ioa, asdu.FileDisturbanceData, want)
	sender := NewSender(srvStore)

	dirCh := make(chan []asdu.DirectoryInfo, 1)
	fileCh := make(chan []byte, 1)
	receiver := NewReceiver(nil)
	receiver.SetDirectoryHandler(func(_ asdu.CommonAddr, d []asdu.DirectoryInfo) { dirCh <- d })
	receiver.SetFileHandler(func(_ Entry, b []byte) { fileCh <- b })

	cli, stop := startPair(t, sender, receiver)
	defer stop()

	if err := receiver.RequestDirectory(cli, ca); err != nil {
		t.Fatalf("RequestDirectory: %v", err)
	}

	var dir []asdu.DirectoryInfo
	select {
	case dir = <-dirCh:
	case <-time.After(10 * time.Second):
		t.Fatal("directory never arrived over cs104")
	}
	if len(dir) != 1 || dir[0].Ioa != ioa || dir[0].LengthOfFile != uint32(len(want)) {
		t.Fatalf("directory = %+v", dir)
	}

	// Fetch the file the directory advertised.
	if err := receiver.RequestFile(cli, ca, dir[0].Ioa, dir[0].Nof); err != nil {
		t.Fatalf("RequestFile: %v", err)
	}
	select {
	case got := <-fileCh:
		if !bytes.Equal(got, want) {
			t.Fatal("content mismatch after directory-driven fetch")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("file never completed over cs104")
	}
}

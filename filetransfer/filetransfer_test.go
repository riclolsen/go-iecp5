package filetransfer

import (
	"bytes"
	"fmt"
	"net"
	"testing"

	"github.com/riclolsen/go-iecp5/asdu"
)

// endpoint is a synchronous asdu.Connect for testing: every ASDU is
// round-tripped through MarshalBinary/UnmarshalBinary (so the wire codecs
// and the variable-length segment decode are exercised) and handed to the
// peer's handler, which replies over the peer endpoint.
type endpoint struct {
	params  *asdu.Params
	peer    *endpoint
	handler func(asdu.Connect, *asdu.ASDU) error

	sent    map[asdu.TypeID]int
	corrupt func(a *asdu.ASDU, raw []byte) // optional wire fault injection
}

func newPair(params *asdu.Params) (master, outstation *endpoint) {
	master = &endpoint{params: params, sent: map[asdu.TypeID]int{}}
	outstation = &endpoint{params: params, sent: map[asdu.TypeID]int{}}
	master.peer, outstation.peer = outstation, master
	return
}

func (e *endpoint) Params() *asdu.Params     { return e.params }
func (e *endpoint) UnderlyingConn() net.Conn { return nil }

func (e *endpoint) Send(a *asdu.ASDU) error {
	raw, err := a.MarshalBinary()
	if err != nil {
		return fmt.Errorf("marshal %v: %w", a.Type, err)
	}
	if e.corrupt != nil {
		e.corrupt(a, raw)
	}
	got := asdu.NewEmptyASDU(e.params)
	if err := got.UnmarshalBinary(raw); err != nil {
		return fmt.Errorf("unmarshal %v: %w", a.Type, err)
	}
	e.sent[a.Type]++
	if e.peer.handler == nil {
		return nil
	}
	return e.peer.handler(e.peer, got)
}

// wire builds a master/outstation pair driving the given receiver and sender.
func wire(params *asdu.Params, r *Receiver, s *Sender) (master, outstation *endpoint) {
	master, outstation = newPair(params)
	master.handler = func(c asdu.Connect, a *asdu.ASDU) error {
		_, err := r.Handle(c, a)
		return err
	}
	outstation.handler = func(c asdu.Connect, a *asdu.ASDU) error {
		_, err := s.Handle(c, a)
		return err
	}
	return
}

func testData(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + 13)
	}
	return b
}

// TestTransferRoundTrip runs a complete monitor-direction transfer of a
// multi-section, multi-segment file between a Sender and a Receiver.
func TestTransferRoundTrip(t *testing.T) {
	const ca = asdu.CommonAddr(1)
	const ioa = asdu.InfoObjAddr(100)
	want := testData(1000)

	srvStore := NewMemStore()
	if err := srvStore.Write(ioa, asdu.FileDisturbanceData, want); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	sender := NewSender(srvStore).SetSectionSize(400) // -> 3 sections

	cliStore := NewMemStore()
	receiver := NewReceiver(cliStore)
	var gotEntry Entry
	var gotData []byte
	calls := 0
	receiver.SetFileHandler(func(e Entry, b []byte) {
		gotEntry, gotData = e, b
		calls++
	})

	master, outstation := wire(asdu.ParamsWide, receiver, sender)

	if err := receiver.RequestFile(master, ca, ioa, asdu.FileDisturbanceData); err != nil {
		t.Fatalf("RequestFile: %v", err)
	}

	// The test transport is synchronous, so the transfer has completed.
	if calls != 1 {
		t.Fatalf("file handler called %d times, want 1", calls)
	}
	if !bytes.Equal(gotData, want) {
		t.Fatalf("received %d octets, want %d (content mismatch: %v)",
			len(gotData), len(want), !bytes.Equal(gotData, want))
	}
	if gotEntry.Ioa != ioa || gotEntry.Nof != asdu.FileDisturbanceData || gotEntry.Size != uint32(len(want)) {
		t.Fatalf("entry = %+v", gotEntry)
	}

	// It must also have landed in the receiving store.
	stored, err := cliStore.Read(ioa, asdu.FileDisturbanceData)
	if err != nil {
		t.Fatalf("store read: %v", err)
	}
	if !bytes.Equal(stored, want) {
		t.Fatal("stored content differs from the original")
	}

	// Both state machines must be idle again.
	if sender.InProgress() || receiver.InProgress() {
		t.Fatalf("transfer still in progress (sender=%v receiver=%v)",
			sender.InProgress(), receiver.InProgress())
	}

	// Check the message counts match the standard's procedure:
	// 3 sections -> 3 section-ready, 3 section acks + 1 file ack.
	maxSeg := asdu.ParamsWide.MaxSegmentSize()
	wantSegments := 0
	for _, s := range [][]byte{want[:400], want[400:800], want[800:]} {
		wantSegments += (len(s) + maxSeg - 1) / maxSeg
	}
	if got := outstation.sent[asdu.F_SG_NA_1]; got != wantSegments {
		t.Fatalf("sent %d segments, want %d", got, wantSegments)
	}
	if got := outstation.sent[asdu.F_SR_NA_1]; got != 3 {
		t.Fatalf("sent %d section-ready, want 3", got)
	}
	if got := outstation.sent[asdu.F_LS_NA_1]; got != 4 { // 3 sections + 1 file
		t.Fatalf("sent %d last-section, want 4", got)
	}
	if got := master.sent[asdu.F_AF_NA_1]; got != 4 { // 3 section acks + 1 file ack
		t.Fatalf("sent %d acks, want 4", got)
	}
}

// TestTransferSingleSection covers the common small-file case: one section,
// one segment.
func TestTransferSingleSection(t *testing.T) {
	const ca, ioa = asdu.CommonAddr(1), asdu.InfoObjAddr(7)
	want := testData(32)

	srvStore := NewMemStore()
	_ = srvStore.Write(ioa, asdu.FileTransparent, want)
	sender := NewSender(srvStore)

	receiver := NewReceiver(nil) // no store: callback only
	var got []byte
	receiver.SetFileHandler(func(_ Entry, b []byte) { got = b })

	master, outstation := wire(asdu.ParamsWide, receiver, sender)
	if err := receiver.RequestFile(master, ca, ioa, asdu.FileTransparent); err != nil {
		t.Fatalf("RequestFile: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %d octets, want %d", len(got), len(want))
	}
	if n := outstation.sent[asdu.F_SG_NA_1]; n != 1 {
		t.Fatalf("sent %d segments, want 1", n)
	}
}

// TestTransferEmptyFile checks the degenerate zero-length file.
func TestTransferEmptyFile(t *testing.T) {
	const ca, ioa = asdu.CommonAddr(1), asdu.InfoObjAddr(9)
	srvStore := NewMemStore()
	_ = srvStore.Write(ioa, asdu.FileTransparent, nil)
	sender := NewSender(srvStore)

	receiver := NewReceiver(nil)
	delivered := false
	receiver.SetFileHandler(func(_ Entry, b []byte) {
		delivered = true
		if len(b) != 0 {
			t.Errorf("got %d octets, want 0", len(b))
		}
	})

	master, _ := wire(asdu.ParamsWide, receiver, sender)
	if err := receiver.RequestFile(master, ca, ioa, asdu.FileTransparent); err != nil {
		t.Fatalf("RequestFile: %v", err)
	}
	if !delivered {
		t.Fatal("empty file was never delivered")
	}
}

// TestSectionChecksumRetry corrupts one segment on the wire; the receiver
// must reject the section and the sender must serve it again.
func TestSectionChecksumRetry(t *testing.T) {
	const ca, ioa = asdu.CommonAddr(1), asdu.InfoObjAddr(100)
	want := testData(600)

	srvStore := NewMemStore()
	_ = srvStore.Write(ioa, asdu.FileDisturbanceData, want)
	sender := NewSender(srvStore).SetSectionSize(300) // 2 sections

	receiver := NewReceiver(nil)
	var got []byte
	receiver.SetFileHandler(func(_ Entry, b []byte) { got = b })

	master, outstation := wire(asdu.ParamsWide, receiver, sender)

	// Flip a payload bit in the very first segment, once.
	corrupted := false
	outstation.corrupt = func(a *asdu.ASDU, raw []byte) {
		if a.Type == asdu.F_SG_NA_1 && !corrupted {
			corrupted = true
			raw[len(raw)-1] ^= 0xff
		}
	}

	// Handle returns the checksum error for the rejected section; the
	// transfer continues regardless, so ignore the error here.
	_ = receiver.RequestFile(master, ca, ioa, asdu.FileDisturbanceData)

	if !corrupted {
		t.Fatal("fault injection never fired")
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file not recovered after checksum failure: got %d octets, want %d", len(got), len(want))
	}
	if n := master.sent[asdu.F_AF_NA_1]; n != 4 { // neg ack + 2 section acks + file ack
		t.Fatalf("sent %d acks, want 4 (1 negative + 2 section + 1 file)", n)
	}
}

// TestDirectoryAndOffer covers the directory call and the outstation
// announcing a file, which the receiver accepts automatically.
func TestDirectoryAndOffer(t *testing.T) {
	const ca, ioa = asdu.CommonAddr(1), asdu.InfoObjAddr(42)
	want := testData(100)

	srvStore := NewMemStore()
	_ = srvStore.Write(ioa, asdu.FileDisturbanceData, want)
	_ = srvStore.Write(ioa+1, asdu.FileSequencesOfEvents, testData(10))
	sender := NewSender(srvStore)

	receiver := NewReceiver(nil)
	var dir []asdu.DirectoryInfo
	receiver.SetDirectoryHandler(func(_ asdu.CommonAddr, d []asdu.DirectoryInfo) { dir = d })
	var got []byte
	receiver.SetFileHandler(func(_ Entry, b []byte) { got = b })

	master, _ := wire(asdu.ParamsWide, receiver, sender)

	if err := receiver.RequestDirectory(master, ca); err != nil {
		t.Fatalf("RequestDirectory: %v", err)
	}
	if len(dir) != 2 {
		t.Fatalf("directory has %d entries, want 2", len(dir))
	}
	if dir[0].Ioa != ioa || dir[0].Nof != asdu.FileDisturbanceData || dir[0].LengthOfFile != uint32(len(want)) {
		t.Fatalf("directory entry 0 = %+v", dir[0])
	}
	if dir[0].Sof.IsLastFileOfDirectory {
		t.Error("entry 0 must not be flagged as last of directory")
	}
	if !dir[1].Sof.IsLastFileOfDirectory {
		t.Error("entry 1 must be flagged as last of directory")
	}

	// The outstation offers a file; auto-accept pulls it across.
	outstationConn := master.peer
	if err := sender.Offer(outstationConn, ca, ioa, asdu.FileDisturbanceData); err != nil {
		t.Fatalf("Offer: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("offered file not received: got %d octets, want %d", len(got), len(want))
	}
}

// TestSelectUnknownFile checks the negative acknowledge path.
func TestSelectUnknownFile(t *testing.T) {
	sender := NewSender(NewMemStore())
	receiver := NewReceiver(nil)
	master, outstation := wire(asdu.ParamsWide, receiver, sender)

	// The receiver's Handle rejects the F_AF_NA_1 it gets back (that ASDU
	// belongs to the control direction), so an error is expected here.
	_ = receiver.RequestFile(master, 1, 5, asdu.FileTransparent)

	if n := outstation.sent[asdu.F_AF_NA_1]; n != 1 {
		t.Fatalf("outstation sent %d negative acks, want 1", n)
	}
	if sender.InProgress() {
		t.Fatal("sender must not hold a transfer for an unknown file")
	}
}

// TestMemStore covers the in-memory store contract.
func TestMemStore(t *testing.T) {
	s := NewMemStore()
	if _, err := s.Read(1, asdu.FileTransparent); err != ErrFileNotFound {
		t.Fatalf("Read of missing file = %v, want ErrFileNotFound", err)
	}
	if err := s.Delete(1, asdu.FileTransparent); err != ErrFileNotFound {
		t.Fatalf("Delete of missing file = %v, want ErrFileNotFound", err)
	}

	data := testData(8)
	if err := s.Write(1, asdu.FileTransparent, data); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := s.Read(1, asdu.FileTransparent)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("Read = %v, %v", got, err)
	}
	// The store must hold a copy, not alias the caller's slice.
	data[0] ^= 0xff
	if got2, _ := s.Read(1, asdu.FileTransparent); bytes.Equal(got2, data) {
		t.Fatal("store aliases the caller's buffer")
	}

	list, err := s.List()
	if err != nil || len(list) != 1 || list[0].Size != 8 {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if err := s.Delete(1, asdu.FileTransparent); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// TestSplitSections covers the section splitter, including the empty file.
func TestSplitSections(t *testing.T) {
	if got := splitSections(nil, 100); len(got) != 1 || len(got[0]) != 0 {
		t.Fatalf("empty file split into %d sections", len(got))
	}
	if got := splitSections(testData(250), 100); len(got) != 3 ||
		len(got[0]) != 100 || len(got[1]) != 100 || len(got[2]) != 50 {
		t.Fatalf("250/100 split = %d sections with wrong sizes", len(got))
	}
	if got := splitSections(testData(100), 100); len(got) != 1 {
		t.Fatalf("exact fit split into %d sections, want 1", len(got))
	}
}

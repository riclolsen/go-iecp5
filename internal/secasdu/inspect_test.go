package secasdu

import (
	"sync"
	"testing"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/secevent"
)

type host struct {
	mu sync.Mutex
	n  map[secevent.Kind]int
}

func (h *host) Emit(e secevent.Event) {
	h.mu.Lock()
	if h.n == nil {
		h.n = map[secevent.Kind]int{}
	}
	h.n[e.Kind]++
	h.mu.Unlock()
}
func (h *host) Enabled() bool                     { return true }
func (h *host) NegativeConfirm(*asdu.ASDU)        {}
func (h *host) ReplyCause(*asdu.ASDU, asdu.Cause) {}
func (h *host) Disconnect(string)                 {}

type noObjects struct{}

func (noObjects) KnownCA(asdu.CommonAddr) bool                                 { return true }
func (noObjects) KnownIOA(asdu.CommonAddr, asdu.TypeID, asdu.InfoObjAddr) bool { return false }

func read(ioa asdu.InfoObjAddr) *asdu.ASDU {
	a := asdu.NewASDU(asdu.ParamsWide, asdu.Identifier{Type: asdu.C_RD_NA_1,
		Variable: asdu.VariableStruct{Number: 1}, Coa: asdu.CauseOfTransmission{Cause: asdu.Request}, CommonAddr: 1})
	_ = a.AppendInfoObjAddr(ioa)
	return a
}

// Replies are observed on whatever goroutine sends them, while requests are
// inspected on the handler goroutine. Run with -race.
func TestInspectAndObserveConcurrently(t *testing.T) {
	h := &host{}
	pol := secevent.DefaultPolicy()
	pol.AddressSpace = noObjects{}
	in := New(h, nil, pol)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 500 {
			in.Inspect(read(asdu.InfoObjAddr(i)), nil)
		}
	}()
	go func() {
		defer wg.Done()
		for i := range 500 {
			r := read(asdu.InfoObjAddr(10000 + i))
			r.Coa.Cause = asdu.UnknownIOA
			in.ObserveOutbound(r)
			n := read(1)
			n.Type, n.Coa.Cause, n.Coa.IsNegative = asdu.C_SC_NA_1, asdu.ActivationCon, true
			in.ObserveOutbound(n)
		}
	}()
	wg.Wait()
	if h.n[secevent.KindAccessScan] == 0 {
		t.Error("no scan detected across 1000 unknown objects")
	}
}

// Without a Guard the inspector must still use the default thresholds: the
// first unknown object is not a scan.
func TestInspectorAppliesDefaults(t *testing.T) {
	h := &host{}
	pol := secevent.Policy{AddressSpace: noObjects{}}
	in := New(h, nil, pol)
	in.Inspect(read(1), nil)
	if h.n[secevent.KindAccessScan] != 0 {
		t.Fatal("a single unknown object was reported as a scan")
	}
	if p := in.Policy(); p.ScanThreshold == 0 || p.Limits.Commands.N == 0 || p.ClockStepThreshold == 0 {
		t.Fatalf("defaults not applied: %+v", p.Limits)
	}
}

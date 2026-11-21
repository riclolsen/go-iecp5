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
	in := New(h, nil, pol, CS104)
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
	in := New(h, nil, pol, CS104)
	in.Inspect(read(1), nil)
	if h.n[secevent.KindAccessScan] != 0 {
		t.Fatal("a single unknown object was reported as a scan")
	}
	if p := in.Policy(); p.ScanThreshold == 0 || p.Limits.Commands.N == 0 || p.ClockStepThreshold == 0 {
		t.Fatalf("defaults not applied: %+v", p.Limits)
	}
}

// offHost has no sink attached, and records what the inspector refused.
type offHost struct {
	host
	refused int
}

func (h *offHost) Enabled() bool                     { return false }
func (h *offHost) NegativeConfirm(*asdu.ASDU)        { h.refused++ }
func (h *offHost) ReplyCause(*asdu.ASDU, asdu.Cause) { h.refused++ }

func command() *asdu.ASDU {
	a := asdu.NewASDU(asdu.ParamsWide, asdu.Identifier{Type: asdu.C_SC_NA_1,
		Variable: asdu.VariableStruct{Number: 1}, Coa: asdu.CauseOfTransmission{Cause: asdu.Activation}, CommonAddr: 1})
	_ = a.AppendInfoObjAddr(1)
	a.InfoObj = append(a.InfoObj, 0x01)
	return a
}

// The request policy is enforced whether or not anyone collects events.
func TestPolicyEnforcedWithoutSink(t *testing.T) {
	h := &offHost{}
	pol := secevent.DefaultPolicy()
	pol.Functions = secevent.FunctionPolicy{ReadOnly: true}
	in := New(h, nil, pol, CS104)
	if !in.Inspect(command(), nil) {
		t.Fatal("a read-only policy let a process command through without a sink")
	}
	if h.refused != 1 {
		t.Fatalf("refusal sent %d times, want 1", h.refused)
	}
	if len(h.n) != 0 {
		t.Fatalf("events emitted with the sink off: %v", h.n)
	}

	pol = secevent.DefaultPolicy()
	pol.AddressSpace = noObjects{}
	pol.AutoRejectUnknown = true
	in = New(h, nil, pol, CS104)
	if !in.Inspect(read(7), nil) {
		t.Fatal("an unknown object was not refused without a sink")
	}
}

// Malformed requests are reported, never allowed to crash the session.
func TestAuditSurvivesTruncatedObjects(t *testing.T) {
	for _, typ := range []asdu.TypeID{asdu.C_IC_NA_1, asdu.C_CI_NA_1, asdu.C_RP_NA_1, asdu.C_CS_NA_1} {
		for _, n := range []byte{0, 2} {
			h := &host{}
			in := New(h, nil, secevent.DefaultPolicy(), CS104)
			a := asdu.NewASDU(asdu.ParamsWide, asdu.Identifier{Type: typ,
				Variable: asdu.VariableStruct{Number: n}, Coa: asdu.CauseOfTransmission{Cause: asdu.Activation}, CommonAddr: 1})
			if n > 0 {
				_ = a.AppendInfoObjAddr(0) // address only, no element
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s with %d objects panicked: %v", typ, n, r)
					}
				}()
				in.Inspect(a, nil)
			}()
		}
	}
}

// IEC 60870-5-101 types outside the 104 profile are normal on a 101 link.
func TestProfile104OnlyOn104(t *testing.T) {
	cd := func() *asdu.ASDU {
		a := asdu.NewASDU(asdu.ParamsWide, asdu.Identifier{Type: asdu.C_CD_NA_1,
			Variable: asdu.VariableStruct{Number: 1}, Coa: asdu.CauseOfTransmission{Cause: asdu.Activation}, CommonAddr: 1})
		_ = a.AppendInfoObjAddr(0)
		a.InfoObj = append(a.InfoObj, 0, 0)
		return a
	}
	h101, h104 := &host{}, &host{}
	New(h101, nil, secevent.DefaultPolicy(), CS101).Inspect(cd(), nil)
	New(h104, nil, secevent.DefaultPolicy(), CS104).Inspect(cd(), nil)
	if h101.n[secevent.KindProtoNotInProfile] != 0 {
		t.Error("a 101 delay acquisition was reported as outside the 104 profile")
	}
	if h104.n[secevent.KindProtoNotInProfile] != 1 {
		t.Error("a 104 delay acquisition was not reported as outside the profile")
	}
}

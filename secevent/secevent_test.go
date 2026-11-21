package secevent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/netpeer"
)

type collector struct {
	mu sync.Mutex
	ev []Event
}

func (c *collector) Handle(e Event) { c.mu.Lock(); c.ev = append(c.ev, e); c.mu.Unlock() }
func (c *collector) all() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Event(nil), c.ev...)
}
func (c *collector) wait(t *testing.T, n int) []Event {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if e := c.all(); len(e) >= n {
			return e
		}
	}
	t.Fatalf("got %d events, want %d: %v", len(c.all()), n, c.all())
	return nil
}

func TestKindTableComplete(t *testing.T) {
	for _, k := range Kinds() {
		i, ok := Info(k)
		if !ok || i.Category == "" {
			t.Errorf("%s has no category", k)
		}
		if !strings.HasPrefix(string(k), strings.Split(string(k), ".")[0]+".") {
			t.Errorf("%s is not dotted", k)
		}
	}
	if len(Kinds()) < 100 {
		t.Errorf("only %d kinds defined", len(Kinds()))
	}
}

func TestEmitterFillsFromTable(t *testing.T) {
	c := &collector{}
	var em Emitter
	em.Protocol, em.Role = "iec104", "server"
	if em.Enabled() {
		t.Fatal("enabled without a sink")
	}
	em.Emit(Event{Kind: KindSeqNsGap}) // must be a no-op, not a panic
	em.SetSink(c)
	defer em.Close()
	raw := bytes.Repeat([]byte{0xAB}, 200)
	em.Emit(Event{Kind: KindSeqNsGap, Raw: raw})
	e := c.wait(t, 1)[0]
	if e.Category != CatSequence || e.Severity != SevAlert || e.Technique == "" ||
		e.Protocol != "iec104" || e.Role != "server" || e.Action != ActObserved || e.Time.IsZero() {
		t.Errorf("not completed: %+v", e)
	}
	if len(e.Raw) != 64 {
		t.Errorf("raw len %d, want truncation to 64", len(e.Raw))
	}
	raw[0] = 0
	if e.Raw[0] != 0xAB {
		t.Error("Raw aliases the caller's buffer")
	}
}

func TestAggregationAndAlways(t *testing.T) {
	c := &collector{}
	d := NewDispatcher(c, Options{AggregateWindow: 200 * time.Millisecond})
	for i := 0; i < 50; i++ {
		d.Emit(Event{Kind: KindProtoBadAPCI, Category: CatProtocol, SessionID: 1})
		d.Emit(Event{Kind: KindSeqNsGap, Category: CatSequence, SessionID: 1})
	}
	// A different session is a different key.
	d.Emit(Event{Kind: KindProtoBadAPCI, Category: CatProtocol, SessionID: 2})
	d.Close()
	var bad1, gaps, bad2, repeats int
	for _, e := range c.all() {
		switch {
		case e.Kind == KindSeqNsGap:
			gaps++ // always-deliver: none folded
		case e.Kind == KindProtoBadAPCI && e.SessionID == 1:
			bad1++
			if e.Count > 1 {
				repeats += e.Count
			}
		case e.Kind == KindProtoBadAPCI && e.SessionID == 2:
			bad2++
		}
	}
	if gaps != 50 {
		t.Errorf("always-deliver kind delivered %d of 50", gaps)
	}
	if bad1 != 2 || repeats != 49 || bad2 != 1 {
		t.Errorf("bad1=%d (want first + one aggregate) repeats=%d (want 49) bad2=%d", bad1, repeats, bad2)
	}
	if s := d.Stats(); s.Aggregated != 49 || s.ByKind[KindSeqNsGap] != 50 {
		t.Errorf("stats %+v", s)
	}
}

func TestSlowSinkNeverBlocksAndDropsAreReported(t *testing.T) {
	release := make(chan struct{})
	c := &collector{}
	d := NewDispatcher(SinkFunc(func(e Event) {
		<-release
		c.Handle(e)
	}), Options{QueueSize: 4, AggregateWindow: -1})
	start := time.Now()
	for i := 0; i < 1000; i++ {
		d.Emit(Event{Kind: KindConnAccepted, Category: CatConnection})
	}
	if time.Since(start) > time.Second {
		t.Fatal("Emit blocked on a slow sink")
	}
	close(release)
	d.Close()
	st := d.Stats()
	if st.Dropped < 900 {
		t.Errorf("dropped %d, want most of 1000", st.Dropped)
	}
	var reported int
	for _, e := range c.all() {
		if e.Kind == KindMetaEventsDropped {
			reported += e.Count
		}
	}
	if uint64(reported) != st.Dropped {
		t.Errorf("reported %d drops, counted %d", reported, st.Dropped)
	}
}

func TestSinkPanicIsContained(t *testing.T) {
	var n int
	d := NewDispatcher(SinkFunc(func(Event) { n++; panic("boom") }), Options{AggregateWindow: -1})
	d.Emit(Event{Kind: KindConnClosed})
	d.Emit(Event{Kind: KindConnClosed})
	d.Close() // must return
	if n < 2 {
		t.Errorf("sink called %d times", n)
	}
}

func TestJSONShape(t *testing.T) {
	e := Event{
		Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Kind: KindAccessUnknownIOA,
		Category: CatAccess, Severity: SevWarning, Action: ActRejected, Protocol: "iec104",
		Fields: FieldType | FieldCommonAddr | FieldIOA, TypeID: asdu.C_RD_NA_1, CommonAddr: 1, IOA: 0,
		Raw: []byte{0x68, 0x04},
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{"kind": "access.unknown_ioa", "severity": "warning", "category": "access",
		"action": "rejected", "type_id": float64(102), "common_addr": float64(1), "ioa": float64(0), "raw": "6804"} {
		if m[k] != want {
			t.Errorf("%s = %v, want %v", k, m[k], want)
		}
	}
	if _, ok := m["link_addr"]; ok {
		t.Error("link_addr present though its field flag is unset")
	}
}

func TestFilterAndMultiSink(t *testing.T) {
	a, b := &collector{}, &collector{}
	s := MultiSink(a, FilterSink(SevAlert, b, CatSequence))
	s.Handle(Event{Severity: SevInfo, Category: CatSequence})
	s.Handle(Event{Severity: SevAlert, Category: CatAccess})
	s.Handle(Event{Severity: SevAlert, Category: CatSequence})
	if len(a.all()) != 3 || len(b.all()) != 1 {
		t.Errorf("a=%d b=%d", len(a.all()), len(b.all()))
	}
}

// Concurrent connections must not slip past MaxSessions while one of them is
// still being identified.
func TestAdmitHonoursMaxSessionsUnderConcurrency(t *testing.T) {
	p := DefaultPolicy()
	p.MaxSessions = 1
	p.AcceptRate, p.GlobalAcceptRate = Rate{}, Rate{1000, time.Second}
	p.Resolver = netpeer.ResolverFunc(func(context.Context, netip.Addr) (netpeer.Result, error) {
		time.Sleep(50 * time.Millisecond)
		return netpeer.Result{}, netpeer.ErrNotFound
	})
	g := NewGuard(p, nil)
	var wg sync.WaitGroup
	var admitted atomic.Int32
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			peer := PeerInfo{Network: "tcp", RemoteAddr: netip.AddrPortFrom(netip.MustParseAddr("192.0.2.1"), uint16(1000+i))}
			if _, ok := g.Admit(peer, uint64(i)); ok {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := admitted.Load(); n != 1 {
		t.Fatalf("%d sessions admitted with MaxSessions=1", n)
	}
	if n := g.Sessions(); n != 1 {
		t.Fatalf("%d slots held, want 1", n)
	}
}

// Every event Emit accepts is delivered, even when Close races with it.
func TestEmitRacingCloseLosesNothing(t *testing.T) {
	for range 50 {
		var got atomic.Uint64
		d := NewDispatcher(SinkFunc(func(Event) { got.Add(1) }), Options{QueueSize: 4096, AggregateWindow: -1})
		var accepted atomic.Uint64
		var wg sync.WaitGroup
		for g := range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range 200 {
					if d.Emit(Event{Kind: KindConnClosed, SessionID: uint64(g*1000 + i)}) {
						accepted.Add(1)
					}
				}
			}()
		}
		d.Close()
		wg.Wait()
		if got.Load() != accepted.Load() {
			t.Fatalf("%d events accepted, %d delivered", accepted.Load(), got.Load())
		}
	}
}

// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package secevent

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Options tune a Dispatcher. Zero values take the defaults.
type Options struct {
	// QueueSize bounds the events waiting for the sink (default 1024).
	QueueSize int
	// AggregateWindow merges repeats of the same event from one session into
	// one delivery with a Count (default 1s; negative disables).
	AggregateWindow time.Duration
	// AggregateKeys bounds the memory of the aggregator (default 4096).
	AggregateKeys int
	// MaxRaw truncates Event.Raw (default 64).
	MaxRaw int
}

func (o *Options) defaults() {
	if o.QueueSize <= 0 {
		o.QueueSize = 1024
	}
	if o.AggregateWindow == 0 {
		o.AggregateWindow = time.Second
	}
	if o.AggregateKeys <= 0 {
		o.AggregateKeys = 4096
	}
	if o.MaxRaw <= 0 {
		o.MaxRaw = 64
	}
}

// Stats is a snapshot of the counters of a Dispatcher.
type Stats struct {
	Emitted    uint64 // events offered, before aggregation and drops
	Delivered  uint64 // events handed to the sink
	Dropped    uint64 // events lost because the queue was full
	Aggregated uint64 // repeats folded into an earlier delivery
	ByKind     map[Kind]uint64
	ByCategory map[Category]uint64
}

type aggKey struct {
	session uint64
	kind    Kind
	ca      uint16
	ioa     uint32
	typ     uint8
	link    uint16
}

type aggEntry struct {
	start      time.Time
	suppressed int
	last       Event
}

// Dispatcher queues events and delivers them to a Sink on its own goroutine.
type Dispatcher struct {
	sink Sink
	opt  Options
	ch   chan Event
	done chan struct{}
	wg   sync.WaitGroup

	closeOnce sync.Once
	closed    atomic.Bool

	emitted, delivered, dropped, aggregated atomic.Uint64
	pendingDrop                             atomic.Uint64
	panicked                                atomic.Bool

	mu         sync.Mutex
	byKind     map[Kind]uint64
	byCategory map[Category]uint64

	agg map[aggKey]*aggEntry // owned by the delivery goroutine
}

// NewDispatcher starts a Dispatcher delivering to sink. Call Close to flush.
func NewDispatcher(sink Sink, opt Options) *Dispatcher {
	opt.defaults()
	d := &Dispatcher{
		sink: sink, opt: opt,
		ch:         make(chan Event, opt.QueueSize),
		done:       make(chan struct{}),
		byKind:     make(map[Kind]uint64),
		byCategory: make(map[Category]uint64),
		agg:        make(map[aggKey]*aggEntry),
	}
	d.wg.Add(1)
	go d.run()
	return d
}

// MaxRaw is the truncation applied to Event.Raw.
func (d *Dispatcher) MaxRaw() int { return d.opt.MaxRaw }

// Emit queues e without blocking. It reports false when the event was
// dropped because the queue is full or the Dispatcher is closed.
func (d *Dispatcher) Emit(e Event) bool {
	if d.closed.Load() {
		return false
	}
	d.emitted.Add(1)
	d.mu.Lock()
	d.byKind[e.Kind]++
	d.byCategory[e.Category]++
	d.mu.Unlock()
	select {
	case d.ch <- e:
		return true
	default:
		d.dropped.Add(1)
		d.pendingDrop.Add(1)
		return false
	}
}

// Stats returns the counters.
func (d *Dispatcher) Stats() Stats {
	s := Stats{
		Emitted: d.emitted.Load(), Delivered: d.delivered.Load(),
		Dropped: d.dropped.Load(), Aggregated: d.aggregated.Load(),
		ByKind: map[Kind]uint64{}, ByCategory: map[Category]uint64{},
	}
	d.mu.Lock()
	for k, v := range d.byKind {
		s.ByKind[k] = v
	}
	for k, v := range d.byCategory {
		s.ByCategory[k] = v
	}
	d.mu.Unlock()
	return s
}

// Close stops accepting events, delivers what is queued and aggregated, and
// waits for the sink goroutine to finish.
func (d *Dispatcher) Close() {
	d.closeOnce.Do(func() {
		d.closed.Store(true)
		close(d.done)
	})
	d.wg.Wait()
}

func (d *Dispatcher) run() {
	defer d.wg.Done()
	var tick <-chan time.Time
	if d.opt.AggregateWindow > 0 {
		t := time.NewTicker(d.opt.AggregateWindow / 2)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case e := <-d.ch:
			d.handle(e)
		case now := <-tick:
			d.flush(now, false)
		case <-d.done:
			for { // drain what was queued before Close
				select {
				case e := <-d.ch:
					d.handle(e)
				default:
					d.flush(time.Now(), true)
					return
				}
			}
		}
	}
}

func (d *Dispatcher) handle(e Event) {
	d.reportDrops()
	if e.Count == 0 {
		e.Count = 1
	}
	info, known := kinds[e.Kind]
	if d.opt.AggregateWindow < 0 || (known && info.Always) {
		d.deliver(e)
		return
	}
	k := aggKey{e.SessionID, e.Kind, uint16(e.CommonAddr), uint32(e.IOA), uint8(e.TypeID), e.LinkAddr}
	now := e.Time
	if now.IsZero() {
		now = time.Now()
	}
	if a, ok := d.agg[k]; ok && now.Sub(a.start) < d.opt.AggregateWindow {
		a.suppressed++
		a.last = e
		d.aggregated.Add(1)
		return
	}
	if len(d.agg) >= d.opt.AggregateKeys {
		d.flush(now, true) // bounded memory: settle everything rather than grow
	}
	d.agg[k] = &aggEntry{start: now}
	d.deliver(e)
}

// flush delivers the repeats whose window has ended (all of them when force).
func (d *Dispatcher) flush(now time.Time, force bool) {
	for k, a := range d.agg {
		if !force && now.Sub(a.start) < d.opt.AggregateWindow {
			continue
		}
		if a.suppressed > 0 {
			e := a.last
			e.Count = a.suppressed
			e.FirstSeen = a.start
			d.deliver(e)
		}
		delete(d.agg, k)
	}
	d.reportDrops()
}

func (d *Dispatcher) reportDrops() {
	if n := d.pendingDrop.Swap(0); n > 0 {
		info := kinds[KindMetaEventsDropped]
		d.deliver(Event{
			Time: time.Now(), Kind: KindMetaEventsDropped, Category: info.Category,
			Severity: info.Severity, Action: ActObserved, Count: int(n),
			Detail: fmt.Sprintf("%d event(s) were dropped because the sink could not keep up", n),
		})
	}
}

func (d *Dispatcher) deliver(e Event) {
	defer func() {
		if r := recover(); r != nil {
			d.dropped.Add(1)
			if d.panicked.CompareAndSwap(false, true) {
				// Reported once: a sink that panics will panic again.
				info := kinds[KindMetaSinkPanic]
				func() {
					defer func() { _ = recover() }()
					d.sink.Handle(Event{Time: time.Now(), Kind: KindMetaSinkPanic, Category: info.Category,
						Severity: info.Severity, Detail: fmt.Sprintf("sink panicked: %v", r), Count: 1})
				}()
			}
		}
	}()
	d.delivered.Add(1)
	d.sink.Handle(e)
}

// Emitter is what an endpoint embeds. Until a sink is set it costs one
// atomic load per Enabled check.
type Emitter struct {
	d        atomic.Pointer[Dispatcher]
	owned    atomic.Bool
	Protocol string
	Role     string
	Endpoint int
}

// SetSink starts a Dispatcher for sink with default options, replacing and
// closing any this Emitter created before. A nil sink turns events off.
func (em *Emitter) SetSink(s Sink) { em.SetSinkOptions(s, Options{}) }

// SetSinkOptions is SetSink with options.
func (em *Emitter) SetSinkOptions(s Sink, opt Options) {
	var nd *Dispatcher
	if s != nil {
		nd = NewDispatcher(s, opt)
	}
	old := em.d.Swap(nd)
	if old != nil && em.owned.Load() {
		old.Close()
	}
	em.owned.Store(nd != nil)
}

// SetDispatcher shares an existing Dispatcher between endpoints. The Emitter
// does not close it.
func (em *Emitter) SetDispatcher(d *Dispatcher) {
	old := em.d.Swap(d)
	if old != nil && em.owned.Load() {
		old.Close()
	}
	em.owned.Store(false)
}

// Close flushes and stops a Dispatcher this Emitter created.
func (em *Emitter) Close() {
	old := em.d.Swap(nil)
	if old != nil && em.owned.Load() {
		old.Close()
	}
	em.owned.Store(false)
}

// Enabled reports whether events are being collected. Callers check it before
// building an Event, so that an endpoint with no sink does no work.
func (em *Emitter) Enabled() bool { return em.d.Load() != nil }

// Stats returns the counters of the current Dispatcher.
func (em *Emitter) Stats() Stats {
	if d := em.d.Load(); d != nil {
		return d.Stats()
	}
	return Stats{}
}

// Emit completes e from the kind table and the endpoint's identity and
// queues it. Fields the caller set are kept.
func (em *Emitter) Emit(e Event) {
	d := em.d.Load()
	if d == nil {
		return
	}
	if info, ok := kinds[e.Kind]; ok {
		if e.Category == "" {
			e.Category = info.Category
		}
		if e.Severity == 0 && info.Severity != 0 {
			e.Severity = info.Severity
		}
		if e.Technique == "" {
			e.Technique = info.Technique
		}
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	if e.Action == "" {
		e.Action = ActObserved
	}
	if e.Protocol == "" {
		e.Protocol = em.Protocol
	}
	if e.Role == "" {
		e.Role = em.Role
	}
	if e.Endpoint == 0 {
		e.Endpoint = em.Endpoint
	}
	if len(e.Raw) > 0 {
		n := len(e.Raw)
		if m := d.MaxRaw(); n > m {
			n = m
		}
		e.Raw = append([]byte(nil), e.Raw[:n]...)
	}
	d.Emit(e)
}

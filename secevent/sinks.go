// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package secevent

import (
	"encoding/json"
	"io"
	"sync/atomic"

	"github.com/riclolsen/go-iecp5/clog"
)

// ChannelSink sends events to ch without blocking. Events that do not fit
// are counted by Dropped.
type ChannelSink struct {
	C       chan<- Event
	dropped atomic.Uint64
}

// Handle implements Sink.
func (s *ChannelSink) Handle(e Event) {
	select {
	case s.C <- e:
	default:
		s.dropped.Add(1)
	}
}

// Dropped is how many events did not fit in the channel.
func (s *ChannelSink) Dropped() uint64 { return s.dropped.Load() }

// ClogSink writes events through a clog.LogProvider, prefixed "[CYBER]", at
// the level that matches their severity.
func ClogSink(p clog.LogProvider) Sink {
	return SinkFunc(func(e Event) {
		switch {
		case e.Severity >= SevCritical:
			p.Critical("[CYBER] %s", e)
		case e.Severity >= SevAlert:
			p.Error("[CYBER] %s", e)
		case e.Severity >= SevWarning:
			p.Warn("[CYBER] %s", e)
		default:
			p.Debug("[CYBER] %s", e)
		}
	})
}

// JSONLinesSink writes one JSON object per line to w. Writes are serialised
// by the Dispatcher, so w needs no locking of its own.
func JSONLinesSink(w io.Writer) Sink {
	enc := json.NewEncoder(w)
	return SinkFunc(func(e Event) { _ = enc.Encode(e) })
}

// MultiSink delivers each event to every sink in turn.
func MultiSink(sinks ...Sink) Sink {
	return SinkFunc(func(e Event) {
		for _, s := range sinks {
			s.Handle(e)
		}
	})
}

// FilterSink passes on events at or above min whose category is in cats (all
// categories when none are given).
func FilterSink(min Severity, s Sink, cats ...Category) Sink {
	set := make(map[Category]bool, len(cats))
	for _, c := range cats {
		set[c] = true
	}
	return SinkFunc(func(e Event) {
		if e.Severity < min || (len(set) > 0 && !set[e.Category]) {
			return
		}
		s.Handle(e)
	})
}

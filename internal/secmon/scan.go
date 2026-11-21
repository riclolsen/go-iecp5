// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package secmon

import "time"

// Scan counts the distinct things one peer has asked for within a window, to
// notice enumeration: many different unknown addresses in a short time is
// someone looking for what exists. Like Bucket it is not safe for concurrent
// use. Its memory is bounded.
type Scan struct {
	window    time.Duration
	threshold int
	seen      map[uint64]time.Time
	fired     time.Time
}

// NewScan fires once threshold distinct keys are seen within window.
func NewScan(threshold int, window time.Duration) *Scan {
	return &Scan{window: window, threshold: threshold, seen: map[uint64]time.Time{}}
}

// Note records key and reports true when the threshold has just been
// crossed. It fires at most once per window, so a long scan raises one event
// per window rather than one per probe.
func (s *Scan) Note(key uint64, now time.Time) bool {
	if s == nil {
		return false
	}
	if len(s.seen) >= 4*s.threshold {
		for k, t := range s.seen {
			if now.Sub(t) > s.window {
				delete(s.seen, k)
			}
		}
		if len(s.seen) >= 4*s.threshold { // all fresh: keep the newest half
			for k := range s.seen {
				delete(s.seen, k)
				if len(s.seen) <= 2*s.threshold {
					break
				}
			}
		}
	}
	s.seen[key] = now
	n := 0
	for _, t := range s.seen {
		if now.Sub(t) <= s.window {
			n++
		}
	}
	if n >= s.threshold && now.Sub(s.fired) >= s.window {
		s.fired = now
		return true
	}
	return false
}

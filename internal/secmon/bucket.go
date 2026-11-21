// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

// Package secmon holds the detectors shared by the protocol endpoints.
package secmon

import "time"

// Bucket is a token bucket. It is not safe for concurrent use: each belongs
// to one goroutine, such as a session's protocol loop, or is guarded by its
// owner.
type Bucket struct {
	rate   float64 // tokens per second
	burst  float64
	tokens float64
	last   time.Time
}

// NewBucket allows n events per period, with bursts of up to burst events
// (n when burst <= 0). A zero n or period gives a bucket that never limits.
func NewBucket(n int, per time.Duration, burst int) *Bucket {
	if n <= 0 || per <= 0 {
		return nil
	}
	if burst <= 0 {
		burst = n
	}
	return &Bucket{rate: float64(n) / per.Seconds(), burst: float64(burst), tokens: float64(burst)}
}

// Allow takes one token at time now, reporting whether one was available.
// A nil Bucket allows everything.
func (b *Bucket) Allow(now time.Time) bool {
	if b == nil {
		return true
	}
	if !b.last.IsZero() {
		if dt := now.Sub(b.last).Seconds(); dt > 0 {
			b.tokens += dt * b.rate
			if b.tokens > b.burst {
				b.tokens = b.burst
			}
		}
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// Idle reports whether the bucket has been unused for at least d, so its
// owner can discard it.
func (b *Bucket) Idle(now time.Time, d time.Duration) bool {
	return b == nil || now.Sub(b.last) >= d
}

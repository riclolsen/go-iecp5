package secmon

import (
	"testing"
	"time"
)

func TestBucketAllowsBurstThenRefills(t *testing.T) {
	b := NewBucket(5, 10*time.Second, 0) // 0.5/s, burst 5
	t0 := time.Unix(1000, 0)
	for i := 0; i < 5; i++ {
		if !b.Allow(t0) {
			t.Fatalf("burst event %d refused", i)
		}
	}
	if b.Allow(t0) {
		t.Fatal("sixth event in the burst allowed")
	}
	if b.Allow(t0.Add(time.Second)) {
		t.Fatal("refilled too early (0.5 token after 1s)")
	}
	if !b.Allow(t0.Add(3 * time.Second)) {
		t.Fatal("not refilled after 3s")
	}
	// Time going backwards must not mint tokens.
	for i := 0; i < 10; i++ {
		b.Allow(t0.Add(-time.Hour))
	}
	if b.Allow(t0.Add(-time.Hour)) {
		t.Fatal("tokens from nowhere")
	}
}

func TestNilBucketNeverLimits(t *testing.T) {
	var b *Bucket
	if !b.Allow(time.Now()) || !NewBucket(0, time.Second, 0).Allow(time.Now()) || !NewBucket(-1, time.Second, 0).Allow(time.Now()) {
		t.Error("an unconfigured bucket limited")
	}
}

func TestScanFiresOncePerWindowAndStaysBounded(t *testing.T) {
	s := NewScan(10, time.Minute)
	t0 := time.Unix(1000, 0)
	fired := 0
	for i := 0; i < 9; i++ {
		if s.Note(uint64(i), t0) {
			t.Fatal("fired below the threshold")
		}
	}
	for i := 9; i < 60; i++ {
		if s.Note(uint64(i), t0.Add(time.Duration(i)*time.Millisecond)) {
			fired++
		}
	}
	if fired != 1 {
		t.Errorf("fired %d times within one window, want 1", fired)
	}
	// A huge scan does not grow the detector.
	for i := 0; i < 100000; i++ {
		s.Note(uint64(1000+i), t0.Add(time.Second))
	}
	if len(s.seen) > 4*10+1 {
		t.Errorf("detector holds %d keys", len(s.seen))
	}
	// Repeats of one key are not a scan.
	r := NewScan(10, time.Minute)
	for i := 0; i < 1000; i++ {
		if r.Note(7, t0) {
			t.Fatal("the same object, asked 1000 times, is not a scan")
		}
	}
}

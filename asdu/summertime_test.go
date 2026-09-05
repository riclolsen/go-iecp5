package asdu

import (
	"testing"
	"time"
)

// Bit 7 of the hour octet of CP56Time2a is SU: the reading is expressed in
// summer time. IEC 60870-5-4 lays the octet out as
//
//	| SU(D7) | RES2(D6-D5) | Hours(D4-D0) |
//
// Omitting it makes every summer timestamp look like standard time to a peer
// that honours the flag — an hour of skew, twice a year, in the direction
// that makes an event look like it happened before its cause.

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("no tzdata for %s: %v", name, err)
	}
	return loc
}

func TestCP56Time2aSummerTimeBit(t *testing.T) {
	berlin := mustLoad(t, "Europe/Berlin")

	for _, tc := range []struct {
		name   string
		moment time.Time
		loc    *time.Location
		wantSU bool
	}{
		{"winter in Berlin", time.Date(2026, 1, 15, 14, 30, 0, 0, berlin), berlin, false},
		{"summer in Berlin", time.Date(2026, 7, 15, 14, 30, 0, 0, berlin), berlin, true},
		{"summer expressed in UTC", time.Date(2026, 7, 15, 14, 30, 0, 0, time.UTC), time.UTC, false},
		{"fixed zone never observes summer time",
			time.Date(2026, 7, 15, 14, 30, 0, 0, time.FixedZone("X", 3600)),
			time.FixedZone("X", 3600), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := CP56Time2a(tc.moment, tc.loc)
			if got := b[3]&0x80 != 0; got != tc.wantSU {
				t.Errorf("SU = %v, want %v (hour octet 0x%02X)", got, tc.wantSU, b[3])
			}
			// The hour itself must still be readable in D4-D0.
			if got, want := int(b[3]&0x1f), tc.moment.In(tc.loc).Hour(); got != want {
				t.Errorf("hour = %d, want %d", got, want)
			}
		})
	}
}

func TestCP32Time2aStyleRoundTrip(t *testing.T) {
	berlin := mustLoad(t, "Europe/Berlin")
	for _, moment := range []time.Time{
		time.Date(2026, 1, 15, 14, 30, 15, 250e6, berlin),
		time.Date(2026, 7, 15, 14, 30, 15, 250e6, berlin),
	} {
		b := CP56Time2a(moment, berlin)
		back := ParseCP56Time2a(b, berlin)
		if !back.Equal(moment) {
			t.Errorf("round trip of %s gave %s", moment.Format(time.RFC3339), back.Format(time.RFC3339))
		}
	}
}

// A peer that honours SU must read back the instant we meant, not the wall
// clock reinterpreted as standard time.
func TestConformingPeerReadsOurSummerTimestamp(t *testing.T) {
	berlin := mustLoad(t, "Europe/Berlin")
	moment := time.Date(2026, 7, 15, 14, 30, 0, 0, berlin) // 12:30 UTC
	b := CP56Time2a(moment, berlin)

	// The peer takes the wall clock and the SU flag, and knows the zone's
	// standard and summer offsets.
	offset := 1 * 3600
	if b[3]&0x80 != 0 {
		offset = 2 * 3600
	}
	asPeerReadsIt := time.Date(2000+int(b[6]&0x7f), time.Month(b[5]&0x0f), int(b[4]&0x1f),
		int(b[3]&0x1f), int(b[2]&0x3f), 0, 0, time.FixedZone("peer", offset))

	if skew := asPeerReadsIt.Sub(moment); skew != 0 {
		t.Errorf("a peer honouring SU misreads the timestamp by %v", skew)
	}
}

// SU is what settles the hour that happens twice when the clocks go back.
// Without it the two readings are indistinguishable and time.Date resolves
// the ambiguity arbitrarily.
func TestSummerTimeResolvesTheAmbiguousHour(t *testing.T) {
	berlin := mustLoad(t, "Europe/Berlin")

	// 2026-10-25 02:30 local occurs twice in Berlin: first as CEST (00:30
	// UTC), then as CET (01:30 UTC).
	summer := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC).In(berlin)
	standard := time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC).In(berlin)
	if summer.Hour() != standard.Hour() || summer.Equal(standard) {
		t.Skipf("this zone does not put 02:30 twice on that date: %s / %s", summer, standard)
	}

	for _, tc := range []struct {
		name   string
		moment time.Time
	}{
		{"the summer time reading", summer},
		{"the standard time reading", standard},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := CP56Time2a(tc.moment, berlin)
			back := ParseCP56Time2a(b, berlin)
			if !back.Equal(tc.moment) {
				t.Errorf("round trip gave %s (%s UTC), want %s (%s UTC)",
					back.Format(time.RFC3339), back.UTC().Format("15:04"),
					tc.moment.Format(time.RFC3339), tc.moment.UTC().Format("15:04"))
			}
		})
	}
}

// ResolveSummerTime must leave a time alone when nothing with the same wall
// clock matches the flag — a sender whose summer time rules differ from ours.
func TestResolveSummerTimeKeepsTheWallClockWhenItCannotAgree(t *testing.T) {
	berlin := mustLoad(t, "Europe/Berlin")
	// Mid-January: there is no instant reading 14:30 that is in summer time.
	winter := time.Date(2026, 1, 15, 14, 30, 0, 0, berlin)
	got := ResolveSummerTime(winter, true)
	if !got.Equal(winter) {
		t.Errorf("got %s, want the input unchanged (%s)", got, winter)
	}
}

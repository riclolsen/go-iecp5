package asdu

import (
	"testing"
	"time"
)

// Every field of a CP-series time tag is carried in more bits than its range
// needs: milliseconds run to 59999 in sixteen bits, minutes to 59 in six,
// hours to 23 in five, months to 12 in four, years to 99 in seven.
//
// A value outside the range is a fault in the sender, not a time. Handed to
// time.Date it does not fail — it is normalised into a neighbouring instant
// that reads as perfectly ordinary: month 0 becomes December of the previous
// year, hour 31 becomes 07:30 the next day, 31 April becomes 1 May. An event
// log cannot show any of those as suspect, which is what makes silently
// normalising worse than refusing.

// buildCP56 assembles the seven octets from raw field values, so a test can
// put in values the standard does not define.
func buildCP56(msecRaw, min, hour, day, month, yearOffset int) []byte {
	return []byte{
		byte(msecRaw), byte(msecRaw >> 8),
		byte(min), byte(hour),
		byte(day), byte(month), byte(yearOffset),
	}
}

func TestParseCP56Time2aRejectsOutOfRangeFields(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  []byte
	}{
		{"milliseconds 60000, the first undefined value", buildCP56(60000, 30, 14, 15, 7, 26)},
		{"milliseconds 65535", buildCP56(65535, 30, 14, 15, 7, 26)},
		{"minute 60", buildCP56(500, 60, 14, 15, 7, 26)},
		{"minute 63, the widest the field holds", buildCP56(500, 63, 14, 15, 7, 26)},
		{"hour 24", buildCP56(500, 30, 24, 15, 7, 26)},
		{"hour 31, the widest the field holds", buildCP56(500, 30, 31, 15, 7, 26)},
		{"day 0", buildCP56(500, 30, 14, 0, 7, 26)},
		{"month 0", buildCP56(500, 30, 14, 15, 0, 26)},
		{"month 13", buildCP56(500, 30, 14, 15, 13, 26)},
		{"month 15, the widest the field holds", buildCP56(500, 30, 14, 15, 15, 26)},
		{"year offset 100", buildCP56(500, 30, 14, 15, 7, 100)},
		{"year offset 127, the widest the field holds", buildCP56(500, 30, 14, 15, 7, 127)},
		{"31 April, a day that month does not have", buildCP56(500, 30, 14, 31, 4, 26)},
		{"30 February", buildCP56(500, 30, 14, 30, 2, 26)},
		{"29 February in a common year", buildCP56(500, 30, 14, 29, 2, 27)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseCP56Time2a(tt.raw, time.UTC); !got.IsZero() {
				t.Errorf("accepted as %s; a malformed tag must decode to the zero time",
					got.Format(time.RFC3339Nano))
			}
		})
	}
}

// The boundaries themselves must still be accepted, or the check is a bug of
// its own.
func TestParseCP56Time2aAcceptsTheBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  []byte
		want time.Time
	}{
		{"first millisecond of a minute", buildCP56(0, 0, 0, 1, 1, 0),
			time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"last millisecond of a minute", buildCP56(59999, 59, 23, 31, 12, 99),
			time.Date(2099, 12, 31, 23, 59, 59, 999e6, time.UTC)},
		{"29 February in a leap year", buildCP56(500, 30, 14, 29, 2, 24),
			time.Date(2024, 2, 29, 14, 30, 0, 500e6, time.UTC)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseCP56Time2a(tt.raw, time.UTC)
			if !got.Equal(tt.want) {
				t.Errorf("got %s, want %s", got.Format(time.RFC3339Nano), tt.want.Format(time.RFC3339Nano))
			}
		})
	}
}

func TestParseCP24Time2aRejectsOutOfRangeFields(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  []byte
	}{
		{"milliseconds 60000", []byte{0x60, 0xEA, 30}},
		{"milliseconds 65535", []byte{0xFF, 0xFF, 30}},
		{"minute 60", []byte{0xF4, 0x01, 60}},
		{"minute 63", []byte{0xF4, 0x01, 63}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseCP24Time2a(tt.raw, time.UTC); !got.IsZero() {
				t.Errorf("accepted as %s", got.Format(time.RFC3339Nano))
			}
		})
	}

	// The boundary is still valid.
	if got := ParseCP24Time2a([]byte{0x5F, 0xEA, 59}, time.UTC); got.IsZero() {
		t.Error("59:59.999 was rejected")
	}
}

// Everything the encoder produces must survive its own decoder.
func TestCP56RoundTripAcrossAYear(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 400; i++ {
		moment := base.AddDate(0, 0, i).Add(
			time.Duration(i)*time.Hour + time.Duration(i)*time.Minute +
				time.Duration(i*37)*time.Millisecond)
		b := CP56Time2a(moment, time.UTC)
		got := ParseCP56Time2a(b, time.UTC)
		if got.IsZero() {
			t.Fatalf("%s encoded to % x and was then rejected", moment.Format(time.RFC3339Nano), b)
		}
		if !got.Equal(moment) {
			t.Fatalf("round trip of %s gave %s", moment.Format(time.RFC3339Nano), got.Format(time.RFC3339Nano))
		}
	}
}

package cs103

import (
	"testing"
	"time"
)

// CP32Time2a's fourth octet is | SU(D7) | RES(D6-D5) | Hours(D4-D0) |, the
// same layout as CP56Time2a's. The doc comment on the encoder always said
// "hours with summer-time flag"; the flag is now actually set.

func TestCP32Time2aSummerTimeBit(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}

	for _, tc := range []struct {
		name   string
		moment time.Time
		loc    *time.Location
		wantSU bool
	}{
		{"winter in Berlin", time.Date(2026, 1, 15, 14, 30, 0, 0, berlin), berlin, false},
		{"summer in Berlin", time.Date(2026, 7, 15, 14, 30, 0, 0, berlin), berlin, true},
		{"UTC", time.Date(2026, 7, 15, 14, 30, 0, 0, time.UTC), time.UTC, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := CP32Time2a(tc.moment, tc.loc)
			if len(b) != 4 {
				t.Fatalf("CP32Time2a produced %d octets, want 4", len(b))
			}
			if got := b[3]&0x80 != 0; got != tc.wantSU {
				t.Errorf("SU = %v, want %v (hour octet 0x%02X)", got, tc.wantSU, b[3])
			}
			if got, want := int(b[3]&0x1f), tc.moment.In(tc.loc).Hour(); got != want {
				t.Errorf("hour = %d, want %d", got, want)
			}
		})
	}
}

// The decoder must not read the SU bit as part of the hour.
func TestParseCP32Time2aIgnoresSUInTheHourValue(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	// A summer reading close to now, so the "previous day" rule does not
	// move it: build it from the current date in Berlin.
	now := time.Now().In(berlin)
	moment := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 30, 0, 0, berlin)

	b := CP32Time2a(moment, berlin)
	got := ParseCP32Time2a(b, berlin)
	if got.Hour() != moment.Hour() || got.Minute() != 30 {
		t.Errorf("decoded %s, want the hour and minute of %s", got.Format(time.RFC3339),
			moment.Format(time.RFC3339))
	}
}

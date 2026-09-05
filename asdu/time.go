// Copyright 2020 thinkgos (thinkgo@aliyun.com).  All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package asdu

import (
	"encoding/binary"
	"time"
)

// CP56Time2a , CP24Time2a, CP16Time2a
// |         Milliseconds(D7--D0)        | Milliseconds = 0-59999
// |         Milliseconds(D15--D8)       |
// | IV(D7)   RES1(D6)  Minutes(D5--D0)  | Minutes = 1-59, IV = invalid,0 = valid, 1 = invalid
// | SU(D7)   RES2(D6-D5)  Hours(D4--D0) | Hours = 0-23, SU = summer Time,0 = standard time, 1 = summer time,
// | DayOfWeek(D7--D5) DayOfMonth(D4--D0)| DayOfMonth = 1-31  DayOfWeek = 1-7
// | RES3(D7--D4)        Months(D3--D0)  | Months = 1-12
// | RES4(D7)            Year(D6--D0)    | Year = 0-99

// CP56Time2a time to CP56Time2a
func CP56Time2a(t time.Time, loc *time.Location) []byte {
	if loc == nil {
		loc = time.UTC
	}
	ts := t.In(loc)
	msec := ts.Nanosecond()/int(time.Millisecond) + ts.Second()*1000
	// IEC 60870-5-4 day of week: 1 = Monday .. 7 = Sunday (0 = not used).
	// Go's Weekday has Sunday = 0, so map it to 7.
	dow := byte(ts.Weekday())
	if dow == 0 {
		dow = 7
	}
	return []byte{byte(msec), byte(msec >> 8), byte(ts.Minute()), summerTimeHour(ts),
		dow<<5 | byte(ts.Day()), byte(ts.Month()), byte(ts.Year() - 2000)}
}

// summerTimeHour builds the hour octet: the hour in D4-D0 and the SU flag in
// D7. SU says the reading is expressed in summer time, which is what lets a
// receiver resolve the hour that occurs twice when the clocks go back. It is
// zero for UTC and for any fixed zone, because neither observes summer time.
func summerTimeHour(ts time.Time) byte {
	h := byte(ts.Hour())
	if ts.IsDST() {
		h |= 0x80
	}
	return h
}

// ResolveSummerTime picks the instant matching a CP56Time2a or CP32Time2a SU
// (summer time) flag.
//
// A wall clock reading is ambiguous for one hour a year: when the clocks go
// back the same local time occurs twice, once in summer time and once in
// standard time. time.Date resolves that arbitrarily — the standard provides
// the SU bit to settle it.
//
// It returns t unchanged when t already agrees with su, and when no instant
// with the same wall clock reading agrees. The latter happens when the
// sender's summer time rules differ from loc's, and there the wall clock is
// the only thing the two ends agree on.
func ResolveSummerTime(t time.Time, su bool) time.Time {
	if t.IsDST() == su {
		return t
	}
	y, mo, d := t.Date()
	h, mi, s := t.Clock()
	// A summer time offset is an hour almost everywhere and half an hour in
	// a few places; the shifted instant is only the right one if it still
	// reads as the same wall clock.
	for _, delta := range []time.Duration{
		-time.Hour, time.Hour,
		-30 * time.Minute, 30 * time.Minute,
		-2 * time.Hour, 2 * time.Hour,
	} {
		alt := t.Add(delta)
		if alt.IsDST() != su {
			continue
		}
		ay, amo, ad := alt.Date()
		ah, ami, as := alt.Clock()
		if ay == y && amo == mo && ad == d && ah == h && ami == mi && as == s {
			return alt
		}
	}
	return t
}

// ParseCP56Time2a 7 octets binary time, it is recommended to use UTC for all time stamps, read 7 bytes, return time
// The year is assumed to be in the 20th century.
// See IEC 60870-5-4 § 6.8 and IEC 60870-5-101 second edition § 7.2.6.18.
func ParseCP56Time2a(bytes []byte, loc *time.Location) time.Time {
	if len(bytes) < 7 || bytes[2]&0x80 == 0x80 {
		return time.Time{}
	}

	x := int(binary.LittleEndian.Uint16(bytes))
	msec := x % 1000
	sec := x / 1000
	min := int(bytes[2] & 0x3f)
	hour := int(bytes[3] & 0x1f)
	day := int(bytes[4] & 0x1f)
	month := time.Month(bytes[5] & 0x0f)
	year := 2000 + int(bytes[6]&0x7f)

	nsec := msec * int(time.Millisecond)
	if loc == nil {
		loc = time.UTC
	}
	return ResolveSummerTime(
		time.Date(year, month, day, hour, min, sec, nsec, loc),
		bytes[3]&0x80 != 0)
}

// CP24Time2a time to CP56Time2a 3 octets binary time, UTC is recommended for all time scales
// See companion standard 101, subclass 7.2.6.19.
func CP24Time2a(t time.Time, loc *time.Location) []byte {
	if loc == nil {
		loc = time.UTC
	}
	ts := t.In(loc)
	msec := ts.Nanosecond()/int(time.Millisecond) + ts.Second()*1000
	return []byte{byte(msec), byte(msec >> 8), byte(ts.Minute())}
}

// ParseCP24Time2a 3 octets binary time, it is recommended that all time scales use UTC, read 3 bytes, and return a time
// See companion standard 101, subclass 7.2.6.19.
func ParseCP24Time2a(bytes []byte, loc *time.Location) time.Time {
	if len(bytes) < 3 || bytes[2]&0x80 == 0x80 {
		return time.Time{}
	}
	x := int(binary.LittleEndian.Uint16(bytes))
	msec := x % 1000
	sec := (x / 1000)
	min := int(bytes[2] & 0x3f)

	if loc == nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	year, month, day := now.Date()
	hour, currentMin, _ := now.Clock()

	nsec := msec * int(time.Millisecond)
	val := time.Date(year, month, day, hour, min, sec, nsec, loc)

	// CP24Time2a only carries minutes and milliseconds; the date and hour are
	// taken from the local clock. If the encoded minute is well ahead of the
	// current minute, the tag belongs to the previous hour (5 minute skew
	// allowance, 55 minute span).
	if min > currentMin+5 {
		val = val.Add(-time.Hour)
	}

	return val
}

// CP16Time2a msec to CP16Time2a 2 octets binary time
// See companion standard 101, subclass 7.2.6.20.
func CP16Time2a(msec uint16) []byte {
	return []byte{byte(msec), byte(msec >> 8)}
}

// ParseCP16Time2a 2 octet binary time, read 2 bytes, return a value
// See companion standard 101, subclass 7.2.6.20.
func ParseCP16Time2a(b []byte) uint16 {
	return binary.LittleEndian.Uint16(b)
}

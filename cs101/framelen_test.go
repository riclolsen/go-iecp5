package cs101

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// The FT1.2 variable-length frame is
//
//	68H L L 68H | control | link address | ASDU | checksum | 16H
//
// where L counts control + link address + ASDU. L is one octet, so it runs to
// 255 and the frame runs to L+6 = 261 octets on the wire. The largest ASDU is
// therefore 255 - 1 - linkAddrSize: 253 octets with a one octet link address,
// which is the figure IEC 60870-5-103 quotes.
//
// This used to be capped at L <= 249, which confused the length field with
// the frame length and cost six octets of every frame — enough to refuse the
// largest frames the standard defines, in both directions.

// buildVariableFrame assembles a wire-format frame by hand, so the parser is
// tested against the format rather than against MarshalBinary.
func buildVariableFrame(control byte, linkAddr, asdu []byte) []byte {
	l := byte(1 + len(linkAddr) + len(asdu))
	out := []byte{StartVariable, l, l, StartVariable, control}
	out = append(out, linkAddr...)
	out = append(out, asdu...)
	out = append(out, calculateChecksum(control, linkAddr, asdu))
	return append(out, EndChar)
}

func TestMaxASDULen(t *testing.T) {
	for _, tc := range []struct {
		linkAddrSize byte
		want         int
	}{{0, 254}, {1, 253}, {2, 252}} {
		if got := MaxASDULen(tc.linkAddrSize); got != tc.want {
			t.Errorf("MaxASDULen(%d) = %d, want %d", tc.linkAddrSize, got, tc.want)
		}
	}
	if MaxFrameLen != 261 {
		t.Errorf("MaxFrameLen = %d; the largest frame on the wire is L+6 = 261", MaxFrameLen)
	}
}

// TestParseAcceptsMaximumLengthFrame: every length field up to 255 describes
// a legal frame and must be accepted.
func TestParseAcceptsMaximumLengthFrame(t *testing.T) {
	ctx := context.Background()
	for _, linkAddrSize := range []byte{1, 2} {
		maxASDU := MaxASDULen(linkAddrSize)
		for _, asduLen := range []int{0, 1, 247, 248, maxASDU - 1, maxASDU} {
			addr := bytes.Repeat([]byte{0x01}, int(linkAddrSize))
			asdu := bytes.Repeat([]byte{0xAB}, asduLen)
			wire := buildVariableFrame(0x53, addr, asdu)

			wantL := 1 + int(linkAddrSize) + asduLen
			if got := int(wire[1]); got != wantL {
				t.Fatalf("built L=%d, want %d", got, wantL)
			}
			if len(wire) != wantL+6 {
				t.Fatalf("frame is %d octets, want L+6 = %d", len(wire), wantL+6)
			}

			f, err := ParseFrame(bytes.NewReader(wire), linkAddrSize, &ctx)
			if err != nil {
				t.Errorf("linkAddrSize=%d ASDU=%d (L=%d, %d octets): %v",
					linkAddrSize, asduLen, wantL, len(wire), err)
				continue
			}
			if len(f.ASDU) != asduLen {
				t.Errorf("linkAddrSize=%d: ASDU came back %d octets, want %d",
					linkAddrSize, len(f.ASDU), asduLen)
			}
			if !bytes.Equal(f.ASDU, asdu) {
				t.Errorf("linkAddrSize=%d ASDU=%d: contents differ", linkAddrSize, asduLen)
			}
		}
	}
}

// TestMarshalMaximumLengthFrame: the largest ASDU must go out, and one octet
// more must be refused rather than wrapped.
func TestMarshalMaximumLengthFrame(t *testing.T) {
	for _, linkAddrSize := range []byte{1, 2} {
		maxASDU := MaxASDULen(linkAddrSize)

		f := &Frame{
			Start:    StartVariable,
			Control:  0x53,
			LinkAddr: bytes.Repeat([]byte{0x01}, int(linkAddrSize)),
			ASDU:     bytes.Repeat([]byte{0xCD}, maxASDU),
		}
		out, err := f.MarshalBinary(linkAddrSize)
		if err != nil {
			t.Fatalf("linkAddrSize=%d: the largest ASDU (%d octets) was refused: %v",
				linkAddrSize, maxASDU, err)
		}
		if got := int(out[1]); got != MaxLengthField {
			t.Errorf("linkAddrSize=%d: L=%d, want %d", linkAddrSize, got, MaxLengthField)
		}
		if len(out) != MaxFrameLen {
			t.Errorf("linkAddrSize=%d: frame is %d octets, want %d", linkAddrSize, len(out), MaxFrameLen)
		}

		// It must survive a round trip.
		ctx := context.Background()
		back, err := ParseFrame(bytes.NewReader(out), linkAddrSize, &ctx)
		if err != nil {
			t.Fatalf("linkAddrSize=%d: the frame it produced will not parse: %v", linkAddrSize, err)
		}
		if !bytes.Equal(back.ASDU, f.ASDU) {
			t.Errorf("linkAddrSize=%d: ASDU did not survive the round trip", linkAddrSize)
		}
	}
}

// TestMarshalRefusesOversizedASDU: the length field is computed before it is
// narrowed to a byte. Narrowing first wrapped a 256 octet total to L=0 and
// put a frame on the wire whose length field described none of it.
func TestMarshalRefusesOversizedASDU(t *testing.T) {
	for _, linkAddrSize := range []byte{1, 2} {
		for _, over := range []int{1, 2, 47} {
			asduLen := MaxASDULen(linkAddrSize) + over
			f := &Frame{
				Start:    StartVariable,
				Control:  0x53,
				LinkAddr: bytes.Repeat([]byte{0x01}, int(linkAddrSize)),
				ASDU:     bytes.Repeat([]byte{0xEF}, asduLen),
			}
			out, err := f.MarshalBinary(linkAddrSize)
			if err == nil {
				t.Errorf("linkAddrSize=%d ASDU=%d (L would be %d): accepted, emitting %d octets with L=%d",
					linkAddrSize, asduLen, 1+int(linkAddrSize)+asduLen, len(out), out[1])
				continue
			}
			if !errors.Is(err, ErrFrameLenExceeded) {
				t.Errorf("linkAddrSize=%d ASDU=%d: wrong error: %v", linkAddrSize, asduLen, err)
			}
		}
	}
}

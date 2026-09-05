package asdu

import (
	"bytes"
	"errors"
	"testing"
)

// MarshalBinary is the last place a malformed ASDU can be stopped before it
// is on the wire, and the only place the caller can still be told which side
// is wrong. Everything below used to marshal without complaint, producing
// output this library's own decoder rejects.

func newTestASDU(typ TypeID, sq bool, n byte, infoObj []byte) *ASDU {
	a := NewASDU(ParamsWide, Identifier{
		Type:       typ,
		Variable:   VariableStruct{IsSequence: sq, Number: n},
		Coa:        CauseOfTransmission{Cause: Spontaneous},
		CommonAddr: 1,
	})
	a.InfoObj = infoObj
	return a
}

func TestMarshalBinaryRejectsMalformed(t *testing.T) {
	onePoint := []byte{0x01, 0x00, 0x00, 0x01} // IOA(3) + SIQ(1)

	for _, tt := range []struct {
		name string
		a    *ASDU
		want error
	}{
		{"type identification 0", newTestASDU(TypeID(0), false, 1, onePoint), ErrTypeIDZero},
		{"no information objects", newTestASDU(M_SP_NA_1, false, 0, nil), ErrInfoObjCountZero},
		{"one object claimed, none carried",
			newTestASDU(M_SP_NA_1, false, 1, nil), ErrInfoObjSizeMismatch},
		{"one object claimed, three carried",
			newTestASDU(M_SP_NA_1, false, 1, bytes.Repeat(onePoint, 3)), ErrInfoObjSizeMismatch},
		{"five claimed, one carried",
			newTestASDU(M_SP_NA_1, false, 5, onePoint), ErrInfoObjSizeMismatch},
		{"payload not a whole number of objects",
			newTestASDU(M_SP_NA_1, false, 1, []byte{0x01, 0x00}), ErrInfoObjSizeMismatch},
		{"larger than the ASDU maximum",
			newTestASDU(M_SP_NA_1, false, 127, bytes.Repeat(onePoint, 127)), ErrLengthOutOfRange},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := tt.a.MarshalBinary()
			if err == nil {
				t.Fatalf("marshalled %d octets", len(raw))
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

// The boundary must still marshal, and a sequence must be measured as a
// sequence: one address followed by bare elements.
func TestMarshalBinaryAcceptsTheBoundaries(t *testing.T) {
	onePoint := []byte{0x01, 0x00, 0x00, 0x01}

	// 60 objects: 6 + 240 = 246 octets, inside the 249 maximum.
	if _, err := newTestASDU(M_SP_NA_1, false, 60, bytes.Repeat(onePoint, 60)).MarshalBinary(); err != nil {
		t.Errorf("the largest ordinary ASDU was refused: %v", err)
	}
	// One object more does not fit.
	if _, err := newTestASDU(M_SP_NA_1, false, 61, bytes.Repeat(onePoint, 61)).MarshalBinary(); !errors.Is(err, ErrLengthOutOfRange) {
		t.Errorf("error = %v, want ErrLengthOutOfRange", err)
	}
	// A sequence: IOA(3) once, then one SIQ per object.
	seq := append([]byte{0x01, 0x00, 0x00}, bytes.Repeat([]byte{0x01}, 20)...)
	if _, err := newTestASDU(M_SP_NA_1, true, 20, seq).MarshalBinary(); err != nil {
		t.Errorf("a well formed sequence was refused: %v", err)
	}
}

// The private range has no defined object size, so its payload cannot be
// checked — and refusing it outright would make the range unusable.
func TestMarshalBinaryAllowsThePrivateRange(t *testing.T) {
	a := newTestASDU(TypeID(200), false, 1, []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x01, 0x02})
	if _, err := a.MarshalBinary(); err != nil {
		t.Fatalf("a private range type was refused: %v", err)
	}
	// The size limit still applies to it.
	big := newTestASDU(TypeID(200), false, 1, bytes.Repeat([]byte{0xAA}, 300))
	if _, err := big.MarshalBinary(); !errors.Is(err, ErrLengthOutOfRange) {
		t.Fatalf("error = %v, want ErrLengthOutOfRange", err)
	}
}

// Whatever marshals must decode: the encoder and the decoder must not
// disagree about what a well formed ASDU is.
func TestMarshalledASDUsDecode(t *testing.T) {
	onePoint := []byte{0x01, 0x00, 0x00, 0x01}
	for n := 1; n <= 60; n++ {
		a := newTestASDU(M_SP_NA_1, false, byte(n), bytes.Repeat(onePoint, n))
		raw, err := a.MarshalBinary()
		if err != nil {
			t.Fatalf("%d objects: %v", n, err)
		}
		back := NewEmptyASDU(ParamsWide)
		if err := back.UnmarshalBinary(raw); err != nil {
			t.Fatalf("%d objects marshalled but will not decode: %v", n, err)
		}
		if got := len(back.GetSinglePoint()); got != n {
			t.Fatalf("%d objects went out, %d came back", n, got)
		}
	}
}

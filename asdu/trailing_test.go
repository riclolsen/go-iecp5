package asdu

import (
	"bytes"
	"errors"
	"testing"
)

// An ASDU carrying more information object octets than its variable structure
// qualifier accounts for was not produced by a conforming sender: the frame
// fixes the total length, the qualifier fixes the object count and the type
// identification fixes the object size, so there is nowhere for a surplus
// octet to come from.
//
// Truncating it silently meant a control ASDU of one valid command followed
// by anything at all reached the handler and was executed, with the rest
// discarded unseen.

// buildRaw assembles a raw ASDU: type, VSQ, cause, originator, common
// address, the information objects, then any trailing octets.
func buildRaw(t TypeID, sq bool, n byte, cause byte, ca uint16, body, trailing []byte) []byte {
	vsq := n & 0x7f
	if sq {
		vsq |= 0x80
	}
	out := []byte{byte(t), vsq, cause, 0x00, byte(ca), byte(ca >> 8)}
	out = append(out, body...)
	return append(out, trailing...)
}

func TestTrailingOctetsRejected(t *testing.T) {
	// C_SC_NA_1, one object: IOA(3) + SCO(1).
	cmdBody := []byte{0x64, 0x00, 0x00, 0x01}
	// M_SP_NA_1, two objects, each IOA(3) + SIQ(1).
	twoPoints := []byte{0x01, 0x00, 0x00, 0x01, 0x02, 0x00, 0x00, 0x00}

	for _, tt := range []struct {
		name     string
		raw      []byte
		accepted bool
	}{
		{"command, exact", buildRaw(C_SC_NA_1, false, 1, 6, 1, cmdBody, nil), true},
		{"command, one trailing octet",
			buildRaw(C_SC_NA_1, false, 1, 6, 1, cmdBody, []byte{0xAA}), false},
		{"command, four trailing octets",
			buildRaw(C_SC_NA_1, false, 1, 6, 1, cmdBody, []byte{0xDE, 0xAD, 0xBE, 0xEF}), false},
		{"command, forty trailing octets",
			buildRaw(C_SC_NA_1, false, 1, 6, 1, cmdBody, bytes.Repeat([]byte{0xFF}, 40)), false},

		{"two points, exact", buildRaw(M_SP_NA_1, false, 2, 20, 1, twoPoints, nil), true},
		{"two points, a third object the qualifier does not account for",
			buildRaw(M_SP_NA_1, false, 2, 20, 1, twoPoints, []byte{0x03, 0x00, 0x00, 0x01}), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := NewEmptyASDU(ParamsWide)
			err := a.UnmarshalBinary(tt.raw)
			switch {
			case tt.accepted && err != nil:
				t.Fatalf("a well formed ASDU was rejected: %v", err)
			case !tt.accepted && err == nil:
				t.Fatalf("accepted: InfoObj kept %d octets, the surplus was discarded unseen",
					len(a.InfoObj))
			case !tt.accepted && !errors.Is(err, ErrTrailingOctets):
				t.Fatalf("wrong error: %v", err)
			}
		})
	}
}

// TestTrailingOctetsAllowed: a device that pads can be accommodated
// deliberately, which is different from doing it silently for everyone.
func TestTrailingOctetsAllowed(t *testing.T) {
	raw := buildRaw(C_SC_NA_1, false, 1, 6, 1,
		[]byte{0x64, 0x00, 0x00, 0x01}, []byte{0xDE, 0xAD})

	lenient := *ParamsWide
	lenient.AllowTrailingOctets = true

	a := NewEmptyASDU(&lenient)
	if err := a.UnmarshalBinary(raw); err != nil {
		t.Fatalf("UnmarshalBinary() = %v, want nil with AllowTrailingOctets", err)
	}
	if cmd := a.GetSingleCmd(); cmd.Ioa != 100 || !cmd.Value {
		t.Fatalf("command decoded as %+v", cmd)
	}

	// And the default is still strict.
	if err := NewEmptyASDU(ParamsWide).UnmarshalBinary(raw); !errors.Is(err, ErrTrailingOctets) {
		t.Fatalf("the default must reject: %v", err)
	}
}

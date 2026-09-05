package cs103

import (
	"testing"

	"github.com/riclolsen/go-iecp5/cs101"
)

// ASDUSizeMax is derived from the FT1.2 frame that carries it: the length
// octet runs to 255 and counts the control field, the link address and the
// ASDU. These two numbers live in different packages and must agree, or an
// ASDU this package considers legal is one the frame layer will not send.
func TestASDUSizeMaxMatchesTheFrameThatCarriesIt(t *testing.T) {
	if got := cs101.MaxASDULen(linkAddrSize); got != ASDUSizeMax {
		t.Errorf("ASDUSizeMax is %d but a %d octet link address leaves room for %d",
			ASDUSizeMax, linkAddrSize, got)
	}
}

// TestMaximumASDUFitsItsFrame: an ASDU of the advertised maximum must
// actually marshal into a frame.
func TestMaximumASDUFitsItsFrame(t *testing.T) {
	f := &cs101.Frame{
		Start:    cs101.StartVariable,
		Control:  0x53,
		LinkAddr: []byte{0x01},
		ASDU:     make([]byte, ASDUSizeMax),
	}
	out, err := f.MarshalBinary(linkAddrSize)
	if err != nil {
		t.Fatalf("an ASDU of the advertised maximum (%d octets) will not marshal: %v",
			ASDUSizeMax, err)
	}
	if len(out) != cs101.MaxFrameLen {
		t.Errorf("frame is %d octets, want %d", len(out), cs101.MaxFrameLen)
	}
}

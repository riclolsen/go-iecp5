package cs104

import (
	"errors"
	"reflect"
	"testing"
)

func TestIAPCI_String(t *testing.T) {
	tests := []struct {
		name string
		this iAPCI
		want string
	}{
		{"iFrame", iAPCI{sendSN: 0x02, rcvSN: 0x02}, "I[sendNO: 2, recvNO: 2]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.this.String(); got != tt.want {
				t.Errorf("APCI.String() = %v, want %v", got, tt.want)
			}
		})
	}
}
func TestSAPCI_String(t *testing.T) {
	tests := []struct {
		name string
		this sAPCI
		want string
	}{
		{"sFrame", sAPCI{rcvSN: 123}, "S[recvNO: 123]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.this.String(); got != tt.want {
				t.Errorf("APCI.String() = %v, want %v", got, tt.want)
			}
		})
	}
}
func TestUAPCI_String(t *testing.T) {
	tests := []struct {
		name string
		this uAPCI
		want string
	}{
		{"uFrame", uAPCI{function: uStartDtActive}, "U[function: StartDtActive]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.this.String(); got != tt.want {
				t.Errorf("APCI.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_newIFrame(t *testing.T) {
	type args struct {
		asdu   []byte
		sendSN uint16
		RcvSN  uint16
	}
	tests := []struct {
		name    string
		args    args
		want    []byte
		wantErr bool
	}{
		{
			"asdu out of range",
			args{asdu: make([]byte, 250)},
			nil,
			true,
		},
		{
			"asdu right",
			args{[]byte{0x01, 0x02}, 0x06, 0x07},
			[]byte{startFrame, 0x06, 0x0c, 0x00, 0x0e, 0x00, 0x01, 0x02},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := newIFrame(tt.args.sendSN, tt.args.RcvSN, tt.args.asdu)
			if (err != nil) != tt.wantErr {
				t.Errorf("newIFrame() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("newIFrame() = % x, want % x", got, tt.want)
			}
		})
	}
}

func Test_newSFrame(t *testing.T) {
	type args struct {
		RcvSN uint16
	}
	tests := []struct {
		name string
		args args
		want []byte
	}{
		{"", args{0x06}, []byte{startFrame, 0x04, 0x01, 0x00, 0x0c, 0x00}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := newSFrame(tt.args.RcvSN); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("newSFrame() = % x, want % x", got, tt.want)
			}
		})
	}
}

func Test_newUFrame(t *testing.T) {
	type args struct {
		which byte
	}
	tests := []struct {
		name string
		args args
		want []byte
	}{
		{"", args{uStopDtActive}, []byte{startFrame, 0x04, 0x13, 0x00, 0x00, 0x00}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := newUFrame(tt.args.which); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("newUFrame() = % x, want % x", got, tt.want)
			}
		})
	}
}

func Test_parse(t *testing.T) {
	tests := []struct {
		name  string
		apdu  []byte
		want  interface{}
		want1 []byte
	}{
		{
			// An I frame carries an ASDU, and bit 0 of the third control
			// octet belongs to the format, not to the sequence number.
			"iAPCI",
			[]byte{startFrame, 0x05, 0x02, 0x00, 0x02, 0x00, 0x64},
			iAPCI{sendSN: 0x01, rcvSN: 0x01},
			[]byte{0x64},
		},
		{
			"sAPCI",
			[]byte{startFrame, 0x04, 0x01, 0x00, 0x02, 0x00},
			sAPCI{rcvSN: 0x01},
			[]byte{},
		},
		{
			"uAPCI",
			[]byte{startFrame, 0x04, 0x07, 0x00, 0x00, 0x00},
			uAPCI{uStartDtActive},
			[]byte{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, got1, err := parse(tt.apdu)
			if err != nil {
				t.Fatalf("parse() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parse() got = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(got1, tt.want1) {
				t.Errorf("parse() got1 = %v, want %v", got1, tt.want1)
			}
		})
	}
}

// Test_parseRejectsMalformedAPCI: the S and U formats carry no payload to be
// validated later, so whatever their control field says is acted on directly
// — activating or deactivating data transfer, or moving the acknowledged
// sequence number. parse is the only place a malformed one can be stopped.
//
// IEC 60870-5-104 subclause 5.1 fixes both formats at an APDU length of 4 and
// requires every unused control bit to be zero.
func Test_parseRejectsMalformedAPCI(t *testing.T) {
	for _, tt := range []struct {
		name string
		apdu []byte
	}{
		{"truncated", []byte{startFrame, 0x04, 0x07}},
		{"length field disagrees with the octets read",
			[]byte{startFrame, 0x08, 0x01, 0x00, 0x02, 0x00}},

		{"U format with reserved octets set",
			[]byte{startFrame, 0x04, 0x07, 0xFF, 0xFF, 0xFF}},
		{"U format with one reserved octet set",
			[]byte{startFrame, 0x04, 0x07, 0x00, 0x01, 0x00}},
		{"U format with length 10",
			[]byte{startFrame, 0x0A, 0x07, 0x00, 0x00, 0x00,
				0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA}},
		{"U format with two function bits set",
			[]byte{startFrame, 0x04, 0x0F, 0x00, 0x00, 0x00}},
		{"U format with no function bit set",
			[]byte{startFrame, 0x04, 0x03, 0x00, 0x00, 0x00}},

		{"S format with second control octet set",
			[]byte{startFrame, 0x04, 0x01, 0xFF, 0x02, 0x00}},
		{"S format with unused bits of the first octet set",
			[]byte{startFrame, 0x04, 0xF1, 0x00, 0x02, 0x00}},
		{"S format with the sequence format bit set",
			[]byte{startFrame, 0x04, 0x01, 0x00, 0x03, 0x00}},
		{"S format with length 8",
			[]byte{startFrame, 0x08, 0x01, 0x00, 0x02, 0x00, 0xAA, 0xAA, 0xAA, 0xAA}},

		{"I format with no ASDU",
			[]byte{startFrame, 0x04, 0x02, 0x00, 0x02, 0x00}},
		{"I format with the sequence format bit set",
			[]byte{startFrame, 0x05, 0x02, 0x00, 0x03, 0x00, 0x64}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			apci, _, err := parse(tt.apdu)
			if err == nil {
				t.Fatalf("accepted as %T: % x", apci, tt.apdu)
			}
			if !errors.Is(err, ErrInvalidAPCI) {
				t.Fatalf("wrong error: %v", err)
			}
		})
	}
}

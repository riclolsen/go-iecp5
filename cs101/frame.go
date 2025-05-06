// Copyright 2025 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs101

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/riclolsen/go-iecp5/asdu"
)

// CS101 Frame Format Constants (FT1.2)
const (
	// StartFixed is the start character for fixed-length frames
	StartFixed byte = 0x10
	// StartVariable is the start character for variable-length frames
	StartVariable byte = 0x68
	// EndChar is the end character for frames (not standard FT1.2, but sometimes used)
	EndChar byte = 0x16

	// Max FTU Length (Frame Transfer Unit)
	MaxFrameLen = 255
)

const (
	// Control Field Bits (Primary Station Message)
	// DIR: Direction bit (0: Secondary -> Primary, 1: Primary -> Secondary)
	CtrlDIR byte = 0x80 // Not used directly in PRM=1 functions
	// PRM: Primary Message bit (1: From Primary Station, 0: From Secondary Station)
	CtrlPRM byte = 0x40
	// FCB: Frame Count Bit
	CtrlFCB byte = 0x20
	// FCV: Frame Count Valid bit
	CtrlFCV byte = 0x10
	// Control Field Bits (Secondary Station Message)
	// ACD: Access Demand bit
	CtrlACD byte = 0x20
	// DFC: Data Flow Control bit
	CtrlDFC byte = 0x10
	// Function Code Mask
	CtrlFuncMask byte = 0x0F
)

// Primary Function Codes (PRM=1, bits 0-3)
const (
	// --- Send/Confirm Functions ---
	PrimFcResetLink      byte = 0 // Reset of remote link
	PrimFcResetUser      byte = 1 // Reset of user process
	PrimFcTestLink       byte = 2 // Reserved for balanced mode
	PrimFcUserDataConf   byte = 3 // User data, confirmed
	PrimFcUserDataNoConf byte = 4 // User data, unconfirmed
	// --- Request/Respond Functions ---
	PrimFcReqAccess byte = 8  // Request access demand
	PrimFcReqStatus byte = 9  // Request status of link
	PrimFcReqData1  byte = 10 // Request user data class 1
	PrimFcReqData2  byte = 11 // Request user data class 2
)

// Secondary Function Codes (PRM=0, bits 0-3)
const (
	// --- Send/Confirm Functions ---
	// ACD: Access Demand bit (part of func code)
	// DFC: Data Flow Control bit (part of func code)
	SecFcConfACK       byte = 0 // Confirm: Positive acknowledge (ACK)
	SecFcConfNACK      byte = 1 // Confirm: Negative acknowledge (NACK, link busy)
	SecFcUserDataConf  byte = 8 // User data, confirmed
	SecFcUserDataNoRep byte = 9 // User data, no reply expected
	// --- Request/Respond Functions ---
	SecFcRespStatus byte = 11 // Respond: Status of link / Access Demand
	SecFcRespLinkNF byte = 14 // Respond: NACK - Link service not functioning
	SecFcRespLinkNI byte = 15 // Respond: NACK - Link service not implemented
)

// ControlField represents the parsed control field byte
type ControlField struct {
	PRM bool // Primary Message (true if from primary station)
	FCB bool // Frame Count Bit
	FCV bool // Frame Count Valid
	ACD bool // Access Demand (secondary station only)
	DFC bool // Data Flow Control (secondary station only)
	Fun byte // Function Code (masked)
}

// ParseControlField parses the control field byte.
func ParseControlField(b byte) ControlField {
	cf := ControlField{
		PRM: (b & CtrlPRM) != 0,
		Fun: b & CtrlFuncMask,
	}
	if cf.PRM { // Primary Station Message
		cf.FCB = (b & CtrlFCB) != 0
		cf.FCV = (b & CtrlFCV) != 0
	} else { // Secondary Station Message
		// ACD and DFC
		cf.ACD = (b & CtrlACD) != 0
		cf.DFC = (b & CtrlDFC) != 0
	}
	return cf
}

// Value encodes the ControlField struct back to a byte.
func (cf ControlField) Value() byte {
	var b byte
	if cf.PRM {
		b |= CtrlPRM
		if cf.FCB {
			b |= CtrlFCB
		}
		if cf.FCV {
			b |= CtrlFCV
		}
	} else {
		// Reconstruct secondary function code based on ACD/DFC and base function if needed.
		// This logic depends heavily on how secondary function codes are defined.
		// Assuming cf.Fun already holds the correct secondary code:
	}
	b |= (cf.Fun & CtrlFuncMask)
	return b
}

// String provides a string representation of the control field.
func (cf ControlField) String() string {
	prm := "SEC"
	if cf.PRM {
		prm = "PRM"
	}
	fcb := ""
	if cf.FCV {
		if cf.FCB {
			fcb = " FCB=1"
		} else {
			fcb = " FCB=0"
		}
	}
	acd := ""
	if !cf.PRM && cf.ACD {
		acd = " ACD=1"
	}
	dfc := ""
	if !cf.PRM && cf.DFC {
		dfc = " DFC=1"
	}
	return fmt.Sprintf("CTRL<%s FC=0x%02X%s%s%s>", prm, cf.Fun, fcb, acd, dfc)
}

// Frame represents a generic CS101 frame.
// Specific types (Fixed, Variable) will embed or use this.
type Frame struct {
	Start    byte
	Length1  byte // First length field (present in variable length)
	Length2  byte // Second length field (present in variable length, confirms Length1)
	Start2   byte // Second start character (present in variable length)
	Control  byte
	LinkAddr []byte // Link Address (0, 1, or 2 bytes depending on config)
	ASDU     []byte // Application Service Data Unit (optional)
	Checksum byte
	End      byte // End character (e.g., 0x16)
}

// Errors related to frame parsing
var (
	ErrInvalidStartChar   = errors.New("invalid start character")
	ErrLengthMismatch     = errors.New("length fields do not match")
	ErrChecksumMismatch   = errors.New("checksum mismatch")
	ErrFrameTooShort      = errors.New("frame is too short for headers")
	ErrFrameLenExceeded   = errors.New("frame length exceeds maximum")
	ErrInvalidLinkAddrLen = errors.New("invalid link address length in config")
	ErrInvalidEndChar     = errors.New("invalid end character")
)

// calculateChecksum calculates the checksum (sum of bytes from Control field to end of ASDU).
func calculateChecksum(control byte, linkAddr []byte, asdu []byte) byte {
	var sum uint16
	sum += uint16(control)
	for _, b := range linkAddr {
		sum += uint16(b)
	}
	for _, b := range asdu {
		sum += uint16(b)
	}
	return byte(sum) // Truncate to 8 bits
}

// ParseFrame attempts to parse a CS101 frame from a reader.
// It requires the link address size from the configuration.
// Returns the parsed frame or an error.
// TODO: Implement robust parsing logic handling timeouts and byte-by-byte reading.
func ParseFrame(r io.Reader, linkAddrSize byte) (*Frame, error) {
	if linkAddrSize > 2 {
		return nil, ErrInvalidLinkAddrLen
	}

	// --- This is a simplified placeholder for parsing ---
	// A real implementation needs careful byte-by-byte reading, timeout handling,
	// and state management to correctly identify frame boundaries and types.

	// 1. Read Start Byte
	startByte := make([]byte, 1)
	if _, err := io.ReadFull(r, startByte); err != nil {
		return nil, fmt.Errorf("reading start byte: %w", err)
	}

	frame := &Frame{Start: startByte[0]}

	switch frame.Start {
	case StartFixed: // Fixed Length Frame (e.g., ACK, NACK, Link Status)
		// Read Control + LinkAddr (optional) + Checksum + End
		fixedLen := 1 + int(linkAddrSize) + 1 + 1 // Control + LinkAddr + Checksum + End
		fixedData := make([]byte, fixedLen)
		if _, err := io.ReadFull(r, fixedData); err != nil {
			return nil, fmt.Errorf("reading fixed frame data: %w", err)
		}
		frame.Control = fixedData[0]
		frame.LinkAddr = fixedData[1 : 1+int(linkAddrSize)]
		frame.Checksum = fixedData[fixedLen-2]
		frame.End = fixedData[fixedLen-1]

		// Verify checksum
		calculatedCS := calculateChecksum(frame.Control, frame.LinkAddr, nil)
		if frame.End != EndChar {
			return nil, fmt.Errorf("%w: expected 0x%02X, got 0x%02X", ErrInvalidEndChar, EndChar, frame.End)
		}

		if calculatedCS != frame.Checksum {
			return nil, fmt.Errorf("%w: expected 0x%02X, got 0x%02X", ErrChecksumMismatch, calculatedCS, frame.Checksum)
		}

	case StartVariable: // Variable Length Frame (contains ASDU)
		// Read Length1, Length2, Control
		header := make([]byte, 4)
		if _, err := io.ReadFull(r, header); err != nil {
			return nil, fmt.Errorf("reading variable frame header: %w", err)
		}
		frame.Length1 = header[0]
		frame.Length2 = header[1]
		frame.Start2 = header[2]
		frame.Control = header[3]

		// Validate lengths
		if frame.Length1 != frame.Length2 {
			return nil, fmt.Errorf("%w: L1=0x%02X, L2=0x%02X", ErrLengthMismatch, frame.Length1, frame.Length2)
		}
		if frame.Length1 < linkAddrSize { // Length includes LinkAddr + ASDU
			return nil, fmt.Errorf("%w: length %d less than link address size %d", ErrFrameTooShort, frame.Length1, linkAddrSize)
		}
		// Check against overall max length (Start+L1+L2+Start2+Ctrl+CS+End = 7 bytes overhead)
		if frame.Length1 > MaxFrameLen-6 {
			return nil, fmt.Errorf("%w: L=%d", ErrFrameLenExceeded, frame.Length1)
		}

		// Read LinkAddr + ASDU + Checksum + End
		bodyLen := -1 + int(frame.Length1) + 1 + 1 // (-Control) + LinkAddr + ASDU + Checksum + End
		bodyData := make([]byte, bodyLen)
		if _, err := io.ReadFull(r, bodyData); err != nil {
			return nil, fmt.Errorf("reading variable frame body: %w", err)
		}
		frame.LinkAddr = bodyData[0:linkAddrSize]
		frame.ASDU = bodyData[linkAddrSize : bodyLen-2]
		frame.Checksum = bodyData[bodyLen-2]
		frame.End = bodyData[bodyLen-1]
		if frame.End != EndChar {
			return nil, fmt.Errorf("%w: expected 0x%02X, got 0x%02X", ErrInvalidEndChar, EndChar, frame.End)
		}

		// Verify checksum
		calculatedCS := calculateChecksum(frame.Control, frame.LinkAddr, frame.ASDU)
		if calculatedCS != frame.Checksum {
			return nil, fmt.Errorf("%w: expected 0x%02X, got 0x%02X", ErrChecksumMismatch, calculatedCS, frame.Checksum)
		}

	default:
		return nil, fmt.Errorf("%w: expected 0x10 or 0x68, got 0x%02X", ErrInvalidStartChar, frame.Start)
	}

	return frame, nil
}

// MarshalBinary encodes the Frame struct into its byte representation.
func (f *Frame) MarshalBinary(linkAddrSize byte) ([]byte, error) {
	if linkAddrSize > 2 {
		return nil, ErrInvalidLinkAddrLen
	}
	if len(f.LinkAddr) != int(linkAddrSize) {
		// If linkAddrSize is 0, f.LinkAddr should be nil or empty
		if !(linkAddrSize == 0 && (len(f.LinkAddr) == 0)) {
			return nil, fmt.Errorf("link address length mismatch: expected %d, got %d", linkAddrSize, len(f.LinkAddr))
		}
	}

	switch f.Start {
	case StartFixed:
		// Frame: Start(1) + Control(1) + LinkAddr(linkAddrSize) + Checksum(1) + End(1)
		totalLen := 4 + int(linkAddrSize)
		buf := make([]byte, totalLen)
		buf[0] = StartFixed
		buf[1] = f.Control
		copy(buf[2:], f.LinkAddr)
		f.Checksum = calculateChecksum(f.Control, f.LinkAddr, nil)
		buf[2+int(linkAddrSize)] = f.Checksum
		buf[totalLen-1] = EndChar // Add End character
		return buf, nil

	case StartVariable:
		// Frame: Start(1) + Len1(1) + Len2(1) + Control(1) + LinkAddr(linkAddrSize) + ASDU(N) + Checksum(1) + End(1)
		asduLen := len(f.ASDU)
		frameLenField := byte(1 + int(linkAddrSize) + asduLen) // Length field = LinkAddr + ASDU length
		// Check overall length constraint (Start+L1+L2+Start+Ctrl+CS+End = 6 bytes overhead)
		if int(frameLenField) > MaxFrameLen-6 {
			return nil, fmt.Errorf("%w: calculated L=%d", ErrFrameLenExceeded, frameLenField)
		}

		buf := make([]byte, 0)
		buf = append(buf, StartVariable)
		buf = append(buf, frameLenField)
		buf = append(buf, frameLenField) // Length repeated
		buf = append(buf, StartVariable)
		buf = append(buf, f.Control)
		buf = append(buf, f.LinkAddr...)
		buf = append(buf, f.ASDU...)
		f.Checksum = calculateChecksum(f.Control, f.LinkAddr, f.ASDU)
		buf = append(buf, f.Checksum)
		buf = append(buf, EndChar) // Add End character
		return buf, nil

	default:
		return nil, fmt.Errorf("%w: cannot marshal frame with start 0x%02X", ErrInvalidStartChar, f.Start)
	}
}

// DecodeASDU decodes the ASDU part of the frame using the provided ASDU parameters.
func (f *Frame) DecodeASDU(params *asdu.Params) (*asdu.ASDU, error) {
	if f.Start != StartVariable || len(f.ASDU) == 0 {
		return nil, errors.New("frame does not contain an ASDU")
	}
	a := asdu.NewEmptyASDU(params)
	err := a.UnmarshalBinary(f.ASDU)
	if err != nil {
		return nil, fmt.Errorf("failed to decode ASDU: %w", err)
	}
	// Set link address from frame if needed (though ASDU has CommonAddr)
	// a.LinkAddress = binary.LittleEndian.Uint16(f.LinkAddr) // Example if needed
	return a, nil
}

// NewFixedFrame creates a new fixed-length frame (e.g., ACK/NACK).
func NewFixedFrame(control byte, linkAddr []byte) *Frame {
	return &Frame{
		Start:    StartFixed,
		Control:  control,
		LinkAddr: linkAddr, // Ensure this matches config linkAddrSize when marshaling
		// Checksum and End are added during marshaling
	}
}

// NewDataFrame creates a new variable-length frame containing an ASDU.
func NewDataFrame(control byte, linkAddr []byte, asduData []byte) *Frame {
	// Length fields and checksum are calculated during marshaling.
	return &Frame{
		Start:    StartVariable,
		Control:  control,
		LinkAddr: linkAddr, // Ensure this matches config linkAddrSize when marshaling
		ASDU:     asduData,
		// Length1, Length2, Checksum, and End are added during marshaling
	}
}

// GetASDU attempts to decode and return the ASDU from the frame.
// Requires ASDU parameters for decoding. Returns nil if not a data frame or on error.
func (f *Frame) GetASDU(params *asdu.Params) *asdu.ASDU {
	if f.Start != StartVariable || len(f.ASDU) == 0 {
		return nil
	}
	a, err := f.DecodeASDU(params)
	if err != nil {
		// Log error?
		return nil
	}
	return a
}

// GetControlField parses and returns the ControlField struct.
func (f *Frame) GetControlField() ControlField {
	return ParseControlField(f.Control)
}

// GetLinkAddress returns the link address as uint16 (assuming Little Endian).
// Returns 0 if link address size is 0 or invalid.
func (f *Frame) GetLinkAddress() uint16 {
	switch len(f.LinkAddr) {
	case 1:
		return uint16(f.LinkAddr[0])
	case 2:
		return binary.LittleEndian.Uint16(f.LinkAddr)
	default:
		return 0
	}
}

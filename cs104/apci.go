// Copyright 2020 thinkgos (thinkgo@aliyun.com).  All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs104

import (
	"errors"
	"fmt"

	"github.com/riclolsen/go-iecp5/asdu"
)

const startFrame byte = 0x68 // start character

// APDU form Max size 255
//
//	|              APCI                   |       ASDU         |
//	| start | APDU length | control field |       ASDU         |
//	                 |          APDU field size(253)           |
//
// bytes|    1  |    1   |        4           |                    |
const (
	APCICtlFiledSize = 4 // control filed(4)

	APDUSizeMax      = 255                                 // start(1) + length(1) + control field(4) + ASDU
	APDUFieldSizeMax = APCICtlFiledSize + asdu.ASDUSizeMax // control field(4) + ASDU
)

// U frame control domain function
const (
	uStartDtActive  byte = 4 << iota // start activation 0x04
	uStartDtConfirm                  // start confirmation 0x08
	uStopDtActive                    // deactivate 0x10
	uStopDtConfirm                   // stop confirmation 0x20
	uTestFrActive                    // test activation 0x40
	uTestFrConfirm                   // test confirmation 0x80
)

// I frame contains apci and asdu information frame. Used for numbered information transmission
type iAPCI struct {
	sendSN, rcvSN uint16
}

func (sf iAPCI) String() string {
	return fmt.Sprintf("I[sendNO: %d, recvNO: %d]", sf.sendSN, sf.rcvSN)
}

// S frame only contains the apci S frame for the correct transmission of the main confirmation frame, the protocol is called monitoring. supervisory
type sAPCI struct {
	rcvSN uint16
}

func (sf sAPCI) String() string {
	return fmt.Sprintf("S[recvNO: %d]", sf.rcvSN)
}

// U frame contains only apci unnumbered control information unnumbered
type uAPCI struct {
	function byte // bit8 test confirmation
}

func (sf uAPCI) String() string {
	var s string
	switch sf.function {
	case uStartDtActive:
		s = "StartDtActive"
	case uStartDtConfirm:
		s = "StartDtConfirm"
	case uStopDtActive:
		s = "StopDtActive"
	case uStopDtConfirm:
		s = "StopDtConfirm"
	case uTestFrActive:
		s = "TestFrActive"
	case uTestFrConfirm:
		s = "TestFrConfirm"
	default:
		s = "Unknown"
	}
	return fmt.Sprintf("U[function: %s]", s)
}

// newIFrame creates an I frame and returns to apdu
func newIFrame(sendSN, RcvSN uint16, asdus []byte) ([]byte, error) {
	if len(asdus) > asdu.ASDUSizeMax {
		return nil, fmt.Errorf("ASDU filed large than max %d", asdu.ASDUSizeMax)
	}

	b := make([]byte, len(asdus)+6)

	b[0] = startFrame
	b[1] = byte(len(asdus) + 4)
	b[2] = byte(sendSN << 1)
	b[3] = byte(sendSN >> 7)
	b[4] = byte(RcvSN << 1)
	b[5] = byte(RcvSN >> 7)
	copy(b[6:], asdus)

	return b, nil
}

// newSFrame creates an S frame and returns to apdu
func newSFrame(RcvSN uint16) []byte {
	return []byte{startFrame, 4, 0x01, 0x00, byte(RcvSN << 1), byte(RcvSN >> 7)}
}

// newUFrame creates a U frame and returns apdu
func newUFrame(which byte) []byte {
	return []byte{startFrame, 4, which | 0x03, 0x00, 0x00, 0x00}
}

// APCI apci Application Protocol Control Information
type APCI struct {
	start                  byte
	apduFiledLen           byte // control + asdu length
	ctr1, ctr2, ctr3, ctr4 byte
}

// ErrInvalidAPCI reports an APDU whose control field is not one the standard
// defines. The frame is not acted on: a peer must not be able to change the
// state of the link with a frame it had no right to send.
var ErrInvalidAPCI = errors.New("cs104: malformed APCI")

// isValidUFunction reports whether exactly one of the six defined U-format
// functions is selected. The function occupies the upper six bits as three
// act/con pairs, and exactly one bit is set in a legal frame — a frame with
// two of them set is not "both", it is malformed.
func isValidUFunction(f byte) bool {
	switch f {
	case uStartDtActive, uStartDtConfirm, uStopDtActive, uStopDtConfirm,
		uTestFrActive, uTestFrConfirm:
		return true
	}
	return false
}

// parse returns the frame type, the ASDU that follows it, and an error when
// the APCI is malformed.
//
// The checks matter because the S and U formats carry no payload to be
// validated later: whatever the control field says is acted on directly, so
// this is the only place a malformed one can be caught. IEC 60870-5-104
// subclause 5.1 fixes both formats at an APDU length of 4 and requires every
// unused control bit to be zero, and a frame that violates either was not
// produced by a conforming peer.
func parse(apdu []byte) (interface{}, []byte, error) {
	if len(apdu) < 6 {
		return nil, nil, fmt.Errorf("%w: %d octets, minimum 6", ErrInvalidAPCI, len(apdu))
	}
	apci := APCI{apdu[0], apdu[1], apdu[2], apdu[3], apdu[4], apdu[5]}

	// The length octet counts the control field and the ASDU, so it must
	// describe exactly what was read.
	if int(apci.apduFiledLen) != len(apdu)-2 {
		return nil, nil, fmt.Errorf("%w: length field %d does not match %d octets read",
			ErrInvalidAPCI, apci.apduFiledLen, len(apdu)-2)
	}

	switch {
	case apci.ctr1&0x01 == 0: // I format
		// An I frame carries an ASDU; one without a payload has nothing to
		// say. The low bit of the third octet is the format bit of the
		// receive sequence number and is always zero.
		if apci.apduFiledLen <= APCICtlFiledSize {
			return nil, nil, fmt.Errorf("%w: I format with no ASDU", ErrInvalidAPCI)
		}
		if apci.ctr3&0x01 != 0 {
			return nil, nil, fmt.Errorf("%w: I format with control octet 3 = 0x%02X, bit 0 must be clear",
				ErrInvalidAPCI, apci.ctr3)
		}
		return iAPCI{
			sendSN: uint16(apci.ctr1)>>1 + uint16(apci.ctr2)<<7,
			rcvSN:  uint16(apci.ctr3)>>1 + uint16(apci.ctr4)<<7,
		}, apdu[6:], nil

	case apci.ctr1&0x03 == 0x01: // S format
		if apci.apduFiledLen != APCICtlFiledSize {
			return nil, nil, fmt.Errorf("%w: S format with length %d, must be %d",
				ErrInvalidAPCI, apci.apduFiledLen, APCICtlFiledSize)
		}
		if apci.ctr1 != 0x01 || apci.ctr2 != 0 || apci.ctr3&0x01 != 0 {
			return nil, nil, fmt.Errorf("%w: S format with control field %02X %02X %02X %02X, unused bits must be clear",
				ErrInvalidAPCI, apci.ctr1, apci.ctr2, apci.ctr3, apci.ctr4)
		}
		return sAPCI{
			rcvSN: uint16(apci.ctr3)>>1 + uint16(apci.ctr4)<<7,
		}, apdu[6:], nil

	default: // U format, apci.ctr1&0x03 == 0x03
		if apci.apduFiledLen != APCICtlFiledSize {
			return nil, nil, fmt.Errorf("%w: U format with length %d, must be %d",
				ErrInvalidAPCI, apci.apduFiledLen, APCICtlFiledSize)
		}
		if apci.ctr2 != 0 || apci.ctr3 != 0 || apci.ctr4 != 0 {
			return nil, nil, fmt.Errorf("%w: U format with reserved octets %02X %02X %02X, must be zero",
				ErrInvalidAPCI, apci.ctr2, apci.ctr3, apci.ctr4)
		}
		function := apci.ctr1 & 0xfc
		if !isValidUFunction(function) {
			return nil, nil, fmt.Errorf("%w: U format function 0x%02X is not one of the six defined",
				ErrInvalidAPCI, function)
		}
		return uAPCI{function: function}, apdu[6:], nil
	}
}

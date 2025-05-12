// Copyright 2025 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs101

import (
	"time"

	"go.bug.st/serial"
)

// DefaultReconnectInterval defined default value
const DefaultReconnectInterval = 1 * time.Minute

// seqPending might be used for sequence number tracking, similar to CS104.
// Review if its usage aligns with CS101 frame handling later.
//type seqPending struct {
//	seq      uint16
//	sendTime time.Time
//}

// SerialConfig holds serial port configuration parameters.
type SerialConfig struct {
	// Address is the serial port address (e.g., "COM3" on Windows, "/dev/ttyS0" on Linux).
	Address string
	// BaudRate is the serial port speed (e.g., 9600, 19200, 115200).
	BaudRate int
	// DataBits is the number of data bits (usually 7 or 8).
	DataBits int // Usually 8 for IEC 60870-5
	// StopBits specifies the number of stop bits. Use serial.OneStopBit or serial.TwoStopBits.
	StopBits serial.StopBits
	// Parity specifies the parity mode. Use serial.NoParity, serial.OddParity, serial.EvenParity.
	Parity serial.Parity
	// Timeout specifies the read/write timeout for the serial port. 0 means no timeout.
	Timeout time.Duration
}

// mapParity maps a byte representation to serial.Parity.
// Used for transitioning from placeholder values if needed, or for validation.
// 0 = None, 1 = Odd, 2 = Even. Returns NoParity for invalid values.
func mapParity(p byte) serial.Parity {
	switch p {
	case 1:
		return serial.OddParity
	case 2:
		return serial.EvenParity
	default: // Includes 0
		return serial.NoParity
	}
}

// mapStopBits maps a byte representation to serial.StopBits.
// 1 = OneStopBit, 2 = TwoStopBits. Returns OneStopBit for invalid values.
func mapStopBits(s byte) serial.StopBits {
	if s == 2 {
		return serial.TwoStopBits
	}
	return serial.OneStopBit // Default includes 1
}

// Copyright 2025 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs101

import (
	"errors"
	"time"
)

// TransmissionMode defines the transmission mode (Balanced or Unbalanced)
type TransmissionMode byte

const (
	ModeUnbalanced TransmissionMode = iota // Master polls slaves
	ModeBalanced                           // Point-to-point or multi-point with contention
)

// Constants defining default values and ranges for CS101 parameters.
// These may need adjustment based on specific device profiles or requirements.
const (
	// Default timeout waiting for ACK/response from secondary station (master side)
	DefaultTimeoutResponseT1 = 10 * time.Second // Example value
	TimeoutResponseT1Min     = 1 * time.Second
	TimeoutResponseT1Max     = 255 * time.Second

	// Default timeout waiting for response after repeated request (master side)
	DefaultTimeoutRepeatT2 = 5 * time.Second // Example value, typically T2 < T1
	TimeoutRepeatT2Min     = 1 * time.Second
	TimeoutRepeatT2Max     = 255 * time.Second // Should be less than T1

	// Default timeout for sending test frames if no other traffic (master/balanced)
	DefaultTimeoutTestT3 = 20 * time.Second // Example value
	TimeoutTestT3Min     = 1 * time.Second
	TimeoutTestT3Max     = 172800 * time.Second // 48 hours

	// Default Link Address size (1 or 2 octets, 0 for structured/unused)
	DefaultLinkAddrSize = 1
	LinkAddrSizeMin     = 0
	LinkAddrSizeMax     = 2

	// Default Max length of APDU (Application Protocol Data Unit)
	// Standard defines 255 max for FTU (Frame Transfer Unit), APDU is part of it.
	DefaultMaxAPDULength = 253 // Max FTU length (255) - Start(1) - Length(1) - Control(1) - Checksum(1) = 251? Recheck standard. Let's use 253 for now.
	MaxAPDULengthMin     = 1
	MaxAPDULengthMax     = 253

	TimeoutSendLinkMsgMin     = 1 * time.Millisecond
	TimeoutSendLinkMsgMax     = 1000 * time.Millisecond
	DefaultTimeoutSendLinkMsg = 200 * time.Millisecond

	DefaultMaxSendQueueSize = 100 // Example value, can be adjusted
)

// Config defines an IEC 60870-5-101 configuration.
type Config struct {
	// Serial port settings
	Serial SerialConfig

	// Transmission Mode (Balanced or Unbalanced)
	Mode TransmissionMode

	// Link Address of this station (if used, depends on mode and config)
	// Size determined by LinkAddrSize. Can be 0 if unused.
	LinkAddress uint16
	// Size of Link Address field in octets (0, 1, or 2)
	LinkAddrSize byte

	// --- ASDU Address Configuration (from asdu package) ---
	// Common Address size in octets (1 or 2)
	CommonAddrSize byte
	// Cause of Transmission size in octets (1 or 2)
	CotSize byte
	// Information Object Address size in octets (1, 2, or 3)
	InfoObjAddrSize byte

	// --- Timeouts ---
	// Timeout waiting for ACK/response from secondary station (master side)
	// "t1" range [1, 255]s, example default 15s.
	TimeoutResponseT1 time.Duration

	// Timeout waiting for response after repeated request (master side)
	// "t2" range [1, 255]s, example default 10s (must be < t1).
	TimeoutRepeatT2 time.Duration

	// Timeout for sending test frames if no other traffic (master/balanced)
	// "t3" range [1s, 48h], example default 20s.
	TimeoutTestT3 time.Duration

	// Timeout for sending link messages (e.g., status request)
	TimeoutSendLinkMsg time.Duration

	// Maximum size of the send queue (number of frames)
	MaxSendQueueSize int

	// Maximum length of the APDU (ASDU part of the frame)
	MaxAPDULength uint8 // Range [1, 253] typically
}

// Valid applies defaults and checks configuration validity.
func (sf *Config) Valid() error {
	if sf == nil {
		return errors.New("invalid nil config")
	}

	// Validate SerialConfig (basic check, detailed check in openSerialPort)

	if sf.Serial.Address == "" {
		return errors.New("serial address (port name) must be configured")
	}
	if sf.Serial.BaudRate <= 0 {
		return errors.New("serial baud rate must be positive")
	}
	// Validate Transmission Mode
	if sf.Mode != ModeUnbalanced && sf.Mode != ModeBalanced {
		return errors.New("invalid transmission mode")
	}

	// Validate Link Address Size
	if sf.LinkAddrSize < LinkAddrSizeMin || sf.LinkAddrSize > LinkAddrSizeMax {
		return errors.New("link address size must be 0, 1, or 2")
	}
	// Validate Link Address value based on size
	if sf.LinkAddrSize == 1 && sf.LinkAddress > 0xFF {
		return errors.New("link address exceeds 1 octet limit")
	}
	// If LinkAddrSize is 2, LinkAddress can be up to 0xFFFF (uint16 max)
	// If LinkAddrSize is 0, LinkAddress should ideally be 0, but depends on usage.

	// Validate ASDU Address Sizes (must be 1 or 2 for CommonAddr/COT, 1, 2 or 3 for IOA)
	if sf.CommonAddrSize != 1 && sf.CommonAddrSize != 2 {
		// Default if not set
		if sf.CommonAddrSize == 0 {
			sf.CommonAddrSize = 1 // Default Common Address Size
		} else {
			return errors.New("invalid common address size, must be 1 or 2")
		}
	}
	if sf.CotSize != 1 && sf.CotSize != 2 {
		// Default if not set
		if sf.CotSize == 0 {
			sf.CotSize = 1 // Default COT Size
		} else {
			return errors.New("invalid cause of transmission size, must be 1 or 2")
		}
	}
	if sf.InfoObjAddrSize != 1 && sf.InfoObjAddrSize != 2 && sf.InfoObjAddrSize != 3 {
		// Default if not set
		if sf.InfoObjAddrSize == 0 {
			sf.InfoObjAddrSize = 2 // Default IOA Size (Example, adjust if needed)
		} else {
			return errors.New("invalid information object address size, must be 1, 2 or 3")
		}
	}

	// Validate and default Timeouts
	if sf.TimeoutResponseT1 == 0 {
		sf.TimeoutResponseT1 = DefaultTimeoutResponseT1
	} else if sf.TimeoutResponseT1 < TimeoutResponseT1Min || sf.TimeoutResponseT1 > TimeoutResponseT1Max {
		return errors.New("timeout T1 out of range [1, 255]s")
	}

	if sf.TimeoutRepeatT2 == 0 {
		sf.TimeoutRepeatT2 = DefaultTimeoutRepeatT2
	} else if sf.TimeoutRepeatT2 < TimeoutRepeatT2Min || sf.TimeoutRepeatT2 > TimeoutRepeatT2Max {
		return errors.New("timeout T2 out of range [1, 255]s")
	}
	// Ensure T2 < T1
	if sf.TimeoutRepeatT2 >= sf.TimeoutResponseT1 {
		return errors.New("timeout T2 must be less than T1")
	}

	if sf.TimeoutTestT3 == 0 {
		sf.TimeoutTestT3 = DefaultTimeoutTestT3
	} else if sf.TimeoutTestT3 < TimeoutTestT3Min || sf.TimeoutTestT3 > TimeoutTestT3Max {
		return errors.New("timeout T3 out of range [1s, 48h]")
	}

	if sf.TimeoutSendLinkMsg == 0 {
		sf.TimeoutSendLinkMsg = DefaultTimeoutSendLinkMsg
	} else if sf.TimeoutSendLinkMsg < TimeoutSendLinkMsgMin || sf.TimeoutSendLinkMsg > TimeoutSendLinkMsgMax {
		return errors.New("timeout for sending link message out of range [1s, 48h]")
	}

	if sf.MaxSendQueueSize < 0 {
		return errors.New("MaxSendQueueSize must be positive")
	}

	// Validate Max APDU Length
	if sf.MaxAPDULength == 0 {
		sf.MaxAPDULength = DefaultMaxAPDULength
	} else if sf.MaxAPDULength < MaxAPDULengthMin || sf.MaxAPDULength > MaxAPDULengthMax {
		return errors.New("max APDU length out of range") // Adjust range if needed
	}

	return nil
}

// DefaultConfig provides a default CS101 configuration.
// NOTE: SerialConfig needs to be set explicitly.
func DefaultConfig() Config {
	return Config{
		Serial:             SerialConfig{}, // Must be configured by user
		Mode:               ModeUnbalanced, // Common default
		LinkAddress:        1,              // Example link address
		LinkAddrSize:       DefaultLinkAddrSize,
		CommonAddrSize:     1, // Default Common Address Size
		CotSize:            1, // Default COT Size
		InfoObjAddrSize:    2, // Default IOA Size (Example, adjust if needed)
		TimeoutResponseT1:  DefaultTimeoutResponseT1,
		TimeoutRepeatT2:    DefaultTimeoutRepeatT2,
		TimeoutTestT3:      DefaultTimeoutTestT3,
		TimeoutSendLinkMsg: DefaultTimeoutSendLinkMsg,
		MaxSendQueueSize:   DefaultMaxSendQueueSize,
		MaxAPDULength:      DefaultMaxAPDULength,
	}
}

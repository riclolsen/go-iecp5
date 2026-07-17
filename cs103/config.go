// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs103

import (
	"errors"
	"time"

	"github.com/riclolsen/go-iecp5/cs101"
)

// SerialConfig re-exports the serial port configuration shared with the
// cs101 package (both companion standards use the same FT1.2 serial link).
type SerialConfig = cs101.SerialConfig

// Parameter ranges and defaults.
const (
	DefaultTimeoutResponseT1 = 10 * time.Second
	TimeoutResponseT1Min     = 1 * time.Second
	TimeoutResponseT1Max     = 255 * time.Second

	DefaultTimeoutRepeatT2 = 5 * time.Second
	TimeoutRepeatT2Min     = 1 * time.Second
	TimeoutRepeatT2Max     = 255 * time.Second // must be < T1

	DefaultTimeoutTestT3 = 20 * time.Second
	TimeoutTestT3Min     = 1 * time.Second
	TimeoutTestT3Max     = 172800 * time.Second // 48 hours

	TimeoutSendLinkMsgMin     = 1 * time.Millisecond
	TimeoutSendLinkMsgMax     = 10 * time.Second
	DefaultTimeoutSendLinkMsg = 200 * time.Millisecond

	DefaultMaxSendQueueSize = 100
)

// Config defines an IEC 60870-5-103 primary station configuration.
// The link operates in unbalanced mode only (the 103 standard defines no
// balanced procedure). The link address and the ASDU common address are one
// octet and conventionally equal.
type Config struct {
	// Serial port settings.
	Serial SerialConfig

	// LinkAddress is the address of the (single/default) secondary station.
	// More stations are added with ClientOption.AddSecondaryAddress.
	LinkAddress byte

	// TimeoutResponseT1 is the response timeout for confirmed frames.
	// Range [1, 255] s, default 10 s.
	TimeoutResponseT1 time.Duration

	// TimeoutRepeatT2 is the retry timeout after the first repetition.
	// Range [1, 255] s and < T1, default 5 s.
	TimeoutRepeatT2 time.Duration

	// TimeoutTestT3 is the idle time before a test-link keep-alive.
	// Range [1 s, 48 h], default 20 s.
	TimeoutTestT3 time.Duration

	// TimeoutSendLinkMsg paces the poll/transmit scheduler: every tick the
	// primary either transmits queued user data or polls the next station
	// for class 1/2 data. Range [1 ms, 10 s], default 200 ms.
	TimeoutSendLinkMsg time.Duration

	// MaxSendQueueSize caps the outbound ASDU queue.
	MaxSendQueueSize int

	// AutoInit, when true (the default configuration), makes the client
	// automatically send a time synchronization followed by a general
	// interrogation whenever a station's link becomes active.
	AutoInit bool
}

// Valid applies defaults and checks configuration validity.
func (sf *Config) Valid() error {
	if sf == nil {
		return errors.New("invalid nil config")
	}
	if sf.Serial.Address == "" {
		return errors.New("serial address (port name) must be configured")
	}
	if sf.Serial.BaudRate <= 0 {
		return errors.New("serial baud rate must be positive")
	}

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
		return errors.New("timeout for sending link message out of range [1ms, 10s]")
	}

	if sf.MaxSendQueueSize < 0 {
		return errors.New("MaxSendQueueSize must be positive")
	}
	if sf.MaxSendQueueSize == 0 {
		sf.MaxSendQueueSize = DefaultMaxSendQueueSize
	}

	return nil
}

// DefaultConfig provides a default CS103 configuration.
// NOTE: SerialConfig must be set explicitly.
func DefaultConfig() Config {
	return Config{
		Serial:             SerialConfig{},
		LinkAddress:        1,
		TimeoutResponseT1:  DefaultTimeoutResponseT1,
		TimeoutRepeatT2:    DefaultTimeoutRepeatT2,
		TimeoutTestT3:      DefaultTimeoutTestT3,
		TimeoutSendLinkMsg: DefaultTimeoutSendLinkMsg,
		MaxSendQueueSize:   DefaultMaxSendQueueSize,
		AutoInit:           true,
	}
}

// Copyright 2025 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs101

import (
	"errors"
)

// error defined
var (
	ErrUseClosedConnection = errors.New("use of closed connection")
	ErrBufferFulled        = errors.New("buffer is full")
	ErrNotActive           = errors.New("server or client is not active") // Made slightly more generic
	ErrSendQueueFull       = errors.New("send queue is full")
)

// CS101 specific errors
var (
	// ErrChecksumMismatch defined in frame.go
	ErrFramingError       = errors.New("framing error")
	ErrTimeoutT1          = errors.New("response timeout (T1/T2)") // Combined T1/T2 timeout error
	ErrTimeoutT3          = errors.New("test frame timeout (T3)")
	ErrInvalidFrameLength = errors.New("invalid frame length")
	ErrUnexpectedFrame    = errors.New("unexpected frame received")
	// ErrRetryFailed        = errors.New("retry failed after NACK/timeout") // Example
)

// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs103

import (
	"errors"
)

// Errors of the cs103 package.
var (
	ErrUseClosedConnection = errors.New("use of closed connection")
	ErrBufferFulled        = errors.New("buffer is full")
	ErrNotActive           = errors.New("link is not active")
	ErrSendQueueFull       = errors.New("send queue is full")
	ErrTimeoutT1           = errors.New("response timeout (T1/T2)")

	ErrTypeIDZero       = errors.New("cs103: type identification 0 is not used")
	ErrCauseZero        = errors.New("cs103: cause of transmission 0 is not used")
	ErrLengthOutOfRange = errors.New("cs103: ASDU length exceeds maximum")
	ErrTypeIDNotMatch   = errors.New("cs103: type identification does not match the decoder")
)

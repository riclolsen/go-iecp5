// Copyright 2020 thinkgos (thinkgo@aliyun.com).  All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package asdu

import (
	"errors"
	"fmt"
)

// error defined
var (
	ErrTypeIdentifier = errors.New("asdu: type identification unknown")
	ErrCauseZero      = errors.New("asdu: cause of transmission 0 is not used")
	ErrCommonAddrZero = errors.New("asdu: common address 0 is not used")

	ErrParam           = errors.New("asdu: system parameter out of range")
	ErrInvalidTimeTag  = errors.New("asdu: invalid time tag")
	ErrOriginAddrFit   = errors.New("asdu: originator address not allowed with cause size 1 system parameter")
	ErrCommonAddrFit   = errors.New("asdu: common address exceeds size system parameter")
	ErrInfoObjAddrFit  = errors.New("asdu: information object address exceeds size system parameter")
	ErrInfoObjIndexFit = errors.New("asdu: information object index not in [1, 127]")
	// ErrTrailingOctets reports an ASDU carrying more information object
	// octets than its variable structure qualifier accounts for. Such an
	// ASDU was not produced by a conforming sender: the length is fixed by
	// the frame, the object count by the qualifier and the object size by
	// the type identification, so there is nowhere for a surplus octet to
	// come from. Accepting it means executing the part that parsed and
	// discarding the rest unseen.
	ErrTrailingOctets  = errors.New("asdu: information objects longer than the variable structure qualifier accounts for")
	ErrInroGroupNumFit = errors.New("asdu: interrogation group number exceeds 16")

	// ErrTypeIDZero reports type identification 0, which no range of the
	// standard defines — not the compatible range, not the private one.
	ErrTypeIDZero = errors.New("asdu: type identification 0 is not defined")
	// ErrInfoObjCountZero reports a variable structure qualifier claiming no
	// information objects. An ASDU exists to carry them; one that says it
	// carries none has nothing to say and no receiver can act on it.
	ErrInfoObjCountZero = errors.New("asdu: variable structure qualifier claims 0 information objects")
	// ErrInfoObjSizeMismatch reports a payload that does not match what the
	// variable structure qualifier and the type identification imply. Sending
	// one produces an ASDU the receiver must reject, so it is refused here
	// where the caller can still be told which side is wrong.
	ErrInfoObjSizeMismatch = errors.New("asdu: information object payload does not match the variable structure qualifier")

	ErrLengthOutOfRange = fmt.Errorf("asdu: asdu filed length large than max %d", ASDUSizeMax)
	ErrNotAnyObjInfo    = errors.New("asdu: not any object information")
	ErrTypeIDNotMatch   = errors.New("asdu: type identifier doesn't match call or time tag")

	ErrCmdCause = errors.New("asdu: cause of transmission for command not standard requirement")
)

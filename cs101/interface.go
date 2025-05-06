// Copyright 2025 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs101

import (
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
)

// ServerHandlerInterface is the interface of server (secondary station) handler
// Note: Removed asdu.Connect parameter as it's less relevant for serial implementation details
type ServerHandlerInterface interface {
	InterrogationHandler(*asdu.ASDU, asdu.QualifierOfInterrogation) error
	CounterInterrogationHandler(*asdu.ASDU, asdu.QualifierCountCall) error
	ReadHandler(*asdu.ASDU, asdu.InfoObjAddr) error
	ClockSyncHandler(*asdu.ASDU, time.Time) error
	ResetProcessHandler(*asdu.ASDU, asdu.QualifierOfResetProcessCmd) error
	DelayAcquisitionHandler(*asdu.ASDU, uint16) error
	ASDUHandler(*asdu.ASDU) error
	ASDUHandlerAll(*asdu.ASDU, int) error // allow handling of all AL messages
}

// ClientHandlerInterface is the interface of client (primary station) handler
// Note: Removed asdu.Connect parameter
type ClientHandlerInterface interface {
	InterrogationHandler(*asdu.ASDU) error
	CounterInterrogationHandler(*asdu.ASDU) error
	ReadHandler(*asdu.ASDU) error
	TestCommandHandler(*asdu.ASDU) error
	ClockSyncHandler(*asdu.ASDU) error
	ResetProcessHandler(*asdu.ASDU) error
	DelayAcquisitionHandler(*asdu.ASDU) error
	ASDUHandler(*asdu.ASDU, int) error    // Removed *Server parameter
	ASDUHandlerAll(*asdu.ASDU, int) error // Removed *Server parameter, allow handling of all AL messages
}

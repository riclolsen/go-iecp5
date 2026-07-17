// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs103

// ClientHandlerInterface is the interface of the primary station (master)
// handler. The device (station) that produced an ASDU is identified by
// its common address, available as a.CommonAddr.
//
// Dispatch by received type identification:
//
//	ASDU 1/2  -> TimeTaggedHandler (the cause distinguishes spontaneous
//	             events, general interrogation replies and command
//	             acknowledgements — cause 20/21 with the RII in Sin)
//	ASDU 3/9  -> MeasurandsHandler
//	ASDU 5    -> IdentificationHandler (sent by devices after reset/start)
//	ASDU 8    -> GITerminationHandler (scan number of the completed GI)
//	other     -> ASDUHandler (ASDU 4, 6, generic and disturbance types)
//
// ASDUHandlerAll additionally sees every received ASDU before dispatch.
type ClientHandlerInterface interface {
	TimeTaggedHandler(*ASDU, TimeTaggedInfo) error
	MeasurandsHandler(*ASDU, MeasurandsInfo) error
	IdentificationHandler(*ASDU, IdentificationInfo) error
	GITerminationHandler(*ASDU, byte) error
	ASDUHandler(*ASDU) error
	ASDUHandlerAll(*ASDU) error
}

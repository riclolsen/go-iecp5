// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package filetransfer

import (
	"sync"

	"github.com/riclolsen/go-iecp5/asdu"
)

// Receiver is the controlling station (master) side of a monitor-direction
// file transfer: it calls the directory, selects files, requests their
// sections, verifies each section checksum and assembles the result.
//
// One transfer runs at a time.
type Receiver struct {
	mu    sync.Mutex
	store Store

	autoAccept bool

	// active transfer state
	active     bool
	ca         asdu.CommonAddr
	ioa        asdu.InfoObjAddr
	nof        asdu.NameOfFile
	nos        byte
	sectionBuf []byte
	fileBuf    []byte

	onFile      func(Entry, []byte)
	onDirectory func(asdu.CommonAddr, []asdu.DirectoryInfo)
}

// NewReceiver returns a Receiver storing completed files in store. The
// store may be nil when only the completion callback is used.
func NewReceiver(store Store) *Receiver {
	return &Receiver{store: store, autoAccept: true}
}

// SetAutoAccept controls whether an announced file ([F_FR_NA_1] from the
// outstation) is selected automatically. Default true; set false to decide
// per file and call RequestFile yourself.
func (sf *Receiver) SetAutoAccept(b bool) *Receiver {
	sf.mu.Lock()
	sf.autoAccept = b
	sf.mu.Unlock()
	return sf
}

// SetFileHandler sets the callback invoked with each completed file. The
// data slice belongs to the callback.
func (sf *Receiver) SetFileHandler(f func(Entry, []byte)) *Receiver {
	sf.mu.Lock()
	sf.onFile = f
	sf.mu.Unlock()
	return sf
}

// SetDirectoryHandler sets the callback invoked with each received
// directory listing.
func (sf *Receiver) SetDirectoryHandler(f func(asdu.CommonAddr, []asdu.DirectoryInfo)) *Receiver {
	sf.mu.Lock()
	sf.onDirectory = f
	sf.mu.Unlock()
	return sf
}

// InProgress reports whether a transfer is currently running.
func (sf *Receiver) InProgress() bool {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.active
}

// Abort clears any transfer in progress without notifying the peer.
func (sf *Receiver) Abort() {
	sf.mu.Lock()
	sf.reset()
	sf.mu.Unlock()
}

// reset clears the transfer state. The caller must hold the lock.
func (sf *Receiver) reset() {
	sf.active = false
	sf.sectionBuf = nil
	sf.fileBuf = nil
	sf.nos = 0
	sf.ioa = 0
	sf.nof = 0
}

// RequestDirectory calls the directory of the given common address
// ([F_SC_NA_1] with SCQ = default).
func (sf *Receiver) RequestDirectory(c asdu.Connect, ca asdu.CommonAddr) error {
	return asdu.CallOrSelectFile(c, asdu.CauseOfTransmission{Cause: asdu.Request}, ca,
		asdu.CallOrSelectFileInfo{
			Ioa: asdu.InfoObjAddrIrrelevant,
			Nof: asdu.FileDefault,
			Scq: asdu.SelectAndCallQualifier{Action: asdu.SCQDefault},
		})
}

// RequestFile selects a file for transfer ([F_SC_NA_1] with SCQ = select
// file). The outstation answers with the first section, and the transfer
// then runs on its own until the file is complete.
func (sf *Receiver) RequestFile(c asdu.Connect, ca asdu.CommonAddr, ioa asdu.InfoObjAddr, nof asdu.NameOfFile) error {
	sf.mu.Lock()
	if sf.active {
		sf.mu.Unlock()
		return ErrBusy
	}
	sf.active = true
	sf.ca, sf.ioa, sf.nof = ca, ioa, nof
	sf.nos = 0
	sf.sectionBuf = nil
	sf.fileBuf = nil
	sf.mu.Unlock()

	err := asdu.CallOrSelectFile(c, asdu.CauseOfTransmission{Cause: asdu.FileTransfer}, ca,
		asdu.CallOrSelectFileInfo{
			Ioa: ioa,
			Nof: nof,
			Scq: asdu.SelectAndCallQualifier{Action: asdu.SCQSelectFile},
		})
	if err != nil {
		sf.Abort()
	}
	return err
}

// Handle processes one received ASDU. It reports whether the ASDU belonged
// to the file transfer service; when it returns false the caller should
// continue with its own dispatch.
func (sf *Receiver) Handle(c asdu.Connect, a *asdu.ASDU) (bool, error) {
	if !isFileASDU(a.Type) {
		return false, nil
	}

	sf.mu.Lock()
	actions, done, err := sf.step(a)
	sf.mu.Unlock()

	// Callbacks and sends run outside the lock.
	if done != nil {
		if sf.store != nil {
			if writeErr := sf.store.Write(done.entry.Ioa, done.entry.Nof, done.data); writeErr != nil && err == nil {
				err = writeErr
			}
		}
		if sf.onFile != nil {
			sf.onFile(done.entry, done.data)
		}
	}
	for _, act := range actions {
		if sendErr := act(c); sendErr != nil {
			sf.Abort()
			return true, sendErr
		}
	}
	return true, err
}

// completed carries a finished file out of the locked state transition.
type completed struct {
	entry Entry
	data  []byte
}

// step performs the state transition for one ASDU. The caller must hold the
// lock.
func (sf *Receiver) step(a *asdu.ASDU) ([]action, *completed, error) {
	switch a.Type {
	case asdu.F_DR_TA_1:
		dir := a.GetFileDirectory()
		ca := a.CommonAddr
		cb := sf.onDirectory
		if cb == nil {
			return nil, nil, nil
		}
		// Deliver via an action so the callback runs outside the lock.
		return []action{func(asdu.Connect) error { cb(ca, dir); return nil }}, nil, nil

	case asdu.F_FR_NA_1:
		info := a.GetFileReady()
		if info.Frq.IsNegative {
			sf.reset()
			return nil, nil, ErrFileNotFound
		}
		if !sf.autoAccept || sf.active {
			return nil, nil, nil
		}
		sf.active = true
		sf.ca, sf.ioa, sf.nof = a.CommonAddr, info.Ioa, info.Nof
		sf.nos = 0
		sf.sectionBuf = nil
		sf.fileBuf = nil
		ca, ioa, nof := sf.ca, sf.ioa, sf.nof
		return []action{func(c asdu.Connect) error {
			return asdu.CallOrSelectFile(c, asdu.CauseOfTransmission{Cause: asdu.FileTransfer}, ca,
				asdu.CallOrSelectFileInfo{
					Ioa: ioa, Nof: nof,
					Scq: asdu.SelectAndCallQualifier{Action: asdu.SCQSelectFile},
				})
		}}, nil, nil

	case asdu.F_SR_NA_1:
		info := a.GetSectionReady()
		if !sf.active || info.Nof != sf.nof {
			return nil, nil, ErrNoTransfer
		}
		if info.Srq.IsNotReady {
			sf.reset()
			return nil, nil, ErrNoTransfer
		}
		sf.nos = info.Nos
		sf.sectionBuf = nil
		ca, ioa, nof, nos := sf.ca, sf.ioa, sf.nof, sf.nos
		return []action{func(c asdu.Connect) error {
			return asdu.CallOrSelectFile(c, asdu.CauseOfTransmission{Cause: asdu.FileTransfer}, ca,
				asdu.CallOrSelectFileInfo{
					Ioa: ioa, Nof: nof, Nos: nos,
					Scq: asdu.SelectAndCallQualifier{Action: asdu.SCQRequestSection},
				})
		}}, nil, nil

	case asdu.F_SG_NA_1:
		info := a.GetFileSegment()
		if !sf.active || info.Nof != sf.nof || info.Nos != sf.nos {
			return nil, nil, ErrNoTransfer
		}
		sf.sectionBuf = append(sf.sectionBuf, info.Segment...)
		return nil, nil, nil

	case asdu.F_LS_NA_1:
		return sf.onLastSectionOrSegment(a)

	case asdu.F_AF_NA_1:
		// Control-direction ASDU: produced by this component, not received.
		return nil, nil, ErrUnsupported

	case asdu.F_SC_NA_1:
		return nil, nil, ErrUnsupported
	}
	return nil, nil, nil
}

func (sf *Receiver) onLastSectionOrSegment(a *asdu.ASDU) ([]action, *completed, error) {
	info := a.GetLastSectionOrSegment()
	if !sf.active || info.Nof != sf.nof {
		return nil, nil, ErrNoTransfer
	}
	ca, ioa, nof := sf.ca, sf.ioa, sf.nof

	switch info.Lsq {
	case asdu.LSQSectionWithoutDeactivate, asdu.LSQSectionWithDeactivate:
		// End of a section: verify its checksum before acknowledging.
		if asdu.FileChecksum(sf.sectionBuf) != info.Chs {
			nos := sf.nos
			sf.sectionBuf = nil
			return []action{func(c asdu.Connect) error {
				return asdu.AckFileOrSection(c, asdu.CauseOfTransmission{Cause: asdu.FileTransfer}, ca,
					asdu.AckFileOrSectionInfo{
						Ioa: ioa, Nof: nof, Nos: nos,
						Afq: asdu.AckFileOrSectionQualifier{
							Action: asdu.AFQNegAckSection,
							Error:  asdu.FileErrChecksumFailed,
						},
					})
			}}, nil, ErrChecksum
		}
		sf.fileBuf = append(sf.fileBuf, sf.sectionBuf...)
		sf.sectionBuf = nil
		nos := sf.nos
		return []action{func(c asdu.Connect) error {
			return asdu.AckFileOrSection(c, asdu.CauseOfTransmission{Cause: asdu.FileTransfer}, ca,
				asdu.AckFileOrSectionInfo{
					Ioa: ioa, Nof: nof, Nos: nos,
					Afq: asdu.AckFileOrSectionQualifier{Action: asdu.AFQPosAckSection},
				})
		}}, nil, nil

	case asdu.LSQFileWithoutDeactivate, asdu.LSQFileWithDeactivate:
		// End of the file: acknowledge and deliver.
		data := sf.fileBuf
		nos := info.Nos
		done := &completed{
			entry: Entry{Ioa: ioa, Nof: nof, Size: uint32(len(data))},
			data:  data,
		}
		sf.reset()
		return []action{func(c asdu.Connect) error {
			return asdu.AckFileOrSection(c, asdu.CauseOfTransmission{Cause: asdu.FileTransfer}, ca,
				asdu.AckFileOrSectionInfo{
					Ioa: ioa, Nof: nof, Nos: nos,
					Afq: asdu.AckFileOrSectionQualifier{Action: asdu.AFQPosAckFile},
				})
		}}, done, nil
	}
	return nil, nil, nil
}

// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package filetransfer

import (
	"sync"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
)

// DefaultSectionSize is the default number of octets per section.
const DefaultSectionSize = 4096

// Sender is the controlled station (outstation) side of a monitor-direction
// file transfer: it announces files, answers directory calls, and serves the
// sections and segments a controlling station asks for.
//
// One transfer runs at a time, which is what the standard's procedure
// allows per common address.
type Sender struct {
	mu          sync.Mutex
	store       Store
	sectionSize int

	// active transfer state
	active   bool
	ca       asdu.CommonAddr
	ioa      asdu.InfoObjAddr
	nof      asdu.NameOfFile
	sections [][]byte
	cur      int // index of the section being served
}

// NewSender returns a Sender serving files out of store.
func NewSender(store Store) *Sender {
	return &Sender{store: store, sectionSize: DefaultSectionSize}
}

// SetSectionSize sets the maximum number of octets per section
// (default DefaultSectionSize). Sections are the unit that carries a
// checksum, so smaller sections detect corruption earlier at the cost of
// more round trips.
func (sf *Sender) SetSectionSize(n int) *Sender {
	sf.mu.Lock()
	if n > 0 {
		sf.sectionSize = n
	}
	sf.mu.Unlock()
	return sf
}

// InProgress reports whether a transfer is currently running.
func (sf *Sender) InProgress() bool {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.active
}

// Abort clears any transfer in progress without notifying the peer.
func (sf *Sender) Abort() {
	sf.mu.Lock()
	sf.reset()
	sf.mu.Unlock()
}

// reset clears the transfer state. The caller must hold the lock.
func (sf *Sender) reset() {
	sf.active = false
	sf.sections = nil
	sf.cur = 0
	sf.ioa = 0
	sf.nof = 0
}

// Offer announces that a file is ready for transfer by sending
// [F_FR_NA_1]. A controlling station typically answers by selecting the
// file, which starts the transfer.
func (sf *Sender) Offer(c asdu.Connect, ca asdu.CommonAddr, ioa asdu.InfoObjAddr, nof asdu.NameOfFile) error {
	data, err := sf.store.Read(ioa, nof)
	if err != nil {
		return err
	}
	return asdu.FileReady(c, asdu.CauseOfTransmission{Cause: asdu.FileTransfer}, ca,
		asdu.FileReadyInfo{
			Ioa:          ioa,
			Nof:          nof,
			LengthOfFile: uint32(len(data)),
			Frq:          asdu.FileReadyQualifier{},
		})
}

// Handle processes one received ASDU. It reports whether the ASDU belonged
// to the file transfer service; when it returns false the caller should
// continue with its own dispatch.
func (sf *Sender) Handle(c asdu.Connect, a *asdu.ASDU) (bool, error) {
	if !isFileASDU(a.Type) {
		return false, nil
	}

	sf.mu.Lock()
	actions, err := sf.step(c, a)
	sf.mu.Unlock()

	// Actions run outside the lock so that a synchronous transport cannot
	// re-enter this state machine while it is locked.
	for _, act := range actions {
		if sendErr := act(c); sendErr != nil {
			sf.Abort()
			return true, sendErr
		}
	}
	return true, err
}

// step performs the state transition for one ASDU and returns the ASDUs to
// transmit. The caller must hold the lock.
func (sf *Sender) step(c asdu.Connect, a *asdu.ASDU) ([]action, error) {
	switch a.Type {
	case asdu.F_SC_NA_1:
		return sf.onCallOrSelect(c, a)
	case asdu.F_AF_NA_1:
		return sf.onAck(c, a)
	case asdu.F_FR_NA_1, asdu.F_SR_NA_1, asdu.F_LS_NA_1, asdu.F_SG_NA_1, asdu.F_DR_TA_1:
		// Monitor-direction ASDUs: this component produces them, a
		// controlled station does not receive them.
		return nil, ErrUnsupported
	}
	return nil, nil
}

func (sf *Sender) onCallOrSelect(c asdu.Connect, a *asdu.ASDU) ([]action, error) {
	req := a.GetCallOrSelectFile()
	ca := a.CommonAddr

	switch req.Scq.Action {
	case asdu.SCQDefault: // call directory
		return sf.directoryActions(ca)

	case asdu.SCQSelectFile, asdu.SCQRequestFile:
		data, err := sf.store.Read(req.Ioa, req.Nof)
		if err != nil {
			return sf.negAckFileActions(ca, req, asdu.FileErrUnexpectedNameOfFile), err
		}
		sf.active = true
		sf.ca, sf.ioa, sf.nof = ca, req.Ioa, req.Nof
		sf.sections = splitSections(data, sf.sectionSize)
		sf.cur = 0
		return sf.sectionReadyActions(), nil

	case asdu.SCQSelectSection:
		if !sf.active || req.Nof != sf.nof {
			return sf.negAckFileActions(ca, req, asdu.FileErrUnexpectedNameOfFile), ErrNoTransfer
		}
		idx := int(req.Nos) - 1
		if idx < 0 || idx >= len(sf.sections) {
			return sf.negAckSectionActions(ca, req, asdu.FileErrUnexpectedNameOfSection), ErrNoTransfer
		}
		sf.cur = idx
		return sf.sectionReadyActions(), nil

	case asdu.SCQRequestSection:
		if !sf.active || req.Nof != sf.nof {
			return sf.negAckFileActions(ca, req, asdu.FileErrUnexpectedNameOfFile), ErrNoTransfer
		}
		if req.Nos != 0 {
			idx := int(req.Nos) - 1
			if idx < 0 || idx >= len(sf.sections) {
				return sf.negAckSectionActions(ca, req, asdu.FileErrUnexpectedNameOfSection), ErrNoTransfer
			}
			sf.cur = idx
		}
		return sf.sectionDataActions(), nil

	case asdu.SCQDeactivateFile, asdu.SCQDeactivateSection:
		sf.reset()
		return nil, nil

	case asdu.SCQDeleteFile:
		err := sf.store.Delete(req.Ioa, req.Nof)
		if err != nil {
			return sf.negAckFileActions(ca, req, asdu.FileErrUnexpectedNameOfFile), err
		}
		if sf.active && sf.ioa == req.Ioa && sf.nof == req.Nof {
			sf.reset()
		}
		return nil, nil
	}
	return nil, nil
}

func (sf *Sender) onAck(c asdu.Connect, a *asdu.ASDU) ([]action, error) {
	ack := a.GetAckFileOrSection()
	if !sf.active {
		return nil, ErrNoTransfer
	}

	switch ack.Afq.Action {
	case asdu.AFQPosAckSection:
		sf.cur++
		if sf.cur < len(sf.sections) {
			return sf.sectionReadyActions(), nil
		}
		// all sections acknowledged: close the file
		return sf.lastSectionActions(), nil

	case asdu.AFQNegAckSection:
		// The controlling station rejected the section (typically a
		// checksum mismatch): serve it again.
		return sf.sectionDataActions(), nil

	case asdu.AFQPosAckFile:
		sf.reset()
		return nil, nil

	case asdu.AFQNegAckFile:
		sf.reset()
		return nil, nil
	}
	return nil, nil
}

// directoryActions builds the directory reply [F_DR_TA_1].
func (sf *Sender) directoryActions(ca asdu.CommonAddr) ([]action, error) {
	entries, err := sf.store.List()
	if err != nil {
		return nil, err
	}
	infos := make([]asdu.DirectoryInfo, 0, len(entries))
	for i, e := range entries {
		t := e.Time
		if t.IsZero() {
			t = time.Now()
		}
		infos = append(infos, asdu.DirectoryInfo{
			Ioa:          e.Ioa,
			Nof:          e.Nof,
			LengthOfFile: e.Size,
			Sof: asdu.StatusOfFile{
				IsLastFileOfDirectory: i == len(entries)-1,
				IsDirectory:           e.IsDirectory,
			},
			Time: t,
		})
	}
	if len(infos) == 0 {
		// Nothing to report; the standard has no empty-directory ASDU, so
		// stay silent rather than sending a malformed one.
		return nil, nil
	}
	return []action{func(c asdu.Connect) error {
		return asdu.FileDirectory(c, asdu.CauseOfTransmission{Cause: asdu.Request}, ca, infos...)
	}}, nil
}

// sectionReadyActions announces the current section [F_SR_NA_1].
func (sf *Sender) sectionReadyActions() []action {
	info := asdu.SectionReadyInfo{
		Ioa:             sf.ioa,
		Nof:             sf.nof,
		Nos:             byte(sf.cur + 1),
		LengthOfSection: uint32(len(sf.sections[sf.cur])),
	}
	ca := sf.ca
	return []action{func(c asdu.Connect) error {
		return asdu.SectionReady(c, asdu.CauseOfTransmission{Cause: asdu.FileTransfer}, ca, info)
	}}
}

// sectionDataActions transmits the current section as segments followed by
// the last-segment marker carrying the section checksum.
func (sf *Sender) sectionDataActions() []action {
	section := sf.sections[sf.cur]
	nos := byte(sf.cur + 1)
	ioa, nof, ca := sf.ioa, sf.nof, sf.ca
	chs := asdu.FileChecksum(section)

	acts := []action{func(c asdu.Connect) error {
		max := c.Params().MaxSegmentSize()
		if max <= 0 {
			return asdu.ErrParam
		}
		for off := 0; off < len(section); off += max {
			end := off + max
			if end > len(section) {
				end = len(section)
			}
			err := asdu.FileSegment(c, asdu.CauseOfTransmission{Cause: asdu.FileTransfer}, ca,
				asdu.SegmentInfo{Ioa: ioa, Nof: nof, Nos: nos, Segment: section[off:end]})
			if err != nil {
				return err
			}
		}
		return nil
	}}

	acts = append(acts, func(c asdu.Connect) error {
		return asdu.LastSectionOrSegment(c, asdu.CauseOfTransmission{Cause: asdu.FileTransfer}, ca,
			asdu.LastSectionOrSegmentInfo{
				Ioa: ioa, Nof: nof, Nos: nos,
				Lsq: asdu.LSQSectionWithoutDeactivate,
				Chs: chs,
			})
	})
	return acts
}

// lastSectionActions closes the file [F_LS_NA_1 with LSQ = file].
func (sf *Sender) lastSectionActions() []action {
	info := asdu.LastSectionOrSegmentInfo{
		Ioa: sf.ioa,
		Nof: sf.nof,
		Nos: byte(len(sf.sections)),
		Lsq: asdu.LSQFileWithoutDeactivate,
	}
	ca := sf.ca
	return []action{func(c asdu.Connect) error {
		return asdu.LastSectionOrSegment(c, asdu.CauseOfTransmission{Cause: asdu.FileTransfer}, ca, info)
	}}
}

func (sf *Sender) negAckFileActions(ca asdu.CommonAddr, req asdu.CallOrSelectFileInfo, fe asdu.FileError) []action {
	info := asdu.AckFileOrSectionInfo{
		Ioa: req.Ioa, Nof: req.Nof, Nos: req.Nos,
		Afq: asdu.AckFileOrSectionQualifier{Action: asdu.AFQNegAckFile, Error: fe},
	}
	return []action{func(c asdu.Connect) error {
		return asdu.AckFileOrSection(c, asdu.CauseOfTransmission{Cause: asdu.FileTransfer}, ca, info)
	}}
}

func (sf *Sender) negAckSectionActions(ca asdu.CommonAddr, req asdu.CallOrSelectFileInfo, fe asdu.FileError) []action {
	info := asdu.AckFileOrSectionInfo{
		Ioa: req.Ioa, Nof: req.Nof, Nos: req.Nos,
		Afq: asdu.AckFileOrSectionQualifier{Action: asdu.AFQNegAckSection, Error: fe},
	}
	return []action{func(c asdu.Connect) error {
		return asdu.AckFileOrSection(c, asdu.CauseOfTransmission{Cause: asdu.FileTransfer}, ca, info)
	}}
}

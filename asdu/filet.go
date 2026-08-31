// Copyright 2020 thinkgos (thinkgo@aliyun.com).  All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package asdu

import (
	"time"
)

// Application Service Data Unit for file transfer.
// See companion standard 101, subclass 7.3.6 (ASDUs) and subclass 7.4.11
// (transfer procedures).
//
//	F_FR_NA_1 <120> file ready
//	F_SR_NA_1 <121> section ready
//	F_SC_NA_1 <122> call directory, select file, call file, call section
//	F_LS_NA_1 <123> last section, last segment
//	F_AF_NA_1 <124> ack file, ack section
//	F_SG_NA_1 <125> segment
//	F_DR_TA_1 <126> directory
//
// F_SC_NB_1 <127> (query log) is not implemented.

// NameOfFile is the name (identification) of a file.
// See companion standard 101, subclass 7.2.6.33.
type NameOfFile uint16

// NameOfFile defined in the compatible range. Values above are available
// for private assignment.
const (
	FileDefault              NameOfFile = 0
	FileTransparent          NameOfFile = 1
	FileDisturbanceData      NameOfFile = 2
	FileSequencesOfEvents    NameOfFile = 3
	FileSequencesOfAnalogues NameOfFile = 4
)

// FileError is the error code carried in the high nibble of the select and
// call qualifier (SCQ) and of the acknowledge qualifier (AFQ).
// See companion standard 101, subclass 7.2.6.31 and 7.2.6.33.
type FileError byte

// FileError defined.
const (
	FileErrNone                    FileError = iota // 0: default, no error
	FileErrMemoryUnavailable                        // 1: requested memory space not available
	FileErrChecksumFailed                           // 2: checksum failed
	FileErrUnexpectedService                        // 3: unexpected communication service
	FileErrUnexpectedNameOfFile                     // 4: unexpected name of file
	FileErrUnexpectedNameOfSection                  // 5: unexpected name of section
)

// SCQAction is the action carried in the low nibble of the select and call
// qualifier. See companion standard 101, subclass 7.2.6.31.
type SCQAction byte

// SCQAction defined.
const (
	SCQDefault           SCQAction = iota // 0: default (call directory)
	SCQSelectFile                         // 1: select file
	SCQRequestFile                        // 2: request file
	SCQDeactivateFile                     // 3: deactivate file
	SCQDeleteFile                         // 4: delete file
	SCQSelectSection                      // 5: select section
	SCQRequestSection                     // 6: request section
	SCQDeactivateSection                  // 7: deactivate section
)

// AFQAction is the action carried in the low nibble of the acknowledge file
// or section qualifier. See companion standard 101, subclass 7.2.6.33.
type AFQAction byte

// AFQAction defined.
const (
	AFQNotUsed       AFQAction = iota // 0: not used
	AFQPosAckFile                     // 1: positive acknowledge of file transfer
	AFQNegAckFile                     // 2: negative acknowledge of file transfer
	AFQPosAckSection                  // 3: positive acknowledge of section transfer
	AFQNegAckSection                  // 4: negative acknowledge of section transfer
)

// LastSectionQualifier is the last section or segment qualifier (LSQ).
// See companion standard 101, subclass 7.2.6.32.
type LastSectionQualifier byte

// LastSectionQualifier defined.
const (
	LSQNotUsed                  LastSectionQualifier = iota // 0: not used
	LSQFileWithoutDeactivate                                // 1: file transfer without deactivation
	LSQFileWithDeactivate                                   // 2: file transfer with deactivation
	LSQSectionWithoutDeactivate                             // 3: section transfer without deactivation
	LSQSectionWithDeactivate                                // 4: section transfer with deactivation
)

// FileReadyQualifier is the file ready qualifier (FRQ).
// See companion standard 101, subclass 7.2.6.29.
// Qual: bit1..bit7, IsNegative: bit8 (P/N).
type FileReadyQualifier struct {
	Qual       byte
	IsNegative bool
}

// ParseFileReadyQualifier parses a byte to FileReadyQualifier.
func ParseFileReadyQualifier(b byte) FileReadyQualifier {
	return FileReadyQualifier{Qual: b & 0x7f, IsNegative: b&0x80 == 0x80}
}

// Value encodes FileReadyQualifier to a byte.
func (sf FileReadyQualifier) Value() byte {
	v := sf.Qual & 0x7f
	if sf.IsNegative {
		v |= 0x80
	}
	return v
}

// SectionReadyQualifier is the section ready qualifier (SRQ).
// See companion standard 101, subclass 7.2.6.30.
// Qual: bit1..bit7, IsNotReady: bit8 (1 = section not ready to load).
type SectionReadyQualifier struct {
	Qual       byte
	IsNotReady bool
}

// ParseSectionReadyQualifier parses a byte to SectionReadyQualifier.
func ParseSectionReadyQualifier(b byte) SectionReadyQualifier {
	return SectionReadyQualifier{Qual: b & 0x7f, IsNotReady: b&0x80 == 0x80}
}

// Value encodes SectionReadyQualifier to a byte.
func (sf SectionReadyQualifier) Value() byte {
	v := sf.Qual & 0x7f
	if sf.IsNotReady {
		v |= 0x80
	}
	return v
}

// SelectAndCallQualifier is the select and call qualifier (SCQ).
// See companion standard 101, subclass 7.2.6.31.
// Action: bit1..bit4, Error: bit5..bit8.
type SelectAndCallQualifier struct {
	Action SCQAction
	Error  FileError
}

// ParseSelectAndCallQualifier parses a byte to SelectAndCallQualifier.
func ParseSelectAndCallQualifier(b byte) SelectAndCallQualifier {
	return SelectAndCallQualifier{
		Action: SCQAction(b & 0x0f),
		Error:  FileError(b >> 4),
	}
}

// Value encodes SelectAndCallQualifier to a byte.
func (sf SelectAndCallQualifier) Value() byte {
	return byte(sf.Action)&0x0f | byte(sf.Error)<<4
}

// AckFileOrSectionQualifier is the acknowledge file or section qualifier (AFQ).
// See companion standard 101, subclass 7.2.6.33.
// Action: bit1..bit4, Error: bit5..bit8.
type AckFileOrSectionQualifier struct {
	Action AFQAction
	Error  FileError
}

// ParseAckFileOrSectionQualifier parses a byte to AckFileOrSectionQualifier.
func ParseAckFileOrSectionQualifier(b byte) AckFileOrSectionQualifier {
	return AckFileOrSectionQualifier{
		Action: AFQAction(b & 0x0f),
		Error:  FileError(b >> 4),
	}
}

// Value encodes AckFileOrSectionQualifier to a byte.
func (sf AckFileOrSectionQualifier) Value() byte {
	return byte(sf.Action)&0x0f | byte(sf.Error)<<4
}

// StatusOfFile is the status of file (SOF).
// See companion standard 101, subclass 7.2.6.38.
// Status: bit1..bit5, IsLastFileOfDirectory: bit6 (LFD),
// IsDirectory: bit7 (FOR), IsTransferActive: bit8 (FA).
type StatusOfFile struct {
	Status                byte
	IsLastFileOfDirectory bool
	IsDirectory           bool
	IsTransferActive      bool
}

// ParseStatusOfFile parses a byte to StatusOfFile.
func ParseStatusOfFile(b byte) StatusOfFile {
	return StatusOfFile{
		Status:                b & 0x1f,
		IsLastFileOfDirectory: b&0x20 == 0x20,
		IsDirectory:           b&0x40 == 0x40,
		IsTransferActive:      b&0x80 == 0x80,
	}
}

// Value encodes StatusOfFile to a byte.
func (sf StatusOfFile) Value() byte {
	v := sf.Status & 0x1f
	if sf.IsLastFileOfDirectory {
		v |= 0x20
	}
	if sf.IsDirectory {
		v |= 0x40
	}
	if sf.IsTransferActive {
		v |= 0x80
	}
	return v
}

// LengthOfFileMax is the largest value the 3 octet length of file (LOF)
// information element can carry.
const LengthOfFileMax = 1<<24 - 1

// AppendLengthOfFile appends a 3 octet length of file (LOF) to the
// information object.
func (sf *ASDU) AppendLengthOfFile(n uint32) *ASDU {
	sf.InfoObj = append(sf.InfoObj, byte(n), byte(n>>8), byte(n>>16))
	return sf
}

// DecodeLengthOfFile decodes a 3 octet length of file (LOF).
func (sf *ASDU) DecodeLengthOfFile() uint32 {
	v := uint32(sf.InfoObj[0]) | uint32(sf.InfoObj[1])<<8 | uint32(sf.InfoObj[2])<<16
	sf.InfoObj = sf.InfoObj[3:]
	return v
}

// AppendNameOfFile appends a 2 octet name of file (NOF).
func (sf *ASDU) AppendNameOfFile(nof NameOfFile) *ASDU {
	return sf.AppendUint16(uint16(nof))
}

// DecodeNameOfFile decodes a 2 octet name of file (NOF).
func (sf *ASDU) DecodeNameOfFile() NameOfFile {
	return NameOfFile(sf.DecodeUint16())
}

// MaxSegmentSize returns the largest segment payload that fits one
// F_SG_NA_1 ASDU with the given parameters.
func (sf Params) MaxSegmentSize() int {
	// identifier + IOA + NOF(2) + NOS(1) + LOS(1)
	n := ASDUSizeMax - sf.IdentifierSize() - sf.InfoObjAddrSize - 4
	if n > 255 { // LOS is a single octet
		n = 255
	}
	if n < 0 {
		n = 0
	}
	return n
}

// FileChecksum computes the checksum (CHS) of a section: the arithmetic sum
// of all its segment octets, modulo 256.
// See companion standard 101, subclass 7.2.6.35.
func FileChecksum(b []byte) byte {
	var sum byte
	for _, v := range b {
		sum += v
	}
	return sum
}

// checkFileCause validates the cause of transmission of a file transfer
// ASDU and the system parameters.
func checkFileCause(c Connect, coa CauseOfTransmission, allowed ...Cause) error {
	ok := false
	for _, v := range allowed {
		if coa.Cause == v {
			ok = true
			break
		}
	}
	if !ok {
		return ErrCmdCause
	}
	return c.Params().Valid()
}

// FileReadyInfo is the information object of [F_FR_NA_1].
type FileReadyInfo struct {
	Ioa InfoObjAddr
	Nof NameOfFile
	// LengthOfFile is the total length of the file in octets.
	LengthOfFile uint32
	Frq          FileReadyQualifier
}

// FileReady sends a type identification [F_FR_NA_1]. File ready, only a
// single information object (SQ = 0).
// [F_FR_NA_1] See companion standard 101, subclass 7.3.6.1
// The cause of transmission (coa) is used for
// monitor direction:
// <13> := file transfer
func FileReady(c Connect, coa CauseOfTransmission, ca CommonAddr, info FileReadyInfo) error {
	if err := checkFileCause(c, coa, FileTransfer); err != nil {
		return err
	}
	if info.LengthOfFile > LengthOfFileMax {
		return ErrLengthOutOfRange
	}

	u := NewASDU(c.Params(), Identifier{
		F_FR_NA_1,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})
	if err := u.AppendInfoObjAddr(info.Ioa); err != nil {
		return err
	}
	u.AppendNameOfFile(info.Nof).
		AppendLengthOfFile(info.LengthOfFile).
		AppendBytes(info.Frq.Value())
	return c.Send(u)
}

// GetFileReady [F_FR_NA_1] gets the file ready information object.
func (sf *ASDU) GetFileReady() FileReadyInfo {
	defer sf.restoreInfoObj(sf.InfoObj)
	return FileReadyInfo{
		Ioa:          sf.DecodeInfoObjAddr(),
		Nof:          sf.DecodeNameOfFile(),
		LengthOfFile: sf.DecodeLengthOfFile(),
		Frq:          ParseFileReadyQualifier(sf.DecodeByte()),
	}
}

// SectionReadyInfo is the information object of [F_SR_NA_1].
type SectionReadyInfo struct {
	Ioa InfoObjAddr
	Nof NameOfFile
	// Nos is the name (number) of the section.
	Nos byte
	// LengthOfSection is the length of this section in octets.
	LengthOfSection uint32
	Srq             SectionReadyQualifier
}

// SectionReady sends a type identification [F_SR_NA_1]. Section ready, only
// a single information object (SQ = 0).
// [F_SR_NA_1] See companion standard 101, subclass 7.3.6.2
// The cause of transmission (coa) is used for
// monitor direction:
// <13> := file transfer
func SectionReady(c Connect, coa CauseOfTransmission, ca CommonAddr, info SectionReadyInfo) error {
	if err := checkFileCause(c, coa, FileTransfer); err != nil {
		return err
	}
	if info.LengthOfSection > LengthOfFileMax {
		return ErrLengthOutOfRange
	}

	u := NewASDU(c.Params(), Identifier{
		F_SR_NA_1,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})
	if err := u.AppendInfoObjAddr(info.Ioa); err != nil {
		return err
	}
	u.AppendNameOfFile(info.Nof).
		AppendBytes(info.Nos).
		AppendLengthOfFile(info.LengthOfSection).
		AppendBytes(info.Srq.Value())
	return c.Send(u)
}

// GetSectionReady [F_SR_NA_1] gets the section ready information object.
func (sf *ASDU) GetSectionReady() SectionReadyInfo {
	defer sf.restoreInfoObj(sf.InfoObj)
	return SectionReadyInfo{
		Ioa:             sf.DecodeInfoObjAddr(),
		Nof:             sf.DecodeNameOfFile(),
		Nos:             sf.DecodeByte(),
		LengthOfSection: sf.DecodeLengthOfFile(),
		Srq:             ParseSectionReadyQualifier(sf.DecodeByte()),
	}
}

// CallOrSelectFileInfo is the information object of [F_SC_NA_1].
type CallOrSelectFileInfo struct {
	Ioa InfoObjAddr
	Nof NameOfFile
	Nos byte
	Scq SelectAndCallQualifier
}

// CallOrSelectFile sends a type identification [F_SC_NA_1]. Call directory,
// select file, call file, call section; only a single information
// object (SQ = 0).
// [F_SC_NA_1] See companion standard 101, subclass 7.3.6.3
// The cause of transmission (coa) is used for
// control direction:
// <5> := request (call directory)
// <13> := file transfer
func CallOrSelectFile(c Connect, coa CauseOfTransmission, ca CommonAddr, info CallOrSelectFileInfo) error {
	if err := checkFileCause(c, coa, Request, FileTransfer); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		F_SC_NA_1,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})
	if err := u.AppendInfoObjAddr(info.Ioa); err != nil {
		return err
	}
	u.AppendNameOfFile(info.Nof).
		AppendBytes(info.Nos, info.Scq.Value())
	return c.Send(u)
}

// GetCallOrSelectFile [F_SC_NA_1] gets the call/select information object.
func (sf *ASDU) GetCallOrSelectFile() CallOrSelectFileInfo {
	defer sf.restoreInfoObj(sf.InfoObj)
	return CallOrSelectFileInfo{
		Ioa: sf.DecodeInfoObjAddr(),
		Nof: sf.DecodeNameOfFile(),
		Nos: sf.DecodeByte(),
		Scq: ParseSelectAndCallQualifier(sf.DecodeByte()),
	}
}

// LastSectionOrSegmentInfo is the information object of [F_LS_NA_1].
type LastSectionOrSegmentInfo struct {
	Ioa InfoObjAddr
	Nof NameOfFile
	Nos byte
	Lsq LastSectionQualifier
	// Chs is the checksum of the section; it is 0 when Lsq marks the end
	// of a file rather than the end of a section.
	Chs byte
}

// LastSectionOrSegment sends a type identification [F_LS_NA_1]. Last
// section, last segment; only a single information object (SQ = 0).
// [F_LS_NA_1] See companion standard 101, subclass 7.3.6.4
// The cause of transmission (coa) is used for
// monitor direction:
// <13> := file transfer
func LastSectionOrSegment(c Connect, coa CauseOfTransmission, ca CommonAddr, info LastSectionOrSegmentInfo) error {
	if err := checkFileCause(c, coa, FileTransfer); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		F_LS_NA_1,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})
	if err := u.AppendInfoObjAddr(info.Ioa); err != nil {
		return err
	}
	u.AppendNameOfFile(info.Nof).
		AppendBytes(info.Nos, byte(info.Lsq), info.Chs)
	return c.Send(u)
}

// GetLastSectionOrSegment [F_LS_NA_1] gets the last section/segment
// information object.
func (sf *ASDU) GetLastSectionOrSegment() LastSectionOrSegmentInfo {
	defer sf.restoreInfoObj(sf.InfoObj)
	return LastSectionOrSegmentInfo{
		Ioa: sf.DecodeInfoObjAddr(),
		Nof: sf.DecodeNameOfFile(),
		Nos: sf.DecodeByte(),
		Lsq: LastSectionQualifier(sf.DecodeByte()),
		Chs: sf.DecodeByte(),
	}
}

// AckFileOrSectionInfo is the information object of [F_AF_NA_1].
type AckFileOrSectionInfo struct {
	Ioa InfoObjAddr
	Nof NameOfFile
	Nos byte
	Afq AckFileOrSectionQualifier
}

// AckFileOrSection sends a type identification [F_AF_NA_1]. Ack file, ack
// section; only a single information object (SQ = 0).
// [F_AF_NA_1] See companion standard 101, subclass 7.3.6.5
// The cause of transmission (coa) is used for
// control direction:
// <13> := file transfer
func AckFileOrSection(c Connect, coa CauseOfTransmission, ca CommonAddr, info AckFileOrSectionInfo) error {
	if err := checkFileCause(c, coa, FileTransfer); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		F_AF_NA_1,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})
	if err := u.AppendInfoObjAddr(info.Ioa); err != nil {
		return err
	}
	u.AppendNameOfFile(info.Nof).
		AppendBytes(info.Nos, info.Afq.Value())
	return c.Send(u)
}

// GetAckFileOrSection [F_AF_NA_1] gets the acknowledge information object.
func (sf *ASDU) GetAckFileOrSection() AckFileOrSectionInfo {
	defer sf.restoreInfoObj(sf.InfoObj)
	return AckFileOrSectionInfo{
		Ioa: sf.DecodeInfoObjAddr(),
		Nof: sf.DecodeNameOfFile(),
		Nos: sf.DecodeByte(),
		Afq: ParseAckFileOrSectionQualifier(sf.DecodeByte()),
	}
}

// SegmentInfo is the information object of [F_SG_NA_1].
type SegmentInfo struct {
	Ioa InfoObjAddr
	Nof NameOfFile
	Nos byte
	// Segment is the segment payload; its length must not exceed
	// Params.MaxSegmentSize.
	Segment []byte
}

// FileSegment sends a type identification [F_SG_NA_1]. Segment, only a
// single information object (SQ = 0).
// [F_SG_NA_1] See companion standard 101, subclass 7.3.6.6
// The cause of transmission (coa) is used for
// monitor direction:
// <13> := file transfer
func FileSegment(c Connect, coa CauseOfTransmission, ca CommonAddr, info SegmentInfo) error {
	if err := checkFileCause(c, coa, FileTransfer); err != nil {
		return err
	}
	if len(info.Segment) > c.Params().MaxSegmentSize() {
		return ErrLengthOutOfRange
	}

	u := NewASDU(c.Params(), Identifier{
		F_SG_NA_1,
		VariableStruct{IsSequence: false, Number: 1},
		coa,
		0,
		ca,
	})
	if err := u.AppendInfoObjAddr(info.Ioa); err != nil {
		return err
	}
	u.AppendNameOfFile(info.Nof).
		AppendBytes(info.Nos, byte(len(info.Segment))).
		AppendBytes(info.Segment...)
	return c.Send(u)
}

// GetFileSegment [F_SG_NA_1] gets the segment information object. The
// returned Segment aliases no ASDU memory; it is a copy.
func (sf *ASDU) GetFileSegment() SegmentInfo {
	defer sf.restoreInfoObj(sf.InfoObj)
	info := SegmentInfo{
		Ioa: sf.DecodeInfoObjAddr(),
		Nof: sf.DecodeNameOfFile(),
		Nos: sf.DecodeByte(),
	}
	los := int(sf.DecodeByte())
	if los > len(sf.InfoObj) {
		los = len(sf.InfoObj)
	}
	info.Segment = append([]byte(nil), sf.InfoObj[:los]...)
	return info
}

// DirectoryInfo is one directory entry of [F_DR_TA_1].
type DirectoryInfo struct {
	Ioa InfoObjAddr
	Nof NameOfFile
	// LengthOfFile is the total length of the file in octets.
	LengthOfFile uint32
	Sof          StatusOfFile
	Time         time.Time
}

// FileDirectory sends a type identification [F_DR_TA_1]. Directory, with
// one information object per entry (SQ = 0).
// [F_DR_TA_1] See companion standard 101, subclass 7.3.6.7
// The cause of transmission (coa) is used for
// monitor direction:
// <3> := spontaneous
// <5> := requested
func FileDirectory(c Connect, coa CauseOfTransmission, ca CommonAddr, infos ...DirectoryInfo) error {
	if err := checkFileCause(c, coa, Spontaneous, Request); err != nil {
		return err
	}
	if err := checkValid(c, F_DR_TA_1, false, len(infos)); err != nil {
		return err
	}

	u := NewASDU(c.Params(), Identifier{
		F_DR_TA_1,
		VariableStruct{IsSequence: false},
		coa,
		0,
		ca,
	})
	if err := u.SetVariableNumber(len(infos)); err != nil {
		return err
	}
	for _, v := range infos {
		if v.LengthOfFile > LengthOfFileMax {
			return ErrLengthOutOfRange
		}
		if err := u.AppendInfoObjAddr(v.Ioa); err != nil {
			return err
		}
		u.AppendNameOfFile(v.Nof).
			AppendLengthOfFile(v.LengthOfFile).
			AppendBytes(v.Sof.Value()).
			AppendBytes(CP56Time2a(v.Time, u.InfoObjTimeZone)...)
	}
	return c.Send(u)
}

// GetFileDirectory [F_DR_TA_1] gets the directory entries.
func (sf *ASDU) GetFileDirectory() []DirectoryInfo {
	defer sf.restoreInfoObj(sf.InfoObj)
	info := make([]DirectoryInfo, 0, sf.Variable.Number)
	for i := 0; i < int(sf.Variable.Number); i++ {
		info = append(info, DirectoryInfo{
			Ioa:          sf.DecodeInfoObjAddr(),
			Nof:          sf.DecodeNameOfFile(),
			LengthOfFile: sf.DecodeLengthOfFile(),
			Sof:          ParseStatusOfFile(sf.DecodeByte()),
			Time:         sf.DecodeCP56Time2a(),
		})
	}
	return info
}

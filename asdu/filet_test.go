package asdu

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"testing"
)

// The expected byte strings below pin the on-wire layout of the file
// transfer ASDUs with ParamsWide (COT 2, CA 2, IOA 3) and common address
// 0x1234, information object address 1, name of file 2.

func TestFileReady(t *testing.T) {
	want := []byte{
		byte(F_FR_NA_1), 0x01, byte(FileTransfer), 0x00, 0x34, 0x12, // identifier
		0x01, 0x00, 0x00, // IOA
		0x02, 0x00, // NOF
		0xe8, 0x03, 0x00, // LOF = 1000
		0x00, // FRQ
	}
	err := FileReady(newConn(want, t), CauseOfTransmission{Cause: FileTransfer}, 0x1234,
		FileReadyInfo{Ioa: 1, Nof: FileDisturbanceData, LengthOfFile: 1000})
	if err != nil {
		t.Fatalf("FileReady() error = %v", err)
	}

	a := NewEmptyASDU(ParamsWide)
	if err := a.UnmarshalBinary(want); err != nil {
		t.Fatalf("UnmarshalBinary() error = %v", err)
	}
	got := a.GetFileReady()
	if got.Ioa != 1 || got.Nof != FileDisturbanceData || got.LengthOfFile != 1000 || got.Frq.IsNegative {
		t.Fatalf("GetFileReady() = %+v", got)
	}
}

func TestSectionReady(t *testing.T) {
	want := []byte{
		byte(F_SR_NA_1), 0x01, byte(FileTransfer), 0x00, 0x34, 0x12,
		0x01, 0x00, 0x00, // IOA
		0x02, 0x00, // NOF
		0x01,             // NOS
		0x90, 0x01, 0x00, // LOF = 400
		0x00, // SRQ
	}
	err := SectionReady(newConn(want, t), CauseOfTransmission{Cause: FileTransfer}, 0x1234,
		SectionReadyInfo{Ioa: 1, Nof: FileDisturbanceData, Nos: 1, LengthOfSection: 400})
	if err != nil {
		t.Fatalf("SectionReady() error = %v", err)
	}

	a := NewEmptyASDU(ParamsWide)
	if err := a.UnmarshalBinary(want); err != nil {
		t.Fatalf("UnmarshalBinary() error = %v", err)
	}
	got := a.GetSectionReady()
	if got.Nos != 1 || got.LengthOfSection != 400 || got.Srq.IsNotReady {
		t.Fatalf("GetSectionReady() = %+v", got)
	}
}

func TestCallOrSelectFile(t *testing.T) {
	want := []byte{
		byte(F_SC_NA_1), 0x01, byte(FileTransfer), 0x00, 0x34, 0x12,
		0x01, 0x00, 0x00, // IOA
		0x02, 0x00, // NOF
		0x01, // NOS
		0x06, // SCQ: request section
	}
	err := CallOrSelectFile(newConn(want, t), CauseOfTransmission{Cause: FileTransfer}, 0x1234,
		CallOrSelectFileInfo{Ioa: 1, Nof: FileDisturbanceData, Nos: 1,
			Scq: SelectAndCallQualifier{Action: SCQRequestSection}})
	if err != nil {
		t.Fatalf("CallOrSelectFile() error = %v", err)
	}

	a := NewEmptyASDU(ParamsWide)
	if err := a.UnmarshalBinary(want); err != nil {
		t.Fatalf("UnmarshalBinary() error = %v", err)
	}
	got := a.GetCallOrSelectFile()
	if got.Scq.Action != SCQRequestSection || got.Scq.Error != FileErrNone || got.Nos != 1 {
		t.Fatalf("GetCallOrSelectFile() = %+v", got)
	}
}

func TestLastSectionOrSegment(t *testing.T) {
	want := []byte{
		byte(F_LS_NA_1), 0x01, byte(FileTransfer), 0x00, 0x34, 0x12,
		0x01, 0x00, 0x00, // IOA
		0x02, 0x00, // NOF
		0x01, // NOS
		0x03, // LSQ: section transfer without deactivation
		0xab, // CHS
	}
	err := LastSectionOrSegment(newConn(want, t), CauseOfTransmission{Cause: FileTransfer}, 0x1234,
		LastSectionOrSegmentInfo{Ioa: 1, Nof: FileDisturbanceData, Nos: 1,
			Lsq: LSQSectionWithoutDeactivate, Chs: 0xab})
	if err != nil {
		t.Fatalf("LastSectionOrSegment() error = %v", err)
	}

	a := NewEmptyASDU(ParamsWide)
	if err := a.UnmarshalBinary(want); err != nil {
		t.Fatalf("UnmarshalBinary() error = %v", err)
	}
	got := a.GetLastSectionOrSegment()
	if got.Lsq != LSQSectionWithoutDeactivate || got.Chs != 0xab {
		t.Fatalf("GetLastSectionOrSegment() = %+v", got)
	}
}

func TestAckFileOrSection(t *testing.T) {
	want := []byte{
		byte(F_AF_NA_1), 0x01, byte(FileTransfer), 0x00, 0x34, 0x12,
		0x01, 0x00, 0x00, // IOA
		0x02, 0x00, // NOF
		0x01, // NOS
		0x23, // AFQ: positive ack section (3) + checksum failed (2)
	}
	err := AckFileOrSection(newConn(want, t), CauseOfTransmission{Cause: FileTransfer}, 0x1234,
		AckFileOrSectionInfo{Ioa: 1, Nof: FileDisturbanceData, Nos: 1,
			Afq: AckFileOrSectionQualifier{Action: AFQPosAckSection, Error: FileErrChecksumFailed}})
	if err != nil {
		t.Fatalf("AckFileOrSection() error = %v", err)
	}

	a := NewEmptyASDU(ParamsWide)
	if err := a.UnmarshalBinary(want); err != nil {
		t.Fatalf("UnmarshalBinary() error = %v", err)
	}
	got := a.GetAckFileOrSection()
	if got.Afq.Action != AFQPosAckSection || got.Afq.Error != FileErrChecksumFailed {
		t.Fatalf("GetAckFileOrSection() = %+v", got)
	}
}

func TestFileSegment(t *testing.T) {
	payload := []byte{0xaa, 0xbb, 0xcc}
	want := []byte{
		byte(F_SG_NA_1), 0x01, byte(FileTransfer), 0x00, 0x34, 0x12,
		0x01, 0x00, 0x00, // IOA
		0x02, 0x00, // NOF
		0x01,             // NOS
		0x03,             // LOS
		0xaa, 0xbb, 0xcc, // segment
	}
	err := FileSegment(newConn(want, t), CauseOfTransmission{Cause: FileTransfer}, 0x1234,
		SegmentInfo{Ioa: 1, Nof: FileDisturbanceData, Nos: 1, Segment: payload})
	if err != nil {
		t.Fatalf("FileSegment() error = %v", err)
	}

	// The segment is the one variable-length ASDU: check that the decoder
	// derives its size from LOS.
	a := NewEmptyASDU(ParamsWide)
	if err := a.UnmarshalBinary(want); err != nil {
		t.Fatalf("UnmarshalBinary() error = %v", err)
	}
	got := a.GetFileSegment()
	if !bytes.Equal(got.Segment, payload) || got.Nos != 1 || got.Nof != FileDisturbanceData {
		t.Fatalf("GetFileSegment() = %+v", got)
	}

	// A segment longer than one ASDU must be rejected.
	tooBig := SegmentInfo{Ioa: 1, Segment: make([]byte, ParamsWide.MaxSegmentSize()+1)}
	if err := FileSegment(newConn(nil, t), CauseOfTransmission{Cause: FileTransfer}, 0x1234, tooBig); err != ErrLengthOutOfRange {
		t.Fatalf("oversized segment error = %v, want ErrLengthOutOfRange", err)
	}
}

func TestFileSegmentVariableLength(t *testing.T) {
	head := []byte{
		byte(F_SG_NA_1), 0x01, byte(FileTransfer), 0x00, 0x34, 0x12,
		0x01, 0x00, 0x00, // IOA
		0x02, 0x00, // NOF
		0x01, // NOS
	}
	tests := []struct {
		name    string
		raw     []byte
		wantErr error
		wantLen int
	}{
		{"exact", append(append([]byte{}, head...), 0x02, 0x11, 0x22), nil, 2},
		{"zero length", append(append([]byte{}, head...), 0x00), nil, 0},
		// A segment whose LOS accounts for less than the ASDU carries was not
		// produced by a conforming sender: the frame fixes the length and LOS
		// fixes the segment, so there is nowhere for the surplus to come from.
		{"trailing octets rejected", append(append([]byte{}, head...), 0x01, 0x11, 0x99, 0x99), ErrTrailingOctets, 0},
		{"truncated payload", append(append([]byte{}, head...), 0x04, 0x11, 0x22), io.EOF, 0},
		{"truncated header", head[:len(head)-1], io.EOF, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewEmptyASDU(ParamsWide)
			err := a.UnmarshalBinary(tt.raw)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("UnmarshalBinary() error = %v, want %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got := a.GetFileSegment(); len(got.Segment) != tt.wantLen {
				t.Fatalf("segment length = %d, want %d", len(got.Segment), tt.wantLen)
			}
		})
	}
}

// A device that pads can be accommodated deliberately, which is different
// from doing it silently for everyone.
func TestFileSegmentTrailingOctetsAllowed(t *testing.T) {
	head := []byte{
		byte(F_SG_NA_1), 0x01, byte(FileTransfer), 0x00, 0x34, 0x12,
		0x01, 0x00, 0x00, // IOA
		0x02, 0x00, // NOF
		0x01, // NOS
	}
	raw := append(append([]byte{}, head...), 0x01, 0x11, 0x99, 0x99)

	lenient := *ParamsWide
	lenient.AllowTrailingOctets = true
	a := NewEmptyASDU(&lenient)
	if err := a.UnmarshalBinary(raw); err != nil {
		t.Fatalf("UnmarshalBinary() error = %v, want nil with AllowTrailingOctets", err)
	}
	if got := a.GetFileSegment(); len(got.Segment) != 1 || got.Segment[0] != 0x11 {
		t.Fatalf("segment = % x, want 11", got.Segment)
	}
}

func TestFileDirectory(t *testing.T) {
	want := []byte{
		byte(F_DR_TA_1), 0x01, byte(Request), 0x00, 0x34, 0x12,
		0x01, 0x00, 0x00, // IOA
		0x02, 0x00, // NOF
		0xe8, 0x03, 0x00, // LOF = 1000
		0x20, // SOF: last file of directory
	}
	want = append(want, tm0CP56Time2aBytes...)

	err := FileDirectory(newConn(want, t), CauseOfTransmission{Cause: Request}, 0x1234,
		DirectoryInfo{
			Ioa: 1, Nof: FileDisturbanceData, LengthOfFile: 1000,
			Sof:  StatusOfFile{IsLastFileOfDirectory: true},
			Time: tm0,
		})
	if err != nil {
		t.Fatalf("FileDirectory() error = %v", err)
	}

	a := NewEmptyASDU(ParamsWide)
	if err := a.UnmarshalBinary(want); err != nil {
		t.Fatalf("UnmarshalBinary() error = %v", err)
	}
	got := a.GetFileDirectory()
	if len(got) != 1 {
		t.Fatalf("GetFileDirectory() returned %d entries, want 1", len(got))
	}
	if got[0].LengthOfFile != 1000 || !got[0].Sof.IsLastFileOfDirectory || !got[0].Time.Equal(tm0) {
		t.Fatalf("GetFileDirectory()[0] = %+v", got[0])
	}
}

func TestFileTransferCauseValidation(t *testing.T) {
	c := newConn(nil, t)
	bad := CauseOfTransmission{Cause: Activation}
	if err := FileReady(c, bad, 1, FileReadyInfo{}); err != ErrCmdCause {
		t.Errorf("FileReady with cause Activation = %v, want ErrCmdCause", err)
	}
	if err := SectionReady(c, bad, 1, SectionReadyInfo{}); err != ErrCmdCause {
		t.Errorf("SectionReady with cause Activation = %v, want ErrCmdCause", err)
	}
	if err := AckFileOrSection(c, bad, 1, AckFileOrSectionInfo{}); err != ErrCmdCause {
		t.Errorf("AckFileOrSection with cause Activation = %v, want ErrCmdCause", err)
	}
	if err := FileDirectory(c, bad, 1, DirectoryInfo{}); err != ErrCmdCause {
		t.Errorf("FileDirectory with cause Activation = %v, want ErrCmdCause", err)
	}
}

func TestFileQualifierCodecs(t *testing.T) {
	for _, q := range []FileReadyQualifier{{}, {Qual: 0x7f}, {IsNegative: true}, {Qual: 5, IsNegative: true}} {
		if got := ParseFileReadyQualifier(q.Value()); got != q {
			t.Errorf("FileReadyQualifier round trip: got %+v, want %+v", got, q)
		}
	}
	for _, q := range []SectionReadyQualifier{{}, {Qual: 0x7f}, {IsNotReady: true}} {
		if got := ParseSectionReadyQualifier(q.Value()); got != q {
			t.Errorf("SectionReadyQualifier round trip: got %+v, want %+v", got, q)
		}
	}
	for _, q := range []SelectAndCallQualifier{
		{}, {Action: SCQSelectFile}, {Action: SCQRequestSection, Error: FileErrChecksumFailed},
		{Action: SCQDeactivateSection, Error: FileErrUnexpectedNameOfSection},
	} {
		if got := ParseSelectAndCallQualifier(q.Value()); got != q {
			t.Errorf("SelectAndCallQualifier round trip: got %+v, want %+v", got, q)
		}
	}
	for _, q := range []AckFileOrSectionQualifier{
		{}, {Action: AFQPosAckFile}, {Action: AFQNegAckSection, Error: FileErrMemoryUnavailable},
	} {
		if got := ParseAckFileOrSectionQualifier(q.Value()); got != q {
			t.Errorf("AckFileOrSectionQualifier round trip: got %+v, want %+v", got, q)
		}
	}
	for _, s := range []StatusOfFile{
		{}, {Status: 0x1f}, {IsLastFileOfDirectory: true}, {IsDirectory: true},
		{IsTransferActive: true}, {Status: 3, IsLastFileOfDirectory: true, IsTransferActive: true},
	} {
		if got := ParseStatusOfFile(s.Value()); got != s {
			t.Errorf("StatusOfFile round trip: got %+v, want %+v", got, s)
		}
	}
}

func TestFileChecksum(t *testing.T) {
	if got := FileChecksum([]byte{0xaa, 0xbb, 0xcc}); got != 0x31 {
		t.Fatalf("FileChecksum() = 0x%02x, want 0x31", got)
	}
	if got := FileChecksum(nil); got != 0 {
		t.Fatalf("FileChecksum(nil) = 0x%02x, want 0", got)
	}
}

func TestLengthOfFileCodec(t *testing.T) {
	for _, v := range []uint32{0, 1, 255, 256, 65535, 65536, LengthOfFileMax} {
		a := NewEmptyASDU(ParamsWide)
		a.AppendLengthOfFile(v)
		if got := a.DecodeLengthOfFile(); got != v {
			t.Fatalf("LOF round trip: got %d, want %d", got, v)
		}
	}
}

func TestMaxSegmentSize(t *testing.T) {
	// identifier(6) + IOA(3) + NOF(2) + NOS(1) + LOS(1) = 13
	if got := ParamsWide.MaxSegmentSize(); got != ASDUSizeMax-13 {
		t.Fatalf("ParamsWide.MaxSegmentSize() = %d, want %d", got, ASDUSizeMax-13)
	}
	// identifier(4) + IOA(1) + NOF(2) + NOS(1) + LOS(1) = 9. The ASDU size
	// limit binds before the 255 octet ceiling of the LOS field does.
	if got := ParamsNarrow.MaxSegmentSize(); got != ASDUSizeMax-9 {
		t.Fatalf("ParamsNarrow.MaxSegmentSize() = %d, want %d", got, ASDUSizeMax-9)
	}
	if !reflect.DeepEqual(ParamsStandard104, ParamsWide) {
		t.Fatal("ParamsStandard104 is expected to alias ParamsWide")
	}
}

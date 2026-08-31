// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

// Package filetransfer implements the IEC 60870-5-101/104 file transfer
// procedures (ASDU types F_FR_NA_1 <120> … F_DR_TA_1 <126>) on top of the
// asdu package.
//
// Two components drive a transfer in the monitor direction, that is a file
// travelling from the controlled station (outstation) to the controlling
// station (master) — the common case, e.g. fetching disturbance records:
//
//	Sender   — controlled station side: offers files and serves them.
//	Receiver — controlling station side: requests files and assembles them.
//
// Both are transport agnostic: they act on an asdu.Connect and consume the
// ASDUs handed to them by the application's ASDU handler, so the same code
// works with cs104 and (once wired) cs101 endpoints.
//
//	sender := filetransfer.NewSender(store)
//
//	func (h *myHandler) ASDUHandler(c asdu.Connect, a *asdu.ASDU) error {
//		if handled, err := sender.Handle(c, a); handled {
//			return err
//		}
//		... // the application's own types
//		return nil
//	}
//
// Files are held by a pluggable Store; MemStore is an in-memory
// implementation, and any other backing (disk, database) can implement the
// interface.
package filetransfer

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
)

// Errors of the filetransfer package.
var (
	// ErrFileNotFound is returned by a Store when the requested file does
	// not exist. It is answered on the wire with a negative acknowledge
	// carrying asdu.FileErrUnexpectedNameOfFile.
	ErrFileNotFound = errors.New("filetransfer: file not found")
	// ErrNoTransfer indicates an ASDU that belongs to a transfer which is
	// not currently active.
	ErrNoTransfer = errors.New("filetransfer: no transfer in progress")
	// ErrBusy is returned when a new transfer is requested while another
	// one is still running.
	ErrBusy = errors.New("filetransfer: a transfer is already in progress")
	// ErrChecksum indicates a section whose checksum did not match.
	ErrChecksum = errors.New("filetransfer: section checksum mismatch")
	// ErrUnsupported indicates a requested service this implementation does
	// not provide.
	ErrUnsupported = errors.New("filetransfer: unsupported service")
)

// Entry describes one file held by a Store.
type Entry struct {
	// Ioa is the information object address the file is associated with.
	Ioa asdu.InfoObjAddr
	// Nof is the name of file. Values below 5 are predefined by the
	// standard (asdu.FileDisturbanceData and friends).
	Nof asdu.NameOfFile
	// Size is the file length in octets.
	Size uint32
	// Time is the file creation/acquisition time, reported in a directory.
	Time time.Time
	// IsDirectory marks a subdirectory entry rather than a file.
	IsDirectory bool
}

// Store is the pluggable backing store of the file transfer components.
// Implementations must be safe for concurrent use.
type Store interface {
	// List returns the directory of available files.
	List() ([]Entry, error)
	// Read returns the whole content of one file. It returns
	// ErrFileNotFound when the file is unknown.
	Read(ioa asdu.InfoObjAddr, nof asdu.NameOfFile) ([]byte, error)
	// Write stores a received file, replacing any previous content.
	Write(ioa asdu.InfoObjAddr, nof asdu.NameOfFile, data []byte) error
	// Delete removes a file. It returns ErrFileNotFound when the file is
	// unknown.
	Delete(ioa asdu.InfoObjAddr, nof asdu.NameOfFile) error
}

type fileKey struct {
	ioa asdu.InfoObjAddr
	nof asdu.NameOfFile
}

type memFile struct {
	data []byte
	time time.Time
}

// MemStore is an in-memory Store, safe for concurrent use.
type MemStore struct {
	mu    sync.RWMutex
	files map[fileKey]memFile
}

var _ Store = (*MemStore)(nil)

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{files: make(map[fileKey]memFile)}
}

// List returns the directory of stored files, ordered by IOA then name.
func (sf *MemStore) List() ([]Entry, error) {
	sf.mu.RLock()
	defer sf.mu.RUnlock()
	out := make([]Entry, 0, len(sf.files))
	for k, v := range sf.files {
		out = append(out, Entry{
			Ioa:  k.ioa,
			Nof:  k.nof,
			Size: uint32(len(v.data)),
			Time: v.time,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ioa != out[j].Ioa {
			return out[i].Ioa < out[j].Ioa
		}
		return out[i].Nof < out[j].Nof
	})
	return out, nil
}

// Read returns a copy of the file content.
func (sf *MemStore) Read(ioa asdu.InfoObjAddr, nof asdu.NameOfFile) ([]byte, error) {
	sf.mu.RLock()
	defer sf.mu.RUnlock()
	f, ok := sf.files[fileKey{ioa, nof}]
	if !ok {
		return nil, ErrFileNotFound
	}
	return append([]byte(nil), f.data...), nil
}

// Write stores a copy of data under the given address and name.
func (sf *MemStore) Write(ioa asdu.InfoObjAddr, nof asdu.NameOfFile, data []byte) error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	sf.files[fileKey{ioa, nof}] = memFile{
		data: append([]byte(nil), data...),
		time: time.Now(),
	}
	return nil
}

// Delete removes a file from the store.
func (sf *MemStore) Delete(ioa asdu.InfoObjAddr, nof asdu.NameOfFile) error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	k := fileKey{ioa, nof}
	if _, ok := sf.files[k]; !ok {
		return ErrFileNotFound
	}
	delete(sf.files, k)
	return nil
}

// action is one outbound ASDU produced by a state transition. Actions are
// executed after the component's lock is released, so that a synchronous
// transport cannot re-enter a locked state machine.
type action func(asdu.Connect) error

// isFileASDU reports whether the type identification belongs to the file
// transfer set handled by this package.
func isFileASDU(t asdu.TypeID) bool {
	switch t {
	case asdu.F_FR_NA_1, asdu.F_SR_NA_1, asdu.F_SC_NA_1, asdu.F_LS_NA_1,
		asdu.F_AF_NA_1, asdu.F_SG_NA_1, asdu.F_DR_TA_1:
		return true
	}
	return false
}

// splitSections cuts data into sections of at most sectionSize octets.
// An empty file yields a single empty section.
func splitSections(data []byte, sectionSize int) [][]byte {
	if sectionSize <= 0 {
		sectionSize = DefaultSectionSize
	}
	if len(data) == 0 {
		return [][]byte{{}}
	}
	var out [][]byte
	for off := 0; off < len(data); off += sectionSize {
		end := off + sectionSize
		if end > len(data) {
			end = len(data)
		}
		out = append(out, data[off:end])
	}
	return out
}

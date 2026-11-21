// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs104

import (
	"bufio"
	"io"
	"net"
	"sync/atomic"
	"time"
)

// frameReader assembles APDUs from a stream.
//
// Between frames it waits without a deadline: an idle link is policed by the
// t₃ test frame, not by the reader. Once the first octet of a frame has
// arrived the rest must follow within assemble, or the read fails with a
// timeout. Without that bound a peer that sends 0x68 and then nothing holds
// the connection, and its buffers, for ever.
type frameReader struct {
	conn     net.Conn
	r        *bufio.Reader
	assemble time.Duration

	// Octets discarded while looking for a start character, and frames whose
	// length octet was out of range. Read through the accessors: they are
	// updated by the receive goroutine and read by others.
	garbage atomic.Uint64
	badLen  atomic.Uint64
}

func newFrameReader(conn net.Conn, assemble time.Duration) *frameReader {
	return &frameReader{conn: conn, r: bufio.NewReaderSize(conn, 4*APDUSizeMax), assemble: assemble}
}

// next returns the next complete APDU, start character and length octet
// included. Octets that cannot begin a frame, and frames announcing a length
// no APDU can have, are skipped and counted. Any read error ends the stream.
func (fr *frameReader) next() ([]byte, error) {
	buf := make([]byte, APDUSizeMax)
	for {
		// Idle: no deadline.
		if fr.assemble > 0 {
			_ = fr.conn.SetReadDeadline(time.Time{})
		}
		b, err := fr.r.ReadByte()
		if err != nil {
			return nil, err
		}
		if b != startFrame {
			fr.garbage.Add(1)
			continue
		}
		if fr.assemble > 0 {
			_ = fr.conn.SetReadDeadline(time.Now().Add(fr.assemble))
		}
		buf[0] = b
		if _, err = io.ReadFull(fr.r, buf[1:2]); err != nil {
			return nil, err
		}
		length := int(buf[1]) + 2
		if length < APCICtlFiledSize+2 || length > APDUSizeMax {
			fr.badLen.Add(1)
			// A start character in the length position may begin the real
			// frame; look at it again instead of discarding it.
			if buf[1] == startFrame {
				_ = fr.r.UnreadByte() // the last read was exactly this octet
			}
			continue
		}
		if _, err = io.ReadFull(fr.r, buf[2:length]); err != nil {
			return nil, err
		}
		return buf[:length], nil
	}
}

// Garbage is the count of octets discarded while resynchronising.
func (fr *frameReader) Garbage() uint64 { return fr.garbage.Load() }

// BadLength is the count of frames announcing an impossible length.
func (fr *frameReader) BadLength() uint64 { return fr.badLen.Load() }

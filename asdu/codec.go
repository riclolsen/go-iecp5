// Copyright 2020 thinkgos (thinkgo@aliyun.com).  All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package asdu

import (
	"encoding/binary"
	"math"
	"time"
)

// AppendBytes append some bytes to info object
func (sf *ASDU) AppendBytes(b ...byte) *ASDU {
	sf.InfoObj = append(sf.InfoObj, b...)
	return sf
}

// DecodeByte decode a byte then the pass it
func (sf *ASDU) DecodeByte() byte {
	v := sf.InfoObj[0]
	sf.InfoObj = sf.InfoObj[1:]
	return v
}

// AppendUint16 append some uint16 to info object
func (sf *ASDU) AppendUint16(b uint16) *ASDU {
	sf.InfoObj = append(sf.InfoObj, byte(b&0xff), byte((b>>8)&0xff))
	return sf
}

// DecodeUint16 decode a uint16 then the pass it
func (sf *ASDU) DecodeUint16() uint16 {
	v := binary.LittleEndian.Uint16(sf.InfoObj)
	sf.InfoObj = sf.InfoObj[2:]
	return v
}

// AppendInfoObjAddr append information object address to information object
func (sf *ASDU) AppendInfoObjAddr(addr InfoObjAddr) error {
	switch sf.InfoObjAddrSize {
	case 1:
		if addr > 255 {
			return ErrInfoObjAddrFit
		}
		sf.InfoObj = append(sf.InfoObj, byte(addr))
	case 2:
		if addr > 65535 {
			return ErrInfoObjAddrFit
		}
		sf.InfoObj = append(sf.InfoObj, byte(addr), byte(addr>>8))
	case 3:
		if addr > 16777215 {
			return ErrInfoObjAddrFit
		}
		sf.InfoObj = append(sf.InfoObj, byte(addr), byte(addr>>8), byte(addr>>16))
	default:
		return ErrParam
	}
	return nil
}

// DecodeInfoObjAddr decode info object address then the pass it
func (sf *ASDU) DecodeInfoObjAddr() InfoObjAddr {
	var ioa InfoObjAddr
	switch sf.InfoObjAddrSize {
	case 1:
		ioa = InfoObjAddr(sf.InfoObj[0])
		sf.InfoObj = sf.InfoObj[1:]
	case 2:
		ioa = InfoObjAddr(sf.InfoObj[0]) | (InfoObjAddr(sf.InfoObj[1]) << 8)
		sf.InfoObj = sf.InfoObj[2:]
	case 3:
		ioa = InfoObjAddr(sf.InfoObj[0]) | (InfoObjAddr(sf.InfoObj[1]) << 8) | (InfoObjAddr(sf.InfoObj[2]) << 16)
		sf.InfoObj = sf.InfoObj[3:]
	default:
		panic(ErrParam)
	}
	return ioa
}

// AppendNormalize append a Normalize value to info object
func (sf *ASDU) AppendNormalize(n Normalize) *ASDU {
	sf.InfoObj = append(sf.InfoObj, byte(n), byte(n>>8))
	return sf
}

// DecodeNormalize decode info object byte to a Normalize value
func (sf *ASDU) DecodeNormalize() Normalize {
	n := Normalize(binary.LittleEndian.Uint16(sf.InfoObj))
	sf.InfoObj = sf.InfoObj[2:]
	return n
}

// AppendScaled append a Scaled value to info object
// See companion standard 101, subclass 7.2.6.7.
func (sf *ASDU) AppendScaled(i int16) *ASDU {
	sf.InfoObj = append(sf.InfoObj, byte(i), byte(i>>8))
	return sf
}

// DecodeScaled decode info object byte to a Scaled value
func (sf *ASDU) DecodeScaled() int16 {
	s := int16(binary.LittleEndian.Uint16(sf.InfoObj))
	sf.InfoObj = sf.InfoObj[2:]
	return s
}

// AppendFloat32 append a float32 value to info object
// See companion standard 101, subclass 7.2.6.8.
func (sf *ASDU) AppendFloat32(f float32) *ASDU {
	bits := math.Float32bits(f)
	sf.InfoObj = append(sf.InfoObj, byte(bits), byte(bits>>8), byte(bits>>16), byte(bits>>24))
	return sf
}

// DecodeFloat32 decode info object byte to a float32 value
func (sf *ASDU) DecodeFloat32() float32 {
	f := math.Float32frombits(binary.LittleEndian.Uint32(sf.InfoObj))
	sf.InfoObj = sf.InfoObj[4:]
	return f
}

// AppendBinaryCounterReading append binary couter reading value to info object
// See companion standard 101, subclass 7.2.6.9.
func (sf *ASDU) AppendBinaryCounterReading(v BinaryCounterReading) *ASDU {
	value := v.SeqNumber & 0x1f
	if v.HasCarry {
		value |= 0x20
	}
	if v.IsAdjusted {
		value |= 0x40
	}
	if v.IsInvalid {
		value |= 0x80
	}
	sf.InfoObj = append(sf.InfoObj, byte(v.CounterReading), byte(v.CounterReading>>8),
		byte(v.CounterReading>>16), byte(v.CounterReading>>24), value)
	return sf
}

// DecodeBinaryCounterReading decode info object byte to binary couter reading value
func (sf *ASDU) DecodeBinaryCounterReading() BinaryCounterReading {
	v := int32(binary.LittleEndian.Uint32(sf.InfoObj))
	b := sf.InfoObj[4]
	sf.InfoObj = sf.InfoObj[5:]
	return BinaryCounterReading{
		v,
		b & 0x1f,
		b&0x20 == 0x20,
		b&0x40 == 0x40,
		b&0x80 == 0x80,
	}
}

// AppendBitsString32 append a bits string value to info object
// See companion standard 101, subclass 7.2.6.13.
func (sf *ASDU) AppendBitsString32(v uint32) *ASDU {
	sf.InfoObj = append(sf.InfoObj, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
	return sf
}

// DecodeBitsString32 decode info object byte to a bits string value
func (sf *ASDU) DecodeBitsString32() uint32 {
	v := binary.LittleEndian.Uint32(sf.InfoObj)
	sf.InfoObj = sf.InfoObj[4:]
	return v
}

// AppendCP56Time2a append a CP56Time2a value to info object
func (sf *ASDU) AppendCP56Time2a(t time.Time, loc *time.Location) *ASDU {
	sf.InfoObj = append(sf.InfoObj, CP56Time2a(t, loc)...)
	return sf
}

// DecodeCP56Time2a decode info object byte to CP56Time2a
func (sf *ASDU) DecodeCP56Time2a() time.Time {
	t := ParseCP56Time2a(sf.InfoObj, sf.InfoObjTimeZone)
	sf.InfoObj = sf.InfoObj[7:]
	return t
}

// AppendCP24Time2a append CP24Time2a to asdu info object
func (sf *ASDU) AppendCP24Time2a(t time.Time, loc *time.Location) *ASDU {
	sf.InfoObj = append(sf.InfoObj, CP24Time2a(t, loc)...)
	return sf
}

// DecodeCP24Time2a decode info object byte to CP24Time2a
func (sf *ASDU) DecodeCP24Time2a() time.Time {
	t := ParseCP24Time2a(sf.InfoObj, sf.Params.InfoObjTimeZone)
	sf.InfoObj = sf.InfoObj[3:]
	return t
}

// AppendCP16Time2a append CP16Time2a to asdu info object
func (sf *ASDU) AppendCP16Time2a(msec uint16) *ASDU {
	sf.InfoObj = append(sf.InfoObj, CP16Time2a(msec)...)
	return sf
}

// DecodeCP16Time2a decode info object byte to CP16Time2a
func (sf *ASDU) DecodeCP16Time2a() uint16 {
	t := ParseCP16Time2a(sf.InfoObj)
	sf.InfoObj = sf.InfoObj[2:]
	return t
}

// AppendStatusAndStatusChangeDetection append StatusAndStatusChangeDetection value to asdu info object
func (sf *ASDU) AppendStatusAndStatusChangeDetection(scd StatusAndStatusChangeDetection) *ASDU {
	sf.InfoObj = append(sf.InfoObj, byte(scd), byte(scd>>8), byte(scd>>16), byte(scd>>24))
	return sf
}

// DecodeStatusAndStatusChangeDetection decode info object byte to StatusAndStatusChangeDetection
func (sf *ASDU) DecodeStatusAndStatusChangeDetection() StatusAndStatusChangeDetection {
	s := StatusAndStatusChangeDetection(binary.LittleEndian.Uint32(sf.InfoObj))
	sf.InfoObj = sf.InfoObj[4:]
	return s
}

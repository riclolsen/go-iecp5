// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

//go:build windows

package netpeer

import (
	"context"
	"net"
	"net/netip"
	"syscall"
	"unsafe"
)

var (
	iphlpapi = syscall.NewLazyDLL("iphlpapi.dll")
	sendARP  = iphlpapi.NewProc("SendARP")
)

// lookupNeighbor uses SendARP, which answers from the ARP cache and resolves
// on the link when the entry is missing. It is IPv4 only: IPv6 neighbours are
// reported as not found.
func lookupNeighbor(_ context.Context, addr netip.Addr, _ string) (net.HardwareAddr, error) {
	if !addr.Is4() {
		return nil, ErrNotFound
	}
	a := addr.As4()
	dst := uint32(a[0]) | uint32(a[1])<<8 | uint32(a[2])<<16 | uint32(a[3])<<24
	var mac [8]byte
	n := uint32(len(mac))
	r, _, _ := sendARP.Call(uintptr(dst), 0, uintptr(unsafe.Pointer(&mac[0])), uintptr(unsafe.Pointer(&n)))
	if r != 0 || n < 6 {
		return nil, ErrNotFound
	}
	return net.HardwareAddr(append([]byte(nil), mac[:6]...)), nil
}

// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

//go:build darwin || freebsd || openbsd || netbsd

package netpeer

import (
	"context"
	"net"
	"net/netip"
	"os/exec"
	"strings"
)

// lookupNeighbor asks the system's own tools, arp(8) for IPv4 and ndp(8) for
// IPv6, because the routing socket's neighbour dump is not portable across
// the BSDs. The tools only read the table; nothing is probed on the wire.
func lookupNeighbor(ctx context.Context, addr netip.Addr, _ string) (net.HardwareAddr, error) {
	var out []byte
	var err error
	if addr.Is4() {
		out, err = exec.CommandContext(ctx, "arp", "-n", addr.String()).Output()
	} else {
		out, err = exec.CommandContext(ctx, "ndp", "-n", addr.String()).Output()
	}
	if err != nil {
		return nil, ErrNotFound
	}
	return parseBSDNeighbor(string(out), addr)
}

// parseBSDNeighbor extracts the hardware address from arp/ndp output such as
//
//	? (192.168.1.5) at aa:bb:cc:dd:ee:ff on en0 ifscope [ethernet]
//	fe80::1%en0   aa:bb:cc:dd:ee:ff   en0  23h58m  S  R
func parseBSDNeighbor(out string, addr netip.Addr) (net.HardwareAddr, error) {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		for i, w := range f {
			if (w == "at" && i+1 < len(f)) || (strings.HasPrefix(w, addr.String()) && i+1 < len(f)) {
				mac, err := net.ParseMAC(strings.TrimSuffix(f[i+1], ","))
				if err == nil && len(mac) == 6 && !isZeroMAC(mac) {
					return mac, nil
				}
			}
		}
	}
	return nil, ErrNotFound
}

func isZeroMAC(m net.HardwareAddr) bool {
	for _, b := range m {
		if b != 0 {
			return false
		}
	}
	return true
}

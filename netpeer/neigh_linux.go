// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package netpeer

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strings"
	"syscall"
)

// Neighbour cache entry states (linux/neighbour.h) that carry a usable
// link-layer address.
const (
	ndIncomplete = 0x01
	ndFailed     = 0x20
)

const (
	ndaDst    = 1
	ndaLLAddr = 2
)

type neighEntry struct {
	addr  netip.Addr
	mac   net.HardwareAddr
	ifidx int
	state uint16
}

func lookupNeighbor(ctx context.Context, addr netip.Addr, ifname string) (net.HardwareAddr, error) {
	family := syscall.AF_INET6
	if addr.Is4() {
		family = syscall.AF_INET
	}
	var entries []neighEntry
	if rib, err := syscall.NetlinkRIB(syscall.RTM_GETNEIGH, family); err == nil {
		entries, err = parseNeighbors(rib)
		if err != nil {
			entries = nil
		}
	}
	if len(entries) == 0 && addr.Is4() {
		if f, err := os.Open("/proc/net/arp"); err == nil {
			entries, _ = parseProcARP(f)
			_ = f.Close()
		}
	}
	var ifidx int
	if ifname != "" {
		if ifc, err := net.InterfaceByName(ifname); err == nil {
			ifidx = ifc.Index
		}
	}
	for _, e := range entries {
		if e.addr != addr || e.mac == nil {
			continue
		}
		if ifidx != 0 && e.ifidx != 0 && e.ifidx != ifidx {
			continue
		}
		return e.mac, nil
	}
	return nil, ErrNotFound
}

// parseNeighbors decodes an RTM_GETNEIGH dump.
func parseNeighbors(rib []byte) ([]neighEntry, error) {
	msgs, err := syscall.ParseNetlinkMessage(rib)
	if err != nil {
		return nil, err
	}
	var out []neighEntry
	for _, m := range msgs {
		if m.Header.Type != syscall.RTM_NEWNEIGH || len(m.Data) < 12 {
			continue
		}
		// struct ndmsg { u8 family; u8 pad1; u16 pad2; s32 ifindex; u16 state; u8 flags; u8 type }
		e := neighEntry{
			ifidx: int(int32(binary.LittleEndian.Uint32(m.Data[4:8]))),
			state: binary.LittleEndian.Uint16(m.Data[8:10]),
		}
		if e.state&(ndIncomplete|ndFailed) != 0 {
			continue
		}
		// Attributes follow the 12-octet ndmsg: u16 length, u16 type, value,
		// padded to 4 octets.
		for b := m.Data[12:]; len(b) >= 4; {
			l := int(binary.LittleEndian.Uint16(b[0:2]))
			if l < 4 || l > len(b) {
				break
			}
			val := b[4:l]
			switch binary.LittleEndian.Uint16(b[2:4]) & 0x3fff {
			case ndaDst:
				if ip, ok := netip.AddrFromSlice(val); ok {
					e.addr = ip.Unmap()
				}
			case ndaLLAddr:
				if len(val) == 6 || len(val) == 8 || len(val) == 20 {
					e.mac = append(net.HardwareAddr(nil), val...)
				}
			}
			adv := (l + 3) &^ 3
			if adv >= len(b) {
				break
			}
			b = b[adv:]
		}
		if e.addr.IsValid() && e.mac != nil {
			out = append(out, e)
		}
	}
	return out, nil
}

// parseProcARP decodes /proc/net/arp, the IPv4 fallback when netlink is not
// available (for instance under a restrictive seccomp profile).
func parseProcARP(r io.Reader) ([]neighEntry, error) {
	var out []neighEntry
	sc := bufio.NewScanner(r)
	sc.Scan() // header
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 6 {
			continue
		}
		ip, err := netip.ParseAddr(f[0])
		if err != nil {
			continue
		}
		var flags int
		if _, err := fmt.Sscanf(f[2], "0x%x", &flags); err != nil || flags&0x2 == 0 { // ATF_COM
			continue
		}
		mac, err := net.ParseMAC(f[3])
		if err != nil {
			continue
		}
		e := neighEntry{addr: ip, mac: mac}
		if ifc, err := net.InterfaceByName(f[5]); err == nil {
			e.ifidx = ifc.Index
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

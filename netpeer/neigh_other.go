// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

//go:build !linux && !windows && !darwin && !freebsd && !openbsd && !netbsd

package netpeer

import (
	"context"
	"net"
	"net/netip"
)

func lookupNeighbor(context.Context, netip.Addr, string) (net.HardwareAddr, error) {
	return nil, ErrUnsupported
}

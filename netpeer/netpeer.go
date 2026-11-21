// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

// Package netpeer finds the link-layer (MAC) address behind a peer's IP
// address and remembers which MAC went with which IP, so that a change can be
// noticed.
//
// A MAC address is only meaningful for a peer on the same link. For a routed
// peer the neighbour table holds the next hop's address, which says nothing
// about the peer; Lookup reports such a peer as off-link with no MAC rather
// than hand back the router's address as if it were the peer's. ARP and
// neighbour discovery are also spoofable by anyone on the link, so a MAC is
// evidence to correlate, not proof of identity.
package netpeer

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"
)

// Errors.
var (
	// ErrUnsupported means this platform has no neighbour table backend.
	ErrUnsupported = errors.New("netpeer: neighbour table not supported on this platform")
	// ErrNotFound means the neighbour table has no usable entry for the address.
	ErrNotFound = errors.New("netpeer: no neighbour entry")
)

// Source says where a MAC came from.
type Source string

// Sources.
const (
	SourceNone     Source = ""
	SourceNeighbor Source = "neighbor"
	SourceStatic   Source = "static"
	SourceCustom   Source = "custom"
)

// Result is what is known about the link layer behind an IP address.
type Result struct {
	MAC       net.HardwareAddr // nil when unknown
	Source    Source
	OnLink    bool   // the address is inside a directly attached subnet
	Loopback  bool   // the address is this host
	Interface string // interface the address is reached through, when known
}

// Resolver finds the MAC address behind an IP address.
type Resolver interface {
	Lookup(ctx context.Context, addr netip.Addr) (Result, error)
}

// ResolverFunc adapts a function to a Resolver.
type ResolverFunc func(ctx context.Context, addr netip.Addr) (Result, error)

// Lookup implements Resolver.
func (f ResolverFunc) Lookup(ctx context.Context, addr netip.Addr) (Result, error) {
	return f(ctx, addr)
}

var (
	defMu  sync.RWMutex
	defRes Resolver = systemResolver{}
)

// Default returns the resolver used when none is configured.
func Default() Resolver {
	defMu.RLock()
	defer defMu.RUnlock()
	return defRes
}

// SetDefault replaces the default resolver, for tests and for deployments
// that learn MACs elsewhere, such as from a switch.
func SetDefault(r Resolver) {
	defMu.Lock()
	defRes = r
	defMu.Unlock()
}

// Lookup resolves addr with the default resolver.
func Lookup(ctx context.Context, addr netip.Addr) (Result, error) {
	return Default().Lookup(ctx, addr)
}

type systemResolver struct{}

func (systemResolver) Lookup(ctx context.Context, addr netip.Addr) (Result, error) {
	addr = addr.Unmap()
	if addr.IsLoopback() {
		return Result{Loopback: true, OnLink: true, Interface: "lo"}, ErrNotFound
	}
	if addr.IsUnspecified() || !addr.IsValid() {
		return Result{}, ErrNotFound
	}
	ifc, onLink := onLinkInterface(addr)
	if !onLink {
		return Result{}, ErrNotFound // routed: the table would hold the gateway
	}
	res := Result{OnLink: true, Interface: ifc}
	mac, err := lookupNeighbor(ctx, addr, ifc)
	if err != nil {
		return res, err
	}
	res.MAC, res.Source = mac, SourceNeighbor
	return res, nil
}

// onLinkInterface reports the interface whose attached subnet contains addr.
func onLinkInterface(addr netip.Addr) (string, bool) {
	ifs, err := net.Interfaces()
	if err != nil {
		return "", false
	}
	for _, ifc := range ifs {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			ones, _ := ipn.Mask.Size()
			if p := netip.PrefixFrom(ip.Unmap(), ones); p.Contains(addr) {
				if ip.Unmap() == addr {
					return ifc.Name, false // our own address
				}
				return ifc.Name, true
			}
		}
	}
	return "", false
}

// StaticResolver answers from a fixed table, for tests and for sites that
// keep their own inventory.
type StaticResolver map[netip.Addr]net.HardwareAddr

// Lookup implements Resolver.
func (s StaticResolver) Lookup(_ context.Context, addr netip.Addr) (Result, error) {
	if m, ok := s[addr.Unmap()]; ok {
		return Result{MAC: m, Source: SourceStatic, OnLink: true}, nil
	}
	return Result{}, ErrNotFound
}

// lookupTimeout bounds a resolver call that may block in a system call.
func LookupTimeout(r Resolver, addr netip.Addr, d time.Duration) (Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	type out struct {
		r   Result
		err error
	}
	ch := make(chan out, 1)
	go func() { r, err := r.Lookup(ctx, addr); ch <- out{r, err} }()
	select {
	case o := <-ch:
		return o.r, o.err
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

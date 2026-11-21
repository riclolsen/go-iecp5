// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package netpeer

import (
	"encoding/json"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"
)

// ChangeKind classifies what an observation revealed.
type ChangeKind uint8

// Change kinds.
const (
	// MACChanged: the IP was last seen with a different MAC. Possible ARP
	// spoofing or a man in the middle, or a legitimate device swap.
	MACChanged ChangeKind = iota + 1
	// IPChanged: the MAC was last seen with a different IP that is no longer
	// active. Usual with DHCP; also what a host taking over an address does.
	IPChanged
	// MACShared: the MAC is in use with another IP that is still active. A
	// proxy-ARP device, or one host claiming several addresses.
	MACShared
	// CertChanged: the IP presented a different certificate than before.
	CertChanged
)

// Change is one thing an observation revealed.
type Change struct {
	Kind    ChangeKind
	IP      netip.Addr
	MAC     net.HardwareAddr
	PrevMAC net.HardwareAddr // MACChanged
	OtherIP netip.Addr       // IPChanged, MACShared
	PrevFP  string           // CertChanged
	FP      string           // CertChanged
}

// CacheOptions tune a Cache. Zero values take the defaults.
type CacheOptions struct {
	// MaxEntries bounds the memory of the cache (default 4096).
	MaxEntries int
	// TTL is how long an unseen IP is remembered (default 30 days).
	TTL time.Duration
	// ActiveWindow is how recently a MAC must have been seen with another IP
	// for that IP to count as still active (default 10 minutes).
	ActiveWindow time.Duration
	// Now overrides the clock, for tests.
	Now func() time.Time
}

type ipRec struct {
	MAC      string    `json:"mac,omitempty"`
	First    time.Time `json:"first"`
	Last     time.Time `json:"last"`
	CertFP   string    `json:"cert_fp,omitempty"`
	Reported bool      `json:"-"` // "no MAC available" already reported
}

// Cache remembers which MAC and certificate went with which IP address. It
// is safe for concurrent use and bounded in size, so hostile peers cannot
// grow it without limit.
type Cache struct {
	opt CacheOptions
	mu  sync.Mutex
	ips map[netip.Addr]*ipRec
	// last IP observed for a MAC, with the time
	macs map[string]macRec
}

type macRec struct {
	ip   netip.Addr
	last time.Time
}

// NewCache creates a Cache.
func NewCache(opt CacheOptions) *Cache {
	if opt.MaxEntries <= 0 {
		opt.MaxEntries = 4096
	}
	if opt.TTL <= 0 {
		opt.TTL = 30 * 24 * time.Hour
	}
	if opt.ActiveWindow <= 0 {
		opt.ActiveWindow = 10 * time.Minute
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	return &Cache{opt: opt, ips: map[netip.Addr]*ipRec{}, macs: map[string]macRec{}}
}

// Observe records that ip was seen with mac and returns what that revealed.
func (c *Cache) Observe(ip netip.Addr, mac net.HardwareAddr) []Change {
	ip = ip.Unmap()
	if len(mac) == 0 {
		return nil
	}
	now := c.opt.Now()
	ms := mac.String()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire(now)

	var out []Change
	r := c.ips[ip]
	if r == nil {
		c.makeRoom()
		r = &ipRec{First: now}
		c.ips[ip] = r
	} else if r.MAC != "" && r.MAC != ms {
		prev, _ := net.ParseMAC(r.MAC)
		out = append(out, Change{Kind: MACChanged, IP: ip, MAC: mac, PrevMAC: prev})
	}
	if m, ok := c.macs[ms]; ok && m.ip != ip {
		kind := IPChanged
		if now.Sub(m.last) < c.opt.ActiveWindow {
			kind = MACShared
		}
		out = append(out, Change{Kind: kind, IP: ip, MAC: mac, OtherIP: m.ip})
	}
	r.MAC, r.Last = ms, now
	if _, ok := c.macs[ms]; !ok {
		c.makeMACRoom()
	}
	c.macs[ms] = macRec{ip: ip, last: now}
	return out
}

// ObserveCert records the fingerprint of the certificate ip presented and
// reports whether it differs from the last one.
func (c *Cache) ObserveCert(ip netip.Addr, fingerprint string) (Change, bool) {
	ip = ip.Unmap()
	if fingerprint == "" {
		return Change{}, false
	}
	now := c.opt.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire(now)
	r := c.ips[ip]
	if r == nil {
		c.makeRoom()
		r = &ipRec{First: now}
		c.ips[ip] = r
	}
	prev := r.CertFP
	r.CertFP, r.Last = fingerprint, now
	if prev != "" && prev != fingerprint {
		return Change{Kind: CertChanged, IP: ip, PrevFP: prev, FP: fingerprint}, true
	}
	return Change{}, false
}

// FirstMissing reports true once per IP when no MAC could be found for it, so
// that "no MAC available" is raised once and not on every connection.
func (c *Cache) FirstMissing(ip netip.Addr) bool {
	ip = ip.Unmap()
	now := c.opt.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.ips[ip]
	if r == nil {
		c.makeRoom()
		r = &ipRec{First: now, Last: now}
		c.ips[ip] = r
	}
	if r.Reported {
		return false
	}
	r.Reported = true
	return true
}

// MAC returns the MAC last seen with ip.
func (c *Cache) MAC(ip netip.Addr) (net.HardwareAddr, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.ips[ip.Unmap()]
	if r == nil || r.MAC == "" {
		return nil, false
	}
	m, _ := net.ParseMAC(r.MAC)
	return m, true
}

// Len is the number of remembered IP addresses.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.ips)
}

func (c *Cache) expire(now time.Time) {
	if len(c.ips) >= c.opt.MaxEntries/2 {
		for ip, r := range c.ips {
			if now.Sub(r.Last) > c.opt.TTL {
				c.removeIP(ip)
			}
		}
	}
	// The reverse index is bounded on its own: it can hold MACs that no
	// longer belong to any remembered IP.
	if len(c.macs) >= c.opt.MaxEntries/2 {
		for m, r := range c.macs {
			if now.Sub(r.last) > c.opt.TTL {
				delete(c.macs, m)
			}
		}
	}
}

// removeIP forgets ip and the reverse entry that points at it.
func (c *Cache) removeIP(ip netip.Addr) {
	if r := c.ips[ip]; r != nil && r.MAC != "" {
		c.dropMAC(r.MAC, ip)
	}
	delete(c.ips, ip)
}

// dropMAC removes the reverse entry of mac if it still points at ip.
func (c *Cache) dropMAC(mac string, ip netip.Addr) {
	if m, ok := c.macs[mac]; ok && m.ip == ip {
		delete(c.macs, mac)
	}
}

// makeMACRoom evicts the least recently seen MAC when the reverse index is
// full.
func (c *Cache) makeMACRoom() {
	if len(c.macs) < c.opt.MaxEntries {
		return
	}
	var oldest string
	var ot time.Time
	first := true
	for m, r := range c.macs {
		if first || r.last.Before(ot) {
			oldest, ot, first = m, r.last, false
		}
	}
	delete(c.macs, oldest)
}

// makeRoom evicts the least recently seen entry when the cache is full.
func (c *Cache) makeRoom() {
	if len(c.ips) < c.opt.MaxEntries {
		return
	}
	var oldest netip.Addr
	var ot time.Time
	first := true
	for ip, r := range c.ips {
		if first || r.Last.Before(ot) {
			oldest, ot, first = ip, r.Last, false
		}
	}
	c.removeIP(oldest)
}

type saved struct {
	IPs map[string]*ipRec `json:"ips"`
}

// Save writes the cache as JSON, so a restart does not forget the baseline
// and miss a device swapped while the process was down.
func (c *Cache) Save(w io.Writer) error {
	c.mu.Lock()
	s := saved{IPs: make(map[string]*ipRec, len(c.ips))}
	for ip, r := range c.ips {
		cp := *r
		s.IPs[ip.String()] = &cp
	}
	c.mu.Unlock()
	return json.NewEncoder(w).Encode(s)
}

// Load reads what Save wrote, replacing the contents.
func (c *Cache) Load(r io.Reader) error {
	var s saved
	if err := json.NewDecoder(r).Decode(&s); err != nil {
		return err
	}
	ips := make(map[netip.Addr]*ipRec, len(s.IPs))
	macs := map[string]macRec{}
	for k, v := range s.IPs {
		ip, err := netip.ParseAddr(k)
		if err != nil || v == nil {
			continue
		}
		ips[ip.Unmap()] = v
		if v.MAC != "" {
			if m, ok := macs[v.MAC]; !ok || v.Last.After(m.last) {
				macs[v.MAC] = macRec{ip: ip.Unmap(), last: v.Last}
			}
		}
	}
	c.mu.Lock()
	c.ips, c.macs = ips, macs
	c.mu.Unlock()
	return nil
}

var (
	defCacheOnce sync.Once
	defCache     *Cache
)

// DefaultCache is the process-wide cache used when none is configured, so
// that a Server and a Client in one process share what they have seen.
func DefaultCache() *Cache {
	defCacheOnce.Do(func() { defCache = NewCache(CacheOptions{}) })
	return defCache
}

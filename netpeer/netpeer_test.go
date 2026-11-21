package netpeer

import (
	"bytes"
	"context"
	"net"
	"net/netip"
	"testing"
	"time"
)

func mac(s string) net.HardwareAddr { m, _ := net.ParseMAC(s); return m }

func TestCacheDetectsChanges(t *testing.T) {
	now := time.Unix(1000, 0)
	c := NewCache(CacheOptions{Now: func() time.Time { return now }})
	a, b := netip.MustParseAddr("10.0.0.5"), netip.MustParseAddr("10.0.0.6")
	m1, m2 := mac("aa:bb:cc:00:00:01"), mac("aa:bb:cc:00:00:02")

	if ch := c.Observe(a, m1); len(ch) != 0 {
		t.Fatalf("first sight changed: %+v", ch)
	}
	now = now.Add(time.Minute)
	if ch := c.Observe(a, m1); len(ch) != 0 {
		t.Fatalf("same pair changed: %+v", ch)
	}
	// Same IP, new MAC.
	now = now.Add(time.Minute)
	ch := c.Observe(a, m2)
	if len(ch) != 1 || ch[0].Kind != MACChanged || ch[0].PrevMAC.String() != m1.String() {
		t.Fatalf("want MACChanged from m1, got %+v", ch)
	}
	// m2 now also claims b while a is still active: shared.
	ch = c.Observe(b, m2)
	if len(ch) != 1 || ch[0].Kind != MACShared || ch[0].OtherIP != a {
		t.Fatalf("want MACShared, got %+v", ch)
	}
	// Much later m2 appears on a third address: the old ones are stale.
	now = now.Add(time.Hour)
	ch = c.Observe(netip.MustParseAddr("10.0.0.7"), m2)
	if len(ch) != 1 || ch[0].Kind != IPChanged {
		t.Fatalf("want IPChanged, got %+v", ch)
	}
}

func TestCacheCertAndMissing(t *testing.T) {
	c := NewCache(CacheOptions{})
	ip := netip.MustParseAddr("192.168.1.9")
	if _, ch := c.ObserveCert(ip, "aa"); ch {
		t.Error("first cert reported as changed")
	}
	if ch, ok := c.ObserveCert(ip, "bb"); !ok || ch.PrevFP != "aa" {
		t.Errorf("cert change not seen: %+v", ch)
	}
	if !c.FirstMissing(ip) || c.FirstMissing(ip) {
		t.Error("FirstMissing must be true exactly once")
	}
}

func TestCacheBoundedAndPersistent(t *testing.T) {
	c := NewCache(CacheOptions{MaxEntries: 8})
	for i := 0; i < 100; i++ {
		c.Observe(netip.AddrFrom4([4]byte{10, 0, byte(i >> 8), byte(i)}), mac("aa:bb:cc:00:00:01"))
	}
	if c.Len() > 8 {
		t.Fatalf("cache grew to %d with MaxEntries 8", c.Len())
	}
	var buf bytes.Buffer
	if err := c.Save(&buf); err != nil {
		t.Fatal(err)
	}
	d := NewCache(CacheOptions{})
	if err := d.Load(&buf); err != nil {
		t.Fatal(err)
	}
	last := netip.AddrFrom4([4]byte{10, 0, 0, 99})
	if m, ok := d.MAC(last); !ok || m.String() != "aa:bb:cc:00:00:01" {
		t.Errorf("not restored: %v %v", m, ok)
	}
	// The baseline survives a restart, so a swap is still caught.
	if ch := d.Observe(last, mac("aa:bb:cc:00:00:09")); len(ch) == 0 || ch[0].Kind != MACChanged {
		t.Errorf("swap after reload not detected: %+v", ch)
	}
}

func TestLoopbackIsOnLinkWithoutMAC(t *testing.T) {
	r, err := Lookup(context.Background(), netip.MustParseAddr("127.0.0.1"))
	if err == nil || !r.Loopback || r.MAC != nil {
		t.Errorf("loopback: %+v, %v", r, err)
	}
}

func TestStaticResolverAndTimeout(t *testing.T) {
	ip := netip.MustParseAddr("10.1.1.1")
	r := StaticResolver{ip: mac("de:ad:be:ef:00:01")}
	res, err := LookupTimeout(r, ip, time.Second)
	if err != nil || res.Source != SourceStatic {
		t.Fatalf("%+v %v", res, err)
	}
	slow := ResolverFunc(func(ctx context.Context, _ netip.Addr) (Result, error) {
		<-ctx.Done()
		return Result{}, ctx.Err()
	})
	start := time.Now()
	if _, err := LookupTimeout(slow, ip, 50*time.Millisecond); err == nil || time.Since(start) > time.Second {
		t.Errorf("slow resolver not bounded: %v", err)
	}
}

// The MAC index is bounded on its own: one IP cycling through MACs must not
// grow it without limit.
func TestCacheReverseIndexBounded(t *testing.T) {
	c := NewCache(CacheOptions{MaxEntries: 8})
	ip := netip.MustParseAddr("10.0.0.1")
	for i := 0; i < 1000; i++ {
		c.Observe(ip, net.HardwareAddr{2, 0, 0, 0, byte(i >> 8), byte(i)})
	}
	for i := 0; i < 1000; i++ {
		c.Observe(netip.AddrFrom4([4]byte{10, 1, byte(i >> 8), byte(i)}), net.HardwareAddr{4, 0, 0, 0, byte(i >> 8), byte(i)})
	}
	c.mu.Lock()
	n := len(c.macs)
	c.mu.Unlock()
	if n > 8 || c.Len() > 8 {
		t.Fatalf("reverse index %d, ips %d, with MaxEntries 8", n, c.Len())
	}
}

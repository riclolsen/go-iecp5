//go:build darwin || freebsd || openbsd || netbsd

package netpeer

import (
	"net/netip"
	"testing"
)

func TestParseBSDNeighbor(t *testing.T) {
	a := netip.MustParseAddr("192.168.1.5")
	m, err := parseBSDNeighbor("? (192.168.1.5) at aa:bb:cc:dd:ee:ff on en0 ifscope [ethernet]\n", a)
	if err != nil || m.String() != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("%v %v", m, err)
	}
	if _, err := parseBSDNeighbor("? (192.168.1.5) at (incomplete) on en0\n", a); err == nil {
		t.Error("incomplete entry accepted")
	}
}

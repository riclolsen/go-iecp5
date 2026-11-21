package netpeer

import (
	"encoding/binary"
	"strings"
	"testing"
)

const procARP = `IP address       HW type     Flags       HW address            Mask     Device
192.168.1.1      0x1         0x2         aa:bb:cc:dd:ee:01     *        eth0
192.168.1.77     0x1         0x0         00:00:00:00:00:00     *        eth0
10.0.0.9         0x1         0x6         aa:bb:cc:dd:ee:09     *        eth1
`

func TestParseProcARPSkipsIncomplete(t *testing.T) {
	es, err := parseProcARP(strings.NewReader(procARP))
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 2 || es[0].addr.String() != "192.168.1.1" || es[1].mac.String() != "aa:bb:cc:dd:ee:09" {
		t.Errorf("%+v", es)
	}
}

// A canned RTM_NEWNEIGH message: ndmsg for 192.168.1.1 REACHABLE with a MAC.
func TestParseNeighborsCannedNetlink(t *testing.T) {
	attr := func(typ uint16, v []byte) []byte {
		b := make([]byte, 4, 4+len(v)+3)
		binary.LittleEndian.PutUint16(b[0:], uint16(4+len(v)))
		binary.LittleEndian.PutUint16(b[2:], typ)
		b = append(b, v...)
		for len(b)%4 != 0 {
			b = append(b, 0)
		}
		return b
	}
	nd := make([]byte, 12)
	nd[0] = 2 // AF_INET
	binary.LittleEndian.PutUint32(nd[4:], 3)
	binary.LittleEndian.PutUint16(nd[8:], 0x02) // REACHABLE
	payload := append(nd, attr(1, []byte{192, 168, 1, 1})...)
	payload = append(payload, attr(2, []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x01})...)
	hdr := make([]byte, 16)
	binary.LittleEndian.PutUint32(hdr[0:], uint32(16+len(payload)))
	binary.LittleEndian.PutUint16(hdr[4:], 28) // RTM_NEWNEIGH
	msg := append(hdr, payload...)

	es, err := parseNeighbors(msg)
	if err != nil || len(es) != 1 {
		t.Fatalf("%v %+v", err, es)
	}
	if es[0].addr.String() != "192.168.1.1" || es[0].mac.String() != "aa:bb:cc:dd:ee:01" || es[0].ifidx != 3 {
		t.Errorf("%+v", es[0])
	}
	// FAILED entries carry no usable address.
	binary.LittleEndian.PutUint16(msg[16+8:], 0x20)
	if es, _ := parseNeighbors(msg); len(es) != 0 {
		t.Errorf("failed entry returned: %+v", es)
	}
}

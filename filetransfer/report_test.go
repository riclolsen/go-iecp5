package filetransfer

import (
	"net"
	"testing"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/secevent"
)

type reportingConn struct{ got []secevent.Event }

func (c *reportingConn) Params() *asdu.Params              { return asdu.ParamsWide }
func (c *reportingConn) Send(*asdu.ASDU) error             { return nil }
func (c *reportingConn) UnderlyingConn() net.Conn          { return nil }
func (c *reportingConn) ReportCyberEvent(e secevent.Event) { c.got = append(c.got, e) }

func TestFailuresAreReported(t *testing.T) {
	c := &reportingConn{}
	a := asdu.NewASDU(asdu.ParamsWide, asdu.Identifier{Type: asdu.F_SC_NA_1, CommonAddr: 5})
	report(c, a, ErrChecksum)
	report(c, a, ErrFileNotFound)
	report(c, a, ErrUnsupported)
	report(c, a, nil)
	want := []secevent.Kind{secevent.KindFileChecksum, secevent.KindFileRejected, secevent.KindFileUnsupported}
	if len(c.got) != len(want) {
		t.Fatalf("%d events, want %d", len(c.got), len(want))
	}
	for i, k := range want {
		if c.got[i].Kind != k {
			t.Errorf("event %d is %s, want %s", i, c.got[i].Kind, k)
		}
	}
	if c.got[0].CommonAddr != 5 {
		t.Errorf("common address %d", c.got[0].CommonAddr)
	}
}

package cs103

import (
	"sync"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/cs101"
	"github.com/riclolsen/go-iecp5/secevent"
)

type collector struct {
	mu sync.Mutex
	ev []secevent.Event
}

func (c *collector) Handle(e secevent.Event) { c.mu.Lock(); c.ev = append(c.ev, e); c.mu.Unlock() }

func (c *collector) wait(t *testing.T, k secevent.Kind) secevent.Event {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		c.mu.Lock()
		for _, e := range c.ev {
			if e.Kind == k {
				c.mu.Unlock()
				return e
			}
		}
		c.mu.Unlock()
	}
	t.Fatalf("no %s event", k)
	return secevent.Event{}
}

func TestClientLinkEvents(t *testing.T) {
	portA, portB := pipeLine()
	c := &collector{}
	cli, stop := startClient(t, &recHandler{}, testConfig(), portA)
	defer stop()
	cli.SetCyberEventSink(c)
	defer cli.cyber.em.Close()
	go func() {
		buf := make([]byte, 256)
		for {
			if _, err := portB.Read(buf); err != nil {
				return
			}
		}
	}()

	// A status answer from a station that is not polled.
	ctrl := byte(cs101.SecFcRespStatus)
	if _, err := portB.Write([]byte{cs101.StartFixed, ctrl, 0x09, ctrl + 0x09, cs101.EndChar}); err != nil {
		t.Fatal(err)
	}
	if e := c.wait(t, secevent.KindLinkUnexpectedAddr); e.LinkAddr != 9 {
		t.Errorf("link address %d", e.LinkAddr)
	}

	// A bad checksum.
	if _, err := portB.Write([]byte{cs101.StartFixed, ctrl, 0x01, 0x00, cs101.EndChar}); err != nil {
		t.Fatal(err)
	}
	c.wait(t, secevent.KindProtoCS101Frame)
}

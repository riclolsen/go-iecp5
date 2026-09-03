// Command cs104_server_general is an IEC 60870-5-104 outstation that
// simulates a small substation: over a hundred information objects covering
// every monitored type this library supports, with values that move and
// quality descriptors that cover every flag, plus commands and file transfer.
//
// It exists to be pointed at. A master is only as tested as the device it was
// tested against, and a device that reports twelve good analogues is not a
// test of anything.
//
//	go run .                 # serve on :2404
//	go run . -listen :2405 -period 2s
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/cs104"
	"github.com/riclolsen/go-iecp5/filetransfer"
)

// Files this outstation offers for transfer, addressed independently of
// everything else.
const (
	fileRecordIoa = asdu.InfoObjAddr(100)
	fileEventsIoa = asdu.InfoObjAddr(101)
)

func main() {
	var (
		listen = flag.String("listen", ":2404", "address to listen on")
		period = flag.Duration("period", time.Second, "how often the process advances and reports")
		quiet  = flag.Bool("quiet", false, "do not log the protocol")
		offer  = flag.Bool("offer-file", true, "announce the disturbance record after a master connects")
		scale  = flag.Int("scale", 1, "repeat the whole address plan this many times, at 10000 address strides — for testing a master against a large database")
	)
	flag.Parse()

	sim := newSim(*scale)
	store := seedFiles()
	h := &handler{
		sim:    sim,
		sender: filetransfer.NewSender(store).SetSectionSize(4096),
	}

	srv := cs104.NewServer(h)
	srv.LogMode(!*quiet)
	srv.SetOnConnectionHandler(func(c asdu.Connect) {
		log.Println("master connected")
		// A device that has just come up says so, and a master uses that to
		// know its picture is stale.
		go func() {
			time.Sleep(500 * time.Millisecond)
			sim.endOfInitialization(c)
			if *offer {
				time.Sleep(1500 * time.Millisecond)
				if err := h.sender.Offer(c, simCA, fileRecordIoa, asdu.FileDisturbanceData); err != nil {
					log.Printf("offer file: %v", err)
				} else {
					log.Println("announced the disturbance record (F_FR_NA_1)")
				}
			}
		}()
	})
	srv.SetConnectionLostHandler(func(c asdu.Connect) {
		log.Println("master disconnected")
		h.sel.clear()
		ok, errs := sendStats()
		log.Printf("sends: %d succeeded, %d failed", ok, errs)
	})

	// The process runs whether or not anyone is watching; reports only go
	// out when a master is connected.
	go func() {
		t := time.NewTicker(*period)
		defer t.Stop()
		for range t.C {
			sim.advance()
			if srv.GetSessionsLen() > 0 {
				// The broadcast retries each session on its own, so a
				// master that is behind does not cost the others their
				// data and does not get duplicates.
				ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
				sim.spontaneous(srv.WaitingConn(ctx))
				cancel()
			}
		}
	}()

	log.Printf("IEC 60870-5-104 outstation on %s", *listen)
	log.Printf("common address %d · %d information objects · 2 files",
		simCA, totalObjects(*scale))
	if *scale > 1 {
		log.Printf("the address plan is repeated %d times at %d strides: the last copy starts at %d",
			*scale, ioaStride, (*scale-1)*ioaStride)
	}
	log.Printf("commands at %d..%d — see commands.go for what each one moves",
		ioaCmdSingle, ioaCmdBits)
	if err := srv.ListenAndServer(*listen); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}

// seedFiles fills the file store a master can browse and fetch.
func seedFiles() *filetransfer.MemStore {
	store := filetransfer.NewMemStore()

	record := []byte("COMTRADE-LIKE SAMPLE RECORD\n")
	for i := 0; i < 400; i++ {
		record = append(record, []byte(fmt.Sprintf("%04d,%8.3f,%8.3f\n",
			i, 10*float64(i%50), 230+float64(i%7)))...)
	}
	if err := store.Write(fileRecordIoa, asdu.FileDisturbanceData, record); err != nil {
		log.Fatalf("seed disturbance record: %v", err)
	}

	events := []byte("2026-01-01T00:00:00Z trip L1\n2026-01-01T00:00:01Z reclose\n")
	if err := store.Write(fileEventsIoa, asdu.FileSequencesOfEvents, events); err != nil {
		log.Fatalf("seed event list: %v", err)
	}
	return store
}

// handler is the outstation's application layer.
type handler struct {
	sim    *sim
	sender *filetransfer.Sender
	sel    selection
}

func (h *handler) InterrogationHandler(c asdu.Connect, a *asdu.ASDU, qoi asdu.QualifierOfInterrogation) error {
	if qoi != asdu.QOIStation {
		// Group interrogation is not simulated; saying so is more useful
		// than answering a group with the whole database.
		log.Printf("interrogation: group qualifier %d not supported", qoi)
		a.Coa.IsNegative = true
		return a.SendReplyMirror(c, asdu.ActivationCon)
	}
	log.Println("general interrogation")
	_ = a.SendReplyMirror(c, asdu.ActivationCon)
	h.sim.interrogation(c, asdu.CauseOfTransmission{Cause: asdu.InterrogatedByStation})
	return a.SendReplyMirror(c, asdu.ActivationTerm)
}

func (h *handler) CounterInterrogationHandler(c asdu.Connect, a *asdu.ASDU, qcc asdu.QualifierCountCall) error {
	log.Printf("counter interrogation, request %d freeze %d", qcc.Request, qcc.Freeze)
	_ = a.SendReplyMirror(c, asdu.ActivationCon)
	h.sim.counterInterrogation(c,
		asdu.CauseOfTransmission{Cause: asdu.RequestByGeneralCounter})
	return a.SendReplyMirror(c, asdu.ActivationTerm)
}

// ReadHandler answers a read command for one address, which is how a master
// asks about a single object rather than the whole database.
func (h *handler) ReadHandler(c asdu.Connect, a *asdu.ASDU, ioa asdu.InfoObjAddr) error {
	if h.sim.readOne(c, ioa) {
		return nil
	}
	log.Printf("read command: nothing at ioa %d", ioa)
	return a.SendReplyMirror(c, asdu.UnknownIOA)
}

func (h *handler) ClockSyncHandler(c asdu.Connect, a *asdu.ASDU, t time.Time) error {
	log.Printf("clock synchronisation: %s (this simulator keeps its own clock)",
		t.Format(time.RFC3339))
	return a.SendReplyMirror(c, asdu.ActivationCon)
}

func (h *handler) ResetProcessHandler(c asdu.Connect, a *asdu.ASDU, qrp asdu.QualifierOfResetProcessCmd) error {
	log.Printf("reset process, qualifier %d", qrp)
	h.sel.clear()
	return a.SendReplyMirror(c, asdu.ActivationCon)
}

func (h *handler) DelayAcquisitionHandler(c asdu.Connect, a *asdu.ASDU, msec uint16) error {
	log.Printf("delay acquisition, %d ms", msec)
	return a.SendReplyMirror(c, asdu.ActivationCon)
}

func (h *handler) ASDUHandlerAll(asdu.Connect, *asdu.ASDU, int) error { return nil }

// ASDUHandler takes everything the dispatcher does not route elsewhere: the
// process commands and the file transfer.
func (h *handler) ASDUHandler(c asdu.Connect, a *asdu.ASDU) error {
	if handled, err := h.sender.Handle(c, a); handled {
		if err != nil {
			log.Printf("file transfer: %v", err)
		}
		return nil
	}
	if h.handleCommand(c, a) {
		return nil
	}
	log.Printf("unhandled %s", typeName(a.Type))
	return a.SendReplyMirror(c, asdu.UnknownTypeID)
}

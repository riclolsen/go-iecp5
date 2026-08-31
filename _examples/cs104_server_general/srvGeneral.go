package main

import (
	"fmt"
	"log"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/cs104"
	"github.com/riclolsen/go-iecp5/filetransfer"
)

// Files this outstation offers for transfer. A real device would fill the
// store with captured disturbance records; here two sample files are seeded
// at start-up. Files are addressed by (information object address, name of
// file).
const (
	recordIoa = asdu.InfoObjAddr(100) // the point the record belongs to
	eventsIoa = asdu.InfoObjAddr(101)
)

func seedFiles(store *filetransfer.MemStore) {
	// A fake disturbance record, big enough to need several sections.
	record := []byte("COMTRADE-LIKE SAMPLE RECORD\n")
	for i := 0; i < 400; i++ {
		record = append(record, []byte(fmt.Sprintf("%04d,%8.3f,%8.3f\n",
			i, 10*float64(i%50), 230+float64(i%7)))...)
	}
	if err := store.Write(recordIoa, asdu.FileDisturbanceData, record); err != nil {
		log.Fatalf("seed disturbance record: %v", err)
	}

	events := []byte("2026-01-01T00:00:00Z trip L1\n2026-01-01T00:00:01Z reclose\n")
	if err := store.Write(eventsIoa, asdu.FileSequencesOfEvents, events); err != nil {
		log.Fatalf("seed event list: %v", err)
	}
}

func main() {
	store := filetransfer.NewMemStore()
	seedFiles(store)

	// The Sender serves the file transfer ASDUs; the handler below delegates
	// to it. SetSectionSize controls how much data each checksummed section
	// carries.
	sender := filetransfer.NewSender(store).SetSectionSize(4096)

	srv := cs104.NewServer(&mysrv{sender: sender})
	srv.SetOnConnectionHandler(func(c asdu.Connect) {
		log.Println("on connect")
		// Announce the disturbance record a moment after the master
		// connects (it must activate data transfer first). A master that
		// accepts the offer pulls the file across on its own.
		go func() {
			time.Sleep(2 * time.Second)
			if err := sender.Offer(c, 1, recordIoa, asdu.FileDisturbanceData); err != nil {
				log.Printf("offer file: %v", err)
			} else {
				log.Println("announced disturbance record (F_FR_NA_1)")
			}
		}()
	})
	srv.SetConnectionLostHandler(func(c asdu.Connect) {
		log.Println("connect lost")
	})
	srv.LogMode(true)

	log.Println("serving IEC 104 on :2404 with 2 files available for transfer")
	if err := srv.ListenAndServer(":2404"); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}

type mysrv struct {
	sender *filetransfer.Sender
}

func (sf *mysrv) InterrogationHandler(c asdu.Connect, asduPack *asdu.ASDU, qoi asdu.QualifierOfInterrogation) error {
	log.Println("qoi", qoi)
	asduPack.SendReplyMirror(c, asdu.ActivationCon)
	err := asdu.Single(c, false, asdu.CauseOfTransmission{Cause: asdu.InterrogatedByStation}, asdu.GlobalCommonAddr,
		asdu.SinglePointInfo{})
	if err != nil {
		// log.Println("falied")
	} else {
		// log.Println("success")
	}
	asduPack.SendReplyMirror(c, asdu.ActivationTerm)
	return nil
}
func (sf *mysrv) CounterInterrogationHandler(asdu.Connect, *asdu.ASDU, asdu.QualifierCountCall) error {
	return nil
}
func (sf *mysrv) ReadHandler(asdu.Connect, *asdu.ASDU, asdu.InfoObjAddr) error { return nil }
func (sf *mysrv) ClockSyncHandler(asdu.Connect, *asdu.ASDU, time.Time) error   { return nil }
func (sf *mysrv) ResetProcessHandler(asdu.Connect, *asdu.ASDU, asdu.QualifierOfResetProcessCmd) error {
	return nil
}
func (sf *mysrv) DelayAcquisitionHandler(asdu.Connect, *asdu.ASDU, uint16) error { return nil }

// ASDUHandler receives everything the dispatcher does not route to a
// dedicated handler — including the file transfer ASDUs, which are passed to
// the Sender.
func (sf *mysrv) ASDUHandler(c asdu.Connect, a *asdu.ASDU) error {
	if handled, err := sf.sender.Handle(c, a); handled {
		if err != nil {
			log.Printf("file transfer: %v", err)
		}
		return nil // consumed: do not mirror UnknownTypeID back
	}
	return nil
}

func (sf *mysrv) ASDUHandlerAll(asdu.Connect, *asdu.ASDU, int) error { return nil }

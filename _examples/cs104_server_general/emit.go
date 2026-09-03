package main

import (
	"log"
	"sync/atomic"
)

var sendOK, sendErr uint64

// emit sends one ASDU and does not pretend it succeeded.
//
// cs104's Send does not block: when the session's send buffer is full it
// returns ErrBufferFulled and queues nothing. Discarding that error is how an
// interrogation over a large database silently loses points — the outstation
// believes it answered in full and the master has holes it cannot see.
//
// The sends below go through cs104.Waiting, so a full buffer is waited out
// rather than refused; an error reaching here is a real one and is logged.
func emit(f func() error) {
	if err := f(); err != nil {
		atomic.AddUint64(&sendErr, 1)
		log.Printf("send: %v", err)
		return
	}
	atomic.AddUint64(&sendOK, 1)
}

func sendStats() (ok, errs uint64) {
	return atomic.LoadUint64(&sendOK), atomic.LoadUint64(&sendErr)
}

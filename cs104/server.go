// Copyright 2020 thinkgos (thinkgo@aliyun.com).  All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs104

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/clog"
)

// timeoutResolution is seconds according to companion standard 104,
// subclass 6.9, caption "Definition of time outs". However, then
// of a second make this system much more responsive i.c.w. S-frames.
const timeoutResolution = 100 * time.Millisecond

// Server the common server
type Server struct {
	config         Config
	params         asdu.Params
	handler        ServerHandlerInterface
	TLSConfig      *tls.Config
	mux            sync.Mutex
	sessions       map[*SrvSession]struct{}
	listen         net.Listener
	onConnection   func(asdu.Connect)
	connectionLost func(asdu.Connect)
	clog.Clog
	wg           sync.WaitGroup
	serverNumber int
}

// NewServer new a server, default config and default asdu.ParamsWide params
func NewServer(handler ServerHandlerInterface) *Server {
	return &Server{
		config:   DefaultConfig(),
		params:   *asdu.ParamsWide,
		handler:  handler,
		sessions: make(map[*SrvSession]struct{}),
		Clog:     clog.NewLogger("cs104 server => "),
	}
}

// SetConfig set config if config is valid it will use DefaultConfig()
func (sf *Server) SetConfig(cfg Config) *Server {
	if err := cfg.Valid(); err != nil {
		sf.config = DefaultConfig()
	} else {
		sf.config = cfg
	}
	return sf
}

// SetParams set asdu params if params is valid it will use asdu.ParamsWide
func (sf *Server) SetParams(p *asdu.Params) *Server {
	if err := p.Valid(); err != nil {
		sf.params = *asdu.ParamsWide
	} else {
		sf.params = *p
	}
	return sf
}

// SetTLSConfig set tls config
func (sf *Server) SetTLSConfig(t *tls.Config) *Server {
	sf.TLSConfig = t
	return sf
}

// ListenAndServer run the server. It blocks until the listener fails or the
// server is closed, and returns the error that stopped it.
func (sf *Server) ListenAndServer(addr string) error {
	var listen net.Listener
	var err error

	if sf.TLSConfig != nil {
		listen, err = tls.Listen("tcp", addr, sf.TLSConfig)
	} else {
		listen, err = net.Listen("tcp", addr)
	}

	if err != nil {
		sf.Critical("server run failed, %v", err)
		return err
	}
	sf.mux.Lock()
	sf.listen = listen
	sf.mux.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		_ = sf.Close()
		sf.Debug("server stop")
	}()
	sf.Debug("server run")
	for {
		conn, err := listen.Accept()
		if err != nil {
			sf.Critical("server run failed, %v", err)
			return err
		}

		sf.wg.Add(1)
		go func() {
			sess := &SrvSession{
				config:   &sf.config,
				params:   &sf.params,
				handler:  sf.handler,
				conn:     conn,
				rcvASDU:  make(chan []byte, sf.config.RecvUnAckLimitW<<4),
				sendASDU: make(chan []byte, sf.config.SendUnAckLimitK<<4),
				rcvRaw:   make(chan []byte, sf.config.RecvUnAckLimitW<<5),
				sendRaw:  make(chan []byte, sf.config.SendUnAckLimitK<<5), // may not block!

				onConnection:   sf.onConnection,
				connectionLost: sf.connectionLost,
				Clog:           sf.Clog,
				serverNumber:   sf.serverNumber,
			}
			sf.mux.Lock()
			sf.sessions[sess] = struct{}{}
			sf.mux.Unlock()
			sess.run(ctx)
			sf.mux.Lock()
			delete(sf.sessions, sess)
			sf.mux.Unlock()
			sf.wg.Done()
		}()
	}
}

// Close close the server
func (sf *Server) Close() error {
	var err error

	sf.mux.Lock()
	if sf.listen != nil {
		err = sf.listen.Close()
		sf.listen = nil
	}
	sf.mux.Unlock()
	sf.wg.Wait()
	return err
}

// Send imp interface Connect
// Send broadcasts an ASDU to every connected master.
//
// It reports an error when a session could not accept the ASDU: a session's
// send buffer is finite and SrvSession.Send does not block, so a burst that
// outruns the link is refused rather than queued. Every session is still
// attempted, so one master that is not keeping up does not stop the others.
//
// Do not discard this error. An outstation that ignores it believes it
// answered an interrogation in full while the master has holes it cannot
// see, which is the hardest kind of fault to find from either end. Use
// SendWait when the ASDU must go out even if the link is momentarily behind.
//
// A retry re-sends to the sessions that did accept the ASDU. When duplicates
// matter, use SendWait, which retries only the sessions that refused it.
func (sf *Server) Send(a *asdu.ASDU) error {
	sf.mux.Lock()
	var total, failed int
	var firstErr error
	for k := range sf.sessions {
		total++
		if err := k.Send(a.Clone()); err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	sf.mux.Unlock()

	switch {
	case failed == 0:
		return nil
	case failed == total:
		return firstErr
	default:
		return fmt.Errorf("%d of %d sessions could not accept the ASDU: %w",
			failed, total, firstErr)
	}
}

// SendWait broadcasts an ASDU, waiting for room when a session's send buffer
// is full rather than losing the ASDU for that master.
//
// Each session is retried on its own, so a session that already accepted the
// ASDU never receives it twice. Waiting is usually what an outstation wants:
// the buffer drains as the master acknowledges, so a full buffer means the
// link is momentarily behind, not that the data is unwanted.
//
// It gives up on a session when ctx is done, and reports how many sessions
// never took it.
func (sf *Server) SendWait(ctx context.Context, a *asdu.ASDU) error {
	const retry = 2 * time.Millisecond

	sf.mux.Lock()
	sessions := make([]*SrvSession, 0, len(sf.sessions))
	for k := range sf.sessions {
		sessions = append(sessions, k)
	}
	sf.mux.Unlock()

	var failed int
	var firstErr error
	for _, sess := range sessions {
		for {
			err := sess.Send(a.Clone())
			if err == nil {
				break
			}
			if !errors.Is(err, ErrBufferFulled) {
				// A closed connection or a malformed ASDU will not be fixed
				// by waiting.
				failed++
				if firstErr == nil {
					firstErr = err
				}
				break
			}
			select {
			case <-ctx.Done():
				failed++
				if firstErr == nil {
					firstErr = fmt.Errorf("send buffer full: %w", ctx.Err())
				}
			case <-time.After(retry):
				continue
			}
			break
		}
	}

	switch {
	case failed == 0:
		return nil
	case failed == len(sessions):
		return firstErr
	default:
		return fmt.Errorf("%d of %d sessions could not accept the ASDU: %w",
			failed, len(sessions), firstErr)
	}
}

// Params imp interface Connect
func (sf *Server) Params() *asdu.Params { return &sf.params }

// UnderlyingConn imp interface Connect
func (sf *Server) UnderlyingConn() net.Conn { return nil }

// SetInfoObjTimeZone set info object time zone
func (sf *Server) SetInfoObjTimeZone(zone *time.Location) {
	sf.params.InfoObjTimeZone = zone
}

// SetOnConnectionHandler set on connect handler
func (sf *Server) SetOnConnectionHandler(f func(asdu.Connect)) {
	sf.onConnection = f
}

// SetConnectionLostHandler set connect lost handler
func (sf *Server) SetConnectionLostHandler(f func(asdu.Connect)) {
	sf.connectionLost = f
}

// Get the number of sessions
func (sf *Server) GetSessionsLen() int {
	sf.mux.Lock()
	n := len(sf.sessions)
	sf.mux.Unlock()
	return n
}

func (sf *Server) SetServerNumber(n int) {
	sf.serverNumber = n
}

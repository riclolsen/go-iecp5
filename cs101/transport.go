// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs101

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"go.bug.st/serial"
)

// TransportType selects how FT1.2 frames are carried.
type TransportType byte

const (
	// TransportSerial uses a local serial port (the default).
	TransportSerial TransportType = iota
	// TransportTCPClient dials out to TCP.Address and runs the FT1.2 byte
	// stream over the TCP connection — the usual setup with a terminal
	// server / serial-device server encapsulating the line.
	TransportTCPClient
	// TransportTCPServer listens on TCP.Address and serves one incoming
	// connection at a time.
	TransportTCPServer
)

func (t TransportType) String() string {
	switch t {
	case TransportSerial:
		return "serial"
	case TransportTCPClient:
		return "tcp-client"
	case TransportTCPServer:
		return "tcp-server"
	default:
		return fmt.Sprintf("transport<%d>", byte(t))
	}
}

// DefaultTCPConnectTimeout bounds TCP dialing when no timeout is configured.
const DefaultTCPConnectTimeout = 30 * time.Second

// TCPConfig configures the TCP encapsulation transport.
type TCPConfig struct {
	// Address is "host:port" to dial (TransportTCPClient) or the listen
	// address (TransportTCPServer, e.g. ":2400").
	Address string
	// ConnectTimeout bounds dialing and the TLS handshake
	// (TransportTCPClient). Default 30 s.
	ConnectTimeout time.Duration
	// TLSConfig enables TLS on the TCP transport when non-nil.
	TLSConfig *tls.Config
}

// Transporter opens byte-stream connections for a configured transport.
// It is used by the cs101 and cs103 endpoints; with TransportTCPServer it
// keeps its listener open across connections.
type Transporter struct {
	typ    TransportType
	serial SerialConfig
	tcp    TCPConfig

	mu       sync.Mutex
	listener net.Listener
	watched  bool
	acc      *tcpAcceptor
	busy     atomic.Bool
	onBusy   func(net.Addr)
}

// tcpAcceptor owns the listener's Accept loop. One connection is served at a
// time; while it is, any other that arrives is accepted and closed at once.
// Left in the kernel backlog instead, it would sit unanswered until the
// active one dropped, which gives a port scanner a free slot and tells the
// operator nothing.
type tcpAcceptor struct {
	conns chan net.Conn
	done  chan struct{}
	err   error
}

// busyConn marks the Transporter free again when the connection closes.
type busyConn struct {
	net.Conn
	once sync.Once
	free func()
}

func (c *busyConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.free)
	return err
}

// SetBusyHandler sets a function called with the remote address of every
// connection turned away because another is being served (TCP server
// transport only).
func (tr *Transporter) SetBusyHandler(f func(net.Addr)) {
	tr.mu.Lock()
	tr.onBusy = f
	tr.mu.Unlock()
}

// NewTransporter builds a Transporter for the given transport selection.
func NewTransporter(typ TransportType, serialCfg SerialConfig, tcpCfg TCPConfig) *Transporter {
	if tcpCfg.ConnectTimeout <= 0 {
		tcpCfg.ConnectTimeout = DefaultTCPConnectTimeout
	}
	return &Transporter{typ: typ, serial: serialCfg, tcp: tcpCfg}
}

// Open establishes one connection and returns it together with a
// human-readable description for logging. For TransportTCPServer it blocks
// until a peer connects or ctx is done; the listener is created on first
// use and closed when ctx is done or Close is called.
func (tr *Transporter) Open(ctx context.Context) (io.ReadWriteCloser, string, error) {
	switch tr.typ {
	case TransportTCPClient:
		d := net.Dialer{Timeout: tr.tcp.ConnectTimeout}
		conn, err := d.DialContext(ctx, "tcp", tr.tcp.Address)
		if err != nil {
			return nil, "", err
		}
		if tr.tcp.TLSConfig != nil {
			tlsConn := tls.Client(conn, tr.tcp.TLSConfig)
			_ = tlsConn.SetDeadline(time.Now().Add(tr.tcp.ConnectTimeout))
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				_ = conn.Close()
				return nil, "", err
			}
			_ = tlsConn.SetDeadline(time.Time{})
			return tlsConn, "tcp+tls -> " + tr.tcp.Address, nil
		}
		return conn, "tcp -> " + tr.tcp.Address, nil

	case TransportTCPServer:
		l, acc, err := tr.getListener(ctx)
		if err != nil {
			return nil, "", err
		}
		select {
		case conn := <-acc.conns:
			tr.busy.Store(true)
			bc := &busyConn{Conn: conn, free: func() { tr.busy.Store(false) }}
			return bc, fmt.Sprintf("tcp-listen %s (peer %s)", l.Addr(), conn.RemoteAddr()), nil
		case <-acc.done:
			if ctx.Err() != nil {
				return nil, "", ctx.Err()
			}
			return nil, "", acc.err
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}

	default: // TransportSerial
		mode := &serial.Mode{
			BaudRate: tr.serial.BaudRate,
			DataBits: tr.serial.DataBits,
			Parity:   tr.serial.Parity,
			StopBits: tr.serial.StopBits,
		}
		port, err := serial.Open(tr.serial.Address, mode)
		if err == nil && tr.serial.Timeout > 0 {
			err = port.SetReadTimeout(tr.serial.Timeout)
		}
		if err != nil {
			if port != nil {
				_ = port.Close()
			}
			return nil, "", err
		}
		return port, "serial " + tr.serial.Address, nil
	}
}

func (tr *Transporter) getListener(ctx context.Context) (net.Listener, *tcpAcceptor, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.listener != nil {
		return tr.listener, tr.acc, nil
	}
	var l net.Listener
	var err error
	if tr.tcp.TLSConfig != nil {
		l, err = tls.Listen("tcp", tr.tcp.Address, tr.tcp.TLSConfig)
	} else {
		l, err = net.Listen("tcp", tr.tcp.Address)
	}
	if err != nil {
		return nil, nil, err
	}
	tr.listener = l
	acc := &tcpAcceptor{conns: make(chan net.Conn, 1), done: make(chan struct{})}
	tr.acc = acc
	go tr.acceptLoop(l, acc)
	if !tr.watched {
		tr.watched = true
		// Unblock a pending Accept when the endpoint shuts down.
		go func() {
			<-ctx.Done()
			_ = tr.Close()
		}()
	}
	return l, acc, nil
}

func (tr *Transporter) acceptLoop(l net.Listener, acc *tcpAcceptor) {
	defer close(acc.done)
	for {
		conn, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				acc.err = err
				return
			}
			// Not fatal (descriptor exhaustion and the like): keep serving.
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if tr.busy.Load() {
			tr.mu.Lock()
			f := tr.onBusy
			tr.mu.Unlock()
			if f != nil {
				f(conn.RemoteAddr())
			}
			_ = conn.Close()
			continue
		}
		select {
		case acc.conns <- conn:
		default:
			_ = conn.Close() // one already waiting for Open
		}
	}
}

// Close closes the listener (if any). Connections handed out by Open are
// owned and closed by the endpoint that requested them.
func (tr *Transporter) Close() error {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.listener != nil {
		err := tr.listener.Close()
		tr.listener = nil
		return err
	}
	return nil
}

// ListenAddr returns the bound listener address (nil when not listening).
// Useful when TCP.Address was configured with port 0.
func (tr *Transporter) ListenAddr() net.Addr {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.listener != nil {
		return tr.listener.Addr()
	}
	return nil
}

// transportLabel returns a short description of the configured endpoint
// address, for logger prefixes.
func (sf *Config) transportLabel() string {
	if sf.Transport == TransportSerial {
		return sf.Serial.Address
	}
	return sf.TCP.Address
}

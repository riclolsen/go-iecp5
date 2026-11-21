package cs104

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/riclolsen/go-iecp5/secevent"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newCA(t *testing.T, name string) *testCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &testCA{cert, key, pool}
}

func (ca *testCA) issue(t *testing.T, cn string, notAfter time.Duration) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(notAfter),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func tlsServer(t *testing.T, ca *testCA, cfg Config) (*evCollector, string) {
	t.Helper()
	srvCert := ca.issue(t, "outstation", 24*time.Hour)
	addr := freeAddr(t)
	c := &evCollector{}
	srv := NewServer(&nullHandler{})
	srv.LogMode(false)
	srv.SetConfig(cfg)
	srv.SetCyberEventSink(c)
	srv.SetTLSConfig(&tls.Config{Certificates: []tls.Certificate{srvCert}, ClientCAs: ca.pool,
		ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS12})
	go func() { _ = srv.ListenAndServer(addr) }()
	t.Cleanup(func() { _ = srv.Close(); srv.SetCyberEventSink(nil) })
	return c, addr
}

func TestTLSEvents(t *testing.T) {
	ca, other := newCA(t, "site CA"), newCA(t, "attacker CA")
	cfg := DefaultConfig()
	cfg.ConnectTimeout0 = time.Second
	c, addr := tlsServer(t, ca, cfg)
	good := ca.issue(t, "master", 24*time.Hour)

	handshake := func(clientCfg *tls.Config) error {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			return err
		}
		defer conn.Close()
		tc := tls.Client(conn, clientCfg)
		_ = tc.SetDeadline(time.Now().Add(2 * time.Second))
		if err := tc.Handshake(); err != nil {
			return err
		}
		// TLS 1.3: the server's verdict arrives with the first read.
		_, err = tc.Read(make([]byte, 1))
		return err
	}
	base := func() *tls.Config {
		return &tls.Config{RootCAs: ca.pool, MinVersion: tls.VersionTLS12, ServerName: "127.0.0.1"}
	}

	t.Run("client certificate from an unknown CA", func(t *testing.T) {
		cc := base()
		evil := other.issue(t, "evil master", time.Hour)
		// Offer it even though the server did not list its issuer, as an attacker would.
		cc.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &evil, nil }
		_ = handshake(cc)
		e := c.wait(t, secevent.KindTLSCertRejected)
		if e.Severity != secevent.SevAlert || e.Peer.TLS == nil || e.Peer.TLS.Subject == "" ||
			len(e.Peer.TLS.FingerprintSHA256) != 64 {
			t.Errorf("%+v / %+v", e, e.Peer.TLS)
		}
	})
	t.Run("no client certificate", func(t *testing.T) {
		_ = handshake(base())
		c.wait(t, secevent.KindTLSNoClientCert)
	})
	t.Run("plaintext IEC 104 on the TLS port", func(t *testing.T) {
		conn := dialRetry(t, addr)
		defer conn.Close()
		_, _ = conn.Write([]byte{0x68, 4, 0x07, 0, 0, 0})
		c.wait(t, secevent.KindTLSPlaintext)
	})
	t.Run("a scanner speaking HTTP", func(t *testing.T) {
		conn := dialRetry(t, addr)
		defer conn.Close()
		_, _ = conn.Write([]byte("GET / HTTP/1.1\r\n\r\n"))
		c.wait(t, secevent.KindTLSNonTLSProbe)
	})
	t.Run("handshake that never completes", func(t *testing.T) {
		conn := dialRetry(t, addr)
		defer conn.Close()
		c.wait(t, secevent.KindTLSHandshakeTO)
	})
	t.Run("a valid master is served and its certificate is observed", func(t *testing.T) {
		cc := base()
		cc.Certificates = []tls.Certificate{good}
		conn, err := tls.Dial("tcp", addr, cc)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		e := c.wait(t, secevent.KindConnAccepted)
		if e.Peer.TLS == nil || !e.Peer.TLS.Verified || e.Peer.TLS.Subject != "CN=master" {
			t.Errorf("accepted TLS peer: %+v", e.Peer.TLS)
		}
	})
	t.Run("its certificate changes for the same IP", func(t *testing.T) {
		cc := base()
		cc.Certificates = []tls.Certificate{ca.issue(t, "master", 24*time.Hour)} // new key, same name
		conn, err := tls.Dial("tcp", addr, cc)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		c.wait(t, secevent.KindTLSCertChanged)
	})
	t.Run("an expiring certificate", func(t *testing.T) {
		cc := base()
		cc.Certificates = []tls.Certificate{ca.issue(t, "short-lived", 2*time.Hour)}
		conn, err := tls.Dial("tcp", addr, cc)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		c.wait(t, secevent.KindTLSCertExpiring)
	})
}

// The client side: a server whose certificate is not trusted.
func TestClientRejectsUntrustedServerCert(t *testing.T) {
	ca, attacker := newCA(t, "site CA"), newCA(t, "attacker CA")
	srvCert := attacker.issue(t, "impostor", time.Hour)
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{srvCert}})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { _ = c.(*tls.Conn).Handshake(); c.Close() }()
		}
	}()

	c := &evCollector{}
	opt := NewOption()
	_ = opt.AddRemoteServer("tcps://" + l.Addr().String())
	opt.SetAutoReconnect(false)
	opt.SetTLSConfig(&tls.Config{RootCAs: ca.pool, MinVersion: tls.VersionTLS12})
	cl := NewClient(nopClientHandler{}, opt)
	cl.LogMode(false)
	cl.SetCyberEventSink(c)
	defer cl.SetCyberEventSink(nil)
	_ = cl.Start()
	e := c.wait(t, secevent.KindTLSCertRejected)
	if e.Role != "client" || e.Peer.TLS == nil || e.Peer.TLS.Subject != "CN=impostor" {
		t.Errorf("%+v / %+v", e, e.Peer.TLS)
	}
	c.wait(t, secevent.KindConnDialFailed)
}

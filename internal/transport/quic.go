// Package transport establishes an authenticated, encrypted, multiplexed
// connection over a rendezvous Session using QUIC.
//
// Authentication model: the host generates an ephemeral self-signed
// certificate and publishes its SHA-256 fingerprint through the (separately
// reachable) signaling server. The client pins that fingerprint, so a MITM on
// the relay/UDP path cannot impersonate the host without also compromising the
// signaling channel. The shared session code gates who may pair at all.
package transport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/chriswirz/relay-tunnel/internal/signal"
	"github.com/quic-go/quic-go"
)

const alpn = "relay-tunnel/1"

// GenerateCert creates an ephemeral P-256 self-signed cert and returns it plus
// its SHA-256 fingerprint (hex of the DER, matching client-side pinning).
func GenerateCert() (tls.Certificate, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "relay-tunnel"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	sum := sha256.Sum256(der)
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	return cert, hex.EncodeToString(sum[:]), nil
}

func quicConfig() *quic.Config {
	return &quic.Config{
		MaxIdleTimeout:  60 * time.Second,
		KeepAlivePeriod: 15 * time.Second,
	}
}

// Listen (host side) accepts exactly one incoming QUIC connection over the
// session's packet conn.
func Listen(ctx context.Context, sess *signal.Session, cert tls.Certificate) (*quic.Conn, error) {
	tr := &quic.Transport{Conn: sess.PacketConn}
	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{alpn},
		MinVersion:   tls.VersionTLS13,
	}
	ln, err := tr.Listen(tlsConf, quicConfig())
	if err != nil {
		return nil, err
	}
	conn, err := ln.Accept(ctx)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// Dial (client side) connects to the host and verifies its pinned fingerprint.
func Dial(ctx context.Context, sess *signal.Session) (*quic.Conn, error) {
	if sess.CertFingerprint == "" {
		return nil, errors.New("missing host cert fingerprint; cannot verify peer")
	}
	want := sess.CertFingerprint
	tlsConf := &tls.Config{
		InsecureSkipVerify: true, // we pin manually below
		NextProtos:         []string{alpn},
		MinVersion:         tls.VersionTLS13,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("peer presented no certificate")
			}
			sum := sha256.Sum256(rawCerts[0])
			got := hex.EncodeToString(sum[:])
			if got != want {
				return fmt.Errorf("cert fingerprint mismatch: got %s want %s", got, want)
			}
			return nil
		},
	}
	tr := &quic.Transport{Conn: sess.PacketConn}
	conn, err := tr.Dial(ctx, sess.PeerAddr, tlsConf, quicConfig())
	if err != nil {
		return nil, err
	}
	return conn, nil
}

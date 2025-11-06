// Package nat performs UDP address reflection and hole punching, and provides
// net.PacketConn adapters that quic-go can run over either a direct punched
// path or a server-relayed path.
package nat

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/chriswirz/relay-tunnel/internal/xlog"
)

// Wire constants must match internal/signal/server.go.
const (
	magic     = "RT01"
	magicAddr = "RTAD"
	tokenLen  = 16
	hdrLen    = 4 + tokenLen
)

// Reflect sends a reflection probe to the server and returns the public address
// the server observed for this socket.
func Reflect(sock *net.UDPConn, serverUDP string, token string) (string, error) {
	srv, err := net.ResolveUDPAddr("udp", serverUDP)
	if err != nil {
		return "", err
	}
	probe := append([]byte(magic), []byte(token)...) // empty payload => reflection
	buf := make([]byte, 1500)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := sock.WriteToUDP(probe, srv); err != nil {
			return "", err
		}
		sock.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, _, err := sock.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		if n > 4 && string(buf[:4]) == magicAddr {
			sock.SetReadDeadline(time.Time{})
			return string(buf[4:n]), nil
		}
	}
	return "", errors.New("reflection timed out")
}

// Punch attempts to open a direct path to peerAddr from sock by exchanging
// small probes. It returns nil on success. Both peers must call this roughly
// simultaneously (the server's "go" message coordinates that).
func Punch(sock *net.UDPConn, peerAddr string, timeout time.Duration) error {
	peer, err := net.ResolveUDPAddr("udp", peerAddr)
	if err != nil {
		return err
	}
	const hello = "RTPUNCH"
	const ack = "RTPUNCH-ACK"
	buf := make([]byte, 1500)
	deadline := time.Now().Add(timeout)
	gotPeer := false
	for time.Now().Before(deadline) {
		sock.WriteToUDP([]byte(hello), peer)
		if gotPeer {
			sock.WriteToUDP([]byte(ack), peer)
		}
		sock.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, from, err := sock.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		if !sameHostPort(from, peer) {
			continue
		}
		msg := string(buf[:n])
		switch msg {
		case hello:
			gotPeer = true
		case ack:
			// Peer has heard us; drain the deadline and succeed.
			sock.SetReadDeadline(time.Time{})
			xlog.Debugf("hole punch succeeded with %s", peerAddr)
			// Send a couple more acks so the peer also exits.
			for i := 0; i < 3; i++ {
				sock.WriteToUDP([]byte(ack), peer)
			}
			return nil
		}
	}
	sock.SetReadDeadline(time.Time{})
	return fmt.Errorf("hole punch to %s timed out", peerAddr)
}

func sameHostPort(a, b *net.UDPAddr) bool {
	return a.Port == b.Port && a.IP.Equal(b.IP)
}

// DirectConn wraps a *net.UDPConn as a net.PacketConn whose datagrams are all
// implicitly to/from the punched peer. quic-go uses WriteTo/ReadFrom with the
// peer address; we simply pass through.
//
// It must not define its own methods that shadow those of the embedded
// *net.UDPConn: on Linux, quic-go takes an OOB/batch read path for
// OOB-capable connections and requires the packet conn to also implement
// net.Conn. Shadowing, e.g. RemoteAddr, would break that and cause
// "OOBCapablePacketConn must implement net.Conn or ReadBatch".
type DirectConn struct {
	*net.UDPConn
}

// Compile-time checks that DirectConn keeps satisfying both interfaces quic-go
// relies on (net.PacketConn always, net.Conn for the OOB fast path).
var (
	_ net.PacketConn = DirectConn{}
	_ net.Conn       = DirectConn{}
)

// RelayConn adapts a UDP socket so that every datagram is tunneled through the
// rendezvous server: outbound datagrams are framed magic+token+payload and sent
// to the server; inbound datagrams from the server are unframed. To quic-go the
// peer appears at a single synthetic address (the server), which is fine because
// QUIC identifies connections by its own connection IDs, not the 4-tuple.
type RelayConn struct {
	sock      *net.UDPConn
	serverUDP *net.UDPAddr
	token     []byte // our token (framing tag)
}

// NewRelayConn builds a relayed packet conn.
func NewRelayConn(sock *net.UDPConn, serverUDP, token string) (*RelayConn, error) {
	srv, err := net.ResolveUDPAddr("udp", serverUDP)
	if err != nil {
		return nil, err
	}
	if len(token) != tokenLen {
		return nil, fmt.Errorf("bad token length %d", len(token))
	}
	return &RelayConn{sock: sock, serverUDP: srv, token: []byte(token)}, nil
}

// ReadFrom implements net.PacketConn. It strips the relay header and reports the
// server as the source address (the synthetic peer address).
func (r *RelayConn) ReadFrom(p []byte) (int, net.Addr, error) {
	buf := make([]byte, len(p)+hdrLen)
	for {
		n, _, err := r.sock.ReadFromUDP(buf)
		if err != nil {
			return 0, nil, err
		}
		if n < hdrLen || string(buf[:4]) != magic {
			continue // ignore stray/punch packets
		}
		payload := buf[hdrLen:n]
		copy(p, payload)
		return len(payload), r.serverUDP, nil
	}
}

// WriteTo implements net.PacketConn. addr is ignored (always the peer via relay).
func (r *RelayConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	out := make([]byte, 0, hdrLen+len(p))
	out = append(out, magic...)
	out = append(out, r.token...)
	out = append(out, p...)
	if _, err := r.sock.WriteToUDP(out, r.serverUDP); err != nil {
		return 0, err
	}
	return len(p), nil
}

// RemoteAddr returns the synthetic peer address (the server) for relay mode.
func (r *RelayConn) RemoteAddr() net.Addr { return r.serverUDP }

// The remaining methods satisfy net.PacketConn.
func (r *RelayConn) Close() error                       { return r.sock.Close() }
func (r *RelayConn) LocalAddr() net.Addr                { return r.sock.LocalAddr() }
func (r *RelayConn) SetDeadline(t time.Time) error      { return r.sock.SetDeadline(t) }
func (r *RelayConn) SetReadDeadline(t time.Time) error  { return r.sock.SetReadDeadline(t) }
func (r *RelayConn) SetWriteDeadline(t time.Time) error { return r.sock.SetWriteDeadline(t) }

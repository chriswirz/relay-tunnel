// Package nat performs UDP address reflection and hole punching, and provides
// net.PacketConn adapters that quic-go can run over either a direct punched
// path or a server-relayed path.
package nat

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
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

// Punch attempts to open a direct path to the peer from sock, racing every
// candidate address the peer offered and returning the first that answers.
//
// Racing matters more than it looks. A peer's reflected public address is the
// only one the server can observe, but it is the wrong address whenever both
// peers sit behind the same NAT: reaching it means asking the router to loop a
// packet back to the inside network (NAT hairpinning), which plenty of routers
// will not do. The peer's own LAN addresses are the ones that work in that
// case, and they cost nothing to try alongside.
//
// Both peers must call this at roughly the same time, which the server's "go"
// message coordinates.
func Punch(sock *net.UDPConn, candidates []string, timeout time.Duration) (*net.UDPAddr, error) {
	peers, err := resolveCandidates(candidates)
	if err != nil {
		return nil, err
	}
	const hello = "RTPUNCH"
	const ack = "RTPUNCH-ACK"
	buf := make([]byte, 1500)
	deadline := time.Now().Add(timeout)
	// heard records candidates that have sent us a hello, so later rounds
	// acknowledge them. A candidate can go quiet again (a dropped ack), so the
	// hello keeps being sent to every candidate until one completes.
	heard := make(map[string]bool, len(peers))

	for time.Now().Before(deadline) {
		for _, peer := range peers {
			sock.WriteToUDP([]byte(hello), peer)
			if heard[peer.String()] {
				sock.WriteToUDP([]byte(ack), peer)
			}
		}
		sock.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, from, err := sock.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		// Only an address the peer actually offered may complete the punch;
		// anything else on this socket is a stray probe or relayed traffic.
		match := matchCandidate(peers, from)
		if match == nil {
			continue
		}
		switch string(buf[:n]) {
		case hello:
			// Answer immediately rather than waiting for the next round: the
			// peer is listening right now, and this is what turns its hello
			// into the ack that ends its own loop.
			heard[match.String()] = true
			sock.WriteToUDP([]byte(ack), match)
		case ack:
			sock.SetReadDeadline(time.Time{})
			xlog.Debugf("hole punch succeeded with %s", match)
			// A few more acks so the peer also exits, rather than waiting out
			// its full timeout after we have already stopped listening.
			for i := 0; i < 3; i++ {
				sock.WriteToUDP([]byte(ack), match)
			}
			return match, nil
		}
	}
	sock.SetReadDeadline(time.Time{})
	return nil, fmt.Errorf("hole punch to %s timed out", strings.Join(candidates, ", "))
}

// resolveCandidates turns the offered addresses into UDP addresses, dropping
// duplicates and anything unusable. A peer behind no NAT reports the same
// address twice (reflected and local), and probing it twice per round is
// merely wasteful; an unparseable entry from a newer peer is skipped rather
// than failing the whole punch.
func resolveCandidates(candidates []string) ([]*net.UDPAddr, error) {
	out := make([]*net.UDPAddr, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		if c == "" {
			continue
		}
		addr, err := net.ResolveUDPAddr("udp", c)
		if err != nil {
			xlog.Debugf("ignoring unusable peer candidate %q: %v", c, err)
			continue
		}
		if seen[addr.String()] {
			continue
		}
		seen[addr.String()] = true
		out = append(out, addr)
	}
	if len(out) == 0 {
		return nil, errors.New("no usable peer addresses to punch to")
	}
	return out, nil
}

// matchCandidate reports which candidate a datagram came from, or nil.
func matchCandidate(peers []*net.UDPAddr, from *net.UDPAddr) *net.UDPAddr {
	for _, p := range peers {
		if sameHostPort(from, p) {
			return p
		}
	}
	return nil
}

// LocalCandidates lists the addresses this socket can be reached at from
// inside its own networks, as "ip:port" using the port the socket is bound to.
// These travel to the peer alongside the reflected public address, and are what
// let two peers behind one NAT find each other directly.
//
// Loopback is excluded because it can only ever describe this machine, and
// IPv6 link-local because reaching it needs a zone the peer cannot use.
func LocalCandidates(sock *net.UDPConn) []string {
	local, ok := sock.LocalAddr().(*net.UDPAddr)
	if !ok {
		return nil
	}
	port := local.Port
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		xlog.Debugf("could not enumerate local addresses: %v", err)
		return nil
	}
	var out []string
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, net.JoinHostPort(ip.String(), strconv.Itoa(port)))
	}
	return out
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

// SetReadBuffer and SetWriteBuffer hand quic-go through to the real socket.
// quic-go sizes the kernel buffers itself and warns loudly ("connection doesn't
// allow setting of receive buffer size") when the packet conn it is given does
// not offer them - which a hand-written net.PacketConn like this one does not,
// unlike DirectConn, which inherits them from the embedded *net.UDPConn. The
// relay path carries the whole session, so it is the path that most wants the
// larger buffers.
func (r *RelayConn) SetReadBuffer(n int) error  { return r.sock.SetReadBuffer(n) }
func (r *RelayConn) SetWriteBuffer(n int) error { return r.sock.SetWriteBuffer(n) }

// The remaining methods satisfy net.PacketConn.
func (r *RelayConn) Close() error                       { return r.sock.Close() }
func (r *RelayConn) LocalAddr() net.Addr                { return r.sock.LocalAddr() }
func (r *RelayConn) SetDeadline(t time.Time) error      { return r.sock.SetDeadline(t) }
func (r *RelayConn) SetReadDeadline(t time.Time) error  { return r.sock.SetReadDeadline(t) }
func (r *RelayConn) SetWriteDeadline(t time.Time) error { return r.sock.SetWriteDeadline(t) }

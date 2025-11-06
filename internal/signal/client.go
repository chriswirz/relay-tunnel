package signal

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/chriswirz/relay-tunnel/internal/nat"
	"github.com/chriswirz/relay-tunnel/internal/xlog"
	"github.com/gorilla/websocket"
)

// Session is the negotiated result handed to the transport layer.
type Session struct {
	Role            Role
	PacketConn      net.PacketConn // feed to quic-go
	PeerAddr        net.Addr       // remote address for quic-go to dial
	CertFingerprint string         // client side: expected host cert fingerprint
	Relayed         bool
}

// DialParams configures a rendezvous attempt.
type DialParams struct {
	ServerURL string // ws://host:7000/signal
	Code      string
	Role      Role
	// Name is an optional label for this peer, shown in the server's admin
	// interface. It identifies the machine to whoever runs the server; it is
	// not part of pairing, and carries no authority.
	Name string
	// CertFingerprint (host only): the fingerprint to advertise to the client.
	CertFingerprint string
	PunchTimeout    time.Duration
}

// Dial performs the full rendezvous: WS pairing, UDP reflection, hole punching,
// and falls back to server relay if the direct path fails. It returns a Session
// ready for QUIC.
func Dial(p DialParams) (*Session, error) {
	if p.PunchTimeout == 0 {
		p.PunchTimeout = 6 * time.Second
	}
	// Local UDP socket used for reflection, punching, and the tunnel itself.
	sock, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			sock.Close()
		}
	}()

	u, err := url.Parse(p.ServerURL)
	if err != nil {
		return nil, err
	}
	ws, resp, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return nil, err
	}
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	defer ws.Close()

	if err := ws.WriteJSON(Message{Type: TypeHello, Role: p.Role, Code: p.Code, Name: p.Name}); err != nil {
		return nil, err
	}

	// Expect Reflected with our relay token + server UDP addr.
	var refl Message
	if err := ws.ReadJSON(&refl); err != nil {
		return nil, err
	}
	if refl.Type == TypeError {
		return nil, errors.New(refl.Error)
	}
	if refl.Type != TypeReflected || refl.RelayToken == "" || refl.RelayAddr == "" {
		return nil, errors.New("unexpected server response during reflection")
	}

	public, err := nat.Reflect(sock, refl.RelayAddr, refl.RelayToken)
	if err != nil {
		return nil, err
	}
	xlog.Infof("public address: %s", public)

	// The reflected address is the only one the server can see. Send the
	// addresses of our own networks too, so a peer that turns out to be behind
	// the same NAT has something reachable to aim at.
	locals := nat.LocalCandidates(sock)
	if len(locals) > 0 {
		xlog.Debugf("local addresses: %s", strings.Join(locals, ", "))
	}
	if err := ws.WriteJSON(Message{
		Type:            TypeReady,
		PublicAddr:      public,
		LocalAddrs:      locals,
		CertFingerprint: p.CertFingerprint,
	}); err != nil {
		return nil, err
	}

	// Wait for Paired then Go.
	var paired Message
	for {
		var m Message
		if err := ws.ReadJSON(&m); err != nil {
			return nil, err
		}
		if m.Type == TypeError {
			return nil, errors.New(m.Error)
		}
		if m.Type == TypePaired {
			paired = m
		}
		if m.Type == TypeGo {
			break
		}
	}
	if paired.PeerAddr == "" {
		return nil, errors.New("server did not provide peer address")
	}
	// The peer's reflected address first, then the addresses it reports for
	// itself. Punch races them all; the reflected one is what works across the
	// internet, and a local one is what works when both peers are behind the
	// same NAT and the router will not hairpin.
	candidates := append([]string{paired.PeerAddr}, paired.PeerLocalAddrs...)
	xlog.Infof("peer candidates: %s (starting hole punch)", strings.Join(candidates, ", "))

	sess := &Session{Role: p.Role, CertFingerprint: paired.CertFingerprint}

	// Try direct hole punch (unless the operator forces relay).
	forceRelay := os.Getenv("RT_FORCE_RELAY") == "1"
	if !forceRelay {
		if peer, err := nat.Punch(sock, candidates, p.PunchTimeout); err == nil {
			// Report which candidate won: "direct" over a LAN address and
			// "direct" through two NATs look identical otherwise, and they are
			// worth telling apart when a tunnel is slower than expected.
			xlog.Infof("direct P2P connection established with %s", peer)
			sess.PacketConn = nat.DirectConn{UDPConn: sock}
			sess.PeerAddr = peer
			ok = true
			return sess, nil
		}
		xlog.Infof("hole punch failed, falling back to server relay")
	} else {
		xlog.Infof("RT_FORCE_RELAY set, using server relay")
	}

	rc, err := nat.NewRelayConn(sock, refl.RelayAddr, refl.RelayToken)
	if err != nil {
		return nil, err
	}
	sess.PacketConn = rc
	sess.PeerAddr = rc.RemoteAddr()
	sess.Relayed = true
	ok = true
	return sess, nil
}

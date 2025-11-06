package signal

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/chriswirz/relay-tunnel/internal/xlog"
	"github.com/gorilla/websocket"
)

// ServerConfig configures the rendezvous server.
type ServerConfig struct {
	HTTPAddr string // e.g. ":7000" for the WebSocket signaling endpoint
	UDPAddr  string // e.g. ":7001" for reflection + relay
	// PublicUDP is the address peers should send UDP to. If empty it is derived
	// from the request host + UDP port. Set this when behind NAT/LB.
	PublicUDP string
	// PublicURL, if set, is the externally reachable signaling URL logged at
	// startup (for example wss://relay.example.com/signal behind a TLS proxy).
	// Informational only; it does not affect behavior.
	PublicURL string
}

// Server is the rendezvous/relay server.
type Server struct {
	cfg ServerConfig

	upgrader websocket.Upgrader

	mu       sync.Mutex
	sessions map[string]*session // by code
	// relay routing: token -> the paired peer's token, and token -> last UDP addr.
	// This state must outlive the signaling WebSocket, because peers only relay
	// after signaling completes; it is reclaimed by a TTL sweep.
	relayPeer map[string]string
	relayAddr map[string]*net.UDPAddr
	relaySeen map[string]time.Time

	udp *net.UDPConn
}

const relayTTL = 3 * time.Minute

type peerConn struct {
	role   Role
	conn   *websocket.Conn
	token  string
	public string // observed public UDP addr, filled on reflection
	fp     string // cert fingerprint (host only)
	ready  chan struct{}
	writeM sync.Mutex
}

type session struct {
	code   string
	peers  map[Role]*peerConn
	paired bool
}

// NewServer builds a server from config.
func NewServer(cfg ServerConfig) *Server {
	return &Server{
		cfg:       cfg,
		upgrader:  websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		sessions:  map[string]*session{},
		relayPeer: map[string]string{},
		relayAddr: map[string]*net.UDPAddr{},
		relaySeen: map[string]time.Time{},
	}
}

func randToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Run starts both listeners and blocks.
func (s *Server) Run() error {
	uaddr, err := net.ResolveUDPAddr("udp", s.cfg.UDPAddr)
	if err != nil {
		return err
	}
	s.udp, err = net.ListenUDP("udp", uaddr)
	if err != nil {
		return err
	}
	go s.serveUDP()
	go s.gcRelay()

	mux := http.NewServeMux()
	mux.HandleFunc("/signal", s.handleWS)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("relay-tunnel rendezvous server\n"))
	})
	xlog.Infof("signaling on %s (ws /signal), udp reflect/relay on %s", s.cfg.HTTPAddr, s.cfg.UDPAddr)
	if s.cfg.PublicURL != "" {
		xlog.Infof("clients should connect with --server %s", s.cfg.PublicURL)
	}
	return http.ListenAndServe(s.cfg.HTTPAddr, mux)
}

func (p *peerConn) send(m Message) error {
	p.writeM.Lock()
	defer p.writeM.Unlock()
	return p.conn.WriteJSON(m)
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	c, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()

	var hello Message
	if err := c.ReadJSON(&hello); err != nil || hello.Type != TypeHello {
		c.WriteJSON(Message{Type: TypeError, Error: "expected hello"})
		return
	}
	if hello.Code == "" || (hello.Role != RoleHost && hello.Role != RoleClient) {
		c.WriteJSON(Message{Type: TypeError, Error: "missing code or role"})
		return
	}

	token := randToken()
	pc := &peerConn{role: hello.Role, conn: c, token: token, ready: make(chan struct{})}

	// Register into session.
	s.mu.Lock()
	sess := s.sessions[hello.Code]
	if sess == nil {
		sess = &session{code: hello.Code, peers: map[Role]*peerConn{}}
		s.sessions[hello.Code] = sess
	}
	if sess.peers[hello.Role] != nil {
		s.mu.Unlock()
		c.WriteJSON(Message{Type: TypeError, Error: "a " + string(hello.Role) + " is already connected for this code"})
		return
	}
	sess.peers[hello.Role] = pc
	s.mu.Unlock()

	xlog.Infof("peer joined code=%s role=%s token=%s", hello.Code, hello.Role, token)

	// Tell the peer its relay token + where to send UDP probes.
	pc.send(Message{
		Type:       TypeReflected,
		RelayToken: token,
		RelayAddr:  s.publicUDP(r),
	})

	defer func() {
		s.mu.Lock()
		if sess.peers[hello.Role] == pc {
			delete(sess.peers, hello.Role)
			if len(sess.peers) == 0 {
				delete(s.sessions, hello.Code)
			}
		}
		s.mu.Unlock()
		// Relay routing (relayPeer/relayAddr) is intentionally NOT deleted here;
		// peers may still be relaying after signaling ends. The TTL sweep
		// reclaims it. token is otherwise unused past this point.
		_ = token
		xlog.Infof("peer left code=%s role=%s", hello.Code, hello.Role)
	}()

	// Read loop: expect a "ready" once the peer has reflected its public addr
	// (and, for host, discovered its fingerprint).
	for {
		var m Message
		if err := c.ReadJSON(&m); err != nil {
			return
		}
		switch m.Type {
		case TypeReady:
			pc.public = m.PublicAddr
			pc.fp = m.CertFingerprint
			select {
			case <-pc.ready:
			default:
				close(pc.ready)
			}
			s.tryPair(sess)
		}
	}
}

// tryPair fires once both peers are ready, exchanging addresses/fingerprint and
// telling both to begin connecting.
func (s *Server) tryPair(sess *session) {
	s.mu.Lock()
	host := sess.peers[RoleHost]
	client := sess.peers[RoleClient]
	if host == nil || client == nil || sess.paired {
		s.mu.Unlock()
		return
	}
	if !isClosed(host.ready) || !isClosed(client.ready) {
		s.mu.Unlock()
		return
	}
	sess.paired = true
	// Wire relay routing so datagrams can be forwarded if punching fails.
	s.relayPeer[host.token] = client.token
	s.relayPeer[client.token] = host.token
	s.mu.Unlock()

	host.send(Message{Type: TypePaired, PeerAddr: client.public, PeerRole: RoleClient, RelayToken: host.token})
	client.send(Message{Type: TypePaired, PeerAddr: host.public, PeerRole: RoleHost, CertFingerprint: host.fp, RelayToken: client.token})
	host.send(Message{Type: TypeGo})
	client.send(Message{Type: TypeGo})
	xlog.Infof("paired code=%s host=%s client=%s", sess.code, host.public, client.public)
}

func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func (s *Server) publicUDP(r *http.Request) string {
	if s.cfg.PublicUDP != "" {
		return s.cfg.PublicUDP
	}
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	_, port, _ := net.SplitHostPort(s.cfg.UDPAddr)
	return net.JoinHostPort(host, port)
}

// --- UDP reflection + relay -------------------------------------------------

// Reflection/relay wire format on the UDP socket:
//
//	probe:  "RT01" + token(16 hex bytes) + payload
//
// If payload is empty -> it's a reflection request; server replies with the
// observed public address as text: "RTAD" + "ip:port".
// If payload is non-empty -> it's tunnel data to relay to the paired token.
const (
	magic     = "RT01"
	magicAddr = "RTAD"
	tokenLen  = 16 // hex chars of an 8-byte token
	hdrLen    = 4 + tokenLen
)

func (s *Server) serveUDP() {
	buf := make([]byte, 64*1024)
	for {
		n, addr, err := s.udp.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if n < hdrLen || string(buf[:4]) != magic {
			continue
		}
		token := string(buf[4:hdrLen])
		payload := buf[hdrLen:n]

		s.mu.Lock()
		s.relayAddr[token] = addr
		s.relaySeen[token] = time.Now()
		peerTok := s.relayPeer[token]
		peerAddr := s.relayAddr[peerTok]
		s.mu.Unlock()

		if len(payload) == 0 {
			// Reflection request: reply with observed address.
			reply := append([]byte(magicAddr), []byte(addr.String())...)
			s.udp.WriteToUDP(reply, addr)
			continue
		}
		// Relay: forward payload to the paired peer, tagged with the SENDER's
		// token so the receiver knows the datagram is relayed (and from whom).
		if peerAddr != nil {
			out := make([]byte, 0, hdrLen+len(payload))
			out = append(out, magic...)
			out = append(out, token...)
			out = append(out, payload...)
			s.udp.WriteToUDP(out, peerAddr)
		}
	}
}

// gcRelay periodically reclaims relay routing state for tokens that have gone
// idle beyond relayTTL.
func (s *Server) gcRelay() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		s.mu.Lock()
		for tok, seen := range s.relaySeen {
			if now.Sub(seen) > relayTTL {
				delete(s.relaySeen, tok)
				delete(s.relayAddr, tok)
				delete(s.relayPeer, tok)
			}
		}
		s.mu.Unlock()
	}
}

// Small helper reused by client to marshal.
func mustJSON(m Message) []byte { b, _ := json.Marshal(m); return b }

var _ = mustJSON
var _ = time.Second

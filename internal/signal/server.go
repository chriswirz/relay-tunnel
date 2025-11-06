package signal

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/chriswirz/relay-tunnel/internal/xlog"
	"github.com/gorilla/websocket"
)

// ServerConfig configures the rendezvous server.
type ServerConfig struct {
	HTTPAddr string // e.g. ":7000" for the WebSocket signaling endpoint
	UDPAddr  string // e.g. ":7001" for reflection + relay
	// Name is an optional label for this server, shown in the admin interface.
	// An operator running more than one rendezvous tells them apart by it.
	Name string
	// PublicUDP is the address peers should send UDP to. If empty it is derived
	// from the request host + UDP port. Set this when behind NAT/LB.
	PublicUDP string
	// PublicURL, if set, is the externally reachable signaling URL logged at
	// startup (for example wss://relay.example.com/signal behind a TLS proxy).
	// Informational only; it does not affect behavior.
	PublicURL string
	// Version is the build this server is running, reported by the admin API.
	Version string
	// Admin configures the web interface served on the same HTTP listener.
	Admin AdminConfig
	// SaveAdmin persists an account change made in the web interface, normally
	// into the config file. An empty string for either argument means "leave
	// that one alone". A nil value makes the account page report that this
	// server has nowhere to write the change.
	SaveAdmin func(username, passwordHash string) error
	// UI is the embedded web interface. A nil value serves the API alone.
	UI Frontend
}

// AdminConfig configures the administrative web interface and its API. It
// mirrors the admin section of the config file; the command maps one onto the
// other, which is what keeps this package free of a dependency on the config.
type AdminConfig struct {
	// Disabled leaves the server with only /signal.
	Disabled bool
	// Username is the administrator's name; empty means "admin".
	Username string
	// PasswordHash is pbkdf2-sha256$iterations$salt$key, all base64. Empty means
	// no password has been chosen yet, and admin/admin is accepted once.
	PasswordHash string
	// SessionTTL is how long a signed-in browser stays signed in; 0 means 12h.
	SessionTTL time.Duration
	// TrustForwardedHeaders believes X-Forwarded-Proto and X-Forwarded-For.
	TrustForwardedHeaders bool
}

// Frontend serves the embedded web application. It is supplied by the command,
// which owns the go:embed of the exported frontend.
type Frontend interface {
	http.Handler
	// ServePage writes one exported page, such as "404", with the given status.
	// It reports false when that page is not in the export, which happens when
	// the binary was built without building the frontend.
	ServePage(w http.ResponseWriter, r *http.Request, name string, status int) bool
}

// Server is the rendezvous/relay server.
type Server struct {
	cfg ServerConfig

	upgrader websocket.Upgrader

	mu       sync.Mutex
	sessions map[string]*session // by code
	// relay routing, by token. This state must outlive the signaling WebSocket,
	// because peers only relay after signaling completes; it is reclaimed by a
	// TTL sweep.
	relays map[string]*relayRoute

	udp *net.UDPConn

	// started stamps the embedded assets and reports uptime.
	started time.Time
	// stats are the running totals the admin overview shows. They are read
	// without the mutex, so they are atomic rather than plain counters.
	stats serverStats

	// admin holds the signed in browsers; adminMu guards the account fields in
	// cfg, which the web interface can change at runtime.
	admin   *adminAuth
	adminMu sync.RWMutex
}

// serverStats are lifetime totals, kept for the admin overview.
type serverStats struct {
	peersJoined  atomic.Uint64
	pairs        atomic.Uint64
	reflections  atomic.Uint64
	relayedPkts  atomic.Uint64
	relayedBytes atomic.Uint64
	// droppedPkts counts relay datagrams with nowhere to go, which is the usual
	// symptom of a peer that outlived its pairing.
	droppedPkts atomic.Uint64

	// udpPackets counts every datagram read off the UDP socket, valid or not.
	// It is the one number that distinguishes "the firewall is eating the
	// probes" from "the probes arrive and something is wrong with them": if a
	// peer reports a reflection timeout while this stays flat, nothing is
	// reaching the socket at all.
	udpPackets atomic.Uint64
	// udpInvalid counts datagrams discarded for being too short or not carrying
	// the RT01 magic. Port scanners produce a few; a steady stream alongside
	// failing peers means something else is talking to this port.
	udpInvalid atomic.Uint64
	// lastUDPUnix and lastReflectUnix stamp the most recent datagram and the
	// most recent answered reflection, in Unix seconds so they stay lock-free.
	lastUDPUnix     atomic.Int64
	lastReflectUnix atomic.Int64
}

// stamp reads one of the atomic "last seen" marks as a time, reporting false
// when nothing has been recorded yet.
func stampOf(v *atomic.Int64) (time.Time, bool) {
	if n := v.Load(); n != 0 {
		return time.Unix(n, 0), true
	}
	return time.Time{}, false
}

const relayTTL = 3 * time.Minute

// relayRoute is one peer's side of a relayed pair, keyed by its relay token.
type relayRoute struct {
	// peer is the token datagrams from this one are forwarded to.
	peer string
	// code names the session the pairing came from, so the admin interface can
	// show a relay next to the rendezvous it belongs to.
	code string
	// role is which side of that session this token is.
	role Role
	// addr is where this peer was last seen sending from, and is where
	// datagrams for it are sent.
	addr *net.UDPAddr
	// first and seen bound the route's life; seen is what the TTL sweep reads.
	first time.Time
	seen  time.Time

	packets uint64
	bytes   uint64
}

type peerConn struct {
	role Role
	// name is the label the peer gave for itself, already sanitized. Empty when
	// it offered none.
	name   string
	conn   *websocket.Conn
	token  string
	public string // observed public UDP addr, filled on reflection
	// local are the addresses the peer reports for itself on its own networks.
	// The server never verifies or uses them; it only hands them to the peer.
	local  []string
	fp     string // cert fingerprint (host only)
	remote string // the WebSocket's own peer address
	joined time.Time
	ready  chan struct{}
	writeM sync.Mutex
}

type session struct {
	code    string
	peers   map[Role]*peerConn
	paired  bool
	created time.Time
	// pairedAt is when both peers were told to connect, zero until then.
	pairedAt time.Time
}

// NewServer builds a server from config.
func NewServer(cfg ServerConfig) *Server {
	s := &Server{
		cfg:      cfg,
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		sessions: map[string]*session{},
		relays:   map[string]*relayRoute{},
		started:  time.Now(),
		admin:    newAdminAuth(cfg.Admin.SessionTTL),
	}
	if !cfg.Admin.Disabled && cfg.Admin.PasswordHash == "" {
		// Naming the account matters: after a rename the default password
		// belongs to the new name, and the old one is gone for good.
		xlog.Infof("warning: no admin password set; the web interface accepts %s/%s once and will demand a new one",
			s.adminUsername(), initialAdminPassword)
	}
	return s
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

	xlog.Infof("signaling on %s (ws /signal), udp reflect/relay on %s", s.cfg.HTTPAddr, s.cfg.UDPAddr)
	if s.cfg.PublicURL != "" {
		xlog.Infof("clients should connect with --server %s", s.cfg.PublicURL)
	}
	if s.cfg.Admin.Disabled {
		xlog.Infof("admin interface disabled")
	} else {
		xlog.Infof("admin interface on http://%s/ (api under /api/v1, spec at /openapi.json)", s.cfg.HTTPAddr)
	}
	srv := &http.Server{
		Addr:    s.cfg.HTTPAddr,
		Handler: s.Handler(),
		// A signaling WebSocket is long lived and mostly idle, so only the
		// header read is bounded.
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return srv.ListenAndServe()
}

// Handler is the server's HTTP surface: signaling, and unless it is disabled,
// the admin API and web interface. It is exported so tests, and anything
// embedding this package, can serve it without binding a port.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/signal", s.handleWS)
	if s.cfg.Admin.Disabled {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			// Only the root gets the banner. Everything else is genuinely not
			// here, and answering 200 to /api/v1/status on a server with the
			// interface turned off would be a lie a client could act on.
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte("relay-tunnel rendezvous server\n"))
		})
		return mux
	}

	mux.HandleFunc("GET /api/v1/status", s.handleStatus)
	mux.HandleFunc("GET /api/v1/sessions", s.handleListSessions)
	mux.HandleFunc("GET /api/v1/sessions/{code}", s.handleGetSession)
	mux.HandleFunc("DELETE /api/v1/sessions/{code}", s.handleDeleteSession)
	mux.HandleFunc("GET /api/v1/relays", s.handleListRelays)
	mux.HandleFunc("DELETE /api/v1/relays/{token}", s.handleDeleteRelay)
	mux.HandleFunc("DELETE /api/v1/relays", s.handlePruneRelays)
	mux.HandleFunc("GET /api/v1/auth/session", s.handleAuthSession)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleAuthLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleAuthLogout)
	mux.HandleFunc("POST /api/v1/auth/account", s.handleAuthAccount)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	// The spec is unauthenticated on purpose: it documents how to authenticate.
	mux.HandleFunc("GET /openapi.json", s.handleOpenAPI)
	mux.HandleFunc("GET /api/v1/openapi.json", s.handleOpenAPI)

	if s.cfg.UI != nil {
		mux.Handle("/", s.cfg.UI)
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			// A build without the frontend still serves the API, and says so
			// rather than answering a browser with a bare 404.
			if r.URL.Path != "/" {
				writeJSONError(w, http.StatusNotFound, "not found")
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("relay-tunnel rendezvous server\n\nthis build has no web interface; the api is under /api/v1 and the spec is at /openapi.json\n"))
		})
	}
	return mux
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
	pc := &peerConn{
		role:   hello.Role,
		name:   sanitizeName(hello.Name),
		conn:   c,
		token:  token,
		remote: c.RemoteAddr().String(),
		joined: time.Now(),
		ready:  make(chan struct{}),
	}

	// Register into session.
	s.mu.Lock()
	sess := s.sessions[hello.Code]
	if sess == nil {
		sess = &session{code: hello.Code, peers: map[Role]*peerConn{}, created: time.Now()}
		s.sessions[hello.Code] = sess
	}
	if sess.peers[hello.Role] != nil {
		s.mu.Unlock()
		c.WriteJSON(Message{Type: TypeError, Error: "a " + string(hello.Role) + " is already connected for this code"})
		return
	}
	sess.peers[hello.Role] = pc
	s.mu.Unlock()

	s.stats.peersJoined.Add(1)
	xlog.Infof("peer joined code=%s role=%s%s token=%s", hello.Code, hello.Role, logName(pc.name), token)

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
		// Relay routing (s.relays) is intentionally NOT deleted here;
		// peers may still be relaying after signaling ends. The TTL sweep
		// reclaims it. token is otherwise unused past this point.
		_ = token
		xlog.Infof("peer left code=%s role=%s%s", hello.Code, hello.Role, logName(pc.name))
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
			pc.local = m.LocalAddrs
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
	sess.pairedAt = time.Now()
	// Wire relay routing so datagrams can be forwarded if punching fails.
	// Both peers have already reflected, which is what made them ready, so a
	// route for each token usually exists with the address it was reflected at.
	// Pairing fills in the rest rather than replacing it: that address is where
	// the first relayed datagram goes, before the peer has sent one of its own.
	s.pairLocked(host.token, client.token, sess.code, RoleHost)
	s.pairLocked(client.token, host.token, sess.code, RoleClient)
	s.mu.Unlock()
	s.stats.pairs.Add(1)

	host.send(Message{
		Type: TypePaired, PeerAddr: client.public, PeerLocalAddrs: client.local,
		PeerRole: RoleClient, RelayToken: host.token,
	})
	client.send(Message{
		Type: TypePaired, PeerAddr: host.public, PeerLocalAddrs: host.local,
		PeerRole: RoleHost, CertFingerprint: host.fp, RelayToken: client.token,
	})
	host.send(Message{Type: TypeGo})
	client.send(Message{Type: TypeGo})
	xlog.Infof("paired code=%s host=%s client=%s", sess.code, host.public, client.public)
}

// pairLocked points one token at its peer, creating the route if the peer has
// not reflected yet. The caller holds s.mu.
func (s *Server) pairLocked(token, peer, code string, role Role) {
	route := s.relays[token]
	if route == nil {
		route = &relayRoute{first: time.Now(), seen: time.Now()}
		s.relays[token] = route
	}
	route.peer = peer
	route.code = code
	route.role = role
}

// maxNameLen bounds a peer-supplied name. It is long enough for a hostname and
// short enough not to distort the admin interface's tables.
const maxNameLen = 64

// sanitizeName makes a peer-supplied label safe to store, log and render.
// Anyone holding the session code can send anything here, so control characters
// - which could forge log lines - are dropped and the result is length-capped.
func sanitizeName(name string) string {
	// Whitespace collapses to a plain space and other control characters are
	// dropped, so a name cannot span lines or forge a log record.
	name = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = strings.Join(strings.Fields(name), " ")
	// Cut on a rune boundary so a truncated multi-byte character does not
	// become replacement junk in the interface.
	for len(name) > maxNameLen {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return name
}

// logName renders a name for a log line, or nothing when there is none.
func logName(name string) string {
	if name == "" {
		return ""
	}
	return fmt.Sprintf(" name=%q", name)
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
		s.stats.udpPackets.Add(1)
		s.stats.lastUDPUnix.Store(time.Now().Unix())
		if n < hdrLen || string(buf[:4]) != magic {
			s.stats.udpInvalid.Add(1)
			xlog.Debugf("udp: ignoring %d-byte datagram from %s (not an RT01 probe)", n, addr)
			continue
		}
		token := string(buf[4:hdrLen])
		payload := buf[hdrLen:n]

		s.mu.Lock()
		route := s.relays[token]
		if route == nil {
			// A peer reflecting before it has been paired: remember where it is
			// so the pairing below has an address to forward to.
			route = &relayRoute{first: time.Now()}
			s.relays[token] = route
		}
		route.addr = addr
		route.seen = time.Now()
		if len(payload) > 0 {
			route.packets++
			route.bytes += uint64(len(payload))
		}
		var peerAddr *net.UDPAddr
		if peer := s.relays[route.peer]; peer != nil {
			peerAddr = peer.addr
		}
		s.mu.Unlock()

		if len(payload) == 0 {
			// Reflection request: reply with observed address.
			s.stats.reflections.Add(1)
			s.stats.lastReflectUnix.Store(time.Now().Unix())
			reply := append([]byte(magicAddr), []byte(addr.String())...)
			if _, err := s.udp.WriteToUDP(reply, addr); err != nil {
				xlog.Infof("udp: reflection reply to %s failed: %v", addr, err)
			} else {
				xlog.Debugf("udp: reflected %s for token %s", addr, token)
			}
			continue
		}
		// Relay: forward payload to the paired peer, tagged with the SENDER's
		// token so the receiver knows the datagram is relayed (and from whom).
		if peerAddr == nil {
			s.stats.droppedPkts.Add(1)
			continue
		}
		out := make([]byte, 0, hdrLen+len(payload))
		out = append(out, magic...)
		out = append(out, token...)
		out = append(out, payload...)
		s.udp.WriteToUDP(out, peerAddr)
		s.stats.relayedPkts.Add(1)
		s.stats.relayedBytes.Add(uint64(len(payload)))
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
		for tok, route := range s.relays {
			if now.Sub(route.seen) > relayTTL {
				delete(s.relays, tok)
			}
		}
		s.mu.Unlock()
	}
}

// Small helper reused by client to marshal.
func mustJSON(m Message) []byte { b, _ := json.Marshal(m); return b }

var _ = mustJSON
var _ = time.Second

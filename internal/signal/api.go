package signal

import (
	"bytes"
	"cmp"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/chriswirz/relay-tunnel/internal/xlog"
)

// The admin API is the contract in openapi.json, and the web interface is only
// its first client. Everything here reads or edits the two pieces of state the
// rendezvous server keeps: the sessions peers are signaling through, and the
// relay routes that carry their datagrams when a direct path could not be
// punched.
//
// Every endpoint below needs an administrator, which is a signed-in browser
// holding the session cookie. A first boot session that has not chosen a
// password yet counts as signed out here, so nothing about this server is
// readable until the default password is replaced.

// PeerView is one side of a rendezvous session.
type PeerView struct {
	Role Role `json:"role"`
	// Name is the label the peer gave for itself, empty when it gave none. It
	// is peer-supplied and unverified - useful for telling machines apart, not
	// for deciding anything.
	Name  string `json:"name,omitempty"`
	Token string `json:"token"`
	// Ready is true once the peer has reflected its public address and, for a
	// host, published its certificate fingerprint.
	Ready bool `json:"ready"`
	// PublicAddr is the address the peer was reflected at, empty until then.
	PublicAddr string `json:"public_addr,omitempty"`
	// LocalAddrs are the addresses the peer reports for its own networks. The
	// server passes them to the other peer without verifying them. Two peers
	// sharing a public address are behind one NAT, and these are what they
	// connect over.
	LocalAddrs []string `json:"local_addrs,omitempty"`
	// CertFingerprint is the host's QUIC certificate fingerprint, which the
	// client pins. Hosts only.
	CertFingerprint string `json:"cert_fingerprint,omitempty"`
	// RemoteAddr is where the signaling WebSocket itself came from, which is not
	// necessarily where the peer's UDP is seen.
	RemoteAddr string    `json:"remote_addr,omitempty"`
	JoinedAt   time.Time `json:"joined_at"`
}

// SessionView is one rendezvous code and whoever is on it.
type SessionView struct {
	Code      string     `json:"code"`
	CreatedAt time.Time  `json:"created_at"`
	Paired    bool       `json:"paired"`
	PairedAt  *time.Time `json:"paired_at,omitempty"`
	Peers     []PeerView `json:"peers"`
}

// RelayView is one peer's side of a relayed pair.
type RelayView struct {
	Token string `json:"token"`
	// PeerToken is where this token's datagrams are forwarded; empty for a peer
	// that has reflected but is not paired yet.
	PeerToken string `json:"peer_token,omitempty"`
	// Code names the session the pairing came from, empty for an unpaired token.
	Code string `json:"code,omitempty"`
	Role Role   `json:"role,omitempty"`
	// Addr is where this peer's datagrams are last known to come from, and where
	// its peer's are sent.
	Addr      string    `json:"addr,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	// ExpiresAt is when the idle sweep will reclaim this route if nothing more
	// arrives on it.
	ExpiresAt time.Time `json:"expires_at"`
	Packets   uint64    `json:"packets"`
	Bytes     uint64    `json:"bytes"`
}

// TotalsView are this process's lifetime counters.
type TotalsView struct {
	PeersJoined  uint64 `json:"peers_joined"`
	Pairs        uint64 `json:"pairs"`
	Reflections  uint64 `json:"reflections"`
	RelayedPkts  uint64 `json:"relayed_packets"`
	RelayedBytes uint64 `json:"relayed_bytes"`
	// DroppedPkts counts relay datagrams with nowhere to forward them, usually a
	// peer still relaying after its pairing was reclaimed.
	DroppedPkts uint64 `json:"dropped_packets"`
}

// UDPView is the health of the reflection/relay socket, which is the part of a
// rendezvous that fails silently. Signaling runs over the HTTP listener and is
// usually proxied, so it can work perfectly while UDP never arrives; peers only
// see "reflection timed out" and cannot tell which side dropped the packets.
// These fields answer that from the server: PacketsReceived counts everything
// read off the socket, so it stays at zero when a firewall or a wrong
// advertised address means the probes never land.
type UDPView struct {
	// BoundAddr is what the socket listens on, AdvertisedAddr what peers are
	// told to probe. A loopback bind with a public advertisement is the classic
	// misconfiguration, and showing both side by side makes it visible.
	BoundAddr      string `json:"bound_addr"`
	AdvertisedAddr string `json:"advertised_addr,omitempty"`
	// AdvertisedDerived is true when no public_udp is configured and the address
	// peers get is derived per-request from the Host header, which is correct
	// behind a proxy that sets it and wrong when it is missing.
	AdvertisedDerived bool `json:"advertised_derived"`

	PacketsReceived uint64 `json:"packets_received"`
	// InvalidPackets is datagrams that were not RT01 probes.
	InvalidPackets uint64 `json:"invalid_packets"`

	// LastPacketAt is when anything last arrived, LastReflectionAt when a
	// reflection was last answered. Both nil when it has never happened.
	LastPacketAt     *time.Time `json:"last_packet_at,omitempty"`
	LastReflectionAt *time.Time `json:"last_reflection_at,omitempty"`
}

// StatusView is what this server is and what it is currently carrying.
type StatusView struct {
	// Name is the operator's label for this server, empty when unset. It is
	// what distinguishes one rendezvous from another in a browser tab.
	Name          string    `json:"name,omitempty"`
	Version       string    `json:"version"`
	StartedAt     time.Time `json:"started_at"`
	UptimeSeconds float64   `json:"uptime_seconds"`

	HTTPAddr  string `json:"http_addr"`
	UDPAddr   string `json:"udp_addr"`
	PublicUDP string `json:"public_udp,omitempty"`
	PublicURL string `json:"public_url,omitempty"`

	Sessions       int `json:"sessions"`
	SessionsPaired int `json:"sessions_paired"`
	PeersConnected int `json:"peers_connected"`
	Relays         int `json:"relays"`

	RelayTTLSeconds float64    `json:"relay_ttl_seconds"`
	Totals          TotalsView `json:"totals"`
	UDP             UDPView    `json:"udp"`
}

// snapshotSessions builds the view of every live session, newest last so a list
// reads in the order codes arrived.
func (s *Server) snapshotSessions() []SessionView {
	s.mu.Lock()
	out := make([]SessionView, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, sess.view())
	}
	s.mu.Unlock()
	slices.SortFunc(out, func(a, b SessionView) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out
}

// view renders a session. The caller holds s.mu.
func (sess *session) view() SessionView {
	v := SessionView{
		Code:      sess.code,
		CreatedAt: sess.created,
		Paired:    sess.paired,
		Peers:     make([]PeerView, 0, len(sess.peers)),
	}
	if !sess.pairedAt.IsZero() {
		at := sess.pairedAt
		v.PairedAt = &at
	}
	for _, role := range []Role{RoleHost, RoleClient} {
		p := sess.peers[role]
		if p == nil {
			continue
		}
		v.Peers = append(v.Peers, PeerView{
			Role:            p.role,
			Name:            p.name,
			Token:           p.token,
			Ready:           isClosed(p.ready),
			PublicAddr:      p.public,
			LocalAddrs:      p.local,
			CertFingerprint: p.fp,
			RemoteAddr:      p.remote,
			JoinedAt:        p.joined,
		})
	}
	return v
}

// snapshotRelays builds the view of every live relay route, busiest first: a
// route carrying traffic is the one an operator is looking for.
func (s *Server) snapshotRelays() []RelayView {
	s.mu.Lock()
	out := make([]RelayView, 0, len(s.relays))
	for token, route := range s.relays {
		v := RelayView{
			Token:     token,
			PeerToken: route.peer,
			Code:      route.code,
			Role:      route.role,
			FirstSeen: route.first,
			LastSeen:  route.seen,
			ExpiresAt: route.seen.Add(relayTTL),
			Packets:   route.packets,
			Bytes:     route.bytes,
		}
		if route.addr != nil {
			v.Addr = route.addr.String()
		}
		out = append(out, v)
	}
	s.mu.Unlock()
	slices.SortFunc(out, func(a, b RelayView) int {
		if c := cmp.Compare(b.Bytes, a.Bytes); c != 0 {
			return c
		}
		return b.LastSeen.Compare(a.LastSeen)
	})
	return out
}

// udpView reports the reflection socket's health. It takes the request so the
// advertised address matches what a peer signaling right now would actually be
// told, derivation included, rather than only what the config says.
func (s *Server) udpView(r *http.Request) UDPView {
	v := UDPView{
		BoundAddr:         s.cfg.UDPAddr,
		AdvertisedAddr:    s.publicUDP(r),
		AdvertisedDerived: s.cfg.PublicUDP == "",
		PacketsReceived:   s.stats.udpPackets.Load(),
		InvalidPackets:    s.stats.udpInvalid.Load(),
	}
	if t, ok := stampOf(&s.stats.lastUDPUnix); ok {
		v.LastPacketAt = &t
	}
	if t, ok := stampOf(&s.stats.lastReflectUnix); ok {
		v.LastReflectionAt = &t
	}
	return v
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSONError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	s.mu.Lock()
	sessions, paired, peers := len(s.sessions), 0, 0
	for _, sess := range s.sessions {
		if sess.paired {
			paired++
		}
		peers += len(sess.peers)
	}
	relays := len(s.relays)
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, StatusView{
		Name:            s.cfg.Name,
		Version:         cmp.Or(s.cfg.Version, "dev"),
		StartedAt:       s.started,
		UptimeSeconds:   time.Since(s.started).Seconds(),
		HTTPAddr:        s.cfg.HTTPAddr,
		UDPAddr:         s.cfg.UDPAddr,
		PublicUDP:       s.cfg.PublicUDP,
		PublicURL:       s.cfg.PublicURL,
		Sessions:        sessions,
		SessionsPaired:  paired,
		PeersConnected:  peers,
		Relays:          relays,
		RelayTTLSeconds: relayTTL.Seconds(),
		Totals: TotalsView{
			PeersJoined:  s.stats.peersJoined.Load(),
			Pairs:        s.stats.pairs.Load(),
			Reflections:  s.stats.reflections.Load(),
			RelayedPkts:  s.stats.relayedPkts.Load(),
			RelayedBytes: s.stats.relayedBytes.Load(),
			DroppedPkts:  s.stats.droppedPkts.Load(),
		},
		UDP: s.udpView(r),
	})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSONError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": s.snapshotSessions()})
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSONError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	s.mu.Lock()
	sess := s.sessions[r.PathValue("code")]
	var v SessionView
	if sess != nil {
		v = sess.view()
	}
	s.mu.Unlock()
	if sess == nil {
		writeJSONError(w, http.StatusNotFound, "no such session")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// handleDeleteSession drops a rendezvous: both peers' signaling connections are
// closed and the relay routes for their tokens are reclaimed.
//
// This does not reach an established tunnel. Once two peers have punched a
// direct path they talk to each other and no longer need this server, so
// deleting the session ends the rendezvous, not the connection it produced.
// Deleting the relay routes does cut a pair that never got a direct path.
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSONError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	code := r.PathValue("code")

	s.mu.Lock()
	sess := s.sessions[code]
	if sess == nil {
		s.mu.Unlock()
		writeJSONError(w, http.StatusNotFound, "no such session")
		return
	}
	closing := make([]*peerConn, 0, len(sess.peers))
	for _, p := range sess.peers {
		closing = append(closing, p)
		delete(s.relays, p.token)
	}
	// The read loop's own defer removes the peer and then the session; deleting
	// the map entry here as well keeps a code that is being torn down from
	// showing up in the very next list.
	delete(s.sessions, code)
	s.mu.Unlock()

	// Closing outside the lock: a write can block, and the read loop that wakes
	// up wants the same mutex.
	for _, p := range closing {
		_ = p.send(Message{Type: TypeError, Error: "session closed by the server"})
		_ = p.conn.Close()
	}
	xlog.Infof("admin closed session code=%s peers=%d", code, len(closing))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListRelays(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSONError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"relays": s.snapshotRelays()})
}

// handleDeleteRelay reclaims one relay route and the one pointing back at it,
// which together are the pair. A pair still relaying will stop; one that
// punched a direct path is unaffected, since it is no longer sending here.
func (s *Server) handleDeleteRelay(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSONError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	token := r.PathValue("token")

	s.mu.Lock()
	route := s.relays[token]
	if route == nil {
		s.mu.Unlock()
		writeJSONError(w, http.StatusNotFound, "no such relay route")
		return
	}
	delete(s.relays, token)
	if route.peer != "" {
		delete(s.relays, route.peer)
	}
	s.mu.Unlock()

	xlog.Infof("admin dropped relay token=%s peer=%s", token, route.peer)
	w.WriteHeader(http.StatusNoContent)
}

// pruneAges are the idle windows the UI offers, and the only ones this endpoint
// accepts. A fixed list keeps an accidental "1" from reclaiming far more than
// intended.
var pruneAges = map[string]time.Duration{
	"1m":  time.Minute,
	"5m":  5 * time.Minute,
	"15m": 15 * time.Minute,
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"24h": 24 * time.Hour,
}

// sortedPruneAges lists the accepted windows shortest first, for error messages.
func sortedPruneAges() []string {
	ages := make([]string, 0, len(pruneAges))
	for age := range pruneAges {
		ages = append(ages, age)
	}
	slices.SortFunc(ages, func(a, b string) int { return cmp.Compare(pruneAges[a], pruneAges[b]) })
	return ages
}

// handlePruneRelays reclaims every relay route idle longer than the given age.
// The background sweep already does this at relayTTL; this is the same thing on
// demand, and with a shorter window when a server is carrying more relayed
// traffic than it should be.
func (s *Server) handlePruneRelays(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSONError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	age := r.URL.Query().Get("idle_for")
	idle, ok := pruneAges[age]
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "idle_for must be one of "+strings.Join(sortedPruneAges(), ", "))
		return
	}

	now := time.Now()
	// An empty result is an empty list, not null: the spec says this is an
	// array, and a client that iterates it should not have to check first.
	removed := []string{}
	s.mu.Lock()
	for token, route := range s.relays {
		if now.Sub(route.seen) > idle {
			delete(s.relays, token)
			removed = append(removed, token)
		}
	}
	s.mu.Unlock()
	slices.Sort(removed)

	if len(removed) > 0 {
		xlog.Infof("admin pruned relays idle_for=%s count=%d", age, len(removed))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"idle_for": age,
		"deleted":  len(removed),
		"tokens":   removed,
	})
}

// handleOpenAPI serves the embedded specification for this API.
func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	http.ServeContent(w, r, "openapi.json", s.started, bytes.NewReader(openapiJSON))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

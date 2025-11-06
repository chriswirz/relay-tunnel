// Package signal defines the rendezvous protocol used to pair two peers and
// exchange the information they need to establish a direct connection.
//
// The server never sees tunnel payload (unless relay fallback is used, in which
// case it forwards opaque, QUIC-encrypted UDP datagrams). Its jobs are:
//   - pair two peers that present the same session code,
//   - reflect each peer's public UDP address (STUN-like),
//   - relay the host's TLS cert fingerprint to the client for pinning,
//   - optionally relay UDP datagrams if hole punching fails.
package signal

// Role identifies which side of the tunnel a peer is.
type Role string

const (
	RoleHost   Role = "host"   // the machine that exposes services (`expose`)
	RoleClient Role = "client" // the machine that reaches them (`connect`)
)

// Message is the envelope for every signaling message (WebSocket, JSON).
type Message struct {
	Type string `json:"type"`

	// Hello (peer -> server)
	Role Role   `json:"role,omitempty"`
	Code string `json:"code,omitempty"` // shared session code

	// Reflected (server -> peer): the peer's own public address as observed
	// on the UDP reflection socket.
	PublicAddr string `json:"public_addr,omitempty"`

	// Paired (server -> peer): information about the other peer.
	PeerAddr string `json:"peer_addr,omitempty"` // peer's public UDP addr
	PeerRole Role   `json:"peer_role,omitempty"`

	// Host announces its ephemeral TLS cert fingerprint (SHA-256, hex) so the
	// client can pin it and reject anyone else.
	CertFingerprint string `json:"cert_fingerprint,omitempty"`

	// RelayToken is issued by the server to identify a peer on the UDP
	// reflection/relay socket. Each peer echoes it in its UDP probes.
	RelayToken string `json:"relay_token,omitempty"`
	RelayAddr  string `json:"relay_addr,omitempty"` // server's UDP addr for relay

	// Go tells both peers to begin hole punching now.
	// Fallback, when true, means the server could not/should not rely on a
	// direct path and both peers should relay through the server.
	Fallback bool `json:"fallback,omitempty"`

	// Error carries a fatal condition (server -> peer).
	Error string `json:"error,omitempty"`
}

const (
	TypeHello     = "hello"     // peer -> server
	TypeReflected = "reflected" // server -> peer
	TypePaired    = "paired"    // server -> peer
	TypeReady     = "ready"     // peer -> server (reflection done, fingerprint set)
	TypeGo        = "go"        // server -> peer (start connecting)
	TypeError     = "error"
)

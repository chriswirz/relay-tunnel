package app

import (
	"context"
	"sync"
	"time"

	"github.com/chriswirz/relay-tunnel/internal/xlog"
	"github.com/quic-go/quic-go"
)

// ConnSource yields a live QUIC connection, waiting for one when the tunnel is
// temporarily down (for example while the peer restarts). Local listeners hold
// a ConnSource rather than a fixed connection so they survive reconnections.
type ConnSource interface {
	Conn(ctx context.Context) (*quic.Conn, error)
}

// DialFunc establishes a fresh connection (rendezvous + QUIC dial).
type DialFunc func(ctx context.Context) (*quic.Conn, error)

// ConnManager maintains a single live QUIC connection, transparently
// reconnecting with exponential backoff whenever it drops. This is what lets a
// long-running client tunnel resume automatically after the peer, the network,
// or the rendezvous server goes offline and comes back.
type ConnManager struct {
	dial       DialFunc
	minBackoff time.Duration
	maxBackoff time.Duration

	mu    sync.Mutex
	conn  *quic.Conn
	ready chan struct{} // closed when conn becomes available; replaced on loss
}

// NewConnManager creates a manager that (re)establishes connections with dial.
func NewConnManager(dial DialFunc) *ConnManager {
	return &ConnManager{
		dial:       dial,
		minBackoff: time.Second,
		maxBackoff: 15 * time.Second,
		ready:      make(chan struct{}),
	}
}

// Run keeps the connection alive until ctx is cancelled. It blocks, so run it
// in its own goroutine.
func (m *ConnManager) Run(ctx context.Context) {
	backoff := m.minBackoff
	for ctx.Err() == nil {
		conn, err := m.dial(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			xlog.Infof("tunnel unavailable: %v (retrying in %s)", err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > m.maxBackoff {
				backoff = m.maxBackoff
			}
			continue
		}
		backoff = m.minBackoff
		m.set(conn)
		xlog.Infof("tunnel connected")

		select {
		case <-ctx.Done():
			m.clear()
			conn.CloseWithError(0, "bye")
			return
		case <-conn.Context().Done():
			xlog.Infof("tunnel lost; reconnecting")
			m.clear()
		}
	}
}

func (m *ConnManager) set(conn *quic.Conn) {
	m.mu.Lock()
	m.conn = conn
	close(m.ready)
	m.mu.Unlock()
}

func (m *ConnManager) clear() {
	m.mu.Lock()
	m.conn = nil
	m.ready = make(chan struct{})
	m.mu.Unlock()
}

// Conn returns the current live connection, waiting until one is available or
// ctx is cancelled.
func (m *ConnManager) Conn(ctx context.Context) (*quic.Conn, error) {
	for {
		m.mu.Lock()
		conn, ready := m.conn, m.ready
		m.mu.Unlock()
		if conn != nil {
			return conn, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ready:
		}
	}
}

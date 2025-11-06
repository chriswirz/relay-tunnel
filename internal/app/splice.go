package app

import (
	"io"
	"net"
	"sync"

	"github.com/quic-go/quic-go"
)

// splice copies bytes bidirectionally between a QUIC stream and a net.Conn,
// returning when both directions have finished or errored.
func splice(s *quic.Stream, c net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(s, c) // remote-service -> stream
		s.Close()     // signal EOF to peer's read side
	}()
	go func() {
		defer wg.Done()
		io.Copy(c, s) // stream -> remote-service
		if tc, ok := c.(*net.TCPConn); ok {
			tc.CloseWrite()
		} else {
			c.Close()
		}
	}()
	wg.Wait()
	c.Close()
}

package app

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"

	"github.com/chriswirz/relay-tunnel/internal/xlog"
	"github.com/quic-go/quic-go"
)

// SOCKS listens a minimal SOCKS5 (CONNECT, no auth) proxy locally. Each request
// target is dialed on the host side via a KindTCP stream. This gives the client
// arbitrary outbound reach through the peer.
func SOCKS(ctx context.Context, conn *quic.Conn, localAddr string) error {
	if _, _, err := net.SplitHostPort(localAddr); err != nil {
		localAddr = net.JoinHostPort("127.0.0.1", localAddr)
	}
	ln, err := net.Listen("tcp", localAddr)
	if err != nil {
		return err
	}
	xlog.Infof("SOCKS5 proxy on %s -> peer", localAddr)
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go serveSOCKS(ctx, conn, c)
	}
}

func serveSOCKS(ctx context.Context, conn *quic.Conn, c net.Conn) {
	defer c.Close()
	target, err := socksHandshake(c)
	if err != nil {
		xlog.Debugf("socks handshake: %v", err)
		return
	}
	s, err := openTCP(ctx, conn, target)
	if err != nil {
		xlog.Debugf("socks open stream: %v", err)
		return
	}
	splice(s, c)
}

// socksHandshake performs the SOCKS5 greeting + CONNECT and replies success,
// returning the requested "host:port".
func socksHandshake(c net.Conn) (string, error) {
	buf := make([]byte, 262)
	// Greeting: VER, NMETHODS, METHODS...
	if _, err := io.ReadFull(c, buf[:2]); err != nil {
		return "", err
	}
	if buf[0] != 0x05 {
		return "", fmt.Errorf("not socks5")
	}
	nm := int(buf[1])
	if _, err := io.ReadFull(c, buf[:nm]); err != nil {
		return "", err
	}
	// Reply: no auth.
	if _, err := c.Write([]byte{0x05, 0x00}); err != nil {
		return "", err
	}
	// Request: VER CMD RSV ATYP ...
	if _, err := io.ReadFull(c, buf[:4]); err != nil {
		return "", err
	}
	if buf[1] != 0x01 { // CONNECT only
		c.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return "", fmt.Errorf("unsupported command %d", buf[1])
	}
	var host string
	switch buf[3] {
	case 0x01: // IPv4
		if _, err := io.ReadFull(c, buf[:4]); err != nil {
			return "", err
		}
		host = net.IP(buf[:4]).String()
	case 0x03: // domain
		if _, err := io.ReadFull(c, buf[:1]); err != nil {
			return "", err
		}
		l := int(buf[0])
		if _, err := io.ReadFull(c, buf[:l]); err != nil {
			return "", err
		}
		host = string(buf[:l])
	case 0x04: // IPv6
		if _, err := io.ReadFull(c, buf[:16]); err != nil {
			return "", err
		}
		host = net.IP(buf[:16]).String()
	default:
		return "", fmt.Errorf("bad atyp")
	}
	if _, err := io.ReadFull(c, buf[:2]); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(buf[:2])
	// Success reply (BND.ADDR ignored by most clients).
	c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

package app

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/chriswirz/relay-tunnel/internal/mux"
	"github.com/chriswirz/relay-tunnel/internal/xlog"
	"github.com/quic-go/quic-go"
)

// openWait bounds how long an accepted local connection waits for the tunnel to
// come back before it is dropped.
const openWait = 30 * time.Second

// LocalForward listens on localAddr and tunnels each accepted connection to
// remoteTarget ("host:port") via the host, which dials it. spec form is
// "localport:remotehost:remoteport" or "localaddr:localport:remotehost:remoteport".
//
// It takes a ConnSource rather than a fixed connection, so the listener stays
// up and tunnels resume automatically across reconnections.
func LocalForward(ctx context.Context, src ConnSource, spec string) error {
	localAddr, remoteTarget, err := parseForward(spec)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", localAddr)
	if err != nil {
		return err
	}
	xlog.Infof("forwarding %s -> (peer) %s", localAddr, remoteTarget)
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer c.Close()
			s, err := openTCP(ctx, src, remoteTarget)
			if err != nil {
				xlog.Debugf("open stream failed: %v", err)
				return
			}
			splice(s, c)
		}()
	}
}

// openTCP opens a KindTCP stream to the given target on the current live
// connection, waiting briefly for one if the tunnel is momentarily down.
func openTCP(ctx context.Context, src ConnSource, target string) (*quic.Stream, error) {
	waitCtx, cancel := context.WithTimeout(ctx, openWait)
	defer cancel()
	conn, err := src.Conn(waitCtx)
	if err != nil {
		return nil, err
	}
	s, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	if err := mux.WriteHeader(s, mux.OpenMsg{Kind: mux.KindTCP, Target: target}); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func parseForward(spec string) (localAddr, remoteTarget string, err error) {
	parts := strings.Split(spec, ":")
	switch len(parts) {
	case 3: // localport:remotehost:remoteport
		return net.JoinHostPort("127.0.0.1", parts[0]), net.JoinHostPort(parts[1], parts[2]), nil
	case 4: // localaddr:localport:remotehost:remoteport
		return net.JoinHostPort(parts[0], parts[1]), net.JoinHostPort(parts[2], parts[3]), nil
	default:
		return "", "", fmt.Errorf("bad forward spec %q (want localport:host:port)", spec)
	}
}

// Exec runs args on the host and wires local stdin/stdout/stderr to the stream.
func Exec(ctx context.Context, conn *quic.Conn, args []string) error {
	s, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := mux.WriteHeader(s, mux.OpenMsg{Kind: mux.KindExec, Exec: &mux.ExecHeader{Args: args}}); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		io.Copy(s, os.Stdin)
		s.Close()
		close(done)
	}()
	io.Copy(os.Stdout, s)
	return nil
}

// FileGet downloads remotePath from the host to localPath.
func FileGet(ctx context.Context, conn *quic.Conn, remotePath, localPath string) error {
	s, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := mux.WriteHeader(s, mux.OpenMsg{Kind: mux.KindFile, File: &mux.FileHeader{Op: "get", Path: remotePath}}); err != nil {
		return err
	}
	f, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(f, s)
	if err != nil {
		return err
	}
	xlog.Infof("downloaded %d bytes to %s", n, localPath)
	return nil
}

// FilePut uploads localPath to remotePath on the host.
func FilePut(ctx context.Context, conn *quic.Conn, localPath, remotePath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	s, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	hdr := mux.OpenMsg{Kind: mux.KindFile, File: &mux.FileHeader{
		Op: "put", Path: remotePath, Size: info.Size(), Mode: uint32(info.Mode().Perm()),
	}}
	if err := mux.WriteHeader(s, hdr); err != nil {
		return err
	}
	n, err := io.Copy(s, f)
	if err != nil {
		return err
	}
	s.Close() // signal EOF to the host's read side
	// Wait for the host to finish writing the file and close its side, so we
	// don't tear down the connection mid-transfer.
	io.Copy(io.Discard, s)
	xlog.Infof("uploaded %d bytes to (peer) %s", n, remotePath)
	return nil
}

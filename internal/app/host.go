package app

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chriswirz/relay-tunnel/internal/mux"
	"github.com/chriswirz/relay-tunnel/internal/xlog"
	"github.com/quic-go/quic-go"
)

// HostPolicy controls what the connecting client is permitted to do.
type HostPolicy struct {
	AllowDial bool     // allow arbitrary host:port dials (needed for -L/-D/SOCKS)
	Allow     []string // if non-empty, whitelist of allowed "host:port" dial targets
	AllowExec bool     // allow remote command execution
	FileRoot  string   // if non-empty, allow file get/put rooted here; else disabled
}

func (p HostPolicy) dialAllowed(target string) bool {
	if !p.AllowDial {
		return false
	}
	if len(p.Allow) == 0 {
		return true
	}
	for _, a := range p.Allow {
		if a == target {
			return true
		}
	}
	return false
}

// ServeHost accepts streams on conn until it closes, dispatching by kind.
func ServeHost(ctx context.Context, conn *quic.Conn, pol HostPolicy) error {
	xlog.Infof("connected; serving requests")
	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			return err
		}
		go handleStream(stream, pol)
	}
}

func handleStream(s *quic.Stream, pol HostPolicy) {
	defer s.Close()
	hdr, err := mux.ReadHeader(s)
	if err != nil {
		xlog.Debugf("bad stream header: %v", err)
		return
	}
	switch hdr.Kind {
	case mux.KindTCP:
		hostTCP(s, hdr.Target, pol)
	case mux.KindFile:
		hostFile(s, hdr.File, pol)
	case mux.KindExec:
		hostExec(s, hdr.Exec, pol)
	default:
		xlog.Debugf("unknown stream kind %q", hdr.Kind)
	}
}

func hostTCP(s *quic.Stream, target string, pol HostPolicy) {
	if !pol.dialAllowed(target) {
		xlog.Infof("denied dial to %s (policy)", target)
		return
	}
	c, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		xlog.Debugf("dial %s failed: %v", target, err)
		return
	}
	defer c.Close()
	xlog.Debugf("forwarding to %s", target)
	splice(s, c)
}

func hostExec(s *quic.Stream, h *mux.ExecHeader, pol HostPolicy) {
	if !pol.AllowExec || h == nil || len(h.Args) == 0 {
		xlog.Infof("denied exec (policy or empty command)")
		return
	}
	xlog.Infof("exec: %s", strings.Join(h.Args, " "))
	cmd := exec.Command(h.Args[0], h.Args[1:]...)
	cmd.Stdin = s
	cmd.Stdout = s
	cmd.Stderr = s
	_ = cmd.Run()
}

func hostFile(s *quic.Stream, h *mux.FileHeader, pol HostPolicy) {
	if pol.FileRoot == "" || h == nil {
		xlog.Infof("denied file transfer (policy)")
		return
	}
	// Resolve and confine the path within FileRoot.
	clean := filepath.Clean(filepath.Join(pol.FileRoot, h.Path))
	root, _ := filepath.Abs(pol.FileRoot)
	abs, _ := filepath.Abs(clean)
	if !strings.HasPrefix(abs+string(os.PathSeparator), root+string(os.PathSeparator)) && abs != root {
		xlog.Infof("denied file path outside root: %s", h.Path)
		return
	}
	switch h.Op {
	case "get":
		f, err := os.Open(abs)
		if err != nil {
			xlog.Debugf("get open failed: %v", err)
			return
		}
		defer f.Close()
		io.Copy(s, f)
	case "put":
		mode := os.FileMode(h.Mode)
		if mode == 0 {
			mode = 0o644
		}
		f, err := os.OpenFile(abs, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			xlog.Debugf("put create failed: %v", err)
			return
		}
		defer f.Close()
		io.Copy(f, s)
		xlog.Infof("received file %s", abs)
	}
}

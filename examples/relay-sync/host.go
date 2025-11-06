package main

// Host side of relay-sync: serves manifest/get/put/delete requests for files
// under a single root directory, over the QUIC connection established by the
// relay-tunnel module.

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/chriswirz/relay-tunnel/internal/signal"
	"github.com/chriswirz/relay-tunnel/internal/transport"
	"github.com/quic-go/quic-go"
)

// serve runs the sync host until ctx is cancelled. It re-registers with the
// rendezvous server after each client disconnects so the endpoint stays
// available, mirroring how `relay-tunnel expose` behaves.
func serve(ctx context.Context, wsURL, code, root string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(absRoot); err != nil || !fi.IsDir() {
		return fmt.Errorf("serve root %q is not a directory", root)
	}

	cert, fp, err := transport.GenerateCert()
	if err != nil {
		return fmt.Errorf("cert: %w", err)
	}
	fmt.Printf("\n  relay-sync serving %s\n", absRoot)
	fmt.Printf("  session code: %s\n\n", code)

	for ctx.Err() == nil {
		err := serveOnce(ctx, wsURL, code, cert, fp, absRoot)
		if ctx.Err() != nil {
			break
		}
		log.Printf("session ended: %v (waiting for next client)", err)
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	return nil
}

func serveOnce(ctx context.Context, wsURL, code string, cert tls.Certificate, fp, root string) error {
	sess, err := signal.Dial(signal.DialParams{
		ServerURL:       wsURL,
		Code:            code,
		Role:            signal.RoleHost,
		CertFingerprint: fp,
	})
	if err != nil {
		return err
	}
	conn, err := transport.Listen(ctx, sess, cert)
	if err != nil {
		return err
	}
	// Close gracefully on shutdown so a client waiting on us reconnects at once.
	defer conn.CloseWithError(0, "host shutting down")
	log.Printf("client connected (relayed=%v)", sess.Relayed)
	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			return err
		}
		go handle(stream, root)
	}
}

func handle(s *quic.Stream, root string) {
	defer s.Close()
	var req Request
	if err := readJSON(s, &req); err != nil {
		return
	}
	switch req.Op {
	case "manifest":
		hostManifest(s, root, req)
	case "get":
		hostGet(s, root, req)
	case "put":
		hostPut(s, root, req)
	case "delete":
		hostDelete(s, root, req)
	}
}

func hostManifest(s *quic.Stream, root string, req Request) {
	base, ok := confine(root, req.Path)
	if !ok {
		writeJSON(s, ManifestReply{Err: "path outside root"})
		return
	}
	// A missing subtree is treated as empty rather than an error.
	if _, err := os.Stat(base); os.IsNotExist(err) {
		writeJSON(s, ManifestReply{OK: true})
		return
	}
	entries, err := buildManifest(base, req.Gitignore)
	if err != nil {
		writeJSON(s, ManifestReply{Err: err.Error()})
		return
	}
	writeJSON(s, ManifestReply{OK: true, Entries: entries})
}

func hostGet(s *quic.Stream, root string, req Request) {
	abs, ok := confine(root, req.Path)
	if !ok {
		writeJSON(s, GetReply{Err: "path outside root"})
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		writeJSON(s, GetReply{Err: err.Error()})
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeJSON(s, GetReply{Err: err.Error()})
		return
	}
	if err := writeJSON(s, GetReply{OK: true, Size: info.Size()}); err != nil {
		return
	}
	io.Copy(s, f)
}

func hostPut(s *quic.Stream, root string, req Request) {
	abs, ok := confine(root, req.Path)
	if !ok {
		writeJSON(s, Reply{Err: "path outside root"})
		return
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		writeJSON(s, Reply{Err: err.Error()})
		return
	}
	mode := os.FileMode(req.Mode)
	if mode == 0 {
		mode = 0o644
	}
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		writeJSON(s, Reply{Err: err.Error()})
		return
	}
	if _, err := io.CopyN(f, s, req.Size); err != nil {
		f.Close()
		writeJSON(s, Reply{Err: err.Error()})
		return
	}
	f.Close()
	if req.Mtime > 0 {
		t := time.Unix(req.Mtime, 0)
		os.Chtimes(abs, t, t)
	}
	writeJSON(s, Reply{OK: true})
}

func hostDelete(s *quic.Stream, root string, req Request) {
	abs, ok := confine(root, req.Path)
	if !ok || abs == root {
		writeJSON(s, Reply{Err: "path outside root"})
		return
	}
	if err := os.RemoveAll(abs); err != nil {
		writeJSON(s, Reply{Err: err.Error()})
		return
	}
	writeJSON(s, Reply{OK: true})
}

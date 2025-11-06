package main

// Client side of relay-sync: performs a single one-way mirror pass, either
// push (local -> remote) or pull (remote -> local), over the QUIC connection
// established by the relay-tunnel module.

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/chriswirz/relay-tunnel/internal/signal"
	"github.com/chriswirz/relay-tunnel/internal/transport"
	"github.com/quic-go/quic-go"
)

// dialClient establishes the QUIC connection to the sync host.
func dialClient(ctx context.Context, wsURL, code string) (*quic.Conn, error) {
	sess, err := signal.Dial(signal.DialParams{
		ServerURL: wsURL,
		Code:      code,
		Role:      signal.RoleClient,
	})
	if err != nil {
		return nil, fmt.Errorf("rendezvous: %w", err)
	}
	conn, err := transport.Dial(ctx, sess)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	log.Printf("connected to host (relayed=%v)", sess.Relayed)
	return conn, nil
}

// syncOpts holds the options that shape a sync pass.
type syncOpts struct {
	del       bool
	dryRun    bool
	gitignore bool
}

// push mirrors localDir onto remoteDir on the host.
func push(ctx context.Context, conn *quic.Conn, localDir, remoteDir string, opts syncOpts) error {
	local, err := buildManifest(localDir, opts.gitignore)
	if err != nil {
		return fmt.Errorf("scan local: %w", err)
	}
	remote, err := reqManifest(ctx, conn, remoteDir, opts.gitignore)
	if err != nil {
		return fmt.Errorf("remote manifest: %w", err)
	}
	actions := planSync(local, remote, opts.del)
	report("push", actions, opts.dryRun)
	if opts.dryRun {
		return nil
	}
	localBy := index(local)
	for _, a := range actions {
		switch a.Op {
		case "copy":
			src := filepath.Join(localDir, filepath.FromSlash(a.Path))
			if err := putFile(ctx, conn, src, path.Join(remoteDir, a.Path), localBy[a.Path]); err != nil {
				return fmt.Errorf("upload %s: %w", a.Path, err)
			}
		case "delete":
			if err := reqDelete(ctx, conn, path.Join(remoteDir, a.Path)); err != nil {
				return fmt.Errorf("delete remote %s: %w", a.Path, err)
			}
		}
		log.Printf("%s %s", a.Op, a.Path)
	}
	return nil
}

// pull mirrors remoteDir on the host onto localDir.
func pull(ctx context.Context, conn *quic.Conn, remoteDir, localDir string, opts syncOpts) error {
	remote, err := reqManifest(ctx, conn, remoteDir, opts.gitignore)
	if err != nil {
		return fmt.Errorf("remote manifest: %w", err)
	}
	local, err := buildManifest(localDir, opts.gitignore)
	if err != nil {
		return fmt.Errorf("scan local: %w", err)
	}
	actions := planSync(remote, local, opts.del)
	report("pull", actions, opts.dryRun)
	if opts.dryRun {
		return nil
	}
	remoteBy := index(remote)
	for _, a := range actions {
		switch a.Op {
		case "copy":
			dst := filepath.Join(localDir, filepath.FromSlash(a.Path))
			if err := getFile(ctx, conn, path.Join(remoteDir, a.Path), dst, remoteBy[a.Path].Mtime); err != nil {
				return fmt.Errorf("download %s: %w", a.Path, err)
			}
		case "delete":
			if err := os.RemoveAll(filepath.Join(localDir, filepath.FromSlash(a.Path))); err != nil {
				return fmt.Errorf("delete local %s: %w", a.Path, err)
			}
		}
		log.Printf("%s %s", a.Op, a.Path)
	}
	return nil
}

func index(entries []Entry) map[string]Entry {
	m := make(map[string]Entry, len(entries))
	for _, e := range entries {
		m[e.Path] = e
	}
	return m
}

func report(dir string, actions []Action, dryRun bool) {
	var copies, deletes int
	for _, a := range actions {
		if a.Op == "copy" {
			copies++
		} else {
			deletes++
		}
	}
	prefix := dir
	if dryRun {
		prefix = dir + " (dry-run)"
	}
	log.Printf("%s: %d to copy, %d to delete", prefix, copies, deletes)
}

// --- request helpers --------------------------------------------------------

func reqManifest(ctx context.Context, conn *quic.Conn, dir string, gitignore bool) ([]Entry, error) {
	s, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if err := writeJSON(s, Request{Op: "manifest", Path: dir, Gitignore: gitignore}); err != nil {
		return nil, err
	}
	var reply ManifestReply
	if err := readJSON(s, &reply); err != nil {
		return nil, err
	}
	if !reply.OK {
		return nil, fmt.Errorf("%s", reply.Err)
	}
	return reply.Entries, nil
}

func putFile(ctx context.Context, conn *quic.Conn, localPath, remotePath string, meta Entry) error {
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
	req := Request{
		Op:    "put",
		Path:  remotePath,
		Size:  info.Size(),
		Mtime: info.ModTime().Unix(),
		Mode:  uint32(info.Mode().Perm()),
	}
	if err := writeJSON(s, req); err != nil {
		return err
	}
	if _, err := io.CopyN(s, f, info.Size()); err != nil {
		return err
	}
	s.Close() // signal EOF to the host's read side
	var reply Reply
	if err := readJSON(s, &reply); err != nil {
		return err
	}
	if !reply.OK {
		return fmt.Errorf("%s", reply.Err)
	}
	return nil
}

func getFile(ctx context.Context, conn *quic.Conn, remotePath, localPath string, mtime int64) error {
	s, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := writeJSON(s, Request{Op: "get", Path: remotePath}); err != nil {
		return err
	}
	var reply GetReply
	if err := readJSON(s, &reply); err != nil {
		return err
	}
	if !reply.OK {
		return fmt.Errorf("%s", reply.Err)
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(localPath)
	if err != nil {
		return err
	}
	if _, err := io.CopyN(f, s, reply.Size); err != nil {
		f.Close()
		return err
	}
	f.Close()
	if mtime > 0 {
		t := time.Unix(mtime, 0)
		os.Chtimes(localPath, t, t)
	}
	return nil
}

func reqDelete(ctx context.Context, conn *quic.Conn, remotePath string) error {
	s, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := writeJSON(s, Request{Op: "delete", Path: remotePath}); err != nil {
		return err
	}
	var reply Reply
	if err := readJSON(s, &reply); err != nil {
		return err
	}
	if !reply.OK {
		return fmt.Errorf("%s", reply.Err)
	}
	return nil
}

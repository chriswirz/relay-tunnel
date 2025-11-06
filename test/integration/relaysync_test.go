//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// startBin starts an arbitrary built binary (like start, but not limited to the
// relay-tunnel binary) with captured output.
func startBin(t *testing.T, ctx context.Context, bin, name string, args ...string) *proc {
	t.Helper()
	cmd := exec.CommandContext(ctx, bin, args...)
	log := &syncBuffer{}
	cmd.Stderr = log
	cmd.Stdout = log
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	p := &proc{name: name, cmd: cmd, log: log}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return p
}

// startRendezvous starts just the rendezvous server and returns its ws URL.
func startRendezvous(t *testing.T, ctx context.Context) string {
	t.Helper()
	httpAddr := fmt.Sprintf("127.0.0.1:%d", freeTCPPort(t))
	udpAddr := fmt.Sprintf("127.0.0.1:%d", freeUDPPort(t))
	start(t, ctx, "server", nil, "server", "--http", httpAddr, "--udp", udpAddr, "-v")
	waitTCP(t, httpAddr, 10*time.Second)
	return fmt.Sprintf("ws://%s/signal", httpAddr)
}

// runSync runs a one-shot relay-sync client command to completion.
func runSync(t *testing.T, ctx context.Context, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(ctx, syncBinPath, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestRelaySyncPushPull(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	wsURL := startRendezvous(t, ctx)

	// Host serves an (initially empty) directory.
	hostRoot := t.TempDir()
	srv := startBin(t, ctx, syncBinPath, "sync-serve",
		"serve", "--server", wsURL, "--code", "itest", hostRoot)
	time.Sleep(1500 * time.Millisecond)

	// Local source tree to push.
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha")
	writeFile(t, filepath.Join(src, "sub", "b.txt"), "bravo")

	// Push src -> host under subdir "data".
	if out, err := runSync(t, ctx, "push", "--server", wsURL, "--code", "itest", src, "data"); err != nil {
		t.Fatalf("push: %v\n%s\nserve log:\n%s", err, out, srv.log.String())
	}
	assertFile(t, filepath.Join(hostRoot, "data", "a.txt"), "alpha")
	assertFile(t, filepath.Join(hostRoot, "data", "sub", "b.txt"), "bravo")

	// Pull host "data" -> a fresh local dir.
	dst := t.TempDir()
	time.Sleep(1500 * time.Millisecond)
	if out, err := runSync(t, ctx, "pull", "--server", wsURL, "--code", "itest", "data", dst); err != nil {
		t.Fatalf("pull: %v\n%s\nserve log:\n%s", err, out, srv.log.String())
	}
	assertFile(t, filepath.Join(dst, "a.txt"), "alpha")
	assertFile(t, filepath.Join(dst, "sub", "b.txt"), "bravo")

	// Delete propagation: remove a.txt locally, push --delete.
	os.Remove(filepath.Join(src, "a.txt"))
	time.Sleep(1500 * time.Millisecond)
	if out, err := runSync(t, ctx, "push", "--server", wsURL, "--code", "itest", src, "data", "--delete"); err != nil {
		t.Fatalf("push --delete: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(hostRoot, "data", "a.txt")); !os.IsNotExist(err) {
		t.Errorf("expected a.txt to be deleted on host, stat err=%v", err)
	}
	assertFile(t, filepath.Join(hostRoot, "data", "sub", "b.txt"), "bravo")

	// Dry-run: add a file, preview only, host must not change.
	writeFile(t, filepath.Join(src, "c.txt"), "charlie")
	time.Sleep(1500 * time.Millisecond)
	if out, err := runSync(t, ctx, "push", "--server", wsURL, "--code", "itest", src, "data", "--dry-run"); err != nil {
		t.Fatalf("push --dry-run: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(hostRoot, "data", "c.txt")); !os.IsNotExist(err) {
		t.Errorf("dry-run must not upload c.txt, stat err=%v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Errorf("%s = %q, want %q", path, string(got), want)
	}
}

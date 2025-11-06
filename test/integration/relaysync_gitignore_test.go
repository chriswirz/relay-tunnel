//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRelaySyncGitignore checks that .gitignore'd files are skipped on both
// sides: the client never sends them, and the host never offers them for
// deletion.
func TestRelaySyncGitignore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	wsURL := startRendezvous(t, ctx)
	hostRoot := t.TempDir()
	startBin(t, ctx, syncBinPath, "sync-serve",
		"serve", "--server", wsURL, "--code", "gi", hostRoot)
	time.Sleep(1500 * time.Millisecond)

	src := t.TempDir()
	writeFile(t, filepath.Join(src, ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(src, "keep.txt"), "keep")
	writeFile(t, filepath.Join(src, "ignored.log"), "secret")

	// Push with gitignore (default on): ignored.log must not be sent.
	if out, err := runSync(t, ctx, "push", "--server", wsURL, "--code", "gi", src, "data"); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	assertFile(t, filepath.Join(hostRoot, "data", "keep.txt"), "keep")
	assertFile(t, filepath.Join(hostRoot, "data", ".gitignore"), "*.log\n")
	if _, err := os.Stat(filepath.Join(hostRoot, "data", "ignored.log")); !os.IsNotExist(err) {
		t.Errorf("gitignored ignored.log must not be pushed, stat err=%v", err)
	}

	// A gitignored file that exists only on the host must survive a --delete
	// push, because the host excludes it from its manifest.
	writeFile(t, filepath.Join(hostRoot, "data", "host-only.log"), "hostlog")
	time.Sleep(1500 * time.Millisecond)
	if out, err := runSync(t, ctx, "push", "--server", wsURL, "--code", "gi", src, "data", "--delete"); err != nil {
		t.Fatalf("push --delete: %v\n%s", err, out)
	}
	assertFile(t, filepath.Join(hostRoot, "data", "host-only.log"), "hostlog")
	assertFile(t, filepath.Join(hostRoot, "data", "keep.txt"), "keep")

	// With --gitignore=false the ignored file is transferred.
	time.Sleep(1500 * time.Millisecond)
	if out, err := runSync(t, ctx, "push", "--server", wsURL, "--code", "gi", src, "data", "--gitignore=false"); err != nil {
		t.Fatalf("push --gitignore=false: %v\n%s", err, out)
	}
	assertFile(t, filepath.Join(hostRoot, "data", "ignored.log"), "secret")
}

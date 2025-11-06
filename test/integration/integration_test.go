//go:build integration

// Package integration runs relay-tunnel end-to-end as real, separate processes:
// a rendezvous server, an `expose` host, and `connect` clients. It exercises
// the direct hole-punched path, the server-relay fallback, TCP forwarding,
// SOCKS5, file transfer and remote exec.
//
// Run with:  go test -tags integration ./test/integration/...
package integration

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	binPath     string
	syncBinPath string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "rt-integration")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mktemp:", err)
		os.Exit(1)
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	binPath = filepath.Join(dir, "relay-tunnel"+ext)
	syncBinPath = filepath.Join(dir, "relay-sync"+ext)
	// Build from the repo root (two levels up from this package).
	for _, b := range []struct{ out, pkg string }{
		{binPath, "../../cmd/relay-tunnel"},
		{syncBinPath, "../../examples/relay-sync"},
	} {
		build := exec.Command("go", "build", "-o", b.out, b.pkg)
		build.Stderr = os.Stderr
		build.Stdout = os.Stdout
		if err := build.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "build:", err)
			os.Exit(1)
		}
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// proc wraps a running child process with captured stderr for diagnostics.
type proc struct {
	name string
	cmd  *exec.Cmd
	log  *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func start(t *testing.T, ctx context.Context, name string, env []string, args ...string) *proc {
	t.Helper()
	cmd := exec.CommandContext(ctx, binPath, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
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

// runToCompletion runs a one-shot client and returns its combined output.
func runToCompletion(t *testing.T, ctx context.Context, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(ctx, binPath, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freeTCPPort: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("freeUDPPort: %v", err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

func waitTCP(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", addr)
}

// startEchoServer starts an in-process TCP echo server and returns its address.
func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	return ln.Addr().String()
}

// harness holds the running server + expose and the signaling URL.
type harness struct {
	wsURL  string
	server *proc
	expose *proc
}

func setup(t *testing.T, ctx context.Context, code string, forceRelay bool, exposeArgs ...string) *harness {
	t.Helper()
	httpPort := freeTCPPort(t)
	udpPort := freeUDPPort(t)
	httpAddr := fmt.Sprintf("127.0.0.1:%d", httpPort)
	udpAddr := fmt.Sprintf("127.0.0.1:%d", udpPort)
	wsURL := fmt.Sprintf("ws://%s/signal", httpAddr)

	srv := start(t, ctx, "server", nil,
		"server", "--http", httpAddr, "--udp", udpAddr, "-v")
	waitTCP(t, httpAddr, 10*time.Second)

	var env []string
	if forceRelay {
		env = []string{"RT_FORCE_RELAY=1"}
	}
	args := append([]string{"expose", "--server", wsURL, "--code", code}, exposeArgs...)
	args = append(args, "-v")
	exp := start(t, ctx, "expose", env, args...)

	// Give expose a moment to register with the server.
	time.Sleep(1500 * time.Millisecond)

	return &harness{wsURL: wsURL, server: srv, expose: exp}
}

func (h *harness) dumpLogs(t *testing.T) {
	t.Helper()
	t.Logf("=== server log ===\n%s", h.server.log.String())
	t.Logf("=== expose log ===\n%s", h.expose.log.String())
}

func testForward(t *testing.T, forceRelay bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	echoAddr := startEchoServer(t)
	code := "fwd"
	if forceRelay {
		code = "fwdrelay"
	}
	h := setup(t, ctx, code, forceRelay, "--allow", echoAddr)

	localPort := freeTCPPort(t)
	localAddr := fmt.Sprintf("127.0.0.1:%d", localPort)
	var env []string
	if forceRelay {
		env = []string{"RT_FORCE_RELAY=1"}
	}
	spec := fmt.Sprintf("%d:%s", localPort, echoAddr)
	conn := start(t, ctx, "connect", env,
		"connect", "--server", h.wsURL, "--code", code, "-L", spec, "-v")

	// The local listener opens only after the tunnel is up.
	waitTCP(t, localAddr, 25*time.Second)

	c, err := net.DialTimeout("tcp", localAddr, 5*time.Second)
	if err != nil {
		h.dumpLogs(t)
		t.Logf("connect log:\n%s", conn.log.String())
		t.Fatalf("dial local forward: %v", err)
	}
	defer c.Close()

	msg := "ping-through-tunnel"
	if _, err := c.Write([]byte(msg + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	got, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		h.dumpLogs(t)
		t.Logf("connect log:\n%s", conn.log.String())
		t.Fatalf("read echo: %v", err)
	}
	if strings.TrimSpace(got) != msg {
		t.Fatalf("echo mismatch: got %q want %q", got, msg)
	}

	if forceRelay {
		if !strings.Contains(conn.log.String(), "server relay") {
			t.Errorf("expected relay mode in connect log, got:\n%s", conn.log.String())
		}
	}
}

func TestForwardDirect(t *testing.T) { testForward(t, false) }
func TestForwardRelay(t *testing.T)  { testForward(t, true) }

func TestFileTransfer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	root := t.TempDir()
	const contents = "the quick brown fox\n"
	if err := os.WriteFile(filepath.Join(root, "greeting.txt"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	h := setup(t, ctx, "files", false, "--file-root", root)

	// get
	localOut := filepath.Join(t.TempDir(), "out.txt")
	if _, err := runToCompletion(t, ctx, nil,
		"connect", "--server", h.wsURL, "--code", "files", "get", "greeting.txt", localOut); err != nil {
		h.dumpLogs(t)
		t.Fatalf("get: %v", err)
	}
	got, err := os.ReadFile(localOut)
	if err != nil || string(got) != contents {
		t.Fatalf("downloaded content mismatch: %q err=%v", string(got), err)
	}

	// put
	src := filepath.Join(t.TempDir(), "up.txt")
	const upContents = "uploaded-payload-42\n"
	if err := os.WriteFile(src, []byte(upContents), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runToCompletion(t, ctx, nil,
		"connect", "--server", h.wsURL, "--code", "files", "put", src, "received.txt"); err != nil {
		h.dumpLogs(t)
		t.Fatalf("put: %v", err)
	}
	up, err := os.ReadFile(filepath.Join(root, "received.txt"))
	if err != nil || string(up) != upContents {
		t.Fatalf("uploaded content mismatch: %q err=%v", string(up), err)
	}

	// path traversal must be denied (empty output file, no escape).
	esc := filepath.Join(t.TempDir(), "esc.txt")
	_, _ = runToCompletion(t, ctx, nil,
		"connect", "--server", h.wsURL, "--code", "files", "get", "../../../etc/hostname", esc)
	if info, err := os.Stat(esc); err == nil && info.Size() > 0 {
		t.Errorf("path traversal not denied: got %d bytes", info.Size())
	}
}

func TestExec(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	h := setup(t, ctx, "exec", false, "--allow-exec")

	var args []string
	if runtime.GOOS == "windows" {
		args = []string{"connect", "--server", h.wsURL, "--code", "exec", "exec", "--", "cmd", "/c", "echo", "EXEC_MARKER"}
	} else {
		args = []string{"connect", "--server", h.wsURL, "--code", "exec", "exec", "--", "/bin/echo", "EXEC_MARKER"}
	}
	out, err := runToCompletion(t, ctx, nil, args...)
	if err != nil {
		h.dumpLogs(t)
		t.Fatalf("exec: %v (out=%q)", err, out)
	}
	if !strings.Contains(out, "EXEC_MARKER") {
		h.dumpLogs(t)
		t.Fatalf("exec output missing marker: %q", out)
	}
}

func TestSOCKS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	echoAddr := startEchoServer(t)
	h := setup(t, ctx, "socks", false, "--allow-dial")

	socksPort := freeTCPPort(t)
	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	start(t, ctx, "connect", nil,
		"connect", "--server", h.wsURL, "--code", "socks", "-D", fmt.Sprintf("%d", socksPort), "-v")
	waitTCP(t, socksAddr, 25*time.Second)

	// Speak SOCKS5 CONNECT to the echo server, then echo through it.
	c, err := net.DialTimeout("tcp", socksAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial socks: %v", err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))

	host, portStr, _ := net.SplitHostPort(echoAddr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	// greeting
	if _, err := c.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatal(err)
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(c, resp); err != nil || resp[0] != 0x05 || resp[1] != 0x00 {
		t.Fatalf("socks greeting failed: %v %v", resp, err)
	}
	// CONNECT request with IPv4 target
	ip := net.ParseIP(host).To4()
	req := []byte{0x05, 0x01, 0x00, 0x01}
	req = append(req, ip...)
	req = append(req, byte(port>>8), byte(port&0xff))
	if _, err := c.Write(req); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(c, reply); err != nil || reply[1] != 0x00 {
		t.Fatalf("socks connect reply failed: %v %v", reply, err)
	}
	// Now the stream is an echo pipe.
	msg := "socks-echo-test"
	if _, err := c.Write([]byte(msg + "\n")); err != nil {
		t.Fatal(err)
	}
	got, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		h.dumpLogs(t)
		t.Fatalf("socks read: %v", err)
	}
	if strings.TrimSpace(got) != msg {
		t.Fatalf("socks echo mismatch: got %q want %q", got, msg)
	}
}

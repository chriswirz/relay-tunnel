//go:build integration

package integration

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// echoOnce dials addr, sends msg, and returns the echoed line.
func echoOnce(addr, msg string, timeout time.Duration) (string, error) {
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return "", err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(timeout))
	if _, err := c.Write([]byte(msg + "\n")); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	return strings.TrimSpace(line), err
}

// TestReconnectResumesTunnel verifies that a long-running `connect` tunnel keeps
// its local listener up and automatically resumes after the host goes offline
// and a new host comes back with the same session code.
func TestReconnectResumesTunnel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	wsURL := startRendezvous(t, ctx)
	echoAddr := startEchoServer(t)

	host1 := start(t, ctx, "expose1", nil,
		"expose", "--server", wsURL, "--code", "recon", "--allow", echoAddr, "-v")

	localPort := freeTCPPort(t)
	localAddr := fmt.Sprintf("127.0.0.1:%d", localPort)
	spec := fmt.Sprintf("%d:%s", localPort, echoAddr)
	conn := start(t, ctx, "connect", nil,
		"connect", "--server", wsURL, "--code", "recon", "-L", spec, "-v")

	waitTCP(t, localAddr, 25*time.Second)
	if got, err := echoOnce(localAddr, "before", 10*time.Second); err != nil || got != "before" {
		t.Fatalf("initial echo: got %q err %v\nconnect log:\n%s", got, err, conn.log.String())
	}

	// Take the host offline (hard kill; no graceful close).
	_ = host1.cmd.Process.Kill()
	_, _ = host1.cmd.Process.Wait()

	// The forward listener must stay up even while the tunnel is down.
	if !listening(localAddr) {
		t.Fatalf("local listener should remain open during the outage")
	}

	// Bring a new host up with the same code.
	start(t, ctx, "expose2", nil,
		"expose", "--server", wsURL, "--code", "recon", "--allow", echoAddr, "-v")

	// Poll until the tunnel resumes (dead-peer detection + reconnect).
	deadline := time.Now().Add(80 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		got, err := echoOnce(localAddr, "after", 5*time.Second)
		if err == nil && got == "after" {
			return // resumed
		}
		lastErr = err
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("tunnel did not resume after host restart: %v\nconnect log:\n%s", lastErr, conn.log.String())
}

// listening reports whether a TCP listener accepts a connection at addr.
func listening(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

package nat

import (
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func mustSocket(t *testing.T) *net.UDPConn {
	t.Helper()
	sock, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sock.Close() })
	return sock
}

func TestResolveCandidates(t *testing.T) {
	t.Run("drops duplicates and unusable entries", func(t *testing.T) {
		got, err := resolveCandidates([]string{
			"127.0.0.1:5000",
			"",
			"not-an-address",
			"127.0.0.1:5000", // the no-NAT case, where reflected == local
			"127.0.0.1:5001",
		})
		if err != nil {
			t.Fatalf("resolveCandidates: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("resolveCandidates() returned %d addresses, want 2: %v", len(got), got)
		}
	})

	t.Run("reports having nothing to punch to", func(t *testing.T) {
		if _, err := resolveCandidates([]string{"", "bad"}); err == nil {
			t.Error("resolveCandidates accepted a list with no usable address")
		}
	})
}

// Punch must succeed on whichever candidate answers, not only the first, which
// is the whole point of racing them: the reflected address is listed first and
// is exactly the one that fails when both peers are behind one NAT.
func TestPunchRacesCandidates(t *testing.T) {
	a, b := mustSocket(t), mustSocket(t)

	// A dead address stands in for the unreachable reflected one. Port 1 on
	// loopback has nothing on it, so probes there are simply never answered.
	dead := "127.0.0.1:1"
	live := b.LocalAddr().String()

	var wg sync.WaitGroup
	var aAddr *net.UDPAddr
	var aErr, bErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		aAddr, aErr = Punch(a, []string{dead, live}, 5*time.Second)
	}()
	go func() {
		defer wg.Done()
		_, bErr = Punch(b, []string{a.LocalAddr().String()}, 5*time.Second)
	}()
	wg.Wait()

	if aErr != nil {
		t.Fatalf("Punch(a): %v", aErr)
	}
	if bErr != nil {
		t.Fatalf("Punch(b): %v", bErr)
	}
	if aAddr.String() != live {
		t.Errorf("Punch settled on %s, want the reachable candidate %s", aAddr, live)
	}
}

// A punch with nobody on the far end has to give up rather than hang, and say
// what it tried so the failure is diagnosable.
func TestPunchTimesOut(t *testing.T) {
	sock := mustSocket(t)
	start := time.Now()
	_, err := Punch(sock, []string{"127.0.0.1:1", "127.0.0.1:2"}, 500*time.Millisecond)
	if err == nil {
		t.Fatal("Punch reported success with nothing on the other end")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Punch took %v to give up on a 500ms timeout", elapsed)
	}
	for _, want := range []string{"127.0.0.1:1", "127.0.0.1:2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name candidate %s", err, want)
		}
	}
}

// Traffic from an address the peer never offered must not complete a punch.
func TestPunchIgnoresStrangers(t *testing.T) {
	target, stranger := mustSocket(t), mustSocket(t)
	done := make(chan error, 1)
	go func() {
		_, err := Punch(target, []string{"127.0.0.1:1"}, 700*time.Millisecond)
		done <- err
	}()
	deadline := time.After(500 * time.Millisecond)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	to := target.LocalAddr().(*net.UDPAddr)
sending:
	for {
		select {
		case <-deadline:
			break sending
		case <-tick.C:
			stranger.WriteToUDP([]byte("RTPUNCH-ACK"), to)
		}
	}
	if err := <-done; err == nil {
		t.Error("Punch completed on an address the peer never offered")
	}
}

// The candidates are only useful if they carry the socket's own port, since
// that is what the peer sends to.
func TestLocalCandidatesUsePortOfSocket(t *testing.T) {
	sock := mustSocket(t)
	port := strconv.Itoa(sock.LocalAddr().(*net.UDPAddr).Port)
	for _, c := range LocalCandidates(sock) {
		_, p, err := net.SplitHostPort(c)
		if err != nil {
			t.Errorf("candidate %q is not host:port: %v", c, err)
			continue
		}
		if p != port {
			t.Errorf("candidate %q has port %s, want the socket's %s", c, p, port)
		}
		ip := net.ParseIP(strings.Trim(c[:strings.LastIndex(c, ":")], "[]"))
		if ip == nil {
			t.Errorf("candidate %q has an unparseable address", c)
			continue
		}
		// Loopback would only ever describe this machine, and a link-local
		// address needs a zone the peer cannot use.
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			t.Errorf("candidate %q is not reachable from another machine", c)
		}
	}
}

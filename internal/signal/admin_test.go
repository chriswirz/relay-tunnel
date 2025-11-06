package signal

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestServer returns a server with the admin interface on and an httptest
// server in front of it, plus a client that keeps cookies so a sign in sticks.
func newTestServer(t *testing.T, cfg ServerConfig) (*Server, *httptest.Server, *http.Client) {
	t.Helper()
	s := NewServer(cfg)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	return s, ts, &http.Client{Jar: mustJar(t), Timeout: 10 * time.Second}
}

// get, post and del are thin wrappers so a test reads as a sequence of calls
// rather than a sequence of request construction.
func get(t *testing.T, c *http.Client, url string) (int, []byte) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return readAll(t, resp)
}

func post(t *testing.T, c *http.Client, url, body string) (int, []byte) {
	t.Helper()
	resp, err := c.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return readAll(t, resp)
}

func del(t *testing.T, c *http.Client, url string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", url, err)
	}
	return readAll(t, resp)
}

func readAll(t *testing.T, resp *http.Response) (int, []byte) {
	t.Helper()
	defer resp.Body.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return resp.StatusCode, buf
}

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := hashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "pbkdf2-sha256$") {
		t.Fatalf("hash is not self describing: %q", hash)
	}
	if strings.Contains(hash, "correct horse") {
		t.Fatal("the hash contains the password")
	}
	if !verifyPassword(hash, "correct horse battery") {
		t.Fatal("the right password did not verify")
	}
	if verifyPassword(hash, "correct horse batter") {
		t.Fatal("a wrong password verified")
	}
	// A hash this build cannot parse must fail closed rather than let anything in.
	for _, bad := range []string{"", "plaintext", "pbkdf2-sha256$0$x$y", "argon2$1$x$y", "pbkdf2-sha256$a$b$c"} {
		if verifyPassword(bad, "anything") {
			t.Fatalf("unparseable hash %q accepted a password", bad)
		}
	}
}

func TestValidateUsername(t *testing.T) {
	for _, ok := range []string{"admin", "crwirz", "ops-1", "a.b_c"} {
		if err := validateUsername(ok); err != nil {
			t.Errorf("validateUsername(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"ab", strings.Repeat("x", 33), "has space", "Upper", "sym$bol"} {
		if err := validateUsername(bad); err == nil {
			t.Errorf("validateUsername(%q) = nil, want an error", bad)
		}
	}
}

// TestFirstBootGate is the rule that everything else rests on: the default
// password gets a session in, and that session can do nothing but change it.
func TestFirstBootGate(t *testing.T) {
	saved := map[string]string{}
	_, ts, c := newTestServer(t, ServerConfig{
		SaveAdmin: func(username, hash string) error {
			saved["username"], saved["hash"] = username, hash
			return nil
		},
	})

	if code, body := get(t, c, ts.URL+"/api/v1/status"); code != http.StatusUnauthorized {
		t.Fatalf("status without signing in = %d %s, want 401", code, body)
	}

	var status authStatus
	code, body := get(t, c, ts.URL+"/api/v1/auth/session")
	if code != http.StatusOK {
		t.Fatalf("auth session = %d %s", code, body)
	}
	mustJSONInto(t, body, &status)
	if !status.PasswordUnset || status.Authenticated {
		t.Fatalf("fresh server reported %+v", status)
	}

	if code, body := post(t, c, ts.URL+"/api/v1/auth/login", `{"username":"admin","password":"wrong"}`); code != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d %s, want 401", code, body)
	}

	code, body = post(t, c, ts.URL+"/api/v1/auth/login", `{"username":"admin","password":"admin"}`)
	if code != http.StatusOK {
		t.Fatalf("default login = %d %s", code, body)
	}
	mustJSONInto(t, body, &status)
	if !status.MustChangePassword {
		t.Fatal("the default password did not demand a change")
	}

	// Signed in, but penned in: nothing about the server is readable yet.
	for _, path := range []string{"/api/v1/status", "/api/v1/sessions", "/api/v1/relays"} {
		if code, body := get(t, c, ts.URL+path); code != http.StatusUnauthorized {
			t.Fatalf("%s before choosing a password = %d %s, want 401", path, code, body)
		}
	}

	// The new password has to be a real one.
	for _, bad := range []string{`{"current_password":"admin","new_password":"short"}`, `{"current_password":"admin","new_password":"admin"}`} {
		if code, body := post(t, c, ts.URL+"/api/v1/auth/account", bad); code != http.StatusBadRequest {
			t.Fatalf("account %s = %d %s, want 400", bad, code, body)
		}
	}

	if code, body := post(t, c, ts.URL+"/api/v1/auth/account", `{"current_password":"admin","new_password":"correct horse"}`); code != http.StatusOK {
		t.Fatalf("password change = %d %s", code, body)
	}
	if saved["hash"] == "" {
		t.Fatal("the new password was not persisted")
	}
	if code, body := get(t, c, ts.URL+"/api/v1/status"); code != http.StatusOK {
		t.Fatalf("status after choosing a password = %d %s", code, body)
	}
	// And the default is now dead, in a fresh browser.
	fresh := &http.Client{Jar: mustJar(t)}
	if code, _ := post(t, fresh, ts.URL+"/api/v1/auth/login", `{"username":"admin","password":"admin"}`); code != http.StatusUnauthorized {
		t.Fatalf("the default password still works after a change (%d)", code)
	}
}

// TestRenameRetiresOldName covers the promise the account page makes: there is
// one account, and renaming it does not leave the old name behind.
func TestRenameRetiresOldName(t *testing.T) {
	hash, err := hashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	_, ts, c := newTestServer(t, ServerConfig{
		Admin:     AdminConfig{PasswordHash: hash},
		SaveAdmin: func(string, string) error { return nil },
	})

	if code, body := post(t, c, ts.URL+"/api/v1/auth/login", `{"username":"admin","password":"correct horse"}`); code != http.StatusOK {
		t.Fatalf("login = %d %s", code, body)
	}
	if code, body := post(t, c, ts.URL+"/api/v1/auth/account", `{"current_password":"correct horse","new_username":"crwirz"}`); code != http.StatusOK {
		t.Fatalf("rename = %d %s", code, body)
	}

	fresh := &http.Client{Jar: mustJar(t)}
	if code, _ := post(t, fresh, ts.URL+"/api/v1/auth/login", `{"username":"admin","password":"correct horse"}`); code != http.StatusUnauthorized {
		t.Fatalf("the old name still signs in (%d)", code)
	}
	if code, body := post(t, fresh, ts.URL+"/api/v1/auth/login", `{"username":"crwirz","password":"correct horse"}`); code != http.StatusOK {
		t.Fatalf("the new name does not sign in: %d %s", code, body)
	}
}

// TestSessionAndRelayViews drives the read and delete endpoints against state
// planted directly on the server, which is how a rendezvous in progress looks.
func TestSessionAndRelayViews(t *testing.T) {
	s, ts, c := signedIn(t)

	now := time.Now()
	s.mu.Lock()
	s.sessions["quiet-harbor"] = &session{
		code:    "quiet-harbor",
		created: now,
		peers: map[Role]*peerConn{
			RoleHost: {role: RoleHost, token: "aaaa", public: "203.0.113.7:51820", ready: closedChan(), joined: now},
		},
	}
	s.relays["aaaa"] = &relayRoute{
		peer: "bbbb", code: "quiet-harbor", role: RoleHost,
		addr:  &net.UDPAddr{IP: net.IPv4(203, 0, 113, 7), Port: 51820},
		first: now, seen: now, packets: 3, bytes: 300,
	}
	s.relays["bbbb"] = &relayRoute{peer: "aaaa", code: "quiet-harbor", role: RoleClient, first: now, seen: now}
	s.relays["idle"] = &relayRoute{first: now.Add(-time.Hour), seen: now.Add(-time.Hour)}
	s.mu.Unlock()

	var list struct {
		Sessions []SessionView `json:"sessions"`
	}
	code, body := get(t, c, ts.URL+"/api/v1/sessions")
	if code != http.StatusOK {
		t.Fatalf("sessions = %d %s", code, body)
	}
	mustJSONInto(t, body, &list)
	if len(list.Sessions) != 1 || list.Sessions[0].Code != "quiet-harbor" {
		t.Fatalf("sessions = %+v", list.Sessions)
	}
	if p := list.Sessions[0].Peers; len(p) != 1 || p[0].Role != RoleHost || !p[0].Ready {
		t.Fatalf("peers = %+v", p)
	}

	if code, body := get(t, c, ts.URL+"/api/v1/sessions/quiet-harbor"); code != http.StatusOK {
		t.Fatalf("one session = %d %s", code, body)
	}
	if code, _ := get(t, c, ts.URL+"/api/v1/sessions/nope"); code != http.StatusNotFound {
		t.Fatalf("unknown session = %d, want 404", code)
	}

	var relays struct {
		Relays []RelayView `json:"relays"`
	}
	code, body = get(t, c, ts.URL+"/api/v1/relays")
	if code != http.StatusOK {
		t.Fatalf("relays = %d %s", code, body)
	}
	mustJSONInto(t, body, &relays)
	if len(relays.Relays) != 3 {
		t.Fatalf("want 3 relay routes, got %d", len(relays.Relays))
	}
	// Busiest first, so the route actually carrying traffic leads.
	if relays.Relays[0].Token != "aaaa" || relays.Relays[0].Bytes != 300 {
		t.Fatalf("relays not ordered by traffic: %+v", relays.Relays[0])
	}
	if relays.Relays[0].Addr != "203.0.113.7:51820" {
		t.Fatalf("relay address = %q", relays.Relays[0].Addr)
	}

	// Dropping one side drops the pair, and leaves the unpaired route alone.
	if code, body := del(t, c, ts.URL+"/api/v1/relays/aaaa"); code != http.StatusNoContent {
		t.Fatalf("delete relay = %d %s", code, body)
	}
	s.mu.Lock()
	_, gotA := s.relays["aaaa"]
	_, gotB := s.relays["bbbb"]
	_, gotIdle := s.relays["idle"]
	s.mu.Unlock()
	if gotA || gotB {
		t.Fatal("dropping one side left the other behind")
	}
	if !gotIdle {
		t.Fatal("dropping a pair took an unrelated route with it")
	}
	if code, _ := del(t, c, ts.URL+"/api/v1/relays/aaaa"); code != http.StatusNotFound {
		t.Fatalf("deleting a gone relay = %d, want 404", code)
	}
}

func TestPruneRelays(t *testing.T) {
	s, ts, c := signedIn(t)

	now := time.Now()
	s.mu.Lock()
	s.relays["fresh"] = &relayRoute{first: now, seen: now}
	s.relays["stale"] = &relayRoute{first: now.Add(-time.Hour), seen: now.Add(-time.Hour)}
	s.mu.Unlock()

	if code, body := del(t, c, ts.URL+"/api/v1/relays?idle_for=7y"); code != http.StatusBadRequest {
		t.Fatalf("bogus window = %d %s, want 400", code, body)
	}

	code, body := del(t, c, ts.URL+"/api/v1/relays?idle_for=15m")
	if code != http.StatusOK {
		t.Fatalf("prune = %d %s", code, body)
	}
	var result struct {
		Deleted int      `json:"deleted"`
		Tokens  []string `json:"tokens"`
	}
	mustJSONInto(t, body, &result)
	if result.Deleted != 1 || len(result.Tokens) != 1 || result.Tokens[0] != "stale" {
		t.Fatalf("prune reclaimed %+v, want just the idle route", result)
	}
	s.mu.Lock()
	_, fresh := s.relays["fresh"]
	s.mu.Unlock()
	if !fresh {
		t.Fatal("prune took a route that was not idle")
	}
}

func TestStatusReportsConfiguredAddresses(t *testing.T) {
	s, ts, c := signedInWith(t, ServerConfig{
		HTTPAddr:  ":7000",
		UDPAddr:   ":7001",
		PublicUDP: "relay.example.com:7001",
		PublicURL: "wss://relay.example.com/signal",
		Version:   "v1.2.3",
	})
	s.stats.pairs.Add(2)
	s.stats.relayedBytes.Add(4096)

	code, body := get(t, c, ts.URL+"/api/v1/status")
	if code != http.StatusOK {
		t.Fatalf("status = %d %s", code, body)
	}
	var got StatusView
	mustJSONInto(t, body, &got)
	if got.Version != "v1.2.3" || got.PublicUDP != "relay.example.com:7001" || got.PublicURL != "wss://relay.example.com/signal" {
		t.Fatalf("status did not report the configuration: %+v", got)
	}
	if got.Totals.Pairs != 2 || got.Totals.RelayedBytes != 4096 {
		t.Fatalf("totals = %+v", got.Totals)
	}
	if got.RelayTTLSeconds != relayTTL.Seconds() {
		t.Fatalf("relay ttl = %v", got.RelayTTLSeconds)
	}
}

// TestAdminDisabled is the promise of the config switch: no interface, no API,
// and signaling still there.
func TestAdminDisabled(t *testing.T) {
	_, ts, c := newTestServer(t, ServerConfig{Admin: AdminConfig{Disabled: true}})
	for _, path := range []string{"/api/v1/status", "/api/v1/sessions", "/api/v1/auth/session", "/openapi.json"} {
		if code, _ := get(t, c, ts.URL+path); code != http.StatusNotFound {
			t.Errorf("%s with the admin interface off = %d, want 404", path, code)
		}
	}
	if code, body := get(t, c, ts.URL+"/"); code != http.StatusOK || !strings.Contains(string(body), "rendezvous server") {
		t.Fatalf("root = %d %s", code, body)
	}
}

// TestOpenAPIIsServedAndParses keeps the embedded spec honest: it is the
// contract the Swagger page and any generated client read.
func TestOpenAPIIsServedAndParses(t *testing.T) {
	_, ts, c := newTestServer(t, ServerConfig{})
	// Unauthenticated on purpose: it documents how to authenticate.
	code, body := get(t, c, ts.URL+"/openapi.json")
	if code != http.StatusOK {
		t.Fatalf("openapi.json = %d", code)
	}
	var doc struct {
		OpenAPI string                    `json:"openapi"`
		Paths   map[string]map[string]any `json:"paths"`
	}
	mustJSONInto(t, body, &doc)
	if !strings.HasPrefix(doc.OpenAPI, "3.") {
		t.Fatalf("openapi version = %q", doc.OpenAPI)
	}
	// Every route the server actually serves should be described, or the page
	// rendered from this document is lying about the server it is served by.
	for _, want := range []string{
		"/api/v1/status", "/api/v1/sessions", "/api/v1/sessions/{code}",
		"/api/v1/relays", "/api/v1/relays/{token}",
		"/api/v1/auth/session", "/api/v1/auth/login", "/api/v1/auth/logout", "/api/v1/auth/account",
		"/healthz", "/openapi.json",
	} {
		if _, ok := doc.Paths[want]; !ok {
			t.Errorf("openapi.json does not document %s", want)
		}
	}
}

// --- helpers ---------------------------------------------------------------

// signedIn is a server with a password set and a client already through the
// door, which is the starting point for every endpoint test.
func signedIn(t *testing.T) (*Server, *httptest.Server, *http.Client) {
	t.Helper()
	return signedInWith(t, ServerConfig{})
}

func signedInWith(t *testing.T, cfg ServerConfig) (*Server, *httptest.Server, *http.Client) {
	t.Helper()
	hash, err := hashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Admin.PasswordHash = hash
	s, ts, c := newTestServer(t, cfg)
	if code, body := post(t, c, ts.URL+"/api/v1/auth/login", `{"username":"admin","password":"correct horse"}`); code != http.StatusOK {
		t.Fatalf("login = %d %s", code, body)
	}
	return s, ts, c
}

func closedChan() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func mustJSONInto(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
}

// mustJar gives a client somewhere to keep the session cookie, which is the
// whole of the browser side of this API's authentication.
func mustJar(t *testing.T) http.CookieJar {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return jar
}

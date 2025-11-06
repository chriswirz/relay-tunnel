// Package config loads an optional JSON config file whose values are used only
// when the corresponding command-line flag was not set. Command-line arguments
// always take priority; the config file lets you keep settings (most usefully
// the session code) handy across re-runs.
//
// The file is organized into three sections that mirror the subcommands:
// "server", "expose" and "connect". Each section holds the settings for that
// subcommand.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/chriswirz/relay-tunnel/internal/xlog"
)

// DefaultPath is the config file looked for when --config is not given.
const DefaultPath = "config.json"

// Config is the top-level file, split into one section per subcommand.
type Config struct {
	Server  ServerSection  `json:"server"`
	Expose  ExposeSection  `json:"expose"`
	Connect ConnectSection `json:"connect"`
}

// ServerSection holds settings for the `server` subcommand.
// Enabled makes this mode run when the binary is started with no subcommand.
type ServerSection struct {
	Enabled bool `json:"enabled,omitempty"`
	// Name is an optional label for this server, shown in its admin interface.
	Name      string `json:"name,omitempty"`
	HTTP      string `json:"http,omitempty"`
	UDP       string `json:"udp,omitempty"`
	PublicUDP string `json:"public_udp,omitempty"`
	// PublicURL is the externally reachable signaling URL (for example
	// wss://relay.example.com/signal when running behind a TLS reverse proxy).
	// It is informational: the server logs it at startup so operators share the
	// right URL. Unlike PublicUDP it does not change behavior, because clients
	// dial the signaling URL directly rather than being told it by the server.
	PublicURL string `json:"public_url,omitempty"`
	Verbose   bool   `json:"verbose,omitempty"`
	// Admin configures the built-in web interface, which is served on the same
	// HTTP listener as signaling.
	Admin AdminSection `json:"admin,omitempty"`
}

// AdminSection configures the administrative web interface and its API.
//
// There is exactly one administrator account. Until a password is chosen the
// server accepts admin/admin once and then demands a new one, which is stored
// here as a PBKDF2-HMAC-SHA256 hash rather than as the password itself.
type AdminSection struct {
	// Disabled turns the web interface and its API off, leaving the server with
	// only /signal. The zero value keeps it on, so an existing config file that
	// says nothing about the admin interface still gets one.
	Disabled bool `json:"disabled,omitempty"`
	// Username is the administrator's name; empty means the default, "admin".
	Username string `json:"username,omitempty"`
	// PasswordHash is pbkdf2-sha256$iterations$salt$key, all base64. Empty means
	// no password has been chosen yet.
	PasswordHash string `json:"password_hash,omitempty"`
	// SessionHours is how long a signed-in browser stays signed in; 0 means 12.
	SessionHours int `json:"session_hours,omitempty"`
	// TrustForwardedHeaders makes the server believe X-Forwarded-Proto and
	// X-Forwarded-For. Set it only when a reverse proxy you control sets them,
	// since anyone can send those headers to a server reachable directly.
	TrustForwardedHeaders bool `json:"trust_forwarded_headers,omitempty"`
}

// ExposeSection holds settings for the `expose` subcommand.
// Enabled makes this mode run when the binary is started with no subcommand.
type ExposeSection struct {
	Enabled bool `json:"enabled,omitempty"`
	// Name is an optional label for this peer, shown in the rendezvous server's
	// admin interface so an operator can tell one machine from another.
	Name      string   `json:"name,omitempty"`
	Server    string   `json:"server,omitempty"`
	Code      string   `json:"code,omitempty"`
	AllowDial bool     `json:"allow_dial,omitempty"`
	Allow     []string `json:"allow,omitempty"`
	AllowExec bool     `json:"allow_exec,omitempty"`
	FileRoot  string   `json:"file_root,omitempty"`
	Verbose   bool     `json:"verbose,omitempty"`
}

// ConnectSection holds settings for the `connect` subcommand.
// Enabled makes this mode run when the binary is started with no subcommand.
type ConnectSection struct {
	Enabled bool `json:"enabled,omitempty"`
	// Name is an optional label for this peer, shown in the rendezvous server's
	// admin interface so an operator can tell one machine from another.
	Name    string   `json:"name,omitempty"`
	Server  string   `json:"server,omitempty"`
	Code    string   `json:"code,omitempty"`
	Forward []string `json:"forward,omitempty"`
	Socks   []string `json:"socks,omitempty"`
	Verbose bool     `json:"verbose,omitempty"`
}

// Load reads and parses the config at path.
// If the file does not exist and explicit is false, it returns an empty config
// and no error, so the default path is optional.
// If explicit is true, a missing file is an error.
func Load(path string, explicit bool) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !explicit {
			return &Config{}, nil
		}
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	// encoding/json discards keys it does not recognize, which turns a setting
	// in the wrong section - "socks" under "expose", say - into a line that is
	// read, ignored, and never mentioned again. Report them instead. This is a
	// warning rather than an error so that a config written for a newer version
	// still runs on an older binary.
	for _, key := range unknownKeys(data) {
		xlog.Infof("config: %s: ignoring unknown setting %q", path, key)
	}
	return &c, nil
}

// unknownKeys lists the keys in the file that no section field claims, as
// dotted paths ("expose.socks"). It walks the same one-level-of-sections shape
// Config has rather than being general, because that is the whole file.
func unknownKeys(data []byte) []string {
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return nil // a parse failure is reported by the caller
	}
	var out []string
	cfg := reflect.TypeOf(Config{})
	for key, section := range raw {
		field, ok := fieldByJSONName(cfg, key)
		if !ok {
			out = append(out, key)
			continue
		}
		for _, sub := range unknownFields(section, field.Type) {
			out = append(out, key+"."+sub)
		}
	}
	sort.Strings(out)
	return out
}

// unknownFields lists the keys of one JSON object that struct type t does not
// declare. A value that is not an object (or a type that is not a struct) has
// nothing to check, and reports none.
func unknownFields(data json.RawMessage, t reflect.Type) []string {
	if t.Kind() != reflect.Struct {
		return nil
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	var out []string
	for key, value := range raw {
		field, ok := fieldByJSONName(t, key)
		if !ok {
			out = append(out, key)
			continue
		}
		for _, sub := range unknownFields(value, field.Type) {
			out = append(out, key+"."+sub)
		}
	}
	return out
}

// fieldByJSONName finds the field a JSON key unmarshals into. Matching is
// case-insensitive because that is what encoding/json itself does.
func fieldByJSONName(t reflect.Type, key string) (reflect.StructField, bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" {
			name = f.Name
		}
		if name == "-" {
			continue
		}
		if strings.EqualFold(name, key) {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

// Example returns a sample configuration as indented JSON. It documents every
// field of every section with representative values. A mirror of the section
// types without omitempty is used so that all fields are shown.
func Example() string {
	type adminFull struct {
		Disabled              bool   `json:"disabled"`
		Username              string `json:"username"`
		PasswordHash          string `json:"password_hash"`
		SessionHours          int    `json:"session_hours"`
		TrustForwardedHeaders bool   `json:"trust_forwarded_headers"`
	}
	type serverFull struct {
		Enabled   bool      `json:"enabled"`
		Name      string    `json:"name"`
		HTTP      string    `json:"http"`
		UDP       string    `json:"udp"`
		PublicUDP string    `json:"public_udp"`
		PublicURL string    `json:"public_url"`
		Verbose   bool      `json:"verbose"`
		Admin     adminFull `json:"admin"`
	}
	type exposeFull struct {
		Enabled   bool     `json:"enabled"`
		Name      string   `json:"name"`
		Server    string   `json:"server"`
		Code      string   `json:"code"`
		AllowDial bool     `json:"allow_dial"`
		Allow     []string `json:"allow"`
		AllowExec bool     `json:"allow_exec"`
		FileRoot  string   `json:"file_root"`
		Verbose   bool     `json:"verbose"`
	}
	type connectFull struct {
		Enabled bool     `json:"enabled"`
		Name    string   `json:"name"`
		Server  string   `json:"server"`
		Code    string   `json:"code"`
		Forward []string `json:"forward"`
		Socks   []string `json:"socks"`
		Verbose bool     `json:"verbose"`
	}
	full := struct {
		Server  serverFull  `json:"server"`
		Expose  exposeFull  `json:"expose"`
		Connect connectFull `json:"connect"`
	}{
		Server: serverFull{
			Enabled:   false,
			Name:      "rendezvous",
			HTTP:      ":7000",
			UDP:       ":7001",
			PublicUDP: "your.host.example:7001",
			PublicURL: "wss://your.host.example/signal",
			Verbose:   false,
			Admin: adminFull{
				Disabled:              false,
				Username:              "admin",
				PasswordHash:          "",
				SessionHours:          12,
				TrustForwardedHeaders: false,
			},
		},
		Expose: exposeFull{
			Enabled:   false,
			Name:      "workshop-pi",
			Server:    "ws://your.host.example:7000/signal",
			Code:      "your-session-code",
			AllowDial: true,
			Allow:     []string{"127.0.0.1:8080"},
			AllowExec: false,
			FileRoot:  "/srv/share",
			Verbose:   false,
		},
		Connect: connectFull{
			Enabled: false,
			Name:    "laptop",
			Server:  "ws://your.host.example:7000/signal",
			Code:    "your-session-code",
			Forward: []string{"9000:127.0.0.1:8080"},
			Socks:   []string{"1080"},
			Verbose: false,
		},
	}
	b, _ := json.MarshalIndent(full, "", "  ")
	return string(b) + "\n"
}

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
	Enabled   bool   `json:"enabled,omitempty"`
	HTTP      string `json:"http,omitempty"`
	UDP       string `json:"udp,omitempty"`
	PublicUDP string `json:"public_udp,omitempty"`
	Verbose   bool   `json:"verbose,omitempty"`
}

// ExposeSection holds settings for the `expose` subcommand.
// Enabled makes this mode run when the binary is started with no subcommand.
type ExposeSection struct {
	Enabled   bool     `json:"enabled,omitempty"`
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
	Enabled bool     `json:"enabled,omitempty"`
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
	return &c, nil
}

// Example returns a sample configuration as indented JSON. It documents every
// field of every section with representative values. A mirror of the section
// types without omitempty is used so that all fields are shown.
func Example() string {
	type serverFull struct {
		Enabled   bool   `json:"enabled"`
		HTTP      string `json:"http"`
		UDP       string `json:"udp"`
		PublicUDP string `json:"public_udp"`
		Verbose   bool   `json:"verbose"`
	}
	type exposeFull struct {
		Enabled   bool     `json:"enabled"`
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
			HTTP:      ":7000",
			UDP:       ":7001",
			PublicUDP: "your.host.example:7001",
			Verbose:   false,
		},
		Expose: exposeFull{
			Enabled:   false,
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

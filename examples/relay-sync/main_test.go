package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveEndpointFromSection(t *testing.T) {
	dir := t.TempDir()
	cfg := writeConfig(t, dir, `{
		"expose":  { "server": "ws://h/signal", "code": "expcode" },
		"connect": { "server": "ws://h/signal", "code": "concode" }
	}`)

	// serve maps to the expose section.
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.String("config", "", "")
	fs.String("server", "", "")
	fs.String("code", "", "")
	fs.Parse([]string{"--config", cfg})
	srv, code := resolveEndpoint(fs, cfg, "expose", "", "")
	if srv != "ws://h/signal" || code != "expcode" {
		t.Errorf("expose section: got (%q,%q)", srv, code)
	}

	// push/pull map to the connect section.
	fs2 := flag.NewFlagSet("push", flag.ContinueOnError)
	fs2.String("config", "", "")
	fs2.String("server", "", "")
	fs2.String("code", "", "")
	fs2.Parse([]string{"--config", cfg})
	srv, code = resolveEndpoint(fs2, cfg, "connect", "", "")
	if srv != "ws://h/signal" || code != "concode" {
		t.Errorf("connect section: got (%q,%q)", srv, code)
	}
}

func TestResolveEndpointFlagWins(t *testing.T) {
	dir := t.TempDir()
	cfg := writeConfig(t, dir, `{ "connect": { "server": "ws://cfg/signal", "code": "cfgcode" } }`)

	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	fs.String("config", "", "")
	flagServer := fs.String("server", "", "")
	flagCode := fs.String("code", "", "")
	fs.Parse([]string{"--config", cfg, "--code", "clicode"})
	srv, code := resolveEndpoint(fs, cfg, "connect", *flagServer, *flagCode)
	// server not set on CLI -> from config; code set on CLI -> flag wins.
	if srv != "ws://cfg/signal" {
		t.Errorf("server should come from config, got %q", srv)
	}
	if code != "clicode" {
		t.Errorf("code flag should override config, got %q", code)
	}
}

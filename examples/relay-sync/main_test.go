package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
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

// Flags are natural to type on either side of the command, and the command
// itself has to be found without swallowing a flag's value.
func TestSplitCommand(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		command string
		rest    []string
	}{
		{
			"flags after the command",
			[]string{"serve", "--config", "x.json", "."},
			"serve", []string{"--config", "x.json", "."},
		},
		{
			"flags before the command",
			[]string{"--config", "x.json", "serve", "."},
			"serve", []string{"--config", "x.json", "."},
		},
		{
			"flags on both sides",
			[]string{"--config", "x.json", "push", "--delete", "a", "b"},
			"push", []string{"--config", "x.json", "--delete", "a", "b"},
		},
		{
			"attached value",
			[]string{"--config=x.json", "pull", "a", "b"},
			"pull", []string{"--config=x.json", "a", "b"},
		},
		{
			"a boolean flag does not swallow the command",
			[]string{"--dry-run", "push", "a", "b"},
			"push", []string{"--dry-run", "a", "b"},
		},
		{
			"a file named like a command is not the command",
			[]string{"--config", "serve", "push", "a", "b"},
			"push", []string{"--config", "serve", "a", "b"},
		},
		{
			"only the first occurrence is the command",
			[]string{"push", "serve", "b"},
			"push", []string{"serve", "b"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			command, rest, err := splitCommand(c.args)
			if err != nil {
				t.Fatalf("splitCommand(%q): %v", c.args, err)
			}
			if command != c.command {
				t.Errorf("command = %q, want %q", command, c.command)
			}
			if strings.Join(rest, " ") != strings.Join(c.rest, " ") {
				t.Errorf("rest = %q, want %q", rest, c.rest)
			}
		})
	}
}

func TestSplitCommandErrors(t *testing.T) {
	if _, _, err := splitCommand([]string{"--config", "x.json"}); err == nil {
		t.Error("splitCommand accepted flags with no command")
	}
	if _, _, err := splitCommand([]string{"sync", "a", "b"}); err == nil {
		t.Error("splitCommand accepted an unknown command")
	}
}

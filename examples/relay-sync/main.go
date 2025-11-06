// Command relay-sync is an example application built on the relay-tunnel
// module. It performs one-way directory synchronization (a mirror) between two
// machines over a direct peer-to-peer connection, using relay-tunnel only as
// the transport: it reuses the module's rendezvous (internal/signal) and
// authenticated QUIC transport (internal/transport), and layers its own small
// sync protocol on top (see proto.go).
//
// It relies on the same rendezvous server as relay-tunnel:
//
//	relay-tunnel server --http :7000 --udp :7001
//
// Then, on the machine that holds the files:
//
//	relay-sync serve --server ws://HOST:7000/signal --code CODE /path/to/dir
//
// And on the other machine, mirror in either direction:
//
//	relay-sync push --server ws://HOST:7000/signal --code CODE ./local remote/sub
//	relay-sync pull --server ws://HOST:7000/signal --code CODE remote/sub ./local
//
// push copies local -> remote; pull copies remote -> local. Remote paths are
// relative to the served root. Use --delete to remove extraneous files at the
// destination and --dry-run to preview.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/chriswirz/relay-tunnel/internal/config"
)

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "example-config", "-example-config", "--example-config":
		fmt.Print(config.Example())
		return
	case "-h", "--help", "help":
		usage()
		return
	}

	// Flags may come before the command as well as after it: both
	// "relay-sync --config x.json serve DIR" and "relay-sync serve --config
	// x.json DIR" are natural to type, and the command's FlagSet parses either
	// once the two halves are joined.
	command, args, err := splitCommand(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n\n", err)
		usage()
		os.Exit(2)
	}
	switch command {
	case "serve":
		cmdServe(args)
	case "push", "pull":
		cmdSync(command, args)
	}
}

// commands are the subcommands splitCommand looks for.
var commands = map[string]bool{"serve": true, "push": true, "pull": true}

// valueFlags are the flags that take a separate argument, so the value of one
// is never mistaken for the command - "--config serve" names a file. The
// boolean flags (--delete, --dry-run, --gitignore) take no separate argument.
var valueFlags = map[string]bool{"config": true, "server": true, "code": true, "retry": true}

// splitCommand finds the command among args and returns it along with every
// other argument, in order, with the command itself removed. Flags keep their
// relative order, so one given on both sides of the command resolves the way
// the flag package would.
func splitCommand(args []string) (command string, rest []string, err error) {
	rest = make([]string, 0, len(args))
	skipValue := false
	for i, a := range args {
		if skipValue {
			skipValue = false
			rest = append(rest, a)
			continue
		}
		if command == "" && commands[a] {
			command = a
			continue
		}
		if name, attached := flagName(a); name != "" {
			// "--config PATH" swallows the next argument; "--config=PATH" does
			// not, and neither does a boolean flag.
			skipValue = !attached && valueFlags[name] && i+1 < len(args)
		}
		rest = append(rest, a)
	}
	if command == "" {
		if strings.HasPrefix(args[0], "-") {
			return "", nil, fmt.Errorf("no command given; expected serve, push or pull alongside %s", args[0])
		}
		return "", nil, fmt.Errorf("unknown command %q", args[0])
	}
	return command, rest, nil
}

// flagName reports the name of a flag argument ("--config=x" gives "config")
// and whether its value was attached with "=". A non-flag argument has no name.
func flagName(arg string) (name string, attached bool) {
	if !strings.HasPrefix(arg, "-") || arg == "-" || arg == "--" {
		return "", false
	}
	trimmed := strings.TrimLeft(arg, "-")
	if before, _, found := strings.Cut(trimmed, "="); found {
		return before, true
	}
	return trimmed, false
}

func usage() {
	fmt.Fprint(os.Stderr, `relay-sync - one-way directory sync over relay-tunnel

USAGE
  relay-sync serve --server ws://HOST:7000/signal --code CODE DIR
  relay-sync push  --server ws://HOST:7000/signal --code CODE LOCAL REMOTE [--delete] [--dry-run]
  relay-sync pull  --server ws://HOST:7000/signal --code CODE REMOTE LOCAL [--delete] [--dry-run]
  relay-sync --example-config

  Flags may come before or after the command, so these are the same:
    relay-sync --config sync.json push ./local remote
    relay-sync push --config sync.json ./local remote

  serve  host the directory DIR and answer sync requests
  push   mirror LOCAL (this machine) onto REMOTE (relative to the host's DIR)
  pull   mirror REMOTE (relative to the host's DIR) onto LOCAL (this machine)

FLAGS
  --server URL     rendezvous server, e.g. ws://HOST:7000/signal
  --code CODE      shared session code (must match on both sides)
  --delete         delete files at the destination that are absent from the source
  --dry-run        report what would change without transferring anything
  --gitignore      skip files matched by .gitignore on both sides (default true)
  --retry DUR      keep retrying for this long if the peer is offline (default 1m; 0 = once)

CONFIG
  --config PATH  read settings from a JSON file (default config.json), using the
                 same schema as relay-tunnel. serve reads the "expose" section;
                 push and pull read the "connect" section. Command-line flags
                 take priority over config values.
  --example-config  print a sample config.json to stdout
`)
}

// resolveEndpoint applies the shared relay-tunnel config schema: it takes the
// server URL and session code from the named section ("expose" for the host,
// "connect" for the client) unless the corresponding flag was set, which wins.
func resolveEndpoint(fs *flag.FlagSet, configPath, section string, flagServer, flagCode string) (server, code string) {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	cfg, err := config.Load(configPath, set["config"])
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	var secServer, secCode string
	switch section {
	case "connect":
		secServer, secCode = cfg.Connect.Server, cfg.Connect.Code
	default: // "expose"
		secServer, secCode = cfg.Expose.Server, cfg.Expose.Code
	}
	server = flagServer
	if !set["server"] && secServer != "" {
		server = secServer
	}
	code = flagCode
	if !set["code"] && secCode != "" {
		code = secCode
	}
	return server, code
}

// parsePositional parses flags that may be interspersed with positional
// arguments (Go's flag package otherwise stops at the first positional), and
// returns the positional arguments in order.
func parsePositional(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		fs.Parse(args)
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	return pos
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath, "path to JSON config file (relay-tunnel schema)")
	server := fs.String("server", "", "rendezvous server URL, e.g. ws://HOST:7000/signal")
	code := fs.String("code", "", "session code")
	rest := parsePositional(fs, args)

	srv, sessionCode := resolveEndpoint(fs, *configPath, "expose", *server, *code)
	if srv == "" || sessionCode == "" {
		log.Fatal("--server and --code are required (flag or config [expose] section)")
	}
	if len(rest) != 1 {
		log.Fatal("usage: relay-sync serve --server URL --code CODE DIR")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := serve(ctx, srv, sessionCode, rest[0]); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

func cmdSync(direction string, args []string) {
	fs := flag.NewFlagSet(direction, flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath, "path to JSON config file (relay-tunnel schema)")
	server := fs.String("server", "", "rendezvous server URL, e.g. ws://HOST:7000/signal")
	code := fs.String("code", "", "session code")
	del := fs.Bool("delete", false, "delete extraneous files at the destination")
	dryRun := fs.Bool("dry-run", false, "preview changes without transferring")
	gitignore := fs.Bool("gitignore", true, "skip files matched by .gitignore on both sides")
	retry := fs.Duration("retry", time.Minute, "keep retrying for this long if the peer is offline (0 = single attempt)")
	rest := parsePositional(fs, args)

	srv, sessionCode := resolveEndpoint(fs, *configPath, "connect", *server, *code)
	if srv == "" || sessionCode == "" {
		log.Fatal("--server and --code are required (flag or config [connect] section)")
	}
	if len(rest) != 2 {
		log.Fatalf("usage: relay-sync %s --server URL --code CODE SRC DST", direction)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := syncOpts{del: *del, dryRun: *dryRun, gitignore: *gitignore}

	// One full pass: establish a connection and run the sync. Because the sync
	// compares manifests, re-running a pass after an interruption resumes
	// correctly, transferring only what is still missing or changed.
	pass := func(ctx context.Context) error {
		conn, err := dialClient(ctx, srv, sessionCode)
		if err != nil {
			return err
		}
		defer conn.CloseWithError(0, "bye")
		switch direction {
		case "push":
			return push(ctx, conn, rest[0], rest[1], opts) // push LOCAL REMOTE
		default:
			return pull(ctx, conn, rest[0], rest[1], opts) // pull REMOTE LOCAL
		}
	}

	if err := runWithRetry(ctx, *retry, pass); err != nil {
		log.Fatalf("%s: %v", direction, err)
	}
	log.Printf("%s complete", direction)
}

// runWithRetry runs fn, retrying with exponential backoff for up to the given
// window if it fails (for example because the peer is temporarily offline). A
// window of 0 means a single attempt. It stops early when ctx is cancelled.
func runWithRetry(ctx context.Context, window time.Duration, fn func(context.Context) error) error {
	deadline := time.Now().Add(window)
	backoff := time.Second
	const maxBackoff = 15 * time.Second
	for {
		err := fn(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		if window == 0 || time.Now().After(deadline) {
			return err
		}
		log.Printf("attempt failed: %v (retrying in %s)", err, backoff)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

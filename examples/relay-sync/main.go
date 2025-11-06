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
	case "serve":
		cmdServe(os.Args[2:])
	case "push", "pull":
		cmdSync(os.Args[1], os.Args[2:])
	case "example-config", "-example-config", "--example-config":
		fmt.Print(config.Example())
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `relay-sync - one-way directory sync over relay-tunnel

USAGE
  relay-sync serve --server ws://HOST:7000/signal --code CODE DIR
  relay-sync push  --server ws://HOST:7000/signal --code CODE LOCAL REMOTE [--delete] [--dry-run]
  relay-sync pull  --server ws://HOST:7000/signal --code CODE REMOTE LOCAL [--delete] [--dry-run]
  relay-sync --example-config

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

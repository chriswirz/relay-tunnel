// Command relay-tunnel connects two clients directly (peer-to-peer) with the
// help of a lightweight rendezvous server for signaling and NAT traversal.
//
// Subcommands:
//
//	relay-tunnel server   [--http :7000] [--udp :7001] [--public-udp host:7001]
//	relay-tunnel expose   --server ws://HOST:7000/signal [--code CODE] [policy flags]
//	relay-tunnel connect  --server ws://HOST:7000/signal --code CODE [actions]
//
// The `expose` side is the machine you want to reach; `connect` is you.
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base32"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/chriswirz/relay-tunnel/internal/app"
	"github.com/chriswirz/relay-tunnel/internal/config"
	sig "github.com/chriswirz/relay-tunnel/internal/signal"
	"github.com/chriswirz/relay-tunnel/internal/transport"
	"github.com/chriswirz/relay-tunnel/internal/xlog"
)

// Build metadata, injected via -ldflags by GoReleaser/Makefile.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	// With no subcommand, run whichever config sections are enabled.
	if len(os.Args) < 2 {
		cmdAuto(nil)
		return
	}
	switch os.Args[1] {
	case "server":
		cmdServer(os.Args[2:])
	case "expose":
		cmdExpose(os.Args[2:])
	case "connect":
		cmdConnect(os.Args[2:])
	case "version", "-version", "--version":
		fmt.Printf("relay-tunnel %s (commit %s, built %s)\n", version, commit, date)
	case "example-config", "-example-config", "--example-config":
		fmt.Print(config.Example())
	case "-h", "--help", "help":
		usage()
	default:
		// A leading flag (e.g. --config) with no subcommand means config-driven
		// mode: run whichever sections are enabled.
		if strings.HasPrefix(os.Args[1], "-") {
			cmdAuto(os.Args[1:])
			return
		}
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `relay-tunnel — direct peer-to-peer tunnels with NAT traversal

USAGE
  relay-tunnel server   [--http :7000] [--udp :7001] [--public-udp HOST:7001]
  relay-tunnel expose   --server ws://HOST:7000/signal [--code CODE] [policy]
  relay-tunnel connect  --server ws://HOST:7000/signal --code CODE [actions]
  relay-tunnel [--config PATH]   run every enabled section from the config file
  relay-tunnel version
  relay-tunnel --example-config

CONFIG
  --config PATH           read settings from a JSON file (default config.json)
  --example-config        print a sample config.json to stdout
  Command-line flags always take priority over config file values.
  With no subcommand, each config section whose "enabled" is true is started.

EXPOSE POLICY
  --allow-dial            allow the client to open TCP connections through this host
  --allow host:port       whitelist a specific dial target (repeatable)
  --allow-exec            allow remote command execution
  --file-root DIR         allow file get/put rooted at DIR

CONNECT ACTIONS
  -L [laddr:]lport:rhost:rport   local port forward (repeatable)
  -D [laddr:]lport               local SOCKS5 proxy through the peer
  get   REMOTE LOCAL             download a file from the host
  put   LOCAL REMOTE             upload a file to the host
  exec  -- CMD [ARGS...]         run a command on the host
`)
}

// stringSlice collects repeatable flags.
type stringSlice []string

func (s *stringSlice) String() string     { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error { *s = append(*s, v); return nil }

func newCode() string {
	b := make([]byte, 10)
	rand.Read(b)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

// loadConfig parses --config from an already-parsed flag set and loads it.
// A missing default-path file is not an error; a missing explicitly-requested
// one is.
func loadConfig(fs *flag.FlagSet, configPath string) (*config.Config, map[string]bool) {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	cfg, err := config.Load(configPath, set["config"])
	if err != nil {
		xlog.Fatalf("config: %v", err)
	}
	return cfg, set
}

// Merge helpers: a command-line flag wins if it was set; otherwise the config
// value is used when present; otherwise the flag's built-in default stands.
func pickStr(set map[string]bool, name, flagVal, cfgVal string) string {
	if set[name] || cfgVal == "" {
		return flagVal
	}
	return cfgVal
}

func pickBool(set map[string]bool, name string, flagVal, cfgVal bool) bool {
	if set[name] {
		return flagVal
	}
	return flagVal || cfgVal
}

func pickSlice(set map[string]bool, name string, flagVal, cfgVal []string) []string {
	if set[name] || len(cfgVal) == 0 {
		return flagVal
	}
	return cfgVal
}

func cmdServer(args []string) {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath, "path to JSON config file")
	http := fs.String("http", ":7000", "WebSocket signaling listen address")
	udp := fs.String("udp", ":7001", "UDP reflection/relay listen address")
	pub := fs.String("public-udp", "", "public UDP address to advertise (host:port); default derived from request")
	v := fs.Bool("v", false, "verbose logging")
	fs.Parse(args)

	cfg, set := loadConfig(fs, *configPath)
	httpAddr := pickStr(set, "http", *http, cfg.Server.HTTP)
	udpAddr := pickStr(set, "udp", *udp, cfg.Server.UDP)
	publicUDP := pickStr(set, "public-udp", *pub, cfg.Server.PublicUDP)
	xlog.SetVerbose(pickBool(set, "v", *v, cfg.Server.Verbose))

	if err := runServer(httpAddr, udpAddr, publicUDP); err != nil {
		xlog.Fatalf("server: %v", err)
	}
}

// runServer starts the rendezvous server and blocks.
func runServer(httpAddr, udpAddr, publicUDP string) error {
	s := sig.NewServer(sig.ServerConfig{HTTPAddr: httpAddr, UDPAddr: udpAddr, PublicUDP: publicUDP})
	return s.Run()
}

func cmdExpose(args []string) {
	fs := flag.NewFlagSet("expose", flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath, "path to JSON config file")
	server := fs.String("server", "", "signaling server URL, e.g. ws://HOST:7000/signal")
	code := fs.String("code", "", "session code (generated if empty)")
	allowDial := fs.Bool("allow-dial", false, "allow client to open TCP connections through this host")
	allowExec := fs.Bool("allow-exec", false, "allow remote command execution")
	fileRoot := fs.String("file-root", "", "allow file get/put rooted at this directory")
	var allow stringSlice
	fs.Var(&allow, "allow", "whitelist a specific dial target host:port (repeatable)")
	v := fs.Bool("v", false, "verbose logging")
	fs.Parse(args)

	cfg, set := loadConfig(fs, *configPath)
	ex := cfg.Expose
	srv := pickStr(set, "server", *server, ex.Server)
	allowList := pickSlice(set, "allow", allow, ex.Allow)
	pol := app.HostPolicy{
		AllowDial: pickBool(set, "allow-dial", *allowDial, ex.AllowDial) || len(allowList) > 0,
		Allow:     allowList,
		AllowExec: pickBool(set, "allow-exec", *allowExec, ex.AllowExec),
		FileRoot:  pickStr(set, "file-root", *fileRoot, ex.FileRoot),
	}
	xlog.SetVerbose(pickBool(set, "v", *v, ex.Verbose))

	srvCode := pickStr(set, "code", *code, ex.Code)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runExpose(ctx, srv, srvCode, pol); err != nil {
		xlog.Fatalf("%v", err)
	}
}

// runExpose hosts the exposed services until ctx is cancelled. If code is empty
// a random one is generated. It re-registers after each client disconnects so
// the endpoint stays available for the next connection.
func runExpose(ctx context.Context, server, code string, pol app.HostPolicy) error {
	if server == "" {
		return fmt.Errorf("expose: server is required (flag or config)")
	}
	if code == "" {
		code = newCode()
	}
	cert, fp, err := transport.GenerateCert()
	if err != nil {
		return fmt.Errorf("cert: %w", err)
	}

	fmt.Printf("\n  relay-tunnel session code:  %s\n", code)
	fmt.Printf("  share it, then run:  relay-tunnel connect --server %s --code %s ...\n\n", server, code)

	// Persist across sessions: each client `connect` is a fresh rendezvous, so
	// after one disconnects we re-register and wait for the next.
	for ctx.Err() == nil {
		if err := hostOnce(ctx, server, code, cert, fp, pol); err != nil {
			if ctx.Err() != nil {
				break
			}
			xlog.Infof("session ended: %v (waiting for next client)", err)
			// Back off briefly so a persistently failing rendezvous (e.g. the
			// server not up yet) does not spin.
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
	}
	return nil
}

// hostOnce performs one rendezvous + serve cycle.
func hostOnce(ctx context.Context, server, code string, cert tls.Certificate, fp string, pol app.HostPolicy) error {
	sess, err := sig.Dial(sig.DialParams{
		ServerURL:       server,
		Code:            code,
		Role:            sig.RoleHost,
		CertFingerprint: fp,
	})
	if err != nil {
		return err
	}
	if sess.Relayed {
		xlog.Infof("using server relay (direct path unavailable)")
	}
	conn, err := transport.Listen(ctx, sess, cert)
	if err != nil {
		return err
	}
	return app.ServeHost(ctx, conn, pol)
}

func cmdConnect(args []string) {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath, "path to JSON config file")
	server := fs.String("server", "", "signaling server URL, e.g. ws://HOST:7000/signal")
	code := fs.String("code", "", "session code (from the expose side)")
	var forwards stringSlice
	var socks stringSlice
	fs.Var(&forwards, "L", "local forward [laddr:]lport:rhost:rport (repeatable)")
	fs.Var(&socks, "D", "local SOCKS5 proxy [laddr:]lport (repeatable)")
	v := fs.Bool("v", false, "verbose logging")
	fs.Parse(args)

	cfg, set := loadConfig(fs, *configPath)
	cc := cfg.Connect
	srv := pickStr(set, "server", *server, cc.Server)
	sessionCode := pickStr(set, "code", *code, cc.Code)
	fwdList := pickSlice(set, "L", forwards, cc.Forward)
	socksList := pickSlice(set, "D", socks, cc.Socks)
	xlog.SetVerbose(pickBool(set, "v", *v, cc.Verbose))

	if srv == "" || sessionCode == "" {
		xlog.Fatalf("--server and --code are required (flag or config)")
	}
	rest := fs.Args()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// One-shot commands dial their own connection and exit when done.
	if len(rest) > 0 {
		sess, err := sig.Dial(sig.DialParams{ServerURL: srv, Code: sessionCode, Role: sig.RoleClient})
		if err != nil {
			xlog.Fatalf("rendezvous: %v", err)
		}
		if sess.Relayed {
			xlog.Infof("using server relay (direct path unavailable)")
		}
		conn, err := transport.Dial(ctx, sess)
		if err != nil {
			xlog.Fatalf("quic dial: %v", err)
		}
		defer conn.CloseWithError(0, "bye")

		switch rest[0] {
		case "get":
			if len(rest) != 3 {
				xlog.Fatalf("usage: get REMOTE LOCAL")
			}
			if err := app.FileGet(ctx, conn, rest[1], rest[2]); err != nil {
				xlog.Fatalf("get: %v", err)
			}
		case "put":
			if len(rest) != 3 {
				xlog.Fatalf("usage: put LOCAL REMOTE")
			}
			if err := app.FilePut(ctx, conn, rest[1], rest[2]); err != nil {
				xlog.Fatalf("put: %v", err)
			}
		case "exec":
			cmdArgs := rest[1:]
			if len(cmdArgs) > 0 && cmdArgs[0] == "--" {
				cmdArgs = cmdArgs[1:]
			}
			if len(cmdArgs) == 0 {
				xlog.Fatalf("usage: exec -- CMD [ARGS...]")
			}
			if err := app.Exec(ctx, conn, cmdArgs); err != nil {
				xlog.Fatalf("exec: %v", err)
			}
		default:
			xlog.Fatalf("unknown action %q", rest[0])
		}
		return
	}

	if err := runConnect(ctx, srv, sessionCode, fwdList, socksList); err != nil {
		xlog.Fatalf("%v", err)
	}
}

// cmdAuto runs whichever config sections are enabled. It is used when the binary
// is started with no subcommand. Multiple enabled sections run concurrently.
func cmdAuto(args []string) {
	fs := flag.NewFlagSet("relay-tunnel", flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath, "path to JSON config file")
	v := fs.Bool("v", false, "verbose logging")
	fs.Parse(args)

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	cfg, err := config.Load(*configPath, set["config"])
	if err != nil {
		xlog.Fatalf("config: %v", err)
	}

	verbose := *v ||
		(cfg.Server.Enabled && cfg.Server.Verbose) ||
		(cfg.Expose.Enabled && cfg.Expose.Verbose) ||
		(cfg.Connect.Enabled && cfg.Connect.Verbose)
	xlog.SetVerbose(verbose)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	type mode struct {
		name string
		run  func() error
	}
	var modes []mode

	if cfg.Server.Enabled {
		sc := cfg.Server
		modes = append(modes, mode{"server", func() error {
			return runServer(orDefault(sc.HTTP, ":7000"), orDefault(sc.UDP, ":7001"), sc.PublicUDP)
		}})
	}
	if cfg.Expose.Enabled {
		ex := cfg.Expose
		pol := app.HostPolicy{
			AllowDial: ex.AllowDial || len(ex.Allow) > 0,
			Allow:     ex.Allow,
			AllowExec: ex.AllowExec,
			FileRoot:  ex.FileRoot,
		}
		modes = append(modes, mode{"expose", func() error {
			return runExpose(ctx, ex.Server, ex.Code, pol)
		}})
	}
	if cfg.Connect.Enabled {
		cc := cfg.Connect
		modes = append(modes, mode{"connect", func() error {
			return runConnect(ctx, cc.Server, cc.Code, cc.Forward, cc.Socks)
		}})
	}

	if len(modes) == 0 {
		xlog.Errorf("no subcommand given and no enabled section in %s", *configPath)
		fmt.Fprintln(os.Stderr, "Enable a section (\"enabled\": true) or run a subcommand. See --help.")
		os.Exit(2)
	}

	names := make([]string, len(modes))
	for i, m := range modes {
		names[i] = m.name
	}
	xlog.Infof("starting enabled mode(s) from %s: %s", *configPath, strings.Join(names, ", "))

	errc := make(chan error, len(modes))
	for _, m := range modes {
		m := m
		go func() {
			if err := m.run(); err != nil {
				errc <- fmt.Errorf("%s: %w", m.name, err)
			}
		}()
	}

	// Return (and exit) on the first fatal error or on a shutdown signal. The
	// ctx-aware modes wind down on their own; the process exit stops the rest.
	select {
	case <-ctx.Done():
	case err := <-errc:
		if err != nil {
			xlog.Errorf("%v", err)
		}
	}
}

// orDefault returns v, or d when v is empty.
func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// runConnect establishes the tunnel and runs the requested local forwards and
// SOCKS proxies until ctx is cancelled or one of them errors.
func runConnect(ctx context.Context, server, code string, forwards, socks []string) error {
	if server == "" || code == "" {
		return fmt.Errorf("connect: server and code are required (flag or config)")
	}
	if len(forwards) == 0 && len(socks) == 0 {
		return fmt.Errorf("connect: nothing to do (no -L forward or -D socks configured)")
	}
	sess, err := sig.Dial(sig.DialParams{ServerURL: server, Code: code, Role: sig.RoleClient})
	if err != nil {
		return fmt.Errorf("rendezvous: %w", err)
	}
	if sess.Relayed {
		xlog.Infof("using server relay (direct path unavailable)")
	}
	conn, err := transport.Dial(ctx, sess)
	if err != nil {
		return fmt.Errorf("quic dial: %w", err)
	}
	// Close the QUIC connection on exit so the host can promptly serve the next
	// client instead of waiting out the idle timeout.
	defer conn.CloseWithError(0, "bye")

	errc := make(chan error, len(forwards)+len(socks))
	for _, spec := range forwards {
		go func(s string) { errc <- app.LocalForward(ctx, conn, s) }(spec)
	}
	for _, spec := range socks {
		go func(s string) { errc <- app.SOCKS(ctx, conn, s) }(spec)
	}
	xlog.Infof("tunnels running; press Ctrl+C to stop")
	select {
	case <-ctx.Done():
		return nil
	case err := <-errc:
		return err
	}
}

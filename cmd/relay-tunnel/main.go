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
	"github.com/chriswirz/relay-tunnel/internal/webui"
	"github.com/chriswirz/relay-tunnel/internal/xlog"
	"github.com/quic-go/quic-go"
)

// Build metadata, injected via -ldflags by GoReleaser/Makefile.
var (
	version = "dev"
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
	case "update", "-update", "--update":
		cmdUpdate(os.Args[2:])
	case "-update-version", "--update-version":
		// The value form, so `--update-version v0.1.0042` works as a bare flag.
		cmdUpdate(append([]string{"--version"}, os.Args[2:]...))
	case "version", "-version", "--version":
		fmt.Printf("relay-tunnel %s\n", version)
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
  relay-tunnel server   [--http :7000] [--udp :7001] [--public-udp HOST:7001] [--public-url URL] [--admin=false]
  relay-tunnel expose   --server ws://HOST:7000/signal [--code CODE] [policy]
  relay-tunnel connect  --server ws://HOST:7000/signal --code CODE [actions]
  relay-tunnel [--config PATH]   run every enabled section from the config file
  relay-tunnel update [--version TAG]
  relay-tunnel version
  relay-tunnel --example-config

SERVER
  --admin=false           serve signaling alone, with no web interface and no admin API
  --name NAME             label this server in its own admin interface
  The web interface, on the signaling listener, shows the sessions and relay
  routes this server is carrying. It signs in as a single administrator, whose
  password is stored hashed in the config file; a server with none set accepts
  admin/admin once and then demands a new one.

UPDATE
  --update                download the latest release, verify it against the
                          release's SHA256SUMS and replace this binary in place
  --update-version TAG    update to that release tag instead of the latest
  The running binary is replaced only after its checksum matches, and the old
  one is kept alongside until the replacement is in place. A system-wide install
  needs the privileges of the directory it lives in.

CONFIG
  --config PATH           read settings from a JSON file (default config.json)
  --example-config        print a sample config.json to stdout
  Command-line flags always take priority over config file values.
  With no subcommand, each config section whose "enabled" is true is started.

IDENTIFYING A PEER
  --name NAME             label this peer in the server's admin interface, so an
                          operator can tell two machines on one code apart. It is
                          optional, unverified, and used for nothing else.

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
	pubURL := fs.String("public-url", "", "externally reachable signaling URL to log at startup (e.g. wss://host/signal)")
	admin := fs.Bool("admin", true, "serve the admin web interface and its API on the signaling listener")
	name := fs.String("name", "", "label for this server, shown in the admin interface")
	v := fs.Bool("v", false, "verbose logging")
	fs.Parse(args)

	cfg, set := loadConfig(fs, *configPath)
	httpAddr := pickStr(set, "http", *http, cfg.Server.HTTP)
	udpAddr := pickStr(set, "udp", *udp, cfg.Server.UDP)
	publicUDP := pickStr(set, "public-udp", *pub, cfg.Server.PublicUDP)
	publicURL := pickStr(set, "public-url", *pubURL, cfg.Server.PublicURL)
	serverName := pickStr(set, "name", *name, cfg.Server.Name)
	xlog.SetVerbose(pickBool(set, "v", *v, cfg.Server.Verbose))

	// --admin is a way to turn the interface off from the command line; the
	// config file says the same thing the other way round, as "disabled".
	adminCfg := cfg.Server.Admin
	if set["admin"] && !*admin {
		adminCfg.Disabled = true
	}

	if err := runServer(httpAddr, udpAddr, publicUDP, publicURL, serverName, adminCfg, *configPath); err != nil {
		xlog.Fatalf("server: %v", err)
	}
}

// runServer starts the rendezvous server and blocks.
func runServer(httpAddr, udpAddr, publicUDP, publicURL, name string, admin config.AdminSection, configPath string) error {
	s := sig.NewServer(sig.ServerConfig{
		HTTPAddr:  httpAddr,
		UDPAddr:   udpAddr,
		PublicUDP: publicUDP,
		PublicURL: publicURL,
		Name:      name,
		Version:   version,
		Admin: sig.AdminConfig{
			Disabled:              admin.Disabled,
			Username:              admin.Username,
			PasswordHash:          admin.PasswordHash,
			SessionTTL:            time.Duration(admin.SessionHours) * time.Hour,
			TrustForwardedHeaders: admin.TrustForwardedHeaders,
		},
		// An account change made in the browser is written back to the same
		// config file this server was started with, so it survives a restart.
		SaveAdmin: func(username, passwordHash string) error {
			return config.SaveAdmin(configPath, username, passwordHash)
		},
		UI: webui.New(),
	})
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
	name := fs.String("name", "", "label for this peer, shown in the server's admin interface")
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
	peerName := pickStr(set, "name", *name, ex.Name)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runExpose(ctx, srv, srvCode, peerName, pol); err != nil {
		xlog.Fatalf("%v", err)
	}
}

// runExpose hosts the exposed services until ctx is cancelled. If code is empty
// a random one is generated. It re-registers after each client disconnects so
// the endpoint stays available for the next connection.
func runExpose(ctx context.Context, server, code, name string, pol app.HostPolicy) error {
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
		err := hostOnce(ctx, server, code, name, cert, fp, pol)
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
	return nil
}

// hostOnce performs one rendezvous + serve cycle.
func hostOnce(ctx context.Context, server, code, name string, cert tls.Certificate, fp string, pol app.HostPolicy) error {
	sess, err := sig.Dial(sig.DialParams{
		ServerURL:       server,
		Code:            code,
		Name:            name,
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
	// Close gracefully on shutdown so the client sees the disconnect at once and
	// reconnects promptly (rather than waiting out the idle timeout).
	defer conn.CloseWithError(0, "host shutting down")
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
	name := fs.String("name", "", "label for this peer, shown in the server's admin interface")
	v := fs.Bool("v", false, "verbose logging")
	fs.Parse(args)

	cfg, set := loadConfig(fs, *configPath)
	cc := cfg.Connect
	srv := pickStr(set, "server", *server, cc.Server)
	sessionCode := pickStr(set, "code", *code, cc.Code)
	fwdList := pickSlice(set, "L", forwards, cc.Forward)
	socksList := pickSlice(set, "D", socks, cc.Socks)
	peerName := pickStr(set, "name", *name, cc.Name)
	xlog.SetVerbose(pickBool(set, "v", *v, cc.Verbose))

	if srv == "" || sessionCode == "" {
		xlog.Fatalf("--server and --code are required (flag or config)")
	}
	rest := fs.Args()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// One-shot commands dial their own connection and exit when done.
	if len(rest) > 0 {
		sess, err := sig.Dial(sig.DialParams{ServerURL: srv, Code: sessionCode, Name: peerName, Role: sig.RoleClient})
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

	if err := runConnect(ctx, srv, sessionCode, peerName, fwdList, socksList); err != nil {
		xlog.Fatalf("%v", err)
	}
}

// cmdAuto runs whichever config sections are enabled. It is used when the binary
// is started with no subcommand. Multiple enabled sections run concurrently.
// cmdUpdate replaces this binary with a release build. It is a subcommand
// rather than a flag on the others because it runs instead of a tunnel, not
// alongside one.
func cmdUpdate(args []string) {
	fs := flag.NewFlagSet("relay-tunnel update", flag.ExitOnError)
	tag := fs.String("version", "", "release tag to install (default: the latest release)")
	fs.Parse(args)
	if err := selfUpdate(os.Stdout, *tag); err != nil {
		xlog.Fatalf("update: %v", err)
	}
}

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
		path := *configPath
		modes = append(modes, mode{"server", func() error {
			return runServer(orDefault(sc.HTTP, ":7000"), orDefault(sc.UDP, ":7001"), sc.PublicUDP, sc.PublicURL, sc.Name, sc.Admin, path)
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
			return runExpose(ctx, ex.Server, ex.Code, ex.Name, pol)
		}})
	}
	if cfg.Connect.Enabled {
		cc := cfg.Connect
		modes = append(modes, mode{"connect", func() error {
			return runConnect(ctx, cc.Server, cc.Code, cc.Name, cc.Forward, cc.Socks)
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

// runConnect runs the requested local forwards and SOCKS proxies until ctx is
// cancelled or a listener fails. The underlying tunnel is maintained by a
// ConnManager that reconnects automatically, so the listeners survive the peer,
// the network, or the rendezvous server going offline and coming back.
func runConnect(ctx context.Context, server, code, name string, forwards, socks []string) error {
	if server == "" || code == "" {
		return fmt.Errorf("connect: server and code are required (flag or config)")
	}
	if len(forwards) == 0 && len(socks) == 0 {
		return fmt.Errorf("connect: nothing to do (no -L forward or -D socks configured)")
	}

	dial := func(ctx context.Context) (*quic.Conn, error) {
		sess, err := sig.Dial(sig.DialParams{ServerURL: server, Code: code, Name: name, Role: sig.RoleClient})
		if err != nil {
			return nil, fmt.Errorf("rendezvous: %w", err)
		}
		if sess.Relayed {
			xlog.Infof("using server relay (direct path unavailable)")
		}
		conn, err := transport.Dial(ctx, sess)
		if err != nil {
			return nil, fmt.Errorf("quic dial: %w", err)
		}
		return conn, nil
	}
	mgr := app.NewConnManager(dial)
	go mgr.Run(ctx)

	errc := make(chan error, len(forwards)+len(socks))
	for _, spec := range forwards {
		go func(s string) { errc <- app.LocalForward(ctx, mgr, s) }(spec)
	}
	for _, spec := range socks {
		go func(s string) { errc <- app.SOCKS(ctx, mgr, s) }(spec)
	}
	xlog.Infof("tunnels running; press Ctrl+C to stop")
	select {
	case <-ctx.Done():
		return nil
	case err := <-errc:
		return err
	}
}

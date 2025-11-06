# relay-tunnel

[![CI](https://github.com/chriswirz/relay-tunnel/actions/workflows/ci.yml/badge.svg)](https://github.com/chriswirz/relay-tunnel/actions/workflows/ci.yml)
[![Lint](https://github.com/chriswirz/relay-tunnel/actions/workflows/lint.yml/badge.svg)](https://github.com/chriswirz/relay-tunnel/actions/workflows/lint.yml)
[![CodeQL](https://github.com/chriswirz/relay-tunnel/actions/workflows/codeql.yml/badge.svg)](https://github.com/chriswirz/relay-tunnel/actions/workflows/codeql.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/chriswirz/relay-tunnel)](https://goreportcard.com/report/github.com/chriswirz/relay-tunnel)

**relay-tunnel** connects two machines directly, peer-to-peer, without manually opening any ports.
A tiny rendezvous server helps the two peers find each other and punch through their NATs.
Once connected, your traffic flows straight between the peers, so the server is not an exit node.

Think of it like `http-tunnel`, except relay-tunnel establishes a real P2P link instead of relaying every byte through the server.
The server only relays data as a last-resort fallback when a direct path is impossible, for example symmetric NAT or CGNAT on both ends.

```mermaid
flowchart TB
    server["rendezvous server<br/>ws :7000 / udp :7001<br/>signaling + STUN-like reflection + relay fallback"]
    expose["expose<br/>(host A)"]
    connect["connect<br/>(client B)"]

    server -.->|"signaling / punch"| expose
    server -.->|"signaling / punch"| connect
    expose <==>|"direct QUIC, encrypted P2P tunnel"| connect
```

## Features

- NAT traversal via UDP hole punching, with automatic server-relay fallback when a direct path cannot be established.
- Encrypted and authenticated transport using QUIC (TLS 1.3), with the host's ephemeral certificate fingerprint pinned through the signaling channel so a compromised relay cannot impersonate the host.
- Multiplexed, so one connection carries many concurrent streams.
- TCP port forwarding (`-L`), like SSH local forwards.
- SOCKS5 proxy (`-D`) to reach arbitrary destinations through the peer.
- File transfer (`get` and `put`), with the host's file access confined to a chosen root directory.
- Remote command execution (`exec`), opt-in on the host.
- Single static binary for Linux, macOS, and Windows.

## Install

```sh
go install github.com/chriswirz/relay-tunnel/cmd/relay-tunnel@latest
```

Or grab a prebuilt binary from the [Releases](../../releases) page, or build locally:

```sh
make build          # produces ./relay-tunnel
```

### Linux packages (.deb / .rpm)

Every tagged release publishes `.deb` and `.rpm` packages (amd64 and arm64), built with [nfpm](https://nfpm.goreleaser.com/) from [`packaging/nfpm.yaml`](packaging/nfpm.yaml).

```sh
sudo dpkg -i relay-tunnel_*_amd64.deb     # Debian/Ubuntu
sudo rpm -i  relay-tunnel-*.x86_64.rpm    # Fedora/RHEL/openSUSE
```

The package installs the binary to `/usr/bin/relay-tunnel` and ships a systemd unit for the rendezvous server (not auto-enabled).

```sh
sudo nano /etc/relay-tunnel/server.env          # set ports and --public-udp
sudo systemctl enable --now relay-tunnel-server
```

## Quick start

### 1. Run a rendezvous server (once, somewhere both peers can reach)

```sh
relay-tunnel server --http :7000 --udp :7001
# Behind NAT/load-balancer, advertise the reachable UDP address:
relay-tunnel server --public-udp your.host.example:7001
```

The server needs its TCP (signaling) and UDP (reflection/relay) ports reachable.
It never terminates your tunnel traffic unless relay fallback kicks in.

### 2. On the machine you want to reach (`expose`)

```sh
relay-tunnel expose --server ws://your.host.example:7000/signal --allow-dial
```

This prints a session code.
Share it with the other side out-of-band.

### 3. On your machine (`connect`)

```sh
# Forward local :9000 to the host's 127.0.0.1:8080
relay-tunnel connect --server ws://your.host.example:7000/signal --code CODE \
  -L 9000:127.0.0.1:8080
```

Now `http://localhost:9000` on your machine hits `127.0.0.1:8080` on the host, directly and peer-to-peer.

## Usage

```
relay-tunnel server   [--http :7000] [--udp :7001] [--public-udp HOST:7001]
relay-tunnel expose   --server ws://HOST:7000/signal [--code CODE] [policy]
relay-tunnel connect  --server ws://HOST:7000/signal --code CODE [actions]
relay-tunnel version
```

### Session code (`--code`)

The session code is the shared secret that pairs the two peers on the rendezvous server.
Both sides must present the same code, and the server only pairs a `host` (`expose`) with a `client` (`connect`) that carry an identical code.
On `expose`, `--code CODE` sets the code explicitly, and if you omit it a random code is generated and printed for you to share.
On `connect`, `--code CODE` is required and must match the code from the `expose` side.
Share the code over a trusted channel and treat it as a secret, because anyone who has it (and can reach the server) can pair with your host, subject to the host's policy flags.

### `expose` policy flags

The host is deny-by-default.
Grant exactly what you need.

| Flag                | Effect                                                        |
|---------------------|--------------------------------------------------------------|
| `--allow-dial`      | Let the client open TCP connections through this host (`-L`, `-D`). |
| `--allow host:port` | Whitelist a single dial target (repeatable). Implies dialing. |
| `--allow-exec`      | Allow remote command execution (`exec`).                     |
| `--file-root DIR`   | Allow file `get`/`put`, confined to `DIR`.                   |

### `connect` actions

| Action                              | Description                              |
|-------------------------------------|------------------------------------------|
| `-L [laddr:]lport:rhost:rport`      | Local port forward (repeatable).         |
| `-D [laddr:]lport`                  | Local SOCKS5 proxy through the peer.      |
| `get REMOTE LOCAL`                  | Download a file from the host.           |
| `put LOCAL REMOTE`                  | Upload a file to the host.               |
| `exec -- CMD [ARGS...]`             | Run a command on the host.               |

#### Local port forward (`-L`) vs SOCKS5 proxy (`-D`)

A local port forward has a single fixed destination that you choose when you start it.
With `-L 9000:127.0.0.1:8080`, every connection to local port 9000 is sent to `127.0.0.1:8080` as seen from the host, and nothing else.
Use `-L` when you want to reach one specific service through the peer.

A SOCKS5 proxy has no fixed destination; the destination is chosen per-connection by the client application.
With `-D 1080`, any SOCKS5-aware program (a browser, `curl --socks5`, and so on) can ask the proxy for any host and port, and the host dials it on demand.
Use `-D` when you want general, on-demand access to many destinations reachable from the peer, similar to a lightweight VPN for TCP.

In short, `-L` is one preset tunnel to one address, while `-D` is a dynamic proxy that opens a new tunnel to whatever address each request names.
Both require the host to permit dialing (`--allow-dial`, or an `--allow` entry that matches the target).

### Examples

```sh
# SOCKS5 proxy: browse the host's network from your machine
relay-tunnel connect --server ws://SRV:7000/signal --code CODE -D 1080

# Multiple forwards at once
relay-tunnel connect --server ws://SRV:7000/signal --code CODE \
  -L 8000:127.0.0.1:80 -L 5432:db.internal:5432

# File transfer (host started with --file-root /srv/share)
relay-tunnel connect --server ws://SRV:7000/signal --code CODE get report.pdf ./report.pdf
relay-tunnel connect --server ws://SRV:7000/signal --code CODE put ./patch.tar.gz incoming/patch.tar.gz

# Remote command (host started with --allow-exec)
relay-tunnel connect --server ws://SRV:7000/signal --code CODE exec -- uname -a
```

## Configuration file

Every subcommand can read its settings from a JSON config file, which is handy for keeping the session code and server URL nearby across re-runs.
Command-line flags always take priority, so a value in the file is used only when the matching flag is not given.
By default the tool looks for `config.json` in the current directory, and `--config PATH` points it at another file.
A missing default `config.json` is ignored, but a file named explicitly with `--config` must exist.

Print a fully populated sample to stdout and save it as a starting point.

```sh
relay-tunnel --example-config > config.json
```

The file has one section per subcommand: `server`, `expose`, and `connect`.
Each section mirrors that subcommand's flags, and each subcommand reads only its own section.
Empty or omitted fields never override a flag or a built-in default.

```json
{
  "server": {
    "enabled": false,
    "http": ":7000",
    "udp": ":7001",
    "public_udp": "your.host.example:7001",
    "verbose": false
  },
  "expose": {
    "enabled": false,
    "server": "ws://your.host.example:7000/signal",
    "code": "your-session-code",
    "allow_dial": true,
    "allow": ["127.0.0.1:8080"],
    "allow_exec": false,
    "file_root": "/srv/share",
    "verbose": false
  },
  "connect": {
    "enabled": false,
    "server": "ws://your.host.example:7000/signal",
    "code": "your-session-code",
    "forward": ["9000:127.0.0.1:8080"],
    "socks": ["1080"],
    "verbose": false
  }
}
```

The `server` field inside `expose` and `connect` is the signaling server URL those sides dial.
The top-level `server` section, by contrast, configures the rendezvous server subcommand itself.

### Running from config with `enabled`

Each section has an `enabled` flag.
When you run the binary with no subcommand, every section whose `enabled` is `true` is started, and the running mode uses that section's settings.

```sh
relay-tunnel                 # run every enabled section in config.json
relay-tunnel --config other.json
```

If more than one section is enabled, they run concurrently in the same process, which is useful for running the rendezvous server and an `expose` host together on one machine.
When you run an explicit subcommand, `enabled` is ignored and only that subcommand runs.
If no subcommand is given and no section is enabled, the tool prints an error and the usage help.

With that file present, the earlier three-terminal example becomes shorter.

```sh
relay-tunnel server                 # reads http/udp from config.json
relay-tunnel expose                 # reads server/code/policy from config.json
relay-tunnel connect                # reads server/code/forward from config.json
```

The config file may contain your session code, so treat it as a secret and keep it out of version control.

## How it works

1. Rendezvous.
   Both peers connect to the server over WebSocket with the same session code, and the server pairs them.
2. Reflection.
   Each peer sends a UDP probe to the server, which echoes back the peer's observed public `ip:port` (STUN-like).
3. Exchange.
   The server relays each peer's public address to the other, plus the host's TLS certificate fingerprint to the client.
4. Hole punch.
   Both peers simultaneously send UDP packets to each other's public address, opening their NAT mappings.
5. QUIC.
   If punching succeeds, the peers run QUIC directly over the punched socket, and the client pins the host's certificate fingerprint.
6. Fallback.
   If punching fails, both peers tunnel their (still QUIC-encrypted) datagrams through the server, which forwards opaque packets between them.
   Set `RT_FORCE_RELAY=1` to force this path.

## Security model

- All tunnel traffic is QUIC/TLS 1.3 encrypted end-to-end, including over the relay fallback, where the server sees only opaque ciphertext.
- The host authenticates via an ephemeral certificate whose fingerprint is pinned by the client through the signaling channel, and the session code gates who may pair.
- The host is deny-by-default: dialing, exec, and file access are each opt-in, and file access is path-confined to `--file-root`.
- Treat the session code as a secret and share it over a trusted channel.
- Run the signaling server over TLS (`wss://`, for example behind a reverse proxy) in production.

Note: `--allow-exec` grants remote code execution to whoever holds the session code.
Enable it only when you trust the other party.

## Development

```sh
make build      # build ./relay-tunnel
make test       # go test -race ./...
make vet        # go vet ./...
make fmt        # gofmt -w .
make lint       # golangci-lint run
```

You can also build with the helper scripts.

```sh
./build.sh              # host build into ./relay-tunnel
./build.sh --all        # cross-compile every release target into ./dist
./build.sh --test       # gofmt, go vet, and go test
```

```powershell
.\build.ps1             # host build into relay-tunnel.exe
.\build.ps1 -All        # cross-compile every release target into dist\
.\build.ps1 -Test       # gofmt, go vet, and go test
```

On Windows, `build.cmd` is a thin wrapper that runs `build.ps1` and accepts the same `--all` and `--test` arguments.

### Integration tests

A multi-process suite in [`test/integration`](test/integration) launches a real server, `expose`, and `connect` as separate processes and drives traffic through the tunnel.
It covers the direct path, relay fallback, `-L`, SOCKS, file transfer, and exec.
It is build-tagged so it does not run in the default unit-test pass.

```sh
go test -tags integration ./test/integration/...
```

CI runs it on Linux, macOS, and Windows.

### Layout

```
cmd/relay-tunnel      CLI entrypoint and subcommands
internal/signal       rendezvous protocol, server, and client
internal/nat          UDP reflection, hole punching, relay packet-conn
internal/transport    QUIC transport, cert generation and pinning
internal/mux          per-stream framing (tcp / file / exec)
internal/app          host serving plus client forwards/SOCKS/file/exec
internal/xlog         tiny leveled logger
```

## Roadmap

- WebRTC/ICE transport as an additional traversal path.
- Config file and persistent session identities.

## License

[MIT](LICENSE)

# relay-sync

relay-sync is an example application built on the relay-tunnel module.
It performs one-way directory synchronization (a mirror) between two machines over a direct peer-to-peer connection.

It uses relay-tunnel purely as a transport library.
The rendezvous and NAT traversal come from `internal/signal`, the authenticated QUIC connection comes from `internal/transport`, and relay-sync layers its own small sync protocol on top (see `proto.go`).
This is meant to show how to build a real application on the relay-tunnel module.

## How it works

One side hosts a directory and answers requests; the other side runs a single sync pass.

The client asks the host for a manifest of the target subtree (each file's relative path, size, and modification time).
It compares that manifest to the local tree and transfers only the files that are missing or differ in size or modification time.
With `--delete`, files that exist at the destination but not the source are removed.
All host file access is confined to the served root directory.

Empty directories are not synchronized; only files (and the directories needed to hold them) are transferred.

## Usage

First run the shared rendezvous server (the same one relay-tunnel uses).

```sh
relay-tunnel server --http :7000 --udp :7001
```

On the machine that holds the files, serve a directory.

```sh
relay-sync serve --server ws://HOST:7000/signal --code CODE /path/to/dir
```

On the other machine, mirror in either direction.
Remote paths are relative to the served directory.

```sh
# push: copy local -> remote
relay-sync push --server ws://HOST:7000/signal --code CODE ./local remote/sub

# pull: copy remote -> local
relay-sync pull --server ws://HOST:7000/signal --code CODE remote/sub ./local
```

Flags may appear before or after the positional arguments.

```sh
relay-sync push --server ws://HOST:7000/signal --code CODE ./local remote/sub --delete --dry-run
```

- `--delete` removes files at the destination that are absent from the source.
- `--dry-run` reports what would change without transferring anything.
- `--gitignore` skips files matched by `.gitignore` on both sides (on by default; pass `--gitignore=false` to sync everything).
- `--retry DURATION` keeps retrying if the peer is offline (default `1m`; `0` means a single attempt).

## Respecting .gitignore

By default relay-sync skips files that `.gitignore` masks, so build output, logs, and other ignored files are never transferred.
The filtering is applied on both sides: the client never sends an ignored file, and the host never offers one for deletion, so ignored files at either end are left untouched.

It reads `.gitignore` files throughout the synced tree, including nested ones, and understands the common pattern syntax: comments, `!` negation, a leading `/` to anchor to the file's directory, a trailing `/` for directories only, and the `*`, `?`, and `**` wildcards.
The `.git` directory is always skipped, regardless of this setting.
The synced directory is treated as the top of the tree, so `.gitignore` files above it are not consulted.

## Resuming after an outage

relay-sync resumes on its own when a system goes offline and comes back.
The `serve` host stays up and re-registers with the rendezvous server after each client disconnects.
A `push` or `pull` retries with exponential backoff for the `--retry` window if the peer is temporarily unreachable.
Because each pass compares manifests before transferring, re-running after an interruption resumes cleanly: files that already arrived are skipped, and only what is still missing or changed is sent.

## Configuration file

relay-sync reads the same `config.json` schema as relay-tunnel, so both tools can share one file.
Pass `--config PATH` to choose the file (the default is `config.json` in the current directory), and print a sample with `relay-sync --example-config`.

The server URL and session code come from the config sections that match each role.
`serve` reads the `expose` section (it is the host), and `push` and `pull` read the `connect` section (they are the client).
Command-line flags take priority over config values.

```json
{
  "server":  { "http": ":7000", "udp": ":7001" },
  "expose":  { "server": "ws://HOST:7000/signal", "code": "your-session-code" },
  "connect": { "server": "ws://HOST:7000/signal", "code": "your-session-code" }
}
```

With that file present, the commands need only their paths.

```sh
relay-tunnel server            # rendezvous server (reads the "server" section)
relay-sync serve ./dir         # reads server/code from the "expose" section
relay-sync push ./local remote/sub
relay-sync pull remote/sub ./local
```

Only the `server`, `code` (and, for the rendezvous server, `http`/`udp`/`public_udp`) fields are used; the other relay-tunnel fields are ignored by relay-sync.

## Build

```sh
go build -o relay-sync ./examples/relay-sync
```

## Notes and limitations

- Synchronization is one-way (a mirror), not bidirectional; there is no conflict resolution.
- Change detection uses size and modification time (whole-second resolution), like a lightweight rsync, not content hashing.
- The host stays up and serves repeated syncs; it re-registers with the rendezvous server after each client disconnects.

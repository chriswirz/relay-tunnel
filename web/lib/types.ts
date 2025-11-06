// Shapes returned by the relay-tunnel admin API.
// These mirror internal/signal/openapi.json; `npm run codegen` regenerates a
// full typed client from that document into lib/api.gen.ts when it is wanted.

export type Role = 'host' | 'client';

export interface Peer {
  role: Role;
  /** Peer-supplied label, absent when it gave none. Unverified: display only. */
  name?: string;
  token: string;
  ready: boolean;
  public_addr?: string;
  /** Addresses the peer reports for its own networks, raced alongside public_addr. */
  local_addrs?: string[];
  cert_fingerprint?: string;
  remote_addr?: string;
  joined_at: string;
}

export interface Session {
  code: string;
  created_at: string;
  paired: boolean;
  paired_at?: string;
  peers: Peer[];
}

export interface SessionList {
  sessions: Session[];
}

export interface Relay {
  token: string;
  peer_token?: string;
  code?: string;
  role?: Role;
  addr?: string;
  first_seen: string;
  last_seen: string;
  expires_at: string;
  packets: number;
  bytes: number;
}

export interface RelayList {
  relays: Relay[];
}

export interface Totals {
  peers_joined: number;
  pairs: number;
  reflections: number;
  relayed_packets: number;
  relayed_bytes: number;
  dropped_packets: number;
}

/**
 * Health of the reflection/relay socket. Signaling is proxied in most
 * deployments and can work while UDP never arrives, which peers can only report
 * as "reflection timed out"; `packets_received` is what separates probes that
 * never land from probes that land and fail.
 */
export interface Udp {
  bound_addr: string;
  advertised_addr?: string;
  advertised_derived: boolean;
  packets_received: number;
  invalid_packets: number;
  last_packet_at?: string;
  last_reflection_at?: string;
}

export interface Status {
  /** The operator's label for this server, absent when unset. */
  name?: string;
  version: string;
  started_at: string;
  uptime_seconds: number;
  http_addr: string;
  udp_addr: string;
  public_udp?: string;
  public_url?: string;
  sessions: number;
  sessions_paired: number;
  peers_connected: number;
  relays: number;
  relay_ttl_seconds: number;
  totals: Totals;
  udp: Udp;
}

/** The idle windows the server accepts for a bulk relay prune, shortest first. */
export const PRUNE_AGES = ['1m', '5m', '15m', '1h', '6h', '24h'] as const;
export type PruneAge = (typeof PRUNE_AGES)[number];

export interface PruneResult {
  idle_for: PruneAge;
  deleted: number;
  tokens: string[];
}

export interface AuthStatus {
  authenticated: boolean;
  username?: string;
  must_change_password: boolean;
  /** True while the server has no administrator password, so the login form can say so. */
  password_unset: boolean;
}

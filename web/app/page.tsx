'use client';

import * as React from 'react';
import { Alert, Card, CardContent, CircularProgress, Divider, Stack, Typography } from '@mui/material';
import Grid from '@mui/material/Grid2';
import AuthGate, { ApiErrorAlert } from '../components/AuthGate';
import SessionsTable from '../components/SessionsTable';
import RelaysTable from '../components/RelaysTable';
import { api } from '../lib/api';
import { usePoll } from '../lib/use-poll';
import { bytes, since, stamp, uptime } from '../lib/format';
import type { Relay, Session, Status } from '../lib/types';

function StatCard({ label, value, hint }: { label: string; value: React.ReactNode; hint?: string }) {
  return (
    <Card>
      <CardContent>
        <Typography variant="h4" sx={{ fontWeight: 600, fontVariantNumeric: 'tabular-nums' }}>
          {value}
        </Typography>
        <Typography variant="caption" color="text.secondary">
          {label}
        </Typography>
        {hint && (
          <Typography variant="caption" color="text.disabled" display="block">
            {hint}
          </Typography>
        )}
      </CardContent>
    </Card>
  );
}

/** The one line an operator needs when handing this server's address to someone. */
function ServerCard({ status }: { status: Status }) {
  const rows: [string, string][] = [
    ...(status.name ? ([['name', status.name]] as [string, string][]) : []),
    ['version', status.version],
    ['signaling', status.public_url || `${status.http_addr} (ws /signal)`],
    ['udp reflect/relay', status.public_udp || status.udp_addr],
    ['started', `${stamp(status.started_at)} (up ${uptime(status.uptime_seconds)})`],
  ];
  return (
    <Card>
      <CardContent>
        <Typography variant="h2" gutterBottom>
          {status.name || 'This server'}
        </Typography>
        <Stack spacing={0.6}>
          {rows.map(([label, value]) => (
            <Stack key={label} direction="row" spacing={2}>
              <Typography variant="body2" color="text.secondary" sx={{ minWidth: 150 }}>
                {label}
              </Typography>
              <Typography variant="body2" fontFamily="ui-monospace, monospace">
                {value}
              </Typography>
            </Stack>
          ))}
        </Stack>
      </CardContent>
    </Card>
  );
}

/**
 * How long a peer may sit unreflected before it is worth pointing at. A healthy
 * reflection completes in well under a second; nat.Reflect gives up at five.
 */
const STALLED_AFTER_MS = 10_000;

/**
 * The reflection socket is where a rendezvous fails silently. Signaling arrives
 * over the HTTP listener, which is typically proxied and therefore fine, while
 * UDP is deliberately not proxied and so has its own firewall, its own port and
 * its own ways of being unreachable. A peer that cannot reflect sees only
 * "reflection timed out" and cannot tell whether its probes arrived, so this
 * card answers that from the side that knows.
 */
function UdpCard({ status, sessions }: { status: Status; sessions: Session[] }) {
  const u = status.udp;

  // A peer that signaled but never reflected is the same failure seen from the
  // other end, and it names the code an operator should be looking at.
  const stalled = sessions.flatMap((sess) =>
    sess.peers
      .filter((p) => !p.ready && Date.now() - new Date(p.joined_at).getTime() > STALLED_AFTER_MS)
      .map((p) => `${sess.code} (${p.role})`),
  );

  const verdict = ((): { severity: 'error' | 'warning' | 'success'; text: React.ReactNode } => {
    if (u.packets_received === 0) {
      return {
        severity: 'error',
        text: (
          <>
            No UDP datagram has ever reached this server. Peers are being told to probe{' '}
            <code>{u.advertised_addr || '(underived)'}</code> while the socket listens on <code>{u.bound_addr}</code>.
            Every peer will report a reflection timeout until that path is open: check that the port is bound on a
            public interface rather than loopback, and that the host firewall and any cloud security group allow it.
            The reverse proxy is not involved - UDP must reach this process directly.
          </>
        ),
      };
    }
    if (!u.last_reflection_at) {
      return {
        severity: 'warning',
        text: (
          <>
            {u.packets_received} datagram(s) arrived but none was a valid reflection probe
            {u.invalid_packets > 0 && <> ({u.invalid_packets} discarded as not RT01)</>}. Something is reaching this
            port, but it is not a relay-tunnel peer.
          </>
        ),
      };
    }
    return {
      severity: 'success',
      text: (
        <>
          Last reflection answered {since(u.last_reflection_at)}, {u.packets_received} datagram(s) received.
          {stalled.length > 0 && (
            <>
              {' '}
              UDP is reachable in general, so a peer stuck here is likely blocked on its own side of the path.
            </>
          )}
        </>
      ),
    };
  })();

  const rows: [string, React.ReactNode][] = [
    ['bound', u.bound_addr],
    ['advertised to peers', `${u.advertised_addr || '-'}${u.advertised_derived ? ' (derived from Host)' : ''}`],
    ['datagrams received', `${u.packets_received}${u.invalid_packets > 0 ? ` (${u.invalid_packets} invalid)` : ''}`],
    ['reflections answered', String(status.totals.reflections)],
    ['last datagram', u.last_packet_at ? since(u.last_packet_at) : 'never'],
    ['last reflection', u.last_reflection_at ? since(u.last_reflection_at) : 'never'],
  ];

  return (
    <Card>
      <CardContent>
        <Typography variant="h2" gutterBottom>
          UDP reachability
        </Typography>
        <Alert severity={verdict.severity} sx={{ mb: 2 }}>
          {verdict.text}
        </Alert>
        {stalled.length > 0 && (
          <Alert severity="warning" sx={{ mb: 2 }}>
            Signaled but never reflected: {stalled.join(', ')}. A peer in this state holds its role on that code, so a
            reconnecting peer is refused with &quot;already connected&quot; until it is cleared - delete the session
            below to release it.
          </Alert>
        )}
        <Stack spacing={0.6}>
          {rows.map(([label, value]) => (
            <Stack key={label} direction="row" spacing={2}>
              <Typography variant="body2" color="text.secondary" sx={{ minWidth: 150 }}>
                {label}
              </Typography>
              <Typography variant="body2" fontFamily="ui-monospace, monospace">
                {value}
              </Typography>
            </Stack>
          ))}
        </Stack>
      </CardContent>
    </Card>
  );
}

function Overview() {
  const status = usePoll<Status>(React.useCallback(() => api.status(), []));
  const sessions = usePoll<{ sessions: Session[] }>(React.useCallback(() => api.listSessions(), []));
  const relays = usePoll<{ relays: Relay[] }>(React.useCallback(() => api.listRelays(), []));

  const error = status.error || sessions.error || relays.error;
  if (error) return <ApiErrorAlert error={error} />;
  if (!status.data || !sessions.data || !relays.data) return <CircularProgress size={22} />;

  const s = status.data;
  return (
    <Stack spacing={3}>
      <Grid container spacing={1.5}>
        <Grid size={{ xs: 6, sm: 3 }}>
          <StatCard label="sessions" value={s.sessions} hint={`${s.sessions_paired} paired`} />
        </Grid>
        <Grid size={{ xs: 6, sm: 3 }}>
          <StatCard label="peers signaling" value={s.peers_connected} />
        </Grid>
        <Grid size={{ xs: 6, sm: 3 }}>
          <StatCard label="relay routes" value={s.relays} hint={`${bytes(s.totals.relayed_bytes)} relayed`} />
        </Grid>
        <Grid size={{ xs: 6, sm: 3 }}>
          <StatCard label="rendezvous completed" value={s.totals.pairs} hint={`${s.totals.peers_joined} peers joined`} />
        </Grid>
      </Grid>

      <ServerCard status={s} />

      <UdpCard status={s} sessions={sessions.data.sessions} />

      <div>
        <Typography variant="h2" gutterBottom>
          Signaling now
        </Typography>
        <SessionsTable sessions={sessions.data.sessions} dense onDeleted={() => void sessions.refresh()} />
      </div>

      <Divider />

      <div>
        <Typography variant="h2" gutterBottom>
          Relayed traffic
        </Typography>
        <RelaysTable relays={relays.data.relays} dense onDeleted={() => void relays.refresh()} />
      </div>
    </Stack>
  );
}

export default function HomePage() {
  return (
    <Stack spacing={3}>
      <div>
        <Typography variant="h1">Overview</Typography>
        <Typography variant="body2" color="text.secondary" sx={{ maxWidth: 720 }}>
          The rendezvous server introduces two peers to each other and then gets out of the way. What it holds is
          short-lived: the sessions peers are signaling through, and the relay routes carrying datagrams for the pairs
          that could not punch a direct path.
        </Typography>
      </div>
      <AuthGate>
        <Overview />
      </AuthGate>
    </Stack>
  );
}

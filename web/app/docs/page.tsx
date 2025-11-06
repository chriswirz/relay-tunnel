'use client';

import * as React from 'react';
import Link from 'next/link';
import {
  Card,
  CardContent,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Typography,
} from '@mui/material';

// Prose and code share one measure, so the page reads as a single column rather
// than paragraphs indented inside wider slabs of code.
const COLUMN = 680;

function Code({ children }: { children: React.ReactNode }) {
  return (
    <Card sx={{ maxWidth: COLUMN }}>
      <CardContent
        component="pre"
        sx={{ m: 0, fontFamily: 'ui-monospace, monospace', fontSize: '0.82rem', overflowX: 'auto' }}
      >
        {children}
      </CardContent>
    </Card>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <Stack spacing={1.2}>
      <Typography variant="h2">{title}</Typography>
      {children}
    </Stack>
  );
}

function P({ children }: { children: React.ReactNode }) {
  return (
    <Typography variant="body2" color="text.secondary" sx={{ maxWidth: COLUMN }}>
      {children}
    </Typography>
  );
}

const ENDPOINTS: [string, string, string][] = [
  ['GET', '/api/v1/status', 'addresses, uptime, live counts and lifetime totals'],
  ['GET', '/api/v1/sessions', 'every rendezvous code with a peer on it'],
  ['GET', '/api/v1/sessions/{code}', 'one rendezvous'],
  ['DELETE', '/api/v1/sessions/{code}', 'close a rendezvous and reclaim its relay routes'],
  ['GET', '/api/v1/relays', 'every relay route this server is holding'],
  ['DELETE', '/api/v1/relays/{token}', 'drop a relayed pair'],
  ['DELETE', '/api/v1/relays?idle_for=15m', 'reclaim every route idle that long'],
  ['POST', '/api/v1/auth/login', 'sign in and set the session cookie'],
  ['POST', '/api/v1/auth/account', 'rename the account or change its password'],
  ['GET', '/healthz', 'liveness probe'],
  ['GET', '/openapi.json', 'this API as OpenAPI 3.1'],
];

export default function DocsPage() {
  const [host, setHost] = React.useState('relay.example.com:7000');
  React.useEffect(() => setHost(window.location.host), []);

  return (
    <Stack spacing={4}>
      <div>
        <Typography variant="h1">How it works</Typography>
        <P>
          This server introduces two peers to each other so they can talk directly. A host and a client both open a
          WebSocket to <code>/signal</code> with the same session code; each reflects its public UDP address off this
          server, is handed the other&apos;s, and from there they punch a QUIC connection between themselves. The tunnel
          does not pass through this machine. The full admin API is described by <a href="/openapi.json">/openapi.json</a>{' '}
          and rendered as <Link href="/swagger">Swagger UI</Link>.
        </P>
      </div>

      <Section title="Two ports, and why relays exist">
        <P>
          Signaling is HTTP on one port; reflection and relay are UDP on another. When two NATs will not let a direct
          path form, the peers fall back to sending their datagrams through the UDP port and this server forwards them
          between the pair. That is what the <Link href="/relays">relays</Link> page shows, and it is the only case where
          tunnel traffic touches this machine at all. Everything else here is bookkeeping that lasts as long as the
          handshake.
        </P>
      </Section>

      <Section title="Connecting to this server">
        <P>On the machine you want to reach:</P>
        <Code>{`relay-tunnel expose --server ws://${host}/signal --allow-dial`}</Code>
        <P>It prints a session code. On your machine, with that code:</P>
        <Code>{`relay-tunnel connect --server ws://${host}/signal \\
  --code CODE -L 9000:127.0.0.1:8080`}</Code>
        <P>
          Both peers appear on the <Link href="/sessions">sessions</Link> page while they are signaling, and are gone
          once they have connected to each other.
        </P>
      </Section>

      <Section title="The API">
        <P>
          Every endpoint below except <code>/healthz</code> and the spec itself needs an administrator: this browser,
          signed in, holding the session cookie. Sign in on this site and the <Link href="/swagger">Swagger page</Link>{' '}
          can call them directly.
        </P>
        <Card sx={{ maxWidth: COLUMN }}>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>method</TableCell>
                <TableCell>path</TableCell>
                <TableCell>what it does</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {ENDPOINTS.map(([method, path, what]) => (
                <TableRow key={`${method} ${path}`}>
                  <TableCell sx={{ fontFamily: 'ui-monospace, monospace' }}>{method}</TableCell>
                  <TableCell sx={{ fontFamily: 'ui-monospace, monospace' }}>{path}</TableCell>
                  <TableCell sx={{ whiteSpace: 'normal' }}>{what}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      </Section>

      <Section title="What deleting things does">
        <P>
          Closing a session ends a rendezvous: both signaling connections are dropped and the relay routes for their
          tokens are reclaimed. Dropping a relay cuts a pair that never got a direct path, so their tunnel stops. Neither
          reaches a tunnel that already punched through, because that traffic no longer comes here.
        </P>
      </Section>

      <Section title="The administrator account">
        <P>
          There is exactly one, stored in the server&apos;s config file as a PBKDF2-HMAC-SHA256 hash. Renaming it on the{' '}
          <Link href="/account">account</Link> page retires the old name rather than adding a second account. A server
          with no password set accepts <code>admin</code>/<code>admin</code> once and then demands a new one before it
          will show anything.
        </P>
        <Code>{`{
  "server": {
    "http": ":7000",
    "udp": ":7001",
    "admin": {
      "disabled": false,
      "session_hours": 12,
      "trust_forwarded_headers": true
    }
  }
}`}</Code>
        <P>
          Set <code>trust_forwarded_headers</code> only behind a reverse proxy you control: it is what tells the server
          the browser arrived over HTTPS, so the session cookie is marked Secure. Set{' '}
          <code>&quot;disabled&quot;: true</code>, or start the server with <code>--admin=false</code>, to serve
          signaling alone with no interface and no API.
        </P>
      </Section>
    </Stack>
  );
}

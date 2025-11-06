'use client';

import * as React from 'react';
import DeleteOutlineIcon from '@mui/icons-material/DeleteOutline';
import {
  Alert,
  Box,
  Button,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  IconButton,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Tooltip,
  Typography,
} from '@mui/material';
import type { Peer, Session } from '../lib/types';
import { since } from '../lib/format';
import { api, ApiError } from '../lib/api';

/** Where a session is in the handshake, in one word. */
function state(s: Session): { label: string; color: 'success' | 'warning' | 'default' } {
  if (s.paired) return { label: 'paired', color: 'success' };
  if (s.peers.length === 2) return { label: 'pairing', color: 'warning' };
  return { label: 'waiting', color: 'default' };
}

const Mono = ({ children }: { children: React.ReactNode }) => (
  <Typography variant="caption" fontFamily="ui-monospace, monospace" color="text.secondary">
    {children}
  </Typography>
);

/** One side of a session: who it is, whether it is ready, and where it was seen. */
function PeerCell({ peer }: { peer: Peer | undefined }) {
  if (!peer) {
    return (
      <Typography variant="caption" color="text.disabled">
        not here yet
      </Typography>
    );
  }
  return (
    <Stack spacing={0.3}>
      <Stack direction="row" spacing={0.8} alignItems="center">
        <Chip size="small" variant="outlined" color={peer.ready ? 'success' : 'default'} label={peer.ready ? 'ready' : 'joining'} />
        {/*
          The name is chosen by the peer and never verified, so it is shown as
          the peer's own claim about itself and the token stays underneath as
          the identifier that actually came from this server.
        */}
        {peer.name && (
          <Tooltip title="name this peer reports for itself; not verified">
            <Typography variant="body2" noWrap sx={{ maxWidth: 180, fontWeight: 500 }}>
              {peer.name}
            </Typography>
          </Tooltip>
        )}
        <Mono>{peer.public_addr || peer.remote_addr || '-'}</Mono>
      </Stack>
      <Mono>{peer.token}</Mono>
    </Stack>
  );
}

/**
 * Confirms and performs a close.
 *
 * Closing a rendezvous drops both signaling connections and the relay routes
 * for their tokens, so it is worth asking first. A pair that already punched a
 * direct path is untouched, which is the part worth saying out loud.
 */
export function CloseDialog({
  session,
  open,
  onClose,
  onDeleted,
}: {
  session: Session | null;
  open: boolean;
  onClose: () => void;
  onDeleted?: () => void;
}) {
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  React.useEffect(() => {
    if (open) setError(null);
  }, [open]);

  if (!session) return null;

  return (
    <Dialog open={open} onClose={busy ? undefined : onClose}>
      <DialogTitle>Close {session.code}?</DialogTitle>
      <DialogContent>
        <DialogContentText component="div">
          Both peers are disconnected from signaling and their relay routes are reclaimed. A pair that already punched a
          direct path keeps its tunnel: that traffic no longer passes through this server. A peer that is still running
          will reconnect and start a new rendezvous on the same code.
        </DialogContentText>
        {error && (
          <Alert severity="error" sx={{ mt: 2 }}>
            {error}
          </Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={busy}>
          cancel
        </Button>
        <Button
          color="error"
          disabled={busy}
          onClick={async () => {
            setBusy(true);
            try {
              await api.deleteSession(session.code);
              onDeleted?.();
              onClose();
            } catch (e) {
              setError(e instanceof ApiError ? e.message : String(e));
            } finally {
              setBusy(false);
            }
          }}
        >
          {busy ? 'closing...' : 'close'}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

export default function SessionsTable({
  sessions,
  dense,
  onDeleted,
}: {
  sessions: Session[];
  dense?: boolean;
  /** When given, each row gets a close button and this is called after one succeeds. */
  onDeleted?: () => void;
}) {
  const [closing, setClosing] = React.useState<Session | null>(null);

  if (sessions.length === 0) {
    return (
      <Typography variant="body2" color="text.secondary">
        No peer is signaling right now. A session appears here as soon as one runs <code>relay-tunnel expose</code> or{' '}
        <code>relay-tunnel connect</code> against this server.
      </Typography>
    );
  }

  return (
    <Box sx={{ overflowX: 'auto' }}>
      <Table size="small">
        <TableHead>
          <TableRow>
            <TableCell>state</TableCell>
            <TableCell>code</TableCell>
            <TableCell>host</TableCell>
            <TableCell>client</TableCell>
            {!dense && <TableCell>created</TableCell>}
            <TableCell>paired</TableCell>
            {onDeleted && <TableCell align="right" />}
          </TableRow>
        </TableHead>
        <TableBody>
          {sessions.map((s) => {
            const st = state(s);
            return (
              <TableRow key={s.code} hover>
                <TableCell>
                  <Chip size="small" variant="outlined" color={st.color} label={st.label} />
                </TableCell>
                <TableCell sx={{ fontFamily: 'ui-monospace, monospace' }}>{s.code}</TableCell>
                <TableCell>
                  <PeerCell peer={s.peers.find((p) => p.role === 'host')} />
                </TableCell>
                <TableCell>
                  <PeerCell peer={s.peers.find((p) => p.role === 'client')} />
                </TableCell>
                {!dense && <TableCell>{since(s.created_at)}</TableCell>}
                <TableCell>{s.paired_at ? since(s.paired_at) : '-'}</TableCell>
                {onDeleted && (
                  <TableCell align="right" padding="checkbox">
                    <Tooltip title="close this rendezvous">
                      <IconButton size="small" aria-label={`close ${s.code}`} onClick={() => setClosing(s)}>
                        <DeleteOutlineIcon fontSize="small" />
                      </IconButton>
                    </Tooltip>
                  </TableCell>
                )}
              </TableRow>
            );
          })}
        </TableBody>
      </Table>

      <CloseDialog session={closing} open={closing !== null} onClose={() => setClosing(null)} onDeleted={onDeleted} />
    </Box>
  );
}

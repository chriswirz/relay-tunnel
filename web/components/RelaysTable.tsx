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
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Tooltip,
  Typography,
} from '@mui/material';
import type { Relay } from '../lib/types';
import { bytes, since, until } from '../lib/format';
import { api, ApiError } from '../lib/api';

/**
 * Confirms and performs a drop.
 *
 * A relay route only exists because two peers could not reach each other
 * directly, so dropping one cuts a tunnel that is actually carrying traffic.
 * That is the point of the button, and the reason it asks first.
 */
export function DropDialog({
  relay,
  open,
  onClose,
  onDeleted,
}: {
  relay: Relay | null;
  open: boolean;
  onClose: () => void;
  onDeleted?: () => void;
}) {
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  React.useEffect(() => {
    if (open) setError(null);
  }, [open]);

  if (!relay) return null;

  return (
    <Dialog open={open} onClose={busy ? undefined : onClose}>
      <DialogTitle>Drop this relayed pair?</DialogTitle>
      <DialogContent>
        <DialogContentText component="div">
          Both sides of the pair are reclaimed{relay.code ? ` (${relay.code})` : ''}. If these peers are still relaying,
          their tunnel stops the moment this happens: a relayed pair has no direct path to fall back on. They will have
          to start a new rendezvous.
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
              await api.deleteRelay(relay.token);
              onDeleted?.();
              onClose();
            } catch (e) {
              setError(e instanceof ApiError ? e.message : String(e));
            } finally {
              setBusy(false);
            }
          }}
        >
          {busy ? 'dropping...' : 'drop'}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

export default function RelaysTable({
  relays,
  dense,
  onDeleted,
}: {
  relays: Relay[];
  dense?: boolean;
  /** When given, each row gets a drop button and this is called after one succeeds. */
  onDeleted?: () => void;
}) {
  const [dropping, setDropping] = React.useState<Relay | null>(null);

  if (relays.length === 0) {
    return (
      <Typography variant="body2" color="text.secondary">
        No relayed traffic. Peers that punch a direct path never appear here, so an empty table is the good outcome.
      </Typography>
    );
  }

  return (
    <Box sx={{ overflowX: 'auto' }}>
      <Table size="small">
        <TableHead>
          <TableRow>
            <TableCell>token</TableCell>
            <TableCell>role</TableCell>
            {!dense && <TableCell>code</TableCell>}
            <TableCell>from</TableCell>
            <TableCell align="right">packets</TableCell>
            <TableCell align="right">bytes</TableCell>
            <TableCell>last seen</TableCell>
            {!dense && <TableCell>expires</TableCell>}
            {onDeleted && <TableCell align="right" />}
          </TableRow>
        </TableHead>
        <TableBody>
          {relays.map((r) => (
            <TableRow key={r.token} hover>
              <TableCell sx={{ fontFamily: 'ui-monospace, monospace' }}>{r.token}</TableCell>
              <TableCell>
                {r.peer_token ? (
                  <Chip size="small" variant="outlined" label={r.role || 'paired'} />
                ) : (
                  <Tooltip title="reflected its address but was never paired">
                    <Chip size="small" variant="outlined" color="default" label="unpaired" />
                  </Tooltip>
                )}
              </TableCell>
              {!dense && <TableCell sx={{ fontFamily: 'ui-monospace, monospace' }}>{r.code || '-'}</TableCell>}
              <TableCell sx={{ fontFamily: 'ui-monospace, monospace' }}>{r.addr || '-'}</TableCell>
              <TableCell align="right">{r.packets.toLocaleString()}</TableCell>
              <TableCell align="right">{bytes(r.bytes)}</TableCell>
              <TableCell>{since(r.last_seen)}</TableCell>
              {!dense && <TableCell>{until(r.expires_at)}</TableCell>}
              {onDeleted && (
                <TableCell align="right" padding="checkbox">
                  <Tooltip title="drop this relayed pair">
                    <IconButton size="small" aria-label={`drop ${r.token}`} onClick={() => setDropping(r)}>
                      <DeleteOutlineIcon fontSize="small" />
                    </IconButton>
                  </Tooltip>
                </TableCell>
              )}
            </TableRow>
          ))}
        </TableBody>
      </Table>

      <DropDialog relay={dropping} open={dropping !== null} onClose={() => setDropping(null)} onDeleted={onDeleted} />
    </Box>
  );
}

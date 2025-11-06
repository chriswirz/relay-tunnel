'use client';

import * as React from 'react';
import {
  Alert,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  MenuItem,
  DialogTitle,
  Stack,
  TextField,
  Typography,
} from '@mui/material';
import { api, ApiError } from '../lib/api';
import { PRUNE_AGES, type PruneAge, type Relay } from '../lib/types';

/** How each window reads in a sentence, rather than as a bare token. */
const LABELS: Record<PruneAge, string> = {
  '1m': '1 minute',
  '5m': '5 minutes',
  '15m': '15 minutes',
  '1h': '1 hour',
  '6h': '6 hours',
  '24h': '24 hours',
};

const WINDOWS: Record<PruneAge, number> = {
  '1m': 60e3,
  '5m': 5 * 60e3,
  '15m': 15 * 60e3,
  '1h': 3600e3,
  '6h': 6 * 3600e3,
  '24h': 24 * 3600e3,
};

/** Routes this would reclaim, computed here so the confirmation can count them. */
function stale(relays: Relay[], age: PruneAge): Relay[] {
  const cutoff = Date.now() - WINDOWS[age];
  return relays.filter((r) => new Date(r.last_seen).getTime() < cutoff);
}

export default function PruneRelays({ relays, onPruned }: { relays: Relay[]; onPruned: () => void }) {
  const [age, setAge] = React.useState<PruneAge>('15m');
  const [confirming, setConfirming] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);
  const [done, setDone] = React.useState<string | null>(null);

  const doomed = stale(relays, age);

  return (
    <Stack spacing={1.5}>
      <Stack direction="row" spacing={1.5} alignItems="center" flexWrap="wrap">
        <TextField
          select
          size="small"
          label="idle for at least"
          value={age}
          onChange={(e) => setAge(e.target.value as PruneAge)}
          sx={{ minWidth: 190 }}
        >
          {PRUNE_AGES.map((a) => (
            <MenuItem key={a} value={a}>
              {LABELS[a]}
            </MenuItem>
          ))}
        </TextField>
        <Button
          variant="outlined"
          color="error"
          disabled={doomed.length === 0}
          onClick={() => {
            setError(null);
            setDone(null);
            setConfirming(true);
          }}
        >
          {doomed.length === 0 ? 'nothing to clean up' : `clean up ${doomed.length}`}
        </Button>
        <Typography variant="caption" color="text.secondary">
          A route carrying traffic is never idle, so this only reclaims what is already finished.
        </Typography>
      </Stack>

      {error && <Alert severity="error">{error}</Alert>}
      {done && <Alert severity="success">{done}</Alert>}

      <Dialog open={confirming} onClose={busy ? undefined : () => setConfirming(false)}>
        <DialogTitle>Reclaim {doomed.length} relay routes?</DialogTitle>
        <DialogContent>
          <DialogContentText component="div">
            Every route with nothing arriving on it for {LABELS[age]} is removed:
            <Typography component="div" variant="caption" sx={{ mt: 1, fontFamily: 'ui-monospace, monospace' }}>
              {doomed
                .slice(0, 12)
                .map((r) => r.token)
                .join(', ')}
              {doomed.length > 12 ? `, and ${doomed.length - 12} more` : ''}
            </Typography>
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirming(false)} disabled={busy}>
            cancel
          </Button>
          <Button
            color="error"
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              try {
                const result = await api.pruneRelays(age);
                setDone(`Reclaimed ${result.deleted} route${result.deleted === 1 ? '' : 's'}.`);
                setConfirming(false);
                onPruned();
              } catch (e) {
                setError(e instanceof ApiError ? e.message : String(e));
              } finally {
                setBusy(false);
              }
            }}
          >
            {busy ? 'reclaiming...' : 'reclaim'}
          </Button>
        </DialogActions>
      </Dialog>
    </Stack>
  );
}

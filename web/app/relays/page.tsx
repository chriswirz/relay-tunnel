'use client';

import * as React from 'react';
import { CircularProgress, Divider, Stack, Typography } from '@mui/material';
import AuthGate, { ApiErrorAlert } from '../../components/AuthGate';
import RelaysTable from '../../components/RelaysTable';
import PruneRelays from '../../components/PruneRelays';
import { api } from '../../lib/api';
import { usePoll } from '../../lib/use-poll';
import type { Relay } from '../../lib/types';

function List() {
  const { data, error, refresh } = usePoll<{ relays: Relay[] }>(React.useCallback(() => api.listRelays(), []));
  if (error) return <ApiErrorAlert error={error} />;
  if (!data) return <CircularProgress size={22} />;
  return (
    <Stack spacing={3}>
      <RelaysTable relays={data.relays} onDeleted={() => void refresh()} />
      {data.relays.length > 0 && (
        <>
          <Divider />
          <div>
            <Typography variant="h2" gutterBottom>
              Clean up
            </Typography>
            <PruneRelays relays={data.relays} onPruned={() => void refresh()} />
          </div>
        </>
      )}
    </Stack>
  );
}

export default function RelaysPage() {
  return (
    <Stack spacing={3}>
      <div>
        <Typography variant="h1">Relays</Typography>
        <Typography variant="body2" color="text.secondary" sx={{ maxWidth: 720 }}>
          Pairs whose datagrams pass through this server, because a direct path could not be punched between them. Each
          peer is one row, pointing at the other; the bytes are what this machine is actually carrying. Routes are
          reclaimed on their own once idle, and an empty table is the outcome to hope for.
        </Typography>
      </div>
      <AuthGate>
        <List />
      </AuthGate>
    </Stack>
  );
}

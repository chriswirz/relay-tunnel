'use client';

import * as React from 'react';
import { CircularProgress, Stack, Typography } from '@mui/material';
import AuthGate, { ApiErrorAlert } from '../../components/AuthGate';
import SessionsTable from '../../components/SessionsTable';
import { api } from '../../lib/api';
import { usePoll } from '../../lib/use-poll';
import type { Session } from '../../lib/types';

function List() {
  const { data, error, refresh } = usePoll<{ sessions: Session[] }>(React.useCallback(() => api.listSessions(), []));
  if (error) return <ApiErrorAlert error={error} />;
  if (!data) return <CircularProgress size={22} />;
  return <SessionsTable sessions={data.sessions} onDeleted={() => void refresh()} />;
}

export default function SessionsPage() {
  return (
    <Stack spacing={3}>
      <div>
        <Typography variant="h1">Sessions</Typography>
        <Typography variant="body2" color="text.secondary" sx={{ maxWidth: 720 }}>
          One row per rendezvous code with a peer on it. A session appears when its first peer connects and is gone when
          its last one leaves, so this is signaling in progress rather than a history. Pairing waits for both sides to
          reflect their public address; once they have, each is told the other&apos;s and they connect directly.
        </Typography>
      </div>
      <AuthGate>
        <List />
      </AuthGate>
    </Stack>
  );
}

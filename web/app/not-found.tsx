import * as React from 'react';
import { Stack, Typography } from '@mui/material';

/** Served for any path the exported application does not have a page for. */
export default function NotFound() {
  return (
    <Stack spacing={2}>
      <Typography variant="h1">404</Typography>
      <Typography variant="body2" color="text.secondary">
        No page here.
      </Typography>
      <Typography variant="body2">
        <a href="/">Back to the overview</a>
      </Typography>
    </Stack>
  );
}

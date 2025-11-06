'use client';

import * as React from 'react';
import { ApiError } from './api';

/**
 * Polls one API call so a page reflects peers coming and going.
 *
 * Everything this server reports is in-memory state that changes on its own:
 * sessions appear when peers connect, relay routes are swept when they go idle.
 * A page that loaded once would be stale within seconds, so every view here is
 * built on this rather than a single fetch.
 */
export function usePoll<T>(load: () => Promise<T>, intervalMs = 5000) {
  const [data, setData] = React.useState<T | null>(null);
  const [error, setError] = React.useState<ApiError | null>(null);
  const live = React.useRef(true);
  // The caller passes a fresh closure each render; keeping it in a ref is what
  // stops that from restarting the interval on every one.
  const loader = React.useRef(load);
  loader.current = load;

  const refresh = React.useCallback(async () => {
    try {
      const next = await loader.current();
      if (!live.current) return;
      setData(next);
      setError(null);
    } catch (e) {
      if (!live.current) return;
      setError(e instanceof ApiError ? e : new ApiError(0, String(e)));
    }
  }, []);

  React.useEffect(() => {
    live.current = true;
    void refresh();
    const t = setInterval(() => void refresh(), intervalMs);
    return () => {
      live.current = false;
      clearInterval(t);
    };
  }, [intervalMs, refresh]);

  // refresh lets an action that changed something show the result at once,
  // rather than waiting out the poll interval.
  return { data, error, refresh };
}

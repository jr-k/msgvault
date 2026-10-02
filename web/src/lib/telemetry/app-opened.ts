import { captureTelemetryEvent } from '../api/generated/api/api';
import type { APIClient } from '../api/client';

export type AppOpenedReporter = { start(client: APIClient): () => void };

// One reporter per tab: it reports app_opened at most once per UTC day, on start or on window focus.
export function createAppOpenedReporter(): AppOpenedReporter {
  let lastReportedDay = '';
  const report = (client: APIClient) => {
    const day = new Date().toISOString().slice(0, 10);
    if (day === lastReportedDay) return;
    lastReportedDay = day;
    void captureTelemetryEvent({ event: 'app_opened' }, client).catch(() => undefined);
  };
  return {
    start(client) {
      const onFocus = () => report(client);
      report(client);
      window.addEventListener('focus', onFocus);
      return () => window.removeEventListener('focus', onFocus);
    },
  };
}

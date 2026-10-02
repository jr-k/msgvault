import { captureTelemetryEvent } from '../api/generated/api/api';
import type { APIClient } from '../api/client';

export type AppOpenedReporter = { start(client: APIClient): () => void };

export const APP_OPENED_DAY_KEY = 'msgvault-app-opened-day';

// Reports app_opened at most once per UTC day per browser, on start or on window focus.
// localStorage carries the day across tabs and reloads; memory covers a browser that blocks storage.
export function createAppOpenedReporter(): AppOpenedReporter {
  let lastReportedDay = '';
  const readDay = () => {
    try {
      return localStorage.getItem(APP_OPENED_DAY_KEY) ?? lastReportedDay;
    } catch {
      return lastReportedDay;
    }
  };
  const report = (client: APIClient) => {
    const day = new Date().toISOString().slice(0, 10);
    if (day === lastReportedDay || day === readDay()) return;
    lastReportedDay = day;
    try {
      localStorage.setItem(APP_OPENED_DAY_KEY, day);
    } catch {
      // Storage may be disabled; memory still holds the day for this tab.
    }
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

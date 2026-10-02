import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createSessionAwareAPIClient } from '../api/client';
import { createAppOpenedReporter } from './app-opened';

function settle(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve));
}

describe('app_opened reporter', () => {
  let fetchMock: ReturnType<typeof vi.fn<typeof fetch>>;
  let client: ReturnType<typeof createSessionAwareAPIClient>;
  let stops: Array<() => void>;

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'] });
    fetchMock = vi.fn<typeof fetch>(async () => Response.json({ status: 'disabled' }, { status: 202 }));
    client = createSessionAwareAPIClient(fetchMock, () => 'csrf-token');
    stops = [];
  });

  afterEach(() => {
    for (const stop of stops) stop();
    vi.useRealTimers();
  });

  function start(reporter = createAppOpenedReporter()) {
    stops.push(reporter.start(client));
    return reporter;
  }

  function focusAt(iso: string) {
    vi.setSystemTime(new Date(iso));
    window.dispatchEvent(new Event('focus'));
  }

  it('posts one same-origin app_opened request with the CSRF token on start', async () => {
    vi.setSystemTime(new Date('2026-10-02T08:00:00Z'));
    start();
    await settle();
    expect(fetchMock).toHaveBeenCalledOnce();
    const request = fetchMock.mock.calls[0][0] as Request;
    const url = new URL(request.url);
    expect(request.method).toBe('POST');
    expect(url.pathname).toBe('/api/v1/telemetry/events');
    expect(url.origin).toBe(window.location.origin);
    expect(request.headers.get('X-CSRF-Token')).toBe('csrf-token');
    expect(request.headers.get('Content-Type')).toBe('application/json');
    expect(await request.text()).toBe('{"event":"app_opened"}');
  });

  it('ignores focus later the same UTC day', async () => {
    vi.setSystemTime(new Date('2026-10-02T08:00:00Z'));
    start();
    focusAt('2026-10-02T09:00:00Z');
    focusAt('2026-10-02T20:00:00Z');
    await settle();
    expect(fetchMock).toHaveBeenCalledOnce();
  });

  it('posts on the first focus of a later UTC day only', async () => {
    vi.setSystemTime(new Date('2026-10-02T08:00:00Z'));
    start();
    focusAt('2026-10-03T07:00:00Z');
    focusAt('2026-10-03T18:00:00Z');
    await settle();
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('counts a focus just past UTC midnight as a new day', async () => {
    vi.setSystemTime(new Date('2026-10-02T23:59:00Z'));
    start();
    focusAt('2026-10-03T00:01:00Z');
    await settle();
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('sends nothing when the same tab starts again that day, and posts on a later day', async () => {
    vi.setSystemTime(new Date('2026-10-02T08:00:00Z'));
    const reporter = start();
    stops.pop()?.();
    start(reporter);
    await settle();
    expect(fetchMock).toHaveBeenCalledOnce();
    stops.pop()?.();
    vi.setSystemTime(new Date('2026-10-03T08:00:00Z'));
    start(reporter);
    await settle();
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('swallows a failed post and still reports the next day', async () => {
    // Vitest fails the run on an unhandled rejection, so settling proves the catch.
    fetchMock.mockRejectedValueOnce(new TypeError('connection refused'));
    vi.setSystemTime(new Date('2026-10-02T08:00:00Z'));
    start();
    await settle();
    focusAt('2026-10-03T08:00:00Z');
    await settle();
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('stops listening after cleanup', async () => {
    vi.setSystemTime(new Date('2026-10-02T08:00:00Z'));
    start();
    stops.pop()?.();
    focusAt('2026-10-03T08:00:00Z');
    await settle();
    expect(fetchMock).toHaveBeenCalledOnce();
  });
});

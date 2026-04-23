import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import App from './App';
import { cloneMockConsoleData } from './mock-console-data';

const consolePayload = cloneMockConsoleData();

function createFetchResponse(body: unknown, ok = true, status = 200) {
  return {
    ok,
    status,
    json: async () => body,
    text: async () => (typeof body === 'string' ? body : JSON.stringify(body)),
  };
}

function findRequestByPath(fetchMock: ReturnType<typeof vi.fn>, pathname: string): [URL, RequestInit | undefined] | undefined {
  const call = fetchMock.mock.calls.find((entry) => {
    const url = entry[0] as URL;
    return url.pathname === pathname;
  });
  if (!call) {
    return undefined;
  }
  return [call[0] as URL, call[1] as RequestInit | undefined];
}

describe('App', () => {
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    window.history.replaceState({}, '', '/');
    window.localStorage.clear();
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
    window.history.replaceState({}, '', '/');
    window.localStorage.clear();
  });

  it('renders the routed local operator shell and overview from the live console payload', async () => {
    const fetchMock = vi.fn().mockResolvedValue(createFetchResponse(consolePayload));
    globalThis.fetch = fetchMock as typeof fetch;
    window.localStorage.setItem('bot-platform.console.operator-bearer-token', 'opaque-viewer-token');

    render(<App />);

    expect(screen.getByText('Loading local operator console')).toBeInTheDocument();
    await screen.findByRole('heading', { name: 'Local operator console' });

    expect(screen.getByText('Stored in this browser and sent as Authorization: Bearer .... The runtime resolves actor/session identity and returns that metadata in the console payload.')).toBeInTheDocument();
    expect(screen.getByLabelText('Bearer token')).toHaveValue('opaque-viewer-token');
    expect(screen.getByText('viewer-user')).toBeInTheDocument();
    expect(screen.getByText('session-operator-bearer-viewer-user')).toBeInTheDocument();
    expect(screen.getByText('viewer-main')).toBeInTheDocument();
    expect(screen.getByText('bearer')).toBeInTheDocument();
    expect(screen.getByText('read+operator-plugin-enable-disable+plugin-config+job-retry+schedule-cancel')).toBeInTheDocument();
    expect(screen.getByText('Recovery and alert evidence')).toBeInTheDocument();
    expect(screen.getAllByText('job-dead-letter-console').length).toBeGreaterThan(0);
    expect(screen.getByText('workflow-user-1')).toBeInTheDocument();
    expect(screen.getByText(/category operator · code plugin_disabled/i)).toBeInTheDocument();

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledTimes(1);
    });

    const [requestURL, requestInit] = fetchMock.mock.calls[0] ?? [];
    expect(requestURL).toBeInstanceOf(URL);
    expect((requestURL as URL).pathname).toBe('/api/console');
    expect(requestInit?.headers).toBeInstanceOf(Headers);
    expect((requestInit?.headers as Headers).get('Authorization')).toBe('Bearer opaque-viewer-token');
  });

  it('applies a new local bearer token and refetches the console snapshot with Authorization bearer auth', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(createFetchResponse(consolePayload))
      .mockResolvedValueOnce(createFetchResponse(consolePayload));
    globalThis.fetch = fetchMock as typeof fetch;

    render(<App />);
    await screen.findByRole('heading', { name: 'Local operator console' });

    fireEvent.change(screen.getByLabelText('Bearer token'), { target: { value: 'opaque-admin-token' } });
    fireEvent.click(screen.getByRole('button', { name: 'Apply token' }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledTimes(2);
    });

    const secondInit = fetchMock.mock.calls[1]?.[1];
    expect((secondInit?.headers as Headers).get('Authorization')).toBe('Bearer opaque-admin-token');
    expect(window.localStorage.getItem('bot-platform.console.operator-bearer-token')).toBe('opaque-admin-token');
  });

  it('navigates to a plugin detail route and submits the narrow plugin-echo config update through the existing runtime endpoint', async () => {
    const updatedPayload = cloneMockConsoleData();
    const updatedPlugin = updatedPayload.plugins.find((plugin) => plugin.id === 'plugin-echo');
    if (!updatedPlugin) {
      throw new Error('missing plugin-echo in mock payload');
    }
    updatedPlugin.config = { prefix: 'operator: ' };
    updatedPlugin.configUpdatedAt = '2026-04-19T12:05:00Z';

    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(createFetchResponse(consolePayload))
      .mockResolvedValueOnce(createFetchResponse(consolePayload))
      .mockResolvedValueOnce(
        createFetchResponse({
          status: 'ok',
          action: 'config.update',
          target: 'plugin-echo',
          accepted: true,
          reason: 'plugin_config_updated',
          plugin_id: 'plugin-echo',
          config: { prefix: 'operator: ' },
          updated_at: '2026-04-19T12:05:00Z',
          persisted: true,
          config_path: '/demo/plugins/plugin-echo/config',
        }),
      )
      .mockResolvedValueOnce(createFetchResponse(updatedPayload));
    globalThis.fetch = fetchMock as typeof fetch;
    window.localStorage.setItem('bot-platform.console.operator-bearer-token', 'opaque-viewer-token');

    render(<App />);
    await screen.findByRole('heading', { name: 'Local operator console' });

    fireEvent.click(screen.getByLabelText('Open plugin plugin-echo details'));
    await screen.findByRole('heading', { name: 'Echo Plugin · plugin-echo' });
    expect(window.location.pathname).toBe('/plugins/plugin-echo');

    fireEvent.change(screen.getByLabelText('Prefix'), { target: { value: 'operator: ' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save prefix' }));

    await screen.findByText('Operator action accepted');

    const actionCall = findRequestByPath(fetchMock, '/demo/plugins/plugin-echo/config');
    expect(actionCall).toBeDefined();
    if (!actionCall) {
      throw new Error('missing plugin config action call');
    }
    const [actionURL, actionInit] = actionCall;
    expect(actionURL.pathname).toBe('/demo/plugins/plugin-echo/config');
    expect(actionInit?.method).toBe('POST');
    expect((actionInit?.headers as Headers).get('Authorization')).toBe('Bearer opaque-viewer-token');
    expect(actionInit?.body).toBe(JSON.stringify({ prefix: 'operator: ' }));
    expect(fetchMock).toHaveBeenCalledTimes(4);
    const finalRequest = fetchMock.mock.calls[3]?.[0] as URL;
    expect(finalRequest.pathname).toBe('/api/console');
    expect(finalRequest.searchParams.get('plugin_id')).toBe('plugin-echo');
  });

  it('retries a dead-letter job from the routed detail page and refetches the console payload afterward', async () => {
    const retriedPayload = cloneMockConsoleData();
    const retriedJob = retriedPayload.jobs.find((job) => job.id === 'job-dead-letter-console');
    if (!retriedJob) {
      throw new Error('missing dead-letter job in mock payload');
    }
    retriedJob.status = 'done';
    retriedJob.deadLetter = false;
    retriedPayload.alerts = [];

    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(createFetchResponse(consolePayload))
      .mockResolvedValueOnce(createFetchResponse(consolePayload))
      .mockResolvedValueOnce(
        createFetchResponse({
          status: 'ok',
          action: 'retry',
          target: 'job-dead-letter-console',
          accepted: true,
          reason: 'job_dead_letter_retried',
          job_id: 'job-dead-letter-console',
        }),
      )
      .mockResolvedValueOnce(createFetchResponse(retriedPayload));
    globalThis.fetch = fetchMock as typeof fetch;
    window.localStorage.setItem('bot-platform.console.operator-bearer-token', 'opaque-viewer-token');

    render(<App />);
    await screen.findByRole('heading', { name: 'Local operator console' });

    fireEvent.click(screen.getByLabelText('Open job job-dead-letter-console details'));
    await screen.findByRole('heading', { name: 'job-dead-letter-console · ai.chat' });

    fireEvent.click(screen.getByRole('button', { name: 'Retry dead-letter job' }));
    await screen.findByText('Operator action accepted');

    const actionCall = findRequestByPath(fetchMock, '/demo/jobs/job-dead-letter-console/retry');
    expect(actionCall).toBeDefined();
    if (!actionCall) {
      throw new Error('missing job retry action call');
    }
    const [actionURL, actionInit] = actionCall;
    expect(actionURL.pathname).toBe('/demo/jobs/job-dead-letter-console/retry');
    expect((actionInit?.headers as Headers).get('Authorization')).toBe('Bearer opaque-viewer-token');
    expect(fetchMock).toHaveBeenCalledTimes(4);
    const finalRequest = fetchMock.mock.calls[3]?.[0] as URL;
    expect(finalRequest.pathname).toBe('/api/console');
  });

  it('cancels a schedule from the routed detail page and refetches the console payload afterward', async () => {
    const cancelledPayload = cloneMockConsoleData();
    cancelledPayload.schedules = [];

    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(createFetchResponse(consolePayload))
      .mockResolvedValueOnce(createFetchResponse(consolePayload))
      .mockResolvedValueOnce(createFetchResponse({ status: 'ok', schedule_id: 'schedule-console', action: 'cancel' }))
      .mockResolvedValueOnce(createFetchResponse(cancelledPayload));
    globalThis.fetch = fetchMock as typeof fetch;
    window.localStorage.setItem('bot-platform.console.operator-bearer-token', 'opaque-viewer-token');

    render(<App />);
    await screen.findByRole('heading', { name: 'Local operator console' });

    fireEvent.click(screen.getByLabelText('Open schedule schedule-console details'));
    await screen.findByRole('heading', { name: 'schedule-console · message.received' });

    fireEvent.click(screen.getByRole('button', { name: 'Cancel schedule' }));
    await screen.findByText('Operator action accepted');

    const actionCall = findRequestByPath(fetchMock, '/demo/schedules/schedule-console/cancel');
    expect(actionCall).toBeDefined();
    if (!actionCall) {
      throw new Error('missing schedule cancel action call');
    }
    const [actionURL, actionInit] = actionCall;
    expect(actionURL.pathname).toBe('/demo/schedules/schedule-console/cancel');
    expect((actionInit?.headers as Headers).get('Authorization')).toBe('Bearer opaque-viewer-token');
    expect(fetchMock).toHaveBeenCalledTimes(4);
    const finalRequest = fetchMock.mock.calls[3]?.[0] as URL;
    expect(finalRequest.pathname).toBe('/api/console');
  });

  it('shows workflow observability ids on the routed workflow detail page', async () => {
    const fetchMock = vi.fn().mockResolvedValue(createFetchResponse(consolePayload));
    globalThis.fetch = fetchMock as typeof fetch;

    render(<App />);
    await screen.findByRole('heading', { name: 'Local operator console' });

    fireEvent.click(screen.getByLabelText('Open workflow workflow-user-1 details'));
    await screen.findByRole('heading', { name: 'workflow-user-1 · plugin-workflow-demo' });

    expect(screen.getByText('trace-workflow-console')).toBeInTheDocument();
    expect(screen.getByText('evt-workflow-console-origin')).toBeInTheDocument();
    expect(screen.getByText('run-workflow-console')).toBeInTheDocument();
    expect(screen.getByText('corr-workflow-console')).toBeInTheDocument();
    expect(screen.getAllByText('message.received')).toHaveLength(2);
  });

  it('toggles auto refresh preference and performs an explicit manual refresh', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(createFetchResponse(consolePayload))
      .mockResolvedValueOnce(createFetchResponse(consolePayload));
    globalThis.fetch = fetchMock as typeof fetch;

    render(<App />);
    await screen.findByRole('heading', { name: 'Local operator console' });

    const autoRefreshToggle = screen.getByLabelText('Auto refresh every 15s');
    expect(autoRefreshToggle).toBeChecked();
    fireEvent.click(autoRefreshToggle);
    expect(window.localStorage.getItem('bot-platform.console.auto-refresh')).toBe('false');

    fireEvent.click(screen.getByRole('button', { name: 'Refresh now' }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledTimes(2);
    });
  });

  it('shows the runtime error state while keeping the local operator shell visible', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: false,
      status: 403,
      json: async () => ({ message: 'permission denied' }),
      text: async () => 'permission denied',
    });
    globalThis.fetch = fetchMock as typeof fetch;

    render(<App />);

    await screen.findByText('Console API unavailable');
    expect(screen.getByLabelText('Bearer token')).toBeInTheDocument();
    expect(screen.getByText('permission denied')).toBeInTheDocument();
  });
});

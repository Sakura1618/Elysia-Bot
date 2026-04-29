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

function requestPathnames(fetchMock: ReturnType<typeof vi.fn>): string[] {
  return fetchMock.mock.calls.map((entry) => (entry[0] as URL).pathname);
}

function requestAt(fetchMock: ReturnType<typeof vi.fn>, index: number): [URL, RequestInit | undefined] {
  const call = fetchMock.mock.calls[index];
  if (!call) {
    throw new Error(`missing fetch call at index ${index}`);
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
    expect(screen.getByText('read+operator-plugin-enable-disable+plugin-config+job-control+schedule-cancel')).toBeInTheDocument();
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

  it('disables a plugin from the routed detail page and proves the final refetched disabled state on the same route', async () => {
    const disabledPayload = cloneMockConsoleData();
    const disabledPlugin = disabledPayload.plugins.find((plugin) => plugin.id === 'plugin-echo');
    if (!disabledPlugin) {
      throw new Error('missing plugin-echo in mock payload');
    }
    disabledPlugin.enabled = false;
    disabledPlugin.enabledStateSource = 'sqlite-plugin-enabled-overlay';
    disabledPlugin.enabledStatePersisted = true;

    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(createFetchResponse(consolePayload))
      .mockResolvedValueOnce(
        createFetchResponse({
          status: 'ok',
          action: 'disable',
          target: 'plugin-echo',
          accepted: true,
          reason: 'plugin_disabled',
          plugin_id: 'plugin-echo',
          enabled: false,
        }),
      )
      .mockResolvedValueOnce(createFetchResponse(disabledPayload));
    globalThis.fetch = fetchMock as typeof fetch;
    window.localStorage.setItem('bot-platform.console.operator-bearer-token', 'opaque-viewer-token');
    window.history.replaceState({}, '', '/plugins/plugin-echo');

    render(<App />);
    await screen.findByRole('heading', { name: 'Echo Plugin · plugin-echo' });
    expect(screen.getByRole('button', { name: 'Disable plugin' })).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Disable plugin' }));

    await screen.findByRole('heading', { name: 'Operator action accepted' });
    await screen.findByRole('button', { name: 'Enable plugin' });
    await screen.findByText('sqlite-plugin-enabled-overlay');

    const [initialURL, initialInit] = requestAt(fetchMock, 0);
    expect(initialURL.pathname).toBe('/api/console');
    expect(initialURL.searchParams.get('plugin_id')).toBe('plugin-echo');
    expect((initialInit?.headers as Headers).get('Authorization')).toBe('Bearer opaque-viewer-token');

    const [actionURL, actionInit] = requestAt(fetchMock, 1);
    expect(actionURL.pathname).toBe('/demo/plugins/plugin-echo/disable');
    expect(actionInit?.method).toBe('POST');
    expect((actionInit?.headers as Headers).get('Authorization')).toBe('Bearer opaque-viewer-token');

    const [refetchURL, refetchInit] = requestAt(fetchMock, 2);
    expect(refetchURL.pathname).toBe('/api/console');
    expect(refetchURL.searchParams.get('plugin_id')).toBe('plugin-echo');
    expect((refetchInit?.headers as Headers).get('Authorization')).toBe('Bearer opaque-viewer-token');

    expect(requestPathnames(fetchMock)).toEqual(['/api/console', '/demo/plugins/plugin-echo/disable', '/api/console']);
    expect(window.location.pathname).toBe('/plugins/plugin-echo');
    expect(screen.queryByRole('button', { name: 'Disable plugin' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Enable plugin' })).toBeInTheDocument();
  });

  it('shows read-only rollout state, recent rollout evidence, and rollout provenance on the plugin detail route', async () => {
    const fetchMock = vi.fn().mockResolvedValue(createFetchResponse(consolePayload));
    globalThis.fetch = fetchMock as typeof fetch;

    render(<App />);
    await screen.findByRole('heading', { name: 'Local operator console' });

    fireEvent.click(screen.getByLabelText('Open plugin plugin-echo details'));
    await screen.findByRole('heading', { name: 'Echo Plugin · plugin-echo' });

    expect(screen.getByRole('heading', { name: 'Rollout policy declaration' })).toBeInTheDocument();
    expect(screen.getByText('sqlite-current-runtime-rollout-operations')).toBeInTheDocument();
    expect(screen.getByText('/admin prepare <plugin-id>')).toBeInTheDocument();
    expect(screen.getByText('/admin activate <plugin-id>')).toBeInTheDocument();
    expect(screen.getByText('manifest.id-match')).toBeInTheDocument();
    expect(screen.getByText('prepared-record-required')).toBeInTheDocument();
    expect(screen.getByText('rollout_prepared')).toBeInTheDocument();
    expect(screen.getByText('manual-prepare-activate')).toBeInTheDocument();
    expect(screen.getByText('staged-rollout')).toBeInTheDocument();
    expect(screen.getByText('rollout is currently a manual admin chain limited to /admin prepare <plugin-id> and /admin activate <plugin-id>')).toBeInTheDocument();
    expect(screen.getByText('manual /admin prepare|activate only; preflight checks ID|Mode|APIVersion|Version; activate re-checks drift before lifecycle enable; rollout attempts persist as operational records; audit reasons are minimal; no rollback or staged rollout')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Rollout state' })).toBeInTheDocument();
    expect(screen.getByText('canary')).toBeInTheDocument();
    expect(screen.getByText('canarying')).toBeInTheDocument();
    expect(screen.getByText('0.1.0 · api v0 · subprocess')).toBeInTheDocument();
    expect(screen.getAllByText('0.2.0-candidate · api v0 · subprocess')).toHaveLength(2);
    expect(screen.getAllByText('rollout-console-1').length).toBeGreaterThan(0);
    expect(screen.getAllByText('sqlite-rollout-heads').length).toBeGreaterThan(0);
    expect(screen.getAllByText('sqlite-rollout-operation-records').length).toBeGreaterThan(0);
    expect(screen.getByText('rollout policy declaration is read-only and mirrors existing runtime behavior only')).toBeInTheDocument();
    expect(screen.getByText('rollout remains limited to manual /admin prepare|activate with minimal manifest preflight and activate-time drift re-check; no rollback or staged rollout')).toBeInTheDocument();
    expect(screen.getByText(/rollout prepare prepared for plugin-echo/i)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /prepare rollout/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /activate rollout/i })).not.toBeInTheDocument();
  });

  it('shows an explicit empty rollout state for plugins without rollout data', async () => {
    const payloadWithoutPluginAdminRollout = cloneMockConsoleData();
    const fetchMock = vi.fn().mockResolvedValue(createFetchResponse(payloadWithoutPluginAdminRollout));
    globalThis.fetch = fetchMock as typeof fetch;

    render(<App />);
    await screen.findByRole('heading', { name: 'Local operator console' });

    fireEvent.click(screen.getByRole('button', { name: 'Plugins' }));
    fireEvent.click(screen.getByLabelText('Open plugin plugin-admin details'));
    await screen.findByRole('heading', { name: 'Admin Plugin · plugin-admin' });

    expect(screen.getByRole('heading', { name: 'Rollout state' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Rollout policy declaration' })).toBeInTheDocument();
    expect(screen.getByText('No rollout state for this plugin')).toBeInTheDocument();
    expect(screen.getByText('The current console payload does not include a rollout head or rollout operation records for this plugin.')).toBeInTheDocument();
    expect(screen.getByText('No recent rollout operations')).toBeInTheDocument();
    expect(screen.getByText('The current console payload does not include rollout operation evidence for this plugin.')).toBeInTheDocument();
    expect(screen.getByText('/admin prepare <plugin-id>')).toBeInTheDocument();
    expect(screen.getAllByText('sqlite-rollout-heads').length).toBeGreaterThan(0);
    expect(screen.getAllByText('sqlite-rollout-operation-records').length).toBeGreaterThan(0);
  });

  it('shows pause and cancel actions for a queued job while hiding resume and retry', async () => {
    const fetchMock = vi.fn().mockResolvedValue(createFetchResponse(consolePayload));
    globalThis.fetch = fetchMock as typeof fetch;

    render(<App />);
    await screen.findByRole('heading', { name: 'Local operator console' });

    fireEvent.click(screen.getByRole('button', { name: 'Jobs' }));
    fireEvent.click(screen.getByLabelText('Open job job-console-ready details'));
    await screen.findByRole('heading', { name: 'job-console-ready · ai.chat' });

    expect(screen.getByRole('heading', { name: 'Job operator actions' })).toBeInTheDocument();
    expect(
      screen.getByText('Pause keeps a queued job from dispatching again until a later resume, then the route refetch shows the runtime queue truth.'),
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Pause job' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Cancel job' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Resume job' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Retry dead-letter job' })).not.toBeInTheDocument();
  });

  it('shows resume and cancel for a paused job while hiding pause and retry', async () => {
    const pausedPayload = cloneMockConsoleData();
    const pausedJob = pausedPayload.jobs.find((job) => job.id === 'job-console-ready');
    if (!pausedJob) {
      throw new Error('missing queued job in mock payload');
    }
    pausedJob.status = 'paused';
    pausedJob.queueStateSummary = 'paused by operator';

    const fetchMock = vi.fn().mockResolvedValue(createFetchResponse(pausedPayload));
    globalThis.fetch = fetchMock as typeof fetch;

    render(<App />);
    await screen.findByRole('heading', { name: 'Local operator console' });

    fireEvent.click(screen.getByRole('button', { name: 'Jobs' }));
    fireEvent.click(screen.getByLabelText('Open job job-console-ready details'));
    await screen.findByRole('heading', { name: 'job-console-ready · ai.chat' });

    expect(screen.getByText('paused')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Resume job' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Cancel job' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Pause job' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Retry dead-letter job' })).not.toBeInTheDocument();
  });

  it('shows pause and cancel actions for a retrying job while hiding resume and retry', async () => {
    const retryingPayload = cloneMockConsoleData();
    const retryingJob = retryingPayload.jobs.find((job) => job.id === 'job-console-ready');
    if (!retryingJob) {
      throw new Error('missing queued job in mock payload');
    }
    retryingJob.status = 'retrying';
    retryingJob.queueStateSummary = 'waiting to retry after previous failure';

    const fetchMock = vi.fn().mockResolvedValue(createFetchResponse(retryingPayload));
    globalThis.fetch = fetchMock as typeof fetch;

    render(<App />);
    await screen.findByRole('heading', { name: 'Local operator console' });

    fireEvent.click(screen.getByRole('button', { name: 'Jobs' }));
    fireEvent.click(screen.getByLabelText('Open job job-console-ready details'));
    await screen.findByRole('heading', { name: 'job-console-ready · ai.chat' });

    expect(screen.getByRole('button', { name: 'Pause job' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Cancel job' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Resume job' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Retry dead-letter job' })).not.toBeInTheDocument();
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
    const finalRequest = fetchMock.mock.calls[fetchMock.mock.calls.length - 1]?.[0] as URL;
    expect(finalRequest.pathname).toBe('/api/console');
    expect(screen.queryByRole('button', { name: 'Pause job' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Resume job' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Cancel job' })).not.toBeInTheDocument();
  });

  it('pauses a queued job from the routed detail page and proves the final refetched paused state', async () => {
    const pausedPayload = cloneMockConsoleData();
    const pausedJob = pausedPayload.jobs.find((job) => job.id === 'job-console-ready');
    if (!pausedJob) {
      throw new Error('missing queued job in mock payload');
    }
    pausedJob.status = 'paused';
    pausedJob.queueStateSummary = 'paused by operator after verification refetch';

    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(createFetchResponse(consolePayload))
      .mockResolvedValueOnce(
        createFetchResponse({
          status: 'ok',
          action: 'pause',
          target: 'job-console-ready',
          accepted: true,
          reason: 'job_paused',
          job_id: 'job-console-ready',
        }),
      )
      .mockResolvedValueOnce(createFetchResponse(pausedPayload));
    globalThis.fetch = fetchMock as typeof fetch;
    window.localStorage.setItem('bot-platform.console.operator-bearer-token', 'opaque-viewer-token');
    window.history.replaceState({}, '', '/jobs/job-console-ready');

    render(<App />);
    await screen.findByRole('heading', { name: 'job-console-ready · ai.chat' });

    fireEvent.click(screen.getByRole('button', { name: 'Pause job' }));
    await screen.findByRole('button', { name: 'Resume job' });
    await screen.findByText('paused by operator after verification refetch');
    await screen.findByText('Operator action accepted');

    const [initialURL, initialInit] = requestAt(fetchMock, 0);
    expect(initialURL.pathname).toBe('/api/console');
    expect((initialInit?.headers as Headers).get('Authorization')).toBe('Bearer opaque-viewer-token');

    const [actionURL, actionInit] = requestAt(fetchMock, 1);
    expect(actionURL.pathname).toBe('/demo/jobs/job-console-ready/pause');
    expect(actionInit?.method).toBe('POST');
    expect((actionInit?.headers as Headers).get('Authorization')).toBe('Bearer opaque-viewer-token');

    const [refetchURL, refetchInit] = requestAt(fetchMock, 2);
    expect(refetchURL.pathname).toBe('/api/console');
    expect((refetchInit?.headers as Headers).get('Authorization')).toBe('Bearer opaque-viewer-token');
    expect(requestPathnames(fetchMock)).toEqual(['/api/console', '/demo/jobs/job-console-ready/pause', '/api/console']);
    expect(window.location.pathname).toBe('/jobs/job-console-ready');
    expect(screen.queryByRole('button', { name: 'Pause job' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Cancel job' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Retry dead-letter job' })).not.toBeInTheDocument();
  });

  it('resumes a paused job from the routed detail page and proves the final refetched ready state', async () => {
    const pausedPayload = cloneMockConsoleData();
    const pausedJob = pausedPayload.jobs.find((job) => job.id === 'job-console-ready');
    if (!pausedJob) {
      throw new Error('missing queued job in mock payload');
    }
    pausedJob.status = 'paused';
    pausedJob.queueStateSummary = 'paused before resume verification';

    const resumedPayload = cloneMockConsoleData();
    const resumedJob = resumedPayload.jobs.find((job) => job.id === 'job-console-ready');
    if (!resumedJob) {
      throw new Error('missing resumed queued job in mock payload');
    }
    resumedJob.queueStateSummary = 'ready again after verification refetch';

    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(createFetchResponse(pausedPayload))
      .mockResolvedValueOnce(
        createFetchResponse({
          status: 'ok',
          action: 'resume',
          target: 'job-console-ready',
          accepted: true,
          reason: 'job_resumed',
          job_id: 'job-console-ready',
        }),
      )
      .mockResolvedValueOnce(createFetchResponse(resumedPayload));
    globalThis.fetch = fetchMock as typeof fetch;
    window.localStorage.setItem('bot-platform.console.operator-bearer-token', 'opaque-viewer-token');
    window.history.replaceState({}, '', '/jobs/job-console-ready');

    render(<App />);
    await screen.findByRole('heading', { name: 'job-console-ready · ai.chat' });
    expect(screen.getByText('paused before resume verification')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Resume job' }));
    await screen.findByRole('button', { name: 'Pause job' });
    await screen.findByRole('button', { name: 'Cancel job' });
    await screen.findByText('ready again after verification refetch');
    await screen.findByText('Operator action accepted');

    const [initialURL, initialInit] = requestAt(fetchMock, 0);
    expect(initialURL.pathname).toBe('/api/console');
    expect((initialInit?.headers as Headers).get('Authorization')).toBe('Bearer opaque-viewer-token');

    const [actionURL, actionInit] = requestAt(fetchMock, 1);
    expect(actionURL.pathname).toBe('/demo/jobs/job-console-ready/resume');
    expect(actionInit?.method).toBe('POST');
    expect((actionInit?.headers as Headers).get('Authorization')).toBe('Bearer opaque-viewer-token');

    const [refetchURL, refetchInit] = requestAt(fetchMock, 2);
    expect(refetchURL.pathname).toBe('/api/console');
    expect((refetchInit?.headers as Headers).get('Authorization')).toBe('Bearer opaque-viewer-token');
    expect(requestPathnames(fetchMock)).toEqual(['/api/console', '/demo/jobs/job-console-ready/resume', '/api/console']);
    expect(window.location.pathname).toBe('/jobs/job-console-ready');
    expect(screen.queryByRole('button', { name: 'Resume job' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Retry dead-letter job' })).not.toBeInTheDocument();
  });

  it('cancels a queued job from the routed detail page and proves the final refetched cancelled state', async () => {
    const cancelledPayload = cloneMockConsoleData();
    const cancelledJob = cancelledPayload.jobs.find((job) => job.id === 'job-console-ready');
    if (!cancelledJob) {
      throw new Error('missing queued job in mock payload');
    }
    cancelledJob.status = 'cancelled';
    cancelledJob.queueStateSummary = 'cancelled by operator after verification refetch';

    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(createFetchResponse(consolePayload))
      .mockResolvedValueOnce(
        createFetchResponse({
          status: 'ok',
          action: 'cancel',
          target: 'job-console-ready',
          accepted: true,
          reason: 'job_cancelled',
          job_id: 'job-console-ready',
        }),
      )
      .mockResolvedValueOnce(createFetchResponse(cancelledPayload));
    globalThis.fetch = fetchMock as typeof fetch;
    window.localStorage.setItem('bot-platform.console.operator-bearer-token', 'opaque-viewer-token');
    window.history.replaceState({}, '', '/jobs/job-console-ready');

    render(<App />);
    await screen.findByRole('heading', { name: 'job-console-ready · ai.chat' });

    fireEvent.click(screen.getByRole('button', { name: 'Cancel job' }));
    await screen.findByText('No job operator actions for this state');
    await screen.findByText('cancelled by operator after verification refetch');
    await screen.findByText('Operator action accepted');

    const [initialURL, initialInit] = requestAt(fetchMock, 0);
    expect(initialURL.pathname).toBe('/api/console');
    expect((initialInit?.headers as Headers).get('Authorization')).toBe('Bearer opaque-viewer-token');

    const [actionURL, actionInit] = requestAt(fetchMock, 1);
    expect(actionURL.pathname).toBe('/demo/jobs/job-console-ready/cancel');
    expect(actionInit?.method).toBe('POST');
    expect((actionInit?.headers as Headers).get('Authorization')).toBe('Bearer opaque-viewer-token');

    const [refetchURL, refetchInit] = requestAt(fetchMock, 2);
    expect(refetchURL.pathname).toBe('/api/console');
    expect((refetchInit?.headers as Headers).get('Authorization')).toBe('Bearer opaque-viewer-token');
    expect(requestPathnames(fetchMock)).toEqual(['/api/console', '/demo/jobs/job-console-ready/cancel', '/api/console']);
    expect(window.location.pathname).toBe('/jobs/job-console-ready');
    expect(screen.queryByRole('button', { name: 'Pause job' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Resume job' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Cancel job' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Retry dead-letter job' })).not.toBeInTheDocument();
  });

  it('surfaces a distinct verification failure when a job action write succeeds but the post-action console refetch fails', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(createFetchResponse(consolePayload))
      .mockResolvedValueOnce(
        createFetchResponse({
          status: 'ok',
          action: 'pause',
          target: 'job-console-ready',
          accepted: true,
          reason: 'job_paused',
          job_id: 'job-console-ready',
        }),
      )
      .mockResolvedValueOnce({
        ok: false,
        status: 503,
        json: async () => ({ message: 'console refetch denied' }),
        text: async () => 'console refetch denied',
      });
    globalThis.fetch = fetchMock as typeof fetch;
    window.localStorage.setItem('bot-platform.console.operator-bearer-token', 'opaque-viewer-token');
    window.history.replaceState({}, '', '/jobs/job-console-ready');

    render(<App />);
    await screen.findByRole('heading', { name: 'job-console-ready · ai.chat' });

    fireEvent.click(screen.getByRole('button', { name: 'Pause job' }));

    await screen.findByRole('heading', { name: 'Operator action accepted, but verification refetch failed' });
    expect(screen.queryByRole('heading', { name: 'Operator action accepted' })).not.toBeInTheDocument();
    expect(screen.getByText('Console API unavailable')).toBeInTheDocument();
    expect(screen.getAllByText('console refetch denied').length).toBeGreaterThan(0);

    const [initialURL, initialInit] = requestAt(fetchMock, 0);
    expect(initialURL.pathname).toBe('/api/console');
    expect((initialInit?.headers as Headers).get('Authorization')).toBe('Bearer opaque-viewer-token');

    const [actionURL, actionInit] = requestAt(fetchMock, 1);
    expect(actionURL.pathname).toBe('/demo/jobs/job-console-ready/pause');
    expect(actionInit?.method).toBe('POST');
    expect((actionInit?.headers as Headers).get('Authorization')).toBe('Bearer opaque-viewer-token');

    const [refetchURL, refetchInit] = requestAt(fetchMock, 2);
    expect(refetchURL.pathname).toBe('/api/console');
    expect((refetchInit?.headers as Headers).get('Authorization')).toBe('Bearer opaque-viewer-token');
    expect(requestPathnames(fetchMock)).toEqual(['/api/console', '/demo/jobs/job-console-ready/pause', '/api/console']);
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

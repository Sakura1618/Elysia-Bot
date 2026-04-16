import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import App from './App';

const consolePayload = {
  status: { adapters: 1, plugins: 2, jobs: 1, schedules: 1 },
  adapters: [
    {
      id: 'adapter-onebot-demo',
      adapter: 'onebot',
      source: 'onebot',
      status: 'registered',
      health: 'ready',
      online: true,
      statusSource: 'sqlite-adapter-instances',
      configSource: 'runtime-bootstrap',
      statePersisted: true,
      updatedAt: '2026-04-06T00:00:00Z',
      summary: 'registered adapter lifecycle facts from sqlite-adapter-instances',
    },
  ],
  plugins: [
    {
      id: 'plugin-echo',
      name: 'Echo Plugin',
      version: '0.1.0',
      apiVersion: 'v0',
      mode: 'subprocess',
      permissions: ['message:read'],
      publish: {
        sourceType: 'git',
        sourceUri: 'https://github.com/ohmyopencode/bot-platform/tree/main/plugins/plugin-echo',
        runtimeVersionRange: '>=0.1.0 <1.0.0',
      },
      entry: { module: 'plugins/plugin-echo' },
      configStateKind: 'plugin-owned-persisted-input',
      configSource: 'sqlite-plugin-config',
      configPersisted: true,
      configUpdatedAt: '2026-04-05T23:57:00Z',
      enabled: true,
      enabledStateSource: 'runtime-default-enabled',
      enabledStatePersisted: false,
      statusSource: 'runtime-registry+runtime-dispatch-results',
      statusEvidence: 'runtime-dispatch-result:instance-config-reject',
      statusSummary:
        'last runtime event dispatch was rejected before subprocess launch via runtime-registry+runtime-dispatch-results; stage=instance-config; recovery=last-dispatch-failed; evidence=process-local-volatile: plugin host dispatch "plugin-echo": plugin "plugin-echo" instance config required property "prefix" must be provided',
      runtimeStateLive: true,
      statusPersisted: false,
      statusLevel: 'error',
      statusRecovery: 'last-dispatch-failed',
      statusStaleness: 'process-local-volatile',
      lastDispatchKind: 'event',
      lastDispatchSuccess: false,
      lastDispatchError: 'plugin host dispatch "plugin-echo": plugin "plugin-echo" instance config required property "prefix" must be provided',
      lastDispatchAt: '2026-04-05T23:59:00Z',
      lastRecoveredAt: '2026-04-05T23:58:00Z',
      lastRecoveryFailureCount: 2,
      currentFailureStreak: 1,
    },
    {
      id: 'plugin-admin',
      name: 'Admin Plugin',
      version: '0.2.0',
      apiVersion: 'v0',
      mode: 'builtin',
      permissions: [],
      entry: { module: 'plugins/plugin-admin' },
      enabled: true,
      enabledStateSource: 'runtime-default-enabled',
      enabledStatePersisted: false,
      statusSource: 'runtime-registry',
      statusEvidence: 'manifest-only',
      statusSummary: 'manifest registered without live runtime evidence yet',
      runtimeStateLive: false,
      statusPersisted: false,
      statusLevel: 'info',
      statusRecovery: 'no-runtime-evidence',
      statusStaleness: 'static-registration',
    },
  ],
  jobs: [
    {
      id: 'job-console',
      type: 'ai.call',
      status: 'pending',
      retryCount: 0,
      maxRetries: 1,
      timeout: 30000000000,
      lastError: '',
      createdAt: '2026-04-06T00:00:00Z',
      startedAt: null,
      finishedAt: null,
      nextRunAt: null,
      deadLetter: false,
      correlation: 'corr-console',
      payload: {},
      dispatchReady: true,
      queueStateSummary: 'ready',
      recoverySummary: '',
    },
  ],
  schedules: [
    {
      id: 'schedule-console',
      kind: 'delay',
      source: 'runtime-demo-scheduler',
      eventType: 'message.received',
      cronExpr: '',
      delayMs: 30000,
      executeAt: null,
      dueAt: '2026-04-06T00:00:30Z',
      dueReady: false,
      dueStateSummary: 'scheduled',
      scheduleSummary: 'message.received | delay scheduled at 2026-04-06T00:00:30Z',
      createdAt: '2026-04-06T00:00:00Z',
      updatedAt: '2026-04-06T00:00:00Z',
    },
  ],
  logs: ['runtime started', 'plugin-echo ready'],
  config: {
    Runtime: {
      Environment: 'development',
      LogLevel: 'debug',
      HTTPPort: 8080,
    },
  },
  meta: {
    runtime_entry: 'apps/runtime',
    demo_paths: ['/demo/onebot/message', '/demo/ai/message'],
    sqlite_path: 'data/dev/runtime.sqlite',
    scheduler_interval_ms: 100,
    ai_job_dispatcher_registered: true,
    console_mode: 'read-only',
  },
  observability: {
    jobDispatchReady: 1,
    scheduleDueReady: 0,
    jobStateSource: 'sqlite-jobs',
    scheduleStateSource: 'sqlite-schedule-plans',
    logStateSource: 'runtime-log-buffer',
    traceStateSource: 'runtime-trace-recorder',
    metricsStateSource: 'runtime-metrics-registry',
    verificationEndpoints: ['GET /api/console', 'GET /metrics'],
    summary: 'jobs=sqlite-jobs ready=1 | schedules=sqlite-schedule-plans due=0 | metrics=runtime-metrics-registry | logs=runtime-log-buffer | traces=runtime-trace-recorder',
  },
};

describe('App', () => {
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    window.history.replaceState({}, '', '/');
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
    window.history.replaceState({}, '', '/');
  });

  it('fetches the live console payload and renders the read-only panel with plugin_id lookup state', async () => {
    window.history.replaceState({}, '', '/?log_query=plugin-echo&job_query=ai.call&plugin_id=plugin-echo');
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => consolePayload,
    });
    globalThis.fetch = fetchMock as typeof fetch;

    render(<App />);

    expect(screen.getByText('Loading read-only operations panel')).toBeInTheDocument();

    await screen.findByText('Read-only operations panel');

    expect(screen.getByRole('heading', { name: 'Adapter lifecycle' })).toBeInTheDocument();
    const adapterRow = screen.getByText('adapter-onebot-demo').closest('tr');
    expect(adapterRow).not.toBeNull();
    expect(within(adapterRow as HTMLTableRowElement).getByText('onebot / onebot')).toBeInTheDocument();
    expect(within(adapterRow as HTMLTableRowElement).getByText('registered')).toBeInTheDocument();
    expect(within(adapterRow as HTMLTableRowElement).getByText('ready')).toBeInTheDocument();
    expect(within(adapterRow as HTMLTableRowElement).getByText('online')).toBeInTheDocument();
    expect(within(adapterRow as HTMLTableRowElement).getByText('2026-04-06T00:00:00Z')).toBeInTheDocument();
    expect(
      within(adapterRow as HTMLTableRowElement).getByText('registered adapter lifecycle facts from sqlite-adapter-instances'),
    ).toBeInTheDocument();
    expect(screen.getByText('Echo Plugin')).toBeInTheDocument();
    expect(screen.getByText('event failed')).toBeInTheDocument();
    expect(screen.getByText('last dispatch at: 2026-04-05T23:59:00Z')).toBeInTheDocument();
    expect(screen.getByText('evidence time scope: this process only, not persisted history')).toBeInTheDocument();
    expect(screen.getByText('dispatch kind: event')).toBeInTheDocument();
    expect(screen.getAllByText('recovery: last-dispatch-failed')).toHaveLength(2);
    expect(screen.getAllByText('evidence staleness: process-local-volatile')).toHaveLength(2);
    expect(screen.getByText('current failure streak: 1')).toBeInTheDocument();
    expect(screen.getByText('last recovered at: 2026-04-05T23:58:00Z')).toBeInTheDocument();
    expect(screen.getByText('failures before recovery: 2')).toBeInTheDocument();
    expect(screen.getByText('dispatch stopped before subprocess launch: instance-config rejected')).toBeInTheDocument();
    expect(screen.getByText('plugin host dispatch "plugin-echo": plugin "plugin-echo" instance config required property "prefix" must be provided')).toBeInTheDocument();
    expect(screen.getByText('runtime-registry+runtime-dispatch-results')).toBeInTheDocument();
    expect(screen.getByText('runtime-dispatch-result:instance-config-reject')).toBeInTheDocument();
    expect(screen.getByText('plugin config: plugin-owned-persisted-input via sqlite-plugin-config')).toBeInTheDocument();
    expect(screen.getByText('plugin config updated at: 2026-04-05T23:57:00Z')).toBeInTheDocument();
    expect(screen.getAllByText('enabled overlay: runtime-default-enabled / enabled / runtime default only')).toHaveLength(2);
    expect(screen.getByText('publish source type: git')).toBeInTheDocument();
    expect(screen.getByText('runtime version range: >=0.1.0 <1.0.0')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'https://github.com/ohmyopencode/bot-platform/tree/main/plugins/plugin-echo' })).toBeInTheDocument();
    expect(screen.getByText('publish metadata: not declared in this registered plugin manifest')).toBeInTheDocument();
    expect(screen.getByText('status snapshot: runtime-registry+runtime-dispatch-results / runtime-dispatch-result:instance-config-reject')).toBeInTheDocument();
    expect(screen.getByText('last runtime event dispatch was rejected before subprocess launch via runtime-registry+runtime-dispatch-results; stage=instance-config; recovery=last-dispatch-failed; evidence=process-local-volatile: plugin host dispatch "plugin-echo": plugin "plugin-echo" instance config required property "prefix" must be provided')).toBeInTheDocument();
    expect(screen.getByText('last dispatch at: none observed in this process')).toBeInTheDocument();
    expect(screen.getByText('runtime started')).toBeInTheDocument();
    expect(screen.getByText('apps/runtime')).toBeInTheDocument();
    expect(screen.getByText('data/dev/runtime.sqlite')).toBeInTheDocument();
    expect(screen.getByText('Schedule list')).toBeInTheDocument();
    expect(screen.getByText('Async observability')).toBeInTheDocument();
    expect(screen.getByText('schedule-console')).toBeInTheDocument();
    expect(screen.getByText('Jobs dispatch-ready')).toBeInTheDocument();
    expect(screen.getByText('runtime-metrics-registry')).toBeInTheDocument();
    const jobRow = screen.getByText('job-console').closest('tr');
    expect(jobRow).not.toBeNull();
    expect(within(jobRow as HTMLTableRowElement).getByText('ready')).toBeInTheDocument();
    expect(screen.getByLabelText('Log filter')).toHaveDisplayValue('plugin-echo');
    expect(screen.getByLabelText('Job filter')).toHaveDisplayValue('ai.call');
    expect(screen.getByLabelText('Plugin ID')).toHaveDisplayValue('plugin-echo');

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledTimes(1);
    });

    const requestURL = fetchMock.mock.calls[0]?.[0];
    expect(requestURL).toBeInstanceOf(URL);
    expect((requestURL as URL).pathname).toBe('/api/console');
    expect((requestURL as URL).searchParams.get('log_query')).toBe('plugin-echo');
    expect((requestURL as URL).searchParams.get('job_query')).toBe('ai.call');
    expect((requestURL as URL).searchParams.get('plugin_id')).toBe('plugin-echo');
  });

  it('omits plugin_id from the request when the query is blank or whitespace-only', async () => {
    window.history.replaceState({}, '', '/?plugin_id=%20%20');
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => consolePayload,
    });
    globalThis.fetch = fetchMock as typeof fetch;

    render(<App />);

    await screen.findByText('Read-only operations panel');
    expect(screen.getByLabelText('Plugin ID')).toHaveDisplayValue('  ');

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledTimes(1);
    });

    const requestURL = fetchMock.mock.calls[0]?.[0] as URL;
    expect(requestURL.searchParams.get('plugin_id')).toBeNull();
    expect(window.location.search).toBe('');
  });

  it('keeps unknown plugin_id visible and renders the filtered empty state', async () => {
    window.history.replaceState({}, '', '/?plugin_id=plugin-missing');
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ ...consolePayload, plugins: [] }),
    });
    globalThis.fetch = fetchMock as typeof fetch;

    render(<App />);

    await screen.findByText('Read-only operations panel');

    expect(screen.getByLabelText('Plugin ID')).toHaveDisplayValue('plugin-missing');
    expect(screen.getByText('No plugins match the current plugin ID filter.')).toBeInTheDocument();

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledTimes(1);
    });

    const requestURL = fetchMock.mock.calls[0]?.[0] as URL;
    expect(requestURL.searchParams.get('plugin_id')).toBe('plugin-missing');
  });

  it('locates a plugin from the plugin table and reuses the existing plugin_id filter state, URL, and request chain', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => consolePayload,
    });
    globalThis.fetch = fetchMock as typeof fetch;

    render(<App />);

    await screen.findByText('Read-only operations panel');

    fireEvent.click(screen.getByRole('button', { name: 'Filter to plugin plugin-admin' }));

    await waitFor(() => {
      expect(screen.getByLabelText('Plugin ID')).toHaveDisplayValue('plugin-admin');
    });

    await waitFor(() => {
      expect(window.location.search).toBe('?plugin_id=plugin-admin');
    });

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledTimes(2);
    });

    const requestURL = fetchMock.mock.calls[1]?.[0] as URL;
    expect(requestURL.searchParams.get('plugin_id')).toBe('plugin-admin');
    expect(screen.getByRole('button', { name: 'Currently filtered to plugin plugin-admin' })).toBeDisabled();
  });

  it('keeps the locate action stable for an already-filtered single plugin result', async () => {
    window.history.replaceState({}, '', '/?plugin_id=plugin-echo');
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ ...consolePayload, plugins: [consolePayload.plugins[0]] }),
    });
    globalThis.fetch = fetchMock as typeof fetch;

    render(<App />);

    await screen.findByText('Read-only operations panel');

    expect(screen.getByLabelText('Plugin ID')).toHaveDisplayValue('plugin-echo');
    expect(screen.getByRole('button', { name: 'Currently filtered to plugin plugin-echo' })).toBeDisabled();

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledTimes(1);
    });

    expect(window.location.search).toBe('?plugin_id=plugin-echo');
  });

  it('prioritizes failing and recently recovered plugins with visible table labels', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        ...consolePayload,
        status: { ...consolePayload.status, plugins: 3 },
        plugins: [
          { ...consolePayload.plugins[1] },
          {
            ...consolePayload.plugins[0],
            id: 'plugin-recovered',
            name: 'Recovered Plugin',
            statusEvidence: 'runtime-dispatch-result',
            statusSummary:
              'last runtime event dispatch success via runtime-registry+runtime-dispatch-results; recovery=recovered-after-failures; evidence=process-local-volatile',
            statusRecovery: 'recovered-after-failures',
            lastDispatchSuccess: true,
            lastDispatchError: '',
            lastRecoveredAt: '2026-04-06T00:01:00Z',
            lastRecoveryFailureCount: 2,
            currentFailureStreak: 0,
          },
          {
            ...consolePayload.plugins[0],
            id: 'plugin-failing',
            name: 'Failing Plugin',
            currentFailureStreak: 3,
          },
        ],
      }),
    });
    globalThis.fetch = fetchMock as typeof fetch;

    render(<App />);

    await screen.findByText('Read-only operations panel');

    const pluginTableBody = screen.getByText('Failing Plugin').closest('tbody');
    expect(pluginTableBody).not.toBeNull();

    const pluginRows = within(pluginTableBody as HTMLTableSectionElement).getAllByRole('row');
    expect(pluginRows[0]).toHaveTextContent('Failing Plugin');
    expect(pluginRows[0]).toHaveTextContent('Needs attention');
    expect(pluginRows[1]).toHaveTextContent('Recovered Plugin');
    expect(pluginRows[1]).toHaveTextContent('Recovered recently');
    expect(pluginRows[2]).toHaveTextContent('Admin Plugin');
  });

  it('shows attention summary counts and filters the plugin table to failing or recovered entries only', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        ...consolePayload,
        status: { ...consolePayload.status, plugins: 3 },
        plugins: [
          { ...consolePayload.plugins[1] },
          {
            ...consolePayload.plugins[0],
            id: 'plugin-recovered',
            name: 'Recovered Plugin',
            statusEvidence: 'runtime-dispatch-result',
            statusSummary:
              'last runtime event dispatch success via runtime-registry+runtime-dispatch-results; recovery=recovered-after-failures; evidence=process-local-volatile',
            statusRecovery: 'recovered-after-failures',
            lastDispatchSuccess: true,
            lastDispatchError: '',
            lastRecoveredAt: '2026-04-06T00:01:00Z',
            lastRecoveryFailureCount: 2,
            currentFailureStreak: 0,
          },
          {
            ...consolePayload.plugins[0],
            id: 'plugin-failing',
            name: 'Failing Plugin',
            currentFailureStreak: 3,
          },
        ],
      }),
    });
    globalThis.fetch = fetchMock as typeof fetch;

    render(<App />);

    await screen.findByText('Read-only operations panel');

    expect(screen.getByText('1 failing / 1 recovered')).toBeInTheDocument();
    expect(screen.getByLabelText('Attention only')).not.toBeChecked();

    fireEvent.click(screen.getByLabelText('Attention only'));

    expect(screen.getByLabelText('Attention only')).toBeChecked();
    expect(screen.getByText('Failing Plugin')).toBeInTheDocument();
    expect(screen.getByText('Recovered Plugin')).toBeInTheDocument();
    expect(screen.queryByText('Admin Plugin')).not.toBeInTheDocument();
  });

  it('keeps healthy plugin order stable and omits attention labels without failure or recovery facts', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        ...consolePayload,
        plugins: [
          { ...consolePayload.plugins[1] },
          {
            ...consolePayload.plugins[0],
            id: 'plugin-stable',
            name: 'Stable Plugin',
            statusEvidence: 'runtime-dispatch-result',
            statusSummary:
              'last runtime event dispatch success via runtime-registry+runtime-dispatch-results; recovery=last-dispatch-succeeded; evidence=process-local-volatile',
            statusRecovery: 'last-dispatch-succeeded',
            lastDispatchSuccess: true,
            lastDispatchError: '',
            lastRecoveredAt: undefined,
            lastRecoveryFailureCount: undefined,
            currentFailureStreak: 0,
          },
        ],
      }),
    });
    globalThis.fetch = fetchMock as typeof fetch;

    render(<App />);

    await screen.findByText('Read-only operations panel');

    const pluginTableBody = screen.getByText('Admin Plugin').closest('tbody');
    expect(pluginTableBody).not.toBeNull();

    const pluginRows = within(pluginTableBody as HTMLTableSectionElement).getAllByRole('row');
    expect(pluginRows[0]).toHaveTextContent('Admin Plugin');
    expect(pluginRows[1]).toHaveTextContent('Stable Plugin');
    expect(screen.queryByText('Needs attention')).not.toBeInTheDocument();
    expect(screen.queryByText('Recovered recently')).not.toBeInTheDocument();
  });

  it('shows an attention-only empty state when the current plugin result set has no failing or recovered entries', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        ...consolePayload,
        plugins: [
          {
            ...consolePayload.plugins[1],
            id: 'plugin-stable',
            name: 'Stable Plugin',
            statusEvidence: 'runtime-dispatch-result',
            statusSummary:
              'last runtime event dispatch success via runtime-registry+runtime-dispatch-results; recovery=last-dispatch-succeeded; evidence=process-local-volatile',
            statusRecovery: 'last-dispatch-succeeded',
            lastDispatchSuccess: true,
            lastDispatchError: '',
            lastRecoveredAt: undefined,
            lastRecoveryFailureCount: undefined,
            currentFailureStreak: 0,
          },
        ],
      }),
    });
    globalThis.fetch = fetchMock as typeof fetch;

    render(<App />);

    await screen.findByText('Read-only operations panel');

    expect(screen.getByText('0 failing / 0 recovered')).toBeInTheDocument();

    fireEvent.click(screen.getByLabelText('Attention only'));

    expect(screen.getByText('No failing or recently recovered plugins in the current result set.')).toBeInTheDocument();
    expect(screen.queryByText('Stable Plugin')).not.toBeInTheDocument();
  });

  it('renders conservative runtime evidence time text when a dispatch timestamp is unavailable', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        ...consolePayload,
        plugins: [
          {
            ...consolePayload.plugins[0],
            id: 'plugin-runtime-no-time',
            name: 'Runtime No Time Plugin',
            statusEvidence: 'runtime-dispatch-result',
            statusSummary:
              'last runtime event dispatch success via runtime-registry+runtime-dispatch-results; recovery=last-dispatch-succeeded; evidence=process-local-volatile',
            statusRecovery: 'last-dispatch-succeeded',
            lastDispatchSuccess: true,
            lastDispatchError: '',
            lastDispatchAt: undefined,
            lastRecoveredAt: undefined,
            lastRecoveryFailureCount: undefined,
            currentFailureStreak: 0,
          },
        ],
      }),
    });
    globalThis.fetch = fetchMock as typeof fetch;

    render(<App />);

    await screen.findByText('Read-only operations panel');

    expect(screen.getByText('Runtime No Time Plugin')).toBeInTheDocument();
    expect(
      screen.getByText('last dispatch at: runtime evidence observed in this process, timestamp unavailable'),
    ).toBeInTheDocument();
    expect(screen.getByText('evidence time scope: this process only, not persisted history')).toBeInTheDocument();
  });

  it('renders conservative plugin recovery facts for success-only history without inventing a recovery event', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        ...consolePayload,
        plugins: [
          {
            ...consolePayload.plugins[0],
            id: 'plugin-stable',
            name: 'Stable Plugin',
            statusEvidence: 'runtime-dispatch-result',
            statusSummary: 'last runtime event dispatch success via runtime-registry+runtime-dispatch-results; recovery=last-dispatch-succeeded; evidence=process-local-volatile',
            statusRecovery: 'last-dispatch-succeeded',
            lastDispatchSuccess: true,
            lastDispatchError: '',
            lastRecoveredAt: undefined,
            lastRecoveryFailureCount: undefined,
            currentFailureStreak: 0,
          },
        ],
      }),
    });
    globalThis.fetch = fetchMock as typeof fetch;

    render(<App />);

    await screen.findByText('Read-only operations panel');

    expect(screen.getByText('Stable Plugin')).toBeInTheDocument();
    expect(screen.getByText('event success')).toBeInTheDocument();
    expect(screen.getByText('current failure streak: 0')).toBeInTheDocument();
    expect(screen.getByText('last recovered at: none observed in this process')).toBeInTheDocument();
    expect(screen.queryByText(/^failures before recovery:/)).not.toBeInTheDocument();
  });
});

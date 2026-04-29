import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import {
  DEFAULT_OPERATOR_BEARER_TOKEN,
  fetchConsolePayload,
  getConsoleApiURL,
  getStoredAutoRefresh,
  getStoredOperatorBearerToken,
  persistAutoRefresh,
  persistOperatorBearerToken,
  postOperatorAction,
} from './consoleApiClient';
import type {
  AuditEntry,
  ConsoleAuthorizationPolicy,
  ConsoleConfig,
  ConsoleFilters,
  ConsoleRolloutPolicyDeclaration,
  ConsolePayload,
  Job,
  PluginManifest,
  RolloutOperation,
  Schedule,
} from './types';

const consoleApiURL = getConsoleApiURL();
const AUTO_REFRESH_MS = 15_000;

type ConsoleSection = 'overview' | 'plugins' | 'jobs' | 'schedules' | 'adapters' | 'workflows';

type ConsoleRoute =
  | { section: 'overview' }
  | { section: 'plugins'; pluginId?: string }
  | { section: 'jobs'; jobId?: string }
  | { section: 'schedules'; scheduleId?: string }
  | { section: 'adapters'; adapterId?: string }
  | { section: 'workflows'; workflowId?: string };

type ActionState =
  | { kind: 'idle' }
  | { kind: 'pending'; label: string }
  | { kind: 'success'; label: string; details: Record<string, unknown>; occurredAt: string }
  | { kind: 'verification-error'; label: string; details: Record<string, unknown>; message: string; occurredAt: string }
  | { kind: 'error'; label: string; message: string; occurredAt: string };

type PanelProps = {
  title: string;
  description?: string;
  actions?: ReactNode;
  tone?: 'default' | 'error' | 'muted';
  children: ReactNode;
};

type RouteLinkProps = {
  to: ConsoleRoute;
  navigate: (route: ConsoleRoute) => void;
  children: ReactNode;
  className?: string;
  ariaLabel?: string;
};

type DetailFieldProps = {
  label: string;
  value: ReactNode;
};

type StatusTone = 'default' | 'good' | 'danger' | 'warning' | 'muted';

type JobOperatorActionDescriptor = {
  key: 'pause' | 'resume' | 'cancel' | 'retry';
  title: string;
  description: string;
  buttonLabel: string;
  requestLabel: string;
  permission: string;
  path: string;
};

type RolloutSnapshot = ConsolePayload['rolloutHeads'][number]['stable'];

function parseRoute(pathname: string): ConsoleRoute {
  const segments = pathname
    .split('/')
    .filter(Boolean)
    .map((segment) => decodeURIComponent(segment));

  if (segments.length === 0) {
    return { section: 'overview' };
  }

  const [section, entityId] = segments;
  switch (section) {
    case 'plugins':
      return entityId ? { section: 'plugins', pluginId: entityId } : { section: 'plugins' };
    case 'jobs':
      return entityId ? { section: 'jobs', jobId: entityId } : { section: 'jobs' };
    case 'schedules':
      return entityId ? { section: 'schedules', scheduleId: entityId } : { section: 'schedules' };
    case 'adapters':
      return entityId ? { section: 'adapters', adapterId: entityId } : { section: 'adapters' };
    case 'workflows':
      return entityId ? { section: 'workflows', workflowId: entityId } : { section: 'workflows' };
    default:
      return { section: 'overview' };
  }
}

function buildRoutePath(route: ConsoleRoute): string {
  switch (route.section) {
    case 'overview':
      return '/';
    case 'plugins':
      return route.pluginId ? `/plugins/${encodeURIComponent(route.pluginId)}` : '/plugins';
    case 'jobs':
      return route.jobId ? `/jobs/${encodeURIComponent(route.jobId)}` : '/jobs';
    case 'schedules':
      return route.scheduleId ? `/schedules/${encodeURIComponent(route.scheduleId)}` : '/schedules';
    case 'adapters':
      return route.adapterId ? `/adapters/${encodeURIComponent(route.adapterId)}` : '/adapters';
    case 'workflows':
      return route.workflowId ? `/workflows/${encodeURIComponent(route.workflowId)}` : '/workflows';
  }
}

function readFilters(search: string): ConsoleFilters {
  const params = new URLSearchParams(search);
  return {
    logQuery: params.get('log_query') ?? '',
    jobQuery: params.get('job_query') ?? '',
    pluginID: params.get('plugin_id') ?? '',
  };
}

function buildSearch(filters: ConsoleFilters): string {
  const params = new URLSearchParams();
  if (filters.logQuery.trim() !== '') {
    params.set('log_query', filters.logQuery.trim());
  }
  if (filters.jobQuery.trim() !== '') {
    params.set('job_query', filters.jobQuery.trim());
  }
  if (filters.pluginID.trim() !== '') {
    params.set('plugin_id', filters.pluginID.trim());
  }
  const query = params.toString();
  return query === '' ? '' : `?${query}`;
}

function formatTimestamp(value?: string | null): string {
  if (!value) {
    return '—';
  }
  return value;
}

function formatDuration(durationNs: number): string {
  if (durationNs % 1_000_000_000 === 0) {
    return `${durationNs / 1_000_000_000}s`;
  }
  if (durationNs % 1_000_000 === 0) {
    return `${durationNs / 1_000_000}ms`;
  }
  return `${durationNs}ns`;
}

function uniqueStrings(items: string[]): string[] {
  return [...new Set(items.filter((item) => item.trim() !== ''))];
}

function formatStringList(items: string[] | undefined): string {
  if (!items || items.length === 0) {
    return '—';
  }
  return items.join(', ');
}

function formatMetaString(value: unknown, fallback = 'not declared'): string {
  if (typeof value !== 'string' || value.trim() === '') {
    return fallback;
  }
  return value;
}

function formatMetaBoolean(value: unknown): string {
  if (typeof value !== 'boolean') {
    return 'not declared';
  }
  return value ? 'true' : 'false';
}

function formatMetaStringList(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.filter((item): item is string => typeof item === 'string' && item.trim() !== '');
}

function formatRolloutSnapshot(snapshot?: RolloutSnapshot | null): string {
  if (!snapshot) {
    return 'not returned';
  }
  const parts = [snapshot.version ?? '', snapshot.apiVersion ? `api ${snapshot.apiVersion}` : '', snapshot.mode ?? ''].filter(
    (part) => part.trim() !== '',
  );
  if (parts.length === 0) {
    return 'not returned';
  }
  return parts.join(' · ');
}

function rolloutPolicyDetails(policy: ConsoleRolloutPolicyDeclaration | undefined): Array<{ label: string; items: string[] }> {
  if (!policy) {
    return [];
  }
  return [
    { label: 'Entry points', items: policy.entryPoints ?? [] },
    { label: 'Preflight checks', items: policy.preflightChecks ?? [] },
    { label: 'Activation checks', items: policy.activationChecks ?? [] },
    { label: 'Audit reasons', items: policy.auditReasons ?? [] },
    { label: 'Supported modes', items: policy.supportedModes ?? [] },
    { label: 'Unsupported modes', items: policy.unsupportedModes ?? [] },
    { label: 'Verification endpoints', items: policy.verificationEndpoints ?? [] },
    { label: 'Facts', items: policy.facts ?? [] },
  ].filter((section) => section.items.length > 0);
}

function getStatusToneFromRolloutStatus(status?: string | null): StatusTone {
  if (!status || status.trim() === '') {
    return 'muted';
  }
  const normalized = status.toLowerCase();
  if (
    normalized.includes('failed') ||
    normalized.includes('error') ||
    normalized.includes('invalid') ||
    normalized.includes('drift') ||
    normalized.includes('denied')
  ) {
    return 'danger';
  }
  if (
    normalized.includes('active') ||
    normalized.includes('activated') ||
    normalized.includes('succeeded') ||
    normalized.includes('complete') ||
    normalized.includes('stable')
  ) {
    return 'good';
  }
  if (normalized.includes('canary') || normalized.includes('prepare') || normalized.includes('rollback')) {
    return 'warning';
  }
  return 'default';
}

function sortRolloutOperations(operations: RolloutOperation[]): RolloutOperation[] {
  return [...operations].sort((left, right) => {
    const leftTimestamp = left.updatedAt ?? left.occurredAt ?? '';
    const rightTimestamp = right.updatedAt ?? right.occurredAt ?? '';
    return rightTimestamp.localeCompare(leftTimestamp);
  });
}

function formatResolvedIdentityField(value?: string | null): string {
  if (!value || value.trim() === '') {
    return 'not returned';
  }
  return value;
}

function formatAuditObservability(entry: AuditEntry): string {
  const parts = [
    entry.trace_id ? `trace ${entry.trace_id}` : '',
    entry.event_id ? `event ${entry.event_id}` : '',
    entry.plugin_id ? `plugin ${entry.plugin_id}` : '',
    entry.run_id ? `run ${entry.run_id}` : '',
    entry.correlation_id ? `correlation ${entry.correlation_id}` : '',
    entry.error_category ? `category ${entry.error_category}` : '',
    entry.error_code ? `code ${entry.error_code}` : '',
  ].filter((part) => part !== '');
  return parts.join(' · ');
}

function getPluginAttentionState(plugin: PluginManifest): 'failing' | 'recovered' | 'normal' {
  if ((plugin.currentFailureStreak ?? 0) > 0) {
    return 'failing';
  }
  if (plugin.lastRecoveredAt) {
    return 'recovered';
  }
  return 'normal';
}

function getStatusToneFromPlugin(plugin: PluginManifest): StatusTone {
  const state = getPluginAttentionState(plugin);
  if (state === 'failing') {
    return 'danger';
  }
  if (state === 'recovered') {
    return 'warning';
  }
  if (!plugin.enabled) {
    return 'muted';
  }
  return 'good';
}

function getStatusToneFromJob(job: Job): StatusTone {
  if (job.deadLetter || job.status === 'dead' || job.status === 'failed') {
    return 'danger';
  }
  if (job.status === 'paused' || job.status === 'retrying') {
    return 'warning';
  }
  if (job.status === 'done') {
    return 'good';
  }
  if (job.status === 'pending' || job.status === 'running') {
    return 'default';
  }
  return 'muted';
}

function supportsOperatorAction(availableActions: string[] | undefined, path: string): boolean {
  return availableActions?.includes(path) ?? false;
}

function getJobOperatorActions(meta: ConsolePayload['meta'], job: Job): JobOperatorActionDescriptor[] {
  const actions: JobOperatorActionDescriptor[] = [];

  if (!job.deadLetter && (job.status === 'pending' || job.status === 'retrying') && supportsOperatorAction(meta.job_operator_actions, '/demo/jobs/{job-id}/pause')) {
    actions.push({
      key: 'pause',
      title: 'Pause queued job',
      description: 'Pause keeps a queued job from dispatching again until a later resume, then the route refetch shows the runtime queue truth.',
      buttonLabel: 'Pause job',
      requestLabel: 'Pause',
      permission: 'job:pause',
      path: `/demo/jobs/${job.id}/pause`,
    });
  }

  if (!job.deadLetter && job.status === 'paused' && supportsOperatorAction(meta.job_operator_actions, '/demo/jobs/{job-id}/resume')) {
    actions.push({
      key: 'resume',
      title: 'Resume queued job',
      description: 'Resume hands a paused job back to the runtime queue and relies on the follow-up console read to confirm the current state.',
      buttonLabel: 'Resume job',
      requestLabel: 'Resume',
      permission: 'job:resume',
      path: `/demo/jobs/${job.id}/resume`,
    });
  }

  if (
    !job.deadLetter &&
    (job.status === 'pending' || job.status === 'retrying' || job.status === 'paused') &&
    supportsOperatorAction(meta.job_operator_actions, '/demo/jobs/{job-id}/cancel')
  ) {
    actions.push({
      key: 'cancel',
      title: 'Cancel queued job',
      description:
        'Cancel stops further queued dispatch attempts for pending, retrying, or paused jobs through the existing runtime endpoint instead of inventing any client-side authority.',
      buttonLabel: 'Cancel job',
      requestLabel: 'Cancel',
      permission: 'job:cancel',
      path: `/demo/jobs/${job.id}/cancel`,
    });
  }

  if (job.deadLetter && supportsOperatorAction(meta.job_operator_actions, '/demo/jobs/{job-id}/retry')) {
    actions.push({
      key: 'retry',
      title: 'Retry dead-letter job',
      description: 'Retry stays limited to dead-letter jobs, then the route refetches /api/console so alerts and queue evidence can converge.',
      buttonLabel: 'Retry dead-letter job',
      requestLabel: 'Retry',
      permission: 'job:retry',
      path: `/demo/jobs/${job.id}/retry`,
    });
  }

  return actions;
}

function getStatusToneFromSchedule(schedule: Schedule): StatusTone {
  if (schedule.overdue) {
    return 'danger';
  }
  if (schedule.dueReady) {
    return 'warning';
  }
  return 'default';
}

function matchesPluginScope(policy: ConsoleAuthorizationPolicy | undefined, target: string): boolean {
  if (!policy) {
    return false;
  }
  const scopes = policy.pluginScope ?? [];
  return scopes.includes('*') || scopes.includes(target);
}

function actorRoles(config: ConsoleConfig | undefined, actor: string): string[] {
  if (!config?.RBAC) {
    return [];
  }
  return config.RBAC.ActorRoles[actor] ?? [];
}

function actorPermissions(config: ConsoleConfig | undefined, actor: string): string[] {
  if (!config?.RBAC) {
    return [];
  }
  const permissions: string[] = [];
  for (const role of actorRoles(config, actor)) {
    const policy = config.RBAC.Policies[role];
    if (!policy) {
      continue;
    }
    permissions.push(...policy.permissions);
  }
  return uniqueStrings(permissions);
}

function actorCan(config: ConsoleConfig | undefined, actor: string, permission: string, target: string): boolean {
  if (!config?.RBAC || actor.trim() === '') {
    return false;
  }
  return actorRoles(config, actor).some((role) => {
    const policy = config.RBAC?.Policies[role];
    return policy !== undefined && policy.permissions.includes(permission) && matchesPluginScope(policy, target);
  });
}

function actorsLikelyAllowed(config: ConsoleConfig | undefined, permission: string, target: string): string[] {
  if (!config?.RBAC) {
    return [];
  }
  return Object.keys(config.RBAC.ActorRoles).filter((actor) => actorCan(config, actor, permission, target));
}

function sectionLabel(section: ConsoleSection): string {
  switch (section) {
    case 'overview':
      return 'Overview';
    case 'plugins':
      return 'Plugins';
    case 'jobs':
      return 'Jobs';
    case 'schedules':
      return 'Schedules';
    case 'adapters':
      return 'Adapters';
    case 'workflows':
      return 'Workflows';
  }
}

function lineMatches(line: string, terms: string[]): boolean {
  if (terms.length === 0) {
    return true;
  }
  const lower = line.toLowerCase();
  return terms.some((term) => lower.includes(term.toLowerCase()));
}

function relatedLogs(lines: string[], ...terms: Array<string | undefined>): string[] {
  const normalized = terms.map((term) => term?.trim() ?? '').filter((term) => term !== '');
  return lines.filter((line) => lineMatches(line, normalized)).slice(0, 8);
}

function relatedAudits(audits: AuditEntry[], target: string): AuditEntry[] {
  return audits
    .filter((entry) => entry.target === target)
    .sort((left, right) => right.occurred_at.localeCompare(left.occurred_at))
    .slice(0, 8);
}

function scheduleConfigSummary(schedule: Schedule): string {
  if (schedule.kind === 'cron') {
    return schedule.cronExpr || '—';
  }
  if (schedule.kind === 'delay') {
    return `${schedule.delayMs}ms`;
  }
  return schedule.executeAt ?? '—';
}

function Panel({ title, description, actions, tone = 'default', children }: PanelProps) {
  return (
    <section className={`panel panel-${tone}`}>
      <div className="panel-header">
        <div>
          <h2>{title}</h2>
          {description ? <p>{description}</p> : null}
        </div>
        {actions ? <div className="panel-actions">{actions}</div> : null}
      </div>
      {children}
    </section>
  );
}

function RouteLink({ to, navigate, children, className, ariaLabel }: RouteLinkProps) {
  return (
    <a
      href={buildRoutePath(to)}
      className={className}
      aria-label={ariaLabel}
      onClick={(event) => {
        event.preventDefault();
        navigate(to);
      }}
    >
      {children}
    </a>
  );
}

function DetailField({ label, value }: DetailFieldProps) {
  return (
    <div className="detail-field">
      <span className="detail-label">{label}</span>
      <div className="detail-value">{value}</div>
    </div>
  );
}

function StatusPill(props: { label: string; tone?: StatusTone }) {
  const { label, tone = 'default' } = props;
  return <span className={`status-pill status-pill-${tone}`}>{label}</span>;
}

function EmptyState(props: { title: string; description: string }) {
  return (
    <div className="empty-state-card">
      <strong>{props.title}</strong>
      <p>{props.description}</p>
    </div>
  );
}

function App() {
  const [route, setRoute] = useState<ConsoleRoute>(() => parseRoute(window.location.pathname));
  const [filters, setFilters] = useState<ConsoleFilters>(() => readFilters(window.location.search));
  const [data, setData] = useState<ConsolePayload | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [lastFetchedAt, setLastFetchedAt] = useState<string>('');
  const [actionState, setActionState] = useState<ActionState>({ kind: 'idle' });
  const [currentBearerToken, setCurrentBearerToken] = useState(() => getStoredOperatorBearerToken());
  const [bearerTokenDraft, setBearerTokenDraft] = useState(() => getStoredOperatorBearerToken());
  const [autoRefresh, setAutoRefresh] = useState(() => getStoredAutoRefresh());
  const [pluginEchoPrefixDraft, setPluginEchoPrefixDraft] = useState('');
  const [pluginEchoDirty, setPluginEchoDirty] = useState(false);
  const hasLoadedOnce = useRef(false);

  const effectiveFilters = useMemo<ConsoleFilters>(() => {
    return {
      logQuery: filters.logQuery,
      jobQuery: filters.jobQuery,
      pluginID: route.section === 'plugins' && route.pluginId ? route.pluginId : filters.pluginID,
    };
  }, [filters, route]);

  const navigate = useCallback(
    (nextRoute: ConsoleRoute) => {
      setRoute(nextRoute);
      const nextURL = `${buildRoutePath(nextRoute)}${buildSearch(filters)}`;
      window.history.pushState(null, '', nextURL);
    },
    [filters],
  );

  useEffect(() => {
    const nextURL = `${buildRoutePath(route)}${buildSearch(filters)}`;
    if (`${window.location.pathname}${window.location.search}` !== nextURL) {
      window.history.replaceState(null, '', nextURL);
    }
  }, [route, filters]);

  useEffect(() => {
    function syncFromLocation() {
      setRoute(parseRoute(window.location.pathname));
      setFilters(readFilters(window.location.search));
    }

    window.addEventListener('popstate', syncFromLocation);
    return () => {
      window.removeEventListener('popstate', syncFromLocation);
    };
  }, []);

  const refreshConsole = useCallback(async (options?: { throwOnError?: boolean }) => {
    const initialLoad = !hasLoadedOnce.current;
    if (initialLoad) {
      setLoading(true);
    } else {
      setRefreshing(true);
    }

    try {
      const payload = await fetchConsolePayload(window.location.origin, currentBearerToken, effectiveFilters);
      hasLoadedOnce.current = true;
      setData(payload);
      setError('');
      setLastFetchedAt(new Date().toISOString());
      return payload;
    } catch (fetchError) {
      const message = fetchError instanceof Error ? fetchError.message : 'Failed to load Console API payload';
      setError(message);
      if (options?.throwOnError) {
        throw new Error(message);
      }
      return null;
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, [currentBearerToken, effectiveFilters]);

  useEffect(() => {
    void refreshConsole();
  }, [refreshConsole]);

  useEffect(() => {
    if (!autoRefresh) {
      return undefined;
    }

    const timer = window.setInterval(() => {
      void refreshConsole();
    }, AUTO_REFRESH_MS);

    return () => {
      window.clearInterval(timer);
    };
  }, [autoRefresh, refreshConsole]);

  const currentPlugin = useMemo(() => {
    if (!data || route.section !== 'plugins' || !route.pluginId) {
      return null;
    }
    return data.plugins.find((plugin) => plugin.id === route.pluginId) ?? null;
  }, [data, route]);

  useEffect(() => {
    if (route.section !== 'plugins' || route.pluginId !== 'plugin-echo') {
      setPluginEchoDirty(false);
      return;
    }

    if (pluginEchoDirty) {
      return;
    }

    const persistedPrefix = currentPlugin?.config?.prefix;
    setPluginEchoPrefixDraft(typeof persistedPrefix === 'string' ? persistedPrefix : '');
  }, [currentPlugin, pluginEchoDirty, route]);

  const applyBearerToken = useCallback(() => {
    const normalized = persistOperatorBearerToken(bearerTokenDraft);
    if (normalized === currentBearerToken) {
      void refreshConsole();
      return;
    }
    setCurrentBearerToken(normalized);
  }, [bearerTokenDraft, currentBearerToken, refreshConsole]);

  const toggleAutoRefresh = useCallback(() => {
    setAutoRefresh((current) => {
      const next = !current;
      persistAutoRefresh(next);
      return next;
    });
  }, []);

  const runAction = useCallback(
    async (label: string, path: string, body?: Record<string, unknown>) => {
      setActionState({ kind: 'pending', label });
      try {
        const result = await postOperatorAction(window.location.origin, currentBearerToken, path, body);
        try {
          await refreshConsole({ throwOnError: true });
          setActionState({
            kind: 'success',
            label,
            details: result,
            occurredAt: new Date().toISOString(),
          });
        } catch (refetchError) {
          setActionState({
            kind: 'verification-error',
            label,
            details: result,
            message:
              refetchError instanceof Error
                ? refetchError.message
                : 'The runtime write succeeded, but the follow-up console verification refetch failed',
            occurredAt: new Date().toISOString(),
          });
        }
      } catch (actionError) {
        setActionState({
          kind: 'error',
          label,
          message: actionError instanceof Error ? actionError.message : 'Operator action failed',
          occurredAt: new Date().toISOString(),
        });
      }
    },
    [currentBearerToken, refreshConsole],
  );

  const requestIdentity = data?.meta.request_identity;
  const resolvedActor = requestIdentity?.actor_id?.trim() ?? '';
  const currentRoles = useMemo(() => actorRoles(data?.config, resolvedActor), [data?.config, resolvedActor]);
  const currentPermissions = useMemo(() => actorPermissions(data?.config, resolvedActor), [data?.config, resolvedActor]);

  const pluginAttention = useMemo(() => {
    const plugins = data?.plugins ?? [];
    return {
      failing: plugins.filter((plugin) => getPluginAttentionState(plugin) === 'failing'),
      recovered: plugins.filter((plugin) => getPluginAttentionState(plugin) === 'recovered'),
    };
  }, [data?.plugins]);

  const recentAudits = useMemo(() => {
    return [...(data?.audits ?? [])].sort((left, right) => right.occurred_at.localeCompare(left.occurred_at)).slice(0, 8);
  }, [data?.audits]);

  const visibleLogs = data?.logs ?? [];
  const deadLetterJobs = useMemo(() => (data?.jobs ?? []).filter((job) => job.deadLetter), [data?.jobs]);
  const liveSchedules = data?.schedules ?? [];
  const workflows = data?.workflows ?? [];

  const summaryCards = data
    ? [
        { label: 'Adapters', value: data.status.adapters, section: 'adapters' as const },
        { label: 'Plugins', value: data.status.plugins, section: 'plugins' as const },
        { label: 'Jobs', value: data.status.jobs, section: 'jobs' as const },
        { label: 'Schedules', value: data.status.schedules, section: 'schedules' as const },
      ]
    : [];

  const actionBusy = actionState.kind === 'pending';

  function renderActionState() {
    if (actionState.kind === 'idle') {
      return null;
    }
    if (actionState.kind === 'pending') {
      return (
        <Panel title="Applying operator action" description={actionState.label} tone="muted">
          <p className="muted-copy">Waiting for the runtime write endpoint to return and then refetching /api/console with the current bearer token.</p>
        </Panel>
      );
    }
    if (actionState.kind === 'error') {
      return (
        <Panel title="Operator action failed" description={`${actionState.label} at ${formatTimestamp(actionState.occurredAt)}`} tone="error">
          <pre className="code-block">{actionState.message}</pre>
        </Panel>
      );
    }
    if (actionState.kind === 'verification-error') {
      return (
        <Panel
          title="Operator action accepted, but verification refetch failed"
          description={`${actionState.label} at ${formatTimestamp(actionState.occurredAt)}`}
          tone="error"
        >
          <p className="muted-copy">
            The runtime write endpoint returned success, but the follow-up <code>/api/console</code> read failed, so this route may still show stale state until a later successful refresh.
          </p>
          <pre className="code-block">{actionState.message}</pre>
          <pre className="code-block">{JSON.stringify(actionState.details, null, 2)}</pre>
        </Panel>
      );
    }
    return (
      <Panel title="Operator action accepted" description={`${actionState.label} at ${formatTimestamp(actionState.occurredAt)}`}>
        <pre className="code-block">{JSON.stringify(actionState.details, null, 2)}</pre>
      </Panel>
    );
  }

  function renderOverview(payload: ConsolePayload) {
    return (
      <>
        <Panel
          title="Local operator console"
          description="The browser stores a local bearer token and sends Authorization: Bearer ... for console reads and operator writes. The runtime resolves actor and session identity; the browser only carries the token."
          actions={
            <div className="inline-badges">
              <StatusPill label={payload.meta.console_mode} tone="default" />
              <StatusPill
                label={payload.meta.request_identity?.auth_method === 'bearer' ? 'server resolved bearer identity' : 'request identity not returned'}
                tone={payload.meta.request_identity?.auth_method === 'bearer' ? 'good' : 'warning'}
              />
            </div>
          }
        >
          <div className="metric-grid">
            {summaryCards.map((card) => (
              <RouteLink key={card.label} to={{ section: card.section }} navigate={navigate} className="metric-card" ariaLabel={`Open ${card.label.toLowerCase()} route`}>
                <span className="metric-label">{card.label}</span>
                <strong>{card.value}</strong>
                <span className="metric-detail">Open {card.label.toLowerCase()} details</span>
              </RouteLink>
            ))}
          </div>
        </Panel>

        <div className="page-grid two-column">
          <Panel title="Current operator surfaces" description="Discovered from the runtime console meta, then carried through existing /demo/* endpoints.">
            <div className="capability-list">
              <article className="capability-card">
                <strong>Plugin lifecycle</strong>
                <p>Enable or disable already-registered plugins through the existing runtime admin path.</p>
                <code>{formatStringList(payload.meta.plugin_operator_actions)}</code>
              </article>
              <article className="capability-card">
                <strong>Plugin-echo config</strong>
                <p>Update the current narrow persisted plugin config binding exposed by the runtime.</p>
                <code>{formatStringList(payload.meta.plugin_config_operator_actions)}</code>
              </article>
              <article className="capability-card">
                <strong>Job control</strong>
                <p>Pause, resume, or cancel queued jobs, and retry dead-letter jobs, through the existing runtime operator endpoints.</p>
                <p>Scope: {payload.meta.job_operator_scope ?? 'not declared'}</p>
                <code>{formatStringList(payload.meta.job_operator_actions)}</code>
              </article>
              <article className="capability-card">
                <strong>Schedule cancel</strong>
                <p>Cancel currently-registered schedules through the existing runtime operator surface.</p>
                <code>{formatStringList(payload.meta.schedule_operator_actions)}</code>
              </article>
            </div>
          </Panel>

          <Panel title="Recovery and alert evidence" description="Surfaced directly from the runtime recovery snapshot, alerts, and persisted scheduler evidence.">
            <div className="detail-grid compact">
              <DetailField label="Recovered at" value={formatTimestamp(payload.recovery.recoveredAt)} />
              <DetailField label="Recovered jobs" value={`${payload.recovery.recoveredJobs}/${payload.recovery.totalJobs}`} />
              <DetailField label="Recovered schedules" value={`${payload.recovery.recoveredSchedules}/${payload.recovery.totalSchedules}`} />
              <DetailField label="Invalid schedules" value={payload.recovery.invalidSchedules} />
            </div>
            <p className="muted-copy">{payload.recovery.summary || 'No recovery evidence was returned by the runtime.'}</p>
            {payload.alerts.length === 0 ? (
              <EmptyState title="No live alerts" description="Dead-letter alerts resolve as the runtime write surfaces clear them." />
            ) : (
              <div className="stack-list">
                {payload.alerts.map((alert) => (
                  <RouteLink
                    key={alert.id}
                    to={{ section: 'jobs', jobId: alert.objectId }}
                    navigate={navigate}
                    className="list-card"
                    ariaLabel={`Open alert target job ${alert.objectId}`}
                  >
                    <div className="list-card-header">
                      <strong>{alert.objectId}</strong>
                      <StatusPill label={alert.failureType} tone="danger" />
                    </div>
                    <p>{alert.latestReason}</p>
                    <span className="list-meta">First seen {formatTimestamp(alert.firstOccurredAt)} · Latest {formatTimestamp(alert.latestOccurredAt)}</span>
                  </RouteLink>
                ))}
              </div>
            )}
          </Panel>
        </div>

        <div className="page-grid two-column">
          <Panel title="Plugin attention" description="Focusing the overview on failing or recently recovered plugins makes the routed detail pages easier to use.">
            {pluginAttention.failing.length === 0 && pluginAttention.recovered.length === 0 ? (
              <EmptyState title="No plugin attention needed" description="No failing or recently recovered plugin evidence is visible in the current snapshot." />
            ) : (
              <div className="stack-list">
                {[...pluginAttention.failing, ...pluginAttention.recovered].map((plugin) => (
                  <RouteLink
                    key={plugin.id}
                    to={{ section: 'plugins', pluginId: plugin.id }}
                    navigate={navigate}
                    className="list-card"
                    ariaLabel={`Open plugin ${plugin.id} details`}
                  >
                    <div className="list-card-header">
                      <strong>{plugin.name}</strong>
                      <StatusPill label={plugin.enabled ? 'enabled' : 'disabled'} tone={plugin.enabled ? 'good' : 'muted'} />
                    </div>
                    <p>{plugin.statusSummary ?? 'No plugin status summary available.'}</p>
                    <div className="inline-badges">
                      <StatusPill label={plugin.statusRecovery ?? 'no-runtime-evidence'} tone={getStatusToneFromPlugin(plugin)} />
                      <StatusPill label={plugin.statusStaleness ?? 'unknown staleness'} tone="muted" />
                    </div>
                  </RouteLink>
                ))}
              </div>
            )}
          </Panel>

          <Panel title="Queue, schedules, and workflows" description="Key entities keep their own routes, but the overview surfaces the evidence that usually leads an operator into detail pages.">
            <div className="stack-list">
              {deadLetterJobs.slice(0, 2).map((job) => (
                <RouteLink
                  key={job.id}
                  to={{ section: 'jobs', jobId: job.id }}
                  navigate={navigate}
                  className="list-card"
                  ariaLabel={`Open job ${job.id} details`}
                >
                  <div className="list-card-header">
                    <strong>{job.id}</strong>
                    <StatusPill label={job.status} tone={getStatusToneFromJob(job)} />
                  </div>
                  <p>{job.recoverySummary || job.lastError || 'No recovery summary available.'}</p>
                </RouteLink>
              ))}
              {liveSchedules.slice(0, 2).map((schedule) => (
                <RouteLink
                  key={schedule.id}
                  to={{ section: 'schedules', scheduleId: schedule.id }}
                  navigate={navigate}
                  className="list-card"
                  ariaLabel={`Open schedule ${schedule.id} details`}
                >
                  <div className="list-card-header">
                    <strong>{schedule.id}</strong>
                    <StatusPill label={schedule.dueStateSummary ?? 'scheduled'} tone={getStatusToneFromSchedule(schedule)} />
                  </div>
                  <p>{schedule.scheduleSummary ?? 'No schedule summary available.'}</p>
                </RouteLink>
              ))}
              {workflows.slice(0, 2).map((workflow) => (
                <RouteLink
                  key={workflow.id}
                  to={{ section: 'workflows', workflowId: workflow.id }}
                  navigate={navigate}
                  className="list-card"
                  ariaLabel={`Open workflow ${workflow.id} details`}
                >
                  <div className="list-card-header">
                    <strong>{workflow.id}</strong>
                    <StatusPill label={workflow.status} tone={workflow.completed ? 'good' : 'default'} />
                  </div>
                  <p>{workflow.summary ?? 'No workflow summary available.'}</p>
                </RouteLink>
              ))}
            </div>
          </Panel>
        </div>

        <div className="page-grid two-column">
          <Panel title="Recent operator evidence" description="Audit rows, generated timestamps, and verification endpoints help the browser console stay honest about what it is and is not.">
            {recentAudits.length === 0 ? (
              <EmptyState title="No audits yet" description="The runtime has not returned any audit entries in the current snapshot." />
            ) : (
              <div className="stack-list">
                {recentAudits.map((entry, index) => (
                  <article key={`${entry.actor}-${entry.target}-${entry.occurred_at}-${index}`} className="list-card static-card">
                    <div className="list-card-header">
                      <strong>{entry.action}</strong>
                      <StatusPill label={entry.allowed ? 'allowed' : 'denied'} tone={entry.allowed ? 'good' : 'danger'} />
                    </div>
                    <p>{entry.target}</p>
                    <span className="list-meta">{entry.actor || 'header omitted'} · {entry.permission ?? 'no permission field'} · {formatTimestamp(entry.occurred_at)}</span>
                    {formatAuditObservability(entry) ? <span className="list-meta">{formatAuditObservability(entry)}</span> : null}
                  </article>
                ))}
              </div>
            )}
          </Panel>

          <Panel title="Log evidence" description="Logs stay string-based because the runtime log buffer is still the source of truth; the browser just lets an operator filter and route from there.">
            <div className="filter-row">
              <label className="field-label" htmlFor="log-query">
                Log filter
              </label>
              <input
                id="log-query"
                className="text-input"
                type="search"
                value={filters.logQuery}
                onChange={(event) => setFilters((current) => ({ ...current, logQuery: event.target.value }))}
                placeholder="Filter runtime log lines by substring"
              />
            </div>
            {visibleLogs.length === 0 ? (
              <EmptyState title="No logs in view" description="Either the runtime returned no logs or the current log filter removed them from this snapshot." />
            ) : (
              <div className="log-list" role="log" aria-label="runtime logs">
                {visibleLogs.map((line, index) => (
                  <code key={`${index}-${line}`} className="code-block log-line">
                    {line}
                  </code>
                ))}
              </div>
            )}
          </Panel>
        </div>
      </>
    );
  }

  function renderPlugins(payload: ConsolePayload) {
    if (route.section === 'plugins' && route.pluginId) {
      const plugin = payload.plugins.find((candidate) => candidate.id === route.pluginId) ?? null;
      if (!plugin) {
        return <EmptyState title="Plugin not found" description="The current runtime snapshot does not include this plugin ID." />;
      }

      const pluginAudits = relatedAudits(payload.audits, plugin.id);
      const pluginWorkflows = payload.workflows.filter((workflow) => workflow.pluginId === plugin.id);
      const pluginLogs = relatedLogs(payload.logs, plugin.id, plugin.name, filters.logQuery);
      const currentPrefix = typeof plugin.config?.prefix === 'string' ? plugin.config.prefix : '';
      const pluginRolloutHead = payload.rolloutHeads.find((candidate) => candidate.pluginId === plugin.id) ?? null;
      const pluginRolloutOps = sortRolloutOperations(
        payload.rolloutOps.filter((candidate) => candidate.pluginId === plugin.id),
      ).slice(0, 5);
      const rolloutPolicy = payload.meta.rollout_policy;
      const rolloutPolicySections = rolloutPolicyDetails(rolloutPolicy);
      const rolloutRecordStore = rolloutPolicy?.recordStore ?? payload.meta.rollout_record_store ?? 'not declared';
      const rolloutHeadReadModel = formatMetaString(payload.meta.rollout_head_read_model);
      const rolloutRecordReadModel = formatMetaString(payload.meta.rollout_record_read_model);
      const rolloutConsoleLimitations = formatMetaStringList(payload.meta.rollout_console_limitations);
      const pluginRolloutEmpty = pluginRolloutHead === null && pluginRolloutOps.length === 0;

      return (
        <>
          <Panel
            title={`${plugin.name} · ${plugin.id}`}
            description="Plugin detail routes keep the write surfaces narrow and evidence-first: lifecycle toggle, plugin-echo config, runtime recovery facts, related audits, and workflow evidence."
            actions={
              <RouteLink to={{ section: 'plugins' }} navigate={navigate} className="secondary-link" ariaLabel="Back to plugins">
                Back to plugins
              </RouteLink>
            }
          >
            <div className="detail-grid">
              <DetailField label="Version" value={plugin.version} />
              <DetailField label="Mode" value={plugin.mode} />
              <DetailField label="Enabled" value={<StatusPill label={plugin.enabled ? 'enabled' : 'disabled'} tone={plugin.enabled ? 'good' : 'muted'} />} />
              <DetailField label="Status" value={<StatusPill label={plugin.statusRecovery ?? 'no-runtime-evidence'} tone={getStatusToneFromPlugin(plugin)} />} />
              <DetailField label="Permissions" value={formatStringList(plugin.permissions)} />
              <DetailField label="Config updated at" value={formatTimestamp(plugin.configUpdatedAt)} />
              <DetailField label="Dispatch evidence" value={plugin.statusEvidence ?? 'manifest-only'} />
              <DetailField label="Recovery evidence" value={plugin.statusSummary ?? 'No runtime recovery summary available.'} />
            </div>
          </Panel>

          <div className="page-grid two-column">
            <Panel title="Plugin operator actions" description="Read-after-write refresh keeps the browser console honest about runtime authority.">
              <div className="action-group">
                <div className="action-card">
                  <div className="action-copy">
                    <strong>{plugin.enabled ? 'Disable plugin' : 'Enable plugin'}</strong>
                    <p>Persist the existing enabled overlay through the runtime admin command surface.</p>
                    <span className="list-meta">Likely RBAC actors: {formatStringList(actorsLikelyAllowed(payload.config, plugin.enabled ? 'plugin:disable' : 'plugin:enable', plugin.id))}</span>
                  </div>
                  <button
                    type="button"
                    className="primary-button"
                    disabled={actionBusy}
                    onClick={() =>
                      void runAction(
                        plugin.enabled ? `Disable ${plugin.id}` : `Enable ${plugin.id}`,
                        `/demo/plugins/${plugin.id}/${plugin.enabled ? 'disable' : 'enable'}`,
                      )
                    }
                  >
                    {plugin.enabled ? 'Disable plugin' : 'Enable plugin'}
                  </button>
                </div>

                {plugin.id === 'plugin-echo' ? (
                  <form
                    className="action-card"
                    onSubmit={(event) => {
                      event.preventDefault();
                      setPluginEchoDirty(false);
                      void runAction('Update plugin-echo prefix', `/demo/plugins/${plugin.id}/config`, { prefix: pluginEchoPrefixDraft });
                    }}
                  >
                    <div className="action-copy">
                      <strong>Update plugin-echo prefix</strong>
                      <p>The config editor stays intentionally narrow to the existing `plugin-echo` persisted input contract.</p>
                      <span className="list-meta">Current prefix from /api/console: {currentPrefix || '(empty string)'}</span>
                       <span className="list-meta">Likely RBAC actors: {formatStringList(actorsLikelyAllowed(payload.config, 'plugin:config', plugin.id))}</span>
                    </div>
                    <label className="field-label" htmlFor="plugin-echo-prefix">
                      Prefix
                    </label>
                    <input
                      id="plugin-echo-prefix"
                      className="text-input"
                      value={pluginEchoPrefixDraft}
                      onChange={(event) => {
                        setPluginEchoDirty(true);
                        setPluginEchoPrefixDraft(event.target.value);
                      }}
                      placeholder="persisted: "
                    />
                    <button type="submit" className="primary-button" disabled={actionBusy}>
                      Save prefix
                    </button>
                  </form>
                ) : null}
              </div>
            </Panel>

            <Panel title="Plugin evidence and provenance" description="The detail page surfaces persisted config, runtime dispatch evidence, publish metadata, and lifecycle provenance separately.">
              <div className="detail-grid">
                <DetailField label="Entry" value={plugin.entry.binary ?? plugin.entry.module ?? 'internal entry'} />
                <DetailField label="Publish source" value={plugin.publish?.sourceType ?? 'not declared'} />
                <DetailField label="Publish URI" value={plugin.publish?.sourceUri ?? 'not declared'} />
                <DetailField label="Runtime range" value={plugin.publish?.runtimeVersionRange ?? 'not declared'} />
                <DetailField label="Config state" value={plugin.configStateKind ?? 'not declared'} />
                <DetailField label="Config source" value={plugin.configSource ?? 'not persisted'} />
                <DetailField label="Enabled source" value={plugin.enabledStateSource ?? 'runtime-default-enabled'} />
                <DetailField label="Last dispatch at" value={formatTimestamp(plugin.lastDispatchAt)} />
                <DetailField label="Last recovered at" value={formatTimestamp(plugin.lastRecoveredAt)} />
                <DetailField label="Failure streak" value={plugin.currentFailureStreak ?? 0} />
                <DetailField label="Recovery failures before last success" value={plugin.lastRecoveryFailureCount ?? 0} />
              </div>
              <pre className="code-block">{JSON.stringify(plugin.config ?? {}, null, 2)}</pre>
            </Panel>
          </div>

          <div className="page-grid two-column">
            <Panel
              title="Rollout policy declaration"
              description="This panel mirrors the existing rollout declaration returned by /api/console. It stays evidence-oriented and read-only; rollout writes remain outside this route."
            >
              {rolloutPolicy ? (
                <>
                  <div className="detail-grid">
                    <DetailField label="Record store" value={rolloutRecordStore} />
                    <DetailField label="Operation read model" value={rolloutRecordReadModel} />
                    <DetailField label="Head read model" value={rolloutHeadReadModel} />
                    <DetailField label="Operations persisted" value={formatMetaBoolean(payload.meta.rollout_record_persisted)} />
                    <DetailField label="Head persisted" value={formatMetaBoolean(payload.meta.rollout_head_persisted)} />
                  </div>
                  <p className="muted-copy">{rolloutPolicy.summary ?? 'No rollout policy summary available.'}</p>
                  {rolloutPolicySections.length === 0 ? (
                    <p className="muted-copy">No rollout policy declaration details were returned in console meta.</p>
                  ) : (
                    <div className="stack-list compact-stack">
                      {rolloutPolicySections.map((section) => (
                        <article key={`${plugin.id}-rollout-policy-${section.label}`} className="list-card static-card">
                          <strong>{section.label}</strong>
                          {section.items.map((item) => (
                            <p key={`${section.label}-${item}`}>{item}</p>
                          ))}
                        </article>
                      ))}
                    </div>
                  )}
                </>
              ) : (
                <EmptyState
                  title="No rollout policy declaration"
                  description="The current console payload does not include meta.rollout_policy for this runtime snapshot."
                />
              )}
            </Panel>

            <Panel
              title="Rollout state"
              description="Read-only rollout visibility comes directly from the current /api/console snapshot. This route does not add rollout prepare, canary, activate, or rollback controls."
            >
              {pluginRolloutEmpty ? (
                <EmptyState
                  title="No rollout state for this plugin"
                  description="The current console payload does not include a rollout head or rollout operation records for this plugin."
                />
              ) : pluginRolloutHead ? (
                <>
                  <div className="detail-grid">
                    <DetailField label="Phase" value={<StatusPill label={pluginRolloutHead.phase} tone="default" />} />
                    <DetailField
                      label="Status"
                      value={<StatusPill label={pluginRolloutHead.status} tone={getStatusToneFromRolloutStatus(pluginRolloutHead.status)} />}
                    />
                    <DetailField label="Stable version" value={formatRolloutSnapshot(pluginRolloutHead.stable)} />
                    <DetailField label="Active version" value={formatRolloutSnapshot(pluginRolloutHead.active)} />
                    <DetailField label="Candidate version" value={formatRolloutSnapshot(pluginRolloutHead.candidate)} />
                    <DetailField label="Last operation ID" value={pluginRolloutHead.lastOperationId ?? '—'} />
                    <DetailField label="Updated at" value={formatTimestamp(pluginRolloutHead.updatedAt)} />
                    <DetailField label="State source" value={pluginRolloutHead.stateSource ?? rolloutHeadReadModel} />
                    <DetailField label="Persisted" value={pluginRolloutHead.persisted ? 'true' : 'false'} />
                  </div>
                  <p className="muted-copy">{pluginRolloutHead.summary ?? 'No rollout head summary available.'}</p>
                </>
              ) : (
                <EmptyState
                  title="No rollout head in view"
                  description="This plugin has rollout operation evidence in the current snapshot, but no current rollout head row was returned."
                />
              )}
            </Panel>
          </div>

          <div className="page-grid">
            <Panel
              title="Recent rollout evidence and limitations"
              description="Operation rows and rollout console meta stay read-only so the plugin route can surface rollout provenance without becoming a new control-plane surface."
            >
              <div className="detail-grid">
                <DetailField label="Head read model" value={rolloutHeadReadModel} />
                <DetailField label="Head persisted" value={formatMetaBoolean(payload.meta.rollout_head_persisted)} />
                <DetailField label="Operation read model" value={rolloutRecordReadModel} />
                <DetailField label="Operations persisted" value={formatMetaBoolean(payload.meta.rollout_record_persisted)} />
              </div>

              {rolloutConsoleLimitations.length === 0 ? (
                <p className="muted-copy">No rollout limitation copy was returned in console meta.</p>
              ) : (
                <div className="stack-list compact-stack">
                  {rolloutConsoleLimitations.map((limitation, index) => (
                    <article key={`${plugin.id}-rollout-limitation-${index}`} className="list-card static-card">
                      <strong>Rollout limitation {index + 1}</strong>
                      <p>{limitation}</p>
                    </article>
                  ))}
                </div>
              )}

              {pluginRolloutOps.length === 0 ? (
                <EmptyState
                  title="No recent rollout operations"
                  description="The current console payload does not include rollout operation evidence for this plugin."
                />
              ) : (
                <div className="stack-list">
                  {pluginRolloutOps.map((operation) => (
                    <article key={operation.operationId} className="list-card static-card">
                      <div className="list-card-header">
                        <strong>{operation.operationId}</strong>
                        <div className="inline-badges">
                          <StatusPill label={operation.action} tone="default" />
                          <StatusPill label={operation.status} tone={getStatusToneFromRolloutStatus(operation.status)} />
                        </div>
                      </div>
                      <p>{operation.summary ?? 'No rollout operation summary available.'}</p>
                      <span className="list-meta">
                        Current {operation.currentVersion ?? '—'} · Candidate {operation.candidateVersion ?? '—'}
                      </span>
                      <span className="list-meta">
                        Occurred {formatTimestamp(operation.occurredAt)} · Updated {formatTimestamp(operation.updatedAt)} · {operation.stateSource ?? rolloutRecordReadModel} · persisted {operation.persisted ? 'true' : 'false'}
                      </span>
                    </article>
                  ))}
                </div>
              )}
            </Panel>
          </div>

          <div className="page-grid two-column">
            <Panel title="Related audits" description="Audit evidence is filtered client-side from the current console snapshot.">
              {pluginAudits.length === 0 ? (
                <EmptyState title="No plugin audits in view" description="The current console payload does not include audit rows targeting this plugin." />
              ) : (
                <div className="stack-list">
                  {pluginAudits.map((entry, index) => (
                    <article key={`${entry.action}-${entry.occurred_at}-${index}`} className="list-card static-card">
                      <div className="list-card-header">
                        <strong>{entry.action}</strong>
                        <StatusPill label={entry.allowed ? 'allowed' : 'denied'} tone={entry.allowed ? 'good' : 'danger'} />
                      </div>
                      <p>{entry.reason ?? 'no audit reason'}</p>
                      <span className="list-meta">{entry.actor || 'header omitted'} · {entry.permission ?? 'no permission'} · {formatTimestamp(entry.occurred_at)}</span>
                      {formatAuditObservability(entry) ? <span className="list-meta">{formatAuditObservability(entry)}</span> : null}
                    </article>
                  ))}
                </div>
              )}
            </Panel>

            <Panel title="Related workflows and logs" description="Workflow state and raw log lines give operators a quick path from plugin status to runtime evidence.">
              {pluginWorkflows.length > 0 ? (
                <div className="stack-list compact-stack">
                  {pluginWorkflows.map((workflow) => (
                    <RouteLink
                      key={workflow.id}
                      to={{ section: 'workflows', workflowId: workflow.id }}
                      navigate={navigate}
                      className="list-card"
                      ariaLabel={`Open workflow ${workflow.id} details`}
                    >
                      <div className="list-card-header">
                        <strong>{workflow.id}</strong>
                        <StatusPill label={workflow.status} tone={workflow.completed ? 'good' : 'default'} />
                      </div>
                      <p>{workflow.summary ?? 'No workflow summary available.'}</p>
                    </RouteLink>
                  ))}
                </div>
              ) : (
                <EmptyState title="No related workflows" description="No workflow instances in the current snapshot belong to this plugin." />
              )}
              <div className="log-list">
                {pluginLogs.length === 0 ? (
                  <EmptyState title="No related logs" description="The current log snapshot does not include lines matching this plugin route and log filter." />
                ) : (
                  pluginLogs.map((line, index) => (
                    <code key={`${index}-${line}`} className="code-block log-line">
                      {line}
                    </code>
                  ))
                )}
              </div>
            </Panel>
          </div>
        </>
      );
    }

    return (
      <Panel
        title="Plugins"
        description="List and filter the current plugin read model, then route into detail pages for runtime evidence and supported operator actions."
      >
        <div className="filter-row">
          <label className="field-label" htmlFor="plugin-filter">
            Plugin ID filter
          </label>
          <input
            id="plugin-filter"
            className="text-input"
            type="search"
            value={filters.pluginID}
            onChange={(event) => setFilters((current) => ({ ...current, pluginID: event.target.value }))}
            placeholder="Filter plugins by exact plugin ID"
          />
        </div>
        {payload.plugins.length === 0 ? (
          <EmptyState title="No plugins in view" description="The current plugin route returned no plugin rows for this filter or runtime snapshot." />
        ) : (
          <div className="stack-list">
            {payload.plugins.map((plugin) => (
              <RouteLink
                key={plugin.id}
                to={{ section: 'plugins', pluginId: plugin.id }}
                navigate={navigate}
                className="list-card"
                ariaLabel={`Open plugin ${plugin.id} details`}
              >
                <div className="list-card-header">
                  <strong>{plugin.name}</strong>
                  <div className="inline-badges">
                    <StatusPill label={plugin.enabled ? 'enabled' : 'disabled'} tone={plugin.enabled ? 'good' : 'muted'} />
                    <StatusPill label={plugin.statusRecovery ?? 'no-runtime-evidence'} tone={getStatusToneFromPlugin(plugin)} />
                  </div>
                </div>
                <p>{plugin.statusSummary ?? 'No plugin summary available.'}</p>
                <span className="list-meta">{plugin.id} · {plugin.mode} · config source {plugin.configSource ?? 'not persisted'}</span>
              </RouteLink>
            ))}
          </div>
        )}
      </Panel>
    );
  }

  function renderJobs(payload: ConsolePayload) {
    if (route.section === 'jobs' && route.jobId) {
      const job = payload.jobs.find((candidate) => candidate.id === route.jobId) ?? null;
      if (!job) {
        return <EmptyState title="Job not found" description="The current runtime snapshot does not include this job ID." />;
      }

      const jobAlerts = payload.alerts.filter((alert) => alert.objectId === job.id);
      const jobAudits = relatedAudits(payload.audits, job.id);
      const jobLogs = relatedLogs(payload.logs, job.id, job.traceId, job.eventId, filters.logQuery);
      const jobOperatorActions = getJobOperatorActions(payload.meta, job);
      const jobOperatorScope = formatMetaString(payload.meta.job_operator_scope);

      return (
        <>
          <Panel
            title={`${job.id} · ${job.type}`}
            description="Job detail routes carry the current job-control surface, dispatch contract evidence, and the alert/audit context around queue recovery."
            actions={
              <RouteLink to={{ section: 'jobs' }} navigate={navigate} className="secondary-link" ariaLabel="Back to jobs">
                Back to jobs
              </RouteLink>
            }
          >
            <div className="detail-grid">
              <DetailField label="Status" value={<StatusPill label={job.status} tone={getStatusToneFromJob(job)} />} />
              <DetailField label="Dead-letter" value={job.deadLetter ? 'true' : 'false'} />
              <DetailField label="Retries" value={`${job.retryCount}/${job.maxRetries}`} />
              <DetailField label="Timeout" value={formatDuration(job.timeout)} />
              <DetailField label="Created at" value={formatTimestamp(job.createdAt)} />
              <DetailField label="Current queue state" value={job.queueStateSummary ?? '—'} />
              <DetailField label="Dispatch ready" value={job.dispatchReady ? 'true' : 'false'} />
              <DetailField label="Recovery summary" value={job.recoverySummary ?? '—'} />
            </div>
          </Panel>

          <div className="page-grid two-column">
            <Panel title="Job operator actions" description="Controls stay on the existing runtime job endpoints, then refetch /api/console so the route reflects queue truth instead of optimistic browser state.">
              {jobOperatorActions.length === 0 ? (
                <EmptyState
                  title="No job operator actions for this state"
                  description={`The current runtime scope says ${jobOperatorScope}, but this job does not presently match any supported action state.`}
                />
              ) : (
                <div className="action-group">
                  {jobOperatorActions.map((action) => (
                    <div key={action.key} className="action-card">
                      <div className="action-copy">
                        <strong>{action.title}</strong>
                        <p>{action.description}</p>
                        <span className="list-meta">Runtime scope: {jobOperatorScope}</span>
                        <span className="list-meta">Likely RBAC actors: {formatStringList(actorsLikelyAllowed(payload.config, action.permission, job.id))}</span>
                      </div>
                      <button
                        type="button"
                        className="primary-button"
                        disabled={actionBusy}
                        onClick={() => void runAction(`${action.requestLabel} ${job.id}`, action.path)}
                      >
                        {action.buttonLabel}
                      </button>
                    </div>
                  ))}
                </div>
              )}
            </Panel>

            <Panel title="Dispatch and queue contract" description="These fields come from the existing runtime queue projection rather than from frontend-only interpretation.">
              <div className="detail-grid">
                <DetailField label="Dispatch actor" value={job.dispatchActor ?? '—'} />
                <DetailField label="Dispatch permission" value={job.dispatchPermission ?? '—'} />
                <DetailField label="Target plugin" value={job.targetPluginId ?? '—'} />
                <DetailField label="Queue contract" value={job.queueContractSummary ?? '—'} />
                <DetailField label="Lease" value={job.leaseSummary ?? '—'} />
                <DetailField label="Reply summary" value={job.replySummary ?? '—'} />
              </div>
              <pre className="code-block">{JSON.stringify(job.dispatchRbac ?? {}, null, 2)}</pre>
            </Panel>
          </div>

          <div className="page-grid two-column">
            <Panel title="Alerts and audits" description="Queued-state writes should leave audit evidence after refetch, while dead-letter alerts only clear after a successful retry path is reflected in the next read model.">
              {jobAlerts.length === 0 && jobAudits.length === 0 ? (
                <EmptyState title="No job evidence rows" description="The current snapshot does not include matching alerts or audit entries for this job." />
              ) : (
                <div className="stack-list">
                  {jobAlerts.map((alert) => (
                    <article key={alert.id} className="list-card static-card">
                      <div className="list-card-header">
                        <strong>{alert.failureType}</strong>
                        <StatusPill label="alert" tone="danger" />
                      </div>
                      <p>{alert.latestReason}</p>
                      <span className="list-meta">{formatTimestamp(alert.latestOccurredAt)}</span>
                    </article>
                  ))}
                  {jobAudits.map((entry, index) => (
                    <article key={`${entry.action}-${entry.occurred_at}-${index}`} className="list-card static-card">
                      <div className="list-card-header">
                        <strong>{entry.action}</strong>
                        <StatusPill label={entry.allowed ? 'allowed' : 'denied'} tone={entry.allowed ? 'good' : 'danger'} />
                      </div>
                      <p>{entry.reason ?? 'no reason'}</p>
                      <span className="list-meta">{entry.actor || 'header omitted'} · {formatTimestamp(entry.occurred_at)}</span>
                      {formatAuditObservability(entry) ? <span className="list-meta">{formatAuditObservability(entry)}</span> : null}
                    </article>
                  ))}
                </div>
              )}
            </Panel>

            <Panel title="Related logs" description="Logs remain raw runtime evidence; the route only narrows them for the current job context.">
              {jobLogs.length === 0 ? (
                <EmptyState title="No related logs" description="The current log snapshot does not include lines for this job route and filter." />
              ) : (
                <div className="log-list">
                  {jobLogs.map((line, index) => (
                    <code key={`${index}-${line}`} className="code-block log-line">
                      {line}
                    </code>
                  ))}
                </div>
              )}
            </Panel>
          </div>
        </>
      );
    }

    return (
      <Panel title="Jobs" description="The jobs route keeps the current queue projection browsable and links queue evidence into the detail-scoped job-control surface.">
        <div className="filter-row">
          <label className="field-label" htmlFor="job-filter">
            Job filter
          </label>
          <input
            id="job-filter"
            className="text-input"
            type="search"
            value={filters.jobQuery}
            onChange={(event) => setFilters((current) => ({ ...current, jobQuery: event.target.value }))}
            placeholder="Filter jobs by id, type, status, correlation, or last error"
          />
        </div>
        {payload.jobs.length === 0 ? (
          <EmptyState title="No jobs in view" description="The current job route returned no rows for this filter or runtime snapshot." />
        ) : (
          <div className="stack-list">
            {payload.jobs.map((job) => (
              <RouteLink
                key={job.id}
                to={{ section: 'jobs', jobId: job.id }}
                navigate={navigate}
                className="list-card"
                ariaLabel={`Open job ${job.id} details`}
              >
                <div className="list-card-header">
                  <strong>{job.id}</strong>
                  <div className="inline-badges">
                    <StatusPill label={job.status} tone={getStatusToneFromJob(job)} />
                    {job.deadLetter ? <StatusPill label="dead-letter" tone="danger" /> : null}
                  </div>
                </div>
                <p>{job.recoverySummary || job.lastError || job.queueStateSummary || 'No queue summary available.'}</p>
                <span className="list-meta">{job.type} · correlation {job.correlation}</span>
              </RouteLink>
            ))}
          </div>
        )}
      </Panel>
    );
  }

  function renderSchedules(payload: ConsolePayload) {
    if (route.section === 'schedules' && route.scheduleId) {
      const schedule = payload.schedules.find((candidate) => candidate.id === route.scheduleId) ?? null;
      if (!schedule) {
        return <EmptyState title="Schedule not found" description="The current runtime snapshot does not include this schedule ID." />;
      }

      const scheduleAudits = relatedAudits(payload.audits, schedule.id);
      const scheduleLogs = relatedLogs(payload.logs, schedule.id, schedule.source, filters.logQuery);

      return (
        <>
          <Panel
            title={`${schedule.id} · ${schedule.eventType}`}
            description="Schedule detail routes carry persisted due-at evidence, startup recovery facts, and the existing cancel write path."
            actions={
              <RouteLink to={{ section: 'schedules' }} navigate={navigate} className="secondary-link" ariaLabel="Back to schedules">
                Back to schedules
              </RouteLink>
            }
          >
            <div className="detail-grid">
              <DetailField label="Kind" value={schedule.kind} />
              <DetailField label="Source" value={schedule.source} />
              <DetailField label="Due at" value={formatTimestamp(schedule.dueAt)} />
              <DetailField label="Due evidence" value={schedule.dueAtEvidence ?? '—'} />
              <DetailField label="Recovery state" value={schedule.recoveryState ?? '—'} />
              <DetailField label="Claim owner" value={schedule.claimOwner || '—'} />
              <DetailField label="Claimed at" value={formatTimestamp(schedule.claimedAt)} />
              <DetailField label="Config" value={scheduleConfigSummary(schedule)} />
            </div>
          </Panel>

          <div className="page-grid two-column">
            <Panel title="Schedule cancel" description="Cancel remains a narrow runtime operator action; the browser carries bearer auth and refetches evidence afterward.">
              <div className="action-card">
                <div className="action-copy">
                  <strong>Cancel schedule</strong>
                  <p>Useful for local/dev operators when a due or stale schedule should stop before the next scheduler loop.</p>
                  <span className="list-meta">Likely RBAC actors: {formatStringList(actorsLikelyAllowed(payload.config, 'schedule:cancel', schedule.id))}</span>
                </div>
                <button
                  type="button"
                  className="primary-button"
                  disabled={actionBusy}
                  onClick={() => void runAction(`Cancel ${schedule.id}`, `/demo/schedules/${schedule.id}/cancel`)}
                >
                  Cancel schedule
                </button>
              </div>
            </Panel>

            <Panel title="Schedule evidence" description="The browser surfaces more of the persisted schedule payload because it materially improves the detail experience.">
              <div className="detail-grid">
                <DetailField label="Due state" value={schedule.dueStateSummary ?? '—'} />
                <DetailField label="Due summary" value={schedule.dueSummary ?? '—'} />
                <DetailField label="Schedule summary" value={schedule.scheduleSummary ?? '—'} />
                <DetailField label="Created at" value={formatTimestamp(schedule.createdAt)} />
                <DetailField label="Updated at" value={formatTimestamp(schedule.updatedAt)} />
              </div>
            </Panel>
          </div>

          <div className="page-grid two-column">
            <Panel title="Related audits" description="Cancel writes are expected to leave operator evidence behind in the current console snapshot.">
              {scheduleAudits.length === 0 ? (
                <EmptyState title="No schedule audits" description="The current snapshot does not include matching audit rows for this schedule." />
              ) : (
                <div className="stack-list">
                  {scheduleAudits.map((entry, index) => (
                    <article key={`${entry.action}-${entry.occurred_at}-${index}`} className="list-card static-card">
                      <div className="list-card-header">
                        <strong>{entry.action}</strong>
                        <StatusPill label={entry.allowed ? 'allowed' : 'denied'} tone={entry.allowed ? 'good' : 'danger'} />
                      </div>
                      <p>{entry.reason ?? 'no reason'}</p>
                      <span className="list-meta">{entry.actor || 'header omitted'} · {formatTimestamp(entry.occurred_at)}</span>
                      {formatAuditObservability(entry) ? <span className="list-meta">{formatAuditObservability(entry)}</span> : null}
                    </article>
                  ))}
                </div>
              )}
            </Panel>

            <Panel title="Related logs" description="Raw runtime logs stay visible for route-level diagnosis.">
              {scheduleLogs.length === 0 ? (
                <EmptyState title="No schedule logs" description="The current log snapshot does not include lines for this schedule route and filter." />
              ) : (
                <div className="log-list">
                  {scheduleLogs.map((line, index) => (
                    <code key={`${index}-${line}`} className="code-block log-line">
                      {line}
                    </code>
                  ))}
                </div>
              )}
            </Panel>
          </div>
        </>
      );
    }

    return (
      <Panel title="Schedules" description="Persisted schedule state, due evidence, and cancel surfaces stay together here before routing into a single schedule detail page.">
        {payload.schedules.length === 0 ? (
          <EmptyState title="No schedules in view" description="The runtime returned no persisted schedule rows in this snapshot." />
        ) : (
          <div className="stack-list">
            {payload.schedules.map((schedule) => (
              <RouteLink
                key={schedule.id}
                to={{ section: 'schedules', scheduleId: schedule.id }}
                navigate={navigate}
                className="list-card"
                ariaLabel={`Open schedule ${schedule.id} details`}
              >
                <div className="list-card-header">
                  <strong>{schedule.id}</strong>
                  <StatusPill label={schedule.dueStateSummary ?? 'scheduled'} tone={getStatusToneFromSchedule(schedule)} />
                </div>
                <p>{schedule.scheduleSummary ?? 'No schedule summary available.'}</p>
                <span className="list-meta">{schedule.source} · due {formatTimestamp(schedule.dueAt)}</span>
              </RouteLink>
            ))}
          </div>
        )}
      </Panel>
    );
  }

  function renderAdapters(payload: ConsolePayload) {
    if (route.section === 'adapters' && route.adapterId) {
      const adapter = payload.adapters.find((candidate) => candidate.id === route.adapterId) ?? null;
      if (!adapter) {
        return <EmptyState title="Adapter not found" description="The current runtime snapshot does not include this adapter instance ID." />;
      }

      const adapterLogs = relatedLogs(payload.logs, adapter.id, adapter.source, filters.logQuery);

      return (
        <>
          <Panel
            title={`${adapter.id} · ${adapter.adapter}`}
            description="Adapter detail routes stay read-focused, but they help operators verify ingress/source/config facts before drilling into plugin or queue issues."
            actions={
              <RouteLink to={{ section: 'adapters' }} navigate={navigate} className="secondary-link" ariaLabel="Back to adapters">
                Back to adapters
              </RouteLink>
            }
          >
            <div className="detail-grid">
              <DetailField label="Source" value={adapter.source} />
              <DetailField label="Status" value={adapter.status ?? '—'} />
              <DetailField label="Health" value={adapter.health ?? '—'} />
              <DetailField label="Online" value={<StatusPill label={adapter.online ? 'online' : 'offline'} tone={adapter.online ? 'good' : 'danger'} />} />
              <DetailField label="Status source" value={adapter.statusSource ?? '—'} />
              <DetailField label="Config source" value={adapter.configSource ?? '—'} />
              <DetailField label="Updated at" value={formatTimestamp(adapter.updatedAt)} />
              <DetailField label="Summary" value={adapter.summary ?? '—'} />
            </div>
            <pre className="code-block">{JSON.stringify(adapter.config ?? {}, null, 2)}</pre>
          </Panel>

          <Panel title="Related logs" description="Adapters do not get write actions in this slice, so the detail page leans into runtime evidence instead.">
            {adapterLogs.length === 0 ? (
              <EmptyState title="No adapter logs" description="The current log snapshot does not include lines for this adapter route and filter." />
            ) : (
              <div className="log-list">
                {adapterLogs.map((line, index) => (
                  <code key={`${index}-${line}`} className="code-block log-line">
                    {line}
                  </code>
                ))}
              </div>
            )}
          </Panel>
        </>
      );
    }

    return (
      <Panel title="Adapters" description="Adapter instance facts come directly from the runtime read model and route into instance detail pages for ingress/config evidence.">
        {payload.adapters.length === 0 ? (
          <EmptyState title="No adapters in view" description="The runtime returned no adapter instance rows in this snapshot." />
        ) : (
          <div className="stack-list">
            {payload.adapters.map((adapter) => (
              <RouteLink
                key={adapter.id}
                to={{ section: 'adapters', adapterId: adapter.id }}
                navigate={navigate}
                className="list-card"
                ariaLabel={`Open adapter ${adapter.id} details`}
              >
                <div className="list-card-header">
                  <strong>{adapter.id}</strong>
                  <StatusPill label={adapter.online ? 'online' : 'offline'} tone={adapter.online ? 'good' : 'danger'} />
                </div>
                <p>{adapter.summary ?? 'No adapter summary available.'}</p>
                <span className="list-meta">{adapter.adapter} / {adapter.source}</span>
              </RouteLink>
            ))}
          </div>
        )}
      </Panel>
    );
  }

  function renderWorkflows(payload: ConsolePayload) {
    if (route.section === 'workflows' && route.workflowId) {
      const workflow = payload.workflows.find((candidate) => candidate.id === route.workflowId) ?? null;
      if (!workflow) {
        return <EmptyState title="Workflow not found" description="The current runtime snapshot does not include this workflow ID." />;
      }

      const workflowLogs = relatedLogs(
        payload.logs,
        workflow.id,
        workflow.pluginId,
        workflow.traceId,
        workflow.eventId,
        workflow.runId,
        workflow.correlationId,
        workflow.lastEventId,
        filters.logQuery,
      );

      return (
        <>
          <Panel
            title={`${workflow.id} · ${workflow.pluginId}`}
            description="Workflow detail routes add more meaning to the existing console payload by putting persisted state, waiting/sleeping facts, and related plugin evidence in one place."
            actions={
              <RouteLink to={{ section: 'workflows' }} navigate={navigate} className="secondary-link" ariaLabel="Back to workflows">
                Back to workflows
              </RouteLink>
            }
          >
            <div className="detail-grid">
              <DetailField label="Status" value={<StatusPill label={workflow.status} tone={workflow.completed ? 'good' : 'default'} />} />
              <DetailField label="Trace ID" value={workflow.traceId ?? '—'} />
              <DetailField label="Origin event ID" value={workflow.eventId ?? '—'} />
              <DetailField label="Run ID" value={workflow.runId ?? '—'} />
              <DetailField label="Correlation ID" value={workflow.correlationId ?? '—'} />
              <DetailField label="Current index" value={workflow.currentIndex} />
              <DetailField label="Waiting for" value={workflow.waitingFor ?? '—'} />
              <DetailField label="Sleeping until" value={formatTimestamp(workflow.sleepingUntil)} />
              <DetailField label="Completed" value={workflow.completed ? 'true' : 'false'} />
              <DetailField label="Compensated" value={workflow.compensated ? 'true' : 'false'} />
              <DetailField label="Last event cursor" value={workflow.lastEventId ?? '—'} />
              <DetailField label="Last event type" value={workflow.lastEventType ?? '—'} />
              <DetailField label="Status source" value={workflow.statusSource ?? '—'} />
            </div>
            <pre className="code-block">{JSON.stringify(workflow.state ?? {}, null, 2)}</pre>
          </Panel>

          <div className="page-grid two-column">
            <Panel title="Related plugin route" description="Workflow detail pages link back into plugin lifecycle evidence without adding new backend control-plane concepts.">
              <RouteLink
                to={{ section: 'plugins', pluginId: workflow.pluginId }}
                navigate={navigate}
                className="list-card"
                ariaLabel={`Open plugin ${workflow.pluginId} details`}
              >
                <div className="list-card-header">
                  <strong>{workflow.pluginId}</strong>
                  <StatusPill label="plugin detail" tone="default" />
                </div>
                <p>Open the plugin route for lifecycle, config, audit, and runtime dispatch evidence.</p>
              </RouteLink>
            </Panel>

            <Panel title="Related logs" description="Logs stay raw; the route simply narrows them around workflow identifiers and the current log filter.">
              {workflowLogs.length === 0 ? (
                <EmptyState title="No workflow logs" description="The current log snapshot does not include lines for this workflow route and filter." />
              ) : (
                <div className="log-list">
                  {workflowLogs.map((line, index) => (
                    <code key={`${index}-${line}`} className="code-block log-line">
                      {line}
                    </code>
                  ))}
                </div>
              )}
            </Panel>
          </div>
        </>
      );
    }

    return (
      <Panel title="Workflows" description="The workflow route exposes persisted runtime-core workflow state, then links each row into a focused detail page.">
        {payload.workflows.length === 0 ? (
          <EmptyState title="No workflows in view" description="The runtime returned no workflow rows in this snapshot." />
        ) : (
          <div className="stack-list">
            {payload.workflows.map((workflow) => (
              <RouteLink
                key={workflow.id}
                to={{ section: 'workflows', workflowId: workflow.id }}
                navigate={navigate}
                className="list-card"
                ariaLabel={`Open workflow ${workflow.id} details`}
              >
                <div className="list-card-header">
                  <strong>{workflow.id}</strong>
                  <StatusPill label={workflow.status} tone={workflow.completed ? 'good' : 'default'} />
                </div>
                <p>{workflow.summary ?? 'No workflow summary available.'}</p>
                <span className="list-meta">plugin {workflow.pluginId} · trace {workflow.traceId ?? '—'} · event {workflow.eventId ?? '—'}</span>
              </RouteLink>
            ))}
          </div>
        )}
      </Panel>
    );
  }

  function renderPageContent(payload: ConsolePayload) {
    switch (route.section) {
      case 'overview':
        return renderOverview(payload);
      case 'plugins':
        return renderPlugins(payload);
      case 'jobs':
        return renderJobs(payload);
      case 'schedules':
        return renderSchedules(payload);
      case 'adapters':
        return renderAdapters(payload);
      case 'workflows':
        return renderWorkflows(payload);
    }
  }

  return (
    <div className="console-layout">
      <aside className="console-sidebar">
        <div className="brand-block">
          <p className="eyebrow">bot-platform / Console Web A11</p>
          <h1>Local operator console</h1>
          <p>
            Routed browser console for local operators. It reads from <code>{consoleApiURL}</code> and carries write actions through bearer-token Authorization headers.
          </p>
        </div>

        <Panel title="Local bearer token" description="Stored in this browser and sent as Authorization: Bearer .... The runtime resolves actor/session identity and returns that metadata in the console payload.">
          <form
            className="identity-form"
            onSubmit={(event) => {
              event.preventDefault();
              applyBearerToken();
            }}
          >
            <label className="field-label" htmlFor="operator-bearer-token">
              Bearer token
            </label>
            <input
              id="operator-bearer-token"
              className="text-input"
              type="password"
              value={bearerTokenDraft}
              onChange={(event) => setBearerTokenDraft(event.target.value)}
              placeholder={DEFAULT_OPERATOR_BEARER_TOKEN || 'opaque-operator-token'}
              autoComplete="off"
              spellCheck={false}
            />
            <button type="submit" className="primary-button">
              Apply token
            </button>
          </form>

          <div className="identity-summary">
            <DetailField label="Request auth header" value={currentBearerToken === '' ? 'Authorization omitted' : 'Authorization: Bearer …'} />
            <DetailField label="Resolved actor" value={formatResolvedIdentityField(requestIdentity?.actor_id)} />
            <DetailField label="Resolved session" value={formatResolvedIdentityField(requestIdentity?.session_id)} />
            <DetailField label="Resolved token ID" value={formatResolvedIdentityField(requestIdentity?.token_id)} />
            <DetailField label="Resolved auth method" value={formatResolvedIdentityField(requestIdentity?.auth_method)} />
            <DetailField label="Roles from current snapshot" value={formatStringList(currentRoles)} />
            <DetailField label="Permissions from current snapshot" value={formatStringList(currentPermissions)} />
          </div>
        </Panel>

        <nav className="section-nav" aria-label="Console routes">
          {(['overview', 'plugins', 'jobs', 'schedules', 'adapters', 'workflows'] as const).map((section) => {
            const active = route.section === section;
            return (
              <button
                key={section}
                type="button"
                className={`nav-button ${active ? 'nav-button-active' : ''}`}
                onClick={() => navigate(section === 'overview' ? { section } : { section })}
              >
                <span>{sectionLabel(section)}</span>
              </button>
            );
          })}
        </nav>
      </aside>

      <main className="console-main">
        <header className="topbar panel panel-muted">
          <div>
            <p className="eyebrow">Snapshot control</p>
            <h2>{sectionLabel(route.section)}</h2>
            <p className="muted-copy">
              Runtime generated at {formatTimestamp(data?.meta.generated_at)} · Browser fetched at {formatTimestamp(lastFetchedAt || undefined)}.
            </p>
          </div>
          <div className="topbar-actions">
            <label className="toggle-row" htmlFor="auto-refresh-toggle">
              <input id="auto-refresh-toggle" type="checkbox" checked={autoRefresh} onChange={toggleAutoRefresh} />
              <span>Auto refresh every {AUTO_REFRESH_MS / 1000}s</span>
            </label>
            <button type="button" className="primary-button" onClick={() => void refreshConsole()} disabled={refreshing || loading}>
              {refreshing ? 'Refreshing…' : 'Refresh now'}
            </button>
          </div>
        </header>

        {renderActionState()}

        {error !== '' ? (
          <Panel title="Console API unavailable" description="The console keeps the local bearer token form visible so you can recover from a missing, invalid, or denied token." tone="error">
            <pre className="code-block">{error}</pre>
          </Panel>
        ) : null}

        {loading && data === null ? (
          <Panel title="Loading local operator console" description={`Requesting ${consoleApiURL} with the current browser bearer token.`}>
            <p className="muted-copy">The routed operator shell stays mounted while the first runtime snapshot loads.</p>
          </Panel>
        ) : data ? (
          renderPageContent(data)
        ) : (
          <Panel title="Waiting for a valid runtime snapshot" description="Apply a bearer token or refresh once the runtime is available.">
            <p className="muted-copy">No compatible console payload is loaded yet.</p>
          </Panel>
        )}
      </main>
    </div>
  );
}

export default App;
